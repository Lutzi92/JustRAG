package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/database"
	"github.com/justrag/go-backend/internal/vector"
)

// --print-keyword-sql: print the keyword arm's SQL for one query, in BOTH
// scoring modes, and exit — no golden set, no search, no LLM call.
//
// Added for the Wave-3 Task-7 scale check: measuring what the keyword-arm
// candidate CTE costs at 100k chunks means running EXPLAIN (ANALYZE, BUFFERS)
// on the statement production actually sends, and that statement is assembled
// from a dozen resolved inputs (KB language, simple arm, tiered boost, dim-
// keyed stats tables, k1/b). Retyping it by hand would measure a statement
// that drifts from the builders the next time they change.

// validateKeywordSQLFlags checks the flag combination for the diagnostic
// mode. The query itself is the caller's business (an empty-match query is a
// legitimate thing to inspect — the renderer reports ok=false for it), but a
// missing --kb-id has no sensible default: the KB decides the chunk table,
// the text-search config and the stats tables.
func validateKeywordSQLFlags(query, kbID string) error {
	if kbID == "" {
		return fmt.Errorf("--print-keyword-sql requires --kb-id")
	}
	return nil
}

// writeKeywordSQLJSON emits the diagnostics as ONE indented JSON document.
// Machine-readable on purpose: eval/fixtures/bm25-scale/time-keyword-sql.sh
// parses it and feeds each mode's executable_sql to EXPLAIN.
func writeKeywordSQLJSON(w io.Writer, diag vector.KeywordSQLDiagnostics) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(diag)
}

// runPrintKeywordSQL connects to the configured databases, resolves the KB's
// keyword-arm settings, renders both builders' SQL and writes the JSON
// document to out. Returns an error rather than exiting so main() owns the
// exit code.
//
// Logging is re-pointed at stderr for the duration: main() sends slog to
// stdout, and this mode's contract is that stdout carries the JSON document
// and nothing else.
func runPrintKeywordSQL(query, kbID string, limit int, out io.Writer) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config load: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	db, err := database.Connect(ctx, cfg.DB, cfg.VectorDB)
	if err != nil {
		return fmt.Errorf("database connect: %w", err)
	}
	defer db.Main.Close()
	if db.Vector != db.Main {
		defer db.Vector.Close()
	}

	svc := vector.NewSearchService(db.Vector, db.Main, ai.NewConfigResolver(ai.NewStore(db.Main)),
		vector.WithSiteConfigReader(chat.NewStore(db.Main)),
		// resolveKBChunkTable probes the dim tables through the chunk
		// service; without it the KB's table cannot be discovered.
		vector.WithChunkService(vector.NewChunkService(db.Vector)),
	)

	diag, err := svc.RenderKeywordArmSQL(ctx, kbID, query, limit)
	if err != nil {
		return err
	}
	return writeKeywordSQLJSON(out, diag)
}
