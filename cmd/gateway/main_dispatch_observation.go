package main

import (
	"context"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

// dispatchJourneyAdapter is the composition adapter between dispatch's
// content-free observation contract and the persisted RequestJourney contract.
// The dispatch pipeline owns ordering and bounded asynchronous delivery; this
// adapter only translates immutable values and never changes execution.
type dispatchJourneyAdapter struct {
	recorder *requestjourney.Recorder
}

func newDispatchJourneyAdapter(recorder *requestjourney.Recorder) dispatch.ObservationSink {
	if recorder == nil {
		return nil
	}
	return &dispatchJourneyAdapter{recorder: recorder}
}

// ObserveDispatch translates one dispatch observation into a RequestJourney
// event and applies it to the recorder. Translation errors are logged and
// dropped; execution never depends on observation persistence.
func (a *dispatchJourneyAdapter) ObserveDispatch(ctx context.Context, observation dispatch.Observation) {
	if a == nil || a.recorder == nil {
		return
	}
	event, err := translateDispatchObservation(observation)
	if err != nil {
		slog.Warn("dispatch observation translation rejected", "request_id", observation.RequestID, "seq", observation.Seq, "error", err)
		return
	}
	if err := a.recorder.Apply(ctx, event); err != nil {
		slog.Warn("dispatch request journey observation rejected", "request_id", observation.RequestID, "seq", observation.Seq, "error", err)
	}
}

func translateDispatchObservation(observation dispatch.Observation) (requestjourney.JourneyEvent, error) {
	if err := observation.Validate(); err != nil {
		return requestjourney.JourneyEvent{}, err
	}
	event := requestjourney.JourneyEvent{
		TenantID: observation.TenantID, GatewayInstanceID: observation.GatewayInstanceID, RequestID: observation.RequestID,
		Seq: observation.Seq, Type: requestjourney.EventType(observation.Type), Stage: requestjourney.JourneyStage(observation.Stage),
		RequestedModel: observation.RequestedModel, ResolvedModel: observation.ResolvedModel, Model: observation.Model,
		ProviderID: observation.ProviderID, Provider: observation.Provider, CredentialID: observation.CredentialID,
		FromModel: observation.FromModel, ToModel: observation.ToModel, FromCredentialID: observation.FromCredentialID,
		ToCredentialID: observation.ToCredentialID, Outcome: requestjourney.Outcome(observation.Outcome), ErrorKind: observation.ErrorKind,
		HTTPStatus: observation.HTTPStatus, RetryReason: observation.RetryReason, SwitchReason: observation.SwitchReason,
		RetryAt:           observation.RetryAt,
		ObservationStatus: requestjourney.ObservationComplete, OccurredAt: observation.OccurredAt,
	}
	if observation.Attempt != nil {
		event.Attempt = &requestjourney.AttemptRef{
			AttemptID: observation.Attempt.AttemptID, AttemptNo: observation.Attempt.AttemptNo,
			Model: observation.Attempt.Model, ProviderID: observation.Attempt.ProviderID,
			Provider: observation.Attempt.Provider, CredentialID: observation.Attempt.CredentialID,
		}
	}
	if err := event.Validate(); err != nil {
		return requestjourney.JourneyEvent{}, err
	}
	return event, nil
}
