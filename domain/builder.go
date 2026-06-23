package domain

import (
	"context"
	"net/http"
	"time"
)

// EnvelopeBuilder 以流式 API 构造 RequestEnvelope。
//
// 典型用法：
//
//	env := domain.NewEnvelopeBuilder(reqID).
//	    WithGoContext(ctx).
//	    WithTransport(tctx).
//	    WithTenant(tctx2).
//	    Build()
type EnvelopeBuilder struct {
	env RequestEnvelope
}

// NewEnvelopeBuilder 创建一个新的 builder。
func NewEnvelopeBuilder(requestID string) *EnvelopeBuilder {
	return &EnvelopeBuilder{
		env: RequestEnvelope{
			RequestID: requestID,
			CreatedAt: time.Now(),
		},
	}
}

// WithGoContext 设置 Go context。
func (b *EnvelopeBuilder) WithGoContext(ctx context.Context) *EnvelopeBuilder {
	b.env.GoContext = ctx
	return b
}

// WithTransport 设置 Transport 上下文。
func (b *EnvelopeBuilder) WithTransport(tc *TransportContext) *EnvelopeBuilder {
	b.env.Transport = tc
	return b
}

// WithSecurity 设置 Security 上下文。
func (b *EnvelopeBuilder) WithSecurity(sc *SecurityContext) *EnvelopeBuilder {
	b.env.Security = sc
	return b
}

// WithTenant 设置 Tenant 上下文。
func (b *EnvelopeBuilder) WithTenant(tc *TenantContext) *EnvelopeBuilder {
	b.env.Tenant = tc
	return b
}

// WithTaskRoute 设置 TaskRoute 上下文。
func (b *EnvelopeBuilder) WithTaskRoute(tc *TaskRouteContext) *EnvelopeBuilder {
	b.env.TaskRoute = tc
	return b
}

// WithCredRoute 设置 CredRoute 上下文。
func (b *EnvelopeBuilder) WithCredRoute(cc *CredRouteContext) *EnvelopeBuilder {
	b.env.CredRoute = cc
	return b
}

// WithSession 设置 Session 上下文。
func (b *EnvelopeBuilder) WithSession(sc *SessionContext) *EnvelopeBuilder {
	b.env.Session = sc
	return b
}

// WithCompression 设置 Compression 上下文。
func (b *EnvelopeBuilder) WithCompression(cc *CompressionContext) *EnvelopeBuilder {
	b.env.Compression = cc
	return b
}

// WithCost 设置 Cost 上下文。
func (b *EnvelopeBuilder) WithCost(cc *CostContext) *EnvelopeBuilder {
	b.env.Cost = cc
	return b
}

// WithSummary 设置 Summary 上下文。
func (b *EnvelopeBuilder) WithSummary(sc *SummaryContext) *EnvelopeBuilder {
	b.env.Summary = sc
	return b
}

// WithAudit 设置 Audit 上下文。
func (b *EnvelopeBuilder) WithAudit(ac *AuditContext) *EnvelopeBuilder {
	b.env.Audit = ac
	return b
}

// WithHTTP 同时设置 GoContext + Transport（最常见的入口）。
func (b *EnvelopeBuilder) WithHTTP(ctx context.Context, w http.ResponseWriter, r *http.Request, body []byte) *EnvelopeBuilder {
	b.env.GoContext = ctx
	if b.env.Transport == nil {
		b.env.Transport = &TransportContext{
			W:         w,
			R:         r,
			BodyBytes: body,
		}
	} else {
		b.env.Transport.W = w
		b.env.Transport.R = r
		b.env.Transport.BodyBytes = body
	}
	return b
}

// Build 返回构造好的 RequestEnvelope。
func (b *EnvelopeBuilder) Build() *RequestEnvelope {
	return &b.env
}
