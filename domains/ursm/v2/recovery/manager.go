package recovery

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

type Manager struct {
	rdb    *redis.Client
	prefix string
}

func New(rdb *redis.Client, prefix string) *Manager {
	return &Manager{rdb: rdb, prefix: prefix}
}

func (m *Manager) Ready(ctx context.Context) bool {
	if m == nil || m.rdb == nil {
		return false
	}
	v, err := m.rdb.Get(ctx, store.ReadyKey(m.prefix)).Result()
	if err != nil {
		return false
	}
	return v == "1"
}

func (m *Manager) SetReady(ctx context.Context, ready bool) error {
	val := "0"
	if ready {
		val = "1"
	}
	return m.rdb.Set(ctx, store.ReadyKey(m.prefix), val, 0).Err()
}

func (m *Manager) EnterRecovery(ctx context.Context, reason string) error {
	if err := m.SetReady(ctx, false); err != nil {
		return fmt.Errorf("ursm.v2: enter recovery: %w", err)
	}
	pipe := m.rdb.Pipeline()
	pipe.HIncrBy(ctx, store.EpochKey(m.prefix), "counter", 1)
	pipe.HSet(ctx, store.EpochKey(m.prefix),
		"reason", reason,
		"started_at", time.Now().UTC().Format(time.RFC3339),
	)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("ursm.v2: enter recovery pipeline: %w", err)
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
		return false, fmt.Errorf("ursm.v2: setnx debounce: %w", err)
	}
	if !ok {
		return false, nil
	}
	if err := m.EnterRecovery(ctx, reason); err != nil {
		return true, err
	}
	return true, nil
}
