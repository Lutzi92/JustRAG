package widcert

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Advisory is the subset of a WID advisory we ingest.
type Advisory struct {
	Name               string
	Title              string
	BaseScore          string
	TemporalScore      string
	Classification     string
	ProductDescription string
	Description        string
	Products           []string
	CVEs               []string
	References         []string
	OperatingSystems   string
	InitialRelease     string
}

// rawNode mirrors the recursive WID content JSON shape: every node has a type,
// a properties bag, and a list of child nodes.
type rawNode struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Children   []rawNode      `json:"children"`
}

// str coerces a JSON property value (string or number) to a string; "" if
// absent. WID CVSS scores arrive as JSON numbers (basescore: 98), so the
// float64 case is exercised in practice.
func str(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// parseAdvisory parses the WID content JSON into an Advisory. Top-level child
// sections are identified by their stable "type" discriminator (see the
// section* constants in doc.go), so the parse is independent of section order
// and tolerant of missing sections. Each section's leaf values live under the
// child node's "properties" bag.
func parseAdvisory(name string, data []byte) (*Advisory, error) {
	var root rawNode
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse WID advisory %s: %w", name, err)
	}

	adv := &Advisory{
		Name:               name,
		Title:              str(root.Properties, "title"),
		ProductDescription: str(root.Properties, "productdescription"),
		Description:        str(root.Properties, "description"),
		OperatingSystems:   str(root.Properties, "operatingsystems"),
		InitialRelease:     str(root.Properties, "initialreleasedate"),
	}

	for _, section := range root.Children {
		switch section.Type {
		case sectionScores:
			if len(section.Children) > 0 {
				p := section.Children[0].Properties // first score node wins
				adv.BaseScore = str(p, "basescore")
				adv.TemporalScore = str(p, "temporalscore")
				adv.Classification = str(p, "classification")
			}
		case sectionCVEIDs:
			for _, c := range section.Children {
				if v := str(c.Properties, "cveId"); v != "" {
					adv.CVEs = append(adv.CVEs, v)
				}
			}
		case sectionReferences:
			for _, c := range section.Children {
				if v := str(c.Properties, "url"); v != "" {
					adv.References = append(adv.References, v)
				}
			}
		case sectionProducts:
			for _, c := range section.Children {
				if v := str(c.Properties, "name"); v != "" {
					adv.Products = append(adv.Products, v)
				}
			}
		}
	}
	return adv, nil
}
