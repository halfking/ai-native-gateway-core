package requestjourney

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	defaultObservationOutboxLease        = time.Minute
	defaultObservationOutboxPollInterval = time.Second
	defaultObservationOutboxBatch        = 32
)

var (
	ErrObservationReplayNotDue    = errors.New("request journey observation replay is not due")
	ErrObservationReplayLeaseHeld = errors.New("request journey observation replay lease is held")
)

type observationOutboxDB interface {
	Begin(context.Context) (pgx.Tx, error)
}

type journeyEventTxWriter interface {
	ApplyTx(context.Context, pgx.Tx, JourneyEvent) error
}

type claimedObservation struct {
	ID          int64
	Event       JourneyEvent
	Owner       string
	ClaimUntil  time.Time
	ClaimToken  int64
	Attempts    int
	PayloadHash string
}

// ObservationOutbox commits content-free lifecycle observations before their
// PostgreSQL and Redis projections run. Every worker transaction establishes
// its own RLS context because pooled connections do not preserve request GUCs.
type ObservationOutbox struct {
	db    observationOutboxDB
	pg    journeyEventWriter
	redis journeyEventWriter
	owner string
	clock func() time.Time

	lease        time.Duration
	pollInterval time.Duration
	batch        int

	stopOnce  sync.Once
	stop      chan struct{}
	done      chan struct{}
	workerCtx context.Context
	cancel    context.CancelFunc
}

func newObservationOutbox(db observationOutboxDB, pg, redis journeyEventWriter, owner string) *ObservationOutbox {
	if db == nil || pg == nil {
		return nil
	}
	if owner == "" {
		owner = "request-journey-observer"
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	return &ObservationOutbox{
		db: db, pg: pg, redis: redis, owner: owner, clock: time.Now,
		lease: defaultObservationOutboxLease, pollInterval: defaultObservationOutboxPollInterval,
		batch: defaultObservationOutboxBatch, stop: make(chan struct{}), done: make(chan struct{}),
		workerCtx: workerCtx, cancel: cancel,
	}
}

func (o *ObservationOutbox) start() {
	if o == nil {
		return
	}
	go func() {
		defer close(o.done)
		o.RunOnce(o.workerCtx)
		ticker := time.NewTicker(o.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-o.stop:
				return
			case <-o.workerCtx.Done():
				return
			case <-ticker.C:
				o.RunOnce(o.workerCtx)
			}
		}
	}()
}

func setObservationTenant(ctx context.Context, tx pgx.Tx, tenantID string) error {
	_, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID)
	return err
}

func setObservationWorkerBypass(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_role', 'super_admin', true)`); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT set_config('app.bypass_rls', 'true', true)`)
	return err
}

// Enqueue only returns success after the event was committed to PostgreSQL.
// Exact replays are idempotent; different content at a stable sequence is a
// sequence conflict.
func (o *ObservationOutbox) Enqueue(ctx context.Context, event JourneyEvent) error {
	if o == nil || o.db == nil {
		return errors.New("request journey observation outbox is unavailable")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal request journey observation: %w", err)
	}
	hash := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(hash[:])
	now := o.clock()

	tx, err := o.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin request journey observation enqueue: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := setObservationTenant(ctx, tx, event.TenantID); err != nil {
		return fmt.Errorf("set request journey enqueue tenant: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO request_journey_observation_outbox (
			tenant_id, request_id, seq, payload, payload_hash, next_retry_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4::jsonb, $5, $6, $6, $6)
		ON CONFLICT (tenant_id, request_id, seq) DO NOTHING`,
		event.TenantID, event.RequestID, event.Seq, string(payload), payloadHash, now)
	if err != nil {
		return fmt.Errorf("enqueue request journey observation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var existingHash string
		if err := tx.QueryRow(ctx, `
			SELECT payload_hash
			FROM request_journey_observation_outbox
			WHERE tenant_id=$1 AND request_id=$2 AND seq=$3
			FOR UPDATE`, event.TenantID, event.RequestID, event.Seq).Scan(&existingHash); err != nil {
			return fmt.Errorf("load existing request journey observation: %w", err)
		}
		if existingHash != payloadHash {
			return fmt.Errorf("%w: seq %d", ErrSequenceConflict, event.Seq)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit request journey observation enqueue: %w", err)
	}
	return nil
}

// RunOnce drives due observations and leaves failures durably parked with
// backoff. The global worker is explicitly authorized only transaction-locally.
func (o *ObservationOutbox) RunOnce(ctx context.Context) {
	if o == nil || ctx == nil || ctx.Err() != nil {
		return
	}
	claims, err := o.claim(ctx, "", "", 0, false)
	if err != nil {
		return
	}
	for _, claim := range claims {
		if err := o.deliver(ctx, claim); err != nil {
			_ = o.release(ctx, claim, err)
		}
	}
}

// Replay force-claims a selected failed/pending observation when it is due. An
// active claim and a future retry are explicit errors instead of a silent no-op.
func (o *ObservationOutbox) Replay(ctx context.Context, tenantID, requestID string, seq int64) error {
	if tenantID == "" || requestID == "" || seq <= 0 {
		return errors.New("request journey observation replay requires tenant_id, request_id, and positive seq")
	}
	claims, err := o.claim(ctx, tenantID, requestID, seq, true)
	if err != nil {
		return err
	}
	if len(claims) == 0 {
		return nil
	}
	if err := o.deliver(ctx, claims[0]); err != nil {
		if releaseErr := o.release(ctx, claims[0], err); releaseErr != nil {
			return releaseErr
		}
		return err
	}
	return nil
}

func (o *ObservationOutbox) claim(ctx context.Context, tenantID, requestID string, seq int64, replay bool) ([]claimedObservation, error) {
	now := o.clock()
	tx, err := o.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin request journey observation claim: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := setObservationWorkerBypass(ctx, tx); err != nil {
		return nil, fmt.Errorf("set request journey worker bypass: %w", err)
	}

	query := `
		SELECT id, tenant_id, request_id, seq, payload, payload_hash, attempts, claim_token, status, next_retry_at, claim_until
		FROM request_journey_observation_outbox
		WHERE (
			(status IN ('pending', 'failed') AND next_retry_at <= $1)
			OR (status = 'processing' AND (claim_until IS NULL OR claim_until < $1))
		)`
	args := []any{now}
	if tenantID != "" {
		query += " AND tenant_id=$2 AND request_id=$3 AND seq=$4"
		args = append(args, tenantID, requestID, seq)
	}
	query += " ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT " + fmt.Sprint(o.batch)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("select request journey observation outbox: %w", err)
	}
	defer rows.Close()

	var claims []claimedObservation
	for rows.Next() {
		var (
			claim                       claimedObservation
			storedTenant, storedRequest string
			storedSeq                   int64
			payload                     []byte
			status                      string
			nextRetry, claimUntil       pgtype.Timestamptz
		)
		if err := rows.Scan(&claim.ID, &storedTenant, &storedRequest, &storedSeq, &payload, &claim.PayloadHash, &claim.Attempts, &claim.ClaimToken, &status, &nextRetry, &claimUntil); err != nil {
			return nil, fmt.Errorf("scan request journey observation outbox: %w", err)
		}
		if err := json.Unmarshal(payload, &claim.Event); err != nil {
			return nil, fmt.Errorf("decode request journey observation outbox: %w", err)
		}
		if err := claim.Event.Validate(); err != nil {
			return nil, fmt.Errorf("validate request journey observation outbox: %w", err)
		}
		if claim.Event.TenantID != storedTenant || claim.Event.RequestID != storedRequest || claim.Event.Seq != storedSeq {
			return nil, errors.New("request journey observation outbox identity mismatch")
		}
		if replay && status == "processing" && claimUntil.Valid && claimUntil.Time.After(now) {
			return nil, ErrObservationReplayLeaseHeld
		}
		if replay && status != "processing" && nextRetry.Valid && nextRetry.Time.After(now) {
			return nil, ErrObservationReplayNotDue
		}
		claim.Owner = o.owner
		claim.ClaimUntil = now.Add(o.lease)
		var nextToken int64
		if err := tx.QueryRow(ctx, `
			UPDATE request_journey_observation_outbox
			SET status='processing', claim_owner=$2, claim_until=$3,
				claim_token=claim_token+1, attempts=attempts+1, updated_at=$4
			WHERE id=$1
			RETURNING claim_token`, claim.ID, claim.Owner, claim.ClaimUntil, now).Scan(&nextToken); err != nil {
			return nil, fmt.Errorf("claim request journey observation outbox: %w", err)
		}
		claim.ClaimToken = nextToken
		claim.Attempts++
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate request journey observation outbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit request journey observation claim: %w", err)
	}
	return claims, nil
}

func (o *ObservationOutbox) deliver(ctx context.Context, claim claimedObservation) error {
	writer, ok := o.pg.(journeyEventTxWriter)
	if !ok {
		return errors.New("request journey PostgreSQL transaction writer is unavailable")
	}
	tx, err := o.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin request journey observation projection: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := setObservationTenant(ctx, tx, claim.Event.TenantID); err != nil {
		return fmt.Errorf("set request journey projection tenant: %w", err)
	}
	if err := writer.ApplyTx(ctx, tx, claim.Event); err != nil {
		return fmt.Errorf("persist request journey observation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit request journey observation projection: %w", err)
	}
	if o.redis != nil {
		if err := o.redis.Apply(ctx, claim.Event); err != nil {
			return fmt.Errorf("project request journey observation to redis: %w", err)
		}
	}

	tx, err = o.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin request journey observation ack: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := setObservationTenant(ctx, tx, claim.Event.TenantID); err != nil {
		return fmt.Errorf("set request journey acknowledgement tenant: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM request_journey_observation_outbox
		WHERE id=$1 AND tenant_id=$2 AND claim_owner=$3 AND claim_token=$4 AND status='processing'`,
		claim.ID, claim.Event.TenantID, claim.Owner, claim.ClaimToken)
	if err != nil {
		return fmt.Errorf("ack request journey observation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("request journey observation lease lost before acknowledgement")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit request journey observation acknowledgement: %w", err)
	}
	return nil
}

func (o *ObservationOutbox) release(ctx context.Context, claim claimedObservation, cause error) error {
	now := o.clock()
	delay := 250 * time.Millisecond
	for i := 1; i < claim.Attempts && delay < time.Minute; i++ {
		delay *= 2
	}
	tx, err := o.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin request journey observation retry: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := setObservationTenant(ctx, tx, claim.Event.TenantID); err != nil {
		return fmt.Errorf("set request journey retry tenant: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE request_journey_observation_outbox
		SET status='failed', claim_owner=NULL, claim_until=NULL,
			last_error=$4, next_retry_at=$5, updated_at=$6
		WHERE id=$1 AND tenant_id=$2 AND claim_owner=$3 AND claim_token=$7 AND status='processing'`,
		claim.ID, claim.Event.TenantID, claim.Owner, fmt.Sprint(cause), now.Add(delay), now, claim.ClaimToken)
	if err != nil {
		return fmt.Errorf("release request journey observation retry: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("request journey observation lease lost before retry")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit request journey observation retry: %w", err)
	}
	return nil
}

func (o *ObservationOutbox) Close(ctx context.Context) error {
	if o == nil {
		return nil
	}
	o.stopOnce.Do(func() {
		close(o.stop)
		if o.cancel != nil {
			o.cancel()
		}
	})
	select {
	case <-o.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
