package ursm

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Manager URSM统一路由状态管理器
type Manager struct {
	// 配置
	config *Config

	// 存储层
	db          *pgxpool.Pool
	redisClient *redis.Client

	// 四层缓存
	providerCache   *LayerCache[ProviderState]
	credentialCache *LayerCache[CredentialState]
	modelCache      *LayerCache[ModelState]
	nodeCache       *LayerCache[NodeState]

	// 资源管理器（Task 2 中实现）
	fpSlotMgr   *FingerprintSlotManager
	concSlotMgr *ConcurrencySlotManager

	// 成本评分器（Task 3 中实现）
	costScorer *CostScorer

	// 批量写入器
	batchWriter *BatchWriter

	// 探测器提交函数（函数注入，避免循环依赖）
	probeSubmitter ProbeSubmitter
}

// NewManager 创建URSM管理器
func NewManager(db *pgxpool.Pool, redisClient *redis.Client, cfg *Config) *Manager {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	m := &Manager{
		config:      cfg,
		db:          db,
		redisClient: redisClient,
	}

	// 初始化四层缓存
	m.providerCache = NewLayerCache[ProviderState](
		redisClient,
		db,
		cfg.MemCacheTTL,
		cfg.RedisCacheTTL,
		"ursm:provider",
		nil, // DB查询函数由后续任务实现
	)

	m.credentialCache = NewLayerCache[CredentialState](
		redisClient,
		db,
		cfg.MemCacheTTL,
		cfg.RedisCacheTTL,
		"ursm:credential",
		nil,
	)

	m.modelCache = NewLayerCache[ModelState](
		redisClient,
		db,
		cfg.MemCacheTTL,
		cfg.RedisCacheTTL,
		"ursm:model",
		nil,
	)

	m.nodeCache = NewLayerCache[NodeState](
		redisClient,
		db,
		cfg.MemCacheTTL,
		cfg.RedisCacheTTL,
		"ursm:node",
		nil,
	)

	// 初始化批量写入器
	m.batchWriter = NewBatchWriter(db, redisClient, nil)

	// 初始化资源管理器
	m.fpSlotMgr = NewFingerprintSlotManager(redisClient, cfg.FpSlotConfig)
	m.concSlotMgr = NewConcurrencySlotManager(redisClient)

	// 初始化成本评分器
	m.costScorer = NewCostScorer(cfg.ScoringWeights)

	return m
}

// Start 启动管理器
func (m *Manager) Start(ctx context.Context) error {
	slog.Info("ursm manager starting",
		"mem_cache_ttl", m.config.MemCacheTTL,
		"redis_cache_ttl", m.config.RedisCacheTTL)

	// 启动批量写入器（后续任务实现）
	// m.batchWriter.Start(ctx)

	slog.Info("ursm manager started")
	return nil
}

// Stop 停止管理器
func (m *Manager) Stop() error {
	slog.Info("ursm manager stopping")

	// 停止批量写入器（后续任务实现）
	// m.batchWriter.Stop()

	slog.Info("ursm manager stopped")
	return nil
}

// Enabled 检查URSM是否启用（特性开关）
func (m *Manager) Enabled() bool {
	// URSM在Task 1完成后默认启用
	return m.config != nil
}

// SetProbeSubmitter 设置探测提交函数（避免循环依赖）
func (m *Manager) SetProbeSubmitter(submitter ProbeSubmitter) {
	m.probeSubmitter = submitter
}

// 以下方法在后续任务中实现

// ReleaseResources 释放资源（Task Package 2）
func (m *Manager) ReleaseResources(ctx context.Context, credentialID int, sessionID string, fpSlotIndex int) error {
	// TODO: Task 2 - 实现资源释放
	return nil
}
