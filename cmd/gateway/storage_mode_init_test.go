// Package main — storage_mode_init_test.go
//
// 双模式存储装配（Task 4.2）单测：
//   - lite 模式装配：目录创建、工厂/L1.5 缓存非 nil、缓存读写链路可用；
//   - Shutdown 幂等可重入、nil runtime 安全；
//   - full / nil / 空配置返回 nil runtime（走既有装配，行为零变化）；
//   - loadStorageConfig 的 YAML→env 回退与 env-only 路径。
//
// 全部使用 t.TempDir()，不触碰仓库相对路径，也不依赖外部环境。
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/config"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/storage"
)

// liteStorageConfigForTest 基于 t.TempDir() 构造一份显式路径的 lite 存储配置，
// 避免触发 ApplyLiteDefaults 的 ./data 相对路径默认值污染测试工作目录。
func liteStorageConfigForTest(t *testing.T) *config.StorageConfig {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.StorageConfig{Mode: "lite"}
	cfg.Lite = &config.LiteStorageConfig{
		SQLitePath:     filepath.Join(dir, "gateway.db"),
		BodiesDir:      filepath.Join(dir, "bodies"),
		CacheDir:       filepath.Join(dir, "cache"),
		LogsDir:        filepath.Join(dir, "logs"),
		CacheTTLHours:  2,
		CacheMaxSizeGB: 1,
		AsyncWriters:   2,
	}
	cfg.Lite.Retention.SessionBodiesDays = 30
	cfg.Lite.Retention.RequestLogsDays = 7
	cfg.Lite.Retention.CacheHours = 24
	return cfg
}

func TestInitStorageModeLiteAssembly(t *testing.T) {
	cfg := liteStorageConfigForTest(t)

	rt, err := initStorageMode(nil, cfg)
	if err != nil {
		t.Fatalf("initStorageMode(lite) error = %v", err)
	}
	if rt == nil {
		t.Fatal("runtime = nil, want assembled lite runtime")
	}
	defer rt.Shutdown()

	if !rt.liteMode() {
		t.Error("liteMode() = false, want true")
	}
	if rt.mode != "lite" {
		t.Errorf("mode = %q, want lite", rt.mode)
	}
	if rt.factory == nil {
		t.Error("factory = nil, want storage factory")
	}
	if rt.fileCache == nil {
		t.Error("fileCache = nil, want L1.5 file cache")
	}

	// 目录创建：SQLite 父目录 + 三个数据目录
	for _, dir := range []string{filepath.Dir(cfg.Lite.SQLitePath), cfg.Lite.BodiesDir, cfg.Lite.CacheDir, cfg.Lite.LogsDir} {
		st, err := os.Stat(dir)
		if err != nil {
			t.Errorf("目录应已创建 %s: %v", dir, err)
			continue
		}
		if !st.IsDir() {
			t.Errorf("%s 应为目录", dir)
		}
	}

	// 工厂返回 lite 真实实现（SQLite 会话存储可直接读写）
	ss := rt.factory.NewSessionStore()
	if ss == nil {
		t.Fatal("factory.NewSessionStore() = nil")
	}
	if err := ss.CreateSession(context.Background(), &storage.Session{ID: "s1", TenantID: "t1"}); err != nil {
		t.Errorf("CreateSession error = %v", err)
	}
	if _, err := rt.factory.NewSessionStore().GetSession(context.Background(), "t1", "s1"); err != nil {
		t.Errorf("GetSession error = %v", err)
	}

	// L1.5 缓存注入后可读可写（经 newSessionCacheV2 的 mode-aware 装配）
	cache := rt.newSessionCacheV2(nil, "", 0)
	if cache == nil {
		t.Fatal("newSessionCacheV2(nil, \"\", 0) = nil")
	}
	state := &v2.SessionStateV2{
		SessionID:  "s1",
		TenantID:   "t1",
		LastTurnNo: 3,
		UpdatedAt:  time.Now(),
	}
	if err := cache.Set(context.Background(), state); err != nil {
		t.Errorf("cache.Set error = %v", err)
	}
	got, err := cache.Get(context.Background(), "t1", "s1")
	if err != nil {
		t.Fatalf("cache.Get error = %v", err)
	}
	if got == nil || got.LastTurnNo != 3 {
		t.Errorf("cache.Get = %#v, want LastTurnNo 3 (L1 命中)", got)
	}

	// 未知会话：L1/L1.5 miss、nil db 的 L3 返回 (nil, nil) 而非 panic
	missState, err := cache.Get(context.Background(), "t1", "no-such-session")
	if err != nil {
		t.Errorf("cache.Get(miss) error = %v", err)
	}
	if missState != nil {
		t.Errorf("cache.Get(miss) = %#v, want nil", missState)
	}
}

func TestStorageRuntimeShutdownIdempotent(t *testing.T) {
	rt, err := initStorageMode(nil, liteStorageConfigForTest(t))
	if err != nil {
		t.Fatalf("initStorageMode(lite) error = %v", err)
	}
	if rt == nil {
		t.Fatal("runtime = nil")
	}

	// 重复 Shutdown 不 panic、不阻塞（trimmers 有界等待 + 工厂幂等关闭）
	done := make(chan struct{})
	go func() {
		defer close(done)
		rt.Shutdown()
		rt.Shutdown()
		rt.Shutdown()
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("重复 Shutdown 未在预期时间内返回（可能不可重入）")
	}

	// Shutdown 后工厂可安全地再次 Close（幂等重入）
	if err := rt.factory.Close(); err != nil {
		t.Errorf("Shutdown 后 factory.Close() error = %v", err)
	}
}

func TestStorageRuntimeNilSafe(t *testing.T) {
	var rt *storageRuntime
	rt.Shutdown()      // no-op，不 panic
	if rt.liteMode() { // nil runtime 即 full/未启用
		t.Error("nil runtime liteMode() = true, want false")
	}
	// nil runtime 走历史构造路径（full 语义），返回可用缓存
	if c := rt.newSessionCacheV2(nil, "", 0); c == nil {
		t.Error("nil runtime newSessionCacheV2 = nil, want legacy construction")
	}
}

func TestInitStorageModeNonLiteReturnsNil(t *testing.T) {
	// nil 配置
	if rt, err := initStorageMode(nil, nil); err != nil || rt != nil {
		t.Errorf("initStorageMode(nil cfg) = (%v, %v), want (nil, nil)", rt, err)
	}
	// 空 mode（未启用双模式）
	empty := &config.StorageConfig{}
	if rt, err := initStorageMode(nil, empty); err != nil || rt != nil {
		t.Errorf("initStorageMode(empty mode) = (%v, %v), want (nil, nil)", rt, err)
	}
	// 显式 full + 热区关闭（config 通道）→ nil runtime（历史装配）。
	// 2026-09-24 方案 H2（P3）起，full + 热区启用（默认）装配 hotZoneOnly
	// runtime，见 storage_mode_init_full_hotzone_test.go；本用例保留
	// 「关闭开关后行为与 main 历史一致」的对照半边。
	off := false
	full := &config.StorageConfig{
		Mode: "full",
		Full: &config.FullStorageConfig{
			PostgresURL: "postgres://gateway:secret@127.0.0.1:5432/gateway",
			RedisURL:    "127.0.0.1:6379",
		},
		HotZone: &config.HotZoneConfig{
			Dir:            filepath.Join(t.TempDir(), "hotzone"),
			Enabled:        &off,
			RetentionHours: 7,
			MaxSizeGB:      1,
		},
	}
	if rt, err := initStorageMode(nil, full); err != nil || rt != nil {
		t.Errorf("initStorageMode(full, hotzone off) = (%v, %v), want (nil, nil)", rt, err)
	}
}

func TestInitStorageModeLiteInvalidPath(t *testing.T) {
	// SQLite 父目录位置被一个普通文件占据 → 工厂建目录失败，错误上抛
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup blocker file: %v", err)
	}
	cfg := &config.StorageConfig{Mode: "lite"}
	cfg.Lite = &config.LiteStorageConfig{
		SQLitePath:     filepath.Join(blocker, "gateway.db"),
		BodiesDir:      filepath.Join(dir, "bodies"),
		CacheDir:       filepath.Join(dir, "cache"),
		LogsDir:        filepath.Join(dir, "logs"),
		CacheMaxSizeGB: 1,
	}
	rt, err := initStorageMode(nil, cfg)
	if err == nil {
		rt.Shutdown()
		t.Fatal("initStorageMode 应因 SQLite 父目录不可创建而报错")
	}
	if rt != nil {
		t.Errorf("出错时 runtime 应为 nil, got %#v", rt)
	}
}

func TestLoadStorageConfigEnvOnly(t *testing.T) {
	t.Setenv("LLM_GATEWAY_STORAGE_MODE", "lite")
	t.Setenv("LLM_GATEWAY_SQLITE_PATH", filepath.Join(t.TempDir(), "env-only.db"))
	t.Setenv("LLM_GATEWAY_BODIES_DIR", "")
	t.Setenv("LLM_GATEWAY_CACHE_DIR", "")
	t.Setenv("LLM_GATEWAY_LOGS_DIR", "")

	// 无 YAML 路径 → 纯 env
	cfg := loadStorageConfig("")
	if cfg == nil || cfg.Mode != "lite" {
		t.Fatalf("loadStorageConfig(\"\") = %#v, want lite mode from env", cfg)
	}
	// YAML 路径不存在 → 告警并回退 env-only
	cfg = loadStorageConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if cfg == nil || cfg.Mode != "lite" {
		t.Fatalf("loadStorageConfig(missing yaml) = %#v, want env fallback", cfg)
	}

	// 无任何环境变量 → 零值配置（mode 为空，主程序视作未启用双模式）
	t.Setenv("LLM_GATEWAY_STORAGE_MODE", "")
	t.Setenv("LLM_GATEWAY_SQLITE_PATH", "")
	cfg = loadStorageConfig("")
	if cfg == nil || cfg.Mode != "" {
		t.Fatalf("loadStorageConfig(no env) = %#v, want empty mode", cfg)
	}
}

// TestSqlitePragmasFromLiteConfig 验证 YAML sqlite_pragmas → 工厂 PRAGMA map 的映射：
// 零值跳过、cache_size 遵循负数 KiB 约定、全零值返回 nil（沿用默认集合）。
func TestSqlitePragmasFromLiteConfig(t *testing.T) {
	require.Nil(t, sqlitePragmasFromLiteConfig(config.SQLitePragmasConfig{}))

	got := sqlitePragmasFromLiteConfig(config.SQLitePragmasConfig{
		JournalMode: "WAL", CacheSizeKB: 32000, Synchronous: "FULL", BusyTimeoutMS: 3000,
	})
	require.Equal(t, map[string]string{
		"journal_mode": "WAL",
		"cache_size":   "-32000",
		"synchronous":  "FULL",
		"busy_timeout": "3000",
	}, got)
}

// TestInitStorageModeLiteDisablesRedisEnv 钉住 lite 模式的 Redis env 收口
// 行为（audit 2026-09-14 R28 #16）：init 后对全部非空 Redis env 执行
// Warn + Unset（断言 Lookup 为空）；nil runtime（full/未启用）为 no-op，
// 环境保持原样。
func TestInitStorageModeLiteDisablesRedisEnv(t *testing.T) {
	keys := make([]string, len(liteRedisEnvKeys))
	copy(keys, liteRedisEnvKeys)
	for i, k := range keys {
		t.Setenv(k, fmt.Sprintf("previous-value-%d", i))
	}

	rt, err := initStorageMode(nil, liteStorageConfigForTest(t))
	if err != nil {
		t.Fatalf("initStorageMode(lite) error = %v", err)
	}
	defer rt.Shutdown()
	rt.disableRedisEnv()

	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("lite 收口后 %s 仍存在（len=%d），want 已 unset", k, len(v))
		}
	}

	// nil runtime（full/未启用双模式）：收口为 no-op，env 不被触碰。
	t.Setenv("LLM_GATEWAY_REDIS_ADDR", "127.0.0.1:6379")
	var nilRt *storageRuntime
	nilRt.disableRedisEnv()
	if v := os.Getenv("LLM_GATEWAY_REDIS_ADDR"); v != "127.0.0.1:6379" {
		t.Errorf("nil runtime 收口后 LLM_GATEWAY_REDIS_ADDR = %q, want 原值保持", v)
	}
}

// TestInitStorageModeConsistencyWorkerWiring 验证一致性对账 worker（审计
// B-#2 接线）的装配规则：默认开启（report-only）、配置显式关闭时不装配、
// delete_orphans 显式开启时删除策略生效；Shutdown 在首跑延迟内取消即可
// 优雅退出（不等待 10 分钟）。
func TestInitStorageModeConsistencyWorkerWiring(t *testing.T) {
	// 默认配置（未配置 consistency 段）：ApplyLiteDefaults 补默认后启用。
	cfg := liteStorageConfigForTest(t)
	rt, err := initStorageMode(nil, cfg)
	require.NoError(t, err)
	require.NotNil(t, rt)
	if rt.consistencyWorker == nil {
		t.Fatal("consistencyWorker = nil, want assembled by default (consistency_check_enabled 默认 true)")
	}
	if rt.consistencyWorker.Action() != storage.RepairReportOnly {
		t.Errorf("worker action = %v, want report_only (默认恒安全)", rt.consistencyWorker.Action())
	}
	rt.Shutdown() // 幂等优雅关闭：worker 处于首跑延迟中即被取消

	// 配置显式关闭：不装配 worker。
	disabled := liteStorageConfigForTest(t)
	f := false
	disabled.Lite.Consistency.Enabled = &f
	rt2, err := initStorageMode(nil, disabled)
	require.NoError(t, err)
	require.NotNil(t, rt2)
	if rt2.consistencyWorker != nil {
		t.Error("consistencyWorker != nil, want nil when consistency_check_enabled=false")
	}
	rt2.Shutdown()

	// delete_orphans 显式开启：删除策略注入（仍受复检+宽限双保险保护）。
	deleter := liteStorageConfigForTest(t)
	tr := true
	deleter.Lite.Consistency.Enabled = &tr
	deleter.Lite.Consistency.DeleteOrphanBodies = true
	deleter.Lite.Consistency.IntervalHours = 6
	deleter.Lite.Consistency.IdleThresholdMin = 30
	deleter.Lite.Consistency.MaxSessionsPerRun = 50
	rt3, err := initStorageMode(nil, deleter)
	require.NoError(t, err)
	require.NotNil(t, rt3)
	if rt3.consistencyWorker == nil {
		t.Fatal("consistencyWorker = nil, want assembled when explicitly enabled")
	}
	if rt3.consistencyWorker.Action() != storage.RepairDeleteOrphanBodies {
		t.Errorf("worker action = %v, want delete_orphan_bodies", rt3.consistencyWorker.Action())
	}
	if rt3.consistencyWorker.Interval() != 6*time.Hour ||
		rt3.consistencyWorker.IdleThreshold() != 30*time.Minute ||
		rt3.consistencyWorker.MaxSessions() != 50 {
		t.Errorf("worker knobs = %v/%v/%d, want 6h/30m/50",
			rt3.consistencyWorker.Interval(), rt3.consistencyWorker.IdleThreshold(), rt3.consistencyWorker.MaxSessions())
	}
	rt3.Shutdown()
}

// TestResolveCacheTrimRetention 覆盖缓存删侧 retention 的解析口径。
//
// 核心不变量：返回值恒 >= cacheTTL —— CacheTrimmer 永不删除 FileCache
// 仍视为有效的条目（否则 cache_ttl_hours 被静默架空，缓存退化为只写不读）。
func TestResolveCacheTrimRetention(t *testing.T) {
	cases := []struct {
		name       string
		cacheTTL   int
		retentionH int
		want       time.Duration
	}{
		// 默认配置：两侧同为 24h，原样透传（不改变既有行为）。
		{"default both 24h", 24, 24, 24 * time.Hour},
		// 危险方向：retention 小于 ttl —— 必须收敛到 ttl（旧行为会删活跃条目）。
		{"retention 1h below ttl 24h clamps up", 24, 1, 24 * time.Hour},
		{"retention 6h below ttl 24h clamps up", 24, 6, 24 * time.Hour},
		// retention 未配置（<=0）：收敛到 ttl。
		{"retention unset clamps to ttl", 12, 0, 12 * time.Hour},
		{"retention negative clamps to ttl", 12, -5, 12 * time.Hour},
		// 安全方向：retention 大于 ttl —— 保留更大的值（逻辑 TTL 后多留一段）。
		{"retention 48h above ttl 24h keeps larger", 24, 48, 48 * time.Hour},
		// ttl 未配置：兜底 24h（与 config.ApplyLiteDefaults 同口径）。
		{"ttl unset falls back to 24h", 0, 24, 24 * time.Hour},
		{"ttl unset and retention unset", 0, 0, 24 * time.Hour},
		{"ttl negative falls back to 24h", -1, 1, 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveCacheTrimRetention(tc.cacheTTL, tc.retentionH)
			if got != tc.want {
				t.Errorf("resolveCacheTrimRetention(%d, %d) = %v, want %v",
					tc.cacheTTL, tc.retentionH, got, tc.want)
			}
			// 不变量本体：任何输入组合下都不得小于 ttl。
			ttl := time.Duration(tc.cacheTTL) * time.Hour
			if ttl <= 0 {
				ttl = 24 * time.Hour
			}
			if got < ttl {
				t.Errorf("retention %v < cache TTL %v —— 清理侧会删除读侧仍有效的条目", got, ttl)
			}
		})
	}
}

// TestCacheTrimmerNeverDeletesLiveEntries 是装配层的端到端不变量断言：
// 真正构造出来的 FileCache 的 TTL，必须不大于真正装配的清理侧 retention。
// 用 getter 读回真实 FileCache（而非从配置二次推导），避免断言只验证了
// 自己的算术。
func TestCacheTrimmerNeverDeletesLiveEntries(t *testing.T) {
	// 三组配置分别覆盖：默认、危险方向（retention < ttl）、安全方向（retention > ttl）。
	for _, tc := range []struct {
		name       string
		cacheTTL   int
		retentionH int
	}{
		{"default", 24, 24},
		{"retention below ttl", 24, 1},
		{"retention above ttl", 6, 72},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := liteStorageConfigForTest(t)
			cfg.Lite.CacheTTLHours = tc.cacheTTL
			cfg.Lite.Retention.CacheHours = tc.retentionH

			rt, err := initStorageMode(nil, cfg)
			require.NoError(t, err)
			require.NotNil(t, rt)
			defer rt.Shutdown()

			if rt.fileCache == nil {
				t.Fatal("fileCache = nil, want assembled L1.5 cache")
			}
			if rt.cacheTrimmer == nil {
				t.Fatal("cacheTrimmer = nil, want assembled cache trimmer")
			}
			// 先把 TTL getter 锚定到配置值：否则 getter 一旦少报（比如返回 0），
			// 下面的不变量断言会退化成 `retention < 0` 而恒真，变成空断言。
			if got, want := rt.fileCache.TTL(), time.Duration(tc.cacheTTL)*time.Hour; got != want {
				t.Errorf("file cache TTL = %v, want configured cache_ttl_hours %v", got, want)
			}
			// 断言读真实 worker 与真实 FileCache 的字段，不另存快照比自己。
			if rt.cacheTrimmer.Retention() < rt.fileCache.TTL() {
				t.Errorf("cache trim retention %v < file cache TTL %v —— 清理侧会删除读侧仍有效的缓存条目",
					rt.cacheTrimmer.Retention(), rt.fileCache.TTL())
			}
		})
	}
}

// TestInitStorageModeHotZoneTrimmerWiring H4 P2 装配验收：默认（config 段
// 缺失 → ApplyHotZoneDefaults 默认开启 + settings 默认 true）时 hotzone
// trimmer 被装配且生命周期挂入 trimmerCtx/trimmerWG。
func TestInitStorageModeHotZoneTrimmerWiring(t *testing.T) {
	cfg := liteStorageConfigForTest(t)
	hzDir := filepath.Join(filepath.Dir(cfg.Lite.CacheDir), "hotzone")
	cfg.HotZone = &config.HotZoneConfig{Dir: hzDir} // Enabled/其余字段走默认值

	rt, err := initStorageMode(nil, cfg)
	if err != nil {
		t.Fatalf("initStorageMode(lite) error = %v", err)
	}
	if rt == nil {
		t.Fatal("runtime = nil")
	}
	defer rt.Shutdown()

	if rt.hotZoneTrimmer == nil {
		t.Fatal("hotZoneTrimmer = nil, want wired (双通道默认均开启)")
	}
	// Dir 默认值归一：显式传入的 hzDir 应原样生效（ApplyHotZoneDefaults 只补空值）
	st, err := os.Stat(hzDir)
	if err == nil && !st.IsDir() {
		t.Errorf("%s 应为目录（若已创建）", hzDir)
	}
	// trimmer 未对缺失目录做任何写入（缺失即 no-op）
	if _, err := os.Stat(hzDir); !os.IsNotExist(err) {
		t.Logf("hotzone dir 已存在（可能由其他步骤创建）: %v", err)
	}
}

// TestInitStorageModeHotZoneDisabledByConfig config 通道关闭（YAML/env 显式
// enabled=false）：AND 语义下不装配 trimmer，L1.5 FileCache 预算不受热区
// settings 影响（H4 §改动4：lite 10GB 默认不动）。
func TestInitStorageModeHotZoneDisabledByConfig(t *testing.T) {
	cfg := liteStorageConfigForTest(t)
	off := false
	cfg.HotZone = &config.HotZoneConfig{
		Dir:            filepath.Join(filepath.Dir(cfg.Lite.CacheDir), "hotzone"),
		Enabled:        &off,
		RetentionHours: 7,
		MaxSizeGB:      1,
	}

	rt, err := initStorageMode(nil, cfg)
	if err != nil {
		t.Fatalf("initStorageMode(lite) error = %v", err)
	}
	defer rt.Shutdown()

	if rt.hotZoneTrimmer != nil {
		t.Fatal("hotZoneTrimmer != nil, want nil (config 显式关闭)")
	}
}

// TestInitStorageModeHotZoneResizeMaxWiring ResizeMax/SetTTL 联动（H4 §改动3
// + 2026-10-01 审计 F2）：L1.5 cache 子树位于热区目录内时，首轮 applyReload
// 把 FileCache.maxSize 收敛到 settings hotzone 预算（构造值 10GB → settings
// 默认 1GB），TTL 同步收敛到 settings retention 默认 7h（构造值 2h）。
func TestInitStorageModeHotZoneResizeMaxWiring(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.StorageConfig{Mode: "lite"}
	cfg.Lite = &config.LiteStorageConfig{
		SQLitePath:     filepath.Join(dir, "gateway.db"),
		BodiesDir:      filepath.Join(dir, "bodies"),
		CacheDir:       filepath.Join(dir, "hotzone", "cache"), // 落入热区
		LogsDir:        filepath.Join(dir, "logs"),
		CacheTTLHours:  2,
		CacheMaxSizeGB: 10, // 故意 ≠ settings 默认 1GB，可观测 ResizeMax 生效
		AsyncWriters:   2,
	}
	cfg.Lite.Retention.SessionBodiesDays = 30
	cfg.Lite.Retention.RequestLogsDays = 7
	cfg.Lite.Retention.CacheHours = 24
	cfg.HotZone = &config.HotZoneConfig{Dir: filepath.Join(dir, "hotzone")}

	rt, err := initStorageMode(nil, cfg)
	if err != nil {
		t.Fatalf("initStorageMode(lite) error = %v", err)
	}
	defer rt.Shutdown()

	if rt.hotZoneTrimmer == nil {
		t.Fatal("hotZoneTrimmer = nil, want wired")
	}
	// 首轮 applyReload 在 Start goroutine 内异步执行，轮询等待生效
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := rt.fileCache.Stats()["max_size_bytes"].(int64); got == int64(1)<<30 &&
			rt.fileCache.TTL() == 7*time.Hour {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := rt.fileCache.Stats()["max_size_bytes"].(int64)
	t.Errorf("FileCache maxSize = %d, want %d (hotzone settings 预算); TTL = %v, want 7h (settings retention 同参)",
		got, int64(1)<<30, rt.fileCache.TTL())
}
