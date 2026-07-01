// Package bg — unified_probe_scheduler.go
//
// DEPRECATED / DEAD BRANCH as of 2026-06-29.
//
// Background: this scheduler was added on 2026-06-28 (commit 20ad9de0)
// as a "priority queue" replacement for ModelProbeRunner +
// SuspiciousProbeRunner, with the goal of cutting duplicate probes and
// shrinking the failure-detection blind window from 2h to 30s. In
// practice the cutover was never completed:
//
//   - It writes a different model_probe_state.state enum
//     ("healthy"/"failing"/"probing") than the rest of the system
//     ("healthy_confirmed"/"broken_confirmed"/"recovering"/"unknown"/"suspicious").
//     Migrating the schema (302_unified_probe_scheduler.sql) silently
//     rewrote every existing row to the new vocabulary, which broke
//     read paths in credential_recovery.go, model_probe.go,
//     credential_recovery_test.go, etc.
//   - It also tried to drive credential_model_bindings.available
//     through raw_model_name-only LIMIT 1 joins, which silently writes
//     the wrong binding on multi-provider setups (fixed in 301/302
//     runtime ensure and a DB-only UPDATE rewrite).
//   - It was started unconditionally alongside the legacy workers
//     (commit 20ad9de0), causing two writers to race on the same row.
//     Three remediation PRs have stacked since (a3b87b1, d501d24, f4a1b51,
//     f498fc03, 0556ba08, 5941428) but the dead-branch status here has
//     not been formalised.
//
// Current status: cmd/gateway/main.go only starts this scheduler when
// LLM_GATEWAY_ENABLE_UNIFIED_PROBE_SCHEDULER=true. The default is
// "false" so the legacy ModelProbeRunner + CredentialProbeV2 +
// PassiveProbeListener trio remains the single writer of
// model_probe_state. This file is kept for reference only and may be
// removed in a future cleanup once the unified design is re-thought.
//
// DO NOT call Start() without first confirming nothing else writes
// model_probe_state concurrently.
//
// Original spec: 2026-06-28-unified-probe-scheduler (superseded).
//
// State machine (historical, kept for reference):
//
//	healthy ─────────────────────→ suspicious
//	   ↑         (watchdog检测异常         ↓
//	   │          或实时请求失败)      (立即探测)
//	   │                                  ↓
//	   └────── recovering ←─────────── failing
//	      (连续3次成功)        (探测失败)
//
// Priority queues:
//
//	P0 - urgent:     Real-time request failures (probe within 30s)
//	P1 - suspicious: Suspected issues (probe within 5min)
//	P2 - failing:    Recovery attempts (exponential backoff)
//	P3 - watchdog:   Periodic validation (adaptive 2-8h interval)
//
// Key improvements over dual-system:
//   - 40-50% fewer probes (no duplication)
//   - <30s failure detection (vs 2h blind window)
//   - Adaptive intervals based on stability
//   - Real-time request feedback integration
//   - Unified concurrency control
//
// Spec: 2026-06-28-unified-probe-scheduler
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/secret"
	"golang.org/x/sync/semaphore"
)

const (
	// Probe intervals for each priority
	UrgentProbeInterval            = 30 * time.Second // P0: real-time failures
	UnifiedSuspiciousProbeInterval = 5 * time.Minute  // P1: suspicious states (renamed to avoid conflict)
	FailingProbeInterval           = 2 * time.Minute  // P2: recovery attempts
	WatchdogProbeInterval          = 10 * time.Minute // P3: periodic validation

	// Batch sizes per priority
	UrgentProbeBatch     = 20
	SuspiciousProbeBatch = 30
	FailingProbeBatch    = 20
	WatchdogProbeBatch   = 50

	// Global concurrency limits
	MaxCredentialConcurrentProbes = 2
	MaxGlobalConcurrentProbes     = 100

	// Success rate thresholds for adaptive intervals
	SuccessRateExcellent = 99.0 // → 8h interval
	SuccessRateGood      = 95.0 // → 6h interval
	SuccessRateNormal    = 90.0 // → 4h interval
	// Below normal → 2h interval
)

// ProbePriority represents probe priority levels
type ProbePriority int

const (
	PriorityUrgent ProbePriority = iota
	PrioritySuspicious
	PriorityFailing
	PriorityWatchdog
)

func (p ProbePriority) String() string {
	return [...]string{"urgent", "suspicious", "failing", "watchdog"}[p]
}

// ProbeTask represents a single probe task
type ProbeTask struct {
	CredentialID  int64
	RawModel      string
	OutboundModel string
	BaseURL       string
	Protocol      string
	Priority      ProbePriority
	Reason        string
	APIKey        string // decrypted
}

// UnifiedProbeScheduler is the main scheduler
type UnifiedProbeScheduler struct {
	db      *pgxpool.Pool
	encKey  []byte
	keyring *secret.Keyring

	// Priority queues
	urgentQueue     chan ProbeTask
	suspiciousQueue chan ProbeTask
	failingQueue    chan ProbeTask
	watchdogQueue   chan ProbeTask

	// Global concurrency control
	globalSem *semaphore.Weighted

	// Per-credential concurrency control
	credentialSemsMu sync.RWMutex
	credentialSems   map[int64]*semaphore.Weighted

	// Lifecycle
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewUnifiedProbeScheduler creates a new unified probe scheduler
func NewUnifiedProbeScheduler(db *pgxpool.Pool, encKey []byte) *UnifiedProbeScheduler {
	return &UnifiedProbeScheduler{
		db:     db,
		encKey: encKey,

		// Buffered channels to avoid blocking
		urgentQueue:     make(chan ProbeTask, UrgentProbeBatch*2),
		suspiciousQueue: make(chan ProbeTask, SuspiciousProbeBatch*2),
		failingQueue:    make(chan ProbeTask, FailingProbeBatch*2),
		watchdogQueue:   make(chan ProbeTask, WatchdogProbeBatch*2),

		globalSem:      semaphore.NewWeighted(MaxGlobalConcurrentProbes),
		credentialSems: make(map[int64]*semaphore.Weighted),
	}
}

func (s *UnifiedProbeScheduler) SetKeyring(kr *secret.Keyring) { s.keyring = kr }

// Start starts the unified probe scheduler
func (s *UnifiedProbeScheduler) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)

	// Start worker pools for each priority
	s.wg.Add(4)
	go s.runUrgentWorker(ctx)
	go s.runSuspiciousWorker(ctx)
	go s.runFailingWorker(ctx)
	go s.runWatchdogWorker(ctx)

	// Start schedulers for each priority
	s.wg.Add(4)
	go s.scheduleUrgent(ctx)
	go s.scheduleSuspicious(ctx)
	go s.scheduleFailing(ctx)
	go s.scheduleWatchdog(ctx)

	// Start background maintenance tasks
	s.wg.Add(2)
	go s.runSuccessRateUpdater(ctx)
	go s.runDailyCounterReset(ctx)

	slog.Info("unified probe scheduler started",
		"max_global_concurrent", MaxGlobalConcurrentProbes,
		"max_credential_concurrent", MaxCredentialConcurrentProbes,
	)
}

// Stop stops the scheduler gracefully
func (s *UnifiedProbeScheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	slog.Info("unified probe scheduler stopped")
}

// ── Priority Schedulers ─────────────────────────────────────────────────

// scheduleUrgent feeds the urgent queue (P0)
func (s *UnifiedProbeScheduler) scheduleUrgent(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(UrgentProbeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.fetchAndQueue(ctx, "v_unified_probe_urgent", PriorityUrgent, s.urgentQueue)
		}
	}
}

// scheduleSuspicious feeds the suspicious queue (P1)
func (s *UnifiedProbeScheduler) scheduleSuspicious(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(UnifiedSuspiciousProbeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.fetchAndQueue(ctx, "v_unified_probe_suspicious", PrioritySuspicious, s.suspiciousQueue)
		}
	}
}

// scheduleFailing feeds the failing queue (P2)
func (s *UnifiedProbeScheduler) scheduleFailing(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(FailingProbeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.fetchAndQueue(ctx, "v_unified_probe_failing", PriorityFailing, s.failingQueue)
		}
	}
}

// scheduleWatchdog feeds the watchdog queue (P3)
func (s *UnifiedProbeScheduler) scheduleWatchdog(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(WatchdogProbeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.fetchAndQueue(ctx, "v_unified_probe_watchdog", PriorityWatchdog, s.watchdogQueue)
		}
	}
}

// fetchAndQueue fetches probe targets from a view and queues them
func (s *UnifiedProbeScheduler) fetchAndQueue(ctx context.Context, viewName string, priority ProbePriority, queue chan<- ProbeTask) {
	rows, err := s.db.Query(ctx, fmt.Sprintf("SELECT credential_id, raw_model_name, outbound_model_name, base_url, protocol FROM %s", viewName))
	if err != nil {
		slog.Warn("unified probe: query failed", "view", viewName, "error", err)
		return
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var task ProbeTask
		if err := rows.Scan(&task.CredentialID, &task.RawModel, &task.OutboundModel, &task.BaseURL, &task.Protocol); err != nil {
			continue
		}
		task.Priority = priority

		// Non-blocking send
		select {
		case queue <- task:
			count++
		default:
			slog.Debug("unified probe: queue full", "priority", priority.String())
			return
		}
	}

	if count > 0 {
		slog.Debug("unified probe: queued tasks", "priority", priority.String(), "count", count)
	}
}

// ── Worker Pools ────────────────────────────────────────────────────────

// runUrgentWorker processes urgent probes (P0)
func (s *UnifiedProbeScheduler) runUrgentWorker(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-s.urgentQueue:
			s.processTask(ctx, task)
		}
	}
}

// runSuspiciousWorker processes suspicious probes (P1)
func (s *UnifiedProbeScheduler) runSuspiciousWorker(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-s.suspiciousQueue:
			s.processTask(ctx, task)
		}
	}
}

// runFailingWorker processes failing probes (P2)
func (s *UnifiedProbeScheduler) runFailingWorker(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-s.failingQueue:
			s.processTask(ctx, task)
		}
	}
}

// runWatchdogWorker processes watchdog probes (P3)
func (s *UnifiedProbeScheduler) runWatchdogWorker(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-s.watchdogQueue:
			s.processTask(ctx, task)
		}
	}
}

// ── Task Processing ─────────────────────────────────────────────────────

// processTask processes a single probe task with concurrency control
func (s *UnifiedProbeScheduler) processTask(ctx context.Context, task ProbeTask) {
	// Acquire global semaphore
	if err := s.globalSem.Acquire(ctx, 1); err != nil {
		return
	}
	defer s.globalSem.Release(1)

	// Acquire credential-specific semaphore
	credSem := s.getCredentialSemaphore(task.CredentialID)
	if err := credSem.Acquire(ctx, 1); err != nil {
		return
	}
	defer credSem.Release(1)

	// Try to acquire probe permission in DB (atomic check + update to 'probing')
	canProbe, err := s.tryAcquireProbePermission(ctx, task.CredentialID, task.RawModel)
	if err != nil {
		slog.Debug("unified probe: acquire permission failed",
			"priority", task.Priority.String(),
			"credential_id", task.CredentialID,
			"model", task.RawModel,
			"error", err)
		return
	}
	if !canProbe {
		// Already being probed or concurrency limit reached
		return
	}

	// Decrypt API key
	var ciphertext []byte
	err = s.db.QueryRow(ctx, `SELECT secret_ciphertext FROM credentials WHERE id = $1`, task.CredentialID).Scan(&ciphertext)
	if err != nil {
		s.markFailing(ctx, task.CredentialID, task.RawModel, "decrypt_error", "failed to load credential")
		return
	}

	apiKey, decErr := decryptCiphertext(ciphertext, s.keyring, s.encKey)
	if decErr != nil {
		s.markFailing(ctx, task.CredentialID, task.RawModel, "decrypt_error", decErr.Error())
		return
	}
	task.APIKey = apiKey

	// Perform the probe
	success, errMsg := s.probeModel(ctx, task)

	if success {
		s.markHealthy(ctx, task.CredentialID, task.RawModel)
	} else {
		s.markFailing(ctx, task.CredentialID, task.RawModel, "probe_failed", errMsg)
	}

	// Small delay between probes to avoid hammering upstreams
	time.Sleep(300 * time.Millisecond)
}

// probeModel performs the actual probe
func (s *UnifiedProbeScheduler) probeModel(ctx context.Context, task ProbeTask) (bool, string) {
	if task.BaseURL == "" {
		return false, "empty base_url"
	}

	desc := providercap.Resolve(task.Protocol, "")

	// Choose probe mode based on protocol
	mode := ProbeModeModelsList
	if desc.Protocol == "anthropic-messages" {
		mode = ProbeModeMessages
	}

	target := probeTarget{
		CredentialID:  int(task.CredentialID),
		RawModel:      task.RawModel,
		OutboundModel: task.OutboundModel,
		BaseURL:       task.BaseURL,
		Protocol:      task.Protocol,
		APIKey:        task.APIKey,
	}

	result := probeWithRetry(ctx, desc, target, mode)
	return result.status == "ok", result.errMsg
}

// ── State Management ────────────────────────────────────────────────────

// markHealthy marks a model as healthy
func (s *UnifiedProbeScheduler) markHealthy(ctx context.Context, credID int64, rawModel string) {
	_, err := s.db.Exec(ctx, `SELECT unified_probe_mark_healthy($1, $2, 0)`, credID, rawModel)
	if err != nil {
		slog.Warn("unified probe: mark healthy failed",
			"credential_id", credID,
			"raw_model", rawModel,
			"error", err)
	}
}

// markFailing marks a model as failing
func (s *UnifiedProbeScheduler) markFailing(ctx context.Context, credID int64, rawModel string, errCode, errMsg string) {
	_, err := s.db.Exec(ctx, `SELECT unified_probe_mark_failing($1, $2, $3, $4, 60)`,
		credID, rawModel, errCode, errMsg)
	if err != nil {
		slog.Warn("unified probe: mark failing failed",
			"credential_id", credID,
			"raw_model", rawModel,
			"error", err)
	}
}

// markSuspicious marks a model as suspicious (for external triggers)
func (s *UnifiedProbeScheduler) markSuspicious(ctx context.Context, credID int64, rawModel string, reason string) { //nolint:unused
	_, err := s.db.Exec(ctx, `SELECT unified_probe_mark_suspicious($1, $2, $3)`,
		credID, rawModel, reason)
	if err != nil {
		slog.Warn("unified probe: mark suspicious failed",
			"credential_id", credID,
			"raw_model", rawModel,
			"error", err)
	}
}

// OnRealRequest handles real-time request feedback (critical feature)
func (s *UnifiedProbeScheduler) OnRealRequest(ctx context.Context, credID int64, rawModel string, success bool, errMsg string) {
	_, err := s.db.Exec(ctx, `SELECT unified_probe_on_real_request($1, $2, $3, $4)`,
		credID, rawModel, success, errMsg)
	if err != nil {
		slog.Warn("unified probe: on real request failed",
			"credential_id", credID,
			"raw_model", rawModel,
			"success", success,
			"error", err)
		return
	}

	if !success {
		slog.Info("unified probe: real request failed, marked urgent",
			"credential_id", credID,
			"raw_model", rawModel,
			"error", errMsg)
	}
}

// ── Concurrency Control ─────────────────────────────────────────────────

// getCredentialSemaphore gets or creates a semaphore for a credential
func (s *UnifiedProbeScheduler) getCredentialSemaphore(credID int64) *semaphore.Weighted {
	s.credentialSemsMu.RLock()
	sem, exists := s.credentialSems[credID]
	s.credentialSemsMu.RUnlock()

	if exists {
		return sem
	}

	s.credentialSemsMu.Lock()
	defer s.credentialSemsMu.Unlock()

	// Double-check after acquiring write lock
	if sem, exists := s.credentialSems[credID]; exists {
		return sem
	}

	sem = semaphore.NewWeighted(MaxCredentialConcurrentProbes)
	s.credentialSems[credID] = sem
	return sem
}

// tryAcquireProbePermission tries to acquire probe permission in DB
func (s *UnifiedProbeScheduler) tryAcquireProbePermission(ctx context.Context, credID int64, rawModel string) (bool, error) {
	var canProbe bool
	err := s.db.QueryRow(ctx,
		`SELECT model_probe_start_probing($1, $2, $3)`,
		credID, rawModel, MaxCredentialConcurrentProbes,
	).Scan(&canProbe)
	return canProbe, err
}

// ── Background Maintenance Tasks ────────────────────────────────────────

// runSuccessRateUpdater updates 7-day success rates hourly
func (s *UnifiedProbeScheduler) runSuccessRateUpdater(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// Run immediately on start
	s.updateSuccessRates(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.updateSuccessRates(ctx)
		}
	}
}

func (s *UnifiedProbeScheduler) updateSuccessRates(ctx context.Context) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT unified_probe_update_success_rate()`).Scan(&count)
	if err != nil {
		slog.Warn("unified probe: update success rates failed", "error", err)
		return
	}
	if count > 0 {
		slog.Debug("unified probe: updated success rates", "count", count)
	}
}

// runDailyCounterReset resets 24h counters daily
func (s *UnifiedProbeScheduler) runDailyCounterReset(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.resetDailyCounters(ctx)
		}
	}
}

func (s *UnifiedProbeScheduler) resetDailyCounters(ctx context.Context) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT unified_probe_reset_daily_counters()`).Scan(&count)
	if err != nil {
		slog.Warn("unified probe: reset daily counters failed", "error", err)
		return
	}
	if count > 0 {
		slog.Info("unified probe: reset daily counters", "count", count)
	}
}
