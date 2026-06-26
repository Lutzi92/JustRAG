//go:build integration

// Community-summary insert/delete exercises the live pgvector store via
// TEST_VECTOR_DSN. Gated by the `integration` build tag so `go test ./...`
// without the tag stays runnable on machines without docker.

package vector

import (
	"context"
	"testing"
)

func TestInsertAndDeleteCommunitySummary(t *testing.T) {
	pool := openTestVectorPool(t)
	svc := NewChunkService(pool)
	ctx := context.Background()

	kbID := "33333333-3333-3333-3333-333333333333"
	fileID := "44444444-4444-4444-4444-444444444444"

	emb := make([]float64, 8)
	for i := range emb {
		emb[i] = 1.0 / float64(i+1)
	}

	t.Cleanup(func() {
		if err := svc.DeleteCommunitySummaries(ctx, kbID); err != nil {
			t.Logf("cleanup DeleteCommunitySummaries: %v", err)
		}
	})

	id, err := svc.InsertCommunitySummary(ctx, kbID, fileID, "Community 0: procurement and vendor onboarding.", emb, "english")
	if err != nil {
		t.Fatalf("InsertCommunitySummary: %v", err)
	}
	if id == "" {
		t.Fatalf("expected non-empty chunk id")
	}

	if err := svc.DeleteCommunitySummaries(ctx, kbID); err != nil {
		t.Fatalf("DeleteCommunitySummaries: %v", err)
	}

	// A second insert+delete confirms the delete actually cleared the prior
	// row (lookup returns the most recent by created_at, so a stale row would
	// still be found, but the table should now be empty of community rows).
	if _, err := svc.lookupCommunitySummaryID(ctx, kbID, fileID, "Community 0: procurement and vendor onboarding.", 8); err == nil {
		t.Fatalf("expected no community summary row after delete")
	}
}
