package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/prompts"
)

// fakeLister implements KBRouterCandidateLister for unit tests.
type fakeLister struct {
	candidates []prompts.KBRouterCandidate
	err        error
}

func (f *fakeLister) ListKBRouterCandidates(_ context.Context, _ string) ([]prompts.KBRouterCandidate, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.candidates, nil
}

// TestResolveKBViaRouter_AutoFalse: when the request didn't ask for
// auto routing, the resolver returns the requested kb unchanged AND
// short-circuits before any LLM/lister call. This is the most-common
// path; even a misconfigured lister mustn't break it.
func TestResolveKBViaRouter_AutoFalse(t *testing.T) {
	t.Parallel()
	got, outcome, err := ResolveKBViaRouter(
		context.Background(), nil, nil, nil,
		"user-1", "kb-explicit", "any query",
		false, // auto = false
	)
	if err != nil || got != "kb-explicit" || outcome != "skipped_no_auto" {
		t.Errorf("got=%q outcome=%q err=%v; want kb-explicit/skipped_no_auto/nil", got, outcome, err)
	}
}

// TestResolveKBViaRouter_GateOff: auto=true but the site_config gate
// is off → return requested unchanged with skipped_disabled.
func TestResolveKBViaRouter_GateOff(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	got, outcome, err := ResolveKBViaRouter(
		context.Background(), nil, r, nil,
		"user-1", "kb-explicit", "any query",
		true,
	)
	if err != nil || got != "kb-explicit" || outcome != "skipped_disabled" {
		t.Errorf("got=%q outcome=%q err=%v; want kb-explicit/skipped_disabled/nil", got, outcome, err)
	}
}

// TestResolveKBViaRouter_NoLister: gate on, auto on, but no lister
// wired → llm_error with original kb preserved (fail-open).
func TestResolveKBViaRouter_NoLister(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_kb_router_enabled": strPtr("true"),
	}}
	got, outcome, _ := ResolveKBViaRouter(
		context.Background(), nil, r, nil,
		"user-1", "kb-explicit", "any query",
		true,
	)
	if got != "kb-explicit" || outcome != "llm_error" {
		t.Errorf("got=%q outcome=%q; want kb-explicit/llm_error", got, outcome)
	}
}

// TestResolveKBViaRouter_NoCandidates: gate on, lister wired, but the
// user has zero accessible KBs. skipped_no_candidates outcome; the
// requested kb is preserved (the URL kb is presumably the user's
// only one — the permission check downstream catches the mismatch).
func TestResolveKBViaRouter_NoCandidates(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_kb_router_enabled": strPtr("true"),
	}}
	lister := &fakeLister{candidates: nil}
	got, outcome, _ := ResolveKBViaRouter(
		context.Background(), nil, r, lister,
		"user-1", "kb-explicit", "q",
		true,
	)
	if got != "kb-explicit" || outcome != "skipped_no_candidates" {
		t.Errorf("got=%q outcome=%q; want kb-explicit/skipped_no_candidates", got, outcome)
	}
}

// TestResolveKBViaRouter_ListerError: DB error from the lister
// surfaces as a non-nil err with the requested kb preserved (fail-
// open). Caller logs and proceeds.
func TestResolveKBViaRouter_ListerError(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_kb_router_enabled": strPtr("true"),
	}}
	lister := &fakeLister{err: errors.New("db boom")}
	got, outcome, err := ResolveKBViaRouter(
		context.Background(), nil, r, lister,
		"user-1", "kb-explicit", "q",
		true,
	)
	if err == nil {
		t.Error("expected non-nil err on lister failure")
	}
	if got != "kb-explicit" || outcome != "llm_error" {
		t.Errorf("got=%q outcome=%q; want kb-explicit/llm_error", got, outcome)
	}
}
