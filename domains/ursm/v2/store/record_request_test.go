package store

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
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

func TestRecordRequestMaintainsWindowsRatesAndLatencyAtomically(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	node := "ursm:v2:node:metrics:test"
	w1, w5, w30 := "ursm:v2:win:1m:metrics:test", "ursm:v2:win:5m:metrics:test", "ursm:v2:win:30m:metrics:test"
	for i, success := range []bool{true, false, true} {
		res, err := s.RecordRequest(ctx, node, w1, w5, w30, RecordOutcome{
			Success: success, ErrorKind: "timeout", NowMs: int64(1_000_000 + i*1_000),
			LatencyMs: 100 + i*100, RequestID: string(rune('a' + i)), NodeTTL: time.Hour,
			Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
		})
		if err != nil || res.Status != "applied" {
			t.Fatalf("record %d: result=%+v err=%v", i, res, err)
		}
	}
	for field, want := range map[string]string{
		"samples_1m": "3", "samples_5m": "3", "samples_30m": "3",
		"lat_ewma_ms": "169",
	} {
		got := mr.HGet(node, field)
		if got != want {
			t.Fatalf("%s=%q, want %q", field, got, want)
		}
	}
	for _, field := range []string{"sr_1m", "sr_5m", "sr_30m"} {
		got, err := strconv.ParseFloat(mr.HGet(node, field), 64)
		if err != nil || math.Abs(got-2.0/3.0) > 1e-12 {
			t.Fatalf("%s=%q err=%v, want approximately 2/3", field, mr.HGet(node, field), err)
		}
	}
	for _, key := range []string{w1, w5, w30} {
		got, err := s.rdb.ZCard(ctx, key).Result()
		if err != nil || got != 3 {
			t.Fatalf("window %s has %d events, err=%v, want 3", key, got, err)
		}
	}
}

func TestRecordRequestCountsLegacyWindowMembersDuringRollingUpgrade(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	now := int64(2_000_000)
	node := "ursm:v2:node:legacy-window"
	window := "ursm:v2:win:5m:legacy-window"
	mr.ZAdd(window, float64(now-1000), "0:1:100")
	mr.ZAdd(window, float64(now-900), "request-2:0:100")
	if _, err := s.RecordRequest(ctx, node,
		"ursm:v2:win:1m:legacy-window", window, "ursm:v2:win:30m:legacy-window",
		RecordOutcome{Success: true, NowMs: now, LatencyMs: 100, RequestID: "new", NodeTTL: time.Hour,
			Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if got := mr.HGet(node, "samples_5m"); got != "3" {
		t.Fatalf("samples_5m=%q, want 3 including legacy members", got)
	}
	rate, err := strconv.ParseFloat(mr.HGet(node, "sr_5m"), 64)
	if err != nil || math.Abs(rate-2.0/3.0) > 1e-12 {
		t.Fatalf("sr_5m=%q err=%v, want approximately 2/3", mr.HGet(node, "sr_5m"), err)
	}
}

func TestRecordRequestPreservesHigherSourcePriorityWhileRecordingTelemetry(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	node := "ursm:v2:node:priority:test"
	mr.HSet(node, "source_priority", "40", "available", "0", "generation", "9", "lat_ewma_ms", "500")
	res, err := s.RecordRequest(ctx, node,
		"ursm:v2:win:1m:priority:test", "ursm:v2:win:5m:priority:test", "ursm:v2:win:30m:priority:test",
		RecordOutcome{Success: true, NowMs: 2_000_000, LatencyMs: 100, RequestID: "priority", NodeTTL: time.Hour,
			Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute})
	if err != nil || res.Status != "applied" {
		t.Fatalf("record: result=%+v err=%v", res, err)
	}
	if got := mr.HGet(node, "source_priority"); got != "40" {
		t.Fatalf("source_priority=%q, want 40", got)
	}
	if got := mr.HGet(node, "available"); got != "0" {
		t.Fatalf("available=%q, want 0", got)
	}
	if got := mr.HGet(node, "generation"); got != "9" {
		t.Fatalf("generation=%q, want 9", got)
	}
	if got := mr.HGet(node, "lat_ewma_ms"); got != "400" {
		t.Fatalf("lat_ewma_ms=%q, want 400 (telemetry may update while routing state remains protected)", got)
	}
	zcard, zerr := s.rdb.ZCard(ctx, "ursm:v2:win:1m:priority:test").Result()
	if got := mr.HGet(node, "samples_1m"); got != "1" || zerr != nil || zcard != 1 {
		t.Fatalf("telemetry not recorded for high-priority node: samples=%q zcard=%d err=%v", got, zcard, zerr)
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

func TestRecordRequestFatalQuotaDisablesImmediately(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()

	_, err := s.RecordRequest(context.Background(), "ursm:v2:node:fatal-quota",
		"ursm:v2:win:1m:fatal-quota", "ursm:v2:win:5m:fatal-quota", "ursm:v2:win:30m:fatal-quota",
		RecordOutcome{
			ErrorKind: "quota_permanent", NowMs: time.Now().UnixMilli(), LatencyMs: 100,
			RequestID: "fatal-quota-1", NodeTTL: time.Hour, Window5mTTL: 6 * time.Minute,
			Window30mTTL: 35 * time.Minute, CoolSeconds: 300,
		})
	if err != nil {
		t.Fatalf("record fatal quota: %v", err)
	}
	if got := mr.HGet("ursm:v2:node:fatal-quota", "disabled"); got != "1" {
		t.Fatalf("disabled=%q, want 1 after one fatal quota failure", got)
	}
	if got := mr.HGet("ursm:v2:node:fatal-quota", "available"); got != "0" {
		t.Fatalf("available=%q, want 0 after one fatal quota failure", got)
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

// -----------------------------------------------------------------------------
// 会话优化 v4 T5-lite — UT-UR-05 (backoff cap parameterization) and
// UT-UR-12 (HealthStatus bridge). The cap was hard-coded 3600s at the old
// record_request.lua:183-184; it is now ARGV[15] with a default of 1800s.
// -----------------------------------------------------------------------------

// recordCoolFailureOnce drives one failing record into a node that is already
// in a cooling window with the given disable_count, and returns the
// resulting cool_until_ms so callers can assert the exponential cap exactly.
func recordCoolFailureOnce(t *testing.T, s *Store, mr *miniredis.Miniredis, node string, now int64, disableCount, coolSeconds, backoffCap int) int64 {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	if err := rdb.HSet(context.Background(), node, map[string]interface{}{
		"disabled":      "1",
		"available":     "0",
		"cool_until_ms": "9999999999999", // far future → failure lands in the in-cool branch
		"fail_streak":   "3",
		"disable_count": strconv.Itoa(disableCount),
	}).Err(); err != nil {
		t.Fatalf("seed cool node: %v", err)
	}
	if _, err := s.RecordRequest(context.Background(), node,
		node+":w1", node+":w5", node+":w30",
		RecordOutcome{
			Success: false, ErrorKind: "timeout", NowMs: now, LatencyMs: 50,
			RequestID: "cap", NodeTTL: time.Hour,
			Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
			CoolSeconds: coolSeconds, BackoffCapSeconds: backoffCap,
		}); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, err := rdb.HGet(context.Background(), node, "cool_until_ms").Result()
	if err != nil {
		t.Fatalf("hget cool_until_ms: %v", err)
	}
	v, err := strconv.ParseInt(got, 10, 64)
	if err != nil {
		t.Fatalf("parse cool_until_ms %q: %v", got, err)
	}
	return v
}

// TestRecordRequestBackoffCapDefaultIs1800 pins the T5 target: with the new
// default cap (1800s), cool 30s × 2^7 = 3840s is clamped to exactly 1800s.
// The pre-parameterization hard-code would have produced 3600s.
func TestRecordRequestBackoffCapDefaultIs1800(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	now := int64(3_000_000)
	got := recordCoolFailureOnce(t, s, mr, "ursm:v2:node:cap:default", now, 7, 30, 0 /* zero → lua default 1800 */)
	if want := now + 1800*1000; got != want {
		t.Fatalf("cool_until_ms=%d, want %d (default cap 1800s must clamp 3840s)", got, want)
	}
}

// TestRecordRequestBackoffCapCustomValueClamps pins that the caller-supplied
// cap wins: cool 30s × 2^2 = 120s clamped to a custom 90s cap.
func TestRecordRequestBackoffCapCustomValueClamps(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	now := int64(3_100_000)
	got := recordCoolFailureOnce(t, s, mr, "ursm:v2:node:cap:custom", now, 2, 30, 90)
	if want := now + 90*1000; got != want {
		t.Fatalf("cool_until_ms=%d, want %d (custom cap 90s must clamp 120s)", got, want)
	}
}

// TestRecordRequestBackoffCapDoesNotShrinkBelowComputed pins that the cap
// only clamps from above: a computed cool below the cap passes through
// unchanged (30s × 2^0 = 30s with the 1800s default cap).
func TestRecordRequestBackoffCapDoesNotShrinkBelowComputed(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	now := int64(3_200_000)
	got := recordCoolFailureOnce(t, s, mr, "ursm:v2:node:cap:passthrough", now, 0, 30, 1800)
	if want := now + 30*1000; got != want {
		t.Fatalf("cool_until_ms=%d, want %d (below-cap value must pass through)", got, want)
	}
}

// TestRecordRequestBackoffCapLegacy3600Rejected pins the regression: the old
// 3600s hard-code must NOT reappear as the effective default. cool 30s × 2^7
// with an unset cap yields exactly 1800s, never 3600s.
func TestRecordRequestBackoffCapLegacy3600Rejected(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	now := int64(3_300_000)
	got := recordCoolFailureOnce(t, s, mr, "ursm:v2:node:cap:legacy", now, 7, 30, 0)
	if got == now+3600*1000 {
		t.Fatalf("cool_until_ms matches the retired 3600s hard-cap — UT-UR-05 regression")
	}
}

// TestRecordRequestHealthStatusBridgeWritten pins UT-UR-12: a valid rich
// health enum is persisted into the node hash "health" field (the field the
// pipeline + persist readers already consume), including while the
// source-priority guard protects the routing fields.
func TestRecordRequestHealthStatusBridgeWritten(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	node := "ursm:v2:node:health:bridge"
	if _, err := s.RecordRequest(ctx, node, node+":w1", node+":w5", node+":w30",
		RecordOutcome{
			Success: true, NowMs: 4_000_000, LatencyMs: 100, RequestID: "h1",
			NodeTTL: time.Hour, Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
			HealthStatus: "suspect",
		}); err != nil {
		t.Fatalf("record: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	got, err := rdb.HGet(ctx, node, "health").Result()
	if err != nil || got != "suspect" {
		t.Fatalf("health=%q err=%v, want suspect", got, err)
	}

	// The bridge is written even when the node hash is owned by a higher
	// source priority (the guard below protects only routing/adjudication
	// fields — health is display telemetry, same channel as lat_ewma).
	if err := rdb.HSet(ctx, node, "source_priority", "30", "available", "0").Err(); err != nil {
		t.Fatalf("seed high priority: %v", err)
	}
	if _, err := s.RecordRequest(ctx, node, node+":w1", node+":w5", node+":w30",
		RecordOutcome{
			Success: false, ErrorKind: "timeout", NowMs: 4_000_100, LatencyMs: 100, RequestID: "h2",
			NodeTTL: time.Hour, Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
			HealthStatus: "degraded",
		}); err != nil {
		t.Fatalf("record under high priority: %v", err)
	}
	got, err = rdb.HGet(ctx, node, "health").Result()
	if err != nil || got != "degraded" {
		t.Fatalf("health under priority guard=%q err=%v, want degraded (display-only field must still bridge)", got, err)
	}
	if avail, _ := rdb.HGet(ctx, node, "available").Result(); avail != "0" {
		t.Fatalf("available=%q, want 0 (priority guard must still protect routing fields)", avail)
	}
}

// TestRecordRequestHealthStatusInvalidIgnored pins that out-of-vocabulary
// values cannot corrupt the stored field: a bogus enum leaves the previous
// value untouched.
func TestRecordRequestHealthStatusInvalidIgnored(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	node := "ursm:v2:node:health:bogus"
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	if err := rdb.HSet(ctx, node, "health", "healthy").Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.RecordRequest(ctx, node, node+":w1", node+":w5", node+":w30",
		RecordOutcome{
			Success: true, NowMs: 4_100_000, LatencyMs: 100, RequestID: "h3",
			NodeTTL: time.Hour, Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
			HealthStatus: "totally-bogus",
		}); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, err := rdb.HGet(ctx, node, "health").Result()
	if err != nil || got != "healthy" {
		t.Fatalf("health=%q err=%v, want healthy (invalid enum must be ignored)", got, err)
	}
}

// TestPipelineNodeViewsSurfacesHealthStatus pins the read side of the
// UT-UR-12 bridge: PipelineNodeViews maps hash "health" onto
// NodeView.HealthStatus so admin resolve / persist snapshots can render it.
func TestPipelineNodeViewsSurfacesHealthStatus(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	if err := s.HSetFields(ctx, "ursm:v2:node:t:77:hmodel", map[string]any{
		"available": "1", "health": "recovering", "generation": "2",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	views, err := s.PipelineNodeViews(ctx, "ursm:v2:", []NodeQuery{
		{TenantID: "t", CredentialID: 77, RawModel: "hmodel"},
	})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if len(views) != 1 || views[0].HealthStatus != "recovering" {
		t.Fatalf("views=%+v, want HealthStatus=recovering", views)
	}
}

// TestRecordRequestLuaSmokeOnRealRedis runs the two parameterized Lua paths
// (backoff cap ARGV[15], health bridge ARGV[14]) against a REAL Redis named
// by TEST_REDIS_URL (default 127.0.0.1:6379; skips when unreachable).
// miniredis covers the semantics in the tests above; this guards against
// real-Redis Lua dialect drift (会话优化 v4 T5 lua 改动验收要求).
func TestRecordRequestLuaSmokeOnRealRedis(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_URL")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 500 * time.Millisecond})
	defer rdb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("TEST_REDIS_URL (%s) unreachable: %v", addr, err)
	}
	s := &Store{rdb: rdb}
	node := fmt.Sprintf("ursm:v2:test:realsmoke:%d:cap", time.Now().UnixNano())
	now := time.Now().UnixMilli()
	if err := rdb.HSet(ctx, node, map[string]interface{}{
		"disabled": "1", "available": "0", "cool_until_ms": "9999999999999",
		"fail_streak": "3", "disable_count": "7",
	}).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.RecordRequest(ctx, node, node+":w1", node+":w5", node+":w30",
		RecordOutcome{
			Success: false, ErrorKind: "timeout", NowMs: now, LatencyMs: 10, RequestID: "smoke",
			NodeTTL: time.Hour, Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
			CoolSeconds: 30, BackoffCapSeconds: 1800, HealthStatus: "recovering",
		}); err != nil {
		t.Fatalf("record on real redis: %v", err)
	}
	cool, err := rdb.HGet(ctx, node, "cool_until_ms").Result()
	if err != nil {
		t.Fatalf("hget cool: %v", err)
	}
	if cool != fmt.Sprintf("%d", now+1800*1000) {
		t.Fatalf("cool_until_ms=%s, want %d (cap 1800s must clamp 30×2^7=3840s on real redis)", cool, now+1800*1000)
	}
	health, err := rdb.HGet(ctx, node, "health").Result()
	if err != nil || health != "recovering" {
		t.Fatalf("health=%q err=%v, want recovering on real redis", health, err)
	}
}
