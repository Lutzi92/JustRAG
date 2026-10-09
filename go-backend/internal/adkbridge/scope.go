package adkbridge

import (
	"context"
	"errors"
)

// Scope is the server-side truth about who runs an agent turn and against
// what. It is set by the HTTP handler from the authenticated request and
// never from model or client input.
type Scope struct {
	UserID string
	KBID   string
	ChatID string
	// Role is the caller's effective KB role (kbaccess.EffectiveRole).
	Role string
	// LibraryFileIDs scopes retrieval to the user's own library files
	// (KB-less runs). Empty for KB runs.
	LibraryFileIDs []string
	// IsGlobal: the KB is public (kbaccess); KB-write actions need it.
	IsGlobal bool
	// AllowPrivileged mirrors site_config agents_allow_privileged_tools.
	AllowPrivileged bool
}

type scopeKey struct{}

// ErrNoScope is returned by a tool dispatched outside a scoped run.
var ErrNoScope = errors.New("adkbridge: tool dispatched without run scope")

// WithScope attaches s to ctx.
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// ScopeFrom returns the scope attached to ctx.
func ScopeFrom(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(scopeKey{}).(Scope)
	return s, ok
}
