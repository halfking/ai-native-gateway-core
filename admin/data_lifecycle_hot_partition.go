package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// promoteHotRequest 手动触发 hot 表迁移请求
type promoteHotRequest struct {
	TableName      string `json:"table_name"`      // "request_logs_hot", "usage_ledger_hot" 等
	RetentionHours *int   `json:"retention_hours"` // 迁移超过 N 小时的数据，默认 168 (7天)
	BatchSize      int    `json:"batch_size"`      // 每批次迁移行数，默认 1000
	MaxBatches     int    `json:"max_batches"`     // 最多执行几批，0=不限制
}

// promoteHotResponse 迁移响应
type promoteHotResponse struct {
	TableName       string `json:"table_name"`
	TotalMigrated   int64  `json:"total_migrated"`
	BatchesExecuted int    `json:"batches_executed"`
	StartedAt       string `json:"started_at"`
	FinishedAt      string `json:"finished_at"`
	DurationSeconds int64  `json:"duration_seconds"`
	Status          string `json:"status"` // "success", "partial", "failed"
	Message         string `json:"message"`
	Warning         string `json:"warning,omitempty"`
}

// dropPartitionRequest 删除分区表请求
type dropPartitionRequest struct {
	PartitionName string `json:"partition_name"` // e.g., "request_logs_2026_06"
	Confirm       bool   `json:"confirm"`        // 必须为 true 才执行
}

// dropPartitionResponse 删除分区表响应
type dropPartitionResponse struct {
	PartitionName   string `json:"partition_name"`
	ParentTable     string `json:"parent_table"`
	RowsDeleted     int64  `json:"rows_deleted"`
	SpaceFreed      int64  `json:"space_freed_bytes"`
	SpaceFreedHuman string `json:"space_freed_human"`
	ExecutedAt      string `json:"executed_at"`
	Status          string `json:"status"` // "success", "failed"
	Message         string `json:"message"`
}

// POST /api/admin/data-lifecycle/hot/promote
//
// 手动触发 hot 表数据迁移到分区表。
// 背景：PartitionManager 每小时自动迁移超过 7 天的数据，但管理员可能需要：
// 1. 立即迁移（不等下一个小时）
// 2. 调整保留时间（例如立即迁移所有数据）
// 3. 清空 hot 表以释放 TOAST 空间
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

	hours := 24
	if req.RetentionHours != nil {
		hours = *req.RetentionHours
	}
	if hours < 0 {
		writeError(w, http.StatusBadRequest, "retention_hours must be non-negative")
		return
	}
	batch := req.BatchSize
	if batch == 0 {
		batch = 5000
	}
	if batch < 0 {
		writeError(w, http.StatusBadRequest, "batch_size must be positive")
		return
	}
	if batch > 50000 {
		batch = 50000
	}

	fn, ok := hotTablePromoteFnMap[req.TableName]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid table_name: %s", req.TableName))
		return
	}

	run := h.StartJob(JobTypePromoteHot, map[string]any{
		"table_name":      req.TableName,
		"fn_name":         fn,
		"retention_hours": hours,
		"batch_size":      batch,
		"max_batches":     req.MaxBatches,
	}, r.Header.Get("X-Admin-User"), func(ctx context.Context, run *JobRun) {
		h.runPromoteJobSync(ctx, run, req.TableName, fn, hours, batch)
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{
		"run_id":      run.RunID,
		"table_name":  req.TableName,
		"status":      "queued",
		"async":       true,
		"polling_url": "/api/admin/data-lifecycle/jobs/" + run.RunID,
		"started_at":  run.StartedAt.UTC().Format(time.RFC3339),
		"message":     "迁移任务已创建，正在后台执行",
	})
}

func (h *Handler) runPromoteJobSync(ctx context.Context, run *JobRun, table, fnName string, hours, batch int) {
	totalMigrated := int64(0)
	batchCount := 0
	retentionInterval := fmt.Sprintf("%d hours", hours)
	for {
		if ctx.Err() != nil {
			break
		}
		var m int64
		err := h.db.QueryRow(ctx, fmt.Sprintf("SELECT %s($1::interval, $2::int)", fnName), retentionInterval, batch).Scan(&m)
		if err != nil {
			h.failJob(run, err.Error())
			return
		}
		if m == 0 {
			break
		}
		totalMigrated += m
		batchCount++
		h.UpdateProgress(run, totalMigrated, totalMigrated*5, batchCount, fmt.Sprintf("已迁移 %d 行", totalMigrated))
	}
	h.SetResult(run, map[string]any{
		"total_migrated":   totalMigrated,
		"batches_executed": batchCount,
	}, "迁移完成")
	registry := h.getJobRegistry()
	registry.mu.Lock()
	run.Status = JobStatusSucceeded
	run.Message = "迁移完成"
	registry.mu.Unlock()
}

var hotTablePromoteFnMap = map[string]string{
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

	run := h.StartJob(JobTypeDropPartition, map[string]any{
		"partition_name": req.PartitionName,
	}, r.Header.Get("X-Admin-User"), func(ctx context.Context, run *JobRun) {
		h.runDropJobAsync(ctx, run, req.PartitionName)
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{
		"run_id":      run.RunID,
		"status":      "queued",
		"async":       true,
		"polling_url": "/api/admin/data-lifecycle/jobs/" + run.RunID,
		"message":     fmt.Sprintf("正在调度删除 %s", req.PartitionName),
	})
}

func (h *Handler) runDropJobAsync(ctx context.Context, run *JobRun, partition string) {
	if strings.ContainsAny(partition, " ;'\"") {
		h.failJob(run, "invalid partition name characters")
		return
	}
	if _, err := h.db.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", partition)); err != nil {
		h.failJob(run, fmt.Sprintf("drop failed: %v", err))
		return
	}
	h.SetResult(run, map[string]any{
		"partition_name": partition,
	}, "已删除")
	registry := h.getJobRegistry()
	registry.mu.Lock()
	run.Status = JobStatusSucceeded
	run.Message = "已删除"
	registry.mu.Unlock()
}

// isHotTableName indicates whether a table is in the hot list
func isHotTableName(name string) bool {
	switch name {
	case "request_logs_hot", "usage_ledger_hot", "request_wal_hot",
		"routing_decision_log_hot", "credential_model_index_hot",
		"request_logs_bodies_hot", "credit_ledger_hot",
		"tool_usage_stats_hot", "model_probe_runs_hot":
		return true
	}
	return false
}

// ─── Async Job Endpoints ─────────────────────────────────────

func (h *Handler) handleDataLifecycleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		var n int
		for _, c := range v {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		if n > 0 && n <= 100 {
			limit = n
		}
	}
	running, history := h.listJobs(limit)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"running": running, "history": history})
}

func (h *Handler) handleDataLifecycleJobDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	const prefix = "/api/admin/data-lifecycle/jobs/"
	if len(r.URL.Path) <= len(prefix) {
		http.Error(w, "run_id is required", http.StatusBadRequest)
		return
	}
	runID := r.URL.Path[len(prefix):]
	if runID == "" {
		http.Error(w, "run_id is required", http.StatusBadRequest)
		return
	}
	run := h.getJob(runID)
	if run == nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(run)
}
