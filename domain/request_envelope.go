// Package domain 提供领域驱动重构的核心抽象。
//
// request_envelope.go 在保留现有 RequestEnvelope 的基础上，新增
// PipelineRequest —— 一个在 Hook Pipeline 各阶段之间流转的"信封"。
//
// 设计动机：
//   - 现有 RequestEnvelope（见 envelope.go）按"领域"切分上下文
//     （Transport/Security/Tenant/...），是 transport/ 包的事实标准。
//   - Hook Pipeline 需要一个更扁平的、面向"请求生命周期"的载体：
//     Pipeline 关心的瞬态字段（Error、Metadata、StatusCode、字节流等）。
//   - 为不破坏现有代码（envelope.go、builder.go、transport/* 都依赖
//     现有 RequestEnvelope），本文件不修改 RequestEnvelope，而是新增
//     PipelineRequest 嵌入 *RequestEnvelope，并叠加 Pipeline 字段。
package domain

import (
	"context"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain/governance"
)

// PipelineRequest 是 Hook Pipeline 的统一载体。
//
// 它嵌入 *RequestEnvelope，因此可直接访问所有领域上下文（Transport、
// Security、Tenant、...），同时携带 Pipeline 自己的瞬态字段。
type PipelineRequest struct {
	// Envelope 原始领域信封（可为 nil，便于纯 Pipeline 单元测试）
	Envelope *RequestEnvelope

	// TenantID 租户 ID
	TenantID string
	// SessionID 会话 ID
	SessionID string

	// ClientIdentity 客户端识别信息（identity 领域填充）
	ClientIdentity *PipelineClientIdentity

	// APIKey 解析后的 API key 对象
	APIKey *PipelineAPIKey

	// Authenticated 是否已认证
	Authenticated bool

	// SelectedCredential 选中的凭据（routing 阶段填充）
	SelectedCredential *PipelineCredential
	// SelectedProvider 选中的 provider（routing 阶段填充）
	SelectedProvider *PipelineProvider

	// TransformedRequest 转换后的请求体
	TransformedRequest []byte
	// UpstreamResponse 上游响应
	UpstreamResponse []byte
	// FinalResponse 最终响应
	FinalResponse []byte

	// StatusCode HTTP 状态码
	StatusCode int
	// Error 处理过程中的错误
	Error error
	// Metadata 跨阶段元数据（请求内 hook 间共享总线）。
	//
	// 已登记的共享 key 契约（写者 → 读者，2026-07-09 整理）：
	//
	//   "audit_result" (*sessionaudit.DetectResult)
	//     写：sessionaudit.SessionAuditHook.Execute（PreRouting）
	//     读：approval_gate.go / approval_hook.go / cache_update_hook.go
	//
	//   "pii_stripped" (bool)
	//     写：output_compliance 脱敏步骤（OutputComplianceInterceptor.Metadata）
	//     读：cache_update_hook.extractPIIStripped → SessionState.PIIStripped
	//
	//   "output_compliance_result" (map)、"output_compliance_redacted" (bool)、
	//   "output_compliance_error" (string)
	//     写：outputcompliance.Hook.Execute（PostUpstream）
	//     读：（暂无消费方；供 admin/telemetry 观测）
	//
	//   "optimization_applied" (string: strip_tools|compress_thinking|summarize)
	//     写：compression/strip 阶段
	//     读：cache_update_hook → SessionState.ApplyOptimization
	//
	//   "security_verdict"、"security_checked_at"、"audit_checked_at"
	//     写：legacy security/sessionaudit hooks（PreRouting）
	//     读：（治理观测）
	//
	// 新增 key 请在此登记写者/读者，避免悬空契约。
	Metadata map[string]any
	// CreatedAt 信封创建时间
	CreatedAt time.Time

	// ── V4 治理扩展（PR-V4-02）──────────────────────────────────
	// Governance 同步治理层共享状态；PhaseGovernance 阶段内的 hooks
	// 读写，其他阶段应视为只读。nil 表示治理未被启用或尚未到达该阶段。
	Governance *governance.GovernanceState

	// ToolState 客户端工具编排共享状态；由 PhaseGovernance 阶段的
	// ToolOrchestrator 写入，post_upstream 阶段的 streaming/audit
	// hooks 读取。nil 表示本请求不涉及工具编排。
	ToolState *governance.ToolState
}

// PipelineClientIdentity 客户端识别信息（Pipeline 视图）。
type PipelineClientIdentity struct {
	Hash       string
	VirtualIP  string
	VirtualMAC string
}

// PipelineAPIKey API key 简化定义（Pipeline 视图）。
type PipelineAPIKey struct {
	ID        string
	Key       string
	TenantID  string
	Enabled   bool
	ExpiresAt time.Time
}

// PipelineCredential 凭据简化定义（Pipeline 视图）。
type PipelineCredential struct {
	ID       string
	Provider string
	APIKey   string
}

// PipelineProvider provider 简化定义（Pipeline 视图）。
type PipelineProvider struct {
	ID   string
	Name string
	Base string
}

// NewRequestEnvelope 创建一个新的 PipelineRequest。
//
// ctx 为 Go 上下文（保留用于对称接口；实际上下文从 Envelope.GoContext 取）。
// envelope 为底层领域信封，允许为 nil（用于纯 Pipeline 单元测试）。
func NewRequestEnvelope(ctx context.Context, envelope *RequestEnvelope) *PipelineRequest {
	_ = ctx // 保留签名以兼容原实施计划
	return &PipelineRequest{
		Envelope:  envelope,
		Metadata:  make(map[string]any),
		CreatedAt: time.Now(),
	}
}

// Context 返回 PipelineRequest 持有的 Go 上下文。
// 优先返回 Envelope.GoContext；若 Envelope 为 nil 则返回 context.Background()。
func (p *PipelineRequest) Context() context.Context {
	if p == nil {
		return context.Background()
	}
	if p.Envelope != nil && p.Envelope.GoContext != nil {
		return p.Envelope.GoContext
	}
	return context.Background()
}

// SetContext 设置底层信封的 Go 上下文。
func (p *PipelineRequest) SetContext(ctx context.Context) {
	if p == nil || p.Envelope == nil {
		return
	}
	p.Envelope.GoContext = ctx
}

// HasError 报告 PipelineRequest 是否携带错误。
func (p *PipelineRequest) HasError() bool {
	return p != nil && p.Error != nil
}

// EnsureGovernance 幂等获取/创建 GovernanceState。
//
// 调用方在 governance 阶段的 hook 中可直接：
//
//	state := env.EnsureGovernance()
//	state.RecordVerdict(v)
//
// nil 接收者返回 nil（不 panic），便于在 PipelineRequest 未实例化时调用。
func (p *PipelineRequest) EnsureGovernance() *governance.GovernanceState {
	if p == nil {
		return nil
	}
	if p.Governance == nil {
		p.Governance = &governance.GovernanceState{}
	}
	return p.Governance
}

// EnsureToolState 幂等获取/创建 ToolState；nil 接收者返回 nil。
func (p *PipelineRequest) EnsureToolState() *governance.ToolState {
	if p == nil {
		return nil
	}
	if p.ToolState == nil {
		p.ToolState = &governance.ToolState{}
	}
	return p.ToolState
}
