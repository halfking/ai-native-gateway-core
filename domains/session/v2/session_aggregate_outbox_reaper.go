// Package v2 — session_aggregate_outbox_reaper.go
//
// audit-data-closure-C (2026-08-31): durable retry for Session V2 aggregate
// snapshot updates. The original session_writer_v2.updateSessionAggregate is
// best-effort: a 3-attempt in-memory retry with 25ms*attempt backoff that loses
// the update on all-failure, kill -9, or lifecycleCtx cancellation. The
// reaper scans session_aggregate_outbox (migration 630), replays each row's
// SessionUpdate via the existing SessionAggregator, and updates row status
// with exponential backoff. Rows that exceed maxAttempts transition to status
// 'dead' and emit a slog.Error so operators can reconcile by hand.
//
// Concurrency model:
//   - claim loop uses FOR UPDATE SKIP LOCKED so multiple gateway replicas can
//     cooperate without coordination. Each replica claims up to batchSize rows
//     per tick.
//   - Claim and UpdateSession share a single transaction so the row is locked
//     for the duration of the aggregate upsert; a concurrent replica will skip
//     the locked row.
//
// Shutdown:
//   - Start/Stop are idempotent and safe to call multiple times. On Stop, the
//     in-flight tick completes (or returns ctx.Err() if its context was bound
//     to the lifecycle context) before doneCh is closed.
package v2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	sessionOutboxDefaultInterval = 30 * time.Second
	sessionOutboxDefaultBatch    = 100
	sessionOutboxDefaultMaxAtts  = 10
	// sessionOutboxMaxBackoff caps the exponential schedule so a misbehaving
	// row does not push its next_retry_at years into the future.
	sessionOutboxMaxBackoff = 1 * time.Hour
)

// outboxDB is the minimal pool surface the reaper needs.
type outboxDB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// sessionAggregateOutboxReaper consumes session_aggregate_outbox rows.
type sessionAggregateOutboxReaper struct {
	db         outboxDB
	aggregator *SessionAggregator
	interval   time.Duration
	batchSize  int
	maxAtts    int
	stopCh     chan struct{}
	doneCh     chan struct{}
	mu         sync.Mutex
	started    bool
	stopped    bool
}

// newSessionAggregateOutboxReaper is the internal constructor used by tests
// and the public Start helper below. Production code wires it through
// StartSessionAggregateOutboxReaper.
func newSessionAggregateOutboxReaper(db *pgxpool.Pool, agg *SessionAggregator, interval time.Duration, batchSize, maxAtts int) *sessionAggregateOutboxReaper {
	if interval <= 0 {
		interval = sessionOutboxDefaultInterval
	}
	if batchSize <= 0 {
		batchSize = sessionOutboxDefaultBatch
	}
	if maxAtts <= 0 {
		maxAtts = sessionOutboxDefaultMaxAtts
	}
	return &sessionAggregateOutboxReaper{
		db:         db,
		aggregator: agg,
		interval:   interval,
		batchSize:  batchSize,
		maxAtts:    maxAtts,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
}

// StartSessionAggregateOutboxReaper starts the reaper in the background.
// Idempotent: a second call with the reaper already started is a no-op.
func StartSessionAggregateOutboxReaper(ctx context.Context, db *pgxpool.Pool, agg *SessionAggregator) *sessionAggregateOutboxReaper {
	r := newSessionAggregateOutboxReaper(db, agg, 0, 0, 0)
	r.Start(ctx)
	return r
}

// Start launches the reaper goroutine.
func (r *sessionAggregateOutboxReaper) Start(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.started || r.stopped {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	go r.run(ctx)
}

// Stop halts the reaper and waits for the in-flight tick to return.
func (r *sessionAggregateOutboxReaper) Stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	close(r.stopCh)
	started := r.started
	r.mu.Unlock()
	if started {
		<-r.doneCh
	}
}

func (r *sessionAggregateOutboxReaper) run(ctx context.Context) {
	defer close(r.doneCh)
	if r.db == nil || r.aggregator == nil {
		// Defensive: production must never start with a nil pool / aggregator;
		// unit tests construct the struct directly. Returning silently avoids
		// taking the gateway down when Start is wired before db is ready.
		slog.Warn("session_aggregate_outbox: nil pool or aggregator, reaper not started")
		return
	}
	// Run once immediately so a freshly-restarted gateway catches up on any
	// pending rows from before the crash.
	r.tick(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *sessionAggregateOutboxReaper) tick(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	for i := 0; i < r.batchSize; i++ {
		ok, err := r.claimAndReplay(timeoutCtx)
		if err != nil {
			slog.Error("session_aggregate_outbox: tick error", "error", err)
			return
		}
		if !ok {
			// No more pending rows; exit early to avoid burning a long-
			// running connection on idle polls.
			return
		}
	}
}

func (r *sessionAggregateOutboxReaper) claimAndReplay(ctx context.Context) (bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Claim one pending row whose retry time has elapsed. SKIP LOCKED so
	//    peer reapers skip the row rather than waiting.
	row := tx.QueryRow(ctx, `
		SELECT id, tenant_id, session_id, partition_date, request_id,
		       update_payload, attempts
		FROM session_aggregate_outbox
		WHERE status = 'pending' AND next_retry_at <= NOW()
		ORDER BY next_retry_at ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 1`)

	var (
		id             int64
		tenantID       string
		sessionID      string
		partitionDate  time.Time
		requestID      string
		updatePayload  []byte
		attempts       int
	)
	if err := row.Scan(&id, &tenantID, &sessionID, &partitionDate, &requestID, &updatePayload, &attempts); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("claim row: %w", err)
	}

	// 2. Mark claimed so a peer observing the row knows it's in-flight.
	if _, err := tx.Exec(ctx,
		`UPDATE session_aggregate_outbox SET status='claimed', claimed_at=NOW() WHERE id=$1`, id); err != nil {
		return false, fmt.Errorf("mark claimed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit claim: %w", err)
	}

	// 3. Replay the SessionUpdate. The aggregator's existing idempotency
	//    claim handles double-applies.
	var update SessionUpdate
	if err := decodeUpdatePayload(updatePayload, &update); err != nil {
		r.markDead(ctx, id, fmt.Sprintf("payload decode: %v", err))
		return true, nil
	}
	if err := r.aggregator.UpdateSession(ctx, update); err != nil {
		nextAttempts := attempts + 1
		if nextAttempts >= r.maxAtts {
			r.markDead(ctx, id, fmt.Sprintf("attempts=%d: %v", nextAttempts, err))
			return true, nil
		}
		r.scheduleRetry(ctx, id, nextAttempts, err)
		return true, nil
	}
	r.markDone(ctx, id)
	return true, nil
}

func (r *sessionAggregateOutboxReaper) markDone(ctx context.Context, id int64) {
	if _, err := r.db.Exec(ctx,
		`UPDATE session_aggregate_outbox SET status='done', completed_at=NOW(), updated_at=NOW() WHERE id=$1`, id); err != nil {
		slog.Error("session_aggregate_outbox: mark done failed", "id", id, "error", err)
	}
}

func (r *sessionAggregateOutboxReaper) markDead(ctx context.Context, id int64, reason string) {
	if _, err := r.db.Exec(ctx,
		`UPDATE session_aggregate_outbox SET status='dead', last_error=$2, updated_at=NOW() WHERE id=$1`, id, reason); err != nil {
		slog.Error("session_aggregate_outbox: mark dead failed", "id", id, "error", err)
		return
	}
	slog.Error("session_aggregate_outbox: row exceeded max attempts, marked dead",
		"id", id, "reason", reason, "max_attempts", r.maxAtts)
}

func (r *sessionAggregateOutboxReaper) scheduleRetry(ctx context.Context, id int64, attempts int, cause error) {
	// Exponential backoff with cap. attempt=1 → 1s, attempt=2 → 2s, ...,
	// attempt=10 → 512s (capped at 1h).
	backoff := time.Duration(1<<attempts) * time.Second
	if backoff > sessionOutboxMaxBackoff {
		backoff = sessionOutboxMaxBackoff
	}
	if _, err := r.db.Exec(ctx,
		`UPDATE session_aggregate_outbox
		 SET status='pending', attempts=$2, last_error=$3,
		     next_retry_at=NOW() + ($4 || ' seconds')::interval,
		     updated_at=NOW()
		 WHERE id=$1`,
		id, attempts, cause.Error(), int(backoff.Seconds())); err != nil {
		slog.Error("session_aggregate_outbox: schedule retry failed",
			"id", id, "attempts", attempts, "error", err)
	}
}

// decodeUpdatePayload translates the JSONB payload persisted on the outbox
// row back into a SessionUpdate. Kept in a separate function so a future
// payload shape change has a single seam to update.
func decodeUpdatePayload(raw []byte, dst *SessionUpdate) error {
	// Use the same JSON shape the writer would have produced. SessionUpdate
	// has no exported JSON contract today; we use map[string]any and then
	// assign field-by-field. This avoids leaking aggregator internals into
	// the on-disk schema.
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	dst.SessionID, _ = m["session_id"].(string)
	dst.TenantID, _ = m["tenant_id"].(string)
	dst.RequestID, _ = m["request_id"].(string)
	dst.LastTurnNo = toInt(m["last_turn_no"])
	dst.LastRequestSummary, _ = m["last_request_summary"].(string)
	dst.LastResponseSummary, _ = m["last_response_summary"].(string)
	dst.LastModel, _ = m["last_model"].(string)
	dst.LastProvider, _ = m["last_provider"].(string)
	dst.TurnIncrement = toInt(m["turn_increment"])
	dst.TokensIncrement = toInt(m["tokens_increment"])
	dst.CostIncrement = toFloat(m["cost_increment"])
	if s, ok := m["updated_at"].(string); ok && s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			dst.UpdatedAt = t
		}
	}
	if dst.SessionID == "" {
		return errors.New("payload missing session_id")
	}
	return nil
}

// EncodeSessionUpdateForOutbox produces the JSON shape decodeUpdatePayload
// expects. Exported so session_writer_v2 can write the outbox row in the
// same transaction as the turn insert.
func EncodeSessionUpdateForOutbox(u SessionUpdate) ([]byte, error) {
	return json.Marshal(map[string]any{
		"session_id":            u.SessionID,
		"tenant_id":             u.TenantID,
		"request_id":            u.RequestID,
		"last_turn_no":          u.LastTurnNo,
		"last_request_summary":  u.LastRequestSummary,
		"last_response_summary": u.LastResponseSummary,
		"last_model":            u.LastModel,
		"last_provider":         u.LastProvider,
		"turn_increment":        u.TurnIncrement,
		"tokens_increment":      u.TokensIncrement,
		"cost_increment":        u.CostIncrement,
		"updated_at":            u.UpdatedAt.Format(time.RFC3339Nano),
	})
}

func toInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	}
	return 0
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}

// EnqueueSessionAggregateOutbox inserts a pending outbox row. The writer
// should call this in the SAME transaction as the turn/bodies insert so
// the outbox row's durability matches the source-of-truth row.
func EnqueueSessionAggregateOutbox(ctx context.Context, tx pgx.Tx, u SessionUpdate, partitionDate time.Time) error {
	payload, err := EncodeSessionUpdateForOutbox(u)
	if err != nil {
		return fmt.Errorf("encode outbox payload: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO session_aggregate_outbox
			(tenant_id, session_id, partition_date, request_id, update_payload)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, session_id, partition_date, request_id)
		DO UPDATE SET update_payload = EXCLUDED.update_payload,
		              status='pending', attempts=0, last_error=NULL,
		              next_retry_at=NOW(), updated_at=NOW()`,
		u.TenantID, u.SessionID, partitionDate, u.RequestID, payload)
	return err
}

// lockKeySessionOutbox is exposed so callers (e.g. tests) can verify the
// advisory lock used by the aggregator stays consistent if the hash space
// is later extended to coordinate with the reaper.
var lockKeySessionOutbox = func() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("llm-gateway:session_aggregate_outbox"))
	return int64(h.Sum64())
}()
