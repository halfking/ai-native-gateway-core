package routing

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRoundRobin_SingleCandidate 验证单候选场景。
func TestRoundRobin_SingleCandidate(t *testing.T) {
	r := NewRoundRobinRouter()
	c := &Candidate{CredentialID: "c1", Provider: "openai", Model: "gpt-4"}

	decision, err := r.Route(Context{Candidates: []*Candidate{c}})
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, "c1", decision.Selected.CredentialID)
	assert.Equal(t, "round_robin", decision.Strategy)
}

// TestRoundRobin_MultipleCandidates 验证多候选循环分布。
//
// 跑 6 次选取 3 个 candidate，预期每个被选中 2 次。
func TestRoundRobin_MultipleCandidates(t *testing.T) {
	r := NewRoundRobinRouter()
	c1 := &Candidate{CredentialID: "c1", Provider: "openai"}
	c2 := &Candidate{CredentialID: "c2", Provider: "openai"}
	c3 := &Candidate{CredentialID: "c3", Provider: "openai"}
	cands := []*Candidate{c1, c2, c3}

	counts := map[string]int{}
	for i := 0; i < 6; i++ {
		d, err := r.Route(Context{Candidates: cands})
		require.NoError(t, err)
		require.NotNil(t, d)
		counts[d.Selected.CredentialID]++
	}
	assert.Equal(t, 2, counts["c1"], "c1 should be selected twice in 6 rounds")
	assert.Equal(t, 2, counts["c2"], "c2 should be selected twice in 6 rounds")
	assert.Equal(t, 2, counts["c3"], "c3 should be selected twice in 6 rounds")
}

// TestRoundRobin_NoCandidates 验证空候选返回 nil。
func TestRoundRobin_NoCandidates(t *testing.T) {
	r := NewRoundRobinRouter()
	d, err := r.Route(Context{Candidates: nil})
	require.NoError(t, err)
	assert.Nil(t, d, "empty candidates should yield nil decision")
}

// TestSticky_HitsPreferred 验证命中 preferred_credential。
func TestSticky_HitsPreferred(t *testing.T) {
	fallback := NewRoundRobinRouter()
	r := NewStickyRouter(fallback)

	c1 := &Candidate{CredentialID: "c1", Provider: "openai"}
	c2 := &Candidate{CredentialID: "c2", Provider: "openai"}
	cands := []*Candidate{c1, c2}

	ctx := Context{
		Candidates: cands,
		Metadata:   map[string]any{"preferred_credential": "c2"},
	}
	d, err := r.Route(ctx)
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, "c2", d.Selected.CredentialID, "sticky should hit preferred")
	assert.Equal(t, "sticky", d.Strategy)
}

// TestSticky_FallbackWhenPreferredMissing 验证 preferred 不在候选列表时回退。
func TestSticky_FallbackWhenPreferredMissing(t *testing.T) {
	fallback := NewRoundRobinRouter()
	r := NewStickyRouter(fallback)

	c1 := &Candidate{CredentialID: "c1", Provider: "openai"}
	cands := []*Candidate{c1}

	ctx := Context{
		Candidates: cands,
		Metadata:   map[string]any{"preferred_credential": "non-existent"},
	}
	d, err := r.Route(ctx)
	require.NoError(t, err)
	require.NotNil(t, d, "fallback should still produce a decision")
	assert.Equal(t, "c1", d.Selected.CredentialID)
	assert.Equal(t, "round_robin", d.Strategy, "should fall back to round_robin")
}

// TestSticky_NoPreferred 验证无 preferred 时回退。
func TestSticky_NoPreferred(t *testing.T) {
	fallback := NewRoundRobinRouter()
	r := NewStickyRouter(fallback)

	c1 := &Candidate{CredentialID: "c1", Provider: "openai"}
	ctx := Context{Candidates: []*Candidate{c1}, Metadata: map[string]any{}}

	d, err := r.Route(ctx)
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, "round_robin", d.Strategy)
}

// TestSticky_PreferredEmpty 验证 preferred_credential 为空字符串时回退。
func TestSticky_PreferredEmpty(t *testing.T) {
	fallback := NewRoundRobinRouter()
	r := NewStickyRouter(fallback)
	c1 := &Candidate{CredentialID: "c1", Provider: "openai"}

	ctx := Context{
		Candidates: []*Candidate{c1},
		Metadata:   map[string]any{"preferred_credential": ""},
	}
	d, err := r.Route(ctx)
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, "round_robin", d.Strategy, "empty string should not be sticky")
}

// TestRoutingHook_FillsSelectedCredential 验证 Hook 成功填充 SelectedCredential。
func TestRoutingHook_FillsSelectedCredential(t *testing.T) {
	router := NewStickyRouter(NewRoundRobinRouter())
	hook := NewRoutingHook(router)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.TenantID = "tenant-1"
	env.Metadata = map[string]any{
		"candidates": []*Candidate{
			{CredentialID: "cred-A", Provider: "openai", Model: "gpt-4"},
			{CredentialID: "cred-B", Provider: "anthropic", Model: "claude-3"},
		},
	}

	err := hook.Execute(context.Background(), env)
	require.NoError(t, err)
	require.NotNil(t, env.SelectedCredential)
	assert.Equal(t, "cred-A", env.SelectedCredential.ID, "first candidate should be selected")
	assert.Equal(t, "openai", env.SelectedCredential.Provider)
}

// TestRoutingHook_EmptyCandidates 验证空候选不报错。
func TestRoutingHook_EmptyCandidates(t *testing.T) {
	router := NewRoundRobinRouter()
	hook := NewRoutingHook(router)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata = map[string]any{} // 没有 candidates

	err := hook.Execute(context.Background(), env)
	require.NoError(t, err, "empty candidates should not error")
	assert.Nil(t, env.SelectedCredential, "no candidates → no selection")
}

// TestRoutingHook_NilMetadata 验证 metadata 为 nil 时不 panic。
func TestRoutingHook_NilMetadata(t *testing.T) {
	router := NewRoundRobinRouter()
	hook := NewRoutingHook(router)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	// env.Metadata 是 nil

	err := hook.Execute(context.Background(), env)
	require.NoError(t, err)
	assert.Nil(t, env.SelectedCredential)
}

// TestRoutingHook_SkipsWhenAlreadySelected 验证已选时跳过。
func TestRoutingHook_SkipsWhenAlreadySelected(t *testing.T) {
	router := NewRoundRobinRouter()
	hook := NewRoutingHook(router)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.SelectedCredential = &domain.PipelineCredential{ID: "preset", Provider: "preset"}

	enabled := hook.Enabled(context.Background(), env)
	assert.False(t, enabled, "should be disabled when SelectedCredential is already set")
}

// TestRoutingHook_PropagatesRouterError 验证 router 错误被透传。
func TestRoutingHook_PropagatesRouterError(t *testing.T) {
	expectedErr := errors.New("router boom")
	failingRouter := &failingRouter{err: expectedErr}
	hook := NewRoutingHook(failingRouter)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	err := hook.Execute(context.Background(), env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "router boom")

	// OnError 透传
	propagated := hook.OnError(context.Background(), env, err)
	assert.Error(t, propagated, "OnError should propagate the error")
}

// TestRoutingHook_FillsMetadata 验证路由后填充 latency/strategy metadata。
func TestRoutingHook_FillsMetadata(t *testing.T) {
	router := NewStickyRouter(NewRoundRobinRouter())
	hook := NewRoutingHook(router)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata = map[string]any{
		"candidates": []*Candidate{
			{CredentialID: "cred-X", Provider: "openai"},
		},
		"preferred_credential": "cred-X",
	}
	err := hook.Execute(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, "sticky", env.Metadata["routing_strategy"])
	_, hasLatency := env.Metadata["routing_latency_ms"]
	assert.True(t, hasLatency, "latency should be recorded")
}

// failingRouter 始终返回错误的 Router（用于测试错误路径）。
type failingRouter struct {
	err error
}

func (r *failingRouter) Route(ctx Context) (*Decision, error) {
	return nil, r.err
}
