// Package dashboardapi - session_trend.go
// 会话趋势 API：按天统计新会话、活跃会话、关闭会话数量
package dashboardapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionTrendHandler 会话趋势 Handler
type SessionTrendHandler struct {
	db *pgxpool.Pool
}

// NewSessionTrendHandler 创建 Handler
func NewSessionTrendHandler(db *pgxpool.Pool) *SessionTrendHandler {
	return &SessionTrendHandler{db: db}
}

// SessionTrendResponse 会话趋势响应
type SessionTrendResponse struct {
	Trend       []SessionTrendPoint `json:"trend"`
	Summary     TrendSummary        `json:"summary"`
	PeriodStart time.Time           `json:"period_start"`
	PeriodEnd   time.Time           `json:"period_end"`
}

// TrendSummary 趋势摘要
type TrendSummary struct {
	TotalNew      int     `json:"total_new"`
	TotalActive   int     `json:"total_active"`
	TotalClosed   int     `json:"total_closed"`
	AvgDailyNew   float64 `json:"avg_daily_new"`
	GrowthRatePct float64 `json:"growth_rate_pct"`
}

// HandleSessionTrend 处理会话趋势请求
//
// GET /api/admin/dashboard/session-trend
//
// 查询参数：
//   - days: 时间范围（1/7/30/90，默认 7）
//   - tenant_id: 租户 ID（可选）
func (h *SessionTrendHandler) HandleSessionTrend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErrorJSON(w, http.StatusMethodNotAllowed, ErrCodeInvalidParam, "method not allowed", "")
		return
	}

	startTime := time.Now()
	apiStatus := "success"
	defer func() {
		recordAPIRequest("session-trend", apiStatus, time.Since(startTime))
	}()

	params, _, ok := prepareDashboardRequest(w, r, h.db)
	if !ok {
		return
	}
	ctx, cancel := GetRequestContext(r, 15*time.Second)
	defer cancel()

	trend, err := h.queryTrend(ctx, params)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query session trend", err.Error())
		return
	}

	// 计算摘要
	summary := TrendSummary{}
	for _, t := range trend {
		summary.TotalNew += t.NewSessions
		summary.TotalActive += t.ActiveCount
		summary.TotalClosed += t.ClosedCount
	}
	if len(trend) > 0 {
		summary.AvgDailyNew = float64(summary.TotalNew) / float64(len(trend))
	}

	// 计算增长率（与上一周期对比）
	prevTrend, _ := h.queryPrevPeriodTrend(ctx, params)
	prevTotal := 0
	for _, t := range prevTrend {
		prevTotal += t.NewSessions
	}
	if prevTotal > 0 {
		summary.GrowthRatePct = float64(summary.TotalNew-prevTotal) * 100 / float64(prevTotal)
	}

	now := time.Now()
	resp := SessionTrendResponse{
		Trend:       trend,
		Summary:     summary,
		PeriodStart: now.AddDate(0, 0, -params.Days),
		PeriodEnd:   now,
	}

	metadata := &Metadata{
		GeneratedAt: now,
		TookMs:      time.Since(startTime).Milliseconds(),
	}
	writeSuccessJSON(w, resp, metadata)
}

func (h *SessionTrendHandler) queryTrend(ctx context.Context, params QueryParams) ([]SessionTrendPoint, error) {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	where = append(where, fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days'", params.Days))
	appendDashboardScope(&where, params, &args, &argIdx, "", true)
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			TO_CHAR(DATE(first_request_at), 'YYYY-MM-DD') as date,
			COUNT(*) as new_sessions,
			COUNT(*) FILTER (WHERE last_request_at >= NOW() - INTERVAL '24 hours') as active_count,
			COUNT(*) FILTER (WHERE last_request_at < DATE(first_request_at) + INTERVAL '1 day') as closed_count
		FROM session_summaries
		%s
		GROUP BY DATE(first_request_at)
		ORDER BY date
	`, whereClause)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]SessionTrendPoint, 0)
	for rows.Next() {
		var item SessionTrendPoint
		if err := rows.Scan(&item.Date, &item.NewSessions, &item.ActiveCount, &item.ClosedCount); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (h *SessionTrendHandler) queryPrevPeriodTrend(ctx context.Context, params QueryParams) ([]SessionTrendPoint, error) {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	where = append(where, fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days' AND first_request_at < NOW() - INTERVAL '%d days'", params.Days*2, params.Days))
	appendDashboardScope(&where, params, &args, &argIdx, "", true)
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			TO_CHAR(DATE(first_request_at), 'YYYY-MM-DD') as date,
			COUNT(*) as new_sessions,
			0 as active_count,
			0 as closed_count
		FROM session_summaries
		%s
		GROUP BY DATE(first_request_at)
		ORDER BY date
	`, whereClause)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]SessionTrendPoint, 0)
	for rows.Next() {
		var item SessionTrendPoint
		if err := rows.Scan(&item.Date, &item.NewSessions, &item.ActiveCount, &item.ClosedCount); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}
