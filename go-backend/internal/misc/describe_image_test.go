package misc

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
)

type fakeSCR struct{ vals map[string]string }

func (f fakeSCR) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if v, ok := f.vals[key]; ok {
		return &v, nil
	}
	return nil, nil
}

func TestDescribeImage_RejectsNonImage(t *testing.T) {
	h := &Handler{}
	h.SetSiteConfig(fakeSCR{vals: map[string]string{
		"describe_image_enabled": "true",
		"describe_image_model":   "vision-model",
	}})

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, _ := mw.CreateFormFile("image", "x.txt")
	fw.Write([]byte("this is plainly not an image"))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/describe-image", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{ID: "u1"}))
	w := httptest.NewRecorder()

	h.DescribeImage(w, req)

	if w.Code != http.StatusUnsupportedMediaType && w.Code != http.StatusBadRequest {
		t.Fatalf("expected 4xx for non-image, got %d", w.Code)
	}
}

func TestDescribeImage_DisabledReturns503(t *testing.T) {
	h := &Handler{} // no siteConfig set → disabled
	req := httptest.NewRequest(http.MethodPost, "/api/describe-image", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{ID: "u1"}))
	w := httptest.NewRecorder()
	h.DescribeImage(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when disabled, got %d", w.Code)
	}
}
