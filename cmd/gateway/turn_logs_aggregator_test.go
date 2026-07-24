package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAggregateSessionLogs_BuildsJSON(t *testing.T) {
	logs := []StageLog{
		{Stage: "routing", Status: "success", LatencyMs: 10, StartedAt: time.Now()},
		{Stage: "llm_call", Status: "success", LatencyMs: 200, StartedAt: time.Now()},
	}
	summary, err := aggregate(logs)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if summary == nil {
		t.Fatal("nil summary")
	}
	if got, want := len(summary.Stages), 2; got != want {
		t.Fatalf("expected %d stages, got %d", want, got)
	}
	b, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"stages"`) {
		t.Fatalf("missing stages in JSON: %s", string(b))
	}
}

func TestAggregateSessionLogs_NilReturnsNil(t *testing.T) {
	s, err := aggregate(nil)
	if err != nil {
		t.Fatalf("aggregate nil: %v", err)
	}
	if s != nil {
		t.Fatalf("expected nil summary, got %+v", s)
	}
}

func TestAggregateSessionLogs_TimestampIsSet(t *testing.T) {
	before := time.Now()
	s, err := aggregate([]StageLog{{Stage: "x", Status: "success", LatencyMs: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if s.BuiltAt.Before(before) {
		t.Fatalf("BuiltAt %v before start %v", s.BuiltAt, before)
	}
}