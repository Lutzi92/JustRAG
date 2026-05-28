package apidocs

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.json
var specJSON []byte

// Handler serves API documentation.
type Handler struct{}

// NewHandler returns a new docs handler.
func NewHandler() *Handler {
	return &Handler{}
}

// ServeSpec writes the embedded OpenAPI spec as JSON.
func (h *Handler) ServeSpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(specJSON)
}

const scalarHTML = `<!DOCTYPE html>
<html>
<head>
  <title>JustRAG API Docs</title>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
</head>
<body>
  <script id="api-reference" data-url="/api/v1/openapi.json"></script>
  <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
</body>
</html>`

// ServeDocs writes the Scalar API reference HTML page.
// CSP is removed so the Scalar CDN script can load.
func (h *Handler) ServeDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Del("Content-Security-Policy")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(scalarHTML))
}
