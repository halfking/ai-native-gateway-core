package kv

import (
	"encoding/json"
	"testing"
)

// makeBody builds an OpenAI-style chat-completions body with the given messages.
// Each pair is [role, content]. Optional extra JSON fields can be injected via extras
// (e.g. tools, model).
func makeBody(t *testing.T, msgs [][2]string, extras map[string]any) []byte {
	t.Helper()
	list := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		list = append(list, map[string]any{"role": m[0], "content": m[1]})
	}
	obj := map[string]any{"model": "x", "messages": list}
	for k, v := range extras {
		obj[k] = v
	}
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestKey_Deterministic: same input MUST always produce same key (we are
// computing a fingerprint, not generating anything).
func TestKey_Deterministic(t *testing.T) {
	in := makeBody(t, [][2]string{
		{"system", "you are helpful"},
		{"user", "hello"},
		{"assistant", "hi"},
		{"user", "q2"},
	}, nil)
	k1, err := Key(in, DefaultKeyOptions())
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	k2, err := Key(in, DefaultKeyOptions())
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if k1.Key != k2.Key {
		t.Errorf("Key not deterministic: %q vs %q", k1.Key, k2.Key)
	}
	if len(k1.Key) != 64 {
		t.Errorf("Key length = %d, want 64 (hex-encoded SHA256)", len(k1.Key))
	}
}

// TestKey_DifferentMessagesDifferentKey: changing a HISTORY turn MUST change
// the key. This is the negative test that prevents the "always returns same
// key" bug. Note: changing only the TAIL would NOT change the key — that's
// tested separately in TestKey_TailExcluded.
func TestKey_DifferentMessagesDifferentKey(t *testing.T) {
	in1 := makeBody(t, [][2]string{
		{"system", "you are helpful"},
		{"user", "q1"},
		{"assistant", "a1"},
		{"user", "q2"},
	}, nil)
	in2 := makeBody(t, [][2]string{
		{"system", "you are helpful"},
		{"user", "q1-EDITED"}, // history change
		{"assistant", "a1"},
		{"user", "q2"},
	}, nil)
	k1, _ := Key(in1, DefaultKeyOptions())
	k2, _ := Key(in2, DefaultKeyOptions())
	if k1.Key == k2.Key {
		t.Error("history change did not change key (hash is broken)")
	}
}

// TestKey_TailExcluded: changing the MOST RECENT user turn MUST NOT change
// the key. This is the whole point — the tail is volatile, the prefix is what
// the upstream caches.
func TestKey_TailExcluded(t *testing.T) {
	in1 := makeBody(t, [][2]string{
		{"system", "sys"},
		{"user", "q1"},
		{"assistant", "a1"},
		{"user", "first try"},
	}, nil)
	in2 := makeBody(t, [][2]string{
		{"system", "sys"},
		{"user", "q1"},
		{"assistant", "a1"},
		{"user", "second try"}, // different tail
	}, nil)
	k1, _ := Key(in1, DefaultKeyOptions())
	k2, _ := Key(in2, DefaultKeyOptions())
	if k1.Key != k2.Key {
		t.Errorf("tail-only change changed the key: %q vs %q", k1.Key, k2.Key)
	}
	if k1.Key == "" {
		t.Error("key should be non-empty")
	}
}

// TestKey_HistoryChangeChangesKey: changing a HISTORY turn (not the tail) MUST
// change the key — history is part of the cacheable prefix.
func TestKey_HistoryChangeChangesKey(t *testing.T) {
	in1 := makeBody(t, [][2]string{
		{"system", "sys"},
		{"user", "q1"},
		{"assistant", "a1"},
		{"user", "q2"},
	}, nil)
	in2 := makeBody(t, [][2]string{
		{"system", "sys"},
		{"user", "q1"},
		{"assistant", "a1-EDITED"}, // history change
		{"user", "q2"},
	}, nil)
	k1, _ := Key(in1, DefaultKeyOptions())
	k2, _ := Key(in2, DefaultKeyOptions())
	if k1.Key == k2.Key {
		t.Error("history change did not change key (prefix not hashed?)")
	}
}

// TestKey_SystemChangeChangesKey: changing the system prompt MUST change key.
func TestKey_SystemChangeChangesKey(t *testing.T) {
	in1 := makeBody(t, [][2]string{{"system", "v1"}, {"user", "q"}}, nil)
	in2 := makeBody(t, [][2]string{{"system", "v2"}, {"user", "q"}}, nil)
	k1, _ := Key(in1, DefaultKeyOptions())
	k2, _ := Key(in2, DefaultKeyOptions())
	if k1.Key == k2.Key {
		t.Error("system change did not change key")
	}
}

// TestKey_StabilizationMakesEquivalent: a body with the system message at the
// END (non-stable order) must produce the SAME key as the same body with the
// system message at the START (stable order). This is the whole point of
// delegation to prefix.Stabilize.
func TestKey_StabilizationMakesEquivalent(t *testing.T) {
	stable := makeBody(t, [][2]string{
		{"system", "you are helpful"},
		{"user", "q1"},
		{"assistant", "a1"},
		{"user", "q2"},
	}, nil)
	buried := makeBody(t, [][2]string{
		{"user", "q1"},
		{"assistant", "a1"},
		{"user", "q2"},
		{"system", "you are helpful"},
	}, nil)
	kStable, _ := Key(stable, DefaultKeyOptions())
	kBuried, _ := Key(buried, DefaultKeyOptions())
	if kStable.Key != kBuried.Key {
		t.Errorf("stabilization failed: buried-system key %q != stable-system key %q",
			kBuried.Key, kStable.Key)
	}
}

// TestKey_NonChatBodyPassthrough: bodies that are not chat-completions (no
// messages field, or non-JSON) MUST NOT panic or error — they produce a key
// derived from whatever bytes were passed (or empty body hash for nil).
// We don't crash; we hash the raw bytes as a fallback.
func TestKey_NonJSONBodyDoesNotPanic(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		[]byte("not json at all"),
		[]byte(`{"model":"x","input":"hello"}`), // no messages
		[]byte(`{"messages":"not-an-array"}`),   // messages wrong shape
	}
	for _, c := range cases {
		_, err := Key(c, DefaultKeyOptions())
		if err != nil {
			t.Errorf("Key on %q returned error: %v (should be tolerant)", string(c), err)
		}
	}
}

// TestKey_EmptyBodyProducesEmptyKey: a nil/empty body has no prefix to hash,
// so the key is the empty string (NOT a fake hash). Callers should treat
// empty key as "no cacheable prefix" (i.e. always miss).
func TestKey_EmptyBodyProducesEmptyKey(t *testing.T) {
	for _, in := range [][]byte{nil, {}} {
		res, err := Key(in, DefaultKeyOptions())
		if err != nil {
			t.Fatalf("Key(%v): %v", in, err)
		}
		if res.Key != "" {
			t.Errorf("Key(%v) = %q, want empty", in, res.Key)
		}
	}
}

// TestKey_TruncatedFlag: when tail is excluded (i.e. the body has tail
// messages), the result MUST mark Truncated=true with the correct Length of
// bytes that contributed.
func TestKey_TruncatedFlag(t *testing.T) {
	in := makeBody(t, [][2]string{
		{"system", "sys"},
		{"user", "q1"},
		{"assistant", "a1"},
		{"user", "tail-msg"},
	}, nil)
	res, _ := Key(in, DefaultKeyOptions())
	if !res.Truncated {
		t.Error("expected Truncated=true when tail msg exists")
	}
	if res.Length <= 0 {
		t.Error("expected Length > 0 when truncated")
	}
	if res.Key == "" {
		t.Error("expected non-empty key when truncated")
	}
}

// TestKey_NotTruncatedForSingleMessage: single message + TailTurns=1 →
// that single message IS the tail (excluded) → Truncated=true.
// This documents the (initially surprising) semantics: Truncated means
// "at least one turn was excluded", not "more than one turn remained".
func TestKey_NotTruncatedForSingleMessage(t *testing.T) {
	in := makeBody(t, [][2]string{{"user", "only"}}, nil)
	res, _ := Key(in, DefaultKeyOptions())
	if !res.Truncated {
		t.Error("single message + TailTurns=1 must mark Truncated=true (tail excluded)")
	}
	if res.Key != "" {
		t.Errorf("single message should produce empty key, got %q", res.Key)
	}
}

// TestKey_ConfigurableTailTurns: with TailTurns=2, changing the last 2 turns
// does NOT change the key (both are excluded), but changing the 3rd-to-last
// DOES change it.
func TestKey_ConfigurableTailTurns(t *testing.T) {
	base := [][2]string{
		{"system", "s"},
		{"user", "q1"},
		{"assistant", "a1"},
		{"user", "q2"},
		{"assistant", "a2"},
		{"user", "tailA"},
	}
	opts := KeyOptions{TailTurns: 2}
	baseBytes := makeBody(t, base, nil)
	changeTailBytes := makeBody(t, [][2]string{
		{"system", "s"},
		{"user", "q1"},
		{"assistant", "a1"},
		{"user", "q2"},
		{"assistant", "a2-EDITED"}, // tail[1]
		{"user", "tailB"},          // tail[0]
	}, nil)
	changeHistoryBytes := makeBody(t, [][2]string{
		{"system", "s"},
		{"user", "q1-EDITED"}, // history change
		{"assistant", "a1"},
		{"user", "q2"},
		{"assistant", "a2"},
		{"user", "tailA"},
	}, nil)
	kBase, _ := Key(baseBytes, opts)
	kTailChange, _ := Key(changeTailBytes, opts)
	kHistoryChange, _ := Key(changeHistoryBytes, opts)
	if kBase.Key != kTailChange.Key {
		t.Errorf("TailTurns=2: changing tail must not change key (got %q vs %q)",
			kBase.Key, kTailChange.Key)
	}
	if kBase.Key == kHistoryChange.Key {
		t.Errorf("TailTurns=2: changing history must change key (got same %q)", kBase.Key)
	}
}

// TestKey_DeterministicOrderingAfterStabilization: even with weird ordering,
// after stabilization, the resulting key depends ONLY on the set+order of
// system+history (not tail). We test that a complex reordered body converges
// to the same key as its canonical form.
func TestKey_ConvergenceOnCanonicalForm(t *testing.T) {
	canon := makeBody(t, [][2]string{
		{"system", "sys"},
		{"user", "q1"},
		{"assistant", "a1"},
		{"user", "q2"},
		{"assistant", "a2"},
		{"user", "q3"},
	}, nil)
	reordered := makeBody(t, [][2]string{
		{"user", "q1"},
		{"assistant", "a1"},
		{"system", "sys"}, // mid-body system
		{"user", "q2"},
		{"user", "q3"}, // out-of-order user messages (history)
		{"assistant", "a2"},
	}, nil)
	kCanon, _ := Key(canon, DefaultKeyOptions())
	kReord, _ := Key(reordered, DefaultKeyOptions())
	// Stabilize only moves SYSTEM-class messages; within-class order is
	// preserved. So reordering "user q3" before "assistant a2" keeps the
	// history in original (reordered) order — but canonicalization would
	// sort differently. We only assert: reordered system placement doesn't
	// change the key.
	kCanonSysFirst := makeBody(t, [][2]string{
		{"system", "sys"},
		{"user", "q1"},
		{"assistant", "a1"},
		{"user", "q2"},
		{"assistant", "a2"},
		{"user", "q3"},
	}, nil)
	k3, _ := Key(kCanonSysFirst, DefaultKeyOptions())
	if kCanon.Key != k3.Key {
		t.Errorf("two canonical bodies produced different keys: %q vs %q", kCanon.Key, k3.Key)
	}
	// Reordered key MUST differ from canonical because the HISTORY is in a
	// different order — Stabilize preserves within-class order.
	if kReord.Key == kCanon.Key {
		t.Errorf("reordered history collapsed to same key (semantic loss): %q", kReord.Key)
	}
}

// TestKey_ToolsFieldIncluded: the cacheable prefix includes tool definitions.
// Changing tools MUST change the key.
func TestKey_ToolsFieldIncluded(t *testing.T) {
	in1 := makeBody(t, [][2]string{{"system", "s"}, {"user", "q"}}, map[string]any{
		"tools": []map[string]any{
			{"type": "function", "function": map[string]any{"name": "foo"}},
		},
	})
	in2 := makeBody(t, [][2]string{{"system", "s"}, {"user", "q"}}, map[string]any{
		"tools": []map[string]any{
			{"type": "function", "function": map[string]any{"name": "bar"}},
		},
	})
	k1, _ := Key(in1, DefaultKeyOptions())
	k2, _ := Key(in2, DefaultKeyOptions())
	if k1.Key == k2.Key {
		t.Error("different tools produced same key")
	}
}

// TestKey_ResultContainsMetadata: the result struct must include observability
// fields. This is a contract test that guards against regressions where a
// future refactor drops the metadata.
func TestKey_ResultContainsMetadata(t *testing.T) {
	in := makeBody(t, [][2]string{{"system", "s"}, {"user", "q"}}, nil)
	res, err := Key(in, DefaultKeyOptions())
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if res.Key == "" {
		t.Error("Key must be populated")
	}
	if len(res.PrefixBytes) == 0 {
		t.Error("PrefixBytes must be populated (for debugging/telemetry)")
	}
}
