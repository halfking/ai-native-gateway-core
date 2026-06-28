package governance

import "testing"

func TestGovernanceState_NilSafe(t *testing.T) {
	var g *GovernanceState
	g.RecordVerdict(&Verdict{Allow: true})
	g.RecordDecision(&Decision{Kind: DecisionContinue})
	if g.HasBlock() {
		t.Fatal("nil state must not report HasBlock")
	}
	if g.HighestSeverity() != -1 {
		t.Fatalf("nil state HighestSeverity = %d, want -1", g.HighestSeverity())
	}
}

func TestGovernanceState_RecordAndQuery(t *testing.T) {
	g := &GovernanceState{}
	g.RecordVerdict(&Verdict{PluginName: "p1", Allow: true, Severity: 1})
	g.RecordVerdict(&Verdict{PluginName: "p2", Allow: false, Severity: 3})
	g.RecordVerdict(nil) // 忽略

	if len(g.Verdicts) != 2 {
		t.Fatalf("Verdicts len = %d, want 2", len(g.Verdicts))
	}
	if !g.HasBlock() {
		t.Fatal("HasBlock = false, want true (p2 denies)")
	}
	if g.HighestSeverity() != 3 {
		t.Fatalf("HighestSeverity = %d, want 3", g.HighestSeverity())
	}
}

func TestGovernanceState_HighestSeverity_Empty(t *testing.T) {
	g := &GovernanceState{}
	if g.HighestSeverity() != -1 {
		t.Fatalf("empty HighestSeverity = %d, want -1", g.HighestSeverity())
	}
}

func TestGovernanceState_RecordDecisionAppendsTrace(t *testing.T) {
	g := &GovernanceState{}
	g.RecordDecision(&Decision{Kind: DecisionBlock})
	g.RecordDecision(&Decision{Kind: DecisionContinue})

	if g.Decision == nil || g.Decision.Kind != DecisionContinue {
		t.Fatalf("Decision should be the latest, got %v", g.Decision)
	}
	if len(g.DecisionTrace) != 2 {
		t.Fatalf("DecisionTrace len = %d, want 2", len(g.DecisionTrace))
	}
	if g.DecisionTrace[0].Decision != DecisionBlock ||
		g.DecisionTrace[1].Decision != DecisionContinue {
		t.Fatalf("DecisionTrace order wrong: %+v", g.DecisionTrace)
	}
	if g.DecisionTrace[0].Stage != "governance" {
		t.Fatalf("Stage = %q, want governance", g.DecisionTrace[0].Stage)
	}
}

func TestVerdict_ZeroValueIsDenying(t *testing.T) {
	// 零值 Verdict 必须默认 Allow=false（deny by default），
	// 避免未初始化的 verdict 被错误地当作放行。
	var v Verdict
	if v.Allow {
		t.Fatal("zero Verdict must default Allow=false (deny by default)")
	}
}
