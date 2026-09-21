package session

import (
	"context"
	"testing"
	"time"
)

// TestEnsureV2WithID_CreatesAndIsIdempotent: first call registers the
// honored id, repeat calls return the existing record untouched.
func TestEnsureV2WithID_CreatesAndIsIdempotent(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	sess, created, err := mgr.EnsureV2WithID(ctx, "gw_fixed_1", 42, "tenant-a", "seed-1", "task-1")
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if !created {
		t.Fatal("first ensure must create")
	}
	if sess.SessionID != "gw_fixed_1" || sess.APIKeyID != 42 || sess.TenantID != "tenant-a" || sess.TaskID != "task-1" {
		t.Fatalf("unexpected session payload: %+v", sess)
	}
	if sess.ExpiresAt.Before(time.Now()) {
		t.Fatal("expires_at must be armed with the manager TTL")
	}

	got, created, err := mgr.EnsureV2WithID(ctx, "gw_fixed_1", 42, "tenant-a", "seed-1", "task-1")
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if created {
		t.Fatal("second ensure must be idempotent (created=false)")
	}
	if got.SessionID != "gw_fixed_1" || got.APIKeyID != 42 {
		t.Fatalf("idempotent ensure returned wrong record: %+v", got)
	}
}

// TestEnsureV2WithID_FirstWriterWins: the session:<id> hash lives in a
// global namespace, so when two api keys concurrently first-use the same
// legacy id the FIRST registration must own it and the loser must observe
// the owner's record — not silently overwrite it (the overwrite turned the
// first key's follow-ups into permanent 403s).
func TestEnsureV2WithID_FirstWriterWins(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	if _, created, err := mgr.EnsureV2WithID(ctx, "gw_contested", 1, "tenant-a", "", ""); err != nil || !created {
		t.Fatalf("first writer must create: created=%v err=%v", created, err)
	}

	got, created, err := mgr.EnsureV2WithID(ctx, "gw_contested", 2, "tenant-b", "", "")
	if err != nil {
		t.Fatalf("second writer: %v", err)
	}
	if created {
		t.Fatal("second writer must not create over an existing id")
	}
	if got.APIKeyID != 1 {
		t.Fatalf("second writer saw APIKeyID=%d, want 1 (owner must be preserved)", got.APIKeyID)
	}
}

// TestEnsureV2WithID_Guards: nil redis client / empty id / bad api key must
// error out instead of panicking in the caller's goroutine.
func TestEnsureV2WithID_Guards(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	if _, _, err := mgr.EnsureV2WithID(ctx, "", 42, "t", "", ""); err == nil {
		t.Fatal("empty session id must be rejected")
	}
	if _, _, err := mgr.EnsureV2WithID(ctx, "gw_x", 0, "t", "", ""); err == nil {
		t.Fatal("apiKeyID<=0 must be rejected")
	}
	var nilMgr *Manager
	if _, _, err := nilMgr.EnsureV2WithID(ctx, "gw_x", 42, "t", "", ""); err == nil {
		t.Fatal("nil manager must be rejected")
	}
}
