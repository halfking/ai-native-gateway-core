package recovery

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newMini(t *testing.T) *redis.Client {
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func TestReadyGateStartsClosed(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	if m.Ready(context.Background()) {
		t.Fatalf("manager must start with ready=0")
	}
}

func TestReadyGateOpens(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	if err := m.SetReady(context.Background(), true); err != nil {
		t.Fatalf("set ready: %v", err)
	}
	if !m.Ready(context.Background()) {
		t.Fatalf("manager must be ready after SetReady(true)")
	}
}

func TestEnterRecoveryClosesGate(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	_ = m.SetReady(context.Background(), true)
	_ = m.EnterRecovery(context.Background(), "manual")
	if m.Ready(context.Background()) {
		t.Fatalf("EnterRecovery must close gate")
	}
}

func TestEnterRecoveryWritesEpochMetadata(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	if err := m.EnterRecovery(context.Background(), "manual-restart"); err != nil {
		t.Fatalf("enter: %v", err)
	}
	rdb := m.rdb
	epoch, err := rdb.HGetAll(context.Background(), "ursm:v2:meta:epoch").Result()
	if err != nil {
		t.Fatalf("hgetall: %v", err)
	}
	if epoch["reason"] != "manual-restart" {
		t.Fatalf("reason missing: %v", epoch)
	}
	if epoch["counter"] != "1" {
		t.Fatalf("counter not incremented: %v", epoch)
	}
	if _, ok := epoch["started_at"]; !ok {
		t.Fatalf("started_at missing: %v", epoch)
	}
}

// TestMarkClosedDebounced_ClusterCoordinator verifies the cluster-wide
// debounce contract (docs/architecture/2026-07-28-routing-state-anomaly-audit.md
// §4.1 follow-up #1): only the FIRST caller within the debounce window
// records an epoch bump; subsequent callers see the existing debounce
// key and return (false, nil) without writing. This caps epoch counter
// inflation to one bump per debounce window regardless of how many
// gateway instances observe the same failure simultaneously.
func TestMarkClosedDebounced_ClusterCoordinator(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")

	// First caller wins: writes the epoch and returns (true, nil).
	won, err := m.MarkClosedDebounced(context.Background(), "redis_unavailable", 5*time.Minute)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !won {
		t.Fatalf("first caller must win debounce, got won=false")
	}
	epoch, err := m.rdb.HGetAll(context.Background(), "ursm:v2:meta:epoch").Result()
	if err != nil {
		t.Fatalf("hgetall epoch: %v", err)
	}
	if epoch["reason"] != "redis_unavailable" {
		t.Fatalf("epoch.reason = %q, want redis_unavailable", epoch["reason"])
	}
	if epoch["counter"] != "1" {
		t.Fatalf("epoch.counter = %q after first call, want 1", epoch["counter"])
	}
	if !m.Ready(context.Background()) == false {
		// Just for sanity: ready gate was flipped to false.
		t.Fatalf("Ready must be false after EnterRecovery, got true")
	}

	// Second caller within the same debounce window must be a no-op.
	won2, err := m.MarkClosedDebounced(context.Background(), "redis_unavailable", 5*time.Minute)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if won2 {
		t.Fatalf("second caller must lose debounce, got won=true")
	}
	epoch2, err := m.rdb.HGetAll(context.Background(), "ursm:v2:meta:epoch").Result()
	if err != nil {
		t.Fatalf("hgetall epoch 2: %v", err)
	}
	if epoch2["counter"] != "1" {
		t.Fatalf("epoch.counter must NOT increment on debounced caller, got %q", epoch2["counter"])
	}
}

// TestMarkClosedDebounced_NextWindowBumpsAgain verifies that once the
// debounce key expires, the next failure event is free to record a
// fresh epoch bump (counter goes 1 → 2). We simulate expiry by deleting
// the debounce key manually instead of waiting for TTL — miniredis
// doesn't expose TTL fast-forward without an external time stub.
func TestMarkClosedDebounced_NextWindowBumpsAgain(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	if _, err := m.MarkClosedDebounced(context.Background(), "first", time.Minute); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Simulate window expiry by deleting the debounce key.
	if err := m.rdb.Del(context.Background(), "ursm:v2:meta:recovery_debounce").Err(); err != nil {
		t.Fatalf("del debounce: %v", err)
	}
	won, err := m.MarkClosedDebounced(context.Background(), "second", time.Minute)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !won {
		t.Fatalf("second-window caller must win, got won=false")
	}
	epoch, _ := m.rdb.HGetAll(context.Background(), "ursm:v2:meta:epoch").Result()
	if epoch["counter"] != "2" {
		t.Fatalf("epoch.counter = %q after second window, want 2", epoch["counter"])
	}
	if epoch["reason"] != "second" {
		t.Fatalf("epoch.reason = %q, want second", epoch["reason"])
	}
}
