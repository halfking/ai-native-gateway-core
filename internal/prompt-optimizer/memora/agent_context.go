// agent_context.go —— memora 平台集成面（/api/v2/agent-context）客户端方法。
//
// 2026-11-27 P1-2（目标4 闭环）：网关 contextagent 的 L2 长期层此前只能走
// /api/v2/memories/search 单条语义召回；memora 的 agent-context 面一次返回
// 分层上下文（L1 会话短期 / L2 语义事实 / L3 持久知识 / knowledge），并由
// memora 侧做 project ACL 判定（actor_id = 本客户端的 ActorID，需为项目
// owner 或在 project_acl 中有授权）。
//
// 契约来源：ai-platform/memora internal/integration/types.go
// AgentContextRequest / AgentContextResponse（contracts/integration-routes.json
// 在册端点）。403 = ACL 拒绝；503 = ACL 存储不可用（fail-closed，调用方应降级）。
package memora

import (
	"context"
	"fmt"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domains/memory"
)

// AgentContextRequest 对应 memora AgentContextRequest（仅网关用得到的字段）。
type AgentContextRequest struct {
	AgentID   string   `json:"agent_id"`
	UserID    string   `json:"user_id"`
	ProjectID string   `json:"project_id"`
	TaskID    string   `json:"task_id,omitempty"`
	Layers    []string `json:"layers,omitempty"`
	MaxTokens int      `json:"max_tokens,omitempty"`
}

// AgentContextMemory 是分层上下文里的一条记忆（memora MemoryItem 的投影）。
type AgentContextMemory struct {
	ID      string   `json:"id"`
	Content string   `json:"content"`
	Layer   string   `json:"layer"`
	Tags    []string `json:"tags,omitempty"`
	Score   float64  `json:"score,omitempty"`
}

// AgentContextKnowledge 是 knowledge 层的一条条目。
type AgentContextKnowledge struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Summary string   `json:"summary,omitempty"`
	Content string   `json:"content,omitempty"`
	Score   float64  `json:"score,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

// AgentContextResponse 对应 memora AgentContextResponse。
type AgentContextResponse struct {
	Context struct {
		L1Memories     []AgentContextMemory   `json:"L1_memories"`
		L2Episodic     []AgentContextMemory   `json:"L2_episodic"`
		L3Consolidated []AgentContextMemory   `json:"L3_consolidated"`
		Knowledge      []AgentContextKnowledge `json:"knowledge"`
	} `json:"context"`
	TokensUsed int    `json:"tokens_used"`
	ManifestID string `json:"manifest_id"`
	Truncated  bool   `json:"truncated"`
}

// AgentContext 调 memora POST /api/v2/agent-context。
//
// projectID 覆盖客户端默认 project scope（agent-context 的 ACL 按请求体
// project_id 判定，X-Project-ID 头保持与之一致）。
func (c *Client) AgentContext(ctx context.Context, req AgentContextRequest) (*AgentContextResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("memora: client is nil")
	}
	if req.ProjectID == "" {
		req.ProjectID = c.projectID
	}
	if req.AgentID == "" {
		req.AgentID = "llm-gateway-contextagent"
	}

	var resp AgentContextResponse
	headers := map[string]string{"X-Project-ID": req.ProjectID}
	// memora 集成面直接返回 AgentContextResponse 顶层（无 data envelope，
	// 与 memories/search 的 {data:{...}} 包装不同——契约见
	// memora internal/integration/handler.go GetAgentContext）。
	if err := c.do(ctx, http.MethodPost, "/api/v2/agent-context", req, headers, &resp); err != nil {
		return nil, fmt.Errorf("memora: agent-context failed: %w", err)
	}
	return &resp, nil
}

// AgentContextAsMemories 把分层响应压平为 L2 长期层可用的 memory.Memory 切片
// （L2 → L3 → L1 顺序，供 LongTermLoader 直接消费；knowledge 不压——它的
// 结构不是 memory.Memory 形状，调用方需要时应走独立通道）。
func AgentContextAsMemories(resp *AgentContextResponse) []memory.Memory {
	if resp == nil {
		return nil
	}
	out := make([]memory.Memory, 0,
		len(resp.Context.L2Episodic)+len(resp.Context.L3Consolidated)+len(resp.Context.L1Memories))
	out = append(out, flattenMemories(resp.Context.L2Episodic)...)
	out = append(out, flattenMemories(resp.Context.L3Consolidated)...)
	out = append(out, flattenMemories(resp.Context.L1Memories)...)
	return out
}

func flattenMemories(items []AgentContextMemory) []memory.Memory {
	out := make([]memory.Memory, 0, len(items))
	for _, m := range items {
		if m.Content == "" {
			continue
		}
		out = append(out, memory.Memory{ID: m.ID, Text: m.Content, Tags: m.Tags, Score: m.Score})
	}
	return out
}
