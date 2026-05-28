package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/httpclient"
	"golang.org/x/text/unicode/norm"
)

// NewTTSHTTPClient constructs the long-timeout HTTP client used for TTS
// requests. Speech synthesis can take a while, so the default timeout is
// 5 minutes — override via TTS_TIMEOUT_MINUTES.
//
// The function is exported so server/worker bootstrap can construct one
// shared client per process and inject it into GenerateTTS, keeping the
// connection pool tunable separately from the completion client.
func NewTTSHTTPClient() *http.Client {
	timeout := 5 * time.Minute
	if raw := os.Getenv("TTS_TIMEOUT_MINUTES"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Minute
		}
	}
	return httpclient.New(timeout)
}

// podcastSegmentRe matches a speaker-prefixed line ("ALICE:" / "BOB:"); compiled
// once at package init so ParsePodcastSegments doesn't pay regex compilation
// (~5–50 µs) per call on the podcast-export hot path.
var podcastSegmentRe = regexp.MustCompile(`(?i)^(ALICE|BOB)\s*:\s*(.*)`)

// ttsPayload is the request body for the OpenAI-compatible TTS speech endpoint.
type ttsPayload struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format"`
	Speed          float64 `json:"speed"`
}

// ParseTTSModelConfig parses a TTS model config string in the format
// "model", "model:voice", or "model:voice1:voice2".
// Returns (model, primaryVoice, secondaryVoice).
func ParseTTSModelConfig(ttsModel string) (model, voice1, voice2 string) {
	parts := strings.Split(ttsModel, ":")
	model = parts[0]
	voice1 = "alloy"
	voice2 = "alloy"
	if len(parts) >= 2 && parts[1] != "" {
		voice1 = parts[1]
		voice2 = parts[1] // secondary defaults to primary
	}
	if len(parts) >= 3 && parts[2] != "" {
		voice2 = parts[2]
	}
	return model, voice1, voice2
}

// GetTTSVoices resolves TTS voices for a given KB. Returns (primaryVoice, secondaryVoice).
func GetTTSVoices(ctx context.Context, resolver *ConfigResolver, kbID string) (string, string, error) {
	cfg, err := resolver.Resolve(ctx, kbID)
	if err != nil {
		return "alloy", "alloy", err
	}
	if cfg.TtsModel == "" {
		return "alloy", "alloy", nil
	}
	_, v1, v2 := ParseTTSModelConfig(cfg.TtsModel)
	return v1, v2, nil
}

// GenerateTTS generates MP3 audio from text using the OpenAI-compatible TTS
// speech endpoint. Long text is automatically split into chunks. The voice
// parameter overrides the default voice from the model config if non-empty.
func GenerateTTS(ctx context.Context, resolver *ConfigResolver, httpClient *http.Client, text, kbID, voice string) ([]byte, error) {
	if httpClient == nil {
		return nil, fmt.Errorf("tts: httpClient is required")
	}
	cfg, err := resolver.Resolve(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("tts resolve config: %w", err)
	}
	if cfg.TtsModel == "" {
		return nil, fmt.Errorf("tts: no TTS model configured")
	}

	cleaned := CleanTextForTTS(text)
	chunks := SplitTextIntoChunks(cleaned, 1000)
	if len(chunks) == 0 {
		return nil, fmt.Errorf("tts: no text to synthesize after cleaning")
	}

	model, defaultVoice, _ := ParseTTSModelConfig(cfg.TtsModel)
	if voice == "" {
		voice = defaultVoice
	}

	slog.Info("TTS request",
		"model", model,
		"baseURL", cfg.BaseURL,
		"provider", cfg.ProviderName,
		"kbId", kbID,
		"voice", voice,
		"textLen", len(cleaned),
	)
	if voice == "none" {
		voice = "alloy"
	}

	// Google/Gemini models use different voice naming
	if strings.Contains(strings.ToLower(model), "gemini") || strings.Contains(strings.ToLower(model), "google") {
		if voice == defaultVoice && !strings.Contains(voice, "-") {
			voice = "en-US-Standard-A"
		}
	}

	var result bytes.Buffer
	for i, chunk := range chunks {
		if len(chunks) > 1 {
			slog.Debug("processing TTS chunk", "chunk", i+1, "total", len(chunks), "len", len(chunk))
		}

		payload := ttsPayload{
			Model:          model,
			Input:          chunk,
			Voice:          voice,
			ResponseFormat: "mp3",
			Speed:          1.0,
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("tts marshal: %w", err)
		}

		url := cfg.BaseURL + "audio/speech"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("tts new request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("tts request chunk %d: %w", i, err)
		}

		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("tts API error (status %d) chunk %d: %s", resp.StatusCode, i, string(errBody))
		}

		_, copyErr := io.Copy(&result, resp.Body)
		resp.Body.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("tts read response chunk %d: %w", i, copyErr)
		}
	}

	return result.Bytes(), nil
}

// ---------------------------------------------------------------------------
// Text processing helpers (ported from audio_service.ts)
// ---------------------------------------------------------------------------

var (
	reSpeakerLabels    = regexp.MustCompile(`(?im)^\*{0,2}(?:Host\s*\(.*?\)|Expert\s*\(.*?\)|Host|Expert|Bob|Alice):\*{0,2}\s*`)
	reBrackets         = regexp.MustCompile(`\[[^\]]+\]`)
	reParens           = regexp.MustCompile(`\([^)]+\)`)
	reBoldStar         = regexp.MustCompile(`\*\*(.*?)\*\*`)
	reBoldUnderscore   = regexp.MustCompile(`__(.*?)__`)
	reItalicStar       = regexp.MustCompile(`\*(.*?)\*`)
	reItalicUnderscore = regexp.MustCompile(`_(.*?)_`)
	reCodeBlock        = regexp.MustCompile("(?s)```.*?```")
	reInlineCode       = regexp.MustCompile("`(.+?)`")
	reHeaders          = regexp.MustCompile(`(?m)^#+\s+`)
	reHRule            = regexp.MustCompile(`(?m)^---$`)
	reListBullet       = regexp.MustCompile(`(?m)^[\s]*[-*+]\s+`)
	reListNumber       = regexp.MustCompile(`(?m)^[\s]*\d+\.\s+`)
	reMarkdownChars    = regexp.MustCompile(`[#*` + "`" + `_~|]`)
	reMultiNewline     = regexp.MustCompile(`\n\s*\n`)
	reMultiSpace       = regexp.MustCompile(`\s+`)
)

// CleanTextForTTS removes markdown, speaker labels, and normalizes characters
// for broad TTS engine compatibility.
func CleanTextForTTS(text string) string {
	s := reSpeakerLabels.ReplaceAllString(text, "")
	s = reBrackets.ReplaceAllString(s, " ")
	s = reParens.ReplaceAllString(s, " ")
	s = reBoldStar.ReplaceAllString(s, "$1")
	s = reBoldUnderscore.ReplaceAllString(s, "$1")
	s = reItalicStar.ReplaceAllString(s, "$1")
	s = reItalicUnderscore.ReplaceAllString(s, "$1")
	s = reCodeBlock.ReplaceAllString(s, "")
	s = reInlineCode.ReplaceAllString(s, "$1")
	s = reHeaders.ReplaceAllString(s, "")
	s = reHRule.ReplaceAllString(s, "")
	s = reListBullet.ReplaceAllString(s, "")
	s = reListNumber.ReplaceAllString(s, "")

	// Normalize special characters for better speech flow
	replacer := strings.NewReplacer(
		"\u2011", "-", "\u2012", "-", "\u2013", "-", "\u2014", "-", "\u2015", "-",
		"\u2018", "'", "\u2019", "'", "\u201A", "'", "\u201B", "'", "\u2032", "'", "\u2035", "'",
		"\u201C", `"`, "\u201D", `"`, "\u201E", `"`, "\u201F", `"`, "\u2033", `"`, "\u2036", `"`,
		"\u2026", "...",
	)
	s = replacer.Replace(s)

	s = reMarkdownChars.ReplaceAllString(s, " ")
	s = reMultiNewline.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = reMultiSpace.ReplaceAllString(s, " ")

	// Normalize to NFKD and keep only ASCII + Latin-1 characters
	s = norm.NFKD.String(s)
	var filtered strings.Builder
	for _, r := range s {
		if r <= 0x00FF {
			filtered.WriteRune(r)
		}
	}
	return strings.TrimSpace(filtered.String())
}

// SplitTextIntoChunks splits text into chunks for TTS processing, respecting
// word boundaries. Never cuts in the middle of a word.
func SplitTextIntoChunks(text string, maxLength int) []string {
	var chunks []string
	current := text

	for len(current) > 0 {
		if len(current) <= maxLength {
			trimmed := strings.TrimSpace(current)
			if trimmed != "" {
				chunks = append(chunks, trimmed)
			}
			break
		}

		splitIndex := -1

		// 1. Try to split at a newline
		if idx := strings.LastIndex(current[:maxLength], "\n"); idx > maxLength*3/10 {
			splitIndex = idx + 1
		}

		// 2. Try to split at sentence end
		if splitIndex == -1 {
			best := -1
			for _, sep := range []string{". ", "! ", "? "} {
				if idx := strings.LastIndex(current[:maxLength], sep); idx > best {
					best = idx
				}
			}
			if best > maxLength*3/10 {
				splitIndex = best + 2
			}
		}

		// 3. Try to split at comma or semicolon
		if splitIndex == -1 {
			best := -1
			for _, sep := range []string{", ", "; "} {
				if idx := strings.LastIndex(current[:maxLength], sep); idx > best {
					best = idx
				}
			}
			if best > maxLength*3/10 {
				splitIndex = best + 2
			}
		}

		// 4. Split at last space
		if splitIndex == -1 {
			if idx := strings.LastIndex(current[:maxLength], " "); idx > 0 {
				splitIndex = idx + 1
			}
		}

		// 5. Hard cut as last resort
		if splitIndex <= 0 {
			slog.Warn("forced hard-cut in TTS text", "pos", maxLength)
			splitIndex = maxLength
		}

		chunk := strings.TrimSpace(current[:splitIndex])
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		current = strings.TrimSpace(current[splitIndex:])
	}

	return chunks
}

// ParsePodcastSegments parses a podcast script into speaker segments.
// Expects lines starting with "ALICE:" or "BOB:" (case-insensitive).
func ParsePodcastSegments(script string) []PodcastSegment {
	var segments []PodcastSegment
	currentSpeaker := "ALICE"
	var currentText strings.Builder

	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if m := podcastSegmentRe.FindStringSubmatch(trimmed); m != nil {
			if text := strings.TrimSpace(currentText.String()); text != "" {
				segments = append(segments, PodcastSegment{Speaker: currentSpeaker, Text: text})
			}
			currentSpeaker = strings.ToUpper(m[1])
			currentText.Reset()
			currentText.WriteString(m[2])
		} else {
			currentText.WriteString(" ")
			currentText.WriteString(trimmed)
		}
	}

	if text := strings.TrimSpace(currentText.String()); text != "" {
		segments = append(segments, PodcastSegment{Speaker: currentSpeaker, Text: text})
	}

	return segments
}

// PodcastSegment holds a single speaker's dialogue line.
type PodcastSegment struct {
	Speaker string // "ALICE" or "BOB"
	Text    string
}
