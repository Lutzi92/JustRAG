// Package requestid provides a context.Context carrier for HTTP request IDs.
// The middleware (internal/middleware) attaches the ID per request; pipeline
// code retrieves it via FromContext to include in structured logs.
package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

type contextKey struct{}

var key contextKey

// NewContext returns ctx augmented with the given request id.
func NewContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, key, id)
}

// FromContext returns the request id stored in ctx, or "" if none.
func FromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(key).(string); ok {
		return v
	}
	return ""
}

// EnsureContext returns a context that has a request id — generating a new one
// if the parent lacked one. It returns the id so callers can also stamp it
// onto outbound headers / logs.
//
// crypto/rand.Read failure (OS entropy pool exhausted) is the only error
// path; it bubbles up so the caller can choose between failing the request
// (HTTP middleware) and degrading gracefully (worker tasks).
func EnsureContext(parent context.Context) (context.Context, string, error) {
	if id := FromContext(parent); id != "" {
		return parent, id, nil
	}
	id, err := generate()
	if err != nil {
		return parent, "", err
	}
	return NewContext(parent, id), id, nil
}

func generate() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("requestid: crypto/rand.Read: %w", err)
	}
	return hex.EncodeToString(b), nil
}
