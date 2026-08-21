package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSourcePriorityOrdering(t *testing.T) {
	if SourcePriorityRequest >= SourcePriorityProbe {
		t.Fatalf("probe must dominate request")
	}
	if SourcePriorityProbe >= SourcePriorityAdmin {
		t.Fatalf("admin must dominate probe")
	}
}

func TestNodeViewEmptyResponseFieldsOmitZeroValues(t *testing.T) {
	encoded, err := json.Marshal(NodeView{})
	if err != nil {
		t.Fatalf("Marshal(NodeView{}) = %v", err)
	}
	for _, field := range []string{
		"empty_responses_1m", "empty_responses_5m", "empty_responses_30m",
		"empty_response_rate_1m", "empty_response_rate_5m", "empty_response_rate_30m",
	} {
		if strings.Contains(string(encoded), field) {
			t.Fatalf("zero-value NodeView unexpectedly exported %q: %s", field, encoded)
		}
	}
}
