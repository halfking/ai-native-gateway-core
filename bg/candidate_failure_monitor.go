// Package bg — candidate_failure_monitor.go
//
// 2026-06-23 Phase 3 (P2) of the minimax-m3 transient-error fix: alerting
// + auto-cooling based on candidate_failure_logs.
//
// The Phase 2 work recorded every per-candidate failure to
// candidate_failure_logs. This worker periodically rolls up those rows into:
//
//  1. Alert events: when a (credential, model, error_kind) combination
//     accumulates more than `alertThreshold` failures in
//     `alertWindow`, the worker fires a slog.Warn (and a webhook if
//     configured). The alert is debounced — same combo fires at most
//     once per cooldown — so a sustained outage doesn't spam the log.
//  2. Auto-cool: when a credential's failure ratio over the last N
//     attempts exceeds `coolRatio`, the worker flips the credential's
//     availability_state to 'cooling' with a short recover_at so the
//     router stops selecting it. Admin can still force-recover.
//
// Configuration via env (parsed at Start time, no hot reload):
//
//	CANDIDATE_FAILURE_MONITOR_INTERVAL=1m         run period
//	CANDIDATE_FAILURE_ALERT_THRESHOLD=10          failures to trigger alert
//	CANDIDATE_FAILURE_ALERT_WINDOW=5m             window for alert
//	CANDIDATE_FAILURE_ALERT_COOLDOWN=15m          debounce window
//	CANDIDATE_FAILURE_COOL_RATIO=0.8              ratio over recent attempts
//	CANDIDATE_FAILURE_COOL_MIN_SAMPLES=20         min attempts before cool
//	CANDIDATE_FAILURE_COOL_MINUTES=2              cool duration
//	CANDIDATE_FAILURE_WEBHOOK_URL=""               optional webhook for alerts
package bg

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CandidateFailureAlert is the row shape inserted into the in-memory
// alert log + (optionally) the webhook payload.
// CandidateFailureAlert represents a detected anomaly in candidate selection failures.
type CandidateFailureAlert struct {
	Ts            time.Time `json:"ts"`
	CredentialID  int       `json:"credential_id"`
	ProviderID    int       `json:"provider_id"`
	RawModelName  string    `json:"raw_model_name"`
	ErrorKind     string    `json:"error_kind"`
	Count         int       `json:"count"`
	WindowSec     int       `json:"window_sec"`
	DistinctCodes int       `json:"distinct_status_codes"`
	LastBody      string    `json:"last_response_preview,omitempty"`
}

// CandidateFailureMonitor runs the alert + auto-cool loop. nil-safe.
type CandidateFailureMonitor struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}

	interval       time.Duration
	alertThresh    int
	alertWindow    time.Duration
	alertCooldown  time.Duration
	coolRatio      float64
	coolMinSamples int
	coolMinutes    int
	webhookURL     string

	// alertLastFired debounces duplicate alerts. Keyed by
	// "credential_id|raw_model_name|error_kind".
	alertLastFired map[string]time.Time
	alertMu        sync.Mutex

	// Recent alerts kept in-memory for the admin UI to display. Capped
	// at 200 entries so a long outage doesn't blow up memory.
	recentAlerts []CandidateFailureAlert
}

func NewCandidateFailureMonitor(db *pgxpool.Pool) *CandidateFailureMonitor {
	m := &CandidateFailureMonitor{
		db:             db,
		done:           make(chan struct{}),
		interval:       parseDurationEnv("CANDIDATE_FAILURE_MONITOR_INTERVAL", time.Minute),
		alertThresh:    parseIntEnv("CANDIDATE_FAILURE_ALERT_THRESHOLD", 10),
		alertWindow:    parseDurationEnv("CANDIDATE_FAILURE_ALERT_WINDOW", 5*time.Minute),
		alertCooldown:  parseDurationEnv("CANDIDATE_FAILURE_ALERT_COOLDOWN", 15*time.Minute),
		coolRatio:      parseFloatEnv("CANDIDATE_FAILURE_COOL_RATIO", 0.8),
		coolMinSamples: parseIntEnv("CANDIDATE_FAILURE_COOL_MIN_SAMPLES", 20),
		coolMinutes:    parseIntEnv("CANDIDATE_FAILURE_COOL_MINUTES", 2),
		webhookURL:     os.Getenv("CANDIDATE_FAILURE_WEBHOOK_URL"),
		alertLastFired: make(map[string]time.Time),
		recentAlerts:   make([]CandidateFailureAlert, 0, 64),
	}
	slog.Info("candidate_failure_monitor configured",
		"interval", m.interval,
		"alert_threshold", m.alertThresh,
		"alert_window", m.alertWindow,
		"cool_ratio", m.coolRatio,
		"cool_min_samples", m.coolMinSamples,
		"cool_minutes", m.coolMinutes,
		"webhook_set", m.webhookURL != "",
	)
	return m
}

func (m *CandidateFailureMonitor) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)
	go m.run(ctx)
	slog.Info("candidate_failure_monitor started")
}

func (m *CandidateFailureMonitor) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	<-m.done
}

func (m *CandidateFailureMonitor) run(ctx context.Context) {
	defer close(m.done)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	// Run once at start so a freshly-started gateway catches existing
	// failure patterns without waiting a full interval.
	m.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

// tick runs one pass: detect alert conditions + auto-cool triggers.
// Each step is independent; failures in one don't block the other.
func (m *CandidateFailureMonitor) tick(ctx context.Context) {
	if m.db == nil {
		return
	}
	stepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := m.checkAlerts(stepCtx); err != nil {
		slog.Warn("candidate_failure_monitor: checkAlerts failed", "error", err)
	}
	if err := m.checkAutoCool(stepCtx); err != nil {
		slog.Warn("candidate_failure_monitor: checkAutoCool failed", "error", err)
	}
}

// checkAlerts scans candidate_failure_logs over the alert window and
// fires an alert for any (credential, model, error_kind) tuple whose
// count >= alertThresh. Debounced via alertLastFired so a single outage
// doesn't spam the log every interval.
func (m *CandidateFailureMonitor) checkAlerts(ctx context.Context) error {
	since := time.Now().Add(-m.alertWindow)

	rows, err := m.db.Query(ctx, `
		SELECT
			credential_id, provider_id, raw_model_name, error_kind,
			COUNT(*) AS cnt,
			COUNT(DISTINCT upstream_status_code) AS distinct_codes,
			(SELECT upstream_response_preview
			   FROM candidate_failure_logs c2
			   WHERE c2.credential_id = c.credential_id
			     AND c2.raw_model_name = c.raw_model_name
			     AND c2.error_kind = c.error_kind
			   ORDER BY ts DESC LIMIT 1) AS last_body
		FROM candidate_failure_logs c
		WHERE ts >= $1
		GROUP BY credential_id, provider_id, raw_model_name, error_kind
		HAVING COUNT(*) >= $2
	`, since, m.alertThresh)
	if err != nil {
		return err
	}
	defer rows.Close()

	now := time.Now()
	for rows.Next() {
		var a CandidateFailureAlert
		var lastBody *string
		if err := rows.Scan(
			&a.CredentialID, &a.ProviderID, &a.RawModelName, &a.ErrorKind,
			&a.Count, &a.DistinctCodes, &lastBody,
		); err != nil {
			slog.Warn("candidate_failure_monitor: scan alert row", "error", err)
			continue
		}
		a.Ts = now
		a.WindowSec = int(m.alertWindow.Seconds())
		if lastBody != nil {
			a.LastBody = *lastBody
		}

		key := alertKey(a.CredentialID, a.RawModelName, a.ErrorKind)
		if m.shouldFire(key, now) {
			m.fireAlert(a)
		}
	}
	return nil
}

// checkAutoCool looks for credentials whose recent attempts are mostly
// failing. When failure_ratio >= coolRatio AND sample_count >= coolMinSamples,
// the credential is flipped to availability_state='cooling' with
// recover_at = now() + coolMinutes. The recovery worker (credential_recovery.go)
// will restore 'ready' once recover_at passes — unless auto-cool fires again.
func (m *CandidateFailureMonitor) checkAutoCool(ctx context.Context) error {
	// Pull recent candidate_failure_logs + attempt count from request_logs
	// for the same window. Two CTEs joined by credential.
	rows, err := m.db.Query(ctx, `
		WITH win AS (
		    SELECT credential_id, COUNT(*) FILTER (WHERE NOT success) AS fails,
		                  COUNT(*) AS attempts
		    FROM request_logs
		    WHERE ts >= now() - interval '5 minutes'
		      AND credential_id IS NOT NULL
		    GROUP BY credential_id
		),
		cfl AS (
		    SELECT credential_id, COUNT(*) AS recent_failures
		    FROM candidate_failure_logs
		    WHERE ts >= now() - interval '5 minutes'
		    GROUP BY credential_id
		)
		SELECT w.credential_id, w.attempts, w.fails, COALESCE(c.recent_failures, 0)
		FROM win w
		LEFT JOIN cfl c ON c.credential_id = w.credential_id
		WHERE w.attempts >= $1
		  AND w.fails::float / w.attempts >= $2
	`, m.coolMinSamples, m.coolRatio)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var credID, attempts, fails, recentFails int
		if err := rows.Scan(&credID, &attempts, &fails, &recentFails); err != nil {
			continue
		}
		ratio := float64(fails) / float64(attempts)
		slog.Warn("candidate_failure_monitor: auto-cool trigger",
			"credential_id", credID,
			"attempts_5m", attempts,
			"fails_5m", fails,
			"failure_ratio", ratio,
			"candidate_failure_logs_count_5m", recentFails,
		)
		if err := m.applyAutoCool(ctx, credID); err != nil {
			slog.Warn("candidate_failure_monitor: applyAutoCool failed",
				"credential_id", credID, "error", err)
		}
	}
	return nil
}

// applyAutoCool flips a credential to availability_state='cooling' with
// a short recover_at. Only cools active credentials; manual_disable and
// lifecycle_status are preserved.
func (m *CandidateFailureMonitor) applyAutoCool(ctx context.Context, credID int) error {
	tag, err := m.db.Exec(ctx, `
		UPDATE credentials
		SET availability_state = 'cooling',
		    availability_recover_at = now() + ($1 || ' minutes')::interval,
		    state_reason_code = 'auto_cool_high_failure_rate',
		    state_reason_detail = 'candidate_failure_monitor: high failure rate detected',
		    state_updated_at = now()
		WHERE id = $2
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  AND availability_state NOT IN ('cooling')
	`, m.coolMinutes, credID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		slog.Info("candidate_failure_monitor: credential cooled",
			"credential_id", credID,
			"cool_minutes", m.coolMinutes,
		)
	}
	return nil
}

// shouldFire returns true when no alert has fired for this key within
// the cooldown window. The map is locked briefly to keep concurrent
// ticks consistent.
func (m *CandidateFailureMonitor) shouldFire(key string, now time.Time) bool {
	m.alertMu.Lock()
	defer m.alertMu.Unlock()
	last, ok := m.alertLastFired[key]
	if !ok || now.Sub(last) >= m.alertCooldown {
		m.alertLastFired[key] = now
		return true
	}
	return false
}

// fireAlert logs the alert, appends to the in-memory ring buffer, and
// posts to the optional webhook. The webhook call is best-effort with a
// 5s timeout; a failure is logged but does not affect the alert state.
func (m *CandidateFailureMonitor) fireAlert(a CandidateFailureAlert) {
	slog.Warn("candidate_failure_alert",
		"credential_id", a.CredentialID,
		"provider_id", a.ProviderID,
		"raw_model_name", a.RawModelName,
		"error_kind", a.ErrorKind,
		"count_in_window", a.Count,
		"window_sec", a.WindowSec,
		"distinct_status_codes", a.DistinctCodes,
		"last_response_preview", truncateStr(a.LastBody, 200),
	)

	m.alertMu.Lock()
	m.recentAlerts = append(m.recentAlerts, a)
	if len(m.recentAlerts) > 200 {
		// Drop oldest 50 to bound memory on a sustained outage.
		m.recentAlerts = m.recentAlerts[len(m.recentAlerts)-200:]
	}
	m.alertMu.Unlock()

	if m.webhookURL == "" {
		return
	}
	go m.postWebhook(a)
}

func (m *CandidateFailureMonitor) postWebhook(a CandidateFailureAlert) {
	body, err := json.Marshal(a)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.webhookURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("candidate_failure_monitor: webhook post failed",
			"url", m.webhookURL, "error", err)
		return
	}
	//nolint:errcheck // best-effort close
	resp.Body.Close()
}

// RecentAlerts returns a snapshot of the in-memory alert ring buffer.
// Safe to call from any goroutine; the underlying slice is copied.
func (m *CandidateFailureMonitor) RecentAlerts() []CandidateFailureAlert {
	m.alertMu.Lock()
	defer m.alertMu.Unlock()
	out := make([]CandidateFailureAlert, len(m.recentAlerts))
	copy(out, m.recentAlerts)
	return out
}

func alertKey(credID int, model, kind string) string {
	return strconv.Itoa(credID) + "|" + model + "|" + kind
}

func parseDurationEnv(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func parseIntEnv(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func parseFloatEnv(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
