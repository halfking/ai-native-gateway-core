// Package bg/systemmonitor — monitor.go
//
// SystemMonitor 顶层结构：组装 Queue / InflightDedup / Executor / Worker Pool，
// 提供唯一对外接口 Submit() + Start()/Stop()。
// 设计依据: docs/会话优化v2/32-系统监测模块设计.md §6.2
package systemmonitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// SystemMonitor 是系统监测模块的唯一对外入口（rule 04 §1 红线）。
//
// 任何模块要做节点探测，必须调用 SystemMonitor.Submit(task)，不允许直接
// probeDirect 调上游。Phase 2 收敛所有 worker 后此约束是硬性的。
type SystemMonitor struct {
	queue    *Queue
	dedup    *InflightDedup
	executor *Executor
	audit    *Audit

	// Phase 3: migration metrics collector
	metricsCollector *MetricsCollector

	concurrency int    // 全局并发上限（从 self_check_settings 读）
	workerCount int    // 本机 worker 数（env LLM_GATEWAY_MONITOR_WORKERS_PER_NODE）
	workerID    string // 本机 worker_id（hostname:pid:uuid）

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	// fallback: Redis 不可达时降级为本地内存 FIFO
	fallback   bool
	fallbackMu sync.Mutex
	fallbackCh chan *Task // buffered, len = max(concurrency * 4, 1000)

	// 2026-07-28 (audit follow-up #1, docs/architecture/2026-07-28-routing-state-anomaly-audit.md
	// §4.1): on persistent Redis health failures, the monitor may close
	// the URSM v2 authoritative gate so the router falls back to
	// LegacyStateBackend during the incident. recoveryGate is optional;
	// when nil the auto-close wiring is disabled (default behaviour
	// before this change).
	recoveryGate          RecoveryGate
	recoveryFailThreshold int           // consecutive ping failures to trigger; default 3
	recoveryDebounceTTL   time.Duration // debounce window for cluster-wide MarkClosedDebounced; default 5m
	healthMu              sync.Mutex
	consecutiveFailures   int // protected by healthMu; never hold it across gate/Redis I/O

	// pingFn is an optional test seam that overrides the production
	// dedup.Ping. nil in production; tests inject a stub here to drive
	// checkRedisHealthOnce without standing up Redis.
	pingFn func(ctx context.Context) error

	// 2026-07-29 (audit follow-up #2,
	// docs/architecture/2026-07-28-routing-state-anomaly-audit.md §4.2):
	// durable backstop for fallback-mode tasks. When Submit's Redis
	// path fails we currently write the task to fallbackCh (in-memory
	// only) — if the process restarts during a fallback window, every
	// queued task is lost. With fallbackDB wired, we also INSERT the
	// task JSON into system_monitor_fallback_queue (PG), and on the
	// fallback → healthy transition healthCheckLoop drains the table
	// back into Redis. nil disables the durable path; legacy
	// behaviour is preserved for tests / disabled-DB deployments.
	fallbackDB fallbackDBIface
}

// fallbackDBIface is the minimum contract systemmonitor needs from
// the durable backstop pool. Production wires *pgxpool.Pool; tests
// use pgxmock.PgxPoolIface. Keeping this here (not in a public
// types file) signals that the durable backstop is an internal
// implementation detail — admin endpoints should not depend on
// the fallback queue schema.
type fallbackDBIface interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// RecoveryGate is the interface SystemMonitor uses to flip the URSM v2
// authoritative gate on persistent Redis health failures and to
// reopen it on Redis recovery. Production wires it to
// domains/ursm/v2.Manager; tests use a stub. The interface lives in
// bg/systemmonitor to avoid an import cycle (recovery lives in
// domains/ursm/v2/recovery which is already a dependency of
// bg/systemmonitor transitively through the Config).
//
// Methods form the audit follow-up #6 incident lifecycle:
//   - MarkClosedDebounced: auto-close on persistent Redis health failure
//     (follow-up #1, cluster-debounced via SETNX+TTL)
//   - RestoreIfClosed: auto-reopen on Redis recovery (follow-up #6,
//     idempotent; no-op when gate is already open)
//   - Stats: observability snapshot for admin dashboards (follow-up #4).
//     Returns the zero value when no operation has happened yet or
//     when the receiver is nil.
type RecoveryGate interface {
	MarkClosedDebounced(ctx context.Context, reason string, debounceTTL time.Duration) (bool, error)
	RestoreIfClosed(ctx context.Context) (int, error)
	Stats() RecoveryStats
}

// RecoveryStats is the audit follow-up #4 observability surface:
// combines the recovery gate's last-error / last-recovery metadata
// (from the gate itself) with the monitor's own consecutive-failure
// counter + fail threshold (per-instance state). Safe on a nil
// receiver — returns the zero value when no gate is wired.
//
// Note on field naming: we duplicate fields from
// domains/ursm/v2/recovery.Stats intentionally rather than import
// the recovery package, because that would create an import cycle
// (monitor → recovery → systemmonitor via the wired chain). The
// adapter (cmd/gateway/system_monitor_adapter.go) does the typed
// field-by-field conversion at wire time.
type RecoveryStats struct {
	// LastError + LastErrorAt: the most recent gate operation failure.
	// Empty/zero on success or when no operation has happened yet.
	LastError   string
	LastErrorAt time.Time
	// LastRecoveryAt: timestamp of the most recent successful reopen.
	// Zero when no recovery has happened yet.
	LastRecoveryAt time.Time
	// LastRecoveryKeyCount: the key count observed on the most recent
	// successful reopen. 0 when no recovery has happened yet.
	LastRecoveryKeyCount int
	// ConsecutiveFailures + FailThreshold: monitor-internal state
	// included so admin dashboards can render "X / Y failures" +
	// gate state in a single round-trip.
	ConsecutiveFailures int
	FailThreshold       int
}

// DefaultRecoveryFailureThreshold is the default number of consecutive
// 15s health-check ticks (i.e. ~45s of unreachable Redis) before the
// monitor calls MarkClosedDebounced. Overridable per-instance via
// Config.RecoveryFailureThreshold.
const DefaultRecoveryFailureThreshold = 3

// DefaultRecoveryDebounceTTL caps the cluster's epoch counter inflation
// to one bump per TTL regardless of how many gateway instances observe
// the same failure event simultaneously. 5m is long enough that a brief
// Redis flap will not blow up the audit trail, but short enough that
// the next failure event is captured within an operator's incident
// window.
const DefaultRecoveryDebounceTTL = 5 * time.Minute

// Config holds the wiring parameters.
type Config struct {
	DB          *pgxpool.Pool
	Redis       *redis.Client
	Keyring     interface{}
	EncKey      []byte
	ProxyFunc   func(*http.Request) (*url.URL, error)
	TimeoutMs   int
	Concurrency int
	WorkerCount int
	WorkerID    string
	// RecoveryGate is the optional auto-close wiring for the URSM v2
	// authoritative gate. When non-nil, the monitor will close the gate
	// after Config.RecoveryFailureThreshold consecutive ping failures.
	RecoveryGate RecoveryGate
	// RecoveryFailureThreshold overrides DefaultRecoveryFailureThreshold
	// when > 0. Tests use this to drive the auto-close path without
	// waiting for real 15s ticks.
	RecoveryFailureThreshold int
	// RecoveryDebounceTTL overrides DefaultRecoveryDebounceTTL when > 0.
	RecoveryDebounceTTL time.Duration
	// FallbackDB (audit follow-up #2) wires the durable backstop
	// pool. When non-nil, fallback-mode tasks are also INSERTed into
	// system_monitor_fallback_queue so they survive a process
	// restart during the fallback window. nil preserves the legacy
	// in-memory-only behaviour.
	FallbackDB *pgxpool.Pool
}

// NewSystemMonitor constructs the monitor and loads the embedded Lua scripts.
//
// concurrency=0 defaults to 5 (design §3.3). workerCount=0 defaults to 2.
// Redis may be nil; in that case the monitor starts in fallback mode.
func NewSystemMonitor(cfg Config) (*SystemMonitor, error) {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 5
	}
	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = 2
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID = fmt.Sprintf("%s-%s", hostnameOrDefault(), uuid.NewString()[:8])
	}

	// Load Lua scripts (Phase 1 only loads if Redis is present)
	var scripts *LoadedScripts
	if cfg.Redis != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s, err := LoadScripts(ctx, cfg.Redis)
		if err != nil {
			slog.Warn("system_monitor: lua scripts load failed, will retry on first Submit",
				"error", err)
			// continue with nil scripts — Submit will retry LoadScripts lazily
		} else {
			scripts = s
		}
	}

	sm := &SystemMonitor{
		queue:                 NewQueue(cfg.Redis, scripts),
		dedup:                 NewInflightDedup(cfg.Redis),
		executor:              NewExecutor(ExecutorConfig{DB: cfg.DB, Keyring: nil, EncKey: cfg.EncKey, ProxyFunc: cfg.ProxyFunc, TimeoutMs: cfg.TimeoutMs}),
		audit:                 NewAudit(cfg.DB),
		metricsCollector:      NewMetricsCollector(cfg.DB),
		concurrency:           cfg.Concurrency,
		workerCount:           cfg.WorkerCount,
		workerID:              cfg.WorkerID,
		stopCh:                make(chan struct{}),
		fallbackCh:            make(chan *Task, maxInt(cfg.Concurrency*4, 1000)),
		recoveryGate:          cfg.RecoveryGate,
		recoveryFailThreshold: cfg.RecoveryFailureThreshold,
		recoveryDebounceTTL:   cfg.RecoveryDebounceTTL,
	}

	// Apply defaults to the recovery wiring. Centralised here so tests
	// and production share the exact same fallback semantics.
	if sm.recoveryFailThreshold <= 0 {
		sm.recoveryFailThreshold = DefaultRecoveryFailureThreshold
	}
	if sm.recoveryDebounceTTL <= 0 {
		sm.recoveryDebounceTTL = DefaultRecoveryDebounceTTL
	}

	if cfg.Redis == nil {
		sm.fallback = true
		slog.Warn("system_monitor: redis disabled, starting in fallback mode (memory FIFO only)")
	}
	// Audit follow-up #2: durable backstop wiring. If both DB and
	// FallbackDB are nil, fall back to legacy in-memory-only behaviour.
	sm.fallbackDB = cfg.FallbackDB
	if sm.fallbackDB != nil {
		slog.Info("system_monitor: durable fallback backstop enabled (system_monitor_fallback_queue)")
	}
	return sm, nil
}

// Submit enqueues a task. The caller is the only ingress point for new
// probe work — see design §1.2 #1 (唯一入口).
//
// Returns the assigned task_id, or an error if validation fails or the
// backend is unreachable.
func (sm *SystemMonitor) Submit(ctx context.Context, task *Task) (int64, error) {
	if err := task.Validate(); err != nil {
		return 0, fmt.Errorf("submit: %w", err)
	}
	// Ensure worker_id is in context so claim.lua records who claimed it.
	ctx = contextWithWorkerID(ctx, sm.workerID)

	if sm.fallback {
		id, err := sm.submitFallback(task)
		if err == nil {
			sm.publishFallbackDurable(ctx, task)
			sm.publishEvent(ctx, "submitted", task)
		}
		return id, err
	}
	if err := sm.queue.Submit(ctx, task); err != nil {
		// Try once: lazy re-load of Lua scripts (may have been evicted).
		if scripts, lerr := LoadScripts(ctx, sm.queue.rdb); lerr == nil {
			sm.queue.setScripts(scripts) // 2026-07-24 审计修复：atomic 替换避免数据竞争
			if err2 := sm.queue.Submit(ctx, task); err2 == nil {
				sm.publishEvent(ctx, "submitted", task)
				return task.ID, nil
			}
		}
		// Fallback: log warn and insert into memory queue (don't block caller).
		slog.Warn("system_monitor: redis submit failed, falling back to memory queue",
			"error", err, "task_id", task.ID)
		sm.markFallback()
		id, fallbackErr := sm.submitFallback(task)
		if fallbackErr == nil {
			sm.publishFallbackDurable(ctx, task)
			sm.publishEvent(ctx, "submitted", task)
		}
		return id, fallbackErr
	}
	sm.publishEvent(ctx, "submitted", task)
	return task.ID, nil
}

// publishFallbackDurable is the audit follow-up #2 durable backstop
// path. When a task is enqueued in fallback mode, we ALSO INSERT its
// JSON into system_monitor_fallback_queue (best-effort, log on
// failure) so the task survives a process restart during the
// fallback window. On the fallback → healthy transition,
// healthCheckLoop drains the table back into Redis.
//
// Idempotent via ON CONFLICT (task_id) DO NOTHING: if the same task
// id is submitted twice (multi-instance race during fallback), only
// one row survives.
func (sm *SystemMonitor) publishFallbackDurable(ctx context.Context, task *Task) {
	if sm.fallbackDB == nil || task == nil {
		return
	}
	payload, err := json.Marshal(task)
	if err != nil {
		slog.Warn("system_monitor: fallback durable marshal failed",
			"error", err, "task_id", task.ID)
		return
	}
	insertCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = sm.fallbackDB.Exec(insertCtx, `
		INSERT INTO system_monitor_fallback_queue
			(task_id, task_json, worker_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (task_id) DO NOTHING
	`, task.ID, payload, sm.workerID)
	if err != nil {
		slog.Warn("system_monitor: fallback durable insert failed",
			"error", err, "task_id", task.ID)
	}
}

// drainFallbackQueue is called on the fallback → healthy transition
// to re-push persisted fallback tasks back into Redis. We use SELECT
// FOR UPDATE SKIP LOCKED so concurrent gateway instances don't fight
// over the same rows; each row is owned by exactly one instance per
// drain cycle. After LPUSH we DELETE the row.
//
// Best-effort: errors are logged and skipped — the next drain cycle
// retries any leftover rows (drain is idempotent because we DELETE
// only after successful LPUSH).
func (sm *SystemMonitor) drainFallbackQueue(ctx context.Context) (int, error) {
	if sm.fallbackDB == nil || sm.queue == nil || sm.queue.rdb == nil {
		return 0, nil
	}
	drainCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := sm.fallbackDB.Begin(drainCtx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(drainCtx)
	rows, err := tx.Query(drainCtx, `
		SELECT id, task_json
		FROM system_monitor_fallback_queue
		ORDER BY enqueued_at ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 100
	`)
	if err != nil {
		return 0, fmt.Errorf("select: %w", err)
	}
	type drained struct {
		id      int64
		payload []byte
	}
	var batch []drained
	for rows.Next() {
		var d drained
		if err := rows.Scan(&d.id, &d.payload); err != nil {
			slog.Warn("system_monitor: fallback durable scan failed", "error", err)
			continue
		}
		batch = append(batch, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate: %w", err)
	}
	drainedCount := 0
	for _, d := range batch {
		if err := sm.queue.rdb.LPush(drainCtx, RedisKeyQueue, d.payload).Err(); err != nil {
			slog.Warn("system_monitor: fallback durable LPUSH failed",
				"error", err, "id", d.id)
			continue
		}
		if _, err := tx.Exec(drainCtx,
			`DELETE FROM system_monitor_fallback_queue WHERE id = $1`, d.id,
		); err != nil {
			slog.Warn("system_monitor: fallback durable DELETE failed",
				"error", err, "id", d.id)
			continue
		}
		drainedCount++
	}
	if err := tx.Commit(drainCtx); err != nil {
		return drainedCount, fmt.Errorf("commit: %w", err)
	}
	return drainedCount, nil
}

// submitFallback puts the task into the in-memory channel. Used when Redis
// is unreachable so the gateway keeps accepting probes (best-effort).
func (sm *SystemMonitor) submitFallback(task *Task) (int64, error) {
	select {
	case sm.fallbackCh <- task:
		return task.ID, nil
	default:
		return 0, errors.New("fallback queue full (memory FIFO)")
	}
}

func (sm *SystemMonitor) markFallback() {
	sm.fallbackMu.Lock()
	defer sm.fallbackMu.Unlock()
	sm.fallback = true
}

func (sm *SystemMonitor) clearFallback() {
	sm.fallbackMu.Lock()
	defer sm.fallbackMu.Unlock()
	sm.fallback = false
}

func (sm *SystemMonitor) publishEvent(ctx context.Context, eventType string, task *Task) {
	if sm == nil || sm.queue == nil || sm.queue.rdb == nil || task == nil {
		return
	}
	if eventType == string(TaskStatusSuccess) {
		eventType = "completed"
	}
	payload, err := json.Marshal(map[string]any{
		"type": eventType,
		"ts":   time.Now().UTC(),
		"task": map[string]any{
			"id": task.ID, "task_type": task.TaskType, "automaticity": task.Automaticity,
			"status": task.Status, "attempt": task.Attempt, "max_attempts": task.MaxAttempts,
			"credential_id": task.CredentialID, "provider_id": task.ProviderID,
			"raw_model": task.RawModel, "source": task.Source, "worker_id": task.WorkerID,
			"http_status": task.HTTPStatus, "latency_ms": task.LatencyMs,
			"err_code": task.ErrCode, "total_tokens": task.TokenCount,
		},
	})
	if err != nil {
		return
	}
	publishCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sm.queue.rdb.Publish(publishCtx, RedisKeyEventsPub, payload).Err(); err != nil {
		slog.Debug("system_monitor: publish event failed", "event", eventType, "error", err)
	}
}

// publishRecoveryEvent emits a recovery-state-transition event on
// the Redis Pub/Sub channel that admin/systemmonitor_stream_sse.go
// consumes. Audit follow-up #5: lets dashboards show "recovery
// gate closed at T1, reopened at T2" in real time instead of polling
// /api/admin/system-monitor/recovery.
//
// Event shape:
//
//	{
//	  "type": "recovery_closed" | "recovery_reopened",
//	  "ts":   "RFC3339",
//	  "recovery": {
//	    "reason":           "redis_unavailable",
//	    "consecutive_failures": 3,
//	    "threshold":        3,
//	    "debounce_ttl":     "5m0s",
//	    "worker_id":        "...",
//	    "keys_re_warmed":   7  // only on reopened
//	  }
//	}
//
// Best-effort: publish failures are logged and swallowed.
func (sm *SystemMonitor) publishRecoveryEvent(ctx context.Context, eventType string, details map[string]any) {
	if sm == nil || sm.queue == nil || sm.queue.rdb == nil {
		return
	}
	if details == nil {
		details = map[string]any{}
	}
	details["worker_id"] = sm.workerID
	payload, err := json.Marshal(map[string]any{
		"type":     eventType,
		"ts":       time.Now().UTC(),
		"recovery": details,
	})
	if err != nil {
		return
	}
	publishCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sm.queue.rdb.Publish(publishCtx, RedisKeyEventsPub, payload).Err(); err != nil {
		slog.Debug("system_monitor: publish recovery event failed",
			"event", eventType, "error", err)
	}
}

// IsFallback reports whether the monitor is currently degraded.
func (sm *SystemMonitor) IsFallback() bool {
	sm.fallbackMu.Lock()
	defer sm.fallbackMu.Unlock()
	return sm.fallback
}

// Start launches workerCount goroutines that consume from the queue.
//
// Each worker runs the inner loop:
//  1. claim (atomic Lua)
//  2. if automatic + recent success → mark skipped + continue
//  3. executor.Execute(task)
//  4. audit + Complete (Redis status update)
//  5. on failure: Requeue with backoff (if attempt < max_attempts)
//
// Workers do NOT block on Redis unavailability; they fall back to the
// in-memory channel and log a warning.
func (sm *SystemMonitor) Start(ctx context.Context) {
	if sm == nil {
		return
	}
	slog.Info("system_monitor: starting",
		"worker_id", sm.workerID,
		"worker_count", sm.workerCount,
		"concurrency", sm.concurrency,
		"fallback", sm.fallback,
	)
	for i := 0; i < sm.workerCount; i++ {
		sm.wg.Add(1)
		go sm.workerLoop(ctx, i)
	}
	sm.wg.Add(1)
	go sm.healthCheckLoop(ctx)
}

// Stop gracefully shuts down workers. Pending tasks in the in-memory
// fallback channel are NOT preserved; Redis-queued tasks persist.
func (sm *SystemMonitor) Stop() {
	if sm == nil {
		return
	}
	sm.stopOnce.Do(func() { close(sm.stopCh) })
	sm.wg.Wait()
}

// workerLoop is the per-worker consumption loop.
//
// Polls Redis via claim.lua; when in fallback mode drains the in-memory
// channel instead. Includes the 5-minute auto-skip rule (design §4.3).
func (sm *SystemMonitor) workerLoop(ctx context.Context, idx int) {
	defer sm.wg.Done()
	workerLog := slog.With("worker_id", sm.workerID, "worker_idx", idx)
	workerLog.Info("system_monitor: worker started")

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			workerLog.Info("system_monitor: worker ctx cancelled, exiting")
			return
		case <-sm.stopCh:
			workerLog.Info("system_monitor: worker stop signaled, exiting")
			return
		case <-ticker.C:
		}

		task, ok := sm.fetchTask(ctx, workerLog)
		if !ok {
			continue
		}
		sm.processTask(ctx, task, workerLog)
	}
}

// fetchTask pulls the next eligible task. Returns (nil, false) when no
// task is available (queue empty in both Redis and fallback modes).
func (sm *SystemMonitor) fetchTask(ctx context.Context, workerLog *slog.Logger) (*Task, bool) {
	if !sm.IsFallback() {
		// 2026-07-24 审计修复：credID/rawModel 不再由调用方提供（解码前未知），
		// inflight key 在 claim.lua 内从任务 JSON 构造。修复前传 (0,"") 被守卫
		// 拒绝 → 队列永不消费 → fallback 抖动。
		task, err := sm.queue.Claim(ctx)
		if err != nil {
			workerLog.Warn("system_monitor: claim failed", "error", err)
			sm.markFallback()
		} else if task != nil {
			return task, true
		} else {
			// 2026-07-27: add debug log to distinguish queue-empty (nil, nil)
			// from real errors (nil, err). Previously all cases hit the err
			// path because claim.lua returned Lua false → redis.Nil error.
			workerLog.Debug("system_monitor: claim returned empty, queue is likely empty")
		}
	}
	// Fallback path: drain in-memory channel (non-blocking).
	select {
	case t := <-sm.fallbackCh:
		return t, true
	default:
	}
	return nil, false
}

// processTask handles a single claimed task end-to-end.
//
// Order matters:
//  1. 5-min auto-skip check (only automaticity=automatic)
//  2. Execute (real probe)
//  3. Audit write (system_probe_runs)
//  4. Complete (Redis hash + running set)
//  5. On failure: Requeue with backoff (if attempts remain)
func (sm *SystemMonitor) processTask(ctx context.Context, task *Task, workerLog *slog.Logger) {
	task.WorkerID = sm.workerID
	now := time.Now().UTC()
	task.StartedAt = &now
	task.Status = TaskStatusRunning
	task.Attempt++
	sm.publishEvent(ctx, "started", task)

	// 5-min auto-skip
	if task.Automaticity == AutomaticityAutomatic {
		info, err := sm.dedup.ShouldSkipAutoTask(ctx, task.CredentialID, task.RawModel)
		if err != nil {
			workerLog.Warn("system_monitor: recent_success check failed",
				"task_id", task.ID, "error", err)
		}
		if info != nil {
			// Skip: audit + complete
			extras := map[string]any{
				"skip_reason":       string(SkipReasonRecentRequestSuccess),
				"recent_request_id": info.RequestID,
				"http_status":       info.HTTPStatus,
				"latency_ms":        info.LatencyMs,
			}
			if !info.At.IsZero() {
				extras["recent_request_at"] = info.At.UTC().Format(time.RFC3339Nano)
			}
			finish := time.Now().UTC()
			task.FinishedAt = &finish
			task.Status = TaskStatusSkipped
			task.SkipReason = SkipReasonRecentRequestSuccess
			task.RecentRequestID = info.RequestID
			task.RecentRequestAt = &info.At
			sm.publishEvent(ctx, "skipped", task)

			if err := sm.audit.Write(ctx, task, nil, extras); err != nil {
				workerLog.Warn("system_monitor: audit skip write failed", "error", err)
			}
			if !sm.IsFallback() {
				if err := sm.queue.Complete(ctx, task, TaskStatusSkipped, extras); err != nil {
					workerLog.Warn("system_monitor: complete skip failed", "error", err)
				}
				// Refresh 30s inflight so subsequent tasks in the window get dedup'd.
				_ = sm.dedup.MarkInflightSkip(ctx, task.CredentialID, task.RawModel)
			}
			workerLog.Info("system_monitor: task skipped (recent success)",
				"task_id", task.ID,
				"credential_id", task.CredentialID,
				"raw_model", task.RawModel,
				"recent_request_id", info.RequestID,
				"http_status", info.HTTPStatus,
			)
			return
		}
	}

	// Execute
	result, execErr := sm.executor.Execute(ctx, task)
	finish := time.Now().UTC()
	task.FinishedAt = &finish

	if result != nil && result.Result != nil {
		task.HTTPStatus = &result.Result.HTTPStatus
		task.LatencyMs = &result.Result.LatencyMs
		task.ErrCode = result.Result.ErrCode
		task.ErrDetail = result.Result.ErrMsg
		task.TokenCount = result.Result.TotalTokens
	}

	// Determine terminal status
	status, extras := sm.classifyResult(task, result, execErr)
	task.Status = status
	sm.publishEvent(ctx, string(status), task)

	// Audit
	if err := sm.audit.Write(ctx, task, result, extras); err != nil {
		workerLog.Warn("system_monitor: audit write failed", "error", err)
	}

	// Complete (Redis status update + running removal)
	if !sm.IsFallback() {
		if err := sm.queue.Complete(ctx, task, status, extras); err != nil {
			workerLog.Warn("system_monitor: complete failed", "error", err)
		}
	}

	// Backoff: requeue if failed and attempts remain
	if status == TaskStatusFailed && task.Attempt < task.MaxAttempts {
		nextRun := time.Now().UTC().Add(computeBackoff(task.Attempt))
		if !sm.IsFallback() {
			if err := sm.queue.Requeue(ctx, task, nextRun); err != nil {
				workerLog.Warn("system_monitor: requeue failed", "error", err)
			}
		} else {
			// Fallback: push to in-memory queue with delayed dispatch.
			go func(t *Task, at time.Time) {
				timer := time.NewTimer(time.Until(at))
				defer timer.Stop()
				select {
				case <-timer.C:
					select {
					case sm.fallbackCh <- t:
					default:
						workerLog.Warn("system_monitor: fallback requeue dropped", "task_id", t.ID)
					}
				case <-sm.stopCh:
					return
				}
			}(task, nextRun)
		}
	}
}

// classifyResult maps executor output to a terminal TaskStatus + audit extras.
func (sm *SystemMonitor) classifyResult(task *Task, result *ExecutorResult, execErr error) (TaskStatus, map[string]any) {
	extras := map[string]any{}
	if result == nil || result.Result == nil {
		extras["err_code"] = "executor_nil_result"
		extras["err_detail"] = execErr.Error()
		return TaskStatusFailed, extras
	}
	pr := result.Result
	if pr.HTTPStatus > 0 {
		extras["http_status"] = pr.HTTPStatus
	}
	if pr.LatencyMs > 0 {
		extras["latency_ms"] = pr.LatencyMs
	}
	if pr.RequestURL != "" {
		extras["request_url"] = pr.RequestURL
	}
	if pr.ErrCode != "" {
		extras["err_code"] = pr.ErrCode
	}
	if pr.ErrMsg != "" {
		extras["err_detail"] = truncateForAudit(pr.ErrMsg, 500)
	}
	// Parse dns_ms / tls_ms from http_ping response body (executor convention).
	// Phase 2: enrich executor to populate Result.Extras instead of stuffing ResponseBody.
	if pr.ResponseBody != "" {
		extras["response_body_preview"] = truncateForAudit(pr.ResponseBody, 500)
	}
	// Map ProbeStatus → TaskStatus
	switch pr.Status {
	case "success":
		return TaskStatusSuccess, extras
	case "timeout":
		return TaskStatusTimeout, extras
	case "network":
		return TaskStatusNetworkError, extras
	default:
		return TaskStatusFailed, extras
	}
}

// healthCheckLoop periodically pings Redis; on success clears fallback mode.
// On persistent failures (>= recoveryFailThreshold consecutive ticks) it
// also calls RecoveryGate.MarkClosedDebounced to close the URSM v2
// authoritative gate cluster-wide — see
// docs/architecture/2026-07-28-routing-state-anomaly-audit.md §4.1.
func (sm *SystemMonitor) healthCheckLoop(ctx context.Context) {
	defer sm.wg.Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sm.stopCh:
			return
		case <-ticker.C:
			sm.checkRedisHealthOnce(ctx)
		}
	}
}

// checkRedisHealthOnce is the per-tick Redis health check logic, exposed
// (lowercase, package-private) so tests can drive multiple ticks without
// time.Sleep on the 15s ticker. Production calls it from
// healthCheckLoop; tests call it directly with a constructed
// SystemMonitor.
//
// State transitions:
//
//	ping ok, fallback        -> clear fallback, reset consecutiveFailures,
//	                           maybe auto-reopen v2 gate (follow-up #6)
//	ping ok, healthy         -> reset consecutiveFailures (no-op)
//	ping fail, healthy       -> mark fallback, increment, maybe auto-close gate
//	ping fail, fallback      -> increment, maybe auto-close gate
func (sm *SystemMonitor) checkRedisHealthOnce(ctx context.Context) {
	ping := sm.pingFn
	if ping == nil {
		ping = sm.dedup.Ping
	}
	pingErr := ping(ctx)
	switch {
	case pingErr == nil && sm.IsFallback():
		slog.Info("system_monitor: redis recovered, exiting fallback mode")
		sm.clearFallback()
		sm.resetConsecutiveFailures()
		sm.maybeAutoRestoreRecoveryGate(ctx)
		// Audit follow-up #2: drain the durable fallback backstop
		// so tasks persisted during the fallback window get back
		// into Redis. Best-effort: errors are logged + skipped.
		if sm.fallbackDB != nil {
			if n, err := sm.drainFallbackQueue(ctx); err != nil {
				slog.Warn("system_monitor: fallback durable drain failed",
					"error", err)
			} else if n > 0 {
				slog.Info("system_monitor: fallback durable drain complete",
					"drained", n,
					"worker_id", sm.workerID,
				)
			}
		}
	case pingErr == nil && !sm.IsFallback():
		// Healthy and not in fallback — nothing to do, but reset the
		// failure counter so the next failure event starts from 0.
		sm.resetConsecutiveFailures()
	case pingErr != nil && !sm.IsFallback():
		slog.Warn("system_monitor: redis unhealthy, entering fallback mode",
			"error", pingErr)
		sm.markFallback()
		sm.maybeAutoCloseRecoveryGate(ctx, pingErr, sm.incrementConsecutiveFailures())
	case pingErr != nil && sm.IsFallback():
		sm.maybeAutoCloseRecoveryGate(ctx, pingErr, sm.incrementConsecutiveFailures())
	}
}

func (sm *SystemMonitor) resetConsecutiveFailures() {
	sm.healthMu.Lock()
	sm.consecutiveFailures = 0
	sm.healthMu.Unlock()
}

func (sm *SystemMonitor) incrementConsecutiveFailures() int {
	sm.healthMu.Lock()
	sm.consecutiveFailures++
	n := sm.consecutiveFailures
	sm.healthMu.Unlock()
	return n
}

func (sm *SystemMonitor) consecutiveFailureCount() int {
	sm.healthMu.Lock()
	n := sm.consecutiveFailures
	sm.healthMu.Unlock()
	return n
}

// maybeAutoCloseRecoveryGate calls MarkClosedDebounced when the
// consecutive-failure counter has reached the configured threshold.
// The (debounced, single-flight) gate ensures the cluster-wide epoch
// counter increments at most once per debounce window regardless of
// how many instances observe the same failure event — see
// recovery.Manager.MarkClosedDebounced for the cluster-coordination
// contract.
//
// Best-effort: failures are logged and swallowed so a single bad call
// never blocks the health-check loop. RecoveryGate==nil is a no-op
// (production wired default; tests can drive the path explicitly).
func (sm *SystemMonitor) maybeAutoCloseRecoveryGate(ctx context.Context, pingErr error, failures int) {
	if sm.recoveryGate == nil {
		return
	}
	if failures < sm.recoveryFailThreshold {
		return
	}
	won, err := sm.recoveryGate.MarkClosedDebounced(ctx, "redis_unavailable", sm.recoveryDebounceTTL)
	if err != nil {
		slog.Warn("system_monitor: recovery gate auto-close failed",
			"error", err,
			"consecutive_failures", failures,
			"threshold", sm.recoveryFailThreshold,
			"ping_error", pingErr.Error(),
		)
		return
	}
	if won {
		slog.Warn("system_monitor: recovery gate closed by persistent redis failure",
			"reason", "redis_unavailable",
			"consecutive_failures", failures,
			"threshold", sm.recoveryFailThreshold,
			"debounce_ttl", sm.recoveryDebounceTTL.String(),
			"ping_error", pingErr.Error(),
			"worker_id", sm.workerID,
		)
		// Audit follow-up #5: emit a recovery-state-transition event
		// so dashboards subscribed to /api/admin/system-monitor/stream
		// see the gate closure in real time.
		sm.publishRecoveryEvent(ctx, "recovery_closed", map[string]any{
			"reason":               "redis_unavailable",
			"consecutive_failures": failures,
			"threshold":            sm.recoveryFailThreshold,
			"debounce_ttl":         sm.recoveryDebounceTTL.String(),
			"ping_error":           pingErr.Error(),
		})
	}
	// Lost the debounce race (another instance won): stay silent at info
	// level — this is the expected outcome for any cluster of size > 1.
}

// maybeAutoRestoreRecoveryGate is the audit follow-up #6 counterpart to
// maybeAutoCloseRecoveryGate: on the fallback→healthy transition, call
// RecoveryGate.RestoreIfClosed to reopen the v2 gate and stamp
// recovery metadata. Idempotent on the recovery.Manager side, so we
// can call it on every health-check that observes a healthy Redis
// without worrying about cluster-wide thrash.
//
// Best-effort: failures are logged and swallowed so a single bad call
// never blocks the health-check loop.
func (sm *SystemMonitor) maybeAutoRestoreRecoveryGate(ctx context.Context) {
	if sm.recoveryGate == nil {
		return
	}
	n, err := sm.recoveryGate.RestoreIfClosed(ctx)
	if err != nil {
		slog.Warn("system_monitor: recovery gate auto-restore failed",
			"error", err,
			"worker_id", sm.workerID,
		)
		return
	}
	if n > 0 {
		slog.Info("system_monitor: recovery gate auto-reopened",
			"keys_re_warmed", n,
			"worker_id", sm.workerID,
		)
		// Audit follow-up #5: emit a recovery-reopened event so
		// dashboards subscribed to /api/admin/system-monitor/stream
		// see the gate recovery in real time.
		sm.publishRecoveryEvent(ctx, "recovery_reopened", map[string]any{
			"keys_re_warmed": n,
		})
	}
}

// QueueStats is the snapshot returned by QueueStats() for dashboards.
type QueueStats struct {
	QueueSize   int64
	RunningSize int64
	InFallback  bool
}

// QueueStats returns the current queue + running counts.
func (sm *SystemMonitor) QueueStats(ctx context.Context) (QueueStats, error) {
	if sm.IsFallback() {
		return QueueStats{InFallback: true}, nil
	}
	qSize, err := sm.queue.QueueSize(ctx)
	if err != nil {
		return QueueStats{}, fmt.Errorf("queue_size: %w", err)
	}
	rSize, err := sm.queue.RunningSize(ctx)
	if err != nil {
		return QueueStats{}, fmt.Errorf("running_size: %w", err)
	}
	return QueueStats{QueueSize: qSize, RunningSize: rSize}, nil
}

// RecoveryStats returns the combined observability snapshot. Safe on
// a nil receiver. When no gate is wired (production default for
// tests / off-mode deployment), only the monitor-internal fields are
// populated and the gate fields are zero-valued.
func (sm *SystemMonitor) RecoveryStats() RecoveryStats {
	if sm == nil {
		return RecoveryStats{}
	}
	out := RecoveryStats{
		ConsecutiveFailures: sm.consecutiveFailureCount(),
		FailThreshold:       sm.recoveryFailThreshold,
	}
	if sm.recoveryGate != nil {
		// The gate's own Stats() returns the gate-typed
		// RecoveryStats; we merge it in. Production wires
		// (v2.Manager) populate all four gate fields.
		gateStats := sm.recoveryGate.Stats()
		out.LastError = gateStats.LastError
		out.LastErrorAt = gateStats.LastErrorAt
		out.LastRecoveryAt = gateStats.LastRecoveryAt
		out.LastRecoveryKeyCount = gateStats.LastRecoveryKeyCount
	}
	return out
}

// Dedup exposes the InflightDedup for callers (e.g. RecentSuccessHook wiring).
//
// Required by main.go to bridge the dedup helper into the telemetry hook so
// 5-minute "recent success" markers can short-circuit automatic probe tasks.
// Not exposed via the systemMonitorBackend interface (admin doesn't need it).
func (sm *SystemMonitor) Dedup() *InflightDedup {
	if sm == nil {
		return nil
	}
	return sm.dedup
}

// SetFallbackDBForTest replaces the durable fallback DB pool for
// testing only. Production code must NOT call this — wire the pool
// through Config.FallbackDB at construction time. The field name
// ends in "ForTest" so a future linter can flag production callers.
func (sm *SystemMonitor) SetFallbackDBForTest(pool fallbackDBIface) {
	if sm == nil {
		return
	}
	sm.fallbackDB = pool
}

// helper
func hostnameOrDefault() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func computeBackoff(attempt int) time.Duration {
	// 2026-07-24 fix: changed from [5s,30s,60s,5m,1h,2h,24h] to [5s,30s,60s,5m,1h,2h,6h].
	// After reaching 6h (attempt 7+), all subsequent attempts remain at 6h intervals
	// until manually paused or successful. Rationale: 24h was too long.
	ladder := []time.Duration{
		5 * time.Second,
		30 * time.Second,
		60 * time.Second,
		5 * time.Minute,
		1 * time.Hour,
		2 * time.Hour,
		6 * time.Hour, // was 24h
	}
	if attempt < 1 {
		attempt = 1
	}
	idx := attempt - 1
	if idx >= len(ladder) {
		idx = len(ladder) - 1
	}
	return ladder[idx]
}

func truncateForAudit(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// GetMetricsCollector returns the metrics collector for Phase 3 migration tracking.
func (sm *SystemMonitor) GetMetricsCollector() *MetricsCollector {
	return sm.metricsCollector
}
