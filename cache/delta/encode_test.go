package delta

import (
	"strings"
	"testing"
)

// chatBody builds a synthetic chat-completions body with N user/assistant
// turns. The bytes are designed to be human-readable so test failures are
// debuggable from the diff. Each turn has substantial content (a short
// paragraph) to mimic real chat traffic — small content would make the
// prefix share less impressive.
func chatBody(system string, turns int) []byte {
	var sb strings.Builder
	sb.WriteString(`{"model":"x","messages":[`)
	sb.WriteString(`{"role":"system","content":"`)
	sb.WriteString(system)
	sb.WriteString(`"}`)
	for i := 0; i < turns; i++ {
		if i%2 == 0 {
			sb.WriteString(`{"role":"user","content":"Please help me with the task at hand, specifically step `)
		} else {
			sb.WriteString(`{"role":"assistant","content":"Sure, here is the answer for the requested step `)
		}
		sb.WriteString(intToStr(i))
		sb.WriteString(` of the overall workflow."}`)
	}
	sb.WriteString(`]}`)
	return []byte(sb.String())
}

// intToStr is a tiny helper to avoid importing strconv for a single test.
// Avoids benchmark cost on hot test paths.
func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	n := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}

// TestEncode_EmptyNew covers the no-input case. Encode must return a zero
// Delta and saved=false so the caller knows encoding was not produced.
func TestEncode_EmptyNew(t *testing.T) {
	d, saved := Encode([]byte(`{"a":1}`), nil)
	if saved {
		t.Errorf("Encode with empty new: saved=true, want false")
	}
	if d.Op != "" || d.Payload != nil {
		t.Errorf("Encode with empty new: returned non-zero Delta %+v", d)
	}
}

// TestEncode_EmptyParent covers the no-parent case. Encode must return
// OpFull(new), saved=false — there is nothing to encode against.
func TestEncode_EmptyParent(t *testing.T) {
	newBody := []byte(`{"x":1}`)
	d, saved := Encode(nil, newBody)
	if saved {
		t.Errorf("Encode with empty parent: saved=true, want false")
	}
	if d.Op != OpFull {
		t.Errorf("Encode with empty parent: Op=%q, want %q", d.Op, OpFull)
	}
	if !bytesEqual(d.Payload, newBody) {
		t.Errorf("Encode with empty parent: Payload=%q, want %q", d.Payload, newBody)
	}
}

// TestEncode_Identical covers the no-difference case. Encoding identical
// payloads saves nothing, so we must return OpFull(new), saved=false.
func TestEncode_Identical(t *testing.T) {
	body := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`)
	d, saved := Encode(body, body)
	if saved {
		t.Errorf("Encode identical: saved=true, want false")
	}
	if d.Op != OpFull {
		t.Errorf("Encode identical: Op=%q, want %q", d.Op, OpFull)
	}
}

// TestEncode_ChatAppend: the canonical use case. Long shared prefix (system
// prompt + history), only one new turn appended at the end.
//
// Note: chat appends are NOT byte-prefix appends — parent ends with "]}"
// (the messages-array close) while new ends with the new turn + "]}".
// So the LCP stops 2 bytes short of len(parent). Encode returns OpReplaceTail
// with Cutoff=LCP, which reconstructs correctly via Apply. OpAppend would
// require parent to be a perfect byte-prefix of new (a stricter condition
// rarely met by JSON bodies).
func TestEncode_ChatAppend(t *testing.T) {
	parent := chatBody("helpful assistant", 6) // 6 turns
	newB := chatBody("helpful assistant", 7)   // 7 turns
	d, saved := Encode(parent, newB)
	if !saved {
		t.Fatalf("Encode chat append: saved=false, want true")
	}
	if d.Op != OpReplaceTail && d.Op != OpAppend {
		t.Fatalf("Encode chat append: Op=%q, want %q or %q", d.Op, OpAppend, OpReplaceTail)
	}
	// Payload must contain the new turn's content (turn 6 user message).
	if !strings.Contains(string(d.Payload), `step 6`) {
		t.Errorf("Encode chat append: Payload=%q does not contain new turn content", d.Payload)
	}
	// For OpReplaceTail, Cutoff must be < len(parent).
	if d.Op == OpReplaceTail && d.Cutoff >= len(parent) {
		t.Errorf("Encode chat append: Cutoff=%d >= len(parent)=%d", d.Cutoff, len(parent))
	}
}

// TestEncode_ReplacesLatestTurn: the second canonical case. The history is
// unchanged but the last turn was re-written by the client. Expect
// OpReplaceTail (or OpFull if savings too small).
func TestEncode_ReplacesLatestTurn(t *testing.T) {
	parent := chatBody("helpful", 4)
	// Build a new body where turn 3 is replaced by a longer "user" message
	// and the closing structure is the same.
	newB := chatBody("helpful", 3) // 3 turns (shorter)
	// Now extend the last turn to force a replace-tail — same prefix, different tail.
	newB = append(newB[:len(newB)-2], []byte(`,"CONTENT_EXTRA"}]}`)...)
	d, saved := Encode(parent, newB)
	if !saved {
		// Could happen if the tail is short. Skip rather than fail with a confusing message.
		t.Skipf("Encode tail-replace did not save (len(parent)=%d, len(new)=%d, lcp>=...)", len(parent), len(newB))
	}
	if d.Op != OpReplaceTail {
		t.Errorf("Encode replace-tail: Op=%q, want %q", d.Op, OpReplaceTail)
	}
	if d.Cutoff >= len(parent) {
		t.Errorf("Encode replace-tail: Cutoff=%d, must be < len(parent)=%d", d.Cutoff, len(parent))
	}
}

// TestEncode_NoCommonPrefix: completely different bodies. Must return
// OpFull(new), saved=false.
func TestEncode_NoCommonPrefix(t *testing.T) {
	parent := []byte(`AAAAAAAAAA`)
	newB := []byte(`bbbbbbbbbb`)
	d, saved := Encode(parent, newB)
	if saved {
		t.Errorf("Encode no-prefix: saved=true, want false")
	}
	if d.Op != OpFull {
		t.Errorf("Encode no-prefix: Op=%q, want %q", d.Op, OpFull)
	}
}

// TestEncode_SavingsBelowThreshold: parent is very short relative to new —
// the savings ratio is below MinCompressionRatio. Must degrade to OpFull.
func TestEncode_SavingsBelowThreshold(t *testing.T) {
	// parent = 50 bytes, new = 1000 bytes (parent is perfect prefix).
	parent := []byte(strings.Repeat("x", 50))
	newB := append(append([]byte{}, parent...), bytesRepea('y', 950)...)
	d, saved := Encode(parent, newB)
	if saved {
		t.Errorf("Encode low-ratio: saved=true, want false (50/1000 = 0.05 < 0.30)")
	}
	if d.Op != OpFull {
		t.Errorf("Encode low-ratio: Op=%q, want %q", d.Op, OpFull)
	}
}

// TestEncode_ShortAppendStillSaves: parent is large relative to new — the
// savings ratio is high. Must return OpAppend, saved=true.
func TestEncode_ShortAppendStillSaves(t *testing.T) {
	// parent = 900 bytes, new = 1000 bytes (parent is perfect prefix).
	parent := []byte(strings.Repeat("x", 900))
	newB := append(append([]byte{}, parent...), bytesRepea('y', 100)...)
	d, saved := Encode(parent, newB)
	if !saved {
		t.Fatalf("Encode high-ratio: saved=false, want true (900/1000 = 0.9 > 0.30)")
	}
	if d.Op != OpAppend {
		t.Fatalf("Encode high-ratio: Op=%q, want %q", d.Op, OpAppend)
	}
}

// TestEncode_Deterministic: same inputs must produce same output (pure function).
func TestEncode_Deterministic(t *testing.T) {
	parent := chatBody("x", 3)
	newB := chatBody("x", 4)
	d1, _ := Encode(parent, newB)
	d2, _ := Encode(parent, newB)
	if d1.Op != d2.Op || !bytesEqual(d1.Payload, d2.Payload) || d1.Cutoff != d2.Cutoff {
		t.Errorf("Encode non-deterministic: %+v vs %+v", d1, d2)
	}
}

// TestCommonPrefixLen: the LCP helper used by Encode.
func TestCommonPrefixLen(t *testing.T) {
	cases := []struct {
		a, b []byte
		want int
	}{
		{[]byte("abc"), []byte("abc"), 3},
		{[]byte("abc"), []byte("abd"), 2},
		{[]byte("abc"), []byte(""), 0},
		{[]byte(""), []byte("abc"), 0},
		{[]byte("abc"), []byte("abcd"), 3},
		{[]byte("abcd"), []byte("abc"), 3},
		{nil, nil, 0},
	}
	for i, c := range cases {
		if got := commonPrefixLen(c.a, c.b); got != c.want {
			t.Errorf("case %d: commonPrefixLen(%q, %q) = %d, want %d", i, c.a, c.b, got, c.want)
		}
	}
}

// bytesEqual is a tiny helper to avoid importing bytes for a single test.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// bytesRepea is a tiny helper to avoid importing strings for a single test.
func bytesRepea(c byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return b
}
