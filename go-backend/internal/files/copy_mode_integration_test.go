//go:build integration

// End-to-end copy mode (user file library phase 2, Task 6): the worker's
// file-processing handler, wired with the REAL files store, the real
// vector.ChunkService (CopyFileIndex) and a real processor (KG off), serves a
// library file by copying a completed donor's index from another KB — and
// falls back to ingest when the fingerprint does not match. Needs both the
// main DB (DB_*) and the vector DB (TEST_VECTOR_DSN).

package files_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/processor"
	"github.com/justrag/go-backend/internal/vector"
	"github.com/justrag/go-backend/internal/worker"
)

const cmDim = 8

// stubFPProcessor is the real processor with the fingerprint pinned, so the
// test controls whether the target "matches" the donor.
type stubFPProcessor struct {
	*processor.Processor
	fp string
}

func (s stubFPProcessor) IndexFingerprint(context.Context, string, int, int) (string, error) {
	return s.fp, nil
}

// ingestRecorder stands in for ProcessFile (the fallback path).
type ingestRecorder struct{ calls []processor.ProcessFileInput }

func (r *ingestRecorder) ProcessFileWithResult(_ context.Context, in processor.ProcessFileInput) (processor.ProcessOutcome, error) {
	r.calls = append(r.calls, in)
	return processor.ProcessOutcome{}, nil
}

type emptyReader struct{}

func (emptyReader) GetSiteConfigValue(context.Context, string) (*string, error) { return nil, nil }

func openCopyVectorPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_VECTOR_DSN")
	if dsn == "" {
		t.Skip("copy-mode e2e test requires TEST_VECTOR_DSN (vector Postgres)")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestCopyMode_EndToEnd(t *testing.T) {
	mainPool := openMainPool(t)
	vecPool := openCopyVectorPool(t)
	ctx := context.Background()
	store := files.NewStore(mainPool)
	chunks := vector.NewChunkService(vecPool)

	userID, ufID, kbs := seedLibraryKBs(t, mainPool, 3)
	donor := seedCopy(t, store, kbs[0], userID, ufID)
	target := seedCopy(t, store, kbs[1], userID, ufID)
	miss := seedCopy(t, store, kbs[2], userID, ufID)
	t.Cleanup(func() {
		for _, f := range []string{donor, target, miss} {
			_ = chunks.DeleteChunksByFileIDAllDims(context.Background(), f)
			_ = chunks.DeleteParentChunksByFileID(context.Background(), f)
			_ = chunks.DeleteHyPEByFileIDAllDims(context.Background(), f)
		}
	})
	// The target KB is public, so the copy must screen it (P2-R8).
	if _, err := mainPool.Exec(ctx, `UPDATE knowledge_bases SET visibility = 'public', language = 'de' WHERE id = $1::uuid`, kbs[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := mainPool.Exec(ctx, `UPDATE files SET status = 'completed', progress = 100, index_fingerprint = 'F' WHERE id = $1::uuid`, donor); err != nil {
		t.Fatal(err)
	}

	in := make([]vector.ChunkInput, 3)
	for i := range in {
		emb := make([]float64, cmDim)
		for j := range emb {
			emb[j] = float64(i+1) / float64(j+2)
		}
		in[i] = vector.ChunkInput{
			Content:   fmt.Sprintf("Donor chunk %d über laufende Hunde", i),
			Embedding: emb, KbID: kbs[0], FileID: donor,
			Metadata: map[string]any{"chunkIndex": i},
		}
	}
	if err := chunks.AddDocumentChunks(ctx, donor, in, cmDim, "german"); err != nil {
		t.Fatalf("AddDocumentChunks: %v", err)
	}

	proc := processor.NewProcessor(nil, nil, chunks, store)
	proc.SetMainDB(mainPool) // TextSearchConfig resolves the target KB language

	run := func(fileID, kbID, fp string) *ingestRecorder {
		t.Helper()
		rec := &ingestRecorder{}
		h := worker.NewFileProcessingHandlerWithDeps(worker.FileProcessingDeps{
			Proc:  rec,
			Links: store,
			Copy: &worker.CopyDeps{
				Proc:   stubFPProcessor{Processor: proc, fp: fp},
				Store:  store,
				Index:  chunks,
				Reader: emptyReader{},
			},
		})
		raw, _ := json.Marshal(jobs.FileProcessingPayload{
			FileID: fileID, KbID: kbID, OriginalName: "a.pdf", MimeType: "application/pdf", UserFileID: ufID,
		})
		if err := h(ctx, asynq.NewTask(jobs.TypeFileProcessing, raw)); err != nil {
			t.Fatalf("handler: %v", err)
		}
		return rec
	}

	contents := func(fileID string) []string {
		t.Helper()
		rows, err := vecPool.Query(ctx, `SELECT content FROM `+vector.GetVectorTableName(cmDim)+
			` WHERE file_id = $1::uuid ORDER BY (metadata->>'chunkIndex')::int`, fileID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				t.Fatal(err)
			}
			out = append(out, c)
		}
		return out
	}
	fileState := func(fileID string) (status, fp string, screened bool) {
		t.Helper()
		if err := mainPool.QueryRow(ctx, `SELECT status, COALESCE(index_fingerprint, ''), injection_detail IS NOT NULL
			FROM files WHERE id = $1::uuid`, fileID).Scan(&status, &fp, &screened); err != nil {
			t.Fatal(err)
		}
		return
	}

	// Matching fingerprint: copied, no ingest.
	rec := run(target, kbs[1], "F")
	if len(rec.calls) != 0 {
		t.Fatalf("ingest ran %d times; the copy should have served the file", len(rec.calls))
	}
	d, g := contents(donor), contents(target)
	if len(g) != len(d) || len(d) != 3 {
		t.Fatalf("chunk counts donor=%d target=%d, want 3/3", len(d), len(g))
	}
	for i := range d {
		if d[i] != g[i] {
			t.Errorf("chunk %d: target %q != donor %q", i, g[i], d[i])
		}
	}
	var wrongKB int
	if err := vecPool.QueryRow(ctx, `SELECT count(*) FROM `+vector.GetVectorTableName(cmDim)+
		` WHERE file_id = $1::uuid AND (kb_id <> $2::uuid OR vector_index <> to_tsvector('german', COALESCE(contextual_prefix,'') || ' ' || content))`,
		target, kbs[1]).Scan(&wrongKB); err != nil || wrongKB != 0 {
		t.Errorf("target rows with wrong kb or tsvector = %d, %v", wrongKB, err)
	}
	if status, fp, screened := fileState(target); status != "completed" || fp != "F" || !screened {
		t.Errorf("target status=%q fp=%q screened=%v; want completed/F/true", status, fp, screened)
	}
	if status, fp, _ := fileState(donor); status != "completed" || fp != "F" || len(contents(donor)) != 3 {
		t.Errorf("donor changed: status=%q fp=%q", status, fp)
	}

	// Different fingerprint (e.g. another embedding model in KB3): no copy,
	// the ingest fallback runs with the library ids.
	rec = run(miss, kbs[2], "G")
	if len(rec.calls) != 1 || rec.calls[0].UserFileID != ufID || rec.calls[0].OwnerUserID != userID {
		t.Fatalf("ingest calls = %+v, want one with the library ids", rec.calls)
	}
	if n := len(contents(miss)); n != 0 {
		t.Errorf("mismatch target got %d copied chunks, want 0", n)
	}
	if status, fp, _ := fileState(miss); status == "completed" || fp != "" {
		t.Errorf("mismatch target status=%q fp=%q; the copy path must not touch it", status, fp)
	}
}
