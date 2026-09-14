package autoroute

import (
	"testing"
)

func TestScore_Balanced(t *testing.T) {
	c := Candidate{
		CredentialID:      1,
		CanonicalName:     "claude-sonnet-4.5",
		UnitPriceInPer1M:  3.0,
		UnitPriceOutPer1M: 15.0,
		SuccessRate:       0.95,
		P95LatencyMs:      800,
		PressureRatio:     0.2,
		ConcurrencyLimit:  50,
		ActiveSessions:    10,
		ContextWindow:     200_000,
		TaskMatchScore:    0.66,
		Tags:              []string{"reasoning", "code"},
	}
	sigs := ClassificationSignals{
		EstimatedTokens: 4000,
	}
	cost := CostContext{PriceP75: 18.0, SpeedP95: 1500}

	bd := Score(c, sigs, TaskReasoning, ProfileSmart, cost)

	// Smart is balanced — composite should be in [40, 80]
	if bd.Composite < 40 || bd.Composite > 80 {
		t.Fatalf("smart composite out of expected range: %.2f", bd.Composite)
	}
	// Stability: 0.95 × 100 = 95
	if bd.StabilityScore != 95 {
		t.Fatalf("stability: %.2f want 95", bd.StabilityScore)
	}
	// Pressure: (1 - 0.2) × 100 = 80
	if bd.PressureScore != 80 {
		t.Fatalf("pressure: %.2f want 80", bd.PressureScore)
	}
}

func TestScore_FreeModel(t *testing.T) {
	c := Candidate{
		CanonicalName:     "free-model",
		UnitPriceInPer1M:  0,
		UnitPriceOutPer1M: 0,
		SuccessRate:       0.9,
	}
	bd := Score(c, ClassificationSignals{}, TaskChat, ProfileSmart, CostContext{PriceP75: 10})
	if bd.PriceScore != 100 {
		t.Fatalf("free model should score 100 on price, got %.2f", bd.PriceScore)
	}
}

func TestScore_CostFirst_WeightsPrice(t *testing.T) {
	// Same candidate, two profiles. Cost-first should score higher when
	// price is low (cheap model wins under cost-first but not under speed-first).
	cheapFast := Candidate{
		CanonicalName:     "cheap-fast",
		UnitPriceInPer1M:  0.5,
		UnitPriceOutPer1M: 1.5,
		SuccessRate:       0.85,
		P95LatencyMs:      1200,
		PressureRatio:     0.3,
		TaskMatchScore:    0.5,
	}
	expensiveFast := Candidate{
		CanonicalName:     "expensive-fast",
		UnitPriceInPer1M:  10.0,
		UnitPriceOutPer1M: 30.0,
		SuccessRate:       0.98,
		P95LatencyMs:      400,
		PressureRatio:     0.1,
		TaskMatchScore:    0.8,
	}
	sigs := ClassificationSignals{}
	cost := CostContext{PriceP75: 20, SpeedP95: 1200}

	cheapCost := Score(cheapFast, sigs, TaskChat, ProfileCostFirst, cost)
	expensCost := Score(expensiveFast, sigs, TaskChat, ProfileCostFirst, cost)

	// cost-first: cheap should win
	if cheapCost.Composite <= expensCost.Composite {
		t.Fatalf("cost-first should prefer cheap: cheap=%.2f expensive=%.2f",
			cheapCost.Composite, expensCost.Composite)
	}

	cheapSpeed := Score(cheapFast, sigs, TaskChat, ProfileSpeedFirst, cost)
	expensSpeed := Score(expensiveFast, sigs, TaskChat, ProfileSpeedFirst, cost)

	// speed-first: expensive-fast (lower latency) should win
	if expensSpeed.Composite <= cheapSpeed.Composite {
		t.Fatalf("speed-first should prefer expensive-fast: cheap=%.2f expensive=%.2f",
			cheapSpeed.Composite, expensSpeed.Composite)
	}
}

func TestTaskMatchScore(t *testing.T) {
	tags := []string{"reasoning", "code", "math"}
	if got := TaskMatchScore(TaskReasoning, tags); got < 0.6 {
		t.Fatalf("reasoning partial: got %.2f", got)
	}
	if got := TaskMatchScore(TaskCode, tags); got < 0.3 {
		t.Fatalf("code partial: got %.2f", got)
	}
	if got := TaskMatchScore(TaskVision, tags); got != 0 {
		t.Fatalf("vision no match: got %.2f", got)
	}
	if got := TaskMatchScore(TaskChat, tags); got != 0.5 {
		t.Fatalf("chat neutral: got %.2f", got)
	}
}

// 2026-09-14 audit O1′-a: the required-tag vocabulary uses underscores while
// library capability tags use hyphens (cap:tool-use etc.). Before separator
// normalization these never matched, pinning agent/function_call MatchScore
// at 0 and forcing the 48h fallback pool.
func TestTaskMatchScoreSeparatorNormalization(t *testing.T) {
	// agent 词表 = [agent, tool_use, function_call]; both hyphen capability
	// tags hit → 2/3.
	agentTags := []string{"cap:tool-use", "cap:function-call"}
	if got := TaskMatchScore(TaskAgent, agentTags); got < 0.6666 || got > 0.6668 {
		t.Fatalf("agent hyphen tags: want 0.6667, got %.4f", got)
	}
	// function_call 词表 = [function_call, tool_use]; both hit → 1.0.
	if got := TaskMatchScore(TaskFunctionCall, agentTags); got != 1.0 {
		t.Fatalf("function_call hyphen tags: want 1.0, got %.4f", got)
	}
	// long_context 词表 = [long_context, 128k, 200k, 512k, 1m]; one hit → 0.2.
	if got := TaskMatchScore(TaskLongContext, []string{"cap:long-context"}); got != 0.2 {
		t.Fatalf("long_context hyphen tag: want 0.2, got %.4f", got)
	}
	// long_context + window-size tag (both separators, mixed) → 2/5.
	mixed := []string{"cap:long-context", "context_128k"}
	if got := TaskMatchScore(TaskLongContext, mixed); got != 0.4 {
		t.Fatalf("long_context mixed separators: want 0.4, got %.4f", got)
	}
	// Underscore-style library tags keep matching after normalization.
	if got := TaskMatchScore(TaskFunctionCall, []string{"cap:tool_use"}); got != 0.5 {
		t.Fatalf("underscore library tag: want 0.5, got %.4f", got)
	}
	// Non-separator matching is unchanged (code 词表 = [code, programming],
	// "code-review" hits "code" only).
	if got := TaskMatchScore(TaskCode, []string{"cap:code-review"}); got != 0.5 {
		t.Fatalf("plain substring: want 0.5, got %.4f", got)
	}
	if got := TaskMatchScore(TaskVision, []string{"cap:reasoning"}); got != 0 {
		t.Fatalf("no false hit: want 0, got %.4f", got)
	}
}

func TestNormalizeTagSeparators(t *testing.T) {
	cases := map[string]string{
		"tool_use":       "tool-use",
		"cap:tool_use":   "cap:tool-use",
		"long_context":   "long-context",
		"already-hyphen": "already-hyphen",
		"plain":          "plain",
		"cap:128k":       "cap:128k",
		"a_b_c":          "a-b-c",
	}
	for in, want := range cases {
		if got := normalizeTagSeparators(in); got != want {
			t.Fatalf("normalizeTagSeparators(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRequiredTagsForTask(t *testing.T) {
	cases := map[TaskType]bool{
		TaskReasoning:    true,
		TaskCode:         true,
		TaskAgent:        true,
		TaskCreative:     true,
		TaskLongContext:  true,
		TaskVision:       true,
		TaskFunctionCall: true,
		TaskChat:         false,
	}
	for task, hasTags := range cases {
		got := requiredTagsForTask(task)
		if hasTags && len(got) == 0 {
			t.Fatalf("%s should have required tags", task)
		}
		if !hasTags && len(got) != 0 {
			t.Fatalf("%s should not have required tags", task)
		}
	}
}

func TestComputeCostContext(t *testing.T) {
	cands := []Candidate{
		{UnitPriceInPer1M: 1, UnitPriceOutPer1M: 1, P95LatencyMs: 100},
		{UnitPriceInPer1M: 5, UnitPriceOutPer1M: 5, P95LatencyMs: 500},
		{UnitPriceInPer1M: 10, UnitPriceOutPer1M: 10, P95LatencyMs: 1000},
		{UnitPriceInPer1M: 0, UnitPriceOutPer1M: 0}, // free — excluded from price P75
	}
	ctx := computeCostContext(cands)
	if ctx.PriceP75 <= 0 {
		t.Fatalf("PriceP75 should be > 0, got %.2f", ctx.PriceP75)
	}
	if ctx.SpeedP95 != 1000 {
		t.Fatalf("SpeedP95 = %.0f, want 1000", ctx.SpeedP95)
	}
}

func TestPercentile(t *testing.T) {
	sorted := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := percentile(sorted, 0.5); got != 5 {
		t.Fatalf("p50: got %.0f want 5", got)
	}
	if got := percentile(sorted, 0.75); got != 8 {
		t.Fatalf("p75: got %.0f want 8", got)
	}
	if got := percentile(sorted, 0); got != 1 {
		t.Fatalf("p0: got %.0f want 1", got)
	}
	if got := percentile(sorted, 1); got != 10 {
		t.Fatalf("p100: got %.0f want 10", got)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Fatalf("empty: got %.0f want 0", got)
	}
}

func TestParseTagsJSONB(t *testing.T) {
	cases := map[string][]string{
		`["reasoning","code"]`:      {"reasoning", "code"},
		`[]`:                        nil,
		`null`:                      nil,
		"":                          nil,
		`["a","b","c"]`:             {"a", "b", "c"},
		`  ["reasoning", "code"]  `: {"reasoning", "code"},
		`not json`:                  nil,
	}
	for in, want := range cases {
		got := parseTagsJSONB(in)
		if len(got) != len(want) {
			t.Fatalf("input=%q: got %v want %v", in, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("input=%q: got %v want %v", in, got, want)
			}
		}
	}
}
