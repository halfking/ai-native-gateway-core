package memory

import "context"

// Message is the unit of conversation persisted to memory backends.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Memory is a single fact returned by a memory backend search.
type Memory struct {
	ID     string
	Text   string
	Tags   []string
	Score  float64
	CubeID string
}

// WriteOp is one async write item sent to a memory sink.
type WriteOp struct {
	UserID   string
	Messages []Message
	Info     map[string]any
	Source   string
}

// Stats reports async sink counters for admin/healthz views.
// The concrete producer lives in the legacy memora writer adapter;
// admin only needs this transport shape.
type Stats struct {
	Enqueued          uint64 `json:"enqueued"`
	Dropped           uint64 `json:"dropped"`
	Processed         uint64 `json:"processed"`
	Errored           uint64 `json:"errored"`
	QueueLen          int    `json:"queue_len"`
	QueueCap          int    `json:"queue_cap"`
	ConsecutiveErrors int64  `json:"consecutive_errors"`
	LastError         string `json:"last_error"`
	LastErrorAt       string `json:"last_error_at"`
	Paused            bool   `json:"paused"`
}

// Reader is the minimum read surface needed by gateway compression and executor paths.
type Reader interface {
	Disabled() bool
	Search(ctx context.Context, userID, query string, topK int) ([]Memory, error)
	SmartSearch(ctx context.Context, userID, query string, topK int) ([]Memory, error)
}

// Writer is the async enqueue surface used by executor write-back.
type Writer interface {
	Enqueue(WriteOp)
}
