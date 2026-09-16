package bg

// ScanScheduler — periodic FreeDiscovery scan worker (2026-09-14, R20 §二.6).
//
// 目标：按固定间隔扫描所有租户下 enabled=true 的 provider templates，以
// TriggerScheduled 自动触发 discovery run，保持 discovered_models 表新鲜，
// 不依赖人工点 Trigger。与 admin handler 中的 manual TriggerType=manual
// 共用同一条 DiscoveryEngine.Run → scan → import pipeline。
//
// 隔离：每条扫描独立携带 tenant_id 走 setTenantTx → RLS GUC，不跨租户混合
// 数据；worker 自身用 "default" 查全量 tenant 列表（super-admin 视角）。
//
// 安全性：
//   - 禁用（LLM_GATEWAY_FD_SCAN_SCHEDULER=off）即空转
//   - 每 tick 内逐 template 失败隔离（一条失败不影响其余）
//   - top-level + per-template 双层 panic guard
//   - ticker 间隔下限 1m（防误配 1s 打爆上游）
//   - 同一 template 不并发（inFlight 去重，若上一轮未结束则跳过本轮）
//
// Env knobs:
//   LLM_GATEWAY_FD_SCAN_INTERVAL  — sweep 周期，默认 6h（>=1m）
//   LLM_GATEWAY_FD_SCAN_SCHEDULER — "off"/"false"/"0" 关闭

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/freediscovery"
)

const (
	fdScanDefaultInterval = 6 * time.Hour
	fdScanMinInterval     = 1 * time.Minute
	// fdScanCycleTimeout bounds one full sweep (N templates × HTTP 30s
	// worst case). Matches the admin manual-scan budget.
	fdScanCycleTimeout = 60 * time.Second
)

// ScanScheduler periodically triggers FreeDiscovery scans for all enabled
// templates across all tenants.
type ScanScheduler struct {
	db       *pgxpool.Pool
	engine   *freediscovery.DiscoveryEngine
	tmpl     *freediscovery.TemplateManager
	interval time.Duration
	disabled bool
	// cycleTimeout bounds each sweep (aligns the admin manual path's 60s
	// budget; the scheduler path previously ran engine.Run on a bare
	// workerCtx with no deadline — 2026-09-14 audit D-P2-2).
	cycleTimeout time.Duration

	// inFlight tracks template IDs whose scan is still running; prevents
	// overlapping scans of the same template if one tick overlaps the next.
	inFlight     map[int64]struct{}
	inFlightMu   sync.Mutex
	stopCh       chan struct{}
	stopOnce     sync.Once
	startOnce    sync.Once
	workerWG     sync.WaitGroup
	workerCancel context.CancelFunc
	workerMu     sync.Mutex

	// Health counters for liveness probe (atomic, lock-free reads).
	startedFlag  atomic.Bool // true once Start actually spawned the worker
	lastSweepAt  time.Time
	lastSweepMu  sync.RWMutex
	sweepsTotal  uint64 // atomic — completed sweeps
	cyclesFailed uint64 // atomic — failed or interrupted sweeps
	scansTotal   uint64 // atomic — template scans attempted
	scansFailed  uint64 // atomic — template scans that returned error
	scansSkipped uint64 // atomic — in-flight dedup skips
	lastError    string
	lastErrorMu  sync.RWMutex
}

// NewScanScheduler constructs the worker. db is the pgx pool used for the
// tenant-list query; engine/tmpl are the domain services shared with admin.
func NewScanScheduler(db *pgxpool.Pool, engine *freediscovery.DiscoveryEngine, tmpl *freediscovery.TemplateManager) *ScanScheduler {
	interval := fdScanDefaultInterval
	if v := os.Getenv("LLM_GATEWAY_FD_SCAN_INTERVAL"); v != "" {
		var parsed time.Duration
		if d, err := time.ParseDuration(v); err == nil {
			parsed = d
		} else if n, err := strconv.Atoi(v); err == nil && n > 0 {
			parsed = time.Duration(n) * time.Minute
		}
		if parsed > 0 {
			// Clamp sub-minute intervals up to the floor to prevent
			// misconfiguration (e.g. "5s") from hammering upstream APIs.
			if parsed < fdScanMinInterval {
				parsed = fdScanMinInterval
			}
			interval = parsed
		}
	}
	disabled := false
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_FD_SCAN_SCHEDULER"))) {
	case "off", "false", "0":
		disabled = true
	}
	return &ScanScheduler{
		db:           db,
		engine:       engine,
		tmpl:         tmpl,
		interval:     interval,
		cycleTimeout: fdScanCycleTimeout,
		disabled:     disabled,
		inFlight:     make(map[int64]struct{}),
		stopCh:       make(chan struct{}),
	}
}

func (s *ScanScheduler) Start(ctx context.Context) {
	if s == nil || s.disabled || s.db == nil || s.engine == nil || s.tmpl == nil {
		slog.Info("scan_scheduler disabled or not fully wired")
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.startOnce.Do(func() {
		workerCtx, cancel := context.WithCancel(ctx)
		s.workerMu.Lock()
		s.workerCancel = cancel
		s.workerMu.Unlock()
		s.startedFlag.Store(true)
		slog.Info("scan_scheduler started", "interval", s.interval)
		s.workerWG.Add(1)
		go func() {
			defer s.workerWG.Done()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("scan_scheduler worker panicked", "panic", r, "stack", string(debug.Stack()))
					s.recordCycleError(fmt.Errorf("scan_scheduler worker panic: %v", r))
				}
			}()
			// Stop-before-Start guard: a Stop() that landed before the
			// goroutine got scheduled must not trigger a full upstream
			// sweep against the operator's intent (2026-09-14 audit A-P3-1).
			if s.stopRequested(workerCtx) {
				slog.Info("scan_scheduler stopped before initial cycle")
				return
			}
			// Run once at startup so a fresh process does not wait six
			// hours. Bounded retry ladder (30s/60s): a transient startup
			// failure (migration race, keyring not ready) must not create
			// a six-hour blind spot (2026-09-14 audit D-P2-2).
			initialDelays := []time.Duration{30 * time.Second, 60 * time.Second}
			for attempt := 0; ; attempt++ {
				err := s.safeCycle(workerCtx)
				if err == nil {
					break
				}
				s.recordCycleError(err)
				slog.Warn("scan_scheduler initial cycle failed",
					"error", err, "attempt", attempt+1)
				if attempt >= len(initialDelays) {
					break
				}
				select {
				case <-workerCtx.Done():
					return
				case <-s.stopCh:
					return
				case <-time.After(initialDelays[attempt]):
				}
			}
			ticker := time.NewTicker(s.interval)
			defer ticker.Stop()
			for {
				select {
				case <-workerCtx.Done():
					slog.Info("scan_scheduler stopping")
					return
				case <-s.stopCh:
					slog.Info("scan_scheduler stopped")
					return
				case <-ticker.C:
					if err := s.safeCycle(workerCtx); err != nil {
						s.recordCycleError(err)
						slog.Warn("scan_scheduler cycle failed", "error", err)
					}
				}
			}
		}()
	})
}

// stopRequested reports whether Stop()/ctx cancellation already happened.
func (s *ScanScheduler) stopRequested(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

func (s *ScanScheduler) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.workerMu.Lock()
	cancel := s.workerCancel
	s.workerMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.workerWG.Wait()
}

// safeCycle runs one sweep with a per-cycle panic guard and a bounded
// wall-clock budget (fdScanCycleTimeout; zero-value tolerant for struct
// literals in tests).
func (s *ScanScheduler) safeCycle(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("scan_scheduler cycle panicked", "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("scan_scheduler cycle panic: %v", r)
		}
	}()
	budget := s.cycleTimeout
	if budget <= 0 {
		budget = fdScanCycleTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return s.cycle(runCtx)
}

// CycleNow runs one sweep synchronously — test/ops entry point.
func (s *ScanScheduler) CycleNow(ctx context.Context) error {
	if s == nil || s.disabled {
		return nil
	}
	if s.db == nil || s.engine == nil || s.tmpl == nil {
		err := errors.New("scan_scheduler: not fully wired")
		s.recordCycleError(err)
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := s.safeCycle(ctx)
	if err != nil {
		s.recordCycleError(err)
	}
	return err
}

// LastSweepAt returns the timestamp of the most recent completed sweep.
func (s *ScanScheduler) LastSweepAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.lastSweepMu.RLock()
	defer s.lastSweepMu.RUnlock()
	return s.lastSweepAt
}

// cycle queries all (tenant_id, template_id) pairs with enabled=true, then
// triggers a scheduled scan for each. A per-template failure is logged and
// skipped; the sweep continues.
func (s *ScanScheduler) cycle(ctx context.Context) error {
	if s.db == nil {
		return errors.New("scan_scheduler: database is nil")
	}
	// Enumerate under an explicit privileged transaction. provider_templates is
	// RLS-protected; without these LOCAL settings a pool connection sees only
	// the default tenant and the worker silently misses every other tenant.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("scan_scheduler: begin list transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_role', 'super_admin', true)`); err != nil {
		return fmt.Errorf("scan_scheduler: set super-admin role: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.bypass_rls', 'true', true)`); err != nil {
		return fmt.Errorf("scan_scheduler: enable rls bypass: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT tenant_id, id, provider_code
		FROM public.provider_templates
		WHERE enabled = TRUE
		ORDER BY tenant_id, provider_code`)
	if err != nil {
		return fmt.Errorf("scan_scheduler: list enabled templates: %w", err)
	}

	type target struct {
		tenantID     string
		templateID   int64
		providerCode string
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.tenantID, &t.templateID, &t.providerCode); err != nil {
			rows.Close()
			return fmt.Errorf("scan_scheduler: scan row: %w", err)
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("scan_scheduler: rows.Err: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("scan_scheduler: commit list transaction: %w", err)
	}

	slog.Info("scan_scheduler sweep", "templates", len(targets))
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			slog.Info("scan_scheduler context cancelled, aborting sweep", "remaining", len(targets))
			return err
		}
		if !s.tryAcquire(t.templateID) {
			atomic.AddUint64(&s.scansSkipped, 1)
			slog.Warn("scan_scheduler skip in-flight template",
				"template_id", t.templateID, "provider", t.providerCode)
			continue
		}
		atomic.AddUint64(&s.scansTotal, 1)
		// Per-template panic guard: a bad template must not kill the sweep.
		func() {
			defer func() {
				if r := recover(); r != nil {
					err := fmt.Errorf("template %d panic: %v", t.templateID, r)
					atomic.AddUint64(&s.scansFailed, 1)
					s.setLastError(err.Error())
					s.recordScanFailure(ctx, t.tenantID, t.templateID)
					slog.Error("scan_scheduler template panic",
						"template_id", t.templateID, "provider", t.providerCode,
						"panic", r, "stack", string(debug.Stack()))
				}
				s.release(t.templateID)
			}()
			task, err := s.engine.Run(ctx, freediscovery.DiscoveryRequest{
				TemplateID:  t.templateID,
				TenantID:    t.tenantID,
				TriggeredBy: "scan_scheduler",
				TriggerType: freediscovery.TriggerScheduled,
			})
			if err != nil {
				// ErrTemplateDisabled can race if a template was disabled
				// between our SELECT and the Run; log at info to avoid noise.
				if errors.Is(err, freediscovery.ErrTemplateDisabled) {
					slog.Info("scan_scheduler template disabled mid-sweep",
						"template_id", t.templateID, "provider", t.providerCode)
					return
				}
				atomic.AddUint64(&s.scansFailed, 1)
				s.setLastError(err.Error())
				s.recordScanFailure(ctx, t.tenantID, t.templateID)
				slog.Warn("scan_scheduler template scan failed",
					"template_id", t.templateID, "provider", t.providerCode, "error", err)
				return
			}
			// Success: reset failure counter
			if task != nil && task.Status == freediscovery.TaskStatusSuccess {
				s.recordScanSuccess(ctx, t.tenantID, t.templateID)
			}
		}()
	}

	s.lastSweepMu.Lock()
	s.lastSweepAt = time.Now().UTC()
	s.lastSweepMu.Unlock()
	atomic.AddUint64(&s.sweepsTotal, 1)
	return nil
}

// runPrivileged runs fn inside a transaction with the same RLS bypass used by
// cycle(). provider_templates is RLS-protected; without these LOCAL settings a
// pool connection only sees the default tenant, so health feedback for any
// other tenant would silently no-op (0 rows) and the auto-disable threshold
// would never trigger.
func (s *ScanScheduler) runPrivileged(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("scan_scheduler: begin health transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_role', 'super_admin', true)`); err != nil {
		return fmt.Errorf("scan_scheduler: set super-admin role: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.bypass_rls', 'true', true)`); err != nil {
		return fmt.Errorf("scan_scheduler: enable rls bypass: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// recordScanSuccess resets health feedback after a successful scan.
func (s *ScanScheduler) recordScanSuccess(ctx context.Context, tenantID string, templateID int64) {
	if s == nil || s.db == nil {
		return
	}
	err := s.runPrivileged(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE public.provider_templates
			SET consecutive_scan_failures = 0,
			    last_scan_failure_at = NULL
			WHERE id = $1 AND tenant_id = $2`, templateID, tenantID)
		return err
	})
	if err != nil {
		slog.Warn("scan_scheduler: reset template health failed", "template_id", templateID, "error", err)
	}
}

// recordScanFailure increments the consecutive failure counter and disables a
// template at the threshold. The WHERE clause keeps tenant ownership explicit.
func (s *ScanScheduler) recordScanFailure(ctx context.Context, tenantID string, templateID int64) {
	if s == nil || s.db == nil {
		return
	}
	var failures int
	var disabled bool
	err := s.runPrivileged(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			UPDATE public.provider_templates
			SET consecutive_scan_failures = COALESCE(consecutive_scan_failures, 0) + 1,
			    last_scan_failure_at = now(),
			    enabled = CASE WHEN COALESCE(consecutive_scan_failures, 0) + 1 >= 3 THEN FALSE ELSE enabled END,
			    auto_disabled_at = CASE
			        WHEN COALESCE(consecutive_scan_failures, 0) + 1 >= 3 THEN COALESCE(auto_disabled_at, now())
			        ELSE auto_disabled_at
			    END
			WHERE id = $1 AND tenant_id = $2
			RETURNING consecutive_scan_failures, enabled = FALSE`, templateID, tenantID).Scan(&failures, &disabled)
	})
	if err != nil {
		slog.Warn("scan_scheduler: record template health failed", "template_id", templateID, "error", err)
		return
	}
	if disabled {
		slog.Warn("freediscovery: template auto-disabled", "template_id", templateID, "tenant_id", tenantID, "consecutive_failures", failures)
	}
}

// ScanSchedulerStatus is the liveness/health snapshot for the admin probe.
type ScanSchedulerStatus struct {
	Enabled      bool      `json:"enabled"`
	Started      bool      `json:"started"` // worker actually running (vs env-enabled only)
	Interval     string    `json:"interval"`
	LastSweepAt  time.Time `json:"last_sweep_at"`
	SweepsTotal  uint64    `json:"sweeps_total"`
	ScansTotal   uint64    `json:"scans_total"`
	ScansFailed  uint64    `json:"scans_failed"`
	ScansSkipped uint64    `json:"scans_skipped"`
	CyclesFailed uint64    `json:"cycles_failed"`
	LastError    string    `json:"last_error,omitempty"`
}

// Status returns a liveness snapshot for the admin health probe. Returns
// any so the admin package can consume it without importing bg.
func (s *ScanScheduler) Status() any {
	if s == nil {
		return ScanSchedulerStatus{Enabled: false}
	}
	s.lastSweepMu.RLock()
	ls := s.lastSweepAt
	s.lastSweepMu.RUnlock()
	s.lastErrorMu.RLock()
	le := s.lastError
	s.lastErrorMu.RUnlock()
	return ScanSchedulerStatus{
		Enabled:      !s.disabled,
		Started:      s.startedFlag.Load(),
		Interval:     s.interval.String(),
		LastSweepAt:  ls,
		SweepsTotal:  atomic.LoadUint64(&s.sweepsTotal),
		ScansTotal:   atomic.LoadUint64(&s.scansTotal),
		ScansFailed:  atomic.LoadUint64(&s.scansFailed),
		ScansSkipped: atomic.LoadUint64(&s.scansSkipped),
		CyclesFailed: atomic.LoadUint64(&s.cyclesFailed),
		LastError:    le,
	}
}

func (s *ScanScheduler) setLastError(msg string) {
	s.lastErrorMu.Lock()
	s.lastError = msg
	s.lastErrorMu.Unlock()
}

func (s *ScanScheduler) recordCycleError(err error) {
	if s == nil || err == nil {
		return
	}
	atomic.AddUint64(&s.cyclesFailed, 1)
	s.setLastError(err.Error())
}

// tryAcquire returns false if the template is already being scanned.
func (s *ScanScheduler) tryAcquire(templateID int64) bool {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	if _, ok := s.inFlight[templateID]; ok {
		return false
	}
	s.inFlight[templateID] = struct{}{}
	return true
}

func (s *ScanScheduler) release(templateID int64) {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	delete(s.inFlight, templateID)
}
