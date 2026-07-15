package ipblocklist

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	defaultAttackThreshold = 20
	defaultAttackWindow    = 10 * time.Minute
	defaultAutoBlockTTL    = 24 * time.Hour
	attackKeyPrefix        = "llmgw:attack:"
)

// AttackTracker counts failed auth per IP and auto-blocks abusive clients.
type AttackTracker struct {
	rdb       *redis.Client
	store     Store
	cache     *Cache
	threshold int
	window    time.Duration
	blockTTL  time.Duration
}

func NewAttackTracker(rdb *redis.Client, store Store, cache *Cache) *AttackTracker {
	return &AttackTracker{
		rdb:       rdb,
		store:     store,
		cache:     cache,
		threshold: defaultAttackThreshold,
		window:    defaultAttackWindow,
		blockTTL:  defaultAutoBlockTTL,
	}
}

func (t *AttackTracker) key(ip string, scope string) string {
	return attackKeyPrefix + scope + ":" + ip
}

// RecordFailure increments attack counter; auto-blocks when threshold exceeded.
func (t *AttackTracker) RecordFailure(ctx context.Context, ip string, scope, reason string) error {
	if ip == "" {
		return nil
	}
	count := 1
	if t.rdb != nil {
		k := t.key(ip, scope)
		n, err := t.rdb.Incr(ctx, k).Result()
		if err == nil {
			_ = t.rdb.Expire(ctx, k, t.window).Err()
			count = int(n)
		}
	}
	if count < t.threshold {
		return nil
	}
	expires := time.Now().Add(t.blockTTL)
	_, err := t.store.Create(ctx, CreateInput{
		IPOrCIDR:  ip,
		Reason:    fmt.Sprintf("auto_attack: %s (failures=%d)", reason, count),
		Scope:     scope,
		Source:    SourceAutoAttack,
		ExpiresAt: &expires,
		CreatedBy: "system",
	})
	if err != nil {
		return err
	}
	if t.cache != nil {
		_ = t.cache.Invalidate(ctx, scope)
		_ = t.cache.Invalidate(ctx, ScopeGlobal)
	}
	if t.rdb != nil {
		_ = t.rdb.Del(ctx, t.key(ip, scope)).Err()
	}
	return nil
}

// Service combines store, cache, and attack tracking for handlers/middleware.
type Service struct {
	Store   Store
	Cache   *Cache
	Tracker *AttackTracker
}

func NewService(pool *pgxpool.Pool, rdb *redis.Client) *Service {
	store := NewPgxStore(pool)
	cache := NewCache(rdb, store, 60*time.Second)
	return &Service{
		Store:   store,
		Cache:   cache,
		Tracker: NewAttackTracker(rdb, store, cache),
	}
}

func (s *Service) Warmup(ctx context.Context) error {
	for _, scope := range []string{ScopeGlobal, ScopeCollect, ScopeOps} {
		if err := s.Cache.Reload(ctx, scope); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) AfterMutation(ctx context.Context, scope string) {
	_ = s.Cache.Invalidate(ctx, scope)
	if scope != ScopeGlobal {
		_ = s.Cache.Invalidate(ctx, ScopeGlobal)
	}
}
