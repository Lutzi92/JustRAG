package parser

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/justrag/go-backend/internal/safego"
)

// isSparseText returns true if the average characters per page is below the
// given threshold, indicating that pdftotext extracted very little text
// (likely a scanned/image-based PDF).
func isSparseText(pages []PageText, threshold int) bool {
	if len(pages) == 0 {
		return true
	}
	total := 0
	for _, p := range pages {
		total += len(p.Text)
	}
	avg := total / len(pages)
	return avg < threshold
}

// runOCR converts each PDF page to an image via pdftoppm and runs Tesseract
// OCR on the images in parallel (up to 4 workers). It returns per-page text.
// The provided context is checked between pages so OCR can be cancelled.
func runOCR(ctx context.Context, pdfPath, tesseractBin string) ([]PageText, error) {
	// Verify binaries exist.
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		return nil, fmt.Errorf("pdftoppm not found: install poppler-utils for OCR support")
	}
	if _, err := exec.LookPath(tesseractBin); err != nil {
		return nil, fmt.Errorf("tesseract not found at %q: install tesseract-ocr for OCR support", tesseractBin)
	}

	// Create temp directory for page images.
	tmpDir, err := os.MkdirTemp("", "justrag-ocr-*")
	if err != nil {
		return nil, fmt.Errorf("ocr: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Check context before starting expensive conversion.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Convert PDF pages to PNG images at 300 DPI.
	prefix := filepath.Join(tmpDir, "page")
	cmd := exec.CommandContext(ctx, "pdftoppm",
		"-r", "300",
		"-png",
		"-l", strconv.Itoa(PDFMaxPages),
		pdfPath,
		prefix,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pdftoppm failed: %w: %s", err, string(out))
	}

	// Discover generated page images (pdftoppm names them page-01.png, page-02.png, etc.).
	matches, err := filepath.Glob(prefix + "-*.png")
	if err != nil {
		return nil, fmt.Errorf("ocr: glob page images: %w", err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("ocr: pdftoppm produced no page images")
	}
	sort.Strings(matches)

	// Run tesseract on each page image in parallel with a 4-worker semaphore.
	type pageResult struct {
		pageNum int
		text    string
		err     error
	}

	results := make([]pageResult, len(matches))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup

	for i, imgPath := range matches {
		wg.Add(1)
		// safego.GoCtx (not a bare `go`) so a panic that escapes RecoverError
		// is still logged with request-scoped fields + stack rather than
		// crashing the ingest worker — consistent with every other goroutine
		// launch in the codebase.
		safego.GoCtx(ctx, func() {
			defer wg.Done()
			// RecoverError captures a panic from tesseract subprocess plumbing
			// (e.g. exec internals) as results[i].err so the per-page failure
			// path (slog.Warn + failCount) handles it. It recovers before
			// GoCtx's own recover sees the panic; GoCtx is the backstop.
			// Registered first so it runs last on the panic unwind, after the
			// semaphore release.
			defer safego.RecoverError(&results[i].err)

			// Acquire a semaphore slot or bail out on cancellation. Doing
			// the acquire as a select means a cancelled context unblocks
			// pending workers immediately instead of waiting on the slot.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = pageResult{pageNum: i + 1, err: ctx.Err()}
				return
			}
			defer func() { <-sem }()

			// Re-check after acquiring the semaphore slot.
			if ctx.Err() != nil {
				results[i] = pageResult{pageNum: i + 1, err: ctx.Err()}
				return
			}

			// Tesseract writes to stdout with "-" as output base.
			// Use cmd.Output() so only stdout is captured; stderr
			// warnings are kept separate to avoid polluting OCR text.
			cmd := exec.CommandContext(ctx, tesseractBin, imgPath, "stdout")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				results[i] = pageResult{pageNum: i + 1, err: fmt.Errorf("tesseract page %d: %w: %s", i+1, err, stderr.String())}
				return
			}
			results[i] = pageResult{pageNum: i + 1, text: strings.TrimSpace(string(out))}
		})
	}
	wg.Wait()

	var pages []PageText
	var lastErr error
	failCount := 0
	for _, r := range results {
		if r.err != nil {
			slog.Warn("OCR failed for page", "page", r.pageNum, "error", r.err)
			lastErr = r.err
			failCount++
			continue
		}
		if r.text == "" {
			continue
		}
		pages = append(pages, PageText{PageNumber: r.pageNum, Text: r.text})
	}

	if len(pages) == 0 {
		if failCount > 0 {
			return nil, fmt.Errorf("ocr: no usable output from %d pages (%d failed, last error: %w)", len(results), failCount, lastErr)
		}
		return nil, fmt.Errorf("ocr: no usable output from %d pages (all pages returned empty text)", len(results))
	}

	return pages, nil
}
