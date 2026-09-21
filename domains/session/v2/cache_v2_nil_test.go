package v2

import (
	"context"
	"testing"
)

func TestSessionCacheV2PartialTiersAreSafe(t *testing.T) {
	cache := &SessionCacheV2{}
	state, err := cache.Get(context.Background(), "tenant-a", "session-a")
	if err != nil || state != nil {
		t.Fatalf("Get on empty cache = (%#v, %v), want (nil, nil)", state, err)
	}
	if err := cache.Set(context.Background(), &SessionStateV2{TenantID: "tenant-a", SessionID: "session-a"}); err != nil {
		t.Fatalf("Set on empty cache: %v", err)
	}
	if err := cache.Invalidate(context.Background(), "tenant-a", "session-a"); err != nil {
		t.Fatalf("Invalidate on empty cache: %v", err)
	}
}

func TestSessionCacheV2NilCacheIsSafe(t *testing.T) {
	var cache *SessionCacheV2
	if err := cache.Invalidate(context.Background(), "tenant-a", "session-a"); err != nil {
		t.Fatalf("Invalidate on nil cache: %v", err)
	}
}
