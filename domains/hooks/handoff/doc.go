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
// 2026-10-01 R74 (doc correction): this file used to say "Until then this
// package compiles as empty to keep go build ./... green without dragging in
// dead code paths". That is no longer true and never matched the request-side
// wiring: the package is ~7,000 lines, PrepareRequest is called from
// domains/streaming/handler.go on every request, and the confirmation
// endpoint is registered in cmd/gateway/main.go. The stale note contradicted
// trigger_hook.go's own package documentation in the same package and would
// lead a reader to dismiss the whole domain as unwired.
//
// What IS still unwired is narrower and worth stating precisely: the
// RESPONSE-side half (InterceptNonStream / InterceptStreamEnd / fire /
// CommitRequest) never joined an InterceptorChain, so the request-side
// Prepare → 202 → /v1/handoffs/confirm path is the only live one. The
// script notes above describe the historical rollout order; read
// trigger_hook.go for the current contract.
//
// Last build_seq touched: 943 (commit ee1c102c + efd107d7 series).
package handoff
