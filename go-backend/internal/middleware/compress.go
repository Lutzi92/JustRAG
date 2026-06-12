package middleware

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/klauspost/compress/gzhttp"
)

// Compression returns a response-compression middleware negotiated via
// Accept-Encoding (zstd preferred, gzip fallback — gzhttp defaults). Chat
// and search JSON payloads run 10–100 KB and compress 5–10×; responses
// under gzhttp.DefaultMinSize (1 KiB) are sent uncompressed.
//
// SSE is exempt by content type: text/event-stream responses must reach the
// client per-event, and compressing them would also re-wrap the
// ResponseWriter mid-stream. gzhttp keeps the exemption safe even though the
// content type is only known at write time — its writer buffers undecided
// bytes, and the first Flush() (which every SSE handler issues per event)
// forces the compress-or-plain decision using the Content-Type already set
// by the handler. The wrapper implements Unwrap(), so the per-connection
// http.NewResponseController write-deadline opt-out in the SSE handlers
// still reaches the real connection.
func Compression() (func(http.Handler) http.Handler, error) {
	wrapper, err := gzhttp.NewWrapper(
		gzhttp.ContentTypeFilter(func(ct string) bool {
			ct = strings.TrimSpace(strings.ToLower(ct))
			if strings.HasPrefix(ct, "text/event-stream") {
				return false
			}
			// Default filter excludes already-compressed formats
			// (video/audio/JPEG/archives).
			return gzhttp.DefaultContentTypeFilter(ct)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("build gzhttp wrapper: %w", err)
	}
	return func(next http.Handler) http.Handler {
		return wrapper(next)
	}, nil
}
