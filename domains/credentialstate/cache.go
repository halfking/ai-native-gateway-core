// DEPRECATED: This file will be moved to _to-be-deprecated/routing-old/credentialstate/
// Replaced by: domains/ursm/cache.go
// Migration date: 2026-07-03
// Status: 等待 Router/Executor 适配 URSM 完成后迁移
// DO NOT use this package in new code. Use domains/ursm instead.
//
// Deprecated: credentialstate is superseded by URSM v2 (domains/ursm/v2).
// It remains for legacy/off/canary modes only. Do not add new callers.
// In URSM_V2_MODE=authoritative, this package must have zero live reads/writes (spec §10 Step 5 C-1).
package credentialstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

	// 2026-07-27 (C-1): return a CLONE, not the shared pointer. Callers
	// (UpdateOnSuccess/UpdateOnFailure) mutate the returned State in place
	// (state.ConsecutiveFails++, state.LastError = ...). Returning the shared
	// pointer let two concurrent callers mutate the same object — a data race.
	// With copy-on-write here, each caller mutates its own private copy and
	// setToMemCache stores that copy. The per-key mutex (Manager.lockFor)
	// serializes the Load→mutate→Store window so counter increments are not
	// lost.
	clone := *entry.State
	return &clone, true
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
		slog.Warn("credstate: marshal redis cache failed",
			"key", key,
			"error", err,
		)
		return
	}

	if err := m.redisClient.Set(ctx, redisKey, data, m.redisCacheTTL).Err(); err != nil {
		recordRedisWriteFailure()
		slog.Warn("credstate: redis cache write failed",
			"key", key,
			"error", err,
		)
	}
}

func (m *Manager) getFromDB(ctx context.Context, credID int, model string) (*State, error) {
	// node_probe_state is the authoritative model-level state in the new probe
	// mode. Keep the legacy model_probe_state fallback for older deployments and
	// for rows that have not entered the new probe pipeline yet.
	var (
		lastDirectOK *bool
		lastErrCode  *string
		nextRetryAt  time.Time
		consecFails  int
	)
	nodeErr := m.db.QueryRow(ctx, `
		SELECT last_direct_ok, last_err_code, next_retry_at, consecutive_failures
		FROM node_probe_state
		WHERE credential_id = $1 AND raw_model_name = $2
	`, credID, model).Scan(&lastDirectOK, &lastErrCode, &nextRetryAt, &consecFails)
	if nodeErr == nil {
		state := &State{
			CredentialID:     credID,
			Model:            model,
			Available:        true,
			ConsecutiveFails: consecFails,
			LastUpdatedAt:    time.Now(),
			RecoverAt:        &nextRetryAt,
			Source:           "node_probe_db",
		}
		if lastErrCode != nil {
			state.LastError = *lastErrCode
		}
		if lastDirectOK != nil && !*lastDirectOK && nextRetryAt.After(time.Now()) {
			state.Available = false
			state.HealthStatus = "unreachable"
		}
		return state, nil
	}
	if nodeErr != nil && nodeErr != pgx.ErrNoRows && !isUndefinedTable(nodeErr) {
		return nil, nodeErr
	}
	if nodeErr != nil && !isUndefinedTable(nodeErr) && nodeErr != pgx.ErrNoRows {
		return nil, nodeErr
	}

	return m.getLegacyStateFromDB(ctx, credID, model)
}

func (m *Manager) getLegacyStateFromDB(ctx context.Context, credID int, model string) (*State, error) {
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

func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}
