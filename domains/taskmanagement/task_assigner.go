package taskmanagement

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LoadBalancer 负载均衡器接口
type LoadBalancer interface {
	// Select 从成员列表中选择一个成员
	Select(members []MemberInfo, task *Task) (*MemberInfo, error)
}

// RoundRobinBalancer 轮询负载均衡器
type RoundRobinBalancer struct {
	counters map[string]int
	mu       sync.Mutex
}

// NewRoundRobinBalancer 创建轮询负载均衡器
func NewRoundRobinBalancer() *RoundRobinBalancer {
	return &RoundRobinBalancer{
		counters: make(map[string]int),
	}
}

// Select 轮询选择成员
func (b *RoundRobinBalancer) Select(members []MemberInfo, task *Task) (*MemberInfo, error) {
	if len(members) == 0 {
		return nil, fmt.Errorf("no available members")
	}

	// 过滤可用成员
	available := make([]MemberInfo, 0)
	for _, member := range members {
		if member.Available && (member.MaxLoad == 0 || member.CurrentLoad < member.MaxLoad) {
			available = append(available, member)
		}
	}

	if len(available) == 0 {
		return nil, fmt.Errorf("no available members with capacity")
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// 使用任务组ID作为计数器key
	key := task.TenantID
	counter := b.counters[key]

	// 选择成员
	selected := &available[counter%len(available)]

	// 更新计数器
	b.counters[key] = (counter + 1) % len(available)

	return selected, nil
}

// LeastTasksBalancer 最少任务负载均衡器
type LeastTasksBalancer struct{}

// NewLeastTasksBalancer 创建最少任务负载均衡器
func NewLeastTasksBalancer() *LeastTasksBalancer {
	return &LeastTasksBalancer{}
}

// Select 选择当前任务最少的成员
func (b *LeastTasksBalancer) Select(members []MemberInfo, task *Task) (*MemberInfo, error) {
	if len(members) == 0 {
		return nil, fmt.Errorf("no available members")
	}

	var selected *MemberInfo
	minLoad := -1

	for i := range members {
		if !members[i].Available {
			continue
		}

		if members[i].MaxLoad > 0 && members[i].CurrentLoad >= members[i].MaxLoad {
			continue
		}

		if minLoad == -1 || members[i].CurrentLoad < minLoad {
			minLoad = members[i].CurrentLoad
			selected = &members[i]
		}
	}

	if selected == nil {
		return nil, fmt.Errorf("no available members with capacity")
	}

	return selected, nil
}

// WeightedBalancer 加权负载均衡器
type WeightedBalancer struct{}

// NewWeightedBalancer 创建加权负载均衡器
func NewWeightedBalancer() *WeightedBalancer {
	return &WeightedBalancer{}
}

// Select 根据权重选择成员
func (b *WeightedBalancer) Select(members []MemberInfo, task *Task) (*MemberInfo, error) {
	if len(members) == 0 {
		return nil, fmt.Errorf("no available members")
	}

	// 过滤可用成员并计算总权重
	available := make([]MemberInfo, 0)
	totalWeight := 0

	for _, member := range members {
		if !member.Available {
			continue
		}

		if member.MaxLoad > 0 && member.CurrentLoad >= member.MaxLoad {
			continue
		}

		weight := member.Weight
		if weight <= 0 {
			weight = 1
		}

		available = append(available, member)
		totalWeight += weight
	}

	if len(available) == 0 {
		return nil, fmt.Errorf("no available members with capacity")
	}

	// 随机选择（基于权重）
	random := rand.Intn(totalWeight)
	accumulated := 0

	for i := range available {
		weight := available[i].Weight
		if weight <= 0 {
			weight = 1
		}

		accumulated += weight
		if random < accumulated {
			return &available[i], nil
		}
	}

	// 默认返回第一个
	return &available[0], nil
}

// RandomBalancer 随机负载均衡器
type RandomBalancer struct{}

// NewRandomBalancer 创建随机负载均衡器
func NewRandomBalancer() *RandomBalancer {
	return &RandomBalancer{}
}

// Select 随机选择成员
func (b *RandomBalancer) Select(members []MemberInfo, task *Task) (*MemberInfo, error) {
	if len(members) == 0 {
		return nil, fmt.Errorf("no available members")
	}

	// 过滤可用成员
	available := make([]MemberInfo, 0)
	for _, member := range members {
		if member.Available && (member.MaxLoad == 0 || member.CurrentLoad < member.MaxLoad) {
			available = append(available, member)
		}
	}

	if len(available) == 0 {
		return nil, fmt.Errorf("no available members with capacity")
	}

	// 随机选择
	idx := rand.Intn(len(available))
	return &available[idx], nil
}

// TaskAssigner 任务分配器
//
// 职责：
//   - 根据规则路由任务到任务组
//   - 使用负载均衡策略分配任务给成员
//   - 记录任务分配
type TaskAssigner struct {
	groupManager *TaskGroupManager
	pool         *pgxpool.Pool
	balancer     LoadBalancer
}

// NewTaskAssigner 创建任务分配器
func NewTaskAssigner(groupManager *TaskGroupManager, pool *pgxpool.Pool, balancer LoadBalancer) *TaskAssigner {
	if balancer == nil {
		balancer = NewLeastTasksBalancer() // 默认使用最少任务策略
	}

	return &TaskAssigner{
		groupManager: groupManager,
		pool:         pool,
		balancer:     balancer,
	}
}

// Assign 分配任务
func (a *TaskAssigner) Assign(ctx context.Context, task *Task) error {
	if task == nil {
		return fmt.Errorf("task is nil")
	}

	// 1. 查找匹配的任务组
	groups, err := a.findMatchingGroups(ctx, task)
	if err != nil {
		return fmt.Errorf("failed to find matching groups: %w", err)
	}

	if len(groups) == 0 {
		return fmt.Errorf("no matching groups found for task")
	}

	// 2. 对每个组进行任务分配
	for _, group := range groups {
		// 获取成员信息
		members, err := a.getMemberInfo(ctx, group)
		if err != nil {
			slog.Error("failed to get member info", "group_id", group.ID, "error", err)
			continue
		}

		if len(members) == 0 {
			slog.Warn("no members in group", "group_id", group.ID)
			continue
		}

		// 使用负载均衡策略选择成员
		selected, err := a.balancer.Select(members, task)
		if err != nil {
			slog.Error("failed to select member", "group_id", group.ID, "error", err)
			continue
		}

		// 创建任务分配记录
		assignment := &TaskAssignment{
			ID:           uuid.New().String(),
			TaskID:       task.ID,
			TaskType:     task.Type,
			GroupID:      group.ID,
			AssigneeID:   selected.ID,
			AssigneeName: selected.Name,
			Status:       TaskStatusPending,
			Priority:     task.Priority,
			Metadata:     task.Metadata,
			AssignedAt:   time.Now(),
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}

		// 保存分配记录
		if err := a.saveAssignment(ctx, assignment); err != nil {
			slog.Error("failed to save assignment", "group_id", group.ID, "error", err)
			continue
		}

		// 更新任务
		task.AssignedGroups = append(task.AssignedGroups, group.ID)
		task.Assignees = append(task.Assignees, selected.ID)

		slog.Info("task assigned",
			"task_id", task.ID,
			"group_id", group.ID,
			"assignee", selected.Name)
	}

	if len(task.Assignees) == 0 {
		return fmt.Errorf("failed to assign task to any member")
	}

	return nil
}

// findMatchingGroups 查找匹配的任务组
func (a *TaskAssigner) findMatchingGroups(ctx context.Context, task *Task) ([]*TaskGroup, error) {
	// 查询租户的所有任务组
	filter := &TaskGroupFilter{
		TenantID: task.TenantID,
		Limit:    100,
	}

	allGroups, err := a.groupManager.List(ctx, filter)
	if err != nil {
		return nil, err
	}

	// 过滤匹配的任务组
	matched := make([]*TaskGroup, 0)
	for _, group := range allGroups {
		if a.matchRules(task, &group.Rules) {
			matched = append(matched, group)
		}
	}

	return matched, nil
}

// matchRules 检查任务是否匹配分组规则
func (a *TaskAssigner) matchRules(task *Task, rules *GroupingRules) bool {
	// 租户过滤
	if len(rules.TenantFilter) > 0 {
		matched := false
		for _, tenant := range rules.TenantFilter {
			if tenant == task.TenantID {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// 关键词匹配（简化实现）
	// 实际应用中可以从task.Metadata中提取关键词进行匹配

	return true
}

// getMemberInfo 获取成员信息
func (a *TaskAssigner) getMemberInfo(ctx context.Context, group *TaskGroup) ([]MemberInfo, error) {
	members := make([]MemberInfo, 0, len(group.Members))

	for _, memberID := range group.Members {
		// 查询成员当前负载
		currentLoad, err := a.getMemberLoad(ctx, memberID)
		if err != nil {
			slog.Warn("failed to get member load", "member_id", memberID, "error", err)
			currentLoad = 0
		}

		members = append(members, MemberInfo{
			ID:          memberID,
			Name:        memberID, // 实际应该从用户表查询
			Weight:      1,
			CurrentLoad: currentLoad,
			MaxLoad:     100, // 可配置
			Available:   true,
		})
	}

	return members, nil
}

// getMemberLoad 获取成员当前负载（待处理任务数）
func (a *TaskAssigner) getMemberLoad(ctx context.Context, memberID string) (int, error) {
	query := `
		SELECT COUNT(*)
		FROM task_assignments
		WHERE assignee_id = $1 AND status IN ($2, $3)
	`

	var count int
	err := a.pool.QueryRow(ctx, query, memberID, TaskStatusPending, TaskStatusInProgress).Scan(&count)
	if err != nil {
		return 0, err
	}

	return count, nil
}

// saveAssignment 保存任务分配记录
func (a *TaskAssigner) saveAssignment(ctx context.Context, assignment *TaskAssignment) error {
	metadataJSON := []byte("{}")
	if assignment.Metadata != nil {
		var err error
		metadataJSON, err = json.Marshal(assignment.Metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal metadata: %w", err)
		}
	}

	query := `
		INSERT INTO task_assignments (
			id, task_id, task_type, group_id, assignee_id, assignee_name,
			status, priority, metadata, assigned_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`

	_, err := a.pool.Exec(ctx, query,
		assignment.ID, assignment.TaskID, assignment.TaskType, assignment.GroupID,
		assignment.AssigneeID, assignment.AssigneeName, assignment.Status, assignment.Priority,
		metadataJSON, assignment.AssignedAt, assignment.CreatedAt, assignment.UpdatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to save assignment: %w", err)
	}

	return nil
}

// UpdateAssignmentStatus 更新任务分配状态
func (a *TaskAssigner) UpdateAssignmentStatus(ctx context.Context, assignmentID string, status TaskStatus) error {
	now := time.Now()

	query := `
		UPDATE task_assignments
		SET status = $1, updated_at = $2
	`

	args := []any{status, now}
	argIdx := 3

	// 根据状态设置相应的时间字段
	if status == TaskStatusInProgress {
		query += fmt.Sprintf(", started_at = $%d", argIdx)
		args = append(args, now)
		argIdx++
	} else if status == TaskStatusCompleted || status == TaskStatusCanceled {
		query += fmt.Sprintf(", completed_at = $%d", argIdx)
		args = append(args, now)
		argIdx++
	}

	query += fmt.Sprintf(" WHERE id = $%d", argIdx)
	args = append(args, assignmentID)

	result, err := a.pool.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to update assignment status: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("assignment not found: %s", assignmentID)
	}

	return nil
}
