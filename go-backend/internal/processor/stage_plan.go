package processor

// Ingestion stage keys. Stable identifiers the frontend maps to localized
// labels (see web/src/translations.ts ingestStage* keys). Keep in sync.
const (
	stageParse   = "parse"
	stageTabular = "tabular"
	stageEnrich  = "enrich"
	stageEmbed   = "embed"
	stageKG      = "kg"
	stageHyPE    = "hype"
	stageRaptor  = "raptor"
)

// stageFlags captures which optional stages will run for one file. Tabular is
// true only when tabular Q&A is enabled AND the file is a spreadsheet. The
// caller resolves these from site_config + the file's MIME type before the
// pipeline starts, so `total` (the static `x`) is known up front.
type stageFlags struct {
	Tabular bool
	Enrich  bool
	KG      bool
	HyPE    bool
	Raptor  bool
}

// stagePlan is the ordered list of stages a file will pass through, plus a
// reverse index for O(1) "which step number is this stage".
type stagePlan struct {
	stages []string
	index  map[string]int // 1-based; absent key -> 0 via indexOf
}

// buildStagePlan returns the ordered enabled-stage plan. parse and embed always
// run; the rest are gated by flags. Order matches the pipeline in ProcessFile.
func buildStagePlan(f stageFlags) stagePlan {
	order := []string{stageParse}
	if f.Tabular {
		order = append(order, stageTabular)
	}
	if f.Enrich {
		order = append(order, stageEnrich)
	}
	order = append(order, stageEmbed)
	if f.KG {
		order = append(order, stageKG)
	}
	if f.HyPE {
		order = append(order, stageHyPE)
	}
	if f.Raptor {
		order = append(order, stageRaptor)
	}
	idx := make(map[string]int, len(order))
	for i, s := range order {
		idx[s] = i + 1
	}
	return stagePlan{stages: order, index: idx}
}

func (p stagePlan) total() int { return len(p.stages) }

// indexOf returns the 1-based position of stage, or 0 if it is not in the plan.
func (p stagePlan) indexOf(stage string) int { return p.index[stage] }
