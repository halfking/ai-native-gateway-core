package delta

import "fmt"

// Apply reconstructs a payload from a parent payload and a Delta.
//
// The function is the EXACT inverse of Encode for valid (parent, new) inputs
// (round-trip property tested in apply_test.go). It returns an error only on
// structurally invalid inputs; legitimate cache misses (e.g. parent evicted)
// are returned as ErrEmptyParent so the caller can map them to a cache miss.
//
// Algorithm:
//
//	OpFull         → return delta.Payload (no parent needed)
//	OpAppend       → return parent || delta.Payload
//	OpReplaceTail  → return parent[:Cutoff] || delta.Payload
//	<other Op>     → return ErrInvalidOp
//
// For OpAppend, parent MAY be empty (this is a degenerate "store first turn"
// case) — but we still require delta.Payload to be non-empty to surface
// caller bugs. For OpReplaceTail, parent MUST be non-empty.
func Apply(parent []byte, d Delta) ([]byte, error) {
	// Defensive: validate Op first. Without this, an attacker-supplied
	// Delta with Op="garbage" would silently return empty bytes.
	if !d.Op.IsValid() {
		return nil, fmt.Errorf("apply %s: %w", d.Op, ErrInvalidOp)
	}

	switch d.Op {
	case OpFull:
		// No parent needed. We accept empty Payload (caller may legitimately
		// want to store an empty OpFull — rare but valid).
		return d.Payload, nil

	case OpAppend:
		// OpAppend requires a parent to append to.
		if len(parent) == 0 {
			return nil, ErrEmptyParent
		}
		if len(d.Payload) == 0 {
			// Appending nothing is a no-op; surface it so callers don't
			// silently treat a malformed delta as a successful empty append.
			return nil, ErrEmptyPayload
		}
		out := make([]byte, 0, len(parent)+len(d.Payload))
		out = append(out, parent...)
		out = append(out, d.Payload...)
		return out, nil

	case OpReplaceTail:
		if len(parent) == 0 {
			return nil, ErrEmptyParent
		}
		if d.Cutoff < 0 || d.Cutoff > len(parent) {
			return nil, fmt.Errorf("apply replace_tail: cutoff=%d, parent len=%d: %w",
				d.Cutoff, len(parent), ErrInvalidCutoff)
		}
		out := make([]byte, 0, d.Cutoff+len(d.Payload))
		out = append(out, parent[:d.Cutoff]...)
		out = append(out, d.Payload...)
		return out, nil

	default:
		// Unreachable due to IsValid check above, but defensive.
		return nil, ErrInvalidOp
	}
}
