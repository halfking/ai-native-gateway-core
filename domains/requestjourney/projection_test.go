package requestjourney

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestProjectionIngressFIFOEvictsOldestAndUpdatesInPlace(t *testing.T) {
	projection := NewProjection(DefaultConfig())
	arrival := time.Unix(1700000000, 0).UTC()
	for i := 101; i >= 1; i-- {
		requestID := fmt.Sprintf("ingress-%03d", i)
		if err := projection.ApplyIngress(IngressEvent{
			RequestID: requestID, GatewayInstanceID: "gateway-1",
			Protocol: IngressProtocolChat, PathClass: IngressPathChatCompletions,
			ArrivedAt: arrival.Add(time.Duration(i) * time.Millisecond),
			UpdatedAt: arrival.Add(time.Duration(i) * time.Millisecond),
			Status:    IngressStatusArrived,
		}); err != nil {
			t.Fatalf("ApplyIngress(%d): %v", i, err)
		}
	}

	got := projection.RecentIngress()
	if len(got) != 100 || got[0].RequestID != "ingress-002" || got[99].RequestID != "ingress-101" {
		t.Fatalf("ingress boundaries = %#v", got)
	}

	update := IngressEvent{
		RequestID: "ingress-002", GatewayInstanceID: "gateway-1",
		Protocol: IngressProtocolChat, PathClass: IngressPathChatCompletions,
		ArrivedAt: got[0].ArrivedAt, UpdatedAt: arrival.Add(time.Minute),
		Status: IngressStatusFailed, ErrorKind: "missing_key", HTTPStatus: 401,
	}
	if err := projection.ApplyIngress(update); err != nil {
		t.Fatal(err)
	}
	got = projection.RecentIngress()
	if got[0].RequestID != "ingress-002" || got[0].Status != IngressStatusFailed || got[0].HTTPStatus != 401 {
		t.Fatalf("update reordered or lost terminal state: %#v", got[0])
	}
}

func TestProjectionEvictsThe101stOldestRequest(t *testing.T) {
	projection := NewProjection(DefaultConfig())
	for i := 1; i <= 101; i++ {
		event := testJourneyEvent("tenant-a", fmt.Sprintf("request-%03d", i), 1)
		event.ResolvedModel = "model-a"
		if err := projection.Apply(event); err != nil {
			t.Fatalf("Apply(%d): %v", i, err)
		}
	}

	total := projection.RecentTotal("tenant-a")
	model := projection.RecentModel("tenant-a", "model-a")
	// v4: total projection stays 100; per-model projection is 30 (display
	// bound — see DefaultPerModelCapacity). Both evict FIFO oldest-first.
	if len(total) != 100 {
		t.Fatalf("total length = %d, want 100", len(total))
	}
	if total[0].RequestID != "request-002" || total[99].RequestID != "request-101" {
		t.Fatalf("total boundaries = %q..%q", total[0].RequestID, total[99].RequestID)
	}
	if len(model) != 30 {
		t.Fatalf("model length = %d, want 30 (v4 per-model projection)", len(model))
	}
	if model[0].RequestID != "request-072" || model[29].RequestID != "request-101" {
		t.Fatalf("model boundaries = %q..%q", model[0].RequestID, model[29].RequestID)
	}
	if _, err := projection.Detail("tenant-a", "request-001"); !errors.Is(err, ErrJourneyNotFound) {
		t.Fatalf("evicted detail error = %v, want ErrJourneyNotFound", err)
	}
	if _, err := projection.Detail("tenant-a", "request-002"); err != nil {
		t.Fatalf("retained detail: %v", err)
	}
}

func TestProjectionEvictsThe101stOldestAttemptPerNode(t *testing.T) {
	projection := NewProjection(DefaultConfig())
	for i := 1; i <= 101; i++ {
		event := testAttemptEvent(
			"tenant-a", fmt.Sprintf("request-%03d", i), 1,
			fmt.Sprintf("attempt-%03d", i), 1, 11,
		)
		mustApply(t, projection, event)
	}

	node := projection.RecentNode("tenant-a", NodeKey{Model: "model-a", ProviderID: 7, CredentialID: 11})
	if len(node) != 100 {
		t.Fatalf("node length = %d, want 100", len(node))
	}
	if node[0].Attempt.AttemptID != "attempt-002" || node[99].Attempt.AttemptID != "attempt-101" {
		t.Fatalf("node boundaries = %q..%q", node[0].Attempt.AttemptID, node[99].Attempt.AttemptID)
	}
}

func TestProjectionUpdatesWithoutReorderingAndModelSwitchAddsNewFIFO(t *testing.T) {
	projection := NewProjection(Config{TotalRequestCapacity: 3, PerModelCapacity: 3, PerNodeCapacity: 3})
	first := testJourneyEvent("tenant-a", "request-1", 1)
	first.ResolvedModel = "model-a"
	second := testJourneyEvent("tenant-a", "request-2", 1)
	second.ResolvedModel = "model-a"
	mustApply(t, projection, first)
	mustApply(t, projection, second)

	update := testJourneyEvent("tenant-a", "request-1", 2)
	update.Type = EventRouteResolved
	update.Stage = StageRouting
	update.ResolvedModel = "model-a"
	update.OccurredAt = update.OccurredAt.Add(time.Second)
	mustApply(t, projection, update)

	got := projection.RecentTotal("tenant-a")
	if got[0].RequestID != "request-1" || got[1].RequestID != "request-2" {
		t.Fatalf("update reordered total FIFO: %#v", got)
	}
	if got[0].LastSeq != 2 || got[0].CurrentStage != StageRouting {
		t.Fatalf("updated snapshot = %#v", got[0])
	}

	switchEvent := testJourneyEvent("tenant-a", "request-1", 3)
	switchEvent.Type = EventModelSwitched
	switchEvent.Stage = StageRouting
	switchEvent.FromModel = "model-a"
	switchEvent.ToModel = "model-b"
	switchEvent.SwitchReason = "capacity"
	switchEvent.OccurredAt = switchEvent.OccurredAt.Add(2 * time.Second)
	mustApply(t, projection, switchEvent)

	modelA := projection.RecentModel("tenant-a", "model-a")
	modelB := projection.RecentModel("tenant-a", "model-b")
	if len(modelA) != 2 || modelA[0].RequestID != "request-1" || modelA[0].ResolvedModel != "model-b" {
		t.Fatalf("old model FIFO = %#v", modelA)
	}
	if len(modelB) != 1 || modelB[0].RequestID != "request-1" {
		t.Fatalf("new model FIFO = %#v", modelB)
	}
}

func TestProjectionTracksEachAttemptAndIsIdempotentBySequence(t *testing.T) {
	projection := NewProjection(Config{TotalRequestCapacity: 10, PerModelCapacity: 10, PerNodeCapacity: 10})
	first := testAttemptEvent("tenant-a", "request-1", 1, "attempt-1", 1, 11)
	second := testAttemptEvent("tenant-a", "request-1", 2, "attempt-2", 2, 11)
	second.OccurredAt = second.OccurredAt.Add(time.Second)
	third := testAttemptEvent("tenant-a", "request-1", 3, "attempt-3", 3, 12)
	third.OccurredAt = third.OccurredAt.Add(2 * time.Second)
	mustApply(t, projection, first)
	mustApply(t, projection, first)
	mustApply(t, projection, second)
	mustApply(t, projection, third)

	nodeOne := projection.RecentNode("tenant-a", NodeKey{Model: "model-a", ProviderID: 7, CredentialID: 11})
	nodeTwo := projection.RecentNode("tenant-a", NodeKey{Model: "model-a", ProviderID: 7, CredentialID: 12})
	if len(nodeOne) != 2 || nodeOne[0].Attempt.AttemptID != "attempt-1" || nodeOne[1].Attempt.AttemptID != "attempt-2" {
		t.Fatalf("same-node attempts = %#v", nodeOne)
	}
	if len(nodeTwo) != 1 || nodeTwo[0].Attempt.AttemptID != "attempt-3" {
		t.Fatalf("switched-node attempts = %#v", nodeTwo)
	}

	detail, err := projection.Detail("tenant-a", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Events) != 3 {
		t.Fatalf("detail events = %d, want 3", len(detail.Events))
	}

	conflict := first
	conflict.Stage = StageRetrying
	if err := projection.Apply(conflict); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
}

func TestProjectionIsTenantIsolated(t *testing.T) {
	projection := NewProjection(DefaultConfig())
	mustApply(t, projection, testJourneyEvent("tenant-a", "same-request", 1))
	mustApply(t, projection, testJourneyEvent("tenant-b", "same-request", 1))

	if len(projection.RecentTotal("tenant-a")) != 1 || len(projection.RecentTotal("tenant-b")) != 1 {
		t.Fatal("tenant lists did not remain independent")
	}
	if _, err := projection.Detail("tenant-c", "same-request"); !errors.Is(err, ErrJourneyNotFound) {
		t.Fatalf("cross-tenant detail error = %v", err)
	}
}

func TestProjectionConcurrentReadersAndWriters(t *testing.T) {
	projection := NewProjection(DefaultConfig())
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			event := testJourneyEvent("tenant-race", fmt.Sprintf("request-%d", i), 1)
			if err := projection.Apply(event); err != nil {
				t.Errorf("Apply: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			_ = projection.RecentTotal("tenant-race")
			_, _ = projection.Detail("tenant-race", fmt.Sprintf("request-%d", i))
		}()
	}
	wg.Wait()
	if got := len(projection.RecentTotal("tenant-race")); got != DefaultTotalRequestCapacity {
		t.Fatalf("recent total length = %d", got)
	}
}

// TestProjectionDerivesLifecycleStateAndRetryAt (v4 R1.1/T2): snapshots
// expose the derived request-registry state (pending → in_flight →
// completed); a retrying event with retry_at parks the snapshot back to
// pending and keeps retry_at visible until the request resumes.
func TestProjectionDerivesLifecycleStateAndRetryAt(t *testing.T) {
	projection := NewProjection(DefaultConfig())
	const tenant, request = "tenant-a", "request-001"

	mustApply(t, projection, testJourneyEvent(tenant, request, 1))

	total := projection.RecentTotal(tenant)
	if len(total) != 1 || total[0].LifecycleState != LifecyclePending {
		t.Fatalf("received event should project pending, got %#v", total)
	}

	attempt := testAttemptEvent(tenant, request, 2, "attempt-1", 1, 11)
	mustApply(t, projection, attempt)
	total = projection.RecentTotal(tenant)
	if total[0].LifecycleState != LifecycleInFlight {
		t.Fatalf("upstream attempt should project in_flight, got %#v", total[0])
	}

	retryAt := time.Unix(1700000100, 0).UTC()
	retry := testJourneyEvent(tenant, request, 3)
	retry.Type = EventRetryScheduled
	retry.Stage = StageRetrying
	retry.RetryAt = &retryAt
	mustApply(t, projection, retry)
	total = projection.RecentTotal(tenant)
	if total[0].LifecycleState != LifecyclePending {
		t.Fatalf("retrying with retry_at should project pending, got %#v", total[0])
	}
	if total[0].RetryAt == nil || !total[0].RetryAt.Equal(retryAt) {
		t.Fatalf("retry_at should be visible while parked, got %#v", total[0].RetryAt)
	}

	resumed := testAttemptEvent(tenant, request, 4, "attempt-2", 2, 11)
	mustApply(t, projection, resumed)
	total = projection.RecentTotal(tenant)
	if total[0].LifecycleState != LifecycleInFlight || total[0].RetryAt != nil {
		t.Fatalf("resumed attempt should be in_flight without retry_at, got %#v", total[0])
	}

	terminal := testJourneyEvent(tenant, request, 5)
	terminal.Type = EventRequestSucceeded
	terminal.Stage = StageTerminal
	terminal.Outcome = OutcomeSuccess
	mustApply(t, projection, terminal)
	total = projection.RecentTotal(tenant)
	if total[0].LifecycleState != LifecycleCompleted {
		t.Fatalf("terminal should project completed, got %#v", total[0])
	}
}

func testJourneyEvent(tenantID, requestID string, seq int64) JourneyEvent {
	return JourneyEvent{
		TenantID:          tenantID,
		GatewayInstanceID: "gateway-1",
		RequestID:         requestID,
		Seq:               seq,
		Type:              EventRequestReceived,
		Stage:             StageReceived,
		RequestedModel:    "auto",
		ObservationStatus: ObservationComplete,
		OccurredAt:        time.Unix(1700000000+seq, 0).UTC(),
	}
}

func testAttemptEvent(tenantID, requestID string, seq int64, attemptID string, attemptNo int, credentialID int64) JourneyEvent {
	event := testJourneyEvent(tenantID, requestID, seq)
	event.Type = EventAttemptStarted
	event.Stage = StageUpstream
	event.ResolvedModel = "model-a"
	event.Model = "model-a"
	event.ProviderID = 7
	event.CredentialID = credentialID
	event.Attempt = &AttemptRef{
		AttemptID: attemptID, AttemptNo: attemptNo, Model: "model-a",
		ProviderID: 7, CredentialID: credentialID,
	}
	return event
}

func mustApply(t *testing.T, projection *Projection, event JourneyEvent) {
	t.Helper()
	if err := projection.Apply(event); err != nil {
		t.Fatalf("Apply(%s/%d): %v", event.RequestID, event.Seq, err)
	}
}
