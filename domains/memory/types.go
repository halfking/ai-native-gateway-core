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
