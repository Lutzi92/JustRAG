package parser

import "strings"

// Factory holds a registered list of parsers and selects the right one for a file.
type Factory struct {
	parsers []Parser
}

// NewFactory returns an empty Factory.
func NewFactory() *Factory { return &Factory{} }

// Register appends a parser to the factory's list. Order matters: the first
// parser whose CanParse returns true will be used.
func (f *Factory) Register(p Parser) {
	f.parsers = append(f.parsers, p)
}

// GetParser returns the first parser that can handle the given MIME type and
// file name, or nil if none matches.
func (f *Factory) GetParser(mimeType, fileName string) Parser {
	lower := strings.ToLower(fileName)
	for _, p := range f.parsers {
		if p.CanParse(mimeType, lower) {
			return p
		}
	}
	return nil
}

// DefaultFactoryWith is like DefaultFactory but registers any number of
// layout-aware front parsers (typically Docling-backed) before the default
// built-in parsers. When frontParsers is empty, behavior is identical to
// DefaultFactory. Nil parsers in the variadic list are silently skipped.
func DefaultFactoryWith(transcriber Transcriber, frontParsers ...Parser) *Factory {
	f := NewFactory()
	for _, p := range frontParsers {
		if p != nil {
			f.Register(p)
		}
	}
	f.Register(&PDFParser{})
	f.Register(&CSVParser{})
	f.Register(&XLSXParser{})
	f.Register(&XLSParser{})
	f.Register(&OdsParser{})
	f.Register(&DocxParser{})
	f.Register(&PptxParser{})
	f.Register(&OdtParser{})
	f.Register(&EpubParser{})
	f.Register(&ImageParser{})
	f.Register(&AudioParser{Transcriber: transcriber})
	f.Register(&TexParser{})
	f.Register(&TextParser{}) // catch-all, must be last
	return f
}

// DefaultFactory builds a Factory with all built-in parsers pre-registered.
// Parsers are registered from most-specific to least-specific; TextParser is
// last because it acts as a catch-all.
//
// Audio transcription requires an external Transcriber implementation; pass
// nil here only when STT is unavailable. With a nil transcriber, AudioParser
// will still match audio files but Parse will return a clear error rather than
// silently treating them as text.
func DefaultFactory(transcriber Transcriber) *Factory {
	return DefaultFactoryWith(transcriber)
}
