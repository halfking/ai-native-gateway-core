// applications_model_policy.go —— application（Key）级模型 allowlist 的管理
// 读写面（2026-11-28 L144 轮：applications.allowed_models_json 死列激活）。
//
//	GET   /api/admin/applications/{id}/allowed-models   读当前 allowlist（null = 未启用）
//	PATCH /api/admin/applications/{id}/allowed-models   写 allowlist（空数组 = 清除为 NULL = 未启用）
//
// 语义（与 internal/modelpolicy 的 reloadApplicationPolicy 同源同纪律）：
//
//	NULL / [] ⇒ 未启用 ⇒ 该 application 名下所有 Key 放行全部模型
//	（存量 NULL 列零行为变化——激活前该列从未被读写）；非空数组 ⇒
//	allowlist（模型不在名单 ⇒ 数据面 403 model_forbidden，四级链
//	tenant→org→user→application 的第四级）。
//
// 写后 InvalidateApplication ⇒ 生效不等 60s TTL。行形状与
// org/user 策略表不同（单列 JSONB 而非 (subject, model, mode) 行）⇒
// 不复用 subjectModelPolicies 那套 CRUD，独立小面。
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// applicationAllowedModels 上限：条目数与单名长度。防御性上限——
// allowlist 是运维圈定的集合，不是自由文本存储。
const (
	applicationAllowedModelsMaxEntries = 200
	applicationAllowedModelsMaxNameLen = 200
)

// applicationAllowedModelsWire 是 GET 的响应体。
type applicationAllowedModelsWire struct {
	ApplicationID int64    `json:"application_id"`
	AllowedModels []string `json:"allowed_models"`
}

// applicationAllowedModelsPatchReq 是 PATCH 的请求体。
type applicationAllowedModelsPatchReq struct {
	// AllowedModels：nil = 不改（纯 GET 语义不适用 PATCH）；[] = 清除
	// （落 NULL ⇒ 未启用）；非空 = 覆盖写入。逐成员 trim + 非空 + 长度上限。
	AllowedModels []string `json:"allowed_models"`
}

// handleApplicationAllowedModels dispatches
// /api/admin/applications/{id}/allowed-models（GET/PATCH）。
func (h *Handler) handleApplicationAllowedModels(w http.ResponseWriter, r *http.Request) {
	if auth := GetAuthContext(r); auth != nil && auth.Role != "super_admin" && auth.Role != "admin_key" {
		writeError(w, http.StatusForbidden, "super_admin role required for this endpoint")
		return
	}
	if h.policyDB() == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/admin/applications/")
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[1] != "allowed-models" {
		writeError(w, http.StatusNotFound, "expected /api/admin/applications/{id}/allowed-models")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid application id")
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getApplicationAllowedModels(w, r, id)
	case http.MethodPatch:
		h.patchApplicationAllowedModels(w, r, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// getApplicationAllowedModels 返回当前 allowlist；列为 NULL 时
// allowed_models 为 null（未启用的权威读法）。
func (h *Handler) getApplicationAllowedModels(w http.ResponseWriter, r *http.Request, id int64) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var raw []byte
	err := h.policyDB().QueryRow(ctx,
		`SELECT allowed_models_json FROM applications WHERE id = $1`, id,
	).Scan(&raw)
	if err != nil {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}

	out := applicationAllowedModelsWire{ApplicationID: id}
	if len(raw) > 0 {
		// JSONB 数组原文直接透传为 []string；非法 JSON 不会出现
		//（列上没有 CHECK，但写入面只产合法数组）——防御性解析失败
		// 视为未启用而不是 500（读面 fail-open 与判定体同纪律）。
		_ = json.Unmarshal(raw, &out.AllowedModels)
	}
	writeJSON(w, http.StatusOK, out)
}

// patchApplicationAllowedModels 覆盖写入 allowlist；空数组落 NULL
//（清除 = 未启用）。写后 InvalidateApplication。
func (h *Handler) patchApplicationAllowedModels(w http.ResponseWriter, r *http.Request, id int64) {
	var req applicationAllowedModelsPatchReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	normalized := make([]string, 0, len(req.AllowedModels))
	seen := make(map[string]bool, len(req.AllowedModels))
	for _, m := range req.AllowedModels {
		name := strings.ToLower(strings.TrimSpace(m))
		if name == "" || seen[name] {
			continue
		}
		if len(name) > applicationAllowedModelsMaxNameLen {
			writeError(w, http.StatusBadRequest, "model name exceeds length limit")
			return
		}
		seen[name] = true
		normalized = append(normalized, name)
		if len(normalized) > applicationAllowedModelsMaxEntries {
			writeError(w, http.StatusBadRequest, "too many allowed models")
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 目标行必须存在（404 而不是静默写空）。
	var exists bool
	err := h.policyDB().QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM applications WHERE id = $1)`, id,
	).Scan(&exists)
	if err != nil || !exists {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}

	// 空数组 ⇒ NULL（未启用）；非空 ⇒ JSON 数组。规范化（去空/去重/小写）
	// 是写面的责任，读面与判定体只认数组元素。
	var payload any
	if len(normalized) == 0 {
		payload = nil
	} else {
		payload = normalized
	}

	_, err = h.policyDB().Exec(ctx,
		`UPDATE applications SET allowed_models_json = $2, updated_at = NOW() WHERE id = $1`,
		id, payload,
	)
	if err != nil {
		writeInternalErr(w, "update allowed_models_json failed", err)
		return
	}

	// 生效不等 60s TTL。
	if h.modelPolicy != nil {
		h.modelPolicy.InvalidateApplication(id)
	}

	writeJSON(w, http.StatusOK, applicationAllowedModelsWire{
		ApplicationID: id,
		AllowedModels: normalized,
	})
}
