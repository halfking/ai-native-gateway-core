package admin

import (
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/taskprofile"
)

// taskProfileAuditSink 是 taskprofile 四变更端点（corrections create/import、
// reload、apply-tier-config）审计事件的生产 sink（R43 §五#3 落地、R46 F5 从
// handler.go 内联闭包抽出为命名函数：行为可测——actor 提取此前零测试执行）。
//
// 契约（R46 F8⑨，与 taskprofile.AuditEvent 注释对齐）：事件只用于归因，
// ev.Request 仅允许提取 actor/tenant 等鉴权上下文——禁止把原始请求的
// headers/body 记入任何 sink（Authorization 头会带出会话凭据）。
//
// panic 隔离在 taskprofile.emitAudit 侧完成，这里保持非阻塞纯日志。
func taskProfileAuditSink(ev taskprofile.AuditEvent) {
	actor, tenant := "unknown", "default"
	if ev.Request != nil {
		if ac := GetAuthContext(ev.Request); ac != nil {
			if ac.Username != "" {
				actor = ac.Username
			}
			if ac.TenantID != "" {
				tenant = ac.TenantID
			}
		}
	}
	slog.Info("taskprofile.audit",
		"action", ev.Action,
		"outcome", ev.Outcome,
		"actor", actor,
		"tenant_id", tenant,
		"detail", ev.Detail,
	)
}
