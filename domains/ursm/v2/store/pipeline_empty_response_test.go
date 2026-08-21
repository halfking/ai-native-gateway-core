package store

import (
	"context"
	"testing"
)

func TestPipelineNodeViewsSurfacesEmptyResponseWindows(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()

	ctx := context.Background()
	key := NodeKeyForTenant("ursm:v2:", "tenant-a", 77, "vendor-model")
	if err := s.HSetFields(ctx, key, map[string]any{
		"available":               "1",
		"samples_1m":              "3",
		"samples_5m":              "20",
		"samples_30m":             "40",
		"empty_responses_1m":      "1",
		"empty_responses_5m":      "8",
		"empty_responses_30m":     "10",
		"empty_response_rate_1m":  "0.3333333333333333",
		"empty_response_rate_5m":  "0.4",
		"empty_response_rate_30m": "0.25",
	}); err != nil {
		t.Fatalf("seed node: %v", err)
	}

	views, err := s.PipelineNodeViews(ctx, "ursm:v2:", []NodeQuery{{
		TenantID: "tenant-a", CredentialID: 77, RawModel: "vendor-model",
	}})
	if err != nil {
		t.Fatalf("PipelineNodeViews() = %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("views=%d, want 1", len(views))
	}
	view := views[0]
	if view.EmptyResponses5m != 8 || view.EmptyResponseRate5m != 0.4 || view.Samples5m != 20 {
		t.Fatalf("empty-response fields lost in read path: %+v", view)
	}
}
