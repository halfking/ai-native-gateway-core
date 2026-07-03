// DEPRECATED: This file will be moved to _to-be-deprecated/routing-old/credentialstate/
// Replaced by: domains/ursm/cache.go
// Migration date: 2026-07-03
// Status: 等待 Router/Executor 适配 URSM 完成后迁移
// DO NOT use this package in new code. Use domains/ursm instead.

package credentialstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

// 缓存操作方法

func (m *Manager) getFromMemCache(key string) (*State, bool) {
	val, ok := m.memCache.Load(key)
	if !ok {
		return nil, false
	}

	entry := val.(*CacheEntry)
	if time.Now().After(entry.ExpiresAt) {
		m.memCache.Delete(key)
		return nil, false
	}

	return entry.State, true
}

func (m *Manager) setToMemCache(key string, state *State) {
	entry := &CacheEntry{
		State:     state,
		ExpiresAt: time.Now().Add(m.memCacheTTL),
	}
	m.memCache.Store(key, entry)
}

func (m *Manager) getFromRedis(ctx context.Context, key string) (*State, error) {
	if m.redisClient == nil {
		return nil, fmt.Errorf("redis client not configured")
	}

	redisKey := "llmgw:credstate:" + key
	data, err := m.redisClient.Get(ctx, redisKey).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var state State
	if err := json.Unmarshal([]byte(data), &state); err != nil {
		return nil, err
	}

	return &state, nil
}

func (m *Manager) setToRedis(ctx context.Context, key string, state *State) {
	if m.redisClient == nil {
		return
	}

	redisKey := "llmgw:credstate:" + key
	data, err := json.Marshal(state)
	if err != nil {
		return
	}

	m.redisClient.Set(ctx, redisKey, data, m.redisCacheTTL)
}

func (m *Manager) getFromDB(ctx context.Context, credID int, model string) (*State, error) {
	// 从 model_probe_state 表读取最新探测状态。该表由
	// bg.ModelProbeRunner 维护，记录 (credential, model) 级别的健康。
	var (
		state          State
		healthStatus   *string
		consecFailures *int
		lastAttemptAt  *time.Time
		nextRetryAt    *time.Time
	)
	err := m.db.QueryRow(ctx, `
		SELECT
			mps.credential_id,
			mps.raw_model_name,
			mps.state,
			mps.consecutive_failures,
			mps.last_attempt_at,
			mps.next_retry_at
		FROM model_probe_state mps
		WHERE mps.credential_id = $1
		  AND mps.raw_model_name = $2
	`, credID, model).Scan(
		&state.CredentialID,
		&state.Model,
		&healthStatus,
		&consecFailures,
		&lastAttemptAt,
		&nextRetryAt,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if healthStatus != nil {
		state.HealthStatus = *healthStatus
		// model_probe_state.state 的合法值集合（见
		// migrations/329_model_probe_state_canonicalize.sql）：
		//   healthy_confirmed / probing → 可用
		//   recovering / unknown / broken_confirmed /
		//   suspicious / manual_offline / manual_online → 不可用
		//
		// 向后兼容（旧字面量）：
		//   'available' / 'healthy' 是早期废弃 init 路径写入的字面量
		//   （db/db.go），语义上等同 healthy_confirmed。为防止旧
		//   字面量行再次让路由报"无可用凭据"，这里也视作可用，并在
		//   读取时同步把 state 写回 healthy_confirmed（best-effort，
		//   失败不影响本请求）。
		if *healthStatus == "healthy_confirmed" ||
			*healthStatus == "probing" ||
			*healthStatus == "available" ||
			*healthStatus == "healthy" {
			state.Available = true
		}
	}
	if consecFailures != nil {
		state.ConsecutiveFails = *consecFailures
	}
	if lastAttemptAt != nil {
		state.LastUpdatedAt = *lastAttemptAt
	}
	state.RecoverAt = nextRetryAt
	state.Source = "db"

	return &state, nil
}
