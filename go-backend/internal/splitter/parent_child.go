package splitter

import "strings"

// ParentChildGroup holds one parent chunk and its associated children.
// Children are carved from the parent's text by re-splitting at the
// child token budget, so each child is wholly contained within (or
// closely follows) the parent. The processor uses this grouping to
// link children to their parent via the parent_chunk_id FK at insert
// time.
type ParentChildGroup struct {
	ParentText string
	ChildTexts []string
}

// ParentChildSplit splits text twice: first at parent granularity using
// parentCfg, then each resulting parent is split at child granularity
// using childCfg. Returns one ParentChildGroup per parent chunk.
//
// Empty / whitespace-only text returns nil. A parent that is shorter
// than the child budget yields a single child equal to the parent (the
// child-side Split returns the original chunk when it fits).
//
// Why this is two passes rather than one merged loop: the recursive
// splitter's separator hierarchy resolves boundaries at the granularity
// it's targeting. Splitting at parent size first picks paragraph or
// section breaks; splitting children FROM EACH PARENT (not from the
// raw text) ensures children align with their parent's content and
// don't straddle parent boundaries. The cost is one extra Split call
// per parent (cheap; the splitter is pure CPU).
func ParentChildSplit(text string, parentCfg, childCfg Config) []ParentChildGroup {
	parents := Split(text, parentCfg)
	if len(parents) == 0 {
		return nil
	}
	groups := make([]ParentChildGroup, 0, len(parents))
	for _, parent := range parents {
		trimmed := strings.TrimSpace(parent)
		if trimmed == "" {
			continue
		}
		children := Split(trimmed, childCfg)
		if len(children) == 0 {
			continue
		}
		groups = append(groups, ParentChildGroup{
			ParentText: trimmed,
			ChildTexts: children,
		})
	}
	return groups
}
