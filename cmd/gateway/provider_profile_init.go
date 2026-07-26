// Package main — provider_profile_init.go
//
// Provider Profile System Integration (Phase 1, 2026-07-26)
//
// 设计目标：
//  1. 无侵入接续 — 在网关启动后初始化供应商画像系统，不影响主流程
//  2. Feature-flagged — 通过配置项控制是否启用（provider_profile.enabled）
//  3. Fail-safe — 初始化失败只 log WARN 不阻塞网关启动
//  4. Schema 先决 — 数据库表必须已部署（2026-07-26-provider-profile-system.sql）
//  5. 配置驱动 — 采集频率等参数从 settings 读取
//
// 使用方式：
//
//	在 main() 函数的 dbConn != nil 块中调用：
//	  profileWorkers := initProviderProfile(dbConn.Pool())
//	在 shutdown 序列中调用：
//	  stopProviderProfile(profileWorkers)
package main

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// ProviderProfileWorkers holds all provider profile background workers
type ProviderProfileWorkers struct {
	collector  *bg.ProfileCollector
	aggregator *bg.ProfileAggregator
	cleaner    *bg.ProfileCleaner
}

// initProviderProfile initializes the provider profile system
//
// Returns nil when:
//   - pool is nil (no DB mode)
//   - provider_profile.enabled setting is false
//   - initialization fails (logged as WARN)
//
// The system runs 3 background workers:
//   - ProfileCollector: collects metrics every 2 hours (configurable)
//   - ProfileAggregator: aggregates daily profiles every day at 4am
//   - ProfileCleaner: cleans up old metrics every Sunday at 5am
func initProviderProfile(pool *pgxpool.Pool) *ProviderProfileWorkers {
	if pool == nil {
		slog.Warn("provider profile: nil pool, system disabled")
		return nil
	}

	// Check if provider profile system is enabled
	if !settings.GetPlatformBool("provider_profile.enabled", false) {
		slog.Info("provider profile: disabled via settings")
		return nil
	}

	// Read configuration from settings
	collectionSeconds := settings.GetPlatformDuration("provider_profile.collection_interval", int64((2*time.Hour)/time.Second))
	aggregationSeconds := settings.GetPlatformDuration("provider_profile.aggregation_interval", int64((24*time.Hour)/time.Second))
	cleanupSeconds := settings.GetPlatformDuration("provider_profile.cleanup_interval", int64((7*24*time.Hour)/time.Second))
	collectionInterval := time.Duration(collectionSeconds) * time.Second
	aggregationInterval := time.Duration(aggregationSeconds) * time.Second
	cleanupInterval := time.Duration(cleanupSeconds) * time.Second

	// Create workers
	collector := bg.NewProfileCollector(pool, collectionInterval)
	aggregator := bg.NewProfileAggregator(pool, aggregationInterval)
	cleaner := bg.NewProfileCleaner(pool, cleanupInterval)

	// Start workers
	collector.Start()
	aggregator.Start()
	cleaner.Start()

	slog.Info("provider profile system initialized",
		"collection_interval", collectionInterval,
		"aggregation_interval", aggregationInterval,
		"cleanup_interval", cleanupInterval)

	return &ProviderProfileWorkers{
		collector:  collector,
		aggregator: aggregator,
		cleaner:    cleaner,
	}
}

// stopProviderProfile gracefully stops all provider profile workers
func stopProviderProfile(workers *ProviderProfileWorkers) {
	if workers == nil {
		return
	}

	slog.Info("stopping provider profile workers...")

	if workers.collector != nil {
		workers.collector.Stop()
	}
	if workers.aggregator != nil {
		workers.aggregator.Stop()
	}
	if workers.cleaner != nil {
		workers.cleaner.Stop()
	}

	slog.Info("provider profile workers stopped")
}
