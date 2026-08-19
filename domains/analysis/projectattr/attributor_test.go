package projectattr

import (
	"context"
	"errors"
	"testing"
)

func testProjects() []Project {
	return []Project{
		{
			Ref:           "p-gateway",
			Name:          "LLM Gateway",
			MatchKeywords: []string{"gateway"},
			RepoPaths:     []string{"/workspace/llm-gateway-go-3"},
		},
		{
			Ref:           "p-pms",
			Name:          "PMS",
			MatchKeywords: []string{"pms", "property"},
		},
	}
}

func TestAttribute_RuleMatchesRepoPath(t *testing.T) {
	a := New(testProjects())
	got := a.Attribute(context.Background(), Signals{
		SystemPrompt: "You are working in /workspace/llm-gateway-go-3 on the router.",
	})
	if got.ProjectRef != "p-gateway" {
		t.Fatalf("ProjectRef = %q, want p-gateway", got.ProjectRef)
	}
	if got.Method != MethodRule {
		t.Fatalf("Method = %q, want rule", got.Method)
	}
	if got.Status != StatusPending {
		t.Fatalf("Status = %q, want pending (human review by default)", got.Status)
	}
	if got.Evidence["match_kind"] != "repo_path" {
		t.Fatalf("match_kind = %v, want repo_path", got.Evidence["match_kind"])
	}
}

// 路径信号唯一性强于关键词，两者同时命中不同项目时必须选路径。
func TestAttribute_RepoPathBeatsKeyword(t *testing.T) {
	projects := []Project{
		{Ref: "p-gateway", Name: "LLM Gateway", RepoPaths: []string{"/workspace/llm-gateway-go-3"}},
		{Ref: "p-pms", Name: "PMS", MatchKeywords: []string{"gateway"}},
	}
	a := New(projects)
	got := a.Attribute(context.Background(), Signals{
		SystemPrompt: "cd /workspace/llm-gateway-go-3 && fix the gateway",
	})
	if got.ProjectRef != "p-gateway" {
		t.Fatalf("ProjectRef = %q, want p-gateway (repo path wins)", got.ProjectRef)
	}
}

// 多个不同项目同时命中是最易出错的情形，必须降置信度以便人工优先复核。
func TestAttribute_AmbiguousLowersConfidence(t *testing.T) {
	projects := []Project{
		{Ref: "p-a", Name: "A", MatchKeywords: []string{"alpha"}},
		{Ref: "p-b", Name: "B", MatchKeywords: []string{"beta"}},
	}
	a := New(projects)
	got := a.Attribute(context.Background(), Signals{UserText: "alpha and beta both"})
	if got.Method != MethodRule {
		t.Fatalf("Method = %q, want rule", got.Method)
	}
	if got.Confidence >= 0.95 {
		t.Fatalf("Confidence = %v, want lowered for ambiguous match", got.Confidence)
	}
	if got.Evidence["ambiguous"] != true {
		t.Fatalf("ambiguous = %v, want true", got.Evidence["ambiguous"])
	}
}

func TestAttribute_FallsBackToInherit(t *testing.T) {
	called := false
	a := New(testProjects(), WithInherit(func(_ context.Context, s Signals) (string, error) {
		called = true
		if s.IdentityHash != "fp-1" {
			t.Fatalf("IdentityHash = %q, want fp-1", s.IdentityHash)
		}
		return "p-pms", nil
	}))
	got := a.Attribute(context.Background(), Signals{
		UserText:     "no matching signal here",
		IdentityHash: "fp-1",
	})
	if !called {
		t.Fatal("inherit lookup was not called")
	}
	if got.Method != MethodInherit || got.ProjectRef != "p-pms" {
		t.Fatalf("got %+v, want inherit/p-pms", got)
	}
	if got.ProjectLabel != "PMS" {
		t.Fatalf("ProjectLabel = %q, want PMS (resolved from project_dim)", got.ProjectLabel)
	}
}

// 规则命中时不得触发继承或模型，否则成本模型失效。
func TestAttribute_RuleShortCircuitsLowerTiers(t *testing.T) {
	a := New(testProjects(),
		WithInherit(func(context.Context, Signals) (string, error) {
			t.Fatal("inherit must not run after a rule hit")
			return "", nil
		}),
		WithLLM(func(context.Context, Signals, []Project) (string, string, error) {
			t.Fatal("llm must not run after a rule hit")
			return "", "", nil
		}),
	)
	if got := a.Attribute(context.Background(), Signals{UserText: "pms work"}); got.Method != MethodRule {
		t.Fatalf("Method = %q, want rule", got.Method)
	}
}

func TestAttribute_LLMOnlyAsLastResort(t *testing.T) {
	a := New(testProjects(),
		WithInherit(func(context.Context, Signals) (string, error) { return "", nil }),
		WithLLM(func(context.Context, Signals, []Project) (string, string, error) {
			return "", "Ad-hoc Research", nil
		}),
	)
	got := a.Attribute(context.Background(), Signals{UserText: "totally unrelated"})
	if got.Method != MethodLLM {
		t.Fatalf("Method = %q, want llm", got.Method)
	}
	if got.ProjectLabel != "Ad-hoc Research" {
		t.Fatalf("ProjectLabel = %q", got.ProjectLabel)
	}
	if got.Status != StatusPending {
		t.Fatalf("Status = %q, want pending — LLM output always needs review", got.Status)
	}
}

// 判不出来必须留空，不能编造占位值（对齐 OTel gen_ai.conversation.id 规则）。
func TestAttribute_UnresolvedStaysEmpty(t *testing.T) {
	a := New(testProjects())
	got := a.Attribute(context.Background(), Signals{UserText: "nothing relevant"})
	if got.Found() {
		t.Fatalf("expected no attribution, got %+v", got)
	}
	if got.ProjectRef != "" || got.ProjectLabel != "" {
		t.Fatalf("expected empty refs, got %+v", got)
	}
}

// 任一下游层报错都不应阻断后续层。
func TestAttribute_InheritErrorFallsThroughToLLM(t *testing.T) {
	a := New(testProjects(),
		WithInherit(func(context.Context, Signals) (string, error) {
			return "", errors.New("redis down")
		}),
		WithLLM(func(context.Context, Signals, []Project) (string, string, error) {
			return "p-pms", "PMS", nil
		}),
	)
	if got := a.Attribute(context.Background(), Signals{UserText: "x"}); got.Method != MethodLLM {
		t.Fatalf("Method = %q, want llm after inherit error", got.Method)
	}
}

func TestAttribute_NoLLMConfiguredStaysZeroCost(t *testing.T) {
	a := New(testProjects())
	if got := a.Attribute(context.Background(), Signals{UserText: "unmatched"}); got.Found() {
		t.Fatalf("expected unresolved without LLM tier, got %+v", got)
	}
}

func TestAttribute_AutoConfirmRules(t *testing.T) {
	a := New(testProjects(), WithAutoConfirmRules(true))
	got := a.Attribute(context.Background(), Signals{UserText: "pms task"})
	if got.Status != StatusConfirmed {
		t.Fatalf("Status = %q, want confirmed", got.Status)
	}
}

func TestAttribute_EmptySignalsNoPanic(t *testing.T) {
	a := New(testProjects())
	if got := a.Attribute(context.Background(), Signals{}); got.Found() {
		t.Fatalf("expected unresolved for empty signals, got %+v", got)
	}
}

// 无项目维表时不得崩溃，也不得凭空产出归属。
func TestAttribute_NoProjectsConfigured(t *testing.T) {
	a := New(nil)
	if got := a.Attribute(context.Background(), Signals{UserText: "anything"}); got.Found() {
		t.Fatalf("expected unresolved with empty project_dim, got %+v", got)
	}
}
