// Package handoff was the auto-trigger hook for context-window-exhausted
// handoffs. It was parked on 2026-07-06 because the SQL referenced a
// `sessions` master table that does not exist in any branch of the
// codebase. Real session tracking lives in `session_summaries` (PG) +
// Redis (live state).
//
// The original implementation has been moved to:
//
//	_to-be-deprecated/hooks-handoff-20260706/trigger_hook.go
//
// If you intend to revive this auto-handoff hook:
//  1. Replace sessions.id       → session_summaries.session_key
//  2. Replace total_tokens_used → session_summaries.total_tokens
//  3. handoff_count / last_handoff_at now live on session_summaries
//     (migration 354 added them on 2026-07-06).
//  4. Wire NewTriggerHook into cmd/gateway/main.go (Phase 4 response
//     interceptor pipeline).
//
// Until then this package compiles as empty to keep `go build ./...`
// green without dragging in dead code paths.
//
// Last build_seq touched: 943 (commit ee1c102c + efd107d7 series).
package handoff
