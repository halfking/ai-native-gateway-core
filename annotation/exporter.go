// annotation/exporter.go — 2026-09-06
//
// P2.1标注样本导出器
//
// 功能:
//   - 从auto_route_selections导出低置信度样本
//   - 生成CSV文件供人工标注
//   - 只导出结构化特征，不包含prompt/messages
//   - P2.2 Track D: ExportUncertain 主动学习不确定性采样导出
//     （置信度0.4–0.6优先，不足回填<0.4，task_type保底配额防偏斜）
//
// Privacy:
//   - ✅ 只访问15个结构化特征字段
//   - ❌ 不访问prompt/messages/response字段

package annotation

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"sort"
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

// exportCSVHeader 导出CSV的表头。
//
// 兼容性契约/隐私红线: 这些列自P2.1起冻结，任何采样模式（all/uncertain）
// 都不得增删列、改列名或改顺序——P2.1 importer按列名读取（validateHeader），
// 标注人员表格模板也依赖此格式。所有导出模式统一经由 writeAnnotationCSV
// 写出，保证header逐字节一致。绝不新增prompt/messages/response列。
var exportCSVHeader = []string{
	"request_id", "model_name", "task_type", "prompt_tokens",
	"is_streaming", "has_vision", "region", "profile",
	"auto_provider", "confidence",
	"human_provider", "is_correct", "reason", "annotator",
}

// Export 导出低置信度样本到CSV文件（CLI --sampling=all 的默认行为）。
//
// 语义: SQL内按confidence升序排序并LIMIT，confidence最低的样本优先。
// 该行为自P2.1起保持不变。
func (e *Exporter) Export(ctx context.Context, config ExportConfig) error {
	// 1. 查询低置信度样本（LIMIT下推到SQL，保持原行为）
	rows, err := e.querySamples(ctx, config, true)
	if err != nil {
		return fmt.Errorf("query samples failed: %w", err)
	}

	// 2. 写入CSV（header与行格式对所有采样模式一致）
	return writeAnnotationCSV(config.OutputPath, rows)
}

// UncertainSamplingOptions 主动学习不确定性采样参数。
type UncertainSamplingOptions struct {
	// UncertainLow/UncertainHigh 定义"模型最不确定"的置信度区间
	// [UncertainLow, UncertainHigh)。零值或非法区间回退到默认[0.4, 0.6)。
	UncertainLow  float64
	UncertainHigh float64

	// MinPerTaskType 每个task_type的保底配额：分配时先按类型轮转发放，
	// 避免导出被单一任务类型淹没偏斜。0表示不设保底（纯按置信度优先级）。
	MinPerTaskType int
}

// 默认不确定性区间: 置信度0.4–0.6的样本是模型最不确定、主动学习价值最高的。
const (
	defaultUncertainLow  = 0.4
	defaultUncertainHigh = 0.6
)

// DefaultUncertainSamplingOptions 返回默认采样参数：
// 置信度[0.4, 0.6)优先，每个task_type保底10条。
func DefaultUncertainSamplingOptions() UncertainSamplingOptions {
	return UncertainSamplingOptions{
		UncertainLow:   defaultUncertainLow,
		UncertainHigh:  defaultUncertainHigh,
		MinPerTaskType: 10,
	}
}

// SamplingCandidate 配额分配纯函数的输入单元（候选样本）。
type SamplingCandidate struct {
	RequestID  string
	TaskType   string
	Confidence float64
}

// SelectUncertainSamples 主动学习不确定性采样的配额分配纯函数。
//
// 输入: 候选样本（task_type+confidence）、总量上限limit、保底配额参数opts。
// 输出: 选中的候选集合（导出顺序）。不修改输入切片。
//
// 语义:
//   - 三层优先级: tier0 置信度∈[low,high)（模型最不确定）> tier1 <low
//     （不足时回填）> tier2 ≥high（仅兜底）。
//   - 同层内按|confidence-0.5|升序（越接近0.5越不确定），再按RequestID
//     字典序稳定排序，保证输出确定。
//   - MinPerTaskType>0时先按类型轮转（round-robin）发放保底配额: 某类型
//     样本不足时其缺口自然回流给其他类型；剩余预算再按全局优先级回填。
//   - limit<=0表示不限制，全部候选按优先级排序返回。
func SelectUncertainSamples(candidates []SamplingCandidate, limit int, opts UncertainSamplingOptions) []SamplingCandidate {
	// 参数规整: 零值/非法区间回退默认，保证纯函数对零值opts也安全
	if opts.UncertainHigh <= 0 || opts.UncertainHigh > 1 {
		opts.UncertainHigh = defaultUncertainHigh
	}
	if opts.UncertainLow <= 0 || opts.UncertainLow >= opts.UncertainHigh {
		low := defaultUncertainLow
		if low >= opts.UncertainHigh {
			low = opts.UncertainHigh / 2
		}
		opts.UncertainLow = low
	}

	if len(candidates) == 0 {
		return nil
	}

	capacity := len(candidates)
	if limit > 0 && limit < capacity {
		capacity = limit
	}
	selected := make([]SamplingCandidate, 0, capacity)
	isSelected := make(map[string]bool, capacity)
	take := func(cand SamplingCandidate) {
		selected = append(selected, cand)
		isSelected[cand.RequestID] = true
	}

	tierOf := func(conf float64) int {
		switch {
		case conf >= opts.UncertainLow && conf < opts.UncertainHigh:
			return 0
		case conf < opts.UncertainLow:
			return 1
		default:
			return 2
		}
	}
	less := func(a, b SamplingCandidate) bool {
		ta, tb := tierOf(a.Confidence), tierOf(b.Confidence)
		if ta != tb {
			return ta < tb
		}
		da, db := math.Abs(a.Confidence-0.5), math.Abs(b.Confidence-0.5)
		if da != db {
			return da < db
		}
		return a.RequestID < b.RequestID
	}

	// 按task_type分组，组内按优先级排序（byType各组是新切片，不改输入顺序）
	byType := make(map[string][]SamplingCandidate)
	for _, cand := range candidates {
		byType[cand.TaskType] = append(byType[cand.TaskType], cand)
	}
	types := make([]string, 0, len(byType))
	for ty := range byType {
		types = append(types, ty)
	}
	sort.Strings(types)
	for _, ty := range types {
		group := byType[ty]
		sort.Slice(group, func(i, j int) bool { return less(group[i], group[j]) })
	}

	// Phase 1: 按类型轮转发放保底配额（类型不足时缺口回流给其他类型）
	if opts.MinPerTaskType > 0 {
		for round := 0; round < opts.MinPerTaskType; round++ {
			if limit > 0 && len(selected) >= limit {
				break
			}
			for _, ty := range types {
				if limit > 0 && len(selected) >= limit {
					break
				}
				queue := byType[ty]
				for len(queue) > 0 {
					cand := queue[0]
					queue = queue[1:]
					if !isSelected[cand.RequestID] {
						take(cand)
						break
					}
				}
				byType[ty] = queue
			}
		}
	}

	// Phase 2: 剩余预算按全局三层优先级回填
	if limit <= 0 || len(selected) < limit {
		all := make([]SamplingCandidate, len(candidates))
		copy(all, candidates)
		sort.Slice(all, func(i, j int) bool { return less(all[i], all[j]) })
		for _, cand := range all {
			if limit > 0 && len(selected) >= limit {
				break
			}
			if !isSelected[cand.RequestID] {
				take(cand)
			}
		}
	}

	return selected
}

// ExportUncertain 以主动学习不确定性采样模式导出样本到CSV。
//
// 与Export（--sampling=all）的协同语义:
//   - ExportConfig的时间范围/MinConfidence/MaxConfidence过滤条件照常生效；
//   - config.Limit仍是导出总量上限（<=0不限制），但不再下推到SQL，
//     而是由SelectUncertainSamples在完整候选池上做配额分配；
//   - 覆盖排序策略: 置信度0.4–0.6优先，不足回填<0.6以下的低置信样本，
//     并按task_type保底配额防偏斜；
//   - CSV header与Export逐字节一致（同一writeAnnotationCSV路径）。
//
// 返回实际导出的样本数。
func (e *Exporter) ExportUncertain(ctx context.Context, config ExportConfig, opts UncertainSamplingOptions) (int, error) {
	// 查询完整候选池（不条件下推LIMIT，配额分配在Go侧进行）
	rows, err := e.querySamples(ctx, config, false)
	if err != nil {
		return 0, fmt.Errorf("query samples failed: %w", err)
	}

	candidates := make([]SamplingCandidate, len(rows))
	rowByID := make(map[string]CSVAnnotationRow, len(rows))
	for i, row := range rows {
		candidates[i] = SamplingCandidate{
			RequestID:  row.RequestID,
			TaskType:   row.TaskType,
			Confidence: row.Confidence,
		}
		rowByID[row.RequestID] = row
	}

	selected := SelectUncertainSamples(candidates, config.Limit, opts)

	selectedRows := make([]CSVAnnotationRow, 0, len(selected))
	for _, cand := range selected {
		selectedRows = append(selectedRows, rowByID[cand.RequestID])
	}

	if err := writeAnnotationCSV(config.OutputPath, selectedRows); err != nil {
		return 0, err
	}
	return len(selectedRows), nil
}

// writeAnnotationCSV 将样本行写入CSV文件（header + 数据行）。
// 所有采样模式共用此路径，保证header逐字节一致；列集合见exportCSVHeader。
func writeAnnotationCSV(outputPath string, rows []CSVAnnotationRow) error {
	// 创建CSV文件
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output file failed: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// 写入CSV header（P2.1冻结契约，逐字节与原实现一致）
	if err := writer.Write(exportCSVHeader); err != nil {
		return fmt.Errorf("write header failed: %w", err)
	}

	// 写入数据行（只含结构化特征与待填标注列，无prompt/messages/response）
	for i, row := range rows {
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
			return fmt.Errorf("write row %d failed: %w", i+1, err)
		}
	}

	if err := writer.Error(); err != nil {
		return fmt.Errorf("csv writer error: %w", err)
	}

	return nil
}

// querySamples 查询候选样本。
//
// Privacy-compliant query: 只访问结构化特征字段。auto_route_selections
// 隐私最小化设计（migration 478/658）不记录 prompt_tokens/streaming/
// vision/region，CSV 保留这些列但填零值；chosen_model 既是 model_name
// 也是待人工确认的 auto_provider 标签。
//
// applyLimit=true时SQL内ORDER BY confidence ASC并应用config.Limit
// （Export原行为）；false时返回全部匹配候选，排序与截断交由
// SelectUncertainSamples在Go侧完成（ExportUncertain）。
func (e *Exporter) querySamples(ctx context.Context, config ExportConfig, applyLimit bool) ([]CSVAnnotationRow, error) {
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

	if applyLimit {
		// 排序和限制（--sampling=all 原行为）
		query += " ORDER BY ars.confidence ASC, ars.ts DESC"

		if config.Limit > 0 {
			query += fmt.Sprintf(" LIMIT $%d", argIdx)
			args = append(args, config.Limit)
		}
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
