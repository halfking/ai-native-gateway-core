package requestjourney

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRequestJourneyRoundTripUsesStableSequenceAndSafeFields(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	journey := RequestJourney{
		TenantID:          "tenant-1",
		GatewayInstanceID: "gateway-a",
		RequestID:         "request-1",
		ObservationStatus: ObservationComplete,
		StartedAt:         now,
		UpdatedAt:         now.Add(time.Second),
		Events: []JourneyEvent{
			{
				TenantID:          "tenant-1",
				GatewayInstanceID: "gateway-a",
				RequestID:         "request-1",
				Seq:               1,
				Type:              EventRequestReceived,
				Stage:             StageReceived,
				RequestedModel:    "auto",
				ObservationStatus: ObservationComplete,
				OccurredAt:        now,
			},
			{
				TenantID:          "tenant-1",
				GatewayInstanceID: "gateway-a",
				RequestID:         "request-1",
				Seq:               2,
				Type:              EventAttemptFailed,
				Stage:             StageUpstream,
				RequestedModel:    "auto",
				ResolvedModel:     "model-a",
				ToCredentialID:    11,
				Attempt: &AttemptRef{
					AttemptID:    "attempt-1",
					AttemptNo:    1,
					Model:        "model-a",
					ProviderID:   7,
					Provider:     "provider-a",
					CredentialID: 11,
				},
				Outcome:           OutcomeFailure,
				ErrorKind:         "rate_limit",
				HTTPStatus:        429,
				RetryReason:       "provider_throttled",
				NodeHealthStatus:  NodeHealthDegraded,
				ObservationStatus: ObservationComplete,
				OccurredAt:        now.Add(time.Second),
			},
		},
	}

	body, err := MarshalRequestJourney(journey)
	if err != nil {
		t.Fatalf("MarshalRequestJourney() error = %v", err)
	}
	for _, forbidden := range []string{"request_body", "response_body", "headers", "authorization", "api_key", "token", "secret"} {
		if strings.Contains(strings.ToLower(string(body)), forbidden) {
			t.Fatalf("serialized journey contains forbidden field %q: %s", forbidden, body)
		}
	}

	decoded, err := UnmarshalRequestJourney(body)
	if err != nil {
		t.Fatalf("UnmarshalRequestJourney() error = %v", err)
	}
	if len(decoded.Events) != 2 || decoded.Events[1].Seq != 2 {
		t.Fatalf("decoded events = %#v", decoded.Events)
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["events"]; !ok {
		t.Fatal("serialized journey is missing events")
	}
}

func TestUnmarshalRequestJourneyRejectsUnknownAndInvalidFields(t *testing.T) {
	valid := `{
		"tenant_id":"tenant-1",
		"gateway_instance_id":"gateway-a",
		"request_id":"request-1",
		"observation_status":"complete",
		"started_at":"2026-08-17T10:00:00Z",
		"updated_at":"2026-08-17T10:00:00Z",
		"events":[]
	}`

	if _, err := UnmarshalRequestJourney([]byte(strings.Replace(valid, `"events":[]`, `"request_body":"secret","events":[]`, 1))); err == nil {
		t.Fatal("unknown request_body field must be rejected")
	}
	if _, err := UnmarshalRequestJourney([]byte(strings.Replace(valid, `"observation_status":"complete"`, `"observation_status":"partial"`, 1))); err == nil {
		t.Fatal("unknown observation status must be rejected")
	}
}

func TestRequestJourneyValidateRejectsUnstableSequence(t *testing.T) {
	now := time.Now().UTC()
	journey := RequestJourney{
		TenantID:          "tenant-1",
		GatewayInstanceID: "gateway-a",
		RequestID:         "request-1",
		ObservationStatus: ObservationDegraded,
		StartedAt:         now,
		UpdatedAt:         now,
		Events: []JourneyEvent{
			{TenantID: "tenant-1", GatewayInstanceID: "gateway-a", RequestID: "request-1", Seq: 2, Type: EventRequestReceived, Stage: StageReceived, ObservationStatus: ObservationDegraded, OccurredAt: now},
			{TenantID: "tenant-1", GatewayInstanceID: "gateway-a", RequestID: "request-1", Seq: 2, Type: EventRequestFailed, Stage: StageTerminal, Outcome: OutcomeFailure, ObservationStatus: ObservationDegraded, OccurredAt: now},
		},
	}

	if err := journey.Validate(); err == nil {
		t.Fatal("duplicate/non-increasing seq must be rejected")
	}
}

func TestFrozenEnums(t *testing.T) {
	wantEvents := []EventType{
		EventRequestReceived,
		EventRouteResolved,
		EventModelEnqueued,
		EventCredentialSelected,
		EventNodeEnqueued,
		EventNodeSelected,
		EventAttemptStarted,
		EventFirstByte,
		EventAttemptSucceeded,
		EventAttemptFailed,
		EventRetryScheduled,
		EventNodeSwitched,
		EventModelSwitched,
		EventRequestSucceeded,
		EventRequestFailed,
		EventRequestCanceled,
		EventObservationDegraded,
	}
	gotEvents := AllEventTypes()
	if len(gotEvents) != len(wantEvents) {
		t.Fatalf("event type count = %d, want %d: %v", len(gotEvents), len(wantEvents), gotEvents)
	}
	for i, eventType := range wantEvents {
		if gotEvents[i] != eventType || !eventType.Valid() {
			t.Errorf("event type[%d] = %q, want valid %q", i, gotEvents[i], eventType)
		}
	}
	if EventType("route_selected").Valid() || EventType("body_captured").Valid() {
		t.Fatal("unapproved event types must be invalid")
	}

	wantStages := []JourneyStage{
		StageReceived, StageRouting, StageModelQueue, StageCredentialQueue,
		StageNodeSelection, StageUpstream, StageStreaming, StageRetrying, StageTerminal,
	}
	gotStages := AllJourneyStages()
	if len(gotStages) != len(wantStages) {
		t.Fatalf("journey stage count = %d, want %d: %v", len(gotStages), len(wantStages), gotStages)
	}
	for i, stage := range wantStages {
		if gotStages[i] != stage || !stage.Valid() {
			t.Errorf("journey stage[%d] = %q, want valid %q", i, gotStages[i], stage)
		}
	}
	if JourneyStage("arbitrary").Valid() {
		t.Fatal("unknown journey stage must be invalid")
	}

	for _, status := range []ObservationStatus{ObservationComplete, ObservationDegraded} {
		if !status.Valid() {
			t.Errorf("ObservationStatus %q should be valid", status)
		}
	}
	wantNodeHealth := []NodeHealthStatus{
		NodeHealthUnknown, NodeHealthHealthy, NodeHealthSuspect, NodeHealthDegraded,
		NodeHealthCooling, NodeHealthProbing, NodeHealthRecovering,
		NodeHealthQuarantined, NodeHealthDisabled, NodeHealthUnhealthy,
	}
	gotNodeHealth := AllNodeHealthStatuses()
	if len(gotNodeHealth) != len(wantNodeHealth) {
		t.Fatalf("node health count = %d, want %d: %v", len(gotNodeHealth), len(wantNodeHealth), gotNodeHealth)
	}
	for i, status := range wantNodeHealth {
		if gotNodeHealth[i] != status || !status.Valid() {
			t.Errorf("node health[%d] = %q, want valid %q", i, gotNodeHealth[i], status)
		}
	}
	if !NodeHealthUnhealthy.Valid() {
		t.Fatal("unhealthy must remain a valid compatibility state")
	}
	if !OutcomeCanceled.Valid() || Outcome("cancelled").Valid() {
		t.Fatal("outcome must use project spelling canceled, not cancelled")
	}
}

// TestLifecycleStateFrozenEnum (v4 R1.1/UT-CO-06): the request-registry
// lifecycle vocabulary is frozen — pending / in_flight / completed.
func TestLifecycleStateFrozenEnum(t *testing.T) {
	want := []LifecycleState{LifecyclePending, LifecycleInFlight, LifecycleCompleted}
	got := AllLifecycleStates()
	if len(got) != len(want) {
		t.Fatalf("lifecycle state count = %d, want %d: %v", len(got), len(want), got)
	}
	for i, state := range want {
		if got[i] != state || !state.Valid() {
			t.Errorf("lifecycle state[%d] = %q, want valid %q", i, got[i], state)
		}
	}
	if LifecycleState("in-flight").Valid() || LifecycleState("running").Valid() {
		t.Fatal("unapproved lifecycle states must be invalid")
	}
}

// TestErrorKindVocabularyIncludesOverflow (v4 R1.3/UT-CO-06): the dispatch
// error-kind vocabulary must include "overflow" — queue-admission refusal is
// a first-class, fixture-pinned classification.
func TestErrorKindVocabularyIncludesOverflow(t *testing.T) {
	kinds := KnownErrorKinds()
	want := map[string]bool{
		ErrorKindCanceled: false, ErrorKindDeadlineExceeded: false,
		ErrorKindShutdown: false, ErrorKindPaceTimeout: false,
		ErrorKindNoRoute: false, ErrorKindOverflow: false,
		ErrorKindUpstreamError: false,
	}
	for _, kind := range kinds {
		if _, known := want[kind]; !known {
			t.Fatalf("unexpected error kind %q in frozen vocabulary", kind)
		}
		want[kind] = true
	}
	for kind, seen := range want {
		if !seen {
			t.Errorf("frozen error kind %q missing from KnownErrorKinds()", kind)
		}
	}
	if ErrorKindOverflow != "overflow" {
		t.Fatalf("ErrorKindOverflow = %q, want overflow", ErrorKindOverflow)
	}
}

// TestRetryScheduledEventCarriesRetryAt (v4 T3-8): retry_scheduled events
// may carry retry_at; the field round-trips and a zero retry_at is rejected.
func TestRetryScheduledEventCarriesRetryAt(t *testing.T) {
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	retryAt := now.Add(30 * time.Second)
	event := JourneyEvent{
		TenantID: "tenant-1", GatewayInstanceID: "gateway-a", RequestID: "request-1",
		Seq: 4, Type: EventRetryScheduled, Stage: StageRetrying,
		ResolvedModel: "model-a", CredentialID: 11,
		RetryReason: "upstream_error", RetryAt: &retryAt,
		ObservationStatus: ObservationComplete, OccurredAt: now,
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("retry_scheduled with retry_at should validate: %v", err)
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"retry_at":"`) {
		t.Fatalf("serialized retry_scheduled missing retry_at: %s", body)
	}

	zero := time.Time{}
	event.RetryAt = &zero
	if err := event.Validate(); err == nil {
		t.Fatal("zero retry_at must be rejected")
	}
}

func TestJourneyEventExplicitSwitchFields(t *testing.T) {
	now := time.Now().UTC()
	event := JourneyEvent{
		TenantID:          "tenant-1",
		GatewayInstanceID: "gateway-a",
		RequestID:         "request-1",
		Seq:               3,
		Type:              EventModelSwitched,
		Stage:             StageRouting,
		RequestedModel:    "auto",
		ResolvedModel:     "model-b",
		FromModel:         "model-a",
		ToModel:           "model-b",
		FromCredentialID:  11,
		ToCredentialID:    12,
		SwitchReason:      "no_node",
		ObservationStatus: ObservationComplete,
		OccurredAt:        now,
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("model switch event should validate: %v", err)
	}

	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"requested_model":"auto"`,
		`"resolved_model":"model-b"`,
		`"from_model":"model-a"`,
		`"to_model":"model-b"`,
		`"from_credential_id":11`,
		`"to_credential_id":12`,
	} {
		if !strings.Contains(string(body), field) {
			t.Errorf("serialized switch event missing %s: %s", field, body)
		}
	}
}

func TestJourneyEventRejectsInvalidActualIDs(t *testing.T) {
	now := time.Now().UTC()
	event := JourneyEvent{
		TenantID: "tenant-1", GatewayInstanceID: "gateway-a", RequestID: "request-1",
		Seq: 1, Type: EventCredentialSelected, Stage: StageRouting,
		CredentialID: 0, ObservationStatus: ObservationComplete, OccurredAt: now,
	}
	if err := event.Validate(); err == nil {
		t.Fatal("credential_selected requires a positive actual credential ID")
	}
	event.CredentialID = -1
	if err := event.Validate(); err == nil {
		t.Fatal("negative credential ID must be rejected")
	}
}
