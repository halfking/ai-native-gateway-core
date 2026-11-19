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
)

// OverviewSummary 总体统计摘要
type OverviewSummary struct {
	TotalSessions       int64   `json:"total_sessions"`
	TotalSessionsGrowth float64 `json:"total_sessions_growth"`
	AvgTokens           float64 `json:"avg_tokens"`
	AvgContextSize      int64   `json:"avg_context_size"`
	AvgDurationSeconds  float64 `json:"avg_duration_seconds"`
	AvgTurns            float64 `json:"avg_turns"`
	TotalCost           float64 `json:"total_cost"`
	TotalCostGrowth     float64 `json:"total_cost_growth"`
	ActiveUsers         int64   `json:"active_users"`
	ActiveUsersGrowth   float64 `json:"active_users_growth"`
	ActiveProjects      int64   `json:"active_projects"`
	ActiveTasks         int64   `json:"active_tasks"`
	OverallSuccessRate  float64 `json:"overall_success_rate"`
	OverallCacheRate    float64 `json:"overall_cache_rate"`
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
	TotalCost    float64 `json:"total_cost"`
}

// TopProject 最活跃项目
type TopProject struct {
	Project      string  `json:"project"`
	SessionCount int64   `json:"session_count"`
	UserCount    int64   `json:"user_count"`
	TotalCost    float64 `json:"total_cost"`
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
	TotalCost    float64 `json:"total_cost"`
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
	var current OverviewSummary
	query := `
		SELECT
			COALESCE(SUM(session_count), 0) as total_sessions,
			ROUND(COALESCE(AVG(total_tokens::NUMERIC / NULLIF(session_count, 0)), 0), 2) as avg_tokens,
			COALESCE(AVG(avg_duration_seconds), 0) as avg_duration_seconds,
			COALESCE(AVG(avg_turns), 0) as avg_turns,
			COALESCE(SUM(internal_cost + external_cost), 0) as total_cost,
			COALESCE(COUNT(DISTINCT owner), 0) as active_users,
			COALESCE(SUM(project_count), 0) as active_projects,
			COALESCE(SUM(task_count), 0) as active_tasks
		FROM user_session_aggregates
		WHERE date >= $1 AND date <= $2
	`
	args := []interface{}{startDate, endDate}
	if tenantID != nil {
		query += " AND tenant_id = $3"
		args = append(args, *tenantID)
	}

	err = db.QueryRowContext(ctx, query, args...).Scan(
		&current.TotalSessions,
		&current.AvgTokens,
		&current.AvgDurationSeconds,
		&current.AvgTurns,
		&current.TotalCost,
		&current.ActiveUsers,
		&current.ActiveProjects,
		&current.ActiveTasks,
	)
	if err != nil {
		return nil, err
	}

	// 前一周期统计（用于计算增长率）
	var prevTotal int64
	var prevCost float64
	var prevUsers int64
	query = `
		SELECT
			COALESCE(SUM(session_count), 0),
			COALESCE(SUM(internal_cost + external_cost), 0),
			COALESCE(COUNT(DISTINCT owner), 0)
		FROM user_session_aggregates
		WHERE date >= $1 AND date <= $2
	`
	args = []interface{}{prevStartDate, prevEndDate}
	if tenantID != nil {
		query += " AND tenant_id = $3"
		args = append(args, *tenantID)
	}

	err = db.QueryRowContext(ctx, query, args...).Scan(&prevTotal, &prevCost, &prevUsers)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}

	// 计算增长率
	current.TotalSessionsGrowth = calculateGrowth(current.TotalSessions, prevTotal)
	current.TotalCostGrowth = calculateGrowth(int64(current.TotalCost*100), int64(prevCost*100))
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
		SELECT date::TEXT, SUM(session_count) as count
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
			SUM(session_count) as session_count,
			COUNT(DISTINCT owner) as user_count,
			SUM(internal_cost + external_cost) as total_cost
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
		if err := rows.Scan(&p.Project, &p.SessionCount, &p.UserCount, &p.TotalCost); err != nil {
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
	err := db.QueryRowContext(ctx, query, args...).Scan(&stats.SuccessRate, &stats.CacheRate)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}

	return &stats, nil
}

// getPreviousPeriod 计算前一个周期的日期范围
func getPreviousPeriod(startDate, endDate string) (string, string, error) {
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return "", "", err
	}
	end, err := time.Parse("2006-01-02", endDate)
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
