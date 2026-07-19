package quality

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/pkg/logger"
)

// Scheduler 质量评分调度器
type Scheduler struct {
	db         *sql.DB
	calculator *ProfileCalculator
	interval   time.Duration
	running    bool
	mu         sync.Mutex
	stopCh     chan struct{}
	logger     logger.Logger
}

// NewScheduler 创建调度器
func NewScheduler(db *sql.DB, interval time.Duration) *Scheduler {
	return &Scheduler{
		db:         db,
		calculator: NewProfileCalculator(db),
		interval:   interval,
		stopCh:     make(chan struct{}),
		logger:     logger.New("quality-scheduler"),
	}
}

// Start 启动调度器
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("scheduler already running")
	}
	s.running = true
	s.mu.Unlock()

	s.logger.Info("quality scheduler started",
		"interval", s.interval,
	)

	// 立即执行一次
	go func() {
		if err := s.runOnce(ctx); err != nil {
			s.logger.Error("initial run failed", "error", err.Error())
		}
	}()

	// 定时执行
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.runOnce(ctx); err != nil {
				s.logger.Error("scheduled run failed", "error", err.Error())
			}
		case <-s.stopCh:
			s.logger.Info("quality scheduler stopped")
			return nil
		case <-ctx.Done():
			s.logger.Info("quality scheduler context cancelled")
			return ctx.Err()
		}
	}
}

// Stop 停止调度器
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	close(s.stopCh)
	s.running = false
}

// runOnce 执行一次质量评分计算
func (s *Scheduler) runOnce(ctx context.Context) error {
	start := time.Now()
	s.logger.Info("starting quality calculation run")

	// 查询所有活跃的 provider + model 组合
	providers, err := s.getActiveProviders(ctx)
	if err != nil {
		return fmt.Errorf("failed to get active providers: %w", err)
	}

	s.logger.Info("found active providers",
		"count", len(providers),
	)

	// 计算每个 provider + model 的质量评分
	successCount := 0
	failCount := 0

	for _, p := range providers {
		if err := s.calculateAndSave(ctx, p.ProviderID, p.ModelName); err != nil {
			s.logger.Error("failed to calculate quality score",
				"provider_id", p.ProviderID,
				"model", p.ModelName,
				"error", err.Error(),
			)
			failCount++
			continue
		}
		successCount++
	}

	duration := time.Since(start)
	s.logger.Info("quality calculation run completed",
		"duration", duration,
		"success", successCount,
		"failed", failCount,
		"total", len(providers),
	)

	return nil
}

// ProviderModel 是 provider + model 组合
type ProviderModel struct {
	ProviderID int64
	ModelName  string
}

// getActiveProviders 获取所有活跃的 provider + model 组合
func (s *Scheduler) getActiveProviders(ctx context.Context) ([]ProviderModel, error) {
	query := `
SELECT DISTINCT
    provider_id,
    model_name
FROM provider_metrics_hour
WHERE bucket >= NOW() - INTERVAL '24 hours'
  AND total_requests > 0
ORDER BY provider_id, model_name
`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []ProviderModel
	for rows.Next() {
		var p ProviderModel
		if err := rows.Scan(&p.ProviderID, &p.ModelName); err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}

	return providers, rows.Err()
}

// calculateAndSave 计算并保存质量评分
func (s *Scheduler) calculateAndSave(ctx context.Context, providerID int64, modelName string) error {
	// 计算质量评分
	result, err := s.calculator.Calculate(ctx, providerID, modelName)
	if err != nil {
		return fmt.Errorf("calculate failed: %w", err)
	}

	// 保存到数据库
	if err := s.saveProfile(ctx, result); err != nil {
		return fmt.Errorf("save failed: %w", err)
	}

	s.logger.Debug("quality profile saved",
		"provider_id", providerID,
		"model", modelName,
		"quality_score", result.QualityScore,
	)

	return nil
}

// saveProfile 保存质量画像到数据库
func (s *Scheduler) saveProfile(ctx context.Context, result *ScoreResult) error {
	query := `
INSERT INTO provider_quality_profiles (
    provider_id,
    model_name,
    availability_score,
    performance_score,
    reliability_score,
    stability_score,
    cost_efficiency_score,
    quality_score,
    calculated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
ON CONFLICT (provider_id, model_name)
DO UPDATE SET
    availability_score = EXCLUDED.availability_score,
    performance_score = EXCLUDED.performance_score,
    reliability_score = EXCLUDED.reliability_score,
    stability_score = EXCLUDED.stability_score,
    cost_efficiency_score = EXCLUDED.cost_efficiency_score,
    quality_score = EXCLUDED.quality_score,
    calculated_at = EXCLUDED.calculated_at,
    updated_at = NOW()
`

	_, err := s.db.ExecContext(ctx, query,
		result.ProviderID,
		result.ModelName,
		result.AvailabilityScore,
		result.PerformanceScore,
		result.ReliabilityScore,
		result.StabilityScore,
		result.CostEfficiencyScore,
		result.QualityScore,
	)

	return err
}
