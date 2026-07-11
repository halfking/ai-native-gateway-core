package client

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/memory"
)

func TestSinkPauseCountsDroppedWrites(t *testing.T) {
	sink := NewSink(NewClient(ClientConfig{BaseURL: "http://memora.example"}), 1, 1)
	sink.started.Store(true)
	sink.Pause()
	sink.Enqueue(memory.WriteOp{UserID: "tenant:k:1:task"})

	if got := sink.Stats().Dropped; got != 1 {
		t.Fatalf("dropped = %d, want 1", got)
	}
}

func TestSinkStartIsIdempotent(t *testing.T) {
	sink := NewSink(NewClient(ClientConfig{BaseURL: "http://memora.example"}), 1, 1)
	sink.Start()
	sink.Start()

	if !sink.started.Load() {
		t.Fatal("sink did not start")
	}
	sink.Stop(context.Background())
}
