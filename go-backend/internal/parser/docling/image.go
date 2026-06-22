package docling

import (
	"context"
	"strings"

	"github.com/justrag/go-backend/internal/parser"
)

// rasterImageExts mirrors parser.ImageParser's accepted raster formats so the
// Docling path matches exactly the inputs Tesseract would otherwise handle.
var rasterImageExts = []string{".png", ".jpg", ".jpeg", ".webp", ".bmp", ".tif", ".tiff", ".pnm", ".gif"}

var rasterImageMimes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/jpg": true, "image/webp": true,
	"image/bmp": true, "image/x-ms-bmp": true, "image/tiff": true,
	"image/x-portable-anymap": true, "image/gif": true,
}

// DoclingImageParser routes raster images through Docling Serve so embedded
// charts/diagrams get a vision caption (and OCR) instead of OCR-only. Intended
// to be wrapped in a parser.FallbackParser with parser.ImageParser as fallback.
type DoclingImageParser struct {
	Client *Client
}

func (p *DoclingImageParser) Name() string { return "docling-image" }

func (p *DoclingImageParser) CanParse(mimeType, fileName string) bool {
	if rasterImageMimes[strings.ToLower(mimeType)] {
		return true
	}
	lower := strings.ToLower(fileName)
	for _, ext := range rasterImageExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func (p *DoclingImageParser) Parse(ctx context.Context, pctx parser.ParseContext) (*parser.ParseResult, error) {
	return convertAndMap(ctx, p.Client, pctx)
}
