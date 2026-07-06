// Package admin — Session Analytics Handler (重写为 net/http + pgxpool)
//
// 350 迁移修复后，session_summaries.session_key 的值 = request_logs.gw_session_id。
// 所有查询统一使用 gw_session_id 作为对外标识。
//
// 路由（在 cmd/gateway/main.go 注册）：
//   GET    /api/admin/session-analytics                → HandleSessionAnalyticsList
//   GET    /api/admin/session-analytics/stats          → HandleSessionAnalyticsStats
//   GET    /api/admin/session-analytics/<gw_session_id>→ HandleSessionAnalyticsDetail
//   GET    /api/admin/session-analytics/<gw_session_id>/export → HandleSessionAnalyticsExport
package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ── Response types ────────────────────────────────────────────────────

// AnalyticsSessionSummary 会话摘要（对外暴露 gw_session_id）
type AnalyticsSessionSummary struct {
	GwSessionID            string     `json:"gw_session_id"`
	TenantID               string     `json:"tenant_id"`
	TaskID                 *string    `json:"task_id,omitempty"`
	SessionStatus          *string    `json:"session_status,omitempty"`
	FirstRequestAt         time.Time  `json:"first_request_at"`
	LastRequestAt          time.Time  `json:"last_request_at"`
	DurationSeconds        int        `json:"duration_seconds"`
	RequestCount           int        `json:"request_count"`
	SuccessCount           int        `json:"success_count"`
	ErrorCount             int        `json:"error_count"`
	TotalCostUSD           float64    `json:"total_cost_usd"`
	InputCostUSD           float64    `json:"input_cost_usd"`
	OutputCostUSD          float64    `json:"output_cost_usd"`
	TotalPromptTokens      int64      `json:"total_prompt_tokens"`
	TotalCompletionTokens  int64      `json:"total_completion_tokens"`
	TotalTokens            int64      `json:"total_tokens"`
	AvgLatencyMs           int        `json:"avg_latency_ms"`
	MinLatencyMs           *int       `json:"min_latency_ms"`
	MaxLatencyMs           *int       `json:"max_latency_ms"`
	ModelsUsed             []string   `json:"models_used"`
	PrimaryModel           *string    `json:"primary_model"`
	ModelSwitchCount       int        `json:"model_switch_count"`
	Title                  *string    `json:"title"`
	Summary                *string    `json:"summary"`
	KeyTopics              []string   `json:"key_topics"`
	UserIntent             *string    `json:"user_intent"`
	QualityScore           *int       `json:"quality_score"`
	ComplianceStatus       string     `json:"compliance_status"`
	ComplianceIssuesCount  int        `json:"compliance_issues_count"`
	PromptInjectionDetected bool      `json:"prompt_injection_detected"`
	PIIDetected            bool       `json:"pii_detected"`
	ToxicOutputDetected    bool       `json:"toxic_output_detected"`
	WorkTypes              []string   `json:"work_types"`
	Providers              []string   `json:"providers"`
	ClientModels           []string   `json:"client_models"`
	LastSummarizedAt       *time.Time `json:"last_summarized_at"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	// 健康评分字段（T1.5）
	HealthScore            *int       `json:"health_score,omitempty"`
	HealthGrade            *string    `json:"health_grade,omitempty"`
	Outcome                *string    `json:"outcome,omitempty"`
	LastHealthAt           *time.Time `json:"last_health_at,omitempty"`
}

// AnalyticsSessionStats 会话统计（今日）
type AnalyticsSessionStats struct {
	TotalSessions       int     `json:"total_sessions"`
	ActiveSessions      int     `json:"active_sessions"`
	TotalRequests       int64   `json:"total_requests"`
	TotalCost           float64 `json:"total_cost"`
	AvgCostPerSession   float64 `json:"avg_cost_per_session"`
	AvgTokensPerSession int64   `json:"avg_tokens_per_session"`
	AvgLatency          int     `json:"avg_latency"`
	ComplianceRate      float64 `json:"compliance_rate"`
	HighQualityRate     float64 `json:"high_quality_rate"`
}

// RequestEvent 单步请求事件
type RequestEvent struct {
	RequestID        string    `json:"request_id"`
	CreatedAt        time.Time `json:"created_at"`
	Success          bool      `json:"success"`
	ClientModel      string    `json:"client_model"`
	UpstreamModel    string    `json:"upstream_model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	LatencyMs        int       `json:"latency_ms"`
	WorkType         *string   `json:"work_type,omitempty"`
	Provider         *string   `json:"provider,omitempty"`
	CompressionStrategy *string `json:"compression_strategy,omitempty"`
	CacheReadTokens  *int      `json:"cache_read_tokens,omitempty"`
	ErrorMessage     *string   `json:"error_message,omitempty"`
	RequestPreview   *string   `json:"request_preview,omitempty"`
	ResponsePreview  *string   `json:"response_preview,omitempty"`
}

// AnalyticsSessionDetail 会话详情（摘要 + 时间线 + 分析）
type AnalyticsSessionDetail struct {
	Summary  AnalyticsSessionSummary `json:"summary"`
	Timeline []RequestEvent          `json:"timeline"`
	Analysis SessionAnalysis         `json:"analysis"`
}

// SessionAnalysis 会话分析（成本/token 分解 + 模型切换 + 合规）
type SessionAnalysis struct {
	ModelSwitches     []ModelSwitch     `json:"model_switches"`
	ComplianceIssues  []ComplianceIssue `json:"compliance_issues"`
	CostBreakdown     CostBreakdown     `json:"cost_breakdown"`
	TokenDistribution TokenDistribution `json:"token_distribution"`
	CacheSavings      *CacheSavings     `json:"cache_savings,omitempty"`
	CompressionSavings *CompressionSavings `json:"compression_savings,omitempty"`
}

// ModelSwitch 模型切换
type ModelSwitch struct {
	RequestID string    `json:"request_id"`
	Timestamp time.Time `json:"timestamp"`
	FromModel string    `json:"from_model"`
	ToModel   string    `json:"to_model"`
	Reason    string    `json:"reason"`
}

// ComplianceIssue 合规问题
type ComplianceIssue struct {
	RequestID   string    `json:"request_id"`
	Timestamp   time.Time `json:"timestamp"`
	IssueType   string    `json:"issue_type"`
	Severity    int       `json:"severity"`
	Description string    `json:"description"`
	ActionTaken string    `json:"action_taken"`
}

// CostBreakdown 成本分解
type CostBreakdown struct {
	InputCost  float64            `json:"input_cost"`
	OutputCost float64            `json:"output_cost"`
	TotalCost  float64            `json:"total_cost"`
	ByModel    map[string]float64 `json:"by_model"`
	ByProvider map[string]float64 `json:"by_provider"`
}

// TokenDistribution Token 分布
type TokenDistribution struct {
	PromptTokens     int64            `json:"prompt_tokens"`
	CompletionTokens int64            `json:"completion_tokens"`
	TotalTokens      int64            `json:"total_tokens"`
	ByModel          map[string]int64 `json:"by_model"`
}

// CacheSavings 缓存节省（prompt cache）
type CacheSavings struct {
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	EstimatedSavedUSD float64 `json:"estimated_saved_usd"`
}

// CompressionSavings 压缩节省
type CompressionSavings struct {
	CompressedRequests int     `json:"compressed_requests"`
	OutboundTokenEst   int64   `json:"outbound_token_est"`
	EstimatedTokensSaved int64 `json:"estimated_tokens_saved"`
	EstimatedSavedUSD  float64 `json:"estimated_saved_usd"`
}

// ── SQL column list (used by list + detail) ───────────────────────────

const sessionSummarySelectCols = `ss.session_key, ss.tenant_id, sd.task_id, sd.status,
	ss.first_request_at, ss.last_request_at, ss.duration_seconds,
	ss.request_count, ss.success_count, ss.error_count,
	ss.total_cost_usd, ss.input_cost_usd, ss.output_cost_usd,
	ss.total_prompt_tokens, ss.total_completion_tokens, ss.total_tokens,
	ss.avg_latency_ms, ss.min_latency_ms, ss.max_latency_ms,
	ss.models_used, ss.primary_model, ss.model_switch_count,
	ss.title, ss.summary, ss.key_topics, ss.user_intent, ss.quality_score,
	ss.compliance_status, ss.compliance_issues_count,
	ss.prompt_injection_detected, ss.pii_detected, ss.toxic_output_detected,
	ss.work_types, ss.providers, ss.client_models,
	ss.last_summarized_at, ss.created_at, ss.updated_at,
	ss.health_score, ss.health_grade, ss.outcome, ss.last_health_at`

// scanSessionSummary scans one row into AnalyticsSessionSummary.
func scanSessionSummary(row pgx.Row) (AnalyticsSessionSummary, error) {
	var s AnalyticsSessionSummary
	err := row.Scan(
		&s.GwSessionID, &s.TenantID, &s.TaskID, &s.SessionStatus,
		&s.FirstRequestAt, &s.LastRequestAt, &s.DurationSeconds,
		&s.RequestCount, &s.SuccessCount, &s.ErrorCount,
		&s.TotalCostUSD, &s.InputCostUSD, &s.OutputCostUSD,
		&s.TotalPromptTokens, &s.TotalCompletionTokens, &s.TotalTokens,
		&s.AvgLatencyMs, &s.MinLatencyMs, &s.MaxLatencyMs,
		&s.ModelsUsed, &s.PrimaryModel, &s.ModelSwitchCount,
		&s.Title, &s.Summary, &s.KeyTopics, &s.UserIntent, &s.QualityScore,
		&s.ComplianceStatus, &s.ComplianceIssuesCount,
		&s.PromptInjectionDetected, &s.PIIDetected, &s.ToxicOutputDetected,
		&s.WorkTypes, &s.Providers, &s.ClientModels,
		&s.LastSummarizedAt, &s.CreatedAt, &s.UpdatedAt,
		&s.HealthScore, &s.HealthGrade, &s.Outcome, &s.LastHealthAt,
	)
	return s, err
}

// ── Handlers ──────────────────────────────────────────────────────────

// HandleSessionAnalyticsList GET /api/admin/session-analytics
func (h *Handler) HandleSessionAnalyticsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	tenantID := EffectiveTenantIDAll(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 分页
	page := queryInt(r, "page", 1)
	if page < 1 {
		page = 1
	}
	pageSize := queryInt(r, "page_size", 20)
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	// 筛选
	complianceStatus := r.URL.Query().Get("compliance_status")
	userIntent := r.URL.Query().Get("user_intent")
	minCost := r.URL.Query().Get("min_cost")
	maxCost := r.URL.Query().Get("max_cost")
	search := r.URL.Query().Get("search")

	// 排序（白名单校验）
	orderBy := r.URL.Query().Get("order_by")
	if orderBy == "" {
		orderBy = "last_request_at"
	}
	if err := ValidateOrderByColumn("session_summaries", orderBy); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid order_by: "+err.Error())
		return
	}
	orderDir := r.URL.Query().Get("order_dir")
	if orderDir != "ASC" && orderDir != "DESC" {
		orderDir = "DESC"
	}

	// 构建 WHERE
	args := []any{}
	argCount := 1
	where := " WHERE 1=1"
	if tenantID != "" {
		where += " AND ss.tenant_id = $" + strconv.Itoa(argCount)
		args = append(args, tenantID)
		argCount++
	}
	if complianceStatus != "" {
		where += " AND ss.compliance_status = $" + strconv.Itoa(argCount)
		args = append(args, complianceStatus)
		argCount++
	}
	if userIntent != "" {
		where += " AND ss.user_intent = $" + strconv.Itoa(argCount)
		args = append(args, userIntent)
		argCount++
	}
	if minCost != "" {
		where += " AND ss.total_cost_usd >= $" + strconv.Itoa(argCount)
		args = append(args, minCost)
		argCount++
	}
	if maxCost != "" {
		where += " AND ss.total_cost_usd <= $" + strconv.Itoa(argCount)
		args = append(args, maxCost)
		argCount++
	}
	if search != "" {
		where += " AND (ss.title ILIKE $" + strconv.Itoa(argCount) +
			" OR $" + strconv.Itoa(argCount+1) + " = ANY(ss.key_topics))"
		args = append(args, "%"+search+"%", search)
		argCount += 2
	}

	query := "SELECT " + sessionSummarySelectCols +
		" FROM session_summaries ss" +
		" LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key" +
		where + " ORDER BY ss." + orderBy + " " + orderDir +
		" LIMIT $" + strconv.Itoa(argCount) +
		" OFFSET $" + strconv.Itoa(argCount+1)
	args = append(args, pageSize, offset)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	sessions := []AnalyticsSessionSummary{}
	for rows.Next() {
		s, err := scanSessionSummary(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed: "+err.Error())
			return
		}
		sessions = append(sessions, s)
	}

	// 总数
	countQuery := "SELECT COUNT(*) FROM session_summaries ss" + where
	var total int
	_ = h.db.QueryRow(ctx, countQuery, args[:len(args)-2]...).Scan(&total)

	writeJSON(w, http.StatusOK, map[string]any{
		"sessions":  sessions,
		"page":      page,
		"page_size": pageSize,
		"total":     total,
	})
}

// HandleSessionAnalyticsStats GET /api/admin/session-analytics/stats
func (h *Handler) HandleSessionAnalyticsStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}
	tenantID := EffectiveTenantIDAll(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	query := `
		SELECT
			COALESCE(COUNT(*), 0),
			COALESCE(COUNT(*) FILTER (WHERE last_request_at > NOW() - INTERVAL '1 hour'), 0),
			COALESCE(SUM(request_count), 0),
			COALESCE(SUM(total_cost_usd), 0),
			COALESCE(AVG(total_cost_usd), 0),
			COALESCE(AVG(total_tokens), 0)::BIGINT,
			COALESCE(AVG(avg_latency_ms), 0)::INT,
			COALESCE(COUNT(*) FILTER (WHERE compliance_status = 'compliant') * 100.0 / NULLIF(COUNT(*), 0), 0),
			COALESCE(COUNT(*) FILTER (WHERE quality_score >= 8) * 100.0 / NULLIF(COUNT(*) FILTER (WHERE quality_score IS NOT NULL), 0), 0)
		FROM session_summaries
		WHERE first_request_at >= CURRENT_DATE`
	args := []any{}
	if tenantID != "" {
		query += " AND tenant_id = $1"
		args = append(args, tenantID)
	}

	var stats AnalyticsSessionStats
	err := h.db.QueryRow(ctx, query, args...).Scan(
		&stats.TotalSessions,
		&stats.ActiveSessions,
		&stats.TotalRequests,
		&stats.TotalCost,
		&stats.AvgCostPerSession,
		&stats.AvgTokensPerSession,
		&stats.AvgLatency,
		&stats.ComplianceRate,
		&stats.HighQualityRate,
	)
	if err != nil && err != pgx.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "stats query failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// HandleSessionAnalyticsDetail GET /api/admin/session-analytics/<id>
func (h *Handler) HandleSessionAnalyticsDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	gwSessionID := pathSegment(r.URL.Path, "/api/admin/session-analytics/", 0)
	if gwSessionID == "" || gwSessionID == "stats" {
		writeError(w, http.StatusBadRequest, "gw_session_id is required")
		return
	}
	tenantID := EffectiveTenantIDAll(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 摘要
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
	if err == pgx.ErrNoRows {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	// 请求时间线（直接查 request_logs，按 gw_session_id）
	timelineQuery := `
		SELECT request_id, ts, success, client_model, outbound_model,
		       COALESCE(prompt_tokens,0), COALESCE(completion_tokens,0),
		       COALESCE(cost_usd,0), COALESCE(latency_ms,0),
		       work_type, compression_strategy, cache_read_tokens,
		       error_kind, request_preview, response_preview
		FROM request_logs
		WHERE gw_session_id = $1`
	tArgs := []any{gwSessionID}
	if tenantID != "" {
		timelineQuery += " AND tenant_id = $2"
		tArgs = append(tArgs, tenantID)
	}
	timelineQuery += " ORDER BY ts ASC LIMIT 100"

	rows, err := h.db.Query(ctx, timelineQuery, tArgs...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "timeline query failed: "+err.Error())
		return
	}
	defer rows.Close()

	timeline := []RequestEvent{}
	for rows.Next() {
		var e RequestEvent
		var ts time.Time
		if err := rows.Scan(
			&e.RequestID, &ts, &e.Success, &e.ClientModel, &e.UpstreamModel,
			&e.PromptTokens, &e.CompletionTokens, &e.CostUSD, &e.LatencyMs,
			&e.WorkType, &e.CompressionStrategy, &e.CacheReadTokens,
			&e.ErrorMessage, &e.RequestPreview, &e.ResponsePreview,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "timeline scan failed: "+err.Error())
			return
		}
		e.CreatedAt = ts
		timeline = append(timeline, e)
	}

	analysis := h.buildSessionAnalysis(ctx, tenantID, gwSessionID, timeline)

	writeJSON(w, http.StatusOK, AnalyticsSessionDetail{
		Summary:  summary,
		Timeline: timeline,
		Analysis: analysis,
	})
}

// HandleSessionAnalyticsExport GET /api/admin/session-analytics/<id>/export
func (h *Handler) HandleSessionAnalyticsExport(w http.ResponseWriter, r *http.Request) {
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

	query := `SELECT row_to_json(t) FROM (
		SELECT ss.*, sd.task_id, sd.status AS session_status
		FROM session_summaries ss
		LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key
		WHERE ss.session_key = $1`
	args := []any{gwSessionID}
	if tenantID != "" {
		query += " AND ss.tenant_id = $2"
		args = append(args, tenantID)
	}
	query += ") t"

	var result []byte
	err := h.db.QueryRow(ctx, query, args...).Scan(&result)
	if err == pgx.ErrNoRows {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export failed: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=session_"+gwSessionID+".json")
	_, _ = w.Write(result)
}

// buildSessionAnalysis 构建会话分析（成本/token 分解 + 模型切换 + 节省）
func (h *Handler) buildSessionAnalysis(ctx context.Context, tenantID, gwSessionID string, timeline []RequestEvent) SessionAnalysis {
	analysis := SessionAnalysis{
		ModelSwitches:    []ModelSwitch{},
		ComplianceIssues: []ComplianceIssue{},
		CostBreakdown: CostBreakdown{
			ByModel:    make(map[string]float64),
			ByProvider: make(map[string]float64),
		},
		TokenDistribution: TokenDistribution{
			ByModel: make(map[string]int64),
		},
	}

	// 模型切换检测
	var lastModel string
	for i, event := range timeline {
		if i > 0 && event.UpstreamModel != "" && event.UpstreamModel != lastModel && lastModel != "" {
			analysis.ModelSwitches = append(analysis.ModelSwitches, ModelSwitch{
				RequestID: event.RequestID,
				Timestamp: event.CreatedAt,
				FromModel: lastModel,
				ToModel:   event.UpstreamModel,
				Reason:    "auto-routed",
			})
		}
		if event.UpstreamModel != "" {
			lastModel = event.UpstreamModel
		}
	}

	// 成本分解 + token 分布（基于 timeline 实际值）
	var cacheRead int64
	var compressedCount int
	var outboundTokenEst int64
	for _, event := range timeline {
		analysis.CostBreakdown.TotalCost += event.CostUSD
		analysis.TokenDistribution.PromptTokens += int64(event.PromptTokens)
		analysis.TokenDistribution.CompletionTokens += int64(event.CompletionTokens)

		if event.UpstreamModel != "" {
			analysis.CostBreakdown.ByModel[event.UpstreamModel] += event.CostUSD
			analysis.TokenDistribution.ByModel[event.UpstreamModel] += int64(event.PromptTokens + event.CompletionTokens)
		}
		if event.Provider != nil && *event.Provider != "" {
			analysis.CostBreakdown.ByProvider[*event.Provider] += event.CostUSD
		}
		if event.CacheReadTokens != nil {
			cacheRead += int64(*event.CacheReadTokens)
		}
		if event.CompressionStrategy != nil && *event.CompressionStrategy != "" {
			compressedCount++
		}
	}
	analysis.TokenDistribution.TotalTokens = analysis.TokenDistribution.PromptTokens + analysis.TokenDistribution.CompletionTokens
	// 用 session_summaries 已拆分的 input/output 比例（更准）
	analysis.CostBreakdown.InputCost = analysis.CostBreakdown.TotalCost * 0.4
	analysis.CostBreakdown.OutputCost = analysis.CostBreakdown.TotalCost * 0.6
	if total := analysis.TokenDistribution.TotalTokens; total > 0 {
		ratio := float64(analysis.TokenDistribution.PromptTokens) / float64(total)
		analysis.CostBreakdown.InputCost = analysis.CostBreakdown.TotalCost * ratio
		analysis.CostBreakdown.OutputCost = analysis.CostBreakdown.TotalCost - analysis.CostBreakdown.InputCost
	}

	// cache 节省（估算：cache_read 按 input 价 × 0.1 计）
	if cacheRead > 0 {
		analysis.CacheSavings = &CacheSavings{
			CacheReadTokens:   cacheRead,
			EstimatedSavedUSD: 0, // 价格未知，先占位；后续按模型单价计算
		}
	}
	if compressedCount > 0 {
		analysis.CompressionSavings = &CompressionSavings{
			CompressedRequests: compressedCount,
			OutboundTokenEst:   outboundTokenEst,
		}
	}

	// 合规问题（prompt_injection + output_compliance）
	if h.db != nil {
		complianceQuery := `
			SELECT request_id, detected_at, issue_type, severity, evidence, action_taken
			FROM (
				SELECT request_id, detected_at, issue_type, severity, evidence, action_taken
				FROM prompt_injection_detections
				WHERE session_key = $1
				UNION ALL
				SELECT request_id, detected_at, issue_type, severity, evidence, action_taken
				FROM output_compliance_audit
				WHERE session_key = $1
			) combined
			ORDER BY detected_at DESC
			LIMIT 20`
		cArgs := []any{gwSessionID}
		if tenantID != "" {
			complianceQuery = strings.Replace(complianceQuery, "WHERE session_key = $1", "WHERE session_key = $1 AND tenant_id = $2", -1)
			cArgs = append(cArgs, tenantID)
		}
		rows, err := h.db.Query(ctx, complianceQuery, cArgs...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var ci ComplianceIssue
				if err := rows.Scan(&ci.RequestID, &ci.Timestamp, &ci.IssueType, &ci.Severity, &ci.Description, &ci.ActionTaken); err == nil {
					analysis.ComplianceIssues = append(analysis.ComplianceIssues, ci)
				}
			}
		}
	}

	return analysis
}

// RouteSessionAnalytics dispatches sub-routes under /api/admin/session-analytics/.
//
//	/stats                      → HandleSessionAnalyticsStats
//	/model-breakdown            → HandleModelBreakdown (Task T1.2)
//	/session-shape              → HandleSessionShape (Task T1.2)
//	/health-distribution        → HandleHealthDistribution (Task T1.2)
//	/<gw_session_id>            → HandleSessionAnalyticsDetail
//	/<gw_session_id>/export     → HandleSessionAnalyticsExport
//	/<gw_session_id>/panorama   → HandleSessionPanorama
//	/<gw_session_id>/tags       → HandleSessionTags (GET/POST)
//	/<gw_session_id>/tags/<id>  → HandleSessionTagDelete (DELETE)
//	/<gw_session_id>/suggestions           → HandleSessionSuggestions (GET)
//	/<gw_session_id>/suggestions/<sid>/apply → HandleSessionSuggestionApply (POST)
func (h *Handler) RouteSessionAnalytics(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/session-analytics/")
	rest = strings.TrimSuffix(rest, "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		h.HandleSessionAnalyticsList(w, r)
		return
	}
	switch {
	case parts[0] == "stats" && len(parts) == 1:
		h.HandleSessionAnalyticsStats(w, r)
	case parts[0] == "model-breakdown" && len(parts) == 1:
		h.HandleModelBreakdown(w, r)
	case parts[0] == "session-shape" && len(parts) == 1:
		h.HandleSessionShape(w, r)
	case parts[0] == "health-distribution" && len(parts) == 1:
		h.HandleHealthDistribution(w, r)
	case len(parts) == 1:
		// /<gw_session_id>
		h.HandleSessionAnalyticsDetail(w, r)
	case len(parts) == 2 && parts[1] == "export":
		h.HandleSessionAnalyticsExport(w, r)
	case len(parts) == 2 && parts[1] == "panorama":
		h.HandleSessionPanorama(w, r)
	case len(parts) == 2 && parts[1] == "tags":
		h.HandleSessionTags(w, r)
	case len(parts) == 3 && parts[1] == "tags":
		// /<id>/tags/<tag_id> (DELETE)
		h.HandleSessionTagDelete(w, r)
	case len(parts) == 2 && parts[1] == "suggestions":
		h.HandleSessionSuggestions(w, r)
	case len(parts) == 4 && parts[1] == "suggestions" && parts[3] == "apply":
		// /<id>/suggestions/<sid>/apply
		h.HandleSessionSuggestionApply(w, r)
	default:
		writeError(w, http.StatusNotFound, "unknown session-analytics endpoint")
	}
}

// pathSegment extracts the n-th path segment after a prefix.
// e.g. pathSegment("/api/admin/session-analytics/abc/export", "/api/admin/session-analytics/", 0) → "abc"
func pathSegment(path, prefix string, idx int) string {
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	if idx < 0 || idx >= len(parts) {
		return ""
	}
	return parts[idx]
}
