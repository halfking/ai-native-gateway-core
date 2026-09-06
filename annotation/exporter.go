// annotation/exporter.go — 2026-09-06
//
// P2.1标注样本导出器
//
// 功能:
//   - 从auto_route_selections导出低置信度样本
//   - 生成CSV文件供人工标注
//   - 只导出结构化特征，不包含prompt/messages
//
// Privacy:
//   - ✅ 只访问15个结构化特征字段
//   - ❌ 不访问prompt/messages/response字段

package annotation

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Exporter 标注样本导出器
type Exporter struct {
	db *pgxpool.Pool
}

// NewExporter 创建导出器
func NewExporter(db *pgxpool.Pool) *Exporter {
	return &Exporter{db: db}
}

// Export 导出低置信度样本到CSV文件
func (e *Exporter) Export(ctx context.Context, config ExportConfig) error {
	// 1. 查询低置信度样本
	rows, err := e.queryLowConfidenceSamples(ctx, config)
	if err != nil {
		return fmt.Errorf("query samples failed: %w", err)
	}

	// 2. 创建CSV文件
	file, err := os.Create(config.OutputPath)
	if err != nil {
		return fmt.Errorf("create output file failed: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// 3. 写入CSV header
	header := []string{
		"request_id", "model_name", "task_type", "prompt_tokens",
		"is_streaming", "has_vision", "region", "profile",
		"auto_provider", "confidence",
		"human_provider", "is_correct", "reason", "annotator",
	}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("write header failed: %w", err)
	}

	// 4. 写入数据行
	count := 0
	for _, row := range rows {
		record := []string{
			row.RequestID,
			row.ModelName,
			row.TaskType,
			strconv.Itoa(row.PromptTokens),
			strconv.FormatBool(row.IsStreaming),
			strconv.FormatBool(row.HasVision),
			row.Region,
			row.Profile,
			row.AutoProvider,
			fmt.Sprintf("%.3f", row.Confidence),
			"", // human_provider (待填写)
			"", // is_correct (待填写)
			"", // reason (待填写)
			"", // annotator (待填写)
		}

		if err := writer.Write(record); err != nil {
			return fmt.Errorf("write row %d failed: %w", count+1, err)
		}
		count++
	}

	if err := writer.Error(); err != nil {
		return fmt.Errorf("csv writer error: %w", err)
	}

	return nil
}

// queryLowConfidenceSamples 查询低置信度样本
func (e *Exporter) queryLowConfidenceSamples(ctx context.Context, config ExportConfig) ([]CSVAnnotationRow, error) {
	// Privacy-compliant query: 只访问结构化特征字段。auto_route_selections
	// 隐私最小化设计（migration 478/658）不记录 prompt_tokens/streaming/
	// vision/region，CSV 保留这些列但填零值；chosen_model 既是 model_name
	// 也是待人工确认的 auto_provider 标签。
	query := `
		SELECT
			ars.request_id,
			ars.chosen_model AS model_name,
			ars.task_type,
			ars.chosen_model AS auto_provider,
			ars.profile,
			ars.confidence
		FROM auto_route_selections_all ars
		WHERE ars.ts >= $1
		  AND ars.ts < $2
	`

	// 添加置信度过滤条件
	args := []interface{}{config.StartDate, config.EndDate}
	argIdx := 3

	if config.MinConfidence != nil {
		query += fmt.Sprintf(" AND ars.confidence >= $%d", argIdx)
		args = append(args, *config.MinConfidence)
		argIdx++
	}

	if config.MaxConfidence != nil {
		query += fmt.Sprintf(" AND ars.confidence < $%d", argIdx)
		args = append(args, *config.MaxConfidence)
		argIdx++
	}

	// 排除已标注的样本
	query += `
		AND NOT EXISTS (
			SELECT 1 FROM training_human_annotations tha
			WHERE tha.request_id = ars.request_id
		)
	`

	// 排序和限制
	query += " ORDER BY ars.confidence ASC, ars.ts DESC"

	if config.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, config.Limit)
	}

	// 执行查询
	dbRows, err := e.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer dbRows.Close()

	// 解析结果
	var rows []CSVAnnotationRow
	for dbRows.Next() {
		var row CSVAnnotationRow
		err := dbRows.Scan(
			&row.RequestID,
			&row.ModelName,
			&row.TaskType,
			&row.AutoProvider,
			&row.Profile,
			&row.Confidence,
		)
		if err != nil {
			return nil, fmt.Errorf("scan row failed: %w", err)
		}
		rows = append(rows, row)
	}

	if err := dbRows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration failed: %w", err)
	}

	return rows, nil
}

// GetExportStats 获取可导出样本统计
func (e *Exporter) GetExportStats(ctx context.Context, config ExportConfig) (int, error) {
	query := `
		SELECT COUNT(*)
		FROM auto_route_selections_all ars
		WHERE ars.ts >= $1
		  AND ars.ts < $2
	`

	args := []interface{}{config.StartDate, config.EndDate}
	argIdx := 3

	if config.MinConfidence != nil {
		query += fmt.Sprintf(" AND ars.confidence >= $%d", argIdx)
		args = append(args, *config.MinConfidence)
		argIdx++
	}

	if config.MaxConfidence != nil {
		query += fmt.Sprintf(" AND ars.confidence < $%d", argIdx)
		args = append(args, *config.MaxConfidence)
		argIdx++
	}

	query += `
		AND NOT EXISTS (
			SELECT 1 FROM training_human_annotations tha
			WHERE tha.request_id = ars.request_id
		)
	`

	var count int
	err := e.db.QueryRow(ctx, query, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("query count failed: %w", err)
	}

	return count, nil
}

// ParseDateRange 解析日期范围字符串
func ParseDateRange(start, end string) (time.Time, time.Time, error) {
	startDate, err := time.Parse("2006-01-02", start)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start date: %w", err)
	}

	endDate, err := time.Parse("2006-01-02", end)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid end date: %w", err)
	}

	// end date is exclusive, so add 1 day
	endDate = endDate.Add(24 * time.Hour)

	if startDate.After(endDate) {
		return time.Time{}, time.Time{}, fmt.Errorf("start date must not be after end date")
	}

	return startDate, endDate, nil
}
