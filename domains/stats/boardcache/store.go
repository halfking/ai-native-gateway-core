package boardcache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
)

// BaselineBuilder materializes board JSON from PostgreSQL (rebuild path only).
type BaselineBuilder func(ctx context.Context, tenantFilter string, days int, providerID int64) (map[string]any, error)

// DrillBuilder materializes error-drill items from PostgreSQL (rebuild path only).
type DrillBuilder func(ctx context.Context, tenantFilter string, days int, errorKind, dimension string) ([]map[string]any, error)

// GetOrRebuild returns board stats from Redis; on miss rebuilds via PG into Redis first.
func (s *Service) GetOrRebuild(ctx context.Context, scope Scope, days int, providerID int64) (map[string]any, error) {
	if s == nil || s.rdb == nil {
		return nil, fmt.Errorf("board cache unavailable")
	}
	if payload, ok := s.readBoard(ctx, scope, days, providerID); ok {
		s.attachMeta(ctx, scope, days, providerID, payload)
		return payload, nil
	}
	if s.build == nil {
		return nil, fmt.Errorf("board cache miss and builder not configured")
	}
	payload, err := s.build(ctx, TenantFilter(scope), days, providerID)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		payload = map[string]any{"summary": map[string]any{}, "pies": map[string]any{}, "trends": []any{}}
	}
	payload["days"] = days
	since := time.Now().UTC().Truncate(time.Minute)
	if providerID <= 0 {
		if err := s.putBaseline(ctx, scope, days, payload, since); err != nil {
			return nil, err
		}
	}
	meta := map[string]string{
		"source":   "postgresql_baseline",
		"built_at": time.Now().UTC().Format(time.RFC3339),
		"scope":    string(scope),
	}
	if providerID > 0 {
		meta["provider_id"] = fmt.Sprintf("%d", providerID)
	}
	if err := s.putBoard(ctx, scope, days, providerID, payload, meta); err != nil {
		return nil, err
	}
	return payload, nil
}

// GetOrRebuildDrill returns error drill from Redis; on miss rebuilds into Redis first.
func (s *Service) GetOrRebuildDrill(ctx context.Context, scope Scope, days int, errorKind, dimension string, build DrillBuilder) ([]map[string]any, error) {
	if s == nil || s.rdb == nil {
		return nil, fmt.Errorf("board cache unavailable")
	}
	if items, ok := s.readDrill(ctx, scope, days, errorKind, dimension); ok {
		return items, nil
	}
	if build == nil {
		return nil, fmt.Errorf("drill cache miss and builder not configured")
	}
	items, err := build(ctx, TenantFilter(scope), days, errorKind, dimension)
	if err != nil {
		return nil, err
	}
	if err := s.putDrill(ctx, scope, days, errorKind, dimension, items); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) readBoard(ctx context.Context, scope Scope, days int, providerID int64) (map[string]any, bool) {
	raw, err := s.rdb.Get(ctx, cacheKeyBoard(scope, days, providerID)).Result()
	if err != nil || raw == "" {
		if providerID > 0 {
			return nil, false
		}
		raw, err = s.rdb.Get(ctx, baselineKey(scope, days)).Result()
		if err != nil || raw == "" {
			return nil, false
		}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, false
	}
	return payload, true
}

// GetBoard reads folded board without triggering rebuild.
func (s *Service) GetBoard(ctx context.Context, scope Scope, days int) (map[string]any, bool) {
	return s.readBoard(ctx, scope, days, 0)
}

func (s *Service) readDrill(ctx context.Context, scope Scope, days int, errorKind, dimension string) ([]map[string]any, bool) {
	raw, err := s.rdb.Get(ctx, drillKey(scope, days, errorKind, dimension)).Result()
	if err != nil || raw == "" {
		return nil, false
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, false
	}
	return items, true
}

func (s *Service) putDrill(ctx context.Context, scope Scope, days int, errorKind, dimension string, items []map[string]any) error {
	b, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, drillKey(scope, days, errorKind, dimension), b, 24*time.Hour).Err()
}

func (s *Service) putJSON(ctx context.Context, key string, payload map[string]any, ttl time.Duration) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, key, b, ttl).Err()
}

func (s *Service) putBaseline(ctx context.Context, scope Scope, days int, payload map[string]any, since time.Time) error {
	// 2026-07-23: TTL 从 7 天缩短到 1 天。baseline 每 4 小时重建一次（rebuild_interval_hours=4），
	// 1 天足够覆盖 6 次重建周期，更长 TTL 没有业务价值反而累积 key。
	ttl := 1 * 24 * time.Hour
	if err := s.putJSON(ctx, baselineKey(scope, days), payload, ttl); err != nil {
		return err
	}
	meta := map[string]string{
		"since_ts": since.UTC().Format(time.RFC3339),
		"built_at": time.Now().UTC().Format(time.RFC3339),
		"source":   "postgresql",
	}
	// 2026-07-23: meta key 必须显式设置 TTL，否则 HSet 隐式清除 TTL 让它永不过期。
	if err := s.rdb.HSet(ctx, baselineMetaKey(scope, days), meta).Err(); err != nil {
		return err
	}
	return s.rdb.Expire(ctx, baselineMetaKey(scope, days), ttl).Err()
}

func (s *Service) putBoard(ctx context.Context, scope Scope, days int, providerID int64, payload map[string]any, meta map[string]string) error {
	// 2026-07-23: TTL 从 7 天缩短到 1 天。同 putBaseline 的理由。
	ttl := 1 * 24 * time.Hour
	key := cacheKeyBoard(scope, days, providerID)
	if err := s.putJSON(ctx, key, payload, ttl); err != nil {
		return err
	}
	if len(meta) == 0 {
		return nil
	}
	// 2026-07-23: meta key 必须显式设置 TTL，否则 HSet 隐式清除 TTL 让它永不过期。
	if err := s.rdb.HSet(ctx, cacheKeyBoardMeta(scope, days, providerID), meta).Err(); err != nil {
		return err
	}
	return s.rdb.Expire(ctx, cacheKeyBoardMeta(scope, days, providerID), ttl).Err()
}

func (s *Service) loadBaseline(ctx context.Context, scope Scope, days int) (map[string]any, time.Time, bool) {
	raw, err := s.rdb.Get(ctx, baselineKey(scope, days)).Result()
	if err != nil || raw == "" {
		return nil, time.Time{}, false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, time.Time{}, false
	}
	since := time.Time{}
	// audit-24h-20260828-r4 P2: SafeHGetAll prevents WRONGTYPE when the
	// baseline meta key collides with a non-hash type. Empty map is the
	// canonical cache-miss signal — matches raw HGetAll on an absent key.
	if m, err := redissafe.SafeHGetAll(ctx, s.rdb, baselineMetaKey(scope, days)); err == nil {
		if ts, ok := m["since_ts"]; ok {
			since, _ = time.Parse(time.RFC3339, ts)
		}
	}
	return payload, since, true
}

func (s *Service) listDirtyBuckets(ctx context.Context, scope Scope) ([]string, error) {
	return s.rdb.SMembers(ctx, dirtyKey(scope)).Result()
}

func (s *Service) readDeltaHash(ctx context.Context, scope Scope, bucketID string) (map[string]string, error) {
	// audit-24h-20260828-r4 P2: SafeHGetAll prevents WRONGTYPE — caller
	// distinguishes nil map + nil err (no delta) from real errors.
	return redissafe.SafeHGetAll(ctx, s.rdb, deltaKey(scope, bucketID))
}

func (s *Service) clearDirtyBucket(ctx context.Context, scope Scope, bucketID string) {
	_ = s.rdb.SRem(ctx, dirtyKey(scope), bucketID).Err()
}

func (s *Service) attachMeta(ctx context.Context, scope Scope, days int, providerID int64, payload map[string]any) {
	// audit-24h-20260828-r4 P2: SafeHGetAll prevents WRONGTYPE — best-effort
	// attach; cache-miss (empty map) and cache-error (TypedError) are both
	// skipped, matching the original `err != nil || len(meta) == 0` guard.
	meta, err := redissafe.SafeHGetAll(ctx, s.rdb, cacheKeyBoardMeta(scope, days, providerID))
	if err != nil || len(meta) == 0 {
		return
	}
	payload["cache_meta"] = meta
	if src, ok := meta["source"]; ok && src != "" {
		payload["source"] = src
	} else if payload["source"] == nil {
		payload["source"] = "redis_baseline_delta"
	}
}
