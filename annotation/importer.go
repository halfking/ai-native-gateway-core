// annotation/importer.go — 2026-09-06
//
// P2.1标注数据导入器
//
// 功能:
//   - 从CSV文件导入人工标注结果
//   - 验证数据完整性和格式
//   - 存储到training_human_annotations表
//
// Privacy:
//   - ✅ CSV只包含结构化特征，不含原始内容
//   - ✅ 导入前验证字段合规性

package annotation

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Importer 标注数据导入器
type Importer struct {
	db *pgxpool.Pool
}

// NewImporter 创建导入器
func NewImporter(db *pgxpool.Pool) *Importer {
	return &Importer{db: db}
}

// Import 从CSV文件导入标注数据
func (i *Importer) Import(ctx context.Context, csvPath string) (*ImportResult, error) {
	startTime := time.Now()

	// 1. 打开CSV文件
	file, err := os.Open(csvPath)
	if err != nil {
		return nil, fmt.Errorf("open csv file failed: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)

	// 2. 读取header
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read header failed: %w", err)
	}

	// 3. 验证header
	if err := validateHeader(header); err != nil {
		return nil, fmt.Errorf("invalid header: %w", err)
	}

	// 4. 构建列索引映射
	colIndex := buildColumnIndex(header)

	// 5. 逐行读取并导入
	result := &ImportResult{
		Errors: make([]ImportError, 0),
	}

	rowNum := 1 // header是第0行，数据从第1行开始
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			result.Errors = append(result.Errors, ImportError{
				Row:     rowNum,
				Message: fmt.Sprintf("read row failed: %v", err),
			})
			result.ErrorRows++
			rowNum++
			continue
		}

		result.TotalRows++

		// 解析行
		row, err := parseCSVRow(record, colIndex)
		if err != nil {
			result.Errors = append(result.Errors, ImportError{
				Row:     rowNum,
				Line:    strings.Join(record, ","),
				Message: err.Error(),
			})
			result.ErrorRows++
			rowNum++
			continue
		}

		// 验证行
		if err := validateCSVRow(row); err != nil {
			result.Errors = append(result.Errors, ImportError{
				Row:     rowNum,
				Line:    strings.Join(record, ","),
				Message: err.Error(),
			})
			result.ErrorRows++
			rowNum++
			continue
		}

		// 检查是否已标注
		exists, err := i.annotationExists(ctx, row.RequestID)
		if err != nil {
			result.Errors = append(result.Errors, ImportError{
				Row:     rowNum,
				Message: fmt.Sprintf("check existence failed: %v", err),
			})
			result.ErrorRows++
			rowNum++
			continue
		}

		if exists {
			result.SkippedRows++
			rowNum++
			continue
		}

		// 插入标注
		if err := i.insertAnnotation(ctx, row); err != nil {
			result.Errors = append(result.Errors, ImportError{
				Row:     rowNum,
				Message: fmt.Sprintf("insert failed: %v", err),
			})
			result.ErrorRows++
			rowNum++
			continue
		}

		result.SuccessRows++
		rowNum++
	}

	result.DurationSeconds = int(time.Since(startTime).Seconds())

	return result, nil
}

// validateHeader 验证CSV header
func validateHeader(header []string) error {
	requiredCols := []string{
		"request_id", "auto_provider", "confidence",
		"human_provider", "is_correct", "annotator",
	}

	headerMap := make(map[string]bool)
	for _, col := range header {
		headerMap[col] = true
	}

	var missing []string
	for _, req := range requiredCols {
		if !headerMap[req] {
			missing = append(missing, req)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required columns: %v", missing)
	}

	return nil
}

// buildColumnIndex 构建列名到索引的映射
func buildColumnIndex(header []string) map[string]int {
	index := make(map[string]int)
	for i, col := range header {
		index[col] = i
	}
	return index
}

// parseCSVRow 解析CSV行
func parseCSVRow(record []string, colIndex map[string]int) (*CSVAnnotationRow, error) {
	row := &CSVAnnotationRow{}

	// 必填字段
	row.RequestID = getColumn(record, colIndex, "request_id")
	row.AutoProvider = getColumn(record, colIndex, "auto_provider")
	row.HumanProvider = getColumn(record, colIndex, "human_provider")
	row.IsCorrect = getColumn(record, colIndex, "is_correct")
	row.Annotator = getColumn(record, colIndex, "annotator")

	// 解析confidence
	confidenceStr := getColumn(record, colIndex, "confidence")
	if confidenceStr != "" {
		confidence, err := strconv.ParseFloat(confidenceStr, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid confidence: %s", confidenceStr)
		}
		row.Confidence = confidence
	}

	// 可选字段
	row.Reason = getColumn(record, colIndex, "reason")
	row.ModelName = getColumn(record, colIndex, "model_name")
	row.TaskType = getColumn(record, colIndex, "task_type")

	return row, nil
}

// getColumn 安全获取列值
func getColumn(record []string, colIndex map[string]int, colName string) string {
	idx, exists := colIndex[colName]
	if !exists || idx >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[idx])
}

// validateCSVRow 验证CSV行数据
func validateCSVRow(row *CSVAnnotationRow) error {
	// 必填字段检查
	if row.RequestID == "" {
		return fmt.Errorf("request_id is required")
	}
	if row.AutoProvider == "" {
		return fmt.Errorf("auto_provider is required")
	}
	if row.HumanProvider == "" {
		return fmt.Errorf("human_provider is required")
	}
	if row.IsCorrect == "" {
		return fmt.Errorf("is_correct is required")
	}
	if row.Annotator == "" {
		return fmt.Errorf("annotator is required")
	}

	// is_correct格式检查
	isCorrectLower := strings.ToLower(row.IsCorrect)
	if isCorrectLower != "true" && isCorrectLower != "false" {
		return fmt.Errorf("is_correct must be TRUE or FALSE, got: %s", row.IsCorrect)
	}

	// confidence范围检查
	if row.Confidence < 0.0 || row.Confidence > 1.0 {
		return fmt.Errorf("confidence must be in [0, 1], got: %.3f", row.Confidence)
	}

	// reason有效性检查
	if row.Reason != "" && !IsValidReason(row.Reason) {
		return fmt.Errorf("invalid reason: %s (must be one of: %v)",
			row.Reason, ValidAnnotationReasons())
	}

	return nil
}

// annotationExists 检查标注是否已存在
func (i *Importer) annotationExists(ctx context.Context, requestID string) (bool, error) {
	var exists bool
	err := i.db.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM training_human_annotations WHERE request_id = $1)",
		requestID,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// insertAnnotation 插入标注记录
func (i *Importer) insertAnnotation(ctx context.Context, row *CSVAnnotationRow) error {
	// 解析is_correct
	isCorrect := strings.ToLower(row.IsCorrect) == "true"

	// 准备reason（可选）
	var reason *string
	if row.Reason != "" {
		reason = &row.Reason
	}

	// 插入记录
	query := `
		INSERT INTO training_human_annotations (
			request_id, auto_label, auto_confidence,
			human_label, is_correct, annotation_reason, annotator
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`

	_, err := i.db.Exec(ctx, query,
		row.RequestID,
		row.AutoProvider,
		row.Confidence,
		row.HumanProvider,
		isCorrect,
		reason,
		row.Annotator,
	)

	if err != nil {
		return fmt.Errorf("insert annotation failed: %w", err)
	}

	return nil
}

// Validate 验证CSV文件但不导入
func (i *Importer) Validate(ctx context.Context, csvPath string) (*ValidateResult, error) {
	// 打开CSV文件
	file, err := os.Open(csvPath)
	if err != nil {
		return nil, fmt.Errorf("open csv file failed: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)

	// 读取header
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read header failed: %w", err)
	}

	// 验证header
	result := &ValidateResult{
		IsValid: true,
		Errors:  make([]ValidationError, 0),
	}

	if err := validateHeader(header); err != nil {
		result.IsValid = false
		result.Errors = append(result.Errors, ValidationError{
			Row:     0,
			Field:   "header",
			Message: err.Error(),
		})
		return result, nil
	}

	// 构建列索引
	colIndex := buildColumnIndex(header)

	// 验证每一行
	rowNum := 1
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			result.IsValid = false
			result.Errors = append(result.Errors, ValidationError{
				Row:     rowNum,
				Message: fmt.Sprintf("read row failed: %v", err),
			})
			result.InvalidRows++
			rowNum++
			continue
		}

		result.TotalRows++

		// 解析行
		row, err := parseCSVRow(record, colIndex)
		if err != nil {
			result.IsValid = false
			result.Errors = append(result.Errors, ValidationError{
				Row:     rowNum,
				Message: err.Error(),
			})
			result.InvalidRows++
			rowNum++
			continue
		}

		// 验证行
		if err := validateCSVRow(row); err != nil {
			result.IsValid = false
			result.Errors = append(result.Errors, ValidationError{
				Row:     rowNum,
				Message: err.Error(),
			})
			result.InvalidRows++
			rowNum++
			continue
		}

		result.ValidRows++
		rowNum++
	}

	return result, nil
}

// BatchInsert 批量插入标注（用于性能优化）
func (i *Importer) BatchInsert(ctx context.Context, rows []CSVAnnotationRow) error {
	if len(rows) == 0 {
		return nil
	}

	// 使用COPY协议批量插入
	batch := &pgx.Batch{}
	query := `
		INSERT INTO training_human_annotations (
			request_id, auto_label, auto_confidence,
			human_label, is_correct, annotation_reason, annotator
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (request_id) DO NOTHING
	`

	for _, row := range rows {
		isCorrect := strings.ToLower(row.IsCorrect) == "true"
		var reason *string
		if row.Reason != "" {
			reason = &row.Reason
		}

		batch.Queue(query,
			row.RequestID,
			row.AutoProvider,
			row.Confidence,
			row.HumanProvider,
			isCorrect,
			reason,
			row.Annotator,
		)
	}

	br := i.db.SendBatch(ctx, batch)
	defer br.Close()

	// 消费所有结果
	for range rows {
		_, err := br.Exec()
		if err != nil {
			return fmt.Errorf("batch insert failed: %w", err)
		}
	}

	return nil
}
