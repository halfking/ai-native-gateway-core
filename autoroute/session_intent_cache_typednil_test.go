package autoroute

// session_intent_cache_typednil_test.go — R37 self-audit pin: SetRedisStore
// must normalize a nil concrete pointer inside the interface (main.go holds
// *ursmcache.IntentStore; with Redis unavailable it is a typed nil and the
// redisFallback `c.redisStore == nil` guard would pass, then panic on the nil
// receiver). Currently unreachable (the fpSlotRedis gate assigns both
// clients in the same branch), pinned so the two sources may not diverge
// silently (same class as 59eb7fa06's executors/sticky.go 252 crash).

import (
	"context"
	"testing"
	"time"

	ursmcache "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/cache"
)

type stubIntentStore struct{}

func (stubIntentStore) Set(context.Context, string, ursmcache.Intent, time.Duration) error {
	return nil
}
func (stubIntentStore) Get(context.Context, string) (ursmcache.Intent, bool) {
	return ursmcache.Intent{}, false
}
func (stubIntentStore) Delete(context.Context, string) error { return nil }

func TestSessionIntentCacheSetRedisStoreNormalizesTypedNil(t *testing.T) {
	c := NewSessionIntentCache(time.Minute)

	var typedNil *stubIntentStore
	c.SetRedisStore(typedNil)
	if c.redisStore != nil {
		t.Fatalf("typed-nil store survived SetRedisStore: %T", c.redisStore)
	}

	c.SetRedisStore(stubIntentStore{})
	if c.redisStore == nil {
		t.Fatal("real store was dropped")
	}

	c.SetRedisStore(nil)
	if c.redisStore != nil {
		t.Fatalf("nil store became %T", c.redisStore)
	}
}
