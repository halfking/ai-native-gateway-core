// Package main — storage_mode_init.go
//
// 双模式存储架构（Task 4.2）：主程序启动期的存储模式装配。
//
// 职责边界（与 main.go 的分工）：
//   - 本文件持有全部 lite 模式装配逻辑：加载存储配置（YAML + LLM_GATEWAY_* env）、
//     初始化存储工厂（SQLite 三 store + FileBodies/Memory 单例）、L1.5 FileCache
//     与后台任务（bg.CacheTrimmer / bg.BodiesTrimmer 两个 trimmer + 一致性对账
//     worker bg.ConsistencyWorker，审计 B-#2 接线）；
//   - main.go 仅在五个位置做最小插入：config 加载后调用 initStorageMode、
//     lite 模式跳过 PG 初始化、telemetryClient 构造后注入 lite sink
//     （见 lite_telemetry_sink.go，审计 B2）、SessionCacheV2 构造点走
//     mode-aware 装配、优雅关闭段末尾调用 storageRuntime.Shutdown。
//
// 行为兼容性：LLM_GATEWAY_STORAGE_MODE 未设置或为 "full" 时 initStorageMode
// 返回 nil runtime，调用方全部走既有装配路径，行为零变化。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/config"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/storage"
	storagefactory "github.com/kaixuan/llm-gateway-go/storage/factory"
)

// storageModeLite 是 lite 模式的字符串字面量（与 config.StorageModeLite 一致），
// 供日志与 runtime.mode 字段使用。
const storageModeLite = string(config.StorageModeLite)

// trimmerStopWaitBound 是 Shutdown 时等待清理协程退出的有界时长；
// 超时不阻塞进程退出（清理协程最坏情况在一次 TrimOnce 遍历中响应取消）。
const trimmerStopWaitBound = 3 * time.Second

// storageRuntime 是 lite 模式启动期装配出的存储运行时：
// 持有存储工厂、L1.5 文件缓存与后台清理任务的生命周期。
// full/未启用双模式时 main 持有 nil，所有方法均 nil-safe（no-op / 历史行为）。
type storageRuntime struct {
	factory   *storagefactory.StorageFactory // SQLite 三 store + bodies/state 单例
	fileCache *v2.FileCache                  // L1.5 本地文件缓存（注入 SessionCacheV2）
	mode      string                         // 当前为 "lite"（仅 lite 模式才会创建 runtime）

	// consistencyWorker 是 lite 一致性对账 worker（审计 B-#2 接线）；
	// 配置关闭或 session store 不支持空闲枚举时为 nil。快照持有仅供
	// 测试断言与观测，生命周期由 trimmerCtx/trimmerWG 统一管理。
	consistencyWorker *bg.ConsistencyWorker

	// 关键路径快照：供启动日志与测试断言。
	sqlitePath string
	bodiesDir  string
	cacheDir   string
	logsDir    string

	// trimmers 生命周期：可取消 context 挂在这里，Shutdown 先取消再等退出。
	// 挂载对象：cache/bodies 两个 trimmer + 一致性对账 worker（同生命周期）。
	trimmerCancel context.CancelFunc
	trimmerWG     sync.WaitGroup

	shutdownOnce sync.Once // Shutdown 幂等（可重入）
}

// loadStorageConfig 加载存储配置：有 YAML 路径时优先 LoadStorageConfigFromYAML
// （解析失败只告警并回退 env-only，不阻塞启动），否则仅走 LLM_GATEWAY_* 环境
// 变量（LoadStorageConfigFromEnv）。两个入口对 YAML 未配置字段都应用 env 覆盖。
func loadStorageConfig(yamlPath string) *config.StorageConfig {
	if yamlPath != "" {
		cfg, err := config.LoadStorageConfigFromYAML(yamlPath)
		if err == nil {
			return cfg
		}
		slog.Warn("storage: YAML 存储配置加载失败，回退 env-only",
			"path", yamlPath, "error", err)
	}
	return config.LoadStorageConfigFromEnv()
}

// initStorageMode 按存储模式装配启动期存储运行时。
//
//   - storageCfg 为 nil、或 mode 非 lite（未设置 / full）：返回 nil runtime，
//     调用方走既有装配路径，行为零变化；
//   - mode == lite：ApplyLiteDefaults → Validate → 构造存储工厂 → 创建
//     L1.5 FileCache（CacheTTLHours / CacheMaxSizeGB 换算字节）→ 启动
//     cache / bodies 两个后台清理任务。此处在 main 的 PG 初始化之前执行，
//     不创建 SessionCacheV2（其装配点在压缩链路里，见 storageRuntime.newSessionCacheV2）。
//
// cfg 参数为 main 的主配置，当前装配不消费（保留签名给后续波次接入主配置
// 联动），传入 nil 安全。
func initStorageMode(cfg *config.Config, storageCfg *config.StorageConfig) (*storageRuntime, error) {
	_ = cfg // 保留：后续波次可能按主配置联动（当前 lite 装配只消费 storageCfg）
	if storageCfg == nil || storageCfg.NormalizeMode() != config.StorageModeLite {
		return nil, nil
	}

	storageCfg.ApplyLiteDefaults()
	if err := storageCfg.Validate(); err != nil {
		return nil, fmt.Errorf("storage: lite 配置校验失败: %w", err)
	}
	lite := storageCfg.Lite

	f, err := storagefactory.NewStorageFactory(&storage.StorageConfig{
		Mode:          storage.StorageModeLite,
		SQLitePath:    lite.SQLitePath,
		BodiesDir:     lite.BodiesDir,
		CacheDir:      lite.CacheDir,
		LogsDir:       lite.LogsDir,
		AsyncWriters:  lite.AsyncWriters,
		SQLitePragmas: sqlitePragmasFromLiteConfig(lite.SQLitePragmas),
	})
	if err != nil {
		return nil, fmt.Errorf("storage: 创建存储工厂失败: %w", err)
	}

	// L1.5 文件缓存：TTL 以小时计，容量以 GB 换算为字节。失败时回滚工厂，
	// 不留下半个已打开的 SQLite。
	cacheMaxBytes := int64(lite.CacheMaxSizeGB) << 30
	fileCache, err := v2.NewFileCache(lite.CacheDir, time.Duration(lite.CacheTTLHours)*time.Hour, cacheMaxBytes)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("storage: 创建 L1.5 文件缓存失败: %w", err)
	}

	rt := &storageRuntime{
		factory:    f,
		fileCache:  fileCache,
		mode:       storageModeLite,
		sqlitePath: lite.SQLitePath,
		bodiesDir:  lite.BodiesDir,
		cacheDir:   lite.CacheDir,
		logsDir:    lite.LogsDir,
	}

	// 后台清理任务：两个 Start 均为阻塞式，由本处 go 启动；retention 取自
	// Lite.Retention，清理周期沿用 worker 默认值（cache 1h / bodies 6h）。
	trimmerCtx, cancel := context.WithCancel(context.Background())
	rt.trimmerCancel = cancel
	cacheTrimmer := bg.NewCacheTrimmer(lite.CacheDir, time.Duration(lite.Retention.CacheHours)*time.Hour)
	bodiesTrimmer := bg.NewBodiesTrimmer(lite.BodiesDir, time.Duration(lite.Retention.SessionBodiesDays)*24*time.Hour)
	rt.trimmerWG.Add(2)
	go func() {
		defer rt.trimmerWG.Done()
		cacheTrimmer.Start(trimmerCtx)
	}()
	go func() {
		defer rt.trimmerWG.Done()
		bodiesTrimmer.Start(trimmerCtx)
	}()

	// 一致性对账 worker（审计 B-#2 接线）：lite 启动完成后低频（默认每日）
	// 对「近期活跃且已空闲」的 session 跑 Reconcile。默认 report-only（结构化
	// 日志告警，不动数据）；仅当配置显式 delete_orphans 时才删孤儿（删除路径
	// 自带「删除前 meta 复检 + mtime 宽限」双保险，见 storage.RepairTurnArtifacts）。
	// session store 不支持空闲枚举（非 SQLite 实现）时降级关闭并告警。
	consistencyEnabled := lite.Consistency.Enabled != nil && *lite.Consistency.Enabled
	if consistencyEnabled {
		if lister, ok := f.NewSessionStore().(storage.IdleSessionLister); ok {
			worker := bg.NewConsistencyWorker(lister, f.NewTurnsStore(), f.NewBodiesStore()).
				WithInterval(time.Duration(lite.Consistency.IntervalHours) * time.Hour).
				WithIdleThreshold(time.Duration(lite.Consistency.IdleThresholdMin) * time.Minute).
				WithMaxSessions(lite.Consistency.MaxSessionsPerRun)
			if lite.Consistency.DeleteOrphanBodies {
				worker.WithAction(storage.RepairDeleteOrphanBodies)
			}
			rt.consistencyWorker = worker
			rt.trimmerWG.Add(1)
			go func() {
				defer rt.trimmerWG.Done()
				worker.Start(trimmerCtx)
			}()
		} else {
			consistencyEnabled = false
			slog.Warn("storage lite: session store 不支持空闲会话枚举，一致性对账 worker 未装配",
				"session_store_type", fmt.Sprintf("%T", f.NewSessionStore()))
		}
	}

	slog.Info("storage lite 模式已启用",
		"sqlite_path", rt.sqlitePath,
		"bodies_dir", rt.bodiesDir,
		"cache_dir", rt.cacheDir,
		"logs_dir", rt.logsDir,
		"cache_ttl_hours", lite.CacheTTLHours,
		"cache_max_size_gb", lite.CacheMaxSizeGB,
		"async_writers", lite.AsyncWriters,
		"retention_session_bodies_days", lite.Retention.SessionBodiesDays,
		"retention_request_logs_days", lite.Retention.RequestLogsDays,
		"retention_cache_hours", lite.Retention.CacheHours,
		"consistency_check_enabled", consistencyEnabled,
		"consistency_interval_hours", lite.Consistency.IntervalHours,
		"consistency_idle_threshold_min", lite.Consistency.IdleThresholdMin,
		"consistency_delete_orphans", lite.Consistency.DeleteOrphanBodies,
		"consistency_max_sessions_per_run", lite.Consistency.MaxSessionsPerRun)
	return rt, nil
}

// liteMode 报告当前是否处于 lite 模式（runtime 为 nil 即 full/未启用）。
func (r *storageRuntime) liteMode() bool {
	return r != nil
}

// newSessionCacheV2 构造 SessionCacheV2：
//   - lite 模式：mode-aware 构造（摘除 L2 Redis、注入 L1.5 文件缓存）；
//     db 可为 nil（lite 模式跳过 PG），此时 L3 回源永远 miss（返回 nil 状态），
//     缓存退化为 L1 + L1.5 两层，不会 panic（SessionTurnsReader 对 nil db 防御）；
//   - full / 未启用（r == nil）：委托历史构造函数，行为零变化。
func (r *storageRuntime) newSessionCacheV2(db *pgxpool.Pool, redisAddr string, redisDB int) *v2.SessionCacheV2 {
	if r == nil {
		return v2.NewSessionCacheV2(db, redisAddr, redisDB)
	}
	return v2.NewSessionCacheV2WithMode(db, redisAddr, redisDB, storage.StorageModeLite, r.fileCache)
}

// Shutdown 优雅关闭存储运行时：先取消清理任务（有界等待退出），再关闭工厂
// （幂等，内部会排空 FileBodiesStore 的异步写队列保证落盘）。
// nil-safe 且可重入（full/未启用模式 no-op；重复调用仅首次生效）。
func (r *storageRuntime) Shutdown() {
	if r == nil {
		return
	}
	r.shutdownOnce.Do(func() {
		if r.trimmerCancel != nil {
			r.trimmerCancel()
			r.waitTrimmers(trimmerStopWaitBound)
		}
		if r.factory != nil {
			if err := r.factory.Close(); err != nil {
				slog.Warn("storage runtime: 工厂关闭失败", "error", err)
			}
		}
		slog.Info("storage runtime 已关闭", "mode", r.mode)
	})
}

// waitTrimmers 有界等待全部清理协程退出，超时只告警不阻塞进程退出。
func (r *storageRuntime) waitTrimmers(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		r.trimmerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		slog.Warn("storage runtime: 清理协程退出等待超时，继续关闭", "timeout", timeout.String())
	}
}

// sqlitePragmasFromLiteConfig 把 YAML 中的 sqlite_pragmas 调优项转换为工厂可消费的
// PRAGMA 覆盖 map（PRAGMA 名 → 值）。零值字段跳过、继续沿用 storage/sqlite 默认集合；
// cache_size 遵循 SQLite 负数 KiB 约定（cache_size_kb=64000 → "−64000"，约 64MB 页缓存）。
func sqlitePragmasFromLiteConfig(p config.SQLitePragmasConfig) map[string]string {
	m := make(map[string]string, 4)
	if p.JournalMode != "" {
		m["journal_mode"] = p.JournalMode
	}
	if p.Synchronous != "" {
		m["synchronous"] = p.Synchronous
	}
	if p.BusyTimeoutMS > 0 {
		m["busy_timeout"] = strconv.Itoa(p.BusyTimeoutMS)
	}
	if p.CacheSizeKB > 0 {
		m["cache_size"] = "-" + strconv.Itoa(p.CacheSizeKB)
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
