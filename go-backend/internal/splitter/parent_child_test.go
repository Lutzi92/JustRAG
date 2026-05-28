package splitter

import (
	"strings"
	"testing"
)

func TestParentChildSplit_ShortTextSingleParentSingleChild(t *testing.T) {
	t.Parallel()
	text := "This is a short document. It has one paragraph."
	parentCfg := DefaultConfig()
	parentCfg.ChunkSize = 512
	childCfg := DefaultConfig()
	childCfg.ChunkSize = 128

	groups := ParentChildSplit(text, parentCfg, childCfg)

	if len(groups) != 1 {
		t.Fatalf("short text: want 1 parent group, got %d", len(groups))
	}
	if groups[0].ParentText != strings.TrimSpace(text) {
		t.Errorf("parent text mismatch:\n got: %q\nwant: %q", groups[0].ParentText, strings.TrimSpace(text))
	}
	if len(groups[0].ChildTexts) != 1 {
		t.Errorf("short parent: want 1 child, got %d", len(groups[0].ChildTexts))
	}
}

func TestParentChildSplit_LongDocumentMultipleParentsMultipleChildren(t *testing.T) {
	t.Parallel()
	// Build a long synthetic document with clear paragraph boundaries.
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString("This is paragraph number ")
		for j := 0; j < 20; j++ {
			b.WriteString("filler word ")
		}
		b.WriteString(".\n\n")
	}
	text := b.String()

	parentCfg := DefaultConfig()
	parentCfg.ChunkSize = 200
	childCfg := DefaultConfig()
	childCfg.ChunkSize = 50

	groups := ParentChildSplit(text, parentCfg, childCfg)

	if len(groups) < 2 {
		t.Fatalf("long doc: want >= 2 parent groups, got %d", len(groups))
	}
	for i, g := range groups {
		if g.ParentText == "" {
			t.Errorf("group %d: empty parent text", i)
		}
		if len(g.ChildTexts) == 0 {
			t.Errorf("group %d: no children", i)
		}
		// Soft check: at least the first 20 chars of each child (post-trim)
		// should appear in the parent. If not, the splitter is doing
		// something weird.
		for j, c := range g.ChildTexts {
			trimmed := strings.TrimSpace(c)
			n := 20
			if len(trimmed) < n {
				n = len(trimmed)
			}
			if !strings.Contains(g.ParentText, trimmed[:n]) {
				t.Errorf("group %d child %d: prefix %q not found in parent", i, j, trimmed[:n])
				break
			}
		}
	}
}

func TestParentChildSplit_EmptyText(t *testing.T) {
	t.Parallel()
	groups := ParentChildSplit("", DefaultConfig(), DefaultConfig())
	if len(groups) != 0 {
		t.Errorf("empty text: want 0 groups, got %d", len(groups))
	}
}

func TestParentChildSplit_WhitespaceOnly(t *testing.T) {
	t.Parallel()
	groups := ParentChildSplit("   \n\n   ", DefaultConfig(), DefaultConfig())
	if len(groups) != 0 {
		t.Errorf("whitespace-only: want 0 groups, got %d", len(groups))
	}
}
