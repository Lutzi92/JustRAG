package files

import (
	"context"
	"io"
	"strings"

	"github.com/justrag/go-backend/internal/uploadcheck"
)

// SetMaxUploadSizeForTest overrides the transport cap for the duration of a
// test and returns a function that restores the previous value. Test-only —
// kept out of http.go (and thus out of the shipped API surface) via Go's
// export_test.go convention: this file is compiled only for `go test`, so
// files.SetMaxUploadSizeForTest is reachable from the external files_test
// package in tests but does not exist in a production build.
func SetMaxUploadSizeForTest(n int64) (restore func()) {
	old := uploadcheck.MaxUploadSize
	uploadcheck.MaxUploadSize = n
	return func() { uploadcheck.MaxUploadSize = old }
}

// StubFetchForTest replaces the SSRF check and the SSRF-safe fetch with a
// canned response so FetchURL's success path is reachable without network.
// Test-only; returns a restore func.
func StubFetchForTest(body, contentType string) (restore func()) {
	oldV, oldF := validateURL, fetchURL
	validateURL = func(context.Context, string) error { return nil }
	fetchURL = func(context.Context, string) (*fetchURLResponse, error) {
		return &fetchURLResponse{Body: io.NopCloser(strings.NewReader(body)), ContentType: contentType}, nil
	}
	return func() { validateURL, fetchURL = oldV, oldF }
}
