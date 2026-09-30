// Package main — storage_mode_init.go
//
// 双模式存储架构（Task 4.2）：主程序启动期的存储模式装配。
//
// 职责边界（与 main.go 的分工）：
//   - 本文件持有全部 lite 模式装配逻辑：加载存储配置（YAML + LLM_GATEWAY_* env）、
//     初始化存储工厂（SQLite 三 store + FileBodies/Memory 单例）、L1.5 FileCache
//     与后台任务（bg.CacheTrimmer / bg.BodiesTrimmer 两个 trimmer + 一致性对账
//     worker bg.ConsistencyWorker，审计 B-#2 接线）；
//   - 2026-09-24 方案 H2（P3）：full 模式且热区启用时装配 hotZoneOnly runtime
//     （L1.5 FileCache + FileBodiesStore + HotZoneTrimmer，不建 SQLite 工厂、
//     不收口 Redis env、不启 lite 专属 worker），读链经 NewSessionCacheV2WithMode
//     (full, fileCache) 共享 L1.5；
//   - main.go 仅在五个位置做最小插入：config 加载后调用 initStorageMode、
//     lite 模式跳过 PG 初始化、telemetryClient 构造后注入 lite sink
//     （见 lite_telemetry_sink.go，审计 B2）、SessionCacheV2 构造点走
//     mode-aware 装配、优雅关闭段末尾调用 storageRuntime.Shutdown。
//
// 行为兼容性：LLM_GATEWAY_STORAGE_MODE 未设置或为其他非法值时 initStorageMode
// 返回 nil runtime，行为零变化；显式 "full" 且热区关闭（config 通道）时同样
// 返回 nil runtime（历史装配）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/config"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/monitoring"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/kaixuan/llm-gateway-go/storage"
	storagefactory "github.com/kaixuan/llm-gateway-go/storage/factory"
	filestore "github.com/kaixuan/llm-gateway-go/storage/file"
)

// storageModeLite 是 lite 模式的字符串字面量（与 config.StorageModeLite 一致），
// 供日志与 runtime.mode 字段使用。
const storageModeLite = string(config.StorageModeLite)

// storageModeFull 是 full 模式的字符串字面量（与 config.StorageModeFull 一致），
// 供 hotZoneOnly runtime 的 mode 字段使用（NewSessionCacheV2WithMode 据此保留 L2）。
const storageModeFull = string(config.StorageModeFull)

// trimmerStopWaitBound 是 Shutdown 时等待清理协程退出的有界时长；
// 超时不阻塞进程退出（清理协程最坏情况在一次 TrimOnce 遍历中响应取消）。
const trimmerStopWaitBound = 3 * time.Second

// storageRuntime 是启动期装配出的存储运行时：
// 持有存储工厂、L1.5 文件缓存与后台清理任务的生命周期。
// full/未启用双模式时 main 持有 nil，所有方法均 nil-safe（no-op / 历史行为）。
type storageRuntime struct {
	factory   *storagefactory.StorageFactory // SQLite 三 store + bodies/state 单例（lite 专属；full 热区装配恒 nil）
	fileCache *v2.FileCache                  // L1.5 本地文件缓存（注入 SessionCacheV2）
	mode      string                         // "lite" 或 "full"（hotZoneOnly 装配）

	// hotZoneOnly 标记 full 模式热区装配（2026-09-24 方案 H2 / P3）：runtime
	// 持有 L1.5 FileCache + FileBodiesStore + HotZoneTrimmer，但不持有 SQLite
	// 工厂、不收口 Redis env、不启动 LiteRetention/Consistency 等 lite 专属
	// worker。main 侧一切「lite 语义」判据（跳过 PG、Redis 装配、lite sink、
	// lite auth 门）必须走 liteMode()（= r != nil && !hotZoneOnly），禁止直接
	// 判 storageRt != nil——hotZoneOnly runtime 非 nil 但不是 lite。
	hotZoneOnly bool

	// bodiesStore 是 full 热区装配的会话 body 文件存储（H2：HotZone.Dir/
	// session_bodies，复用 FileBodiesStore 组件，gzip 编码）。lite 模式该职责
	// 归工厂惰性单例（factory.NewBodiesStore），此字段恒 nil。full 模式的
	// 写方接线属后续波次，实例先行装配使目录生命周期归 Shutdown/trimmer 管。
	bodiesStore *filestore.FileBodiesStore

	// requestMirror 是请求侧 body 镜像器（H3：HotZone.Dir/requests 子树，
	// fire-and-forget 异步写、失败仅计数）。full 热区装配时按
	// HotZone.RequestMirrorEnabled 创建，经 bodyMirrorFn 注入 telemetry
	//（request_logs_bodies_hot 三件套）与 SessionWriterV2（session bodies
	// 三件套）。lite 模式不装配（沿用既有 session_bodies 写入路径，不重复
	// 镜像），此字段恒 nil。
	requestMirror *filestore.RequestMirror

	// consistencyWorker 是 lite 一致性对账 worker（审计 B-#2 接线）；
	// 配置关闭或 session store 不支持空闲枚举时为 nil。快照持有仅供
	// 测试断言与观测，生命周期由 trimmerCtx/trimmerWG 统一管理。
	consistencyWorker *bg.ConsistencyWorker

	// cacheTrimmer 是 L1.5 缓存清理 worker。持有引用而非只存 retention 快照：
	// 启动期不变量断言必须读真实 worker 的字段，否则只验证了自己的算术、
	// 看不见 NewCacheTrimmer 的调用点（审计同型缺陷：断言只看到定义）。
	cacheTrimmer *bg.CacheTrimmer

	// hotZoneTrimmer 是热区清理 worker（2026-09-24 方案 H4 P2 接线）：遍历
	// HotZone.Dir 整树，三子树共享配额与保留期。config / settings 任一开关
	// 关闭时不装配（nil）。快照持有仅供观测，生命周期归 trimmerCtx/trimmerWG。
	hotZoneTrimmer *bg.HotZoneTrimmer

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

// resolveCacheTrimRetention 决定 L1.5 文件缓存清理侧（bg.CacheTrimmer）的
// retention，返回值恒 **不小于** cacheTTL。
//
// 为什么需要它：读侧 FileCache 的过期判定是 `time.Since(mtime) >= ttl`，
// ttl 来自 lite.CacheTTLHours；删侧 CacheTrimmer 的删除条件是
// `mtime.Before(now - retention)`。两者原本各吃一个独立配置旋钮
// （cache_ttl_hours / retention.cache_hours），无任何交叉校验，于是
// `retention.cache_hours < cache_ttl_hours` 时，删除侧会删掉读侧仍视为
// 有效的条目：cache_ttl_hours 被静默架空，该时间窗内每次读都退化成 miss
// 并回源下层存储，FileCache 退化为「只写不读」。
//
// 语义：
//   - cacheTTL <= 0：兜底为 24h（与 config.ApplyLiteDefaults 的默认值同口径；
//     正常路径上 ApplyLiteDefaults 已把该字段补为正值，此分支仅防直调）。
//   - retentionCacheHours <= cacheTTL：采用 cacheTTL。显式配小了也不采纳——
//     缓存没有数据正确性风险，但静默架空读侧 TTL 是纯粹的配置谎言。调用方
//     负责对此告警（见 initStorageMode）。
//   - retentionCacheHours > cacheTTL：保留更大的值。条目在逻辑 TTL 之后仍
//     多留一段，抬高 cache_ttl_hours 时无需冷启动重填。
//
// 不做硬报错：升级路径上存量部署可能已配了更小的 cache_hours，直接拒绝启动
// 的爆炸半径远大于收益。缓存层可安全降级为「多留一会儿」，故取安全上界。
func resolveCacheTrimRetention(cacheTTL, retentionCacheHours int) time.Duration {
	ttl := time.Duration(cacheTTL) * time.Hour
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	retention := time.Duration(retentionCacheHours) * time.Hour
	if retention < ttl {
		return ttl
	}
	return retention
}

// initStorageMode 按存储模式装配启动期存储运行时（dispatcher）。
//
//   - storageCfg 为 nil、或 mode 非法/未设置：返回 nil runtime，
//     调用方走既有装配路径，行为零变化；
//   - mode == lite：SQLite 工厂 + L1.5 FileCache + 全套后台任务（见
//     initLiteStorageMode）；
//   - mode == full 且热区启用（config 通道）：hotZoneOnly runtime（L1.5 +
//     FileBodiesStore + HotZoneTrimmer，无 SQLite/无 Redis 收口，见
//     initFullHotZoneStorageMode）；热区关闭时返回 nil（历史装配）。
//
// cfg 参数为 main 的主配置，当前装配不消费（保留签名给后续波次接入主配置
// 联动），传入 nil 安全。
func initStorageMode(cfg *config.Config, storageCfg *config.StorageConfig) (*storageRuntime, error) {
	_ = cfg // 保留：后续波次可能按主配置联动（当前装配只消费 storageCfg）
	switch storageCfg.NormalizeMode() {
	case config.StorageModeLite:
		return initLiteStorageMode(storageCfg)
	case config.StorageModeFull:
		return initFullHotZoneStorageMode(storageCfg)
	default:
		// 未设置 / 非法 mode：未启用双模式，历史装配（full 语义），行为零变化。
		monitoring.Default().SetStorageMode(monitoring.ModeFull)
		return nil, nil
	}
}

// initFullHotZoneStorageMode 在 full 模式且热区启用时装配 hotZoneOnly runtime
// （2026-09-24 方案 H2 / P3）。
//
// 装配内容：L1.5 FileCache（HotZone.Dir/cache）+ FileBodiesStore（HotZone.Dir/
// session_bodies）+ HotZoneTrimmer（三子树共享预算）。**不**创建 SQLite 工厂、
// **不**收口 Redis env（full 的 Redis 是 L2 治理缓存，必须保持）、**不**启动
// LiteRetention/Consistency/Cache/Bodies 等 lite 专属 worker。
//
// 装配门只认 config 通道（YAML hotzone.enabled / env LLM_GATEWAY_HOTZONE_ENABLED），
// 不查 settings：settings_kv 后端挂在 PG 之上，main 要等 dbConn 初始化完成后
// 才注册 platform backend，装配点（PG 之前）读到的恒为 default；spec 的 env
// 通道经 EnvNameAuto 与 config env 同名（LLM_GATEWAY_HOTZONE_ENABLED），亦无
// 增量能力。
//
// storage.hotzone_enabled 运行期 false 的显式语义（§2026-09-30 审计遗留 3 的
// 定义，单测钉死）：
//  1. 已装配进程**不卸载**——FileCache/FileBodiesStore 照常读写（纯缓存，
//     fail-open，删目录即回滚），仅 HotZoneTrimmer 每 tick 重读开关、false 即
//     跳过清理轮（P2 既有语义，bg 包测试钉死）；
//  2. 「停新装配」由 config 通道在下一次进程启动生效；settings_kv 的 false
//     不传递到下一次装配判定（装配点读不到 settings_kv，两模式同理）。
//
// 失败语义：热区自身装配失败（FileCache 创建失败等）返回错误、进程退出
// （fail-fast，配置了热区却建不出来属于运维必须介入的状态）；热区根目录已
// 存在但为 symlink/普通文件时告警并降级为历史装配（返回 nil——升级路径上
// 已有目录形态的部署不应被拒绝启动）。
func initFullHotZoneStorageMode(storageCfg *config.StorageConfig) (*storageRuntime, error) {
	// H5（P5）：进程模式戳与热区启用标记（镜像 per-mode 计数分桶依据；
	// /metrics/storage 的 hotzone_enabled 此前恒 false——setter 无调用方的
	// 预存缺口，随本项闭合）。无论热区开关与否，full 语义的进程模式都是 full；
	// hotzone_enabled 以「装配是否真正走通」为准（关闭/降级/失败均 false），
	// 单 defer 出口统一盖章，避免多路径漏记。
	monitoring.Default().SetStorageMode(monitoring.ModeFull)
	hotzoneActive := false
	// mirrorActive 与 hotzoneActive 同一 defer 出口盖章：mirror.hotzone_on
	// 此前是 setter-without-caller 的恒 false 键（2026-09-30 部署演练 O4），
	// 语义=请求侧镜像是否已装配（RequestMirrorEnabled 门控）。
	mirrorActive := false
	defer func() {
		monitoring.Default().SetHotZoneEnabled(hotzoneActive)
		monitoring.Default().SetMirrorHotZoneOn(mirrorActive)
	}()

	// H4：HotZone 段默认值补齐（nil-safe、幂等），full 分支不走 ApplyLiteDefaults。
	storageCfg.ApplyHotZoneDefaults()
	hz := storageCfg.HotZone
	if !hz.IsEnabled(true) {
		slog.Info("storage full: hotzone 关闭（config 通道），走历史装配（runtime=nil）",
			"dir", hz.Dir)
		return nil, nil
	}

	// 热区根目录最小安全检查：已存在但为 symlink / 普通文件 → 拒绝装配并
	// 降级历史路径。与 P2 trimmer 的 OpenRoot symlink 拒绝同向——写链不得
	// 穿出受管目录。Trailing separator 先剥掉，避免 Lstat("dir/") 穿透 symlink。
	// 注意此处**不**跑 config.Validate()：其 full 分支强依赖 full_storage 段
	//（postgres_url/redis_url 必填），env-only 存量 full 部署没有该段，Validate
	// 失败会把热区从所有这类部署上阉割掉（main 对 full 的 Validate 也是
	// non-fatal 同先例）。
	rootForStat := strings.TrimRight(filepath.FromSlash(hz.Dir), string(filepath.Separator))
	if rootForStat == "" {
		rootForStat = filepath.FromSlash(hz.Dir)
	}
	if info, err := os.Lstat(rootForStat); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			slog.Warn("storage full: hotzone.dir 已存在且为符号链接或非目录，热区不装配，走历史装配",
				"dir", hz.Dir)
			return nil, nil
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("storage: hotzone.dir %q 状态检查失败: %w", hz.Dir, err)
	}

	cacheDir := filepath.Join(hz.Dir, "cache")
	bodiesDir := filepath.Join(hz.Dir, "session_bodies")
	retention := time.Duration(hz.RetentionHours) * time.Hour
	maxBytes := int64(hz.MaxSizeGB) << 30

	// L1.5 文件缓存：TTL 对齐热区保留期（读侧过期与删侧 retention 同界）。
	// settings 运行期把 retention 调小于配置 TTL 时，删侧先行——缓存纯语义
	// 无损（miss 回源 PG），lite 侧的 resolveCacheTrimRetention 钳制不适用
	//（full 无独立 cache_ttl 旋钮，两者同源即天然对齐）。
	fileCache, err := v2.NewFileCache(cacheDir, retention, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("storage: 创建 full 热区 L1.5 文件缓存失败: %w", err)
	}

	// 会话 body 文件存储：复用 FileBodiesStore（gzip 默认编码），workers 传 0
	// 由 AsyncFileWriter 取默认（与 factory defaultFileWorkers=4 同值）。
	bodiesStore := filestore.NewFileBodiesStore(bodiesDir, 0)

	// 请求侧 body 镜像器（H3）：HotZone.RequestMirror 默认开启；关闭时镜像
	// 整体缺席（bodyMirrorFn 返回 nil，两个消费方自然 no-op）。
	var requestMirror *filestore.RequestMirror
	if hz.RequestMirrorEnabled(true) {
		requestMirror = filestore.NewRequestMirror(hz.Dir, 0)
	}

	rt := &storageRuntime{
		fileCache:     fileCache,
		bodiesStore:   bodiesStore,
		requestMirror: requestMirror,
		mode:          storageModeFull,
		hotZoneOnly:   true,
		bodiesDir:     bodiesDir,
		cacheDir:      cacheDir,
	}

	// 热区清理 worker：三子树（cache / session_bodies / requests）共享
	// MaxSizeGB 预算与 RetentionHours 保留期；cache 子树在热区内 → settings
	// 预算热重载同步驱动 FileCache.ResizeMax（resizeInHotzone=true）。
	trimmerCtx, cancel := context.WithCancel(context.Background())
	rt.trimmerCancel = cancel
	rt.startHotZoneTrimmer(trimmerCtx, hz, true)

	slog.Info("storage full 模式热区已装配（hotZoneOnly）",
		"hotzone_dir", hz.Dir,
		"cache_dir", cacheDir,
		"session_bodies_dir", bodiesDir,
		"retention_hours", hz.RetentionHours,
		"max_size_gb", hz.MaxSizeGB,
		"l1_5_ttl", retention.String(),
		"sqlite_factory", false,
		"redis_env_untouched", true,
		"lite_workers", false)
	hotzoneActive = true                // 装配走通，defer 出口据此盖章 hotzone_enabled=true
	mirrorActive = requestMirror != nil // mirror.hotzone_on 同出口盖章
	return rt, nil
}

// initLiteStorageMode 装配 lite 模式存储运行时：ApplyLiteDefaults → Validate →
// 构造存储工厂 → 创建 L1.5 FileCache（CacheTTLHours / CacheMaxSizeGB 换算字节）
// → 启动 cache / bodies / 行级保留期 / 一致性对账 / 热区后台任务。此处在 main
// 的 PG 初始化之前执行，不创建 SessionCacheV2（其装配点在压缩链路里，见
// storageRuntime.newSessionCacheV2）。
func initLiteStorageMode(storageCfg *config.StorageConfig) (*storageRuntime, error) {
	// H5（P5）：进程模式戳（镜像 per-mode 计数分桶依据）。
	monitoring.Default().SetStorageMode(monitoring.ModeLite)

	// R52：S4 停写门控（storage.request_logs_write_enabled）此前在 lite
	// 形态结构性失效——specs 只在 main.go 的 dbConn.Enabled() 分支注册，
	// lite 进程里 Global.Spec(key)==nil → GetPlatformBool 恒回落 true，
	// 开关无法表达（docs/storage 会话存储解耦方案写明 lite「同门控」）。
	// 此处注册 storage 族 specs（nil DB = 仅 env→default 解析链），只收窄
	// 到 storage 族，不把 Full 侧其余 settings 语义无差别带进 lite。
	settings.Init(nil)
	for _, sp := range settings.PlatformSpecs() {
		if sp.Category == settings.CategoryStorage {
			// 重复注册（测试双初始化）不致命，忽略 dup 错误。
			_ = settings.Global.RegisterSpec(sp)
		}
	}

	storageCfg.ApplyLiteDefaults()
	// H4（2026-09-24 方案）：HotZone 段默认值补齐（nil-safe、幂等）。lite 装配
	// 路径不走 ApplyDefaults，须显式归一后 hotzone trimmer 才能拿到合法的
	// Dir / RetentionHours / MaxSizeGB。
	storageCfg.ApplyHotZoneDefaults()
	if err := storageCfg.Validate(); err != nil {
		return nil, fmt.Errorf("storage: lite 配置校验失败: %w", err)
	}
	lite := storageCfg.Lite

	f, err := storagefactory.NewStorageFactory(&storage.StorageConfig{
		Mode:          storage.StorageModeLite,
		SQLitePath:    lite.SQLitePath,
		BodiesDir:     lite.BodiesDir,
		BodiesCodec:   lite.BodiesCodec,
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

	// 后台清理任务：三个 Start 均为阻塞式，由本处 go 启动；retention 取自
	// Lite.Retention，清理周期沿用 worker 默认值（cache 1h / bodies 6h /
	// 行级 retention 6h）。cache 的 retention 例外——必须与 FileCache TTL
	// 对齐，见 resolveCacheTrimRetention。
	trimmerCtx, cancel := context.WithCancel(context.Background())
	rt.trimmerCancel = cancel
	cacheTrimRetention := resolveCacheTrimRetention(lite.CacheTTLHours, lite.Retention.CacheHours)
	if lite.Retention.CacheHours > 0 &&
		time.Duration(lite.Retention.CacheHours)*time.Hour < time.Duration(lite.CacheTTLHours)*time.Hour {
		slog.Warn("storage lite: retention.cache_hours 小于 cache_ttl_hours，已按 cache_ttl_hours 对齐缓存清理周期",
			"retention_cache_hours", lite.Retention.CacheHours,
			"cache_ttl_hours", lite.CacheTTLHours,
			"effective_cache_trim_retention", cacheTrimRetention.String())
	}
	cacheTrimmer := bg.NewCacheTrimmer(lite.CacheDir, cacheTrimRetention)
	rt.cacheTrimmer = cacheTrimmer
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

	// 行级保留期 worker（R46 F6）：此前 RequestLogsDays 是死配置——文件侧
	// 有两个 Trimmer，SQLite 行侧（request_logs/sessions/session_turns）无
	// 任何 TTL，长跑单文件无界增长。sessions/turns 以会话为单位整体清理。
	if sqlDB := f.SQLiteDB(); sqlDB != nil {
		retention := bg.NewLiteRetentionWorker(sqlDB,
			time.Duration(lite.Retention.RequestLogsDays)*24*time.Hour)
		rt.trimmerWG.Add(1)
		go func() {
			defer rt.trimmerWG.Done()
			retention.Start(trimmerCtx)
		}()
	} else {
		// 防御分支：lite 分支 factory 打开失败已提前返回，正常不可达。
		// 注意 ApplyLiteDefaults 把 RequestLogsDays<=0 钳为默认 7——行级
		// 保留期没有 opt-out 语义（0 即 7d），运维文档按此口径。
		slog.Warn("storage lite: 行级保留期 worker 未装配（无 SQLite 句柄），SQLite 行数据不会自动清理",
			"request_logs_days", lite.Retention.RequestLogsDays)
	}

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

	// 热区清理 worker（2026-09-24 方案 H4 P2 接线；两模式共用
	// startHotZoneTrimmer）：开关语义（AND）与热重载方式见其注释。今日该
	// 目录在 full 侧的写入方随 H2 装配落地；lite 的 cache_dir 默认独立于
	// 热区，trimmer 对缺失目录静默 no-op。
	if hzCfg := storageCfg.HotZone; hzCfg != nil && hzCfg.IsEnabled(true) &&
		settings.GetPlatformBool("storage.hotzone_enabled", true) {
		// resizeInHotzone：仅当 L1.5 cache 子树位于热区目录内时，settings 才
		// 同步驱动 FileCache.maxSize（H4 §改动3 统一预算 + ResizeMax 联动）。
		// lite 默认 cache_dir 独立于热区，恒不触发。
		rt.startHotZoneTrimmer(trimmerCtx, hzCfg, rt.fileCache != nil && hzCfg.Contains(lite.CacheDir))
		monitoring.Default().SetHotZoneEnabled(true)
	} else {
		monitoring.Default().SetHotZoneEnabled(false)
		slog.Info("storage hotzone 关闭，trimmer 未装配",
			"config_enabled", storageCfg.HotZone.IsEnabled(false),
			"settings_enabled", settings.GetPlatformBool("storage.hotzone_enabled", true))
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
		"cache_trim_retention", rt.cacheTrimmer.Retention().String(),
		"consistency_check_enabled", consistencyEnabled,
		"consistency_interval_hours", lite.Consistency.IntervalHours,
		"consistency_idle_threshold_min", lite.Consistency.IdleThresholdMin,
		"consistency_delete_orphans", lite.Consistency.DeleteOrphanBodies,
		"consistency_max_sessions_per_run", lite.Consistency.MaxSessionsPerRun)
	return rt, nil
}

// startHotZoneTrimmer 装配并启动热区清理 worker（2026-09-24 方案 H4 P2；lite /
// full 两模式共用）。每 30 分钟一轮遍历 HotZone.Dir 三个受管子树
// （cache / session_bodies / requests），先按 mtime 删过期、超配额再最旧先删。
// 前置：rt.trimmerCancel/trimmerWG 生命周期组已初始化、调用方已持有 ctx。
//
// 开关语义（AND，lite 分支）：config（YAML/env HotZone.Enabled）与 settings
// （storage.hotzone_enabled）双通道同时为真才装配——settings 只能关停、不能
// 强行打开 config 已显式关闭的 Dangerous 级特性。（full 分支的装配门仅
// config 通道，差异原因见 initFullHotZoneStorageMode 注释：装配点无 settings
// 后端可读，settings 的运行期语义=停清不卸载。）
//
// 热重载方式（已实证 2026-09-28）：settings.GetPlatformBool/Int 每次调用
// 走 Global.EffectiveValue 直查 settings_kv→env→default，无缓存层
// （settings/helpers.go 头注，热重载测试钉死禁止加缓存）。故经 WithReload
// 在每轮 tick 前直查刷新即可，变更 ≤1 个 trimmer 周期（30 分钟）生效；
// 不经 hotconfig——其只轮询 llmgw_% 前缀键，storage.hotzone_* 不在视野。
//
// resizeInHotzone 由调用方按「L1.5 cache 子树是否位于热区目录内」判定：
// 是则 settings 预算热重载同步驱动 FileCache.ResizeMax（仅当 enabled）；
// 缩容交由本轮 trimmer 执行（H4 §改动3）。
func (r *storageRuntime) startHotZoneTrimmer(ctx context.Context, hzCfg *config.HotZoneConfig, resizeInHotzone bool) {
	hotZoneTrimmer := bg.NewHotZoneTrimmer(
		hzCfg.Dir,
		time.Duration(settings.GetPlatformInt("storage.hotzone_retention_hours", hzCfg.RetentionHours))*time.Hour,
		int64(settings.GetPlatformInt("storage.hotzone_max_size_gb", hzCfg.MaxSizeGB))<<30,
	).WithReload(func() (time.Duration, int64, bool) {
		retention := time.Duration(settings.GetPlatformInt("storage.hotzone_retention_hours", hzCfg.RetentionHours)) * time.Hour
		maxBytes := int64(settings.GetPlatformInt("storage.hotzone_max_size_gb", hzCfg.MaxSizeGB)) << 30
		enabled := settings.GetPlatformBool("storage.hotzone_enabled", true)
		if enabled && resizeInHotzone {
			r.fileCache.ResizeMax(maxBytes) // 内部持锁；扩容即生效，缩容由本轮 trimmer 执行
		}
		return retention, maxBytes, enabled
	})
	r.hotZoneTrimmer = hotZoneTrimmer
	r.trimmerWG.Add(1)
	go func() {
		defer r.trimmerWG.Done()
		hotZoneTrimmer.Start(ctx)
	}()
	slog.Info("storage hotzone trimmer 已装配",
		"mode", r.mode,
		"dir", hzCfg.Dir,
		"retention_hours", settings.GetPlatformInt("storage.hotzone_retention_hours", hzCfg.RetentionHours),
		"max_size_gb", settings.GetPlatformInt("storage.hotzone_max_size_gb", hzCfg.MaxSizeGB),
		"cache_dir_in_hotzone", resizeInHotzone,
		"trim_interval", "30m")
}

// bodyMirrorFn 返回 H3 请求侧镜像的投递闭包：把 storage/file.RequestMirror
// 的 MirrorAsync（fire-and-forget、失败仅计数）适配为 v2.BodyMirrorFunc /
// telemetry.BodyMirrorFunc 共用的函数形态（两处 direction 字面量同词表）。
// 未装配（nil requestMirror：lite 模式 / full 热区关闭 / request_mirror=false
// / runtime nil）恒返回 nil，消费方零开销 no-op。
func (r *storageRuntime) bodyMirrorFn() func(tenantID, requestID, direction string, payload json.RawMessage, at time.Time) {
	if r == nil || r.requestMirror == nil {
		return nil
	}
	mirror := r.requestMirror
	return func(tenantID, requestID, direction string, payload json.RawMessage, at time.Time) {
		mirror.MirrorAsync(tenantID, requestID, filestore.RequestDirection(direction), payload, at)
	}
}

// liteMode 报告当前是否处于 lite 模式：runtime 存在且非 hotZoneOnly。
// full 热区装配（P3）的 runtime 非 nil 但 mode=full，不得被误判为 lite——
// main 侧所有「lite 语义」门控（跳过 PG 初始化、Redis env 收口与 Redis
// 装配门、lite telemetry sink、lite auth 门）一律经由本方法，禁止直接判
// storageRt != nil。
func (r *storageRuntime) liteMode() bool {
	return r != nil && !r.hotZoneOnly
}

// liteRedisEnvKeys 是 lite 模式按策略收口（unset）的 Redis 相关环境变量：
// 前三项对齐主配置 config.go 的 LLM_GATEWAY_REDIS_* 装配源，后三项覆盖
// domains 侧直读 os.Getenv 的 mode-blind 消费者（credential fp slot /
// RPM 限流等，audit 2026-09-14 R28 #16）。
var liteRedisEnvKeys = []string{
	"LLM_GATEWAY_REDIS_ADDR",
	"LLM_GATEWAY_REDIS_PASSWORD",
	"LLM_GATEWAY_REDIS_DB",
	"REDIS_URL",
	"RATE_LIMIT_REDIS_URL",
	"RPM_REDIS_URL",
}

// disableRedisEnv 是 lite 模式的 Redis env 收口（audit 2026-09-14 R28 #16）：
// 遍历 liteRedisEnvKeys，对非空值先记 Warn（只记 key 与原值长度，不落明文，
// 保留溯源线索）再 Unset，保证下游 —— main 的 Redis 装配段与 domains 直读
// env 的消费者 —— 在 lite 模式下读到空值。nil-safe；hotZoneOnly（full 热区
// 装配）恒 no-op——full 的 Redis 是 L2 治理缓存，env 必须原样保留。
// 注意：主 Config 在本调用之前已完成解析，cfg.RedisAddr 等字段不受影响，
// main 侧装配段以 liteMode() 做二次门控。
func (r *storageRuntime) disableRedisEnv() {
	if r == nil || r.hotZoneOnly {
		return
	}
	for _, k := range liteRedisEnvKeys {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			slog.Warn("storage lite mode: Redis disabled by policy", "key", k, "previous_len", len(v))
			_ = os.Unsetenv(k)
		}
	}
}

// newSessionCacheV2 构造 SessionCacheV2：
//   - 未启用双模式（r == nil）：委托历史构造函数，行为零变化；
//   - lite 模式：mode-aware 构造（摘除 L2 Redis、注入 L1.5 文件缓存）；
//     db 可为 nil（lite 模式跳过 PG），此时 L3 回源永远 miss（返回 nil 状态），
//     缓存退化为 L1 + L1.5 两层，不会 panic（SessionTurnsReader 对 nil db 防御）；
//   - full 热区模式（hotZoneOnly）：NewSessionCacheV2WithMode(full, fileCache)——
//     保留 L2 Redis 治理缓存、注入 L1.5（da6b95627 契约翻转：full+fileCache
//     即共享 L1.5），读链 L1 → L1.5 → L2 → L3，由 H2 装配点可选注入正式落地。
func (r *storageRuntime) newSessionCacheV2(db *pgxpool.Pool, redisAddr string, redisDB int) *v2.SessionCacheV2 {
	if r == nil {
		return v2.NewSessionCacheV2(db, redisAddr, redisDB)
	}
	return v2.NewSessionCacheV2WithMode(db, redisAddr, redisDB, storage.StorageMode(r.mode), r.fileCache)
}

// Get*Store getter 已移除（audit 2026-09-14 R28 #19）：SQLite catalog 四表
// 为 EXPERIMENTAL placeholder，未接入 data plane / admin plane，lite 的
// credentials/catalog 生产路径走 YAML 配置。仍需访问测试/实验实现时直接
// 使用 r.factory.New*Store()（storage/factory 保留不动）。

// Shutdown 优雅关闭存储运行时：先取消清理任务（有界等待退出），再关闭
// full 热区的 FileBodiesStore（幂等，内部排空异步写队列保证落盘）与 lite
// 工厂。nil-safe 且可重入（未启用双模式 no-op；重复调用仅首次生效）。
func (r *storageRuntime) Shutdown() {
	if r == nil {
		return
	}
	r.shutdownOnce.Do(func() {
		if r.trimmerCancel != nil {
			r.trimmerCancel()
			r.waitTrimmers(trimmerStopWaitBound)
		}
		if r.bodiesStore != nil {
			if err := r.bodiesStore.Close(); err != nil {
				slog.Warn("storage runtime: 热区会话 body 存储关闭失败", "error", err)
			}
		}
		if r.requestMirror != nil {
			if err := r.requestMirror.Close(); err != nil {
				slog.Warn("storage runtime: 请求镜像写入器关闭失败", "error", err)
			}
		}
		if r.factory != nil {
			if err := r.factory.Close(); err != nil {
				slog.Warn("storage runtime: 工厂关闭失败", "error", err)
			}
		}
		slog.Info("storage runtime 已关闭", "mode", r.mode, "hotzone_only", r.hotZoneOnly)
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
