package main

import (
	"github.com/kaixuan/llm-gateway-go/settings"
)

// S4 gate verdicts. 2026-10-02.
//
// The defect this file exists to close: `Summarize` reported `s4_ready = true`
// whenever `genuine_loss_rows == 0`, and `storage.request_logs_write_enabled =
// false` makes that condition **permanent and unconditional** — once V1 stops
// being written, every window contains zero V1 rows, so there is nothing left
// to be missing from session_turns.
//
// Measured on the real database (PostgreSQL 17.10), running the production
// statements of mirrorDriftScopeSQL with a window that contains no V1 rows —
// which is exactly the shape of any window entirely after a stop-write flip:
//
//	V1Rows              = 0
//	V1RowsWithoutTurns   = 0
//	GenuineLossRows     = 0
//	S4Ready             = true      ← vacuous
//
// So the gate intended to answer "may we stop writing?" would answer "yes" from
// the instant writing stops, and could never answer "no" again — silently, and
// with a JSON payload byte-identical to a healthy result. That is the failure
// mode §8.5 recorded as "S4 会关掉它自己的观测手段"; this is its concrete shape.
//
// Two rules close it, and both are *negative* assertions about the ability to
// make the claim at all:
//
//  1. v1 writes must still be on. Otherwise there is no input to compare.
//  2. the window must actually have contained V1 traffic. A scan that found
//     zero items has established nothing; "no findings" from an empty scan is
//     not a finding. (This one also covers the idle-cluster and
//     tenant-filter-matches-nothing cases, which have the same vacuity without
//     any setting involved.)
//
// A verdict that is not "ready" is additionally classified: `void` means "this
// run could not evaluate drift", `drift` means "it evaluated and found loss".
// Collapsing the two would be the same bug one level up.

const (
	// s4GateReasonV1WritesDisabled: the S4 stop-write gate is off, so the
	// V1 side of the comparison is frozen and no drift can be observed.
	s4GateReasonV1WritesDisabled = "v1_writes_disabled"
	// s4GateReasonNoV1Traffic: the window contained no V1 rows at all, so
	// there was nothing to compare — with or without the setting flipped.
	s4GateReasonNoV1Traffic = "no_v1_traffic_in_window"
)

// s4GateInput is everything the verdict depends on. Kept as a struct rather
// than four positional args so a future field cannot be silently transposed —
// a bool in the wrong position here inverts the gate.
type s4GateInput struct {
	v1Rows      int64
	genuineLoss int64
	v1WritesOn  bool
}

// s4GateVerdict is the S4 pre-gate's answer.
type s4GateVerdict struct {
	// Ready = "the observation supports flipping storage.request_logs_write_enabled".
	Ready bool
	// Void = "this run could not evaluate drift at all" (as opposed to
	// "evaluated and found loss"). Void must never be reported as Ready.
	Void bool
	// Reason is set iff Void; it is the machine-readable form of the same
	// sentence a human needs in order to act.
	Reason string
}

// s4GateVerdictOf is the S4 pre-gate decision, extracted as a pure function so
// it can be tested without a database. The only part of `Summarize` that can
// "say something wrong" lives here; the SQL around it merely supplies numbers.
func s4GateVerdictOf(in s4GateInput) s4GateVerdict {
	if !in.v1WritesOn {
		return s4GateVerdict{Void: true, Reason: s4GateReasonV1WritesDisabled}
	}
	if in.v1Rows == 0 {
		return s4GateVerdict{Void: true, Reason: s4GateReasonNoV1Traffic}
	}
	if in.genuineLoss > 0 {
		return s4GateVerdict{}
	}
	return s4GateVerdict{Ready: true}
}

// currentV1WritesEnabled is the seam that keeps the pure verdict testable.
// Tests replace it; production reads the live setting.
var currentV1WritesEnabled = settings.RequestLogsWriteEnabled
