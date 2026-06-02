package misc

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
)

const maxImageBytes = 10 << 20 // 10 MB

var allowedImageMIME = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
	"image/gif":  true,
}

type describeImageResponse struct {
	Description string `json:"description"`
}

// DescribeImage handles POST /api/describe-image (multipart, field "image",
// optional "prompt" form field). Gated by describe_image_enabled; requires a
// configured vision model (describe_image_model → model_tier_fast).
func (h *Handler) DescribeImage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if auth.UserFromContext(ctx) == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}
	if h.siteConfig == nil || !chat.DescribeImageEnabled(ctx, h.siteConfig) {
		httputil.WriteErrorCtx(ctx, w, http.StatusServiceUnavailable, "image description is disabled")
		return
	}
	model := chat.DescribeImageModel(ctx, h.siteConfig)
	if strings.TrimSpace(model) == "" {
		httputil.WriteErrorCtx(ctx, w, http.StatusServiceUnavailable, "no vision model configured (set describe_image_model)")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxImageBytes+(1<<20))
	if err := r.ParseMultipartForm(maxImageBytes); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "image too large or invalid multipart form")
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "image field is required")
		return
	}
	defer file.Close()
	if header.Size == 0 || header.Size > maxImageBytes {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "image must be 1 byte to 10 MB")
		return
	}

	buf, err := io.ReadAll(io.LimitReader(file, maxImageBytes))
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "could not read image")
		return
	}
	mime := strings.SplitN(http.DetectContentType(buf), ";", 2)[0]
	if !allowedImageMIME[mime] {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnsupportedMediaType, fmt.Sprintf("unsupported image type %q", mime))
		return
	}

	dataURI := fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(buf))
	prompt := strings.TrimSpace(r.FormValue("prompt"))

	cfg, err := h.aiResolver.Resolve(ctx, "")
	if err != nil {
		logctx.From(ctx).Error("describe_image: resolve config", "error", err)
		httputil.WriteErrorCtx(ctx, w, http.StatusBadGateway, "image description unavailable")
		return
	}
	client := ai.CachedClient(cfg.BaseURL, cfg.APIKey)
	desc, err := ai.DescribeImage(ctx, client, model, dataURI, prompt)
	if err != nil {
		logctx.From(ctx).Error("describe_image: vision call", "error", err, "model", model)
		httputil.WriteErrorCtx(ctx, w, http.StatusBadGateway, "image description failed")
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, describeImageResponse{Description: desc})
}
