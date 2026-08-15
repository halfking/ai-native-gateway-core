// foreground.go — SR-W3 Wave 2 request-entry durable execution (doc 18 §6/§12).
//
// Foreground owns the durable task lifecycle of one in-flight gateway request:
// CreateAndClaim at request entry (after auth, before any upstream attempt),
// the BeforeSemanticCommit PostgreSQL checkpoint while streaming, and the
// terminal Complete/Fail handoff that also enqueues the PendingStore
// projection outbox row. The lease travels on the request context so deeper
// layers (bridges, executors) can participate without plumbing parameters.
package durabletask

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

// Default foreground pacing (doc 18 §13.1: worker lease 60s, durable deadline
// 24h; the foreground lease mirrors the worker lease shape).
const (
	DefaultForegroundLease     = 90 * time.Second
	DefaultForegroundDeadline  = 24 * time.Hour
	DefaultForegroundResultTTL = 24 * time.Hour
)

// ForegroundConfig sizes the in-request durable task lifecycle.
type ForegroundConfig struct {
	// OwnerPrefix prefixes the lease owner ("<prefix>/<request_id>").
	OwnerPrefix string
	// Lease is the initial foreground lease duration.
	Lease time.Duration
	// Deadline is the durable survival deadline from request entry.
	Deadline time.Duration
	// ResultTTL extends ResultExpiresAt past the deadline.
	ResultTTL time.Duration
}

func (c ForegroundConfig) withDefaults() ForegroundConfig {
	if c.OwnerPrefix == "" {
		c.OwnerPrefix = "gateway"
	}
	if c.Lease <= 0 {
		c.Lease = DefaultForegroundLease
	}
	if c.Deadline <= 0 {
		c.Deadline = DefaultForegroundDeadline
	}
	if c.ResultTTL <= 0 {
		c.ResultTTL = DefaultForegroundResultTTL
	}
	return c
}

// Foreground is the durable execution handle for one gateway request.
type Foreground struct {
	store    *Store
	cfg      ForegroundConfig
	Lease    Lease
	Snapshot DurableRequestSnapshotV1
	// DeadlineAt / ResultExpiresAt are persisted at creation.
	DeadlineAt      time.Time
	ResultExpiresAt time.Time
}

// BeginForeground creates and claims the durable task for a request. A fresh
// TaskID is generated when the snapshot carries none. The returned Foreground
// holds the initial fencing capability for every later write.
func BeginForeground(ctx context.Context, store *Store, snapshot DurableRequestSnapshotV1, cfg ForegroundConfig) (*Foreground, error) {
	if snapshot.TaskID == "" {
		snapshot.TaskID = uuid.NewString()
	}
	cfg = cfg.withDefaults()
	now := time.Now()
	deadline := now.Add(cfg.Deadline)
	lease, err := store.CreateAndClaim(ctx, CreateAndClaimParams{
		Snapshot:        snapshot,
		LeaseOwner:      cfg.OwnerPrefix + "/" + snapshot.RequestID,
		LeaseUntil:      now.Add(cfg.Lease),
		DeadlineAt:      deadline,
		ResultExpiresAt: deadline.Add(cfg.ResultTTL),
	})
	if err != nil {
		return nil, err
	}
	return &Foreground{store: store, cfg: cfg, Lease: lease, Snapshot: snapshot,
		DeadlineAt: deadline, ResultExpiresAt: deadline.Add(cfg.ResultTTL)}, nil
}

// NewForeground re-attaches a handle to an existing lease (used when a
// caller already holds fencing state, e.g. reconnect paths and wiring tests).
func NewForeground(store *Store, lease Lease, cfg ForegroundConfig) *Foreground {
	return &Foreground{store: store, cfg: cfg.withDefaults(), Lease: lease}
}

// Checkpoint persists the write-ahead commit state before semantic bytes
// reach the client. ErrLeaseLost means the fence moved; the caller must not
// flush.
func (f *Foreground) Checkpoint(ctx context.Context, state CommitState) error {
	if f == nil || f.store == nil {
		return nil
	}
	return f.store.Checkpoint(ctx, f.Lease, state)
}

// Complete atomically persists the final response and enqueues the outbox
// projection row.
func (f *Foreground) Complete(ctx context.Context, body []byte, contentType string) (OutboxItem, error) {
	if f == nil || f.store == nil {
		return OutboxItem{}, nil
	}
	return f.store.Complete(ctx, f.Lease, CompleteParams{Body: body, ContentType: contentType})
}

// Fail atomically persists a terminal failure and enqueues the projection.
func (f *Foreground) Fail(ctx context.Context, params FailureParams) (OutboxItem, error) {
	if f == nil || f.store == nil {
		return OutboxItem{}, nil
	}
	return f.store.Fail(ctx, f.Lease, params)
}

// Reschedule releases the foreground lease back to the recovery scheduler
// (used when a detached request should be replayed later rather than
// terminalized).
func (f *Foreground) Reschedule(ctx context.Context, params RescheduleParams) error {
	if f == nil || f.store == nil {
		return nil
	}
	return f.store.Reschedule(ctx, f.Lease, params)
}

// Renew extends the foreground lease (long attempts must renew before the
// recovery worker can steal the task).
func (f *Foreground) Renew(ctx context.Context, until time.Time) error {
	if f == nil || f.store == nil {
		return nil
	}
	return f.store.RenewLease(ctx, f.Lease, until)
}

// RenewNow extends the lease by one configured foreground lease duration —
// the keepalive form used by the request's renewal loop.
func (f *Foreground) RenewNow(ctx context.Context) error {
	if f == nil || f.store == nil {
		return nil
	}
	return f.store.RenewLease(ctx, f.Lease, time.Now().Add(f.cfg.Lease))
}

// foregroundContextKey namespaces the request-context attachment.
type foregroundContextKey struct{}

// Attach returns a context carrying this foreground handle so deeper layers
// can reach the lease without parameter plumbing.
func (f *Foreground) Attach(ctx context.Context) context.Context {
	if f == nil {
		return ctx
	}
	return context.WithValue(ctx, foregroundContextKey{}, f)
}

// ForegroundFromContext retrieves the durable execution handle attached at
// request entry, if any.
func ForegroundFromContext(ctx context.Context) (*Foreground, bool) {
	f, ok := ctx.Value(foregroundContextKey{}).(*Foreground)
	return f, ok && f != nil
}

// CommitStateFromString maps the streaming gate's state names onto the
// durable commit states. ok=false for unknown values so callers fail closed
// (treat unknown as content).
func CommitStateFromString(s string) (CommitState, bool) {
	switch CommitState(s) {
	case CommitNone:
		return CommitNone, true
	case CommitMetadata:
		return CommitMetadata, true
	case CommitContent:
		return CommitContent, true
	case CommitToolCall:
		return CommitToolCall, true
	case CommitTerminal:
		return CommitTerminal, true
	default:
		return CommitNone, false
	}
}

// HashRequestBody computes the request hash used for snapshot AAD binding
// and anti-tamper checks (sha256, hex-encoded).
func HashRequestBody(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
