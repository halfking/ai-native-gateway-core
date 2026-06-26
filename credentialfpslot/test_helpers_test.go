package credentialfpslot

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestManager(t *testing.T, cfg Config) (*Manager, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
		mr.Close()
	})
	return New(cfg, client), mr
}

func acquireSuccess(t *testing.T, m *Manager, ctx context.Context, credentialID int, limit *int, holder, tenantID string) *Lease {
	t.Helper()
	lease, ok := m.Acquire(ctx, credentialID, limit, holder, tenantID)
	if !ok {
		t.Fatalf("Acquire(%d,%q) failed", credentialID, holder)
	}
	if lease == nil {
		t.Fatalf("Acquire(%d,%q) returned nil lease", credentialID, holder)
	}
	return lease
}

func acquireExpectSaturated(t *testing.T, m *Manager, ctx context.Context, credentialID int, limit *int, holder, tenantID string) {
	t.Helper()
	lease, ok := m.Acquire(ctx, credentialID, limit, holder, tenantID)
	if ok {
		t.Fatalf("expected saturation, got lease=%v ok=%v", lease, ok)
	}
}
