package streaming

import (
	"errors"
	"net/http"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/handoff"
)

// TestHandoffConfirmationErrorMapping (2026-10-01 第十八轮审计) pins the
// status vocabulary of handoffConfirmationError. The previous default branch
// rendered EVERY unclassified error — including raw DB infrastructure errors
// (connection reset, deadlock, timeout) — as 404 handoff_confirmation_invalid,
// which is indistinguishable from "the proposal does not exist": an idempotent
// client would keep retrying a confirmation that can never succeed until the
// TTL expires. 21ea84833 removed the only known trigger (the $3/$7 bind-type
// conflict); this guard keeps the classification itself honest.
func TestHandoffConfirmationErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantKind string
	}{
		{"invalid proposal keeps 404", handoff.ErrConfirmationInvalid, http.StatusNotFound, "handoff_confirmation_invalid"},
		{"expired is 410", handoff.ErrConfirmationExpired, http.StatusGone, "handoff_confirmation_expired"},
		{"replay is 409", handoff.ErrConfirmationReplay, http.StatusConflict, "handoff_confirmation_replayed"},
		{"budget exhausted is 409", handoff.ErrConfirmationBudgetExhausted, http.StatusConflict, "handoff_budget_exhausted"},
		{"goal restore retryable is 503", handoff.ErrGoalRestoreRetryable, http.StatusServiceUnavailable, "handoff_goal_restore_retryable"},
		// The case that motivated the fix: an unclassified (infrastructure)
		// error must NOT be reported as "invalid proposal".
		{"raw DB outage is 503 unavailable", errors.New("simulated DB outage"), http.StatusServiceUnavailable, "handoff_confirmation_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, kind := handoffConfirmationError(tc.err)
			if code != tc.wantCode || kind != tc.wantKind {
				t.Fatalf("handoffConfirmationError(%v) = (%d, %q), want (%d, %q)",
					tc.err, code, kind, tc.wantCode, tc.wantKind)
			}
		})
	}
}
