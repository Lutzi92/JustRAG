package docling

import (
	"context"
	"fmt"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/parser"
)

// FallbackParser tries Primary first; on any error, logs a warn and
// delegates to Fallback. Used to keep ingestion robust when Docling Serve
// is misbehaving or unreachable.
type FallbackParser struct {
	Primary  parser.Parser
	Fallback parser.Parser
}

// Name returns the parser name (used in logs).
func (p *FallbackParser) Name() string { return "docling-fallback" }

// CanParse delegates to either underlying parser.
func (p *FallbackParser) CanParse(mt, fn string) bool {
	if p.Primary != nil && p.Primary.CanParse(mt, fn) {
		return true
	}
	if p.Fallback != nil && p.Fallback.CanParse(mt, fn) {
		return true
	}
	return false
}

// Parse tries Primary first; on any error, falls back to Fallback. The
// failure is logged at warn level via logctx so it carries request_id and
// trace context.
func (p *FallbackParser) Parse(ctx context.Context, pctx parser.ParseContext) (*parser.ParseResult, error) {
	if p.Primary == nil {
		if p.Fallback == nil {
			return nil, fmt.Errorf("no parser configured: both Primary and Fallback are nil")
		}
		return p.Fallback.Parse(ctx, pctx)
	}
	res, err := p.Primary.Parse(ctx, pctx)
	if err == nil {
		return res, nil
	}
	logctx.From(ctx).Warn("docling parse failed; falling back to pdftotext",
		"fileName", pctx.FileName,
		"error", err.Error(),
	)
	if p.Fallback == nil {
		return nil, err
	}
	return p.Fallback.Parse(ctx, pctx)
}
