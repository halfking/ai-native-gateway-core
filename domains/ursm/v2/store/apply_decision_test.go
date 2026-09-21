package store

import (
	"context"
	"testing"
)

func TestApplyDecisionStaleIgnored(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.HSetFields(ctx, "ursm:v2:node:9:m", map[string]any{
		"generation": 5, "source_priority": 40, "available": 0, "fail_streak": 9, "last_err": "x",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := s.ApplyDecision(ctx, "ursm:v2:node:9:m", 4, 40, true, 0, "retry", false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Status != "ignored_stale" {
		t.Fatalf("status=%s", res.Status)
	}
}

// TestApplyDecisionManualHoldLiveRead covers the TOCTOU fix (2026-08-29):
// apply_decision.lua now reads manual_hold INSIDE the script. The Go
// caller may pre-read manual_hold as "0" and pass adminHold=false, but if
// ApplyAdmin flips the flag in Redis between the pre-read and the Lua
// EVAL, the live-read inside the script MUST still short-circuit. The
// test seeds the key with manual_hold=1 (which the Go caller cannot
// observe in advance) and asserts the result is ignored_manual_hold
// regardless of the adminHold argument.
func TestApplyDecisionManualHoldLiveRead(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.HSetFields(ctx, "ursm:v2:node:9:m", map[string]any{
		"manual_hold":   "1",
		"generation":    1,
		"source_priority": 10,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Go caller passes adminHold=false (stale pre-read); Lua's live-read
	// must still refuse to apply.
	res, err := s.ApplyDecision(ctx, "ursm:v2:node:9:m", 2, 20, true, 0, "retry", false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Status != "ignored_manual_hold" {
		t.Fatalf("expected ignored_manual_hold (live-read override), got %s", res.Status)
	}
	// Sanity: the key must not have been mutated.
	got, err := s.RawClient().HGet(ctx, "ursm:v2:node:9:m", "generation").Result()
	if err != nil {
		t.Fatalf("hget: %v", err)
	}
	if got != "1" {
		t.Fatalf("expected generation=1 (untouched), got %q", got)
	}
}
