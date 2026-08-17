package requestjourney

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeJourneyWriter struct {
	started chan JourneyEvent
	release chan struct{}

	mu     sync.Mutex
	events []JourneyEvent
	err    error
}

func (f *fakeJourneyWriter) Apply(ctx context.Context, event JourneyEvent) error {
	if f.started != nil {
		select {
		case f.started <- event:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	f.events = append(f.events, event)
	f.mu.Unlock()
	return f.err
}

func (f *fakeJourneyWriter) snapshot() []JourneyEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]JourneyEvent(nil), f.events...)
}

func TestLifecycleUnboundTerminalOnlyUpdatesGlobalIngress(t *testing.T) {
	memory := NewProjection(DefaultConfig())
	recorder := NewRecorder(memory, nil, nil)
	t.Cleanup(func() { closeRecorder(t, recorder) })
	arrivedAt := time.Unix(1700000000, 0).UTC()
	lifecycle := NewIngressLifecycle(recorder, "gateway-1", "request-unbound", IngressProtocolMessages, IngressPathMessages, arrivedAt)

	lifecycle.Finish(context.Background(), OutcomeFailure, "missing_key", 401)

	if _, err := memory.Detail("default", "request-unbound"); !errors.Is(err, ErrJourneyNotFound) {
		t.Fatalf("unbound request entered default tenant: %v", err)
	}
	ingress := memory.RecentIngress()
	if len(ingress) != 1 || ingress[0].Status != IngressStatusFailed || ingress[0].ErrorKind != "missing_key" {
		t.Fatalf("ingress = %#v", ingress)
	}
}

func TestLifecycleTrustedBindingPreservesIngressArrivalForTenantJourney(t *testing.T) {
	memory := NewProjection(DefaultConfig())
	recorder := NewRecorder(memory, nil, nil)
	t.Cleanup(func() { closeRecorder(t, recorder) })
	arrivedAt := time.Unix(1700000000, 123).UTC()
	lifecycle := NewIngressLifecycle(recorder, "gateway-1", "request-bound", IngressProtocolResponses, IngressPathResponses, arrivedAt)

	lifecycle.BindTenant(context.Background(), "tenant-a", "auto")
	lifecycle.Finish(context.Background(), OutcomeSuccess, "", 200)

	journey, err := memory.Detail("tenant-a", "request-bound")
	if err != nil {
		t.Fatal(err)
	}
	if len(journey.Events) != 2 || !journey.Events[0].OccurredAt.Equal(arrivedAt) {
		t.Fatalf("journey events = %#v", journey.Events)
	}
	if _, err := memory.Detail("default", "request-bound"); !errors.Is(err, ErrJourneyNotFound) {
		t.Fatalf("bound request leaked into default: %v", err)
	}
}

func TestRecorderApplyDoesNotWaitForExternalStore(t *testing.T) {
	memory := NewProjection(DefaultConfig())
	store := &fakeJourneyWriter{started: make(chan JourneyEvent, 1), release: make(chan struct{})}
	recorder := newRecorder(memory, store, nil, recorderOptions{queueCapacity: 4, writeTimeout: time.Second})
	t.Cleanup(func() {
		close(store.release)
		closeRecorder(t, recorder)
	})

	returned := make(chan error, 1)
	go func() {
		returned <- recorder.Apply(context.Background(), testJourneyEvent("tenant-a", "request-1", 1))
	}()

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Apply() error = %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Apply blocked on external store")
	}

	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start external write")
	}
	if got := memory.RecentTotal("tenant-a"); len(got) != 1 {
		t.Fatalf("memory projection size = %d", len(got))
	}
}

func TestRecorderPreservesEventOrderForBothStores(t *testing.T) {
	redisStore := &fakeJourneyWriter{}
	pgStore := &fakeJourneyWriter{}
	recorder := newRecorder(NewProjection(DefaultConfig()), redisStore, pgStore, recorderOptions{queueCapacity: 8})

	for seq := int64(1); seq <= 3; seq++ {
		event := testJourneyEvent("tenant-a", "request-1", seq)
		event.OccurredAt = event.OccurredAt.Add(time.Duration(seq) * time.Second)
		if err := recorder.Apply(context.Background(), event); err != nil {
			t.Fatalf("Apply(seq=%d) error = %v", seq, err)
		}
	}
	closeRecorder(t, recorder)

	for name, events := range map[string][]JourneyEvent{"redis": redisStore.snapshot(), "postgres": pgStore.snapshot()} {
		if len(events) != 3 {
			t.Fatalf("%s event count = %d", name, len(events))
		}
		for i, event := range events {
			if event.Seq != int64(i+1) {
				t.Fatalf("%s event[%d].Seq = %d", name, i, event.Seq)
			}
		}
	}
}

func TestRecorderApplyReturnsMemorySequenceConflictImmediately(t *testing.T) {
	recorder := newRecorder(NewProjection(DefaultConfig()), nil, nil, recorderOptions{})
	t.Cleanup(func() { closeRecorder(t, recorder) })
	first := testJourneyEvent("tenant-a", "request-1", 1)
	if err := recorder.Apply(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	conflict := first
	conflict.Stage = StageRouting
	if err := recorder.Apply(context.Background(), conflict); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("Apply() conflict error = %v", err)
	}
}

func TestRecorderEmitAfterCloseUpdatesMemoryAsDegraded(t *testing.T) {
	memory := NewProjection(DefaultConfig())
	recorder := newRecorder(memory, &fakeJourneyWriter{}, nil, recorderOptions{queueCapacity: 1})
	closeRecorder(t, recorder)
	var reported atomic.Int64
	recorder.SetErrorHandler(func(err error) {
		if errors.Is(err, ErrRecorderClosed) {
			reported.Add(1)
		}
	})

	recorder.EmitJourneyEvent(context.Background(), testJourneyEvent("tenant-a", "request-1", 1))
	journey, err := memory.Detail("tenant-a", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if journey.ObservationStatus != ObservationDegraded || reported.Load() != 1 {
		t.Fatalf("status/reports = %q/%d", journey.ObservationStatus, reported.Load())
	}
}

func TestRecorderQueueOverflowReportsDropAndMarksMemoryDegraded(t *testing.T) {
	store := &fakeJourneyWriter{started: make(chan JourneyEvent, 1), release: make(chan struct{})}
	memory := NewProjection(DefaultConfig())
	recorder := newRecorder(memory, store, nil, recorderOptions{queueCapacity: 1, writeTimeout: time.Second})
	defer func() {
		close(store.release)
		closeRecorder(t, recorder)
	}()

	var reported atomic.Int64
	recorder.SetErrorHandler(func(err error) {
		if errors.Is(err, ErrRecorderQueueFull) {
			reported.Add(1)
		}
	})

	first := testJourneyEvent("tenant-a", "request-1", 1)
	if err := recorder.Apply(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not block in store")
	}
	for seq := int64(2); seq <= 3; seq++ {
		event := testJourneyEvent("tenant-a", "request-1", seq)
		event.OccurredAt = event.OccurredAt.Add(time.Duration(seq) * time.Second)
		if err := recorder.Apply(context.Background(), event); err != nil {
			t.Fatalf("Apply(seq=%d) error = %v", seq, err)
		}
	}

	if reported.Load() != 1 {
		t.Fatalf("queue full reports = %d", reported.Load())
	}
	journey, err := memory.Detail("tenant-a", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if journey.ObservationStatus != ObservationDegraded || len(journey.Events) != 3 {
		t.Fatalf("journey status/events = %q/%d", journey.ObservationStatus, len(journey.Events))
	}
}

func TestRecorderExternalFailureDoesNotPreventOtherStore(t *testing.T) {
	pgStore := &fakeJourneyWriter{err: errors.New("postgres unavailable")}
	redisStore := &fakeJourneyWriter{}
	memory := NewProjection(DefaultConfig())
	recorder := newRecorder(memory, redisStore, pgStore, recorderOptions{queueCapacity: 2})
	var reported atomic.Int64
	recorder.SetErrorHandler(func(error) { reported.Add(1) })

	if err := recorder.Apply(context.Background(), testJourneyEvent("tenant-a", "request-1", 1)); err != nil {
		t.Fatal(err)
	}
	closeRecorder(t, recorder)

	if len(redisStore.snapshot()) != 1 {
		t.Fatalf("redis writes = %d", len(redisStore.snapshot()))
	}
	if reported.Load() != 1 {
		t.Fatalf("reported errors = %d", reported.Load())
	}
	journey, err := memory.Detail("tenant-a", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if journey.ObservationStatus != ObservationDegraded {
		t.Fatalf("observation status = %q", journey.ObservationStatus)
	}
}

func TestRecorderUsesDetachedWriteContextWithTimeout(t *testing.T) {
	store := &fakeJourneyWriter{started: make(chan JourneyEvent, 1), release: make(chan struct{})}
	recorder := newRecorder(NewProjection(DefaultConfig()), store, nil, recorderOptions{
		queueCapacity: 2,
		writeTimeout:  20 * time.Millisecond,
	})
	var reported atomic.Int64
	recorder.SetErrorHandler(func(err error) {
		if errors.Is(err, context.DeadlineExceeded) {
			reported.Add(1)
		}
	})

	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := recorder.Apply(requestCtx, testJourneyEvent("tenant-a", "request-1", 1)); err != nil {
		t.Fatal(err)
	}
	closeRecorder(t, recorder)

	if reported.Load() != 1 {
		t.Fatalf("deadline errors = %d", reported.Load())
	}
}

func TestRecorderCloseDrainsQueuedEvents(t *testing.T) {
	store := &fakeJourneyWriter{}
	recorder := newRecorder(NewProjection(DefaultConfig()), store, nil, recorderOptions{queueCapacity: 8})
	for i := int64(1); i <= 8; i++ {
		event := testJourneyEvent("tenant-a", "request-1", i)
		event.OccurredAt = event.OccurredAt.Add(time.Duration(i) * time.Second)
		if err := recorder.Apply(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	closeRecorder(t, recorder)
	if got := len(store.snapshot()); got != 8 {
		t.Fatalf("drained writes = %d", got)
	}
}

func TestRecorderCloseReturnsWhenContextExpires(t *testing.T) {
	store := &fakeJourneyWriter{started: make(chan JourneyEvent, 1), release: make(chan struct{})}
	recorder := newRecorder(NewProjection(DefaultConfig()), store, nil, recorderOptions{
		queueCapacity: 1,
		writeTimeout:  time.Second,
	})
	if err := recorder.Apply(context.Background(), testJourneyEvent("tenant-a", "request-1", 1)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := recorder.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v", err)
	}
	close(store.release)
	closeRecorder(t, recorder)
}

func TestRecorderEmitAndSetHandlerCanRaceWithClose(t *testing.T) {
	store := &fakeJourneyWriter{}
	recorder := newRecorder(NewProjection(DefaultConfig()), store, nil, recorderOptions{queueCapacity: 8})
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				event := testJourneyEvent("tenant-a", "request-race", int64(worker*100+i+1))
				event.OccurredAt = event.OccurredAt.Add(time.Duration(worker*100+i) * time.Millisecond)
				recorder.EmitJourneyEvent(context.Background(), event)
				recorder.SetErrorHandler(func(error) {})
			}
		}(worker)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = recorder.Close(ctx)
	}()
	wg.Wait()
	closeRecorder(t, recorder)
}

func TestRecorderWithNilStoresStillProjectsAndImplementsEventSinkShape(t *testing.T) {
	memory := NewProjection(DefaultConfig())
	recorder := NewRecorder(memory, nil, nil)
	t.Cleanup(func() { closeRecorder(t, recorder) })
	var sink interface {
		EmitJourneyEvent(context.Context, JourneyEvent)
	} = recorder

	sink.EmitJourneyEvent(context.Background(), testJourneyEvent("tenant-a", "request-1", 1))
	if got := memory.RecentTotal("tenant-a"); len(got) != 1 || got[0].RequestID != "request-1" {
		t.Fatalf("projected requests = %#v", got)
	}
}

func TestRecorderReportsValidationErrorsWithoutPanicking(t *testing.T) {
	recorder := NewRecorder(NewProjection(DefaultConfig()), nil, nil)
	t.Cleanup(func() { closeRecorder(t, recorder) })
	var reported error
	recorder.SetErrorHandler(func(err error) { reported = err })
	recorder.EmitJourneyEvent(context.Background(), JourneyEvent{})
	if reported == nil {
		t.Fatal("validation error was not reported")
	}
	if errors.Is(reported, ErrSequenceConflict) {
		t.Fatalf("unexpected error = %v", reported)
	}
}

func closeRecorder(t *testing.T, recorder *Recorder) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := recorder.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
