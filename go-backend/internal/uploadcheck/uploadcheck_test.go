package uploadcheck_test

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/uploadcheck"
)

type fixedLimits int

func (f fixedLimits) TabularMaxFileBytes(context.Context) int { return int(f) }

func multipartReq(t *testing.T, field, filename string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		req     func(t *testing.T) *http.Request
		limits  uploadcheck.Limits
		shrink  int64
		status  int
		message string
	}{
		{
			name: "body over cap",
			req: func(t *testing.T) *http.Request {
				t.Helper()
				return multipartReq(t, "file", "a.txt", bytes.Repeat([]byte("x"), 4096))
			},
			shrink: 1024, status: 413, message: "File too large: the upload limit is 1 KB",
		},
		{
			name: "not multipart",
			req: func(t *testing.T) *http.Request {
				t.Helper()
				r := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("hello"))
				r.Header.Set("Content-Type", "text/plain")
				return r
			},
			status: 400, message: "Invalid multipart form",
		},
		{
			name:   "missing field",
			req:    func(t *testing.T) *http.Request { t.Helper(); return multipartReq(t, "other", "a.pdf", []byte("x")) },
			status: 400, message: "File field is required",
		},
		{
			name:   "empty file",
			req:    func(t *testing.T) *http.Request { t.Helper(); return multipartReq(t, "file", "a.pdf", nil) },
			status: 400, message: "File must not be empty",
		},
		{
			name:   "dangerous extension",
			req:    func(t *testing.T) *http.Request { t.Helper(); return multipartReq(t, "file", "a.exe", []byte("x")) },
			status: 400, message: "File type not allowed",
		},
		{
			name:   "unsupported extension",
			req:    func(t *testing.T) *http.Request { t.Helper(); return multipartReq(t, "file", "a.doc", []byte("x")) },
			status: 400, message: "File type not supported (.doc)",
		},
		{
			name: "long filename",
			req: func(t *testing.T) *http.Request {
				t.Helper()
				return multipartReq(t, "file", strings.Repeat("a", 252)+".pdf", []byte("x"))
			},
			status: 400, message: "Filename must not exceed 255 bytes",
		},
		{
			name: "spreadsheet over cap",
			req: func(t *testing.T) *http.Request {
				t.Helper()
				return multipartReq(t, "file", "a.csv", bytes.Repeat([]byte("x"), 2048))
			},
			limits: fixedLimits(1024), status: 413,
			message: "Spreadsheet too large (2 KB): the limit is 1 KB (tabular_max_file_bytes)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.shrink > 0 {
				old := uploadcheck.MaxUploadSize
				uploadcheck.MaxUploadSize = tc.shrink
				t.Cleanup(func() { uploadcheck.MaxUploadSize = old })
			}
			up, err := uploadcheck.Parse(httptest.NewRecorder(), tc.req(t), tc.limits)
			if err == nil {
				up.File.Close()
				t.Fatal("expected error")
			}
			var ve *uploadcheck.Error
			if !errors.As(err, &ve) {
				t.Fatalf("want *Error, got %T", err)
			}
			if ve.Status != tc.status || ve.Message != tc.message {
				t.Fatalf("got %d %q, want %d %q", ve.Status, ve.Message, tc.status, tc.message)
			}
		})
	}
}

func TestParse_Valid(t *testing.T) {
	up, err := uploadcheck.Parse(httptest.NewRecorder(), multipartReq(t, "file", "a.pdf", []byte("%PDF")), fixedLimits(1))
	if err != nil {
		t.Fatal(err)
	}
	defer up.File.Close()
	if up.MimeType != "application/pdf" || up.Header.Filename != "a.pdf" {
		t.Fatalf("got %q %q", up.MimeType, up.Header.Filename)
	}
}

func TestValidateName(t *testing.T) {
	long := strings.Repeat("a", uploadcheck.MaxFileNameBytes) + ".pdf"
	for name, wantOK := range map[string]bool{
		"a.pdf": true, "A.PDF": true, "x.svg": false, "x.exe": false,
		"x.doc": false, long: false,
	} {
		err := uploadcheck.ValidateName(name)
		if (err == nil) != wantOK {
			t.Errorf("ValidateName(%q) = %v, wantOK=%v", name, err, wantOK)
		}
	}
}
