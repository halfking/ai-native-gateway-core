package executors

import (
	"context"
	"sync"
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

	// Complete session/model requests persist L1 only. L2/L3 must never
	// contaminate a later session's first load-balancing decision.
	l1, l2, l3 := buildStickyKeys("t1", &appID, &keyID, "default", "sess1", "m")
	redisCred, ok := store.GetLevel(context.Background(), int(StickyLevelSession), l1)
	if !ok || redisCred != 9 {
		t.Errorf("Redis L1 double-write missing for %q: %d ok=%v", l1, redisCred, ok)
	}
	for level, k := range map[int]string{int(StickyLevelClientModel): l2, int(StickyLevelClient): l3} {
		if _, ok := store.GetLevel(context.Background(), level, k); ok {
			t.Errorf("Redis L%d must not be written for a complete session/model request", level)
		}
	}
}

type deadlineExhaustingStickyStore struct {
	mu     sync.Mutex
	writes []int
}

func (s *deadlineExhaustingStickyStore) SetLevel(ctx context.Context, level int, _ int, _ string, _ time.Duration) error {
	if level == int(StickyLevelSession) {
		<-ctx.Done()
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.writes = append(s.writes, level)
	s.mu.Unlock()
	return nil
}

func (s *deadlineExhaustingStickyStore) GetLevel(context.Context, int, string) (int, bool) {
	return 0, false
}

func (s *deadlineExhaustingStickyStore) DeleteLevelIfCredential(context.Context, int, string, int) error {
	return nil
}

func TestExecutorsStickyDoubleWriteKeepsLaterLevelsAfterFirstTimeout(t *testing.T) {
	store := &deadlineExhaustingStickyStore{}
	cache := NewStickyCache()
	t.Cleanup(cache.Close)
	cache.SetRedisStore(store)

	appID, keyID := 1, 2
	cache.RecordSuccessMultiLevel("t1", &appID, &keyID, "default", "sess1", "m", 9)

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.writes) != 0 {
		t.Fatalf("complete session/model request must not write L2/L3 after an L1 timeout, got %v", store.writes)
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

func TestExecutorsStickyRedisDoesNotUseLegacyL2OrL3ForNewSession(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := ursmcache.NewStickyStore(rdb, 100, time.Hour)

	s := NewStickyCache()
	s.SetRedisStore(store)

	appID, keyID := 1, 2
	_, l2, l3 := buildStickyKeys("t1", &appID, &keyID, "default", "old-session", "m")
	if err := store.SetLevel(context.Background(), int(StickyLevelClientModel), 42, l2, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLevel(context.Background(), int(StickyLevelClient), 42, l3, time.Hour); err != nil {
		t.Fatal(err)
	}

	lookup := s.GetMultiLevel("t1", &appID, &keyID, "default", "new-session", "m")
	if lookup.Found {
		t.Fatalf("new session must not inherit legacy L2/L3 Redis binding, got %+v", lookup)
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
