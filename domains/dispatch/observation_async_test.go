package dispatch

import (
	"context"
	"testing"
	"time"
)

type blockingJourneySink struct {
	entered chan struct{}
	release chan struct{}
}

func (s *blockingJourneySink) ObserveDispatch(context.Context, Observation) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
}

func TestBlockingJourneySinkDoesNotBlockDispatchExecution(t *testing.T) {
	sink := &blockingJourneySink{entered: make(chan struct{}, 1), release: make(chan struct{})}
	cred := CredentialRef{CredentialID: 1, ProviderID: 1, ConcurrencyMode: ModeDisabled}
	pipeline := NewPipeline(Deps{
		ObservationSink: sink,
		ModelResolveFunc: func(context.Context, string, []string) (string, []string, error) {
			return "model-a", nil, nil
		},
		RouteFunc: func(context.Context, *QueuedRequest) ([]CredentialRef, error) {
			return []CredentialRef{cred}, nil
		},
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			return ForwardOutcome{Result: "ok"}
		},
	})
	pipeline.Start()
	defer func() {
		close(sink.release)
		pipeline.Stop()
	}()

	qr := NewQueuedRequest("request-nonblocking-observation", "tenant-a", "model-a", context.Background(), nil)
	qr.GatewayInstanceID = "gateway-test"
	done := make(chan error, 1)
	go func() {
		_, err := pipeline.Submit(context.Background(), qr)
		done <- err
	}()

	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("journey sink was not invoked")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("blocking journey sink backpressured dispatch execution")
	}
}
