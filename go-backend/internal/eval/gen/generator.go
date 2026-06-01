package gen

import (
	"context"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/vector"
)

// question is a type alias for eval.Question so internal helper signatures
// stay concise while remaining identical to the external type.
type question = eval.Question

// FileRef is one file in the KB corpus.
type FileRef struct {
	ID   string
	Name string
}

// Neighbor is a cross-file vector neighbor for multi-hop pairing.
type Neighbor struct {
	FileID    string
	FileName  string
	ChunkText string
	Score     float64
}

// corpusReader abstracts listing KB files and fetching their chunks.
type corpusReader interface {
	ListFiles(ctx context.Context, kbID string) ([]FileRef, error)
	GetChunks(ctx context.Context, kbID, fileID string) ([]vector.FileChunkRow, error)
}

// questionGen abstracts all LLM-backed question-generation calls.
type questionGen interface {
	LookupQuestions(ctx context.Context, fileName, document, chunk string, n int) ([]string, error)
	ComplexQuestion(ctx context.Context, fileName, document, chunk string) (string, error)
	EnumerationQuestion(ctx context.Context, fileName string, chunks []string) (string, error)
	BridgeQuestion(ctx context.Context, aName, aText, bName, bText string) (string, error)
}

// neighborFinder finds the nearest chunk from a different file in the KB,
// used for multi-hop / bridge question generation.
type neighborFinder interface {
	NearestOtherFile(ctx context.Context, kbID, seedFileID, seedChunkText string) (Neighbor, bool, error)
}

// Generator drives the generation modes over the injected dependencies.
type Generator struct {
	corpus    corpusReader
	qgen      questionGen
	neighbors neighborFinder
	// simFloor is the minimum cosine similarity score required from
	// neighborFinder before a bridge pair is accepted.
	simFloor float64
}

// New constructs a Generator. simFloor is the minimum neighbor similarity
// for bridge questions; 0 accepts all neighbors.
func New(corpus corpusReader, qgen questionGen, neighbors neighborFinder, simFloor float64) *Generator {
	return &Generator{corpus: corpus, qgen: qgen, neighbors: neighbors, simFloor: simFloor}
}

// minQuestionLen is the minimum rune count for a generated question to be kept.
// "What is X?" is 11 runes — keep the floor at 10 so short but valid
// questions from the LLM (or fakes in tests) are not silently dropped.
// Real LLM output is always well above this; this guards only against
// truly empty or degenerate outputs (e.g. "?", "N/A").
const minQuestionLen = 10

// chunkRef pairs a FileRef with one of its chunks.
type chunkRef struct {
	file  FileRef
	chunk vector.FileChunkRow
}

// sampleChunks returns up to n (file, chunk) pairs evenly spaced across the
// corpus — deterministic (no RNG) so reruns produce the same samples.
// maxFiles ≤ 0 means no file-count cap.
func (g *Generator) sampleChunks(ctx context.Context, kbID string, n, maxFiles int) ([]chunkRef, error) {
	files, err := g.corpus.ListFiles(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	if maxFiles > 0 && len(files) > maxFiles {
		files = files[:maxFiles]
	}
	var all []chunkRef
	for _, f := range files {
		chunks, err := g.corpus.GetChunks(ctx, kbID, f.ID)
		if err != nil {
			logctx.From(ctx).Warn("gen.sampleChunks: GetChunks failed; skipping file",
				"fileId", f.ID, "error", err)
			continue
		}
		for _, c := range chunks {
			all = append(all, chunkRef{file: f, chunk: c})
		}
	}
	if n <= 0 || len(all) <= n {
		return all, nil
	}
	step := float64(len(all)) / float64(n)
	out := make([]chunkRef, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, all[int(float64(i)*step)])
	}
	return out, nil
}

// lookup generates up to n single-chunk factual lookup questions.
func (g *Generator) lookup(ctx context.Context, kbID, lang string, n, maxFiles int) ([]question, error) {
	refs, err := g.sampleChunks(ctx, kbID, n, maxFiles)
	if err != nil {
		return nil, err
	}
	var out []question
	for i, r := range refs {
		qs, err := g.qgen.LookupQuestions(ctx, r.file.Name, "", r.chunk.Content, 1)
		if err != nil {
			logctx.From(ctx).Warn("gen.lookup: LookupQuestions failed; skipping",
				"fileId", r.file.ID, "error", err)
			continue
		}
		for _, q := range qs {
			if qq, ok := mkQuestion("lookup", lang, q, "lookup", r.file, i); ok {
				out = append(out, qq)
			}
		}
	}
	return out, nil
}

// complex generates up to n complex-reasoning questions from individual chunks.
func (g *Generator) complex(ctx context.Context, kbID, lang string, n, maxFiles int) ([]question, error) {
	refs, err := g.sampleChunks(ctx, kbID, n, maxFiles)
	if err != nil {
		return nil, err
	}
	var out []question
	for i, r := range refs {
		q, err := g.qgen.ComplexQuestion(ctx, r.file.Name, "", r.chunk.Content)
		if err != nil {
			logctx.From(ctx).Warn("gen.complex: ComplexQuestion failed; skipping",
				"fileId", r.file.ID, "error", err)
			continue
		}
		if qq, ok := mkQuestion("complex", lang, q, "complex_reasoning", r.file, i); ok {
			out = append(out, qq)
		}
	}
	return out, nil
}

// mkQuestion constructs a single-file eval.Question and reports whether it
// should be kept (text must be at least minQuestionLen runes).
func mkQuestion(mode, lang, text, queryType string, f FileRef, idx int) (question, bool) {
	text = strings.TrimSpace(text)
	if len([]rune(text)) < minQuestionLen {
		return question{}, false
	}
	return question{
		ID:                fmt.Sprintf("gen-%s-%s-%d", mode, shortID(f.ID), idx),
		Question:          text,
		Language:          lang,
		MustCiteFileIDs:   []string{f.ID},
		MustCiteFileNames: []string{f.Name},
		QueryType:         queryType,
		Notes:             "auto:" + mode,
	}, true
}

// shortID returns the first 8 characters of id, or id itself if shorter.
func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

// Counts caps how many questions each mode produces.
type Counts struct {
	Lookup      int
	Complex     int
	Enumeration int
	MultiHop    int
	// MaxFiles caps the number of corpus files considered by every mode.
	// 0 means no cap (all files are eligible).
	MaxFiles int
}

// Stats reports how many questions each mode actually produced (post-filter).
type Stats struct {
	Lookup      int
	Complex     int
	Enumeration int
	MultiHop    int
}

func (s Stats) Total() int { return s.Lookup + s.Complex + s.Enumeration + s.MultiHop }

func (g *Generator) enumeration(ctx context.Context, kbID, lang string, n, maxFiles int) ([]question, error) {
	files, err := g.corpus.ListFiles(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	if maxFiles > 0 && len(files) > maxFiles {
		files = files[:maxFiles]
	}
	if n > 0 && len(files) > n {
		files = files[:n]
	}
	var out []question
	for i, f := range files {
		chunks, err := g.corpus.GetChunks(ctx, kbID, f.ID)
		if err != nil || len(chunks) == 0 {
			continue
		}
		texts := make([]string, 0, len(chunks))
		for _, c := range chunks {
			texts = append(texts, c.Content)
		}
		q, err := g.qgen.EnumerationQuestion(ctx, f.Name, texts)
		if err != nil {
			logctx.From(ctx).Warn("gen.enumeration: failed; skipping", "fileId", f.ID, "error", err)
			continue
		}
		if qq, ok := mkQuestion("enumeration", lang, q, "enumeration", f, i); ok {
			out = append(out, qq)
		}
	}
	return out, nil
}

func (g *Generator) multiHop(ctx context.Context, kbID, lang string, n, maxFiles int) ([]question, error) {
	refs, err := g.sampleChunks(ctx, kbID, n, maxFiles)
	if err != nil {
		return nil, err
	}
	var out []question
	for i, r := range refs {
		nb, ok, err := g.neighbors.NearestOtherFile(ctx, kbID, r.file.ID, r.chunk.Content)
		if err != nil || !ok || nb.FileID == r.file.ID || nb.Score < g.simFloor {
			continue
		}
		q, err := g.qgen.BridgeQuestion(ctx, r.file.Name, r.chunk.Content, nb.FileName, nb.ChunkText)
		if err != nil {
			logctx.From(ctx).Warn("gen.multihop: failed; skipping", "fileId", r.file.ID, "error", err)
			continue
		}
		q = strings.TrimSpace(q)
		if len([]rune(q)) < minQuestionLen {
			continue
		}
		out = append(out, question{
			ID:                fmt.Sprintf("gen-multihop-%s-%s-%d", shortID(r.file.ID), shortID(nb.FileID), i),
			Question:          q,
			Language:          lang,
			MustCiteFileIDs:   []string{r.file.ID, nb.FileID},
			MustCiteFileNames: []string{r.file.Name, nb.FileName},
			QueryType:         "complex_reasoning",
			Notes:             fmt.Sprintf("auto:multihop sim=%.2f", nb.Score),
		})
	}
	return out, nil
}

// GenerateAll runs all four modes, dedups across the combined set, stamps the
// KB id on every question, and returns the questions + per-mode stats. Modes are
// independent: a failing mode logs and contributes nothing.
func (g *Generator) GenerateAll(ctx context.Context, kbID, lang string, counts Counts) ([]question, Stats, error) {
	var all []question
	var st Stats

	// logModeErr surfaces a swallowed per-mode error (e.g. ListFiles failing in
	// sampleChunks) so a misconfigured KB doesn't yield silent zero output.
	logModeErr := func(mode string, err error) {
		if err != nil {
			logctx.From(ctx).Warn("gen.GenerateAll: mode failed", "mode", mode, "kbId", kbID, "error", err)
		}
	}
	if counts.Lookup > 0 {
		qs, err := g.lookup(ctx, kbID, lang, counts.Lookup, counts.MaxFiles)
		logModeErr("lookup", err)
		st.Lookup = len(qs)
		all = append(all, qs...)
	}
	if counts.Complex > 0 {
		qs, err := g.complex(ctx, kbID, lang, counts.Complex, counts.MaxFiles)
		logModeErr("complex", err)
		st.Complex = len(qs)
		all = append(all, qs...)
	}
	if counts.Enumeration > 0 {
		qs, err := g.enumeration(ctx, kbID, lang, counts.Enumeration, counts.MaxFiles)
		logModeErr("enumeration", err)
		st.Enumeration = len(qs)
		all = append(all, qs...)
	}
	if counts.MultiHop > 0 {
		qs, err := g.multiHop(ctx, kbID, lang, counts.MultiHop, counts.MaxFiles)
		logModeErr("multihop", err)
		st.MultiHop = len(qs)
		all = append(all, qs...)
	}
	all = dedup(all)
	// Stamp KbID so the output validates through eval.ParseGoldenSetJSONL.
	for i := range all {
		all[i].KbID = kbID
	}
	return all, st, nil
}

// dedup removes questions whose normalized text (lowercased, whitespace-
// collapsed) matches an earlier entry, keeping the first occurrence.
func dedup(in []question) []question {
	seen := make(map[string]bool, len(in))
	out := make([]question, 0, len(in))
	for _, q := range in {
		key := strings.Join(strings.Fields(strings.ToLower(q.Question)), " ")
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, q)
	}
	return out
}
