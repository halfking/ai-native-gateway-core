package session

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestSessionPreferenceUsesConfiguredTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := NewRedisClient(mr.Addr(), "", 0)
	sp := NewSessionPreferenceWithTTL(rc, 2*time.Hour)

	if err := sp.Set(context.Background(), "gw-test", 21, "minimax-m3"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if ttl := mr.TTL(sp.redisKey("gw-test")); ttl != 2*time.Hour {
		t.Fatalf("TTL=%s, want 2h", ttl)
	}
}
