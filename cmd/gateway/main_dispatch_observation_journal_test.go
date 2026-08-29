package main

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

func testJournalSnapshot(version int64, count int) dispatch.JournalSnapshot {
	entries := make([]dispatch.JournalEntry, count)
	for i := range entries {
		entries[i] = dispatch.JournalEntry{Seq: i + 1, At: time.Unix(int64(i+1), 0).UTC(), Action: dispatch.NextActionRetrySameCred, Model: "model-a"}
	}
	return dispatch.JournalSnapshot{
		TenantID: "tenant-a", RequestID: "request-a", Entries: entries,
		SnapshotVersion: version, CallerTenantID: "tenant-a", CallerAuthorized: true,
	}
}

func TestDispatchJourneyJournalAdapterUsesSnapshotVersionReceipt(t *testing.T) {
	projection := requestjourney.NewProjection(requestjourney.DefaultConfig())
	recorder := requestjourney.NewRecorder(projection, nil, nil)
	adapter := newDispatchJourneyJournalAdapter(recorder, "gateway-a").(*dispatchJourneyJournalAdapter)
	snapshot := testJournalSnapshot(101, 2)
	adapter.ApplyJournalSnapshot(context.Background(), snapshot)
	adapter.ApplyJournalSnapshot(context.Background(), snapshot)

	journey, err := projection.Detail("tenant-a", "request-a")
	if err != nil || journey == nil {
		t.Fatalf("journey detail: %v %+v", err, journey)
	}
	if got := len(journey.Events); got != 2 {
		t.Fatalf("duplicate truncated snapshot appended events: got %d want 2", got)
	}
}

func TestDispatchJourneyJournalAdapterAllowsNewVersionAndRejectsZero(t *testing.T) {
	projection := requestjourney.NewProjection(requestjourney.DefaultConfig())
	recorder := requestjourney.NewRecorder(projection, nil, nil)
	adapter := newDispatchJourneyJournalAdapter(recorder, "gateway-a")
	adapter.ApplyJournalSnapshot(context.Background(), testJournalSnapshot(1, 1))
	adapter.ApplyJournalSnapshot(context.Background(), testJournalSnapshot(2, 1))
	adapter.ApplyJournalSnapshot(context.Background(), testJournalSnapshot(0, 1))

	journey, err := projection.Detail("tenant-a", "request-a")
	if err != nil || journey == nil {
		t.Fatalf("journey detail: %v %+v", err, journey)
	}
	if got := len(journey.Events); got != 2 {
		t.Fatalf("version handling produced %d events, want 2", got)
	}
}

func TestDispatchJourneyJournalAdapterSerializesConcurrentRetry(t *testing.T) {
	projection := requestjourney.NewProjection(requestjourney.DefaultConfig())
	recorder := requestjourney.NewRecorder(projection, nil, nil)
	adapter := newDispatchJourneyJournalAdapter(recorder, "gateway-a")
	snapshot := testJournalSnapshot(11, 3)
	done := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		go func() {
			adapter.ApplyJournalSnapshot(context.Background(), snapshot)
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	journey, err := projection.Detail("tenant-a", "request-a")
	if err != nil || journey == nil || len(journey.Events) != 3 {
		t.Fatalf("concurrent retry produced %+v (err=%v)", journey, err)
	}
}

func TestDispatchJourneyJournalAdapterRejectsSameVersionPayloadConflict(t *testing.T) {
	projection := requestjourney.NewProjection(requestjourney.DefaultConfig())
	recorder := requestjourney.NewRecorder(projection, nil, nil)
	adapter := newDispatchJourneyJournalAdapter(recorder, "gateway-a")
	first := testJournalSnapshot(7, 1)
	second := testJournalSnapshot(7, 1)
	second.Entries[0].Model = "model-b"
	adapter.ApplyJournalSnapshot(context.Background(), first)
	adapter.ApplyJournalSnapshot(context.Background(), second)

	journey, err := projection.Detail("tenant-a", "request-a")
	if err != nil || journey == nil {
		t.Fatalf("journey detail: %v %+v", err, journey)
	}
	if got := len(journey.Events); got != 1 || journey.Events[0].Model != "model-a" {
		t.Fatalf("conflicting snapshot changed projection: %+v", journey.Events)
	}
}
