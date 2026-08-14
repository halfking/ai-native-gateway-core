package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
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
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	lifecycleMu sync.Mutex
	started     bool
	stopped     bool
}

func NewAnomalyHarvester(pool *pgxpool.Pool, cfg AnomalyHarvesterConfig) *AnomalyHarvester {
	ctx, cancel := context.WithCancel(context.Background())
	defaults := DefaultAnomalyHarvesterConfig()
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = defaults.CleanupInterval
	}
	if cfg.BridgeInterval <= 0 {
		cfg.BridgeInterval = defaults.BridgeInterval
	}
	if cfg.RetentionDays <= 0 {
		cfg.RetentionDays = defaults.RetentionDays
	}
	if cfg.FaultEventMinCnt <= 0 {
		cfg.FaultEventMinCnt = defaults.FaultEventMinCnt
	}
	if cfg.FaultEventWindow <= 0 {
		cfg.FaultEventWindow = defaults.FaultEventWindow
	}
	return &AnomalyHarvester{
		pool:   pool,
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}
}

func (h *AnomalyHarvester) Start() {
	if h == nil || h.pool == nil {
		return
	}
	h.lifecycleMu.Lock()
	if h.started || h.stopped {
		h.lifecycleMu.Unlock()
		return
	}
	h.started = true
	h.wg.Add(2)
	h.lifecycleMu.Unlock()
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
	h.lifecycleMu.Lock()
	if h.stopped {
		h.lifecycleMu.Unlock()
		return
	}
	h.stopped = true
	started := h.started
	h.lifecycleMu.Unlock()
	h.cancel()
	if started {
		h.wg.Wait()
	}
	slog.Info("anomaly harvester stopped")
}

func (h *AnomalyHarvester) cleanupLoop() {
	defer h.wg.Done()
	ticker := time.NewTicker(h.cfg.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			h.runCleanup(h.ctx)
		}
	}
}

func (h *AnomalyHarvester) runCleanup(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cutoff := time.Now().AddDate(0, 0, -h.cfg.RetentionDays)
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		slog.Warn("anomaly harvester: cleanup transaction failed", "error", err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
		slog.Warn("anomaly harvester: cleanup RLS setup failed", "error", err)
		return
	}
	ct, err := tx.Exec(ctx,
		`DELETE FROM response_format_anomalies WHERE detected_at < $1`, cutoff)
	if err != nil {
		slog.Warn("anomaly harvester: cleanup failed", "error", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("anomaly harvester: cleanup commit failed", "error", err)
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
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			h.runBridge(h.ctx)
		}
	}
}

func (h *AnomalyHarvester) runBridge(ctx context.Context) {
	// CO-2: before alerting, backfill actual_tokens on estimated anomaly
	// rows whose request later gained real LLM-reported usage.
	h.backfillActualTokens(ctx)

	alerts := h.queryAnomalyAlerts(ctx)
	if len(alerts) == 0 {
		return
	}
	for _, a := range alerts {
		h.createFaultEvent(ctx, a)
	}
}

// backfillActualTokens (CO-2) sweeps response_format_anomalies rows that
// were recorded with actual_tokens=NULL (estimated usage path,
// handler.go emitTelemetry) and fills them from request_logs_hot rows whose
// usage_source has since become "llm" with a real completion_tokens count.
// Runs inside the harvester's bridge cadence; idempotent (skips rows whose
// actual_tokens is already set).
func (h *AnomalyHarvester) backfillActualTokens(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		slog.Warn("anomaly harvester: backfill transaction failed", "error", err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
		slog.Warn("anomaly harvester: backfill RLS setup failed", "error", err)
		return
	}
	ct, err := tx.Exec(ctx, `
		UPDATE response_format_anomalies a
		SET actual_tokens = r.completion_tokens,
		    usage_source  = 'llm'
		FROM request_logs_hot r
		WHERE a.request_id = r.request_id
		  AND a.actual_tokens IS NULL
		  AND a.usage_source  = 'estimated'
		  AND r.usage_source  = 'llm'
		  AND r.completion_tokens IS NOT NULL
		  AND r.completion_tokens > 0
	`)
	if err != nil {
		slog.Warn("anomaly harvester: backfill failed", "error", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("anomaly harvester: backfill commit failed", "error", err)
		return
	}
	if n := ct.RowsAffected(); n > 0 {
		slog.Info("anomaly harvester: backfilled actual_tokens", "rows", n)
	}
}

type anomalyAlert struct {
	anomalyType string
	severity    string
	count       int
}

// queryAnomalyAlerts selects anomaly types that have either hit the configured
// count threshold OR are critical (which always trigger regardless of count).
func (h *AnomalyHarvester) queryAnomalyAlerts(parent context.Context) []anomalyAlert {
	windowStart := time.Now().Add(-h.cfg.FaultEventWindow)
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		slog.Warn("anomaly harvester: bridge transaction failed", "error", err)
		return nil
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
		slog.Warn("anomaly harvester: bridge RLS setup failed", "error", err)
		return nil
	}
	rows, err := tx.Query(ctx, `
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
	if err := rows.Err(); err != nil {
		slog.Warn("anomaly harvester: bridge rows failed", "error", err)
		return nil
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

func (h *AnomalyHarvester) createFaultEvent(parent context.Context, a anomalyAlert) {
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
	ruleName := "data_anomaly:" + a.anomalyType
	title := fmt.Sprintf("数据异常: %s (%d次/%dmin)", a.anomalyType, a.count, int(h.cfg.FaultEventWindow.Minutes()))
	description := fmt.Sprintf("异常类型 %s 在过去 %d 分钟内出现 %d 次(严重度: %s)，触发故障事件",
		a.anomalyType, int(h.cfg.FaultEventWindow.Minutes()), a.count, a.severity)

	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Warn("anomaly harvester: begin fault bridge transaction failed",
			"anomaly_type", a.anomalyType, "error", err)
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
		slog.Warn("anomaly harvester: fault bridge RLS setup failed", "error", err)
		return
	}

	var eventID int64
	err = tx.QueryRow(ctx, `
		WITH lock AS (
			SELECT pg_advisory_xact_lock(hashtext($1))
		), inserted AS (
			INSERT INTO fault_events (rule_id, rule_name, severity, title, description, source,
			                          status, metadata, detected_at, created_at)
			SELECT 0, $1, $2, $3, $4, 'data_anomaly', 'new', $5, NOW(), NOW()
			FROM lock
			WHERE NOT EXISTS (
				SELECT 1 FROM fault_events
				WHERE source = 'data_anomaly'
				  AND rule_name = $1
				  AND status IN ('new', 'acknowledged', 'resolving')
			)
			RETURNING id
		)
		SELECT id FROM inserted
	`, ruleName, faultSev, title, description, metaJSON).Scan(&eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		slog.Warn("anomaly harvester: bridge insert fault_event failed",
			"anomaly_type", a.anomalyType, "error", err)
		return
	}
	windowStart := time.Now().Add(-h.cfg.FaultEventWindow)
	if _, err = tx.Exec(ctx, `
		UPDATE response_format_anomalies
		SET resolved = TRUE,
		    resolved_at = NOW(),
		    resolution_notes = 'bridged to fault_event'
		WHERE anomaly_type = $1
		  AND severity = $2
		  AND detected_at > $3
		  AND resolved = FALSE
	`, a.anomalyType, a.severity, windowStart); err != nil {
		slog.Warn("anomaly harvester: mark bridged anomalies resolved failed",
			"anomaly_type", a.anomalyType, "event_id", eventID, "error", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		slog.Warn("anomaly harvester: commit fault bridge failed",
			"anomaly_type", a.anomalyType, "event_id", eventID, "error", err)
		return
	}
	slog.Warn("anomaly harvester: bridged to fault_event",
		"event_id", eventID, "anomaly_type", a.anomalyType, "count", a.count,
		"anomaly_severity", a.severity, "fault_severity", faultSev)
}
