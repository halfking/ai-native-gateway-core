package admin

import "bytes"

// Body status values exposed on the session detail / turns-list wire.
//
// CONTRACT (2026-09-28): only two states are emitted. The design note
// (docs/audit/2026-09-25-session-storage-audit-handoff.md §5) originally
// specified three — available | dropped | unavailable — where "dropped" was
// meant to mean "cleared by the retention window". That third state is NOT
// emitted, and the reason is a schema fact rather than an implementation
// shortcut:
//
//  1. There is no body-retention setting. `lifecycle.request_body_retention_hours`
//     does not exist; settings/spec_lifecycle.go registers only
//     `hot_retention_hours` (the hot→monthly-partition promote window) and
//     `sessions_v2.turn_logs_retention_hours` (turn logs, not bodies). A
//     retention threshold would have to be invented to compute "dropped".
//
//  2. There is no body-pruning job for session_bodies. The only body-blob
//     reaper (admin/data_lifecycle_blobs.go) targets the V1 table
//     `request_logs_bodies_hot` and NULLs its columns; it never touches
//     `session_bodies`.
//
//  3. `session_bodies` is month-partitioned WITH a DEFAULT partition
//     (docs/audit/2026-08-29-session-v2-followup-readonly.md), so a body row
//     that was written stays reachable through the unified view long after
//     the wall-clock retention window any operator might configure.
//
// Together these mean a body-absent turn is indistinguishable from a turn
// whose body was never written — and for a V2 session that is overwhelmingly
// the common case, because body capture is gated behind
// `sessions_v2.request_bodies_full` / body-writer feature flags. Emitting
// "dropped" for those would make the admin UI tell operators that a retention
// policy destroyed data that in fact was never captured — the exact
// promise-vs-implementation divergence this codebase's audit rounds exist to
// catch (see docs/供应商协议优化-实施规划.md §12.9 for the same shape of finding
// on the P4 side).
//
// If a real retention signal is introduced later (a body-pruning ledger, or a
// registered retention spec), add BodyStatusDropped then and extend
// classifyBodyStatus — do not back-date the constant now.
const (
	// BodyStatusAvailable means at least one of request_delta /
	// response_delta / outbound_body carries a payload.
	BodyStatusAvailable = "available"
	// BodyStatusUnavailable means no body payload is present for this turn.
	// Covers both "never captured" and "cleared"; the two are not separable
	// with the current schema (see the contract comment above).
	BodyStatusUnavailable = "unavailable"
)

// bodyStatusFromPresence maps a "does any body column carry a payload" probe
// onto the wire constant. The list endpoint
// (admin/session_turns_unified.go) is metadata-only by contract and never
// decodes body bytes, so it feeds this from a SQL EXISTS probe instead of
// from raw columns.
func bodyStatusFromPresent(present bool) string {
	if present {
		return BodyStatusAvailable
	}
	return BodyStatusUnavailable
}

// classifyBodyStatus decides a turn's body status from the three raw JSONB
// columns as scanned from public.session_bodies_unified.
//
// A column counts as carrying a payload when it is non-empty and is not the
// JSON literal null. Postgres hands a JSONB null back as the 4 bytes "null",
// which is why a bare length check is not enough: without the null check a
// LEFT-JOIN miss on all three columns would still be reported as "available".
func classifyBodyStatus(requestDelta, responseDelta, outboundBody []byte) string {
	return bodyStatusFromPresent(
		columnHasPayload(requestDelta) ||
			columnHasPayload(responseDelta) ||
			columnHasPayload(outboundBody),
	)
}

// columnHasPayload reports whether one scanned JSONB column holds something
// other than SQL NULL / JSON null / empty input.
func columnHasPayload(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	return !bytes.Equal(trimmed, []byte("null"))
}
