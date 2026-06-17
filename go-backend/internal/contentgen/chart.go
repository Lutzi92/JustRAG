package contentgen

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/tabular"
)

// ChartCatalog is the narrow tabular-catalog read surface GenerateChart needs.
// Satisfied by *tabular.Catalog. ListByKB is KB-scoped, which is what enforces
// cross-KB isolation: a chart can only ever read tables of files in its own KB.
type ChartCatalog interface {
	ListByKB(ctx context.Context, kbID string) ([]tabular.CatalogEntry, error)
}

// ChartQuerier is the read-only SQL surface for the chart SQL path. Satisfied
// by *pgxpool.Pool (the SELECT-only sqlToolDB pool).
type ChartQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// SetChartDeps wires the optional tabular catalog + read-only query pool that
// enable the accurate SQL-over-tabular chart path. Both nil ⇒ GenerateChart
// always uses the LLM-from-context fallback.
func (h *Handler) SetChartDeps(cat ChartCatalog, ro ChartQuerier) {
	h.chartCatalog = cat
	h.chartRO = ro
}

// chartSpec is the stored content shape the frontend chart renderer consumes:
// {description, type, config:{xAxis, keys}, series:[...]}.
type chartSpec struct {
	Description string           `json:"description"`
	Type        string           `json:"type"`
	Config      chartConfig      `json:"config"`
	Series      []map[string]any `json:"series"`
}

type chartConfig struct {
	XAxis string   `json:"xAxis"`
	Keys  []string `json:"keys"`
}

// chartParams is the LLM's structured choice for the SQL path. The LLM picks
// columns + an aggregation (never raw SQL); the query is built deterministically
// from validated identifiers, so there is no SQL-injection surface.
type chartParams struct {
	ChartType   string   `json:"chartType"`
	XColumn     string   `json:"xColumn"`
	YColumns    []string `json:"yColumns"`
	Aggregation string   `json:"aggregation"` // sum|avg|min|max|count|none
	Description string   `json:"description"`
}

var allowedChartTypes = map[string]bool{"bar": true, "line": true, "area": true, "pie": true}
var allowedAggregations = map[string]bool{"sum": true, "avg": true, "min": true, "max": true, "count": true, "none": true}

// GenerateChart — POST /api/kb/{id}/generate/chart. Hybrid: when the selected
// file has materialized tabular tables and a read-only pool is configured, it
// builds an accurate chart from a deterministic SQL aggregation; otherwise (or
// on any SQL-path failure) it falls back to an LLM-generated spec from context.
func (h *Handler) GenerateChart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)

	caller := auth.UserFromContext(ctx)
	if caller == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req GenerateChartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Topic) == "" {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "topic is required")
		return
	}
	lang := defaultLang(req.Language)

	var spec *chartSpec
	if h.chartCatalog != nil && h.chartRO != nil && strings.TrimSpace(req.FileID) != "" {
		s, err := h.chartFromTabular(ctx, kbID, req, lang)
		if err != nil {
			// Non-fatal: log and fall back to the LLM path.
			slog.WarnContext(ctx, "chart SQL path failed, falling back to LLM", "error", err, "kb_id", kbID)
		} else if s != nil {
			spec = s
		}
	}
	if spec == nil {
		s, err := h.chartFromContext(ctx, kbID, req, lang)
		if err != nil {
			httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to generate chart")
			return
		}
		spec = s
	}

	title := fmt.Sprintf("Chart: %s", req.Topic)
	record, err := h.store.CreateGeneratedContent(ctx, kbID, caller.ID, "chart", title, spec)
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to save generated content")
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, record)
}

// chartFromTabular builds a chart from a deterministic SQL aggregation over the
// file's materialized table. Returns (nil, nil) when the file is not a tabular
// file in this KB (so the caller falls back to the LLM path).
func (h *Handler) chartFromTabular(ctx context.Context, kbID string, req GenerateChartRequest, lang string) (*chartSpec, error) {
	entries, err := h.chartCatalog.ListByKB(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("chart catalog: %w", err)
	}
	var entry *tabular.CatalogEntry
	for i := range entries {
		if entries[i].FileID == req.FileID && len(entries[i].Columns) > 0 {
			entry = &entries[i]
			break
		}
	}
	if entry == nil {
		return nil, nil // not a tabular file in this KB → fall back
	}

	params, err := h.chooseChartParams(ctx, kbID, req.Topic, entry, lang)
	if err != nil {
		return nil, err
	}

	sql, keys, err := buildChartSQL(entry, params)
	if err != nil {
		return nil, err
	}

	series, err := h.runChartQuery(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("chart query: %w", err)
	}
	if len(series) == 0 {
		return nil, fmt.Errorf("chart query returned no rows")
	}

	desc := strings.TrimSpace(params.Description)
	if desc == "" {
		desc = req.Topic
	}
	return &chartSpec{
		Description: desc,
		Type:        params.ChartType,
		Config:      chartConfig{XAxis: params.XColumn, Keys: keys},
		Series:      series,
	}, nil
}

// chooseChartParams asks the LLM to pick chart columns + aggregation given the
// table schema. It never returns SQL; buildChartSQL turns the choice into a
// validated query.
func (h *Handler) chooseChartParams(ctx context.Context, kbID, topic string, entry *tabular.CatalogEntry, lang string) (chartParams, error) {
	var schema strings.Builder
	for _, c := range entry.Columns {
		fmt.Fprintf(&schema, "- %s (%s)\n", c.Name, c.Type)
	}
	prompt := fmt.Sprintf("User request: %s\n\nTable columns:\n%s", topic, schema.String())
	raw, err := generateWithLLM(ctx, h.aiResolver, prompt, prompts.ChartParamsSystemPrompt(lang), kbID)
	if err != nil {
		return chartParams{}, err
	}
	var p chartParams
	if err := json.Unmarshal([]byte(stripCodeFence(strings.TrimSpace(raw))), &p); err != nil {
		return chartParams{}, fmt.Errorf("chart params parse: %w", err)
	}
	p.ChartType = strings.ToLower(strings.TrimSpace(p.ChartType))
	if !allowedChartTypes[p.ChartType] {
		p.ChartType = "bar"
	}
	p.Aggregation = strings.ToLower(strings.TrimSpace(p.Aggregation))
	if !allowedAggregations[p.Aggregation] {
		p.Aggregation = "sum"
	}
	return p, nil
}

// buildChartSQL turns validated chart params into a read-only aggregation query.
// All identifiers are validated against the table's actual columns and quoted,
// so the only inputs that reach SQL are known-good identifiers + a fixed set of
// aggregate keywords. Aggregates are cast to double precision so pgx returns
// float64 (not pgtype.Numeric) for clean JSON.
func buildChartSQL(entry *tabular.CatalogEntry, p chartParams) (sql string, keys []string, err error) {
	cols := make(map[string]tabular.ColumnType, len(entry.Columns))
	for _, c := range entry.Columns {
		cols[c.Name] = c.Type
	}
	if _, ok := cols[p.XColumn]; !ok {
		return "", nil, fmt.Errorf("xColumn %q not in table", p.XColumn)
	}
	table := quoteIdent(tabular.TabularSchema) + "." + quoteIdent(entry.TableName)

	if p.Aggregation == "count" {
		// Count rows per x; yColumns are irrelevant.
		return fmt.Sprintf(
			`SELECT %s, COUNT(*)::double precision AS "count" FROM %s WHERE %s IS NOT NULL GROUP BY %s ORDER BY 2 DESC LIMIT 50`,
			quoteIdent(p.XColumn), table, quoteIdent(p.XColumn), quoteIdent(p.XColumn),
		), []string{"count"}, nil
	}

	// Keep only numeric y columns that actually exist.
	for _, y := range p.YColumns {
		if t, ok := cols[y]; ok && (t == tabular.TypeBigint || t == tabular.TypeFloat) {
			keys = append(keys, y)
		}
	}
	if len(keys) == 0 {
		return "", nil, fmt.Errorf("no numeric yColumns among %v", p.YColumns)
	}

	sel := []string{quoteIdent(p.XColumn)}
	if p.Aggregation == "none" {
		for _, y := range keys {
			sel = append(sel, quoteIdent(y))
		}
		return fmt.Sprintf(
			`SELECT %s FROM %s WHERE %s IS NOT NULL LIMIT 200`,
			strings.Join(sel, ", "), table, quoteIdent(p.XColumn),
		), keys, nil
	}

	agg := strings.ToUpper(p.Aggregation) // SUM|AVG|MIN|MAX
	for _, y := range keys {
		sel = append(sel, fmt.Sprintf(`%s(%s)::double precision AS %s`, agg, quoteIdent(y), quoteIdent(y)))
	}
	return fmt.Sprintf(
		`SELECT %s FROM %s WHERE %s IS NOT NULL GROUP BY %s ORDER BY %s LIMIT 100`,
		strings.Join(sel, ", "), table, quoteIdent(p.XColumn), quoteIdent(p.XColumn), quoteIdent(p.XColumn),
	), keys, nil
}

// quoteIdent double-quotes a SQL identifier, escaping embedded quotes.
func quoteIdent(id string) string {
	return `"` + strings.ReplaceAll(id, `"`, `""`) + `"`
}

// runChartQuery executes the (already-validated) SQL on the read-only pool and
// returns each row as a column→value map suitable for JSON / recharts.
func (h *Handler) runChartQuery(ctx context.Context, sql string) ([]map[string]any, error) {
	rows, err := h.chartRO.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	var out []map[string]any
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		m := make(map[string]any, len(fields))
		for i, f := range fields {
			m[f.Name] = normalizeCell(vals[i])
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// normalizeCell converts pgx-scanned values into JSON/recharts-friendly forms.
// Dates render as ISO strings; everything else (float64/int64/string/bool)
// already serialises cleanly.
func normalizeCell(v any) any {
	if t, ok := v.(time.Time); ok {
		return t.Format("2006-01-02")
	}
	return v
}

// chartFromContext is the universal fallback: the LLM emits the full chart spec
// (including the data points) from retrieved context. Used when the file is not
// tabular, the read-only pool is unconfigured, or the SQL path errored.
func (h *Handler) chartFromContext(ctx context.Context, kbID string, req GenerateChartRequest, lang string) (*chartSpec, error) {
	var contextStr string
	var err error
	if strings.TrimSpace(req.FileID) != "" {
		contextStr, err = getContextForFile(ctx, h.searchSvc, kbID, req.FileID, req.Topic, 25)
	} else {
		contextStr, err = getContext(ctx, h.searchSvc, kbID, req.Topic, 25)
	}
	if err != nil {
		return nil, fmt.Errorf("chart context: %w", err)
	}

	prompt := fmt.Sprintf("Chart request: %s\n\nContext:\n%s", req.Topic, contextStr)
	raw, err := generateWithLLM(ctx, h.aiResolver, prompt, prompts.ChartContextSystemPrompt(lang), kbID)
	if err != nil {
		return nil, err
	}
	var spec chartSpec
	if err := json.Unmarshal([]byte(stripCodeFence(strings.TrimSpace(raw))), &spec); err != nil {
		return nil, fmt.Errorf("chart spec parse: %w", err)
	}
	spec.Type = strings.ToLower(strings.TrimSpace(spec.Type))
	if !allowedChartTypes[spec.Type] {
		spec.Type = "bar"
	}
	if spec.Config.XAxis == "" || len(spec.Config.Keys) == 0 || len(spec.Series) == 0 {
		return nil, fmt.Errorf("chart spec incomplete")
	}
	if strings.TrimSpace(spec.Description) == "" {
		spec.Description = req.Topic
	}
	return &spec, nil
}
