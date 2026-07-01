package agentecosystem

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// ---------- Registry ----------

func TestRegistry_Register_Get(t *testing.T) {
	r := NewRegistry()
	a := &Agent{ID: "agent-1", Name: "test"}
	if err := r.Register(a); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok, err := r.Get("agent-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.Name != "test" {
		t.Fatalf("Name mismatch: %q", got.Name)
	}
	if r.Count() != 1 {
		t.Fatalf("Count=%d, want 1", r.Count())
	}
}

func TestRegistry_Get_NotFound(t *testing.T) {
	r := NewRegistry()
	got, ok, err := r.Get("missing")
	if err != nil {
		t.Fatalf("Get err: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false")
	}
	if got != nil {
		t.Fatalf("expected nil agent, got %+v", got)
	}
}

func TestRegistry_List(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&Agent{ID: "a"})
	_ = r.Register(&Agent{ID: "b"})
	_ = r.Register(&Agent{ID: "c"})
	list := r.List()
	if len(list) != 3 {
		t.Fatalf("List len=%d, want 3", len(list))
	}
}

func TestRegistry_FindByCapability_Hit(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&Agent{
		ID: "a1",
		Capabilities: []*Capability{
			{Name: "translate"},
			{Name: "summarize"},
		},
	})
	_ = r.Register(&Agent{
		ID:           "a2",
		Capabilities: []*Capability{{Name: "translate"}},
	})
	_ = r.Register(&Agent{
		ID:           "a3",
		Capabilities: []*Capability{{Name: "search"}},
	})
	hits := r.FindByCapability("translate")
	if len(hits) != 2 {
		t.Fatalf("hits=%d, want 2", len(hits))
	}
}

func TestRegistry_FindByCapability_Miss(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&Agent{ID: "a1", Capabilities: []*Capability{{Name: "x"}}})
	hits := r.FindByCapability("unknown")
	if len(hits) != 0 {
		t.Fatalf("expected empty hits, got %d", len(hits))
	}
}

func TestRegistry_Register_EmptyID(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&Agent{ID: ""}); err == nil {
		t.Fatal("expected error for empty ID")
	}
}

func TestRegistry_Register_NilAgent(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Fatal("expected error for nil agent")
	}
}

func TestRegistry_Unregister(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&Agent{ID: "x"})
	if !r.Unregister("x") {
		t.Fatal("expected unregister=true")
	}
	if r.Unregister("x") {
		t.Fatal("expected unregister=false on second call")
	}
	if r.Count() != 0 {
		t.Fatalf("Count=%d, want 0", r.Count())
	}
}

func TestRegistry_FindByTag(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&Agent{ID: "a", Tags: []string{"prod", "beta"}})
	_ = r.Register(&Agent{ID: "b", Tags: []string{"beta"}})
	_ = r.Register(&Agent{ID: "c", Tags: []string{"dev"}})
	hits := r.FindByTag("beta")
	if len(hits) != 2 {
		t.Fatalf("hits=%d, want 2", len(hits))
	}
}

// ---------- BehaviorAnalyzer ----------

func TestAnalyzer_Record_GetStats(t *testing.T) {
	a := NewBehaviorAnalyzer()
	now := time.Now()
	behaviors := []*Behavior{
		{AgentID: "a1", Action: "invoke", Success: true, LatencyMs: 100, RecordedAt: now},
		{AgentID: "a1", Action: "invoke", Success: true, LatencyMs: 200, RecordedAt: now.Add(time.Second)},
		{AgentID: "a1", Action: "fail", Success: false, LatencyMs: 50, RecordedAt: now.Add(2 * time.Second)},
	}
	for _, b := range behaviors {
		if err := a.Record(b); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	stats := a.GetStats("a1")
	if stats.TotalCount != 3 {
		t.Fatalf("TotalCount=%d", stats.TotalCount)
	}
	if stats.SuccessCount != 2 || stats.FailureCount != 1 {
		t.Fatalf("SuccessCount=%d FailureCount=%d", stats.SuccessCount, stats.FailureCount)
	}
	if stats.AvgLatencyMs != 116 { // (100+200+50)/3
		t.Fatalf("AvgLatencyMs=%d, want 116", stats.AvgLatencyMs)
	}
}

func TestAnalyzer_GetStats_NoRecords(t *testing.T) {
	a := NewBehaviorAnalyzer()
	stats := a.GetStats("ghost")
	if stats.TotalCount != 0 || stats.AgentID != "ghost" {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if rate := stats.SuccessRate(); rate != 0 {
		t.Fatalf("SuccessRate=%f, want 0", rate)
	}
}

func TestAnalyzer_SuccessRate(t *testing.T) {
	s := &Stats{TotalCount: 10, SuccessCount: 7, FailureCount: 3}
	if got := s.SuccessRate(); got != 0.7 {
		t.Fatalf("SuccessRate=%f", got)
	}
	if got := s.FailureRate(); got != 0.3 {
		t.Fatalf("FailureRate=%f", got)
	}

	zero := &Stats{}
	if zero.SuccessRate() != 0 || zero.FailureRate() != 0 {
		t.Fatal("zero stats should return 0 rates")
	}
}

func TestAnalyzer_Record_Validations(t *testing.T) {
	a := NewBehaviorAnalyzer()
	if err := a.Record(nil); err == nil {
		t.Fatal("expected error for nil behavior")
	}
	if err := a.Record(&Behavior{}); err == nil {
		t.Fatal("expected error for empty AgentID")
	}
}

func TestAnalyzer_TopFailingActions(t *testing.T) {
	a := NewBehaviorAnalyzer()
	now := time.Now()
	_ = a.Record(&Behavior{AgentID: "x", Action: "invoke", Success: false, RecordedAt: now})
	_ = a.Record(&Behavior{AgentID: "x", Action: "invoke", Success: false, RecordedAt: now})
	_ = a.Record(&Behavior{AgentID: "x", Action: "timeout", Success: false, RecordedAt: now})
	_ = a.Record(&Behavior{AgentID: "x", Action: "invoke", Success: true, RecordedAt: now})

	top := a.TopFailingActions("x", 10)
	if len(top) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(top))
	}
	if top[0].Action != "invoke" || top[0].Count != 2 {
		t.Fatalf("expected invoke=2 first, got %+v", top[0])
	}
}

func TestAnalyzer_GetBehaviors_CopyIsolated(t *testing.T) {
	a := NewBehaviorAnalyzer()
	_ = a.Record(&Behavior{AgentID: "x", Action: "a", Success: true})
	got := a.GetBehaviors("x")
	if len(got) != 1 {
		t.Fatalf("len=%d", len(got))
	}
	// 修改副本不应影响内部存储
	got[0].Action = "modified"
	again := a.GetBehaviors("x")
	if again[0].Action != "a" {
		t.Fatalf("internal mutated: %q", again[0].Action)
	}
}

func TestAnalyzer_Reset(t *testing.T) {
	a := NewBehaviorAnalyzer()
	_ = a.Record(&Behavior{AgentID: "x", Action: "a", Success: true})
	a.Reset("x")
	if len(a.GetBehaviors("x")) != 0 {
		t.Fatal("expected empty after Reset")
	}
}

// ---------- AgentDiscoveryHook ----------

func TestHook_Name_Priority(t *testing.T) {
	h := NewAgentDiscoveryHook(NewRegistry())
	if h.Name() != "agent.discover" {
		t.Fatalf("Name=%q", h.Name())
	}
	if h.Priority() != 200 {
		t.Fatalf("Priority=%d", h.Priority())
	}
}

func TestHook_Enabled(t *testing.T) {
	h := NewAgentDiscoveryHook(NewRegistry())
	if !h.Enabled(context.Background(), &domain.PipelineRequest{}) {
		t.Fatal("expected Enabled=true with env")
	}
	if h.Enabled(context.Background(), nil) {
		t.Fatal("expected Enabled=false with nil env")
	}
}

func TestHook_Execute_InjectsDiscoveredAgents(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&Agent{
		ID:           "translator",
		Capabilities: []*Capability{{Name: "translate"}},
	})
	_ = r.Register(&Agent{
		ID:           "summarizer",
		Capabilities: []*Capability{{Name: "translate"}},
	})
	h := NewAgentDiscoveryHook(r)

	env := &domain.PipelineRequest{
		Metadata: map[string]any{
			MetaKeyRequiredCapability: "translate",
		},
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	raw, ok := env.Metadata[MetaKeyDiscoveredAgents]
	if !ok {
		t.Fatal("expected discovered_agents key")
	}
	agents, ok := raw.([]*Agent)
	if !ok {
		t.Fatalf("expected []*Agent, got %T", raw)
	}
	if len(agents) != 2 {
		t.Fatalf("agents=%d, want 2", len(agents))
	}
}

func TestHook_Execute_NoRequiredCapability_Skips(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&Agent{ID: "a", Capabilities: []*Capability{{Name: "x"}}})
	h := NewAgentDiscoveryHook(r)

	env := &domain.PipelineRequest{Metadata: map[string]any{}}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, ok := env.Metadata[MetaKeyDiscoveredAgents]; ok {
		t.Fatal("expected no discovered_agents when capability missing")
	}
}

func TestHook_Execute_NoMatchingAgent_Skips(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&Agent{ID: "a", Capabilities: []*Capability{{Name: "x"}}})
	h := NewAgentDiscoveryHook(r)

	env := &domain.PipelineRequest{
		Metadata: map[string]any{MetaKeyRequiredCapability: "unknown"},
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, ok := env.Metadata[MetaKeyDiscoveredAgents]; ok {
		t.Fatal("expected no discovered_agents when no match")
	}
}

func TestHook_OnError_Swallows(t *testing.T) {
	h := NewAgentDiscoveryHook(NewRegistry())
	// 即使给一个奇怪的 env, OnError 也应吞掉错误
	err := h.OnError(context.Background(), &domain.PipelineRequest{}, context.Canceled)
	if err != nil {
		t.Fatalf("OnError returned: %v", err)
	}
}

func TestHook_ImplementsPipelineHook(t *testing.T) {
	var _ interface {
		Name() string
		Execute(context.Context, *domain.PipelineRequest) error
		Priority() int
		Enabled(context.Context, *domain.PipelineRequest) bool
		OnError(context.Context, *domain.PipelineRequest, error) error
	} = (*AgentDiscoveryHook)(nil)
}
