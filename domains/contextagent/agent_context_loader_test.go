package contextagent

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/memory"
	poptimizermemora "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/memora"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFallbackClient 是最小 Client 替身：AgentContext 始终返回固定分层结果。
type fakeFallbackClient struct{ poptimizermemora.Client }

func TestAgentContextLongTermLoader_NilClientReturnsNil(t *testing.T) {
	assert.Nil(t, NewAgentContextLongTermLoader(nil, nil, "proj-1"))
	assert.Nil(t, NewAgentContextLongTermLoader(&poptimizermemora.Client{}, nil, ""))
	// Disabled client（零值 c==nil 判断经由 Disabled）⇒ 不构造。
	assert.Nil(t, NewAgentContextLongTermLoader(nil, stubFallback{}, "proj-1"))
}

type stubFallback struct{}

func (stubFallback) Recall(_ context.Context, _, _ string, _ int) ([]memory.Memory, error) {
	return []memory.Memory{{ID: "fb-1", Text: "fallback hit"}}, nil
}

func TestAgentContextLongTermLoader_NilReceiverSafe(t *testing.T) {
	var l *AgentContextLongTermLoader
	got, err := l.Recall(context.Background(), "user", "query", 5)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestAgentContextLongTermLoader_EmptyQueryShortCircuit(t *testing.T) {
	l := &AgentContextLongTermLoader{Client: &poptimizermemora.Client{}}
	got, err := l.Recall(context.Background(), "user", "  ", 5)
	require.NoError(t, err)
	assert.Nil(t, got, "空查询不发起任何请求")
}

func TestAgentContextLongTermLoader_NoFallbackPropagatesError(t *testing.T) {
	// Client 非nil 但请求会失败（不可达地址）；无 Fallback ⇒ 错误上抛。
	l := &AgentContextLongTermLoader{
		Client:    newBrokenClient(t),
		ProjectID: "proj-1",
	}
	_, err := l.Recall(context.Background(), "user", "query", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent-context")
}

func TestAgentContextLongTermLoader_FallbackPathTypeCheck(t *testing.T) {
	// Fallback 契约：字段存在且为 LongTermLoader（真实降级行为已在
	// memora 包测过 APIError 形态；这里锁字段接线）。
	l := NewAgentContextLongTermLoader(newBrokenClient(t), stubFallback{}, "proj-1")
	require.NotNil(t, l)
	var _ LongTermLoader = l
	assert.Equal(t, "proj-1", l.ProjectID)
	assert.Equal(t, "llm-gateway-contextagent", l.AgentID)
}

func newBrokenClient(t *testing.T) *poptimizermemora.Client {
	t.Helper()
	// 指向必然拒绝连接的本地端口。
	c, err := poptimizermemora.New(poptimizermemora.Config{
		BaseURL:   "http://127.0.0.1:1",
		JWTSecret: "test-secret-agent-context-loader",
		ProjectID: "proj-1",
	})
	require.NoError(t, err)
	return c
}

