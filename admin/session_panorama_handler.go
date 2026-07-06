// Package admin — Session Panorama, Tags, Clusters, Suggestions API.
//
// 扩展 /api/admin/session-analytics/<id>/* 的子路由：
//   GET    /<id>/panorama    会话全景聚合 payload
//   GET    /<id>/tags        会话标签列表
//   POST   /<id>/tags        手动打标签
//   DELETE /<id>/tags/<tag_id> 删除标签
//   GET    /<id>/suggestions 优化建议列表
//   POST   /<id>/suggestions/<sid>/apply 采纳建议
//
// 独立路由组：
//   GET    /api/admin/session-clusters        聚类列表
//   GET    /api/admin/session-clusters/<id>   聚类详情
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ── Panorama ──────────────────────────────────────────────────────────

// SessionPanorama 会话全景聚合（一次返回前端所需全部信息）。
type SessionPanorama struct {
	Summary       AnalyticsSessionSummary    `json:"summary"`
	Timeline      []RequestEvent             `json:"timeline"`
	StepSummaries []SessionStepSummary       `json:"step_summaries"`
	Tags          []SessionTag               `json:"tags"`
	Suggestions   []SessionOptimizationSugg  `json:"suggestions"`
	Cluster       *SessionClusterMembership  `json:"cluster,omitempty"`
	Analysis      SessionAnalysis            `json:"analysis"`
	ModuleEnabled bool                       `json:"module_enabled"`
}

// SessionStepSummary 逐步摘要。
type SessionStepSummary struct {
	StepIndex       int     `json:"step_index"`
	RequestID       string  `json:"request_id"`
	RequestSummary  *string `json:"request_summary,omitempty"`
	ResponseSummary *string `json:"response_summary,omitempty"`
	IsLLMGenerated  bool    `json:"is_llm_generated"`
	ToolCallsSummary *string `json:"tool_calls_summary,omitempty"`
}

// SessionTag 标签。
type SessionTag struct {
	ID          int64   `json:"id"`
	TagKey      string  `json:"tag_key"`
	TagValue    string  `json:"tag_value"`
	TagSource   string  `json:"tag_source"`
	Confidence  float64 `json:"confidence"`
	CreatedBy   *string `json:"created_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// SessionOptimizationSugg 优化建议。
type SessionOptimizationSugg struct {
	ID                    int64      `json:"id"`
	Category              string     `json:"category"`
	Severity              string     `json:"severity"`
	Title                 string     `json:"title"`
	Description           *string    `json:"description,omitempty"`
	PotentialSavingsTokens int64     `json:"potential_savings_tokens"`
	PotentialSavingsCost  float64    `json:"potential_savings_cost"`
	Applied               bool       `json:"applied"`
	Dismissed             bool       `json:"dismissed"`
	CreatedAt             time.Time  `json:"created_at"`
}

// SessionClusterMembership 会话所属聚类。
type SessionClusterMembership struct {
	ClusterID string  `json:"cluster_id"`
	Label     *string `json:"label,omitempty"`
	Score     float64 `json:"score"`
}

// HandleSessionPanorama GET /api/admin/session-analytics/<id>/panorama
func (h *Handler) HandleSessionPanorama(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}
	gwSessionID := pathSegment(r.URL.Path, "/api/admin/session-analytics/", 0)
	if gwSessionID == "" {
		writeError(w, http.StatusBadRequest, "gw_session_id is required")
		return
	}
	tenantID := EffectiveTenantIDAll(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 复用 detail 逻辑获取 summary + timeline + analysis
	detail, err := h.loadSessionDetailData(ctx, tenantID, gwSessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load detail: "+err.Error())
		return
	}
	if detail == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	panorama := SessionPanorama{
		Summary:       detail.Summary,
		Timeline:      detail.Timeline,
		Analysis:      detail.Analysis,
		StepSummaries: h.loadStepSummaries(ctx, gwSessionID),
		Tags:          h.loadTags(ctx, gwSessionID),
		Suggestions:   h.loadSuggestions(ctx, gwSessionID),
		Cluster:       h.loadClusterMembership(ctx, gwSessionID),
		ModuleEnabled: true, // 路由可达即说明模块已启用（前端另有 /modules 检测）
	}
	writeJSON(w, http.StatusOK, panorama)
}

// loadSessionDetailData 复用 HandleSessionAnalyticsDetail 的内部逻辑。
func (h *Handler) loadSessionDetailData(ctx context.Context, tenantID, gwSessionID string) (*AnalyticsSessionDetail, error) {
	query := "SELECT " + sessionSummarySelectCols +
		" FROM session_summaries ss" +
		" LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key" +
		" WHERE ss.session_key = $1"
	args := []any{gwSessionID}
	if tenantID != "" {
		query += " AND ss.tenant_id = $2"
		args = append(args, tenantID)
	}
	summary, err := scanSessionSummary(h.db.QueryRow(ctx, query, args...))
	if err != nil {
		return nil, err
	}
	// timeline
	timelineQuery := `
		SELECT request_id, ts, success, client_model, outbound_model,
		       COALESCE(prompt_tokens,0), COALESCE(completion_tokens,0),
		       COALESCE(cost_usd,0), COALESCE(latency_ms,0),
		       work_type, compression_strategy, cache_read_tokens,
		       error_kind, request_preview, response_preview
		FROM request_logs WHERE gw_session_id = $1`
	tArgs := []any{gwSessionID}
	if tenantID != "" {
		timelineQuery += " AND tenant_id = $2"
		tArgs = append(tArgs, tenantID)
	}
	timelineQuery += " ORDER BY ts ASC LIMIT 100"
	rows, err := h.db.Query(ctx, timelineQuery, tArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	timeline := []RequestEvent{}
	for rows.Next() {
		var e RequestEvent
		var ts time.Time
		if err := rows.Scan(&e.RequestID, &ts, &e.Success, &e.ClientModel, &e.UpstreamModel,
			&e.PromptTokens, &e.CompletionTokens, &e.CostUSD, &e.LatencyMs,
			&e.WorkType, &e.CompressionStrategy, &e.CacheReadTokens,
			&e.ErrorMessage, &e.RequestPreview, &e.ResponsePreview); err != nil {
			return nil, err
		}
		e.CreatedAt = ts
		timeline = append(timeline, e)
	}
	analysis := h.buildSessionAnalysis(ctx, tenantID, gwSessionID, timeline)
	return &AnalyticsSessionDetail{Summary: summary, Timeline: timeline, Analysis: analysis}, nil
}

// ── Tags ──────────────────────────────────────────────────────────────

// HandleSessionTags GET/POST /api/admin/session-analytics/<id>/tags
func (h *Handler) HandleSessionTags(w http.ResponseWriter, r *http.Request) {
	gwSessionID := pathSegment(r.URL.Path, "/api/admin/session-analytics/", 0)
	if gwSessionID == "" {
		writeError(w, http.StatusBadRequest, "gw_session_id is required")
		return
	}
	tenantID := EffectiveTenantIDAll(r)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"tags": h.loadTags(ctx, gwSessionID)})
	case http.MethodPost:
		if RequireSuperAdminForWrite(w, r) {
			return
		}
		var body struct {
			TagKey   string `json:"tag_key"`
			TagValue string `json:"tag_value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
		if body.TagKey == "" || body.TagValue == "" {
			writeError(w, http.StatusBadRequest, "tag_key and tag_value required")
			return
		}
		tid := tenantID
		if tid == "" {
			tid = "default"
		}
		_, err := h.db.Exec(ctx, `
			INSERT INTO session_tags (gw_session_id, tenant_id, tag_key, tag_value, tag_source, confidence, created_by)
			VALUES ($1,$2,$3,$4,'manual',1.0,$5)
			ON CONFLICT (gw_session_id, tag_key, tag_value) DO NOTHING`,
			gwSessionID, tid, body.TagKey, body.TagValue, getUsername(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "save tag: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// HandleSessionTagDelete DELETE /api/admin/session-analytics/<id>/tags/<tag_id>
func (h *Handler) HandleSessionTagDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if RequireSuperAdminForWrite(w, r) {
		return
	}
	gwSessionID := pathSegment(r.URL.Path, "/api/admin/session-analytics/", 0)
	tagIDStr := pathSegment(r.URL.Path, "/api/admin/session-analytics/", 2)
	tagID, err := strconv.ParseInt(tagIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tag id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	_, err = h.db.Exec(ctx, `DELETE FROM session_tags WHERE id=$1 AND gw_session_id=$2`, tagID, gwSessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// ── Suggestions ───────────────────────────────────────────────────────

// HandleSessionSuggestions GET /api/admin/session-analytics/<id>/suggestions
func (h *Handler) HandleSessionSuggestions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	gwSessionID := pathSegment(r.URL.Path, "/api/admin/session-analytics/", 0)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": h.loadSuggestions(ctx, gwSessionID)})
}

// HandleSessionSuggestionApply POST /api/admin/session-analytics/<id>/suggestions/<sid>/apply
func (h *Handler) HandleSessionSuggestionApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if RequireSuperAdminForWrite(w, r) {
		return
	}
	sidStr := pathSegment(r.URL.Path, "/api/admin/session-analytics/", 3)
	sid, err := strconv.ParseInt(sidStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid suggestion id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	_, err = h.db.Exec(ctx,
		`UPDATE session_optimization_suggestions SET applied=TRUE, applied_at=NOW(), applied_by=$1 WHERE id=$2`,
		getUsername(r), sid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "apply failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// ── Cluster load helpers ──────────────────────────────────────────────

func (h *Handler) loadStepSummaries(ctx context.Context, gwSessionID string) []SessionStepSummary {
	rows, err := h.db.Query(ctx, `
		SELECT step_index, request_id, request_summary, response_summary,
		       is_llm_generated, tool_calls_summary
		FROM session_request_summaries WHERE gw_session_id=$1 ORDER BY step_index`, gwSessionID)
	if err != nil {
		return []SessionStepSummary{}
	}
	defer rows.Close()
	out := []SessionStepSummary{}
	for rows.Next() {
		var s SessionStepSummary
		if err := rows.Scan(&s.StepIndex, &s.RequestID, &s.RequestSummary, &s.ResponseSummary,
			&s.IsLLMGenerated, &s.ToolCallsSummary); err == nil {
			out = append(out, s)
		}
	}
	return out
}

func (h *Handler) loadTags(ctx context.Context, gwSessionID string) []SessionTag {
	rows, err := h.db.Query(ctx, `
		SELECT id, tag_key, tag_value, tag_source, confidence, created_by, created_at
		FROM session_tags WHERE gw_session_id=$1 ORDER BY created_at`, gwSessionID)
	if err != nil {
		return []SessionTag{}
	}
	defer rows.Close()
	out := []SessionTag{}
	for rows.Next() {
		var t SessionTag
		if err := rows.Scan(&t.ID, &t.TagKey, &t.TagValue, &t.TagSource, &t.Confidence, &t.CreatedBy, &t.CreatedAt); err == nil {
			out = append(out, t)
		}
	}
	return out
}

func (h *Handler) loadSuggestions(ctx context.Context, gwSessionID string) []SessionOptimizationSugg {
	rows, err := h.db.Query(ctx, `
		SELECT id, category, severity, title, description,
		       potential_savings_tokens, potential_savings_cost, applied, dismissed, created_at
		FROM session_optimization_suggestions WHERE gw_session_id=$1 ORDER BY created_at DESC`, gwSessionID)
	if err != nil {
		return []SessionOptimizationSugg{}
	}
	defer rows.Close()
	out := []SessionOptimizationSugg{}
	for rows.Next() {
		var s SessionOptimizationSugg
		if err := rows.Scan(&s.ID, &s.Category, &s.Severity, &s.Title, &s.Description,
			&s.PotentialSavingsTokens, &s.PotentialSavingsCost, &s.Applied, &s.Dismissed, &s.CreatedAt); err == nil {
			out = append(out, s)
		}
	}
	return out
}

func (h *Handler) loadClusterMembership(ctx context.Context, gwSessionID string) *SessionClusterMembership {
	var m SessionClusterMembership
	var label *string
	err := h.db.QueryRow(ctx, `
		SELECT m.cluster_id, c.label, m.score
		FROM session_cluster_members m
		LEFT JOIN session_clusters c ON c.cluster_id = m.cluster_id
		WHERE m.gw_session_id=$1 ORDER BY m.score DESC LIMIT 1`, gwSessionID).Scan(&m.ClusterID, &label, &m.Score)
	if err != nil {
		return nil
	}
	m.Label = label
	return &m
}

// getUsername 从请求提取用户名（手动标签/建议采纳的 created_by）。
func getUsername(r *http.Request) string {
	if auth := GetAuthContext(r); auth != nil && auth.Username != "" {
		return auth.Username
	}
	return "system"
}

// strings 已在本文件其他 handler 使用，此处确保引用（避免 unused 在裁剪时误判）。
var _ = strings.TrimSpace
