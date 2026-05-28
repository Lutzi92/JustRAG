package raptor

import "context"

// LeafRow is the input shape Builder reads from Store. Same fields as
// vector.RaptorLeafRow, redeclared here so the raptor package does
// not import internal/vector (avoids a dep cycle once Builder is
// wired into ChunkService).
type LeafRow struct {
	ID        string
	Content   string
	Embedding []float64
}

// SummaryRow is what Builder hands back to Store on each cluster
// insert. ChildIDs are the leaf or lower-level-summary ids that this
// summary covers — Store.InsertSummary uses them to back-patch
// raptor_parent_id on the children once the summary's own id is
// known.
type SummaryRow struct {
	Content   string
	Embedding []float64
	TreeLevel int
	ChildIDs  []string
}

// Store is the narrow surface Builder needs from a chunk store.
// Lets us unit-test Builder against a fake without standing up
// Postgres; the concrete adapter lives in internal/processor and
// wraps vector.ChunkService.
type Store interface {
	ListLeaves(ctx context.Context, kbID, fileID string, dim int) ([]LeafRow, error)
	InsertSummary(ctx context.Context, kbID, fileID, pgConfig string, dim int, row SummaryRow) (id string, err error)
}

// Embedder is the narrow surface Builder needs from the ai package:
// one embedding for one summary string.
type Embedder interface {
	EmbedOne(ctx context.Context, kbID, text string) ([]float64, error)
}

// Summariser is the narrow surface Builder needs from the ai
// package: produce the summary text for one cluster. kbID is passed
// through for the resolver's fallback chain (KB-level chat-model
// override).
type Summariser interface {
	Summarise(ctx context.Context, kbID, model, fileName string, level int, concatText string) (string, error)
}
