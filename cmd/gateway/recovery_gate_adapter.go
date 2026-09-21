// Package main (cmd/gateway) — recovery_gate_adapter.go
//
// 适配器：把 *ursmv2.Manager 包成 systemmonitor.RecoveryGate 接口实例。
//
// 2026-07-29 (audit follow-up #4): production wiring uses this adapter
// instead of importing domains/ursm/v2/recovery into bg/systemmonitor
// (which would create a cycle through the SystemMonitor wiring chain).
// Adapter does the typed field-by-field conversion of recovery.Stats
// → systemmonitor.RecoveryStats.
//
// 2026-09-04 (availability work): the adapter gained a self-healing path
// for the "Redis restarted WITHOUT persistence" incident. In that scenario
// the monitor's restore attempt runs WarmupFromCoverage, which refuses with
// recovery.ErrCoverageManifestEmpty, and — before this change — the
// authoritative gate stayed closed forever (every request 503) until the
// process was restarted. The adapter now rebuilds the namespace from
// PostgreSQL (bootstrap.Apply — proven safe against a live namespace: it
// skips admin holds and generation>1 nodes, swaps the manifest atomically
// and never touches the gate) and retries the coverage validation.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/bg/systemmonitor"
	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/bootstrap"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/recovery"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// v2RecoveryGateAdapter bridges domains/ursm/v2.Manager to
// systemmonitor.RecoveryGate. All methods delegate to the wrapped
// v2.Manager; Stats() converts recovery.Stats to systemmonitor.RecoveryStats.
type v2RecoveryGateAdapter struct {
	mgr *ursmv2.Manager

	// rebuild, when non-nil, enables the empty-namespace self-heal below.
	rebuild *rebuildOptions
	// lastRebuildNanos (UnixNano) rate-limits rebuild attempts so a
	// pathological failure loop cannot hammer PostgreSQL. Zero = never.
	lastRebuildNanos atomic.Int64
	// rebuildMu serializes the check-and-claim with bootstrap.Apply. The
	// monitor can request recovery from both the transition and periodic paths.
	rebuildMu sync.Mutex
}

// rebuildOptions carries the bootstrap.Apply dependencies (PostgreSQL pool
// + the same Redis the manager owns). Both must be non-nil for a rebuild.
type rebuildOptions struct {
	pool        *pgxpool.Pool
	rdb         *redis.Client
	keyPrefix   string
	coolSeconds int
	schemaMode  store.KeySchemaMode
}

func newV2RecoveryGateAdapter(mgr *ursmv2.Manager) *v2RecoveryGateAdapter {
	return &v2RecoveryGateAdapter{mgr: mgr}
}

// withRebuild enables the empty-namespace self-heal. Called at wire time
// only in ModeAuthoritative with both dependencies reachable.
func (a *v2RecoveryGateAdapter) withRebuild(opts rebuildOptions) *v2RecoveryGateAdapter {
	if opts.pool == nil || opts.rdb == nil {
		return a
	}
	a.rebuild = &opts
	return a
}

// MarkClosedDebounced delegates to v2.Manager (which in turn delegates
// to recovery.Manager). See recovery.Manager.MarkClosedDebounced for
// the cluster-coordination contract.
func (a *v2RecoveryGateAdapter) MarkClosedDebounced(ctx context.Context, reason string, debounceTTL time.Duration) (bool, error) {
	if a == nil || a.mgr == nil {
		return false, nil
	}
	return a.mgr.MarkClosedDebounced(ctx, reason, debounceTTL)
}

// rebuildMinInterval bounds self-heal attempts. bootstrap.Apply is cheap
// (one PG query + pipelined writes) but rebuilding more often than this
// during an unresolved incident adds noise, not value.
const rebuildMinInterval = 5 * time.Minute

// RestoreIfClosed delegates to v2.Manager. When the restore refuses with
// recovery.ErrCoverageManifestEmpty (Redis lost its data) and rebuild
// dependencies are wired, it re-runs bootstrap.Apply from PostgreSQL and
// retries the coverage validation — closing what was previously a
// permanent routing outage requiring a process restart.
func (a *v2RecoveryGateAdapter) RestoreIfClosed(ctx context.Context) (int, error) {
	if a == nil || a.mgr == nil {
		return 0, nil
	}
	n, err := a.mgr.RestoreIfClosed(ctx)
	if err == nil {
		return n, nil
	}
	if a.rebuild == nil || (!errors.Is(err, recovery.ErrCoverageManifestEmpty) && !errors.Is(err, recovery.ErrCoverageIncomplete)) {
		return n, err
	}
	a.rebuildMu.Lock()
	defer a.rebuildMu.Unlock()
	if last := a.lastRebuildNanos.Load(); last != 0 && time.Since(time.Unix(0, last)) < rebuildMinInterval {
		return n, err
	}
	a.lastRebuildNanos.Store(time.Now().UnixNano())
	slog.Error("ursm.v2: recovery refused with empty coverage manifest — Redis likely lost its data; rebuilding from PostgreSQL",
		"error", err)
	bctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result, berr := bootstrap.Apply(bctx, bootstrap.Options{
		Pool:        a.rebuild.pool,
		Redis:       a.rebuild.rdb,
		KeyPrefix:   a.rebuild.keyPrefix,
		CoolSeconds: a.rebuild.coolSeconds,
		SchemaMode:  a.rebuild.schemaMode,
	})
	if berr != nil {
		return 0, fmt.Errorf("ursm.v2: empty-namespace rebuild failed: %w (original restore error: %v)", berr, err)
	}
	n, werr := a.mgr.WarmupFromCoverage(bctx)
	if werr != nil {
		return 0, fmt.Errorf("ursm.v2: post-rebuild coverage validation failed: %w", werr)
	}
	slog.Info("ursm.v2: empty-namespace rebuild complete, recovery gate reopened",
		"node_count", n,
		"bootstrap_total", result.Total,
		"bootstrap_written", result.Written,
		"bootstrap_skipped", result.Skipped)
	return n, nil
}

// Stats converts the recovery.Stats snapshot (with its named fields)
// to the systemmonitor.RecoveryStats struct (matching fields, same
// time.Time types). Safe on a nil adapter / nil manager.
func (a *v2RecoveryGateAdapter) Stats() systemmonitor.RecoveryStats {
	if a == nil || a.mgr == nil {
		return systemmonitor.RecoveryStats{}
	}
	s := a.mgr.RecoveryStats()
	keyCount := 0
	if !s.LastRecoveryAt.IsZero() {
		keyCount = a.mgr.LastRecoveryKeyCount()
	}
	return systemmonitor.RecoveryStats{
		LastError:            s.LastError,
		LastErrorAt:          s.LastErrorAt,
		LastRecoveryAt:       s.LastRecoveryAt,
		LastRecoveryKeyCount: keyCount,
	}
}
