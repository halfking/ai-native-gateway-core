package taskmanagement

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TaskGroupManager 任务组管理器
//
// 职责：
//   - 任务组的创建、查询、更新、删除
//   - 成员管理
//   - 多租户隔离
type TaskGroupManager struct {
	pool *pgxpool.Pool
}

// NewTaskGroupManager 创建任务组管理器
func NewTaskGroupManager(pool *pgxpool.Pool) *TaskGroupManager {
	return &TaskGroupManager{pool: pool}
}

// Create 创建任务组
func (m *TaskGroupManager) Create(ctx context.Context, group *TaskGroup) error {
	if group == nil {
		return fmt.Errorf("task group is nil")
	}

	if group.TenantID == "" {
		return fmt.Errorf("tenant_id is required")
	}

	if group.Name == "" {
		return fmt.Errorf("name is required")
	}

	// 生成ID
	if group.ID == "" {
		group.ID = uuid.New().String()
	}

	// 序列化JSON字段
	managersJSON, err := json.Marshal(group.Managers)
	if err != nil {
		return fmt.Errorf("failed to marshal managers: %w", err)
	}

	membersJSON, err := json.Marshal(group.Members)
	if err != nil {
		return fmt.Errorf("failed to marshal members: %w", err)
	}

	rulesJSON, err := json.Marshal(group.Rules)
	if err != nil {
		return fmt.Errorf("failed to marshal rules: %w", err)
	}

	// 设置时间戳
	now := time.Now()
	group.CreatedAt = now
	group.UpdatedAt = now

	// 插入数据库
	query := `
		INSERT INTO task_groups (
			id, name, description, tenant_id, type,
			managers, members, rules, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`

	_, err = m.pool.Exec(ctx, query,
		group.ID, group.Name, group.Description, group.TenantID, group.Type,
		managersJSON, membersJSON, rulesJSON, group.CreatedAt, group.UpdatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to create task group: %w", err)
	}

	return nil
}

// Get 获取任务组
func (m *TaskGroupManager) Get(ctx context.Context, groupID, tenantID string) (*TaskGroup, error) {
	query := `
		SELECT id, name, description, tenant_id, type,
		       managers, members, rules, created_at, updated_at
		FROM task_groups
		WHERE id = $1
	`

	var group TaskGroup
	var managersJSON, membersJSON, rulesJSON []byte

	err := m.pool.QueryRow(ctx, query, groupID).Scan(
		&group.ID, &group.Name, &group.Description, &group.TenantID, &group.Type,
		&managersJSON, &membersJSON, &rulesJSON, &group.CreatedAt, &group.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("task group not found: %s", groupID)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get task group: %w", err)
	}

	// 租户隔离检查
	if tenantID != "" && group.TenantID != tenantID {
		return nil, fmt.Errorf("task group does not belong to tenant: %s", tenantID)
	}

	// 反序列化JSON字段
	if err := json.Unmarshal(managersJSON, &group.Managers); err != nil {
		return nil, fmt.Errorf("failed to unmarshal managers: %w", err)
	}

	if err := json.Unmarshal(membersJSON, &group.Members); err != nil {
		return nil, fmt.Errorf("failed to unmarshal members: %w", err)
	}

	if err := json.Unmarshal(rulesJSON, &group.Rules); err != nil {
		return nil, fmt.Errorf("failed to unmarshal rules: %w", err)
	}

	return &group, nil
}

// List 列表查询任务组
func (m *TaskGroupManager) List(ctx context.Context, filter *TaskGroupFilter) ([]*TaskGroup, error) {
	if filter == nil {
		filter = &TaskGroupFilter{Limit: 50}
	}

	// 设置默认值
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}

	if filter.Offset < 0 {
		filter.Offset = 0
	}

	// 构建查询
	query := `
		SELECT id, name, description, tenant_id, type,
		       managers, members, rules, created_at, updated_at
		FROM task_groups
		WHERE 1=1
	`

	args := make([]any, 0)
	argIdx := 1

	// 添加过滤条件
	if filter.TenantID != "" {
		query += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
		args = append(args, filter.TenantID)
		argIdx++
	}

	if filter.Type != "" {
		query += fmt.Sprintf(" AND type = $%d", argIdx)
		args = append(args, filter.Type)
		argIdx++
	}

	if filter.Name != "" {
		query += fmt.Sprintf(" AND name ILIKE $%d", argIdx)
		args = append(args, "%"+filter.Name+"%")
		argIdx++
	}

	// 排序和分页
	query += " ORDER BY created_at DESC"
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, filter.Limit, filter.Offset)

	// 执行查询
	rows, err := m.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list task groups: %w", err)
	}
	defer rows.Close()

	// 解析结果
	groups := make([]*TaskGroup, 0)
	for rows.Next() {
		var group TaskGroup
		var managersJSON, membersJSON, rulesJSON []byte

		if err := rows.Scan(
			&group.ID, &group.Name, &group.Description, &group.TenantID, &group.Type,
			&managersJSON, &membersJSON, &rulesJSON, &group.CreatedAt, &group.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan task group: %w", err)
		}

		// 反序列化JSON字段
		if err := json.Unmarshal(managersJSON, &group.Managers); err != nil {
			continue // 跳过损坏的记录
		}

		if err := json.Unmarshal(membersJSON, &group.Members); err != nil {
			continue
		}

		if err := json.Unmarshal(rulesJSON, &group.Rules); err != nil {
			continue
		}

		groups = append(groups, &group)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate task groups: %w", err)
	}

	return groups, nil
}

// Update 更新任务组
func (m *TaskGroupManager) Update(ctx context.Context, group *TaskGroup) error {
	if group == nil {
		return fmt.Errorf("task group is nil")
	}

	if group.ID == "" {
		return fmt.Errorf("id is required")
	}

	// 序列化JSON字段
	managersJSON, err := json.Marshal(group.Managers)
	if err != nil {
		return fmt.Errorf("failed to marshal managers: %w", err)
	}

	membersJSON, err := json.Marshal(group.Members)
	if err != nil {
		return fmt.Errorf("failed to marshal members: %w", err)
	}

	rulesJSON, err := json.Marshal(group.Rules)
	if err != nil {
		return fmt.Errorf("failed to marshal rules: %w", err)
	}

	// 更新时间戳
	group.UpdatedAt = time.Now()

	// 更新数据库
	query := `
		UPDATE task_groups
		SET name = $1, description = $2, type = $3,
		    managers = $4, members = $5, rules = $6, updated_at = $7
		WHERE id = $8 AND tenant_id = $9
	`

	result, err := m.pool.Exec(ctx, query,
		group.Name, group.Description, group.Type,
		managersJSON, membersJSON, rulesJSON, group.UpdatedAt,
		group.ID, group.TenantID,
	)

	if err != nil {
		return fmt.Errorf("failed to update task group: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("task group not found or tenant mismatch: %s", group.ID)
	}

	return nil
}

// Delete 删除任务组
func (m *TaskGroupManager) Delete(ctx context.Context, groupID, tenantID string) error {
	query := `
		DELETE FROM task_groups
		WHERE id = $1 AND tenant_id = $2
	`

	result, err := m.pool.Exec(ctx, query, groupID, tenantID)
	if err != nil {
		return fmt.Errorf("failed to delete task group: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("task group not found or tenant mismatch: %s", groupID)
	}

	return nil
}

// AddMember 添加成员
func (m *TaskGroupManager) AddMember(ctx context.Context, groupID, tenantID, memberID string) error {
	// 获取任务组
	group, err := m.Get(ctx, groupID, tenantID)
	if err != nil {
		return err
	}

	// 检查成员是否已存在
	for _, member := range group.Members {
		if member == memberID {
			return nil // 已存在，不需要添加
		}
	}

	// 添加成员
	group.Members = append(group.Members, memberID)

	// 更新任务组
	return m.Update(ctx, group)
}

// RemoveMember 移除成员
func (m *TaskGroupManager) RemoveMember(ctx context.Context, groupID, tenantID, memberID string) error {
	// 获取任务组
	group, err := m.Get(ctx, groupID, tenantID)
	if err != nil {
		return err
	}

	// 移除成员
	newMembers := make([]string, 0)
	for _, member := range group.Members {
		if member != memberID {
			newMembers = append(newMembers, member)
		}
	}

	group.Members = newMembers

	// 更新任务组
	return m.Update(ctx, group)
}

// AddManager 添加管理员
func (m *TaskGroupManager) AddManager(ctx context.Context, groupID, tenantID, managerID string) error {
	// 获取任务组
	group, err := m.Get(ctx, groupID, tenantID)
	if err != nil {
		return err
	}

	// 检查管理员是否已存在
	for _, manager := range group.Managers {
		if manager == managerID {
			return nil // 已存在
		}
	}

	// 添加管理员
	group.Managers = append(group.Managers, managerID)

	// 更新任务组
	return m.Update(ctx, group)
}

// RemoveManager 移除管理员
func (m *TaskGroupManager) RemoveManager(ctx context.Context, groupID, tenantID, managerID string) error {
	// 获取任务组
	group, err := m.Get(ctx, groupID, tenantID)
	if err != nil {
		return err
	}

	// 移除管理员
	newManagers := make([]string, 0)
	for _, manager := range group.Managers {
		if manager != managerID {
			newManagers = append(newManagers, manager)
		}
	}

	group.Managers = newManagers

	// 更新任务组
	return m.Update(ctx, group)
}

// Count 统计任务组数量
func (m *TaskGroupManager) Count(ctx context.Context, tenantID string) (int, error) {
	query := `SELECT COUNT(*) FROM task_groups WHERE tenant_id = $1`

	var count int
	err := m.pool.QueryRow(ctx, query, tenantID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count task groups: %w", err)
	}

	return count, nil
}
