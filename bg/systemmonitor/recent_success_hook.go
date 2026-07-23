// Package bg/systemmonitor — recent_success_hook.go
//
// 在 telemetry 持久化 request_logs 行后，调用本 hook 写 Redis 缓存
// llmgw:monitor:node:recent_success:{cred}:{model}，TTL=300s。
// 设计依据: docs/会话优化v2/32-系统监测模块设计.md §4.3 / §6.2
//
// 调用方：cmd/gateway/main.go 在 telemetry.NewClient() 之后挂载：
//
//	telemetryClient.AddOnRequestLogPersisted(systemmonitor.NewRecentSuccessHook(dedup))
//
// 触发条件（与原话"5 分钟内有且是成功的状态"严格对齐）：
//
//	entry.Success == true
//	entry.CredentialID != nil && *entry.CredentialID > 0
//	entry.OutboundModel != nil && *entry.OutboundModel != ""
//	UpstreamStatusCode 为 2xx（如果有）
//
// 不阻塞 telemetry 队列：使用 2 秒超时 + 失败仅 log warn（rule 03 §6）。
package systemmonitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// RecentSuccessHook is a telemetry onPersisted callback that writes the
// 5-minute success marker into Redis.
//
// The hook is intentionally non-blocking:
//   - 2s timeout on the Redis call
//   - panic-recover so a malformed entry never crashes the telemetry worker
//   - any failure logs at WARN level (no metric counters; Phase 2 stretch goal)
//
// Thread safety: the underlying InflightDedup.MarkRecentSuccess uses a
// Redis pipeline (TxPipeline). Multiple goroutines may invoke this hook
// concurrently; redis-go pipelines are safe.
type RecentSuccessHook struct {
	dedup *InflightDedup
}

// NewRecentSuccessHook constructs the hook bound to a dedup helper.
//
// The hook is a no-op when dedup is nil or has no Redis backend.
func NewRecentSuccessHook(dedup *InflightDedup) *RecentSuccessHook {
	return &RecentSuccessHook{dedup: dedup}
}

// Hook returns the onPersisted callback suitable for
// telemetry.Client.AddOnRequestLogPersisted.
func (h *RecentSuccessHook) Hook() func(entry *telemetry.RequestLogEntry) {
	return h.handle
}

// handle implements the onPersisted contract.
//
// On panic, the recover logs at ERROR and swallows — telemetry must
// never crash because of an optional Redis write.
func (h *RecentSuccessHook) handle(entry *telemetry.RequestLogEntry) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("system_monitor: recent_success hook panic",
				"request_id", safeRequestID(entry), "recover", r)
		}
	}()
	if h == nil || h.dedup == nil || !h.dedup.Enabled() {
		return
	}
	if entry == nil {
		return
	}

	// Filter: only 2xx success entries count toward the skip rule
	// (design §4.3: "如果有且是成功的状态").
	if !entry.Success {
		return
	}
	if entry.UpstreamStatusCode != nil {
		code := *entry.UpstreamStatusCode
		if code < 200 || code >= 300 {
			return
		}
	}
	credID := int64(0)
	if entry.CredentialID != nil {
		credID = int64(*entry.CredentialID)
	}
	if credID <= 0 {
		return
	}
	if entry.OutboundModel == nil || *entry.OutboundModel == "" {
		return
	}
	rawModel := *entry.OutboundModel

	httpStatus := 0
	latencyMs := int64(0)
	if entry.UpstreamStatusCode != nil {
		httpStatus = *entry.UpstreamStatusCode
	}
	if entry.LatencyMs != nil {
		latencyMs = int64(*entry.LatencyMs)
	}
	requestID := entry.RequestID
	if entry.ClientRequestID != nil && *entry.ClientRequestID != "" {
		// Prefer the server-generated UUID; fall back to client id if missing.
		requestID = entry.RequestID
	}
	at := time.Now().UTC()
	if entry.EventAt != nil {
		at = entry.EventAt.UTC()
	}

	// 2s timeout so the telemetry worker is never blocked.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.dedup.MarkRecentSuccess(ctx, credID, rawModel, requestID, httpStatus, int(latencyMs), at); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("system_monitor: recent_success hook failed",
			"request_id", requestID,
			"credential_id", credID,
			"raw_model", rawModel,
			"error", fmt.Sprintf("%v", err))
	}
}

// safeRequestID avoids nil-deref in the recover path.
func safeRequestID(entry *telemetry.RequestLogEntry) string {
	if entry == nil {
		return ""
	}
	return entry.RequestID
}
