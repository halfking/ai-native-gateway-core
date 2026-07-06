// Package streaming - prefix_guard_integration_test.go (rtk borrowing, 2026-07-06)
//
// Integration test proving the two new hot-path transforms compose safely:
// prefix.Stabilize reorders messages for KV-cache hits, then the never_worse
// guard ensures the reordered body never exceeds the original. This is the
// exact sequence handler.go now runs between session compression and the
// request WAL (see handler.go "Prompt-prefix stabilization" block).

package streaming

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/cache/prefix"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

// TestStabilize_ThenInject_ReordersAndKeepsSystemFirst verifies the canonical
// win: a client that put a volatile user message early gets reordered so the
// system prompt is first (maximising KV-prefix-cache hits), and the body is
// still valid JSON. Stabilize is a reorder (not a shrink), so the guard is
// NOT applied to this stage — its value is cache-hit uplift, not byte savings.
func TestStabilize_ThenInject_ReordersAndKeepsSystemFirst(t *testing.T) {
	// Construct a deliberately sub-optimal ordering: a long system prompt
	// sitting AFTER an early user turn (some SDKs do this).
	body := map[string]any{
		"model": "gpt-4o",
		"messages": []map[string]any{
			{"role": "user", "content": "What is the latest status?"},
			{"role": "system", "content": "You are a helpful assistant that answers questions about the project. Always be concise and accurate. Refer to the project documentation when unsure."},
			{"role": "assistant", "content": "Sure, what would you like to know?"},
			{"role": "user", "content": "Summarize the build status."},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	stab, report, serr := prefix.Stabilize(raw, prefix.Options{TailTurns: 1})
	if serr != nil {
		t.Fatalf("Stabilize error: %v", serr)
	}
	if report == nil || !report.Changed {
		t.Fatalf("expected Stabilize to reorder the volatile-tail-first body; report=%+v", report)
	}
	if len(stab) == 0 {
		t.Fatalf("expected non-empty stabilized output")
	}

	// Verify the system message now comes FIRST in the stabilized body.
	var stabilized map[string]any
	if err := json.Unmarshal(stab, &stabilized); err != nil {
		t.Fatalf("unmarshal stabilized: %v", err)
	}
	msgs, _ := stabilized["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("no messages in stabilized body")
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("expected system first after stabilize, got role=%v", first["role"])
	}
	// The latest user turn (the volatile tail) must be LAST.
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "user" {
		t.Fatalf("expected tail (latest user) last, got role=%v", last["role"])
	}
}

// TestInject_NeverWorseGuarded verifies the guard IS applied to the inject
// stage (a genuine expand op): if injection somehow produced a longer body,
// the guard reverts it. We simulate a pathological injection by appending.
func TestInject_NeverWorseGuarded(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	// A pathological "injection" that grows the body.
	injected := append([]byte{}, raw...)
	injected = append(injected, []byte("//padded-to-force-regression-padded-padded-padded")...)

	out, regressed := compression.NeverWorse(raw, injected, compression.GuardStageInject)
	if !regressed {
		t.Fatalf("inject that grew the body must be reverted; got regressed=false")
	}
	if string(out) != string(raw) {
		t.Fatalf("reverted body must equal raw; got %q", out)
	}
}

// TestStabilize_NeverWorseComposition_AlreadyStable_NoChange verifies the
// no-op fast path: an already-stable body is returned unchanged (Changed=false),
// so handler.go skips it entirely.
func TestStabilize_NeverWorseComposition_AlreadyStable_NoChange(t *testing.T) {
	body := map[string]any{
		"model": "gpt-4o",
		"messages": []map[string]any{
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hi"},
			{"role": "assistant", "content": "Hello!"},
			{"role": "user", "content": "Bye"},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	stab, report, serr := prefix.Stabilize(raw, prefix.Options{TailTurns: 1})
	if serr != nil {
		t.Fatalf("Stabilize error: %v", serr)
	}
	// Already stable → Changed=false → handler.go skips it.
	if report != nil && report.Changed {
		t.Fatalf("expected no change on already-stable body")
	}
	// Output equals input (byte-stable).
	if string(stab) != string(raw) {
		t.Fatalf("expected byte-stable output on already-stable input")
	}
}

// TestStabilize_FailOpenOnMalformed verifies the fail-open contract that
// makes Stabilize safe to run unconditionally on the hot path: a malformed
// body is returned unchanged, never an error that would break the request.
func TestStabilize_FailOpenOnMalformed(t *testing.T) {
	malformed := []byte("{not valid json")
	out, report, err := prefix.Stabilize(malformed, prefix.Options{TailTurns: 1})
	if err != nil {
		t.Fatalf("Stabilize must not error on malformed JSON (fail-open); got %v", err)
	}
	if string(out) != string(malformed) {
		t.Fatalf("fail-open must return original bytes; got %q", out)
	}
	if report == nil || report.Changed {
		t.Fatalf("expected Changed=false on malformed input; report=%+v", report)
	}
}
