package admin

import (
	"testing"

	"github.com/redis/go-redis/v9"
)

// TestProbeRedisStore_NotifyEnvelopeHasInstanceID pins the 2026-08-12 audit
// fix that makes the Redis pub/sub notify payload carry an instance_id tag so
// the publishing hub's own subscriber can drop its own echo and stop
// double-fanning-out to connected SSE clients. Without this tag, every
// transition reached the dashboard twice.
func TestProbeRedisStore_NotifyEnvelopeHasInstanceID(t *testing.T) {
	// Probe the helper directly — going through Record would require a live
	// Redis. The payload shape is what consumers depend on.
	got := buildProbeNotify("node_probe:42:gpt-5.6", "node_probe", "ok", "p-12345678")
	want := `{"task_id":"node_probe:42:gpt-5.6","source":"node_probe","status":"ok","instance_id":"p-12345678"}`
	if got != want {
		t.Errorf("notify payload mismatch:\n got=%s\nwant=%s", got, want)
	}

	// Also pin the nil-safe path so a misconfigured store (no rdb) doesn't
	// surface Record errors to the probe worker.
	if err := (&ProbeRedisStore{}).RecordWithOrigin(nil, ProbeStreamTask{}, "p-x"); err != nil {
		t.Fatalf("nil rdb must not error, got %v", err)
	}
	// Calling Record with a redis client whose pool is uninitialised must
	// still return nil (best-effort).
	if err := (&ProbeRedisStore{rdb: redis.NewClient(&redis.Options{Addr: "127.0.0.1:65535"})}).RecordWithOrigin(
		nil, ProbeStreamTask{ID: "", Source: "node_probe", Status: "ok"}, "p-x"); err != nil {
		t.Fatalf("empty ID must be a no-op, got %v", err)
	}
}

// TestProbeRedisStore_Record_ClearsPrevStatusLane pins the cross-status
// eviction contract: when the same taskID transitions from pending to
// in-flight to ok, the status ZSET must only ever contain it in the current
// lane. Without the eviction, initial_data replay showed one tile per stage.
//
// The eviction itself is exercised by the run() loop in production. Here we
// pin the invariant "different statuses → different ZSETs" so the ZREM
// lookup can ever find a previous lane to evict from.
func TestProbeRedisStore_Record_ClearsPrevStatusLane(t *testing.T) {
	for _, s1 := range []string{"pending", "in-flight", "ok", "fail"} {
		for _, s2 := range []string{"pending", "in-flight", "ok", "fail"} {
			if s1 != s2 && probeStatusKey(s1) == probeStatusKey(s2) {
				t.Fatalf("status key collision: %q vs %q", s1, s2)
			}
		}
	}
}

// TestRecordTransitionScript_Loaded pins that the atomic lane-transition Lua
// script is registered at package init (2026-08-12 second-pass audit). If the
// script is accidentally removed, RecordWithOrigin silently falls back to a
// no-op and the lane state diverges — this test makes that visible.
func TestRecordTransitionScript_Loaded(t *testing.T) {
	if recordTransitionScript == nil {
		t.Fatalf("recordTransitionScript must be registered for RecordWithOrigin to be atomic")
	}
	if recordTransitionScript.Hash() == "" {
		t.Fatalf("recordTransitionScript hash must be non-empty")
	}
	// The script body must contain the key invariants: cross-status ZREM,
	// HSET task_status, and the trim. We assert substrings rather than the
	// full body so the test survives whitespace tweaks.
	for _, want := range []string{
		"prevStatus",      // reads the previous lane before evicting
		"ZREM",            // evicts from the previous lane
		"taskStatusHash",  // records the new current lane
		"ZREMRANGEBYRANK", // trims the lane to the keep limit
	} {
		if !stringContains(recordTransitionSrc, want) {
			t.Errorf("recordTransitionSrc missing %q — atomic lane transition would be broken", want)
		}
	}
}

// stringContains is a local helper to avoid clashing with probeContains.
func stringContains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
