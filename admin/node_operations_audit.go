// Package admin — node_operations_audit.go
//
// V3.2-LP5 (2026-08-14) 节点操作审计：异步写入 request_state_transitions。
// 失败仅记录日志，不阻塞主流程。
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// nodeOperationAuditLogger 异步审计节点操作。
type nodeOperationAuditLogger struct {
	db txBeginner
}

// newNodeOperationAuditLogger 创建审计 logger。
func newNodeOperationAuditLogger(db *pgxpool.Pool) *nodeOperationAuditLogger {
	return &nodeOperationAuditLogger{db: db}
}

// auditTestNow 异步记录 test-now 操作（不阻塞主流程）。
func (a *nodeOperationAuditLogger) auditTestNow(providerID int, operatorID, status string, latencyMs int64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		metadata := map[string]any{
			"operation":   "test-now",
			"provider_id": providerID,
			"operator_id": operatorID,
			"status":      status,
			"latency_ms":  latencyMs,
			"source":      "admin_api",
		}
		metadataJSON, _ := json.Marshal(metadata)

		// request_id 用 provider:test-now:{timestamp} 格式（无真实请求上下文）
		requestID := generateTestNowRequestID(providerID)

		err := a.insertTransition(ctx, requestID, "admin_trigger", "test_completed", metadataJSON)
		if err != nil {
			slog.Warn("audit test-now failed (non-blocking)",
				"provider_id", providerID,
				"error", err.Error())
		}
	}()
}

// auditNodeToggle 异步记录 enable/disable 操作（不阻塞主流程）。
func (a *nodeOperationAuditLogger) auditNodeToggle(providerID int, enabled bool, reason, operatorID, correlationID, idempotencyKey string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		metadata := map[string]any{
			"operation":       "enable_toggle",
			"provider_id":     providerID,
			"enabled":         enabled,
			"reason":          reason,
			"operator_id":     operatorID,
			"correlation_id":  correlationID,
			"idempotency_key": idempotencyKey,
			"source":          "admin_api",
		}
		metadataJSON, _ := json.Marshal(metadata)

		requestID := generateToggleRequestID(providerID, idempotencyKey)
		fromState := "enabled"
		toState := "disabled"
		if enabled {
			fromState = "disabled"
			toState = "enabled"
		}

		err := a.insertTransition(ctx, requestID, fromState, toState, metadataJSON)
		if err != nil {
			slog.Warn("audit node toggle failed (non-blocking)",
				"provider_id", providerID,
				"enabled", enabled,
				"error", err.Error())
		}
	}()
}

// generateTestNowRequestID 生成 test-now 的伪 request_id。
func (a *nodeOperationAuditLogger) insertTransition(ctx context.Context, requestID, fromState, toState string, metadataJSON []byte) error {
	return withTx(ctx, a.db, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := setLocalTenantGUC(ctx, tx, "default"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO request_state_transitions (request_id, tenant_id, transition_type, from_state, to_state, metadata)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			requestID, "default", "state", fromState, toState, metadataJSON)
		return err
	})
}

func generateTestNowRequestID(providerID int) string {
	return fmt.Sprintf("provider:%d:test-now:%d", providerID, time.Now().UnixNano())
}

// generateToggleRequestID 生成 enable 的伪 request_id（用 idempotency key 去重）。
func generateToggleRequestID(providerID int, idempotencyKey string) string {
	if idempotencyKey == "" {
		return fmt.Sprintf("provider:%d:toggle:%d", providerID, time.Now().UnixNano())
	}
	return fmt.Sprintf("provider:%d:toggle:%s", providerID, idempotencyKey)
}
