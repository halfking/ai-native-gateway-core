package store

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return &Store{rdb: rdb}, mr
}

func TestRecordRequestSuccess(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	res, err := s.RecordRequest(ctx, "ursm:v2:node:1:m",
		"ursm:v2:win:1m:1:m", "ursm:v2:win:5m:1:m", "ursm:v2:win:30m:1:m",
		RecordOutcome{Success: true, NowMs: time.Now().UnixMilli(), LatencyMs: 123, RequestID: "r1",
			NodeTTL: time.Minute, Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if res.Status != "applied" {
		t.Fatalf("status=%s", res.Status)
	}
}
