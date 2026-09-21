// annotation/stats.go — 2026-09-06
//
// P2.1标注统计查询
//
// 功能:
//   - 查询标注总体统计
//   - 查询分provider准确率
//   - 查询标注人员统计
//   - 查询标注原因分布

package annotation

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// StatsQuerier 标注统计查询器
type StatsQuerier struct {
	db *pgxpool.Pool
}

// NewStatsQuerier 创建统计查询器
func NewStatsQuerier(db *pgxpool.Pool) *StatsQuerier {
	return &StatsQuerier{db: db}
}

// GetOverallStats 获取总体统计
func (q *StatsQuerier) GetOverallStats(ctx context.Context) (*AnnotationStats, error) {
	query := `
		SELECT 
			total_annotations,
			correct_count,
			incorrect_count,
			accuracy_percent,
			num_annotators,
			first_annotation_at,
			last_annotation_at
		FROM annotation_stats
	`

	var stats AnnotationStats
	err := q.db.QueryRow(ctx, query).Scan(
		&stats.TotalAnnotations,
		&stats.CorrectCount,
		&stats.IncorrectCount,
		&stats.AccuracyPercent,
		&stats.NumAnnotators,
		&stats.FirstAnnotationAt,
		&stats.LastAnnotationAt,
	)

	if err != nil {
		return nil, fmt.Errorf("query overall stats failed: %w", err)
	}

	return &stats, nil
}

// GetProviderAccuracy 获取分provider准确率
func (q *StatsQuerier) GetProviderAccuracy(ctx context.Context) ([]ProviderAccuracy, error) {
	query := `
		SELECT 
			provider,
			total_predictions,
			correct_predictions,
			incorrect_predictions,
			accuracy_percent,
			avg_confidence
		FROM annotation_accuracy_by_provider
		ORDER BY total_predictions DESC
	`

	rows, err := q.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query provider accuracy failed: %w", err)
	}
	defer rows.Close()

	var results []ProviderAccuracy
	for rows.Next() {
		var pa ProviderAccuracy
		err := rows.Scan(
			&pa.Provider,
			&pa.TotalPredictions,
			&pa.CorrectPredictions,
			&pa.IncorrectPredictions,
			&pa.AccuracyPercent,
			&pa.AvgConfidence,
		)
		if err != nil {
			return nil, fmt.Errorf("scan provider accuracy failed: %w", err)
		}
		results = append(results, pa)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration failed: %w", err)
	}

	return results, nil
}

// GetAnnotatorStats 获取标注人员统计
func (q *StatsQuerier) GetAnnotatorStats(ctx context.Context) ([]AnnotatorStats, error) {
	query := `
		SELECT 
			annotator,
			total_annotations,
			correct_count,
			incorrect_count,
			accuracy_percent,
			first_annotation_at,
			last_annotation_at,
			hours_span
		FROM annotation_stats_by_annotator
		ORDER BY total_annotations DESC
	`

	rows, err := q.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query annotator stats failed: %w", err)
	}
	defer rows.Close()

	var results []AnnotatorStats
	for rows.Next() {
		var as AnnotatorStats
		err := rows.Scan(
			&as.Annotator,
			&as.TotalAnnotations,
			&as.CorrectCount,
			&as.IncorrectCount,
			&as.AccuracyPercent,
			&as.FirstAnnotationAt,
			&as.LastAnnotationAt,
			&as.HoursSpan,
		)
		if err != nil {
			return nil, fmt.Errorf("scan annotator stats failed: %w", err)
		}
		results = append(results, as)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration failed: %w", err)
	}

	return results, nil
}

// GetReasonDistribution 获取标注原因分布
func (q *StatsQuerier) GetReasonDistribution(ctx context.Context) ([]ReasonDistribution, error) {
	query := `
		SELECT 
			annotation_reason,
			count,
			percent
		FROM annotation_reason_distribution
		ORDER BY count DESC
	`

	rows, err := q.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query reason distribution failed: %w", err)
	}
	defer rows.Close()

	var results []ReasonDistribution
	for rows.Next() {
		var rd ReasonDistribution
		err := rows.Scan(
			&rd.Reason,
			&rd.Count,
			&rd.Percent,
		)
		if err != nil {
			return nil, fmt.Errorf("scan reason distribution failed: %w", err)
		}
		results = append(results, rd)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration failed: %w", err)
	}

	return results, nil
}

// GetAnnotationHistory 获取请求的标注历史
func (q *StatsQuerier) GetAnnotationHistory(ctx context.Context, requestID string) ([]AnnotationRecord, error) {
	query := `
		SELECT 
			id, request_id, auto_label, auto_confidence,
			human_label, is_correct, annotation_reason,
			annotator, annotated_at, created_at
		FROM training_human_annotations
		WHERE request_id = $1
		ORDER BY annotated_at DESC
	`

	rows, err := q.db.Query(ctx, query, requestID)
	if err != nil {
		return nil, fmt.Errorf("query annotation history failed: %w", err)
	}
	defer rows.Close()

	var results []AnnotationRecord
	for rows.Next() {
		var ar AnnotationRecord
		err := rows.Scan(
			&ar.ID,
			&ar.RequestID,
			&ar.AutoLabel,
			&ar.AutoConfidence,
			&ar.HumanLabel,
			&ar.IsCorrect,
			&ar.AnnotationReason,
			&ar.Annotator,
			&ar.AnnotatedAt,
			&ar.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan annotation record failed: %w", err)
		}
		results = append(results, ar)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration failed: %w", err)
	}

	return results, nil
}

// GetRecentAnnotations 获取最近的标注记录
func (q *StatsQuerier) GetRecentAnnotations(ctx context.Context, limit int) ([]AnnotationRecord, error) {
	query := `
		SELECT 
			id, request_id, auto_label, auto_confidence,
			human_label, is_correct, annotation_reason,
			annotator, annotated_at, created_at
		FROM training_human_annotations
		ORDER BY annotated_at DESC
		LIMIT $1
	`

	rows, err := q.db.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent annotations failed: %w", err)
	}
	defer rows.Close()

	var results []AnnotationRecord
	for rows.Next() {
		var ar AnnotationRecord
		err := rows.Scan(
			&ar.ID,
			&ar.RequestID,
			&ar.AutoLabel,
			&ar.AutoConfidence,
			&ar.HumanLabel,
			&ar.IsCorrect,
			&ar.AnnotationReason,
			&ar.Annotator,
			&ar.AnnotatedAt,
			&ar.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan annotation record failed: %w", err)
		}
		results = append(results, ar)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration failed: %w", err)
	}

	return results, nil
}

// GetIncorrectPredictions 获取错误预测记录（用于分析）
func (q *StatsQuerier) GetIncorrectPredictions(ctx context.Context, limit int) ([]AnnotationRecord, error) {
	query := `
		SELECT 
			id, request_id, auto_label, auto_confidence,
			human_label, is_correct, annotation_reason,
			annotator, annotated_at, created_at
		FROM training_human_annotations
		WHERE is_correct = false
		ORDER BY auto_confidence DESC, annotated_at DESC
		LIMIT $1
	`

	rows, err := q.db.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("query incorrect predictions failed: %w", err)
	}
	defer rows.Close()

	var results []AnnotationRecord
	for rows.Next() {
		var ar AnnotationRecord
		err := rows.Scan(
			&ar.ID,
			&ar.RequestID,
			&ar.AutoLabel,
			&ar.AutoConfidence,
			&ar.HumanLabel,
			&ar.IsCorrect,
			&ar.AnnotationReason,
			&ar.Annotator,
			&ar.AnnotatedAt,
			&ar.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan annotation record failed: %w", err)
		}
		results = append(results, ar)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration failed: %w", err)
	}

	return results, nil
}
