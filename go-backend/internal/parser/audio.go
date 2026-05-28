package parser

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Transcriber transcribes an audio file at filePath using whatever STT model
// is configured for kbID. The implementation lives outside the parser package
// (typically in internal/ai) to avoid an import cycle.
type Transcriber interface {
	// Transcribe converts an audio file to text. mimeType is the original
	// upload MIME type (may be empty); implementations should use it to label
	// the multipart upload sent to the STT backend so that extension-less or
	// less-common formats (.aac, .opus, …) are accepted.
	Transcribe(ctx context.Context, filePath, kbID, fileName, mimeType string) (string, error)
}

// AudioParser handles audio files by delegating transcription to a Transcriber.
type AudioParser struct {
	Transcriber Transcriber
}

func (p *AudioParser) Name() string { return "AudioParser" }

var audioExtensions = []string{
	".mp3", ".wav", ".m4a", ".mp4", ".mpeg", ".mpga",
	".ogg", ".oga", ".flac", ".webm",
}

// CanParse returns true for any audio/* MIME type or recognized audio extension.
func (p *AudioParser) CanParse(mimeType, fileName string) bool {
	if strings.HasPrefix(mimeType, "audio/") {
		return true
	}
	lower := strings.ToLower(fileName)
	for _, ext := range audioExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// Parse calls the configured Transcriber and returns its output as text.
func (p *AudioParser) Parse(ctx context.Context, pctx ParseContext) (*ParseResult, error) {
	if p.Transcriber == nil {
		return nil, fmt.Errorf("audio: no transcriber configured (set STT model in AI settings)")
	}
	text, err := p.Transcriber.Transcribe(ctx, pctx.FilePath, pctx.KbID, pctx.FileName, pctx.MimeType)
	if err != nil {
		return nil, fmt.Errorf("audio: transcription failed: %w", err)
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
