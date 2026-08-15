package durable

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrLeaseConflict indicates that a task lease was replaced or the task became terminal.
	ErrLeaseConflict = errors.New("durable task lease conflict")
	// ErrNoTask indicates that no runnable durable task was available for claiming.
	ErrNoTask = errors.New("no durable task available")
	// ErrWorkerNotConfigured indicates that a recovery worker lacks a required dependency.
	ErrWorkerNotConfigured = errors.New("durable recovery worker is not configured")
)

const (
	StatusAccepted        = "accepted"
	StatusRunning         = "running"
	StatusStreaming       = "streaming"
	StatusWaitingRecovery = "waiting_recovery"
	StatusRetryScheduled  = "retry_scheduled"
	StatusCompleted       = "completed"
	StatusPermanentFailed = "permanent_failed"
	StatusExpired         = "expired"
	StatusCancelled       = "cancelled"
	StatusSafetyBlocked   = "resume_safety_blocked"

	CommitNone     = "none"
	CommitMetadata = "metadata"
	CommitContent  = "content"
	CommitToolCall = "tool_call"
	CommitTerminal = "terminal"
)

// DB is the transaction-capable PostgreSQL dependency used by Repository.
type DB interface {
	Begin(context.Context) (pgx.Tx, error)
}

// CreateTaskInput is the immutable, normalized data persisted before a durable attempt begins.
type CreateTaskInput struct {
	ID                 string
	TenantID           string
	RequestID          string
	SessionID          string
	Protocol           string
	Endpoint           string
	SnapshotCiphertext string
	SnapshotVersion    int
	EncryptionKeyID    string
	RequestHash        string
	Policy             json.RawMessage
	DeadlineAt         time.Time
}

// Task is the durable task row and its current lease state.
type Task struct {
	ID, TenantID, RequestID, SessionID string
	Protocol, Endpoint                 string
	SnapshotCiphertext                 string
	SnapshotVersion                    int
	EncryptionKeyID, RequestHash       string
	Status, ErrorKind, ReasonCode      string
	AttemptCount                       int
	NextRetryAt                        time.Time
	DeadlineAt                         time.Time
	LeaseOwner                         string
	LeaseUntil                         *time.Time
	FencingToken                       int64
	SemanticContentCommitted           bool
	CommitState                        string
	CommitMetadata                     json.RawMessage
	ResultCiphertext, ResultObjectRef  string
	ResultHash                         string
	ResultVersion                      int64
	ContentType                        string
	Policy                             json.RawMessage
	ConnectionAttached                 bool
	LastDisconnectAt, ExpiresAt        *time.Time
	CreatedAt, UpdatedAt               time.Time
	CompletedAt                        *time.Time
}

// TerminalResult is the atomically committed result of a durable attempt.
type TerminalResult struct {
	Status           string
	ReasonCode       string
	ErrorKind        string
	ResultCiphertext string
	ResultObjectRef  string
	ResultHash       string
	ResultVersion    int64
	ContentType      string
}

// ExecutionResult directs a recovery worker to reschedule or commit a terminal outcome.
type ExecutionResult struct {
	Reschedule  bool
	NextRetryAt time.Time
	ReasonCode  string
	ErrorKind   string
	Terminal    TerminalResult
}

// Store defines the mutable task operations needed by a recovery worker.
type Store interface {
	Claim(context.Context, string, time.Duration) (*Task, error)
	Renew(context.Context, string, string, int64, time.Duration) (*Task, error)
	Checkpoint(context.Context, string, string, int64, string, json.RawMessage, bool) error
	Reschedule(context.Context, string, string, int64, time.Time, string) error
	CommitTerminal(context.Context, string, string, int64, TerminalResult) error
}

// Reaper defines the durable safety and deadline cleanup operations.
type Reaper interface {
	ReapUnsafeCommitState(context.Context) (int64, error)
	ReapDeadlines(context.Context) (int64, error)
}

// Executor reconstructs and executes a claimed durable task.
type Executor interface {
	Execute(context.Context, *Task) (ExecutionResult, error)
}

// WorkerConfig controls recovery worker polling and lease ownership.
type WorkerConfig struct {
	Owner         string
	PollInterval  time.Duration
	ReapInterval  time.Duration
	LeaseDuration time.Duration
	BatchSize     int
	Now           func() time.Time
}

func (c WorkerConfig) withDefaults() WorkerConfig {
	if c.Owner == "" {
		c.Owner = "recovery-worker"
	}
	if c.PollInterval <= 0 {
		c.PollInterval = time.Second
	}
	if c.ReapInterval <= 0 {
		c.ReapInterval = 10 * time.Second
	}
	if c.LeaseDuration <= 0 {
		c.LeaseDuration = time.Minute
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 16
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}
