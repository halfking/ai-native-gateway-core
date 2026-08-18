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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
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
type credentialSessionPingResponse struct {
	CredentialID int    `json:"credential_id"`
	Model        string `json:"model"`
	LatencyMs    int64  `json:"latency_ms"`
	Status       string `json:"status"`
	TestedAt     string `json:"tested_at"`
	ErrorCode    string `json:"error_code,omitempty"`
	Error        string `json:"error,omitempty"`
}

// handleCredentialSessionPing sends one bounded chat request through the exact
// credential and model selected in the node drawer.
// POST /api/admin/credentials/{id}/session-ping {"model":"..."}
func (h *Handler) handleCredentialSessionPing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	credentialID, ok := parseProviderIDFromPath(w, r)
	if !ok {
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Model = strings.TrimSpace(req.Model)
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var providerID int
	var baseURL, protocol, catalogCode string
	var ciphertext []byte
	err := h.db.QueryRow(ctx, `
		SELECT p.id, p.base_url, COALESCE(p.protocol, ''), COALESCE(p.catalog_code, ''), c.secret_ciphertext
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE c.id = $1 AND pm.raw_model_name = $2 AND p.enabled = TRUE
		LIMIT 1
	`, credentialID, req.Model).Scan(&providerID, &baseURL, &protocol, &catalogCode, &ciphertext)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential-model binding not found")
		return
	}
	apiKey, err := h.decryptCredStr(string(ciphertext))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "decrypt credential failed")
		return
	}
	operatorID := extractOperatorID(r)
	if h.rateLimiter != nil {
		if err := h.rateLimiter.checkTestNow(ctx, credentialID, operatorID); err != nil {
			writeError(w, http.StatusTooManyRequests, err.Error())
			return
		}
	}

	startedAt := time.Now()
	status, errorCode, message := h.runCredentialSessionPing(ctx, baseURL, protocol, catalogCode, apiKey, req.Model)
	latency := time.Since(startedAt).Milliseconds()
	response := credentialSessionPingResponse{
		CredentialID: credentialID,
		Model:        req.Model,
		LatencyMs:    latency,
		Status:       status,
		TestedAt:     time.Now().UTC().Format(time.RFC3339),
		ErrorCode:    errorCode,
		Error:        message,
	}
	slog.Info("credential session ping", "credential_id", credentialID, "model", req.Model, "status", status, "latency_ms", latency, "operator_id", operatorID, "source", "web_api")
	if h.auditLogger != nil {
		h.auditLogger.auditTestNow(providerID, operatorID, status, latency)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) runCredentialSessionPing(ctx context.Context, baseURL, protocol, catalogCode, apiKey, model string) (status, errorCode, message string) {
	endpoint := strings.TrimRight(baseURL, "/") + "/chat/completions"
	payload, err := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 1,
		"stream":     false,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
	})
	if err != nil {
		return "error", "encode_error", "could not encode ping request"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "error", "invalid_endpoint", "provider endpoint is invalid"
	}
	setModelsAuthHeaders(req, protocol, apiKey)
	h.applyCatalogHeaderProfile(ctx, req, catalogCode)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
			return "timeout", "timeout", "session ping timed out"
		}
		return "unreachable", "transport_error", "provider could not be reached"
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices && isChatPingResponse(body) {
		return "healthy", "", ""
	}
	message = strings.TrimSpace(string(body))
	if len(message) > 500 {
		message = message[:500]
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "auth_failed", "auth_failed", "credential rejected by provider"
	case resp.StatusCode == http.StatusNotFound:
		return "model_not_found", "model_not_found", message
	case resp.StatusCode >= http.StatusInternalServerError:
		return "upstream_error", "upstream_5xx", message
	case resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices:
		return "error", "invalid_response", "provider returned an invalid chat response"
	default:
		return "error", fmt.Sprintf("http_%d", resp.StatusCode), message
	}
}

func isChatPingResponse(body []byte) bool {
	var response struct {
		Choices json.RawMessage `json:"choices"`
	}
	return json.Unmarshal(body, &response) == nil && len(response.Choices) > 0
}

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
