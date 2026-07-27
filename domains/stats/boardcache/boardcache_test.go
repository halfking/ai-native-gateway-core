package boardcache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestScopesForEntry(t *testing.T) {
	scopes := ScopesForEntry("acme")
	if len(scopes) != 2 {
		t.Fatalf("expected 2 scopes, got %d", len(scopes))
	}
	if scopes[0] != ScopeGlobal {
		t.Fatalf("first scope should be global, got %s", scopes[0])
	}
	if scopes[1] != ScopeTenant("acme") {
		t.Fatalf("second scope should be tenant:acme, got %s", scopes[1])
	}
}

func TestTenantFilter(t *testing.T) {
	if TenantFilter(ScopeGlobal) != "" {
		t.Fatal("global should map to empty tenant filter")
	}
	if TenantFilter(ScopeTenant("x")) != "x" {
		t.Fatal("tenant scope filter mismatch")
	}
}

func TestApplySummaryDelta(t *testing.T) {
	board := map[string]any{
		"summary": map[string]any{
			"total_requests": int64(10),
			"success_rate":   0.8,
			"avg_latency_ms": float64(100),
		},
	}
	applySummaryDelta(board, summaryCounters{
		Requests:     2,
		Success:      2,
		LatencyMsSum: 200,
	})
	s := board["summary"].(map[string]any)
	if s["total_requests"].(int64) != 12 {
		t.Fatalf("requests=%v", s["total_requests"])
	}
}

func TestParseDimField(t *testing.T) {
	f := dimField("model", "gpt-4o", "requests")
	dt, dk, m, ok := parseDimField(f)
	if !ok || dt != "model" || dk != "gpt-4o" || m != "requests" {
		t.Fatalf("parse failed: %s %s %s", dt, dk, m)
	}
}

func TestFoldScopePreservesPreviouslyFoldedDeltas(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	service := New(client)
	ctx := context.Background()
	now := time.Now().UTC()

	baseline := map[string]any{
		"summary": map[string]any{"total_requests": int64(0)},
		"pies":    map[string]any{},
		"trends":  []any{},
	}
	if err := service.putBaseline(ctx, ScopeGlobal, 1, baseline, now.Add(-time.Minute)); err != nil {
		t.Fatalf("put baseline: %v", err)
	}

	firstBucket := bucketIDFor(now)
	client.HSet(ctx, deltaKey(ScopeGlobal, firstBucket), fieldReq, 1)
	client.SAdd(ctx, dirtyKey(ScopeGlobal), firstBucket)
	service.foldScope(ctx, ScopeGlobal)

	secondBucket := bucketIDFor(now.Add(time.Second))
	client.HSet(ctx, deltaKey(ScopeGlobal, secondBucket), fieldReq, 1)
	client.SAdd(ctx, dirtyKey(ScopeGlobal), secondBucket)
	service.foldScope(ctx, ScopeGlobal)

	board, ok := service.GetBoard(ctx, ScopeGlobal, 1)
	if !ok {
		t.Fatal("expected folded board")
	}
	summary := board["summary"].(map[string]any)
	if got := toInt64(summary["total_requests"]); got != 2 {
		t.Fatalf("total_requests=%d, want 2", got)
	}
}
