package dispatch

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestDecisionHistoryOfPreventsTerminalRequeue(t *testing.T) {
	qr := planQR("history-terminal")
	qr.recordDecision(JournalEntry{Action: NextActionFailed, Model: "m", CredentialID: 1, ErrorKind: string(errorsx.KindNetwork), Attempt: 1})

	got := PlanAfterFailure(qr, ForwardOutcome{ErrorKind: string(errorsx.KindNetwork)}, Config{RetryPerCredential: 2})
	if got.Action != NextActionFailed || got.Reason != "history_terminal" {
		t.Fatalf("terminal history planner result = %+v, want history_terminal failure", got)
	}
}

func TestDecisionHistoryOfAvoidsTriedCredentialLoop(t *testing.T) {
	qr := NewQueuedRequest("history-loop", "tenant", "m", context.Background(), nil)
	qr.ResolvedModel = "m"
	qr.SelectedCred = CredentialRef{CredentialID: 7, ProviderID: 11}
	qr.TriedCredentials[7] = struct{}{}
	qr.recordDecision(JournalEntry{
		Action:       NextActionRetrySameCred,
		Model:        "m",
		CredentialID: 7,
		ProviderID:   11,
		ErrorKind:    string(errorsx.KindEmptyResponse),
		Attempt:      1,
	})

	got := PlanAfterFailure(qr, ForwardOutcome{ErrorKind: string(errorsx.KindEmptyResponse)}, Config{RetryPerCredential: 2})
	if got.Action != "" || got.Reason != "node_retry_budget_exhausted" {
		t.Fatalf("tried credential planner result = %+v, want switch-ladder fallthrough", got)
	}
}
