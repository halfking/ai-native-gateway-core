// bg/auto_route_distlock.go — shared Redis token-bucket leader-election gate
// for the auto-route background workers (settle + affinity).
//
// R31 audit fix (docs/audit/2026-09-16-r30-24h-audit-round.md §四#1): on a
// blue-green pair both instances ran every sweep. That is safe — sweeps are
// idempotent (settled_at IS NULL predicates on every write; the affinity EMA
// read-modify-write converges even when interleaved) — but it doubled the
// batch JOIN load and inflated the Prometheus counters (settled/rewarded
// counted once per instance). This mirrors the materialized-view refresher's
// election (main.go:3584 wiring, bg/materialized_view_refresher.go doc
// comment): Redis preferred, and when no manager is wired / it reports
// Enabled()==false / Acquire errors (Redis outage), the helper returns nil
// and the caller sweeps exactly as before this change.
//
// No Postgres advisory-lock fallback here, unlike the refresher: sweeps are
// cheap and idempotent, and session-scoped advisory locks would force the
// whole sweep onto one pinned pool connection (every query in both workers
// goes through w.db). Losing Redis simply returns to pre-R31 duplicate-sweep
// behaviour, which the audit already classified as cosmetic.
//
// Follower semantics are non-blocking (ModeWaitFollower only performs a
// ~3s-bounded pubsub handshake and returns): a follower redeems no token and
// skips the cycle instead of queueing behind the leader.

package bg

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/admin/distlock"
)

// autoRouteDistLockNamespace is the distlock namespace shared by the
// auto-route workers. BuildKey hashes it into one Redis Cluster slot together
// with the logical key.
const autoRouteDistLockNamespace = "bg"

// acquireSweepDistLock attempts the token-bucket leader election for one
// sweep cycle. Returns nil whenever Redis coordination is unusable (no
// manager, disabled manager, or Acquire error) — the caller then proceeds
// with the sweep exactly as before. Returns a non-nil handle (leader OR
// follower) when the Redis round-trip succeeded; the caller must Release()
// it either way and only runs the sweep when IsLeader() is true.
func acquireSweepDistLock(
	ctx context.Context,
	mgr distlock.Manager,
	logicalKey string,
	ttl time.Duration,
	scope string,
) *distlock.Handle {
	if mgr == nil || !mgr.Enabled() {
		return nil
	}
	h, err := mgr.Acquire(ctx, distlock.AcquireOpts{
		Key:   distlock.BuildKey(autoRouteDistLockNamespace, logicalKey),
		TTL:   ttl,
		Mode:  distlock.ModeWaitFollower,
		Scope: scope,
	})
	if err != nil {
		if !errors.Is(err, distlock.ErrNotEnabled) {
			slog.Warn("auto-route worker: redis lock acquire failed, sweeping without cross-instance dedup",
				"worker", scope, "error", err)
		}
		return nil
	}
	return h
}
