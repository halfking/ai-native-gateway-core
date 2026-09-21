package telemetry

import "strings"

// IsInternalAutoEntry (2026-09-17 R34, lifted from internal/sessionv2mirror)
// reports whether a terminal entry is a gateway-internal auto loopback
// (auto-title / auto-summary / session-summary) rather than a business
// auto-route turn.
//
// Business auto-route requests also set IsAutoRequest, but carry TaskType and
// MUST be treated as real turns — sessionv2mirror keeps them in session_turns
// so the task dimension stays queryable, and the is_final_success claim
// (client.go) lets them win the session's final-success marker.
//
// Two gates must stay consistent by construction, which is why this function
// lives in the telemetry package next to RequestLogEntry instead of remaining
// private to the mirror:
//  1. sessionv2mirror excludes internal loopbacks from session_turns;
//  2. shouldClaimFinalSuccess must NOT let an excluded row claim
//     is_final_success — a claimed row without a mirrored turn permanently
//     inflates the GLOBAL_G2 reconciliation counter (a request_logs row with
//     is_final_success=TRUE but no session_turns row).
func IsInternalAutoEntry(entry *RequestLogEntry) bool {
	if entry == nil || entry.IsAutoRequest == nil || !*entry.IsAutoRequest {
		return false
	}
	if entry.RequestType != nil {
		switch strings.TrimSpace(*entry.RequestType) {
		case "title_gen", "summary":
			return true
		}
	}
	if entry.OriginActor != nil {
		switch strings.TrimSpace(*entry.OriginActor) {
		case "auto-title-generator", "auto-summary-generator", "session-summary":
			return true
		}
	}
	return entry.TaskType == nil || strings.TrimSpace(*entry.TaskType) == ""
}
