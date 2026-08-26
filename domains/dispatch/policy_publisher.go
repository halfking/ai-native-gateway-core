package dispatch

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const credentialsRevisionChannel = "credentials_revision"

// PolicyPublisher listens for globally ordered credential governor revisions,
// materializes GovernorPolicy metadata from PostgreSQL, and publishes the
// active revision to the selected backend.
//
// Stage F scope: publication produces a GovernorPolicy and hands it to
// Pipeline.ApplyPolicy, which is the production implementation behind
// policy_applier.ApplyPolicySnapshot. The publisher is responsible only
// for: (1) accepting NOTIFY from the credentials_revision channel,
// (2) loading the credential delta since the last revision, (3)
// calling ApplyPolicy with the materialized policy. The local
// credForwarder governor swap, the active revision stamp, and the
// backend NotifyRevisions round-trip all live inside ApplyPolicy —
// keeping fail-closed behaviour in one place.
type PolicyPublisher struct {
	pool     *pgxpool.Pool
	pipeline *Pipeline

	lastRevision atomic.Uint64
	publishMu    sync.Mutex
	startMu      sync.Mutex
	started      bool
	stopped      bool
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

func NewPolicyPublisher(pool *pgxpool.Pool, pipeline *Pipeline) *PolicyPublisher {
	return &PolicyPublisher{
		pool:     pool,
		pipeline: pipeline,
	}
}

// Start is idempotent and does not block. Cancelling the parent context
// stops the publisher, as does Stop. LISTEN is registered before the
// first catch-up so a revision committed during catch-up cannot be missed.
func (p *PolicyPublisher) Start(parent context.Context) {
	if p == nil || p.pool == nil || p.pipeline == nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if p.started || p.stopped {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	p.started = true
	p.wg.Add(1)
	go p.run(ctx)
}

// Stop is safe before Start and on repeated calls.
func (p *PolicyPublisher) Stop() {
	if p == nil {
		return
	}
	p.startMu.Lock()
	if p.stopped {
		p.startMu.Unlock()
		return
	}
	p.stopped = true
	if p.cancel != nil {
		p.cancel()
	}
	started := p.started
	p.startMu.Unlock()
	if started {
		p.wg.Wait()
	}
}

func (p *PolicyPublisher) run(ctx context.Context) {
	defer p.wg.Done()

	// Acquire the dedicated LISTEN connection first so notifications fired
	// during the initial catch-up are buffered by PostgreSQL and applied
	// after we replay the snapshot.
	conn, err := p.acquireListenConn(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Warn("dispatch policy publisher initial LISTEN acquire failed", "error", err)
		return
	}
	defer conn.Release()

	if err := p.publishCatchUp(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("dispatch policy publisher initial catch-up failed", "error", err)
	}

	for {
		if ctx.Err() != nil {
			return
		}
		notification, waitErr := conn.Conn().WaitForNotification(ctx)
		if waitErr != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("dispatch policy publisher notification wait failed", "error", waitErr)
			// Reconnect and retry. Catch-up runs after LISTEN so any
			// notifications that fired while disconnected are recovered
			// via the revision-bounded DB catch-up query.
			newConn, retryErr := p.acquireListenConn(ctx)
			if retryErr != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Warn("dispatch policy publisher reconnect failed", "error", retryErr)
				if !sleepContext(ctx, time.Second) {
					return
				}
				continue
			}
			conn.Release()
			conn = newConn
			if err := p.publishCatchUp(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("dispatch policy publisher reconnect catch-up failed", "error", err)
			}
			continue
		}
		rev, parseErr := parseCredentialsRevision(notification.Payload)
		if parseErr != nil {
			slog.Warn("dispatch policy publisher ignored invalid revision", "payload", notification.Payload, "error", parseErr)
			continue
		}
		if rev <= p.lastRevision.Load() {
			continue
		}
		if err := p.publishCatchUpWithRetry(ctx); err != nil {
			slog.Warn("dispatch policy publisher catch-up exhausted retries", "revision", rev, "error", err)
		}
	}
}

func (p *PolicyPublisher) acquireListenConn(ctx context.Context) (*pgxpool.Conn, error) {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, "LISTEN "+credentialsRevisionChannel); err != nil {
		conn.Release()
		return nil, err
	}
	return conn, nil
}

// publishCatchUpWithRetry performs bounded backoff retries for transient
// catch-up/backend failures while preserving the last known good revision.
func (p *PolicyPublisher) publishCatchUpWithRetry(ctx context.Context) error {
	const maxAttempts = 4
	backoff := 250 * time.Millisecond
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := p.publishCatchUp(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !sleepContext(ctx, backoff) {
			return ctx.Err()
		}
		backoff *= 2
	}
	return lastErr
}

// publishCatchUp loads every credential whose revision is at or above the
// last published revision. On the initial catch-up (lastRevision == 0)
// this is the complete credential set; afterwards it is the delta since
// the last publication. Because Stage E has no live policy applier, the
// resulting GovernorPolicy is publication metadata: the backend is
// notified and the pipeline's active revision stamp is advanced.
func (p *PolicyPublisher) publishCatchUp(ctx context.Context) error {
	p.publishMu.Lock()
	defer p.publishMu.Unlock()
	last := p.lastRevision.Load()

	rows, err := p.pool.Query(ctx, `
		SELECT id, provider_id, COALESCE(concurrency_mode, 'concurrency'),
		       COALESCE(concurrency_limit, 0), COALESCE(rpm_limit, 0),
		       COALESCE(tpm_limit, 0), revision
		FROM public.credentials
		WHERE revision >= $1
		ORDER BY revision, id`, last)
	if err != nil {
		return err
	}
	defer rows.Close()

	var specs []GovernorSpec
	maxRevision := last
	for rows.Next() {
		var spec GovernorSpec
		var revision int64
		var mode string
		if err := rows.Scan(&spec.CredentialID, &spec.ProviderID, &mode,
			&spec.RPMLimit, &spec.Limit, &spec.TPMLimit, &revision); err != nil {
			return err
		}
		spec.Mode = normalizeConcurrencyMode(mode)
		spec.Limit = selectLimitForMode(spec.Mode, spec.Limit, spec.RPMLimit, spec.TPMLimit)
		spec.Revision = uint64(revision)
		spec.Backend = p.backendKind()
		specs = append(specs, spec)
		if uint64(revision) > maxRevision {
			maxRevision = uint64(revision)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if maxRevision == last {
		return nil
	}

	// Defensive copy so the immutable GovernorPolicy contract holds even if
	// a future caller reuses the specs slice.
	immutableSpecs := make([]GovernorSpec, len(specs))
	copy(immutableSpecs, specs)
	policy := GovernorPolicy{
		Revision:    maxRevision,
		GeneratedAt: time.Now(),
		Specs:       immutableSpecs,
		Source:      "pg_notify",
	}

	// Backend notification is fail-closed: a NotifyRevisions failure keeps
	// lastRevision where it was, so publishCatchUpWithRetry (or the next
	// notification) replays the same delta instead of skipping it.
	backend := p.pipeline.GovernorBackend()
	if backend != nil {
		if err := backend.NotifyRevisions(ctx, maxRevision); err != nil {
			return fmt.Errorf("notify backend revision %d: %w", maxRevision, err)
		}
	}
	slog.Debug("dispatch policy publisher applied revision",
		"revision", policy.Revision, "specs", len(policy.Specs), "source", policy.Source)
	p.pipeline.SetActivePolicyRevision(maxRevision)
	p.lastRevision.Store(maxRevision)
	return nil
}

func (p *PolicyPublisher) backendKind() GovernorBackendKind {
	if backend := p.pipeline.GovernorBackend(); backend != nil {
		return backend.Kind()
	}
	return BackendLocal
}

func parseCredentialsRevision(payload string) (uint64, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return 0, fmt.Errorf("empty revision")
	}
	rev, err := strconv.ParseUint(payload, 10, 64)
	if err != nil || rev == 0 {
		if err == nil {
			err = fmt.Errorf("revision must be positive")
		}
		return 0, err
	}
	return rev, nil
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// normalizeConcurrencyMode coerces legacy NULL/empty values to the default
// mode so policy publishers always carry a stable enum.
func normalizeConcurrencyMode(mode string) string {
	if mode == "" {
		return ModeConcurrency
	}
	return mode
}

// selectLimitForMode maps the mode-specific value into GovernorSpec.Limit.
func selectLimitForMode(mode string, concurrencyLimit, rpmLimit, tpmLimit int) int {
	switch mode {
	case ModeRPM:
		return rpmLimit
	case ModeTPM:
		return tpmLimit
	case ModeDisabled:
		return 0
	default:
		return concurrencyLimit
	}
}
