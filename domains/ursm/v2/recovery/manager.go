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
