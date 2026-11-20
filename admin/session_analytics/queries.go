// Package sessionanalytics 提供会话分析系统的 API 实现
//
// 实现内容：
// - 总体统计仪表板
// - 人员排行
// - 人员详情
// - 智能体排行
// - 模型排行
package sessionanalytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/analytics"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// OverviewSummary 总体统计摘要
type OverviewSummary struct {
	TotalSessions       int64   `json:"total_sessions"`
	TotalSessionsGrowth float64 `json:"total_sessions_growth"`
	AvgTokens           float64 `json:"avg_tokens"`
	AvgContextSize      int64   `json:"avg_context_size"`
	AvgDurationSeconds  float64 `json:"avg_duration_seconds"`
	AvgTurns            float64 `json:"avg_turns"`
	// TotalCost 保留用于向后兼容和排序，但注意这是 internal_cost(美元) + external_cost(积分) 的跨单位相加
	// 建议使用 InternalCost 和 ExternalCost 分别处理
	TotalCost          float64 `json:"total_cost"`
	TotalCostGrowth    float64 `json:"total_cost_growth"`
	InternalCost       float64 `json:"internal_cost"`        // 内部成本（美元）
	InternalCostGrowth float64 `json:"internal_cost_growth"` // 内部成本增长率
	ExternalCost       float64 `json:"external_cost"`        // 外部成本（积分）
	ExternalCostGrowth float64 `json:"external_cost_growth"` // 外部成本增长率
	ActiveUsers        int64   `json:"active_users"`
	ActiveUsersGrowth  float64 `json:"active_users_growth"`
	ActiveProjects     int64   `json:"active_projects"`
	ActiveTasks        int64   `json:"active_tasks"`
	OverallSuccessRate float64 `json:"overall_success_rate"`
	OverallCacheRate   float64 `json:"overall_cache_rate"`
}

// TopModel 最常用模型
type TopModel struct {
	Model string `json:"model"`
	Count int64  `json:"count"`
}

// TrendPoint 趋势数据点
type TrendPoint struct {
	Date  string `json:"date"`
	Count int64  `json:"count"`
}

// CostTrendPoint 成本趋势数据点
type CostTrendPoint struct {
	Date     string  `json:"date"`
	Internal float64 `json:"internal"`
	External float64 `json:"external"`
}

// TokenTrendPoint Token 趋势数据点
type TokenTrendPoint struct {
	Date  string `json:"date"`
	Total int64  `json:"total"`
}

// TopUser 最活跃用户
type TopUser struct {
	Owner        string  `json:"owner"`
	DisplayName  string  `json:"display_name"`
	Email        string  `json:"email"`
	SessionCount int64   `json:"session_count"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCost    float64 `json:"total_cost"`    // 保留用于向后兼容和排序（跨单位相加）
	InternalCost float64 `json:"internal_cost"` // 内部成本（美元）
	ExternalCost float64 `json:"external_cost"` // 外部成本（积分）
}

// TopProject 最活跃项目
type TopProject struct {
	Project      string  `json:"project"`
	SessionCount int64   `json:"session_count"`
	UserCount    int64   `json:"user_count"`
	TotalCost    float64 `json:"total_cost"`    // 保留用于向后兼容和排序（跨单位相加）
	InternalCost float64 `json:"internal_cost"` // 内部成本（美元）
	ExternalCost float64 `json:"external_cost"` // 外部成本（积分）
}

// OverviewResponse 总体统计响应
type OverviewResponse struct {
	Summary     OverviewSummary `json:"summary"`
	TopModels   []TopModel      `json:"top_models"`
	Trends      TrendsData      `json:"trends"`
	TopUsers    []TopUser       `json:"top_users"`
	TopProjects []TopProject    `json:"top_projects"`
}

// TrendsData 趋势数据
type TrendsData struct {
	Sessions []TrendPoint      `json:"sessions"`
	Cost     []CostTrendPoint  `json:"cost"`
	Tokens   []TokenTrendPoint `json:"tokens"`
}

// UserRanking 用户排行
type UserRanking struct {
	Rank         int     `json:"rank"`
	Owner        string  `json:"owner"`
	DisplayName  string  `json:"display_name"`
	Email        string  `json:"email"`
	SessionCount int64   `json:"session_count"`
	ProjectCount int64   `json:"project_count"`
	TaskCount    int64   `json:"task_count"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCost    float64 `json:"total_cost"`    // 保留用于向后兼容和排序（跨单位相加）
	InternalCost float64 `json:"internal_cost"` // 内部成本（美元）
	ExternalCost float64 `json:"external_cost"` // 外部成本（积分）
	LastActiveAt string  `json:"last_active_at"`
	LatestTask   string  `json:"latest_task"`
}

// UserRankingResponse 用户排行响应
type UserRankingResponse struct {
	Total int           `json:"total"`
	Users []UserRanking `json:"users"`
}

// GetOverview 获取总体统计数据
func GetOverview(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate string) (*OverviewResponse, error) {
	// 查询总体统计摘要
	summary, err := getOverviewSummary(ctx, db, tenantID, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("get summary: %w", err)
	}

	// 查询最常用模型 Top 3
	topModels, err := getTopModels(ctx, db, tenantID, startDate, endDate, 3)
	if err != nil {
		return nil, fmt.Errorf("get top models: %w", err)
	}

	// 查询趋势数据
	trends, err := getTrends(ctx, db, tenantID, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("get trends: %w", err)
	}

	// 查询最活跃用户 Top 10
	topUsers, err := getTopUsers(ctx, db, tenantID, startDate, endDate, 10)
	if err != nil {
		return nil, fmt.Errorf("get top users: %w", err)
	}

	// 查询最活跃项目 Top 10
	topProjects, err := getTopProjects(ctx, db, tenantID, startDate, endDate, 10)
	if err != nil {
		return nil, fmt.Errorf("get top projects: %w", err)
	}

	return &OverviewResponse{
		Summary:     *summary,
		TopModels:   topModels,
		Trends:      *trends,
		TopUsers:    topUsers,
		TopProjects: topProjects,
	}, nil
}

// getOverviewSummary 获取总体统计摘要
func getOverviewSummary(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate string) (*OverviewSummary, error) {
	// 计算对比周期（前一个周期）
	prevStartDate, prevEndDate, err := getPreviousPeriod(startDate, endDate)
	if err != nil {
		return nil, err
	}

	// 当前周期统计
	// 2026-11-21 对齐轮（rebase 采纳 4e6e14667 的成本分列 + 增长率；SQL 真源
	// 迁到 domains/analytics.QueryOverviewSummary——/dashboard 对齐指标行调用
	// 同一份实现 ⇒ 两面构造相等）。
	var current OverviewSummary
	row, err := analytics.QueryOverviewSummary(ctx, db, tenantID, startDate, endDate)
	if err != nil {
		return nil, err
	}
	current = OverviewSummary{
		TotalSessions:      row.TotalSessions,
		AvgTokens:          row.AvgTokens,
		AvgDurationSeconds: row.AvgDurationSeconds,
		AvgTurns:           row.AvgTurns,
		TotalCost:          row.TotalCost,
		InternalCost:       row.InternalCostUSD,
		ExternalCost:       row.ExternalCredits,
		ActiveUsers:        row.ActiveUsers,
		ActiveProjects:     row.ActiveProjects,
		ActiveTasks:        row.ActiveTasks,
	}

	// 前一周期统计（用于计算增长率）
	var prevTotal int64
	var prevCost float64
	var prevInternalCost float64
	var prevExternalCost float64
	var prevUsers int64
	query := `
		SELECT
			COALESCE(SUM(session_count)::bigint, 0),
			COALESCE(SUM(internal_cost + external_cost), 0),
			COALESCE(SUM(internal_cost), 0),
			COALESCE(SUM(external_cost), 0),
			COALESCE(COUNT(DISTINCT owner), 0)
		FROM user_session_aggregates
		WHERE date >= $1 AND date <= $2
	`
	args := []interface{}{prevStartDate, prevEndDate}
	if tenantID != nil {
		query += " AND tenant_id = $3"
		args = append(args, *tenantID)
	}

	err = db.QueryRowContext(ctx, query, args...).Scan(&prevTotal, &prevCost, &prevInternalCost, &prevExternalCost, &prevUsers)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}

	// 计算增长率
	current.TotalSessionsGrowth = calculateGrowth(current.TotalSessions, prevTotal)
	current.TotalCostGrowth = calculateGrowth(int64(current.TotalCost*100), int64(prevCost*100))
	current.InternalCostGrowth = calculateGrowth(int64(current.InternalCost*100), int64(prevInternalCost*100))
	current.ExternalCostGrowth = calculateGrowth(int64(current.ExternalCost*100), int64(prevExternalCost*100))
	current.ActiveUsersGrowth = calculateGrowth(current.ActiveUsers, prevUsers)

	// 查询模型成功率和缓存率
	modelStats, err := getModelStats(ctx, db, tenantID, startDate, endDate)
	if err != nil {
		return nil, err
	}
	current.OverallSuccessRate = modelStats.SuccessRate
	current.OverallCacheRate = modelStats.CacheRate

	// TODO: avg_context_size 需要从 sessions 表获取
	current.AvgContextSize = 8192 // 暂时使用默认值

	return &current, nil
}

// getTopModels 获取最常用模型
func getTopModels(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate string, limit int) ([]TopModel, error) {
	query := `
		SELECT model, SUM(request_count) as count
		FROM model_usage_aggregates
		WHERE date >= $1 AND date <= $2
	`
	args := []interface{}{startDate, endDate}
	if tenantID != nil {
		query += " AND tenant_id = $3"
		args = append(args, *tenantID)
	}
	query += `
		GROUP BY model
		ORDER BY count DESC
		LIMIT $` + fmt.Sprintf("%d", len(args)+1)
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []TopModel
	for rows.Next() {
		var m TopModel
		if err := rows.Scan(&m.Model, &m.Count); err != nil {
			return nil, err
		}
		models = append(models, m)
	}

	return models, rows.Err()
}

// getTrends 获取趋势数据
func getTrends(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate string) (*TrendsData, error) {
	trends := &TrendsData{}

	// 会话数趋势
	sessionTrend, err := getSessionTrend(ctx, db, tenantID, startDate, endDate)
	if err != nil {
		return nil, err
	}
	trends.Sessions = sessionTrend

	// 成本趋势
	costTrend, err := getCostTrend(ctx, db, tenantID, startDate, endDate)
	if err != nil {
		return nil, err
	}
	trends.Cost = costTrend

	// Token 趋势
	tokenTrend, err := getTokenTrend(ctx, db, tenantID, startDate, endDate)
	if err != nil {
		return nil, err
	}
	trends.Tokens = tokenTrend

	return trends, nil
}

// getSessionTrend 获取会话数趋势
func getSessionTrend(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate string) ([]TrendPoint, error) {
	query := `
		SELECT date::TEXT, SUM(session_count)::bigint as count
		FROM user_session_aggregates
		WHERE date >= $1 AND date <= $2
	`
	args := []interface{}{startDate, endDate}
	if tenantID != nil {
		query += " AND tenant_id = $3"
		args = append(args, *tenantID)
	}
	query += `
		GROUP BY date
		ORDER BY date
	`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []TrendPoint
	for rows.Next() {
		var p TrendPoint
		if err := rows.Scan(&p.Date, &p.Count); err != nil {
			return nil, err
		}
		points = append(points, p)
	}

	return points, rows.Err()
}

// getCostTrend 获取成本趋势
func getCostTrend(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate string) ([]CostTrendPoint, error) {
	query := `
		SELECT date::TEXT, SUM(internal_cost) as internal, SUM(external_cost) as external
		FROM user_session_aggregates
		WHERE date >= $1 AND date <= $2
	`
	args := []interface{}{startDate, endDate}
	if tenantID != nil {
		query += " AND tenant_id = $3"
		args = append(args, *tenantID)
	}
	query += `
		GROUP BY date
		ORDER BY date
	`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []CostTrendPoint
	for rows.Next() {
		var p CostTrendPoint
		if err := rows.Scan(&p.Date, &p.Internal, &p.External); err != nil {
			return nil, err
		}
		points = append(points, p)
	}

	return points, rows.Err()
}

// getTokenTrend 获取 Token 趋势
func getTokenTrend(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate string) ([]TokenTrendPoint, error) {
	query := `
		SELECT date::TEXT, SUM(total_tokens)::bigint as total
		FROM user_session_aggregates
		WHERE date >= $1 AND date <= $2
	`
	args := []interface{}{startDate, endDate}
	if tenantID != nil {
		query += " AND tenant_id = $3"
		args = append(args, *tenantID)
	}
	query += `
		GROUP BY date
		ORDER BY date
	`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []TokenTrendPoint
	for rows.Next() {
		var p TokenTrendPoint
		if err := rows.Scan(&p.Date, &p.Total); err != nil {
			return nil, err
		}
		points = append(points, p)
	}

	return points, rows.Err()
}

// ── SQL 常量（2026-11-19 抽成具名常量）────────────────────────────────
//
// ★ 为什么要抽出来：这些 SQL 里藏着三处**只有在真库才暴露**的缺陷
//   （users.owner 不存在 / owner 取错表 / task 空串语义），
//   而内联字符串没法被测试断言 ⇒ 契约钉测（contract_test.go）只能对着
//   文本检查。抽成常量后，钉测能直接断言 SQL 的形状。
//
// ★ 口径真源：4 个物化视图由迁移 981 建（底座 = session_summaries ×
//   session_dim × sessions / request_logs_with_current_month）；
//   前三个视图是**会话级**口径，model_usage_aggregates 是**请求级**口径
//   （缓存率/成功率/延迟只有请求级有）⇒ 两者不可相加。

// topUsersSQL 人员排行（总体统计的「最活跃用户」卡片）。
//
// ★ owner 取 a.owner（聚合视图自己的列）而不是 u.owner：
//
//	users 表**没有 owner 列**（真库实测：id/tenant_id/username/.../email/role），
//	写成 u.owner 会得到 42703 column does not exist ⇒ 整条查询失败。
const topUsersSQL = `
		SELECT
			a.owner,
			COALESCE(u.display_name, a.owner) as display_name,
			COALESCE(u.email, '') as email,
			SUM(a.session_count) as session_count,
			SUM(a.total_tokens)::bigint as total_tokens,
			SUM(a.internal_cost + a.external_cost) as total_cost
		FROM user_session_aggregates a
		LEFT JOIN users u ON a.owner = u.username
		WHERE a.date >= $1 AND a.date <= $2
`

// userRankingSQL 用户排行（默认口径：全部项目）。
const userRankingSQL = `
		SELECT
			a.owner,
			COALESCE(u.display_name, a.owner) as display_name,
			COALESCE(u.email, '') as email,
			SUM(a.session_count) as session_count,
			SUM(a.project_count) as project_count,
			SUM(a.task_count) as task_count,
			SUM(a.total_tokens)::bigint as total_tokens,
			SUM(a.internal_cost + a.external_cost) as total_cost,
			MAX(a.last_active_at) as last_active_at
		FROM user_session_aggregates a
		LEFT JOIN users u ON a.owner = u.username
		WHERE a.date >= $1 AND a.date <= $2
`

// userRankingByProjectSQL 用户排行（指定项目口径：走 project_task_aggregates）。
//
// ★ task 用 ” 表示「无具体任务」（迁移 981 的唯一索引要求列上无 NULL），
//
//	所以 COUNT(DISTINCT) 必须用 NULLIF 归一，否则「无任务」会被算成一个任务。
const userRankingByProjectSQL = `
			SELECT
				p.owner,
				COALESCE(u.display_name, p.owner) as display_name,
				COALESCE(u.email, '') as email,
				SUM(p.session_count) as session_count,
				COUNT(DISTINCT p.project) as project_count,
				COUNT(DISTINCT NULLIF(p.task, '')) as task_count,
				SUM(p.total_tokens)::bigint as total_tokens,
				SUM(p.internal_cost + p.external_cost) as total_cost,
				MAX(p.last_active_at) as last_active_at
			FROM project_task_aggregates p
			LEFT JOIN users u ON p.owner = u.username
			WHERE p.date >= $1 AND p.date <= $2
`

// latestTaskSQL 取某人在区间内最近一次有任务的任务名。
//
// ★ 判空用 `<> ”` 而不是 `IS NOT NULL`（task 列是 ” 语义，见上）。
const latestTaskSQL = `
		SELECT task
		FROM project_task_aggregates
		WHERE owner = $1 AND date >= $2 AND date <= $3 AND task <> ''
		ORDER BY last_active_at DESC
		LIMIT 1
`

// getTopUsers 获取最活跃用户
func getTopUsers(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate string, limit int) ([]TopUser, error) {
	query := topUsersSQL
	args := []interface{}{startDate, endDate}
	if tenantID != nil {
		query += " AND a.tenant_id = $3"
		args = append(args, *tenantID)
	}
	query += `
		GROUP BY a.owner, u.display_name, u.email
		ORDER BY session_count DESC
		LIMIT $` + fmt.Sprintf("%d", len(args)+1)
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []TopUser
	for rows.Next() {
		var u TopUser
		if err := rows.Scan(&u.Owner, &u.DisplayName, &u.Email, &u.SessionCount, &u.TotalTokens, &u.TotalCost); err != nil {
			return nil, err
		}
		users = append(users, u)
	}

	return users, rows.Err()
}

// getTopProjects 获取最活跃项目
func getTopProjects(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate string, limit int) ([]TopProject, error) {
	query := `
		SELECT
			project,
			SUM(session_count)::bigint as session_count,
			COUNT(DISTINCT owner) as user_count,
			SUM(internal_cost + external_cost) as total_cost,
			SUM(internal_cost) as internal_cost,
			SUM(external_cost) as external_cost
		FROM project_task_aggregates
		WHERE date >= $1 AND date <= $2
	`
	args := []interface{}{startDate, endDate}
	if tenantID != nil {
		query += " AND tenant_id = $3"
		args = append(args, *tenantID)
	}
	query += `
		GROUP BY project
		ORDER BY session_count DESC
		LIMIT $` + fmt.Sprintf("%d", len(args)+1)
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []TopProject
	for rows.Next() {
		var p TopProject
		if err := rows.Scan(&p.Project, &p.SessionCount, &p.UserCount, &p.TotalCost, &p.InternalCost, &p.ExternalCost); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}

	return projects, rows.Err()
}

// getModelStats 获取模型统计（成功率、缓存率）
func getModelStats(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate string) (*struct {
	SuccessRate float64
	CacheRate   float64
}, error) {
	query := `
		SELECT
			COALESCE(SUM(success_count)::NUMERIC / NULLIF(SUM(request_count), 0) * 100, 0) as success_rate,
			COALESCE(SUM(cache_hit_count)::NUMERIC / NULLIF(SUM(request_count), 0) * 100, 0) as cache_rate
		FROM model_usage_aggregates
		WHERE date >= $1 AND date <= $2
	`
	args := []interface{}{startDate, endDate}
	if tenantID != nil {
		query += " AND tenant_id = $3"
		args = append(args, *tenantID)
	}

	var stats struct {
		SuccessRate float64
		CacheRate   float64
	}
	// 2026-11-21 对齐轮：SQL 迁到 domains/analytics.QueryModelStats 单一真源。
	row, err := analytics.QueryModelStats(ctx, db, tenantID, startDate, endDate)
	if err != nil {
		return nil, err
	}
	stats.SuccessRate = row.SuccessRatePct
	stats.CacheRate = row.CacheRatePct
	return &stats, nil
}

// getPreviousPeriod 计算前一个周期的日期范围
// 时区轮（2026-11-20）：日期串按系统时区解析，使「周期长度」按系统时区
// 日历日计算（DST 时区下两个日期串的间隔可能与 24h 整数倍不同）。
func getPreviousPeriod(startDate, endDate string) (string, string, error) {
	start, err := settings.ParseSystemDate(startDate)
	if err != nil {
		return "", "", err
	}
	end, err := settings.ParseSystemDate(endDate)
	if err != nil {
		return "", "", err
	}

	duration := end.Sub(start)
	prevEnd := start.AddDate(0, 0, -1)
	prevStart := prevEnd.Add(-duration)

	return prevStart.Format("2006-01-02"), prevEnd.Format("2006-01-02"), nil
}

// calculateGrowth 计算增长率（百分比）
func calculateGrowth(current, previous int64) float64 {
	if previous == 0 {
		if current > 0 {
			return 100.0
		}
		return 0.0
	}
	return float64(current-previous) / float64(previous) * 100.0
}

// GetUserRanking 获取用户排行
func GetUserRanking(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate string, project *string, sortBy string, order string, limit, offset int) (*UserRankingResponse, error) {
	// aggAlias 是聚合视图的表别名：默认 user_session_aggregates 用 a，
	// 指定 project 时改查 project_task_aggregates 用 p。
	// ★ 为什么必须用变量而不是两段硬编码 SQL：owner 列**在 users 表里不存在**
	//   （真库 users 列：id/tenant_id/username/password_hash/display_name/email/
	//   role/enabled/...）⇒ 写成 u.owner 会得到 42703 column does not exist，
	//   整条查询直接失败（首版就是这么写的，单测用 sqlmock 没打穿）。
	//   owner 的真源是聚合视图自己的列 ⇒ 取 a.owner / p.owner。
	aggAlias := "a"

	// 构建查询
	query := userRankingSQL
	args := []interface{}{startDate, endDate}
	argIndex := 3

	if tenantID != nil {
		query += fmt.Sprintf(" AND a.tenant_id = $%d", argIndex)
		args = append(args, *tenantID)
		argIndex++
	}

	if project != nil {
		// 如果指定了项目，需要从 project_task_aggregates 表查询
		aggAlias = "p"
		query = userRankingByProjectSQL
		// ★ 项目过滤条件（$N 的序号随前面的参数个数变化 ⇒ 必须在代码里拼）。
		query += " AND p.project = $" + fmt.Sprintf("%d", argIndex)
		args = append(args, *project)
		argIndex++

		if tenantID != nil {
			query += fmt.Sprintf(" AND p.tenant_id = $%d", argIndex)
			args = append(args, *tenantID)
			argIndex++
		}
	}

	query += " GROUP BY " + aggAlias + ".owner, u.display_name, u.email"

	// 排序
	orderClause := " ORDER BY "
	switch sortBy {
	case "sessions":
		orderClause += "session_count"
	case "tokens":
		orderClause += "total_tokens"
	case "cost":
		orderClause += "total_cost"
	case "last_active":
		orderClause += "last_active_at"
	default:
		orderClause += "total_tokens"
	}

	if order == "asc" {
		orderClause += " ASC"
	} else {
		orderClause += " DESC"
	}

	query += orderClause

	// 分页
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argIndex, argIndex+1)
	args = append(args, limit, offset)

	// 执行查询
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []UserRanking
	rank := offset + 1
	for rows.Next() {
		var u UserRanking
		var lastActiveAt sql.NullTime
		if err := rows.Scan(
			&u.Owner,
			&u.DisplayName,
			&u.Email,
			&u.SessionCount,
			&u.ProjectCount,
			&u.TaskCount,
			&u.TotalTokens,
			&u.TotalCost,
			&lastActiveAt,
		); err != nil {
			return nil, err
		}

		u.Rank = rank
		rank++

		if lastActiveAt.Valid {
			u.LastActiveAt = lastActiveAt.Time.Format(time.RFC3339)
		}

		// 获取最新任务
		latestTask, err := getLatestTask(ctx, db, u.Owner, startDate, endDate)
		if err == nil && latestTask != "" {
			u.LatestTask = latestTask
		}

		users = append(users, u)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 获取总数
	total, err := getUserRankingTotal(ctx, db, tenantID, startDate, endDate, project)
	if err != nil {
		return nil, err
	}

	return &UserRankingResponse{
		Total: total,
		Users: users,
	}, nil
}

// getLatestTask 获取用户最新的任务
func getLatestTask(ctx context.Context, db *sql.DB, owner, startDate, endDate string) (string, error) {
	// ★ task 列在物化视图里用 ''（不是 NULL）表示「无具体任务」：
	//   视图的唯一索引必须是纯列索引且列上不能有 NULL（PG 的 REFRESH CONCURRENTLY
	//   要求），所以迁移把 NULL 归一成了 ''。这里用 <> '' 把「无任务」过滤回去，
	//   别写成 IS NOT NULL（那会把所有空任务行当成真任务返回）。
	query := latestTaskSQL
	var task sql.NullString
	err := db.QueryRowContext(ctx, query, owner, startDate, endDate).Scan(&task)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}

	if task.Valid {
		return task.String, nil
	}
	return "", nil
}

// getUserRankingTotal 获取用户排行总数
func getUserRankingTotal(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate string, project *string) (int, error) {
	var query string
	var args []interface{}

	if project != nil {
		query = `
			SELECT COUNT(DISTINCT owner)
			FROM project_task_aggregates
			WHERE date >= $1 AND date <= $2 AND project = $3
		`
		args = []interface{}{startDate, endDate, *project}
		if tenantID != nil {
			query += " AND tenant_id = $4"
			args = append(args, *tenantID)
		}
	} else {
		query = `
			SELECT COUNT(DISTINCT owner)
			FROM user_session_aggregates
			WHERE date >= $1 AND date <= $2
		`
		args = []interface{}{startDate, endDate}
		if tenantID != nil {
			query += " AND tenant_id = $3"
			args = append(args, *tenantID)
		}
	}

	var total int
	err := db.QueryRowContext(ctx, query, args...).Scan(&total)
	if err != nil {
		return 0, err
	}

	return total, nil
}

// MarshalJSON 自定义 JSON 序列化（确保数值字段不为 null）
func (s *OverviewSummary) MarshalJSON() ([]byte, error) {
	type Alias OverviewSummary
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(s),
	})
}

// ============================================================================
// 人员详情相关查询
// ============================================================================

// UserDetailSummary 人员详情摘要
type UserDetailSummary struct {
	SessionCount int64   `json:"session_count"`
	TaskCount    int64   `json:"task_count"`
	TotalCost    float64 `json:"total_cost"`    // 保留用于向后兼容（跨单位相加）
	InternalCost float64 `json:"internal_cost"` // 内部成本（美元）
	ExternalCost float64 `json:"external_cost"` // 外部成本（积分）
	Rank         int     `json:"rank"`
	RankChange   int     `json:"rank_change"`
}

// UserInfo 用户基本信息
type UserInfo struct {
	Owner       string `json:"owner"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	TenantID    string `json:"tenant_id"`
}

// UserTrendPoint 用户趋势数据点
type UserTrendPoint struct {
	Date  string `json:"date"`
	Count int64  `json:"count"`
}

// UserRankTrendPoint 用户排名趋势数据点
type UserRankTrendPoint struct {
	Date string `json:"date"`
	Rank int    `json:"rank"`
}

// UserDetailTrends 用户详情趋势数据
type UserDetailTrends struct {
	Sessions []UserTrendPoint     `json:"sessions"`
	Tasks    []UserTrendPoint     `json:"tasks"`
	Cost     []CostTrendPoint     `json:"cost"`
	Rank     []UserRankTrendPoint `json:"rank"`
}

// UserDetailResponse 人员详情响应
type UserDetailResponse struct {
	User    UserInfo          `json:"user"`
	Summary UserDetailSummary `json:"summary"`
	Trends  UserDetailTrends  `json:"trends"`
}

// UserProject 用户项目
type UserProject struct {
	Project      string  `json:"project"`
	SessionCount int64   `json:"session_count"`
	TaskCount    int64   `json:"task_count"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCost    float64 `json:"total_cost"`    // 保留用于向后兼容和排序（跨单位相加）
	InternalCost float64 `json:"internal_cost"` // 内部成本（美元）
	ExternalCost float64 `json:"external_cost"` // 外部成本（积分）
	LastActiveAt string  `json:"last_active_at"`
}

// UserProjectsResponse 用户项目列表响应
type UserProjectsResponse struct {
	Projects []UserProject `json:"projects"`
}

// ProjectTask 项目任务
type ProjectTask struct {
	Task         string  `json:"task"`
	SessionCount int64   `json:"session_count"`
	RequestCount int64   `json:"request_count"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCost    float64 `json:"total_cost"`    // 保留用于向后兼容和排序（跨单位相加）
	InternalCost float64 `json:"internal_cost"` // 内部成本（美元）
	ExternalCost float64 `json:"external_cost"` // 外部成本（积分）
	LastActiveAt string  `json:"last_active_at"`
}

// ProjectTasksResponse 项目任务列表响应
type ProjectTasksResponse struct {
	Tasks []ProjectTask `json:"tasks"`
}

// GetUserDetail 获取用户详情
func GetUserDetail(ctx context.Context, db *sql.DB, owner, startDate, endDate string) (*UserDetailResponse, error) {
	// 获取用户基本信息
	userInfo, err := getUserInfo(ctx, db, owner)
	if err != nil {
		return nil, fmt.Errorf("get user info: %w", err)
	}

	// 获取汇总指标
	summary, err := getUserDetailSummary(ctx, db, owner, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("get summary: %w", err)
	}

	// 获取趋势数据
	trends, err := getUserDetailTrends(ctx, db, owner, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("get trends: %w", err)
	}

	return &UserDetailResponse{
		User:    *userInfo,
		Summary: *summary,
		Trends:  *trends,
	}, nil
}

// getUserInfo 获取用户基本信息
func getUserInfo(ctx context.Context, db *sql.DB, owner string) (*UserInfo, error) {
	query := `
		SELECT
			username as owner,
			COALESCE(display_name, username) as display_name,
			COALESCE(email, '') as email,
			tenant_id
		FROM users
		WHERE username = $1
	`

	var info UserInfo
	err := db.QueryRowContext(ctx, query, owner).Scan(
		&info.Owner,
		&info.DisplayName,
		&info.Email,
		&info.TenantID,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			// 如果用户不在 users 表中，返回基本信息
			return &UserInfo{
				Owner:       owner,
				DisplayName: owner,
				Email:       "",
				TenantID:    "",
			}, nil
		}
		return nil, err
	}

	return &info, nil
}

// getUserDetailSummary 获取用户详情汇总指标
func getUserDetailSummary(ctx context.Context, db *sql.DB, owner, startDate, endDate string) (*UserDetailSummary, error) {
	query := `
		SELECT
			COALESCE(SUM(session_count), 0) as session_count,
			COALESCE(SUM(task_count), 0) as task_count,
			COALESCE(SUM(internal_cost + external_cost), 0) as total_cost,
			COALESCE(SUM(internal_cost), 0) as internal_cost,
			COALESCE(SUM(external_cost), 0) as external_cost
		FROM user_session_aggregates
		WHERE owner = $1 AND date >= $2 AND date <= $3
	`

	var summary UserDetailSummary
	err := db.QueryRowContext(ctx, query, owner, startDate, endDate).Scan(
		&summary.SessionCount,
		&summary.TaskCount,
		&summary.TotalCost,
		&summary.InternalCost,
		&summary.ExternalCost,
	)
	if err != nil {
		return nil, err
	}

	// 计算排名（基于总 tokens）
	rank, rankChange, err := getUserRank(ctx, db, owner, startDate, endDate)
	if err != nil {
		return nil, err
	}
	summary.Rank = rank
	summary.RankChange = rankChange

	return &summary, nil
}

// getUserRank 获取用户排名及变化
func getUserRank(ctx context.Context, db *sql.DB, owner, startDate, endDate string) (int, int, error) {
	// 当前周期排名
	query := `
		WITH ranked_users AS (
			SELECT
				owner,
				SUM(total_tokens)::bigint as total_tokens,
				RANK() OVER (ORDER BY SUM(total_tokens) DESC) as rank
			FROM user_session_aggregates
			WHERE date >= $1 AND date <= $2
			GROUP BY owner
		)
		SELECT rank FROM ranked_users WHERE owner = $3
	`

	var currentRank int
	err := db.QueryRowContext(ctx, query, startDate, endDate, owner).Scan(&currentRank)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, 0, nil
		}
		return 0, 0, err
	}

	// 前一周期排名（用于计算变化）
	prevStartDate, prevEndDate, err := getPreviousPeriod(startDate, endDate)
	if err != nil {
		return currentRank, 0, nil
	}

	var prevRank int
	err = db.QueryRowContext(ctx, query, prevStartDate, prevEndDate, owner).Scan(&prevRank)
	if err != nil {
		if err == sql.ErrNoRows {
			return currentRank, 0, nil
		}
		return currentRank, 0, nil
	}

	// 排名变化（负数表示排名上升，正数表示排名下降）
	rankChange := prevRank - currentRank

	return currentRank, rankChange, nil
}

// getUserDetailTrends 获取用户详情趋势数据
func getUserDetailTrends(ctx context.Context, db *sql.DB, owner, startDate, endDate string) (*UserDetailTrends, error) {
	trends := &UserDetailTrends{}

	// 会话数趋势
	sessionTrend, err := getUserSessionTrend(ctx, db, owner, startDate, endDate)
	if err != nil {
		return nil, err
	}
	trends.Sessions = sessionTrend

	// 任务数趋势
	taskTrend, err := getUserTaskTrend(ctx, db, owner, startDate, endDate)
	if err != nil {
		return nil, err
	}
	trends.Tasks = taskTrend

	// 成本趋势
	costTrend, err := getUserCostTrend(ctx, db, owner, startDate, endDate)
	if err != nil {
		return nil, err
	}
	trends.Cost = costTrend

	// 排名趋势
	rankTrend, err := getUserRankTrend(ctx, db, owner, startDate, endDate)
	if err != nil {
		return nil, err
	}
	trends.Rank = rankTrend

	return trends, nil
}

// getUserSessionTrend 获取用户会话数趋势
func getUserSessionTrend(ctx context.Context, db *sql.DB, owner, startDate, endDate string) ([]UserTrendPoint, error) {
	query := `
		SELECT date::TEXT, SUM(session_count) as count
		FROM user_session_aggregates
		WHERE owner = $1 AND date >= $2 AND date <= $3
		GROUP BY date
		ORDER BY date
	`

	rows, err := db.QueryContext(ctx, query, owner, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []UserTrendPoint
	for rows.Next() {
		var p UserTrendPoint
		if err := rows.Scan(&p.Date, &p.Count); err != nil {
			return nil, err
		}
		points = append(points, p)
	}

	return points, rows.Err()
}

// getUserTaskTrend 获取用户任务数趋势
func getUserTaskTrend(ctx context.Context, db *sql.DB, owner, startDate, endDate string) ([]UserTrendPoint, error) {
	query := `
		SELECT date::TEXT, SUM(task_count) as count
		FROM user_session_aggregates
		WHERE owner = $1 AND date >= $2 AND date <= $3
		GROUP BY date
		ORDER BY date
	`

	rows, err := db.QueryContext(ctx, query, owner, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []UserTrendPoint
	for rows.Next() {
		var p UserTrendPoint
		if err := rows.Scan(&p.Date, &p.Count); err != nil {
			return nil, err
		}
		points = append(points, p)
	}

	return points, rows.Err()
}

// getUserCostTrend 获取用户成本趋势
func getUserCostTrend(ctx context.Context, db *sql.DB, owner, startDate, endDate string) ([]CostTrendPoint, error) {
	query := `
		SELECT date::TEXT, SUM(internal_cost) as internal, SUM(external_cost) as external
		FROM user_session_aggregates
		WHERE owner = $1 AND date >= $2 AND date <= $3
		GROUP BY date
		ORDER BY date
	`

	rows, err := db.QueryContext(ctx, query, owner, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []CostTrendPoint
	for rows.Next() {
		var p CostTrendPoint
		if err := rows.Scan(&p.Date, &p.Internal, &p.External); err != nil {
			return nil, err
		}
		points = append(points, p)
	}

	return points, rows.Err()
}

// getUserRankTrend 获取用户排名趋势
func getUserRankTrend(ctx context.Context, db *sql.DB, owner, startDate, endDate string) ([]UserRankTrendPoint, error) {
	query := `
		WITH daily_ranks AS (
			SELECT
				date,
				owner,
				RANK() OVER (PARTITION BY date ORDER BY SUM(total_tokens) DESC) as rank
			FROM user_session_aggregates
			WHERE date >= $1 AND date <= $2
			GROUP BY date, owner
		)
		SELECT date::TEXT, rank
		FROM daily_ranks
		WHERE owner = $3
		ORDER BY date
	`

	rows, err := db.QueryContext(ctx, query, startDate, endDate, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []UserRankTrendPoint
	for rows.Next() {
		var p UserRankTrendPoint
		if err := rows.Scan(&p.Date, &p.Rank); err != nil {
			return nil, err
		}
		points = append(points, p)
	}

	return points, rows.Err()
}

// GetUserProjects 获取用户项目列表
func GetUserProjects(ctx context.Context, db *sql.DB, owner, startDate, endDate, sortBy, order string) (*UserProjectsResponse, error) {
	query := `
		SELECT
			project,
			SUM(session_count) as session_count,
			COUNT(DISTINCT NULLIF(task, '')) as task_count,
			SUM(total_tokens)::bigint as total_tokens,
			SUM(internal_cost + external_cost) as total_cost,
			SUM(internal_cost) as internal_cost,
			SUM(external_cost) as external_cost,
			MAX(last_active_at) as last_active_at
		FROM project_task_aggregates
		WHERE owner = $1 AND date >= $2 AND date <= $3
		GROUP BY project
	`

	// 排序
	orderClause := " ORDER BY "
	switch sortBy {
	case "sessions":
		orderClause += "session_count"
	case "cost":
		orderClause += "total_cost"
	case "last_active":
		orderClause += "last_active_at"
	default:
		orderClause += "session_count"
	}

	if order == "asc" {
		orderClause += " ASC"
	} else {
		orderClause += " DESC"
	}

	query += orderClause

	rows, err := db.QueryContext(ctx, query, owner, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []UserProject
	for rows.Next() {
		var p UserProject
		var lastActiveAt sql.NullTime
		if err := rows.Scan(
			&p.Project,
			&p.SessionCount,
			&p.TaskCount,
			&p.TotalTokens,
			&p.TotalCost,
			&p.InternalCost,
			&p.ExternalCost,
			&lastActiveAt,
		); err != nil {
			return nil, err
		}

		if lastActiveAt.Valid {
			p.LastActiveAt = lastActiveAt.Time.Format(time.RFC3339)
		}

		projects = append(projects, p)
	}

	return &UserProjectsResponse{
		Projects: projects,
	}, rows.Err()
}

// GetProjectTasks 获取项目任务列表
func GetProjectTasks(ctx context.Context, db *sql.DB, owner, project, startDate, endDate, sortBy string) (*ProjectTasksResponse, error) {
	query := `
		SELECT
			task,
			SUM(session_count) as session_count,
			SUM(turn_count) as request_count,
			SUM(total_tokens)::bigint as total_tokens,
			SUM(internal_cost + external_cost) as total_cost,
			SUM(internal_cost) as internal_cost,
			SUM(external_cost) as external_cost,
			MAX(last_active_at) as last_active_at
		FROM project_task_aggregates
		WHERE owner = $1 AND project = $2 AND date >= $3 AND date <= $4
			AND task <> ''
		GROUP BY task
	`

	// 排序
	orderClause := " ORDER BY "
	switch sortBy {
	case "sessions":
		orderClause += "session_count"
	case "cost":
		orderClause += "total_cost"
	case "last_active":
		orderClause += "last_active_at"
	default:
		orderClause += "session_count"
	}
	orderClause += " DESC"

	query += orderClause

	rows, err := db.QueryContext(ctx, query, owner, project, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []ProjectTask
	for rows.Next() {
		var t ProjectTask
		var lastActiveAt sql.NullTime
		if err := rows.Scan(
			&t.Task,
			&t.SessionCount,
			&t.RequestCount,
			&t.TotalTokens,
			&t.TotalCost,
			&t.InternalCost,
			&t.ExternalCost,
			&lastActiveAt,
		); err != nil {
			return nil, err
		}

		if lastActiveAt.Valid {
			t.LastActiveAt = lastActiveAt.Time.Format(time.RFC3339)
		}

		tasks = append(tasks, t)
	}

	return &ProjectTasksResponse{
		Tasks: tasks,
	}, rows.Err()
}

// ============================================================================
// 智能体排行查询
// ============================================================================

// AgentRanking 智能体排行
type AgentRanking struct {
	Rank               int     `json:"rank"`
	AgentName          string  `json:"agent_name"`
	UserCount          int64   `json:"user_count"`
	SessionCount       int64   `json:"session_count"`
	TaskCount          int64   `json:"task_count"`
	TotalTokens        int64   `json:"total_tokens"`
	TotalCost          float64 `json:"total_cost"`    // 保留用于向后兼容和排序（跨单位相加）
	InternalCost       float64 `json:"internal_cost"` // 内部成本（美元）
	ExternalCost       float64 `json:"external_cost"` // 外部成本（积分）
	AvgDurationSeconds float64 `json:"avg_duration_seconds"`
	LastUsedAt         string  `json:"last_used_at"`
}

// AgentRankingResponse 智能体排行响应
type AgentRankingResponse struct {
	Agents []AgentRanking `json:"agents"`
}

// GetAgentRanking 获取智能体排行
func GetAgentRanking(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate, sortBy, order string) (*AgentRankingResponse, error) {
	query := `
		SELECT
			agent_name,
			SUM(user_count) as user_count,
			SUM(session_count) as session_count,
			SUM(task_count) as task_count,
			SUM(total_tokens)::bigint as total_tokens,
			SUM(internal_cost + external_cost) as total_cost,
			SUM(internal_cost) as internal_cost,
			SUM(external_cost) as external_cost,
			AVG(avg_duration_seconds) as avg_duration_seconds,
			MAX(last_used_at) as last_used_at
		FROM agent_usage_aggregates
		WHERE date >= $1 AND date <= $2
	`
	args := []interface{}{startDate, endDate}
	if tenantID != nil {
		query += " AND tenant_id = $3"
		args = append(args, *tenantID)
	}
	query += " GROUP BY agent_name"

	// 排序
	orderClause := " ORDER BY "
	switch sortBy {
	case "sessions":
		orderClause += "session_count"
	case "users":
		orderClause += "user_count"
	case "cost":
		orderClause += "total_cost"
	case "last_used":
		orderClause += "last_used_at"
	default:
		orderClause += "session_count"
	}

	if order == "asc" {
		orderClause += " ASC"
	} else {
		orderClause += " DESC"
	}

	query += orderClause

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []AgentRanking
	rank := 1
	for rows.Next() {
		var a AgentRanking
		var lastUsedAt sql.NullTime
		if err := rows.Scan(
			&a.AgentName,
			&a.UserCount,
			&a.SessionCount,
			&a.TaskCount,
			&a.TotalTokens,
			&a.TotalCost,
			&a.InternalCost,
			&a.ExternalCost,
			&a.AvgDurationSeconds,
			&lastUsedAt,
		); err != nil {
			return nil, err
		}

		a.Rank = rank
		rank++

		if lastUsedAt.Valid {
			a.LastUsedAt = lastUsedAt.Time.Format(time.RFC3339)
		}

		agents = append(agents, a)
	}

	return &AgentRankingResponse{
		Agents: agents,
	}, rows.Err()
}

// ============================================================================
// 模型排行查询
// ============================================================================

// ModelRanking 模型排行
type ModelRanking struct {
	Rank          int     `json:"rank"`
	Model         string  `json:"model"`
	Provider      string  `json:"provider"`
	RequestCount  int64   `json:"request_count"`
	SuccessCount  int64   `json:"success_count"`
	SuccessRate   float64 `json:"success_rate"`
	TotalTokens   int64   `json:"total_tokens"`
	CacheHitCount int64   `json:"cache_hit_count"`
	CacheRate     float64 `json:"cache_rate"`
	AvgDurationMs float64 `json:"avg_duration_ms"`
	TotalCost     float64 `json:"total_cost"`    // 保留用于向后兼容和排序（跨单位相加）
	InternalCost  float64 `json:"internal_cost"` // 内部成本（美元）
	ExternalCost  float64 `json:"external_cost"` // 外部成本（积分）
	UserCount     int64   `json:"user_count"`
	LastUsedAt    string  `json:"last_used_at"`
}

// ModelRankingResponse 模型排行响应
type ModelRankingResponse struct {
	Models []ModelRanking `json:"models"`
}

// GetModelRanking 获取模型排行
func GetModelRanking(ctx context.Context, db *sql.DB, tenantID *string, startDate, endDate, sortBy, order string) (*ModelRankingResponse, error) {
	query := `
		SELECT
			model,
			provider,
			SUM(request_count) as request_count,
			SUM(success_count) as success_count,
			COALESCE(SUM(success_count)::NUMERIC / NULLIF(SUM(request_count), 0) * 100, 0) as success_rate,
			SUM(input_tokens + output_tokens) as total_tokens,
			SUM(cache_hit_count) as cache_hit_count,
			COALESCE(SUM(cache_hit_count)::NUMERIC / NULLIF(SUM(request_count), 0) * 100, 0) as cache_rate,
			AVG(avg_duration_ms) as avg_duration_ms,
			SUM(internal_cost + external_cost) as total_cost,
			SUM(internal_cost) as internal_cost,
			SUM(external_cost) as external_cost,
			SUM(user_count) as user_count,
			MAX(last_used_at) as last_used_at
		FROM model_usage_aggregates
		WHERE date >= $1 AND date <= $2
	`
	args := []interface{}{startDate, endDate}
	if tenantID != nil {
		query += " AND tenant_id = $3"
		args = append(args, *tenantID)
	}
	query += " GROUP BY model, provider"

	// 排序
	orderClause := " ORDER BY "
	switch sortBy {
	case "requests":
		orderClause += "request_count"
	case "tokens":
		orderClause += "total_tokens"
	case "cost":
		orderClause += "total_cost"
	case "success_rate":
		orderClause += "success_rate"
	case "cache_rate":
		orderClause += "cache_rate"
	case "last_used":
		orderClause += "last_used_at"
	default:
		orderClause += "request_count"
	}

	if order == "asc" {
		orderClause += " ASC"
	} else {
		orderClause += " DESC"
	}

	query += orderClause

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []ModelRanking
	rank := 1
	for rows.Next() {
		var m ModelRanking
		var lastUsedAt sql.NullTime
		if err := rows.Scan(
			&m.Model,
			&m.Provider,
			&m.RequestCount,
			&m.SuccessCount,
			&m.SuccessRate,
			&m.TotalTokens,
			&m.CacheHitCount,
			&m.CacheRate,
			&m.AvgDurationMs,
			&m.TotalCost,
			&m.InternalCost,
			&m.ExternalCost,
			&m.UserCount,
			&lastUsedAt,
		); err != nil {
			return nil, err
		}

		m.Rank = rank
		rank++

		if lastUsedAt.Valid {
			m.LastUsedAt = lastUsedAt.Time.Format(time.RFC3339)
		}

		models = append(models, m)
	}

	return &ModelRankingResponse{
		Models: models,
	}, rows.Err()
}
