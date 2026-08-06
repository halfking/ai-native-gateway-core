package v2

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestMessageKey_ToolCallIDDisambiguates is the core regression: under the old
// "role:first-100-chars" scheme, two tool results with a shared 100-char prefix
// but different tool_call_ids collapsed to the same key and were wrongly treated
// as duplicates during delta extraction. The ids must now differ.
func TestMessageKey_ToolCallIDDisambiguates(t *testing.T) {
	prefix := strings.Repeat("x", 120) // longer than the old 100-char window
	a := Message{Role: "tool", Content: prefix, ToolCallID: "call_A"}
	b := Message{Role: "tool", Content: prefix, ToolCallID: "call_B"}

	ka, kb := messageKey(a), messageKey(b)
	if ka == kb {
		t.Fatalf("tool results with different tool_call_ids share key %q (disambiguation lost)", ka)
	}
}

// TestMessageKey_StableAndPrefixBounded confirms the 512-byte content window:
// content differences beyond byte 512 do not change the key (intentional, keeps
// it cheap and aligned with V1), but any difference within the first 512 bytes
// does.
func TestMessageKey_StableAndPrefixBounded(t *testing.T) {
	base := Message{Role: "assistant", Content: "hello"}
	if messageKey(base) != messageKey(base) {
		t.Fatal("messageKey is not deterministic for identical input")
	}

	// Difference within the 512-byte window must change the key.
	changed := base
	changed.Content = "hello!"
	if messageKey(base) == messageKey(changed) {
		t.Fatal("content change within 512 bytes did not change key")
	}

	// Difference only past byte 512 must NOT change the key (matches V1
	// contentFingerprint's first-512-bytes contract).
	long1 := Message{Role: "assistant", Content: strings.Repeat("a", 600) + "X"}
	long2 := Message{Role: "assistant", Content: strings.Repeat("a", 600) + "Y"}
	if messageKey(long1) != messageKey(long2) {
		t.Fatal("content difference past byte 512 changed the key; expected V1-aligned 512-byte window")
	}
}

// TestMessageKey_MatchesV1Formula asserts byte-for-byte parity with the V1
// compression fingerprint documented in
// domains/hooks/compression/diff.go msgHash:
//
//	sha256(role + "\x00" + first512Bytes(content) + "\x00" + toolCallID)[:16]
//
// This parity is the reason A2 exists; if either side drifts, V1/LCS and
// V2/delta delta-extraction will disagree on what is "new".
func TestMessageKey_MatchesV1Formula(t *testing.T) {
	msg := Message{
		Role:       "user",
		Content:    "find the bug",
		ToolCallID: "call_42",
	}
	contentKey := msg.Content
	if len(contentKey) > 512 {
		contentKey = contentKey[:512]
	}
	raw := msg.Role + "\x00" + contentKey + "\x00" + msg.ToolCallID
	sum := sha256.Sum256([]byte(raw))
	want := hex.EncodeToString(sum[:16])

	if got := messageKey(msg); got != want {
		t.Fatalf("messageKey = %q, want V1 formula %q", got, want)
	}
}

// TestMessageKey_SeparatorSafe ensures the chosen NUL separator cannot be
// spoofed by content containing the old ":" separator, which previously allowed
// distinct (role, content) pairs to collide.
func TestMessageKey_SeparatorSafe(t *testing.T) {
	// ("user", "a:b") vs ("user:a", "b") — with a plain ":" join these collide.
	m1 := Message{Role: "user", Content: "a:b"}
	m2 := Message{Role: "user:a", Content: "b"}
	if messageKey(m1) == messageKey(m2) {
		t.Fatal("NUL-separated fingerprint collided across field boundary (separator spoofable)")
	}
}
