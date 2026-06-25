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
	// Metadata 跨阶段元数据
	Metadata map[string]any
	// CreatedAt 信封创建时间
	CreatedAt time.Time
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
