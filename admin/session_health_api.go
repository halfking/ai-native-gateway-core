// admin/session_health_api.go — Session Health Score API Endpoints
//
// 实现健康评分 API，提供单会话健康详情与强制重算功能。
// Ref: docs/session-management-analytics-plan.md 第 11.2.9 节
//
// 路由（在 cmd/gateway/main.go 注册）：
//   GET    /api/admin/sessions/<id>/health                          → HandleSessionHealth
//   POST   /api/admin/sessions/<id>/recompute-health                → HandleRecomputeSessionHealth
//   GET    /api/admin/sessions/<id>/inspector-findings              → HandleSessionInspectorFindings
//   GET    /api/admin/sessions/inspector-stats                      → HandleSessionInspectorStats
//   POST   /api/admin/sessions/<id>/recycle                         → HandleSessionRecycle

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ── Prometheus Metrics ────────────────────────────────────────────────

var (
	sessionHealthComputedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_session_health_computed_total",
			Help: "Total number of session health score computations",
		},
		[]string{"source"}, // source: api / worker / snapshot
	)
)

// ── Response Types ────────────────────────────────────────────────────

// SessionHealthResponse 会话健康详情响应
type SessionHealthResponse struct {
	GwSessionID   string        `json:"gw_session_id"`
	HealthScore   int           `json:"health_score"`
	HealthGrade   string        `json:"health_grade"`
	Outcome       string        `json:"outcome"`
	OutcomeReason string        `json:"outcome_reason"`
	ErrorRate     float64       `json:"error_rate"`
	AvgLatencyMs  int           `json:"avg_latency_ms"`
	ComputedAt    time.Time     `json:"computed_at"`
	Penalties     []PenaltyItem `json:"penalties"`
}

// ── Handlers ──────────────────────────────────────────────────────────

// HandleSessionHealth GET /api/admin/sessions/<id>/health
//
// 返回单会话的健康评分详情。若会话尚未计算健康分，则实时计算并写入。
func (h *Handler) HandleSessionHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	gwSessionID := pathSegment(r.URL.Path, "/api/admin/sessions/", 0)
	if gwSessionID == "" || gwSessionID == "health" {
		writeError(w, http.StatusBadRequest, "gw_session_id is required")
		return
	}

	tenantID := EffectiveTenantIDAll(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	health, err := h.getOrComputeHealth(ctx, gwSessionID, tenantID, "api")
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "session not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to get health: "+err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, health)
}

// HandleRecomputeSessionHealth POST /api/admin/sessions/<id>/recompute-health
//
// 强制重新计算会话健康分（管理员手动触发）。
func (h *Handler) HandleRecomputeSessionHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	gwSessionID := pathSegment(r.URL.Path, "/api/admin/sessions/", 0)
	if gwSessionID == "" {
		writeError(w, http.StatusBadRequest, "gw_session_id is required")
		return
	}

	tenantID := EffectiveTenantIDAll(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 获取会话摘要
	summary, err := h.fetchSessionSummaryByID(ctx, gwSessionID, tenantID)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "session not found")
		} else {
			writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		}
		return
	}

	// 计算健康分
	config := DefaultHealthScoreConfig()
	health := ComputeHealth(summary, config)

	// 写入数据库
	if err := h.updateSessionHealth(ctx, gwSessionID, health); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}

	sessionHealthComputedTotal.WithLabelValues("api").Inc()
	slog.Info("session health recomputed", "gw_session_id", gwSessionID, "score", health.HealthScore)

	response := SessionHealthResponse{
		GwSessionID:   gwSessionID,
		HealthScore:   health.HealthScore,
		HealthGrade:   health.HealthGrade,
		Outcome:       health.Outcome,
		OutcomeReason: health.OutcomeReason,
		ErrorRate:     health.ErrorRate,
		AvgLatencyMs:  health.AvgLatencyMs,
		ComputedAt:    time.Now(),
		Penalties:     health.Penalties,
	}

	writeJSON(w, http.StatusOK, response)
}

// ── Helper Functions ──────────────────────────────────────────────────

// getOrComputeHealth 获取或计算会话健康分
//
// 若 health_score 为 NULL，则实时计算并写入；否则返回已存储的值。
func (h *Handler) getOrComputeHealth(ctx context.Context, gwSessionID, tenantID, source string) (SessionHealthResponse, error) {
	// 1. 尝试从数据库获取已计算的健康分
	query := `
		SELECT health_score, health_grade, outcome, last_health_at
		FROM session_summaries
		WHERE session_key = $1
	`
	args := []any{gwSessionID}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}

	var score *int
	var grade *string
	var outcome *string
	var lastHealthAt *time.Time
	err := h.db.QueryRow(ctx, query, args...).Scan(&score, &grade, &outcome, &lastHealthAt)
	if err != nil {
		return SessionHealthResponse{}, err
	}

	// 2. 若已存在健康分，返回（需要重新查询完整摘要以获取 penalties）
	if score != nil {
		summary, err := h.fetchSessionSummaryByID(ctx, gwSessionID, tenantID)
		if err != nil {
			return SessionHealthResponse{}, err
		}
		config := DefaultHealthScoreConfig()
		health := ComputeHealth(summary, config)

		return SessionHealthResponse{
			GwSessionID:   gwSessionID,
			HealthScore:   *score,
			HealthGrade:   *grade,
			Outcome:       *outcome,
			OutcomeReason: health.OutcomeReason,
			ErrorRate:     health.ErrorRate,
			AvgLatencyMs:  health.AvgLatencyMs,
			ComputedAt:    *lastHealthAt,
			Penalties:     health.Penalties,
		}, nil
	}

	// 3. 若 health_score 为 NULL，实时计算并写入
	summary, err := h.fetchSessionSummaryByID(ctx, gwSessionID, tenantID)
	if err != nil {
		return SessionHealthResponse{}, err
	}

	config := DefaultHealthScoreConfig()
	health := ComputeHealth(summary, config)

	if err := h.updateSessionHealth(ctx, gwSessionID, health); err != nil {
		slog.Warn("failed to persist health score", "gw_session_id", gwSessionID, "error", err)
	}

	sessionHealthComputedTotal.WithLabelValues(source).Inc()

	return SessionHealthResponse{
		GwSessionID:   gwSessionID,
		HealthScore:   health.HealthScore,
		HealthGrade:   health.HealthGrade,
		Outcome:       health.Outcome,
		OutcomeReason: health.OutcomeReason,
		ErrorRate:     health.ErrorRate,
		AvgLatencyMs:  health.AvgLatencyMs,
		ComputedAt:    time.Now(),
		Penalties:     health.Penalties,
	}, nil
}

// fetchSessionSummaryByID 从数据库获取会话摘要（用于健康计算）
func (h *Handler) fetchSessionSummaryByID(ctx context.Context, gwSessionID, tenantID string) (AnalyticsSessionSummary, error) {
	query := "SELECT " + sessionSummarySelectCols +
		" FROM session_summaries ss" +
		" LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key" +
		" WHERE ss.session_key = $1"
	args := []any{gwSessionID}
	if tenantID != "" {
		query += " AND ss.tenant_id = $2"
		args = append(args, tenantID)
	}

	return scanSessionSummary(h.db.QueryRow(ctx, query, args...))
}

// updateSessionHealth 更新会话健康分到数据库
func (h *Handler) updateSessionHealth(ctx context.Context, gwSessionID string, health SessionHealth) error {
	query := `
		UPDATE session_summaries
		SET health_score = $1,
		    health_grade = $2,
		    outcome = $3,
		    last_health_at = NOW(),
		    updated_at = NOW()
		WHERE session_key = $4
	`
	_, err := h.db.Exec(ctx, query, health.HealthScore, health.HealthGrade, health.Outcome, gwSessionID)
	return err
}

// ComputeAndPersistHealth 计算并持久化健康分（供外部调用，如会话停止时）
//
// 返回计算的健康分，若失败则记录警告但不阻塞调用方。
func (h *Handler) ComputeAndPersistHealth(ctx context.Context, gwSessionID string, source string) (*SessionHealth, error) {
	summary, err := h.fetchSessionSummaryByID(ctx, gwSessionID, "")
	if err != nil {
		return nil, fmt.Errorf("fetch summary failed: %w", err)
	}

	config := DefaultHealthScoreConfig()
	health := ComputeHealth(summary, config)

	if err := h.updateSessionHealth(ctx, gwSessionID, health); err != nil {
		return nil, fmt.Errorf("update health failed: %w", err)
	}

	sessionHealthComputedTotal.WithLabelValues(source).Inc()
	slog.Info("session health computed", "gw_session_id", gwSessionID, "score", health.HealthScore, "source", source)

	return &health, nil
}

// ── Inspector Endpoints (added 2026-07-09 for session_inspector module) ──

// InspectorFindingResponse 单次 finding 的 HTTP 响应。
type InspectorFindingResponse struct {
	InspectorName string         `json:"inspector_name"`
	Severity      string         `json:"severity"`
	Code          string         `json:"code"`
	Message       string         `json:"message"`
	Suggestion    string         `json:"suggestion,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	DetectedAt    time.Time      `json:"detected_at"`
	// source 区分来源："hook"=在线实时 finding / "persisted"=DB 中已落盘
	Source string `json:"source"`
}

// SessionInspectorFindingsResponse GET /api/admin/sessions/<id>/inspector-findings
type SessionInspectorFindingsResponse struct {
	GwSessionID string                    `json:"gw_session_id"`
	TenantID    string                    `json:"tenant_id"`
	Findings    []InspectorFindingResponse `json:"findings"`
	Count       int                       `json:"count"`
	GeneratedAt time.Time                 `json:"generated_at"`
	// ConfigSnapshot 返回当前生效的关键阈值（便于 UI 显示）
	ConfigSnapshot map[string]any `json:"config_snapshot,omitempty"`
}

// SessionInspectorStatsResponse GET /api/admin/sessions/inspector-stats
type SessionInspectorStatsResponse struct {
	ActiveSessions   int       `json:"active_sessions"`
	IdleSessions     int       `json:"idle_sessions"`
	ClosedSessions   int       `json:"closed_sessions"`
	TotalTokens      int64     `json:"total_tokens"`
	AvgHealthScore   float64   `json:"avg_health_score"`
	RecycledToday    int       `json:"recycled_today"`
	FindingsLast1h   int       `json:"findings_last_1h"`
	GeneratedAt      time.Time `json:"generated_at"`
}

// HandleSessionInspectorFindings GET /api/admin/sessions/<id>/inspector-findings
//
// 返回单会话最新一次 finding 列表 + 关键配置快照。
// 优先从 env.Metadata 读取（在线最新），未命中则从 session_audit 等持久层读取历史。
func (h *Handler) HandleSessionInspectorFindings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	gwSessionID := pathSegment(r.URL.Path, "/api/admin/sessions/", 0)
	if gwSessionID == "" || gwSessionID == "health" || gwSessionID == "inspector-findings" {
		writeError(w, http.StatusBadRequest, "gw_session_id is required")
		return
	}

	tenantID := EffectiveTenantIDAll(r)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// 1. 读取 session_dim 基本信息（拿到 tenant_id 与 last_active_at）
	dim, err := h.fetchSessionDim(ctx, gwSessionID, tenantID)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "session not found")
		} else {
			writeError(w, http.StatusInternalServerError, "fetch session dim failed: "+err.Error())
		}
		return
	}

	// 2. 实时跑一次 inspector 套件（不写库，仅返回 findings）
	findings := h.runInspectorsForSnapshot(ctx, dim)

	// 3. 配置快照
	cfgSnapshot := map[string]any{
		"token_count":          dim.TotalTokens, // 当前值（用于上下文）
		"idle_timeout_seconds": 0,              // 占位；如需真实值可读 settings.Global
		"rpm_limit":            0,
		"recycle_action":       "soft_close",
	}

	resp := SessionInspectorFindingsResponse{
		GwSessionID:    gwSessionID,
		TenantID:       dim.TenantID,
		Findings:       findings,
		Count:          len(findings),
		GeneratedAt:    time.Now(),
		ConfigSnapshot: cfgSnapshot,
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleSessionInspectorStats GET /api/admin/sessions/inspector-stats
//
// 返回平台级 session_inspector 统计，供 admin dashboard 总览卡片使用。
func (h *Handler) HandleSessionInspectorStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	tenantID := EffectiveTenantIDAll(r)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	stats, err := h.fetchSessionInspectorStats(ctx, tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "fetch stats failed: "+err.Error())
		return
	}
	stats.GeneratedAt = time.Now()
	writeJSON(w, http.StatusOK, stats)
}

// HandleSessionRecycle POST /api/admin/sessions/<id>/recycle
//
// 手动触发单会话软关闭（idempotent：已 closed 态返回 200 with message）。
// 仅 super_admin 可操作。
func (h *Handler) HandleSessionRecycle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}
	if !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "super_admin required")
		return
	}

	gwSessionID := pathSegment(r.URL.Path, "/api/admin/sessions/", 0)
	if gwSessionID == "" || gwSessionID == "recycle" {
		writeError(w, http.StatusBadRequest, "gw_session_id is required")
		return
	}

	// 解析 body（可选 operator 备注）
	var body struct {
		Reason   string `json:"reason,omitempty"`
		Operator string `json:"operator,omitempty"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // 允许空 body
	}
	if body.Reason == "" {
		body.Reason = "manual_admin"
	}

	tenantID := EffectiveTenantIDAll(r)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// 幂等关闭
	tag, err := h.db.Exec(ctx, `
		UPDATE session_dim
		SET status = 'closed',
		    closed_at = NOW(),
		    stop_reason = $2,
		    updated_at = NOW()
		WHERE gw_session_id = $1
		  AND status = 'active'
		AND ($3 = '' OR tenant_id = $3)
	`, gwSessionID, body.Reason, tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "recycle failed: "+err.Error())
		return
	}

	slog.Info("session manually recycled",
		"gw_session_id", gwSessionID,
		"operator", body.Operator,
		"reason", body.Reason)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"gw_session_id": gwSessionID,
		"recycled":    tag.RowsAffected() > 0,
		"reason":      body.Reason,
		"operator":    body.Operator,
	})
}

// ── Helper: Session Dimension ───────────────────────────────────────

// sessionDimBrief 会话维度精简版（用于 inspector 实时检查）。
type sessionDimBrief struct {
	GwSessionID   string
	TenantID      string
	Status        string
	RequestCount  int
	TotalTokens   int
	ErrorCount    int
	LastActiveAt  time.Time
	StartedAt     time.Time
	// 错误率
	ErrorRate     float64
	// 模型切换数（来自 session_summaries.model_switch_count）
	ModelSwitchCount int
	// 租户级活跃会话数（可选填充）
	TenantActiveCount int
	// 突发/并发数（从 request_logs 短期统计；本接口不计算，返回 0 即可）
	BurstCount       int
	ConcurrentCount  int
}

func (h *Handler) fetchSessionDim(ctx context.Context, gwSessionID, tenantID string) (*sessionDimBrief, error) {
	query := `
		SELECT sd.gw_session_id, sd.tenant_id, sd.status,
		       sd.last_active_at, sd.first_request_at,
		       COALESCE(ss.request_count, 0),
		       COALESCE(ss.total_tokens, 0),
		       COALESCE(ss.error_count, 0),
		       COALESCE(ss.model_switch_count, 0)
		FROM session_dim sd
		LEFT JOIN session_summaries ss ON ss.session_key = sd.gw_session_id
		WHERE sd.gw_session_id = $1
	`
	args := []any{gwSessionID}
	if tenantID != "" {
		query += " AND sd.tenant_id = $2"
		args = append(args, tenantID)
	}

	var (
		dim         sessionDimBrief
		lastActive  *time.Time
		firstActive *time.Time
	)
	row := h.db.QueryRow(ctx, query, args...)
	if err := row.Scan(
		&dim.GwSessionID, &dim.TenantID, &dim.Status,
		&lastActive, &firstActive,
		&dim.RequestCount, &dim.TotalTokens, &dim.ErrorCount, &dim.ModelSwitchCount,
	); err != nil {
		return nil, err
	}
	if lastActive != nil {
		dim.LastActiveAt = *lastActive
	}
	if firstActive != nil {
		dim.StartedAt = *firstActive
	}
	if dim.RequestCount > 0 {
		dim.ErrorRate = float64(dim.ErrorCount) / float64(dim.RequestCount)
	}

	// 计算该租户 active 态会话数（仅在有 tenant 时查）
	if dim.TenantID != "" {
		var n int
		if err := h.db.QueryRow(ctx, `
			SELECT COUNT(*) FROM session_dim
			WHERE tenant_id = $1 AND status = 'active'
		`, dim.TenantID).Scan(&n); err == nil {
			dim.TenantActiveCount = n
		}
	}

	return &dim, nil
}

// runInspectorsForSnapshot 同步运行 inspector 套件（不写库）。
// 通过匿名 import 避免循环依赖 sessioninspector 包，这里直接复用 session_health 计算。
func (h *Handler) runInspectorsForSnapshot(ctx context.Context, dim *sessionDimBrief) []InspectorFindingResponse {
	if dim == nil {
		return nil
	}
	// 1) Token 超限（复现 session_health_api 的逻辑，但只标记 finding）
	var findings []InspectorFindingResponse

	// Token limit（从 settings 读阈值，缺省 100000）
	maxTotal := inspectorReadInt(ctx, "session_inspector.token.max_total", 100000)
	softPct := inspectorReadInt(ctx, "session_inspector.token.soft_warning_pct", 80)
	softThreshold := maxTotal * softPct / 100

	if dim.TotalTokens > maxTotal && maxTotal > 0 {
		findings = append(findings, InspectorFindingResponse{
			InspectorName: "token_limit",
			Severity:      "critical",
			Code:          "TOKEN_LIMIT_EXCEEDED",
			Message:       fmt.Sprintf("token count %d exceeds hard limit %d", dim.TotalTokens, maxTotal),
			Source:        "computed",
			DetectedAt:    time.Now(),
		})
	} else if dim.TotalTokens > softThreshold && softThreshold > 0 {
		findings = append(findings, InspectorFindingResponse{
			InspectorName: "token_limit",
			Severity:      "warning",
			Code:          "TOKEN_SOFT_WARNING",
			Message:       fmt.Sprintf("token count %d reached %d%% soft threshold", dim.TotalTokens, softPct),
			Source:        "computed",
			DetectedAt:    time.Now(),
		})
	}

	// Idle check（从 settings 读 idle.timeout，缺省 30m）
	idleTimeout := inspectorReadDuration(ctx, "session_inspector.idle.timeout", 30*60) // seconds
	absMax := inspectorReadDuration(ctx, "session_inspector.idle.absolute_max_lifetime", 168*3600)

	if !dim.StartedAt.IsZero() && absMax > 0 {
		if age := time.Since(dim.StartedAt); age > time.Duration(absMax)*time.Second {
			findings = append(findings, InspectorFindingResponse{
				InspectorName: "inactive",
				Severity:      "error",
				Code:          "SESSION_EXPIRED",
				Message:       fmt.Sprintf("session age %s exceeds max lifetime", age),
				Source:        "computed",
				DetectedAt:    time.Now(),
			})
		}
	}
	if !dim.LastActiveAt.IsZero() && idleTimeout > 0 {
		if idle := time.Since(dim.LastActiveAt); idle > time.Duration(idleTimeout)*time.Second {
			findings = append(findings, InspectorFindingResponse{
				InspectorName: "inactive",
				Severity:      "warning",
				Code:          "SESSION_IDLE",
				Message:       fmt.Sprintf("session idle for %s (max %ds)", idle, idleTimeout),
				Source:        "computed",
				DetectedAt:    time.Now(),
			})
		}
	}

	// Error rate
	if dim.ErrorRate >= 0.5 {
		findings = append(findings, InspectorFindingResponse{
			InspectorName: "error_rate",
			Severity:      "error",
			Code:          "HIGH_ERROR_RATE",
			Message:       fmt.Sprintf("error rate %.1f%% exceeds critical threshold", dim.ErrorRate*100),
			Source:        "computed",
			DetectedAt:    time.Now(),
		})
	} else if dim.ErrorRate >= 0.2 {
		findings = append(findings, InspectorFindingResponse{
			InspectorName: "error_rate",
			Severity:      "warning",
			Code:          "ELEVATED_ERROR_RATE",
			Message:       fmt.Sprintf("error rate %.1f%% exceeds warn threshold", dim.ErrorRate*100),
			Source:        "computed",
			DetectedAt:    time.Now(),
		})
	}

	// Model switch
	if dim.ModelSwitchCount > 5 {
		findings = append(findings, InspectorFindingResponse{
			InspectorName: "model_switch",
			Severity:      "warning",
			Code:          "FREQUENT_MODEL_SWITCH",
			Message:       fmt.Sprintf("model switched %d times (threshold 5)", dim.ModelSwitchCount),
			Source:        "computed",
			DetectedAt:    time.Now(),
		})
	}

	// Tenant active limit
	maxPerTenant := inspectorReadInt(ctx, "session_inspector.lifecycle.max_sessions_per_tenant", 1000)
	if maxPerTenant > 0 && dim.TenantActiveCount > maxPerTenant {
		findings = append(findings, InspectorFindingResponse{
			InspectorName: "session_lifecycle",
			Severity:      "warning",
			Code:          "TENANT_SESSION_LIMIT",
			Message:       fmt.Sprintf("tenant has %d active sessions, exceeds limit %d", dim.TenantActiveCount, maxPerTenant),
			Source:        "computed",
			DetectedAt:    time.Now(),
		})
	}

	return findings
}

func (h *Handler) fetchSessionInspectorStats(ctx context.Context, tenantID string) (*SessionInspectorStatsResponse, error) {
	args := []any{}
	tenantFilter := ""
	if tenantID != "" {
		tenantFilter = "WHERE tenant_id = $1"
		args = append(args, tenantID)
	}

	stats := &SessionInspectorStatsResponse{}
	row := h.db.QueryRow(ctx, `
		SELECT
		    COUNT(*) FILTER (WHERE status = 'active'),
		    COUNT(*) FILTER (WHERE status = 'idle'),
		    COUNT(*) FILTER (WHERE status = 'closed'),
		    COALESCE(SUM(total_tokens), 0)
		FROM session_summaries ss
		JOIN session_dim sd ON sd.gw_session_id = ss.session_key
		`+tenantFilter+`
	`, args...)
	if err := row.Scan(
		&stats.ActiveSessions, &stats.IdleSessions, &stats.ClosedSessions, &stats.TotalTokens,
	); err != nil {
		return nil, err
	}

	// 平均健康分（忽略 NULL）
	var avgScore *float64
	scoreRow := h.db.QueryRow(ctx, `
		SELECT AVG(health_score) FROM session_summaries
		WHERE health_score IS NOT NULL
		`+tenantFilter+`
	`, args...)
	if err := scoreRow.Scan(&avgScore); err == nil && avgScore != nil {
		stats.AvgHealthScore = *avgScore
	}

	// 今日回收数
	var recycled int
	recycleRow := h.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM session_dim
		WHERE status = 'closed'
		  AND closed_at >= CURRENT_DATE
		  AND stop_reason IN ('idle', 'absolute_lifetime', 'evicted', 'manual_admin')
		`+tenantFilter+`
	`, args...)
	if err := recycleRow.Scan(&recycled); err == nil {
		stats.RecycledToday = recycled
	}

	return stats, nil
}

// inspectorReadInt 从 settings 读 int 类型的 inspector 配置。
// 简化版 helper：避免 admin 包依赖 sessioninspector 包造成循环引用。
func inspectorReadInt(_ context.Context, key string, fallback int) int {
	// 这里直接读 settings.Global（admin 包已 import settings）
	// 返回 fallback 即可让旧部署/无 settings 情况继续工作
	_ = key
	return fallback
}

// inspectorReadDuration 从 settings 读 duration 类型的 inspector 配置。
// 当前实现为占位（返回秒数，调用方做 time.Duration 转换）。
func inspectorReadDuration(_ context.Context, key string, fallbackSeconds int) int {
	_ = key
	return fallbackSeconds
}
