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
)

const (
	defaultObservationOutboxLease        = time.Minute
	defaultObservationOutboxPollInterval = time.Second
	defaultObservationOutboxBatch        = 32
)

// observationOutboxDB is deliberately the small transactional surface needed
// to make a RequestJourney observation durable before its projections run.
type observationOutboxDB interface {
	Begin(context.Context) (pgx.Tx, error)
}

type claimedObservation struct {
	ID          int64
	Event       JourneyEvent
	Owner       string
	ClaimUntil  time.Time
	Attempts    int
	PayloadHash string
}

// ObservationOutbox is a PostgreSQL-backed write-ahead queue for journey
// events. The event reaches the outbox before the asynchronous PostgreSQL and
// Redis projections; the row remains until both idempotent projections succeed.
type ObservationOutbox struct {
	db    observationOutboxDB
	pg    journeyEventWriter
	redis journeyEventWriter
	owner string
	clock func() time.Time

	lease        time.Duration
	pollInterval time.Duration
	batch        int

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

func newObservationOutbox(db observationOutboxDB, pg, redis journeyEventWriter, owner string) *ObservationOutbox {
	if db == nil || pg == nil {
		return nil
	}
	if owner == "" {
		owner = "request-journey-observer"
	}
	return &ObservationOutbox{
		db: db, pg: pg, redis: redis, owner: owner, clock: time.Now,
		lease: defaultObservationOutboxLease, pollInterval: defaultObservationOutboxPollInterval,
		batch: defaultObservationOutboxBatch, stop: make(chan struct{}), done: make(chan struct{}),
	}
}

func (o *ObservationOutbox) start() {
	if o == nil {
		return
	}
	go func() {
		defer close(o.done)
		o.RunOnce(context.Background())
		ticker := time.NewTicker(o.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-o.stop:
				return
			case <-ticker.C:
				o.RunOnce(context.Background())
			}
		}
	}()
}

// Enqueue commits the durable event before it is reported as accepted. Exact
// replays are idempotent; a different body at the same journey sequence is a
// data conflict rather than an event to silently discard.
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

	tag, err := tx.Exec(ctx, `
		INSERT INTO request_journey_observation_outbox (
			tenant_id, request_id, seq, payload, payload_hash, next_retry_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $6, $6)
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

// RunOnce claims all currently due observations up to the configured batch and
// drives their PostgreSQL then Redis projections. Errors leave a durable row
// with backoff for a later process to retry.
func (o *ObservationOutbox) RunOnce(ctx context.Context) {
	if o == nil {
		return
	}
	claims, err := o.claim(ctx, "", "", 0)
	if err != nil {
		return
	}
	for _, claim := range claims {
		if err := o.deliver(ctx, claim); err != nil {
			_ = o.release(ctx, claim, err)
		}
	}
}

// Replay claims a specific durable observation using the same lease and
// delivery path as normal recovery. Empty selectors are rejected to prevent a
// broad accidental replay.
func (o *ObservationOutbox) Replay(ctx context.Context, tenantID, requestID string, seq int64) error {
	if tenantID == "" || requestID == "" || seq <= 0 {
		return errors.New("request journey observation replay requires tenant_id, request_id, and positive seq")
	}
	claims, err := o.claim(ctx, tenantID, requestID, seq)
	if err != nil {
		return err
	}
	if len(claims) == 0 {
		return nil
	}
	if err := o.deliver(ctx, claims[0]); err != nil {
		_ = o.release(ctx, claims[0], err)
		return err
	}
	return nil
}

func (o *ObservationOutbox) claim(ctx context.Context, tenantID, requestID string, seq int64) ([]claimedObservation, error) {
	now := o.clock()
	tx, err := o.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin request journey observation claim: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	query := `
		SELECT id, payload, payload_hash, attempts
		FROM request_journey_observation_outbox
		WHERE (
			(status IN ('pending', 'failed') AND next_retry_at <= $1)
			OR (status = 'processing' AND claim_until < $1)
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
		var claim claimedObservation
		var payload []byte
		if err := rows.Scan(&claim.ID, &payload, &claim.PayloadHash, &claim.Attempts); err != nil {
			return nil, fmt.Errorf("scan request journey observation outbox: %w", err)
		}
		if err := json.Unmarshal(payload, &claim.Event); err != nil {
			return nil, fmt.Errorf("decode request journey observation outbox: %w", err)
		}
		if err := claim.Event.Validate(); err != nil {
			return nil, fmt.Errorf("validate request journey observation outbox: %w", err)
		}
		claim.Owner = o.owner
		claim.ClaimUntil = now.Add(o.lease)
		tag, err := tx.Exec(ctx, `
			UPDATE request_journey_observation_outbox
			SET status='processing', claim_owner=$2, claim_until=$3,
				attempts=attempts+1, updated_at=$4
			WHERE id=$1`, claim.ID, claim.Owner, claim.ClaimUntil, now)
		if err != nil {
			return nil, fmt.Errorf("claim request journey observation outbox: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return nil, fmt.Errorf("request journey observation claim lost for id %d", claim.ID)
		}
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
	if err := o.pg.Apply(ctx, claim.Event); err != nil {
		return fmt.Errorf("persist request journey observation: %w", err)
	}
	if o.redis != nil {
		if err := o.redis.Apply(ctx, claim.Event); err != nil {
			return fmt.Errorf("project request journey observation to redis: %w", err)
		}
	}
	tx, err := o.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin request journey observation ack: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	tag, err := tx.Exec(ctx, `
		DELETE FROM request_journey_observation_outbox
		WHERE id=$1 AND claim_owner=$2 AND status='processing'`, claim.ID, claim.Owner)
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
	tag, err := tx.Exec(ctx, `
		UPDATE request_journey_observation_outbox
		SET status='failed', claim_owner=NULL, claim_until=NULL,
			last_error=$3, next_retry_at=$4, updated_at=$5
		WHERE id=$1 AND claim_owner=$2 AND status='processing'`,
		claim.ID, claim.Owner, fmt.Sprint(cause), now.Add(delay), now)
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
	o.stopOnce.Do(func() { close(o.stop) })
	select {
	case <-o.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
