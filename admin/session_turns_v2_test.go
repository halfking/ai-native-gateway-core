package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	promtest "github.com/prometheus/client_golang/prometheus/testutil"

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

// ─── digest fallback observability (P1-1a, 2026-09) ───────────────────────

// readFallbackCount reads the current value of the digestFallbackTotal
// counter for one reason label. Uses the prometheus test poison pill so the
// shared registry is cleaned up even on assertion failure.
func readFallbackCount(t *testing.T, reason digestFallbackReason) int {
	t.Helper()
	m := promtest.ToFloat64(digestFallbackTotal.WithLabelValues(string(reason)))
	return int(m)
}

func TestPersistedDigestOrFallback_CountsFallbackReasons(t *testing.T) {
	cases := []struct {
		name    string
		raw     []byte
		reason  digestFallbackReason
	}{
		{"missing null", []byte(`null`), digestFallbackMissing},
		{"missing empty", nil, digestFallbackMissing},
		{"malformed json", []byte(`{"schema_version":`), digestFallbackMalformed},
		{"future version", []byte(`{"schema_version":999,"algorithm_version":"deterministic-v999"}`), digestFallbackVersion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := readFallbackCount(t, tc.reason)
			got := persistedDigestOrFallback(tc.raw, []any{map[string]any{"role": "user", "content": "u"}}, nil, nil, nil)
			after := readFallbackCount(t, tc.reason)
			if got == nil {
				t.Fatalf("expected rebuilt digest, got nil")
			}
			if after != before+1 {
				t.Fatalf("counter for %q: before=%d after=%d (expected +1)", tc.reason, before, after)
			}
		})
	}
}

// A valid, versioned envelope must NOT increment any fallback bucket.
func TestPersistedDigestOrFallback_NoCounterForPersistedHit(t *testing.T) {
	raw, err := sessiondigest.Marshal(&sessiondigest.Envelope{
		SchemaVersion: sessiondigest.SchemaVersion, AlgorithmVersion: sessiondigest.AlgorithmVersion,
		GeneratedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Source: sessiondigest.Source,
		Payload: sessiondigest.Digest{UserInput: "persisted"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	beforeMissing := readFallbackCount(t, digestFallbackMissing)
	beforeMalformed := readFallbackCount(t, digestFallbackMalformed)
	beforeVersion := readFallbackCount(t, digestFallbackVersion)

	got := persistedDigestOrFallback(raw, nil, nil, nil, nil)
	if got == nil || got.UserInput != "persisted" {
		t.Fatalf("persisted digest not returned: %#v", got)
	}
	for _, pair := range []struct {
		r    digestFallbackReason
		base int
	}{{digestFallbackMissing, beforeMissing}, {digestFallbackMalformed, beforeMalformed}, {digestFallbackVersion, beforeVersion}} {
		if got := readFallbackCount(t, pair.r); got != pair.base {
			t.Fatalf("counter for %q incremented on persisted hit: %d -> %d", pair.r, pair.base, got)
		}
	}
}
