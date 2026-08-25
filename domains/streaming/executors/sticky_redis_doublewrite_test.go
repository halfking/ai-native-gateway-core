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

	// 2026-08-26: L2 is intentionally NOT written to Redis. Only L1 and L3
	// are dual-written; L2 entry point is removed from both the lookup and
	// the persistence side so new sessions cannot inherit the previous
	// session's credential via a (client, model) L2 entry.
	l1, _, l3 := buildStickyKeys("t1", &appID, &keyID, "default", "sess1", "m")
	for level, k := range map[int]string{1: l1, 3: l3} {
		if k == "" {
			continue
		}
		redisCred, ok := store.GetLevel(context.Background(), level, k)
		if !ok || redisCred != 9 {
			t.Errorf("Redis L%d double-write missing for %q: %d ok=%v", level, k, redisCred, ok)
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

func (s *deadlineExhaustingStickyStore) ClearForCredential(context.Context, int) (int, error) {
	return 0, nil
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
	// 2026-08-26: L2 is intentionally excluded — only L3 must be written
	// after the L1 timeout. See RecordSuccessMultiLevel contract.
	if len(store.writes) != 1 || store.writes[0] != int(StickyLevelClient) {
		t.Fatalf("only L3 must write after L1 timeout (L2 excluded per 2026-08-26), got %v", store.writes)
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
