package delta

import (
	"errors"
	"testing"
)

// TestApply_OpFull: OpFull bypasses the parent — returns delta.Payload as-is.
func TestApply_OpFull(t *testing.T) {
	payload := []byte(`{"hello":"world"}`)
	d := Delta{Op: OpFull, Payload: payload}
	got, err := Apply(nil, d)
	if err != nil {
		t.Fatalf("Apply OpFull: %v", err)
	}
	if !bytesEqual(got, payload) {
		t.Errorf("Apply OpFull: got %q, want %q", got, payload)
	}
}

// TestApply_OpAppend: parent || delta.Payload.
func TestApply_OpAppend(t *testing.T) {
	parent := []byte(`{"messages":[`)
	tail := []byte(`{"role":"user","content":"hi"}]}`)
	d := Delta{Op: OpAppend, Payload: tail}
	want := append(append([]byte{}, parent...), tail...)
	got, err := Apply(parent, d)
	if err != nil {
		t.Fatalf("Apply OpAppend: %v", err)
	}
	if !bytesEqual(got, want) {
		t.Errorf("Apply OpAppend: got %q, want %q", got, want)
	}
}

// TestApply_OpReplaceTail: parent[:Cutoff] || delta.Payload.
func TestApply_OpReplaceTail(t *testing.T) {
	parent := []byte(`{"messages":[s,h1,h2,h3]}`)
	// len(parent) = 25, replace the last 2 chars `]}` with new tail.
	d := Delta{Op: OpReplaceTail, Cutoff: 23, Payload: []byte(`new_turn"}`)}
	want := []byte(`{"messages":[s,h1,h2,h3new_turn"}`)
	got, err := Apply(parent, d)
	if err != nil {
		t.Fatalf("Apply OpReplaceTail: %v", err)
	}
	if !bytesEqual(got, want) {
		t.Errorf("Apply OpReplaceTail: got %q, want %q", got, want)
	}
}

// TestApply_OpReplaceTailZeroCutoff: Cutoff=0 means "drop everything".
func TestApply_OpReplaceTailZeroCutoff(t *testing.T) {
	parent := []byte(`old_parent`)
	d := Delta{Op: OpReplaceTail, Cutoff: 0, Payload: []byte(`completely_new`)}
	got, err := Apply(parent, d)
	if err != nil {
		t.Fatalf("Apply OpReplaceTail Cutoff=0: %v", err)
	}
	if !bytesEqual(got, []byte(`completely_new`)) {
		t.Errorf("Apply OpReplaceTail Cutoff=0: got %q, want %q", got, `completely_new`)
	}
}

// TestApply_InvalidOp: unknown Op → ErrInvalidOp.
func TestApply_InvalidOp(t *testing.T) {
	d := Delta{Op: Op("garbage"), Payload: []byte(`x`)}
	_, err := Apply([]byte(`p`), d)
	if err == nil {
		t.Fatalf("Apply with invalid Op: no error, want ErrInvalidOp")
	}
	if !errors.Is(err, ErrInvalidOp) {
		t.Errorf("Apply invalid Op: err = %v, want ErrInvalidOp", err)
	}
}

// TestApply_EmptyParent_OpAppend: OpAppend without parent → ErrEmptyParent.
func TestApply_EmptyParent_OpAppend(t *testing.T) {
	d := Delta{Op: OpAppend, Payload: []byte(`tail`)}
	_, err := Apply(nil, d)
	if !errors.Is(err, ErrEmptyParent) {
		t.Errorf("Apply OpAppend empty parent: err = %v, want ErrEmptyParent", err)
	}
}

// TestApply_EmptyParent_OpReplaceTail: same for OpReplaceTail.
func TestApply_EmptyParent_OpReplaceTail(t *testing.T) {
	d := Delta{Op: OpReplaceTail, Cutoff: 0, Payload: []byte(`x`)}
	_, err := Apply(nil, d)
	if !errors.Is(err, ErrEmptyParent) {
		t.Errorf("Apply OpReplaceTail empty parent: err = %v, want ErrEmptyParent", err)
	}
}

// TestApply_InvalidCutoff: Cutoff < 0 → ErrInvalidCutoff.
func TestApply_InvalidCutoff(t *testing.T) {
	parent := []byte(`abc`)
	d := Delta{Op: OpReplaceTail, Cutoff: -1, Payload: []byte(`x`)}
	_, err := Apply(parent, d)
	if !errors.Is(err, ErrInvalidCutoff) {
		t.Errorf("Apply Cutoff=-1: err = %v, want ErrInvalidCutoff", err)
	}
}

// TestApply_CutoffExceedsParent: Cutoff > len(parent) → ErrInvalidCutoff.
func TestApply_CutoffExceedsParent(t *testing.T) {
	parent := []byte(`abc`)
	d := Delta{Op: OpReplaceTail, Cutoff: 10, Payload: []byte(`x`)}
	_, err := Apply(parent, d)
	if !errors.Is(err, ErrInvalidCutoff) {
		t.Errorf("Apply Cutoff>len(parent): err = %v, want ErrInvalidCutoff", err)
	}
}

// TestApply_EmptyPayload_OpAppend: OpAppend with empty payload → ErrEmptyPayload
// (caller bug — silent no-op would be worse than an explicit error).
func TestApply_EmptyPayload_OpAppend(t *testing.T) {
	parent := []byte(`abc`)
	d := Delta{Op: OpAppend, Payload: nil}
	_, err := Apply(parent, d)
	if !errors.Is(err, ErrEmptyPayload) {
		t.Errorf("Apply OpAppend empty payload: err = %v, want ErrEmptyPayload", err)
	}
}

// TestApply_OpFull_EmptyPayload: OpFull with empty payload returns empty (not
// an error) — degenerate but legitimate (caller might want to "store nothing").
func TestApply_OpFull_EmptyPayload(t *testing.T) {
	d := Delta{Op: OpFull, Payload: nil}
	got, err := Apply([]byte(`parent_unused`), d)
	if err != nil {
		t.Fatalf("Apply OpFull empty payload: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Apply OpFull empty payload: got %q, want empty", got)
	}
}

// TestApply_RoundTrip is the AC-2 property test: for a representative set of
// (parent, new) pairs, Apply(parent, Encode(parent, new)) reconstructs new
// EXACTLY when Encode saves. (When Encode falls back to OpFull, Apply simply
// returns delta.Payload == new, trivially.)
func TestApply_RoundTrip(t *testing.T) {
	cases := []struct {
		name        string
		parent, new []byte
	}{
		{"identical", []byte(`abc`), []byte(`abc`)},
		{"chat_append", chatBody("helpful", 6), chatBody("helpful", 7)},
		{"replace_tail", []byte(`{"msgs":[h1,h2,h3]}`), []byte(`{"msgs":[h1,h2,NEW]}`)},
		{"no_common", []byte(`AAA`), []byte(`bbb`)},
		{"long_shared", bytesRepea('x', 1000), append(bytesRepea('x', 1000), []byte("extra_tail")...)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			delta, _ := Encode(c.parent, c.new)
			got, err := Apply(c.parent, delta)
			if err != nil {
				t.Fatalf("Apply(%q, %+v): %v", c.parent, delta, err)
			}
			if !bytesEqual(got, c.new) {
				t.Errorf("RoundTrip: got %q, want %q", got, c.new)
			}
		})
	}
}

// TestApply_EmptyParent_OpFull: OpFull with no parent is allowed (no parent
// is needed), so this is not an error.
func TestApply_OpFull_IgnoresParent(t *testing.T) {
	d := Delta{Op: OpFull, Payload: []byte(`x`)}
	got, err := Apply([]byte(`unused`), d)
	if err != nil {
		t.Fatalf("Apply OpFull with parent: %v", err)
	}
	if !bytesEqual(got, []byte(`x`)) {
		t.Errorf("Apply OpFull ignores parent: got %q", got)
	}
}
