//go:build integration

package requestjourney

import (
	"context"
	"testing"
	"time"
)

func TestPostgresRepositoryAttemptFactsPreserveNodeFailureThenSuccess(t *testing.T) {
	pool, _, _, cleanup := setupIntegrationOutboxEnv(t)
	defer cleanup()

	ctx := context.Background()
	repository := NewPostgresRepository(pool)
	tenantID := "rj-it-attempt-facts"
	requestID := "request-node-a-fails-node-b-succeeds"
	startedAt := time.Now().UTC().Add(-time.Minute)

	events := []JourneyEvent{
		attemptFactEvent(tenantID, requestID, 1, EventAttemptStarted, startedAt, "attempt-a", 1, "model-a", 101, 11, ""),
		attemptFactEvent(tenantID, requestID, 2, EventAttemptFailed, startedAt.Add(time.Second), "attempt-a", 1, "model-a", 101, 11, OutcomeFailure),
		{
			TenantID: tenantID, GatewayInstanceID: "gateway-it", RequestID: requestID,
			Seq: 3, Type: EventNodeSwitched, Stage: StageRetrying,
			FromCredentialID: 11, ToCredentialID: 22, SwitchReason: "cred_switch",
			ObservationStatus: ObservationComplete, OccurredAt: startedAt.Add(2 * time.Second),
		},
		attemptFactEvent(tenantID, requestID, 4, EventAttemptStarted, startedAt.Add(3*time.Second), "attempt-b", 2, "model-b", 202, 22, ""),
		attemptFactEvent(tenantID, requestID, 5, EventAttemptSucceeded, startedAt.Add(4*time.Second), "attempt-b", 2, "model-b", 202, 22, OutcomeSuccess),
	}
	events[1].ErrorKind = ErrorKindUpstreamError
	events[1].HTTPStatus = 503
	for _, event := range events {
		if err := repository.Apply(ctx, event); err != nil {
			t.Fatalf("persist journey event %d: %v", event.Seq, err)
		}
	}

	facts, err := repository.AttemptFacts(ctx, tenantID, startedAt.Add(-time.Second), startedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("AttemptFacts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("attempt facts = %+v, want two attempts", facts)
	}
	if got := facts[0]; got.AttemptID != "attempt-a" || got.AttemptNo != 1 || got.ProviderID != 101 || got.CredentialID != 11 || got.Model != "model-a" || got.Outcome != OutcomeFailure || got.ErrorKind != ErrorKindUpstreamError || got.HTTPStatus != 503 || !got.NodeSwitched {
		t.Fatalf("first attempt fact = %+v", got)
	}
	if got := facts[1]; got.AttemptID != "attempt-b" || got.AttemptNo != 2 || got.ProviderID != 202 || got.CredentialID != 22 || got.Model != "model-b" || got.Outcome != OutcomeSuccess || got.ErrorKind != "" || got.HTTPStatus != 0 || got.NodeSwitched {
		t.Fatalf("second attempt fact = %+v", got)
	}
}

func attemptFactEvent(tenantID, requestID string, seq int64, eventType EventType, occurredAt time.Time, attemptID string, attemptNo int, model string, providerID, credentialID int64, outcome Outcome) JourneyEvent {
	return JourneyEvent{
		TenantID: tenantID, GatewayInstanceID: "gateway-it", RequestID: requestID,
		Seq: seq, Type: eventType, Stage: StageUpstream, Model: model,
		ProviderID: providerID, CredentialID: credentialID,
		Attempt: &AttemptRef{AttemptID: attemptID, AttemptNo: attemptNo, Model: model, ProviderID: providerID, CredentialID: credentialID},
		Outcome: outcome, ObservationStatus: ObservationComplete, OccurredAt: occurredAt,
	}
}
