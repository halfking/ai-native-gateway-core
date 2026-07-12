package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// hotPromoteTableMap: 热表名 → 数据库迁移函数名。
// 与 SQL 函数一一对应（partition_manager 模块定义）。
var hotPromoteTableMap = map[string]string{
	"request_logs_hot":           "promote_request_logs_hot_to_partition",
	"usage_ledger_hot":           "promote_usage_ledger_hot_to_partition",
	"request_wal_hot":            "promote_request_wal_hot_to_partition",
	"routing_decision_log_hot":   "promote_routing_decision_log_hot_to_partition",
	"credential_model_index_hot": "promote_credential_model_index_hot_to_partition",
	"request_logs_bodies_hot":    "promote_request_logs_bodies_hot_to_partition",
	"credit_ledger_hot":          "promote_credit_ledger_hot_to_partition",
	"tool_usage_stats_hot":       "promote_tool_usage_stats_hot_to_partition",
	"model_probe_runs_hot":       "promote_model_probe_runs_hot_to_partition",
}

// HotJobStatus 状态枚举
type HotJobStatus string

const (
	HotJobStatusPending   HotJobStatus = "pending"
	HotJobStatusRunning   HotJobStatus = "running"
	HotJobStatusSuccess   HotJobStatus = "success"
	HotJobStatusPartial   HotJobStatus = "partial"
	HotJobStatusFailed    HotJobStatus = "failed"
	HotJobStatusCancelled HotJobStatus = "cancelled"
)

// HotPromoteJob 异步迁移任务的完整状态
type HotPromoteJob struct {
	ID              string             `json:"id"`
	TableName       string             `json:"table_name"`
	Status          HotJobStatus       `json:"status"`
	RetentionHours  int                `json:"retention_hours"`
	BatchSize       int                `json:"batch_size"`
	StartedAt       time.Time          `json:"started_at"`
	FinishedAt      *time.Time         `json:"finished_at,omitempty"`
	UpdatedAt       time.Time          `json:"updated_at"`
	DurationSeconds int64              `json:"duration_seconds"`
	TotalMigrated   int64              `json:"total_migrated"`
	BatchesExecuted int                `json:"batches_executed"`
	CurrentBatch    int                `json:"current_batch"`
	Message         string             `json:"message,omitempty"`
	Warning         string             `json:"warning,omitempty"`
	Error           string             `json:"error,omitempty"`
	TriggeredBy     string             `json:"triggered_by"` // "manual" | "cron" | "system"
	TriggeredByUser string             `json:"triggered_by_user,omitempty"`
	cancelFn        context.CancelFunc `json:"-"`
}

// hotJobManager 内存中的任务注册表 + 互斥锁
//
// 任务保留策略：每个表名最多保留最近 10 条；全局最多 200 条。
// 任务完成后保留 24h 供前端查询历史。
type hotJobManager struct {
	mu   sync.RWMutex
	jobs map[string]*HotPromoteJob // jobID → job
	// perTable 最近任务索引（按表名分组，便于前端显示每个表的最新状态）
	perTable map[string][]string // tableName → []jobID (按 finishedAt DESC)
}

func newHotJobManager() *hotJobManager {
	return &hotJobManager{
		jobs:     make(map[string]*HotPromoteJob),
		perTable: make(map[string][]string),
	}
}

const (
	hotJobMaxPerTable = 10
	hotJobMaxGlobal   = 200
	hotJobRetention   = 24 * time.Hour
)

// add 注册一个新任务
func (m *hotJobManager) add(job *HotPromoteJob) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs[job.ID] = job
	m.perTable[job.TableName] = append(m.perTable[job.TableName], job.ID)
	// 修剪超限的任务
	m.evictLocked()
}

// get 获取任务
func (m *hotJobManager) get(jobID string) (*HotPromoteJob, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[jobID]
	return j, ok
}

// listAll 返回所有任务（按 StartedAt DESC），自动裁剪过期
func (m *hotJobManager) listAll() []*HotPromoteJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evictLocked()
	out := make([]*HotPromoteJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool {
		return out[i].StartedAt.After(out[k].StartedAt)
	})
	return out
}

// listByTable 返回某表的所有任务（按 StartedAt DESC）
func (m *hotJobManager) listByTable(tableName string) []*HotPromoteJob {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := m.perTable[tableName]
	out := make([]*HotPromoteJob, 0, len(ids))
	for _, id := range ids {
		if j, ok := m.jobs[id]; ok {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, k int) bool {
		return out[i].StartedAt.After(out[k].StartedAt)
	})
	return out
}

// evictLocked 修剪过期与超限任务（调用方需持有 mu 写锁）
func (m *hotJobManager) evictLocked() {
	// 1. 删除 finished_at 超过 hotJobRetention 的任务
	cutoff := time.Now().Add(-hotJobRetention)
	for id, j := range m.jobs {
		if j.FinishedAt != nil && j.FinishedAt.Before(cutoff) {
			delete(m.jobs, id)
			m.removeFromPerTableLocked(j.TableName, id)
		}
	}
	// 2. 全局超限
	if len(m.jobs) > hotJobMaxGlobal {
		// 按 StartedAt ASC 淘汰最旧的（已完成的优先）
		type pair struct {
			id  string
			job *HotPromoteJob
		}
		all := make([]pair, 0, len(m.jobs))
		for id, j := range m.jobs {
			all = append(all, pair{id, j})
		}
		sort.Slice(all, func(i, k int) bool {
			return all[i].job.StartedAt.Before(all[k].job.StartedAt)
		})
		toRemove := len(m.jobs) - hotJobMaxGlobal
		for i := 0; i < toRemove; i++ {
			delete(m.jobs, all[i].id)
			m.removeFromPerTableLocked(all[i].job.TableName, all[i].id)
		}
	}
	// 3. 每张表保留最近 hotJobMaxPerTable 条
	for table, ids := range m.perTable {
		if len(ids) <= hotJobMaxPerTable {
			continue
		}
		// ids 是按 append 顺序，最新在末尾；保留末尾 N 条
		keep := ids[len(ids)-hotJobMaxPerTable:]
		drop := ids[:len(ids)-hotJobMaxPerTable]
		for _, id := range drop {
			delete(m.jobs, id)
		}
		m.perTable[table] = keep
	}
}

func (m *hotJobManager) removeFromPerTableLocked(table, id string) {
	ids := m.perTable[table]
	for i, x := range ids {
		if x == id {
			m.perTable[table] = append(ids[:i], ids[i+1:]...)
			return
		}
	}
}

// promoteHotAsyncRequest 启动异步迁移任务的请求体
type promoteHotAsyncRequest struct {
	TableName      string `json:"table_name"`
	RetentionHours *int   `json:"retention_hours"` // nil → 用默认值
	BatchSize      int    `json:"batch_size"`      // 0 → 默认 500
	MaxBatches     int    `json:"max_batches"`     // 0 → 不限
}

// promoteHotAsyncResponse 异步任务启动响应
type promoteHotAsyncResponse struct {
	RunID      string `json:"run_id"`
	TableName  string `json:"table_name"`
	Status     string `json:"status"`
	Async      bool   `json:"async"`
	PollingURL string `json:"polling_url"`
	StartedAt  string `json:"started_at"`
	Message    string `json:"message"`
}

// 默认保留时间改为 24 小时（2026-07-13 产品调整：hot 表只保留最近 1 天）
const defaultHotRetentionHours = 24

// handleDataLifecyclePromoteHotAsync POST /api/admin/data-lifecycle/hot/promote-async
//
// 异步触发 hot 表迁移，立即返回 job_id，后台 goroutine 执行迁移逻辑。
// 前端通过 GET /api/admin/data-lifecycle/hot/job/:id 轮询状态。
func (h *Handler) handleDataLifecyclePromoteHotAsync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req promoteHotAsyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if _, ok := hotPromoteTableMap[req.TableName]; !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid table_name: %s", req.TableName))
		return
	}

	// 默认值
	retentionHours := defaultHotRetentionHours
	if req.RetentionHours != nil {
		retentionHours = *req.RetentionHours
	}
	if retentionHours < 0 {
		writeError(w, http.StatusBadRequest, "retention_hours must be non-negative")
		return
	}
	if req.BatchSize <= 0 {
		req.BatchSize = 500
	}
	if req.BatchSize > 5000 {
		req.BatchSize = 5000
	}

	// 防止同一张表并发迁移：如果已有 running 任务，直接返回其 job_id
	if existing := h.findRunningJobForTable(req.TableName); existing != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(promoteHotAsyncResponse{
			RunID:      existing.ID,
			TableName:  existing.TableName,
			Status:     string(existing.Status),
			Async:      true,
			PollingURL: fmt.Sprintf("/api/admin/data-lifecycle/hot/job/%s", existing.ID),
			StartedAt:  existing.StartedAt.UTC().Format(time.RFC3339),
			Message:    "an existing job is already running for this table; reused",
		})
		return
	}

	job := &HotPromoteJob{
		ID:              uuid.NewString(),
		TableName:       req.TableName,
		Status:          HotJobStatusPending,
		RetentionHours:  retentionHours,
		BatchSize:       req.BatchSize,
		StartedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
		TriggeredBy:     "manual",
		TriggeredByUser: r.Header.Get("X-Admin-User"), // 由 superAdmin 中间件注入
	}
	h.hotJobMgr.add(job)

	// 启动后台 goroutine：独立 context，独立超时（24h 单任务上限）
	jobCtx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	job.cancelFn = cancel

	go h.runHotPromoteJob(jobCtx, job, req.MaxBatches)

	slog.Info("data-lifecycle: hot promote async started",
		"job_id", job.ID,
		"table", job.TableName,
		"retention_hours", job.RetentionHours,
		"batch_size", job.BatchSize,
		"triggered_by", job.TriggeredBy,
		"triggered_by_user", job.TriggeredByUser)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(promoteHotAsyncResponse{
		RunID:      job.ID,
		TableName:  job.TableName,
		Status:     string(job.Status),
		Async:      true,
		PollingURL: fmt.Sprintf("/api/admin/data-lifecycle/hot/job/%s", job.ID),
		StartedAt:  job.StartedAt.Format(time.RFC3339),
		Message:    "job accepted, poll status endpoint for progress",
	})
}

// runHotPromoteJob 是异步迁移的实际执行体，封装自原 handleDataLifecyclePromoteHot 中的循环逻辑。
//
// 关键点：
//   - 每完成一批就更新 job 的 UpdatedAt / TotalMigrated / BatchesExecuted / CurrentBatch
//   - 写入 final 状态（success / partial / failed / cancelled）时设置 FinishedAt
//   - 任何 panic 由 defer recover 兜底，状态写为 failed 并附 panic 信息
func (h *Handler) runHotPromoteJob(ctx context.Context, job *HotPromoteJob, maxBatches int) {
	defer func() {
		if r := recover(); r != nil {
			now := time.Now().UTC()
			job.Status = HotJobStatusFailed
			job.Error = fmt.Sprintf("panic: %v", r)
			job.FinishedAt = &now
			job.UpdatedAt = now
			job.DurationSeconds = int64(time.Since(job.StartedAt).Seconds())
			slog.Error("data-lifecycle: hot promote job panic",
				"job_id", job.ID, "table", job.TableName, "panic", r)
		}
	}()

	fnName := hotPromoteTableMap[job.TableName]
	retentionInterval := fmt.Sprintf("%d hours", job.RetentionHours)
	job.Status = HotJobStatusRunning
	job.UpdatedAt = time.Now().UTC()

	for {
		select {
		case <-ctx.Done():
			now := time.Now().UTC()
			if ctx.Err() == context.Canceled {
				job.Status = HotJobStatusCancelled
				job.Message = "cancelled by user"
			} else {
				job.Status = HotJobStatusFailed
				job.Error = ctx.Err().Error()
			}
			job.FinishedAt = &now
			job.UpdatedAt = now
			job.DurationSeconds = int64(time.Since(job.StartedAt).Seconds())
			slog.Warn("data-lifecycle: hot promote job terminated",
				"job_id", job.ID, "table", job.TableName, "status", job.Status, "total_migrated", job.TotalMigrated)
			return
		default:
		}

		if maxBatches > 0 && job.BatchesExecuted >= maxBatches {
			now := time.Now().UTC()
			job.Status = HotJobStatusPartial
			job.Message = fmt.Sprintf("max_batches limit reached (%d)", maxBatches)
			job.FinishedAt = &now
			job.UpdatedAt = now
			job.DurationSeconds = int64(time.Since(job.StartedAt).Seconds())
			return
		}

		var migrated int64
		err := h.db.QueryRow(ctx,
			fmt.Sprintf("SELECT %s($1::interval, $2::int)", fnName),
			retentionInterval,
			job.BatchSize,
		).Scan(&migrated)
		if err != nil {
			now := time.Now().UTC()
			job.Status = HotJobStatusFailed
			job.Error = fmt.Sprintf("migration failed at batch %d: %v", job.BatchesExecuted+1, err)
			job.FinishedAt = &now
			job.UpdatedAt = now
			job.DurationSeconds = int64(time.Since(job.StartedAt).Seconds())
			slog.Error("data-lifecycle: hot promote job failed",
				"job_id", job.ID, "table", job.TableName, "batch", job.BatchesExecuted+1, "error", err)
			return
		}

		job.TotalMigrated += migrated
		job.BatchesExecuted++
		job.CurrentBatch = job.BatchesExecuted
		job.UpdatedAt = time.Now().UTC()

		if migrated == 0 {
			// 没有更多数据需要迁移，迁移完成
			now := time.Now().UTC()
			job.Status = HotJobStatusSuccess
			job.Message = "all eligible rows migrated"
			job.FinishedAt = &now
			job.UpdatedAt = now
			job.DurationSeconds = int64(time.Since(job.StartedAt).Seconds())
			slog.Info("data-lifecycle: hot promote job done",
				"job_id", job.ID, "table", job.TableName,
				"total_migrated", job.TotalMigrated,
				"batches", job.BatchesExecuted,
				"duration_seconds", job.DurationSeconds)
			if job.TableName == "request_logs_hot" && job.TotalMigrated > 0 {
				job.Warning = "已迁移数据，但 TOAST 空间尚未释放。请在【存储总览】页面对 request_logs_hot 执行 VACUUM FULL 以回收磁盘空间。"
			}
			return
		}

		slog.Info("data-lifecycle: hot promote batch complete",
			"job_id", job.ID, "table", job.TableName,
			"batch", job.BatchesExecuted, "batch_migrated", migrated, "total", job.TotalMigrated)
	}
}

// findRunningJobForTable 检查同名表是否已有 running 任务，有则返回 job；用于并发去重
func (h *Handler) findRunningJobForTable(tableName string) *HotPromoteJob {
	h.hotJobMgr.mu.RLock()
	defer h.hotJobMgr.mu.RUnlock()
	ids := h.hotJobMgr.perTable[tableName]
	// perTable 是 append 顺序，最新在末尾；倒序查最近任务
	for i := len(ids) - 1; i >= 0; i-- {
		j, ok := h.hotJobMgr.jobs[ids[i]]
		if !ok {
			continue
		}
		if j.Status == HotJobStatusPending || j.Status == HotJobStatusRunning {
			return j
		}
	}
	return nil
}

// handleDataLifecycleHotJob GET /api/admin/data-lifecycle/hot/job/{id}
func (h *Handler) handleDataLifecycleHotJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// path 形如 /api/admin/data-lifecycle/hot/job/{id}（由 mux 路由）
	// 这里通过 r.URL.Path 解析末尾段
	id := strings.TrimPrefix(r.URL.Path, "/api/admin/data-lifecycle/hot/job/")
	id = strings.TrimSpace(id)
	if id == "" {
		writeError(w, http.StatusBadRequest, "job id is required")
		return
	}
	job, ok := h.hotJobMgr.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// 拷贝副本，避免前端读到 cancelFn 等内部字段
	resp := *job
	resp.cancelFn = nil
	_ = json.NewEncoder(w).Encode(resp)
}

// handleDataLifecycleHotJobs GET /api/admin/data-lifecycle/hot/jobs?table=xxx
//
// 列出任务；可选 ?table=xxx 过滤指定表的任务。
func (h *Handler) handleDataLifecycleHotJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tableFilter := r.URL.Query().Get("table")
	var jobs []*HotPromoteJob
	if tableFilter != "" {
		jobs = h.hotJobMgr.listByTable(tableFilter)
	} else {
		jobs = h.hotJobMgr.listAll()
	}
	// 序列化为不带 cancelFn 的副本
	out := make([]HotPromoteJob, 0, len(jobs))
	for _, j := range jobs {
		copy := *j
		copy.cancelFn = nil
		out = append(out, copy)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jobs":  out,
		"count": len(out),
	})
}

// handleDataLifecycleHotJobCancel POST /api/admin/data-lifecycle/hot/job/{id}/cancel
//
// 取消正在运行的任务；通过 cancel() 触发 context.Canceled，
// runHotPromoteJob 的 select 会捕获并把状态写为 cancelled。
func (h *Handler) handleDataLifecycleHotJobCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/admin/data-lifecycle/hot/job/")
	id = strings.TrimSuffix(id, "/cancel")
	id = strings.TrimSpace(id)
	if id == "" {
		writeError(w, http.StatusBadRequest, "job id is required")
		return
	}
	job, ok := h.hotJobMgr.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if job.Status != HotJobStatusPending && job.Status != HotJobStatusRunning {
		writeError(w, http.StatusConflict, fmt.Sprintf("job already in terminal state: %s", job.Status))
		return
	}
	if job.cancelFn != nil {
		job.cancelFn()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"job_id": job.ID,
		"status": "cancellation_requested",
	})
}

// =============================================================================
// 兼容旧版同步接口 (handleDataLifecyclePromoteHot)
// =============================================================================
//
// 旧版 /api/admin/data-lifecycle/hot/promote 继续保留，但内部委托给异步任务管理器
// 并等待最终结果。这样旧的前端调用仍可工作，但前端建议迁移到 /promote-async。

// promoteHotRequest 同步接口的请求体（保留兼容）
type promoteHotRequest struct {
	TableName      string `json:"table_name"`
	RetentionHours *int   `json:"retention_hours"`
	BatchSize      int    `json:"batch_size"`
	MaxBatches     int    `json:"max_batches"`
}

// promoteHotResponse 同步接口的响应（保留兼容）
type promoteHotResponse struct {
	TableName       string `json:"table_name"`
	TotalMigrated   int64  `json:"total_migrated"`
	BatchesExecuted int    `json:"batches_executed"`
	StartedAt       string `json:"started_at"`
	FinishedAt      string `json:"finished_at"`
	DurationSeconds int64  `json:"duration_seconds"`
	Status          string `json:"status"`
	Message         string `json:"message"`
	Warning         string `json:"warning,omitempty"`
	JobID           string `json:"job_id,omitempty"`
}

// handleDataLifecyclePromoteHot POST /api/admin/data-lifecycle/hot/promote
//
// 同步模式（保留兼容）：内部启动异步任务，阻塞等待结果。
// 推荐使用 /promote-async + GET /job/{id} 实现真正的非阻塞调用。
func (h *Handler) handleDataLifecyclePromoteHot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req promoteHotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// 默认值（同步接口保持 168 = 7天 以避免破坏现有 UI；新接口 /promote-async 默认 24h）
	retentionHours := 168
	if req.RetentionHours != nil {
		retentionHours = *req.RetentionHours
	}
	if retentionHours < 0 {
		writeError(w, http.StatusBadRequest, "retention_hours must be non-negative")
		return
	}
	if req.BatchSize == 0 {
		req.BatchSize = 1000
	}
	if req.BatchSize > 10000 {
		req.BatchSize = 10000
	}

	fnName, ok := hotPromoteTableMap[req.TableName]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid table_name: %s", req.TableName))
		return
	}

	// 10 分钟超时（保留兼容）
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	resp := promoteHotResponse{
		TableName: req.TableName,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Status:    "success",
	}
	start := time.Now()

	slog.Info("data-lifecycle: manual promote hot table start (legacy sync)",
		"table", req.TableName,
		"retention_hours", retentionHours,
		"batch_size", req.BatchSize,
		"max_batches", req.MaxBatches)

	totalMigrated := int64(0)
	batchCount := 0
	retentionInterval := fmt.Sprintf("%d hours", retentionHours)

	for {
		if ctx.Err() != nil {
			resp.Status = "partial"
			resp.Message = "timeout reached, partial migration completed"
			break
		}
		if req.MaxBatches > 0 && batchCount >= req.MaxBatches {
			resp.Status = "partial"
			resp.Message = fmt.Sprintf("max_batches limit reached (%d)", req.MaxBatches)
			break
		}

		var migrated int64
		err := h.db.QueryRow(ctx,
			fmt.Sprintf("SELECT %s($1::interval, $2::int)", fnName),
			retentionInterval,
			req.BatchSize,
		).Scan(&migrated)

		if err != nil {
			resp.Status = "failed"
			resp.Message = fmt.Sprintf("migration failed at batch %d: %v", batchCount+1, err)
			slog.Error("data-lifecycle: manual promote failed",
				"table", req.TableName, "batch", batchCount+1, "error", err)
			break
		}

		if migrated == 0 {
			resp.Message = "all eligible rows migrated"
			break
		}

		totalMigrated += migrated
		batchCount++
		slog.Info("data-lifecycle: manual promote batch complete",
			"table", req.TableName,
			"batch", batchCount,
			"migrated", migrated,
			"total", totalMigrated)
	}

	resp.TotalMigrated = totalMigrated
	resp.BatchesExecuted = batchCount
	resp.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	resp.DurationSeconds = int64(time.Since(start).Seconds())

	if totalMigrated == 0 && resp.Status == "success" {
		resp.Message = "no rows eligible for migration (all data within retention window)"
	}
	if req.TableName == "request_logs_hot" && totalMigrated > 0 {
		resp.Warning = "已迁移数据，但 TOAST 空间尚未释放。请在【存储总览】页面对 request_logs_hot 执行 VACUUM FULL 以回收磁盘空间。"
	}

	slog.Info("data-lifecycle: manual promote complete",
		"table", req.TableName,
		"total_migrated", totalMigrated,
		"batches", batchCount,
		"duration_seconds", resp.DurationSeconds,
		"status", resp.Status)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// =============================================================================
// dropPartitionRequest / dropPartitionResponse (保留兼容)
// =============================================================================

type dropPartitionRequest struct {
	PartitionName string `json:"partition_name"`
	Confirm       bool   `json:"confirm"`
}

type dropPartitionResponse struct {
	PartitionName   string `json:"partition_name"`
	ParentTable     string `json:"parent_table"`
	RowsDeleted     int64  `json:"rows_deleted"`
	SpaceFreed      int64  `json:"space_freed_bytes"`
	SpaceFreedHuman string `json:"space_freed_human"`
	ExecutedAt      string `json:"executed_at"`
	Status          string `json:"status"`
	Message         string `json:"message"`
}

// handleDataLifecycleHotCronStats GET /api/admin/data-lifecycle/hot/cron/stats
//
// 返回夜间 cron 调度器当前状态（最后运行时间、当前是否在跑、最近错误等），
// 前端用于显示 cron 状态卡片。
func (h *Handler) handleDataLifecycleHotCronStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.hotCron == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(HotCronStats{Enabled: false})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.hotCron.Stats())
}

// handleDataLifecycleDropPartition POST /api/admin/data-lifecycle/partitions/drop
//
// 同步模式（保留兼容）。推荐使用 /drop-async + GET /jobs/{id} 实现非阻塞。
func (h *Handler) handleDataLifecycleDropPartition(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req dropPartitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest, "confirm must be true to execute drop operation")
		return
	}
	if req.PartitionName == "" {
		writeError(w, http.StatusBadRequest, "partition_name is required")
		return
	}
	if strings.HasSuffix(req.PartitionName, "_hot") {
		writeError(w, http.StatusBadRequest, "cannot drop hot tables, use promote instead")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	resp := dropPartitionResponse{
		PartitionName: req.PartitionName,
		ExecutedAt:    time.Now().UTC().Format(time.RFC3339),
		Status:        "success",
	}

	var parentTable string
	var sizeBytes int64
	var sizeHuman string
	var rowCount int64
	checkQuery := `
		SELECT
			p.relname AS parent_table,
			pg_total_relation_size(c.oid) AS size_bytes,
			pg_size_pretty(pg_total_relation_size(c.oid)) AS size_human,
			c.reltuples::bigint AS row_count
		FROM pg_class c
		JOIN pg_inherits i ON c.oid = i.inhrelid
		JOIN pg_class p ON i.inhparent = p.oid
		WHERE c.relname = $1
			AND c.relkind = 'r'
	`
	err := h.db.QueryRow(ctx, checkQuery, req.PartitionName).Scan(
		&parentTable, &sizeBytes, &sizeHuman, &rowCount,
	)
	if err != nil {
		resp.Status = "failed"
		resp.Message = fmt.Sprintf("partition not found or not a partition table: %v", err)
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	resp.ParentTable = parentTable
	resp.SpaceFreed = sizeBytes
	resp.SpaceFreedHuman = sizeHuman
	resp.RowsDeleted = rowCount

	slog.Warn("data-lifecycle: dropping partition",
		"partition", req.PartitionName,
		"parent_table", parentTable,
		"rows", rowCount, "size", sizeHuman)

	dropSQL := fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", req.PartitionName)
	if _, err := h.db.Exec(ctx, dropSQL); err != nil {
		resp.Status = "failed"
		resp.Message = fmt.Sprintf("drop failed: %v", err)
		slog.Error("data-lifecycle: drop partition failed", "partition", req.PartitionName, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	resp.Message = fmt.Sprintf("successfully dropped partition %s from %s", req.PartitionName, parentTable)
	slog.Warn("data-lifecycle: partition dropped",
		"partition", req.PartitionName, "parent_table", parentTable,
		"rows_deleted", rowCount, "space_freed", sizeHuman)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// dropPartitionAsyncRequest async drop partition request body
type dropPartitionAsyncRequest struct {
	PartitionName string `json:"partition_name"`
	Confirm       bool   `json:"confirm"`
}

// handleDataLifecycleDropPartitionAsync POST /api/admin/data-lifecycle/partitions/drop-async
//
// 异步删除分区：立即返回 job_id，后台 goroutine 执行 DROP TABLE。
// 前端通过 GET /api/admin/data-lifecycle/jobs/{id} 轮询状态。
func (h *Handler) handleDataLifecycleDropPartitionAsync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req dropPartitionAsyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest, "confirm must be true to execute drop operation")
		return
	}
	if req.PartitionName == "" {
		writeError(w, http.StatusBadRequest, "partition_name is required")
		return
	}
	if strings.HasSuffix(req.PartitionName, "_hot") {
		writeError(w, http.StatusBadRequest, "cannot drop hot tables, use promote instead")
		return
	}

	params := map[string]any{
		"partition_name": req.PartitionName,
		"confirm":        req.Confirm,
	}
	operator := r.Header.Get("X-Admin-User")
	run := h.StartJob(JobTypeDropPartition, params, operator, func(ctx context.Context, run *JobRun) {
		h.runDropPartitionJob(ctx, run, req.PartitionName)
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"run_id":      run.RunID,
		"status":      string(run.Status),
		"async":       true,
		"polling_url": "/api/admin/data-lifecycle/jobs/" + run.RunID,
		"message":     fmt.Sprintf("已调度删除分区任务: %s", req.PartitionName),
	})
}

// runDropPartitionJob 后台异步执行 DROP TABLE
func (h *Handler) runDropPartitionJob(ctx context.Context, run *JobRun, partitionName string) {
	h.TouchHeartbeat(run, "正在检查分区信息")

	var parentTable string
	var sizeBytes int64
	var sizeHuman string
	var rowCount int64
	checkQuery := `
		SELECT
			p.relname AS parent_table,
			pg_total_relation_size(c.oid) AS size_bytes,
			pg_size_pretty(pg_total_relation_size(c.oid)) AS size_human,
			c.reltuples::bigint AS row_count
		FROM pg_class c
		JOIN pg_inherits i ON c.oid = i.inhrelid
		JOIN pg_class p ON i.inhparent = p.oid
		WHERE c.relname = $1
			AND c.relkind = 'r'
	`
	err := h.db.QueryRow(ctx, checkQuery, partitionName).Scan(
		&parentTable, &sizeBytes, &sizeHuman, &rowCount,
	)
	if err != nil {
		h.failJob(run, fmt.Sprintf("partition not found: %v", err))
		slog.Error("data-lifecycle: drop partition check failed", "job_id", run.RunID, "partition", partitionName, "error", err)
		return
	}

	if ctx.Err() != nil {
		h.failJob(run, "cancelled")
		return
	}

	h.TouchHeartbeat(run, "执行 DROP TABLE")

	dropSQL := fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", partitionName)
	if _, err := h.db.Exec(ctx, dropSQL); err != nil {
		h.failJob(run, fmt.Sprintf("drop failed: %v", err))
		slog.Error("data-lifecycle: drop partition job failed", "job_id", run.RunID, "partition", partitionName, "error", err)
		return
	}

	h.SetResult(run, map[string]any{
		"partition_name": partitionName,
		"rows_deleted":   rowCount,
		"space_freed":    sizeHuman,
		"space_freed_b":  sizeBytes,
		"parent_table":   parentTable,
	}, fmt.Sprintf("成功删除分区 %s (释放 %s, %d 行)", partitionName, sizeHuman, rowCount))

	h.succeedJob(run, fmt.Sprintf("已删除分区 %s", partitionName))

	slog.Warn("data-lifecycle: drop partition job done",
		"job_id", run.RunID, "partition", partitionName,
		"parent_table", parentTable,
		"rows_deleted", rowCount, "space_freed", sizeHuman)
}
