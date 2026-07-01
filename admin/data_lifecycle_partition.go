package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// partitionedTableConfig defines configuration for a partitioned table
type partitionedTableConfig struct {
	TableName        string // e.g., "request_logs", "request_wal"
	ArchiveTableName string // e.g., "request_logs_archive", "request_wal_archive"
	PartitionColumn  string // e.g., "ts", "created_at"
	Description      string // Human-readable description
	HasArchiveFunc   bool   // Whether archive_<table>() function exists
}

// Supported partitioned tables for columnar archive management
var partitionedTables = []partitionedTableConfig{
	{
		TableName:        "request_logs",
		ArchiveTableName: "request_logs_archive",
		PartitionColumn:  "ts",
		Description:      "请求日志表（主表）",
		HasArchiveFunc:   true,
	},
	{
		TableName:        "request_wal",
		ArchiveTableName: "request_wal_archive",
		PartitionColumn:  "created_at",
		Description:      "请求预写日志表",
		HasArchiveFunc:   true, // Added in migration 305
	},
	{
		TableName:        "routing_decision_log",
		ArchiveTableName: "routing_decision_log_archive",
		PartitionColumn:  "ts",
		Description:      "路由决策日志表",
		HasArchiveFunc:   true, // Added in migration 318
	},
	{
		TableName:        "credential_model_index",
		ArchiveTableName: "credential_model_index_archive",
		PartitionColumn:  "bucket",
		Description:      "凭据模型健康度索引（5min rollup）",
		HasArchiveFunc:   true, // Added in migration 318; main table partitioned in 317
	},
}

// partitionInfo represents information about a table partition
type partitionInfo struct {
	PartitionName string    `json:"partition_name"`
	ParentTable   string    `json:"parent_table"`
	StartDate     time.Time `json:"start_date"`
	EndDate       time.Time `json:"end_date"`
	RowCount      int64     `json:"row_count"`
	SizeBytes     int64     `json:"size_bytes"`
	SizeHuman     string    `json:"size_human"`
	IsArchived    bool      `json:"is_archived"` // Whether in archive table (columnar)
	IsColumnar    bool      `json:"is_columnar"` // Whether using columnar storage
	CanArchive    bool      `json:"can_archive"` // Whether eligible for archive (>= 2 months old)
}

// partitionTableStatus represents the overall status of a partitioned table
type partitionTableStatus struct {
	TableName        string          `json:"table_name"`
	Description      string          `json:"description"`
	TotalPartitions  int             `json:"total_partitions"`
	ArchivedCount    int             `json:"archived_count"`
	ArchivableCount  int             `json:"archivable_count"` // Partitions that can be archived
	TotalRows        int64           `json:"total_rows"`
	TotalSizeBytes   int64           `json:"total_size_bytes"`
	TotalSizeHuman   string          `json:"total_size_human"`
	Partitions       []partitionInfo `json:"partitions"`
	HasArchiveFunc   bool            `json:"has_archive_func"`
	ArchiveTableName string          `json:"archive_table_name"`
}

// GET /api/admin/data-lifecycle/partitions
// Returns status of all partitioned tables and their partitions
func (h *Handler) handleDataLifecyclePartitions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result := make([]partitionTableStatus, 0, len(partitionedTables))

	for _, tableConfig := range partitionedTables {
		status, err := h.getPartitionTableStatus(ctx, tableConfig)
		if err != nil {
			slog.Warn("data_lifecycle_partitions: failed to get status",
				"table", tableConfig.TableName,
				"error", err)
			continue
		}
		result = append(result, status)
	}

	w.Header().Set("Content-Type", "application/json")
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(result)
}

// getPartitionTableStatus retrieves partition information for a table
func (h *Handler) getPartitionTableStatus(ctx context.Context, config partitionedTableConfig) (partitionTableStatus, error) {
	status := partitionTableStatus{
		TableName:        config.TableName,
		Description:      config.Description,
		HasArchiveFunc:   config.HasArchiveFunc,
		ArchiveTableName: config.ArchiveTableName,
		Partitions:       make([]partitionInfo, 0),
	}

	// Get overall table size
	query := fmt.Sprintf(`
		SELECT 
			COUNT(*) AS total_rows,
			pg_total_relation_size('%s') AS total_size,
			pg_size_pretty(pg_total_relation_size('%s')) AS total_size_human
		FROM %s
	`, config.TableName, config.TableName, config.TableName)

	err := h.db.QueryRow(ctx, query).Scan(
		&status.TotalRows,
		&status.TotalSizeBytes,
		&status.TotalSizeHuman,
	)
	if err != nil {
		return status, fmt.Errorf("failed to get table stats: %w", err)
	}

	// Get partition information
	// Query both regular partitions and archive partitions
	partitionQuery := `
		WITH partition_info AS (
			SELECT 
				c.relname AS partition_name,
				pg_get_expr(c.relpartbound, c.oid) AS partition_bound,
				CASE 
					WHEN c.relam = (SELECT oid FROM pg_am WHERE amname = 'columnar') 
					THEN true 
					ELSE false 
				END AS is_columnar,
				pg_total_relation_size(c.oid) AS size_bytes,
				pg_size_pretty(pg_total_relation_size(c.oid)) AS size_human
			FROM pg_class c
			JOIN pg_inherits i ON c.oid = i.inhrelid
			JOIN pg_class p ON i.inhparent = p.oid
			WHERE p.relname IN ($1, $2)
				AND c.relkind = 'r'
		)
		SELECT 
			partition_name,
			partition_bound,
			is_columnar,
			size_bytes,
			size_human
		FROM partition_info
		ORDER BY partition_name
	`

	rows, err := h.db.Query(ctx, partitionQuery, config.TableName, config.ArchiveTableName)
	if err != nil {
		return status, fmt.Errorf("failed to query partitions: %w", err)
	}
	defer rows.Close()

	twoMonthsAgo := time.Now().AddDate(0, -2, 0)

	for rows.Next() {
		var pinfo partitionInfo
		var partitionBound string

		err := rows.Scan(
			&pinfo.PartitionName,
			&partitionBound,
			&pinfo.IsColumnar,
			&pinfo.SizeBytes,
			&pinfo.SizeHuman,
		)
		if err != nil {
			slog.Warn("data_lifecycle_partitions: failed to scan partition row", "error", err)
			continue
		}

		// Parse partition bounds to get start/end dates
		startDate, endDate, err := parsePartitionBounds(partitionBound)
		if err != nil {
			slog.Warn("data_lifecycle_partitions: failed to parse partition bounds",
				"partition", pinfo.PartitionName,
				"bounds", partitionBound,
				"error", err)
			continue
		}
		pinfo.StartDate = startDate
		pinfo.EndDate = endDate

		// Determine if partition is in archive table
		pinfo.IsArchived = containsSubstring(pinfo.PartitionName, "_archive_")
		if pinfo.IsArchived {
			pinfo.ParentTable = config.ArchiveTableName
		} else {
			pinfo.ParentTable = config.TableName
		}

		// Get row count for this partition
		countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s", pinfo.PartitionName)
		err = h.db.QueryRow(ctx, countQuery).Scan(&pinfo.RowCount)
		if err != nil {
			slog.Warn("data_lifecycle_partitions: failed to get partition row count",
				"partition", pinfo.PartitionName,
				"error", err)
			pinfo.RowCount = -1 // Indicate unknown
		}

		// Check if partition can be archived (>= 2 months old and not already archived)
		pinfo.CanArchive = !pinfo.IsArchived && pinfo.EndDate.Before(twoMonthsAgo) && config.HasArchiveFunc

		status.Partitions = append(status.Partitions, pinfo)
		status.TotalPartitions++

		if pinfo.IsArchived || pinfo.IsColumnar {
			status.ArchivedCount++
		}
		if pinfo.CanArchive {
			status.ArchivableCount++
		}
	}

	return status, nil
}

// POST /api/admin/data-lifecycle/partitions/archive
// Archives a specific partition to columnar storage
type archivePartitionRequest struct {
	TableName    string `json:"table_name"`    // e.g., "request_logs"
	ArchiveMonth string `json:"archive_month"` // YYYY-MM format
	DryRun       bool   `json:"dry_run"`
}

type archivePartitionResponse struct {
	Status           string `json:"status"` // "success", "skipped", "error", "dry_run"
	TableName        string `json:"table_name"`
	ArchiveMonth     string `json:"archive_month"`
	RowsMigrated     int64  `json:"rows_migrated"`
	PartitionDropped bool   `json:"partition_dropped"`
	Message          string `json:"message,omitempty"`
	Error            string `json:"error,omitempty"`
}

func (h *Handler) handleDataLifecycleArchivePartition(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req archivePartitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	// Validate table name
	var tableConfig *partitionedTableConfig
	for i := range partitionedTables {
		if partitionedTables[i].TableName == req.TableName {
			tableConfig = &partitionedTables[i]
			break
		}
	}
	if tableConfig == nil {
		writeError(w, http.StatusBadRequest, "unsupported table")
		return
	}

	if !tableConfig.HasArchiveFunc {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("archive function not available for %s", req.TableName))
		return
	}

	// Parse archive month
	archiveDate, err := time.Parse("2006-01", req.ArchiveMonth)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid archive_month format, expected YYYY-MM")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	resp := archivePartitionResponse{
		TableName:    req.TableName,
		ArchiveMonth: req.ArchiveMonth,
	}

	if req.DryRun {
		// For dry run, just check if partition exists and count rows
		partitionName := req.TableName + "_" + archiveDate.Format("2006_01")
		countQuery := `
			SELECT COUNT(*) 
			FROM pg_class 
			WHERE relname = $1 
				AND relnamespace = 'public'::regnamespace
		`
		var exists int
		err := h.db.QueryRow(ctx, countQuery, partitionName).Scan(&exists)
		if err != nil || exists == 0 {
			resp.Status = "skipped"
			resp.Message = fmt.Sprintf("Partition %s not found", partitionName)
		} else {
			// Count rows that would be migrated
			rowCountQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s", partitionName)
			err := h.db.QueryRow(ctx, rowCountQuery).Scan(&resp.RowsMigrated)
			if err != nil {
				resp.Status = "error"
				resp.Error = fmt.Sprintf("Failed to count rows: %v", err)
			} else {
				resp.Status = "dry_run"
				resp.Message = fmt.Sprintf("Would migrate %d rows from %s to columnar archive", resp.RowsMigrated, partitionName)
			}
		}
	} else {
		// Execute archive function
		funcName := fmt.Sprintf("archive_%s", req.TableName)
		query := fmt.Sprintf("SELECT status, rows_migrated, partition_dropped FROM %s($1)", funcName)

		var status string
		err := h.db.QueryRow(ctx, query, archiveDate).Scan(
			&status,
			&resp.RowsMigrated,
			&resp.PartitionDropped,
		)

		if err != nil {
			slog.Error("data_lifecycle_archive: archive function failed",
				"table", req.TableName,
				"month", req.ArchiveMonth,
				"error", err)
			resp.Status = "error"
			resp.Error = err.Error()
		} else {
			resp.Status = status
			switch status {
			case "success":
				resp.Message = fmt.Sprintf("Successfully migrated %d rows to columnar storage", resp.RowsMigrated)
			case "skipped":
				resp.Message = "Partition not found or already archived"
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(resp)
}

// POST /api/admin/data-lifecycle/partitions/archive-batch
// Archives multiple months for a table
type archiveBatchRequest struct {
	TableName string   `json:"table_name"`
	Months    []string `json:"months"` // Array of YYYY-MM strings
	DryRun    bool     `json:"dry_run"`
}

type archiveBatchResponse struct {
	TotalRequested int                        `json:"total_requested"`
	Results        []archivePartitionResponse `json:"results"`
}

func (h *Handler) handleDataLifecycleArchiveBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req archiveBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	resp := archiveBatchResponse{
		TotalRequested: len(req.Months),
		Results:        make([]archivePartitionResponse, 0, len(req.Months)),
	}

	for _, month := range req.Months {
		archiveReq := archivePartitionRequest{
			TableName:    req.TableName,
			ArchiveMonth: month,
			DryRun:       req.DryRun,
		}

		// Create a temporary response writer to capture the result
		// We'll call the single-archive handler logic directly
		result := h.executeArchivePartition(r.Context(), archiveReq)
		resp.Results = append(resp.Results, result)
	}

	w.Header().Set("Content-Type", "application/json")
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(resp)
}

// executeArchivePartition is the core logic extracted for reuse
func (h *Handler) executeArchivePartition(ctx context.Context, req archivePartitionRequest) archivePartitionResponse {
	resp := archivePartitionResponse{
		TableName:    req.TableName,
		ArchiveMonth: req.ArchiveMonth,
	}

	// Validate table name
	var tableConfig *partitionedTableConfig
	for i := range partitionedTables {
		if partitionedTables[i].TableName == req.TableName {
			tableConfig = &partitionedTables[i]
			break
		}
	}
	if tableConfig == nil {
		resp.Status = "error"
		resp.Error = "unsupported table"
		return resp
	}

	if !tableConfig.HasArchiveFunc {
		resp.Status = "error"
		resp.Error = fmt.Sprintf("archive function not available for %s", req.TableName)
		return resp
	}

	// Parse archive month
	archiveDate, err := time.Parse("2006-01", req.ArchiveMonth)
	if err != nil {
		resp.Status = "error"
		resp.Error = "invalid archive_month format, expected YYYY-MM"
		return resp
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	if req.DryRun {
		partitionName := req.TableName + "_" + archiveDate.Format("2006_01")
		countQuery := `
			SELECT COUNT(*) 
			FROM pg_class 
			WHERE relname = $1 
				AND relnamespace = 'public'::regnamespace
		`
		var exists int
		err := h.db.QueryRow(timeoutCtx, countQuery, partitionName).Scan(&exists)
		if err != nil || exists == 0 {
			resp.Status = "skipped"
			resp.Message = fmt.Sprintf("Partition %s not found", partitionName)
		} else {
			rowCountQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s", partitionName)
			err := h.db.QueryRow(timeoutCtx, rowCountQuery).Scan(&resp.RowsMigrated)
			if err != nil {
				resp.Status = "error"
				resp.Error = fmt.Sprintf("Failed to count rows: %v", err)
			} else {
				resp.Status = "dry_run"
				resp.Message = fmt.Sprintf("Would migrate %d rows from %s to columnar archive", resp.RowsMigrated, partitionName)
			}
		}
	} else {
		funcName := fmt.Sprintf("archive_%s", req.TableName)
		query := fmt.Sprintf("SELECT status, rows_migrated, partition_dropped FROM %s($1)", funcName)

		var status string
		err := h.db.QueryRow(timeoutCtx, query, archiveDate).Scan(
			&status,
			&resp.RowsMigrated,
			&resp.PartitionDropped,
		)

		if err != nil {
			slog.Error("data_lifecycle_archive: archive function failed",
				"table", req.TableName,
				"month", req.ArchiveMonth,
				"error", err)
			resp.Status = "error"
			resp.Error = err.Error()
		} else {
			resp.Status = status
			switch status {
			case "success":
				resp.Message = fmt.Sprintf("Successfully migrated %d rows to columnar storage", resp.RowsMigrated)
			case "skipped":
				resp.Message = "Partition not found or already archived"
			}
		}
	}

	return resp
}

// Helper function to parse PostgreSQL partition bounds
// Example: "FOR VALUES FROM ('2026-04-01') TO ('2026-05-01')"
func parsePartitionBounds(bounds string) (time.Time, time.Time, error) {
	// Simple parsing - assumes format "FOR VALUES FROM ('YYYY-MM-DD') TO ('YYYY-MM-DD')"
	var startStr, endStr string
	_, err := fmt.Sscanf(bounds, "FOR VALUES FROM ('%10s') TO ('%10s')", &startStr, &endStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("failed to parse bounds: %w", err)
	}

	startDate, err := time.Parse("2006-01-02", startStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("failed to parse start date: %w", err)
	}

	endDate, err := time.Parse("2006-01-02", endStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("failed to parse end date: %w", err)
	}

	return startDate, endDate, nil
}

// Helper function to check if a string contains a substring
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			(len(s) > len(substr) &&
				(s[:len(substr)] == substr ||
					s[len(s)-len(substr):] == substr ||
					containsSubstringMiddle(s, substr))))
}

func containsSubstringMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
