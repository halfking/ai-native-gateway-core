// Package credential — Auto-revoke on repeated 401/403 (T3.1 of the 154-server
// audit plan, applied as a hardening layer on top of Writer.WriteOnError).
//
// Background
// The existing writer (writer.go) flips credentials.availability_state to
// 'auth_failed' on each KindAuth and to 'suspended' on KindAuthRevoked. The
// state-machine's recovery ticker (bg/credential_recovery.go) flips the state
// back to 'ready' once availability_recover_at elapses — currently 15 minutes
// for KindAuth. If the upstream is permanently broken (key revoked / account
// banned / region restricted), the recovery ticker just keeps flipping
// availability_state auth_failed → ready → auth_failed → ready forever, and
// every cycle costs the gateway one 401 response per attempt.
//
// This file implements a one-shot escalation: once a credential has cycled
// through N auth-failed recoveries within a window, AutoRevoker flips
// manual_disabled=TRUE on the credential (writing a distinctive state_reason
// so operators can distinguish admin revocations from auto revocations) and
// from then on the regular WriteOnError cooldown path is skipped because the
// WHERE clause in writer.go includes
//   AND COALESCE(manual_disabled, FALSE) = FALSE
//
// Safe-by-construction behaviour:
//   - The auto-revoke sweep is OFF by default. Enable with
//     LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE=on (singleton env var; LLM Gateway
//     boot/cmd flag is not in this commit).
//   - It only ever sets manual_disabled=TRUE; it never sets it to FALSE.
//     Re-enabling a credential is always a human action — the same way
//     manual_disabled was originally designed.
//   - Sweep runs in a background ticker (default every 5 minutes), never
//     on the request hot path. Writers continue to record auth_failed state
//     during the window; the auto-revoke is a *post-condition*.
//   - Already-disabled credentials are skipped (no-op UPDATE).
//
// Configuration knobs (env vars):
//   LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE=on|off         — kill switch
//   LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE_THRESHOLD=3     — N auth_failed cycles
//   LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE_WINDOW_HOURS=24 — cycle-count window
//   LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE_INTERVAL=5m    — sweep cadence
package credential

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AutoRevokeDB is the minimal DB surface AutoRevoker needs. It mirrors
// credentialRecoveryDB so we can swap in pgxmock for unit tests.
type AutoRevokeDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// AutoRevokeConfig captures the runtime tunables. Zero values fall back to
// safe defaults (see DefaultAutoRevokeConfig).
//
// Treat as immutable after NewAutoRevoker.
type AutoRevokeConfig struct {
	// Threshold is the number of auth_failed cycles within Window that
	// triggers manual_disabled=TRUE. Must be >= 1.
	Threshold int
	// Window is the rolling time window over which we count cycles.
	Window time.Duration
	// Interval is the cadence of the background sweep.
	Interval time.Duration
	// Enabled is the kill switch. When false, Start is a no-op.
	Enabled bool
}

// DefaultAutoRevokeConfig is the safe baseline: 3 cycles in 24h, sweeping
// every 5 minutes, off by default.
func DefaultAutoRevokeConfig() AutoRevokeConfig {
	return AutoRevokeConfig{
		Threshold: 3,
		Window:    24 * time.Hour,
		Interval:  5 * time.Minute,
		Enabled:   false,
	}
}

// LoadAutoRevokeConfig reads env vars on top of DefaultAutoRevokeConfig.
// LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE=on turns the sweeper on; the numeric
// knobs have safe minimums and never accept zero/negative values.
func LoadAutoRevokeConfig() AutoRevokeConfig {
	cfg := DefaultAutoRevokeConfig()
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE"))); v == "on" || v == "true" || v == "1" {
		cfg.Enabled = true
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE_THRESHOLD")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			cfg.Threshold = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE_WINDOW_HOURS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			cfg.Window = time.Duration(n) * time.Hour
		}
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE_INTERVAL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Second {
			cfg.Interval = d
		}
	}
	return cfg
}

// AutoRevoker runs the periodic escalation sweep. Safe to construct without
// calling Start; Start is the only side-effecting entry point.
type AutoRevoker struct {
	db        AutoRevokeDB
	cfg       AutoRevokeConfig // cfg is immutable after NewAutoRevoker; reads in sweepOnce are safe without locking.
	mu        sync.Mutex
	startOnce sync.Once
	stop      chan struct{}
	done      chan struct{}
}

// NewAutoRevoker wires a revoker against a *pgxpool.Pool. The db argument
// can be nil; nil → NewAutoRevoker returns nil so callers can use a simple
// `if r := NewAutoRevoker(db, cfg); r != nil { r.Start(ctx) }`.
//
// Validates cfg defensively — direct construction (bypassing
// LoadAutoRevokeConfig) with Threshold=0 or negative values would otherwise
// match every credential in the WHERE clause. Returns nil + logs a warn
// when cfg is malformed.
func NewAutoRevoker(db *pgxpool.Pool, cfg AutoRevokeConfig) *AutoRevoker {
	if db == nil {
		return nil
	}
	if cfg.Threshold < 1 {
		slog.Warn("credential.auto_revoke: invalid Threshold, refusing to construct",
			"threshold", cfg.Threshold)
		return nil
	}
	if cfg.Window < time.Second {
		slog.Warn("credential.auto_revoke: invalid Window, refusing to construct",
			"window", cfg.Window.String())
		return nil
	}
	if cfg.Interval < time.Second {
		slog.Warn("credential.auto_revoke: invalid Interval, refusing to construct",
			"interval", cfg.Interval.String())
		return nil
	}
	return &AutoRevoker{
		db:   db,
		cfg:  cfg,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// Enabled reports the effective kill-switch state. Use this from cmd/gateway
// boot to log whether auto-revoke will run.
func (r *AutoRevoker) Enabled() bool {
	if r == nil {
		return false
	}
	return r.cfg.Enabled
}

// Start launches the background sweep. Idempotent: a second Start is a no-op.
// The sweep only does work when cfg.Enabled is true; otherwise Start waits
// for Stop and exits.
//
// Idempotency is enforced by sync.Once: the goroutine (or park goroutine) is
// spawned exactly once. After Stop is called and the goroutine exits, a fresh
// r.stop channel would be needed to start again — callers are expected to
// treat Start as one-shot per AutoRevoker instance.
func (r *AutoRevoker) Start(ctx context.Context) {
	if r == nil {
		return
	}
	// Refuse if Stop already closed r.stop — this is the post-stop guard.
	r.mu.Lock()
	select {
	case <-r.stop:
		r.mu.Unlock()
		return
	default:
	}
	r.mu.Unlock()
	launched := false
	r.startOnce.Do(func() {
		launched = true
		if !r.cfg.Enabled {
			slog.Info("credential.auto_revoke: disabled (set LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE=on to enable)",
				"threshold", r.cfg.Threshold,
				"window", r.cfg.Window.String(),
				"interval", r.cfg.Interval.String())
			// Park goroutine so Stop is still safe.
			go func() {
				<-r.stop
				close(r.done)
			}()
			return
		}
		go r.run(ctx)
	})
	if !launched {
		// Already started; nothing to do.
		return
	}
}

// Stop signals the sweep loop to exit and waits for it to drain. Safe to
// call even if Start was never called.
func (r *AutoRevoker) Stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	select {
	case <-r.stop:
		// already closed
		r.mu.Unlock()
		return
	default:
		close(r.stop)
	}
	r.mu.Unlock()
	<-r.done
}

func (r *AutoRevoker) run(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	// Run one sweep immediately so an idle-then-burst pattern doesn't
	// have to wait a full interval for the first escalation.
	r.sweepOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-ticker.C:
			r.sweepOnce(ctx)
		}
	}
}

// sweepOnce runs a single escalation pass. Exported via the test helper
// SweepNow so unit tests can drive it deterministically.
func (r *AutoRevoker) sweepOnce(ctx context.Context) (revoked int, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Pull every credential that has cycled through availability_state IN
	// ('auth_failed', 'suspended') >= Threshold times within the rolling
	// window, and is not already manual_disabled. We use state_updated_at
	// rather than availability_recover_at because the cycle we want to count
	// is "the writer flipped auth_failed again" — and state_updated_at is
	// the only timestamp Writer.WriteOnError bumps on every transition.
	const sweepSQL = `
SELECT id, cycle_count
FROM (
  SELECT
    c.id,
    COUNT(*) FILTER (
      WHERE c.state_updated_at >= NOW() - $1::interval
        AND c.state_reason_code IN ('auth', 'auth_revoked')
        AND c.availability_state IN ('auth_failed', 'suspended')
    ) AS cycle_count
  FROM credentials c
  WHERE c.lifecycle_status = 'active'
    AND COALESCE(c.manual_disabled, FALSE) = FALSE
    AND c.state_updated_at >= NOW() - $1::interval
  GROUP BY c.id
) AS counted
WHERE cycle_count >= $2
`
	windowInterval := fmt.Sprintf("%d seconds", int(r.cfg.Window.Seconds()))
	rows, err := r.db.Query(ctx, sweepSQL, windowInterval, r.cfg.Threshold)
	if err != nil {
		slog.Warn("credential.auto_revoke: sweep query failed", "error", err)
		return 0, err
	}
	defer rows.Close()
	type candidate struct {
		id  int
		cnt int64
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.cnt); err != nil {
			slog.Warn("credential.auto_revoke: scan row failed", "error", err)
			continue
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, c := range candidates {
		err := r.revokeOne(ctx, c.id, c.cnt)
		if err == nil {
			slog.Info("credential.auto_revoke: credential auto-revoked",
				"credential_id", c.id,
				"cycle_count", c.cnt,
				"threshold", r.cfg.Threshold,
				"window", r.cfg.Window.String())
			revoked++
			continue
		}
		// ErrAutoRevokeNoOp is the documented race-with-admin path and
		// not an error: another revoker or an admin disabled the
		// credential between sweep and revoke. Log at Debug so noise
		// stays out of the production log volume.
		if errors.Is(err, ErrAutoRevokeNoOp) {
			slog.Debug("credential.auto_revoke: skipped (already disabled)",
				"credential_id", c.id)
			continue
		}
		slog.Warn("credential.auto_revoke: revoke failed",
			"credential_id", c.id, "cycle_count", c.cnt, "error", err)
	}
	if revoked > 0 {
		slog.Info("credential.auto_revoke: sweep complete",
			"revoked", revoked,
			"threshold", r.cfg.Threshold,
			"window", r.cfg.Window.String())
	}
	return revoked, nil
}

// revokeOne sets manual_disabled=TRUE and stamps a distinctive state_reason
// so operators can identify auto-revocations in the admin UI. The UPDATE is
// idempotent: if the credential is already manual_disabled (which shouldn't
// happen because we filtered on it in sweepSQL, but a race with an admin is
// possible), the UPDATE returns RowsAffected=0 and we report it as a no-op.
func (r *AutoRevoker) revokeOne(ctx context.Context, credentialID int, cycleCount int64) error {
	const sql = `
UPDATE credentials
SET manual_disabled       = TRUE,
    state_reason_code     = 'auto_revoke_auth',
    state_reason_detail   = $1,
    state_updated_at      = now()
WHERE id = $2
  AND COALESCE(manual_disabled, FALSE) = FALSE
  AND lifecycle_status    = 'active'
`
	detail := fmt.Sprintf("auto-revoked after %d auth cycles (threshold=%d, window=%s)",
		cycleCount, r.cfg.Threshold, r.cfg.Window.String())
	tag, err := r.db.Exec(ctx, sql, detail, credentialID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAutoRevokeNoOp
	}
	return nil
}

// ErrAutoRevokeNoOp is returned by revokeOne when the UPDATE matched zero
// rows because someone else (admin or another revoker instance) already
// disabled the credential. Callers should treat it as success.
var ErrAutoRevokeNoOp = errors.New("credential auto-revoke: no-op (already disabled)")

// SweepNow exposes sweepOnce for tests. The returned values are exported
// only because the test file lives in the same package.
func (r *AutoRevoker) SweepNow(ctx context.Context) (int, error) {
	if r == nil {
		return 0, nil
	}
	return r.sweepOnce(ctx)
}