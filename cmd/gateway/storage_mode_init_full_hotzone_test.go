// Package main — storage_mode_init_full_hotzone_test.go
//
// 2026-09-24 方案 H2（P3）full 模式热区装配单测：
//   - full + 热区启用（config 默认）→ hotZoneOnly runtime：目录创建、组件
//     装配、L1.5 读链经 newSessionCacheV2 注入且保留历史构造的对照；
//   - config 通道关闭（enabled=false / env 一键关闭）→ nil runtime + 不落盘
//     （关闭开关后行为与 main 历史一致的对照验收）；
//   - 热区根目录为 symlink / 普通文件 → 告警降级历史装配（不拒绝启动）；
//   - Redis env 不收口（full 的 Redis 是 L2，必须保留）；
//   - Shutdown 幂等（bodiesStore 无 factory 路径）；
//   - settings_kv 不参与装配判定（storage.hotzone_enabled 运行期 false 语义
//     = 停清不卸载，装配门仅 config 通道）。
//
// 全部使用 t.TempDir()，不触碰仓库相对路径，也不依赖外部环境。
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/config"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// fullHotZoneConfigForTest 基于 t.TempDir() 构造显式 full 存储配置；Full 段
// 仅作占位（装配点不消费 full_storage，main 的 Validate 在更晚处且 non-fatal）。
func fullHotZoneConfigForTest(t *testing.T) *config.StorageConfig {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.StorageConfig{Mode: "full"}
	cfg.Full = &config.FullStorageConfig{
		PostgresURL: "postgres://gateway:secret@127.0.0.1:5432/gateway",
		RedisURL:    "127.0.0.1:6379",
	}
	cfg.HotZone = &config.HotZoneConfig{
		Dir:            filepath.Join(dir, "hotzone"),
		RetentionHours: 7,
		MaxSizeGB:      1,
		// Enabled / RequestMirror 走 nil → 默认 true（默认开启即行为变更点本身）
	}
	return cfg
}

// pinHotZoneSettingsEnv 钉空热区三旋钮 env，防止同包其他测试注册的 storage
// specs 让 GetPlatform* 读到宿主环境造成跨测试串扰。
func pinHotZoneSettingsEnv(t *testing.T) {
	t.Helper()
	t.Setenv("LLM_GATEWAY_HOTZONE_ENABLED", "")
	t.Setenv("LLM_GATEWAY_HOTZONE_DIR", "")
	t.Setenv("LLM_GATEWAY_HOTZONE_RETENTION_HOURS", "")
	t.Setenv("LLM_GATEWAY_HOTZONE_MAX_SIZE_GB", "")
}

// TestInitStorageModeFullHotZoneAssembly P3 主验收：full + 热区启用 →
// hotZoneOnly runtime。逐项断言：
//  1. 组件形态：fileCache/bodiesStore/hotZoneTrimmer 非 nil；factory/
//     consistencyWorker/cacheTrimmer（lite 专属）恒 nil；
//  2. 目录：full 启动后 data/hotzone 根 + cache 子树在盘上出现
//     （session_bodies 由首写 MkdirAll，不要求启动期存在）；
//  3. 旋钮：FileCache TTL == 热区保留期、预算 == MaxSizeGB；
//  4. 读链：rt.fileCache 直接种入状态后，newSessionCacheV2(nil,"",0) 构造的
//     缓存（L1 为空）Get 命中——证明 L1.5 真实进入 full 读链而非仅被持有；
//  5. 语义门：liteMode()==false（main 侧 lite 门不得误判）。
func TestInitStorageModeFullHotZoneAssembly(t *testing.T) {
	pinHotZoneSettingsEnv(t)
	cfg := fullHotZoneConfigForTest(t)

	rt, err := initStorageMode(nil, cfg)
	require.NoError(t, err)
	require.NotNil(t, rt)
	defer rt.Shutdown()

	require.True(t, rt.hotZoneOnly)
	require.Equal(t, "full", rt.mode)
	require.False(t, rt.liteMode(), "hotZoneOnly runtime 不得被 liteMode() 误判为 lite")
	require.Nil(t, rt.factory, "full 热区装配不建 SQLite 工厂")
	require.Nil(t, rt.consistencyWorker, "full 热区装配不启 lite 一致性 worker")
	require.Nil(t, rt.cacheTrimmer, "full 热区装配不启 lite cache trimmer（热区 trimmer 覆盖）")
	require.NotNil(t, rt.fileCache)
	require.NotNil(t, rt.bodiesStore)
	require.NotNil(t, rt.hotZoneTrimmer)

	// 目录：热区根 + cache 子树（NewFileCache 构造期 MkdirAll）。
	st, err := os.Stat(cfg.HotZone.Dir)
	require.NoError(t, err, "full 启动后热区根目录应存在")
	require.True(t, st.IsDir())
	st, err = os.Stat(rt.cacheDir)
	require.NoError(t, err, "full 启动后 cache 子树应存在")
	require.True(t, st.IsDir())
	require.Equal(t, filepath.Join(cfg.HotZone.Dir, "cache"), rt.cacheDir)
	require.Equal(t, filepath.Join(cfg.HotZone.Dir, "session_bodies"), rt.bodiesDir)

	// 旋钮：TTL 对齐热区保留期；预算来自 MaxSizeGB。
	require.Equal(t, 7*time.Hour, rt.fileCache.TTL())
	require.Equal(t, int64(1)<<30, rt.fileCache.Stats()["max_size_bytes"].(int64))

	// 读链：L1 空 → L1.5 文件命中（full+fileCache 共享 L1.5 的直接证据）。
	cache := rt.newSessionCacheV2(nil, "", 0)
	require.NotNil(t, cache)
	seed := &v2.SessionStateV2{SessionID: "s-hz", TenantID: "t-hz", LastTurnNo: 9, UpdatedAt: time.Now()}
	require.NoError(t, rt.fileCache.Set(seed))
	got, err := cache.Get(context.Background(), "t-hz", "s-hz")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, 9, got.LastTurnNo, "Get 应回源 L1.5 文件层（L1 为空、L3 无 db）")

	// 未知会话：L1/L1.5 miss、nil db 的 L3 返回 (nil, nil) 而非 panic（与 lite 同契约）。
	miss, err := cache.Get(context.Background(), "t-hz", "no-such")
	require.NoError(t, err)
	require.Nil(t, miss)
}

// TestInitStorageModeFullHotZoneDisabledMatchesLegacy 对照验收：关闭开关后
// 行为与 main 历史完全一致——nil runtime、不创建任何热区目录、走历史构造。
func TestInitStorageModeFullHotZoneDisabledMatchesLegacy(t *testing.T) {
	pinHotZoneSettingsEnv(t)
	cfg := fullHotZoneConfigForTest(t)
	off := false
	cfg.HotZone.Enabled = &off

	rt, err := initStorageMode(nil, cfg)
	require.NoError(t, err)
	require.Nil(t, rt, "热区关闭时 full 必须返回 nil runtime（历史装配）")
	_, err = os.Stat(cfg.HotZone.Dir)
	require.True(t, os.IsNotExist(err), "热区关闭时不得创建热区目录")

	// nil runtime 的历史行为不受影响（对照 main）。
	require.False(t, rt.liteMode())
	require.NotNil(t, rt.newSessionCacheV2(nil, "", 0))
	rt.disableRedisEnv() // no-op
	rt.Shutdown()        // no-op

	// mode 未设置 / 非法：同样 nil（未启用双模式，行为零变化）。
	for _, mode := range []string{"", "weird"} {
		empty := &config.StorageConfig{Mode: mode}
		rt, err := initStorageMode(nil, empty)
		require.NoError(t, err)
		require.Nil(t, rt, "mode=%q 应保持历史 nil runtime", mode)
	}
}

// TestInitStorageModeFullHotZoneEnvKillSwitch 一键关闭的环境通道端到端：
// LLM_GATEWAY_HOTZONE_ENABLED=false 经 config env → 装配门 → nil runtime；
// 未设置时 full 默认开启（行为变更点本身），且 Dir env 指定的目录生效。
func TestInitStorageModeFullHotZoneEnvKillSwitch(t *testing.T) {
	t.Run("env off kills assembly", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("LLM_GATEWAY_STORAGE_MODE", "full")
		t.Setenv("LLM_GATEWAY_HOTZONE_ENABLED", "false")
		t.Setenv("LLM_GATEWAY_HOTZONE_DIR", filepath.Join(dir, "hotzone"))
		t.Setenv("LLM_GATEWAY_HOTZONE_RETENTION_HOURS", "")
		t.Setenv("LLM_GATEWAY_HOTZONE_MAX_SIZE_GB", "")

		rt, err := initStorageMode(nil, config.LoadStorageConfigFromEnv())
		require.NoError(t, err)
		require.Nil(t, rt)
		_, err = os.Stat(filepath.Join(dir, "hotzone"))
		require.True(t, os.IsNotExist(err), "一键关闭后不得创建热区目录")
	})

	t.Run("env unset defaults on and honors dir env", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("LLM_GATEWAY_STORAGE_MODE", "full")
		t.Setenv("LLM_GATEWAY_HOTZONE_ENABLED", "")
		t.Setenv("LLM_GATEWAY_HOTZONE_DIR", filepath.Join(dir, "hotzone"))
		t.Setenv("LLM_GATEWAY_HOTZONE_RETENTION_HOURS", "")
		t.Setenv("LLM_GATEWAY_HOTZONE_MAX_SIZE_GB", "")

		rt, err := initStorageMode(nil, config.LoadStorageConfigFromEnv())
		require.NoError(t, err)
		require.NotNil(t, rt, "full + 未显式关闭：热区默认开启（H2 行为变更点）")
		defer rt.Shutdown()
		require.True(t, rt.hotZoneOnly)
		st, err := os.Stat(filepath.Join(dir, "hotzone", "cache"))
		require.NoError(t, err, "HotZone.Dir env 指定的目录应被消费")
		require.True(t, st.IsDir())
	})
}

// TestInitStorageModeFullHotZoneRootNotDir 热区根目录形态防护：已存在且为
// symlink / 普通文件 → 告警并降级历史装配（nil runtime），不拒绝启动
// （升级路径上已有目录形态的部署不被卡死）。
func TestInitStorageModeFullHotZoneRootNotDir(t *testing.T) {
	pinHotZoneSettingsEnv(t)
	t.Run("symlink root degrades to legacy", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "elsewhere")
		require.NoError(t, os.MkdirAll(real, 0o755))
		cfg := fullHotZoneConfigForTest(t)
		cfg.HotZone.Dir = filepath.Join(dir, "hotzone")
		require.NoError(t, os.Symlink(real, cfg.HotZone.Dir))

		rt, err := initStorageMode(nil, cfg)
		require.NoError(t, err)
		require.Nil(t, rt, "symlink 根目录应降级为历史装配")
	})

	t.Run("regular file root degrades to legacy", func(t *testing.T) {
		dir := t.TempDir()
		cfg := fullHotZoneConfigForTest(t)
		cfg.HotZone.Dir = filepath.Join(dir, "hotzone")
		require.NoError(t, os.WriteFile(cfg.HotZone.Dir, []byte("x"), 0o644))

		rt, err := initStorageMode(nil, cfg)
		require.NoError(t, err)
		require.Nil(t, rt, "普通文件根目录应降级为历史装配")
	})
}

// TestInitStorageModeFullHotZoneKeepsRedisEnv full 热区装配不收口 Redis env
// （对照 lite 的 Warn+Unset）：Redis 是 full 的 L2 治理缓存，env 必须原样保留。
func TestInitStorageModeFullHotZoneKeepsRedisEnv(t *testing.T) {
	pinHotZoneSettingsEnv(t)
	t.Setenv("LLM_GATEWAY_REDIS_ADDR", "127.0.0.1:6379")
	t.Setenv("REDIS_URL", "redis://127.0.0.1:6379/0")

	rt, err := initStorageMode(nil, fullHotZoneConfigForTest(t))
	require.NoError(t, err)
	require.NotNil(t, rt)
	defer rt.Shutdown()

	rt.disableRedisEnv()
	require.Equal(t, "127.0.0.1:6379", os.Getenv("LLM_GATEWAY_REDIS_ADDR"))
	require.Equal(t, "redis://127.0.0.1:6379/0", os.Getenv("REDIS_URL"))
}

// TestInitStorageModeFullHotZoneShutdownIdempotent Shutdown 幂等可重入：
// bodiesStore（无 factory）路径同样安全排空。
func TestInitStorageModeFullHotZoneShutdownIdempotent(t *testing.T) {
	pinHotZoneSettingsEnv(t)
	rt, err := initStorageMode(nil, fullHotZoneConfigForTest(t))
	require.NoError(t, err)
	require.NotNil(t, rt)

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
}

// TestInitStorageModeFullHotZoneSettingsKvNotAssemblyGate 钉死运行期开关语义
// 的装配侧不变量：settings_kv（fake 平台后端）置 storage.hotzone_enabled=false
// 时 full 装配**照常进行**——装配点在 PG 之前读不到 settings_kv，其运行期
// 效果 = HotZoneTrimmer 每 tick 停清（P2 语义），不卸载已装配组件；下一次
// 进程启动是否装配仅由 config 通道决定。fake backend 复用 stop-write-gate
// 测试的替换手法（settings.Global 可换 + t.Cleanup 恢复）。
func TestInitStorageModeFullHotZoneSettingsKvNotAssemblyGate(t *testing.T) {
	pinHotZoneSettingsEnv(t)
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &hotZoneFakeKV{enabled: "false"})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	for _, spec := range settings.StorageSpecs() {
		require.NoError(t, registry.RegisterSpec(spec))
	}
	settings.Global = registry

	rt, err := initStorageMode(nil, fullHotZoneConfigForTest(t))
	require.NoError(t, err)
	require.NotNil(t, rt, "settings_kv=false 不阻断装配（装配点无 settings 后端；运行期语义=停清不卸载）")
	rt.Shutdown()

	// 同 registry 下 lite 分支的对照：AND 门读 settings → 不装配 trimmer。
	// （settings 通道在 lite 是活的：nil DB 后端仍注册了 spec，直查 fake。）
	liteCfg := liteStorageConfigForTest(t)
	liteCfg.HotZone = &config.HotZoneConfig{Dir: filepath.Join(t.TempDir(), "hotzone")}
	liteRt, err := initStorageMode(nil, liteCfg)
	require.NoError(t, err)
	require.NotNil(t, liteRt)
	defer liteRt.Shutdown()
	require.Nil(t, liteRt.hotZoneTrimmer, "lite AND 门：settings=false 时不装配 trimmer")
}

// hotZoneFakeKV 是只读的 settings_kv 桩：platform scope 返回固定值。
type hotZoneFakeKV struct{ enabled string }

func (f *hotZoneFakeKV) Get(scope settings.Scope, key string) ([]byte, error) {
	if scope == settings.ScopePlatform && key == "storage.hotzone_enabled" {
		return []byte(f.enabled), nil
	}
	return nil, nil
}

func (f *hotZoneFakeKV) Set(scope settings.Scope, key string, value any) ([]byte, error) {
	return nil, nil
}

func (f *hotZoneFakeKV) GetTenant(tenantID, key string) ([]byte, error) { return nil, nil }

func (f *hotZoneFakeKV) SetTenant(tenantID, key string, value any) ([]byte, error) {
	return nil, nil
}
