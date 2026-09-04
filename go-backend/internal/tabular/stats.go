package tabular

import (
	"sort"
	"strconv"
	"strings"

	"github.com/justrag/go-backend/internal/sheetsource"
	"github.com/justrag/go-backend/internal/tabular/profile"
)

const (
	defaultMaxDistinct = 10_000
	valueSetMax        = 50
	sampleMax          = 3
)

var boolTrue = map[string]bool{"ja": true, "yes": true, "true": true, "x": true, "✓": true, "wahr": true}
var boolFalse = map[string]bool{"nein": true, "no": true, "false": true, "falsch": true}

type StatsOptions struct{ MaxDistinct int } // default 10 000

// ColumnAccumulator gathers one column's statistics during the streaming pass.
type ColumnAccumulator struct {
	Profile                                                        profile.ColumnProfile
	Name                                                           string // sanitised, deduped SQL identifier
	NonEmpty, Numeric, Dates, Timestamps, Bools, Texts, NullTokens int64
	LeadingZero, LongDigits                                        int64
	MinNum, MaxNum                                                 float64
	MinDate, MaxDate                                               string
	Distinct                                                       map[string]int64 // nil for non-text roles or after overflow
	Overflowed                                                     bool
	Samples                                                        []string // first 3 non-empty raw values
	// unexported
	hasNum      bool
	maxDistinct int
}

func NewAccumulators(cols []profile.ColumnProfile, opts StatsOptions) []*ColumnAccumulator {
	if opts.MaxDistinct <= 0 {
		opts.MaxDistinct = defaultMaxDistinct
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = SanitizeIdentifier(c.Header)
	}
	names = DedupeIdentifiers(names)
	out := make([]*ColumnAccumulator, len(cols))
	for i, c := range cols {
		out[i] = &ColumnAccumulator{Profile: c, Name: names[i], maxDistinct: opts.MaxDistinct}
		switch c.Role {
		case profile.RoleID, profile.RoleCategory, profile.RoleText:
			out[i].Distinct = map[string]int64{}
		}
	}
	return out
}

func (a *ColumnAccumulator) Add(c sheetsource.Cell) {
	if c.IsEmpty() {
		return
	}
	raw := strings.TrimSpace(c.Raw)
	if c.Kind == sheetsource.KindText && profile.IsNullToken(raw) {
		a.NullTokens++
		return
	}
	a.NonEmpty++
	if len(a.Samples) < sampleMax {
		a.Samples = append(a.Samples, raw)
	}
	canon, kind := a.classify(c)
	switch kind {
	case sheetsource.KindNumber:
		a.Numeric++
		if f, err := strconv.ParseFloat(canon, 64); err == nil {
			if !a.hasNum || f < a.MinNum {
				a.MinNum = f
			}
			if !a.hasNum || f > a.MaxNum {
				a.MaxNum = f
			}
			a.hasNum = true
		}
	case sheetsource.KindDate:
		if strings.Contains(canon, "T") {
			a.Timestamps++
		} else {
			a.Dates++
		}
		if a.MinDate == "" || canon < a.MinDate {
			a.MinDate = canon
		}
		if canon > a.MaxDate {
			a.MaxDate = canon
		}
	case sheetsource.KindBool:
		a.Bools++
	default:
		a.Texts++
	}
	if lz, ld, _ := profile.LooksLikeIDValue(raw); lz {
		a.LeadingZero++
	} else if ld {
		a.LongDigits++
	}
	if a.Distinct != nil {
		a.Distinct[canon]++
		if len(a.Distinct) > a.maxDistinct {
			a.Distinct, a.Overflowed = nil, true
		}
	}
}

func (a *ColumnAccumulator) classify(c sheetsource.Cell) (string, sheetsource.CellKind) {
	raw := strings.TrimSpace(c.Raw)
	switch c.Kind {
	case sheetsource.KindNumber, sheetsource.KindDate:
		return raw, c.Kind
	case sheetsource.KindBool:
		return strings.ToLower(raw), sheetsource.KindBool
	}
	if lz, ld, _ := profile.LooksLikeIDValue(raw); lz || ld {
		return raw, sheetsource.KindText
	}
	if t, ok := profile.ParseDateText(raw); ok {
		if strings.ContainsAny(raw, ":T") {
			return t.Format("2006-01-02T15:04:05"), sheetsource.KindDate
		}
		return t.Format("2006-01-02"), sheetsource.KindDate
	}
	if f, ok := profile.ParseNumber(raw, a.Profile.DecimalComma); ok {
		return strconv.FormatFloat(f, 'f', -1, 64), sheetsource.KindNumber
	}
	switch l := strings.ToLower(raw); {
	case boolTrue[l]:
		return "true", sheetsource.KindBool
	case boolFalse[l]:
		return "false", sheetsource.KindBool
	}
	return raw, sheetsource.KindText
}

// Canonical returns the value to COPY: numbers "1234.5", dates ISO, bools "true"/"false",
// null tokens and empties → ("", false), else trimmed text.
func (a *ColumnAccumulator) Canonical(c sheetsource.Cell) (string, bool) {
	if c.IsEmpty() {
		return "", false
	}
	raw := strings.TrimSpace(c.Raw)
	if c.Kind == sheetsource.KindText && profile.IsNullToken(raw) {
		return "", false
	}
	v, _ := a.classify(c)
	return v, true
}

// FinalSpec decides the SQL type(s) after the pass (spec §4.1): primary + optional shadow.
func (a *ColumnAccumulator) FinalSpec() (ColumnSpec, *ColumnSpec) {
	n := a.NonEmpty
	base := ColumnSpec{Original: a.Profile.Header, Name: a.Name, Role: string(a.Profile.Role), Description: a.Profile.Description, Type: TypeText}
	switch {
	case a.Profile.Role == profile.RoleID || a.LeadingZero > 0 || a.LongDigits > 0:
	case n > 0 && a.Bools == n && a.Profile.Role == profile.RoleBool:
		base.Type = TypeBool
	case n > 0 && a.Dates+a.Timestamps == n:
		base.Type = TypeDate
		if a.Timestamps > 0 {
			base.Type = TypeTimestamp
		}
	case n > 0 && a.Numeric == n:
		base.Type = TypeNumeric
	case a.Profile.Role == profile.RoleMeasure && a.Numeric > 0 && a.Numeric < n:
		shadow := ColumnSpec{Original: a.Profile.Header + " (Zahl)", Name: trimTo(a.Name, maxIdentBytes-4) + "_num", Type: TypeNumeric, Role: string(profile.RoleMeasure), ShadowOf: a.Name}
		return base, &shadow
	}
	return base, nil
}

// Stat renders the catalog column_stats entry; totalRows is the number of materialised rows.
func (a *ColumnAccumulator) Stat(primary ColumnSpec, shadow *ColumnSpec, totalRows int64) ColumnStat {
	st := ColumnStat{Name: primary.Name, Original: primary.Original, Type: string(primary.Type), Role: primary.Role, Description: primary.Description,
		NullCount: totalRows - a.NonEmpty, NullTokens: a.NullTokens, DistinctCount: -1, HighCardinality: a.Overflowed}
	if shadow != nil {
		st.ShadowColumn = shadow.Name
	}
	if a.hasNum {
		st.Min, st.Max = strconv.FormatFloat(a.MinNum, 'f', -1, 64), strconv.FormatFloat(a.MaxNum, 'f', -1, 64)
	} else if a.MinDate != "" {
		st.Min, st.Max = a.MinDate, a.MaxDate
	}
	if a.Distinct != nil {
		st.DistinctCount = int64(len(a.Distinct))
		type kv struct {
			v string
			n int64
		}
		var kvs []kv
		for v, n := range a.Distinct {
			if !profile.LooksLikeInstruction(v) {
				kvs = append(kvs, kv{v, n})
			}
		}
		sort.Slice(kvs, func(i, j int) bool {
			if kvs[i].n != kvs[j].n {
				return kvs[i].n > kvs[j].n
			}
			return kvs[i].v < kvs[j].v
		})
		for i := 0; i < len(kvs) && i < valueSetMax; i++ {
			st.ValueSet = append(st.ValueSet, kvs[i].v)
		}
	}
	for _, s := range a.Samples {
		if !profile.LooksLikeInstruction(s) {
			st.Samples = append(st.Samples, s)
		}
	}
	return st
}

// Values returns the distinct-value map for tabular_column_values (nil when overflowed or non-text).
func (a *ColumnAccumulator) Values() map[string]int64 {
	if a.Overflowed {
		return nil
	}
	return a.Distinct
}
