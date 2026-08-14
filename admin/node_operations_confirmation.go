// Package admin — node_operations_confirmation.go
//
// V3.2-LP5 (2026-08-14) 节点操作二次确认与幂等性保护。
// 提取自 node_operations.go，降低主文件行数（从 311 行降至 ~230 行）。
package admin

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// nodeEnableRequest 是 handleNodeToggle 的请求体。
type nodeEnableRequest struct {
	Enabled bool   `json:"enabled"` // true=启用，false=禁用
	Reason  string `json:"reason"`  // 操作原因（必填）
}

// checkNodeToggleConfirmation 检查 enable 操作的二次确认门禁。
// 返回 (idempotencyKey, operatorID, correlationID, ok)。
// 如果 ok=false，已经调用 writeError 写响应，调用方应直接 return。
func (h *Handler) checkNodeToggleConfirmation(w http.ResponseWriter, r *http.Request, providerID int) (string, string, string, bool) {
	// 二次确认门禁：X-Confirm header 必须为 "yes"
	if r.Header.Get("X-Confirm") != "yes" {
		writeError(w, http.StatusPreconditionRequired, "X-Confirm: yes header required for enable operation")
		return "", "", "", false
	}

	// Idempotency-Key（可选，24h 缓存防重复执行）
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey != "" {
		// 检查 24h 内是否已执行过
		cached, err := h.checkIdempotencyCache(r.Context(), idempotencyKey)
		if err == nil && cached {
			writeError(w, http.StatusConflict, fmt.Sprintf("idempotency key already used within 24h: %s", idempotencyKey))
			return "", "", "", false
		}
	}

	// 提取 operator + correlation ID
	operatorID := extractOperatorID(r)
	correlationID := r.Header.Get("X-Correlation-ID")
	if correlationID == "" {
		correlationID = fmt.Sprintf("admin-toggle-%d-%d", providerID, time.Now().UnixNano())
	}

	return idempotencyKey, operatorID, correlationID, true
}

// checkIdempotencyCache 检查幂等性缓存（24h 内是否已执行）。
// 实际存储在 DB（idempotency_keys 表，或复用 request_state_transitions 表）。
// 当前简化实现：查 request_state_transitions 表中是否有相同 correlation_id。
func (h *Handler) checkIdempotencyCache(ctx context.Context, key string) (bool, error) {
	var count int
	err := h.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM request_state_transitions 
		WHERE correlation_id = $1 AND created_at > now() - interval '24 hours'`, key).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// setIdempotencyCache 设置幂等性缓存（记录已执行）。
// 当前实现：在审计日志中已记录 correlation_id，无需额外操作。
func (h *Handler) setIdempotencyCache(ctx context.Context, key string, ttl time.Duration) error {
	// no-op: correlation_id 已通过 audit logger 写入 request_state_transitions
	return nil
}
