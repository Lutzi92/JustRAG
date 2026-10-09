package adkbridge

import (
	"context"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
)

type stubResolver struct {
	cfg *ai.ResolvedConfig
	err error
	got string
}

func (s *stubResolver) Resolve(_ context.Context, kbID string) (*ai.ResolvedConfig, error) {
	s.got = kbID
	return s.cfg, s.err
}

func TestFactoryResolvesPerKBAndHonoursOverride(t *testing.T) {
	r := &stubResolver{cfg: &ai.ResolvedConfig{BaseURL: "http://x/v1/", APIKey: "k",
		ChatModel: "main", ChatModels: []string{"main", "other"}, ReasoningModels: []string{"main"}}}
	f := NewModelFactory(r)

	m, err := f.For(context.Background(), "kb-1", "", "low")
	if err != nil {
		t.Fatal(err)
	}
	if r.got != "kb-1" || m.Name() != "main" || m.reasoningEffort != "low" {
		t.Fatalf("kb=%q name=%q effort=%q", r.got, m.Name(), m.reasoningEffort)
	}
	m, _ = f.For(context.Background(), "kb-1", "other", "low")
	if m.Name() != "other" || m.reasoningEffort != "" {
		t.Fatalf("override: name=%q effort=%q (non-reasoning model must not think)", m.Name(), m.reasoningEffort)
	}
	m, _ = f.For(context.Background(), "kb-1", "not-offered", "")
	if m.Name() != "main" {
		t.Fatalf("unknown override must fall back, got %q", m.Name())
	}
}

func TestFactoryPropagatesResolverError(t *testing.T) {
	_, err := NewModelFactory(&stubResolver{err: ai.ErrNoActiveProvider}).For(context.Background(), "", "", "")
	if !errors.Is(err, ai.ErrNoActiveProvider) {
		t.Fatalf("err = %v", err)
	}
}
