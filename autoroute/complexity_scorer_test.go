package autoroute

import (
	"testing"
)

// TestEstimateComplexity_BaselineByTask: each task type maps to its base level
// in the absence of other signals. See 22 §22.5 + complexity_scorer.go.
func TestEstimateComplexity_BaselineByTask(t *testing.T) {
	cases := []struct {
		task TaskType
		want ComplexityLevel
	}{
		{TaskChat, ComplexityEasy},
		{TaskCreative, ComplexityEasy},
		{TaskCode, ComplexityMedium},
		{TaskVision, ComplexityMedium},
		{TaskFunctionCall, ComplexityMedium},
		{TaskReasoning, ComplexityHard},
		{TaskAgent, ComplexityHard},
		{TaskLongContext, ComplexityHard},
		{TaskCodeAudit, ComplexityHard},
	}
	th := DefaultComplexityThresholds()
	for _, c := range cases {
		sigs := ClassificationSignals{} // minimal
		got := EstimateComplexity(sigs, c.task, th)
		if got != c.want {
			t.Errorf("task=%s: got %s, want %s", c.task, got, c.want)
		}
	}
}

// TestEstimateComplexity_TokenBoost: tokens > medium threshold boosts easy
// tasks to at least medium; > long-context threshold boosts to hard.
func TestEstimateComplexity_TokenBoost(t *testing.T) {
	th := DefaultComplexityThresholds()
	// chat (easy base) + 10k tokens → medium
	got := EstimateComplexity(ClassificationSignals{EstimatedTokens: 10_000}, TaskChat, th)
	if got != ComplexityMedium {
		t.Fatalf("chat+10k tokens: got %s, want medium", got)
	}
	// chat + 60k tokens → hard
	got = EstimateComplexity(ClassificationSignals{EstimatedTokens: 60_000}, TaskChat, th)
	if got != ComplexityHard {
		t.Fatalf("chat+60k tokens: got %s, want hard", got)
	}
}

// TestEstimateComplexity_FrontierOnlyWhenAllHardSignals: frontier requires
// reasoning/code_audit + long context + many tools simultaneously.
func TestEstimateComplexity_FrontierOnlyWhenAllHardSignals(t *testing.T) {
	th := DefaultComplexityThresholds()
	// reasoning + long + 3 tools → frontier
	got := EstimateComplexity(ClassificationSignals{
		EstimatedTokens: 60_000, ToolCount: 3,
	}, TaskReasoning, th)
	if got != ComplexityFrontier {
		t.Fatalf("reasoning+long+tools: got %s, want frontier", got)
	}
	// reasoning + long but no tools → hard (not frontier)
	got = EstimateComplexity(ClassificationSignals{
		EstimatedTokens: 60_000, ToolCount: 0,
	}, TaskReasoning, th)
	if got != ComplexityHard {
		t.Fatalf("reasoning+long notools: got %s, want hard", got)
	}
	// chat (not hard task) + long + tools → hard, NOT frontier
	got = EstimateComplexity(ClassificationSignals{
		EstimatedTokens: 60_000, ToolCount: 3,
	}, TaskChat, th)
	if got != ComplexityHard {
		t.Fatalf("chat+long+tools: got %s, want hard (not frontier)", got)
	}
}

// TestEstimateComplexity_Boundaries: empty sigs never panics; zero tokens ok.
func TestEstimateComplexity_Boundaries(t *testing.T) {
	th := DefaultComplexityThresholds()
	got := EstimateComplexity(ClassificationSignals{}, TaskChat, th)
	if got != ComplexityEasy {
		t.Fatalf("empty sigs chat: got %s, want easy", got)
	}
	// zero-value thresholds → falls back to defaults (no panic, no div by zero)
	got = EstimateComplexity(ClassificationSignals{EstimatedTokens: 100_000}, TaskReasoning, ComplexityThresholds{})
	if got != ComplexityHard && got != ComplexityFrontier {
		t.Fatalf("zero thresholds: got %s, want hard/frontier", got)
	}
}

// TestComplexityMatch_FilterRules: reqLevel > ceiling rejected;
// reqLevel < min rejected; empty ceiling/min passes.
func TestComplexityMatch_FilterRules(t *testing.T) {
	if !ComplexityMatch(ComplexityEasy, "easy", "") {
		t.Error("easy vs ceiling=easy should pass")
	}
	if ComplexityMatch(ComplexityHard, "easy", "") {
		t.Error("hard vs ceiling=easy should be rejected (model too weak)")
	}
	if ComplexityMatch(ComplexityEasy, "frontier", "hard") {
		t.Error("easy vs min=hard should be rejected (overkill)")
	}
	if !ComplexityMatch(ComplexityHard, "", "") {
		t.Error("empty ceiling/min should always pass (backward compat)")
	}
	if !ComplexityMatch(ComplexityHard, "frontier", "") {
		t.Error("hard vs ceiling=frontier should pass")
	}
}

// TestComplexityMatch_PromotesCostEfficiency: a frontier model with
// min_complexity=hard rejects a simple chat request (cost waste avoided).
func TestComplexityMatch_PromotesCostEfficiency(t *testing.T) {
	// frontier model, min=hard: chat(easy) request rejected
	if ComplexityMatch(ComplexityEasy, "frontier", "hard") {
		t.Error("frontier model should not take easy chat (cost waste)")
	}
	// but a hard reasoning request is allowed
	if !ComplexityMatch(ComplexityHard, "frontier", "hard") {
		t.Error("frontier model should accept hard request")
	}
}
