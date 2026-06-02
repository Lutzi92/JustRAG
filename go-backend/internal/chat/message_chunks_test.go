package chat

import "testing"

func TestChunkRefsFromSources(t *testing.T) {
	sources := []ChatSource{
		{Index: 0, ChunkID: "11111111-1111-1111-1111-111111111111"},
		{Index: 1, ChunkID: ""}, // no chunk id -> skipped
		{Index: 2, ChunkID: "22222222-2222-2222-2222-222222222222"},
	}
	ids, positions := chunkRefsFromSources(sources)
	if len(ids) != 2 || len(positions) != 2 {
		t.Fatalf("expected 2 refs, got ids=%v positions=%v", ids, positions)
	}
	if ids[0] != "11111111-1111-1111-1111-111111111111" || positions[0] != 0 {
		t.Fatalf("unexpected first ref: %s pos %d", ids[0], positions[0])
	}
	if ids[1] != "22222222-2222-2222-2222-222222222222" || positions[1] != 2 {
		t.Fatalf("unexpected second ref: %s pos %d", ids[1], positions[1])
	}
}

func TestChunkRefsFromSources_Empty(t *testing.T) {
	ids, positions := chunkRefsFromSources(nil)
	if ids != nil || positions != nil {
		t.Fatalf("expected nil slices for empty input")
	}
}

func TestChunkRefsFromSources_SkipsNonUUID(t *testing.T) {
	// A non-UUID ChunkID must be dropped, not passed to the ::uuid insert
	// (which would roll back the whole AddMessage transaction).
	sources := []ChatSource{
		{Index: 0, ChunkID: "not-a-uuid"},
		{Index: 1, ChunkID: "33333333-3333-3333-3333-333333333333"},
		{Index: 2, ChunkID: "11111111111111111111111111111111111"}, // 35 chars, no hyphens
	}
	ids, positions := chunkRefsFromSources(sources)
	if len(ids) != 1 || len(positions) != 1 {
		t.Fatalf("expected only the valid UUID, got ids=%v positions=%v", ids, positions)
	}
	if ids[0] != "33333333-3333-3333-3333-333333333333" || positions[0] != 1 {
		t.Fatalf("unexpected ref: %s pos %d", ids[0], positions[0])
	}
}
