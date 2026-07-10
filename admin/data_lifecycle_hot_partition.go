package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// promoteHotRequest 手动触发 hot 表迁移请求
type promoteHotRequest struct {
	TableName      string `json:"table_name"`      // "request_logs_hot", "usage_ledger_hot" 等
	RetentionHours int    `json:"retention_hours"` // 迁移超过 N 小时的数据，默认 168 (7天)
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

	// 默认值
	if req.RetentionHours == 0 {
		req.RetentionHours = 168 // 7 天
	}
	if req.BatchSize == 0 {
		req.BatchSize = 1000
	}
	if req.BatchSize > 10000 {
		req.BatchSize = 10000 // 限制最大批次
	}

	// 验证表名
	validTables := map[string]string{
		"request_logs_hot":           "promote_request_logs_hot_to_partition",
		"usage_ledger_hot":           "promote_usage_ledger_hot_to_partition",
		"request_wal_hot":            "promote_request_wal_hot_to_partition",
		"routing_decision_log_hot":   "promote_routing_decision_log_hot_to_partition",
		"credential_model_index_hot": "promote_credential_model_index_hot_to_partition",
		"request_logs_bodies_hot":    "promote_request_logs_bodies_hot_to_partition",
		"credit_ledger_hot":          "promote_credit_ledger_hot_to_partition",
		"tool_usage_stats_hot":       "promote_tool_usage_stats_hot_to_partition",
	}

	fnName, ok := validTables[req.TableName]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid table_name: %s", req.TableName))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	resp := promoteHotResponse{
		TableName: req.TableName,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Status:    "success",
	}
	start := time.Now()

	slog.Info("data-lifecycle: manual promote hot table start",
		"table", req.TableName,
		"retention_hours", req.RetentionHours,
		"batch_size", req.BatchSize,
		"max_batches", req.MaxBatches)

	// 循环执行迁移，直到没有更多数据或达到批次限制
	totalMigrated := int64(0)
	batchCount := 0
	retentionInterval := fmt.Sprintf("%d hours", req.RetentionHours)

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

		// 调用迁移函数
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
				"table", req.TableName,
				"batch", batchCount+1,
				"error", err)
			break
		}

		if migrated == 0 {
			// 没有更多数据需要迁移
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

	// 特别提醒：如果是 request_logs_hot，迁移后需要 VACUUM 才能真正释放空间
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
	//nolint:errcheck
	json.NewEncoder(w).Encode(resp)
}

// POST /api/admin/data-lifecycle/partitions/drop
//
// 删除指定的分区表及其数据。
// 用途：手动清理旧分区数据，释放磁盘空间。
// 安全措施：
// 1. 需要 super_admin 权限
// 2. 需要 confirm=true
// 3. 只能删除已存在的分区表，不能删除 hot 表
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

	// 安全检查：不允许删除 hot 表
	if req.PartitionName == "request_logs_hot" ||
		req.PartitionName == "usage_ledger_hot" ||
		req.PartitionName == "routing_decision_log_hot" ||
		req.PartitionName == "credential_model_index_hot" {
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

	// 1. 检查分区表是否存在，获取大小和行数
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
		&parentTable,
		&sizeBytes,
		&sizeHuman,
		&rowCount,
	)

	if err != nil {
		resp.Status = "failed"
		resp.Message = fmt.Sprintf("partition not found or not a partition table: %v", err)
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck
		json.NewEncoder(w).Encode(resp)
		return
	}

	resp.ParentTable = parentTable
	resp.SpaceFreed = sizeBytes
	resp.SpaceFreedHuman = sizeHuman
	resp.RowsDeleted = rowCount

	slog.Warn("data-lifecycle: dropping partition",
		"partition", req.PartitionName,
		"parent_table", parentTable,
		"rows", rowCount,
		"size", sizeHuman)

	// 2. 执行删除
	dropSQL := fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", req.PartitionName)
	_, err = h.db.Exec(ctx, dropSQL)
	if err != nil {
		resp.Status = "failed"
		resp.Message = fmt.Sprintf("drop failed: %v", err)
		slog.Error("data-lifecycle: drop partition failed",
			"partition", req.PartitionName,
			"error", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck
		json.NewEncoder(w).Encode(resp)
		return
	}

	resp.Message = fmt.Sprintf("successfully dropped partition %s from %s", req.PartitionName, parentTable)

	slog.Warn("data-lifecycle: partition dropped",
		"partition", req.PartitionName,
		"parent_table", parentTable,
		"rows_deleted", rowCount,
		"space_freed", sizeHuman)

	w.Header().Set("Content-Type", "application/json")
	//nolint:errcheck
	json.NewEncoder(w).Encode(resp)
}
