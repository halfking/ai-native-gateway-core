package recovery

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

var (
	ursmV2ReadyGauge = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "ursm_v2_ready",
			Help: "URSM v2 recovery gate state (1=ready, 0=not ready)",
		},
	)
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
		ursmV2ReadyGauge.Set(0)
		return false
	}
	v, err := m.rdb.Get(ctx, store.ReadyKey(m.prefix)).Result()
	if err != nil {
		ursmV2ReadyGauge.Set(0)
		return false
	}
	isReady := v == "1"
	if isReady {
		ursmV2ReadyGauge.Set(1)
	} else {
		ursmV2ReadyGauge.Set(0)
	}
	return isReady
}

func (m *Manager) SetReady(ctx context.Context, ready bool) error {
	val := "0"
	if ready {
		val = "1"
	}
	if err := m.rdb.Set(ctx, store.ReadyKey(m.prefix), val, 0).Err(); err != nil {
		return err
	}
	if ready {
		ursmV2ReadyGauge.Set(1)
	} else {
		ursmV2ReadyGauge.Set(0)
	}
	return nil
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
