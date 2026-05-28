// Command eval-genset regenerates an eval golden set JSONL from the
// authoritative XLSX, mapping the XLSX's stable Confluence-page-IDs to the
// current KB's files.id UUIDs via files.confluence_page_id.
//
// Use this whenever the file UUIDs in production.jsonl drift — most commonly
// after re-ingesting the same source documents into a fresh KB to A/B a new
// embedder/reranker. The Confluence page IDs in the XLSX (column gold_doc_ids)
// are stable across re-ingests; the JustRAG file UUIDs are not. Re-import the
// emitted JSONL via the admin UI to refresh eval_golden_sets.
//
// Usage:
//
//	eval-genset --xlsx eval/golden/JLU_RAG_Eval_Set_v1.xlsx \
//	            --kb-id <kb-uuid> \
//	            --output eval/golden/production.jsonl
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"

	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/eval"
)

const sheetName = "Fragen"

// xlsxColumns lists the header titles we read from the "Fragen" sheet. We
// resolve them by name rather than by fixed position so column reordering in
// the source spreadsheet is non-breaking.
var xlsxColumns = []string{
	"ID", "Frage", "Kategorie", "gold_doc_ids", "Sprache", "Notizen",
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	xlsxPath := flag.String("xlsx", "", "Path to the XLSX golden-set source")
	kbID := flag.String("kb-id", "", "Target KB UUID — files in this KB are looked up by confluence_page_id")
	outputPath := flag.String("output", "", "Output JSONL path")
	flag.Parse()

	if *xlsxPath == "" || *kbID == "" || *outputPath == "" {
		slog.Error("--xlsx, --kb-id and --output are required")
		flag.Usage()
		os.Exit(2)
	}

	rows, err := readQuestionRows(*xlsxPath)
	if err != nil {
		slog.Error("read xlsx", "path", *xlsxPath, "error", err)
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		cfg.DB.User, cfg.DB.Password, cfg.DB.Host, cfg.DB.Port, cfg.DB.Name)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		slog.Error("connect main db", "host", cfg.DB.Host, "name", cfg.DB.Name, "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Collect all Confluence IDs referenced by non-skipped rows so we can
	// resolve them in a single batched query.
	wanted := map[string]struct{}{}
	for _, r := range rows {
		if _, skip := mapCategoryToQueryType(r.category); skip {
			continue
		}
		for _, id := range tokenizeIDs(r.goldDocIDs) {
			wanted[id] = struct{}{}
		}
	}
	wantedSlice := make([]string, 0, len(wanted))
	for id := range wanted {
		wantedSlice = append(wantedSlice, id)
	}
	sort.Strings(wantedSlice) // deterministic logging

	mapping, err := loadConfluenceMapping(ctx, pool, *kbID, wantedSlice)
	if err != nil {
		slog.Error("load confluence mapping", "error", err)
		os.Exit(1)
	}
	slog.Info("confluence mapping loaded",
		"kb_id", *kbID, "requested", len(wantedSlice), "resolved", len(mapping))

	// Walk rows, emit JSONL.
	out, err := os.Create(*outputPath)
	if err != nil {
		slog.Error("create output", "path", *outputPath, "error", err)
		os.Exit(1)
	}
	defer out.Close()

	enc := json.NewEncoder(out)

	var (
		written, skippedUnanswerable, skippedNoMapping, partial, totalMissing int
	)
	for _, r := range rows {
		qt, skip := mapCategoryToQueryType(r.category)
		if skip {
			skippedUnanswerable++
			continue
		}
		ids := tokenizeIDs(r.goldDocIDs)
		mapped, missing := applyMapping(ids, mapping)
		if len(missing) > 0 {
			totalMissing += len(missing)
			slog.Warn("question has unmapped confluence ids",
				"q_id", r.id, "missing", missing, "mapped_count", len(mapped))
		}
		if len(mapped) == 0 {
			skippedNoMapping++
			slog.Warn("dropping question — no Confluence IDs resolved",
				"q_id", r.id, "requested", ids)
			continue
		}
		if len(mapped) < len(ids) {
			partial++
		}
		q := eval.Question{
			ID:              r.id,
			Question:        r.question,
			KbID:            *kbID,
			Language:        r.language,
			MustCiteFileIDs: mapped,
			QueryType:       qt,
			Notes:           r.notes,
		}
		if err := enc.Encode(&q); err != nil {
			slog.Error("encode question", "q_id", r.id, "error", err)
			os.Exit(1)
		}
		written++
	}

	slog.Info("done",
		"written", written,
		"skipped_unanswerable", skippedUnanswerable,
		"skipped_no_mapping", skippedNoMapping,
		"partial_questions", partial,
		"total_missing_ids", totalMissing,
		"output", *outputPath,
	)
}

// xlsxRow holds the subset of XLSX columns we care about for one question.
type xlsxRow struct {
	id, question, category, goldDocIDs, language, notes string
}

// readQuestionRows opens the XLSX at path and returns one xlsxRow per
// non-empty data row in the "Fragen" sheet. Columns are resolved by header
// name; missing columns are reported as an error.
func readQuestionRows(path string) ([]xlsxRow, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	raw, err := f.GetRows(sheetName)
	if err != nil {
		return nil, fmt.Errorf("read sheet %q: %w", sheetName, err)
	}
	if len(raw) < 2 {
		return nil, fmt.Errorf("sheet %q has no data rows", sheetName)
	}

	headers := raw[0]
	idx := map[string]int{}
	for i, h := range headers {
		idx[h] = i
	}
	for _, name := range xlsxColumns {
		if _, ok := idx[name]; !ok {
			return nil, fmt.Errorf("sheet %q missing required column %q", sheetName, name)
		}
	}

	get := func(row []string, col string) string {
		i := idx[col]
		if i >= len(row) {
			return ""
		}
		return row[i]
	}

	var out []xlsxRow
	for _, row := range raw[1:] {
		// Skip wholly-empty trailing rows.
		if len(row) == 0 {
			continue
		}
		id := get(row, "ID")
		if id == "" {
			continue
		}
		out = append(out, xlsxRow{
			id:         id,
			question:   get(row, "Frage"),
			category:   get(row, "Kategorie"),
			goldDocIDs: get(row, "gold_doc_ids"),
			language:   get(row, "Sprache"),
			notes:      get(row, "Notizen"),
		})
	}
	return out, nil
}

// loadConfluenceMapping returns a map from confluence_page_id → files.id (as
// a UUID string) for every requested ID present in the target KB. IDs not
// found are simply absent from the map; the caller decides how to surface
// that.
func loadConfluenceMapping(ctx context.Context, pool *pgxpool.Pool, kbID string, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT id::text, confluence_page_id
		FROM files
		WHERE kb_id = $1
		  AND confluence_page_id = ANY($2)`, kbID, ids)
	if err != nil {
		return nil, fmt.Errorf("query files: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var fileID string
		var confID *string
		if err := rows.Scan(&fileID, &confID); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if confID != nil {
			out[*confID] = fileID
		}
	}
	return out, rows.Err()
}
