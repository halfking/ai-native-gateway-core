package credentialfpslot

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestDetailedStats_Memory(t *testing.T) {
	m := New(Config{DefaultLimit: 3, Enabled: true}, nil)
	ctx := context.Background()
	credID := 100
	limit := 3

	lim, holders, details, healthy := m.DetailedStats(ctx, credID, &limit)
	if lim == nil || *lim != 3 {
		t.Fatalf("expected limit=3, got %v", lim)
	}
	if len(holders) != 0 {
		t.Errorf("expected 0 holders, got %d", len(holders))
	}
	if healthy != 0 {
		t.Errorf("expected 0 healthy, got %d", healthy)
	}
	if len(details) != 3 {
		t.Fatalf("expected 3 details, got %d", len(details))
	}

	m.Acquire(ctx, credID, &limit, "sess-a", "tenant1")
	m.Acquire(ctx, credID, &limit, "sess-b", "tenant1")

	_, holders, details, healthy = m.DetailedStats(ctx, credID, &limit)
	if healthy != 2 {
		t.Errorf("expected 2 healthy, got %d", healthy)
	}
	if len(holders) != 2 {
		t.Errorf("expected 2 holders, got %d", len(holders))
	}
	t.Logf("holders=%v healthy=%d", holders, healthy)
}

func TestDetailedStats_Redis(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	m := New(Config{DefaultLimit: 2, Enabled: true}, client)
	ctx := context.Background()
	credID := 200
	limit := 2

	_, _, _, healthy := m.DetailedStats(ctx, credID, &limit)
	if healthy != 0 {
		t.Errorf("expected 0 healthy, got %d", healthy)
	}

	l1, _ := m.Acquire(ctx, credID, &limit, "sess-x", "tenant-x")
	_, _, details, healthy := m.DetailedStats(ctx, credID, &limit)
	if healthy != 1 {
		t.Errorf("expected 1 healthy, got %d", healthy)
	}

	for _, d := range details {
		if d.Holder == "sess-x" {
			t.Logf("slot[%d] holder=%s ttl=%ds", d.Index, d.Holder, d.TTLSeconds)
		}
	}

	m.Release(ctx, l1)
}

func TestDetailedStats_Unlimited(t *testing.T) {
	m := New(Config{DefaultLimit: 5, Enabled: true}, nil)
	ctx := context.Background()
	credID := 300

	zeroLimit := 0
	lim, _, _, _ := m.DetailedStats(ctx, credID, &zeroLimit)
	if lim != nil {
		t.Errorf("expected nil limit, got %v", lim)
	}
}

func TestDetailedStats_Disabled(t *testing.T) {
	m := New(Config{DefaultLimit: 5, Enabled: false}, nil)
	ctx := context.Background()
	credID := 400
	limit := 5

	lim, holders, details, healthy := m.DetailedStats(ctx, credID, &limit)
	if lim != nil || holders != nil || details != nil || healthy != 0 {
		t.Errorf("expected all empty when disabled")
	}
}
