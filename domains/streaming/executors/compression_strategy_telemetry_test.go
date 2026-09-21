package executors

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression/strategy"
)

func TestMarshalRunnerStatsIsMetadataOnly(t *testing.T) {
	meta := marshalRunnerStats("parallel", compression.ModeAutoThreshold, strategy.RunStats{
		CandidateCount: 3,
		WinnerName:     "mechanical_trim",
		AppliedNames:   []string{"mechanical_trim"},
		SkippedNames:   []string{"llm_summary"},
		FailedNames:    []string{"broken"},
		TruncatedBy:    []string{"expanding"},
	}, "applied")
	var got map[string]any
	if err := json.Unmarshal(meta, &got); err != nil {
		t.Fatal(err)
	}
	if got["runner_mode"] != "parallel" || got["runner_outcome"] != "applied" || got["candidate_count"] != float64(3) || got["winner"] != "mechanical_trim" {
		t.Fatalf("runner metadata = %#v", got)
	}
	if _, ok := got["body"]; ok {
		t.Fatal("runner metadata must not contain request body")
	}
}
