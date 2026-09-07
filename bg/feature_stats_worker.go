// bg/feature_stats_worker.go — 2026-09-06
//
// 定期计算结构化特征分布和去重率统计，用于监控 AUTO 路由 ML 训练数据质量。
//
// 设计：
//   - 每小时计算一次（默认间隔可配置）
//   - 只查询 auto_route_selections 表的结构化特征字段（15个非可逆特征）
//   - 聚合结果写入 feature_distribution_stats 和 dedup_stats 表
//   - 检测异常：特征集中 >90%、去重率 <50%、特征缺失 >20%
//
// 隐私保证：
//   - 只访问结构化特征列（detected_language, prompt_length_bucket 等）
//   - 不查询 prompt, messages, response 或任何可逆内容
//   - 统计表只存储聚合计数，不存储原始内容
//
// 接入点：cmd/gateway/main.go 在 init bg services 时构造 + Start
//
// Part of: P2.3 - Monitor Feature Distribution and Deduplication Rate
// Ref: docs/implementation-plans/p2-tasks-auto-route-ml-training.md

package bg

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// FeatureStatsWorker 定期计算结构化特征统计。
type FeatureStatsWorker struct {
	db       *pgxpool.Pool
	interval time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewFeatureStatsWorker 构造 worker，默认间隔 1 小时。
func NewFeatureStatsWorker(db *pgxpool.Pool, interval time.Duration) *FeatureStatsWorker {
	if interval <= 0 {
		interval = time.Hour // 默认 1 小时
	}
	return &FeatureStatsWorker{
		db:       db,
		interval: interval,
		done:     make(chan struct{}),
	}
}

// Start 启动后台 goroutine。Stop 之前不能重复 Start。
func (w *FeatureStatsWorker) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)
	go w.run(ctx)
	slog.Info("feature stats worker started", "interval", w.interval)
}

// Stop 取消并等待 goroutine 退出。
func (w *FeatureStatsWorker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	<-w.done
}

func (w *FeatureStatsWorker) run(ctx context.Context) {
	defer close(w.done)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	// 启动后立即计算一次
	w.computeStats(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.computeStats(ctx)
		}
	}
}

func (w *FeatureStatsWorker) computeStats(ctx context.Context) {
	computeCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	statDate := time.Now().UTC().Truncate(24 * time.Hour)

	// 1. 计算特征分布统计
	if err := w.computeFeatureDistributions(computeCtx, statDate); err != nil {
		slog.Warn("feature distribution computation failed", "error", err, "stat_date", statDate.Format("2006-01-02"))
		return
	}

	// 2. 计算去重率统计
	if err := w.computeDedupRate(computeCtx, statDate); err != nil {
		slog.Warn("dedup rate computation failed", "error", err, "stat_date", statDate.Format("2006-01-02"))
		return
	}

	// 3. 检测异常
	w.detectAnomalies(computeCtx, statDate)

	slog.Info("feature stats computed", "stat_date", statDate.Format("2006-01-02"))
}

// computeFeatureDistributions 计算所有结构化特征的分布。
func (w *FeatureStatsWorker) computeFeatureDistributions(ctx context.Context, statDate time.Time) error {
	// 需要统计的特征列（只包含枚举类型和桶类型，排除布尔类型）
	features := []string{
		"detected_language",
		"prompt_length_bucket",
		"context_length_bucket",
		"turn_count_bucket",
		"intent_category",
		"domain_hint",
		"complexity_bucket",
	}

	for _, feature := range features {
		if err := w.computeSingleFeatureDistribution(ctx, statDate, feature); err != nil {
			return err
		}
	}

	return nil
}

// computeSingleFeatureDistribution 计算单个特征的分布。
func (w *FeatureStatsWorker) computeSingleFeatureDistribution(ctx context.Context, statDate time.Time, featureName string) error {
	// PRIVACY: 只查询结构化特征列，不查询 prompt/messages/response
	query := `
		WITH feature_counts AS (
			SELECT 
				COALESCE(` + featureName + `, 'NULL') AS feature_value,
				COUNT(*) AS row_count
			FROM auto_route_selections
			WHERE DATE(ts) = $1
			GROUP BY COALESCE(` + featureName + `, 'NULL')
		),
		total AS (
			SELECT SUM(row_count) AS total_rows FROM feature_counts
		)
		INSERT INTO feature_distribution_stats (stat_date, feature_name, feature_value, row_count, percentage)
		SELECT 
			$1 AS stat_date,
			$2 AS feature_name,
			fc.feature_value,
			fc.row_count,
			ROUND(100.0 * fc.row_count / NULLIF(t.total_rows, 0), 2) AS percentage
		FROM feature_counts fc, total t
		ON CONFLICT (stat_date, feature_name, feature_value) 
		DO UPDATE SET 
			row_count = EXCLUDED.row_count,
			percentage = EXCLUDED.percentage
	`

	_, err := w.db.Exec(ctx, query, statDate, featureName)
	return err
}

// computeDedupRate 计算去重率统计。
func (w *FeatureStatsWorker) computeDedupRate(ctx context.Context, statDate time.Time) error {
	// PRIVACY: 只查询 content_hash（SHA256哈希，非可逆），不查询原始内容
	query := `
		WITH daily_data AS (
			SELECT 
				COUNT(*) AS total_rows,
				COUNT(DISTINCT content_hash) AS unique_hashes
			FROM auto_route_selections
			WHERE DATE(ts) = $1
			  AND content_hash IS NOT NULL
		),
		top_dupes AS (
			SELECT 
				content_hash,
				COUNT(*) AS count
			FROM auto_route_selections
			WHERE DATE(ts) = $1
			  AND content_hash IS NOT NULL
			GROUP BY content_hash
			HAVING COUNT(*) > 1
			ORDER BY count DESC
			LIMIT 10
		)
		INSERT INTO dedup_stats (stat_date, total_rows, unique_hashes, dedup_rate, duplicate_count, top_duplicate_hashes)
		SELECT 
			$1 AS stat_date,
			dd.total_rows,
			dd.unique_hashes,
			-- total_rows=0 时 NULLIF 产生 NULL,直接撞 dedup_stats.dedup_rate
			-- 的 NOT NULL(23502,空表日首次聚合即失败);0 行即 0 去重率。
			COALESCE(ROUND(100.0 * (1.0 - dd.unique_hashes::numeric / NULLIF(dd.total_rows, 0)), 2), 0) AS dedup_rate,
			dd.total_rows - dd.unique_hashes AS duplicate_count,
			COALESCE(
				(SELECT jsonb_agg(jsonb_build_object('hash', content_hash, 'count', count) ORDER BY count DESC) 
				 FROM top_dupes),
				'[]'::jsonb
			) AS top_duplicate_hashes
		FROM daily_data dd
		ON CONFLICT (stat_date) 
		DO UPDATE SET 
			total_rows = EXCLUDED.total_rows,
			unique_hashes = EXCLUDED.unique_hashes,
			dedup_rate = EXCLUDED.dedup_rate,
			duplicate_count = EXCLUDED.duplicate_count,
			top_duplicate_hashes = EXCLUDED.top_duplicate_hashes
	`

	_, err := w.db.Exec(ctx, query, statDate)
	return err
}

// detectAnomalies 检测统计异常并记录日志（告警由 Prometheus 触发）。
func (w *FeatureStatsWorker) detectAnomalies(ctx context.Context, statDate time.Time) {
	// 异常1: 特征值集中度 > 90%（某个值占比过高）
	w.detectFeatureConcentration(ctx, statDate)

	// 异常2: 去重率 < 50%（大量重复请求）
	w.detectLowDedupRate(ctx, statDate)

	// 异常3: 特征缺失率 > 20%（特征提取失败）
	w.detectMissingFeatures(ctx, statDate)
}

// detectFeatureConcentration 检测特征值集中度异常。
func (w *FeatureStatsWorker) detectFeatureConcentration(ctx context.Context, statDate time.Time) {
	query := `
		SELECT feature_name, feature_value, percentage
		FROM feature_distribution_stats
		WHERE stat_date = $1
		  AND percentage > 90
		  AND feature_name != 'detected_language' -- 语言分布天然集中，不告警
		ORDER BY percentage DESC
	`

	rows, err := w.db.Query(ctx, query, statDate)
	if err != nil {
		slog.Warn("feature concentration detection failed", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var featureName, featureValue string
		var percentage float64
		if err := rows.Scan(&featureName, &featureValue, &percentage); err != nil {
			continue
		}

		slog.Warn("feature concentration detected",
			"feature_name", featureName,
			"feature_value", featureValue,
			"percentage", percentage,
			"stat_date", statDate.Format("2006-01-02"),
			"alert", "FeatureConcentration")
	}
}

// detectLowDedupRate 检测低去重率异常。
func (w *FeatureStatsWorker) detectLowDedupRate(ctx context.Context, statDate time.Time) {
	query := `
		SELECT dedup_rate, duplicate_count, total_rows
		FROM dedup_stats
		WHERE stat_date = $1
		  AND dedup_rate > 50 -- 去重率 >50% 表示超过一半是重复的
	`

	var dedupRate float64
	var duplicateCount, totalRows int
	err := w.db.QueryRow(ctx, query, statDate).Scan(&dedupRate, &duplicateCount, &totalRows)
	if err != nil {
		// 无数据或无异常，正常返回
		return
	}

	slog.Warn("low dedup rate detected",
		"dedup_rate", dedupRate,
		"duplicate_count", duplicateCount,
		"total_rows", totalRows,
		"stat_date", statDate.Format("2006-01-02"),
		"alert", "LowDedupRate")
}

// detectMissingFeatures 检测特征缺失率异常。
func (w *FeatureStatsWorker) detectMissingFeatures(ctx context.Context, statDate time.Time) {
	query := `
		SELECT feature_name, percentage
		FROM feature_distribution_stats
		WHERE stat_date = $1
		  AND feature_value = 'NULL'
		  AND percentage > 20 -- 缺失率 >20% 告警
		ORDER BY percentage DESC
	`

	rows, err := w.db.Query(ctx, query, statDate)
	if err != nil {
		slog.Warn("missing features detection failed", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var featureName string
		var percentage float64
		if err := rows.Scan(&featureName, &percentage); err != nil {
			continue
		}

		slog.Warn("high missing rate detected",
			"feature_name", featureName,
			"missing_percentage", percentage,
			"stat_date", statDate.Format("2006-01-02"),
			"alert", "MissingFeatures")
	}
}

// GetLatestStats 获取最新的统计数据（用于测试和监控）。
func (w *FeatureStatsWorker) GetLatestStats(ctx context.Context) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	// 获取最新的去重率
	var dedupRate float64
	var statDate time.Time
	err := w.db.QueryRow(ctx, `
		SELECT stat_date, dedup_rate 
		FROM dedup_stats 
		ORDER BY stat_date DESC 
		LIMIT 1
	`).Scan(&statDate, &dedupRate)
	if err != nil {
		// 无数据时返回空结果，不报错
		return result, nil
	}
	result["dedup_rate"] = dedupRate
	result["stat_date"] = statDate.Format("2006-01-02")

	// 获取特征填充率
	rows, err := w.db.Query(ctx, `
		SELECT feature_name, fill_rate_pct
		FROM feature_quality_metrics
		WHERE stat_date = $1
		ORDER BY fill_rate_pct
	`, statDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fillRates := make(map[string]float64)
	for rows.Next() {
		var featureName string
		var fillRate float64
		if err := rows.Scan(&featureName, &fillRate); err != nil {
			continue
		}
		fillRates[featureName] = fillRate
	}
	result["fill_rates"] = fillRates

	return result, nil
}

// FeatureDistribution 特征分布数据结构（用于测试）。
type FeatureDistribution struct {
	FeatureName  string  `json:"feature_name"`
	FeatureValue string  `json:"feature_value"`
	RowCount     int     `json:"row_count"`
	Percentage   float64 `json:"percentage"`
}

// DedupStats 去重率统计数据结构（用于测试）。
type DedupStats struct {
	StatDate           time.Time              `json:"stat_date"`
	TotalRows          int                    `json:"total_rows"`
	UniqueHashes       int                    `json:"unique_hashes"`
	DedupRate          float64                `json:"dedup_rate"`
	DuplicateCount     int                    `json:"duplicate_count"`
	TopDuplicateHashes []map[string]interface{} `json:"top_duplicate_hashes"`
}

// MarshalJSON 实现自定义 JSON 序列化（隐私保护：不暴露内部字段）。
func (w *FeatureStatsWorker) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"type":     "FeatureStatsWorker",
		"interval": w.interval.String(),
	})
}

// String 实现 Stringer 接口（隐私保护：不暴露数据库连接信息）。
func (w *FeatureStatsWorker) String() string {
	return "FeatureStatsWorker{interval=" + w.interval.String() + "}"
}
