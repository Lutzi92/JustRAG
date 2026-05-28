package confluence

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/justrag/go-backend/internal/safego"
)

// imageExtensions lists file extensions that are worth running OCR on.
var imageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".bmp": true, ".webp": true,
}

// ocrMinImageBytes — images smaller than this are likely icons/emoticons.
const ocrMinImageBytes = 4096

// acImageRe matches <ac:image ...><ri:attachment ... ri:filename="..." />...</ac:image>.
var acImageRe = regexp.MustCompile(`<ac:image[^>]*>[\s\S]*?<ri:attachment\s[^>]*?ri:filename="([^"]+)"[^/]*/\s*>[\s\S]*?</ac:image>`)

// ocrConcurrency is the maximum number of tesseract processes allowed to run
// at the same time across all goroutines. Configurable via OCR_CONCURRENCY.
// Default: max(2, NumCPU/2) so OCR can use roughly half the available cores
// while still leaving room for other work on the worker pod.
var ocrConcurrency = func() int {
	if raw := os.Getenv("OCR_CONCURRENCY"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			return v
		}
	}
	n := runtime.NumCPU() / 2
	if n < 2 {
		n = 2
	}
	return n
}()

// ocrSemaphore limits concurrent tesseract processes to avoid CPU starvation.
var ocrSemaphore = make(chan struct{}, ocrConcurrency)

// tesseractThreads is the number of OpenMP threads per tesseract process.
// Kept low (2) so multiple tesseract processes can run in parallel without
// thrashing the CPU.
var tesseractThreads = "2"

// tesseractChecked caches whether tesseract is installed.
var (
	tesseractOnce      sync.Once
	tesseractInstalled bool
)

// tesseractAvailable checks whether tesseract is installed (cached).
func tesseractAvailable() bool {
	tesseractOnce.Do(func() {
		_, err := exec.LookPath("tesseract")
		tesseractInstalled = err == nil
		if !tesseractInstalled {
			slog.Info("tesseract not found, inline image OCR disabled")
		}
	})
	return tesseractInstalled
}

// ocrImage runs tesseract on a local image file and returns the extracted text.
func ocrImage(parentCtx context.Context, filePath string, timeoutSec int) (string, error) {
	// Acquire semaphore with ctx escape: without it a cancelled page-processing
	// pipeline blocks here (and inside processInlineImages' wg.Wait) for as long
	// as the slowest in-flight tesseract call.
	select {
	case ocrSemaphore <- struct{}{}:
	case <-parentCtx.Done():
		return "", parentCtx.Err()
	}
	defer func() { <-ocrSemaphore }()

	ctx, cancel := context.WithTimeout(parentCtx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tesseract", filePath, "stdout", "-l", "deu+eng")
	cmd.Env = append(os.Environ(), "OMP_THREAD_NUM="+tesseractThreads)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tesseract: %w (stderr: %s)", err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// inlineImageFetcher is the subset of *ConfluenceClient needed by
// processInlineImages. Defining it here makes the preprocessor testable
// with a fake.
type inlineImageFetcher interface {
	GetPageAttachments(ctx context.Context, pageID string) ([]ConfluenceAttachment, error)
	DownloadAttachment(ctx context.Context, downloadPath string) ([]byte, error)
}

// processInlineImages finds <ac:image> tags in Confluence storage HTML and
// replaces each with markup that survives the downstream ac/ri tag stripper:
//
//   - raster (png/jpg/bmp/jpeg/webp) when tesseract is present → OCR text
//   - SVG → <title>/<desc>/<text> labels from the SVG XML
//   - anything else (or when a download/OCR fails) → a plain "[Image: X]" placeholder
//
// The placeholder alone has retrieval value: it records that a visual asset
// exists on the page and names the file, so chunks referencing the diagram
// are no longer silently dropped.
func processInlineImages(ctx context.Context, html string, client inlineImageFetcher, pageID string) string {
	matches := acImageRe.FindAllStringSubmatchIndex(html, -1)
	if len(matches) == 0 {
		return html
	}

	type imageMatch struct {
		fullStart, fullEnd int
		fileName           string
		ext                string
	}
	images := make([]imageMatch, 0, len(matches))
	for _, loc := range matches {
		fileName := html[loc[2]:loc[3]]
		images = append(images, imageMatch{
			fullStart: loc[0],
			fullEnd:   loc[1],
			fileName:  fileName,
			ext:       strings.ToLower(filepath.Ext(fileName)),
		})
	}

	hasOCR := tesseractAvailable()

	// We need attachment links only if at least one image is SVG or OCR-able.
	// A page full of GIFs with no OCR produces placeholder-only replacements
	// and needs no network round-trip.
	needAttachments := false
	for _, img := range images {
		if img.ext == ".svg" || (hasOCR && imageExtensions[img.ext]) {
			needAttachments = true
			break
		}
	}

	var attachmentLinks map[string]string
	if needAttachments {
		attachments, err := client.GetPageAttachments(ctx, pageID)
		if err != nil {
			slog.Warn("failed to fetch attachments for inline images", "pageId", pageID, "error", err)
		} else {
			attachmentLinks = make(map[string]string, len(attachments))
			for _, att := range attachments {
				attachmentLinks[att.Title] = att.DownloadLink
			}
		}
	}

	slog.Info("processing inline images", "pageId", pageID, "imageCount", len(images), "tesseract", hasOCR)

	// Build each replacement in parallel. Goroutines only do real work for
	// SVG or OCR paths; the placeholder-only branch is trivially fast.
	// safego.GoCtx provides panic recovery — buildImageReplacement fans
	// through HTTP fetches, image decoding, and external OCR subprocesses,
	// and an uncaught panic in any of those would otherwise crash the
	// worker process. wg.Done is an inner defer so it runs before panic
	// propagation reaches safego's recover. An empty replacements[i] on
	// panic falls back gracefully — the apply loop below skips empty slots.
	replacements := make([]string, len(images))
	var wg sync.WaitGroup
	for i, img := range images {
		wg.Add(1)
		safego.GoCtx(ctx, func() {
			defer wg.Done()
			replacements[i] = buildImageReplacement(ctx, client, img.fileName, img.ext, attachmentLinks, hasOCR, pageID)
		})
	}
	wg.Wait()

	// Apply in reverse so earlier byte offsets stay valid.
	for i := len(images) - 1; i >= 0; i-- {
		if replacements[i] == "" {
			continue
		}
		html = html[:images[i].fullStart] + replacements[i] + html[images[i].fullEnd:]
	}

	return html
}

// placeholderReplacement wraps a filename in the standard "[Image: X]" tag
// that survives the downstream tag stripper as plain bold text.
func placeholderReplacement(fileName string) string {
	return fmt.Sprintf(`<p><strong>[Image: %s]</strong></p>`, fileName)
}

// buildImageReplacement returns the HTML to substitute for a single <ac:image>.
// Branches by extension and whether OCR/attachment fetching succeeded; always
// returns at least a placeholder so the image's presence is not lost.
func buildImageReplacement(
	ctx context.Context,
	fetcher inlineImageFetcher,
	fileName, ext string,
	attachmentLinks map[string]string,
	hasOCR bool,
	pageID string,
) string {
	downloadLink, hasLink := attachmentLinks[fileName]

	switch {
	case ext == ".svg":
		if !hasLink {
			slog.Debug("inline SVG attachment not found", "pageId", pageID, "fileName", fileName)
			return placeholderReplacement(fileName)
		}
		data, err := fetcher.DownloadAttachment(ctx, downloadLink)
		if err != nil {
			slog.Warn("failed to download inline SVG", "pageId", pageID, "fileName", fileName, "error", err)
			return placeholderReplacement(fileName)
		}
		labels := extractSVGText(data)
		if labels == "" {
			return placeholderReplacement(fileName)
		}
		return fmt.Sprintf(`<p><strong>[Image: %s]</strong></p><p>%s</p>`, fileName, labels)

	case hasOCR && imageExtensions[ext]:
		if !hasLink {
			return placeholderReplacement(fileName)
		}
		data, err := fetcher.DownloadAttachment(ctx, downloadLink)
		if err != nil {
			slog.Warn("failed to download inline image", "pageId", pageID, "fileName", fileName, "error", err)
			return placeholderReplacement(fileName)
		}
		if len(data) < ocrMinImageBytes {
			return placeholderReplacement(fileName)
		}
		ocrText := runOCROnBytes(ctx, data, ext, pageID, fileName)
		if ocrText == "" {
			return placeholderReplacement(fileName)
		}
		return fmt.Sprintf(`<p><strong>[Image: %s]</strong></p><p>%s</p>`, fileName, ocrText)

	default:
		// GIFs, unknown extensions, or OCR disabled — placeholder only.
		return placeholderReplacement(fileName)
	}
}

// runOCROnBytes writes data to a temp file and runs tesseract on it.
// Returns "" on any error (logged at debug).
func runOCROnBytes(ctx context.Context, data []byte, ext, pageID, fileName string) string {
	tmpFile, err := os.CreateTemp("", "confluence-ocr-*"+ext)
	if err != nil {
		return ""
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return ""
	}
	tmpFile.Close()

	text, err := ocrImage(ctx, tmpFile.Name(), 20)
	if err != nil {
		slog.Debug("OCR failed for inline image", "pageId", pageID, "fileName", fileName, "error", err)
		return ""
	}
	return text
}
