package delta

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestOp_String covers all defined Op values plus the unknown fallback.
// This is the "does the enum name match the wire format" guarantee — telemetry
// pipelines depend on stable lowercase names.
func TestOp_String(t *testing.T) {
	cases := []struct {
		op   Op
		want string
	}{
		{OpFull, "full"},
		{OpAppend, "append"},
		{OpReplaceTail, "replace_tail"},
		{Op("garbage"), "unknown"},
		{Op(""), "unknown"},
	}
	for _, c := range cases {
		if got := c.op.String(); got != c.want {
			t.Errorf("Op(%q).String() = %q, want %q", string(c.op), got, c.want)
		}
	}
}

// TestOp_IsValid exercises the validity gate. Op("") must not be valid
// (it is the zero value); known strings must be.
func TestOp_IsValid(t *testing.T) {
	cases := []struct {
		op   Op
		want bool
	}{
		{OpFull, true},
		{OpAppend, true},
		{OpReplaceTail, true},
		{Op(""), false},      // zero value — must NOT be valid
		{Op("bogus"), false}, // first undefined
		{Op("xyz"), false},   // clearly undefined
	}
	for _, c := range cases {
		if got := c.op.IsValid(); got != c.want {
			t.Errorf("Op(%q).IsValid() = %v, want %v", string(c.op), got, c.want)
		}
	}
}

// TestDelta_JSONRoundTrip ensures Delta survives JSON marshal+unmarshal —
// required because DeltaStore encodes Delta as JSON for the inner kv.Store.
func TestDelta_JSONRoundTrip(t *testing.T) {
	cases := []Delta{
		{Op: OpFull, Payload: []byte(`{"hello":"world"}`)},
		{Op: OpAppend, ParentKey: "abc123", Payload: []byte(`[+1 turn]`)},
		{Op: OpReplaceTail, ParentKey: "abc123", Payload: []byte("[+2 turns]"), Cutoff: 1024},
	}
	for i, orig := range cases {
		b, err := json.Marshal(orig)
		if err != nil {
			t.Fatalf("case %d: marshal: %v", i, err)
		}
		var got Delta
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("case %d: unmarshal: %v", i, err)
		}
		if got.Op != orig.Op {
			t.Errorf("case %d: Op = %v, want %v", i, got.Op, orig.Op)
		}
		if got.Cutoff != orig.Cutoff {
			t.Errorf("case %d: Cutoff = %d, want %d", i, got.Cutoff, orig.Cutoff)
		}
		if got.ParentKey != orig.ParentKey {
			t.Errorf("case %d: ParentKey = %q, want %q", i, got.ParentKey, orig.ParentKey)
		}
		if string(got.Payload) != string(orig.Payload) {
			t.Errorf("case %d: Payload = %q, want %q", i, got.Payload, orig.Payload)
		}
	}
}

// TestDelta_JSONOmitsEmptyFields checks that ParentKey and Cutoff are
// omitted from JSON when zero — keeps OpFull entries compact on the wire.
func TestDelta_JSONOmitsEmptyFields(t *testing.T) {
	d := Delta{Op: OpFull, Payload: []byte(`{"x":1}`)}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	// ParentKey and Cutoff have `omitempty` and are zero — must not appear.
	if contains(s, "parent_key") {
		t.Errorf("JSON contains parent_key when empty: %s", s)
	}
	if contains(s, "cutoff") {
		t.Errorf("JSON contains cutoff when 0: %s", s)
	}
	// Op and Payload must appear.
	if !contains(s, `"op":"full"`) {
		t.Errorf("JSON missing op=full: %s", s)
	}
	if !contains(s, `"payload"`) {
		t.Errorf("JSON missing payload: %s", s)
	}
}

// TestSentinelErrors covers the errors.Is contract. Each sentinel must
// match itself but not the others — prevents cross-handler confusion in
// callers (especially DeltaStore.Lookup distinguishing ErrMissingParent
// from ErrInvalidCutoff).
func TestSentinelErrors(t *testing.T) {
	sentinels := []error{
		ErrEmptyPayload,
		ErrEmptyParent,
		ErrInvalidCutoff,
		ErrInvalidOp,
		ErrMissingParent,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i == j {
				if !errors.Is(a, b) {
					t.Errorf("errors.Is(%v, %v) = false, want true (same sentinel)", a, b)
				}
				continue
			}
			if errors.Is(a, b) {
				t.Errorf("errors.Is(%v, %v) = true, want false (distinct sentinels)", a, b)
			}
		}
	}
}

// TestMinCompressionRatio_Documented pins the constant at its designed value.
// Regression guard: changing this constant is a public API change because
// callers (e.g. tests, docs) may rely on the specific 0.30 value.
func TestMinCompressionRatio_Documented(t *testing.T) {
	if MinCompressionRatio != 0.30 {
		t.Errorf("MinCompressionRatio = %v, want 0.30 (documented in package comment)", MinCompressionRatio)
	}
}

// TestMaxDeltaBytes_MatchesKV pins the cap at the kv package's MaxPayloadBytes
// so callers can use one constant for both stores.
func TestMaxDeltaBytes_MatchesKV(t *testing.T) {
	// Duplicated by design — we want the delta package to NOT import
	// kv (avoids a dependency cycle in any future rearrangement).
	// If kv.MaxPayloadBytes ever changes, update both.
	wantBytes := 64 * 1024
	if MaxDeltaBytes != wantBytes {
		t.Errorf("MaxDeltaBytes = %d, want %d (must match cache/kv MaxPayloadBytes)", MaxDeltaBytes, wantBytes)
	}
}

// contains is a tiny substring helper to avoid importing strings just for one use.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
