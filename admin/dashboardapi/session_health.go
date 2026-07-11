// Package dashboardapi - session_health.go
// 会话健康度分布 API：按等级统计会话健康状态
package dashboardapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionHealthHandler 会话健康度 Handler
type SessionHealthHandler struct {
	db *pgxpool.Pool
}

// NewSessionHealthHandler 创建 Handler
func NewSessionHealthHandler(db *pgxpool.Pool) *SessionHealthHandler {
	return &SessionHealthHandler{db: db}
}

// SessionHealthResponse 健康度响应
type SessionHealthResponse struct {
	Distribution HealthDistribution `json:"distribution"`
	Trend        []HealthTrendPoint `json:"trend"`
	TopIssues    []HealthIssue      `json:"top_issues"`
}

// HealthTrendPoint 健康度趋势点
type HealthTrendPoint struct {
	Date     string  `json:"date"`
	AvgScore float64 `json:"avg_score"`
	GradeA   int     `json:"grade_a"`
	GradeF   int     `json:"grade_f"`
}

// HealthIssue 常见健康问题
type HealthIssue struct {
	Issue       string `json:"issue"`
	Count       int    `json:"count"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
}

// HandleSessionHealth 处理会话健康度请求
//
// GET /api/admin/dashboard/session-health
func (h *SessionHealthHandler) HandleSessionHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErrorJSON(w, http.StatusMethodNotAllowed, ErrCodeInvalidParam, "method not allowed", "")
		return
	}

	startTime := time.Now()
	apiStatus := "success"
	defer func() {
		recordAPIRequest("session-health", apiStatus, time.Since(startTime))
	}()

	params, _, ok := prepareDashboardRequest(w, r, h.db)
	if !ok {
		return
	}
	ctx, cancel := GetRequestContext(r, 15*time.Second)
	defer cancel()

	// 1. 健康度分布
	dist, err := h.queryDistribution(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query health distribution", err.Error())
		return
	}

	// 2. 健康度趋势
	trend, err := h.queryHealthTrend(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query health trend", err.Error())
		return
	}

	// 3. 常见问题
	issues, err := h.queryTopIssues(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query top issues", err.Error())
		return
	}

	resp := SessionHealthResponse{
		Distribution: *dist,
		Trend:        trend,
		TopIssues:    issues,
	}

	metadata := &Metadata{
		GeneratedAt: time.Now(),
		TookMs:      time.Since(startTime).Milliseconds(),
	}
	writeSuccessJSON(w, resp, metadata)
}

func (h *SessionHealthHandler) queryDistribution(ctx context.Context, params QueryParams) (*HealthDistribution, error) {
	dist := &HealthDistribution{}
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	appendDashboardScope(&where, params, &args, &argIdx, "", true)
	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + joinStrings(where, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE health_grade = 'A') as a,
			COUNT(*) FILTER (WHERE health_grade = 'B') as b,
			COUNT(*) FILTER (WHERE health_grade = 'C') as c,
			COUNT(*) FILTER (WHERE health_grade = 'D') as d,
			COUNT(*) FILTER (WHERE health_grade = 'F') as f,
			COALESCE(AVG(health_score), 0) as avg_score
		FROM session_summaries
		%s
	`, whereClause)

	err := h.db.QueryRow(ctx, query, args...).Scan(
		&dist.Total, &dist.A, &dist.B, &dist.C, &dist.D, &dist.F, &dist.AvgScore,
	)
	if err != nil {
		return nil, err
	}

	if dist.Total > 0 {
		dist.APercent = float64(dist.A) * 100 / float64(dist.Total)
		dist.BPercent = float64(dist.B) * 100 / float64(dist.Total)
		dist.CPercent = float64(dist.C) * 100 / float64(dist.Total)
		dist.DPercent = float64(dist.D) * 100 / float64(dist.Total)
		dist.FPercent = float64(dist.F) * 100 / float64(dist.Total)
	}
	return dist, nil
}

func (h *SessionHealthHandler) queryHealthTrend(ctx context.Context, params QueryParams) ([]HealthTrendPoint, error) {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	where = append(where, fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days'", params.Days))
	where = append(where, "health_score IS NOT NULL")
	appendDashboardScope(&where, params, &args, &argIdx, "", true)
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			TO_CHAR(DATE(last_health_at), 'YYYY-MM-DD') as date,
			COALESCE(AVG(health_score), 0) as avg_score,
			COUNT(*) FILTER (WHERE health_grade = 'A') as grade_a,
			COUNT(*) FILTER (WHERE health_grade = 'F') as grade_f
		FROM session_summaries
		%s
		GROUP BY DATE(last_health_at)
		ORDER BY date
	`, whereClause)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]HealthTrendPoint, 0)
	for rows.Next() {
		var item HealthTrendPoint
		if err := rows.Scan(&item.Date, &item.AvgScore, &item.GradeA, &item.GradeF); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (h *SessionHealthHandler) queryTopIssues(ctx context.Context, params QueryParams) ([]HealthIssue, error) {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	where = append(where, "health_score < 60")
	appendDashboardScope(&where, params, &args, &argIdx, "", true)
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			CASE
				WHEN error_count > 5 THEN 'high_error_rate'
				WHEN avg_latency_ms > 5000 THEN 'high_latency'
				WHEN prompt_injection_detected THEN 'prompt_injection'
				WHEN pii_detected THEN 'pii_detected'
				WHEN toxic_output_detected THEN 'toxic_output'
				WHEN model_switch_count > 3 THEN 'model_switching'
				ELSE 'low_engagement'
			END as issue,
			COUNT(*) as count
		FROM session_summaries
		%s
		GROUP BY issue
		ORDER BY count DESC
		LIMIT 5
	`, whereClause)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]HealthIssue, 0)
	for rows.Next() {
		var item HealthIssue
		if err := rows.Scan(&item.Issue, &item.Count); err != nil {
			return nil, err
		}
		item.Severity = "warning"
		item.Description = issueDescription(item.Issue)
		items = append(items, item)
	}
	return items, nil
}

func issueDescription(issue string) string {
	descriptions := map[string]string{
		"high_error_rate":  "会话错误率过高",
		"high_latency":     "平均延迟超过5秒",
		"prompt_injection": "检测到提示注入攻击",
		"pii_detected":     "检测到个人信息泄露",
		"toxic_output":     "检测到有害输出",
		"model_switching":  "频繁切换模型",
		"low_engagement":   "低参与度会话",
	}
	if desc, ok := descriptions[issue]; ok {
		return desc
	}
	return issue
}
