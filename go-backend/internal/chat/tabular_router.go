// Package chat: the deterministic tabular router (design §5.1).
//
// TabularRouter turns a chat question into ONE validated, executed read-only
// SQL statement against the KB's materialized spreadsheet tables, and hands
// the answer LLM the result rows as a system-prompt addendum. It runs before
// retrieval on the standard and Supervisor paths, and it is fail-open by
// construction: every failure — a disabled flag, an unreachable catalog, a
// rejected statement, a DB error, an empty result — degrades to a non-fired
// or attempted-only result and never returns an error, so a broken tabular
// path can only cost a little latency, never a chat turn.
package chat

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/promptsafety"
	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/tabular/sqlcheck"
	"github.com/justrag/go-backend/internal/tabular/sqlexec"
)

// TabularCatalogReader is the slice of *tabular.Catalog the router needs.
type TabularCatalogReader interface {
	HasDataForKB(ctx context.Context, kbID string) (bool, error)
	ListByKB(ctx context.Context, kbID string) ([]tabular.CatalogEntry, error)
	LookupValues(ctx context.Context, entries []tabular.CatalogEntry, literals []string, perLiteral int) ([]tabular.ValueHit, error)
}

// TabularSQLGenerator is the LLM seam (ai.GenerateTabularSQL in production).
type TabularSQLGenerator func(ctx context.Context, req ai.TabularSQLRequest, kbID, model string) (ai.TabularSQLProposal, error)

// TabularRouterConfig is the router's resolved per-turn configuration.
// Enabled is the AND of the tabular master flag and the router flag; the
// caller resolves both (Task 5's cfgFn).
type TabularRouterConfig struct {
	Enabled         bool
	Model           string
	MaxRows         int
	MaxRepairs      int
	Timeout         time.Duration
	SchemaMaxTokens int
}

// TabularRouter is safe for concurrent use: its dependencies are stateless
// and the gate cache is a sync.Map.
type TabularRouter struct {
	cat   TabularCatalogReader
	exec  sqlexec.Executor
	gen   TabularSQLGenerator
	cfgFn func(ctx context.Context) TabularRouterConfig
	now   func() time.Time
	gate  *kbGateCache
}

// NewTabularRouter wires the router. Any nil dependency (catalog, executor,
// generator, config function) makes every Run skip with reason "disabled" —
// the deployment simply has no tabular SQL path (e.g. no read-only DSN).
func NewTabularRouter(cat TabularCatalogReader, exec sqlexec.Executor, gen TabularSQLGenerator, cfgFn func(context.Context) TabularRouterConfig) *TabularRouter {
	return &TabularRouter{
		cat:   cat,
		exec:  exec,
		gen:   gen,
		cfgFn: cfgFn,
		now:   time.Now,
		gate:  &kbGateCache{},
	}
}

// TabularRouterInput is one turn's input. Emit is optional (nil-safe) and
// receives raw trajectory-event maps.
type TabularRouterInput struct {
	KbID     string
	Query    string
	Language string
	Emit     func(map[string]any)
	// Config is the per-turn configuration resolved by the caller from the
	// reader that is actually in force for this KB. The chat handler
	// overlays its SiteConfigReader per KB (Handler.forKB), so a config
	// resolved once at wiring time — the router's own cfgFn, which closes
	// over the GLOBAL reader — would silently ignore a per-KB
	// `chat_tabular_router_enabled = false`. Nil falls back to cfgFn,
	// which is what callers without a reader (tests, eval) rely on.
	Config *TabularRouterConfig
}

// TabularRouterResult is what the two chat paths apply.
type TabularRouterResult struct {
	Fired          bool
	SearchQuery    string // input query with id literals quoted (always set)
	ForceSimpleArm bool   // true whenever the KB has >= 1 table
	Addendum       string // "" when nothing to inject
	Trace          *TabularTrace
}

// TabularTrace is persisted post-response (query log) and surfaced to eval.
type TabularTrace struct {
	Fired    bool
	Outcome  string // fired_ok | fired_empty | sql_error | llm_error | validator_rejected | cancelled | skipped_<reason>
	SQL      string
	RowCount int // -1 unknown
	Repairs  int
	Values   int
	Question string
	// Error is the last failure text on a non-success outcome (a validator
	// rejection message, a truncated DB error, an LLM-call error, or
	// ctx.Err() on a mid-repair cancellation) — already capped at
	// tabularFailureCap runes. Empty on fired_ok, on a skip, and on the
	// "model declared unanswerable" fired_empty (there is no failure text,
	// only a declined capability).
	Error string
}

// kbGateTTL bounds how long a HasDataForKB answer is reused. The gate runs
// on every chat turn of every KB, and the answer only changes when a
// spreadsheet is ingested or deleted; a minute of staleness costs at most
// one turn of a missing (or pointlessly attempted) tabular path.
const kbGateTTL = 60 * time.Second

type kbGateEntry struct {
	has bool
	at  time.Time
}

// kbGateCache caches HasDataForKB per KB. Concurrency-safe (sync.Map): chat
// turns of the same KB run in parallel.
type kbGateCache struct{ m sync.Map }

func (c *kbGateCache) has(ctx context.Context, cat TabularCatalogReader, kbID string, now time.Time) (bool, error) {
	if v, ok := c.m.Load(kbID); ok {
		if e, ok := v.(kbGateEntry); ok && now.Sub(e.at) < kbGateTTL {
			return e.has, nil
		}
	}
	has, err := cat.HasDataForKB(ctx, kbID)
	if err != nil {
		// Fail closed for this turn, and do not cache the failure.
		return false, err
	}
	c.m.Store(kbID, kbGateEntry{has: has, at: now})
	return has, nil
}

// tabularFailureCap bounds a DB or validator error message fed back to the repair prompt and the tabular_router_sql event.
const tabularFailureCap = 500

// tabularValueLookupPerLiteral is how many stored values one question
// literal may match.
const tabularValueLookupPerLiteral = 5

// failureKind classifies why an attempt did not produce a usable result; it
// selects the terminal outcome once the repair budget is spent.
type failureKind int

const (
	failureNone failureKind = iota
	failureValidator
	failureDB
	failureEmpty
)

// Run executes the router. It never returns an error and never panics on a
// nil dependency: every failure degrades to a non-fired or attempted-only
// result (§5.1 step 8).
func (r *TabularRouter) Run(ctx context.Context, in TabularRouterInput) TabularRouterResult {
	res := TabularRouterResult{
		SearchQuery: in.Query,
		Trace:       &TabularTrace{Question: in.Query, RowCount: -1},
	}
	if r == nil {
		res.Trace.Outcome = "skipped_disabled"
		return res
	}

	// Step 1: the flag decides before anything touches the database.
	// in.Config (resolved by the caller from the per-KB overlaid reader)
	// wins over the wiring-time cfgFn; see TabularRouterInput.Config.
	if r.cat == nil || r.exec == nil || r.gen == nil {
		return r.skip(in, res, "disabled")
	}
	var cfg TabularRouterConfig
	switch {
	case in.Config != nil:
		cfg = *in.Config
	case r.cfgFn != nil:
		cfg = r.cfgFn(ctx)
	default:
		return r.skip(in, res, "disabled")
	}
	if !cfg.Enabled {
		return r.skip(in, res, "disabled")
	}

	// Step 2: gate on "does this KB have tabular data at all", cached.
	has, err := r.gate.has(ctx, r.cat, in.KbID, r.now())
	if err != nil {
		return r.skip(in, res, "catalog_error")
	}
	if !has {
		return r.skip(in, res, "no_tables")
	}

	// Step 3: cues + retrieval hints. From here on the KB is known to have
	// tables, so the keyword arm is forced and id literals are quoted even
	// if the router itself does not fire.
	cues := DetectTabularCues(in.Query)
	res.SearchQuery = PromoteIdentifierPhrases(in.Query, cues.Literals)
	res.ForceSimpleArm = true

	// Step 4: catalog + stored-value lookup.
	allEntries, err := r.cat.ListByKB(ctx, in.KbID)
	if err != nil {
		return r.skip(in, res, "catalog_error")
	}
	entries := make([]tabular.CatalogEntry, 0, len(allEntries))
	for _, e := range allEntries {
		if e.SheetKind == "table" {
			entries = append(entries, e)
		}
	}

	hits, err := r.cat.LookupValues(ctx, entries, literalTexts(cues.Literals), tabularValueLookupPerLiteral)
	if err != nil {
		// A failed lookup is not fatal: the router can still write SQL from
		// the schema alone, it just has no verbatim values to pin.
		hits = nil
	}
	hits = dropInstructionHits(hits)

	// A quoted/span/id literal that matched a stored value fires the router
	// on its own (§5.1 cue 3) — that match IS the evidence that the question
	// is about a cell, even without an aggregation/filter cue.
	if !cues.Fired() && !anyHitFires(hits, cues.Literals) {
		return r.skip(in, res, "no_cue")
	}

	matched := matchedValueLines(in.Language, hits)
	res.Trace.Values = len(matched)

	// Step 5: the token-budgeted schema the generator writes against.
	schema := tabular.CompactSchema(entries, hits, in.Query, cfg.SchemaMaxTokens)
	if strings.TrimSpace(schema.Text) == "" {
		return r.skip(in, res, "schema_empty")
	}

	// R43 + R44: the gate, ListByKB, LookupValues and CompactSchema above do
	// real work, so the turn can already be dead by the time the loop is
	// reached. Check here as well as inside the loop: no SQL generation call
	// will happen, so this must NOT be reported as fired and must not emit
	// the fired/values events.
	if ctx.Err() != nil {
		return r.cancelled(in, res, ctx.Err().Error())
	}

	// R44: "fired" means an SQL generation call is actually made, so it is
	// set only once the schema check has passed — never on a skip.
	res.Fired = true
	res.Trace.Fired = true
	emitTabular(in, map[string]any{
		"type": "tabular_router_fired",
		"cues": map[string]any{
			"aggregation": cues.Aggregation,
			"filter":      cues.Filter,
			"literals":    len(cues.Literals),
		},
	})
	emitTabular(in, map[string]any{"type": "tabular_router_values", "matched": len(matched)})

	// Step 6/7: generate → validate → execute, with a bounded repair loop.
	req := ai.TabularSQLRequest{
		Lang:       in.Language,
		TodayISO:   r.now().Format("2006-01-02"),
		SchemaText: schema.Text,
		Matched:    matched,
		Question:   in.Query,
	}

	lastKind := failureNone
	var lastFailure string
	for round := 0; ; round++ {
		// R43: a cancelled turn (client disconnect, turn budget) must not
		// spend another LLM call or another DB round trip.
		if ctx.Err() != nil {
			return r.cancelled(in, res, ctx.Err().Error())
		}
		// Each round's result count stands on its own: a repaired attempt
		// must not inherit the previous attempt's row count.
		res.Trace.RowCount = -1

		prop, err := r.gen(ctx, req, in.KbID, cfg.Model)
		if err != nil {
			res.Trace.Outcome = "llm_error"
			res.Trace.Error = truncateRunes(err.Error(), tabularFailureCap)
			observability.RecordTabularRouter(res.Trace.Outcome)
			res.Addendum = r.attemptedOnly(in.Language)
			return res
		}
		if prop.SQL == nil {
			// The model declared the tables cannot answer the question.
			// Repairing that is asking it to guess, so this is terminal.
			res.Trace.Outcome = "fired_empty"
			observability.RecordTabularRouter(res.Trace.Outcome)
			res.Addendum = r.attemptedOnly(in.Language)
			emitTabular(in, map[string]any{"type": "tabular_router_skipped", "reason": "unanswerable"})
			return res
		}

		// Defence in depth for the Phase-4 acceptance failure: the schema
		// heading and both prompt blocks now spell out the two-identifier
		// relation form, but a model that writes `FROM "tabular.sheet_…"`
		// anyway is rewritten here rather than burning the whole repair
		// budget on a rejection it never recovered from (15 of 21 fired
		// questions, 3 repairs each, 0 recoveries). The rewrite is
		// token-level and never touches string literals or comments; from
		// here on `proposed` IS the statement — what is validated, what is
		// executed, what the trace/event reports, and what a repair round
		// is shown as the previous attempt.
		proposed := sqlcheck.NormalizeTabularRelations(*prop.SQL)
		res.Trace.SQL = proposed

		var failure string
		var kind failureKind

		execSQL, info, verr := sqlcheck.Validate(proposed, schema.AllowedTables, cfg.MaxRows)
		if verr != nil {
			failure, kind = truncateRunes(verr.Error(), tabularFailureCap), failureValidator
		} else {
			out, eerr := r.exec.Execute(ctx, execSQL, sqlexec.Options{
				Timeout: cfg.Timeout,
				RowCap:  cfg.MaxRows,
			})
			switch {
			case eerr != nil:
				failure, kind = truncateRunes(eerr.Error(), tabularFailureCap), failureDB
			case out == nil || len(out.Rows) == 0:
				res.Trace.RowCount = 0
				failure, kind = "0 rows", failureEmpty
			case info.IsAggregate && allNull(out):
				res.Trace.RowCount = out.RowCount
				failure, kind = "all aggregate values are NULL", failureEmpty
			default:
				// Success — a clean result is never re-reviewed.
				res.Trace.Outcome = "fired_ok"
				res.Trace.RowCount = out.RowCount
				// I1: Executor.Truncated is only set on an (RowCap+1)-th
				// row, but sqlcheck.Validate already wrapped the statement
				// to `LIMIT cfg.MaxRows` — so a result that exactly fills
				// the cap never trips Truncated even though more rows may
				// exist. RowCount hitting the cap is the same signal.
				capped := out.Truncated || out.RowCount >= cfg.MaxRows
				observability.RecordTabularRouter(res.Trace.Outcome)
				observability.RecordTabularRouterRows(out.RowCount)
				observability.RecordTabularRouterRepairs(res.Trace.Repairs)
				emitTabularSQLOutcome(in, res.Trace, "")
				emitTabular(in, map[string]any{
					"type":      "tabular_router_rows",
					"row_count": out.RowCount,
					"capped":    capped,
				})
				res.Addendum = prompts.TabularRouterAddendum(
					in.Language, proposed, out.Columns, out.Rows,
					out.RowCount, capped, false,
					tabularSources(in.Language, info.Tables, entries),
				)
				return res
			}
		}

		lastKind = kind
		lastFailure = failure
		if round >= cfg.MaxRepairs {
			break
		}
		res.Trace.Repairs++
		emitTabular(in, map[string]any{
			"type":    "tabular_router_repair",
			"round":   round + 1,
			"failure": failure,
		})
		req.PreviousSQL = proposed
		req.Failure = failure
	}

	// Repairs exhausted: tell the answer LLM the SQL path was tried so it
	// falls back to the retrieved context instead of inventing numbers.
	switch lastKind {
	case failureValidator:
		res.Trace.Outcome = "validator_rejected"
	case failureEmpty:
		res.Trace.Outcome = "fired_empty"
	default:
		res.Trace.Outcome = "sql_error"
	}
	observability.RecordTabularRouter(res.Trace.Outcome)
	observability.RecordTabularRouterRepairs(res.Trace.Repairs)
	emitTabularSQLOutcome(in, res.Trace, lastFailure)
	res.Addendum = r.attemptedOnly(in.Language)
	return res
}

// skip records a non-fired outcome, emits the skipped event and returns the
// result as-is — SearchQuery/ForceSimpleArm keep whatever the algorithm had
// already established (a skip after the gate still forces the keyword arm).
//
// M4 (R61): "disabled" is the one reason excluded from the event and the
// metric. It fires on every chat turn of every KB that has the feature (or
// this deployment's read-only DSN) turned off — i.e. potentially every
// turn, deployment-wide — so unlike every other skip reason (which needs a
// tabular KB to even reach it) it carries no diagnostic value proportional
// to its volume: an operator who wants to know whether the router is off
// already knows it from the config, not from a trajectory event or a
// counter increment on every message.
func (r *TabularRouter) skip(in TabularRouterInput, res TabularRouterResult, reason string) TabularRouterResult {
	res.Trace.Outcome = "skipped_" + reason
	if reason == "disabled" {
		return res
	}
	observability.RecordTabularRouter(res.Trace.Outcome)
	emitTabular(in, map[string]any{"type": "tabular_router_skipped", "reason": reason})
	return res
}

// cancelled records an abandoned turn (R43): the client is gone or the turn
// budget is spent, so no further LLM call and no further DB round trip is
// made. res.Fired is left exactly as the caller had it — false before the
// generation loop (no SQL call was made, R44), true once inside it. errText
// is ctx.Err().Error(); it only reaches the emitted event when a prior
// repair round already produced SQL (R67) — otherwise emitTabularSQLOutcome
// no-ops on the empty trace.SQL.
func (r *TabularRouter) cancelled(in TabularRouterInput, res TabularRouterResult, errText string) TabularRouterResult {
	res.Trace.Outcome = "cancelled"
	observability.RecordTabularRouter(res.Trace.Outcome)
	emitTabularSQLOutcome(in, res.Trace, errText)
	res.Addendum = r.attemptedOnly(in.Language)
	return res
}

// attemptedOnly renders the addendum that says "SQL was tried, use the
// retrieved context".
func (r *TabularRouter) attemptedOnly(lang string) string {
	return prompts.TabularRouterAddendum(lang, "", nil, nil, 0, false, true, nil)
}

func emitTabular(in TabularRouterInput, ev map[string]any) {
	if in.Emit != nil {
		in.Emit(ev)
	}
}

// emitTabularSQLOutcome emits tabular_router_sql exactly once per Run call,
// at trace finalisation, on every terminal outcome that actually produced a
// candidate SQL statement — success (fired_ok), a validator rejection, a DB
// error, repairs exhausted, or a mid-repair cancellation (R67). It is a
// no-op when trace.SQL is empty: llm_error and the "model declared
// unanswerable" fired_empty never reach a proposed statement, and a
// cancellation before the first repair round leaves trace.SQL unset too —
// in all of those cases there is no SQL to report.
//
// Only sql + outcome + error travel in the event, deliberately: never the
// prompt or the schema. errText is "" on success. The failure text passed
// in (a validator rejection message, a truncated DB error, ctx.Err()) is
// already sanitized the same way the repair prompt's own failure text is
// (tabularFailureCap truncates a raw DB error before it ever reaches here).
// It also persists onto trace.Error so a caller with only the returned
// TabularRouterResult (no access to the emitted event stream — e.g. eval's
// AgentTrace.Tabular) still sees the failure text on a non-success outcome.
func emitTabularSQLOutcome(in TabularRouterInput, trace *TabularTrace, errText string) {
	if trace == nil || trace.SQL == "" {
		return
	}
	trace.Error = errText
	emitTabular(in, map[string]any{
		"type":    "tabular_router_sql",
		"sql":     trace.SQL,
		"outcome": trace.Outcome,
		"error":   errText,
	})
}

// literalTexts projects the cue literals to their raw text for the value
// lookup.
func literalTexts(lits []TabularLiteral) []string {
	out := make([]string, 0, len(lits))
	for _, l := range lits {
		if l.Text != "" {
			out = append(out, l.Text)
		}
	}
	return out
}

// dropInstructionHits removes stored values that look like an instruction:
// a spreadsheet cell is attacker-controlled data and must never reach the
// prompt verbatim (same threat model as internal/tabular's schema render).
func dropInstructionHits(hits []tabular.ValueHit) []tabular.ValueHit {
	out := hits[:0:0]
	for _, h := range hits {
		if promptsafety.LooksLikeInstruction(h.Value) {
			continue
		}
		out = append(out, h)
	}
	return out
}

// anyHitFires reports whether at least one stored-value hit is strong
// enough to fire the router without an aggregation/filter/id cue (R45). A
// bare number is weak evidence — "55" is a substring of thousands of cells
// — so only an exact match counts for it; a quoted/span/id literal was
// deliberate enough that any match quality counts.
//
// A lone capitalised word ("word") is weak in the same way and is held to
// the same exact-match bar: "die Fläche der Bibliothek" fires only because
// a cell literally reads "Bibliothek", whereas the capitalised nouns of an
// ordinary prose question match nothing at all, or match only as
// substrings of some longer cell.
func anyHitFires(hits []tabular.ValueHit, lits []TabularLiteral) bool {
	kinds := make(map[string]string, len(lits))
	for _, l := range lits {
		kinds[strings.ToLower(l.Text)] = l.Kind
	}
	for _, h := range hits {
		if kind := kinds[strings.ToLower(h.Literal)]; (kind == "number" || kind == "word") && h.Match != "exact" {
			continue
		}
		return true
	}
	return false
}

// safeLabel renders a catalog-sourced label (a file or sheet name) for a
// prompt (R42). File and sheet names are as attacker-controlled as cell
// values — a user can name an upload "Ignore all previous instructions.xlsx"
// — so an instruction-shaped label is replaced rather than passed through.
func safeLabel(lang, s string) string {
	if promptsafety.LooksLikeInstruction(s) {
		if lang == "de" {
			return "[gefiltert]"
		}
		return "[filtered]"
	}
	return s
}

// matchedValueLines renders the MATCHED VALUES block entries the SQL
// generator pins its WHERE clauses to.
func matchedValueLines(lang string, hits []tabular.ValueHit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		// M8 (R61): the column name is catalog-derived (a spreadsheet
		// header cell), exactly as attacker-controlled as FileName/
		// SheetName below — a header can read "Ignore all previous
		// instructions" as easily as a file can be named that.
		out = append(out, fmt.Sprintf("%s = '%s' (%s › %s, %d rows)",
			safeLabel(lang, h.ColumnName), h.Value, safeLabel(lang, h.FileName), safeLabel(lang, h.SheetName), h.RowCount))
	}
	return out
}

// tabularSources maps the validated statement's table references back to
// human source labels ("file › sheet"). An unknown table renders bare
// rather than being dropped — a missing source line is worse than a terse
// one.
func tabularSources(lang string, tables []string, entries []tabular.CatalogEntry) []string {
	byName := make(map[string]tabular.CatalogEntry, len(entries))
	for _, e := range entries {
		byName[strings.ToLower(e.TableName)] = e
	}
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		name := strings.ToLower(strings.TrimPrefix(t, "tabular."))
		if e, ok := byName[name]; ok {
			out = append(out, safeLabel(lang, e.FileName)+" › "+safeLabel(lang, e.SheetName))
			continue
		}
		out = append(out, t)
	}
	return out
}

// allNull reports whether every cell of every row is NULL — an aggregate
// that returned only NULLs answered nothing, so it is a repair trigger.
func allNull(out *sqlexec.Result) bool {
	if out == nil || len(out.Rows) == 0 {
		return false
	}
	for _, row := range out.Rows {
		for _, v := range row {
			if v != nil {
				return false
			}
		}
	}
	return true
}
