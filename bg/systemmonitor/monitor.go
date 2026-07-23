// Package bg/systemmonitor — monitor.go
//
// SystemMonitor 顶层结构：组装 Queue / InflightDedup / Executor / Worker Pool，
// 提供唯一对外接口 Submit() + Start()/Stop()。
// 设计依据: docs/会话优化v2/32-系统监测模块设计.md §6.2
package systemmonitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
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
}

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
		queue:            NewQueue(cfg.Redis, scripts),
		dedup:            NewInflightDedup(cfg.Redis),
		executor:         NewExecutor(ExecutorConfig{DB: cfg.DB, Keyring: nil, EncKey: cfg.EncKey, ProxyFunc: cfg.ProxyFunc, TimeoutMs: cfg.TimeoutMs}),
		audit:            NewAudit(cfg.DB),
		metricsCollector: NewMetricsCollector(cfg.DB),
		concurrency:      cfg.Concurrency,
		workerCount:      cfg.WorkerCount,
		workerID:         cfg.WorkerID,
		stopCh:           make(chan struct{}),
		fallbackCh:       make(chan *Task, maxInt(cfg.Concurrency*4, 1000)),
	}

	if cfg.Redis == nil {
		sm.fallback = true
		slog.Warn("system_monitor: redis disabled, starting in fallback mode (memory FIFO only)")
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
		return sm.submitFallback(task)
	}
	if err := sm.queue.Submit(ctx, task); err != nil {
		// Try once: lazy re-load of Lua scripts (may have been evicted).
		if scripts, lerr := LoadScripts(ctx, sm.queue.rdb); lerr == nil {
			sm.queue.scripts = scripts
			if err2 := sm.queue.Submit(ctx, task); err2 == nil {
				return task.ID, nil
			}
		}
		// Fallback: log warn and insert into memory queue (don't block caller).
		slog.Warn("system_monitor: redis submit failed, falling back to memory queue",
			"error", err, "task_id", task.ID)
		sm.markFallback()
		return sm.submitFallback(task)
	}
	return task.ID, nil
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
		task, err := sm.queue.Claim(ctx /* placeholder credID, will be overridden in claim */, 0, "")
		if err != nil {
			workerLog.Warn("system_monitor: claim failed", "error", err)
			sm.markFallback()
		} else if task != nil {
			return task, true
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
	}

	// Determine terminal status
	status, extras := sm.classifyResult(task, result, execErr)
	task.Status = status

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
			if err := sm.dedup.Ping(ctx); err == nil && sm.IsFallback() {
				slog.Info("system_monitor: redis recovered, exiting fallback mode")
				sm.clearFallback()
			} else if err != nil && !sm.IsFallback() {
				slog.Warn("system_monitor: redis unhealthy, entering fallback mode",
					"error", err)
				sm.markFallback()
			}
		}
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
