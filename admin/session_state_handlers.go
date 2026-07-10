package admin

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session"
)

type sessionListItem struct {
	SessionID             string     `json:"session_id"`
	TenantID              string     `json:"tenant_id"`
	APIKeyID              int        `json:"api_key_id"`
	Status                string     `json:"status"`
	CurrentModel          string     `json:"current_model"`
	CurrentCredential     int        `json:"current_credential_id"`
	CurrentProvider       string     `json:"current_provider"`
	TotalTurns            int64      `json:"total_turns"`
	TotalPromptTokens     int64      `json:"total_prompt_tokens"`
	TotalCompletionTokens int64      `json:"total_completion_tokens"`
	TotalCostUSDCents     int64      `json:"total_cost_usd_cents"`
	TotalCostUSD          float64    `json:"total_cost_usd"`
	Title                 string     `json:"title"`
	Tags                  string     `json:"tags"`
	Annotation            string     `json:"annotation"`
	CreatedAt             time.Time  `json:"created_at"`
	LastActive            time.Time  `json:"last_active"`
	LastRequestAt         time.Time  `json:"last_request_at"`
	StoppedAt             *time.Time `json:"stopped_at,omitempty"`
	// 健康评分字段（T1.5）
	HealthScore           *int       `json:"health_score,omitempty"`
	HealthGrade           *string    `json:"health_grade,omitempty"`
	Outcome               *string    `json:"outcome,omitempty"`
}

type sessionListResponse struct {
	Sessions []sessionListItem `json:"sessions"`
	Total    int               `json:"total"`
}

type sessionDetailResponse struct {
	Session   *session.Session            `json:"session"`
	Stats     *session.SessionStats       `json:"stats"`
	Rotations []session.CredRotationEntry `json:"rotations"`
}

func (h *Handler) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if h.sessionManager == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not wired")
		return
	}

	statusFilter := r.URL.Query().Get("status")
	if statusFilter == "" {
		statusFilter = session.StatusActive
	}
	tenantID := r.URL.Query().Get("tenant_id")
	limit := queryInt(r, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	if !IsSuperAdminOrLegacy(r) {
		tenantID = GetTenantID(r)
	}

	// T1.5: 解析排序和健康过滤参数
	sortBy := r.URL.Query().Get("sort")
	healthGradeFilter := r.URL.Query().Get("health_grade") // e.g., "D,F"

	var sessionIDs []string
	switch statusFilter {
	case session.StatusStopped:
		ids, _ := h.sessionManager.ListStoppedSessions(ctxFn(r), tenantID, limit*2) // 多获取一些，用于过滤后仍有足够数据
		sessionIDs = ids
	case "all":
		active := h.listActiveSessions(ctxFn(r), tenantID, limit*2)
		stopped, _ := h.sessionManager.ListStoppedSessions(ctxFn(r), tenantID, limit*2)
		sessionIDs = append(active, stopped...)
	default:
		sessionIDs = h.listActiveSessions(ctxFn(r), tenantID, limit*2)
	}

	items := make([]sessionListItem, 0, len(sessionIDs))
	for _, sid := range sessionIDs {
		sess, err := h.sessionManager.Get(ctxFn(r), sid)
		if err != nil {
			continue
		}
		if tenantID != "" && sess.TenantID != tenantID {
			continue
		}
		stats, _ := h.sessionManager.GetStats(ctxFn(r), sid)
		item := sessionListItem{
			SessionID:         sess.SessionID,
			TenantID:          sess.TenantID,
			APIKeyID:          sess.APIKeyID,
			Status:            defaultString(sess.Status, session.StatusActive),
			CurrentModel:      sess.CurrentModel,
			CurrentCredential: sess.CurrentCredentialID,
			CurrentProvider:   sess.CurrentProvider,
			Title:             sess.Title,
			Tags:              sess.Tags,
			Annotation:        sess.Annotation,
			CreatedAt:         sess.CreatedAt,
			LastActive:        sess.LastActive,
			LastRequestAt:     sess.LastRequestAt,
		}
		if !sess.StoppedAt.IsZero() {
			st := sess.StoppedAt
			item.StoppedAt = &st
		}
		if stats != nil {
			item.TotalTurns = stats.TotalTurns
			item.TotalPromptTokens = stats.TotalPromptTokens
			item.TotalCompletionTokens = stats.TotalCompletionTokens
			item.TotalCostUSDCents = stats.TotalCostUSDCents
			item.TotalCostUSD = stats.TotalCostUSD
			if !stats.LastRequestAt.IsZero() {
				item.LastRequestAt = stats.LastRequestAt
			}
		}
		items = append(items, item)
	}

	// T1.5: 批量查询健康数据（从 session_summaries）
	if h.db != nil && len(items) > 0 {
		h.enrichHealthData(ctxFn(r), items)
	}

	// T1.5: 健康等级过滤
	if healthGradeFilter != "" {
		items = h.filterByHealthGrade(items, healthGradeFilter)
	}

	// T1.5: 排序
	h.sortSessionList(items, sortBy)

	// 限制结果数量
	if len(items) > limit {
		items = items[:limit]
	}

	writeJSON(w, http.StatusOK, sessionListResponse{Sessions: items, Total: len(items)})
}

func (h *Handler) handleSessionSubrouter(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/sessions/")
	if path == "" {
		writeError(w, http.StatusBadRequest, "session id required")
		return
	}
	parts := strings.Split(path, "/")
	sessionID := parts[0]
	action := ""
	if len(parts) > 1 {
		action = strings.Join(parts[1:], "/")
	}
	if action == "" {
		h.serveSessionDetail(w, r, sessionID)
		return
	}
	switch action {
	case "cred-rotations":
		h.serveCredRotations(w, r, sessionID)
	case "stop":
		h.serveStopSession(w, r, sessionID)
	case "recover":
		h.serveRecoverSession(w, r, sessionID)
	case "annotation":
		h.serveUpdateAnnotation(w, r, sessionID)
	case "tags":
		h.serveUpdateTags(w, r, sessionID)
	case "health":
		// GET /api/admin/sessions/<id>/health — 单会话健康详情（含扣分明细）。
		h.HandleSessionHealth(w, r)
	case "recompute-health":
		// POST /api/admin/sessions/<id>/recompute-health — 强制重算。
		// 写操作，仅 super_admin / admin_key 可执行。
		if RequireSuperAdminForWrite(w, r) {
			return
		}
		h.HandleRecomputeSessionHealth(w, r)
	case "inspector-findings":
		// GET /api/admin/sessions/<id>/inspector-findings — 单会话最新 finding 列表
		// (2026-07-09 session_inspector 模块增强)
		h.HandleSessionInspectorFindings(w, r)
	case "recycle":
		// POST /api/admin/sessions/<id>/recycle — 手动触发软关闭
		// 写操作，仅 super_admin 可执行
		if RequireSuperAdminForWrite(w, r) {
			return
		}
		h.HandleSessionRecycle(w, r)
	default:
		writeError(w, http.StatusNotFound, "unknown action: "+action)
	}
}

func (h *Handler) serveSessionDetail(w http.ResponseWriter, r *http.Request, sessionID string) {
	if h.sessionManager == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not wired")
		return
	}
	sess, err := h.sessionManager.Get(ctxFn(r), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if !IsSuperAdminOrLegacy(r) && sess.TenantID != GetTenantID(r) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	stats, _ := h.sessionManager.GetStats(ctxFn(r), sessionID)
	rotations, _ := h.sessionManager.GetCredRotations(ctxFn(r), sessionID, 100)
	writeJSON(w, http.StatusOK, sessionDetailResponse{Session: sess, Stats: stats, Rotations: rotations})
}

func (h *Handler) serveCredRotations(w http.ResponseWriter, r *http.Request, sessionID string) {
	if h.sessionManager == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not wired")
		return
	}
	sess, err := h.sessionManager.Get(ctxFn(r), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if !IsSuperAdminOrLegacy(r) && sess.TenantID != GetTenantID(r) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	limit := queryInt(r, "limit", 100)
	rotations, err := h.sessionManager.GetCredRotations(ctxFn(r), sessionID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "rotations": rotations, "total": len(rotations)})
}

func (h *Handler) serveStopSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.sessionManager == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not wired")
		return
	}
	if !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "stop requires super_admin")
		return
	}
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		reason = "admin_stop"
	}
	if err := h.sessionManager.StopSession(ctxFn(r), sessionID, reason); err != nil {
		writeError(w, http.StatusInternalServerError, "stop failed: "+err.Error())
		return
	}
	if h.sessionDBWriter != nil {
		h.sessionDBWriter.FlushSession(ctxFn(r), sessionID)
	}
	if auth := GetAuthContext(r); auth != nil {
		h.auditLog(auth.Username, "session.stop", "session", 0, map[string]any{"session_id": sessionID, "reason": reason})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "stopped_at": time.Now().UTC().Format(time.RFC3339)})
}

func (h *Handler) serveRecoverSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.sessionManager == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not wired")
		return
	}
	if !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "recover requires super_admin")
		return
	}
	if err := h.sessionManager.RecoverSession(ctxFn(r), sessionID); err != nil {
		writeError(w, http.StatusInternalServerError, "recover failed: "+err.Error())
		return
	}
	if auth := GetAuthContext(r); auth != nil {
		h.auditLog(auth.Username, "session.recover", "session", 0, map[string]any{"session_id": sessionID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "recovered_at": time.Now().UTC().Format(time.RFC3339)})
}

func (h *Handler) serveUpdateAnnotation(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.sessionManager == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not wired")
		return
	}
	if err := h.sessionManager.SetAnnotation(ctxFn(r), sessionID, r.URL.Query().Get("annotation")); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) serveUpdateTags(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.sessionManager == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not wired")
		return
	}
	tagStr := r.URL.Query().Get("tags")
	var tags []string
	if tagStr != "" {
		tags = strings.Split(tagStr, ",")
	}
	if err := h.sessionManager.SetTags(ctxFn(r), sessionID, tags); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func ctxFn(r *http.Request) context.Context {
	if r == nil {
		return context.Background()
	}
	return r.Context()
}

func (h *Handler) listActiveSessions(ctx context.Context, tenantID string, limit int) []string {
	if h.sessionManager == nil {
		return nil
	}
	rc := h.sessionManager.GetRedisClient()
	if rc == nil || rc.Client() == nil {
		return nil
	}
	iter := rc.Client().Scan(ctx, 0, "session:apiKey:*:active", 100).Iterator()
	ids := make([]string, 0, limit)
	for iter.Next(ctx) {
		members, err := rc.Client().SRandMemberN(ctx, iter.Val(), int64(limit)).Result()
		if err != nil {
			continue
		}
		for _, id := range members {
			if tenantID != "" {
				sess, err := h.sessionManager.Get(ctx, id)
				if err != nil || sess.TenantID != tenantID {
					continue
				}
			}
			ids = append(ids, id)
			if len(ids) >= limit {
				return ids
			}
		}
	}
	return ids
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// T1.5: enrichHealthData 批量查询 session_summaries 补充健康数据
func (h *Handler) enrichHealthData(ctx context.Context, items []sessionListItem) {
	if len(items) == 0 {
		return
	}

	// 构建 session_id 列表
	sessionIDs := make([]string, len(items))
	for i, item := range items {
		sessionIDs[i] = item.SessionID
	}

	// 批量查询健康数据
	query := `SELECT session_key, health_score, health_grade, outcome
		FROM session_summaries
		WHERE session_key = ANY($1)`

	rows, err := h.db.Query(ctx, query, sessionIDs)
	if err != nil {
		// 查询失败不阻塞，只是健康列为空
		return
	}
	defer rows.Close()

	// 构建 session_id -> health 的映射
	healthMap := make(map[string]struct {
		score   *int
		grade   *string
		outcome *string
	})

	for rows.Next() {
		var sessionKey string
		var score *int
		var grade *string
		var outcome *string
		if err := rows.Scan(&sessionKey, &score, &grade, &outcome); err != nil {
			continue
		}
		healthMap[sessionKey] = struct {
			score   *int
			grade   *string
			outcome *string
		}{score, grade, outcome}
	}

	// 将健康数据填充到 items
	for i := range items {
		if health, ok := healthMap[items[i].SessionID]; ok {
			items[i].HealthScore = health.score
			items[i].HealthGrade = health.grade
			items[i].Outcome = health.outcome
		}
	}
}

// T1.5: filterByHealthGrade 按健康等级过滤
func (h *Handler) filterByHealthGrade(items []sessionListItem, gradeFilter string) []sessionListItem {
	if gradeFilter == "" {
		return items
	}

	// 解析过滤等级（逗号分隔）
	grades := strings.Split(gradeFilter, ",")
	gradeSet := make(map[string]bool)
	for _, g := range grades {
		gradeSet[strings.TrimSpace(strings.ToUpper(g))] = true
	}

	filtered := make([]sessionListItem, 0, len(items))
	for _, item := range items {
		if item.HealthGrade == nil {
			// 未计算健康分的会话，根据是否包含 "unknown" 决定是否保留
			// 这里选择保留（因为可能是新会话）
			filtered = append(filtered, item)
			continue
		}
		if gradeSet[*item.HealthGrade] {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// T1.5: sortSessionList 排序会话列表
func (h *Handler) sortSessionList(items []sessionListItem, sortBy string) {
	// 定义状态优先级（error > waiting > active > stopped > recovered）
	statusPriority := func(status string) int {
		switch status {
		case "error":
			return 1
		case "waiting":
			return 2
		case session.StatusActive:
			return 3
		case session.StatusStopped:
			return 4
		case "recovered":
			return 5
		default:
			return 6
		}
	}

	// 根据 sortBy 参数决定排序逻辑
	switch sortBy {
	case "health":
		// 按健康分降序（分数高的在前）
		sort.Slice(items, func(i, j int) bool {
			// 优先按状态排序
			if statusPriority(items[i].Status) != statusPriority(items[j].Status) {
				return statusPriority(items[i].Status) < statusPriority(items[j].Status)
			}
			// 然后按健康分降序
			scoreI := 0
			if items[i].HealthScore != nil {
				scoreI = *items[i].HealthScore
			}
			scoreJ := 0
			if items[j].HealthScore != nil {
				scoreJ = *items[j].HealthScore
			}
			return scoreI > scoreJ // 降序：分高的在前
		})
	case "cost":
		// 按成本降序
		sort.Slice(items, func(i, j int) bool {
			if statusPriority(items[i].Status) != statusPriority(items[j].Status) {
				return statusPriority(items[i].Status) < statusPriority(items[j].Status)
			}
			return items[i].TotalCostUSD > items[j].TotalCostUSD
		})
	case "tokens":
		// 按 token 降序
		sort.Slice(items, func(i, j int) bool {
			if statusPriority(items[i].Status) != statusPriority(items[j].Status) {
				return statusPriority(items[i].Status) < statusPriority(items[j].Status)
			}
			return (items[i].TotalPromptTokens + items[i].TotalCompletionTokens) >
				(items[j].TotalPromptTokens + items[j].TotalCompletionTokens)
		})
	case "created_at":
		// 按创建时间降序（新的在前）
		sort.Slice(items, func(i, j int) bool {
			if statusPriority(items[i].Status) != statusPriority(items[j].Status) {
				return statusPriority(items[i].Status) < statusPriority(items[j].Status)
			}
			return items[i].CreatedAt.After(items[j].CreatedAt)
		})
	default:
		// 默认排序：status(error>waiting>active) + health_score DESC
		sort.Slice(items, func(i, j int) bool {
			// 优先按状态优先级排序
			if statusPriority(items[i].Status) != statusPriority(items[j].Status) {
				return statusPriority(items[i].Status) < statusPriority(items[j].Status)
			}
			// 然后按健康分降序（分数低的会话需要更多关注，但这里按文档是降序，即分高的在前）
			// 实际上文档说"D/F 会话在列表自动置顶"，所以应该是升序（分低的在前）
			// 让我重新理解：默认排序是"最需要关注的在最上面"
			// 所以应该是健康分升序（F=0-39 在最前，A=90-100 在最后）
			scoreI := 100 // 默认值（未计算时视为健康）
			if items[i].HealthScore != nil {
				scoreI = *items[i].HealthScore
			}
			scoreJ := 100
			if items[j].HealthScore != nil {
				scoreJ = *items[j].HealthScore
			}
			return scoreI < scoreJ // 升序：分低的在前（需要关注）
		})
	}
}
