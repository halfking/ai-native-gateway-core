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

	// 5s 超时兜底（rule: 不得挂住）
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 取 provider 的 base URLs + 一个可用 credential 的 apiKey
	baseURLs, apiKey, err := h.nodeProbeTargets(ctx, providerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider probe targets not found: "+err.Error())
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
		"source", "web_api")

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

	var req nodeEnableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 切换该 provider 下所有 credential 的 manual_disabled
	tag, err := h.db.Exec(ctx, `
		UPDATE credentials SET manual_disabled = $1, updated_at = now()
		WHERE provider_id = $2`, !req.Enabled, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "no credentials found under provider")
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
		"credentials_affected", tag.RowsAffected(),
		"source", "web_api")

	// TODO(V3.2): DB-01 就绪后写 request_state_transitions（transition_type='state'）

	writeJSON(w, http.StatusOK, map[string]any{
		"provider_id":           providerID,
		"enabled":               req.Enabled,
		"credentials_affected":  tag.RowsAffected(),
		"candidate_cache_cleared": len(credIDs),
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
