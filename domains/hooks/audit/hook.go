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
		Metadata:   sanitizeAuditMetadata(env.Metadata),
		CreatedAt:  time.Now(),
	})
	return nil
}

// auditRecordMaxValueBytes 是审计记录 metadata 单值的体积上限：本记录的语义是
// 「跨 stage 的判定/标签」，不是载荷副本 —— 超限字符串键一律剥离。
const auditRecordMaxValueBytes = 4096

// sanitizeAuditMetadata 在记录前剥离**大体积/敏感键**，返回浅拷贝（不改 env）。
//
// # rule 71 dump 面审查（2026-11-13 F-41 收口）
//
// 本 hook 此前把 env.Metadata **全量**塞进审计事件（Metadata map 原样透传）。
// user_content 桥接（cmd/gateway/injection_bridge.go）之后，Metadata 会携带
// 用户原文（上限 50KiB）；且 force-compression 头路径早已写入完整会话
// messages（compression.MetaKeyMessages）、api_key 携带**原始客户端密钥** ——
// 全量 dump = 请求原文 + 密钥进审计事件（且 sink 无淘汰机制，永久驻留内存）。
//
// 判定：metadata 记录改为「剥离 denylist + 超限字符串」的浅拷贝：
//   - "user_content"：用户原文（F-41 桥接新增载体）；
//   - "messages"（compression.MetaKeyMessages）：完整会话（force-compression 路径）；
//   - "api_key"：原始客户端密钥（应只以指纹形态出现在持久面）；
//   - 任何 > 4KiB 的字符串值：metadata 不是载荷面，超限即剥离（兜住未来新键）。
//
// 其余键（model/provider/stream/remote/agent/verdicts…）保留 —— 审计语义不变。
func sanitizeAuditMetadata(md map[string]any) map[string]any {
	if md == nil {
		return nil
	}
	out := make(map[string]any, len(md))
	for k, v := range md {
		switch k {
		case "user_content", "messages", "api_key":
			continue
		}
		if s, ok := v.(string); ok && len(s) > auditRecordMaxValueBytes {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
