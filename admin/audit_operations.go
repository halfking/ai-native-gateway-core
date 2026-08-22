// Package admin — audit_operations.go
//
// B3 PR2 (2026-08-17): admin 节点操作审计独立查询面。替换被撤的
// /api/admin/requests/{id}/transitions 中那部分 admin 节点操作审计查询
// （transition_type='state' 的伪 request_id 行）。
//
// 数据源：request_state_transitions。请求生命周期事件
// （event_type IS NOT NULL）已迁入 requestjourney 并由
// /api/admin/request-journeys/ Detail 覆盖；本 endpoint 只返回 audit 行。
//
// 路由：GET /api/admin/audit/node-operations
// 查询参数：
//   - provider_id（可选）过滤写入方节点 ID
//   - operation   （可选）过滤 metadata.operation（test_now / enable_toggle）
//   - operator_id （可选）过滤操作者 ID
//   - since       （可选，RFC3339）只返回 created_at >= since 的行
//   - limit       （可选，1..200，默认 50）
//
// 鉴权：admin 中间件（JWT 或 admin_key）。tenant_id 来自 RLS，非 super_admin
// 看到的是自己租户下 node_operations_audit.go 写入的 'default' 审计行
// （目前节点操作审计统一 tenant='default'，RLS 在 super_admin 时走 bypass）。
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AuditOperationEntry 是 admin 节点操作审计的一行 API 投影。
type AuditOperationEntry struct {
	RequestID      string         `json:"request_id"`
	ProviderID     int64          `json:"provider_id"`
	Operation      string         `json:"operation"`
	FromState      string         `json:"from_state,omitempty"`
	ToState        string         `json:"to_state,omitempty"`
	OperatorID     string         `json:"operator_id,omitempty"`
	CorrelationID  string         `json:"correlation_id,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	Status         string         `json:"status,omitempty"`
	LatencyMs      int64          `json:"latency_ms,omitempty"`
	Reason         string         `json:"reason,omitempty"`
	Enabled        bool           `json:"enabled,omitempty"`
	Source         string         `json:"source,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	Raw            map[string]any `json:"raw,omitempty"`
}

const (
	defaultAuditOperationLimit = 50
	maxAuditOperationLimit     = 200
)

// handleAuditNodeOperations 返回 admin 节点操作审计历史（最近优先）。
// GET /api/admin/audit/node-operations
func (h *Handler) handleAuditNodeOperations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	q := r.URL.Query()
	providerIDRaw := strings.TrimSpace(q.Get("provider_id"))
	operation := strings.TrimSpace(q.Get("operation"))
	operatorID := strings.TrimSpace(q.Get("operator_id"))
	sinceRaw := strings.TrimSpace(q.Get("since"))
	limitRaw := strings.TrimSpace(q.Get("limit"))

	var providerID int64
	if providerIDRaw != "" {
		parsed, err := strconv.ParseInt(providerIDRaw, 10, 64)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid provider_id")
			return
		}
		providerID = parsed
	}
	var since time.Time
	if sinceRaw != "" {
		parsed, err := time.Parse(time.RFC3339, sinceRaw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
		since = parsed
	}
	limit := defaultAuditOperationLimit
	if limitRaw != "" {
		parsed, err := strconv.Atoi(limitRaw)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		if parsed > maxAuditOperationLimit {
			parsed = maxAuditOperationLimit
		}
		limit = parsed
	}

	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	out := make([]AuditOperationEntry, 0, limit)

	query := func(tx pgx.Tx) error {
		args := []any{}
		where := []string{"transition_type = 'state'", "event_type IS NULL"}
		idx := 1
		if providerID > 0 {
			where = append(where, fmt.Sprintf("(metadata->>'provider_id')::bigint = $%d", idx))
			args = append(args, providerID)
			idx++
		}
		if operation != "" {
			where = append(where, fmt.Sprintf("metadata->>'operation' = $%d", idx))
			args = append(args, operation)
			idx++
		}
		if operatorID != "" {
			where = append(where, fmt.Sprintf("metadata->>'operator_id' = $%d", idx))
			args = append(args, operatorID)
			idx++
		}
		if !since.IsZero() {
			where = append(where, fmt.Sprintf("created_at >= $%d", idx))
			args = append(args, since)
			idx++
		}
		args = append(args, limit)

		sql := `SELECT id, request_id, from_state, to_state, metadata, created_at
			FROM request_state_transitions
			WHERE ` + strings.Join(where, " AND ") + `
			ORDER BY created_at DESC, id DESC
			LIMIT $` + strconv.Itoa(idx)

		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var requestID, fromState, toState string
			var metaRaw []byte
			var createdAt time.Time
			if err := rows.Scan(&id, &requestID, &fromState, &toState, &metaRaw, &createdAt); err != nil {
				return fmt.Errorf("scan audit entry: %w", err)
			}
			entry := AuditOperationEntry{
				RequestID: requestID,
				FromState: fromState,
				ToState:   toState,
				CreatedAt: createdAt,
			}
			if len(metaRaw) > 0 {
				var meta map[string]any
				if err := json.Unmarshal(metaRaw, &meta); err == nil {
					entry.Raw = meta
					if pid, ok := meta["provider_id"]; ok {
						switch v := pid.(type) {
						case float64:
							entry.ProviderID = int64(v)
						case int64:
							entry.ProviderID = v
						}
					}
					if op, ok := meta["operation"].(string); ok {
						entry.Operation = op
					}
					if v, ok := meta["operator_id"].(string); ok {
						entry.OperatorID = v
					}
					if v, ok := meta["correlation_id"].(string); ok {
						entry.CorrelationID = v
					}
					if v, ok := meta["idempotency_key"].(string); ok {
						entry.IdempotencyKey = v
					}
					if v, ok := meta["status"].(string); ok {
						entry.Status = v
					}
					if lat, ok := meta["latency_ms"]; ok {
						switch v := lat.(type) {
						case float64:
							entry.LatencyMs = int64(v)
						case int64:
							entry.LatencyMs = v
						}
					}
					if v, ok := meta["reason"].(string); ok {
						entry.Reason = v
					}
					if en, ok := meta["enabled"].(bool); ok {
						entry.Enabled = en
					}
					if v, ok := meta["source"].(string); ok {
						entry.Source = v
					}
				}
			}
			out = append(out, entry)
		}
		return rows.Err()
	}

	var err error
	if IsSuperAdminOrLegacy(r) {
		err = withAllTenantReadOnlyTx(ctx, h.db, query)
	} else {
		err = withTenantTx(ctx, h.db, GetTenantID(r), query)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"entries": out,
		"count":   len(out),
		"limit":   limit,
	})
}
