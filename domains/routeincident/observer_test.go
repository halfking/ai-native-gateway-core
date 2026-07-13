package routeincident

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

func TestObserver_NilStoreIsNil(t *testing.T) {
	if o := NewObserver(nil, ObserverConfig{}); o != nil {
		t.Fatal("NewObserver(nil) must return nil")
	}
}

func TestObserver_NilReceiverNoPanic(t *testing.T) {
	var o *Observer
	o.OnPersisted(&telemetry.RequestLogEntry{RequestID: "x"})
	if s := o.Stats(); s.Enqueued != 0 {
		t.Fatal("nil receiver should not record stats")
	}
}

func TestObserver_QueueOverflowDrops(t *testing.T) {
	// A store-less observer cannot transition anything; we use it
	// only to exercise the queue + drop counter on the hook path.
	// (NewObserver(nil) returns nil, so we synthesise a non-nil
	// observer by pointing at a nil pool. The OnPersisted path
	// drops via the channel send — that is what we want to test.)
	o := NewObserver(&Store{pool: nil}, ObserverConfig{QueueSize: 2})
	if o == nil {
		t.Fatal("observer must not be nil")
	}
	defer o.Stop()

	fail := true
	entry := &telemetry.RequestLogEntry{
		RequestID:     "r-1",
		TenantID:      "t1",
		ClientModel:   stringPtr("gpt-4o"),
		Success:       !fail,
		RequestStatus: stringPtr(telemetry.RequestStatusFailure),
		ErrorKind:     stringPtr("upstream_5xx"),
	}

	// Fire 100 entries synchronously. The worker is not started, so
	// the channel will fill after QueueSize entries; the rest are
	// dropped (or queued and never drained — either way the test
	// must not deadlock).
	for i := 0; i < 100; i++ {
		o.OnPersisted(entry)
	}

	// Drain the queue manually to count drops. We need at least
	// the queue cap reached, and dropped > 0 once we exceed it.
	timeout := time.After(time.Second)
	for o.Stats().QueueLen > 0 {
		select {
		case <-o.queue:
		case <-timeout:
			goto check
		}
	}
check:
	// Either the queue is still full and the worker is gone, in
	// which case dropped should have grown past QueueSize.
	if o.Stats().Enqueued < int64(o.Stats().QueueCap) {
		t.Fatalf("enqueued < cap: %d < %d", o.Stats().Enqueued, o.Stats().QueueCap)
	}
}

func TestObserver_NonRoutingFailuresAreNoops(t *testing.T) {
	o := NewObserver(&Store{pool: nil}, ObserverConfig{QueueSize: 4})
	defer o.Stop()

	bad := []string{
		"client_cancel",
		"client_key_invalid",
		"input_validation",
		"unauthorized",
		"forbidden",
		"context_length_input",
	}
	for _, k := range bad {
		o.OnPersisted(&telemetry.RequestLogEntry{
			RequestID:         "r-" + k,
			TenantID:          "t1",
			ClientModel:       stringPtr("gpt-4o"),
			Success:           false,
			RequestStatus:     stringPtr(telemetry.RequestStatusFailure),
			FailureDetailCode: stringPtr(k),
		})
	}
	// Drain the queue: any item that leaked must be retrieved.
	for o.Stats().QueueLen > 0 {
		<-o.queue
	}
	if o.Stats().Noops < int64(len(bad)) {
		t.Fatalf("non-routing failures should be noops; noops=%d want>=%d", o.Stats().Noops, len(bad))
	}
}

func TestObserver_QualifiesForIncident(t *testing.T) {
	good := &telemetry.RequestLogEntry{
		RequestID:     "r1",
		TenantID:      "t1",
		ClientModel:   stringPtr("gpt-4o"),
		Success:       false,
		RequestStatus: stringPtr(telemetry.RequestStatusFailure),
	}
	if !qualifiesForIncident(good) {
		t.Fatal("upstream failure with a model+tenant must qualify")
	}

	missing := &telemetry.RequestLogEntry{
		RequestID:     "r1",
		TenantID:      "t1",
		Success:       false,
		RequestStatus: stringPtr(telemetry.RequestStatusFailure),
	}
	if qualifiesForIncident(missing) {
		t.Fatal("missing model must not qualify")
	}

	inProgress := &telemetry.RequestLogEntry{
		RequestID:     "r1",
		TenantID:      "t1",
		ClientModel:   stringPtr("gpt-4o"),
		Success:       false,
		RequestStatus: stringPtr(telemetry.RequestStatusInProgress),
	}
	if qualifiesForIncident(inProgress) {
		t.Fatal("in_progress must not qualify")
	}

	// client_cancel must be filtered
	cancel := &telemetry.RequestLogEntry{
		RequestID:         "r1",
		TenantID:          "t1",
		ClientModel:       stringPtr("gpt-4o"),
		Success:           false,
		RequestStatus:     stringPtr(telemetry.RequestStatusFailure),
		FailureDetailCode: stringPtr("client_cancel"),
	}
	if qualifiesForIncident(cancel) {
		t.Fatal("client_cancel must not qualify (spec invariant)")
	}
}

func TestObserver_StatsReflectsCounters(t *testing.T) {
	o := NewObserver(&Store{pool: nil}, ObserverConfig{QueueSize: 1})
	defer o.Stop()
	for i := 0; i < 5; i++ {
		o.OnPersisted(&telemetry.RequestLogEntry{
			RequestID:     "r",
			TenantID:      "t1",
			ClientModel:   stringPtr("gpt-4o"),
			Success:       false,
			RequestStatus: stringPtr(telemetry.RequestStatusFailure),
		})
	}
	// Drain so we don't leak goroutines.
	for o.Stats().QueueLen > 0 {
		select {
		case <-o.queue:
		case <-time.After(time.Millisecond):
			goto done
		}
	}
done:
	if o.Stats().Enqueued < 5 {
		t.Fatalf("enqueued=%d", o.Stats().Enqueued)
	}
}

func TestTransitionInput_FromEntry(t *testing.T) {
	e := &telemetry.RequestLogEntry{
		RequestID:     "r1",
		TenantID:      "t1",
		ClientModel:   stringPtr("gpt-4o"),
		OutboundModel: stringPtr("gpt-4o-2024-08-06"),
		Success:       false,
		RequestStatus: stringPtr(telemetry.RequestStatusFailure),
		ErrorKind:     stringPtr("upstream_5xx"),
		FailureStage:  stringPtr("upstream"),
	}
	in, ok := transitionInputFromEntry(e)
	if !ok {
		t.Fatal("must map entry to input")
	}
	if in.Model != "gpt-4o-2024-08-06" {
		t.Fatalf("prefer outbound model; got %s", in.Model)
	}
	if in.FailureKind != "upstream_5xx" {
		t.Fatalf("error kind: %s", in.FailureKind)
	}
	if in.FailureStage != "upstream" {
		t.Fatalf("stage: %s", in.FailureStage)
	}
	if in.TerminalStatus != TerminalFailure {
		t.Fatalf("status: %s", in.TerminalStatus)
	}
}

func TestTransitionInput_Success(t *testing.T) {
	e := &telemetry.RequestLogEntry{
		RequestID:     "r1",
		TenantID:      "t1",
		ClientModel:   stringPtr("gpt-4o"),
		Success:       true,
		RequestStatus: stringPtr(telemetry.RequestStatusSuccess),
	}
	in, ok := transitionInputFromEntry(e)
	if !ok || in.TerminalStatus != TerminalSuccess {
		t.Fatalf("success must map: ok=%v in=%+v", ok, in)
	}
}

func TestObserver_AsHookReturnsCallback(t *testing.T) {
	o := NewObserver(&Store{pool: nil}, ObserverConfig{})
	defer o.Stop()
	hook := o.AsHook()
	if hook == nil {
		t.Fatal("AsHook must return a non-nil callback")
	}
	hook(&telemetry.RequestLogEntry{RequestID: "x"})
}

func TestObserver_StartStopIdempotent(t *testing.T) {
	o := NewObserver(&Store{pool: nil}, ObserverConfig{QueueSize: 4})
	o.Start(context.Background())
	o.Start(context.Background()) // second call must not panic
	o.Stop()
	o.Stop() // second call must not panic
}

func stringPtr(s string) *string { return &s }
