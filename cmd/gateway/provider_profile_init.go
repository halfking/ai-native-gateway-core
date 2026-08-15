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
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// ProviderProfileWorkers holds all provider profile background workers
type ProviderProfileWorkers struct {
	collector  *bg.ProfileCollector
	aggregator *bg.ProfileAggregator
	cleaner    *bg.ProfileCleaner
	alerts     *bg.ProfileAlertWorker
	// costRecon / costReconciler 仅在 provider_profile.cost_reconciliation.enabled
	// 开启时非空（M3 CO-3，2026-08-15，默认关闭）。
	costRecon      *bg.CostReconciliationWorker
	costReconciler *providerprofile.CostReconciler
}

// AlertEngine returns the alert worker's underlying AlertEngine so main.go can
// inject a handler via SetAlertHandler. Returns nil when the alert worker is
// absent (e.g. provider profile disabled).
func (w *ProviderProfileWorkers) AlertEngine() *providerprofile.AlertEngine {
	if w == nil || w.alerts == nil {
		return nil
	}
	return w.alerts.Engine()
}

// CostReconciler returns the cost reconciliation orchestrator (M3 CO-3) so
// main.go can wire the admin bill-import API to the same instance used by the
// background worker. Returns nil when cost reconciliation is disabled.
func (w *ProviderProfileWorkers) CostReconciler() *providerprofile.CostReconciler {
	if w == nil {
		return nil
	}
	return w.costReconciler
}

// initProviderProfile initializes the provider profile system.
//
// Returns nil when:
//   - pool is nil (no DB mode)
//   - provider_profile is disabled (env var LLM_GATEWAY_PROVIDER_PROFILE_ENABLED=false
//     or settings.GetPlatformBool returns false)
//   - initialization fails (logged as WARN)
//
// The system runs 3 background workers:
//   - ProfileCollector: collects metrics every 2 hours (configurable)
//   - ProfileAggregator: aggregates daily profiles every day at 4am
//   - ProfileCleaner: cleans up old metrics every Sunday at 5am
//
// fernetKey/keyring are the credential decryption keys derived earlier in
// main() (see secret.FernetKeyFromSecret / secret.KeyringFromEnv); the
// collector's network latency probe needs them to decrypt
// credentials.secret_ciphertext before calling GET /v1/models.
func initProviderProfile(pool *pgxpool.Pool, fernetKey []byte, keyring *secret.Keyring) *ProviderProfileWorkers {
	if pool == nil {
		slog.Warn("provider profile: nil pool, system disabled")
		return nil
	}

	// Feature gate: env var takes precedence, then falls back to settings.
	// This avoids any dependency on spec registration in the settings registry.
	var enabled bool
	if envVal, ok := os.LookupEnv("LLM_GATEWAY_PROVIDER_PROFILE_ENABLED"); ok {
		parsed, err := strconv.ParseBool(envVal)
		if err != nil {
			slog.Warn("provider profile: invalid enabled environment value", "value", envVal, "error", err)
			return nil
		}
		enabled = parsed
	} else {
		enabled = settings.GetPlatformBool("provider_profile.enabled", false)
	}
	if !enabled {
		slog.Info("provider profile: disabled via settings")
		return nil
	}

	// Read configuration from settings (env var takes precedence)
	collectionSeconds := int64(7200)
	if envVal := os.Getenv("LLM_GATEWAY_PROVIDER_PROFILE_COLLECTION_INTERVAL"); envVal != "" {
		if v, err := strconv.ParseInt(envVal, 10, 64); err == nil {
			collectionSeconds = v
		}
	}
	if collectionSeconds <= 0 {
		collectionSeconds = int64(settings.GetPlatformDuration("provider_profile.collection_interval", 7200))
	}

	aggregationSeconds := int64(86400)
	if envVal := os.Getenv("LLM_GATEWAY_PROVIDER_PROFILE_AGGREGATION_INTERVAL"); envVal != "" {
		if v, err := strconv.ParseInt(envVal, 10, 64); err == nil {
			aggregationSeconds = v
		}
	}
	if aggregationSeconds <= 0 {
		aggregationSeconds = int64(settings.GetPlatformDuration("provider_profile.aggregation_interval", 86400))
	}

	cleanupSeconds := int64(604800)
	if envVal := os.Getenv("LLM_GATEWAY_PROVIDER_PROFILE_CLEANUP_INTERVAL"); envVal != "" {
		if v, err := strconv.ParseInt(envVal, 10, 64); err == nil {
			cleanupSeconds = v
		}
	}
	if cleanupSeconds <= 0 {
		cleanupSeconds = int64(settings.GetPlatformDuration("provider_profile.cleanup_interval", 604800))
	}

	// Alert evaluation runs daily (after the aggregator). It evaluates auto-disable
	// and auto-enable conditions and flips credential lifecycle accordingly.
	alertSeconds := int64(86400)
	if envVal := os.Getenv("LLM_GATEWAY_PROVIDER_PROFILE_ALERT_INTERVAL"); envVal != "" {
		if v, err := strconv.ParseInt(envVal, 10, 64); err == nil {
			alertSeconds = v
		}
	}
	if alertSeconds <= 0 {
		alertSeconds = int64(settings.GetPlatformDuration("provider_profile.alert_interval", 86400))
	}

	collectionInterval := time.Duration(collectionSeconds) * time.Second
	aggregationInterval := time.Duration(aggregationSeconds) * time.Second
	cleanupInterval := time.Duration(cleanupSeconds) * time.Second
	alertInterval := time.Duration(alertSeconds) * time.Second

	// Create workers
	collector := bg.NewProfileCollector(pool, collectionInterval, fernetKey, keyring)
	aggregator := bg.NewProfileAggregator(pool, aggregationInterval)
	cleaner := bg.NewProfileCleaner(pool, cleanupInterval)
	alerts := bg.NewProfileAlertWorker(pool, alertInterval)

	// Start workers
	collector.Start()
	aggregator.Start()
	cleaner.Start()
	alerts.Start()

	workers := &ProviderProfileWorkers{
		collector:  collector,
		aggregator: aggregator,
		cleaner:    cleaner,
		alerts:     alerts,
	}

	// ── Provider cost reconciliation (M3 CO-3, 2026-08-15) ────────────
	// 月度聚合网关用量到 provider_cost_reconciliation；账单由管理端导入；
	// 差异超阈值写 provider_events 告警事件。独立子开关，默认关闭。
	if costReconciliationEnabled() {
		reconSeconds := int64(86400)
		if envVal := os.Getenv("LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_INTERVAL"); envVal != "" {
			if v, err := strconv.ParseInt(envVal, 10, 64); err == nil && v > 0 {
				reconSeconds = v
			}
		}
		if reconSeconds <= 0 {
			reconSeconds = settings.GetPlatformDuration("provider_profile.cost_reconciliation.interval", 86400)
		}

		costThreshold := settings.GetPlatformFloat("provider_profile.cost_reconciliation.cost_diff_threshold", 0.05)
		tokenThreshold := settings.GetPlatformFloat("provider_profile.cost_reconciliation.token_diff_threshold", 0.05)
		thresholds := providerprofile.DiffThresholds{Cost: costThreshold, Token: tokenThreshold}

		source := providerprofile.NewPGGatewayMonthlyUsageSource(pool)
		store := providerprofile.NewPGReconciliationStore(pool)
		sink := providerprofile.NewPGReconciliationEventSink(pool)
		reconciler := providerprofile.NewCostReconciler(source, store, sink, thresholds)
		costRecon := bg.NewCostReconciliationWorkerFromReconciler(reconciler, time.Duration(reconSeconds)*time.Second)
		costRecon.Start()

		workers.costReconciler = reconciler
		workers.costRecon = costRecon
		slog.Info("provider cost reconciliation enabled",
			"interval", time.Duration(reconSeconds)*time.Second,
			"cost_diff_threshold", costThreshold,
			"token_diff_threshold", tokenThreshold)
	}

	slog.Info("provider profile system initialized",
		"collection_interval", collectionInterval,
		"aggregation_interval", aggregationInterval,
		"cleanup_interval", cleanupInterval,
		"alert_interval", alertInterval)

	return workers
}

// costReconciliationEnabled 读取对账子开关（默认关闭）。
// 环境变量 LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED 优先，
// 其次 settings 的 provider_profile.cost_reconciliation.enabled。
func costReconciliationEnabled() bool {
	if envVal, ok := os.LookupEnv("LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED"); ok {
		parsed, err := strconv.ParseBool(envVal)
		if err != nil {
			slog.Warn("provider cost reconciliation: invalid enabled environment value", "value", envVal, "error", err)
			return false
		}
		return parsed
	}
	return settings.GetPlatformBool("provider_profile.cost_reconciliation.enabled", false)
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
	if workers.alerts != nil {
		workers.alerts.Stop()
	}
	if workers.costRecon != nil {
		workers.costRecon.Stop()
	}

	slog.Info("provider profile workers stopped")
}
