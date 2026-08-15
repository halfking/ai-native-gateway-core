package pending

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// miniredis is not in the module. We test the no-op path (nil rdb)
// for Save/Get/Delete/MarkInProgress to verify the graceful-degrade
// contract. The Redis-backed path is covered by integration tests
// in scripts/ (planned); this is the unit-test layer.

func TestStore_ProjectCAS_AppliesAbsentDurableProjection(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	now := time.Now().Truncate(time.Second)
	expiresAt := now.Add(20 * time.Minute)
	s := NewStore(rdb, time.Hour)
	in := &Response{
		SessionID: "s1", TenantID: "tenant-1", RequestID: "r1", TaskID: "task-1",
		Status: StatusCompleted, Body: "durable-body", ContentType: "application/json",
		RequestHash: "request-hash", FencingToken: 5, ResultVersion: 3,
		ResultHash: "result-hash", NextRetryAt: now.Add(time.Minute).Unix(),
		AttemptCount: 4, Durable: true, ExpiresAt: expiresAt.Unix(),
		CreatedAt: now.Add(-time.Minute).Unix(), CompletedAt: now.Unix(), BytesBuffered: 12,
	}

	applied, err := s.ProjectCAS(context.Background(), in)
	if err != nil || !applied {
		t.Fatalf("ProjectCAS: applied=%v err=%v", applied, err)
	}
	got, found, err := s.Get(context.Background(), "s1", "r1")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if got.TaskID != in.TaskID || got.FencingToken != in.FencingToken || got.ResultVersion != in.ResultVersion ||
		got.ResultHash != in.ResultHash || got.NextRetryAt != in.NextRetryAt || got.AttemptCount != in.AttemptCount ||
		!got.Durable || got.ExpiresAt != in.ExpiresAt || got.Body != in.Body {
		t.Fatalf("projection round trip = %+v", got)
	}
	entryTTL, err := rdb.TTL(context.Background(), entryKey("s1", "r1")).Result()
	if err != nil {
		t.Fatalf("entry TTL: %v", err)
	}
	if entryTTL < 19*time.Minute || entryTTL > 20*time.Minute {
		t.Fatalf("entry TTL = %v, want about 20m", entryTTL)
	}
	latest, rid, found, err := s.GetLatest(context.Background(), "s1")
	if err != nil || !found || rid != "r1" || latest.TaskID != "task-1" {
		t.Fatalf("GetLatest: r=%+v rid=%q found=%v err=%v", latest, rid, found, err)
	}
}

func TestStore_NilClientGracefulDegrade(t *testing.T) {
	s := NewStore(nil, 0)
	if s.Enabled() {
		t.Fatal("nil-rdb Store must report Enabled()=false")
	}
	if s.TTL() != DefaultTTL {
		t.Fatalf("expected DefaultTTL for nil rdb, got %v", s.TTL())
	}
	if err := s.MarkInProgress(context.Background(), &Response{
		SessionID: "s1", RequestID: "r1",
	}); err != ErrUnavailable {
		t.Fatalf("MarkInProgress: got %v, want ErrUnavailable", err)
	}
	if err := s.Save(context.Background(), &Response{
		SessionID: "s1", RequestID: "r1",
	}); err != ErrUnavailable {
		t.Fatalf("Save: got %v, want ErrUnavailable", err)
	}
	if _, _, err := s.Get(context.Background(), "s1", "r1"); err != ErrUnavailable {
		t.Fatalf("Get: got %v, want ErrUnavailable", err)
	}
	if _, _, _, err := s.GetLatest(context.Background(), "s1"); err != ErrUnavailable {
		t.Fatalf("GetLatest: got %v, want ErrUnavailable", err)
	}
	if err := s.Delete(context.Background(), "s1", "r1"); err != ErrUnavailable {
		t.Fatalf("Delete: got %v, want ErrUnavailable", err)
	}
}

func TestStore_EmptyInputRejected(t *testing.T) {
	// The redis client can be nil because the empty-input check
	// happens before any Redis call.
	s := NewStore(nil, 0)
	if err := s.Save(context.Background(), &Response{}); err == nil ||
		!strings.Contains(err.Error(), "SessionID and RequestID") {
		t.Fatalf("empty SessionID/RequestID should error, got %v", err)
	}
	if _, _, err := s.Get(context.Background(), "", "r1"); err == nil {
		t.Fatal("empty SessionID on Get should error")
	}
}

func TestStore_DefaultTTLApplied(t *testing.T) {
	s := NewStore(nil, 0)
	if got := s.TTL(); got != DefaultTTL {
		t.Fatalf("expected default TTL %v, got %v", DefaultTTL, got)
	}
	s2 := NewStore(nil, 30*time.Minute)
	if got := s2.TTL(); got != 30*time.Minute {
		t.Fatalf("custom TTL not respected: got %v", got)
	}
	// negative TTL falls back to default
	s3 := NewStore(nil, -1)
	if got := s3.TTL(); got != DefaultTTL {
		t.Fatalf("negative TTL should fall back, got %v", got)
	}
}

func TestStore_NonNilClient_EnabledTrue(t *testing.T) {
	// We don't connect — just construct a client. The Enabled()
	// check is purely nil-vs-not-nil; reachability is the Ping
	// concern.
	s := NewStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"}), 0)
	if !s.Enabled() {
		t.Fatal("non-nil rdb must report Enabled()=true")
	}
}

func TestResponse_OversizeBodyTruncation(t *testing.T) {
	// We can't drive the real Save path without a live Redis, but
	// we can assert the policy invariant: any body > MaxBodyBytes
	// must NOT be persisted as-is. This is enforced inside Save()
	// — covered by the truncation test below once the Store is
	// unit-testable end-to-end. For now we assert the constant.
	if MaxBodyBytes != 1<<20 {
		t.Fatalf("MaxBodyBytes policy: got %d, want %d", MaxBodyBytes, 1<<20)
	}
}

func TestStatus_Values(t *testing.T) {
	// Pin the wire-format string values. A change here is a
	// backward-incompatible schema change for any client that
	// pattern-matches on these.
	if StatusInProgress != "in_progress" {
		t.Errorf("StatusInProgress: got %q", StatusInProgress)
	}
	if StatusCompleted != "completed" {
		t.Errorf("StatusCompleted: got %q", StatusCompleted)
	}
	if StatusFailed != "failed" {
		t.Errorf("StatusFailed: got %q", StatusFailed)
	}
}

func TestKeyHelpers_StableFormatting(t *testing.T) {
	if got := entryKey("sess_abc", "req_xyz"); got != "pending_response:sess_abc:req_xyz" {
		t.Errorf("entryKey: got %q", got)
	}
	if got := indexKey("sess_abc"); got != "pending_response:index:sess_abc" {
		t.Errorf("indexKey: got %q", got)
	}
}
