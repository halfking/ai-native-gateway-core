package boardcache

// R59 audit (S7-2) regression: the client_ip dim (B7 real source, produced
// by minute_entry.addDim and the rollup HOST(client_ip)) must survive the
// Redis-boardcache fold into the client_ips pie. Pre-fix, dimPieKey had no
// client_ip entry and fold.go dropped every delta — the pie froze at its
// baseline snapshot. Sensitivity: removing the dimPieKey entry turns this
// test red.

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestFoldMapsClientIPDimIntoClientIPsPie(t *testing.T) {
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

	bucket := bucketIDFor(now)
	if err := client.HSet(ctx, deltaKey(ScopeGlobal, bucket),
		"d:client_ip:203.0.113.7:requests", 3,
	).Err(); err != nil {
		t.Fatalf("hset delta: %v", err)
	}
	client.SAdd(ctx, dirtyKey(ScopeGlobal), bucket)
	service.foldScope(ctx, ScopeGlobal)

	board, ok := service.GetBoard(ctx, ScopeGlobal, 1)
	if !ok {
		t.Fatal("expected folded board")
	}
	pies, _ := board["pies"].(map[string]any)
	if pies == nil {
		t.Fatal("board has no pies")
	}
	items, _ := pies["client_ips"].([]any)
	if len(items) == 0 {
		t.Fatalf("client_ips pie empty after fold — client_ip dim deltas are being dropped (S7-2)")
	}
	found := false
	for _, raw := range items {
		m, _ := raw.(map[string]any)
		if m == nil {
			continue
		}
		if m["key"] == "203.0.113.7" && toInt64(m["requests"]) == 3 {
			found = true
		}
	}
	if !found {
		t.Fatalf("client_ips pie missing key 203.0.113.7 with requests=3: %+v", items)
	}
}
