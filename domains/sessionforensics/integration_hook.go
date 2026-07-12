package sessionforensics

import (
	"context"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// MakeRequestLogHook 返回一个适配 streaming/handler.SetRequestLogHook
// 签名的回调函数。每个成功的 request_log 入库后会触发摘要生成。
//
// 用法（cmd/gateway/main_pipeline.go）：
//
//	autoSummary := sessionforensics.NewAutoSummaryHook(svc, innerSumm)
//	autoSummary.Start(ctx)
//	defer autoSummary.Stop()
//	h.SetRequestLogHook(sessionforensics.MakeRequestLogHook(autoSummary, "default"))
//
// 行为：
//   - 仅当 entry.Success && entry.GwSessionID != "" 时触发；
//   - 跳过 auto_request（防止机器内部调用触发浪费 LLM quota）；
//   - first message 从 entry.RequestPreview 截断前 200 字符；
//   - 不阻塞 emitter goroutine。
func MakeRequestLogHook(hook *AutoSummaryHook, tenantID string) func(*telemetry.RequestLogEntry) {
	if hook == nil {
		return func(*telemetry.RequestLogEntry) {}
	}
	return func(entry *telemetry.RequestLogEntry) {
		if entry == nil {
			return
		}
		if !entry.Success {
			return
		}
		if entry.IsAutoRequest != nil && *entry.IsAutoRequest {
			return
		}
		var sid string
		if entry.GwSessionID != nil {
			sid = *entry.GwSessionID
		}
		if len(sid) < 3 || !strings.HasPrefix(sid, "gw_") {
			return
		}
		first := ""
		if entry.RequestPreview != nil {
			first = *entry.RequestPreview
		}
		if first == "" && entry.ResponsePreview != nil {
			first = *entry.ResponsePreview
		}
		enqueued := hook.Enqueue(context.Background(), sid, truncate(first, 200))
		if enqueued {
			slog.Debug("sessionforensics: enqueued summary for session",
				"session", sid, "tenant", tenantID,
				"first_preview_bytes", len(first))
		}
	}
}
