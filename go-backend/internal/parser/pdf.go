package parser

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// PDFParser extracts text from PDF files using the pdftotext command-line tool
// (part of poppler-utils). If pdftotext is not installed, Parse returns an
// actionable error message.
type PDFParser struct{}

func (p *PDFParser) Name() string { return "PDFParser" }

// CanParse returns true for the application/pdf MIME type or the .pdf extension.
func (p *PDFParser) CanParse(mimeType, fileName string) bool {
	return mimeType == "application/pdf" || strings.HasSuffix(fileName, ".pdf")
}

// Parse extracts text from the PDF at pctx.FilePath using pdftotext.
// Only the first PDFMaxPages pages are processed. The result is truncated to
// TextMaxChars if necessary.
//
// Instead of buffering the full pdftotext output, we stream from stdout and
// stop reading once TextMaxChars is reached.
func (p *PDFParser) Parse(ctx context.Context, pctx ParseContext) (*ParseResult, error) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		return nil, fmt.Errorf("pdftotext not found: install poppler-utils")
	}

	args := []string{
		"-layout",
		"-enc", "UTF-8",
		"-l", strconv.Itoa(PDFMaxPages), // -l = last page to extract
		pctx.FilePath,
		"-", // write to stdout
	}

	cmd := exec.CommandContext(ctx, "pdftotext", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pdftotext stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pdftotext start: %w", err)
	}

	// Read up to TextMaxChars from stdout to avoid buffering the entire output.
	limited := io.LimitReader(stdout, int64(TextMaxChars))
	buf, err := io.ReadAll(limited)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("pdftotext read: %w", err)
	}

	// Drain any remaining output so pdftotext doesn't block on a full pipe.
	_, _ = io.Copy(io.Discard, stdout)

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("pdftotext failed: %w", err)
	}

	// pdftotext -layout inserts form-feed characters (\f) between pages.
	// Split on \f to get per-page text so the processor can split each
	// page independently and reliably assign page numbers to chunks.
	raw := string(buf)
	rawPages := strings.Split(raw, "\f")

	var pages []PageText
	var fullText strings.Builder
	for i, p := range rawPages {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		pages = append(pages, PageText{PageNumber: i + 1, Text: trimmed})
		if fullText.Len() > 0 {
			fullText.WriteString("\n\n")
		}
		fullText.WriteString(trimmed)
	}

	// OCR fallback: if pdftotext extracted very little text per page, the PDF
	// is likely scanned/image-based. Attempt Tesseract OCR unless disabled.
	ocrEnv := strings.ToLower(strings.TrimSpace(os.Getenv("PARSER_ENABLE_OCR")))
	ocrDisabled := ocrEnv == "false" || ocrEnv == "0" || ocrEnv == "no"
	if isSparseText(pages, 50) && !ocrDisabled {
		tesseractBin := "tesseract"
		ocrPages, ocrErr := runOCR(ctx, pctx.FilePath, tesseractBin)
		if ocrErr != nil {
			slog.Warn("OCR fallback failed, keeping pdftotext output", "file", pctx.FileName, "error", ocrErr)
		} else if len(ocrPages) > 0 {
			slog.Info("OCR fallback produced text for sparse PDF", "file", pctx.FileName, "pages", len(ocrPages))
			pages = ocrPages
			// Rebuild full text from OCR pages, enforcing TextMaxChars.
			fullText.Reset()
			for _, pg := range pages {
				if fullText.Len() > 0 {
					fullText.WriteString("\n\n")
				}
				remaining := TextMaxChars - fullText.Len()
				if remaining <= 0 {
					break
				}
				if len(pg.Text) > remaining {
					fullText.WriteString(pg.Text[:remaining])
					break
				}
				fullText.WriteString(pg.Text)
			}
		}
	}

	return &ParseResult{Text: fullText.String(), Pages: pages}, nil
}
