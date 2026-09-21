package routing

// sticky_typednil_test.go — R37 self-audit pin: StickyCache is the dormant
// twin of executors/sticky.go (production calls the latter). 59eb7fa06 fixed
// the typed-nil crash there (252: caller holds a nil *ursmcache.StickyStore
// concrete pointer, stores it in the interface, `store != nil` passes, nil
// receiver panics); the twin got the same normalization with a warning
// comment. This test makes the twin's normalization regression-visible.

import (
	"context"
	"testing"
	"time"
)

type stubStickyRedisStore struct{}

func (stubStickyRedisStore) SetLevel(context.Context, int, int, string, time.Duration) error {
	return nil
}
func (stubStickyRedisStore) GetLevel(context.Context, int, string) (int, bool) {
	return 0, false
}
func (stubStickyRedisStore) DeleteLevelIfCredential(context.Context, int, string, int) error {
	return nil
}

func TestStickyCacheSetRedisStoreNormalizesTypedNil(t *testing.T) {
	s := NewStickyCache()

	// A nil concrete pointer inside the interface must not survive as a
	// non-nil interface value.
	var typedNil *stubStickyRedisStore
	s.SetRedisStore(typedNil)
	if got := s.redisStoreSnapshot(); got != nil {
		t.Fatalf("typed-nil store survived SetRedisStore: %T", got)
	}

	// A real store must be preserved.
	s.SetRedisStore(stubStickyRedisStore{})
	if got := s.redisStoreSnapshot(); got == nil {
		t.Fatal("real store was dropped")
	}

	// Plain nil stays nil.
	s.SetRedisStore(nil)
	if got := s.redisStoreSnapshot(); got != nil {
		t.Fatalf("nil store became %T", got)
	}
}
