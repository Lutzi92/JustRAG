//go:build integration

// End-to-end test of the deterministic tabular router (design §5.1) against
// a real materialized catalog: ingest header_row14_metadata.xlsx for real
// (Materialize: true, no LLM), then run chat.TabularRouter with a real
// tabular.Catalog and a real sqlexec.NewReadOnly executor — only the SQL
// generation LLM call is stubbed, per the task-11 brief.
//
// This file is package ingest_test (a black-box test package), NOT package
// ingest like ingest_integration_test.go. internal/chat already imports
// internal/parser -> internal/tabular/ingest (parser/spreadsheet.go renders
// through the ingest+profiler in render-only mode), so a package-ingest test
// file importing internal/chat would be an import cycle
// (ingest -> chat -> parser -> ingest). A black-box _test package sits
// outside that cycle: it imports both ingest and chat, and nothing imports
// it back. The cost is that openMainPool/seedFile from
// ingest_integration_test.go are unexported and so cannot be reused across
// the package boundary; they are duplicated here, mirroring the same
// duplication ingest_integration_test.go itself does relative to
// internal/tabular's materializer_integration_test.go (see that file's
// header comment) and sqlexec's own exec_integration_test.go.
package ingest_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/tabular/ingest"
	"github.com/justrag/go-backend/internal/tabular/sqlexec"
)

// fixtures mirrors ingest_integration_test.go's unexported constant of the
// same name (duplicated, not imported — see the package-level comment).
const fixtures = "../../sheetsource/testdata/"

func openMainPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("tabular router integration tests require DB_* env (main Postgres)")
	}
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		url.QueryEscape(os.Getenv("DB_USER")), url.QueryEscape(os.Getenv("DB_PASSWORD")), host, port, name)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedFile seeds a user + KB + file row so tabular_catalog's FKs resolve,
// and registers cleanup for all three.
func seedFile(t *testing.T, pool *pgxpool.Pool, fileName string) (fileID, kbID string) {
	t.Helper()
	ctx := context.Background()
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1,'x') RETURNING id::text`,
		fmt.Sprintf("tab-rtr-%d-%s", os.Getpid(), t.Name())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) })
	if err := pool.QueryRow(ctx, `INSERT INTO knowledge_bases (name, user_id) VALUES ('tab-rtr', $1) RETURNING id::text`, userID).Scan(&kbID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO files (kb_id, name, type, status) VALUES ($1,$2,'application/xlsx','completed') RETURNING id::text`,
		kbID, fileName).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	return fileID, kbID
}

// baseRouterConfig is the static per-turn config every case starts from;
// individual cases override fields (MaxRepairs for the one-shot rejection
// case) via TabularRouterInput.Config.
func baseRouterConfig() chat.TabularRouterConfig {
	return chat.TabularRouterConfig{
		Enabled:         true,
		Model:           "stub-model",
		MaxRows:         200,
		MaxRepairs:      3,
		Timeout:         5 * time.Second,
		SchemaMaxTokens: 12000,
	}
}

// sqlGen adapts a plain func(round int) string into a chat.TabularSQLGenerator:
// round 0 is the first proposal, round 1 the first repair, and so on.
func sqlGen(byRound func(round int) string) chat.TabularSQLGenerator {
	round := 0
	return func(_ context.Context, _ ai.TabularSQLRequest, _ string, _ string) (ai.TabularSQLProposal, error) {
		sql := byRound(round)
		round++
		return ai.TabularSQLProposal{SQL: &sql, Rationale: "stub", Confidence: 0.9}, nil
	}
}

// TestRouterEndToEnd ingests header_row14_metadata.xlsx for real, then
// drives chat.TabularRouter against the real catalog + read-only executor
// through five cases: a clean fired_ok count, a repair round, a rejected
// out-of-KB table (the mutation from the brief's self-review notes), a
// direct LookupValues assertion, and numeric-as-decimal-string rendering
// (R47) inside the addendum.
func TestRouterEndToEnd(t *testing.T) {
	ctx := context.Background()
	pool := openMainPool(t)
	fileID, kbID := seedFile(t, pool, "header_row14_metadata.xlsx")
	mat := tabular.NewMaterializer(pool)
	// Registered right after the materialiser is created (task-10 review
	// lesson: any t.Fatal between here and an explicit drop must not leak
	// this file's tabular.sheet_* tables and tabular_column_values rows).
	t.Cleanup(func() { _ = mat.DropTablesForFile(context.Background(), fileID) })

	g := ingest.New(mat, nil)
	if _, err := g.Ingest(ctx, ingest.Input{
		FilePath: fixtures + "header_row14_metadata.xlsx",
		FileName: "header_row14_metadata.xlsx",
		FileID:   fileID,
		KBID:     kbID,
		Options:  ingest.Options{Materialize: true, SampleRows: 200, ChunkSize: 512},
	}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	cat := tabular.NewCatalog(pool)
	entries, err := cat.ListByFile(ctx, fileID)
	if err != nil {
		t.Fatal(err)
	}
	var gebaeude tabular.CatalogEntry
	found := false
	for _, e := range entries {
		if e.SheetKind == "table" {
			gebaeude, found = e, true
			break
		}
	}
	if !found {
		t.Fatalf("no table-kind catalog entry among %+v", entries)
	}
	table := gebaeude.TableName

	exec := sqlexec.NewReadOnly(pool)

	// Direct count, independent of the router, that every fired case's
	// addendum is checked against.
	var wantCount int
	if err := pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT count(*) FROM tabular.%q WHERE "stammdaten_denkmalschutz" = 'Nein'`, table),
	).Scan(&wantCount); err != nil {
		t.Fatal(err)
	}
	if wantCount == 0 {
		t.Fatal("wantCount = 0; fixture assumption (some rows have stammdaten_denkmalschutz = 'Nein') no longer holds")
	}
	t.Logf("table=%s wantCount(stammdaten_denkmalschutz='Nein')=%d", table, wantCount)

	t.Run("fired_ok direct count", func(t *testing.T) {
		gen := sqlGen(func(int) string {
			return fmt.Sprintf(`SELECT COUNT(*) AS n FROM tabular.%q WHERE "stammdaten_denkmalschutz" = 'Nein'`, table)
		})
		cfg := baseRouterConfig()
		router := chat.NewTabularRouter(cat, exec, gen, func(context.Context) chat.TabularRouterConfig { return cfg })

		res := router.Run(ctx, chat.TabularRouterInput{
			KbID: kbID, Query: "Wie viele Gebäude haben keinen Denkmalschutz?", Language: "de",
		})
		if !res.Fired {
			t.Fatalf("Fired = false, trace = %+v", res.Trace)
		}
		if res.Trace.Outcome != "fired_ok" {
			t.Fatalf("Outcome = %q, want fired_ok (trace = %+v)", res.Trace.Outcome, res.Trace)
		}
		if res.Trace.RowCount != 1 {
			t.Errorf("RowCount = %d, want 1 (one aggregate row)", res.Trace.RowCount)
		}
		want := fmt.Sprintf("- n: %d", wantCount)
		if !strings.Contains(res.Addendum, want) {
			t.Errorf("addendum = %q, want to contain %q", res.Addendum, want)
		}
	})

	t.Run("repair then fired_ok", func(t *testing.T) {
		gen := sqlGen(func(round int) string {
			if round == 0 {
				// Wrong column: passes AST validation (sqlcheck knows
				// nothing about real columns) but fails at Execute with a
				// real "column does not exist" error.
				return fmt.Sprintf(`SELECT COUNT(*) AS n FROM tabular.%q WHERE "does_not_exist_col" = 'Nein'`, table)
			}
			return fmt.Sprintf(`SELECT COUNT(*) AS n FROM tabular.%q WHERE "stammdaten_denkmalschutz" = 'Nein'`, table)
		})
		cfg := baseRouterConfig()
		router := chat.NewTabularRouter(cat, exec, gen, func(context.Context) chat.TabularRouterConfig { return cfg })

		res := router.Run(ctx, chat.TabularRouterInput{
			KbID: kbID, Query: "Wie viele Gebäude haben keinen Denkmalschutz?", Language: "de",
		})
		if !res.Fired {
			t.Fatalf("Fired = false, trace = %+v", res.Trace)
		}
		if res.Trace.Outcome != "fired_ok" {
			t.Fatalf("Outcome = %q, want fired_ok after one repair (trace = %+v)", res.Trace.Outcome, res.Trace)
		}
		if res.Trace.Repairs != 1 {
			t.Errorf("Repairs = %d, want 1", res.Trace.Repairs)
		}
		want := fmt.Sprintf("- n: %d", wantCount)
		if !strings.Contains(res.Addendum, want) {
			t.Errorf("addendum = %q, want to contain %q", res.Addendum, want)
		}
	})

	t.Run("validator rejects a table outside the KB", func(t *testing.T) {
		// The mutation from the brief's self-review notes: point the
		// generator at a table name that is not in this KB's
		// AllowedTables set (schema.CompactSchema builds AllowedTables
		// solely from cat.ListByKB(kbID), so any name not among this KB's
		// own materialized tables is rejected at the AST-validation stage
		// without ever reaching the database — sqlcheck.Validate does not
		// itself consult Postgres). MaxRepairs: 0 makes this a one-shot:
		// a single call to gen, no repair round.
		gen := sqlGen(func(int) string {
			return `SELECT COUNT(*) AS n FROM tabular."sheet_zz99_0_0"`
		})
		cfg := baseRouterConfig()
		cfg.MaxRepairs = 0
		// cfgFn deliberately returns the un-overridden base config (MaxRepairs
		// 3); Input.Config below is what actually governs this call, proving
		// the per-turn override wins over the wiring-time cfgFn.
		router := chat.NewTabularRouter(cat, exec, gen, func(context.Context) chat.TabularRouterConfig { return baseRouterConfig() })

		res := router.Run(ctx, chat.TabularRouterInput{
			KbID: kbID, Query: "Wie viele Gebäude haben keinen Denkmalschutz?", Language: "de",
			Config: &cfg,
		})
		if !res.Fired {
			t.Fatalf("Fired = false, trace = %+v (an SQL generation call IS made, just rejected)", res.Trace)
		}
		if res.Trace.Outcome != "validator_rejected" {
			t.Fatalf("Outcome = %q, want validator_rejected (trace = %+v)", res.Trace.Outcome, res.Trace)
		}
		if res.Trace.Repairs != 0 {
			t.Errorf("Repairs = %d, want 0 (MaxRepairs=0, one-shot)", res.Trace.Repairs)
		}
	})

	t.Run("LookupValues exact hit on the real value index", func(t *testing.T) {
		hits, err := cat.LookupValues(ctx, []tabular.CatalogEntry{gebaeude}, []string{"nein"}, 5)
		if err != nil {
			t.Fatalf("LookupValues: %v", err)
		}
		var hit *tabular.ValueHit
		for i := range hits {
			if hits[i].ColumnName == "stammdaten_denkmalschutz" {
				hit = &hits[i]
				break
			}
		}
		if hit == nil {
			t.Fatalf("no hit for stammdaten_denkmalschutz among %+v", hits)
		}
		if hit.Value != "Nein" {
			t.Errorf("Value = %q, want %q", hit.Value, "Nein")
		}
		if hit.Match != "exact" {
			t.Errorf("Match = %q, want exact", hit.Match)
		}
		if hit.RowCount != int64(wantCount) {
			t.Errorf("RowCount = %d, want %d (must equal the direct COUNT(*))", hit.RowCount, wantCount)
		}
	})

	t.Run("numeric column renders as a decimal string (R47)", func(t *testing.T) {
		var wantVal string
		if err := pool.QueryRow(ctx,
			fmt.Sprintf(`SELECT "stammdaten_bgf_m2"::text FROM tabular.%q WHERE "_rowid"=1`, table),
		).Scan(&wantVal); err != nil {
			t.Fatal(err)
		}
		t.Logf("stammdaten_bgf_m2 at _rowid=1, ::text cast = %q", wantVal)

		gen := sqlGen(func(int) string {
			return fmt.Sprintf(`SELECT "stammdaten_bgf_m2" AS v FROM tabular.%q WHERE "_rowid"=1 LIMIT 1`, table)
		})
		cfg := baseRouterConfig()
		router := chat.NewTabularRouter(cat, exec, gen, func(context.Context) chat.TabularRouterConfig { return cfg })

		res := router.Run(ctx, chat.TabularRouterInput{
			KbID: kbID, Query: "Wie viele Quadratmeter BGF hat Gebäude 1?", Language: "de",
		})
		if !res.Fired || res.Trace.Outcome != "fired_ok" {
			t.Fatalf("Fired=%v Outcome=%q, want Fired=true Outcome=fired_ok (trace = %+v)", res.Fired, res.Trace.Outcome, res.Trace)
		}
		want := "- v: " + wantVal
		if !strings.Contains(res.Addendum, want) {
			t.Errorf("addendum = %q, want to contain %q", res.Addendum, want)
		}
		// Defensive: a Go float64 artefact (exponent notation like "1.23e+04",
		// or a pgtype.Numeric{Int,Exp} struct dump like "{Int:... Exp:...}")
		// must never reach the rendered value — only sqlexec's
		// canonical-decimal-string normalisation (R47). wantVal itself is
		// Postgres's own ::text cast, so if it happened to be exponential
		// this check would be wrong to apply; NUMERIC's ::text cast never
		// produces exponent notation, so that can't happen here.
		if strings.Contains(want, "e+") || strings.Contains(want, "{Int") {
			t.Fatalf("test bug: wantVal itself looks non-canonical: %q", wantVal)
		}
	})
}
