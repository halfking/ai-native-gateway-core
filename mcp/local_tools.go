package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
)

// LocalTool 是**由网关自己执行**的工具，不由上游适配器执行。
//
// ★ 为什么需要它（现状缺口）：
//
//	mcp 包的 tools/call 只有一个出口 —— g.upstream.Call，且强制要求
//	一条 Adapter 记录（resolveTool 在没有适配器时直接 ErrNotFound）。
//	tools/list 也只镜像上游清单。
//	⇒ 网关自有的能力（例如「按步取回会话记忆」「语义召回长期记忆」）
//	  **既无法注册、也无法被列出、更无法被调用**。
//
// ★ 本文件补上这个缺口：本地工具注册表 + 一条本地派发分支。
//
//	零值（未注册任何本地工具）⇒ 行为与改动前完全一致。
//
// ★ 治理不被绕过：本地工具**同样**走授权链（scope → 可见性 →
//
//	key×tool 允许表（deny-wins）→ 敏感度审批 → 参数白名单/JSON Schema），
//	只是「适配器允许表 / 绑定 / 限流 / 熔断」这四道与上游有关的闸不适用。
type LocalTool struct {
	// Name 对外暴露的工具名（全局唯一）。
	Name string
	// Description 描述（进入客户端上下文）。
	Description string
	// InputSchema JSON Schema（入参白名单与校验依据）。
	InputSchema json.RawMessage
	// AllowedParams 参数白名单：非空时只允许这些参数出现在调用参数里。
	AllowedParams []string
	// Visibility 可见性（默认 public；private 时按 AgentID 收口）。
	Visibility Visibility
	// AgentID 私有工具归属的智能体。
	AgentID string
	// Sensitivity 敏感度分级（high ⇒ 需要审批凭据）。
	Sensitivity Sensitivity
	// RequireApproval 是否必须携带已批准的审批凭据才能调用。
	RequireApproval bool
	// Class 调用类别（限流档位；本地工具不走限流，保留字段以便统计口径一致）。
	Class ToolClass
	// Group 工具分组名。
	Group string
	// Handler 执行工具。args 是已解析的参数。
	//
	// ★ Handler 必须自己处理错误并返回**给模型看**的错误文本；
	//   网关不会把内部错误细节直接抛给客户端。
	Handler func(ctx context.Context, id *Identity, args map[string]any) (any, error)
}

// LocalToolRegistry 保存网关本地工具。
type LocalToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]LocalTool
}

// NewLocalToolRegistry 构造本地工具注册表。
func NewLocalToolRegistry() *LocalToolRegistry {
	return &LocalToolRegistry{tools: map[string]LocalTool{}}
}

// Register 注册一个本地工具（同名覆盖）。
func (r *LocalToolRegistry) Register(t LocalTool) {
	if r == nil || t.Name == "" || t.Handler == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = map[string]LocalTool{}
	}
	r.tools[t.Name] = t
}

// Get 取一个本地工具。
func (r *LocalToolRegistry) Get(name string) (LocalTool, bool) {
	if r == nil {
		return LocalTool{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List 列出全部本地工具（按名字排序）。
func (r *LocalToolRegistry) List() []LocalTool {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]LocalTool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// localTool 取本地工具（nil 安全）。
func (g *Gateway) localTool(name string) (LocalTool, bool) {
	if g == nil || g.cfg.LocalTools == nil {
		return LocalTool{}, false
	}
	return g.cfg.LocalTools.Get(name)
}

// asTool 把本地工具投影成既有治理链认识的 *Tool 形态。
//
// ★ 目的：**复用**既有的可见性判定（toolVisible）、允许表判定
//
//	（keyAllowsTool）、审批判定（checkApproval）与参数校验
//	（validateParams）—— 不另立一套口径，避免治理分叉。
func (t LocalTool) asTool(tenantID string) *Tool {
	vis := t.Visibility
	if vis == "" {
		vis = VisibilityPublic
	}
	return &Tool{
		Name:            t.Name,
		Adapter:         "", // 本地工具没有适配器
		TenantID:        tenantID,
		Description:     t.Description,
		InputSchema:     t.InputSchema,
		Visibility:      vis,
		AgentID:         t.AgentID,
		Sensitivity:     t.Sensitivity,
		Class:           t.Class,
		AllowedParams:   t.AllowedParams,
		RequireApproval: t.RequireApproval,
		Enabled:         true,
		Group:           t.Group,
	}
}

// handleLocalToolCall 执行一次本地工具调用。
//
// ★ 返回形状与上游一致：{"content":[{"type":"text","text":...}]}，
//
//	这样 MCP 客户端不需要区分「本地工具」与「上游工具」。
func (g *Gateway) handleLocalToolCall(
	ctx context.Context,
	id *Identity,
	r *http.Request,
	lt LocalTool,
	args map[string]any,
) (json.RawMessage, *RPCError, int) {
	tool := lt.asTool(id.TenantID)

	// ── 授权链（与上游工具同序，只跳过与上游有关的四道闸）──
	if !id.HasScope(ScopeExecute) {
		authzDeniedTotal.WithLabelValues("scope").Inc()
		return nil, Errorf(CodeToolNotAllowed, "missing scope %s", ScopeExecute), http.StatusForbidden
	}
	if !toolVisible(tool, id) {
		authzDeniedTotal.WithLabelValues("visibility").Inc()
		g.auditToolDenied(id, tool.Name, "visibility")
		return nil, Errorf(CodeToolNotAllowed, "tool is not visible to this principal"), http.StatusForbidden
	}
	toolAllowChecksTotal.Inc()
	if k := g.keyOf(id); k != nil && len(k.AllowedTools) == 0 && len(k.DeniedTools) == 0 {
		toolAllowNotEnforcedTotal.Inc()
	}
	if !keyAllowsTool(g.keyOf(id), tool.Name) {
		authzDeniedTotal.WithLabelValues("tool_allow").Inc()
		toolAllowResultTotal.WithLabelValues("denied").Inc()
		toolAllowDeniedTotal.Inc()
		g.auditToolDenied(id, tool.Name, "tool_allow")
		return nil, Errorf(CodeToolNotAllowed, "tool is not in the allowed list"), http.StatusForbidden
	}
	toolAllowResultTotal.WithLabelValues("allowed").Inc()

	if tool.RequireApproval || tool.Sensitivity == SensitivityHigh {
		if err := g.checkApproval(r, id, tool); err != nil {
			approvalsRequiredTotal.Inc()
			g.auditToolDenied(id, tool.Name, "approval_required")
			return nil, Errorf(CodeApprovalRequired, "%s", err.Error()), http.StatusForbidden
		}
	}

	if err := validateParams(tool, args); err != nil {
		authzDeniedTotal.WithLabelValues("params").Inc()
		g.auditToolDenied(id, tool.Name, "invalid_params")
		return nil, RPCErrorOf(err), http.StatusForbidden
	}

	// ── 执行 ──
	result, err := lt.Handler(ctx, id, args)
	if err != nil {
		g.audit.Record(AuditEvent{
			Kind:     AuditKindUpstream,
			Outcome:  OutcomeError,
			TenantID: id.TenantID,
			OrgID:    id.OrgID,
			UserID:   id.UserID,
			KeyID:    id.KeyID,
			Tool:     lt.Name,
			Reason:   "local_tool_error",
		})
		return nil, Errorf(CodeUpstreamError, "local tool %q failed: %s", lt.Name, err.Error()), http.StatusInternalServerError
	}

	text := renderLocalToolResult(result)
	g.audit.Record(AuditEvent{
		Kind:     AuditKindUpstream,
		Outcome:  OutcomeAllowed,
		TenantID: id.TenantID,
		OrgID:    id.OrgID,
		UserID:   id.UserID,
		KeyID:    id.KeyID,
		Tool:     lt.Name,
		Reason:   "local_tool",
	})
	raw, _ := json.Marshal(map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": false,
	})
	return raw, nil, http.StatusOK
}

// renderLocalToolResult 把本地工具返回值渲染成 MCP 文本内容。
func renderLocalToolResult(result any) string {
	switch v := result.(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	case json.RawMessage:
		return string(v)
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(raw)
	}
}

// localToolViews 把本地工具投影成 tools/list 的条目。
func (g *Gateway) localToolViews(id *Identity) []ToolView {
	if g == nil || g.cfg.LocalTools == nil {
		return nil
	}
	var out []ToolView
	for _, lt := range g.cfg.LocalTools.List() {
		tool := lt.asTool(id.TenantID)
		if !toolVisible(tool, id) {
			continue
		}
		if !keyAllowsTool(g.keyOf(id), tool.Name) {
			continue
		}
		meta := map[string]any{
			"sensitivity": string(tool.Sensitivity),
			"scope":       "local",
		}
		if tool.Group != "" {
			meta["group"] = tool.Group
		}
		out = append(out, ToolView{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
			Annotations: map[string]any{"readOnly": true},
			Meta:        meta,
		})
	}
	return out
}
