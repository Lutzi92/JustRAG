package community

import (
	"fmt"
	"strings"
)

// Member is a community member entity for the summary prompt.
type Member struct {
	ID   int64
	Name string
	Type string
}

// InternalEdge is a relationship between two community members.
type InternalEdge struct {
	Src, Dst      int64
	Rel, Evidence string
}

// BuildSummaryInput renders a community's members + internal relationships into
// the text fed to the summariser, truncated to maxRunes.
func BuildSummaryInput(members []Member, edges []InternalEdge, maxRunes int) string {
	name := map[int64]string{}
	var b strings.Builder
	b.WriteString("Entities in this community:\n")
	for _, m := range members {
		name[m.ID] = m.Name
		fmt.Fprintf(&b, "- %s (%s)\n", m.Name, m.Type)
	}
	if len(edges) > 0 {
		b.WriteString("\nRelationships:\n")
		for _, e := range edges {
			ev := ""
			if e.Evidence != "" {
				ev = " — " + e.Evidence
			}
			fmt.Fprintf(&b, "- %s %s %s%s\n", name[e.Src], e.Rel, name[e.Dst], ev)
		}
	}
	s := b.String()
	if maxRunes > 0 {
		r := []rune(s)
		if len(r) > maxRunes {
			s = string(r[:maxRunes])
		}
	}
	return s
}
