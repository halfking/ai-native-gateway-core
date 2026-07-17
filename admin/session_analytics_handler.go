// Package admin — Session Analytics Handler (重写为 net/http + pgxpool)
//
// 350 迁移修复后，session_summaries.session_key 的值 = request_logs.gw_session_id。
// 所有查询统一使用 gw_session_id 作为对外标识。
//
// 路由（在 cmd/gateway/main.go 注册）：
//
//	GET    /api/admin/session-analytics                → HandleSessionAnalyticsList
//	GET    /api/admin/session-analytics/stats          → HandleSessionAnalyticsStats
//	GET    /api/admin/session-analytics/<gw_session_id>→ HandleSessionAnalyticsDetail
//	GET    /api/admin/session-analytics/<gw_session_id>/export → HandleSessionAnalyticsExport
package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ── Response types ────────────────────────────────────────────────────

// AnalyticsSessionSummary 会话摘要（对外暴露 gw_session_id）
type AnalyticsSessionSummary struct {
	GwSessionID             string     `json:"gw_session_id"`
	TenantID                string     `json:"tenant_id"`
	TaskID                  *string    `json:"task_id,omitempty"`
	SessionStatus           *string    `json:"session_status,omitempty"`
	FirstRequestAt          time.Time  `json:"first_request_at"`
	LastRequestAt           time.Time  `json:"last_request_at"`
	DurationSeconds         int        `json:"duration_seconds"`
	RequestCount            int        `json:"request_count"`
	SuccessCount            int        `json:"success_count"`
	ErrorCount              int        `json:"error_count"`
	TotalCostUSD            float64    `json:"total_cost_usd"`
	InputCostUSD            float64    `json:"input_cost_usd"`
	OutputCostUSD           float64    `json:"output_cost_usd"`
	TotalPromptTokens       int64      `json:"total_prompt_tokens"`
	TotalCompletionTokens   int64      `json:"total_completion_tokens"`
	TotalTokens             int64      `json:"total_tokens"`
	AvgLatencyMs            int        `json:"avg_latency_ms"`
	MinLatencyMs            *int       `json:"min_latency_ms"`
	MaxLatencyMs            *int       `json:"max_latency_ms"`
	ModelsUsed              []string   `json:"models_used"`
	PrimaryModel            *string    `json:"primary_model"`
	ModelSwitchCount        int        `json:"model_switch_count"`
	Title                   *string    `json:"title"`
	Summary                 *string    `json:"summary"`
	KeyTopics               []string   `json:"key_topics"`
	UserIntent              *string    `json:"user_intent"`
	QualityScore            *int       `json:"quality_score"`
	ComplianceStatus        string     `json:"compliance_status"`
	ComplianceIssuesCount   int        `json:"compliance_issues_count"`
	PromptInjectionDetected bool       `json:"prompt_injection_detected"`
	PIIDetected             bool       `json:"pii_detected"`
	ToxicOutputDetected     bool       `json:"toxic_output_detected"`
	WorkTypes               []string   `json:"work_types"`
	Providers               []string   `json:"providers"`
	ClientModels            []string   `json:"client_models"`
	LastSummarizedAt        *time.Time `json:"last_summarized_at"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
	// 健康评分字段（T1.5）
	HealthScore  *int       `json:"health_score,omitempty"`
	HealthGrade  *string    `json:"health_grade,omitempty"`
	Outcome      *string    `json:"outcome,omitempty"`
	LastHealthAt *time.Time `json:"last_health_at,omitempty"`
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
	RequestID           string    `json:"request_id"`
	CreatedAt           time.Time `json:"created_at"`
	Success             bool      `json:"success"`
	ClientModel         string    `json:"client_model"`
	UpstreamModel       string    `json:"upstream_model"`
	PromptTokens        int       `json:"prompt_tokens"`
	CompletionTokens    int       `json:"completion_tokens"`
	CostUSD             float64   `json:"cost_usd"`
	LatencyMs           int       `json:"latency_ms"`
	WorkType            *string   `json:"work_type,omitempty"`
	Provider            *string   `json:"provider,omitempty"`
	CompressionStrategy *string   `json:"compression_strategy,omitempty"`
	CacheReadTokens     *int      `json:"cache_read_tokens,omitempty"`
	ErrorMessage        *string   `json:"error_message,omitempty"`
	RequestPreview      *string   `json:"request_preview,omitempty"`
	ResponsePreview     *string   `json:"response_preview,omitempty"`
}

// AnalyticsSessionDetail 会话详情（摘要 + 时间线 + 分析）
type AnalyticsSessionDetail struct {
	Summary  AnalyticsSessionSummary `json:"summary"`
	Timeline []RequestEvent          `json:"timeline"`
	Analysis SessionAnalysis         `json:"analysis"`
}

// SessionAnalysis 会话分析（成本/token 分解 + 模型切换 + 合规）
type SessionAnalysis struct {
	ModelSwitches      []ModelSwitch       `json:"model_switches"`
	ComplianceIssues   []ComplianceIssue   `json:"compliance_issues"`
	CostBreakdown      CostBreakdown       `json:"cost_breakdown"`
	TokenDistribution  TokenDistribution   `json:"token_distribution"`
	CacheSavings       *CacheSavings       `json:"cache_savings,omitempty"`
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
	CacheReadTokens   int64   `json:"cache_read_tokens"`
	CacheWriteTokens  int64   `json:"cache_write_tokens"`
	EstimatedSavedUSD float64 `json:"estimated_saved_usd"`
}

// CompressionSavings 压缩节省
type CompressionSavings struct {
	CompressedRequests   int     `json:"compressed_requests"`
	OutboundTokenEst     int64   `json:"outbound_token_est"`
	EstimatedTokensSaved int64   `json:"estimated_tokens_saved"`
	EstimatedSavedUSD    float64 `json:"estimated_saved_usd"`
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

// withSessionAnalyticsReadTx selects the tenant-scoped RLS transaction for
// tenant users and the explicit audited all-tenant transaction for super-admins.
func (h *Handler) withSessionAnalyticsReadTx(ctx context.Context, r *http.Request, fn func(tx pgx.Tx) error) error {
	tenantID := effectiveScopeTenant(r)
	if tenantID == "" {
		return withAllTenantReadOnlyTx(ctx, h.db, fn)
	}
	return withTenantTx(ctx, h.db, tenantID, fn)
}

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

	tenantID := effectiveScopeTenant(r)
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

	// 构建 WHERE — 应用层 tenant/owner 过滤与 RLS GUC 同步生效：
	//   - tenant 用户走 withTenantTx(tenantID)，RLS 兜底；
	//   - super_admin/admin-key 走 withAllTenantReadOnlyTx()，显式 bypass GUC。
	where := " WHERE 1=1"
	args := []any{}
	if tenantID != "" {
		args = append(args, tenantID)
		where += " AND ss.tenant_id = $1"
	}
	ownerFrag, ownerArgs, argCount := ownerScopeClause(r, "sd.owner_user", len(args)+1)
	if ownerFrag != "" {
		where += ownerFrag
		args = append(args, ownerArgs...)
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

	listQuery := "SELECT " + sessionSummarySelectCols +
		" FROM session_summaries ss" +
		" LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key" +
		where + " ORDER BY ss." + orderBy + " " + orderDir +
		" LIMIT $" + strconv.Itoa(argCount) +
		" OFFSET $" + strconv.Itoa(argCount+1)
	pagedArgs := append([]any{}, args...)
	pagedArgs = append(pagedArgs, pageSize, offset)

	countQuery := "SELECT COUNT(*) FROM session_summaries ss" +
		" LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key" + where

	var (
		sessions []AnalyticsSessionSummary
		total    int
	)
	err := h.withSessionAnalyticsReadTx(ctx, r, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listQuery, pagedArgs...)
		if err != nil {
			return fmt.Errorf("list query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			s, err := scanSessionSummary(rows)
			if err != nil {
				return fmt.Errorf("scan row: %w", err)
			}
			sessions = append(sessions, s)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("list rows: %w", err)
		}
		if err := tx.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
			return fmt.Errorf("count query: %w", err)
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session analytics list failed: "+err.Error())
		return
	}

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
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tenantID := effectiveScopeTenant(r)
	query := `
		SELECT
			COALESCE(COUNT(*), 0),
			COALESCE(COUNT(*) FILTER (WHERE ss.last_request_at > NOW() - INTERVAL '1 hour'), 0),
			COALESCE(SUM(ss.request_count), 0),
			COALESCE(SUM(ss.total_cost_usd), 0),
			COALESCE(AVG(ss.total_cost_usd), 0),
			COALESCE(AVG(ss.total_tokens), 0)::BIGINT,
			COALESCE(AVG(ss.avg_latency_ms), 0)::INT,
			COALESCE(COUNT(*) FILTER (WHERE ss.compliance_status = 'compliant') * 100.0 / NULLIF(COUNT(*), 0), 0),
			COALESCE(COUNT(*) FILTER (WHERE ss.quality_score >= 8) * 100.0 / NULLIF(COUNT(*) FILTER (WHERE ss.quality_score IS NOT NULL), 0), 0)
		FROM session_summaries ss
		LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key
		WHERE ss.first_request_at >= CURRENT_DATE`
	args := []any{}
	if tenantID != "" {
		query += " AND ss.tenant_id = $1"
		args = append(args, tenantID)
	}
	ownerFrag, ownerArgs, _ := ownerScopeClause(r, "sd.owner_user", len(args)+1)
	if ownerFrag != "" {
		query += ownerFrag
		args = append(args, ownerArgs...)
	}

	var stats AnalyticsSessionStats
	err := h.withSessionAnalyticsReadTx(ctx, r, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, query, args...).Scan(
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
	})
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
	tenantID := effectiveScopeTenant(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// Run owner check + summary + timeline + compliance inside a single
	// RLS-scoped transaction so the read context cannot drift between the
	// pre-check and the actual rows.
	summaryQuery := "SELECT " + sessionSummarySelectCols +
		" FROM session_summaries ss" +
		" LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key" +
		" WHERE ss.session_key = $1"
	summaryArgs := []any{gwSessionID}
	if tenantID != "" {
		summaryQuery += " AND ss.tenant_id = $2"
		summaryArgs = append(summaryArgs, tenantID)
	}

	timelineQuery := `
		SELECT request_id, ts, success, client_model, outbound_model,
		       COALESCE(prompt_tokens,0), COALESCE(completion_tokens,0),
		       COALESCE(cost_usd,0), COALESCE(latency_ms,0),
		       work_type, compression_strategy, cache_read_tokens,
		       error_kind, request_preview, response_preview
		FROM request_logs
		WHERE gw_session_id = $1`
	timelineArgs := []any{gwSessionID}
	if tenantID != "" {
		timelineQuery += " AND tenant_id = $2"
		timelineArgs = append(timelineArgs, tenantID)
	}
	timelineQuery += " ORDER BY ts ASC LIMIT 100"

	var (
		summary  AnalyticsSessionSummary
		timeline []RequestEvent
		analysis SessionAnalysis
		notFound bool
	)
	err := h.withSessionAnalyticsReadTx(ctx, r, func(tx pgx.Tx) error {
		if !IsRegularUser(r) {
			// skip the explicit owner check for admin tiers, but still re-check
			// inside the tx so RLS applies uniformly.
		} else {
			ok, err := assertSessionOwnerAccessInTx(ctx, tx, r, gwSessionID)
			if err != nil {
				return fmt.Errorf("owner check: %w", err)
			}
			if !ok {
				notFound = true
				return nil
			}
		}

		row := tx.QueryRow(ctx, summaryQuery, summaryArgs...)
		s, err := scanSessionSummary(row)
		if err == pgx.ErrNoRows {
			notFound = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("summary query: %w", err)
		}
		summary = s

		rows, err := tx.Query(ctx, timelineQuery, timelineArgs...)
		if err != nil {
			return fmt.Errorf("timeline query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var e RequestEvent
			var ts time.Time
			if err := rows.Scan(
				&e.RequestID, &ts, &e.Success, &e.ClientModel, &e.UpstreamModel,
				&e.PromptTokens, &e.CompletionTokens, &e.CostUSD, &e.LatencyMs,
				&e.WorkType, &e.CompressionStrategy, &e.CacheReadTokens,
				&e.ErrorMessage, &e.RequestPreview, &e.ResponsePreview,
			); err != nil {
				return fmt.Errorf("timeline scan: %w", err)
			}
			e.CreatedAt = ts
			timeline = append(timeline, e)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("timeline rows: %w", err)
		}

		analysis, err = h.buildSessionAnalysisInTx(ctx, tx, tenantID, gwSessionID, timeline)
		if err != nil {
			return fmt.Errorf("session analysis: %w", err)
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session analytics detail failed: "+err.Error())
		return
	}
	if notFound {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

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
	tenantID := effectiveScopeTenant(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if !requireSessionOwnerAccess(w, r, ctx, h.db, gwSessionID) {
		return
	}

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

// buildSessionAnalysisInTx builds the per-session cost/token/model analysis
// using the timeline already loaded by the caller and queries compliance rows
// in the same RLS tx. Errors from compliance reads are propagated so the
// handler can return 5xx instead of silently swallowing the failure.
func (h *Handler) buildSessionAnalysisInTx(ctx context.Context, tx pgx.Tx, tenantID, gwSessionID string, timeline []RequestEvent) (SessionAnalysis, error) {
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

	var cacheRead int64
	var compressedCount int
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
	if total := analysis.TokenDistribution.TotalTokens; total > 0 {
		ratio := float64(analysis.TokenDistribution.PromptTokens) / float64(total)
		analysis.CostBreakdown.InputCost = analysis.CostBreakdown.TotalCost * ratio
		analysis.CostBreakdown.OutputCost = analysis.CostBreakdown.TotalCost - analysis.CostBreakdown.InputCost
	} else {
		analysis.CostBreakdown.InputCost = analysis.CostBreakdown.TotalCost * 0.4
		analysis.CostBreakdown.OutputCost = analysis.CostBreakdown.TotalCost * 0.6
	}

	if cacheRead > 0 {
		analysis.CacheSavings = &CacheSavings{CacheReadTokens: cacheRead}
	}
	if compressedCount > 0 {
		analysis.CompressionSavings = &CompressionSavings{CompressedRequests: compressedCount}
	}

	complianceQuery := `
		SELECT request_id, detected_at, issue_type, severity, evidence, action_taken
		FROM (
			SELECT request_id, detected_at, issue_type, severity, evidence, action_taken, tenant_id
			FROM prompt_injection_detections
			WHERE session_key = $1
			UNION ALL
			SELECT request_id, detected_at, issue_type, severity, evidence, action_taken, tenant_id
			FROM output_compliance_audit
			WHERE session_key = $1
		) combined
		WHERE session_key = $1`
	cArgs := []any{gwSessionID}
	if tenantID != "" {
		complianceQuery += " AND tenant_id = $2"
		cArgs = append(cArgs, tenantID)
	}
	complianceQuery += " ORDER BY detected_at DESC LIMIT 20"

	rows, err := tx.Query(ctx, complianceQuery, cArgs...)
	if err != nil {
		return analysis, fmt.Errorf("compliance query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ci ComplianceIssue
		if err := rows.Scan(&ci.RequestID, &ci.Timestamp, &ci.IssueType, &ci.Severity, &ci.Description, &ci.ActionTaken); err != nil {
			return analysis, fmt.Errorf("compliance scan: %w", err)
		}
		analysis.ComplianceIssues = append(analysis.ComplianceIssues, ci)
	}
	if err := rows.Err(); err != nil {
		return analysis, fmt.Errorf("compliance rows: %w", err)
	}

	return analysis, nil
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
	case parts[0] == "top-sessions" && len(parts) == 1:
		h.HandleTopSessions(w, r)
	case parts[0] == "filter-options" && len(parts) == 1:
		h.HandleFilterOptions(w, r)
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
