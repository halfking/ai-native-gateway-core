// Package taskaggregator 实现任务级别的会话聚合和标签归一化
package taskaggregator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TaskAggregator 任务聚合器
type TaskAggregator struct {
	pool       *pgxpool.Pool
	normalizer *TagNormalizer
}

// NewTaskAggregator 创建任务聚合器
func NewTaskAggregator(pool *pgxpool.Pool) *TaskAggregator {
	return &TaskAggregator{
		pool:       pool,
		normalizer: NewTagNormalizer(),
	}
}

// TaskSummaryInput 任务总结输入
type TaskSummaryInput struct {
	TaskID      string
	TenantID    string
	Name        string
	Description string
	WorkType    string
	ProjectRef  string
	Source      string // "acc" | "inferred" | "manual"
}

// UpdateTaskSummary 更新任务级别的总结和标签
// 从所属会话的总结中聚合信息
func (a *TaskAggregator) UpdateTaskSummary(
	ctx context.Context,
	taskID string,
	tenantID string,
) error {
	if a.pool == nil {
		return fmt.Errorf("pool not configured")
	}

	// 1. 查询该任务下的所有会话总结
	sessions, err := a.getTaskSessions(ctx, tenantID, taskID)
	if err != nil {
		return fmt.Errorf("get task sessions failed: %w", err)
	}

	if len(sessions) == 0 {
		return fmt.Errorf("no sessions found for task %s", taskID)
	}

	// 2. 聚合标签
	allTags := []string{}
	allTopics := []string{}
	for _, s := range sessions {
		allTags = append(allTags, s.Tags...)
		allTopics = append(allTopics, s.KeyTopics...)
	}

	// 3. 归一化标签
	canonicalTags := a.normalizer.Normalize(allTags)

	// 4. 提取关键主题 (去重后取前10个)
	keyThemes := deduplicateStrings(allTopics)
	if len(keyThemes) > 10 {
		keyThemes = keyThemes[:10]
	}

	// 5. 生成任务级别总结 (简单合并会话摘要)
	summary := a.buildTaskSummary(sessions)

	// 6. 计算统计信息
	stats := a.calculateTaskStats(sessions)

	// 7. 更新 tasks 表
	if err := a.updateTaskRecord(ctx, taskID, tenantID, canonicalTags, keyThemes, summary, stats); err != nil {
		return fmt.Errorf("update task record failed: %w", err)
	}

	return nil
}

// SessionSummary 会话总结信息
type SessionSummary struct {
	SessionKey   string
	Title        string
	Summary      string
	KeyTopics    []string
	Tags         []string
	RequestCount int
	CostUSD      float64
	CreatedAt    time.Time
}

// getTaskSessions 获取任务下的所有会话
func (a *TaskAggregator) getTaskSessions(
	ctx context.Context,
	tenantID, taskID string,
) ([]SessionSummary, error) {
	query := `
		SELECT session_key, title, summary, key_topics, tags,
		       COALESCE((
		           SELECT COUNT(*) FROM request_logs_with_current_month rl
		           WHERE rl.gw_session_id = ss.session_key AND rl.tenant_id = ss.tenant_id
		       ), 0) as request_count,
		       COALESCE((
		           SELECT SUM(COALESCE(rl.cost_display, 0) + COALESCE(rl.cost_usd, 0))
		           FROM request_logs_with_current_month rl
		           WHERE rl.gw_session_id = ss.session_key AND rl.tenant_id = ss.tenant_id
		       ), 0) as cost_usd,
		       created_at
		FROM session_summaries ss
		WHERE tenant_id = $1 AND task_id = $2
		ORDER BY created_at ASC
	`

	rows, err := a.pool.Query(ctx, query, tenantID, taskID)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var sessions []SessionSummary
	for rows.Next() {
		var s SessionSummary
		err := rows.Scan(
			&s.SessionKey, &s.Title, &s.Summary, &s.KeyTopics, &s.Tags,
			&s.RequestCount, &s.CostUSD, &s.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		sessions = append(sessions, s)
	}

	return sessions, rows.Err()
}

// buildTaskSummary 构建任务级别总结
func (a *TaskAggregator) buildTaskSummary(sessions []SessionSummary) string {
	if len(sessions) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("本任务包含 %d 个会话，主要活动如下：\n\n", len(sessions)))

	// 取前5个会话的标题和摘要
	limit := 5
	if len(sessions) < limit {
		limit = len(sessions)
	}

	for i := 0; i < limit; i++ {
		s := sessions[i]
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, s.Title))
		if s.Summary != "" {
			summary := s.Summary
			if len(summary) > 100 {
				summary = summary[:100] + "..."
			}
			sb.WriteString(fmt.Sprintf("   %s\n\n", summary))
		}
	}

	if len(sessions) > limit {
		sb.WriteString(fmt.Sprintf("...以及其余 %d 个会话。\n", len(sessions)-limit))
	}

	return sb.String()
}

// TaskStats 任务统计信息
type TaskStats struct {
	SessionCount    int
	TotalRequests   int
	TotalCostUSD    float64
	FirstActivityAt time.Time
	LastActivityAt  time.Time
}

// calculateTaskStats 计算任务统计信息
func (a *TaskAggregator) calculateTaskStats(sessions []SessionSummary) TaskStats {
	stats := TaskStats{
		SessionCount: len(sessions),
	}

	if len(sessions) == 0 {
		return stats
	}

	stats.FirstActivityAt = sessions[0].CreatedAt
	stats.LastActivityAt = sessions[len(sessions)-1].CreatedAt

	for _, s := range sessions {
		stats.TotalRequests += s.RequestCount
		stats.TotalCostUSD += s.CostUSD
	}

	return stats
}

// updateTaskRecord 更新 tasks 表记录
func (a *TaskAggregator) updateTaskRecord(
	ctx context.Context,
	taskID, tenantID string,
	canonicalTags, keyThemes []string,
	summary string,
	stats TaskStats,
) error {
	query := `
		INSERT INTO tasks (
			task_id, tenant_id, 
			canonical_tags, key_themes, summary,
			session_count, total_requests, total_cost_usd,
			first_activity_at, last_activity_at,
			status, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'active', NOW())
		ON CONFLICT (task_id) DO UPDATE SET
			canonical_tags = EXCLUDED.canonical_tags,
			key_themes = EXCLUDED.key_themes,
			summary = EXCLUDED.summary,
			session_count = EXCLUDED.session_count,
			total_requests = EXCLUDED.total_requests,
			total_cost_usd = EXCLUDED.total_cost_usd,
			first_activity_at = EXCLUDED.first_activity_at,
			last_activity_at = EXCLUDED.last_activity_at,
			updated_at = NOW()
	`

	_, err := a.pool.Exec(ctx, query,
		taskID, tenantID,
		canonicalTags, keyThemes, summary,
		stats.SessionCount, stats.TotalRequests, stats.TotalCostUSD,
		stats.FirstActivityAt, stats.LastActivityAt,
	)

	if err != nil {
		return fmt.Errorf("exec failed: %w", err)
	}

	return nil
}

// EnsureTaskExists 确保任务记录存在
func (a *TaskAggregator) EnsureTaskExists(
	ctx context.Context,
	input TaskSummaryInput,
) error {
	if a.pool == nil {
		return fmt.Errorf("pool not configured")
	}

	query := `
		INSERT INTO tasks (
			task_id, tenant_id, project_ref,
			name, description, work_type,
			source, status,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', NOW(), NOW())
		ON CONFLICT (task_id) DO UPDATE SET
			name = COALESCE(EXCLUDED.name, tasks.name),
			description = COALESCE(EXCLUDED.description, tasks.description),
			work_type = COALESCE(EXCLUDED.work_type, tasks.work_type),
			project_ref = COALESCE(EXCLUDED.project_ref, tasks.project_ref),
			updated_at = NOW()
	`

	_, err := a.pool.Exec(ctx, query,
		input.TaskID, input.TenantID, nullString(input.ProjectRef),
		input.Name, nullString(input.Description), nullString(input.WorkType),
		input.Source,
	)

	if err != nil {
		return fmt.Errorf("ensure task exists failed: %w", err)
	}

	return nil
}

// nullString 将空字符串转换为 NULL
func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// deduplicateStrings 字符串去重
func deduplicateStrings(strs []string) []string {
	seen := make(map[string]bool)
	result := []string{}

	for _, s := range strs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}

	return result
}
