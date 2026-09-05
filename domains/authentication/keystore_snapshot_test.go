package authentication

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedSnapshotStore builds a verifier with a populated store (no DB) and
// returns it plus the raw key its entry authorizes.
func seedSnapshotStore(t *testing.T) (*KeyVerifier, string, string) {
	t.Helper()
	kv := NewKeyVerifier()
	raw := "sk-snapshot-key"
	hash := hashAPIKey("snap-secret", raw)
	kv.upsertStore(hash, &KeyInfo{ID: 77, TenantID: "t", KeyPrefix: "sk-77"})
	return kv, raw, hash
}

// TestKeyStore_SnapshotRoundTripColdBoot pins the cold-start gear: a
// process that booted with a healthy DB persists a snapshot; a later
// process that boots with NO database loads it, Enabled() flips true
// (secret-only mode), and Verify authorizes from the snapshot with zero
// DB IO — closing the "restart while DB down" hole.
func TestKeyStore_SnapshotRoundTripColdBoot(t *testing.T) {
	dir := t.TempDir()

	src, raw, hash := seedSnapshotStore(t)
	if err := src.SaveSnapshot(dir); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// The snapshot must NOT contain the raw key material.
	data, err := os.ReadFile(filepath.Join(dir, snapshotFileName))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if strings.Contains(string(data), raw) {
		t.Fatal("snapshot must never contain raw key material")
	}

	// Cold boot: fresh verifier, NO DB pool, secret-only.
	cold := NewKeyVerifier()
	if cold.Enabled() {
		t.Fatal("fresh verifier without secret must not be enabled")
	}
	cold.SetSecretKey("snap-secret")
	if cold.Enabled() {
		t.Fatal("secret without store or pool must not be enabled")
	}
	n, err := cold.LoadSnapshot(dir)
	if err != nil || n != 1 {
		t.Fatalf("LoadSnapshot = (%d, %v), want (1, nil)", n, err)
	}
	if !cold.Enabled() {
		t.Fatal("snapshot-loaded verifier (secret set, no pool) must be enabled")
	}
	if !cold.keyStoreFromSnapshot.Load() {
		t.Fatal("snapshot load must mark the store snapshot-sourced")
	}

	info, err := cold.Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("Verify from snapshot: %v", err)
	}
	if info.ID != 77 {
		t.Fatalf("info = %+v, want ID 77", info)
	}
	if _, err := cold.LookupKeyMeta(context.Background(), raw); err != nil {
		t.Fatalf("snapshot LookupKeyMeta must not require DB: %v", err)
	}
	if err := cold.CheckBudget(context.Background(), info.ID); err != nil {
		t.Fatalf("snapshot CheckBudget must not panic or fail: %v", err)
	}
	if _, err := cold.VerifyByID(context.Background(), info.ID); err == nil {
		t.Fatal("snapshot VerifyByID must fail closed without DB")
	}
	_ = hash
}

// TestKeyStore_SnapshotRefusesTooOld pins the freshness bound: a snapshot
// older than maxSnapshotAge must not be served.
func TestKeyStore_SnapshotRefusesFutureTimestamp(t *testing.T) {
	dir := t.TempDir()
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	hash := hashAPIKey("snap-secret", "sk-snapshot-key")
	data := []byte(fmt.Sprintf(`{"saved_at":%q,"entries":{%q:{"id":77,"status":"active"}}}`, future, hash))
	if err := os.WriteFile(filepath.Join(dir, snapshotFileName), data, 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	kv := NewKeyVerifier()
	kv.SetSecretKey("snap-secret")
	if n, err := kv.LoadSnapshot(dir); err == nil || n != 0 {
		t.Fatalf("future snapshot = (%d, %v), want rejection", n, err)
	}
}

func TestKeyStore_SnapshotRefusesMalformedEntries(t *testing.T) {
	dir := t.TempDir()
	savedAt := time.Now().UTC().Format(time.RFC3339)
	data := []byte(fmt.Sprintf(`{"saved_at":%q,"entries":{"not-a-hash":{"id":1,"status":"active"}}}`, savedAt))
	if err := os.WriteFile(filepath.Join(dir, snapshotFileName), data, 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	kv := NewKeyVerifier()
	if n, err := kv.LoadSnapshot(dir); err == nil || n != 0 {
		t.Fatalf("malformed snapshot = (%d, %v), want rejection", n, err)
	}
}

func TestKeyStore_SnapshotRefusesTooOld(t *testing.T) {
	kv, _, _ := seedSnapshotStore(t)
	dir := t.TempDir()
	if err := kv.SaveSnapshot(dir); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	// Rewrite the snapshot with a saved_at beyond the freshness bound.
	old := `{"saved_at":"2020-01-01T00:00:00Z","entries":{"h":{"id":1}}}`
	if err := os.WriteFile(filepath.Join(dir, snapshotFileName), []byte(old), 0o600); err != nil {
		t.Fatalf("rewrite snapshot: %v", err)
	}
	fresh := NewKeyVerifier()
	if n, err := fresh.LoadSnapshot(dir); err == nil || n != 0 {
		t.Fatalf("LoadSnapshot on stale snapshot = (%d, %v), want (0, error)", n, err)
	}
	if fresh.keyStoreLoaded.Load() {
		t.Fatal("stale snapshot must not mark the store loaded")
	}
}

// TestKeyStore_SnapshotMissingDirIsNotAnError pins the absent-snapshot
// case (first boot): LoadSnapshot errors with fs not-exist and the caller
// treats it as "no snapshot available", never fatal.
func TestKeyStore_SnapshotMissingDirIsNotAnError(t *testing.T) {
	fresh := NewKeyVerifier()
	if n, err := fresh.LoadSnapshot(t.TempDir()); err == nil || n != 0 {
		t.Fatalf("missing snapshot = (%d, %v), want (0, error)", n, err)
	}
	if fresh.keyStoreLoaded.Load() {
		t.Fatal("failed load must not mark the store loaded")
	}
}

// TestKeyStore_SnapshotDisabledByEmptyDir pins that snapshot persistence
// is a no-op without a configured dir.
func TestKeyStore_SnapshotDisabledByEmptyDir(t *testing.T) {
	kv, _, _ := seedSnapshotStore(t)
	kv.SetSnapshotDir("")
	kv.saveSnapshotQuietly() // must be a silent no-op
	if n := kv.tryLoadSnapshot(); n != 0 {
		t.Fatalf("tryLoadSnapshot with empty dir = %d, want 0", n)
	}
}

// TestKeyStore_SnapshotLoopFallsBackWhenDBDownAtBoot pins the sync loop's
// initial-load failure path: a snapshot bridges boot while the loop keeps
// retrying the full load (snapshot-sourced stores prefer full reloads).
func TestKeyStore_SnapshotLoopFallsBackWhenDBDownAtBoot(t *testing.T) {
	dir := t.TempDir()
	src, raw, _ := seedSnapshotStore(t)
	if err := src.SaveSnapshot(dir); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Boot a verifier whose DB is unreachable but configured: the initial
	// full load would fail, so the snapshot bridges the gap. (Here the
	// store hit means no query ever runs against the mock.)
	kv := NewKeyVerifier()
	kv.setDBQuerier(newMockPool(t), "snap-secret")
	kv.SetSnapshotDir(dir)

	n := kv.tryLoadSnapshot()
	if n != 1 {
		t.Fatalf("tryLoadSnapshot = %d, want 1", n)
	}
	if !kv.keyStoreFromSnapshot.Load() {
		t.Fatal("fallback load must mark snapshot-sourced")
	}
	info, err := kv.Verify(context.Background(), raw)
	if err != nil || info.ID != 77 {
		t.Fatalf("Verify during DB-down boot = (%+v, %v)", info, err)
	}
}
