// Package main (cmd/gateway) — system_monitor_adapter.go
//
// 适配器：把 *systemmonitor.SystemMonitor 包成 admin.systemMonitorBackend
// 接口实例。设计依据 docs/会话优化v2/32 §6.2：用结构投影代替跨包 import。
package main

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/bg/systemmonitor"
)

// systemMonitorAdapter 把 *systemmonitor.SystemMonitor 适配为 admin.systemMonitorBackend。
//
// 不转换任何字段（struct field 顺序与 admin.systemMonitorTask/QueueStats
// 严格一致），只是类型包装。
type systemMonitorAdapter struct {
	sm *systemmonitor.SystemMonitor
}

func newSystemMonitorAdapter(sm *systemmonitor.SystemMonitor) admin.SystemMonitorBackend {
	return &systemMonitorAdapter{sm: sm}
}

// Submit 把 admin 端的 systemMonitorTask 投影拷贝到 systemmonitor.Task。
func (a *systemMonitorAdapter) Submit(ctx context.Context, t *admin.SystemMonitorTask) (int64, error) {
	if a == nil || a.sm == nil {
		return 0, admin.ErrSystemMonitorDisabled
	}
	now := t.ScheduledAt
	if now.IsZero() {
		now = t.NextRunAt
	}
	task := &systemmonitor.Task{
		ID:           t.ID,
		TaskType:     systemmonitor.TaskType(t.TaskType),
		Automaticity: systemmonitor.Automaticity(t.Automaticity),
		Source:       systemmonitor.Source(t.Source),
		CredentialID: t.CredentialID,
		ProviderID:   t.ProviderID,
		RawModel:     t.RawModel,
		EnqueuedAt:   now,
		ScheduledAt:  t.ScheduledAt,
		NextRunAt:    t.NextRunAt,
		Attempt:      0,
		MaxAttempts:  t.MaxAttempts,
	}
	if err := task.Validate(); err != nil {
		return 0, err
	}
	return a.sm.Submit(ctx, task)
}

// QueueStats 把 systemmonitor.QueueStats 转回 admin 的快照类型。
func (a *systemMonitorAdapter) QueueStats(ctx context.Context) (admin.SystemMonitorQueueStats, error) {
	if a == nil || a.sm == nil {
		return admin.SystemMonitorQueueStats{}, nil
	}
	stats, err := a.sm.QueueStats(ctx)
	if err != nil {
		return admin.SystemMonitorQueueStats{}, err
	}
	return admin.SystemMonitorQueueStats{
		QueueSize:   stats.QueueSize,
		RunningSize: stats.RunningSize,
		InFallback:  stats.InFallback,
	}, nil
}

// IsFallback 透传。
func (a *systemMonitorAdapter) IsFallback() bool {
	if a == nil || a.sm == nil {
		return true
	}
	return a.sm.IsFallback()
}
