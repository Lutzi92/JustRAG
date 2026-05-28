package parser

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ImageParser extracts text from image files using tesseract OCR.
//
// Vision-API based image description was intentionally not ported from the
// Node.js backend (see CLAUDE.md "Disabled Features"), so this parser is
// OCR-only. If tesseract is not available on the host, Parse returns an error.
type ImageParser struct{}

func (p *ImageParser) Name() string { return "ImageParser" }

// rasterImageExts lists raster image extensions tesseract can actually decode.
// Vector formats like .svg are intentionally excluded — tesseract cannot OCR
// them, so falling through to TextParser (or failing in a more obvious way at
// upload time) is preferable to invoking tesseract on unsupported input.
var rasterImageExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".webp": true,
	".bmp":  true,
	".tif":  true,
	".tiff": true,
	".pnm":  true,
	".gif":  true,
}

// rasterImageMimes mirrors rasterImageExts for MIME-based matching.
var rasterImageMimes = map[string]bool{
	"image/png":               true,
	"image/jpeg":              true,
	"image/jpg":               true,
	"image/webp":              true,
	"image/bmp":               true,
	"image/x-ms-bmp":          true,
	"image/tiff":              true,
	"image/x-portable-anymap": true,
	"image/gif":               true,
}

// CanParse returns true only for raster image formats tesseract can decode.
// Vector formats (e.g. image/svg+xml) are deliberately rejected here so they
// fall through to a parser that can handle them rather than failing inside
// tesseract.
func (p *ImageParser) CanParse(mimeType, fileName string) bool {
	if rasterImageMimes[strings.ToLower(mimeType)] {
		return true
	}
	// Lowercase defensively: callers may pass the original filename (e.g.
	// from URL ingestion paths that don't go through Factory.GetParser),
	// so a URL ending in .PNG must still match .png here.
	lower := strings.ToLower(fileName)
	for ext := range rasterImageExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

var (
	tesseractOnce      sync.Once
	tesseractInstalled bool
	tesseractLangs     string // resolved -l value, e.g. "deu+eng" or "eng"
)

// tesseractAvailable returns true if the tesseract binary is on PATH (cached).
// On first call it also probes the installed traineddata files via
// `tesseract --list-langs` to pick a language string that won't fail at
// runtime: prefer "deu+eng" when both are installed, fall back to "eng",
// otherwise use the first reported language. The first caller's ctx caps the
// probe; subsequent calls return cached state regardless of ctx.
func tesseractAvailable(ctx context.Context) bool {
	tesseractOnce.Do(func() {
		_, err := exec.LookPath("tesseract")
		tesseractInstalled = err == nil
		if !tesseractInstalled {
			slog.Warn("tesseract not found on PATH; image OCR will fail")
			return
		}
		tesseractLangs = resolveTesseractLangs(ctx)
		slog.Info("tesseract OCR ready", "langs", tesseractLangs)
	})
	return tesseractInstalled
}

// resolveTesseractLangs runs `tesseract --list-langs` and returns a -l value
// appropriate for the installed traineddata. If probing fails for any reason,
// it falls back to "eng" — the language pack that ships by default with
// tesseract on virtually every distro.
func resolveTesseractLangs(parent context.Context) string {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tesseract", "--list-langs")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		slog.Warn("tesseract --list-langs failed; defaulting to eng",
			"error", err, "stderr", stderr.String())
		return "eng"
	}

	// `tesseract --list-langs` writes to stderr on some builds and stdout on
	// others. Concatenate both to be safe.
	out := stdout.String() + "\n" + stderr.String()
	available := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		// Skip the "List of available languages..." header and blanks.
		if line == "" || strings.Contains(line, ":") {
			continue
		}
		available[line] = true
	}

	if available["deu"] && available["eng"] {
		return "deu+eng"
	}
	if available["eng"] {
		return "eng"
	}
	// No English pack at all — sort the remaining languages so the fallback
	// is stable across process restarts (Go map iteration order is randomized).
	if len(available) > 0 {
		langs := make([]string, 0, len(available))
		for lang := range available {
			langs = append(langs, lang)
		}
		sort.Strings(langs)
		slog.Warn("tesseract has no eng pack; falling back to first sorted language",
			"chosen", langs[0], "available", langs)
		return langs[0]
	}
	return "eng" // last-resort default; tesseract will surface a clear error
}

// Parse runs tesseract OCR on the image at pctx.FilePath and returns the
// extracted text.
func (p *ImageParser) Parse(ctx context.Context, pctx ParseContext) (*ParseResult, error) {
	if !tesseractAvailable(ctx) {
		return nil, fmt.Errorf("image: tesseract OCR binary not installed")
	}

	// Use a hard timeout to bound runaway OCR jobs.
	ocrCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	langs := tesseractLangs
	if langs == "" {
		langs = "eng"
	}
	cmd := exec.CommandContext(ocrCtx, "tesseract", pctx.FilePath, "stdout", "-l", langs)
	cmd.Env = append(os.Environ(), "OMP_NUM_THREADS=4")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("image: tesseract failed: %w (stderr: %s)", err, stderr.String())
	}

	text := strings.TrimSpace(stdout.String())
	if text == "" {
		slog.Info("image: tesseract returned no text", "file", pctx.FileName)
	}
	if len(text) > TextMaxChars {
		text = text[:TextMaxChars]
		// Drop a trailing partial rune so the result is valid UTF-8.
		for len(text) > 0 && !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return &ParseResult{Text: text}, nil
}
