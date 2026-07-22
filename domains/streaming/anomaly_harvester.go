package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AnomalyHarvesterConfig controls harvester behavior.
type AnomalyHarvesterConfig struct {
	CleanupInterval  time.Duration
	BridgeInterval   time.Duration
	RetentionDays    int
	FaultEventMinCnt int
	FaultEventWindow time.Duration
}

func DefaultAnomalyHarvesterConfig() AnomalyHarvesterConfig {
	return AnomalyHarvesterConfig{
		CleanupInterval:  1 * time.Hour,
		BridgeInterval:   5 * time.Minute,
		RetentionDays:    7,
		FaultEventMinCnt: 5,
		FaultEventWindow: 15 * time.Minute,
	}
}

// AnomalyHarvester runs background tasks: TTL cleanup + fault-event bridge.
type AnomalyHarvester struct {
	pool   *pgxpool.Pool
	cfg    AnomalyHarvesterConfig
	stopCh chan struct{}
	wg     sync.WaitGroup
}

func NewAnomalyHarvester(pool *pgxpool.Pool, cfg AnomalyHarvesterConfig) *AnomalyHarvester {
	return &AnomalyHarvester{
		pool:   pool,
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
}

func (h *AnomalyHarvester) Start() {
	if h == nil || h.pool == nil {
		return
	}
	h.wg.Add(2)
	go h.cleanupLoop()
	go h.bridgeLoop()
	slog.Info("anomaly harvester started",
		"retention_days", h.cfg.RetentionDays,
		"bridge_interval", h.cfg.BridgeInterval)
}

func (h *AnomalyHarvester) Stop() {
	if h == nil {
		return
	}
	close(h.stopCh)
	h.wg.Wait()
	slog.Info("anomaly harvester stopped")
}

func (h *AnomalyHarvester) cleanupLoop() {
	defer h.wg.Done()
	ticker := time.NewTicker(h.cfg.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stopCh:
			return
		case <-ticker.C:
			h.runCleanup()
		}
	}
}

func (h *AnomalyHarvester) runCleanup() {
	cutoff := time.Now().AddDate(0, 0, -h.cfg.RetentionDays)
	ct, err := h.pool.Exec(context.WithoutCancel(context.Background()),
		`DELETE FROM response_format_anomalies WHERE detected_at < $1`, cutoff)
	if err != nil {
		slog.Warn("anomaly harvester: cleanup failed", "error", err)
		return
	}
	deleted := ct.RowsAffected()
	if deleted > 0 {
		slog.Info("anomaly harvester: cleaned old anomalies",
			"deleted", deleted, "cutoff", cutoff.Format(time.DateOnly))
	}
}

func (h *AnomalyHarvester) bridgeLoop() {
	defer h.wg.Done()
	ticker := time.NewTicker(h.cfg.BridgeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stopCh:
			return
		case <-ticker.C:
			h.runBridge()
		}
	}
}

func (h *AnomalyHarvester) runBridge() {
	alerts := h.queryAnomalyAlerts()
	if len(alerts) == 0 {
		return
	}
	for _, a := range alerts {
		h.createFaultEvent(a)
	}
}

type anomalyAlert struct {
	anomalyType string
	severity    string
	count       int
}

// queryAnomalyAlerts selects anomaly types that have either hit the configured
// count threshold OR are critical (which always trigger regardless of count).
func (h *AnomalyHarvester) queryAnomalyAlerts() []anomalyAlert {
	windowStart := time.Now().Add(-h.cfg.FaultEventWindow)
	rows, err := h.pool.Query(context.WithoutCancel(context.Background()), `
		SELECT anomaly_type, severity, COUNT(*) AS cnt
		FROM response_format_anomalies
		WHERE detected_at > $1 AND resolved = FALSE
		GROUP BY anomaly_type, severity
		HAVING COUNT(*) >= $2 OR severity = 'critical'
		ORDER BY cnt DESC
	`, windowStart, h.cfg.FaultEventMinCnt)
	if err != nil {
		slog.Warn("anomaly harvester: bridge query failed", "error", err)
		return nil
	}
	defer rows.Close()

	var alerts []anomalyAlert
	for rows.Next() {
		var at, sev string
		var cnt int
		if err := rows.Scan(&at, &sev, &cnt); err != nil {
			continue
		}
		if sev == "critical" || sev == "high" {
			alerts = append(alerts, anomalyAlert{at, sev, cnt})
		}
	}
	return alerts
}

// toFaultSeverity maps anomaly severity → fault_events severity.
// fault_events CHECK: ('info', 'warning', 'error', 'critical')
func toFaultSeverity(anomalySev string) string {
	switch anomalySev {
	case "critical":
		return "critical"
	case "high":
		return "error"
	case "medium":
		return "warning"
	case "low":
		return "info"
	default:
		return "warning"
	}
}

func (h *AnomalyHarvester) createFaultEvent(a anomalyAlert) {
	meta := map[string]any{
		"anomaly_type": a.anomalyType,
		"count":        a.count,
		"window_min":   h.cfg.FaultEventWindow.Minutes(),
		"source":       "anomaly_harvester",
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		slog.Warn("anomaly harvester: metadata marshal failed",
			"anomaly_type", a.anomalyType, "error", err)
		metaJSON = []byte(`{}`)
	}

	faultSev := toFaultSeverity(a.severity)
	title := fmt.Sprintf("数据异常: %s (%d次/%dmin)", a.anomalyType, a.count, int(h.cfg.FaultEventWindow.Minutes()))
	description := fmt.Sprintf("异常类型 %s 在过去 %d 分钟内出现 %d 次(严重度: %s)，触发故障事件",
		a.anomalyType, int(h.cfg.FaultEventWindow.Minutes()), a.count, a.severity)

	_, err = h.pool.Exec(context.WithoutCancel(context.Background()), `
		INSERT INTO fault_events (rule_id, rule_name, severity, title, description, source,
		                          status, metadata, detected_at, created_at)
		VALUES (0, $1, $2, $3, $4, 'data_anomaly', 'new', $5, NOW(), NOW())
	`, title, faultSev, title, description, metaJSON)
	if err != nil {
		slog.Warn("anomaly harvester: bridge insert fault_event failed",
			"anomaly_type", a.anomalyType, "error", err)
		return
	}
	slog.Warn("anomaly harvester: bridged to fault_event",
		"anomaly_type", a.anomalyType, "count", a.count,
		"anomaly_severity", a.severity, "fault_severity", faultSev)
}
