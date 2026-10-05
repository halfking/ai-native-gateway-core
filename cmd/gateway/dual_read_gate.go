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
//
// # Rule 3, added 2026-10-05 (§9.235): coverage, not just presence
//
// Rules 1 and 2 are both **binary**. Rule 2 asks "were there any v1 rows?",
// so a window that contains v1 traffic for one hour out of a hundred and forty
// passes it — and then reports `s4_ready = true` on the strength of that one
// hour, with nothing in the payload saying the other hundred and thirty-nine
// were never compared against anything.
//
// This is not hypothetical. The local real database has a **5-day v1 write
// outage** inside its own v1 range: writes stop at 2026-09-06 22:00 and resume
// at 2026-09-11 21:00, while `session_turns` keeps recording throughout. The
// `request_logs_2026_09` partition is intact and spans 09-03→09-30, so this is
// a write outage, not a retention drop. A window covering 09-06→09-12 has
// v1 traffic in 26 of 146 hours that saw any traffic at all — **20.3%**.
//
// ⚠ An earlier draft of this comment claimed a second, live instance of the
// same shape: that `storage.request_logs_write_enabled` is absent from
// `settings_kv` (true — the key is not set locally, so the default `true`
// applies) and therefore that v1 had stopped writing, which a parent-only
// query seemed to confirm with a newest row of 2026-10-04 23:42. **That was
// wrong**: `request_logs_hot` is a separate, disjoint table and its newest row
// was minutes old. Re-measured with both storage faces, the last 6h, 24h and
// 72h are each at 100% coverage. §9.160.7's trap, hit for the third time in
// this section — including in the evidence for the rule written to catch that
// class of mistake. The measured trigger case below is the outage window.
//
// Coverage is measured over **hours that saw traffic on either side**, not over
// wall-clock hours: an hour with no requests on either side cannot be
// compared, but it is not a gap in the comparison — penalising it would make
// the number a function of how quiet the cluster is rather than of how much was
// actually verified.
//
// The floor is a design choice, stated here so it can be argued with: the claim
// the gate makes is "this window shows no real loss", and that claim is only
// worth making about the part of the window that had a v1 side to compare
// against. 90% leaves a tenth of the window unverified at most.
const (
	// s4GateReasonV1WritesDisabled: the S4 stop-write gate is off, so the
	// V1 side of the comparison is frozen and no drift can be observed.
	s4GateReasonV1WritesDisabled = "v1_writes_disabled"
	// s4GateReasonNoV1Traffic: the window contained no V1 rows at all, so
	// there was nothing to compare — with or without the setting flipped.
	s4GateReasonNoV1Traffic = "no_v1_traffic_in_window"
	// s4GateReasonInsufficientV1Coverage: v1 traffic was present, but only in
	// a small fraction of the hours that had any traffic, so the drift
	// measurement characterises a minority of the window. Distinct from
	// s4GateReasonNoV1Traffic on purpose: "nothing to compare" and "compared a
	// fifth of it" are different operator messages with different remedies
	// (turn v1 writes back on vs. pick a window that overlaps live v1 traffic).
	s4GateReasonInsufficientV1Coverage = "insufficient_v1_coverage_in_window"
	// s4GateReasonWindowTooShort: the window is too small to be evidence, no
	// matter how clean it looks. Separate from the coverage rule because the
	// operator's remedy is different in kind — a 1h window is 100% covered and
	// there is nothing wrong with the write path; the window itself is just too
	// small to license an irreversible cutover.
	s4GateReasonWindowTooShort = "window_too_short_to_be_evidence"
)

// s4MinWindowHours is the shortest window whose verdict may be "ready".
//
// Rule 3 (§9.235) constrains the **fraction** of a window that was compared
// against v1, and says nothing about the window's **size**. That leaves a
// degenerate case which is not a corner at all: with a 1-hour window, coverage
// is 100% by construction (one hour, covered), so rule 3 passes, and if that
// hour happened to contain no loss the gate answers `s4_ready = true`.
//
// Measured on production 252 on 2026-10-05, read-only: over the last 7 days
// genuine_loss is **10 rows spread across 7 distinct hours — only 4.17% of
// hours contain any loss at all**. Treating the loss hours as independent, the
// probability that a window of N hours catches *none* of them is
// (1 − 0.04167)^N:
//
//	N=1h → 95.8% miss      N=24h → 36.1% miss
//	N=6h → 77.5% miss      N=72h →  4.8% miss
//	                   N=168h →  0.08% miss
//
// So a one-hour observation reports a clean bill of health while being wrong
// nineteen times out of twenty. That is the same failure the three existing
// rules exist to prevent, one level down: they all ask whether the observation
// could support the claim, and none of them looks at how much there was to
// look at.
//
// 24h is chosen as the floor because it is the shortest window that spans a
// full diurnal cycle of traffic, and it is deliberately *weaker* than the
// spec's own "7 天零漂移" exit condition (0.08% miss). This gate is a
// pre-gate: necessary, not sufficient. Anything stricter belongs in the spec's
// exit condition, not here — but nothing looser can honestly be called
// evidence. The miss-rate table above is the argument, so the number can be
// argued with rather than taken on faith.
const s4MinWindowHours = 24

// s4MinV1CoveragePP is the minimum share of traffic-bearing hours that must
// have v1 data for the window's drift measurement to describe the window.
//
// Measured on the local real database, 2026-10-05, **both storage faces**
// (parent ∪ hot — see the note above on what happens when only the parent is
// read):
//
//	last 6h    25/25 traffic-bearing hours covered  → 100%
//	last 24h   25/25                               → 100%
//	last 72h   73/73                               → 100%
//	09-06→09-12 (the local v1 write outage)  26/128  →  20.31%  ← triggers this rule
//
// So the floor has a wide margin above every live window and a wide margin
// below the outage, which is the shape a threshold should have: it is not
// tuned to the data it is judging.
const s4MinV1CoveragePP = 90.0

// s4GateInput is everything the verdict depends on. Kept as a struct rather
// than four positional args so a future field cannot be silently transposed —
// a bool in the wrong position here inverts the gate.
type s4GateInput struct {
	v1Rows      int64
	genuineLoss int64
	v1WritesOn  bool
	// v1CoveragePP is the share of traffic-bearing hours in the window that
	// had v1 data, as a percentage. It is the input to rule 3 and the value
	// that goes into the response, so the operator can see the sample size
	// rather than infer it.
	//
	// Zero is ambiguous — it means either "no v1 traffic at all" (rule 2
	// already handles it, and the message is better) or "nobody measured
	// coverage" (a caller that forgot to fill the field in). Both must NOT be
	// ready, and rule 2's message is the right one for both, so the ordering
	// of the checks below is load-bearing rather than incidental.
	v1CoveragePP float64
	// windowHours is the requested window length, after clamping. It is an
	// input to rule 4 and is the reason a short window cannot buy a Ready
	// verdict. Zero means "the caller did not say" and is treated as too short:
	// like v1CoveragePP, silence must not read as consent.
	windowHours int
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
	// Rule 3 (§9.235). Checked after rule 2 so that "no v1 traffic at all"
	// keeps its own, more specific reason — a window with 0% coverage and a
	// window with 20% coverage need different things done to them, and
	// collapsing them would tell the operator to look at the wrong knob.
	if in.v1CoveragePP < s4MinV1CoveragePP {
		return s4GateVerdict{Void: true, Reason: s4GateReasonInsufficientV1Coverage}
	}
	if in.genuineLoss > 0 {
		return s4GateVerdict{}
	}
	// Rule 4 (§9.236), and it sits BELOW the loss check on purpose.
	//
	// `void` has a contract: "this run could not evaluate drift". A short
	// window that *did* find real loss evaluated drift perfectly well and has
	// an answer, so calling it void would be the same conflation the Void/Ready
	// split was introduced to prevent — one level up, and with a new reason
	// string to disguise it. The first version of this rule checked the window
	// first and its own control pair caught it.
	//
	// Placing it after rules 1–3 is the other half of the same ordering: each
	// earlier rule keeps its own reason even when the window is also too short,
	// because the operator should be told the first thing that is actually
	// wrong. Only a window that passes 1–3, has no loss, and is still too small
	// gets stopped here — which is precisely the case that would otherwise have
	// returned Ready.
	if in.windowHours < s4MinWindowHours {
		return s4GateVerdict{Void: true, Reason: s4GateReasonWindowTooShort}
	}
	return s4GateVerdict{Ready: true}
}

// currentV1WritesEnabled is the seam that keeps the pure verdict testable.
// Tests replace it; production reads the live setting.
var currentV1WritesEnabled = settings.RequestLogsWriteEnabled
