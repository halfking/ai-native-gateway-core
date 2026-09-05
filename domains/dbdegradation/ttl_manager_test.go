package dbdegradation

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/kaixuan/llm-gateway-go/domains/session"
)

// TTLManager must NOT touch URSM keys during DB degraded-mode recovery.
// Reasons: (a) URSM v2 authoritative router relies on TTL semantics to
// detect stale probe evidence; (b) raising TTL masks protocol bugs (see
// 2026-08-18 glm-5.2 lockout). Verified here by running EnterDegradedMode
// and asserting ursm:v2:node:* / ursm:v2:sticky:* keys retain their
// pre-degradation TTL.
func TestTTLManager_DoesNotTouchURSMKeys(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := session.NewRedisClient(mr.Addr(), "", 0)
	tm := NewTTLManager(rc, time.Hour, 30*24*time.Hour)

	mr.Set("ursm:v2:node:1:m", "available")
	mr.SetTTL("ursm:v2:node:1:m", 10*time.Second)
	originalURSM := mr.TTL("ursm:v2:node:1:m")

	mr.Set("session:foo", "namespace=gw")
	mr.SetTTL("session:foo", 30*time.Second)
	mr.Set("session_pref:foo", "credential=21")
	mr.SetTTL("session_pref:foo", 30*time.Second)
	originalSession := mr.TTL("session:foo")

	if err := tm.EnterDegradedMode(t.Context()); err != nil {
		t.Fatalf("EnterDegradedMode: %v", err)
	}

	if got := mr.TTL("ursm:v2:node:1:m"); got != originalURSM {
		t.Fatalf("ursm key TTL changed: %s -> %s", originalURSM, got)
	}
	if got := mr.TTL("session:foo"); got <= originalSession {
		t.Fatalf("session key TTL not extended: %s -> %s", originalSession, got)
	}
	if got := mr.TTL("session_pref:foo"); got <= originalSession {
		t.Fatalf("session preference TTL not extended: %s -> %s", originalSession, got)
	}
	if err := tm.ExitDegradedMode(t.Context()); err != nil {
		t.Fatalf("ExitDegradedMode: %v", err)
	}
	if got := mr.TTL("session:foo"); got > time.Hour+time.Minute {
		t.Fatalf("session TTL not shrunk on exit: %s", got)
	}
	if got := mr.TTL("session_pref:foo"); got > time.Hour+time.Minute {
		t.Fatalf("session preference TTL not shrunk on exit: %s", got)
	}
	if got := mr.TTL("ursm:v2:node:1:m"); got != originalURSM {
		t.Fatalf("ursm TTL touched on exit: %s -> %s", originalURSM, got)
	}
}

// shrinkSessionTTLs must skip keys that disappear between SCAN and TTL
// (the SCAN cursor can return keys another writer deleted before our
// pipeline runs); treating them as "no TTL" / "ttl=0" would write Expire
// against an empty slot, creating a phantom session key. Verified here by
// deleting the key after SCAN, then asserting shrinkSessionTTLs does not
// recreate it.
func TestTTLManager_ShrinkSkipsMissingKeys(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := session.NewRedisClient(mr.Addr(), "", 0)
	tm := NewTTLManager(rc, time.Hour, 30*24*time.Hour)

	mr.Set("session:phantom", "namespace=gw")
	mr.SetTTL("session:phantom", 30*24*time.Hour)

	if err := tm.EnterDegradedMode(t.Context()); err != nil {
		t.Fatalf("EnterDegradedMode: %v", err)
	}
	mr.Del("session:phantom")
	if err := tm.ExitDegradedMode(t.Context()); err != nil {
		t.Fatalf("ExitDegradedMode: %v", err)
	}
	if mr.Exists("session:phantom") {
		t.Fatal("shrinkSessionTTLs recreated a deleted key")
	}
}
