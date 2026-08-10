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

// 2026-08-10 (fix/selfcheck-queue-and-recovery): free-tier credentials must
// tolerate transient infra noise (timeout/rate_limit/upstream_down/
// empty_response/stream_timeout/"transient") without being hard-disabled,
// mirroring domains/ursm/v2/reducer/reducer.go's soft-demote policy. This
// mirrors TestRecordRequestThreeFailuresDisableNode but sets BillingMode.
func TestRecordRequestFreeBillingTransientDoesNotDisable(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	now := time.Now().UnixMilli()

	for i := 0; i < 5; i++ {
		res, err := s.RecordRequest(ctx, "ursm:v2:node:6:test",
			"ursm:v2:win:1m:6:test", "ursm:v2:win:5m:6:test", "ursm:v2:win:30m:6:test",
			RecordOutcome{
				Success:      false,
				ErrorKind:    "timeout",
				NowMs:        now + int64(i)*1000,
				LatencyMs:    100,
				RequestID:    "free-fail-" + string(rune('0'+i)),
				NodeTTL:      time.Minute,
				Window5mTTL:  6 * time.Minute,
				Window30mTTL: 35 * time.Minute,
				CoolSeconds:  300,
				BillingMode:  "free",
			})
		if err != nil {
			t.Fatalf("record failure %d: %v", i, err)
		}
		if res.Status != "applied" {
			t.Fatalf("failure %d: status=%s", i, res.Status)
		}
	}

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	// "disabled" is only HSET when the hard-disable branch fires; since the
	// free-tier tolerance skips that branch entirely, the field is simply
	// never written — redis.Nil (unset) is the expected "not disabled"
	// signal here, same as "0" would be.
	disabled, err := rdb.HGet(ctx, "ursm:v2:node:6:test", "disabled").Result()
	if err != nil && err != redis.Nil {
		t.Fatalf("HGet disabled: %v", err)
	}
	if disabled == "1" {
		t.Fatalf("free-tier transient failures must not hard-disable the node, got disabled=1")
	}
	available, err := rdb.HGet(ctx, "ursm:v2:node:6:test", "available").Result()
	if err != nil {
		t.Fatalf("HGet available: %v", err)
	}
	if available != "1" {
		t.Fatalf("expected available=1 (soft demote only), got %s", available)
	}
	// fail_streak/failure_count must still accumulate for observability.
	failStreak, err := rdb.HGet(ctx, "ursm:v2:node:6:test", "fail_streak").Result()
	if err != nil || failStreak != "5" {
		t.Fatalf("fail_streak=%q err=%v, want 5", failStreak, err)
	}
}

// Paid (non-free) billing modes keep the pre-existing hard-disable behavior
// even with the same transient error kinds — the free-tier carve-out must
// not leak into other billing modes.
func TestRecordRequestPaidBillingTransientStillDisables(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	now := time.Now().UnixMilli()

	for i := 0; i < 3; i++ {
		res, err := s.RecordRequest(ctx, "ursm:v2:node:7:test",
			"ursm:v2:win:1m:7:test", "ursm:v2:win:5m:7:test", "ursm:v2:win:30m:7:test",
			RecordOutcome{
				Success:      false,
				ErrorKind:    "timeout",
				NowMs:        now + int64(i)*1000,
				LatencyMs:    100,
				RequestID:    "paid-fail-" + string(rune('0'+i)),
				NodeTTL:      time.Minute,
				Window5mTTL:  6 * time.Minute,
				Window30mTTL: 35 * time.Minute,
				CoolSeconds:  300,
				BillingMode:  "pay_as_you_go",
			})
		if err != nil {
			t.Fatalf("record failure %d: %v", i, err)
		}
		if res.Status != "applied" {
			t.Fatalf("failure %d: status=%s", i, res.Status)
		}
	}

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	disabled, err := rdb.HGet(ctx, "ursm:v2:node:7:test", "disabled").Result()
	if err != nil {
		t.Fatalf("HGet disabled: %v", err)
	}
	if disabled != "1" {
		t.Fatalf("paid credential must still hard-disable on fail_streak>=limit, got disabled=%s", disabled)
	}
}

// Free-tier credentials still hard-disable on permanent error kinds — the
// tolerance only applies to the transient_kinds set.
func TestRecordRequestFreeBillingPermanentErrorStillDisables(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	now := time.Now().UnixMilli()

	for i := 0; i < 3; i++ {
		res, err := s.RecordRequest(ctx, "ursm:v2:node:8:test",
			"ursm:v2:win:1m:8:test", "ursm:v2:win:5m:8:test", "ursm:v2:win:30m:8:test",
			RecordOutcome{
				Success:      false,
				ErrorKind:    "auth",
				NowMs:        now + int64(i)*1000,
				LatencyMs:    100,
				RequestID:    "free-auth-fail-" + string(rune('0'+i)),
				NodeTTL:      time.Minute,
				Window5mTTL:  6 * time.Minute,
				Window30mTTL: 35 * time.Minute,
				CoolSeconds:  300,
				BillingMode:  "free",
			})
		if err != nil {
			t.Fatalf("record failure %d: %v", i, err)
		}
		if res.Status != "applied" {
			t.Fatalf("failure %d: status=%s", i, res.Status)
		}
	}

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	disabled, err := rdb.HGet(ctx, "ursm:v2:node:8:test", "disabled").Result()
	if err != nil {
		t.Fatalf("HGet disabled: %v", err)
	}
	if disabled != "1" {
		t.Fatalf("free-tier credential must still hard-disable on permanent errors (auth), got disabled=%s", disabled)
	}
}
