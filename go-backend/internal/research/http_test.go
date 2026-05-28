package research_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/research"
)

// ---------------------------------------------------------------------------
// Stub store
// ---------------------------------------------------------------------------

type stubStore struct {
	session *chat.ChatRow
	saveErr error
}

func (s *stubStore) CreateResearchSession(_ context.Context, kbID, userID, goal, sessionType string) (*chat.ChatRow, error) {
	if s.session != nil {
		return s.session, nil
	}
	return &chat.ChatRow{
		ID:        "test-session-id",
		KbID:      kbID,
		UserID:    userID,
		Title:     goal,
		Type:      sessionType,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, nil
}

func (s *stubStore) SaveResearchResults(_ context.Context, chatID, goal, report string, findings any) error {
	return s.saveErr
}

func (s *stubStore) GetChatByID(_ context.Context, chatID string) (*chat.ChatRow, error) {
	if chatID == "not-found" {
		return nil, nil
	}
	return &chat.ChatRow{
		ID:        chatID,
		KbID:      "kb-1",
		UserID:    "user-1",
		Title:     "Test research goal",
		Type:      "research",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, nil
}

func (s *stubStore) GetChatMessages(_ context.Context, chatID string) ([]chat.MessageRow, error) {
	return nil, nil
}

func (s *stubStore) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	v := "true"
	return &v, nil
}

// ---------------------------------------------------------------------------
// Helper: inject a fake authenticated user into the request context.
// ---------------------------------------------------------------------------

func withUser(r *http.Request, userID string) *http.Request {
	claims := &auth.Claims{ID: userID}
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

// ---------------------------------------------------------------------------
// Test: Abort sets Redis key → 200
// ---------------------------------------------------------------------------

func TestAbort_SetsRedisKey(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	store := &stubStore{}
	h := research.NewHandler(store, nil, nil, nil, rdb)

	req := httptest.NewRequest(http.MethodPost, "/api/research/abc123/abort", nil)
	req.SetPathValue("researchId", "abc123")
	req = withUser(req, "user-1")

	w := httptest.NewRecorder()
	h.Abort(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the key was set in Redis.
	val, err := rdb.Get(context.Background(), "research:abort:abc123").Result()
	if err != nil {
		t.Fatalf("expected Redis key to be set, got error: %v", err)
	}
	if val != "1" {
		t.Fatalf("expected value '1', got %q", val)
	}
}

// ---------------------------------------------------------------------------
// Test: ExportReport returns DOCX file download headers
// ---------------------------------------------------------------------------

func TestExportReport_ReturnsDocxHeaders(t *testing.T) {
	store := &stubStore{}
	h := research.NewHandler(store, nil, nil, nil, nil)

	body := map[string]string{"report": "# My Report\n\nThis is the content."}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodGet, "/api/research/sess-1/report", bytes.NewReader(bodyBytes))
	req.SetPathValue("sessionId", "sess-1")
	req = withUser(req, "user-1")

	w := httptest.NewRecorder()
	h.ExportReport(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	ct := w.Header().Get("Content-Type")
	want := "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	if ct != want {
		t.Errorf("Content-Type = %q, want %q", ct, want)
	}

	cd := w.Header().Get("Content-Disposition")
	if cd == "" {
		t.Error("expected Content-Disposition header to be set")
	}

	if w.Body.Len() == 0 {
		t.Error("expected non-empty response body (DOCX bytes)")
	}
}

// ---------------------------------------------------------------------------
// Test: NewHandler compiles and returns non-nil
// ---------------------------------------------------------------------------

func TestNewHandler_Compiles(t *testing.T) {
	store := &stubStore{}
	h := research.NewHandler(store, nil, nil, nil, nil)
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
}

// ---------------------------------------------------------------------------
// Test: GetStatus — session not found → 404
// ---------------------------------------------------------------------------

func TestGetStatus_NotFound(t *testing.T) {
	store := &stubStore{}
	h := research.NewHandler(store, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/research/not-found/status", nil)
	req.SetPathValue("researchId", "not-found")
	req = withUser(req, "user-1")

	w := httptest.NewRecorder()
	h.GetStatus(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Test: GenerateDocx produces valid ZIP (PK magic bytes) with content
// ---------------------------------------------------------------------------

func TestGenerateDocx_NonEmpty(t *testing.T) {
	docx := research.GenerateDocx("Test Title", "# Heading\n\nBody text **bold** end.")
	if len(docx) == 0 {
		t.Fatal("expected non-empty DOCX bytes")
	}
	// A DOCX/ZIP starts with the PK magic bytes (0x50 0x4B).
	if docx[0] != 'P' || docx[1] != 'K' {
		t.Errorf("expected ZIP magic bytes PK, got %02x %02x", docx[0], docx[1])
	}
}
