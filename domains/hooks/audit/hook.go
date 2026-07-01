package audit

import (
	"context"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// AuditLogHook 审计日志 Hook
type AuditLogHook struct {
	writer *BatchWriter
}

// NewAuditLogHook 创建审计日志 Hook
func NewAuditLogHook(writer *BatchWriter) *AuditLogHook {
	return &AuditLogHook{writer: writer}
}

// Name 返回 Hook 名称
func (h *AuditLogHook) Name() string { return "audit.log" }

// Priority 返回优先级
func (h *AuditLogHook) Priority() int { return 50 }

// Enabled 是否启用
func (h *AuditLogHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil && env.Envelope != nil
}

// Execute 记录审计事件
func (h *AuditLogHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	action := "allow"
	if env.Error != nil {
		action = "deny"
	} else if env.StatusCode != 0 {
		action = "modify"
	}

	requestID := ""
	if env.Envelope != nil {
		requestID = env.Envelope.RequestID
	}

	h.writer.Append(&Event{
		RequestID:  requestID,
		TenantID:   env.TenantID,
		SessionID:  env.SessionID,
		Stage:      "post_response",
		Action:     action,
		StatusCode: env.StatusCode,
		LatencyMs:  int(time.Since(env.CreatedAt).Milliseconds()),
		Error:      errString(env.Error),
		Metadata:   env.Metadata,
		CreatedAt:  time.Now(),
	})
	return nil
}

// OnError 错误处理（审计失败不影响主流程）
func (h *AuditLogHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

var _ pipeline.Hook = (*AuditLogHook)(nil)
