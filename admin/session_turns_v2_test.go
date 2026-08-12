package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildCompressionDiagnosticsV2_SeparatesOutboundSummary(t *testing.T) {
	strategy := "llm_summary"
	tokensSaved := 128
	outbound := []byte(`{"messages":[{"role":"assistant","content":"[smm_v1:abc123]\ninternal summary"}]}`)
	meta := []byte(`{"summary_marker":"[smm_v1:abc123]"}`)

	diagnostics := buildCompressionDiagnosticsV2(
		"req-1", true, &strategy, &tokensSaved, meta, outbound,
	)
	if diagnostics == nil {
		t.Fatal("expected compression diagnostics")
	}
	if diagnostics["strategy"] != strategy {
		t.Errorf("strategy = %v, want %q", diagnostics["strategy"], strategy)
	}
	if diagnostics["tokens_saved"] != tokensSaved {
		t.Errorf("tokens_saved = %v, want %d", diagnostics["tokens_saved"], tokensSaved)
	}

	response := turnDetailV2Response{
		Request:     map[string]any{"messages": []any{map[string]any{"role": "user", "content": "original request"}}},
		Compression: diagnostics,
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal detail response: %v", err)
	}

	var projected struct {
		Request     json.RawMessage `json:"request"`
		Compression json.RawMessage `json:"compression"`
	}
	if err := json.Unmarshal(encoded, &projected); err != nil {
		t.Fatalf("decode detail response: %v", err)
	}
	if strings.Contains(string(projected.Request), "[smm_v1:abc123]") {
		t.Fatalf("request projection leaked internal marker: %s", projected.Request)
	}
	if !strings.Contains(string(projected.Compression), "[smm_v1:abc123]") {
		t.Fatalf("compression diagnostics lost internal outbound marker: %s", projected.Compression)
	}
}

func TestBuildCompressionDiagnosticsV2_EmptyTurnReturnsNil(t *testing.T) {
	if got := buildCompressionDiagnosticsV2("req-1", false, nil, nil, nil, nil); got != nil {
		t.Fatalf("empty diagnostics = %#v, want nil", got)
	}
}
