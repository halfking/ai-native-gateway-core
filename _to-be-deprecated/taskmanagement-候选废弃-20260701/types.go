// Package taskmanagement 实现任务分组和管理系统。
//
// 核心能力：
//   - 任务组管理：创建、查询、更新、删除任务组
//   - 任务分配：智能分配任务到合适的人员
//   - 负载均衡：多种负载均衡策略
//   - 统计监控：任务统计和负载监控
//
// 设计原则：
//   - 多租户隔离：所有操作都有租户上下文
//   - 灵活配置：支持多种分组规则和策略
//   - 可扩展：易于添加新的负载均衡策略
package taskmanagement

import (
	"time"
)

// GroupType 任务组类型
type GroupType string

const (
	// GroupTypeProject 项目组
	GroupTypeProject GroupType = "project"

	// GroupTypeDepartment 部门组
	GroupTypeDepartment GroupType = "department"

	// GroupTypeCustom 自定义组
	GroupTypeCustom GroupType = "custom"
)

// TaskGroup 任务组
type TaskGroup struct {
	// ID 任务组唯一标识
	ID string

	// Name 任务组名称
	Name string

	// Description 任务组描述
	Description string

	// TenantID 租户ID
	TenantID string

	// Type 任务组类型
	Type GroupType

	// Managers 管理员列表
	Managers []string

	// Members 成员列表
	Members []string

	// Rules 分组规则
	Rules GroupingRules

	// CreatedAt 创建时间
	CreatedAt time.Time

	// UpdatedAt 更新时间
	UpdatedAt time.Time
}

// GroupingRules 分组规则
type GroupingRules struct {
	// TenantFilter 租户过滤
	TenantFilter []string

	// ProjectFilter 项目过滤
	ProjectFilter []string

	// RiskLevelFilter 风险级别过滤
	RiskLevelFilter []string

	// KeywordMatch 关键词匹配
	KeywordMatch []string

	// CustomRules 自定义规则
	CustomRules map[string]any
}

// TaskType 任务类型
type TaskType string

const (
	// TaskTypeApproval 审批任务
	TaskTypeApproval TaskType = "approval"

	// TaskTypeReview 审查任务
	TaskTypeReview TaskType = "review"

	// TaskTypeMonitor 监控任务
	TaskTypeMonitor TaskType = "monitor"

	// TaskTypeAlert 告警任务
	TaskTypeAlert TaskType = "alert"
)

// TaskStatus 任务状态
type TaskStatus string

const (
	// TaskStatusPending 待处理
	TaskStatusPending TaskStatus = "pending"

	// TaskStatusInProgress 处理中
	TaskStatusInProgress TaskStatus = "in_progress"

	// TaskStatusCompleted 已完成
	TaskStatusCompleted TaskStatus = "completed"

	// TaskStatusCanceled 已取消
	TaskStatusCanceled TaskStatus = "canceled"
)

// Task 任务
type Task struct {
	// ID 任务唯一标识
	ID string

	// Type 任务类型
	Type TaskType

	// Status 任务状态
	Status TaskStatus

	// TenantID 租户ID
	TenantID string

	// SessionID 会话ID（可选）
	SessionID string

	// RequestID 请求ID（可选）
	RequestID string

	// Priority 优先级（数值越大优先级越高）
	Priority int

	// AssignedGroups 分配到的任务组ID列表
	AssignedGroups []string

	// Assignees 分配给的人员ID列表
	Assignees []string

	// Metadata 元数据
	Metadata map[string]any

	// CreatedAt 创建时间
	CreatedAt time.Time

	// UpdatedAt 更新时间
	UpdatedAt time.Time
}

// TaskAssignment 任务分配记录
type TaskAssignment struct {
	// ID 分配记录唯一标识
	ID string

	// TaskID 任务ID
	TaskID string

	// TaskType 任务类型
	TaskType TaskType

	// GroupID 任务组ID
	GroupID string

	// AssigneeID 分配给的用户ID
	AssigneeID string

	// AssigneeName 分配给的用户名称
	AssigneeName string

	// Status 任务状态
	Status TaskStatus

	// Priority 优先级
	Priority int

	// Metadata 元数据
	Metadata map[string]any

	// AssignedAt 分配时间
	AssignedAt time.Time

	// StartedAt 开始处理时间
	StartedAt *time.Time

	// CompletedAt 完成时间
	CompletedAt *time.Time

	// CreatedAt 创建时间
	CreatedAt time.Time

	// UpdatedAt 更新时间
	UpdatedAt time.Time
}

// TaskGroupFilter 任务组查询过滤器
type TaskGroupFilter struct {
	// TenantID 租户ID
	TenantID string

	// Type 任务组类型
	Type GroupType

	// Name 名称（模糊匹配）
	Name string

	// ManagerID 管理员ID
	ManagerID string

	// MemberID 成员ID
	MemberID string

	// Limit 返回数量限制
	Limit int

	// Offset 偏移量
	Offset int
}

// TaskAssignmentFilter 任务分配查询过滤器
type TaskAssignmentFilter struct {
	// TenantID 租户ID
	TenantID string

	// TaskID 任务ID
	TaskID string

	// TaskType 任务类型
	TaskType TaskType

	// GroupID 任务组ID
	GroupID string

	// AssigneeID 分配给的用户ID
	AssigneeID string

	// Status 任务状态
	Status TaskStatus

	// Limit 返回数量限制
	Limit int

	// Offset 偏移量
	Offset int
}

// TaskGroupStats 任务组统计
type TaskGroupStats struct {
	// GroupID 任务组ID
	GroupID string

	// TotalMembers 成员总数
	TotalMembers int

	// TotalTasks 任务总数
	TotalTasks int

	// PendingTasks 待处理任务数
	PendingTasks int

	// InProgressTasks 处理中任务数
	InProgressTasks int

	// CompletedTasks 已完成任务数
	CompletedTasks int

	// AvgCompletionTime 平均完成时间（秒）
	AvgCompletionTime float64

	// MemberLoad 成员负载统计
	MemberLoad map[string]int
}

// LoadBalanceStrategy 负载均衡策略
type LoadBalanceStrategy string

const (
	// StrategyRoundRobin 轮询
	StrategyRoundRobin LoadBalanceStrategy = "round_robin"

	// StrategyLeastTasks 最少任务
	StrategyLeastTasks LoadBalanceStrategy = "least_tasks"

	// StrategyWeighted 加权
	StrategyWeighted LoadBalanceStrategy = "weighted"

	// StrategyRandom 随机
	StrategyRandom LoadBalanceStrategy = "random"
)

// MemberInfo 成员信息
type MemberInfo struct {
	// ID 成员ID
	ID string

	// Name 成员名称
	Name string

	// Email 邮箱
	Email string

	// Weight 权重（用于加权负载均衡）
	Weight int

	// CurrentLoad 当前负载（待处理任务数）
	CurrentLoad int

	// MaxLoad 最大负载
	MaxLoad int

	// Available 是否可用
	Available bool
}
