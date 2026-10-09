//go:build integration

// Index-fingerprint publication tests through the REAL flat / parent-child /
// late-chunking ingest paths (vector DB via TEST_VECTOR_DSN, fake HTTP
// embedder + chat endpoint). The fingerprint gates index copying, so it must
// be written exactly once, only for a completed, non-degraded run, and after
// the HyPE/RAPTOR tail.
package processor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/processor/raptor"
	"github.com/justrag/go-backend/internal/vector"
)

const fpEmbedDim = 64

type fpMode struct {
	failEmbedFrom atomic.Int64 // embed calls with number >= this fail (0 = never)
	failChat      atomic.Bool
	embedCalls    atomic.Int64
}

func fpServer(t *testing.T, m *fpMode) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/embeddings":
			n := m.embedCalls.Add(1)
			if from := m.failEmbedFrom.Load(); from > 0 && n >= from {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			var req struct {
				Input []string `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			type datum struct {
				Index     int       `json:"index"`
				Embedding []float64 `json:"embedding"`
			}
			resp := struct {
				Data []datum `json:"data"`
			}{Data: make([]datum, len(req.Input))}
			for i := range req.Input {
				vec := make([]float64, fpEmbedDim)
				for j := range vec {
					vec[j] = 0.01 + float64((i+j)%50)/100
				}
				resp.Data[i] = datum{Index: i, Embedding: vec}
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "/chat/completions":
			if m.failChat.Load() {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ctx"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type fpURLStore struct{ url string }

func (s fpURLStore) GetActiveAIProvider(context.Context) (*ai.AIProviderInfo, error) {
	return &ai.AIProviderInfo{ID: "p1", Name: "t", APIKey: "k", BaseURL: s.url}, nil
}
func (s fpURLStore) GetAIProviderByID(ctx context.Context, _ string) (*ai.AIProviderInfo, error) {
	return s.GetActiveAIProvider(ctx)
}
func (fpURLStore) GetAIModelsByProvider(context.Context, string) ([]ai.AIModelInfo, error) {
	return []ai.AIModelInfo{{Name: "chat-m"}, {Name: "emb", IsEmbedding: true, Dimensions: fpEmbedDim}}, nil
}
func (fpURLStore) GetKBModelOverrides(context.Context, string) (*ai.KBModelOverrides, error) {
	return nil, nil
}

type fpRaptor struct {
	store *mockStore
	err   error
	// fpAtBuild is the number of fingerprints already written when Build ran.
	fpAtBuild int
	calls     int
}

func (f *fpRaptor) Build(context.Context, raptor.BuildParams) (raptor.Stats, error) {
	f.calls++
	f.fpAtBuild = len(f.store.fingerprints)
	return raptor.Stats{}, f.err
}

func fpDoc(t *testing.T) string {
	t.Helper()
	var sb strings.Builder
	for i := 0; i < 60; i++ {
		sb.WriteString("Paragraph number ")
		sb.WriteString(strings.Repeat("x", i%9+1))
		sb.WriteString(" explains how the ingestion pipeline parses documents, splits them into chunks and embeds each one for retrieval.\n\n")
	}
	path := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// errRealRaptor selects the production raptor.Builder (real adapters over the
// vector DB, summariser/embedder talking to the fake HTTP server).
var errRealRaptor = errors.New("use the real raptor builder")

type fpRun struct {
	store *mockStore
	raptr *fpRaptor
	err   error
	p     *Processor
}

func runFP(t *testing.T, cfg map[string]string, mode *fpMode, withBuilder error) fpRun {
	t.Helper()
	dsn := os.Getenv("TEST_VECTOR_DSN")
	if dsn == "" {
		t.Skip("TEST_VECTOR_DSN unset")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	srv := fpServer(t, mode)
	store := &mockStore{}
	chunkSvc := vector.NewChunkService(pool)
	p := NewProcessor(parser.DefaultFactoryWith(nil), ai.NewConfigResolver(fpURLStore{srv.URL}), chunkSvc, store)
	vals := map[string]*string{"contextual_enrichment": strPtr("false")}
	for k, v := range cfg {
		vals[k] = strPtr(v)
	}
	p.SetSiteConfigReader(&fakeSiteConfigReader{values: vals})
	rb := &fpRaptor{store: store, err: withBuilder}
	if !errors.Is(withBuilder, errRealRaptor) {
		p.raptorBuilder = rb
	}

	kbID := uuid.NewString()
	t.Cleanup(func() { _ = chunkSvc.DeleteChunksByKbID(context.Background(), kbID, fpEmbedDim) })
	chunkSz := 0
	if errors.Is(withBuilder, errRealRaptor) {
		chunkSz = 128 // enough leaves to clear raptor_min_chunks
	}
	err = p.ProcessFile(context.Background(), ProcessFileInput{
		FileID: uuid.NewString(), FilePath: fpDoc(t), FileName: "doc.txt", MimeType: "text/plain",
		KBID: kbID, UserFileID: "uf-1", OwnerUserID: "o1", ChunkSize: chunkSz,
	})
	return fpRun{store: store, raptr: rb, err: err, p: p}
}

func (r fpRun) lastStatus() string {
	if len(r.store.statuses) == 0 {
		return ""
	}
	return r.store.statuses[len(r.store.statuses)-1]
}

func TestFingerprintE2E_FlatCompletedWritesOnceAfterTail(t *testing.T) {
	r := runFP(t, map[string]string{"raptor_enabled": "true"}, &fpMode{}, nil)
	if r.err != nil || r.lastStatus() != "completed" {
		t.Fatalf("err=%v statuses=%v", r.err, r.store.statuses)
	}
	if r.raptr.calls != 1 {
		t.Fatalf("raptor builder calls = %d, want 1", r.raptr.calls)
	}
	if r.raptr.fpAtBuild != 0 {
		t.Error("fingerprint was published BEFORE the RAPTOR tail")
	}
	if r.store.fpCalls != 1 {
		t.Errorf("fingerprints written = %d, want 1", r.store.fpCalls)
	}
}

func TestFingerprintE2E_PartialAndErrorWriteNone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		from   int64
		status string
	}{
		{"partial", 2, "partial"},
		{"error", 1, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &fpMode{}
			m.failEmbedFrom.Store(tc.from)
			r := runFP(t, map[string]string{"embedding_batch_size": "1"}, m, nil)
			if r.lastStatus() != tc.status {
				t.Fatalf("status = %q (err=%v), want %q", r.lastStatus(), r.err, tc.status)
			}
			if len(r.store.fingerprints) != 0 {
				t.Error("fingerprint written for a non-completed run")
			}
		})
	}
}

func TestFingerprintE2E_FailedTailStagesWriteNone(t *testing.T) {
	t.Run("hype enabled but stage fails (no HyPE store)", func(t *testing.T) {
		r := runFP(t, map[string]string{"hype_enabled": "true"}, &fpMode{}, nil)
		if r.lastStatus() != "completed" || len(r.store.fingerprints) != 0 {
			t.Errorf("status=%q fingerprints=%d", r.lastStatus(), len(r.store.fingerprints))
		}
	})
	t.Run("raptor enabled and build fails", func(t *testing.T) {
		r := runFP(t, map[string]string{"raptor_enabled": "true"}, &fpMode{}, errors.New("llm down"))
		if r.lastStatus() != "completed" || len(r.store.fingerprints) != 0 {
			t.Errorf("status=%q fingerprints=%d", r.lastStatus(), len(r.store.fingerprints))
		}
	})
	t.Run("enrichment fails for chunks", func(t *testing.T) {
		m := &fpMode{}
		m.failChat.Store(true)
		r := runFP(t, map[string]string{"contextual_enrichment": "true"}, m, nil)
		if r.lastStatus() != "completed" || len(r.store.fingerprints) != 0 {
			t.Errorf("status=%q fingerprints=%d", r.lastStatus(), len(r.store.fingerprints))
		}
	})
}

func TestFingerprintE2E_ParentChildAndLateChunkingWrite(t *testing.T) {
	for name, cfg := range map[string]map[string]string{
		"parent-child":  {"parent_child_enabled": "true"},
		"late chunking": {"late_chunking_enabled": "true"},
	} {
		t.Run(name, func(t *testing.T) {
			r := runFP(t, cfg, &fpMode{}, nil)
			if r.err != nil || r.lastStatus() != "completed" {
				t.Fatalf("err=%v statuses=%v", r.err, r.store.statuses)
			}
			if r.store.fpCalls != 1 {
				t.Errorf("fingerprints written = %d, want 1", r.store.fpCalls)
			}
		})
	}
}

// The production RAPTOR builder swallows per-cluster LLM failures and still
// returns nil; the fingerprint must nevertheless not be published.
func TestFingerprintE2E_RealRaptorBuilder(t *testing.T) {
	t.Run("summariser down: no fingerprint", func(t *testing.T) {
		m := &fpMode{}
		m.failChat.Store(true)
		r := runFP(t, map[string]string{"raptor_enabled": "true", "raptor_min_chunks": "5"}, m, errRealRaptor)
		if r.lastStatus() != "completed" || r.store.fpCalls != 0 {
			t.Errorf("status=%q fingerprints=%d", r.lastStatus(), r.store.fpCalls)
		}
	})
	t.Run("healthy build: fingerprint written", func(t *testing.T) {
		r := runFP(t, map[string]string{"raptor_enabled": "true", "raptor_min_chunks": "5"}, &fpMode{}, errRealRaptor)
		if r.lastStatus() != "completed" || r.store.fpCalls != 1 {
			t.Errorf("status=%q fingerprints=%d", r.lastStatus(), r.store.fpCalls)
		}
	})
	t.Run("below min chunks: no tree by design, still a donor", func(t *testing.T) {
		m := &fpMode{}
		m.failChat.Store(true)
		r := runFP(t, map[string]string{"raptor_enabled": "true", "raptor_min_chunks": "200"}, m, errRealRaptor)
		if r.lastStatus() != "completed" || r.store.fpCalls != 1 {
			t.Errorf("status=%q fingerprints=%d", r.lastStatus(), r.store.fpCalls)
		}
	})
}
