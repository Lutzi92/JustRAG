package tabular

import "context"

// QueryLogEntry is one row of tabular_query_log (the router's SQL audit
// trail — spec §5.1 step 6). Empty MessageID/SQL and RowCount == -1 round
// -trip to SQL NULL.
type QueryLogEntry struct {
	KBID      string
	MessageID string // "" → NULL
	Question  string
	SQL       string // "" → NULL
	RowCount  int    // -1 → NULL
	Outcome   string // fired_ok | fired_empty | sql_error | llm_error | validator_rejected | skipped_<reason>
}

// InsertQueryLog records one router decision/execution for observability.
func (c *Catalog) InsertQueryLog(ctx context.Context, e QueryLogEntry) error {
	var msgID, sql any
	if e.MessageID != "" {
		msgID = e.MessageID
	}
	if e.SQL != "" {
		sql = e.SQL
	}
	var rows any
	if e.RowCount >= 0 {
		rows = e.RowCount
	}
	_, err := c.pool.Exec(ctx, `INSERT INTO tabular_query_log (kb_id, message_id, question, sql, row_count, outcome) VALUES ($1,$2,$3,$4,$5,$6)`,
		e.KBID, msgID, e.Question, sql, rows, e.Outcome)
	return err
}
