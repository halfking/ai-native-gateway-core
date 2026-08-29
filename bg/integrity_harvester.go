// Package bg — integrity_harvester.go
//
// 2026-07-28: integrity event → fault_events bridge. Mirrors the
// streaming.AnomalyHarvester pattern (advisory-lock + active-fault
// dedup) but reads from model_integrity_events. Critical events can
// bridge immediately; high events bridge per-(cred, model, anomaly)
// after a configurable time window so a burst of repeated integrity
// issues collapses to one operational fault.
//
// Configuration (env, no hot reload):
//
//	LLM_GATEWAY_INTEGRITY_HARVEST_INTERVAL=5m
//	LLM_GATEWAY_INTEGRITY_HARVEST_CRITICAL_AGE=1m
//	LLM_GATEWAY_INTEGRITY_HARVEST_HIGH_WINDOW=15m
//	LLM_GATEWAY_INTEGRITY_HARVEST_HIGH_MIN_COUNT=3
package bg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IntegrityHarvesterConfig controls IntegrityHarvester thresholds.
type IntegrityHarvesterConfig struct {
	Interval     time.Duration
	CriticalAge  time.Duration
	HighWindow   time.Duration
	HighMinCount int
	Enabled      bool
}

// DefaultIntegrityHarvesterConfig mirrors the production defaults.
func DefaultIntegrityHarvesterConfig() IntegrityHarvesterConfig {
	return IntegrityHarvesterConfig{
		Interval:     parseDurationEnv("LLM_GATEWAY_INTEGRITY_HARVEST_INTERVAL", 5*time.Minute),
		CriticalAge:  parseDurationEnv("LLM_GATEWAY_INTEGRITY_HARVEST_CRITICAL_AGE", 1*time.Minute),
		HighWindow:   parseDurationEnv("LLM_GATEWAY_INTEGRITY_HARVEST_HIGH_WINDOW", 15*time.Minute),
		HighMinCount: parseIntEnv("LLM_GATEWAY_INTEGRITY_HARVEST_HIGH_MIN_COUNT", 3),
		Enabled:      os.Getenv("LLM_GATEWAY_INTEGRITY_HARVEST_ENABLED") != "false",
	}
}

// integrityHarvesterDB is the minimal database contract the bridges
// exercise (Begin + a nil-sentinel check). *pgxpool.Pool and the
// pgxmock-backed shim in the test both satisfy it, so the SQL contract
// can be pinned without a real PostgreSQL instance. Mirrors the
// credentialRecoveryDB seam in credential_recovery.go.
type integrityHarvesterDB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// IntegrityHarvester polls model_integrity_events and inserts fault_events
// for unresolved critical rows past the age threshold and high
// per-(cred, model, anomaly) clusters above the count threshold.
type IntegrityHarvester struct {
	db   integrityHarvesterDB
	cfg  IntegrityHarvesterConfig
	done chan struct{}

	started         atomic.Bool
	cycles          atomic.Uint64
	criticalBridged atomic.Uint64
	highBridged     atomic.Uint64
}

// NewIntegrityHarvester constructs a worker. Pass nil pool to disable.
func NewIntegrityHarvester(db *pgxpool.Pool, cfg IntegrityHarvesterConfig) *IntegrityHarvester {
	if db == nil {
		return &IntegrityHarvester{cfg: cfg, done: make(chan struct{})}
	}
	return &IntegrityHarvester{db: db, cfg: cfg, done: make(chan struct{})}
}

// Start launches the background loop. Idempotent.
func (h *IntegrityHarvester) Start(ctx context.Context) {
	if h == nil || h.db == nil || !h.cfg.Enabled {
		return
	}
	if !h.started.CompareAndSwap(false, true) {
		return
	}
	go h.run(ctx)
	slog.Info("integrity_harvester started",
		"interval", h.cfg.Interval,
		"critical_age", h.cfg.CriticalAge,
		"high_window", h.cfg.HighWindow,
		"high_min_count", h.cfg.HighMinCount,
	)
}

// Stop signals the loop to exit and waits for it.
func (h *IntegrityHarvester) Stop() {
	if h == nil || !h.started.Load() {
		return
	}
	// Use a short-lived cancel via goroutine because the loop reads
	// from the same done channel; the loop selects on ctx.Done() so
	// we can also signal it to drain.
	go func() { close(h.done) }()
	slog.Info("integrity_harvester stopped",
		"cycles", h.cycles.Load(),
		"critical_bridged", h.criticalBridged.Load(),
		"high_bridged", h.highBridged.Load(),
	)
}

func (h *IntegrityHarvester) run(ctx context.Context) {
	ticker := time.NewTicker(h.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.done:
			return
		case <-ticker.C:
			h.cycles.Add(1)
			if err := h.cycle(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("integrity_harvester: cycle failed", "error", err)
			}
		}
	}
}

// cycle runs one pass: critical rows past age + high clusters past window.
func (h *IntegrityHarvester) cycle(ctx context.Context) error {
	if err := h.bridgeCritical(ctx); err != nil {
		return fmt.Errorf("bridge critical: %w", err)
	}
	if err := h.bridgeHigh(ctx); err != nil {
		return fmt.Errorf("bridge high: %w", err)
	}
	return nil
}

// bridgeCritical writes fault_events for each unresolved critical
// integrity event older than CriticalAge. Uses an advisory lock so
// concurrent gateway instances do not double-bridge the same rows.
//
// 2026-08-02: rewritten against the real fault_events schema (migration
// 375 / db/db.go:2649). The previous version referenced columns that do
// not exist on this table (context, tenant_id, source_event_id,
// first_seen_at, last_seen_at, occurrence_count), passed a string into
// the BIGINT rule_id, used status='open' (rejected by the CHECK) and an
// ON CONFLICT with no arbiter index — so every tick errored out. The
// fix mirrors domains/streaming/anomaly_harvester.go::createFaultEvent:
// rule_id is the literal 0, the human key lives in rule_name, the
// dedup context goes into the metadata JSONB, status is 'new', and
// cross-tick dedup is a WHERE NOT EXISTS on (source, rule_name,
// metadata->>'integrity_event_id') over the active statuses.
func (h *IntegrityHarvester) bridgeCritical(ctx context.Context) error {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// The lock key is passed as a parameter ($1) rather than inlined so
	// the SQL string has no ':' that pgxmock's placeholder scanner (and
	// some proxied PG pools) misread as a named parameter.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "integrity_harvester:critical"); err != nil {
		return err
	}
	// Only candidates without an active fault for the same integrity
	// event id survive the WHERE NOT EXISTS guard, so a re-tick after a
	// successful bridge is a no-op.
	rows, err := tx.Query(ctx, `
		SELECT e.id, e.tenant_id, e.provider_id, e.credential_id,
		       COALESCE(e.provider_code,''), COALESCE(e.raw_model_name,''),
		       COALESCE(e.client_model,''), COALESCE(e.outbound_model,''),
		       e.anomaly_type, e.severity, e.actual_value
		FROM model_integrity_events e
		WHERE e.severity = 'critical'
		  AND e.resolved = false
		  AND e.ts < now() - $1::interval
		  AND NOT EXISTS (
		    SELECT 1 FROM fault_events f
		    WHERE f.source = 'integrity_harvester'
		      AND f.rule_name = 'integrity:' || e.anomaly_type
		      AND f.metadata->>'integrity_event_id' = e.id::text
		      AND f.status IN ('new', 'acknowledged', 'resolving')
		  )
		ORDER BY e.ts ASC
		LIMIT 200`, h.cfg.CriticalAge)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pending struct {
		eventID     int64
		tenantID    *string
		ruleName    string
		title       string
		desc        string
		severity    string
		provider    string
		rawModel    string
		clientModel string
		credential  *int64
	}
	var batch []pending
	for rows.Next() {
		var p pending
		var providerID, credID *int64
		var providerCode, rawModel, client, outbound, anomaly, severity, actual string
		if err := rows.Scan(&p.eventID, &p.tenantID, &providerID, &credID,
			&providerCode, &rawModel, &client, &outbound, &anomaly, &severity, &actual); err != nil {
			slog.Warn("integrity_harvester: scan row failed", "error", err)
			continue
		}
		_ = providerID
		p.credential = credID
		p.provider = providerCode
		p.rawModel = rawModel
		p.clientModel = client
		p.ruleName = "integrity:" + anomaly
		p.title = fmt.Sprintf("[%s] %s", severity, anomaly)
		p.desc = formatIntegrityFaultDesc(providerCode, rawModel, client, outbound, anomaly, actual)
		p.severity = mapIntegritySeverityToFault(severity)
		batch = append(batch, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range batch {
		// metadata carries every key the operator dashboard + the
		// WHERE NOT EXISTS dedup need: the integrity event id (dedup),
		// plus provider/model/credential context so the fault row is
		// self-describing without a join back to model_integrity_events.
		meta := map[string]any{
			"integrity_event_id": p.eventID,
			"provider_code":      p.provider,
			"raw_model_name":     p.rawModel,
			"client_model":       p.clientModel,
		}
		if p.credential != nil {
			meta["credential_id"] = *p.credential
		}
		if p.tenantID != nil {
			meta["tenant_id"] = *p.tenantID
		}
		metaJSON, err := json.Marshal(meta)
		if err != nil {
			slog.Warn("integrity_harvester: metadata marshal failed", "error", err)
			metaJSON = []byte(`{}`)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO fault_events
				(rule_id, rule_name, severity, title, description, source,
				 status, metadata, detected_at, created_at, updated_at)
			VALUES (0, $1, $2, $3, $4, 'integrity_harvester',
				'new', $5, now(), now(), now())`,
			p.ruleName, p.severity, p.title, p.desc, metaJSON); err != nil {
			return err
		}
		h.criticalBridged.Add(1)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

// bridgeHigh aggregates unresolved high-severity integrity events over the
// time window per (tenant, anomaly, credential, model) and creates a
// single fault_events row when the count crosses HighMinCount. Lower
// severity events (low/medium) are intentionally not bridged here;
// model_integrity_events remains the source of truth for them.
//
// 2026-08-02: rewritten against the real fault_events schema (see the
// note on bridgeCritical). The high cluster's dedup key is
// (anomaly, credential_id, raw_model_name); those live in the metadata
// JSONB and the WHERE NOT EXISTS guard reads them back NULL-safely
// (IS NOT DISTINCT FROM) so a NULL credential does not collapse two
// different clusters into one.
func (h *IntegrityHarvester) bridgeHigh(ctx context.Context) error {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// See bridgeCritical for why the lock key is a parameter.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "integrity_harvester:high"); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		WITH agg AS (
			SELECT tenant_id, anomaly_type, COALESCE(provider_code,'') AS provider_code,
			       credential_id, COALESCE(raw_model_name,'') AS raw_model_name,
			       COUNT(*) AS n,
			       MIN(ts) AS first_ts, MAX(ts) AS last_ts,
			       MAX(actual_value) AS sample_actual
			FROM model_integrity_events
			WHERE severity = 'high'
			  AND resolved = false
			  AND ts > now() - $1::interval
			GROUP BY tenant_id, anomaly_type, provider_code, credential_id, raw_model_name
			HAVING COUNT(*) >= $2
		)
		SELECT a.tenant_id, a.anomaly_type, a.provider_code, a.credential_id,
		       a.raw_model_name, a.n, a.first_ts, a.last_ts, a.sample_actual
		FROM agg a
		WHERE NOT EXISTS (
		    SELECT 1 FROM fault_events f
		    WHERE f.source = 'integrity_harvester'
		      AND f.rule_name = 'integrity-high:' || a.anomaly_type
		      AND f.metadata->>'credential_id' IS NOT DISTINCT FROM COALESCE(a.credential_id::text, '')
		      AND f.metadata->>'raw_model_name' IS NOT DISTINCT FROM a.raw_model_name
		      AND f.status IN ('new', 'acknowledged', 'resolving')
		)
		ORDER BY a.last_ts DESC
		LIMIT 200`, h.cfg.HighWindow, h.cfg.HighMinCount)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pending struct {
		tenantID    *string
		ruleName    string
		severity    string
		title       string
		description string
		provider    string
		credID      *int64
		rawModel    string
		anomaly     string
		firstSeen   time.Time
		lastSeen    time.Time
		count       int
		sample      string
	}
	var batch []pending
	for rows.Next() {
		var (
			tenant       *string
			anomaly      string
			providerCode string
			credID       *int64
			rawModel     string
			count        int
			firstSeen    time.Time
			lastSeen     time.Time
			sampleActual string
		)
		if err := rows.Scan(&tenant, &anomaly, &providerCode, &credID, &rawModel,
			&count, &firstSeen, &lastSeen, &sampleActual); err != nil {
			slog.Warn("integrity_harvester: high scan row failed", "error", err)
			continue
		}
		var credStr string
		if credID != nil {
			credStr = fmt.Sprintf("%d", *credID)
		} else {
			credStr = "(unknown)"
		}
		p := pending{
			tenantID: tenant,
			ruleName: "integrity-high:" + anomaly,
			severity: "warning",
			title:    fmt.Sprintf("[high] %s burst", anomaly),
			description: fmt.Sprintf("%d high-severity %s events between %s and %s on credential %s model %s; latest: %s",
				count, anomaly,
				firstSeen.UTC().Format(time.RFC3339),
				lastSeen.UTC().Format(time.RFC3339),
				credStr, rawModel, sampleActual),
			provider:  providerCode,
			credID:    credID,
			rawModel:  rawModel,
			anomaly:   anomaly,
			firstSeen: firstSeen,
			lastSeen:  lastSeen,
			count:     count,
			sample:    sampleActual,
		}
		batch = append(batch, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range batch {
		// metadata mirrors the critical path's keys plus the cluster
		// aggregates (count, window) so the operator dashboard can show
		// the burst without a second query.
		var credVal any
		credText := ""
		if p.credID != nil {
			credVal = *p.credID
			credText = fmt.Sprintf("%d", *p.credID)
		}
		meta := map[string]any{
			"anomaly_type":   p.anomaly,
			"provider_code":  p.provider,
			"raw_model_name": p.rawModel,
			"credential_id":  credText,
			"event_count":    p.count,
			"first_seen":     p.firstSeen.UTC().Format(time.RFC3339),
			"last_seen":      p.lastSeen.UTC().Format(time.RFC3339),
			"sample_actual":  p.sample,
			"window":         h.cfg.HighWindow.String(),
			"min_count":      h.cfg.HighMinCount,
		}
		if p.tenantID != nil {
			meta["tenant_id"] = *p.tenantID
		}
		_ = credVal
		metaJSON, err := json.Marshal(meta)
		if err != nil {
			slog.Warn("integrity_harvester: high metadata marshal failed", "error", err)
			metaJSON = []byte(`{}`)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO fault_events
				(rule_id, rule_name, severity, title, description, source,
				 status, metadata, detected_at, created_at, updated_at)
			VALUES (0, $1, $2, $3, $4, 'integrity_harvester',
				'new', $5, $6, now(), now())`,
			p.ruleName, p.severity, p.title, p.description, metaJSON, p.lastSeen); err != nil {
			return err
		}
		h.highBridged.Add(1)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

// formatIntegrityFaultDesc builds a short, PII-safe description for the
// fault_events row. Never includes user prompts or model output.
func formatIntegrityFaultDesc(providerCode, rawModel, client, outbound, anomaly, actual string) string {
	var b strings.Builder
	if providerCode != "" {
		b.WriteString("provider=")
		b.WriteString(providerCode)
		b.WriteString(" ")
	}
	if rawModel != "" {
		b.WriteString("model=")
		b.WriteString(rawModel)
		b.WriteString(" ")
	}
	if client != "" && client != rawModel {
		b.WriteString("client=")
		b.WriteString(client)
		b.WriteString(" ")
	}
	if outbound != "" && outbound != rawModel && outbound != client {
		b.WriteString("outbound=")
		b.WriteString(outbound)
		b.WriteString(" ")
	}
	b.WriteString("anomaly=")
	b.WriteString(anomaly)
	if actual != "" {
		b.WriteString(" actual=")
		b.WriteString(actual)
	}
	return strings.TrimSpace(b.String())
}

// mapIntegritySeverityToFault collapses the integrity severity enum to
// the fault_events.severity CHECK list ('info','warning','error','critical').
func mapIntegritySeverityToFault(sev string) string {
	switch sev {
	case "critical":
		return "critical"
	case "high":
		return "error"
	case "medium":
		return "warning"
	default:
		return "info"
	}
}
