// Package main - tool_execution_integration.go
//
// 工具执行追踪集成
// 将 ToolExecution 域（domains/toolexecution）和 Hook（domains/hooks/toolexecution）
// 集成到 Gateway 主流程。
//
// 功能：
// 1. 初始化 PostgresStore（连接到 tool_executions / tool_usage_stats 表）
// 2. 创建 Tracker（记录工具执行生命周期）
// 3. 创建 StatsAggregator（每日统计聚合）
// 4. 创建并注册 Hook 到 Pipeline（在 PhasePostUpstream 阶段追踪）

package main

import (
	"database/sql"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/toolexecution"
	te "github.com/kaixuan/llm-gateway-go/domains/toolexecution"
)

// ToolExecutionTrackingComponents 工具执行追踪的所有组件。
type ToolExecutionTrackingComponents struct {
	Store      te.Store
	Tracker    *te.Tracker
	Aggregator *te.StatsAggregator
	Hook       *toolexecution.Hook
}

// InitializeToolExecutionTracking 初始化工具执行追踪。
func InitializeToolExecutionTracking(db *sql.DB, logger *slog.Logger) (*ToolExecutionTrackingComponents, error) {
	if db == nil {
		return nil, &toolExecutionError{msg: "tool execution: database connection is nil"}
	}
	if logger == nil {
		logger = slog.Default()
	}

	store := te.NewPostgresStore(db, logger)
	tracker := te.NewTracker(store, logger)
	aggregator := te.NewStatsAggregator(store, logger)
	hook := toolexecution.NewHook(tracker, logger)

	logger.Info("tool execution tracking initialized",
		"store", "postgres",
		"hook", hook.Name(),
		"priority", hook.Priority(),
	)

	return &ToolExecutionTrackingComponents{
		Store:      store,
		Tracker:    tracker,
		Aggregator: aggregator,
		Hook:       hook,
	}, nil
}

type toolExecutionError struct{ msg string }

func (e *toolExecutionError) Error() string { return e.msg }

// ValidateToolExecutionTracking 验证工具执行追踪组件的完整性。
func ValidateToolExecutionTracking(components *ToolExecutionTrackingComponents) error {
	if components == nil {
		return &toolExecutionError{msg: "components is nil"}
	}
	if components.Store == nil {
		return &toolExecutionError{msg: "Store is nil"}
	}
	if components.Tracker == nil {
		return &toolExecutionError{msg: "Tracker is nil"}
	}
	if components.Aggregator == nil {
		return &toolExecutionError{msg: "Aggregator is nil"}
	}
	if components.Hook == nil {
		return &toolExecutionError{msg: "Hook is nil"}
	}
	if components.Hook.Name() == "" {
		return &toolExecutionError{msg: "Hook.Name() is empty"}
	}
	if components.Hook.Priority() < 0 {
		return &toolExecutionError{msg: "Hook.Priority() is negative"}
	}
	return nil
}
