package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessiondigest"
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

func TestPersistedDigestOrFallbackPrefersSupportedEnvelope(t *testing.T) {
	raw, err := sessiondigest.Marshal(&sessiondigest.Envelope{
		SchemaVersion: sessiondigest.SchemaVersion, AlgorithmVersion: sessiondigest.AlgorithmVersion,
		GeneratedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Source: sessiondigest.Source,
		Payload: sessiondigest.Digest{UserInput: "persisted user", AssistantOutput: "persisted assistant", Metrics: sessiondigest.Metrics{TokensUsed: 9}},
	})
	if err != nil {
		t.Fatalf("marshal persisted digest: %v", err)
	}
	got := persistedDigestOrFallback(raw, []any{map[string]any{"role": "user", "content": "fallback"}}, nil, nil, nil)
	if got == nil || got.UserInput != "persisted user" || got.AssistantOutput != "persisted assistant" || got.Metrics.TokensUsed != 9 {
		t.Fatalf("persisted digest was not returned: %#v", got)
	}
}

func TestPersistedDigestOrFallbackFallsBackForMalformedValue(t *testing.T) {
	got := persistedDigestOrFallback([]byte(`{"schema_version":`), []any{map[string]any{"role": "user", "content": "fallback user"}}, []any{map[string]any{"role": "assistant", "content": "fallback assistant"}}, map[string]any{"prompt_tokens": 1}, nil)
	if got == nil || got.UserInput != "fallback user" || got.AssistantOutput != "fallback assistant" {
		t.Fatalf("malformed digest did not fall back: %#v", got)
	}
}
