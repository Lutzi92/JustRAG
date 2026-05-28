package auth_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/auth"
)

type mockRedis struct {
	store map[string]string
}

func newMockRedis() *mockRedis {
	return &mockRedis{store: make(map[string]string)}
}

func (m *mockRedis) Get(ctx context.Context, key string) (string, error) {
	v, ok := m.store[key]
	if !ok {
		return "", auth.ErrKeyNotFound
	}
	return v, nil
}

func (m *mockRedis) Set(ctx context.Context, key string, value any, expiration time.Duration) error {
	m.store[key] = fmt.Sprintf("%v", value)
	return nil
}

func (m *mockRedis) Exists(ctx context.Context, keys ...string) (int64, error) {
	count := int64(0)
	for _, k := range keys {
		if _, ok := m.store[k]; ok {
			count++
		}
	}
	return count, nil
}

func (m *mockRedis) Pipeline(ctx context.Context, fn func(pipe auth.PipelineExecer) error) error {
	return fmt.Errorf("pipeline not supported in mock")
}

func TestBlacklist_AddAndCheck(t *testing.T) {
	store := newMockRedis()
	bl := auth.NewBlacklist(store, false)
	ctx := context.Background()

	bl.Add(ctx, "jti-123", time.Now().Add(time.Hour))

	if !bl.IsBlacklisted(ctx, "jti-123") {
		t.Error("expected jti-123 to be blacklisted")
	}
	if bl.IsBlacklisted(ctx, "jti-other") {
		t.Error("expected jti-other to not be blacklisted")
	}
}

func TestBlacklist_UserInvalidation(t *testing.T) {
	store := newMockRedis()
	bl := auth.NewBlacklist(store, false)
	ctx := context.Background()

	bl.InvalidateUserTokens(ctx, "user-1")

	pastIAT := time.Now().Add(-1 * time.Minute).Unix()
	if !bl.IsUserTokenInvalidated(ctx, "user-1", pastIAT) {
		t.Error("expected token issued before invalidation to be invalid")
	}
}

func TestBlacklist_ServerBoot(t *testing.T) {
	store := newMockRedis()
	bl := auth.NewBlacklist(store, false)
	ctx := context.Background()

	bl.RecordServerBoot(ctx)

	pastIAT := time.Now().Add(-1 * time.Hour).Unix()
	if !bl.IsTokenBeforeServerBoot(ctx, pastIAT) {
		t.Error("expected token issued before server boot to be invalid")
	}
}

// Regression: when the boot key has expired (its 25h TTL elapsed) the store
// returns ErrKeyNotFound. A freshly issued token must NOT be rejected — there
// is simply no boot constraint to enforce. Previously, production deployments
// returned isProduction=true on any Get error, which rejected every token
// after ~25h of uptime and required a container restart to recover.
func TestBlacklist_BootKeyExpired_DoesNotRejectFreshToken(t *testing.T) {
	store := newMockRedis() // boot key never set → Get returns ErrKeyNotFound
	bl := auth.NewBlacklist(store, true /* isProduction */)
	ctx := context.Background()

	freshIAT := time.Now().Unix()
	if bl.IsTokenBeforeServerBoot(ctx, freshIAT) {
		t.Error("expected fresh token to be accepted when boot key is missing (TTL expired)")
	}
}

// Same regression but exercising the pipelined CheckAll path.
func TestBlacklist_CheckAll_BootKeyExpired_DoesNotRejectFreshToken(t *testing.T) {
	store := newPipelinedMockRedis() // boot key never set
	bl := auth.NewBlacklist(store, true /* isProduction */)
	ctx := context.Background()

	freshIAT := time.Now().Unix()
	res := bl.CheckAll(ctx, "jti-fresh", "user-1", freshIAT)
	if res.IsTokenBeforeServerBoot {
		t.Error("expected CheckAll to accept fresh token when boot key is missing (TTL expired)")
	}
	if res.IsBlacklisted || res.IsUserTokenInvalidated {
		t.Errorf("unexpected rejection: %+v", res)
	}
}

// pipelinedMockRedis exercises the CheckAll pipeline branch (the production
// path) instead of the per-call fallback. It mirrors go-redis semantics:
// missing keys return ErrKeyNotFound from a StringCmd's Result(), and the
// overall pipeline returns nil even when individual GETs miss.
type pipelinedMockRedis struct {
	store map[string]string
}

func newPipelinedMockRedis() *pipelinedMockRedis {
	return &pipelinedMockRedis{store: make(map[string]string)}
}

func (m *pipelinedMockRedis) Get(_ context.Context, key string) (string, error) {
	v, ok := m.store[key]
	if !ok {
		return "", auth.ErrKeyNotFound
	}
	return v, nil
}

func (m *pipelinedMockRedis) Set(_ context.Context, key string, value any, _ time.Duration) error {
	m.store[key] = fmt.Sprintf("%v", value)
	return nil
}

func (m *pipelinedMockRedis) Exists(_ context.Context, keys ...string) (int64, error) {
	var n int64
	for _, k := range keys {
		if _, ok := m.store[k]; ok {
			n++
		}
	}
	return n, nil
}

func (m *pipelinedMockRedis) Pipeline(ctx context.Context, fn func(pipe auth.PipelineExecer) error) error {
	return fn(&pipelinedMockExecer{store: m.store})
}

type pipelinedMockExecer struct {
	store map[string]string
}

func (p *pipelinedMockExecer) Get(_ context.Context, key string) auth.StringCmd {
	v, ok := p.store[key]
	if !ok {
		return mockStringCmd{err: auth.ErrKeyNotFound}
	}
	return mockStringCmd{val: v}
}

func (p *pipelinedMockExecer) Exists(_ context.Context, keys ...string) auth.IntCmd {
	var n int64
	for _, k := range keys {
		if _, ok := p.store[k]; ok {
			n++
		}
	}
	return mockIntCmd{val: n}
}

type mockStringCmd struct {
	val string
	err error
}

func (c mockStringCmd) Result() (string, error) { return c.val, c.err }

type mockIntCmd struct {
	val int64
	err error
}

func (c mockIntCmd) Result() (int64, error) { return c.val, c.err }
