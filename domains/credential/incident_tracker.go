// Package credential - Incident tracker.
//
// Periodically scans every (provider, model) pair in the store, asks the
// ReputationAnalyzer for anomalies, and persists matching Incidents with
// dedupe to avoid spamming the same outage.
//
// Run it after ReputationWorker.Run() so today's metrics are fresh; the
// tracker itself reads from the same store and is order-independent.
package credential

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// IncidentTracker 事件追踪器
type IncidentTracker struct {
	store        ReputationStore
	analyzer     *ReputationAnalyzer
	logger       *slog.Logger
	dedupeWindow time.Duration // 同 (provider, model, type) 在窗口内已存在未解决事件时跳过
	now          func() time.Time

	// scanWindowDays: 检测异常时使用的时间窗口（默认 7）
	scanWindowDays int
}

// IncidentTrackerConfig 配置
type IncidentTrackerConfig struct {
	Store          ReputationStore
	Analyzer       *ReputationAnalyzer
	Logger         *slog.Logger
	DedupeWindow   time.Duration // 默认 1h
	ScanWindowDays int           // 默认 7
}

// NewIncidentTracker 创建追踪器
func NewIncidentTracker(cfg IncidentTrackerConfig) *IncidentTracker {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.DedupeWindow <= 0 {
		cfg.DedupeWindow = 1 * time.Hour
	}
	if cfg.ScanWindowDays <= 0 {
		cfg.ScanWindowDays = 7
	}
	return &IncidentTracker{
		store:          cfg.Store,
		analyzer:       cfg.Analyzer,
		logger:         cfg.Logger,
		dedupeWindow:   cfg.DedupeWindow,
		scanWindowDays: cfg.ScanWindowDays,
		now:            time.Now,
	}
}

// CheckAndRecordIncidents 扫描所有已知 (provider, model) 并写入事件
func (t *IncidentTracker) CheckAndRecordIncidents(ctx context.Context) error {
	if t.store == nil {
		return fmt.Errorf("credential: incident_tracker store is nil")
	}
	if t.analyzer == nil {
		return fmt.Errorf("credential: incident_tracker analyzer is nil")
	}

	pairs, err := t.store.ListProviderModelPairs(ctx)
	if err != nil {
		return fmt.Errorf("incident_tracker: list pairs: %w", err)
	}

	var created, deduped, failed int
	for _, p := range pairs {
		if err := ctx.Err(); err != nil {
			return err
		}
		anomalies, err := t.analyzer.DetectAnomaliesFor(ctx, p.ProviderID, p.Model, t.scanWindowDays)
		if err != nil {
			t.logger.Warn("incident_tracker: detect anomalies failed",
				"provider", p.ProviderID, "model", p.Model, "error", err)
			failed++
			continue
		}
		for _, a := range anomalies {
			incident := t.anomalyToIncident(p.ProviderID, p.Model, a)
			ok, err := t.store.RecordIncidentIfNotExists(ctx, incident, t.dedupeWindow)
			if err != nil {
				t.logger.Error("incident_tracker: record incident failed",
					"provider", p.ProviderID, "model", p.Model, "error", err)
				failed++
				continue
			}
			if ok {
				created++
			} else {
				deduped++
			}
		}
	}

	t.logger.Info("incident_tracker: scan complete",
		"pairs", len(pairs), "created", created, "deduped", deduped, "failed", failed)
	return nil
}

// RecordManualIncident 手动记录事件（外部触发，如 webhook / alertmanager）
func (t *IncidentTracker) RecordManualIncident(ctx context.Context, incident *Incident) (bool, error) {
	if t.store == nil {
		return false, fmt.Errorf("credential: incident_tracker store is nil")
	}
	if incident == nil {
		return false, fmt.Errorf("credential: nil incident")
	}
	if incident.StartedAt.IsZero() {
		incident.StartedAt = t.now()
	}
	return t.store.RecordIncidentIfNotExists(ctx, incident, t.dedupeWindow)
}

// anomalyToIncident 把 Anomaly 转换为 Incident
func (t *IncidentTracker) anomalyToIncident(providerID, model string, a Anomaly) *Incident {
	incidentType := IncidentDegradedPerformance
	switch a.Type {
	case AnomalyErrorRateSpike:
		incidentType = IncidentErrorRateSpike
	case AnomalyLatencySpike:
		incidentType = IncidentLatencySpike
	case AnomalySuccessDrop:
		incidentType = IncidentDegradedPerformance
	}
	return &Incident{
		ProviderID:  providerID,
		Model:       model,
		Type:        incidentType,
		Impact:      a.Severity,
		Description: a.Message,
		StartedAt:   a.Date,
		Resolved:    false,
	}
}
