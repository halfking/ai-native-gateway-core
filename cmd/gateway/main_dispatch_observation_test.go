package main

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

func TestTranslateDispatchObservationPreservesIdentitySequenceAndAttempt(t *testing.T) {
	occurredAt := time.Now()
	attemptID := uuid.NewString()
	observation := dispatch.Observation{
		TenantID: "tenant-a", GatewayInstanceID: "gateway-a", RequestID: "request-a", Seq: 9,
		Type: dispatch.ObservationAttemptStarted, Stage: dispatch.StageUpstream,
		RequestedModel: "client-model", ResolvedModel: "resolved-model", Model: "resolved-model",
		ProviderID: 7, Provider: "vendor-a", CredentialID: 22,
		Attempt:    &dispatch.AttemptRef{AttemptID: attemptID, AttemptNo: 2, Model: "resolved-model", ProviderID: 7, Provider: "vendor-a", CredentialID: 22},
		OccurredAt: occurredAt,
	}
	event, err := translateDispatchObservation(observation)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if event.Type != requestjourney.EventAttemptStarted || event.Stage != requestjourney.StageUpstream || event.Seq != 9 {
		t.Fatalf("translated lifecycle fields = %+v", event)
	}
	if event.Attempt == nil || event.Attempt.AttemptID != attemptID || event.Attempt.AttemptNo != 2 {
		t.Fatalf("translated attempt = %+v", event.Attempt)
	}
	if !event.OccurredAt.Equal(occurredAt) || event.RequestedModel != "client-model" {
		t.Fatalf("translated identity/timestamp = %+v", event)
	}
}

func TestDispatchJourneyAdapterSharesSequenceTerminalAndAttemptUUID(t *testing.T) {
	projection := requestjourney.NewProjection(requestjourney.DefaultConfig())
	recorder := requestjourney.NewRecorder(projection, nil, nil)
	lifecycle := requestjourney.NewLifecycle(recorder, "gateway-a", "request-a")
	lifecycle.BindTenant(context.Background(), "tenant-a", "client-model")
	lifecycle.RouteResolved(context.Background(), "tenant-a", "client-model", "resolved-model")

	pipeline := dispatch.NewPipeline(dispatch.Deps{
		ObservationSink: newDispatchJourneyAdapter(recorder),
		ModelResolveFunc: func(context.Context, string, []string) (string, []string, error) {
			return "resolved-model", nil, nil
		},
		RouteFunc: func(context.Context, *dispatch.QueuedRequest) ([]dispatch.CredentialRef, error) {
			return []dispatch.CredentialRef{{CredentialID: 22, ProviderID: 7, ConcurrencyMode: dispatch.ModeDisabled}}, nil
		},
		ForwardFunc: func(_ context.Context, request *dispatch.QueuedRequest, _ dispatch.CredentialRef) dispatch.ForwardOutcome {
			request.MarkFirstSemanticByte()
			return dispatch.ForwardOutcome{Result: "ok"}
		},
	})
	pipeline.Start()
	defer pipeline.Stop()
	request := dispatch.NewQueuedRequest("request-a", "tenant-a", "client-model", context.Background(), nil)
	request.GatewayInstanceID = "gateway-a"
	request.JourneySharedSeq = lifecycle.Sequence()
	request.JourneyTerminal = lifecycle.TerminalState()
	if _, err := pipeline.Submit(context.Background(), request); err != nil {
		t.Fatalf("submit: %v", err)
	}
	lifecycle.Terminal(context.Background(), "tenant-a", requestjourney.OutcomeSuccess, "", 200)

	deadline := time.Now().Add(time.Second)
	var journey *requestjourney.RequestJourney
	for time.Now().Before(deadline) {
		journey, _ = projection.Detail("tenant-a", "request-a")
		if journey != nil && len(journey.Events) >= 3 && journey.Events[len(journey.Events)-1].Stage == requestjourney.StageTerminal {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if journey == nil {
		t.Fatal("journey was not projected")
	}
	terminals := 0
	var attemptID string
	for i, event := range journey.Events {
		if event.Seq != int64(i+1) {
			t.Fatalf("event %d seq=%d, want %d", i, event.Seq, i+1)
		}
		if event.Stage == requestjourney.StageTerminal {
			terminals++
		}
		if event.Attempt != nil {
			if attemptID == "" {
				attemptID = event.Attempt.AttemptID
			} else if event.Attempt.AttemptID != attemptID {
				t.Fatalf("attempt ID changed from %q to %q", attemptID, event.Attempt.AttemptID)
			}
		}
	}
	if terminals != 1 || journey.Events[len(journey.Events)-1].Type != requestjourney.EventRequestSucceeded {
		t.Fatalf("terminal contract = %+v", journey.Events)
	}
	if _, err := uuid.Parse(attemptID); err != nil {
		t.Fatalf("attempt ID is not UUID: %q: %v", attemptID, err)
	}
}

func TestTranslateDispatchObservationRejectsInvalidObservation(t *testing.T) {
	if _, err := translateDispatchObservation(dispatch.Observation{}); err == nil {
		t.Fatal("invalid observation was accepted")
	}
}
