package kv

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestKey_TailTurnsZeroClampedToOne: passing TailTurns=0 (zero value) MUST be
// silently clamped to 1, NOT treated as "exclude all messages". This is a
// safety guard against accidentally producing an empty key for everything.
func TestKey_TailTurnsZeroClampedToOne(t *testing.T) {
	in := makeBody(t, [][2]string{
		{"system", "s"},
		{"user", "q1"},
		{"assistant", "a1"},
		{"user", "tail"},
	}, nil)
	res, err := Key(in, KeyOptions{TailTurns: 0})
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	// Should hash the system + q1 + a1 (NOT empty key).
	if res.Key == "" {
		t.Error("TailTurns=0 must be clamped to 1 (not produce empty key)")
	}
	if !res.Truncated {
		t.Error("should mark Truncated=true (last turn excluded)")
	}
}

// TestKey_MoreTailTurnsThanMessages: when there are FEWER messages than
// tailTurns, the cutoff clamps to 0 — produce empty key but still set
// Truncated=true.
func TestKey_MoreTailTurnsThanMessages(t *testing.T) {
	in := makeBody(t, [][2]string{{"user", "only"}}, nil)
	res, _ := Key(in, KeyOptions{TailTurns: 5})
	if res.Key != "" {
		t.Errorf("expected empty key when all msgs excluded, got %q", res.Key)
	}
	if !res.Truncated {
		t.Error("expected Truncated=true when tailTurns > msg count")
	}
}

// TestKey_MalformedToolsIgnored: a body where tools is NOT an array (e.g.
// a string) MUST NOT crash — toolsList returns nil, hash continues over
// messages only.
func TestKey_MalformedToolsIgnored(t *testing.T) {
	in := makeBody(t, [][2]string{{"system", "s"}, {"user", "q1"}, {"user", "tail"}}, map[string]any{
		"tools": "not-an-array",
	})
	res, err := Key(in, DefaultKeyOptions())
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if res.Key == "" {
		t.Error("expected key from messages even with malformed tools")
	}
}

// TestStore_TTLZeroUsesDefault: passing ttl=0 MUST be replaced with
// DefaultTTL — never expires immediately.
func TestStore_TTLZeroUsesDefault(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	// DefaultTTL is 5min — well above the 50ms we sleep.
	if err := s.Store(ctx, "k", []byte("v"), 0); err != nil {
		t.Fatalf("Store: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	_, ok, _ := s.Lookup(ctx, "k")
	if !ok {
		t.Error("ttl=0 must be treated as DefaultTTL (5min), not immediate expiry")
	}
}

// TestStore_TTLNegativeUsesDefault: same as TTLZero — defensive against
// callers passing -1 or other negatives.
func TestStore_TTLNegativeUsesDefault(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	if err := s.Store(ctx, "k", []byte("v"), -1*time.Second); err != nil {
		t.Fatalf("Store: %v", err)
	}
	_, ok, _ := s.Lookup(ctx, "k")
	if !ok {
		t.Error("negative ttl must be treated as DefaultTTL")
	}
}

// TestKey_NilBodyNeverPanics: pass nil explicitly — must NOT panic.
func TestKey_NilBodyNeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Key(nil) panicked: %v", r)
		}
	}()
	res, err := Key(nil, DefaultKeyOptions())
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if res.Key != "" {
		t.Errorf("nil body must give empty key, got %q", res.Key)
	}
}

// TestKey_TooLargeBodyHandled: passing a 5MB body should still work without
// OOM. (Sanity check that we don't materialize anything large.)
func TestKey_TooLargeBodyHandled(t *testing.T) {
	big := make([]byte, 5*1024*1024)
	for i := range big {
		big[i] = 'A'
	}
	res, err := Key(big, DefaultKeyOptions())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// Non-JSON 5MB falls back to hashRaw — should still produce a key.
	if res.Key == "" {
		t.Error("large non-JSON body should still produce a key (hashRaw fallback)")
	}
}

// TestKey_HexEncodingSafeForKeys: keys are hex (0-9a-f), so they're safe as
// Redis keys, URL path components, file names, DB identifiers.
func TestKey_HexEncodingSafeForKeys(t *testing.T) {
	in := makeBody(t, [][2]string{{"system", "s"}, {"user", "q1"}}, nil)
	res, _ := Key(in, DefaultKeyOptions())
	for _, c := range res.Key {
		if c < '0' || c > '9' && (c < 'a' || c > 'f') {
			t.Errorf("key char %q is not hex (key = %q)", c, res.Key)
		}
	}
	if strings.ContainsAny(res.Key, "/:?&") {
		t.Errorf("key contains URL-unsafe chars: %q", res.Key)
	}
}
