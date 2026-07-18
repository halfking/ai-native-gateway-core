package quality

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ProfileUpdater 质量画像更新器
//
// 定时扫描所有供应商，计算质量画像并更新到数据库。
type ProfileUpdater struct {
	db         *sql.DB
	calculator *ProfileCalculator
	interval   time.Duration
	timeout    time.Duration
	enabled    bool
}

// ProfileUpdaterOption 配置选项
type ProfileUpdaterOption func(*ProfileUpdater)

// WithUpdateInterval 设置更新间隔
func WithUpdateInterval(d time.Duration) ProfileUpdaterOption {
	return func(u *ProfileUpdater) { u.interval = d }
}

// WithUpdateTimeout 设置单次超时
func WithUpdateTimeout(d time.Duration) ProfileUpdaterOption {
	return func(u *ProfileUpdater) { u.timeout = d }
}

// WithUpdateEnabled 设置是否启用
func WithUpdateEnabled(enabled bool) ProfileUpdaterOption {
	return func(u *ProfileUpdater) { u.enabled = enabled }
}

// NewProfileUpdater 创建质量画像更新器
func NewProfileUpdater(db *sql.DB, opts ...ProfileUpdaterOption) *ProfileUpdater {
	u := &ProfileUpdater{
		db:         db,
		calculator: NewProfileCalculator(db),
		interval:   1 * time.Hour, // 默认每小时更新
		timeout:    5 * time.Minute,
		enabled:    true,
	}
	for _, opt := range opts {
		opt(u)
	}
	return u
}

// Start 启动更新器（阻塞）
func (u *ProfileUpdater) Start(ctx context.Context) error {
	if !u.enabled {
		return nil
	}

	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()

	// 启动时立即执行一次
	_ = u.UpdateAll(ctx)

	for {
		select {
		case <-ticker.C:
			_ = u.UpdateAll(ctx)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// UpdateAll 更新所有供应商的质量画像
func (u *ProfileUpdater) UpdateAll(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, u.timeout)
	defer cancel()

	// 查询所有活跃的供应商和模型（24小时内有数据）
	query := `
SELECT DISTINCT provider_id, model_name
FROM provider_metrics_hour
WHERE bucket >= NOW() - INTERVAL '24 hours'
ORDER BY provider_id, model_name
`

	rows, err := u.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("查询活跃供应商失败: %w", err)
	}
	defer rows.Close()

	updatedCount := 0
	errorCount := 0

	for rows.Next() {
		var providerID int64
		var modelName string

		if err := rows.Scan(&providerID, &modelName); err != nil {
			errorCount++
			continue
		}

		// 计算质量画像
		result, err := u.calculator.Calculate(ctx, providerID, modelName)
		if err != nil {
			errorCount++
			continue
		}

		// 写入数据库
		if err := u.saveProfile(ctx, result); err != nil {
			errorCount++
			continue
		}

		updatedCount++
	}

	// TODO: 记录日志和指标
	_ = updatedCount
	_ = errorCount

	return rows.Err()
}

// saveProfile 保存质量画像到数据库
func (u *ProfileUpdater) saveProfile(ctx context.Context, result *ScoreResult) error {
	query := `
INSERT INTO provider_quality_profiles (
    provider_id,
    model_name,
    quality_score,
    quality_grade,
    availability_score,
    performance_score,
    reliability_score,
    stability_score,
    cost_efficiency_score,
    calculated_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
ON CONFLICT (provider_id, model_name) DO UPDATE SET
    quality_score = EXCLUDED.quality_score,
    quality_grade = EXCLUDED.quality_grade,
    availability_score = EXCLUDED.availability_score,
    performance_score = EXCLUDED.performance_score,
    reliability_score = EXCLUDED.reliability_score,
    stability_score = EXCLUDED.stability_score,
    cost_efficiency_score = EXCLUDED.cost_efficiency_score,
    calculated_at = EXCLUDED.calculated_at
`

	_, err := u.db.ExecContext(ctx, query,
		result.ProviderID,
		result.ModelName,
		result.QualityScore,
		result.Grade,
		result.AvailabilityScore,
		result.PerformanceScore,
		result.ReliabilityScore,
		result.StabilityScore,
		result.CostEfficiencyScore,
	)
	if err != nil {
		return fmt.Errorf("保存质量画像失败: %w", err)
	}

	return nil
}

// UpdateOne 更新单个供应商的质量画像（手动触发用）
func (u *ProfileUpdater) UpdateOne(ctx context.Context, providerID int64, modelName string) error {
	result, err := u.calculator.Calculate(ctx, providerID, modelName)
	if err != nil {
		return err
	}
	return u.saveProfile(ctx, result)
}
