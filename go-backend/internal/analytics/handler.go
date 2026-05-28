package analytics

import (
	"net/http"
	"time"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// Handler holds the analytics Store dependency.
type Handler struct {
	store Store
}

// NewHandler creates a Handler using the given Store implementation.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// parseFilters extracts optional filter query parameters from the request:
// from, to (RFC3339), fileType, status.
func parseFilters(r *http.Request) Filters {
	var f Filters

	if raw := r.URL.Query().Get("from"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			f.DateRange.From = &t
		}
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			f.DateRange.To = &t
		}
	}
	f.FileType = r.URL.Query().Get("fileType")
	f.Status = r.URL.Query().Get("status")
	return f
}

// getKBID returns the KB ID, preferring the value stored in context by the
// kbaccess middleware, falling back to the {id} path value.
func getKBID(r *http.Request) string {
	if access := kbaccess.AccessFromContext(r.Context()); access != nil && access.KB != nil {
		return access.KB.ID
	}
	return r.PathValue("id")
}

// ensureFileStats replaces nil slices in a FileStats with empty slices so
// that the JSON output never contains null arrays.
func ensureFileStats(s *FileStats) FileStats {
	if s == nil {
		return FileStats{
			ByType:   []TypeCount{},
			ByStatus: []StatusCount{},
			ByOrigin: []OriginCount{},
		}
	}
	if s.ByType == nil {
		s.ByType = []TypeCount{}
	}
	if s.ByStatus == nil {
		s.ByStatus = []StatusCount{}
	}
	if s.ByOrigin == nil {
		s.ByOrigin = []OriginCount{}
	}
	return *s
}

// ensureActivityStats replaces nil slices in an ActivityStats.
func ensureActivityStats(s *ActivityStats) ActivityStats {
	if s == nil {
		return ActivityStats{
			FilesOverTime:    []DateCount{},
			ChatsOverTime:    []DateCount{},
			MessagesOverTime: []DateCount{},
		}
	}
	if s.FilesOverTime == nil {
		s.FilesOverTime = []DateCount{}
	}
	if s.ChatsOverTime == nil {
		s.ChatsOverTime = []DateCount{}
	}
	if s.MessagesOverTime == nil {
		s.MessagesOverTime = []DateCount{}
	}
	return *s
}

// ensureChatStats replaces nil slices in a ChatStats.
func ensureChatStats(s *ChatStats) ChatStats {
	if s == nil {
		return ChatStats{MessagesByRole: []RoleCount{}}
	}
	if s.MessagesByRole == nil {
		s.MessagesByRole = []RoleCount{}
	}
	return *s
}

// ensureGeneratedContentStats replaces nil slices in a GeneratedContentStats.
func ensureGeneratedContentStats(s *GeneratedContentStats) GeneratedContentStats {
	if s == nil {
		return GeneratedContentStats{
			ByType:   []TypeCount{},
			OverTime: []DateCount{},
		}
	}
	if s.ByType == nil {
		s.ByType = []TypeCount{}
	}
	if s.OverTime == nil {
		s.OverTime = []DateCount{}
	}
	return *s
}

// ensureRetrievalQualityStats replaces nil slices in a RetrievalQualityStats.
func ensureRetrievalQualityStats(s *RetrievalQualityStats) RetrievalQualityStats {
	if s == nil {
		return RetrievalQualityStats{
			FeedbackStats:   []FeedbackCount{},
			ScoreOverTime:   []ScorePoint{},
			LowScoreQueries: []LowScoreQuery{},
		}
	}
	if s.FeedbackStats == nil {
		s.FeedbackStats = []FeedbackCount{}
	}
	if s.ScoreOverTime == nil {
		s.ScoreOverTime = []ScorePoint{}
	}
	if s.LowScoreQueries == nil {
		s.LowScoreQueries = []LowScoreQuery{}
	}
	return *s
}

// GetDashboard handles GET /api/kb/{id}/analytics.
// It fetches all four stat groups and returns them as a single DashboardData.
func (h *Handler) GetDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := getKBID(r)
	f := parseFilters(r)

	fileStats, err := h.store.GetFileStats(ctx, kbID, f)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to get file stats"})
		return
	}

	activityStats, err := h.store.GetActivityStats(ctx, kbID, f)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to get activity stats"})
		return
	}

	chatStats, err := h.store.GetChatStats(ctx, kbID, f)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to get chat stats"})
		return
	}

	generatedStats, err := h.store.GetGeneratedContentStats(ctx, kbID, f)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to get generated content stats"})
		return
	}

	dashboard := DashboardData{
		Files:            ensureFileStats(fileStats),
		Activity:         ensureActivityStats(activityStats),
		Chats:            ensureChatStats(chatStats),
		GeneratedContent: ensureGeneratedContentStats(generatedStats),
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, dashboard)
}

// GetFiles handles GET /api/kb/{id}/analytics/files.
func (h *Handler) GetFiles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := getKBID(r)
	f := parseFilters(r)

	stats, err := h.store.GetFileStats(ctx, kbID, f)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to get file stats"})
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, ensureFileStats(stats))
}

// GetActivity handles GET /api/kb/{id}/analytics/activity.
func (h *Handler) GetActivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := getKBID(r)
	f := parseFilters(r)

	stats, err := h.store.GetActivityStats(ctx, kbID, f)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to get activity stats"})
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, ensureActivityStats(stats))
}

// GetChats handles GET /api/kb/{id}/analytics/chats.
func (h *Handler) GetChats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := getKBID(r)
	f := parseFilters(r)

	stats, err := h.store.GetChatStats(ctx, kbID, f)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to get chat stats"})
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, ensureChatStats(stats))
}

// GetGenerated handles GET /api/kb/{id}/analytics/generated.
func (h *Handler) GetGenerated(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := getKBID(r)
	f := parseFilters(r)

	stats, err := h.store.GetGeneratedContentStats(ctx, kbID, f)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to get generated content stats"})
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, ensureGeneratedContentStats(stats))
}

// GetRetrievalQuality handles GET /api/kb/{id}/analytics/retrieval-quality.
func (h *Handler) GetRetrievalQuality(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := getKBID(r)
	f := parseFilters(r)

	stats, err := h.store.GetRetrievalQualityStats(ctx, kbID, f)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to get retrieval quality stats"})
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, ensureRetrievalQualityStats(stats))
}
