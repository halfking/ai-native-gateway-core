package executors

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	ursmcache "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/cache"
)

func TestExecutorsStickyDoubleWriteMultiLevel(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := ursmcache.NewStickyStore(rdb, 100, time.Hour)

	s := NewStickyCache()
	s.SetRedisStore(store)

	appID, keyID := 1, 2
	s.RecordSuccessMultiLevel("t1", &appID, &keyID, "default", "sess1", "m", 9)

	cred := s.GetMultiLevel("t1", &appID, &keyID, "default", "sess1", "m")
	if !cred.Found || cred.CredentialID != 9 {
		t.Fatalf("in-memory lookup failed: %+v", cred)
	}

	// Redis 三级都写入了(显式 level 验证)
	l1, l2, l3 := buildStickyKeys("t1", &appID, &keyID, "default", "sess1", "m")
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

func TestExecutorsStickyRedisFallback(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := ursmcache.NewStickyStore(rdb, 100, time.Hour)

	s := NewStickyCache()
	s.SetRedisStore(store)

	appID, keyID := 1, 2
	l1, _, _ := buildStickyKeys("t1", &appID, &keyID, "default", "sess2", "m")
	// 直接写 Redis L1, 内存 miss 后应回源
	store.SetLevel(context.Background(), 1, 7, l1, time.Hour)

	cred := s.GetMultiLevel("t1", &appID, &keyID, "default", "sess2", "m")
	if !cred.Found || cred.CredentialID != 7 {
		t.Fatalf("Redis fallback failed: %+v", cred)
	}
	if cred.Level != StickyLevelSession {
		t.Errorf("fallback Level: want StickyLevelSession, got %d", cred.Level)
	}
}

func TestExecutorsStickyNilStoreDegraded(t *testing.T) {
	s := NewStickyCache()
	appID, keyID := 1, 2
	s.RecordSuccessMultiLevel("t1", &appID, &keyID, "default", "sess1", "m", 5)
	cred := s.GetMultiLevel("t1", &appID, &keyID, "default", "sess1", "m")
	if !cred.Found || cred.CredentialID != 5 {
		t.Fatalf("nil-store lookup failed: %+v", cred)
	}
}
