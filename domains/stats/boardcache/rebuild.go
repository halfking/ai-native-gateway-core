package boardcache

import (
	"context"
	"log/slog"
	"time"
)

// RebuildScope loads baseline from PostgreSQL into Redis for all day presets.
func (s *Service) RebuildScope(ctx context.Context, scope Scope) error {
	if s == nil || s.rdb == nil || s.build == nil {
		return nil
	}
	tenantFilter := TenantFilter(scope)
	since := time.Now().UTC().Truncate(time.Minute)
	for _, days := range BoardDaysPresets {
		payload, err := s.build(ctx, tenantFilter, days, 0)
		if err != nil {
			return err
		}
		if payload == nil {
			continue
		}
		payload["days"] = days
		if err := s.putBaseline(ctx, scope, days, payload, since); err != nil {
			return err
		}
		if err := s.putBoard(ctx, scope, days, 0, payload, map[string]string{
			"source":   "postgresql_baseline",
			"built_at": time.Now().UTC().Format(time.RFC3339),
			"scope":    string(scope),
		}); err != nil {
			return err
		}
	}
	// 2026-07-23: rebuild:last key 从永不过期改为 7 天 TTL。
	// 重建历史只需要覆盖重建间隔（4h）的几倍，7 天足够查询历史趋势。
	// 永不过期会让这个 key 长期累积。
	_ = s.rdb.Set(ctx, keyRebuildLast+":"+string(scope), time.Now().UTC().Format(time.RFC3339), 7*24*time.Hour).Err()
	slog.Info("boardcache baseline rebuilt", "scope", scope)
	return nil
}

func (s *Service) rebuildAll(ctx context.Context) {
	if s.build == nil {
		return
	}
	_ = s.RebuildScope(ctx, ScopeGlobal)
	tenants, err := s.rdb.SMembers(ctx, "llmgw:live:tenants").Result()
	if err != nil {
		return
	}
	for _, tid := range tenants {
		if tid == "" {
			continue
		}
		_ = s.RebuildScope(ctx, ScopeTenant(tid))
	}
}

func (s *Service) recentQPS(ctx context.Context) int64 {
	n, err := s.rdb.ZCard(ctx, keyQPSWindow).Result()
	if err != nil {
		return 0
	}
	return n / 5
}

func (s *Service) acquireRebuildLock(ctx context.Context) bool {
	ok, err := s.rdb.SetNX(ctx, keyRebuildLock, "1", 30*time.Minute).Result()
	return err == nil && ok
}

func (s *Service) releaseRebuildLock(ctx context.Context) {
	_ = s.rdb.Del(ctx, keyRebuildLock).Err()
}

func (s *Service) shouldRebuild(ctx context.Context, windowStart time.Time) bool {
	lastRaw, err := s.rdb.Get(ctx, keyRebuildLast+":global").Result()
	if err != nil || lastRaw == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339, lastRaw)
	if err != nil {
		return true
	}
	if time.Since(last) < rebuildInterval() {
		return false
	}
	if time.Since(windowStart) > rebuildMaxDelay() {
		return true
	}
	return s.recentQPS(ctx) < rebuildIdleQPS()
}
