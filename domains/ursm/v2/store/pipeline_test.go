package store

import (
	"context"
	"testing"
)

func TestPipelineDecodesScoringFields(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	mr.HSet("ursm:v2:node:tenant-a:9:model", "available", "1")
	mr.HSet("ursm:v2:node:tenant-a:9:model", "lat_ewma_ms", "123")
	mr.HSet("ursm:v2:node:tenant-a:9:model", "sr_5m", "0.91")
	mr.HSet("ursm:v2:node:tenant-a:9:model", "samples_5m", "17")
	views, err := s.PipelineNodeViews(ctx, "ursm:v2:", []NodeQuery{{
		TenantID: "tenant-a", CredentialID: 9, RawModel: "model",
	}})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if len(views) != 1 || views[0].LatEWMA != 123 || views[0].SR5m != 0.91 || views[0].Samples5m != 17 {
		t.Fatalf("scoring fields were not decoded: %+v", views)
	}
}

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
