package recovery

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

//go:embed transition_recovery.lua
var transitionRecoverySrc string

var transitionRecoveryScript = redis.NewScript(transitionRecoverySrc)

// ErrCoverageManifestEmpty reports that the coverage manifest set holds no
// members — the signature of a Redis namespace wiped by a restart without
// persistence (or an incomplete bootstrap). Callers may errors.Is this to
// decide whether a rebuild-from-PostgreSQL (bootstrap.Apply) is the right
// remedy before retrying the reopen (2026-09-04 availability work).
var ErrCoverageManifestEmpty = errors.New("coverage manifest empty")

// ErrCoverageIncomplete reports a non-empty manifest with missing or malformed
// node state, which is also recoverable by rebuilding from PostgreSQL.
var ErrCoverageIncomplete = errors.New("coverage manifest incomplete")

type Manager struct {
	rdb    *redis.Client
	prefix string

	// schemaMode mirrors the store's boot-only key schema mode (doc 14
	// §3): it decides which grammars coverage validation and warmup
	// counting accept. Zero value legacy keeps the historical behavior.
	schemaMode store.KeySchemaMode

	// 2026-07-29 (audit follow-up #4): observability for the incident
	// lifecycle. lastError / lastErrorAt record the most recent
	// operation failure (MarkClosedDebounced, WarmupFromExistingKeys,
	// etc.) so operators can correlate health-check noise with the
	// recovery gate state. lastRecoveryAt records the timestamp of the
	// most recent successful reopen, so a dashboard can show "last
	// recovery was N hours ago". All access is mutex-guarded; the
	// fields are read by admin endpoints and Prometheus exporters, not
	// by the request hot path, so contention is bounded.
	mu              sync.RWMutex
	lastError       string
	lastErrorAt     time.Time
	lastRecoveryAt  time.Time
	lastRecoveryKey int
}

func New(rdb *redis.Client, prefix string) *Manager {
	return &Manager{rdb: rdb, prefix: prefix}
}

// SetKeySchemaMode fixes the key schema mode. Boot-only by contract, kept
// in lockstep with store.Store.SetKeySchemaMode.
func (m *Manager) SetKeySchemaMode(mode store.KeySchemaMode) { m.schemaMode = mode }

func (m *Manager) Ready(ctx context.Context) bool {
	ready, _ := m.ReadyWithError(ctx)
	return ready
}

func (m *Manager) ReadyWithError(ctx context.Context) (bool, error) {
	if m == nil || m.rdb == nil {
		return false, fmt.Errorf("ursm.v2: nil recovery manager / redis client")
	}
	v, err := m.rdb.Get(ctx, store.ReadyKey(m.prefix)).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return v == "1", nil
}

func (m *Manager) SetReady(ctx context.Context, ready bool) error {
	val := "0"
	if ready {
		val = "1"
	}
	return m.rdb.Set(ctx, store.ReadyKey(m.prefix), val, 0).Err()
}

func (m *Manager) EnterRecovery(ctx context.Context, reason string) error {
	if m == nil || m.rdb == nil {
		return fmt.Errorf("ursm.v2: nil manager / redis client")
	}
	if _, err := transitionRecoveryScript.Run(ctx, m.rdb,
		[]string{store.ReadyKey(m.prefix), store.EpochKey(m.prefix)},
		"close", reason, time.Now().UTC().Format(time.RFC3339Nano), "").Result(); err != nil {
		m.recordError(err)
		return fmt.Errorf("ursm.v2: enter recovery: %w", err)
	}
	return nil
}

// MarkClosedDebounced atomically claims the right to call EnterRecovery
// within a cluster-wide debounce window. Returns (true, nil) when this
// caller is the one that actually wrote the epoch (epoch counter
// increments by exactly 1 per debounce window per failure event);
// (false, nil) when a previous caller's debounce window is still
// active and this call should be a no-op.
//
// Cluster-wide coordination: at most one EnterRecovery write per
// debounceTTL across the whole fleet, regardless of how many instances
// simultaneously observe Redis health failures. The debounce key is
// SETNX'd with TTL; on expiry the next failure event is free to
// record a fresh epoch bump.
//
// This closes the audit gap (docs/architecture/2026-07-28-routing-state-anomaly-audit.md
// §4.1) — production now has a path that automatically closes the v2
// gate on persistent Redis health failures and writes incident metadata.
func (m *Manager) MarkClosedDebounced(ctx context.Context, reason string, debounceTTL time.Duration) (bool, error) {
	if m == nil || m.rdb == nil {
		return false, fmt.Errorf("ursm.v2: nil manager / redis client")
	}
	if debounceTTL <= 0 {
		debounceTTL = 5 * time.Minute
	}
	ok, err := m.rdb.SetNX(ctx, store.RecoveryDebounceKey(m.prefix), reason, debounceTTL).Result()
	if err != nil {
		m.recordError(err)
		return false, fmt.Errorf("ursm.v2: setnx debounce: %w", err)
	}
	if !ok {
		return false, nil
	}
	if err := m.EnterRecovery(ctx, reason); err != nil {
		// Do not leave a successful debounce claim behind when the actual
		// gate close failed; that would suppress every retry until the TTL.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = m.rdb.Del(cleanupCtx, store.RecoveryDebounceKey(m.prefix)).Err()
		cancel()
		// EnterRecovery already recorded the error; bubble up. The claim did
		// not produce a closed gate, so report that this caller did not win.
		return false, err
	}
	m.clearError()
	return true, nil
}

// WarmupFromExistingKeys re-opens the recovery gate after a Redis
// health incident without resetting per-node state. It SCANs the
// existing ursm:v2:node:* keys (so an operator can audit "how many
// nodes survived the incident"), records the recovery timestamp + key
// count in the epoch hash, and flips meta:ready to "1".
//
// Crucially, WarmupFromExistingKeys does NOT touch individual node
// hashes — admin holds, fail_streaks, source_priority values, and
// generation counters all persist across the recovery so live traffic
// resumes from the same per-node state it had before the incident.
// Only the gate flips from closed to open.
//
// This is the audit follow-up #6 counterpart to MarkClosedDebounced
// (follow-up #1): together they implement the full incident lifecycle
// (auto-close on persistent failure, auto-reopen on Redis recovery)
// without operator intervention.
//
// Returns the number of node keys observed. An empty Redis namespace is not
// recoverable state: the gate remains closed so authoritative routing falls
// back instead of rejecting every candidate as a missing node.
func (m *Manager) WarmupFromExistingKeys(ctx context.Context) (int, error) {
	if m == nil || m.rdb == nil {
		return 0, fmt.Errorf("ursm.v2: nil manager / redis client")
	}
	observedEpoch, err := m.rdb.HGet(ctx, store.EpochKey(m.prefix), "counter").Result()
	if err != nil && err != redis.Nil {
		m.recordError(err)
		return 0, fmt.Errorf("ursm.v2: read recovery epoch: %w", err)
	}
	if err == redis.Nil {
		observedEpoch = ""
	}
	pattern := m.prefix + "node:*"
	keys, err := m.scanNodeKeys(ctx, pattern)
	if err != nil {
		m.recordError(err)
		return 0, err
	}
	count := m.countWarmupNodes(keys)
	if count == 0 {
		err := fmt.Errorf("ursm.v2: warmup refused: no node state")
		m.recordError(err)
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := transitionRecoveryScript.Run(ctx, m.rdb,
		[]string{store.ReadyKey(m.prefix), store.EpochKey(m.prefix)},
		"open_if_epoch", "", now, observedEpoch, intToString(count)).Text()
	if err != nil {
		m.recordError(err)
		return 0, fmt.Errorf("ursm.v2: warmup transition: %w", err)
	}
	if result == "superseded" {
		err := fmt.Errorf("ursm.v2: warmup superseded by newer recovery close")
		m.recordError(err)
		return 0, err
	}
	m.recordRecovery(count)
	m.clearError()
	return count, nil
}

// countWarmupNodes reduces the scanned node keys to the node count that
// may reopen the gate under the current schema mode: legacy keeps the raw
// key count (historical behavior), dual counts distinct logical tuples
// (a tuple mirrored in both grammars is one node), canonical counts only
// canonical keys — legacy-only state cannot reopen a canonical gate
// (doc 14 §5.3).
func (m *Manager) countWarmupNodes(keys []string) int {
	switch m.schemaMode {
	case store.KeySchemaModeCanonical:
		n := 0
		for _, k := range keys {
			if p, ok := store.ParseNodeKeyAny(m.prefix, k); ok && p.Schema == store.KeySchemaK2 {
				n++
			}
		}
		return n
	case store.KeySchemaModeDual:
		seen := make(map[store.ParsedNodeKey]struct{}, len(keys))
		for _, k := range keys {
			if p, ok := store.ParseNodeKeyAny(m.prefix, k); ok {
				seen[p.ParsedNodeKey] = struct{}{}
			}
		}
		return len(seen)
	default:
		return len(keys)
	}
}

// ValidateCoverage verifies the migration manifest and every expected tenant-aware
// node hash. It intentionally does not change the ready gate.
func (m *Manager) ValidateCoverage(ctx context.Context) (int, error) {
	if m == nil || m.rdb == nil {
		return 0, fmt.Errorf("ursm.v2: nil manager / redis client")
	}
	if pending, err := m.rdb.Exists(ctx, store.CoveragePendingKey(m.prefix)).Result(); err != nil {
		return 0, fmt.Errorf("ursm.v2: read coverage pending marker: %w", err)
	} else if pending != 0 {
		return 0, fmt.Errorf("ursm.v2: coverage migration is still pending")
	}
	keys, err := m.rdb.SMembers(ctx, store.CoverageKey(m.prefix)).Result()
	if err != nil {
		return 0, fmt.Errorf("ursm.v2: read coverage manifest: %w", err)
	}
	if len(keys) == 0 {
		return 0, fmt.Errorf("ursm.v2: coverage manifest is empty: %w", ErrCoverageManifestEmpty)
	}
	pipe := m.rdb.Pipeline()
	exists := make([]*redis.IntCmd, len(keys))
	fields := make([]*redis.SliceCmd, len(keys))
	for i, key := range keys {
		exists[i] = pipe.Exists(ctx, key)
		fields[i] = pipe.HMGet(ctx, key, "generation", "available")
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("ursm.v2: validate coverage: %w", err)
	}
	for i, key := range keys {
		parsed, ok := store.ParseNodeKeyAny(m.prefix, key)
		if !ok || parsed.TenantID == "" {
			return 0, fmt.Errorf("ursm.v2: coverage key is not tenant-aware: %s", key)
		}
		// Canonical mode is canonical-only: a legacy key in the manifest
		// cannot open the authoritative gate (doc 14 §5.3).
		if m.schemaMode == store.KeySchemaModeCanonical && parsed.Schema != store.KeySchemaK2 {
			return 0, fmt.Errorf("ursm.v2: coverage key is not canonical: %s", key)
		}
		if exists[i].Val() != 1 {
			return 0, fmt.Errorf("ursm.v2: coverage key missing: %s: %w", key, ErrCoverageIncomplete)
		}
		values, err := fields[i].Result()
		if err != nil || len(values) < 2 || values[0] == nil || values[1] == nil {
			return 0, fmt.Errorf("ursm.v2: coverage hash incomplete: %s: %w", key, ErrCoverageIncomplete)
		}
	}
	return len(keys), nil
}

// WarmupFromCoverage validates the migration manifest and atomically opens the
// gate only after every expected node is present.
func (m *Manager) WarmupFromCoverage(ctx context.Context) (int, error) {
	observedEpoch, err := m.rdb.HGet(ctx, store.EpochKey(m.prefix), "counter").Result()
	if err != nil && err != redis.Nil {
		m.recordError(err)
		return 0, fmt.Errorf("ursm.v2: read recovery epoch: %w", err)
	}
	if err == redis.Nil {
		observedEpoch = ""
	}
	if err := m.SetReady(ctx, false); err != nil {
		return 0, err
	}
	count, err := m.ValidateCoverage(ctx)
	if err != nil {
		m.recordError(err)
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := transitionRecoveryScript.Run(ctx, m.rdb,
		[]string{store.ReadyKey(m.prefix), store.EpochKey(m.prefix)},
		"open_if_epoch", "", now, observedEpoch, intToString(count)).Text()
	if err != nil {
		m.recordError(err)
		return 0, fmt.Errorf("ursm.v2: coverage warmup transition: %w", err)
	}
	if result == "superseded" {
		err := fmt.Errorf("ursm.v2: coverage warmup superseded by newer recovery close")
		m.recordError(err)
		return 0, err
	}
	m.recordRecovery(count)
	m.clearError()
	return count, nil
}

// When the gate is already open it returns (0, nil) as a no-op; when
// the gate is closed it re-warms from existing keys and returns the
// observed count.
//
// The final reopen is conditional on the epoch observed after the scan. A
// concurrent close advances that epoch, so an older restore cannot reopen the
// gate over a new incident.
func (m *Manager) RestoreIfClosed(ctx context.Context) (int, error) {
	if m == nil || m.rdb == nil {
		return 0, fmt.Errorf("ursm.v2: nil manager / redis client")
	}
	if m.Ready(ctx) {
		return 0, nil
	}
	return m.WarmupFromExistingKeys(ctx)
}

// scanNodeKeys performs a SCAN over the given pattern and returns all
// matching keys. We use SCAN (not KEYS) to avoid blocking Redis on
// large keyspaces; the iteration cost is O(N) amortised over many
// short-lived connections, which is fine for a recovery flow that
// runs at most a few times per hour.
func (m *Manager) scanNodeKeys(ctx context.Context, pattern string) ([]string, error) {
	var (
		cursor uint64
		out    []string
	)
	for {
		keys, next, err := m.rdb.Scan(ctx, cursor, pattern, 200).Result()
		if err != nil {
			return nil, fmt.Errorf("ursm.v2: scan %s: %w", pattern, err)
		}
		out = append(out, keys...)
		if next == 0 {
			break
		}
		cursor = next
	}
	return out, nil
}

func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// Stats is the snapshot of the recovery gate state returned to the
// admin endpoint and the Prometheus exporter. All fields are
// zero-valued on a nil receiver or when no operation has happened.
type Stats struct {
	LastError      string
	LastErrorAt    time.Time
	LastRecoveryAt time.Time
}

// recordError stamps the latest failure on the manager so an admin
// dashboard can show "recovery gate last failed at <time>: <err>".
// Best-effort: callers log + return the error; the recorded value is
// purely diagnostic.
func (m *Manager) recordError(err error) {
	if m == nil || err == nil {
		return
	}
	m.mu.Lock()
	m.lastError = err.Error()
	m.lastErrorAt = time.Now().UTC()
	m.mu.Unlock()
}

// recordRecovery stamps the latest successful reopen so a dashboard
// can compute "time since last recovery" without parsing epoch hashes.
func (m *Manager) recordRecovery(keyCount int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.lastRecoveryAt = time.Now().UTC()
	m.lastRecoveryKey = keyCount
	m.mu.Unlock()
}

// clearError resets the lastError state on a successful operation.
// We do this so a one-shot transient failure doesn't keep showing up
// on the dashboard long after the underlying issue was resolved.
func (m *Manager) clearError() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.lastError = ""
	m.lastErrorAt = time.Time{}
	m.mu.Unlock()
}

// Stats returns the observability snapshot. Safe on a nil receiver.
func (m *Manager) Stats() Stats {
	if m == nil {
		return Stats{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Stats{
		LastError:      m.lastError,
		LastErrorAt:    m.lastErrorAt,
		LastRecoveryAt: m.lastRecoveryAt,
	}
}

// LastRecoveryKeyCount returns the key count observed on the most
// recent successful reopen. Useful for dashboards that want to show
// "last recovery re-warmed N nodes".
func (m *Manager) LastRecoveryKeyCount() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastRecoveryKey
}
