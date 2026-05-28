package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/httpclient"
)

// sttMaxFileBytes is the upper bound on audio file size accepted for
// transcription. OpenAI-compatible /audio/transcriptions endpoints typically
// reject files over 25 MB; we mirror that here to fail fast at the worker
// instead of buffering hundreds of MB just to get a 413 back. Override via
// the STT_MAX_FILE_MB env var if your provider supports larger uploads.
var sttMaxFileBytes = func() int64 {
	const defaultMB int64 = 25
	if raw := os.Getenv("STT_MAX_FILE_MB"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v > 0 {
			return v << 20
		}
	}
	return defaultMB << 20
}()

// NewSTTHTTPClient constructs the long-timeout HTTP client used for STT
// requests. Audio transcription can take a while, so the default timeout is
// 10 minutes — override via STT_TIMEOUT_MINUTES.
//
// The function is exported so server/worker bootstrap can construct one
// shared client per process and inject it into TranscribeAudioFromPath,
// keeping the connection pool tunable separately from the completion client.
func NewSTTHTTPClient() *http.Client {
	timeout := 10 * time.Minute
	if raw := os.Getenv("STT_TIMEOUT_MINUTES"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Minute
		}
	}
	return httpclient.New(timeout)
}

// audioMimeByExt maps audio file extensions to MIME types used in the
// transcription multipart upload. Falls back to "audio/mpeg".
var audioMimeByExt = map[string]string{
	".mp3":  "audio/mpeg",
	".mpeg": "audio/mpeg",
	".mpga": "audio/mpeg",
	".mp4":  "audio/mp4",
	".m4a":  "audio/mp4",
	".wav":  "audio/wav",
	".webm": "audio/webm",
	".ogg":  "audio/ogg",
	".oga":  "audio/ogg",
	".flac": "audio/flac",
}

func audioMimeFromName(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if mt, ok := audioMimeByExt[ext]; ok {
		return mt
	}
	return "audio/mpeg"
}

// TranscribeAudioFromPath transcribes an audio file using the OpenAI-compatible
// /audio/transcriptions endpoint. The KB's configured stt_model is used (or the
// global default if kbID is empty).
//
// mimeType is the original upload MIME type, used to label the multipart part
// so that extension-less filenames or less common formats (.aac, .opus, …)
// are still accepted by STT backends that validate the part content type.
// If mimeType is empty or not an audio/* type, it is derived from fileName.
func TranscribeAudioFromPath(ctx context.Context, resolver *ConfigResolver, httpClient *http.Client, filePath, kbID, fileName, mimeType string) (string, error) {
	if httpClient == nil {
		return "", fmt.Errorf("stt: httpClient is required")
	}
	cfg, err := resolver.Resolve(ctx, kbID)
	if err != nil {
		return "", fmt.Errorf("stt resolve config: %w", err)
	}
	if cfg.SttModel == "" {
		return "", fmt.Errorf("stt: no STT model configured for this KB or globally")
	}

	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("stt open file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stt stat file: %w", err)
	}

	// Reject oversized uploads up front: file uploads can be up to 500 MB,
	// but the STT endpoint typically caps at 25 MB. Buffering a 500 MB .wav
	// only to get a 413 back wastes worker memory and stalls the queue.
	if stat.Size() > sttMaxFileBytes {
		return "", fmt.Errorf("stt: audio file %q is %d bytes, exceeds STT limit of %d bytes (%d MB)",
			fileName, stat.Size(), sttMaxFileBytes, sttMaxFileBytes>>20)
	}

	slog.Info("STT request",
		"model", cfg.SttModel,
		"baseURL", cfg.BaseURL,
		"provider", cfg.ProviderName,
		"kbId", kbID,
		"fileSize", stat.Size(),
		"fileName", fileName,
	)

	// Prefer the upload's real MIME type so non-extension-based audio uploads
	// (e.g. an "aac" or extension-less recording) are labelled correctly.
	partMime := mimeType
	if !strings.HasPrefix(partMime, "audio/") {
		partMime = audioMimeFromName(fileName)
	}

	// Stream the multipart body via io.Pipe so the file is not buffered
	// in memory before being sent. The writer goroutine produces the body
	// while net/http drains it on the wire.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		// Always close the pipe writer; if any step errors we propagate the
		// error via CloseWithError so the http.Do call surfaces it. A panic
		// inside this goroutine (e.g. multipart writer internals on a corrupt
		// reader) must also close the pipe — otherwise httpClient.Do hangs
		// forever waiting for a body that will never be written.
		var werr error
		defer func() {
			if r := recover(); r != nil {
				slog.Error("stt writer goroutine panic recovered",
					"error", r,
					"fileName", fileName,
				)
				_ = pw.CloseWithError(fmt.Errorf("stt writer panic: %v", r))
				return
			}
			if werr != nil {
				_ = pw.CloseWithError(werr)
			} else {
				_ = pw.Close()
			}
		}()

		if werr = mw.WriteField("model", cfg.SttModel); werr != nil {
			return
		}

		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, fileName))
		header.Set("Content-Type", partMime)
		part, perr := mw.CreatePart(header)
		if perr != nil {
			werr = perr
			return
		}
		if _, cerr := io.Copy(part, f); cerr != nil {
			werr = cerr
			return
		}
		if cerr := mw.Close(); cerr != nil {
			werr = cerr
			return
		}
	}()

	url := cfg.BaseURL + "audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, pr)
	if err != nil {
		_ = pr.Close()
		return "", fmt.Errorf("stt new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.ContentLength = -1

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("stt request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("stt API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("stt decode response: %w (body: %s)", err, string(respBody))
	}
	return parsed.Text, nil
}
