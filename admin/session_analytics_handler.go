package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

// SessionAnalyticsHandler 会话分析 Handler
type SessionAnalyticsHandler struct {
	db *sql.DB
}

// NewSessionAnalyticsHandler 创建 Handler
func NewSessionAnalyticsHandler(db *sql.DB) *SessionAnalyticsHandler {
	return &SessionAnalyticsHandler{db: db}
}

// AnalyticsSessionSummary 会话摘要
type AnalyticsSessionSummary struct {
	SessionKey              string    `json:"session_key"`
	TenantID                string    `json:"tenant_id"`
	FirstRequestAt          time.Time `json:"first_request_at"`
	LastRequestAt           time.Time `json:"last_request_at"`
	DurationSeconds         int       `json:"duration_seconds"`
	RequestCount            int       `json:"request_count"`
	SuccessCount            int       `json:"success_count"`
	ErrorCount              int       `json:"error_count"`
	TotalCostUSD            float64   `json:"total_cost_usd"`
	InputCostUSD            float64   `json:"input_cost_usd"`
	OutputCostUSD           float64   `json:"output_cost_usd"`
	TotalPromptTokens       int64     `json:"total_prompt_tokens"`
	TotalCompletionTokens   int64     `json:"total_completion_tokens"`
	TotalTokens             int64     `json:"total_tokens"`
	AvgLatencyMs            int       `json:"avg_latency_ms"`
	MinLatencyMs            *int      `json:"min_latency_ms"`
	MaxLatencyMs            *int      `json:"max_latency_ms"`
	ModelsUsed              []string  `json:"models_used"`
	PrimaryModel            *string   `json:"primary_model"`
	ModelSwitchCount        int       `json:"model_switch_count"`
	Title                   *string   `json:"title"`
	Summary                 *string   `json:"summary"`
	KeyTopics               []string  `json:"key_topics"`
	UserIntent              *string   `json:"user_intent"`
	QualityScore            *int      `json:"quality_score"`
	ComplianceStatus        string    `json:"compliance_status"`
	ComplianceIssuesCount   int       `json:"compliance_issues_count"`
	PromptInjectionDetected bool      `json:"prompt_injection_detected"`
	PIIDetected             bool      `json:"pii_detected"`
	ToxicOutputDetected     bool      `json:"toxic_output_detected"`
	WorkTypes               []string  `json:"work_types"`
	Providers               []string  `json:"providers"`
	ClientModels            []string  `json:"client_models"`
	LastSummarizedAt        *time.Time `json:"last_summarized_at"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

// AnalyticsSessionStats 会话统计
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

// AnalyticsSessionDetail 会话详情
type AnalyticsSessionDetail struct {
	Summary  AnalyticsSessionSummary  `json:"summary"`
	Timeline []RequestEvent  `json:"timeline"`
	Analysis SessionAnalysis `json:"analysis"`
}

// RequestEvent 请求事件
type RequestEvent struct {
	RequestID        string     `json:"request_id"`
	CreatedAt        time.Time  `json:"created_at"`
	Status           string     `json:"status"`
	ClientModel      string     `json:"client_model"`
	UpstreamModel    string     `json:"upstream_model"`
	PromptTokens     int        `json:"prompt_tokens"`
	CompletionTokens int        `json:"completion_tokens"`
	TotalCost        float64    `json:"total_cost"`
	LatencyMs        int        `json:"latency_ms"`
	WorkType         *string    `json:"work_type"`
	Provider         *string    `json:"provider"`
	ErrorMessage     *string    `json:"error_message"`
}

// SessionAnalysis 会话分析
type SessionAnalysis struct {
	ModelSwitches     []ModelSwitch         `json:"model_switches"`
	ComplianceIssues  []ComplianceIssue     `json:"compliance_issues"`
	CostBreakdown     CostBreakdown         `json:"cost_breakdown"`
	TokenDistribution TokenDistribution     `json:"token_distribution"`
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

// ListSessions 列出会话
func (h *SessionAnalyticsHandler) ListSessions(c echo.Context) error {
	tenantID := c.Get("tenant_id").(string)

	// 分页参数
	page, _ := strconv.Atoi(c.QueryParam("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.QueryParam("page_size"))
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	// 筛选参数
	complianceStatus := c.QueryParam("compliance_status")
	userIntent := c.QueryParam("user_intent")
	minCost := c.QueryParam("min_cost")
	maxCost := c.QueryParam("max_cost")
	search := c.QueryParam("search")

	// 排序参数
	orderBy := c.QueryParam("order_by")
	if orderBy == "" {
		orderBy = "last_request_at"
	}
	
	// 验证排序列
	if err := ValidateOrderByColumn("session_summaries", orderBy); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid order by: "+err.Error())
	}

	orderDir := c.QueryParam("order_dir")
	if orderDir != "ASC" && orderDir != "DESC" {
		orderDir = "DESC"
	}

	// 构建查询
	query := `
		SELECT 
			session_key, tenant_id, first_request_at, last_request_at, duration_seconds,
			request_count, success_count, error_count,
			total_cost_usd, input_cost_usd, output_cost_usd,
			total_prompt_tokens, total_completion_tokens, total_tokens,
			avg_latency_ms, min_latency_ms, max_latency_ms,
			models_used, primary_model, model_switch_count,
			title, summary, key_topics, user_intent, quality_score,
			compliance_status, compliance_issues_count,
			prompt_injection_detected, pii_detected, toxic_output_detected,
			work_types, providers, client_models,
			last_summarized_at, created_at, updated_at
		FROM session_summaries
		WHERE tenant_id = $1
	`
	args := []interface{}{tenantID}
	argCount := 2

	if complianceStatus != "" {
		query += " AND compliance_status = $" + strconv.Itoa(argCount)
		args = append(args, complianceStatus)
		argCount++
	}

	if userIntent != "" {
		query += " AND user_intent = $" + strconv.Itoa(argCount)
		args = append(args, userIntent)
		argCount++
	}

	if minCost != "" {
		query += " AND total_cost_usd >= $" + strconv.Itoa(argCount)
		args = append(args, minCost)
		argCount++
	}

	if maxCost != "" {
		query += " AND total_cost_usd <= $" + strconv.Itoa(argCount)
		args = append(args, maxCost)
		argCount++
	}

	if search != "" {
		query += " AND (title ILIKE $" + strconv.Itoa(argCount) + " OR $" + strconv.Itoa(argCount) + " = ANY(key_topics))"
		args = append(args, "%"+search+"%")
		argCount++
	}

	query += " ORDER BY " + orderBy + " " + orderDir
	query += " LIMIT $" + strconv.Itoa(argCount) + " OFFSET $" + strconv.Itoa(argCount+1)
	args = append(args, pageSize, offset)

	rows, err := h.db.QueryContext(c.Request().Context(), query, args...)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to query sessions: "+err.Error())
	}
	defer rows.Close()

	sessions := []AnalyticsSessionSummary{}
	for rows.Next() {
		session := AnalyticsSessionSummary{}
		if err := rows.Scan(
			&session.SessionKey, &session.TenantID, &session.FirstRequestAt, &session.LastRequestAt, &session.DurationSeconds,
			&session.RequestCount, &session.SuccessCount, &session.ErrorCount,
			&session.TotalCostUSD, &session.InputCostUSD, &session.OutputCostUSD,
			&session.TotalPromptTokens, &session.TotalCompletionTokens, &session.TotalTokens,
			&session.AvgLatencyMs, &session.MinLatencyMs, &session.MaxLatencyMs,
			&session.ModelsUsed, &session.PrimaryModel, &session.ModelSwitchCount,
			&session.Title, &session.Summary, &session.KeyTopics, &session.UserIntent, &session.QualityScore,
			&session.ComplianceStatus, &session.ComplianceIssuesCount,
			&session.PromptInjectionDetected, &session.PIIDetected, &session.ToxicOutputDetected,
			&session.WorkTypes, &session.Providers, &session.ClientModels,
			&session.LastSummarizedAt, &session.CreatedAt, &session.UpdatedAt,
		); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to scan session: "+err.Error())
		}
		sessions = append(sessions, session)
	}

	// 查询总数
	countQuery := `SELECT COUNT(*) FROM session_summaries WHERE tenant_id = $1`
	var total int
	h.db.QueryRowContext(c.Request().Context(), countQuery, tenantID).Scan(&total)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"sessions":  sessions,
		"page":      page,
		"page_size": pageSize,
		"total":     total,
	})
}

// GetSessionDetail 获取会话详情
func (h *SessionAnalyticsHandler) GetSessionDetail(c echo.Context) error {
	tenantID := c.Get("tenant_id").(string)
	sessionKey := c.Param("session_key")

	// 获取会话摘要
	summaryQuery := `
		SELECT 
			session_key, tenant_id, first_request_at, last_request_at, duration_seconds,
			request_count, success_count, error_count,
			total_cost_usd, input_cost_usd, output_cost_usd,
			total_prompt_tokens, total_completion_tokens, total_tokens,
			avg_latency_ms, min_latency_ms, max_latency_ms,
			models_used, primary_model, model_switch_count,
			title, summary, key_topics, user_intent, quality_score,
			compliance_status, compliance_issues_count,
			prompt_injection_detected, pii_detected, toxic_output_detected,
			work_types, providers, client_models,
			last_summarized_at, created_at, updated_at
		FROM session_summaries
		WHERE tenant_id = $1 AND session_key = $2
	`

	summary := AnalyticsSessionSummary{}
	err := h.db.QueryRowContext(c.Request().Context(), summaryQuery, tenantID, sessionKey).Scan(
		&summary.SessionKey, &summary.TenantID, &summary.FirstRequestAt, &summary.LastRequestAt, &summary.DurationSeconds,
		&summary.RequestCount, &summary.SuccessCount, &summary.ErrorCount,
		&summary.TotalCostUSD, &summary.InputCostUSD, &summary.OutputCostUSD,
		&summary.TotalPromptTokens, &summary.TotalCompletionTokens, &summary.TotalTokens,
		&summary.AvgLatencyMs, &summary.MinLatencyMs, &summary.MaxLatencyMs,
		&summary.ModelsUsed, &summary.PrimaryModel, &summary.ModelSwitchCount,
		&summary.Title, &summary.Summary, &summary.KeyTopics, &summary.UserIntent, &summary.QualityScore,
		&summary.ComplianceStatus, &summary.ComplianceIssuesCount,
		&summary.PromptInjectionDetected, &summary.PIIDetected, &summary.ToxicOutputDetected,
		&summary.WorkTypes, &summary.Providers, &summary.ClientModels,
		&summary.LastSummarizedAt, &summary.CreatedAt, &summary.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return echo.NewHTTPError(http.StatusNotFound, "Session not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to get session: "+err.Error())
	}

	// 获取请求时间线
	timelineQuery := `
		SELECT 
			request_id, created_at, status, client_model, upstream_model,
			prompt_tokens, completion_tokens, total_cost, latency_ms,
			work_type, provider, error_message
		FROM request_logs
		WHERE tenant_id = $1 AND session_key = $2
		ORDER BY created_at ASC
		LIMIT 100
	`

	rows, err := h.db.QueryContext(c.Request().Context(), timelineQuery, tenantID, sessionKey)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to get timeline: "+err.Error())
	}
	defer rows.Close()

	timeline := []RequestEvent{}
	for rows.Next() {
		event := RequestEvent{}
		if err := rows.Scan(
			&event.RequestID, &event.CreatedAt, &event.Status, &event.ClientModel, &event.UpstreamModel,
			&event.PromptTokens, &event.CompletionTokens, &event.TotalCost, &event.LatencyMs,
			&event.WorkType, &event.Provider, &event.ErrorMessage,
		); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to scan event: "+err.Error())
		}
		timeline = append(timeline, event)
	}

	// 构建分析数据
	analysis := h.buildSessionAnalysis(c.Request().Context(), tenantID, sessionKey, timeline)

	detail := AnalyticsSessionDetail{
		Summary:  summary,
		Timeline: timeline,
		Analysis: analysis,
	}

	return c.JSON(http.StatusOK, detail)
}

// GetSessionStats 获取会话统计
func (h *SessionAnalyticsHandler) GetSessionStats(c echo.Context) error {
	tenantID := c.Get("tenant_id").(string)

	query := `
		SELECT 
			COALESCE(session_count, 0),
			COALESCE(active_sessions, 0),
			COALESCE(total_requests, 0),
			COALESCE(total_cost, 0),
			COALESCE(avg_cost_per_session, 0),
			COALESCE(avg_tokens_per_session, 0),
			COALESCE(avg_latency, 0),
			COALESCE(compliance_rate, 0),
			COALESCE(high_quality_rate, 0)
		FROM session_stats_today
		WHERE tenant_id = $1
	`

	stats := AnalyticsSessionStats{}
	err := h.db.QueryRowContext(c.Request().Context(), query, tenantID).Scan(
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

	if err == sql.ErrNoRows {
		stats = AnalyticsSessionStats{}
	} else if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to get stats: "+err.Error())
	}

	return c.JSON(http.StatusOK, stats)
}

// buildSessionAnalysis 构建会话分析
func (h *SessionAnalyticsHandler) buildSessionAnalysis(ctx context.Context, tenantID, sessionKey string, timeline []RequestEvent) SessionAnalysis {
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

	// 检测模型切换
	var lastModel string
	for i, event := range timeline {
		if event.UpstreamModel != lastModel && i > 0 {
			analysis.ModelSwitches = append(analysis.ModelSwitches, ModelSwitch{
				RequestID: event.RequestID,
				Timestamp: event.CreatedAt,
				FromModel: lastModel,
				ToModel:   event.UpstreamModel,
				Reason:    "Auto-routed",
			})
		}
		lastModel = event.UpstreamModel
	}

	// 成本分解
	for _, event := range timeline {
		analysis.CostBreakdown.TotalCost += event.TotalCost
		
		if event.UpstreamModel != "" {
			analysis.CostBreakdown.ByModel[event.UpstreamModel] += event.TotalCost
		}
		
		if event.Provider != nil && *event.Provider != "" {
			analysis.CostBreakdown.ByProvider[*event.Provider] += event.TotalCost
		}
	}

	analysis.CostBreakdown.InputCost = analysis.CostBreakdown.TotalCost * 0.4
	analysis.CostBreakdown.OutputCost = analysis.CostBreakdown.TotalCost * 0.6

	// Token 分布
	for _, event := range timeline {
		analysis.TokenDistribution.PromptTokens += int64(event.PromptTokens)
		analysis.TokenDistribution.CompletionTokens += int64(event.CompletionTokens)
		
		if event.UpstreamModel != "" {
			analysis.TokenDistribution.ByModel[event.UpstreamModel] += int64(event.PromptTokens + event.CompletionTokens)
		}
	}
	analysis.TokenDistribution.TotalTokens = analysis.TokenDistribution.PromptTokens + analysis.TokenDistribution.CompletionTokens

	// 查询合规问题
	complianceQuery := `
		SELECT request_id, detected_at, issue_type, severity, evidence, action_taken
		FROM (
			SELECT request_id, detected_at, issue_type, severity, evidence, action_taken
			FROM prompt_injection_detections
			WHERE tenant_id = $1 AND session_key = $2
			UNION ALL
			SELECT request_id, detected_at, issue_type, severity, evidence, action_taken
			FROM output_compliance_audit
			WHERE tenant_id = $1 AND session_key = $2
		) combined
		ORDER BY detected_at DESC
		LIMIT 20
	`

	rows, err := h.db.QueryContext(ctx, complianceQuery, tenantID, sessionKey)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var requestID string
			var timestamp time.Time
			var issueType string
			var severity int
			var evidence string
			var actionTaken string

			if err := rows.Scan(&requestID, &timestamp, &issueType, &severity, &evidence, &actionTaken); err == nil {
				analysis.ComplianceIssues = append(analysis.ComplianceIssues, ComplianceIssue{
					RequestID:   requestID,
					Timestamp:   timestamp,
					IssueType:   issueType,
					Severity:    severity,
					Description: evidence,
					ActionTaken: actionTaken,
				})
			}
		}
	}

	return analysis
}

// ExportSession 导出会话（JSON 格式）
func (h *SessionAnalyticsHandler) ExportSession(c echo.Context) error {
	tenantID := c.Get("tenant_id").(string)
	sessionKey := c.Param("session_key")

	// 获取完整会话数据
	detail, err := h.getFullSessionDetail(c.Request().Context(), tenantID, sessionKey)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to export session: "+err.Error())
	}

	c.Response().Header().Set("Content-Type", "application/json")
	c.Response().Header().Set("Content-Disposition", "attachment; filename=session_"+sessionKey+".json")

	return json.NewEncoder(c.Response()).Encode(detail)
}

// getFullSessionDetail 获取完整会话详情（内部方法）
func (h *SessionAnalyticsHandler) getFullSessionDetail(ctx context.Context, tenantID, sessionKey string) (interface{}, error) {
	query := `SELECT row_to_json(t) FROM (SELECT * FROM session_summaries WHERE tenant_id = $1 AND session_key = $2) t`
	var result interface{}
	err := h.db.QueryRowContext(ctx, query, tenantID, sessionKey).Scan(&result)
	return result, err
}
