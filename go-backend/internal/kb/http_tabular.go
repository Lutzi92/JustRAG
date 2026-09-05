package kb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/store"
	"github.com/justrag/go-backend/internal/tabular"
)

// GetFileTabular handles GET /api/kb/{id}/files/{fileId}/tabular — the
// "Tabellen" file-detail panel: the persisted spreadsheet ingest report
// (files.parse_report) plus the tabular_catalog projection for the file,
// joined into tabular.FileTabularDTO.
//
// The route sits on kbViewChain, which only proves the caller holds `view`
// on {id} — not that {fileId} belongs to that KB. fileBelongsToKB is the
// guard: a file that exists but belongs to a different KB 404s exactly like
// an unknown file id, so this endpoint cannot be used to probe another KB's
// tabular data by fileId alone. A non-spreadsheet file (no parse_report, no
// catalog rows) is a normal 200 with `{"report":null,"tables":[]}`, not a
// 404 — the panel needs to render an empty state for it, not an error page.
func (h *Handler) GetFileTabular(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	kbID := r.PathValue("id")
	fileID := r.PathValue("fileId")
	if kbID == "" || fileID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing id")
		return
	}

	belongs, err := h.fileBelongsToKB(r.Context(), kbID, fileID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to look up file")
		return
	}
	if !belongs {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "file not found")
		return
	}

	reportJSON, err := h.store.GetFileParseReport(r.Context(), fileID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to load parse report")
		return
	}

	var report *tabular.ParseReport
	if len(reportJSON) > 0 {
		report = &tabular.ParseReport{}
		if err := json.Unmarshal(reportJSON, report); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to decode parse report")
			return
		}
	}

	entries, err := h.store.ListTabularCatalogByFile(r.Context(), fileID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to load tabular catalog")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, tabular.BuildFileTabularDTO(report, entries))
}

// fileBelongsToKB reports whether fileID exists and is owned by kbID.
func (h *Handler) fileBelongsToKB(ctx context.Context, kbID, fileID string) (bool, error) {
	file, err := h.store.GetFileByID(ctx, fileID)
	if err != nil {
		return false, err
	}
	if file == nil {
		return false, nil
	}
	return file.KbID == kbID, nil
}
