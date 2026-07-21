package store

import (
	"context"
	"testing"
)

func TestApplyDecisionStaleIgnored(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.HSetFields(ctx, "ursm:v2:node:9:m", map[string]any{
		"generation": 5, "source_priority": 40, "available": 0, "fail_streak": 9, "last_err": "x",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := s.ApplyDecision(ctx, "ursm:v2:node:9:m", 4, 40, true, 0, "retry", false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Status != "ignored_stale" {
		t.Fatalf("status=%s", res.Status)
	}
}
