package analytics

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// PGStore is a PostgreSQL-backed implementation of the analytics Store interface.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

// fileWhereClause builds a dynamic WHERE clause for file queries based on filters.
// Returns the clause string (starting with " AND ...") and the args to append.
// paramOffset is the next $N placeholder number to use.
func fileWhereClause(f Filters, paramOffset int) (string, []any) {
	var clause string
	var args []any
	if f.DateRange.From != nil {
		clause += fmt.Sprintf(" AND created_at >= $%d", paramOffset)
		args = append(args, *f.DateRange.From)
		paramOffset++
	}
	if f.DateRange.To != nil {
		clause += fmt.Sprintf(" AND created_at <= $%d", paramOffset)
		args = append(args, *f.DateRange.To)
		paramOffset++
	}
	if f.Status != "" {
		clause += fmt.Sprintf(" AND status = $%d", paramOffset)
		args = append(args, f.Status)
		paramOffset++
	}
	if f.FileType != "" {
		clause += fmt.Sprintf(" AND type = $%d", paramOffset)
		args = append(args, f.FileType)
		paramOffset++
	}
	return clause, args
}

// GetFileStats returns file statistics for the given knowledge base.
func (s *PGStore) GetFileStats(ctx context.Context, kbID string, f Filters) (*FileStats, error) {
	extraWhere, extraArgs := fileWhereClause(f, 2)
	baseArgs := append([]any{kbID}, extraArgs...)

	byType, err := pgxutil.QueryRows[TypeCount](ctx, s.pool,
		`SELECT type, COUNT(*)::int AS count, COALESCE(SUM(size), 0)::bigint AS total_size
		 FROM files WHERE kb_id = $1`+extraWhere+` GROUP BY type ORDER BY count DESC`, baseArgs...)
	if err != nil {
		return nil, fmt.Errorf("file stats by type: %w", err)
	}

	byStatus, err := pgxutil.QueryRows[StatusCount](ctx, s.pool,
		`SELECT status, COUNT(*)::int AS count FROM files WHERE kb_id = $1`+extraWhere+` GROUP BY status`, baseArgs...)
	if err != nil {
		return nil, fmt.Errorf("file stats by status: %w", err)
	}

	byOrigin, err := pgxutil.QueryRows[OriginCount](ctx, s.pool,
		`SELECT COALESCE(origin, 'upload') AS origin, COUNT(*)::int AS count
		 FROM files WHERE kb_id = $1`+extraWhere+` GROUP BY origin`, baseArgs...)
	if err != nil {
		return nil, fmt.Errorf("file stats by origin: %w", err)
	}

	var totalFiles int
	var totalSize int64
	err = s.pool.QueryRow(ctx,
		`SELECT COUNT(*)::int, COALESCE(SUM(size), 0)::bigint FROM files WHERE kb_id = $1`+extraWhere, baseArgs...).
		Scan(&totalFiles, &totalSize)
	if err != nil {
		return nil, fmt.Errorf("file stats totals: %w", err)
	}

	return &FileStats{
		ByType:     byType,
		ByStatus:   byStatus,
		ByOrigin:   byOrigin,
		TotalFiles: totalFiles,
		TotalSize:  totalSize,
	}, nil
}

// defaultDateRange returns the last 30 days when the filter's DateRange is nil.
func defaultDateRange(f Filters) (time.Time, time.Time) {
	to := time.Now()
	from := to.AddDate(0, 0, -30)
	if f.DateRange.From != nil {
		from = *f.DateRange.From
	}
	if f.DateRange.To != nil {
		to = *f.DateRange.To
	}
	return from, to
}

// GetActivityStats returns upload/chat/message activity over time.
func (s *PGStore) GetActivityStats(ctx context.Context, kbID string, f Filters) (*ActivityStats, error) {
	from, to := defaultDateRange(f)

	filesOverTime, err := pgxutil.QueryRows[DateCount](ctx, s.pool,
		`SELECT DATE(created_at)::text AS date, COUNT(*)::int AS count
		 FROM files WHERE kb_id = $1 AND created_at >= $2 AND created_at <= $3
		 GROUP BY DATE(created_at) ORDER BY date`, kbID, from, to)
	if err != nil {
		return nil, fmt.Errorf("activity stats files: %w", err)
	}

	chatsOverTime, err := pgxutil.QueryRows[DateCount](ctx, s.pool,
		`SELECT DATE(created_at)::text AS date, COUNT(*)::int AS count
		 FROM chats WHERE kb_id = $1 AND created_at >= $2 AND created_at <= $3
		 GROUP BY DATE(created_at) ORDER BY date`, kbID, from, to)
	if err != nil {
		return nil, fmt.Errorf("activity stats chats: %w", err)
	}

	messagesOverTime, err := pgxutil.QueryRows[DateCount](ctx, s.pool,
		`SELECT DATE(m.created_at)::text AS date, COUNT(*)::int AS count
		 FROM messages m INNER JOIN chats c ON m.chat_id = c.id
		 WHERE c.kb_id = $1 AND m.created_at >= $2 AND m.created_at <= $3
		 GROUP BY DATE(m.created_at) ORDER BY date`, kbID, from, to)
	if err != nil {
		return nil, fmt.Errorf("activity stats messages: %w", err)
	}

	return &ActivityStats{
		FilesOverTime:    filesOverTime,
		ChatsOverTime:    chatsOverTime,
		MessagesOverTime: messagesOverTime,
	}, nil
}

// chatWhereClause builds a dynamic WHERE clause for chat queries based on date filters.
func chatWhereClause(f Filters, paramOffset int) (string, []any) {
	var clause string
	var args []any
	if f.DateRange.From != nil {
		clause += fmt.Sprintf(" AND c.created_at >= $%d", paramOffset)
		args = append(args, *f.DateRange.From)
		paramOffset++
	}
	if f.DateRange.To != nil {
		clause += fmt.Sprintf(" AND c.created_at <= $%d", paramOffset)
		args = append(args, *f.DateRange.To)
		paramOffset++
	}
	return clause, args
}

// GetChatStats returns aggregate chat and message counts.
func (s *PGStore) GetChatStats(ctx context.Context, kbID string, f Filters) (*ChatStats, error) {
	chatExtra, chatArgs := chatWhereClause(f, 2)
	chatBaseArgs := append([]any{kbID}, chatArgs...)

	var totalChats int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM chats c WHERE c.kb_id = $1`+chatExtra, chatBaseArgs...).Scan(&totalChats)
	if err != nil {
		return nil, fmt.Errorf("chat stats total chats: %w", err)
	}

	var totalMessages int
	err = s.pool.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM messages m INNER JOIN chats c ON m.chat_id = c.id WHERE c.kb_id = $1`+chatExtra, chatBaseArgs...).
		Scan(&totalMessages)
	if err != nil {
		return nil, fmt.Errorf("chat stats total messages: %w", err)
	}

	messagesByRole, err := pgxutil.QueryRows[RoleCount](ctx, s.pool,
		`SELECT m.role, COUNT(*)::int AS count
		 FROM messages m INNER JOIN chats c ON m.chat_id = c.id
		 WHERE c.kb_id = $1`+chatExtra+` GROUP BY m.role`, chatBaseArgs...)
	if err != nil {
		return nil, fmt.Errorf("chat stats by role: %w", err)
	}

	var avgMessagesPerChat float64
	if totalChats > 0 {
		avgMessagesPerChat = float64(totalMessages) / float64(totalChats)
	}

	return &ChatStats{
		TotalChats:         totalChats,
		TotalMessages:      totalMessages,
		MessagesByRole:     messagesByRole,
		AvgMessagesPerChat: avgMessagesPerChat,
	}, nil
}

// generatedContentTypeCount is an internal struct for scanning generated_content counts.
type generatedContentTypeCount struct {
	Type      string `db:"type"`
	Count     int    `db:"count"`
	TotalSize int64  `db:"total_size"`
}

// generatedContentDateCount is an internal struct for scanning generated_content over time.
type generatedContentDateCount struct {
	Date  string `db:"date"`
	Count int    `db:"count"`
}

// genContentWhereClause builds dynamic WHERE conditions for generated_content.
func genContentWhereClause(f Filters, paramOffset int) (string, []any) {
	var clause string
	var args []any
	if f.DateRange.From != nil {
		clause += fmt.Sprintf(" AND created_at >= $%d", paramOffset)
		args = append(args, *f.DateRange.From)
		paramOffset++
	}
	if f.DateRange.To != nil {
		clause += fmt.Sprintf(" AND created_at <= $%d", paramOffset)
		args = append(args, *f.DateRange.To)
		paramOffset++
	}
	return clause, args
}

// GetGeneratedContentStats returns statistics about generated content.
func (s *PGStore) GetGeneratedContentStats(ctx context.Context, kbID string, f Filters) (*GeneratedContentStats, error) {
	from, to := defaultDateRange(f)
	extraWhere, extraArgs := genContentWhereClause(f, 2)
	baseArgs := append([]any{kbID}, extraArgs...)

	rows, err := pgxutil.QueryRows[generatedContentTypeCount](ctx, s.pool,
		`SELECT type, COUNT(*)::int AS count, 0::bigint AS total_size
		 FROM generated_content WHERE kb_id = $1`+extraWhere+` GROUP BY type ORDER BY count DESC`, baseArgs...)
	if err != nil {
		return nil, fmt.Errorf("generated content by type: %w", err)
	}

	byType := make([]TypeCount, len(rows))
	for i, r := range rows {
		byType[i] = TypeCount{Type: r.Type, Count: r.Count}
	}

	var totalGenerated int
	err = s.pool.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM generated_content WHERE kb_id = $1`+extraWhere, baseArgs...).Scan(&totalGenerated)
	if err != nil {
		return nil, fmt.Errorf("generated content total: %w", err)
	}

	overTimeRows, err := pgxutil.QueryRows[generatedContentDateCount](ctx, s.pool,
		`SELECT DATE(created_at)::text AS date, COUNT(*)::int AS count
		 FROM generated_content WHERE kb_id = $1 AND created_at >= $2 AND created_at <= $3
		 GROUP BY DATE(created_at) ORDER BY date`, kbID, from, to)
	if err != nil {
		return nil, fmt.Errorf("generated content over time: %w", err)
	}

	overTime := make([]DateCount, len(overTimeRows))
	for i, r := range overTimeRows {
		overTime[i] = DateCount{Date: r.Date, Count: r.Count}
	}

	return &GeneratedContentStats{
		ByType:         byType,
		TotalGenerated: totalGenerated,
		OverTime:       overTime,
	}, nil
}

// GetRetrievalQualityStats returns feedback distribution, score over time, and low-score queries.
// Respects from/to date filters; defaults to 30 days for scores and 7 days for low-score queries.
func (s *PGStore) GetRetrievalQualityStats(ctx context.Context, kbID string, f Filters) (*RetrievalQualityStats, error) {
	// Determine date range for all sub-queries
	scoreFrom := time.Now().AddDate(0, 0, -30)
	lowScoreFrom := time.Now().AddDate(0, 0, -7)
	dateTo := time.Now()
	if f.DateRange.From != nil {
		scoreFrom = *f.DateRange.From
		lowScoreFrom = *f.DateRange.From
	}
	if f.DateRange.To != nil {
		dateTo = *f.DateRange.To
	}

	// Feedback distribution — filtered by date range
	feedbackStats, err := pgxutil.QueryRows[FeedbackCount](ctx, s.pool,
		`SELECT COALESCE(m.feedback, 'none') AS feedback, COUNT(*)::int AS count
		 FROM messages m INNER JOIN chats c ON m.chat_id = c.id
		 WHERE c.kb_id = $1 AND m.role = 'ai'
		   AND m.created_at >= $2 AND m.created_at <= $3
		 GROUP BY m.feedback`, kbID, scoreFrom, dateTo)
	if err != nil {
		return nil, fmt.Errorf("retrieval quality feedback: %w", err)
	}

	scoreOverTime, err := pgxutil.QueryRows[ScorePoint](ctx, s.pool,
		`SELECT DATE(m.created_at)::text AS day,
		        AVG((source->>'score')::float) AS avg_score,
		        COUNT(DISTINCT m.id)::int AS message_count
		 FROM messages m
		 INNER JOIN chats c ON m.chat_id = c.id
		 CROSS JOIN LATERAL jsonb_array_elements(m.sources::jsonb) AS source
		 WHERE c.kb_id = $1 AND m.role = 'ai' AND m.sources IS NOT NULL
		   AND m.created_at >= $2 AND m.created_at <= $3
		 GROUP BY DATE(m.created_at) ORDER BY day`, kbID, scoreFrom, dateTo)
	if err != nil {
		return nil, fmt.Errorf("retrieval quality score over time: %w", err)
	}

	lowScoreQueries, err := pgxutil.QueryRows[LowScoreQuery](ctx, s.pool,
		`SELECT m.id, m.content, to_char(m.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS created_at,
		        AVG((source->>'score')::float) AS avg_score
		 FROM messages m
		 INNER JOIN chats c ON m.chat_id = c.id
		 CROSS JOIN LATERAL jsonb_array_elements(m.sources::jsonb) AS source
		 WHERE c.kb_id = $1 AND m.role = 'ai' AND m.sources IS NOT NULL
		   AND m.created_at >= $2 AND m.created_at <= $3
		 GROUP BY m.id, m.content, m.created_at
		 HAVING AVG((source->>'score')::float) < 0.3
		 ORDER BY avg_score ASC LIMIT 20`, kbID, lowScoreFrom, dateTo)
	if err != nil {
		return nil, fmt.Errorf("retrieval quality low score queries: %w", err)
	}

	return &RetrievalQualityStats{
		FeedbackStats:   feedbackStats,
		ScoreOverTime:   scoreOverTime,
		LowScoreQueries: lowScoreQueries,
	}, nil
}
