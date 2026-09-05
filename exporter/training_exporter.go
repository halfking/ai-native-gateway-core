// exporter/training_exporter.go — 2026-09-06
//
// TrainingExporter 从auto_route_selections导出结构化特征+标签。
//
// Privacy compliance:
//   - ✅ 查询白名单：只访问15个结构化特征字段
//   - ❌ 禁止访问：prompt, messages, response, summary, keywords
//   - ✅ 去重：基于content_hash或request_id
//   - ✅ 导出格式：Parquet（列式存储，高效压缩）
//
// Usage:
//   exporter := NewTrainingExporter(db)
//   exportID, err := exporter.Export(ctx, configID, outputPath)
//
// Part of: P2.2 - Training Data Export Pipeline

package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xitongsys/parquet-go-source/local"
	"github.com/xitongsys/parquet-go/writer"
)

// TrainingExporter 训练数据导出器。
type TrainingExporter struct {
	db *pgxpool.Pool
}

// NewTrainingExporter 构造导出器。
func NewTrainingExporter(db *pgxpool.Pool) *TrainingExporter {
	return &TrainingExporter{db: db}
}

// ExportConfig 导出配置（从training_export_configs表加载）。
type ExportConfig struct {
	ID              int
	Name            string
	FeatureVersion  string
	TimeRangeStart  time.Time
	TimeRangeEnd    time.Time
	QualityFilters  map[string]interface{}
	DedupStrategy   string
	OutputFormat    string
	Compression     string
}

// ExportResult 导出结果统计。
type ExportResult struct {
	ExportID         int
	RowCountRaw      int
	RowCountFiltered int
	RowCountDeduped  int
	FileSizeBytes    int64
	DurationSeconds  int
	OutputPath       string
}

// Export 执行导出任务。
// 流程：
//   1. 加载配置
//   2. 创建执行记录（status=pending）
//   3. 查询数据（只访问结构化特征字段）
//   4. 质量过滤
//   5. 去重
//   6. 写入Parquet文件
//   7. 更新执行记录（status=completed）
func (e *TrainingExporter) Export(ctx context.Context, configID int, outputPath string) (*ExportResult, error) {
	startTime := time.Now()

	// 1. 加载配置
	config, err := e.loadConfig(ctx, configID)
	if err != nil {
		return nil, fmt.Errorf("load config failed: %w", err)
	}

	slog.Info("starting export",
		"config_id", configID,
		"config_name", config.Name,
		"time_range", fmt.Sprintf("%s to %s", config.TimeRangeStart.Format("2006-01-02"), config.TimeRangeEnd.Format("2006-01-02")),
		"output_path", outputPath)

	// 2. 创建执行记录
	exportID, err := e.createExportRecord(ctx, configID, outputPath)
	if err != nil {
		return nil, fmt.Errorf("create export record failed: %w", err)
	}

	// 更新状态为running
	if err := e.updateExportStatus(ctx, exportID, "running", nil); err != nil {
		return nil, fmt.Errorf("update status to running failed: %w", err)
	}

	// 3. 查询数据（PRIVACY: 只访问结构化特征字段）
	records, err := e.queryRecords(ctx, config)
	if err != nil {
		e.updateExportStatus(ctx, exportID, "failed", err)
		return nil, fmt.Errorf("query records failed: %w", err)
	}

	rowCountRaw := len(records)
	slog.Info("query completed", "row_count_raw", rowCountRaw)

	// 边界条件检查：空数据集
	if rowCountRaw == 0 {
		err := fmt.Errorf("no records found in time range %s to %s",
			config.TimeRangeStart.Format("2006-01-02"),
			config.TimeRangeEnd.Format("2006-01-02"))
		e.updateExportStatus(ctx, exportID, "failed", err)
		return nil, err
	}

	// 4. 质量过滤
	filtered := e.filterRecords(records, config.QualityFilters)
	rowCountFiltered := len(filtered)
	slog.Info("quality filter completed",
		"row_count_filtered", rowCountFiltered,
		"filtered_out", rowCountRaw-rowCountFiltered)

	// 5. 去重
	deduped := e.dedupRecords(filtered, config.DedupStrategy)
	rowCountDeduped := len(deduped)
	slog.Info("deduplication completed",
		"row_count_deduped", rowCountDeduped,
		"duplicates_removed", rowCountFiltered-rowCountDeduped,
		"dedup_strategy", config.DedupStrategy)

	// 边界条件检查：过滤/去重后为空
	if rowCountDeduped == 0 {
		err := fmt.Errorf("no records after quality filtering and deduplication (raw: %d, filtered: %d, deduped: %d)",
			rowCountRaw, rowCountFiltered, rowCountDeduped)
		e.updateExportStatus(ctx, exportID, "failed", err)
		return nil, err
	}

	// 6. 写入Parquet文件
	fileSizeBytes, err := e.writeParquetFile(deduped, outputPath, config.Compression)
	if err != nil {
		e.updateExportStatus(ctx, exportID, "failed", err)
		return nil, fmt.Errorf("write parquet file failed: %w", err)
	}

	slog.Info("parquet file written",
		"output_path", outputPath,
		"file_size_bytes", fileSizeBytes,
		"compression", config.Compression)

	// 7. 更新执行记录
	duration := time.Since(startTime)
	result := &ExportResult{
		ExportID:         exportID,
		RowCountRaw:      rowCountRaw,
		RowCountFiltered: rowCountFiltered,
		RowCountDeduped:  rowCountDeduped,
		FileSizeBytes:    fileSizeBytes,
		DurationSeconds:  int(duration.Seconds()),
		OutputPath:       outputPath,
	}

	if err := e.updateExportComplete(ctx, result); err != nil {
		return nil, fmt.Errorf("update export complete failed: %w", err)
	}

	slog.Info("export completed",
		"export_id", exportID,
		"duration_seconds", result.DurationSeconds,
		"row_count_deduped", rowCountDeduped)

	return result, nil
}

// loadConfig 从数据库加载导出配置。
func (e *TrainingExporter) loadConfig(ctx context.Context, configID int) (*ExportConfig, error) {
	query := `
		SELECT id, name, feature_version, time_range_start, time_range_end,
		       quality_filters, dedup_strategy, output_format, compression
		FROM training_export_configs
		WHERE id = $1
	`

	var config ExportConfig
	var qualityFiltersJSON []byte

	err := e.db.QueryRow(ctx, query, configID).Scan(
		&config.ID,
		&config.Name,
		&config.FeatureVersion,
		&config.TimeRangeStart,
		&config.TimeRangeEnd,
		&qualityFiltersJSON,
		&config.DedupStrategy,
		&config.OutputFormat,
		&config.Compression,
	)
	if err != nil {
		return nil, fmt.Errorf("query config failed: %w", err)
	}

	// 解析quality_filters JSON
	if len(qualityFiltersJSON) > 0 {
		if err := json.Unmarshal(qualityFiltersJSON, &config.QualityFilters); err != nil {
			return nil, fmt.Errorf("parse quality_filters failed: %w", err)
		}
	}

	return &config, nil
}

// createExportRecord 创建导出执行记录（status=pending）。
func (e *TrainingExporter) createExportRecord(ctx context.Context, configID int, outputPath string) (int, error) {
	query := `
		INSERT INTO training_exports (config_id, status, output_path, triggered_by)
		VALUES ($1, 'pending', $2, 'cli')
		RETURNING id
	`

	var exportID int
	err := e.db.QueryRow(ctx, query, configID, outputPath).Scan(&exportID)
	if err != nil {
		return 0, fmt.Errorf("insert export record failed: %w", err)
	}

	return exportID, nil
}

// updateExportStatus 更新导出状态。
func (e *TrainingExporter) updateExportStatus(ctx context.Context, exportID int, status string, err error) error {
	var query string
	var args []interface{}

	if err != nil {
		query = `
			UPDATE training_exports
			SET status = $1, error_message = $2
			WHERE id = $3
		`
		args = []interface{}{status, err.Error(), exportID}
	} else {
		query = `
			UPDATE training_exports
			SET status = $1, started_at = NOW()
			WHERE id = $2
		`
		args = []interface{}{status, exportID}
	}

	_, execErr := e.db.Exec(ctx, query, args...)
	return execErr
}

// updateExportComplete 更新导出完成状态（包含统计数据）。
func (e *TrainingExporter) updateExportComplete(ctx context.Context, result *ExportResult) error {
	query := `
		UPDATE training_exports
		SET status = 'completed',
		    row_count_raw = $1,
		    row_count_filtered = $2,
		    row_count_deduped = $3,
		    file_size_bytes = $4,
		    completed_at = NOW()
		WHERE id = $5
	`

	_, err := e.db.Exec(ctx, query,
		result.RowCountRaw,
		result.RowCountFiltered,
		result.RowCountDeduped,
		result.FileSizeBytes,
		result.ExportID)

	return err
}

// queryRecords 查询符合条件的记录（PRIVACY: 只访问结构化特征字段）。
func (e *TrainingExporter) queryRecords(ctx context.Context, config *ExportConfig) ([]*TrainingDataRecord, error) {
	// PRIVACY: 只查询15个结构化特征字段，不查询prompt/messages/response
	query := `
		SELECT 
		  request_id,
		  EXTRACT(EPOCH FROM ts) * 1000 AS timestamp,
		  task_type,
		  profile,
		  classifier,
		  confidence,
		  detected_language,
		  prompt_length_bucket,
		  context_length_bucket,
		  turn_count_bucket,
		  has_code_indicator,
		  has_math_indicator,
		  has_table_indicator,
		  has_multimedia_indicator,
		  intent_category,
		  domain_hint,
		  complexity_bucket,
		  latency_sensitive,
		  cost_sensitive,
		  feature_version,
		  content_hash,
		  chosen_model,
		  success,
		  latency_ms,
		  reward
		FROM auto_route_selections
		WHERE DATE(ts) >= $1
		  AND DATE(ts) <= $2
		  AND feature_version = $3
		ORDER BY ts
	`

	rows, err := e.db.Query(ctx, query,
		config.TimeRangeStart,
		config.TimeRangeEnd,
		config.FeatureVersion)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var records []*TrainingDataRecord
	for rows.Next() {
		rec := &TrainingDataRecord{}
		err := rows.Scan(
			&rec.RequestID,
			&rec.Timestamp,
			&rec.TaskType,
			&rec.Profile,
			&rec.Classifier,
			&rec.Confidence,
			&rec.DetectedLanguage,
			&rec.PromptLengthBucket,
			&rec.ContextLengthBucket,
			&rec.TurnCountBucket,
			&rec.HasCodeIndicator,
			&rec.HasMathIndicator,
			&rec.HasTableIndicator,
			&rec.HasMultimediaIndicator,
			&rec.IntentCategory,
			&rec.DomainHint,
			&rec.ComplexityBucket,
			&rec.LatencySensitive,
			&rec.CostSensitive,
			&rec.FeatureVersion,
			&rec.ContentHash,
			&rec.ChosenModel,
			&rec.Success,
			&rec.LatencyMs,
			&rec.Reward,
		)
		if err != nil {
			return nil, fmt.Errorf("scan row failed: %w", err)
		}
		records = append(records, rec)
	}

	return records, rows.Err()
}

// filterRecords 根据质量过滤条件过滤记录。
func (e *TrainingExporter) filterRecords(records []*TrainingDataRecord, filters map[string]interface{}) []*TrainingDataRecord {
	if len(filters) == 0 {
		return records // 无过滤条件，返回全部
	}

	var filtered []*TrainingDataRecord
	for _, rec := range records {
		if e.matchFilters(rec, filters) {
			filtered = append(filtered, rec)
		}
	}
	return filtered
}

// matchFilters 检查记录是否匹配过滤条件。
func (e *TrainingExporter) matchFilters(rec *TrainingDataRecord, filters map[string]interface{}) bool {
	// min_confidence
	if minConf, ok := filters["min_confidence"].(float64); ok {
		if rec.Confidence < minConf {
			return false
		}
	}

	// max_confidence
	if maxConf, ok := filters["max_confidence"].(float64); ok {
		if rec.Confidence > maxConf {
			return false
		}
	}

	// require_settled: 只导出已结算的行（reward不为nil）
	if requireSettled, ok := filters["require_settled"].(bool); ok && requireSettled {
		if rec.Reward == nil {
			return false
		}
	}

	// min_reward
	if minReward, ok := filters["min_reward"].(float64); ok {
		if rec.Reward == nil || *rec.Reward < minReward {
			return false
		}
	}

	// allowed_profiles
	if allowedProfiles, ok := filters["allowed_profiles"].([]interface{}); ok {
		found := false
		for _, p := range allowedProfiles {
			if pStr, ok := p.(string); ok && pStr == rec.Profile {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// exclude_explore: 排除探索模式（classifier="explore_*"）
	if excludeExplore, ok := filters["exclude_explore"].(bool); ok && excludeExplore {
		if len(rec.Classifier) > 8 && rec.Classifier[:8] == "explore_" {
			return false
		}
	}

	return true
}

// dedupRecords 根据去重策略去重。
func (e *TrainingExporter) dedupRecords(records []*TrainingDataRecord, strategy string) []*TrainingDataRecord {
	if strategy == "none" {
		return records // 不去重
	}

	seen := make(map[string]bool)
	var deduped []*TrainingDataRecord

	for _, rec := range records {
		var key string
		switch strategy {
		case "content_hash":
			key = rec.ContentHash
		case "request_id":
			key = rec.RequestID
		default:
			key = rec.ContentHash // 默认使用content_hash
		}

		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, rec)
		}
	}

	return deduped
}

// writeParquetFile 写入Parquet文件。
func (e *TrainingExporter) writeParquetFile(records []*TrainingDataRecord, outputPath string, compression string) (int64, error) {
	// 创建输出文件
	fw, err := local.NewLocalFileWriter(outputPath)
	if err != nil {
		return 0, fmt.Errorf("create file writer failed: %w", err)
	}
	defer fw.Close()

	// 创建Parquet writer
	pw, err := writer.NewParquetWriter(fw, new(TrainingDataRecord), 4) // 4 goroutines
	if err != nil {
		return 0, fmt.Errorf("create parquet writer failed: %w", err)
	}
	defer func() {
		if err := pw.WriteStop(); err != nil {
			slog.Error("failed to stop parquet writer during cleanup", "error", err)
		}
	}()

	// 设置压缩
	switch compression {
	case "snappy":
		pw.CompressionType = 1 // SNAPPY
	case "gzip":
		pw.CompressionType = 2 // GZIP
	case "none":
		pw.CompressionType = 0 // UNCOMPRESSED
	default:
		pw.CompressionType = 1 // 默认SNAPPY
	}

	// 写入记录
	const progressInterval = 100000 // 每10万行输出一次进度
	totalRecords := len(records)
	
	for i, rec := range records {
		if err := pw.Write(rec); err != nil {
			return 0, fmt.Errorf("write record failed: %w", err)
		}
		
		// 进度日志（改善大数据集导出体验）
		if totalRecords > progressInterval && (i+1)%progressInterval == 0 {
			percent := float64(i+1) / float64(totalRecords) * 100
			slog.Info("export progress",
				"written", i+1,
				"total", totalRecords,
				"percent", fmt.Sprintf("%.1f%%", percent))
		}
	}
	
	if totalRecords > progressInterval {
		slog.Info("export progress - completed", "written", totalRecords, "total", totalRecords, "percent", "100.0%")
	}

	// 显式调用WriteStop以捕获错误（defer会在失败时再次调用，但会被忽略）
	if err := pw.WriteStop(); err != nil {
		return 0, fmt.Errorf("write stop failed: %w", err)
	}

	// 获取文件大小
	fileInfo, err := os.Stat(outputPath)
	if err != nil {
		return 0, fmt.Errorf("stat file failed: %w", err)
	}

	return fileInfo.Size(), nil
}

// ListExports 列出导出历史。
func (e *TrainingExporter) ListExports(ctx context.Context, configID *int, limit int) ([]ExportSummary, error) {
	query := `
		SELECT 
		  e.id,
		  c.name AS config_name,
		  e.status,
		  e.row_count_deduped,
		  e.file_size_bytes,
		  e.output_path,
		  e.duration_seconds,
		  e.created_at
		FROM training_exports e
		JOIN training_export_configs c ON c.id = e.config_id
	`

	var args []interface{}
	if configID != nil {
		query += " WHERE e.config_id = $1"
		args = append(args, *configID)
	}

	query += " ORDER BY e.created_at DESC LIMIT $" + fmt.Sprintf("%d", len(args)+1)
	args = append(args, limit)

	rows, err := e.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query exports failed: %w", err)
	}
	defer rows.Close()

	var exports []ExportSummary
	for rows.Next() {
		var exp ExportSummary
		err := rows.Scan(
			&exp.ID,
			&exp.ConfigName,
			&exp.Status,
			&exp.RowCount,
			&exp.FileSizeBytes,
			&exp.OutputPath,
			&exp.DurationSeconds,
			&exp.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan export failed: %w", err)
		}
		exports = append(exports, exp)
	}

	return exports, rows.Err()
}

// ExportSummary 导出摘要（用于list命令）。
type ExportSummary struct {
	ID              int
	ConfigName      string
	Status          string
	RowCount        *int
	FileSizeBytes   *int64
	OutputPath      string
	DurationSeconds *int
	CreatedAt       time.Time
}
