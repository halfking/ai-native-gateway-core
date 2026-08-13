// Package admin — node_operations.go
//
// V3.2 (2026-08-13) 节点操作 API：同步测试 + 启停切换。
//
// Routes (registered in RegisterRoutes):
//
//	POST  /api/admin/providers/{id}/test-now   同步探测一个节点（5s 超时兜底）
//	PATCH /api/admin/providers/{id}/enable     启用/禁用节点（切 manual_disabled + 候选缓存失效）
//
// 与现有端点的区别：
//   - /api/credentials/{id}/test (credential_state_handlers.go) 是异步 202，
//     本端点 test-now 是同步返回真实延迟，供首页"点击节点测试"即时反馈。
//   - /api/credentials/clear-manual-disabled 只能清除禁用，
//     本端点 enable 支持双向切换（启用/禁用）。
//
// 权限：super_admin / admin_key（写操作走 RequireSuperAdminForWrite）。
// 审计：操作写结构化日志；TODO(V3.2 DB-01 就绪后) 对接 request_state_transitions。
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// nodeTestNowResponse 是 test-now 的返回结构。
type nodeTestNowResponse struct {
	ProviderID int    `json:"provider_id"`
	LatencyMs  int64  `json:"latency_ms"`
	Status     string `json:"status"` // healthy | unreachable | auth_failed | error
	TestedAt   string `json:"tested_at"`
	Error      string `json:"error,omitempty"`
}

// handleNodeTestNow 同步探测一个 provider 节点，5s 超时兜底。
// POST /api/admin/providers/{id}/test-now
func (h *Handler) handleNodeTestNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if RequireSuperAdminForWrite(w, r) {
		return
	}

	providerID, ok := parseProviderIDFromPath(w, r)
	if !ok {
		return
	}

	// 提取 operator ID（用于限流）
	operatorID := extractOperatorID(r)

	// 5s 超时兜底（rule: 不得挂住）
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 取 provider 的 base URLs + 一个可用 credential 的 apiKey
	baseURLs, apiKey, err := h.nodeProbeTargets(ctx, providerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider probe targets not found: "+err.Error())
		return
	}

	// 获取 credential_id（用于限流）
	var credentialID int
	err = h.db.QueryRow(ctx, `
		SELECT c.id FROM credentials c
		WHERE c.provider_id = $1 AND c.status = 'active'
		  AND COALESCE(c.manual_disabled, false) = false
		ORDER BY c.id LIMIT 1`, providerID).Scan(&credentialID)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("no active credential found: %v (provider_id=%d)", err, providerID))
		return
	}

	// 限流检查：1 req/s per-cred + 10 req/min per-operator
	if err := h.rateLimiter.checkTestNow(ctx, credentialID, operatorID); err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}

	start := time.Now()
	result, probeErr := doProbeRequest(ctx, baseURLs, apiKey)
	latency := time.Since(start).Milliseconds()

	resp := nodeTestNowResponse{
		ProviderID: providerID,
		LatencyMs:  latency,
		TestedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	switch {
	case probeErr != nil:
		resp.Status = "unreachable"
		resp.Error = probeErr.Error()
	case !result.authOK:
		resp.Status = "auth_failed"
		resp.Error = "credential rejected by provider"
	default:
		resp.Status = "healthy"
	}

	slog.Info("node test-now",
		"provider_id", providerID,
		"status", resp.Status,
		"latency_ms", latency,
		"operator_id", operatorID,
		"source", "web_api")

	// 异步审计（不阻塞响应）
	h.auditLogger.auditTestNow(providerID, operatorID, resp.Status, latency)

	writeJSON(w, http.StatusOK, resp)
}

// nodeProbeTargets 取 provider 的候选 base URL 与一个可用 credential 的解密 apiKey。
// 复用现有 probe 模式：secret_ciphertext + decryptCredStr + modelsURLCandidatesForBase。
func (h *Handler) nodeProbeTargets(ctx context.Context, providerID int) ([]string, string, error) {
	var baseURL, protocol string
	var ciphertext []byte
	err := h.db.QueryRow(ctx, `
		SELECT p.base_url, COALESCE(p.protocol, ''), COALESCE((
		  SELECT c.secret_ciphertext FROM credentials c
		  WHERE c.provider_id = p.id AND c.status = 'active'
		    AND COALESCE(c.manual_disabled, false) = false
		  ORDER BY c.id LIMIT 1
		), '')
		FROM providers p WHERE p.id = $1 AND p.enabled = TRUE`, providerID).
		Scan(&baseURL, &protocol, &ciphertext)
	if err != nil {
		return nil, "", err
	}
	if baseURL == "" {
		return nil, "", fmt.Errorf("provider %d has no base_url", providerID)
	}
	apiKey := ""
	if len(ciphertext) > 0 {
		decrypted, decErr := h.decryptCredStr(string(ciphertext))
		if decErr != nil {
			return nil, "", fmt.Errorf("decrypt credential failed: %w", decErr)
		}
		apiKey = decrypted
	}
	return modelsURLCandidatesForBase(baseURL), apiKey, nil
}

// nodeEnableRequest 是 enable 端点的请求体。
type nodeEnableRequest struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"`
}

// handleNodeToggle 启用/禁用一个 provider 节点。
// PATCH /api/admin/providers/{id}/enable
//
// 行为：
//   - 禁用：manual_disabled=true，立即从路由候选摘除（候选缓存失效）
//   - 启用：manual_disabled=false，重新入池
//
// 安全门禁：
//   - 二次确认：X-Confirm: yes（必填）
//   - reason 必填
//   - Idempotency-Key 24h 缓存（防重复执行）
func (h *Handler) handleNodeToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if RequireSuperAdminForWrite(w, r) {
		return
	}

	providerID, ok := parseProviderIDFromPath(w, r)
	if !ok {
		return
	}

	// 二次确认门禁：X-Confirm header 必须为 "yes"
	if r.Header.Get("X-Confirm") != "yes" {
		writeError(w, http.StatusPreconditionRequired, "X-Confirm: yes header required for enable operation")
		return
	}

	var req nodeEnableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	// reason 必填
	if req.Reason == "" {
		writeError(w, http.StatusBadRequest, "reason is required")
		return
	}

	// Idempotency-Key（可选，24h 缓存防重复执行）
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey != "" {
		// 检查 24h 内是否已执行过
		cached, err := h.checkIdempotencyCache(r.Context(), idempotencyKey)
		if err == nil && cached {
			writeError(w, http.StatusConflict, fmt.Sprintf("idempotency key already used within 24h: %s", idempotencyKey))
			return
		}
	}

	// 提取 operator + correlation ID
	operatorID := extractOperatorID(r)
	correlationID := r.Header.Get("X-Correlation-ID")
	if correlationID == "" {
		correlationID = fmt.Sprintf("admin-toggle-%d-%d", providerID, time.Now().UnixNano())
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 切换该 provider 下所有 credential 的 manual_disabled
	tag, err := h.db.Exec(ctx, `
		UPDATE credentials SET manual_disabled = $1, updated_at = now()
		WHERE provider_id = $2`, !req.Enabled, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("update failed: %v (provider_id=%d)", err, providerID))
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, fmt.Sprintf("no credentials found under provider (provider_id=%d)", providerID))
		return
	}

	// 立即失效路由候选缓存（按 credential 精细失效，避免全量清空）
	var credIDs []int
	rows, qerr := h.db.Query(ctx, `SELECT id FROM credentials WHERE provider_id = $1`, providerID)
	if qerr == nil {
		defer rows.Close()
		for rows.Next() {
			var id int
			if rows.Scan(&id) == nil {
				credIDs = append(credIDs, id)
			}
		}
	}
	for _, cid := range credIDs {
		provider.InvalidateCandidateCacheForCredential(cid)
	}

	slog.Info("node enable toggled",
		"provider_id", providerID,
		"enabled", req.Enabled,
		"reason", req.Reason,
		"operator_id", operatorID,
		"correlation_id", correlationID,
		"idempotency_key", idempotencyKey,
		"credentials_affected", tag.RowsAffected(),
		"source", "web_api")

	// 异步审计（写 request_state_transitions）
	h.auditLogger.auditNodeToggle(providerID, req.Enabled, req.Reason, operatorID, correlationID, idempotencyKey)

	// 记录 idempotency key（24h 过期）
	if idempotencyKey != "" {
		_ = h.setIdempotencyCache(context.Background(), idempotencyKey, 24*time.Hour)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"provider_id":             providerID,
		"enabled":                 req.Enabled,
		"credentials_affected":    tag.RowsAffected(),
		"candidate_cache_cleared": len(credIDs),
		"correlation_id":          correlationID,
	})
}

// parseProviderIDFromPath 从路径解析 provider id（兼容 {id} 通配）。
func parseProviderIDFromPath(w http.ResponseWriter, r *http.Request) (int, bool) {
	idStr := r.PathValue("id")
	if idStr == "" {
		writeError(w, http.StatusBadRequest, "provider id required")
		return 0, false
	}
	var id int
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid provider id")
		return 0, false
	}
	return id, true
}

// checkIdempotencyCache 检查 idempotency key 是否在 24h 内已使用。
// 返回 true=已缓存（拒绝重复执行），false=未缓存（可执行）。
func (h *Handler) checkIdempotencyCache(ctx context.Context, key string) (bool, error) {
	var exists bool
	err := h.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM request_state_transitions
			WHERE metadata->>'idempotency_key' = $1
			  AND created_at > NOW() - INTERVAL '24 hours'
		)`, key).Scan(&exists)
	return exists, err
}

// setIdempotencyCache 标记 idempotency key 已使用（通过已写入的审计记录实现，无需额外存储）。
// 实际缓存通过 auditNodeToggle 写入的 request_state_transitions 记录实现。
func (h *Handler) setIdempotencyCache(ctx context.Context, key string, ttl time.Duration) error {
	// 无需额外操作，auditNodeToggle 已写入 metadata 包含 idempotency_key
	return nil
}
