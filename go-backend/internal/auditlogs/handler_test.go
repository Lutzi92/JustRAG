package auditlogs_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/auditlogs"
)

var _ auditlogs.Store = (*mockStore)(nil)

// mockStore is a test double for auditlogs.Store.
type mockStore struct {
	rows []auditlogs.AuditLogRow
	err  error
	// last filters passed to GetAuditLogs, inspectable by tests
	lastFilters auditlogs.AuditLogFilters
}

func (m *mockStore) GetAuditLogs(_ context.Context, f auditlogs.AuditLogFilters) ([]auditlogs.AuditLogRow, error) {
	m.lastFilters = f
	return m.rows, m.err
}

func TestGetAuditLogs_DefaultsAndResponse(t *testing.T) {
	operatorID := "user-abc"
	rows := []auditlogs.AuditLogRow{
		{
			ID:         "log-1",
			OperatorID: &operatorID,
			Action:     "create_kb",
			TargetType: "knowledge_base",
			TargetID:   nil,
			Diff:       nil,
			CreatedAt:  time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		},
	}

	store := &mockStore{rows: rows}
	h := auditlogs.NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/audit-logs", nil)
	rr := httptest.NewRecorder()

	h.GetAuditLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	// Verify default limit and offset
	if store.lastFilters.Limit != 100 {
		t.Errorf("expected default limit=100, got %d", store.lastFilters.Limit)
	}
	if store.lastFilters.Offset != 0 {
		t.Errorf("expected default offset=0, got %d", store.lastFilters.Offset)
	}

	var result []auditlogs.AuditLogRow
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result))
	}
	if result[0].ID != "log-1" {
		t.Errorf("expected ID=log-1, got %s", result[0].ID)
	}
	if result[0].Action != "create_kb" {
		t.Errorf("expected Action=create_kb, got %s", result[0].Action)
	}
}

func TestGetAuditLogs_QueryParamParsing(t *testing.T) {
	store := &mockStore{rows: []auditlogs.AuditLogRow{}}
	h := auditlogs.NewHandler(store)

	url := "/api/admin/audit-logs" +
		"?operatorId=user-42" +
		"&action=delete_file" +
		"&targetType=file" +
		"&from=2025-03-01T00:00:00Z" +
		"&to=2025-03-31T23:59:59Z" +
		"&limit=25" +
		"&offset=50"

	req := httptest.NewRequest(http.MethodGet, url, nil)
	rr := httptest.NewRecorder()

	h.GetAuditLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	f := store.lastFilters
	if f.OperatorID != "user-42" {
		t.Errorf("expected OperatorID=user-42, got %q", f.OperatorID)
	}
	if f.Action != "delete_file" {
		t.Errorf("expected Action=delete_file, got %q", f.Action)
	}
	if f.TargetType != "file" {
		t.Errorf("expected TargetType=file, got %q", f.TargetType)
	}
	if f.Limit != 25 {
		t.Errorf("expected limit=25, got %d", f.Limit)
	}
	if f.Offset != 50 {
		t.Errorf("expected offset=50, got %d", f.Offset)
	}
	if f.From == nil {
		t.Error("expected From to be set")
	} else if f.From.Year() != 2025 || f.From.Month() != 3 || f.From.Day() != 1 {
		t.Errorf("unexpected From: %v", f.From)
	}
	if f.To == nil {
		t.Error("expected To to be set")
	} else if f.To.Year() != 2025 || f.To.Month() != 3 || f.To.Day() != 31 {
		t.Errorf("unexpected To: %v", f.To)
	}
}

func TestGetAuditLogs_EmptyResultIsArray(t *testing.T) {
	store := &mockStore{rows: []auditlogs.AuditLogRow{}}
	h := auditlogs.NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/audit-logs", nil)
	rr := httptest.NewRecorder()

	h.GetAuditLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	body := rr.Body.String()
	// The JSON body must be an array (not null or an object).
	if len(body) < 2 || body[0] != '[' {
		t.Errorf("expected JSON array, got: %s", body)
	}
}

func TestGetAuditLogs_StoreError(t *testing.T) {
	store := &mockStore{err: context.DeadlineExceeded}
	h := auditlogs.NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/audit-logs", nil)
	rr := httptest.NewRecorder()

	h.GetAuditLogs(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}
