package taskmanagement

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TaskStatsManager 任务统计管理器
//
// 职责：
//   - 任务统计
//   - 负载监控
//   - 性能分析
type TaskStatsManager struct {
	pool *pgxpool.Pool
}

// NewTaskStatsManager 创建任务统计管理器
func NewTaskStatsManager(pool *pgxpool.Pool) *TaskStatsManager {
	return &TaskStatsManager{pool: pool}
}

// GetGroupStats 获取任务组统计
func (m *TaskStatsManager) GetGroupStats(ctx context.Context, groupID string) (*TaskGroupStats, error) {
	stats := &TaskGroupStats{
		GroupID:    groupID,
		MemberLoad: make(map[string]int),
	}

	// 查询任务组成员数
	memberCountQuery := `
		SELECT jsonb_array_length(members) 
		FROM task_groups 
		WHERE id = $1
	`
	err := m.pool.QueryRow(ctx, memberCountQuery, groupID).Scan(&stats.TotalMembers)
	if err != nil {
		return nil, fmt.Errorf("failed to get member count: %w", err)
	}

	// 查询任务统计
	taskStatsQuery := `
		SELECT 
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE status = 'pending') as pending,
			COUNT(*) FILTER (WHERE status = 'in_progress') as in_progress,
			COUNT(*) FILTER (WHERE status = 'completed') as completed
		FROM task_assignments
		WHERE group_id = $1
	`

	err = m.pool.QueryRow(ctx, taskStatsQuery, groupID).Scan(
		&stats.TotalTasks,
		&stats.PendingTasks,
		&stats.InProgressTasks,
		&stats.CompletedTasks,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get task stats: %w", err)
	}

	// 查询平均完成时间
	avgTimeQuery := `
		SELECT AVG(EXTRACT(EPOCH FROM (completed_at - assigned_at)))
		FROM task_assignments
		WHERE group_id = $1 AND status = 'completed' AND completed_at IS NOT NULL
	`

	var avgTime *float64
	err = m.pool.QueryRow(ctx, avgTimeQuery, groupID).Scan(&avgTime)
	if err != nil {
		return nil, fmt.Errorf("failed to get avg completion time: %w", err)
	}

	if avgTime != nil {
		stats.AvgCompletionTime = *avgTime
	}

	// 查询成员负载
	memberLoadQuery := `
		SELECT assignee_id, COUNT(*)
		FROM task_assignments
		WHERE group_id = $1 AND status IN ('pending', 'in_progress')
		GROUP BY assignee_id
	`

	rows, err := m.pool.Query(ctx, memberLoadQuery, groupID)
	if err != nil {
		return nil, fmt.Errorf("failed to get member load: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var memberID string
		var count int
		if err := rows.Scan(&memberID, &count); err != nil {
			continue
		}
		stats.MemberLoad[memberID] = count
	}

	return stats, nil
}

// GetMemberStats 获取成员统计
func (m *TaskStatsManager) GetMemberStats(ctx context.Context, memberID string, timeRange time.Duration) (*MemberStats, error) {
	stats := &MemberStats{
		MemberID: memberID,
	}

	startTime := time.Now().Add(-timeRange)

	// 查询任务统计
	taskStatsQuery := `
		SELECT 
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE status = 'pending') as pending,
			COUNT(*) FILTER (WHERE status = 'in_progress') as in_progress,
			COUNT(*) FILTER (WHERE status = 'completed') as completed,
			COUNT(*) FILTER (WHERE status = 'canceled') as canceled
		FROM task_assignments
		WHERE assignee_id = $1 AND assigned_at >= $2
	`

	err := m.pool.QueryRow(ctx, taskStatsQuery, memberID, startTime).Scan(
		&stats.TotalTasks,
		&stats.PendingTasks,
		&stats.InProgressTasks,
		&stats.CompletedTasks,
		&stats.CanceledTasks,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get member task stats: %w", err)
	}

	// 查询平均完成时间
	avgTimeQuery := `
		SELECT AVG(EXTRACT(EPOCH FROM (completed_at - assigned_at)))
		FROM task_assignments
		WHERE assignee_id = $1 AND status = 'completed' 
		  AND completed_at IS NOT NULL AND assigned_at >= $2
	`

	var avgTime *float64
	err = m.pool.QueryRow(ctx, avgTimeQuery, memberID, startTime).Scan(&avgTime)
	if err != nil {
		return nil, fmt.Errorf("failed to get avg completion time: %w", err)
	}

	if avgTime != nil {
		stats.AvgCompletionTime = *avgTime
	}

	// 计算完成率
	if stats.TotalTasks > 0 {
		stats.CompletionRate = float64(stats.CompletedTasks) / float64(stats.TotalTasks)
	}

	return stats, nil
}

// GetTenantStats 获取租户统计
func (m *TaskStatsManager) GetTenantStats(ctx context.Context, tenantID string) (*TenantStats, error) {
	stats := &TenantStats{
		TenantID: tenantID,
	}

	// 查询任务组数量
	groupCountQuery := `SELECT COUNT(*) FROM task_groups WHERE tenant_id = $1`
	err := m.pool.QueryRow(ctx, groupCountQuery, tenantID).Scan(&stats.TotalGroups)
	if err != nil {
		return nil, fmt.Errorf("failed to get group count: %w", err)
	}

	// 查询任务统计
	taskStatsQuery := `
		SELECT 
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE status = 'pending') as pending,
			COUNT(*) FILTER (WHERE status = 'in_progress') as in_progress,
			COUNT(*) FILTER (WHERE status = 'completed') as completed
		FROM task_assignments ta
		JOIN task_groups tg ON ta.group_id = tg.id
		WHERE tg.tenant_id = $1
	`

	err = m.pool.QueryRow(ctx, taskStatsQuery, tenantID).Scan(
		&stats.TotalTasks,
		&stats.PendingTasks,
		&stats.InProgressTasks,
		&stats.CompletedTasks,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get task stats: %w", err)
	}

	return stats, nil
}

// GetTaskDistribution 获取任务分布（按类型）
func (m *TaskStatsManager) GetTaskDistribution(ctx context.Context, tenantID string) (map[TaskType]int, error) {
	distribution := make(map[TaskType]int)

	query := `
		SELECT ta.task_type, COUNT(*)
		FROM task_assignments ta
		JOIN task_groups tg ON ta.group_id = tg.id
		WHERE tg.tenant_id = $1
		GROUP BY ta.task_type
	`

	rows, err := m.pool.Query(ctx, query, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task distribution: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var taskType TaskType
		var count int
		if err := rows.Scan(&taskType, &count); err != nil {
			continue
		}
		distribution[taskType] = count
	}

	return distribution, nil
}

// GetLoadTrend 获取负载趋势
func (m *TaskStatsManager) GetLoadTrend(ctx context.Context, groupID string, interval time.Duration, points int) ([]LoadPoint, error) {
	trend := make([]LoadPoint, 0, points)

	now := time.Now()

	for i := points - 1; i >= 0; i-- {
		pointTime := now.Add(-time.Duration(i) * interval)

		query := `
			SELECT COUNT(*)
			FROM task_assignments
			WHERE group_id = $1 
			  AND assigned_at <= $2
			  AND (completed_at IS NULL OR completed_at > $2)
		`

		var count int
		err := m.pool.QueryRow(ctx, query, groupID, pointTime).Scan(&count)
		if err != nil {
			continue
		}

		trend = append(trend, LoadPoint{
			Timestamp: pointTime,
			Load:      count,
		})
	}

	return trend, nil
}

// MemberStats 成员统计
type MemberStats struct {
	MemberID          string
	TotalTasks        int
	PendingTasks      int
	InProgressTasks   int
	CompletedTasks    int
	CanceledTasks     int
	AvgCompletionTime float64
	CompletionRate    float64
}

// TenantStats 租户统计
type TenantStats struct {
	TenantID        string
	TotalGroups     int
	TotalTasks      int
	PendingTasks    int
	InProgressTasks int
	CompletedTasks  int
}

// LoadPoint 负载点
type LoadPoint struct {
	Timestamp time.Time
	Load      int
}

// GetTopPerformers 获取表现最佳的成员
func (m *TaskStatsManager) GetTopPerformers(ctx context.Context, groupID string, limit int) ([]MemberPerformance, error) {
	query := `
		SELECT 
			assignee_id,
			assignee_name,
			COUNT(*) FILTER (WHERE status = 'completed') as completed_count,
			AVG(EXTRACT(EPOCH FROM (completed_at - assigned_at))) FILTER (WHERE status = 'completed') as avg_time,
			COUNT(*) FILTER (WHERE status = 'completed')::float / NULLIF(COUNT(*), 0) as completion_rate
		FROM task_assignments
		WHERE group_id = $1
		GROUP BY assignee_id, assignee_name
		ORDER BY completed_count DESC, avg_time ASC
		LIMIT $2
	`

	rows, err := m.pool.Query(ctx, query, groupID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get top performers: %w", err)
	}
	defer rows.Close()

	performers := make([]MemberPerformance, 0)
	for rows.Next() {
		var perf MemberPerformance
		var avgTime *float64
		var completionRate *float64

		if err := rows.Scan(&perf.MemberID, &perf.MemberName, &perf.CompletedTasks, &avgTime, &completionRate); err != nil {
			continue
		}

		if avgTime != nil {
			perf.AvgCompletionTime = *avgTime
		}
		if completionRate != nil {
			perf.CompletionRate = *completionRate
		}

		performers = append(performers, perf)
	}

	return performers, nil
}

// MemberPerformance 成员表现
type MemberPerformance struct {
	MemberID          string
	MemberName        string
	CompletedTasks    int
	AvgCompletionTime float64
	CompletionRate    float64
}

// GetOverloadedMembers 获取过载的成员
func (m *TaskStatsManager) GetOverloadedMembers(ctx context.Context, groupID string, threshold int) ([]string, error) {
	query := `
		SELECT assignee_id, COUNT(*)
		FROM task_assignments
		WHERE group_id = $1 AND status IN ('pending', 'in_progress')
		GROUP BY assignee_id
		HAVING COUNT(*) > $2
	`

	rows, err := m.pool.Query(ctx, query, groupID, threshold)
	if err != nil {
		return nil, fmt.Errorf("failed to get overloaded members: %w", err)
	}
	defer rows.Close()

	members := make([]string, 0)
	for rows.Next() {
		var memberID string
		var count int
		if err := rows.Scan(&memberID, &count); err != nil {
			continue
		}
		members = append(members, memberID)
	}

	return members, nil
}
