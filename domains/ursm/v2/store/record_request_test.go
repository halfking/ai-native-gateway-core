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

func TestRecordRequestThreeFailuresDisableNode(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	now := time.Now().UnixMilli()

	// Record 3 failures - should disable the node
	for i := 0; i < 3; i++ {
		res, err := s.RecordRequest(ctx, "ursm:v2:node:2:test",
			"ursm:v2:win:1m:2:test", "ursm:v2:win:5m:2:test", "ursm:v2:win:30m:2:test",
			RecordOutcome{
				Success:      false,
				ErrorKind:    "timeout",
				NowMs:        now + int64(i)*1000,
				LatencyMs:    100,
				RequestID:    "fail-" + string(rune('0'+i)),
				NodeTTL:      time.Minute,
				Window5mTTL:  6 * time.Minute,
				Window30mTTL: 35 * time.Minute,
				CoolSeconds:  300,
			})
		if err != nil {
			t.Fatalf("record failure %d: %v", i, err)
		}
		if res.Status != "applied" {
			t.Fatalf("failure %d: status=%s", i, res.Status)
		}
	}

	// Check node state via Redis client - should be disabled
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	disabled, err := rdb.HGet(ctx, "ursm:v2:node:2:test", "disabled").Result()
	if err != nil {
		t.Fatalf("HGet disabled: %v", err)
	}
	if disabled != "1" {
		t.Fatalf("expected disabled=1, got %s", disabled)
	}
}

func TestRecordRequestDuplicateRequestDoesNotIncrementFailureState(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	outcome := RecordOutcome{
		Success: false, ErrorKind: "timeout", NowMs: now, LatencyMs: 100,
		RequestID: "same-request", DedupKey: "same-request", NodeTTL: time.Minute,
		Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
	}
	for i := 0; i < 2; i++ {
		res, err := s.RecordRequest(ctx, "ursm:v2:node:5:test",
			"ursm:v2:win:1m:5:test", "ursm:v2:win:5m:5:test", "ursm:v2:win:30m:5:test", outcome)
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if i == 0 && res.Status != "applied" {
			t.Fatalf("first status=%q, want applied", res.Status)
		}
		if i == 1 && res.Status != "duplicate" {
			t.Fatalf("second status=%q, want duplicate", res.Status)
		}
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	if got, err := rdb.HGet(ctx, "ursm:v2:node:5:test", "fail_streak").Result(); err != nil || got != "1" {
		t.Fatalf("fail_streak=%q err=%v, want 1", got, err)
	}
	if got, err := rdb.HGet(ctx, "ursm:v2:node:5:test", "generation").Result(); err != nil || got != "1" {
		t.Fatalf("generation=%q err=%v, want 1", got, err)
	}
}

func TestRecordRequestSuccessDuringCoolRecoversNode(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	now := time.Now().UnixMilli()

	// First disable the node by setting disabled=1 and cool_until_ms to future
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	err := rdb.HSet(ctx, "ursm:v2:node:3:test", map[string]interface{}{
		"disabled":      "1",
		"available":     "0",
		"cool_until_ms": "9999999999999", // far future
		"fail_streak":   "3",
	}).Err()
	if err != nil {
		t.Fatalf("HSet: %v", err)
	}

	// Now record success during cool period
	res, err := s.RecordRequest(ctx, "ursm:v2:node:3:test",
		"ursm:v2:win:1m:3:test", "ursm:v2:win:5m:3:test", "ursm:v2:win:30m:3:test",
		RecordOutcome{
			Success:      true,
			NowMs:        now,
			LatencyMs:    100,
			RequestID:    "success-during-cool",
			NodeTTL:      time.Minute,
			Window5mTTL:  6 * time.Minute,
			Window30mTTL: 35 * time.Minute,
		})
	if err != nil {
		t.Fatalf("record success during cool: %v", err)
	}
	if res.Status != "applied" {
		t.Fatalf("status=%s", res.Status)
	}

	// Check node state - should be recovered
	disabled, err := rdb.HGet(ctx, "ursm:v2:node:3:test", "disabled").Result()
	if err != nil {
		t.Fatalf("HGet disabled: %v", err)
	}
	if disabled != "0" {
		t.Fatalf("expected disabled=0 after recovery, got %s", disabled)
	}
}

func TestRecordRequestFailureDuringCoolExtendsCooldown(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	now := time.Now().UnixMilli()

	// First disable the node with a near-future cool_until
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	err := rdb.HSet(ctx, "ursm:v2:node:4:test", map[string]interface{}{
		"disabled":      "1",
		"available":     "0",
		"cool_until_ms": "9999999999999", // far future - still in cool
		"fail_streak":   "3",
		"disable_count": "1",
	}).Err()
	if err != nil {
		t.Fatalf("HSet: %v", err)
	}

	// Now record failure during cool period
	res, err := s.RecordRequest(ctx, "ursm:v2:node:4:test",
		"ursm:v2:win:1m:4:test", "ursm:v2:win:5m:4:test", "ursm:v2:win:30m:4:test",
		RecordOutcome{
			Success:      false,
			ErrorKind:    "timeout",
			NowMs:        now,
			LatencyMs:    100,
			RequestID:    "fail-during-cool",
			NodeTTL:      time.Minute,
			Window5mTTL:  6 * time.Minute,
			Window30mTTL: 35 * time.Minute,
			CoolSeconds:  300,
		})
	if err != nil {
		t.Fatalf("record failure during cool: %v", err)
	}
	if res.Status != "applied" {
		t.Fatalf("status=%s", res.Status)
	}
}
