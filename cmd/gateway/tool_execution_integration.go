// Package main - tool_execution_integration.go
//
// 工具执行追踪集成

package main

import (
	"database/sql"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/toolexecution"
	te "github.com/kaixuan/llm-gateway-go/domains/toolexecution"
)

type ToolExecutionTrackingComponents struct {
	Store      te.Store
	Tracker    *te.Tracker
	Aggregator *te.StatsAggregator
	Hook       *toolexecution.Hook
}

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

	return &ToolExecutionTrackingComponents{
		Store:      store,
		Tracker:    tracker,
		Aggregator: aggregator,
		Hook:       hook,
	}, nil
}

type toolExecutionError struct{ msg string }

func (e *toolExecutionError) Error() string { return e.msg }

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
	return nil
}
