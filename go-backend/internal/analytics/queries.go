package analytics

import (
	"context"
	"time"
)

type DateRange struct {
	From *time.Time
	To   *time.Time
}

type Filters struct {
	DateRange DateRange
	FileType  string
	Status    string
}

type TypeCount struct {
	Type      string `json:"type" db:"type"`
	Count     int    `json:"count" db:"count"`
	TotalSize int64  `json:"totalSize,omitempty" db:"total_size"`
}

type StatusCount struct {
	Status string `json:"status" db:"status"`
	Count  int    `json:"count" db:"count"`
}

type OriginCount struct {
	Origin string `json:"origin" db:"origin"`
	Count  int    `json:"count" db:"count"`
}

type FileStats struct {
	ByType     []TypeCount   `json:"byType"`
	ByStatus   []StatusCount `json:"byStatus"`
	ByOrigin   []OriginCount `json:"byOrigin"`
	TotalFiles int           `json:"totalFiles"`
	TotalSize  int64         `json:"totalSize"`
}

type DateCount struct {
	Date  string `json:"date" db:"date"`
	Count int    `json:"count" db:"count"`
}

type ActivityStats struct {
	FilesOverTime    []DateCount `json:"filesOverTime"`
	ChatsOverTime    []DateCount `json:"chatsOverTime"`
	MessagesOverTime []DateCount `json:"messagesOverTime"`
}

type RoleCount struct {
	Role  string `json:"role" db:"role"`
	Count int    `json:"count" db:"count"`
}

type ChatStats struct {
	TotalChats         int         `json:"totalChats"`
	TotalMessages      int         `json:"totalMessages"`
	MessagesByRole     []RoleCount `json:"messagesByRole"`
	AvgMessagesPerChat float64     `json:"avgMessagesPerChat"`
}

type GeneratedContentStats struct {
	ByType         []TypeCount `json:"byType"`
	TotalGenerated int         `json:"totalGenerated"`
	OverTime       []DateCount `json:"overTime"`
}

type FeedbackCount struct {
	Feedback string `json:"feedback" db:"feedback"`
	Count    int    `json:"count" db:"count"`
}

type ScorePoint struct {
	Day          string  `json:"day" db:"day"`
	AvgScore     float64 `json:"avgScore" db:"avg_score"`
	MessageCount int     `json:"messageCount" db:"message_count"`
}

type LowScoreQuery struct {
	ID        string  `json:"id" db:"id"`
	Content   string  `json:"content" db:"content"`
	CreatedAt string  `json:"createdAt" db:"created_at"`
	AvgScore  float64 `json:"avgScore" db:"avg_score"`
}

type RetrievalQualityStats struct {
	FeedbackStats   []FeedbackCount `json:"feedbackStats"`
	ScoreOverTime   []ScorePoint    `json:"scoreOverTime"`
	LowScoreQueries []LowScoreQuery `json:"lowScoreQueries"`
}

type DashboardData struct {
	Files            FileStats             `json:"files"`
	Activity         ActivityStats         `json:"activity"`
	Chats            ChatStats             `json:"chats"`
	GeneratedContent GeneratedContentStats `json:"generatedContent"`
}

type Store interface {
	GetFileStats(ctx context.Context, kbID string, f Filters) (*FileStats, error)
	GetActivityStats(ctx context.Context, kbID string, f Filters) (*ActivityStats, error)
	GetChatStats(ctx context.Context, kbID string, f Filters) (*ChatStats, error)
	GetGeneratedContentStats(ctx context.Context, kbID string, f Filters) (*GeneratedContentStats, error)
	GetRetrievalQualityStats(ctx context.Context, kbID string, f Filters) (*RetrievalQualityStats, error)
}
