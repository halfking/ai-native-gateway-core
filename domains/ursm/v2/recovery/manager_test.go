package recovery

import (
	"context"
	"fmt"
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

// TestWarmupFromExistingKeys_ReopensGate verifies the audit
// follow-up #6 contract: after MarkClosedDebounced closes the gate,
// WarmupFromExistingKeys reopens it AND records the recovery event in
// the epoch hash. Existing per-node state (admin holds, fail_streaks,
// source_priority) must be PRESERVED — only the gate + epoch metadata
// change.
func TestWarmupFromExistingKeys_ReopensGate(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	ctx := context.Background()

	// Seed two node hashes with admin hold + non-trivial fail_streak
	// so we can verify they're preserved across the warmup.
	if err := m.rdb.HSet(ctx, "ursm:v2:node:1:gpt-4",
		"available", "1",
		"source_priority", "40",
		"manual_hold", "1",
		"fail_streak", "5",
		"generation", "7",
	).Err(); err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	if err := m.rdb.HSet(ctx, "ursm:v2:node:tenant-a:2:claude",
		"available", "0",
		"source_priority", "20",
		"fail_streak", "2",
		"generation", "11",
	).Err(); err != nil {
		t.Fatalf("seed 2: %v", err)
	}

	// Close the gate via MarkClosedDebounced.
	if _, err := m.MarkClosedDebounced(ctx, "redis_unavailable", 5*time.Minute); err != nil {
		t.Fatalf("close: %v", err)
	}
	if m.Ready(ctx) {
		t.Fatalf("gate must be closed after MarkClosedDebounced")
	}

	// Reopen via WarmupFromExistingKeys.
	n, err := m.WarmupFromExistingKeys(ctx)
	if err != nil {
		t.Fatalf("warmup: %v", err)
	}
	if n != 2 {
		t.Fatalf("warmup observed %d keys, want 2", n)
	}
	if !m.Ready(ctx) {
		t.Fatalf("gate must be open after WarmupFromExistingKeys")
	}

	// Verify per-node state is preserved (admin hold, fail_streak,
	// generation, source_priority — everything except what warmup
	// itself didn't touch).
	hash1, _ := m.rdb.HGetAll(ctx, "ursm:v2:node:1:gpt-4").Result()
	if hash1["manual_hold"] != "1" {
		t.Fatalf("manual_hold lost on warmup: %v", hash1)
	}
	if hash1["fail_streak"] != "5" {
		t.Fatalf("fail_streak lost on warmup: %v", hash1)
	}
	if hash1["generation"] != "7" {
		t.Fatalf("generation lost on warmup: %v", hash1)
	}
	if hash1["source_priority"] != "40" {
		t.Fatalf("source_priority lost on warmup: %v", hash1)
	}

	hash2, _ := m.rdb.HGetAll(ctx, "ursm:v2:node:tenant-a:2:claude").Result()
	if hash2["source_priority"] != "20" {
		t.Fatalf("node 2 source_priority lost: %v", hash2)
	}
	if hash2["fail_streak"] != "2" {
		t.Fatalf("node 2 fail_streak lost: %v", hash2)
	}

	// Verify recovery metadata was written.
	epoch, _ := m.rdb.HGetAll(ctx, "ursm:v2:meta:epoch").Result()
	if epoch["recovered_keys_count"] != "2" {
		t.Fatalf("epoch.recovered_keys_count = %q, want 2", epoch["recovered_keys_count"])
	}
	if epoch["recovered_at"] == "" {
		t.Fatalf("epoch.recovered_at is empty")
	}
	if epoch["recovery_counter"] != "1" {
		t.Fatalf("epoch.recovery_counter = %q, want 1", epoch["recovery_counter"])
	}
}

// TestRestoreIfClosed_AlreadyOpen verifies the no-op branch: when
// the gate is already open, RestoreIfClosed returns (0, nil) and does
// NOT touch the epoch hash (no spurious recovery_counter bumps).
func TestRestoreIfClosed_AlreadyOpen(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	ctx := context.Background()
	if err := m.SetReady(ctx, true); err != nil {
		t.Fatalf("set ready: %v", err)
	}

	n, err := m.RestoreIfClosed(ctx)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if n != 0 {
		t.Fatalf("RestoreIfClosed on open gate must return 0, got %d", n)
	}

	// recovery_counter must NOT have been incremented (gate was open
	// from the start, nothing to restore).
	epoch, _ := m.rdb.HGetAll(ctx, "ursm:v2:meta:epoch").Result()
	if epoch["recovery_counter"] != "" {
		t.Fatalf("recovery_counter = %q, want empty (no recovery happened)", epoch["recovery_counter"])
	}
	if epoch["recovered_at"] != "" {
		t.Fatalf("recovered_at = %q, want empty", epoch["recovered_at"])
	}
}

// TestRestoreIfClosed_WhenClosed verifies that a closed gate is
// re-opened on call, and the observed key count is returned.
func TestRestoreIfClosed_WhenClosed(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	ctx := context.Background()

	// Seed a node, close gate, call RestoreIfClosed.
	if err := m.rdb.HSet(ctx, "ursm:v2:node:1:m", "available", "1").Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := m.SetReady(ctx, false); err != nil {
		t.Fatalf("close: %v", err)
	}

	n, err := m.RestoreIfClosed(ctx)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if n != 1 {
		t.Fatalf("restore observed %d keys, want 1", n)
	}
	if !m.Ready(ctx) {
		t.Fatalf("gate must be open after RestoreIfClosed")
	}
}

// TestWarmupFromExistingKeys_EmptyRedisKeepsGateClosed verifies that an empty
// authoritative namespace is not mistaken for usable runtime state. Opening
// the gate here would reject every candidate because missing nodes default to
// unavailable.
func TestWarmupFromExistingKeys_EmptyRedisKeepsGateClosed(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	ctx := context.Background()
	if err := m.SetReady(ctx, false); err != nil {
		t.Fatalf("close: %v", err)
	}

	n, err := m.WarmupFromExistingKeys(ctx)
	if err == nil {
		t.Fatal("warmup empty must refuse to open the gate")
	}
	if n != 0 {
		t.Fatalf("warmup on empty redis returned %d, want 0", n)
	}
	if m.Ready(ctx) {
		t.Fatal("gate must remain closed without recoverable node state")
	}
}

// TestStats_RecordsErrorAndRecovery is the audit follow-up #4 surface:
// verify that on success/failure the Stats() snapshot reflects the
// last operation accurately.
func TestStats_RecordsErrorAndRecovery(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	ctx := context.Background()

	// Step 1: close the gate successfully (no error expected, no
	// LastError stamped).
	if _, err := m.MarkClosedDebounced(ctx, "test", time.Minute); err != nil {
		t.Fatalf("close: %v", err)
	}
	stats := m.Stats()
	if stats.LastError != "" {
		t.Fatalf("LastError must be empty on success, got %q", stats.LastError)
	}
	if stats.LastRecoveryAt.IsZero() == false {
		t.Fatalf("LastRecoveryAt must be zero (no recovery yet), got %v",
			stats.LastRecoveryAt)
	}

	// Step 2: re-warm (records LastRecoveryAt).
	if err := m.rdb.HSet(ctx, "ursm:v2:node:1:m", "available", "1").Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := m.SetReady(ctx, false); err != nil {
		t.Fatalf("close: %v", err)
	}
	before := time.Now().UTC()
	n, err := m.WarmupFromExistingKeys(ctx)
	if err != nil {
		t.Fatalf("warmup: %v", err)
	}
	if n != 1 {
		t.Fatalf("warmup observed %d, want 1", n)
	}
	stats = m.Stats()
	if stats.LastRecoveryAt.IsZero() {
		t.Fatalf("LastRecoveryAt must be set after warmup")
	}
	if stats.LastRecoveryAt.Before(before) {
		t.Fatalf("LastRecoveryAt = %v, want >= %v", stats.LastRecoveryAt, before)
	}
	if m.LastRecoveryKeyCount() != 1 {
		t.Fatalf("LastRecoveryKeyCount = %d, want 1", m.LastRecoveryKeyCount())
	}
}

// TestStats_RecordsErrorOnFailure verifies the error path stamps
// LastError + LastErrorAt so an admin dashboard can show "last error
// at <time>: <msg>".
func TestStats_RecordsErrorOnFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	m := New(rdb, "ursm:v2:")

	// Close miniredis so the SetNX call fails.
	mr.Close()
	before := time.Now().UTC()
	_, err := m.MarkClosedDebounced(context.Background(), "test", time.Minute)
	if err == nil {
		t.Fatalf("MarkClosedDebounced must fail when redis is down")
	}

	stats := m.Stats()
	if stats.LastError == "" {
		t.Fatalf("LastError must be set after a failure, got empty")
	}
	if stats.LastErrorAt.Before(before) {
		t.Fatalf("LastErrorAt = %v, want >= %v", stats.LastErrorAt, before)
	}
}

// TestStats_NilReceiverIsSafe verifies the safe-on-nil-receiver
// contract: callers (admin handlers, Prometheus exporters) must not
// have to nil-check the manager before reading stats.
func TestStats_NilReceiverIsSafe(t *testing.T) {
	var m *Manager
	stats := m.Stats()
	if stats.LastError != "" || !stats.LastErrorAt.IsZero() || !stats.LastRecoveryAt.IsZero() {
		t.Fatalf("nil receiver must return zero Stats, got %+v", stats)
	}
	if n := m.LastRecoveryKeyCount(); n != 0 {
		t.Fatalf("nil receiver LastRecoveryKeyCount = %d, want 0", n)
	}
}

// TestStats_ClearErrorOnSuccess verifies that a successful operation
// AFTER a failure resets LastError so a one-shot transient failure
// doesn't keep showing up on the dashboard indefinitely.
func TestStats_ClearErrorOnSuccess(t *testing.T) {
	// We can't easily simulate "redis comes back" with miniredis (the
	// closed mr.Addr panics), so we exercise the success-then-clear path
	// directly: record an error via recordError (private), then call
	// clearError through MarkClosedDebounced, then verify.
	mr1 := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr1.Addr()})
	m := New(rdb, "ursm:v2:")

	// Plant an error so we can verify the clear-on-success path.
	m.recordError(fmt.Errorf("synthetic prior failure"))
	if m.Stats().LastError == "" {
		t.Fatalf("setup: LastError must be set")
	}

	// Successful MarkClosedDebounced must clear the planted error.
	won, err := m.MarkClosedDebounced(context.Background(), "test", time.Minute)
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if !won {
		t.Fatalf("first caller must win")
	}
	if got := m.Stats().LastError; got != "" {
		t.Fatalf("LastError must be cleared after success, got %q", got)
	}
}
