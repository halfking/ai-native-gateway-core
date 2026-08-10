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