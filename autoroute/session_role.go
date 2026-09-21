// session_role.go — R48（2026-09-20）会话角色识别：多子代理并行场景下，
// 从请求头识别发起方在代理层级中的角色（main/orchestrator/planner/worker），
// 供 role_llm_router 按「角色 × 任务类型」选择轻量/重量池 LLM。
//
// 信任模型（与 handoff §3.5 陷阱 #4 对齐）：
//  1. X-Gw-Agent-Role 是**客户端声明式提示**，与 X-Gw-Task-Hint /
//     X-Gw-Work-Type 同一信任级——不进 loopback.CorrelationHeaders 剥离
//     清单（否则外部编排客户端的子代理声明会被中间件剥掉，特性失效）。
//     伪造该头只影响伪造者自己的 auto 模式路由（等价于显式选模型），
//     不影响网关内部关联/可观测性字段。
//  2. X-Gw-Source-Actor 推断路径**不可伪造**：该头在 loopback 令牌校验
//     中间件（middleware/requestid_mw.go → StripUntrustedCorrelationHeaders，
//     R35-R1）里被剥离，到达本包时只可能是网关内部 loopback 发出的。
//     auto-title-generator / auto-summary-generator / session-summary 等
//     内部回环都是"摘要型子任务"，推断为 worker → 轻量池。
package autoroute

import (
	"context"
	"strings"
)

// AgentRole 是会话在代理层级中的角色标识。取值与 730 迁移
// sessions.agent_role 列的 CHECK 约束、role_task_llm_mapping.agent_role 列
// 保持一致，改动需同步三处 + AllAgentRoles。
type AgentRole string

const (
	// RoleMain 是用户直接交互的主会话（默认值，兼容存量行）。
	RoleMain AgentRole = "main"
	// RoleOrchestrator 是编排代理：调度子代理、汇总结果，少深度推理。
	RoleOrchestrator AgentRole = "orchestrator"
	// RolePlanner 是规划代理：产出计划/任务拆解，长文本一次生成。
	RolePlanner AgentRole = "planner"
	// RoleWorker 是子任务/子代理：搜索、总结、git 操作、运维等执行单元。
	RoleWorker AgentRole = "worker"
	// RoleUnknown 是未声明（外部客户端没带角色头且无法推断时的兜底，
	// role 路由不介入，行为与该特性存在之前完全一致）。
	RoleUnknown AgentRole = "unknown"
)

// AllAgentRoles 是合法角色全集（校验 + admin UI 用）。
var AllAgentRoles = []AgentRole{RoleMain, RoleOrchestrator, RolePlanner, RoleWorker, RoleUnknown}

// AgentRoleHeader 是客户端声明代理角色的请求头（声明式提示，见包注释）。
const AgentRoleHeader = "X-Gw-Agent-Role"

// sourceActorHeaderName 与 domains/streaming.autoSourceActorHeader 同名；
// 此处独立声明避免 autoroute → domains 的反向依赖（domains/streaming
// 已 import autoroute）。
const sourceActorHeaderName = "X-Gw-Source-Actor"

// ParseAgentRole 规范化并校验角色字符串：trim + 小写后比对枚举，
// 非法/空值返回 RoleUnknown（不报错——角色头是提示，坏值静默降级）。
func ParseAgentRole(raw string) AgentRole {
	s := strings.ToLower(strings.TrimSpace(raw))
	for _, r := range AllAgentRoles {
		if string(r) == s {
			return r
		}
	}
	return RoleUnknown
}

// loopbackActorRoles 是网关内部 loopback 组件 → 角色推断表。这些 actor
// 只可能来自带 loopback 令牌的内部自调用（外部伪造的 Source-Actor 头已被
// R35-R1 中间件剥离），因此推断结果可信。新增内部 loopback 组件时在此登记。
var loopbackActorRoles = map[string]AgentRole{
	"auto-title-generator":   RoleWorker, // 自动标题：摘要型轻任务
	"auto-summary-generator": RoleWorker, // 自动摘要：摘要型轻任务
	"session-summary":        RoleWorker, // 会话总结：摘要型轻任务
}

// InferRoleFromActor 按网关内部 loopback 的 Source-Actor 推断角色。
// 未登记的 actor 返回 RoleUnknown（调用方继续走默认路径）。
func InferRoleFromActor(actor string) AgentRole {
	a := strings.ToLower(strings.TrimSpace(actor))
	if r, ok := loopbackActorRoles[a]; ok {
		return r
	}
	return RoleUnknown
}

// ResolveAgentRoleFromHeaders 解析请求头的角色：
//  1. X-Gw-Agent-Role（客户端声明，主来源）；
//  2. 为空/非法时回退 X-Gw-Source-Actor 推断（仅网关内部 loopback 可信，
//     见包注释信任模型）。
//
// 返回 RoleUnknown 表示"无角色信息"，role 路由不介入。
// 两个入参由调用方（domains/streaming 的 maybeResolveAuto）从 http.Header
// 提取后传入；autoroute 不直接依赖 http.Header 以保持本文件可被
// 非 HTTP 上下文（测试/管理工具）复用。
func ResolveAgentRoleFromHeaders(roleRaw, actorRaw string) AgentRole {
	if r := ParseAgentRole(roleRaw); r != RoleUnknown {
		return r
	}
	return InferRoleFromActor(actorRaw)
}

// agentRoleCtxKey 携带已解析的会话角色穿透 context（供 sessions 表落库
// 路径等未来消费者读取；Decide 管线本身经 ClassificationSignals.AgentRole
// 传递，不经 context）。
type agentRoleCtxKey struct{}

// WithAgentRole returns a context carrying the resolved agent role.
func WithAgentRole(ctx context.Context, role AgentRole) context.Context {
	if ctx == nil || role == "" || role == RoleUnknown {
		return ctx
	}
	return context.WithValue(ctx, agentRoleCtxKey{}, role)
}

// AgentRoleFromContext reads the agent role set by WithAgentRole, or RoleUnknown.
func AgentRoleFromContext(ctx context.Context) AgentRole {
	if ctx == nil {
		return RoleUnknown
	}
	if v, ok := ctx.Value(agentRoleCtxKey{}).(AgentRole); ok {
		return v
	}
	return RoleUnknown
}
