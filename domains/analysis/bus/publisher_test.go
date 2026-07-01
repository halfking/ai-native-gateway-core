package bus

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

func TestNoopPublisher_PublishIsNil(t *testing.T) {
	p := NoopPublisher{}
	err := p.Publish(context.Background(), analysis.AnalysisEvent{EventID: "x"})
	if err != nil {
		t.Fatalf("NoopPublisher.Publish should return nil, got %v", err)
	}
}

func TestNoopPublisher_CloseIsNil(t *testing.T) {
	if err := (NoopPublisher{}).Close(); err != nil {
		t.Fatalf("NoopPublisher.Close should return nil, got %v", err)
	}
}

func TestPublisher_NilSafePublish(t *testing.T) {
	// *PGPublisher(nil).Publish should not panic.
	defer func() {
		if recover() != nil {
			t.Fatal("nil PGPublisher.Publish panicked")
		}
	}()
	var p *PGPublisher
	_ = p.Publish(context.Background(), analysis.AnalysisEvent{})
}
