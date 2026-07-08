package intentconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Manager 配置管理器（热加载，30秒轮询）
type Manager struct {
	pool   *pgxpool.Pool
	logger *slog.Logger

	// 配置缓存（tenant_id -> *ClassifierConfig）
	mu    sync.RWMutex
	cache map[string]*ClassifierConfig

	// 平台级默认配置
	platformConfig *ClassifierConfig

	// 控制通道
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewManager 创建配置管理器
func NewManager(pool *pgxpool.Pool, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}

	return &Manager{
		pool:           pool,
		logger:         logger,
		cache:          make(map[string]*ClassifierConfig),
		platformConfig: DefaultClassifierConfig(),
		stopCh:         make(chan struct{}),
	}
}

// Start 启动配置热加载（30秒轮询）
func (m *Manager) Start(ctx context.Context) error {
	// 初始加载
	if err := m.reload(ctx); err != nil {
		return fmt.Errorf("intentconfig: initial load failed: %w", err)
	}

	// 后台轮询
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := m.reload(ctx); err != nil {
					m.logger.Error("intentconfig: reload failed", "error", err)
				}
			case <-m.stopCh:
				return
			}
		}
	}()

	m.logger.Info("intentconfig: manager started", "poll_interval", "30s")
	return nil
}

// Stop 停止配置热加载
func (m *Manager) Stop() {
	close(m.stopCh)
	m.wg.Wait()
	m.logger.Info("intentconfig: manager stopped")
}

// GetConfig 获取租户配置（租户级覆盖平台级）
func (m *Manager) GetConfig(tenantID string) *ClassifierConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// 优先返回租户级配置
	if cfg, ok := m.cache[tenantID]; ok {
		return cfg
	}

	// 回退到平台级配置
	if m.platformConfig != nil {
		return m.platformConfig
	}

	// 最后的兜底
	return DefaultClassifierConfig()
}

// reload 从数据库重新加载所有配置
func (m *Manager) reload(ctx context.Context) error {
	query := `
		SELECT 
			id, tenant_id, strategy, enabled_layers, keywords_config, 
			patterns_config, confidence_thresholds, drift_threshold, 
			multi_turn_memory, llm_fallback_enabled, llm_model, 
			llm_confidence_threshold, version, created_at, updated_at
		FROM intent_classifier_config
		WHERE enabled = true
		ORDER BY tenant_id NULLS FIRST
	`

	rows, err := m.pool.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("query configs: %w", err)
	}
	defer rows.Close()

	newCache := make(map[string]*ClassifierConfig)
	var newPlatformConfig *ClassifierConfig

	for rows.Next() {
		var (
			id                       int
			tenantID                 *string
			strategy                 string
			enabledLayersJSON        []byte
			keywordsConfigJSON       []byte
			patternsConfigJSON       []byte
			confidenceThresholdsJSON []byte
			driftThreshold           float64
			multiTurnMemory          int
			llmFallbackEnabled       bool
			llmModel                 string
			llmConfidenceThreshold   float64
			version                  int64
			createdAt                time.Time
			updatedAt                time.Time
		)

		err := rows.Scan(
			&id, &tenantID, &strategy, &enabledLayersJSON, &keywordsConfigJSON,
			&patternsConfigJSON, &confidenceThresholdsJSON, &driftThreshold,
			&multiTurnMemory, &llmFallbackEnabled, &llmModel,
			&llmConfidenceThreshold, &version, &createdAt, &updatedAt,
		)
		if err != nil {
			m.logger.Warn("intentconfig: scan failed", "error", err)
			continue
		}

		cfg := &ClassifierConfig{
			ID:                     id,
			TenantID:               "",
			Strategy:               Strategy(strategy),
			DriftThreshold:         driftThreshold,
			MultiTurnMemory:        multiTurnMemory,
			LLMFallbackEnabled:     llmFallbackEnabled,
			LLMModel:               llmModel,
			LLMConfidenceThreshold: llmConfidenceThreshold,
			Version:                version,
			CreatedAt:              createdAt,
			UpdatedAt:              updatedAt,
		}

		if tenantID != nil {
			cfg.TenantID = *tenantID
		}

		// 解析 JSONB 字段
		if err := json.Unmarshal(enabledLayersJSON, &cfg.EnabledLayers); err != nil {
			m.logger.Warn("intentconfig: parse enabled_layers failed", "tenant_id", cfg.TenantID, "error", err)
			continue
		}

		if err := json.Unmarshal(keywordsConfigJSON, &cfg.KeywordsConfig); err != nil {
			m.logger.Warn("intentconfig: parse keywords_config failed", "tenant_id", cfg.TenantID, "error", err)
			continue
		}

		if err := json.Unmarshal(patternsConfigJSON, &cfg.PatternsConfig); err != nil {
			m.logger.Warn("intentconfig: parse patterns_config failed", "tenant_id", cfg.TenantID, "error", err)
			continue
		}

		if err := json.Unmarshal(confidenceThresholdsJSON, &cfg.ConfidenceThresholds); err != nil {
			m.logger.Warn("intentconfig: parse confidence_thresholds failed", "tenant_id", cfg.TenantID, "error", err)
			continue
		}

		// 分类存储
		if cfg.TenantID == "" {
			newPlatformConfig = cfg
		} else {
			newCache[cfg.TenantID] = cfg
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows iteration: %w", err)
	}

	// 原子更新缓存
	m.mu.Lock()
	m.cache = newCache
	if newPlatformConfig != nil {
		m.platformConfig = newPlatformConfig
	}
	m.mu.Unlock()

	m.logger.Debug("intentconfig: reloaded", 
		"tenant_count", len(newCache), 
		"has_platform_config", newPlatformConfig != nil)

	return nil
}

// UpdateConfig 更新配置（立即生效）
func (m *Manager) UpdateConfig(ctx context.Context, cfg *ClassifierConfig) error {
	// 序列化 JSONB 字段
	enabledLayersJSON, err := json.Marshal(cfg.EnabledLayers)
	if err != nil {
		return fmt.Errorf("marshal enabled_layers: %w", err)
	}

	keywordsConfigJSON, err := json.Marshal(cfg.KeywordsConfig)
	if err != nil {
		return fmt.Errorf("marshal keywords_config: %w", err)
	}

	patternsConfigJSON, err := json.Marshal(cfg.PatternsConfig)
	if err != nil {
		return fmt.Errorf("marshal patterns_config: %w", err)
	}

	confidenceThresholdsJSON, err := json.Marshal(cfg.ConfidenceThresholds)
	if err != nil {
		return fmt.Errorf("marshal confidence_thresholds: %w", err)
	}

	// 构造租户ID（空字符串转为NULL）
	var tenantID *string
	if cfg.TenantID != "" {
		tenantID = &cfg.TenantID
	}

	query := `
		INSERT INTO intent_classifier_config (
			tenant_id, strategy, enabled_layers, keywords_config, 
			patterns_config, confidence_thresholds, drift_threshold, 
			multi_turn_memory, llm_fallback_enabled, llm_model, 
			llm_confidence_threshold, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW())
		ON CONFLICT (tenant_id) DO UPDATE SET
			strategy = EXCLUDED.strategy,
			enabled_layers = EXCLUDED.enabled_layers,
			keywords_config = EXCLUDED.keywords_config,
			patterns_config = EXCLUDED.patterns_config,
			confidence_thresholds = EXCLUDED.confidence_thresholds,
			drift_threshold = EXCLUDED.drift_threshold,
			multi_turn_memory = EXCLUDED.multi_turn_memory,
			llm_fallback_enabled = EXCLUDED.llm_fallback_enabled,
			llm_model = EXCLUDED.llm_model,
			llm_confidence_threshold = EXCLUDED.llm_confidence_threshold,
			version = intent_classifier_config.version + 1,
			updated_at = NOW()
	`

	_, err = m.pool.Exec(ctx, query,
		tenantID, cfg.Strategy, enabledLayersJSON, keywordsConfigJSON,
		patternsConfigJSON, confidenceThresholdsJSON, cfg.DriftThreshold,
		cfg.MultiTurnMemory, cfg.LLMFallbackEnabled, cfg.LLMModel,
		cfg.LLMConfidenceThreshold,
	)
	if err != nil {
		return fmt.Errorf("upsert config: %w", err)
	}

	// 立即重新加载
	if err := m.reload(ctx); err != nil {
		m.logger.Warn("intentconfig: reload after update failed", "error", err)
	}

	m.logger.Info("intentconfig: config updated", "tenant_id", cfg.TenantID, "strategy", cfg.Strategy)
	return nil
}

// ReloadTenant 重新加载指定租户配置
func (m *Manager) ReloadTenant(ctx context.Context, tenantID string) error {
	// 简化实现：直接重新加载所有配置
	// 生产环境可优化为只加载指定租户
	return m.reload(ctx)
}
