package store

import (
	"context"
	"testing"
)

func TestPipelineBatchReadMissing(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	views, err := s.PipelineNodeViews(ctx, "ursm:v2:", []NodeQuery{
		{CredentialID: 1, RawModel: "gpt"},
		{CredentialID: 2, RawModel: "gpt"},
	})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("expected 2 views, got %d", len(views))
	}
	if views[0].Available {
		t.Fatalf("missing node must default to unavailable")
	}
}
