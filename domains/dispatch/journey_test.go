package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type journeyRecorder struct {
	mu     sync.Mutex
	events []Observation
	errs   []error
}

func (r *journeyRecorder) ObserveDispatch(_ context.Context, observation Observation) {
	event := observation
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := event.Validate(); err != nil {
		r.errs = append(r.errs, err)
	}
	r.events = append(r.events, event)
}

func (r *journeyRecorder) snapshot(t *testing.T) []Observation {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.errs) > 0 {
		t.Fatalf("invalid journey events: %v", r.errs)
	}
	return append([]Observation(nil), r.events...)
}

func (r *journeyRecorder) waitSnapshot(t *testing.T, minimum int) []Observation {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		events := r.snapshot(t)
		if len(events) >= minimum {
			return events
		}
		time.Sleep(time.Millisecond)
	}
	return r.snapshot(t)
}

func journeyCredential(id, provider int, vendor string) CredentialRef {
	return CredentialRef{
		CredentialID:     id,
		ProviderID:       provider,
		Vendor:           vendor,
		ConcurrencyMode:  ModeConcurrency,
		ConcurrencyLimit: 4,
	}
}

func journeyPipeline(t *testing.T, sink ObservationSink, refs map[string][]CredentialRef, forward ForwardFunc, allowModelChange bool) *Pipeline {
	t.Helper()
	p := NewPipeline(Deps{
		ObservationSink: sink,
		RouteFunc: func(_ context.Context, qr *QueuedRequest) ([]CredentialRef, error) {
			candidates := refs[qr.ResolvedModel]
			out := make([]CredentialRef, 0, len(candidates))
			for _, candidate := range candidates {
				if !qr.HasTriedCredential(candidate.CredentialID) {
					out = append(out, candidate)
				}
			}
			return out, nil
		},
		ModelResolveFunc: func(_ context.Context, requested string, tried []string) (string, []string, error) {
			if requested == "" || isAutoModel(requested) {
				return "", nil, errors.New("test requires concrete model")
			}
			alternatives := make([]string, 0, len(refs))
			for model := range refs {
				if model != requested && !contains(tried, model) {
					alternatives = append(alternatives, model)
				}
			}
			return requested, alternatives, nil
		},
		ForwardFunc:      forward,
		AllowModelChange: allowModelChange,
	})
	p.Start()
	t.Cleanup(p.Stop)
	return p
}

func newJourneyRequest(id, model string) *QueuedRequest {
	qr := NewQueuedRequest(id, "tenant-journey", model, context.Background(), nil)
	qr.GatewayInstanceID = "gateway-test"
	return qr
}

func eventTypes(events []Observation) []ObservationType {
	out := make([]ObservationType, len(events))
	for i := range events {
		out[i] = events[i].Type
	}
	return out
}

func assertEventTypes(t *testing.T, events []Observation, want []ObservationType) {
	t.Helper()
	got := eventTypes(events)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
}

func assertAttemptSequence(t *testing.T, events []Observation, firstSeq int64) {
	t.Helper()
	attemptIDs := map[int]string{}
	for i, event := range events {
		if event.Seq != firstSeq+int64(i) {
			t.Fatalf("event %d seq = %d, want %d", i, event.Seq, firstSeq+int64(i))
		}
		if event.Attempt == nil {
			continue
		}
		if previous := attemptIDs[event.Attempt.AttemptNo]; previous != "" && previous != event.Attempt.AttemptID {
			t.Fatalf("attempt %d changed ID from %q to %q", event.Attempt.AttemptNo, previous, event.Attempt.AttemptID)
		}
		attemptIDs[event.Attempt.AttemptNo] = event.Attempt.AttemptID
	}
	for attemptNo := 1; attemptNo <= len(attemptIDs); attemptNo++ {
		if attemptIDs[attemptNo] == "" {
			t.Fatalf("missing attempt number %d in %v", attemptNo, attemptIDs)
		}
	}
}

func TestJourneySameNodeRetryFailureThenSuccess(t *testing.T) {
	recorder := &journeyRecorder{}
	ref := journeyCredential(11, 101, "vendor-a")
	var calls atomic.Int32
	p := journeyPipeline(t, recorder, map[string][]CredentialRef{"m1": {ref}}, func(_ context.Context, qr *QueuedRequest, _ CredentialRef) ForwardOutcome {
		if calls.Add(1) == 1 {
			return ForwardOutcome{Err: errors.New("temporary upstream failure"), ErrorKind: "upstream_503", HTTPStatus: 503}
		}
		qr.MarkFirstSemanticByte()
		return ForwardOutcome{Result: "ok"}
	}, false)

	qr := newJourneyRequest("journey-retry", "m1")
	qr.JourneySeq.Store(7)
	qr.RetryPerCredential = 1
	result, err := p.Submit(context.Background(), qr)
	if err != nil || result != "ok" {
		t.Fatalf("Submit() = (%v, %v), want (ok, nil)", result, err)
	}

	events := recorder.waitSnapshot(t, 13)
	assertEventTypes(t, events, []ObservationType{
		ObservationModelEnqueued,
		ObservationCredentialSelected,
		ObservationNodeEnqueued,
		ObservationNodeSelected,
		ObservationAttemptStarted,
		ObservationAttemptFailed,
		ObservationRetryScheduled,
		ObservationNodeEnqueued,
		ObservationNodeSelected,
		ObservationAttemptStarted,
		ObservationFirstByte,
		ObservationAttemptSucceeded,
		ObservationRequestSucceeded,
	})
	assertAttemptSequence(t, events, 8)
	if events[5].ErrorKind != "upstream_503" || events[5].HTTPStatus != 503 {
		t.Fatalf("failed attempt diagnostics = %+v", events[5])
	}
	if events[6].RetryReason != "upstream_503" {
		t.Fatalf("retry reason = %q, want upstream_503", events[6].RetryReason)
	}

	snapshot := p.SnapshotWaterfall(1, "", 0)
	if len(snapshot.Requests) != 1 || len(snapshot.Requests[0].Attempts) != 2 {
		t.Fatalf("waterfall attempts = %+v", snapshot.Requests)
	}
	attempts := snapshot.Requests[0].Attempts
	if attempts[0].AttemptNo != 1 || attempts[0].CredentialID != 11 || attempts[0].Outcome != "failure" || attempts[0].ErrorKind != "upstream_503" {
		t.Fatalf("first waterfall attempt = %+v", attempts[0])
	}
	if attempts[0].FirstByteAt != "" {
		t.Fatalf("failed pre-byte attempt fabricated first byte: %+v", attempts[0])
	}
	if attempts[1].AttemptNo != 2 || attempts[1].Outcome != "success" || attempts[1].FirstByteAt == "" || attempts[1].EndedAt == "" {
		t.Fatalf("second waterfall attempt = %+v", attempts[1])
	}
	if attempts[0].AttemptID == attempts[1].AttemptID {
		t.Fatal("retry reused attempt ID")
	}
}

func TestJourneyDoesNotInferOrMisattributeFirstByte(t *testing.T) {
	recorder := &journeyRecorder{}
	ref := journeyCredential(11, 101, "vendor-a")
	var calls int
	var staleCallback func()
	p := journeyPipeline(t, recorder, map[string][]CredentialRef{"m1": {ref}}, func(_ context.Context, qr *QueuedRequest, _ CredentialRef) ForwardOutcome {
		calls++
		if calls == 1 {
			staleCallback = qr.FirstSemanticByteCallback()
			return ForwardOutcome{Err: errors.New("retry")}
		}
		staleCallback()
		return ForwardOutcome{Result: "ok"}
	}, false)
	qr := newJourneyRequest("journey-no-fabricated-byte", "m1")
	qr.RetryPerCredential = 1
	if _, err := p.Submit(context.Background(), qr); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	for _, event := range recorder.waitSnapshot(t, 10) {
		if event.Type == ObservationFirstByte {
			t.Fatalf("unexpected first_byte from outcome or stale callback: %+v", event)
		}
	}
	attempts := p.SnapshotWaterfall(1, "", 0).Requests[0].Attempts
	if len(attempts) != 2 || attempts[0].FirstByteAt != "" || attempts[1].FirstByteAt != "" {
		t.Fatalf("fabricated waterfall first byte: %+v", attempts)
	}
}

func TestJourneyNodeAndModelSwitches(t *testing.T) {
	t.Run("node switch", func(t *testing.T) {
		recorder := &journeyRecorder{}
		refs := map[string][]CredentialRef{"m1": {
			journeyCredential(11, 101, "vendor-a"),
			journeyCredential(12, 101, "vendor-a"),
		}}
		p := journeyPipeline(t, recorder, refs, func(_ context.Context, _ *QueuedRequest, cred CredentialRef) ForwardOutcome {
			if cred.CredentialID == 11 {
				return ForwardOutcome{Err: errors.New("node unavailable"), ErrorKind: "node_unavailable"}
			}
			return ForwardOutcome{Result: "ok"}
		}, false)
		qr := newJourneyRequest("journey-node-switch", "m1")
		qr.RetryPerCredential = 0
		if _, err := p.Submit(context.Background(), qr); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		events := recorder.waitSnapshot(t, 12)
		assertAttemptSequence(t, events, 1)
		var switched *Observation
		for i := range events {
			if events[i].Type == ObservationNodeSwitched {
				switched = &events[i]
			}
		}
		if switched == nil || switched.FromCredentialID != 11 || switched.ToCredentialID != 12 || switched.SwitchReason != "cred_switch" {
			t.Fatalf("node switch event = %+v", switched)
		}
	})

	t.Run("model switch", func(t *testing.T) {
		recorder := &journeyRecorder{}
		refs := map[string][]CredentialRef{
			"m1": {journeyCredential(11, 101, "vendor-a")},
			"m2": {journeyCredential(21, 201, "vendor-b")},
		}
		p := journeyPipeline(t, recorder, refs, func(_ context.Context, _ *QueuedRequest, cred CredentialRef) ForwardOutcome {
			if cred.CredentialID == 11 {
				return ForwardOutcome{Err: errors.New("model exhausted"), ErrorKind: "capacity"}
			}
			return ForwardOutcome{Result: "ok"}
		}, true)
		qr := newJourneyRequest("journey-model-switch", "m1")
		qr.RetryPerCredential = 0
		qr.AllowModelChange = true
		qr.ModelAlternatives = []string{"m2"}
		if _, err := p.Submit(context.Background(), qr); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		events := recorder.waitSnapshot(t, 12)
		assertAttemptSequence(t, events, 1)
		var switched *Observation
		for i := range events {
			if events[i].Type == ObservationModelSwitched {
				switched = &events[i]
			}
		}
		if switched == nil || switched.FromModel != "m1" || switched.ToModel != "m2" || switched.SwitchReason != "no_node" {
			t.Fatalf("model switch event = %+v", switched)
		}
		attempts := p.SnapshotWaterfall(1, "", 0).Requests[0].Attempts
		if len(attempts) != 2 || attempts[0].Model != "m1" || attempts[1].Model != "m2" {
			t.Fatalf("model attempt waterfall = %+v", attempts)
		}
	})
}

func TestJourneyTerminalFailurePreservesDiagnostics(t *testing.T) {
	recorder := &journeyRecorder{}
	ref := journeyCredential(11, 101, "vendor-a")
	p := journeyPipeline(t, recorder, map[string][]CredentialRef{"m1": {ref}}, func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
		return ForwardOutcome{Err: errors.New("quota exhausted"), ErrorKind: "quota_exhausted", HTTPStatus: 429}
	}, false)
	qr := newJourneyRequest("journey-failed", "m1")
	qr.RetryPerCredential = 0
	if _, err := p.Submit(context.Background(), qr); err == nil {
		t.Fatal("Submit succeeded, want terminal failure")
	}
	events := recorder.waitSnapshot(t, 7)
	last := events[len(events)-1]
	if last.Type != ObservationRequestFailed || last.Outcome != OutcomeFailure || last.ErrorKind != "quota_exhausted" || last.HTTPStatus != 429 {
		t.Fatalf("terminal event = %+v", last)
	}
}

func TestJourneyShutdownAndNilSinkAreSafe(t *testing.T) {
	recorder := &journeyRecorder{}
	p := NewPipeline(Deps{ObservationSink: recorder})
	p.Start()
	p.Stop()
	qr := newJourneyRequest("journey-shutdown", "m1")
	if _, err := p.Submit(context.Background(), qr); !errors.Is(err, ErrShutdown) {
		t.Fatalf("Submit after Stop = %v, want ErrShutdown", err)
	}
	events := recorder.snapshot(t)
	if len(events) != 0 {
		t.Fatalf("shutdown must not synchronously invoke a closed observation sink: %+v", events)
	}

	p.SetObservationSink(nil)
	qr2 := newJourneyRequest("journey-nil-sink", "m1")
	if _, err := p.Submit(context.Background(), qr2); !errors.Is(err, ErrShutdown) {
		t.Fatalf("Submit with nil sink = %v, want ErrShutdown", err)
	}
}

func TestActiveAttemptRefReturnsStableSnapshot(t *testing.T) {
	qr := newJourneyRequest("journey-active-attempt", "m1")
	reserved := qr.reserveAttempt(journeyCredential(11, 101, "vendor-a"))
	if _, ok := qr.commitReservedAttempt(reserved.AttemptID); !ok {
		t.Fatal("commitReservedAttempt failed")
	}
	if _, _, ok := qr.startAllocatedAttempt(); !ok {
		t.Fatal("startAllocatedAttempt failed")
	}

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			active, ok := qr.ActiveAttemptRef()
			if !ok {
				t.Error("ActiveAttemptRef reported no active attempt")
				return
			}
			if active != reserved {
				t.Errorf("ActiveAttemptRef = %+v, want %+v", active, reserved)
			}
		}()
	}
	wg.Wait()

	if _, _, _, _, ok := qr.finishAttempt(reserved.AttemptID, ForwardOutcome{Result: "ok"}); !ok {
		t.Fatal("finishAttempt failed")
	}
	if _, ok := qr.ActiveAttemptRef(); ok {
		t.Fatal("finished attempt remained active")
	}
}

func TestJourneyConcurrentFirstByteIsEmittedOnce(t *testing.T) {
	recorder := &journeyRecorder{}
	ref := journeyCredential(11, 101, "vendor-a")
	p := journeyPipeline(t, recorder, map[string][]CredentialRef{"m1": {ref}}, func(_ context.Context, qr *QueuedRequest, _ CredentialRef) ForwardOutcome {
		markFirstByte := qr.FirstSemanticByteCallback()
		var wg sync.WaitGroup
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				markFirstByte()
			}()
		}
		wg.Wait()
		return ForwardOutcome{Result: "ok"}
	}, false)
	qr := newJourneyRequest("journey-first-byte-race", "m1")
	if _, err := p.Submit(context.Background(), qr); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	events := recorder.waitSnapshot(t, 8)
	firstBytes := 0
	for _, event := range events {
		if event.Type == ObservationFirstByte {
			firstBytes++
		}
	}
	if firstBytes != 1 {
		t.Fatalf("first_byte events = %d, want 1: %v", firstBytes, eventTypes(events))
	}
	assertAttemptSequence(t, events, 1)
}
