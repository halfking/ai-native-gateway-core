package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
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

// ─── rune-safe truncation (P1-1b, 2026-09) ─────────────────────────────────

// TestSummarizeDigestText_RuneSafeTruncation pins the P1-1b fix: CJK content
// must be cut on rune boundaries. The previous byte-slice cut produced
// invalid UTF-8 (or, after the repair loop, a materially shorter string).
func TestSummarizeDigestText_RuneSafeTruncation(t *testing.T) {
	// Punctuation-free input with no key-point markers: splitDigestSentences
	// yields ONE part, selection = [part0] (len(selected)==1, len(parts)==1
	// so no second part is appended) and the joined result exceeds 260 runes,
	// exercising the truncateRunes(result, 259) + "…" path with 3-byte runes.
	long := strings.Repeat("字", 300) // 900 bytes, 300 runes
	got := summarizeDigestText(long)
	if !utf8.ValidString(got) {
		t.Fatalf("summarized text is not valid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > digestMaxRunes {
		t.Fatalf("result %d runes exceeds cap %d: %q", n, digestMaxRunes, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected trailing ellipsis on truncated result: %q", got)
	}
}

// A run of sentences with no key-point markers keeps first+second sentence
// only when short enough; force the head+tail branch instead via one huge
// punctuation-free sentence plus a short key-point-free tail — the single
// selected sentence overflows and takes the truncateRunes path. For the true
// head+tail branch (selected empty) we use a >100-byte input whose ONLY
// sentence boundary sits at the very end: parts = [whole], selected=[whole],
// still the sentence path. The head+tail branch needs parts == 0 which
// requires an empty sentence split — impossible for non-empty input — so it
// is a defensive fallback; we simply assert it never produces invalid UTF-8
// by calling the helpers directly.
func TestSummarizeDigestText_TruncationHelpersRuneSafe(t *testing.T) {
	long := strings.Repeat("字", 500)
	for name, fn := range map[string]func(string, int) string{
		"truncateRunes":     truncateRunes,
		"truncateTailRunes": truncateTailRunes,
	} {
		for _, max := range []int{0, 1, 50, 110, 260} {
			got := fn(long, max)
			if !utf8.ValidString(got) {
				t.Fatalf("%s(%d runes) produced invalid UTF-8: %q", name, max, got)
			}
			if n := utf8.RuneCountInString(got); n != max {
				t.Fatalf("%s max=%d returned %d runes", name, max, n)
			}
		}
	}
}

func TestSummarizeDigestText_SentenceSelectionCapIsRunes(t *testing.T) {
	// Several sentences, all containing the key-point marker "结论" so every
	// one is selected; the joined result exceeds the cap and must be cut to
	// <=260 runes (matching the persisted sessiondigest.compact ceiling).
	s := strings.Repeat("这是结论很重要的一句话。", 40) // 13 runes/sentence * 40
	got := summarizeDigestText(s)
	if !utf8.ValidString(got) {
		t.Fatalf("result not valid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > digestMaxRunes+1 {
		t.Fatalf("result %d runes exceeds cap %d (+ellipsis): %q", n, digestMaxRunes, got)
	}
}

func TestSummarizeDigestText_ShortTextUntouched(t *testing.T) {
	for _, s := range []string{"", "短句", strings.Repeat("a", 100)} {
		if got := summarizeDigestText(s); got != strings.Join(strings.Fields(s), " ") {
			t.Fatalf("short input %q should pass through (whitespace-collapsed), got %q", s, got)
		}
	}
}
