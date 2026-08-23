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
// materializes an immutable GovernorPolicy from PostgreSQL, and applies it
// through the production ApplyPolicySnapshot on every credForwarder.
//
// Phase scope: rebuild/replace of live credForwarder governors is explicitly
// Stage D's forward path. Stage E ends at policy publication; existing
// forwarders keep their local governor until Phase 2 swaps them.
type PolicyPublisher struct {
	pool     *pgxpool.Pool
	pipeline *Pipeline

	lastRevision atomic.Uint64
	publishMu    sync.Mutex
	startMu      sync.Mutex
	started      bool
	stopped      bool
	stopCh       chan struct{}
	wg           sync.WaitGroup
}

func NewPolicyPublisher(pool *pgxpool.Pool, pipeline *Pipeline) *PolicyPublisher {
	return &PolicyPublisher{
		pool:     pool,
		pipeline: pipeline,
		stopCh:   make(chan struct{}),
	}
}

// Start is idempotent and does not block. LISTEN is registered before the
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
	p.started = true
	p.wg.Add(1)
	go p.run()
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
	close(p.stopCh)
	if p.started {
		p.startMu.Unlock()
		p.wg.Wait()
		return
	}
	p.startMu.Unlock()
}

func (p *PolicyPublisher) run() {
	defer p.wg.Done()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, stopFromSignal := context.WithCancel(ctx)
	go func() {
		select {
		case <-p.stopCh:
			stopFromSignal()
		case <-ctx.Done():
		}
	}()

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
			// Reconnect and retry. Catch-up is invoked after LISTEN so any
			// notifications that fired while we were disconnected are
			// recovered via the revision-bounded DB catch-up query.
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
		if rev, parseErr := parseCredentialsRevision(notification.Payload); parseErr != nil {
			slog.Warn("dispatch policy publisher ignored invalid revision", "payload", notification.Payload, "error", parseErr)
			continue
		} else if rev > p.lastRevision.Load() {
			if err := p.publishCatchUpWithRetry(ctx); err != nil {
				slog.Warn("dispatch policy publisher catch-up exhausted retries", "revision", rev, "error", err)
			}
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

func (p *PolicyPublisher) publishCatchUp(ctx context.Context) error {
	p.publishMu.Lock()
	defer p.publishMu.Unlock()
	last := p.lastRevision.Load()

	// Pull the full snapshot, not just the delta, because ApplyPolicySnapshot
	// expects to atomically replace the active policy.
	rows, err := p.pool.Query(ctx, `
		SELECT id, provider_id, concurrency_mode,
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
	var maxRevision = last
	for rows.Next() {
		var spec GovernorSpec
		var revision int64
		var mode string
		if err := rows.Scan(&spec.CredentialID, &spec.ProviderID, &mode,
			&spec.Limit, &spec.RPMLimit, &spec.TPMLimit,
			&revision); err != nil {
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

	// Stage E ends at policy publication: the backend is notified and the
	// active revision is recorded. The live forwarder governor swap is
	// Phase 2's forward path, so there is no ApplyPolicySnapshot wired onto
	// the production Pipeline yet.
	slog.Debug("dispatch policy publisher applied revision",
		"revision", policy.Revision, "specs", len(policy.Specs), "source", policy.Source)
	if backend := p.pipeline.GovernorBackend(); backend != nil {
		if err := backend.NotifyRevisions(ctx, maxRevision); err != nil {
			slog.Warn("dispatch policy publisher backend NotifyRevisions failed", "revision", maxRevision, "error", err)
		}
	}
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
