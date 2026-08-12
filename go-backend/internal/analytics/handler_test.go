package analytics_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/analytics"
	"github.com/justrag/go-backend/internal/kbaccess"
)

var _ analytics.Store = (*mockStore)(nil)

// mockStore is a test double for analytics.Store that returns pre-set values.
type mockStore struct {
	fileStats      *analytics.FileStats
	activityStats  *analytics.ActivityStats
	chatStats      *analytics.ChatStats
	generatedStats *analytics.GeneratedContentStats
	retrievalStats *analytics.RetrievalQualityStats
}

func (m *mockStore) GetFileStats(_ context.Context, _ string, _ analytics.Filters) (*analytics.FileStats, error) {
	return m.fileStats, nil
}

func (m *mockStore) GetActivityStats(_ context.Context, _ string, _ analytics.Filters) (*analytics.ActivityStats, error) {
	return m.activityStats, nil
}

func (m *mockStore) GetChatStats(_ context.Context, _ string, _ analytics.Filters) (*analytics.ChatStats, error) {
	return m.chatStats, nil
}

func (m *mockStore) GetGeneratedContentStats(_ context.Context, _ string, _ analytics.Filters) (*analytics.GeneratedContentStats, error) {
	return m.generatedStats, nil
}

func (m *mockStore) GetRetrievalQualityStats(_ context.Context, _ string, _ analytics.Filters) (*analytics.RetrievalQualityStats, error) {
	return m.retrievalStats, nil
}

// makeContext injects a KBAccessResult into the given context so that
// getKBID() inside the handler resolves to kbID.
func makeContext(ctx context.Context, kbID string) context.Context {
	access := &kbaccess.KBAccessResult{
		KB:      &kbaccess.KnowledgeBase{ID: kbID, IsGlobal: true},
		IsOwner: false,
		Role:    kbaccess.RoleEdit,
	}
	return kbaccess.WithAccess(ctx, access)
}

func TestGetDashboard(t *testing.T) {
	store := &mockStore{
		fileStats: &analytics.FileStats{
			ByType:     []analytics.TypeCount{{Type: "pdf", Count: 3}},
			ByStatus:   []analytics.StatusCount{{Status: "ready", Count: 3}},
			ByOrigin:   []analytics.OriginCount{{Origin: "upload", Count: 3}},
			TotalFiles: 3,
			TotalSize:  1024,
		},
		activityStats: &analytics.ActivityStats{
			FilesOverTime:    []analytics.DateCount{{Date: "2025-01-01", Count: 1}},
			ChatsOverTime:    []analytics.DateCount{},
			MessagesOverTime: []analytics.DateCount{},
		},
		chatStats: &analytics.ChatStats{
			TotalChats:         5,
			TotalMessages:      20,
			MessagesByRole:     []analytics.RoleCount{{Role: "user", Count: 10}},
			AvgMessagesPerChat: 4.0,
		},
		generatedStats: &analytics.GeneratedContentStats{
			ByType:         []analytics.TypeCount{{Type: "summary", Count: 2}},
			TotalGenerated: 2,
			OverTime:       []analytics.DateCount{},
		},
	}

	h := analytics.NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/analytics", nil)
	req = req.WithContext(makeContext(req.Context(), "kb-1"))
	rr := httptest.NewRecorder()

	h.GetDashboard(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var result analytics.DashboardData
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if result.Files.TotalFiles != 3 {
		t.Errorf("expected TotalFiles=3, got %d", result.Files.TotalFiles)
	}
	if result.Chats.TotalChats != 5 {
		t.Errorf("expected TotalChats=5, got %d", result.Chats.TotalChats)
	}
	if result.GeneratedContent.TotalGenerated != 2 {
		t.Errorf("expected TotalGenerated=2, got %d", result.GeneratedContent.TotalGenerated)
	}
	if len(result.Activity.FilesOverTime) != 1 {
		t.Errorf("expected 1 FilesOverTime entry, got %d", len(result.Activity.FilesOverTime))
	}
}

func TestGetFiles(t *testing.T) {
	store := &mockStore{
		fileStats: &analytics.FileStats{
			ByType:     []analytics.TypeCount{{Type: "docx", Count: 7}},
			ByStatus:   []analytics.StatusCount{},
			ByOrigin:   []analytics.OriginCount{},
			TotalFiles: 7,
			TotalSize:  2048,
		},
	}

	h := analytics.NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb-2/analytics/files", nil)
	req = req.WithContext(makeContext(req.Context(), "kb-2"))
	rr := httptest.NewRecorder()

	h.GetFiles(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var result analytics.FileStats
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if result.TotalFiles != 7 {
		t.Errorf("expected TotalFiles=7, got %d", result.TotalFiles)
	}
	if len(result.ByType) != 1 || result.ByType[0].Type != "docx" {
		t.Errorf("unexpected ByType: %+v", result.ByType)
	}
	// nil slices should be replaced with empty slices, not null
	if result.ByStatus == nil {
		t.Error("ByStatus should not be nil")
	}
}

func TestGetRetrievalQuality(t *testing.T) {
	store := &mockStore{
		retrievalStats: &analytics.RetrievalQualityStats{
			FeedbackStats: []analytics.FeedbackCount{{Feedback: "positive", Count: 10}},
			ScoreOverTime: []analytics.ScorePoint{
				{Day: "2025-01-01", AvgScore: 0.85, MessageCount: 5},
			},
			LowScoreQueries: []analytics.LowScoreQuery{
				{ID: "msg-1", Content: "what is X?", CreatedAt: "2025-01-01T00:00:00Z", AvgScore: 0.2},
			},
		},
	}

	h := analytics.NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb-3/analytics/retrieval-quality", nil)
	req = req.WithContext(makeContext(req.Context(), "kb-3"))
	rr := httptest.NewRecorder()

	h.GetRetrievalQuality(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var result analytics.RetrievalQualityStats
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(result.FeedbackStats) != 1 || result.FeedbackStats[0].Feedback != "positive" {
		t.Errorf("unexpected FeedbackStats: %+v", result.FeedbackStats)
	}
	if len(result.ScoreOverTime) != 1 {
		t.Errorf("expected 1 ScoreOverTime entry, got %d", len(result.ScoreOverTime))
	}
	if len(result.LowScoreQueries) != 1 {
		t.Errorf("expected 1 LowScoreQuery, got %d", len(result.LowScoreQueries))
	}
}
