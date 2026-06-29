package gitrepo

import "testing"

func TestReconcile(t *testing.T) {
	existing := []GitRepoFileRow{
		{FileID: "f1", Path: "a.md", BlobSHA: "sha-a"},     // unchanged
		{FileID: "f2", Path: "b.md", BlobSHA: "sha-b-old"}, // changed
		{FileID: "f3", Path: "gone.md", BlobSHA: "sha-g"},  // removed
	}
	desired := []RepoFile{
		{Path: "a.md", BlobSHA: "sha-a"},     // unchanged -> skip
		{Path: "b.md", BlobSHA: "sha-b-new"}, // changed -> recreate
		{Path: "c.md", BlobSHA: "sha-c"},     // new -> create
	}
	create, del := reconcile(existing, desired)

	gotCreate := map[string]bool{}
	for _, f := range create {
		gotCreate[f.Path] = true
	}
	if !gotCreate["c.md"] || !gotCreate["b.md"] || gotCreate["a.md"] {
		t.Fatalf("create set wrong: %v", gotCreate)
	}
	gotDel := map[string]bool{}
	for _, d := range del {
		gotDel[d.Path] = true
	}
	// changed b.md (old row) + removed gone.md must be deleted; a.md kept.
	if !gotDel["b.md"] || !gotDel["gone.md"] || gotDel["a.md"] {
		t.Fatalf("delete set wrong: %v", gotDel)
	}
}
