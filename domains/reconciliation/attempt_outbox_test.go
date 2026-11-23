package reconciliation

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestAttemptOutboxSurvivesRestartAndReplaysOnlyEvidence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "attempts.db")
	failed := errors.New("projection unavailable")
	outbox, err := openAttemptOutbox(ctx, path, func(context.Context, attemptEvent) error { return failed })
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 31, 15, 59, 59, 0, time.UTC)
	a := NewAttempt(Attempt{PreparedAt: start, AttemptNo: 1, TrafficClass: "business", TenantID: "tenant", RequestID: "request", Source: "fixture"})
	if err := outbox.Prepare(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := outbox.MarkDispatched(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	JournalTransportSend(outbox, a.ID, start.Add(time.Second))
	if _, err := outbox.drain(ctx); !errors.Is(err, failed) {
		t.Fatalf("projection failure = %v", err)
	}
	if err := outbox.Close(); err != nil {
		t.Fatal(err)
	}

	var events []attemptEvent
	recovered, err := openAttemptOutbox(ctx, path, func(_ context.Context, event attemptEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if err := recovered.RecordHTTP(ctx, a.ID, 200, "provider-id", start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	amount := "0.000000000000000123"
	if err := recovered.RecordUsage(ctx, a.ID, UsageEvidence{OutputTokens: &zero, ReportedAmount: &amount, ReportedSemantics: "settlement", ReportedCurrency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if err := recovered.Finish(ctx, a.ID, "success"); err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.drain(ctx); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, event := range events {
		got = append(got, event.Kind)
		if event.ID != a.ID {
			t.Fatalf("replay changed attempt ID: %+v", event)
		}
	}
	if want := []string{"prepare", "dispatch", "send", "http", "usage", "finish"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("projection order = %v, want %v", got, want)
	}
	if !events[2].At.Equal(start.Add(time.Second)) || *events[4].Usage.OutputTokens != 0 || *events[4].Usage.ReportedAmount != amount {
		t.Fatalf("send timestamp, observed zero or decimal changed: %+v", events)
	}
	if count, err := recovered.drain(ctx); count != 0 || err != nil {
		t.Fatalf("acknowledged events were retained: %d, %v", count, err)
	}
}

func TestAttemptOutboxSerializesProjectorsWithoutBlockingAppends(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "attempts.db")
	entered, release := make(chan struct{}), make(chan struct{})
	first, err := openAttemptOutbox(ctx, path, func(_ context.Context, _ attemptEvent) error {
		close(entered)
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	var handled []string
	second, err := openAttemptOutbox(ctx, path, func(_ context.Context, event attemptEvent) error {
		handled = append(handled, event.ID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := first.MarkDispatched(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := first.drain(ctx); finished <- err }()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first projector never started")
	}
	if err := second.MarkDispatched(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.drain(ctx); err == nil || len(handled) != 0 {
		t.Fatalf("second projector acquired the held lock: %v, events=%v", err, handled)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if _, err := second.drain(ctx); err != nil || !reflect.DeepEqual(handled, []string{"second"}) {
		t.Fatalf("second projector did not resume remaining events: %v, events=%v", err, handled)
	}
}
