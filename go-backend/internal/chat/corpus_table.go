package chat

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/vector"
)

// CorpusChunkReader returns every chunk of a file's text. Satisfied by
// *vector.ChunkService. Narrow interface so the chat Handler stays
// test-stubbable.
type CorpusChunkReader interface {
	GetChunksByFileID(ctx context.Context, kbID, fileID string, dimensions int) ([]vector.FileChunkRow, error)
}

// CorpusTableParams configures one corpus-table turn.
type CorpusTableParams struct {
	KbID           string
	Query          string
	Language       string
	FileIDs        []string
	KbSystemPrompt string
	Model          string
	MaxFiles       int
	Concurrency    int
}

type scopeFile struct {
	FileID   string
	FileName string
	Score    float64
}

// RunCorpusTableChat executes the map-reduce corpus-table path and returns a
// ChatContext whose SystemPrompt carries the compact assembled table for the
// narrative pass, plus the populated StructuredTable. emit streams progress
// ({"tableProgress":{done,total}}).
func RunCorpusTableChat(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	searchSvc vector.Searcher,
	chunkReader CorpusChunkReader,
	params CorpusTableParams,
	emit func(map[string]any),
) (*ChatContext, error) {
	observability.RecordCorpusTableStart()

	opts := vector.SearchOptions{QueryType: vector.QueryTypeComplexReasoning, FileIDs: params.FileIDs}
	result, err := searchSvc.Search(ctx, params.KbID, params.Query, params.MaxFiles*4, opts)
	if err != nil {
		return nil, fmt.Errorf("corpus_table: scope search: %w", err)
	}
	files, total := selectScopeFiles(result.Chunks, params.MaxFiles)
	if len(files) == 0 {
		return nil, fmt.Errorf("corpus_table: no in-scope files")
	}
	dims := dimensionsFromTableName(result.TableName)
	truncated := total > len(files)

	sample := buildColumnSample(ctx, chunkReader, params.KbID, files, dims)
	planned := ai.PlanCorpusColumns(ctx, aiResolver, params.Query, sample, params.KbID, params.Language, params.Model)
	userCols := parseUserStatedColumns(params.Query)
	cols := ai.MergeUserColumns(planned, userCols)

	var extractCols []ai.CorpusColumn
	for _, c := range cols {
		if c.DeriveFrom == "" {
			extractCols = append(extractCols, c)
		}
	}

	rows := make([]TableRow, len(files))
	concurrency := params.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	doneCount := 0
	for i, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		// Go 1.22+ per-iteration loop vars: capturing i, f directly is safe.
		// safego.GoCtx takes (ctx context.Context, fn func()) — use it so
		// panic recoveries carry the request-scoped logger fields.
		iCap := i
		fCap := f
		safego.GoCtx(ctx, func() {
			defer wg.Done()
			defer func() { <-sem }()
			text := assembleFileText(ctx, chunkReader, params.KbID, fCap.FileID, dims)
			vals := ai.ExtractCorpusRow(ctx, aiResolver, params.Query, fCap.FileName, text, extractCols, params.KbID, params.Language, params.Model)
			row := TableRow{"_file_id": fCap.FileID, "_file_name": fCap.FileName, "_source_idx": iCap + 1}
			for k, v := range vals {
				row[k] = v
			}
			for _, c := range cols {
				if c.DeriveFrom == "filename" {
					row[c.Key] = deriveFarmIDFromFilename(fCap.FileName)
				}
			}
			rows[iCap] = row
			mu.Lock()
			doneCount++
			emit(map[string]any{"tableProgress": map[string]any{"done": doneCount, "total": len(files)}})
			mu.Unlock()
		})
	}
	wg.Wait()

	cleanRows := make([]TableRow, 0, len(rows))
	for _, r := range rows {
		if r != nil {
			cleanRows = append(cleanRows, r)
		}
	}

	tbl := &StructuredTable{
		Columns:    toTableColumns(cols),
		Rows:       cleanRows,
		Truncated:  truncated,
		TotalFiles: total,
	}

	sources := make([]ChatSource, len(files))
	for i, f := range files {
		sources[i] = ChatSource{Index: i + 1, FileID: f.FileID, FileName: f.FileName}
	}
	tableMD := renderTableMarkdown(tbl)
	var sb strings.Builder
	if params.KbSystemPrompt != "" {
		sb.WriteString(params.KbSystemPrompt)
		sb.WriteString("\n\n")
	}
	sb.WriteString(prompts.CorpusTableNarrativeSystem(params.Language))
	sb.WriteString("\n\nASSEMBLED COMPARISON TABLE (authoritative — do not invent rows beyond it):\n")
	sb.WriteString(tableMD)
	if truncated {
		sb.WriteString(fmt.Sprintf("\n\nNote: %d files matched but only the top %d are shown (truncated).", total, len(files)))
	}

	logctx.From(ctx).Info("rag.corpus_table.done",
		"files", len(files), "total", total, "truncated", truncated,
		"columns", len(cols), "extract_columns", len(extractCols))
	observability.RecordCorpusTableComplete(len(files))

	return &ChatContext{
		SystemPrompt:    sb.String(),
		Sources:         sources,
		Context:         tableMD,
		StructuredTable: tbl,
		FinalChunks:     result.Chunks,
	}, nil
}

func selectScopeFiles(chunks []vector.SearchChunk, maxFiles int) ([]scopeFile, int) {
	best := map[string]scopeFile{}
	var order []string
	for _, c := range chunks {
		if c.FileID == "" {
			continue
		}
		if cur, ok := best[c.FileID]; !ok {
			best[c.FileID] = scopeFile{FileID: c.FileID, FileName: c.FileName, Score: c.Score}
			order = append(order, c.FileID)
		} else if c.Score > cur.Score {
			cur.Score = c.Score
			best[c.FileID] = cur
		}
	}
	files := make([]scopeFile, 0, len(order))
	for _, id := range order {
		files = append(files, best[id])
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Score > files[j].Score })
	total := len(files)
	if maxFiles > 0 && len(files) > maxFiles {
		files = files[:maxFiles]
	}
	return files, total
}

func dimensionsFromTableName(name string) int {
	i := strings.LastIndex(name, "_")
	if i < 0 || i == len(name)-1 {
		return 0
	}
	n, err := strconv.Atoi(name[i+1:])
	if err != nil {
		return 0
	}
	return n
}

func assembleFileText(ctx context.Context, r CorpusChunkReader, kbID, fileID string, dims int) string {
	rowsC, err := r.GetChunksByFileID(ctx, kbID, fileID, dims)
	if err != nil {
		logctx.From(ctx).Warn("corpus_table: get chunks failed", "fileId", fileID, "error", err)
		return ""
	}
	var sb strings.Builder
	for _, c := range rowsC {
		if p := strings.TrimSpace(c.ContextualPrefix); p != "" {
			sb.WriteString(p)
			sb.WriteString("\n")
		}
		sb.WriteString(c.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

func buildColumnSample(ctx context.Context, r CorpusChunkReader, kbID string, files []scopeFile, dims int) string {
	var sb strings.Builder
	n := 3
	if len(files) < n {
		n = len(files)
	}
	for i := 0; i < n; i++ {
		t := assembleFileText(ctx, r, kbID, files[i].FileID, dims)
		if r := []rune(t); len(r) > 600 {
			t = string(r[:600])
		}
		sb.WriteString("### " + files[i].FileName + "\n" + t + "\n\n")
	}
	return sb.String()
}

func toTableColumns(cols []ai.CorpusColumn) []TableColumn {
	out := make([]TableColumn, len(cols))
	for i, c := range cols {
		out[i] = TableColumn{Key: c.Key, Label: c.Label, Type: c.Type, DeriveFrom: c.DeriveFrom}
	}
	return out
}

func escapeTableCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}

func renderTableMarkdown(t *StructuredTable) string {
	var sb strings.Builder
	for i, c := range t.Columns {
		if i > 0 {
			sb.WriteString(" | ")
		}
		sb.WriteString(escapeTableCell(c.Label))
	}
	sb.WriteString("\n")
	for i := range t.Columns {
		if i > 0 {
			sb.WriteString(" | ")
		}
		sb.WriteString("---")
	}
	sb.WriteString("\n")
	for _, row := range t.Rows {
		for i, c := range t.Columns {
			if i > 0 {
				sb.WriteString(" | ")
			}
			sb.WriteString(escapeTableCell(fmt.Sprintf("%v", row[c.Key])))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// parseUserStatedColumns extracts columns the user explicitly demanded. v1
// recognises a filename-derived ID column ("add a column with farm IDs … from
// the file names"); richer NL column parsing is a follow-up.
func parseUserStatedColumns(query string) []ai.CorpusColumn {
	q := strings.ToLower(query)
	wantsID := strings.Contains(q, "from the file name") || strings.Contains(q, "from the filename") ||
		strings.Contains(q, "aus dem dateinamen") || strings.Contains(q, "aus den dateinamen")
	mentionsID := strings.Contains(q, "id")
	if wantsID && mentionsID {
		return []ai.CorpusColumn{{Key: "id", Label: "ID", Type: "string", DeriveFrom: "filename"}}
	}
	return nil
}
