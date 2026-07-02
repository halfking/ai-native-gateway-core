package toolexecution

import (
	"context"
	"time"
)

// Store 工具执行记录的持久化接口。
//
// 设计目标：
//   - 与具体存储解耦（PostgreSQL / 内存 / Mock 均可实现）
//   - 追踪器 (Tracker) 与聚合器 (StatsAggregator) 都通过此接口访问数据
//   - 错误更新采用 updater 回调，避免读-改-写之间的并发竞争
type Store interface {
	// Save 插入一条新记录（通常由 RecordStart 调用）。
	// 期望 exec.ExecutionID 已由调用方生成；冲突时返回错误。
	Save(ctx context.Context, exec *ToolExecution) error

	// Get 按 execution_id 获取单条记录。记录不存在返回 ErrNotFound。
	Get(ctx context.Context, executionID string) (*ToolExecution, error)

	// Update 在 store 内部完成读-改-写流程，避免外部并发竞争。
	// updater 接收当前记录的指针，调用方修改字段即可。
	// 若 updater 返回非 nil 错误，store 应放弃此次更新并返回该错误。
	Update(ctx context.Context, executionID string, updater func(*ToolExecution) error) error

	// ListBySession 获取某会话下的所有工具执行（按 started_at 倒序）。
	ListBySession(ctx context.Context, sessionID string) ([]*ToolExecution, error)

	// ListByIdentity 获取某客户端最近 limit 条工具执行。
	// limit <= 0 时取默认值 100；limit > 1000 时上限 1000。
	ListByIdentity(ctx context.Context, identityHash string, limit int) ([]*ToolExecution, error)

	// ListByToolName 获取某工具在 [startTime, endTime) 区间的所有执行。
	// 用于每日聚合统计。
	ListByToolName(ctx context.Context, toolName string, startTime, endTime time.Time) ([]*ToolExecution, error)

	// ListByTenant 获取某租户在 [startTime, endTime) 区间内的所有执行记录。
	// 主要供运维 / 审计 / 后台管理使用。
	ListByTenant(ctx context.Context, tenantID string, startTime, endTime time.Time, limit int) ([]*ToolExecution, error)

	// SaveStats upsert 一条聚合统计（按 tool_name, date 唯一）。
	SaveStats(ctx context.Context, stats *ToolUsageStats) error

	// GetStats 获取某工具某天的统计。
	GetStats(ctx context.Context, toolName string, date time.Time) (*ToolUsageStats, error)

	// ListStats 按时间倒序获取统计列表（limit 上限 1000）。
	ListStats(ctx context.Context, toolName string, startTime, endTime time.Time, limit int) ([]*ToolUsageStats, error)

	// ListToolNamesWithActivity 获取 [startTime, endTime) 区间内有过调用的所有工具名。
	// 用于聚合时遍历所有工具。
	ListToolNamesWithActivity(ctx context.Context, startTime, endTime time.Time) ([]string, error)
}

// 存储层公共错误
//
// ErrNotFound 记录不存在。
// 其它错误（连接失败、约束冲突等）由具体实现透传。
var ErrNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "tool execution: not found" }

// IsNotFound 判断 err 是否为记录不存在错误。
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(errNotFound)
	return ok
}
