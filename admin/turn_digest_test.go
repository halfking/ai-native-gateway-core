package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

// nullErrorKind exercises the audit P1-1 fix: NULL error_kind in the DB must
// not cause the digest builder or the scan path to panic.
func TestExtractMetrics_NullErrorKind(t *testing.T) {
	meta := map[string]any{
		"cost_usd":     0.0123,
		"latency_ms":   1500,
		"status_code":  200,
		"success":      true,
		"error_kind":   nil, // mirrors a SQL NULL row
	}
	events := extractEvents(meta, nil)
	for _, e := range events {
		if e.Type == "error" && strings.Contains(e.Message, "<nil>") {
			t.Fatalf("event leaked nil pointer as message: %#v", e)
		}
	}
	got := extractMetrics(meta, nil)
	if got.LatencyMs != 1500 {
		t.Fatalf("latency lost: %+v", got)
	}
}

// audit P1-2: tokens_used should NOT be silently overwritten with prompt +
// completion when both tokens_used and prompt_tokens are present (list view).
func TestExtractMetrics_TokensUsedNotDoubleCounted(t *testing.T) {
	meta := map[string]any{
		"tokens_used":      100,
		"prompt_tokens":    40,
		"completion_tokens": 60,
		"cost_usd":         0.01,
		"latency_ms":       500,
	}
	got := extractMetrics(meta, nil)
	if got.TokensUsed != 100 {
		t.Fatalf("expected tokens_used=100 (pre-supplied), got %d", got.TokensUsed)
	}
}

// fallback path: only prompt+completion present (detail view) → sum is OK.
func TestExtractMetrics_TokensUsedFallback(t *testing.T) {
	meta := map[string]any{
		"prompt_tokens":     40,
		"completion_tokens": 60,
		"cost_usd":          0.01,
		"latency_ms":        500,
	}
	got := extractMetrics(meta, nil)
	if got.TokensUsed != 100 {
		t.Fatalf("expected fallback sum=100, got %d", got.TokensUsed)
	}
}

// audit P1-3: cache_hit_rate formula must include cache_write_tokens when
// available so a cache_write-heavy trace does not look like 100% hits.
func TestExtractMetrics_CacheHitRate(t *testing.T) {
	cases := []struct {
		name           string
		meta           map[string]any
		wantNil        bool
		wantLo, wantHi float64
	}{
		{
			name: "with write tokens — denominator = read+write",
			meta: map[string]any{
				"prompt_tokens":      100,
				"cache_read_tokens":  40,
				"cache_write_tokens": 60,
			},
			wantLo: 0.39, wantHi: 0.41,
		},
		{
			name: "no write tokens — denominator = prompt",
			meta: map[string]any{
				"prompt_tokens":     100,
				"cache_read_tokens": 25,
			},
			wantLo: 0.24, wantHi: 0.26,
		},
		{
			name: "read > prompt (edge case) — denominator = read",
			meta: map[string]any{
				"prompt_tokens":     10,
				"cache_read_tokens": 50,
			},
			wantLo: 0.99, wantHi: 1.0,
		},
		{
			name:    "zero denominator — keep nil",
			meta:    map[string]any{"cache_read_tokens": 0},
			wantNil: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractMetrics(tc.meta, nil)
			if tc.wantNil {
				if got.CacheHitRate != nil {
					t.Fatalf("expected nil CacheHitRate, got %v", *got.CacheHitRate)
				}
				return
			}
			if got.CacheHitRate == nil {
				t.Fatalf("expected CacheHitRate in (%v, %v), got nil", tc.wantLo, tc.wantHi)
			}
			if *got.CacheHitRate < tc.wantLo || *got.CacheHitRate > tc.wantHi {
				t.Fatalf("CacheHitRate %v outside [%v, %v]", *got.CacheHitRate, tc.wantLo, tc.wantHi)
			}
		})
	}
}

// buildTurnDigest on a minimal payload must not panic and must surface the
// high-latency warning event.
func TestBuildTurnDigest_BasicPayload(t *testing.T) {
	req, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{
			{"role": "user", "content": "hi there"},
		},
	})
	resp, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"role": "assistant", "content": "hello back"}},
		},
	})
	var reqAny, respAny any
	_ = json.Unmarshal(req, &reqAny)
	_ = json.Unmarshal(resp, &respAny)
	d := buildTurnDigest(reqAny, respAny,
		map[string]any{"cost_usd": 0.001, "latency_ms": 8000, "status_code": 200},
		nil)
	if d == nil {
		t.Fatal("expected non-nil digest for healthy payload")
	}
	if d.UserInput == "" || d.AssistantOutput == "" {
		t.Fatalf("missing user/assistant text: %+v", d)
	}
	foundLatencyWarn := false
	for _, e := range d.Events {
		if e.Type == "warning" && e.Category == "performance" {
			foundLatencyWarn = true
		}
	}
	if !foundLatencyWarn {
		t.Fatalf("expected high-latency warning event, got %+v", d.Events)
	}
}

// buildTurnDigest must be nil-safe: every nil input → empty digest, no panic.
// All-nil inputs intentionally return nil — there's nothing to summarise.
func TestBuildTurnDigest_NilInputs(t *testing.T) {
	d := buildTurnDigest(nil, nil, nil, nil)
	if d != nil {
		t.Fatalf("expected nil digest for all-nil inputs, got %+v", d)
	}
}

// nil request/response with non-nil meta (e.g. metrics only) → digest with
// metrics, no user/assistant text. Ensures the metrics-only path survives.
func TestBuildTurnDigest_MetricsOnly(t *testing.T) {
	d := buildTurnDigest(nil, nil,
		map[string]any{"cost_usd": 0.001, "latency_ms": 100, "status_code": 200},
		nil)
	if d == nil {
		t.Fatal("expected non-nil digest when meta has metrics")
	}
	if d.UserInput != "" || d.AssistantOutput != "" {
		t.Fatalf("expected empty text fields, got %+v", d)
	}
	if d.Metrics.LatencyMs != 100 {
		t.Fatalf("metrics.latency_ms lost: %+v", d.Metrics)
	}
}

// tool deduplication: same tool name across messages collapses to a single
// entry with tool_call_count = number of invocations.
func TestBuildTurnDigest_ToolDedup(t *testing.T) {
	resp, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{
				"role": "assistant",
				"tool_calls": []map[string]any{
					{"id": "call-1", "function": map[string]any{"name": "web_search"}},
					{"id": "call-2", "function": map[string]any{"name": "web_search"}},
					{"id": "call-3", "function": map[string]any{"name": "calculator"}},
				},
			}},
		},
	})
	var respAny any
	_ = json.Unmarshal(resp, &respAny)
	d := buildTurnDigest(nil, respAny,
		map[string]any{"cost_usd": 0, "latency_ms": 100},
		nil)
	if d == nil || d.ToolUsage == nil {
		t.Fatal("expected tool usage block")
	}
	if d.ToolUsage.ToolCallCount != 3 {
		t.Fatalf("tool_call_count = %d, want 3", d.ToolUsage.ToolCallCount)
	}
	if len(d.ToolUsage.ToolsUsed) != 2 {
		t.Fatalf("tools_used length = %d, want 2 (deduped)", len(d.ToolUsage.ToolsUsed))
	}
}
