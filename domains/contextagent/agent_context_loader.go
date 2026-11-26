// agent_context_loader.go —— L2 长期层的 agent-context 升级通道（P1-2）。
//
// 与 MemoraLongTermLoader（单条 /api/v2/memories/search 语义召回）的分工：
//
//	本 loader 走 memora 的 POST /api/v2/agent-context，一次拿回分层上下文
//	（L1 会话短期 / L2 语义事实 / L3 持久知识），且 project ACL 由 memora
//	侧判定（actor_id 是授权主体）。配置了 AgentContext 客户端时优先用它；
//	调用失败降级到 Fallback（既有检索通道），不因升级通道故障拖垮召回。
//
// namespace 即 memora 的 user_id 语义域（与 MemoraLongTermLoader 一致）。
package contextagent

import (
	"context"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/memory"
	poptimizermemora "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/memora"
)

// AgentContextLongTermLoader 用 memora 的 agent-context 分层面做 L2 召回。
type AgentContextLongTermLoader struct {
	// Client 是 memora v2 客户端（nil ⇒ 本 loader 不可用）。
	Client *poptimizermemora.Client
	// Fallback 是升级通道不可用/失败时的降级召回（通常为 MemoraLongTermLoader）。
	Fallback LongTermLoader
	// ProjectID 是 agent-context 请求的项目作用域。
	ProjectID string
	// AgentID 标识调用方智能体（memora 侧审计/追踪用）。
	AgentID string
	// Layers 请求的分层（nil ⇒ memora 默认 ["L1","L2","L3","knowledge"]）。
	Layers []string
	// MaxTokens 上下文预算（0 ⇒ memora 默认不限）。
	MaxTokens int
}

// NewAgentContextLongTermLoader 构造 agent-context 长期层加载器。
// client 为 nil 或已禁用 ⇒ 返回 nil（调用方回落 MemoraLongTermLoader）。
func NewAgentContextLongTermLoader(client *poptimizermemora.Client, fallback LongTermLoader, projectID string) *AgentContextLongTermLoader {
	if client == nil || client.Disabled() || strings.TrimSpace(projectID) == "" {
		return nil
	}
	return &AgentContextLongTermLoader{
		Client:    client,
		Fallback:  fallback,
		ProjectID: projectID,
		AgentID:   "llm-gateway-contextagent",
	}
}

// Recall 实现 LongTermLoader：agent-context 分层召回，失败降级 Fallback。
func (l *AgentContextLongTermLoader) Recall(ctx context.Context, namespace, query string, topK int) ([]memory.Memory, error) {
	if l == nil || l.Client == nil || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	resp, err := l.Client.AgentContext(ctx, poptimizermemora.AgentContextRequest{
		UserID:    namespace,
		ProjectID: l.ProjectID,
		AgentID:   l.AgentID,
		Layers:    l.Layers,
		MaxTokens: l.MaxTokens,
	})
	if err != nil {
		if l.Fallback != nil {
			slog.Warn("context agent: agent-context recall failed, falling back to memora search",
				"error", err)
			return l.Fallback.Recall(ctx, namespace, query, topK)
		}
		return nil, err
	}
	return poptimizermemora.AgentContextAsMemories(resp), nil
}
