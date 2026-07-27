package routing

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	ursmcache "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/cache"
)

func TestStickyDoubleWriteMultiLevel(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := ursmcache.NewStickyStore(rdb, 100, time.Hour)

	s := NewStickyCache()
	s.SetRedisStore(store)

	s.RecordSuccessMultiLevel("t1", intPtr(1), intPtr(2), "default", "sess1", "m", 9)

	// 内存命中
	cred := s.GetMultiLevel("t1", intPtr(1), intPtr(2), "default", "sess1", "m")
	if !cred.Found || cred.CredentialID != 9 {
		t.Fatalf("in-memory lookup failed: %+v", cred)
	}

	// Redis 侧三级都写入了(用显式 level 验证, 不依赖 levelOf)
	l1, l2, l3 := buildStickyKeys("t1", intPtr(1), intPtr(2), "default", "sess1", "m")
	for level, k := range map[int]string{1: l1, 2: l2, 3: l3} {
		if k == "" {
			continue
		}
		redisCred, ok := store.GetLevel(context.Background(), level, k)
		if !ok || redisCred != 9 {
			t.Errorf("Redis L%d double-write missing for %q: %d ok=%v", level, k, redisCred, ok)
		}
	}
}

func TestStickyDeleteMultiLevel_ClearsRedisOnColdLRU(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := ursmcache.NewStickyStore(rdb, 100, time.Hour)
	s := NewStickyCache()
	s.SetRedisStore(store)

	l1, l2, l3 := buildStickyKeys("t3", intPtr(1), intPtr(2), "default", "sess3", "m")
	for level, key := range map[int]string{1: l1, 2: l2, 3: l3} {
		if key != "" {
			if err := store.SetLevel(context.Background(), level, 9, key, time.Hour); err != nil {
				t.Fatal(err)
			}
		}
	}
	s.DeleteMultiLevel("t3", intPtr(1), intPtr(2), "default", "sess3", "m", 9)
	for level, key := range map[int]string{1: l1, 2: l2, 3: l3} {
		if key != "" {
			if _, ok := store.GetLevel(context.Background(), level, key); ok {
				t.Errorf("Redis L%d binding was not deleted", level)
			}
		}
	}
}

func TestStickyDeleteMultiLevel_DoesNotDeleteReboundRedisCredential(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := ursmcache.NewStickyStore(rdb, 100, time.Hour)
	s := NewStickyCache()
	s.SetRedisStore(store)

	l1, _, _ := buildStickyKeys("t4", intPtr(1), intPtr(2), "default", "sess4", "m")
	if err := store.SetLevel(context.Background(), 1, 10, l1, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLevel(context.Background(), 1, 11, l1, time.Hour); err != nil {
		t.Fatal(err)
	}
	s.DeleteMultiLevel("t4", intPtr(1), intPtr(2), "default", "sess4", "m", 10)
	if got, ok := store.GetLevel(context.Background(), 1, l1); !ok || got != 11 {
		t.Fatalf("rebound Redis credential changed: got=%d ok=%v", got, ok)
	}
}
func TestStickyNilRedisStoreDegraded(t *testing.T) {
	// 不注入 redisStore 时退化为纯内存(旧行为)
	s := NewStickyCache()
	s.RecordSuccessMultiLevel("t1", intPtr(1), intPtr(2), "default", "sess1", "m", 5)
	cred := s.GetMultiLevel("t1", intPtr(1), intPtr(2), "default", "sess1", "m")
	if !cred.Found || cred.CredentialID != 5 {
		t.Fatalf("nil-store lookup failed: %+v", cred)
	}
}

func TestStickyRedisFallbackOnMemoryMiss(t *testing.T) {
	// 内存 miss 时, GetMultiLevel 应回源 Redis 并回填内存。
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := ursmcache.NewStickyStore(rdb, 100, time.Hour)

	s := NewStickyCache()
	s.SetRedisStore(store)

	// 直接写 Redis(不经 StickyCache), 模拟另一实例写入
	l1, _, _ := buildStickyKeys("t2", intPtr(1), intPtr(2), "default", "sess2", "m")
	if err := store.SetLevel(context.Background(), 1, 7, l1, time.Hour); err != nil {
		t.Fatal(err)
	}

	// 内存应为空, 回源 Redis 命中 L1
	if s.Len() != 0 {
		t.Fatalf("memory should be empty, got len=%d", s.Len())
	}
	cred := s.GetMultiLevel("t2", intPtr(1), intPtr(2), "default", "sess2", "m")
	if !cred.Found || cred.CredentialID != 7 {
		t.Fatalf("redis fallback lookup failed: %+v", cred)
	}
	// Redis 命中 L1, Level 应映射到 StickyLevelSession(原 bug 硬编码 StickyLevelClient)
	if cred.Level != StickyLevelSession {
		t.Errorf("Redis L1 fallback Level: want StickyLevelSession, got %d", cred.Level)
	}
	// 回填后内存应有该 entry
	if s.Len() == 0 {
		t.Fatal("memory not backfilled after redis fallback")
	}
	// 第二次应从内存命中(此时 Redis 可被清空仍命中)
	mr.FlushDB()
	cred2 := s.GetMultiLevel("t2", intPtr(1), intPtr(2), "default", "sess2", "m")
	if !cred2.Found || cred2.CredentialID != 7 {
		t.Fatalf("in-memory lookup after backfill failed: %+v", cred2)
	}
}

func intPtr(i int) *int { return &i }
