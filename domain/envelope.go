package domain

import (
	"context"
	"time"
)

// RequestEnvelope 是跨所有领域的请求信封。
//
// 设计原则：
//   - 各领域只能访问自己的 Context（编译期隔离）
//   - Context 指针可为 nil（按需加载）
//   - RequestEnvelope 不依赖任何业务包（位于 domain/，是叶子依赖）
type RequestEnvelope struct {
	RequestID string
	CreatedAt time.Time
	GoContext context.Context

	Transport   *TransportContext
	Compression *CompressionContext
	Security    *SecurityContext
	Cost        *CostContext
	Session     *SessionContext
	Summary     *SummaryContext
	TaskRoute   *TaskRouteContext
	CredRoute   *CredRouteContext
	Tenant      *TenantContext

	Audit *AuditContext
}

// ResponseEnvelope 是响应信封。
type ResponseEnvelope struct {
	RequestID  string
	FinishedAt time.Time
	Transport  *TransportResponseContext
	Cost       *CostResponseContext
	Audit      *AuditResponseContext
}

// HasTransport 报告 Transport 领域是否激活。
func (e *RequestEnvelope) HasTransport() bool { return e != nil && e.Transport != nil }

// HasSecurity 报告 Security 领域是否激活。
func (e *RequestEnvelope) HasSecurity() bool { return e != nil && e.Security != nil }

// HasTenant 报告 Tenant 领域是否激活。
func (e *RequestEnvelope) HasTenant() bool { return e != nil && e.Tenant != nil }
