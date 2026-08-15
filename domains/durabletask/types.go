package durabletask

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Status is a durable task scheduler state.
type Status string

const (
	// StatusAccepted is the append-only origin state used by the creation event.
	StatusAccepted Status = "accepted"
	// StatusRunning identifies a task held by an active lease.
	StatusRunning Status = "running"
	// StatusWaitingRecovery identifies a task waiting for an external recovery condition.
	StatusWaitingRecovery Status = "waiting_recovery"
	// StatusRetryScheduled identifies a task runnable at its next retry time.
	StatusRetryScheduled Status = "retry_scheduled"
	// StatusCompleted identifies a successful terminal task.
	StatusCompleted Status = "completed"
	// StatusPermanentFailed identifies an unrecoverable terminal task.
	StatusPermanentFailed Status = "permanent_failed"
	// StatusExpired identifies a task reaped after its survival deadline.
	StatusExpired Status = "expired"
	// StatusCancelled identifies an explicitly cancelled terminal task.
	StatusCancelled Status = "cancelled"
	// StatusResumeSafetyBlocked identifies a terminal task that cannot be replayed safely.
	StatusResumeSafetyBlocked Status = "resume_safety_blocked"
)

// CommitState records the strongest semantic frame durably committed before network output.
type CommitState string

const (
	// CommitNone means no response semantics have been committed.
	CommitNone CommitState = "none"
	// CommitMetadata means only replay-safe metadata has been committed.
	CommitMetadata CommitState = "metadata"
	// CommitContent means response content has been committed.
	CommitContent CommitState = "content"
	// CommitToolCall means a potentially side-effecting tool call has been committed.
	CommitToolCall CommitState = "tool_call"
	// CommitTerminal means a terminal protocol frame has been committed.
	CommitTerminal CommitState = "terminal"
)

const (
	// ReasonCreated records initial durable task acceptance.
	ReasonCreated = "durable_accepted"
	// ReasonLeaseClaimed records a recovery worker claim.
	ReasonLeaseClaimed = "lease_claimed"
	// ReasonSurvivalExpired is the stable deadline-reaper reason.
	ReasonSurvivalExpired = "survival_expired"
	// ReasonResumeSafetyBlocked is the stable unsafe-resume reason.
	ReasonResumeSafetyBlocked = "resume_safety_blocked"
)

var (
	// ErrLeaseLost reports a fenced write whose task, owner, or fencing token no longer matches.
	ErrLeaseLost = errors.New("durabletask: lease lost")
	// ErrInvalidTerminalStatus reports a non-terminal status supplied to Fail.
	ErrInvalidTerminalStatus = errors.New("durabletask: invalid terminal failure status")
)

// DB is the pgx pool surface required by Store. Both pgxpool.Pool and pgxmock pools satisfy it.
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Lease is the fencing capability required for task mutations.
type Lease struct {
	TaskID       string
	Owner        string
	FencingToken int64
	LeaseUntil   time.Time
}

// ClaimedTask is a task returned to a recovery worker with its new lease.
type ClaimedTask struct {
	Lease
	TenantID           string
	RequestID          string
	SessionID          string
	RequestHash        string
	SnapshotCiphertext string
	SnapshotVersion    int
	EncryptionKeyID    string
	AttemptCount       int
	DeadlineAt         time.Time
	CommitState        CommitState
}

// Event is an append-only durable task transition.
type Event struct {
	TaskID       string
	TenantID     string
	RequestID    string
	SessionID    string
	AttemptCount int
	FromStatus   Status
	ToStatus     Status
	ReasonCode   string
	FencingToken int64
	CreatedAt    time.Time
}

// OutboxItem is a terminal PendingStore projection awaiting delivery.
type OutboxItem struct {
	ID            int64
	TaskID        string
	TenantID      string
	RequestID     string
	SessionID     string
	Status        Status
	ReasonCode    string
	FencingToken  int64
	ResultVersion int64
	ResultHash    string
	// RequestHash binds the result decryption AAD (durable-result domain).
	RequestHash      string
	ResultCiphertext string
	ContentType      string
	AttemptCount     int
	CreatedAt        time.Time
}
