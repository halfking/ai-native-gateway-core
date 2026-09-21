// Package v2: SessionCacheV2 多层缓存集成测试（Task 3.1，L1 → L1.5 → L2 → L3）
//
// 覆盖（不需要真实 PG/Redis：L3 用 pgxmock，L2 用 disabled RedisGovernanceCache，
// L1.5 用临时目录 FileCache）：
//   - lite 模式：Set 注入 → L1 与 L1.5 同步有数据；清空 L1 / 新建等价实例后
//     Get 仍从 L1.5 命中并回填 L1；L3 命中后回填 L1 与 L1.5；Invalidate 后
//     三层都拿不到；构造时 l2 被摘除。
//   - full 模式（含零值 mode）：不触碰 L1.5（nil 时不出错），行为与历史
//     NewSessionCacheV2 等价；L3 命中不报错。
//
// 运行：go test -race -run 'Cache' ./domains/session/v2/
package v2

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/storage"
)

// newIntegrationFileCache 在临时目录创建 L1.5 FileCache，测试结束自动清理。
func newIntegrationFileCache(t *testing.T) *FileCache {
	t.Helper()
	fc, err := NewFileCache(t.TempDir(), time.Minute, 1<<20)
	require.NoError(t, err)
	return fc
}

// newIntegrationState 构造测试用 SessionStateV2。
func newIntegrationState(tenantID, sessionID string, turn int) *SessionStateV2 {
	return &SessionStateV2{
		SessionID:  sessionID,
		TenantID:   tenantID,
		LastTurnNo: turn,
		UpdatedAt:  time.Unix(1700000000, 0).UTC(),
		CompressionMeta: CompressionMeta{
			Strategy:      "truncate",
			TokenEstimate: 100 * turn,
			MsgCount:      turn,
		},
		GovernanceMeta: GovernanceMeta{
			LastInjectionVerdict: "pass",
			LastOutputVerdict:    "skip",
		},
	}
}

// ── 模式归一化 ───────────────────────────────────────────────────────────

// TestSessionCacheV2_EffectiveMode 锁定零值/未知模式归一化为 full，lite 保持 lite。
func TestSessionCacheV2_EffectiveMode(t *testing.T) {
	assert.Equal(t, storage.StorageModeFull, (&SessionCacheV2{}).effectiveMode(), "零值必须等价 full")
	assert.Equal(t, storage.StorageModeFull,
		(&SessionCacheV2{mode: storage.StorageMode("bogus")}).effectiveMode(), "未知值必须等价 full")
	assert.Equal(t, storage.StorageModeLite,
		(&SessionCacheV2{mode: storage.StorageModeLite}).effectiveMode())
}

// TestSessionCacheV2WithMode_LiteDropsL2 锁定 lite 装配语义：l2 被摘除、l1_5 就位。
func TestSessionCacheV2WithMode_LiteDropsL2(t *testing.T) {
	fc := newIntegrationFileCache(t)
	c := NewSessionCacheV2WithMode(nil, "", 0, storage.StorageModeLite, fc)
	assert.Nil(t, c.l2, "lite 模式不得持有 L2 Redis 缓存")
	assert.Same(t, fc, c.l1_5)
	assert.NotNil(t, c.l1)
	assert.NotNil(t, c.l3)

	// SetFileCache 装配后注入也应生效（含摘除已有的 l2）
	c2 := NewSessionCacheV2(nil, "", 0)
	require.NotNil(t, c2.l2)
	c2.SetFileCache(fc, storage.StorageModeLite)
	assert.Nil(t, c2.l2, "SetFileCache(lite) 必须摘除 L2")
	assert.Same(t, fc, c2.l1_5)
}

// ── lite 模式：写路径（Set → L1 + L1.5）──────────────────────────────────

// TestSessionCacheV2Lite_SetBackfillsL15AndL1 验证 lite 写路径：Set 后 L1 与
// L1.5 都有数据，且 FileCache 里的内容可被直接读回（回填成功）。
func TestSessionCacheV2Lite_SetBackfillsL15AndL1(t *testing.T) {
	fc := newIntegrationFileCache(t)
	c := NewSessionCacheV2WithMode(nil, "", 0, storage.StorageModeLite, fc)
	ctx := context.Background()

	state := newIntegrationState("tenant-lite", "sess-lite-001", 3)
	require.NoError(t, c.Set(ctx, state))

	// L1 命中
	l1state := c.l1.Get("tenant-lite", "sess-lite-001")
	require.NotNil(t, l1state)
	assert.Equal(t, 3, l1state.LastTurnNo)

	// L1.5 命中（直接用 FileCache.Get 验证回填）
	fcState, err := fc.Get("tenant-lite", "sess-lite-001")
	require.NoError(t, err)
	require.NotNil(t, fcState)
	assert.Equal(t, 3, fcState.LastTurnNo)
	assert.Equal(t, "truncate", fcState.CompressionMeta.Strategy)
	assert.Equal(t, "pass", fcState.GovernanceMeta.LastInjectionVerdict)
}

// ── lite 模式：读路径（L1 miss → L1.5 hit → 回填 L1）─────────────────────

// TestSessionCacheV2Lite_GetHitsL15AfterL1Eviction 清空 L1 后 Get 仍能从 L1.5
// 命中，并把结果回填进 L1。
func TestSessionCacheV2Lite_GetHitsL15AfterL1Eviction(t *testing.T) {
	fc := newIntegrationFileCache(t)
	c := NewSessionCacheV2WithMode(nil, "", 0, storage.StorageModeLite, fc)
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, newIntegrationState("tenant-lite", "sess-l15-hit", 7)))

	// 模拟 L1 失效（容量淘汰 / 进程内丢失），L1.5 文件仍在
	c.l1.Delete("tenant-lite", "sess-l15-hit")
	assert.Nil(t, c.l1.Get("tenant-lite", "sess-l15-hit"))

	got, err := c.Get(ctx, "tenant-lite", "sess-l15-hit")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 7, got.LastTurnNo)

	// 命中 L1.5 后必须回填 L1
	assert.NotNil(t, c.l1.Get("tenant-lite", "sess-l15-hit"), "L1.5 命中后应回填 L1")
}

// TestSessionCacheV2Lite_ColdInstanceHitsL15 新建等价实例（模拟进程重启后 L1
// 为空），Get 仍从共享的 L1.5 命中。
func TestSessionCacheV2Lite_ColdInstanceHitsL15(t *testing.T) {
	fc := newIntegrationFileCache(t)
	ctx := context.Background()

	hot := NewSessionCacheV2WithMode(nil, "", 0, storage.StorageModeLite, fc)
	require.NoError(t, hot.Set(ctx, newIntegrationState("tenant-lite", "sess-restart", 9)))

	cold := NewSessionCacheV2WithMode(nil, "", 0, storage.StorageModeLite, fc)
	assert.Nil(t, cold.l1.Get("tenant-lite", "sess-restart"))

	got, err := cold.Get(ctx, "tenant-lite", "sess-restart")
	require.NoError(t, err)
	require.NotNil(t, got, "冷实例必须从 L1.5 命中")
	assert.Equal(t, 9, got.LastTurnNo)
	assert.NotNil(t, cold.l1.Get("tenant-lite", "sess-restart"), "L1.5 命中后应回填冷实例的 L1")
}

// ── lite 模式：L3 命中回填（L1 + L1.5）───────────────────────────────────

// TestSessionCacheV2Lite_L3HitBackfillsL15AndL1 用 pgxmock 驱动 L3 冷启动：
// L3 命中后必须回填 L1 与 L1.5。
func TestSessionCacheV2Lite_L3HitBackfillsL15AndL1(t *testing.T) {
	fc := newIntegrationFileCache(t)
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`WITH latest AS \(`).
		WithArgs("tenant-lite", "sess-l3-001").
		WillReturnRows(pgxmock.NewRows([]string{
			"turn_no", "ts", "compression_strategy", "compression_meta",
			"prompt_tokens", "completion_tokens", "injection_verdict", "output_verdict",
		}).AddRow(4, time.Now(), "truncate", nil, 40, 8, "pass", "skip"))

	c := &SessionCacheV2{
		l1:   NewCompressionMetaCache(10),
		l1_5: fc,
		l3:   newSessionTurnsReader(mock),
		mode: storage.StorageModeLite,
	}

	got, err := c.Get(context.Background(), "tenant-lite", "sess-l3-001")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 4, got.LastTurnNo)

	// L3 命中 → 回填 L1
	assert.NotNil(t, c.l1.Get("tenant-lite", "sess-l3-001"), "L3 命中后应回填 L1")
	// L3 命中 → 回填 L1.5（用 FileCache.Get 直接验证）
	fcState, err := fc.Get("tenant-lite", "sess-l3-001")
	require.NoError(t, err)
	require.NotNil(t, fcState)
	assert.Equal(t, 4, fcState.LastTurnNo)
	assert.Equal(t, "truncate", fcState.CompressionMeta.Strategy)

	require.NoError(t, mock.ExpectationsWereMet())
}

// ── lite 模式：Delete 三层失效 ───────────────────────────────────────────

// TestSessionCacheV2Lite_InvalidateClearsAllTiers Invalidate 后 L1、L1.5、L2
// 三层都拿不到（新建等价实例也拿不到）。
func TestSessionCacheV2Lite_InvalidateClearsAllTiers(t *testing.T) {
	fc := newIntegrationFileCache(t)
	c := NewSessionCacheV2WithMode(nil, "", 0, storage.StorageModeLite, fc)
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, newIntegrationState("tenant-lite", "sess-del-001", 2)))
	// 再直接种一条进 L1.5（模拟仅文件层存在的场景）
	require.NoError(t, fc.Set(newIntegrationState("tenant-lite", "sess-del-002", 5)))

	require.NoError(t, c.Invalidate(ctx, "tenant-lite", "sess-del-001"))
	require.NoError(t, c.Invalidate(ctx, "tenant-lite", "sess-del-002"))

	// L1 空
	assert.Nil(t, c.l1.Get("tenant-lite", "sess-del-001"))
	// L1.5 空（未命中必须命中私有 errCacheMiss 哨兵）
	_, err := fc.Get("tenant-lite", "sess-del-001")
	assert.True(t, errors.Is(err, errCacheMiss), "Invalidate 后 L1.5 应未命中, got %v", err)
	_, err = fc.Get("tenant-lite", "sess-del-002")
	assert.True(t, errors.Is(err, errCacheMiss))

	// 新建等价实例（L1 为空）：L1.5 已失效、L3 为 nil-db → 拿不到任何状态
	fresh := NewSessionCacheV2WithMode(nil, "", 0, storage.StorageModeLite, fc)
	got, err := fresh.Get(ctx, "tenant-lite", "sess-del-001")
	require.NoError(t, err)
	assert.Nil(t, got)
	got, err = fresh.Get(ctx, "tenant-lite", "sess-del-002")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestSessionCacheV2Lite_InvalidateVsGetNoReseed 锁定 B4 修复：并发 Get 与
// Invalidate 竞争时，Invalidate 返回后 L1 不得残留从 L1.5 回填的旧状态。
// 旧实现的失效顺序是 L1 → L1.5：Get 落在两步之间会从 L1.5 命中旧状态并
// 回填 L1（重播种），此后无人清除，脏数据最长存活到 LRU 逐出。新顺序
// （L2 → L1.5 → L1，L1 最后删）保证回填进来的数据终被清除。
func TestSessionCacheV2Lite_InvalidateVsGetNoReseed(t *testing.T) {
	const iterations = 200
	ctx := context.Background()

	for i := 0; i < iterations; i++ {
		fc := newIntegrationFileCache(t)
		c := NewSessionCacheV2WithMode(nil, "", 0, storage.StorageModeLite, fc)
		tenant, session := "tenant-lite", "sess-race-"+strconv.Itoa(i)

		// 仅种 L1.5（模拟只有文件层有旧数据的场景），L1 保持为空。
		require.NoError(t, fc.Set(newIntegrationState(tenant, session, 7)))

		var wg sync.WaitGroup
		stop := make(chan struct{})
		// 并发 Get：每次未命中 L1 都会从 L1.5 命中并回填 L1。
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = c.Get(ctx, tenant, session)
				}
			}
		}()
		require.NoError(t, c.Invalidate(ctx, tenant, session))
		close(stop)
		wg.Wait()

		// Invalidate 已返回：任何回填发生的时刻都先于 L1.Delete，L1 必须为空。
		assert.Nil(t, c.l1.Get(tenant, session),
			"iteration %d: Invalidate 返回后 L1 残留了 L1.5 回填的旧状态（B4 重播种）", i)
	}
}

// ── full 模式（含零值 mode）：等价性 ─────────────────────────────────────

// TestSessionCacheV2Full_IgnoresFileCache full 模式即使注入了 L1.5 也不读写它：
// L1.5 里预置的数据不能被 full 读路径命中，Set 也不落盘。
func TestSessionCacheV2Full_IgnoresFileCache(t *testing.T) {
	fc := newIntegrationFileCache(t)
	c := NewSessionCacheV2WithMode(nil, "", 0, storage.StorageModeFull, fc)
	assert.NotNil(t, c.l2, "full 模式保留 L2（此处为 disabled 实例，无需真实 Redis）")
	ctx := context.Background()

	// 预置 L1.5：full 读路径不得命中它
	seeded := newIntegrationState("tenant-full", "sess-full-001", 11)
	require.NoError(t, fc.Set(seeded))

	require.NoError(t, c.Set(ctx, newIntegrationState("tenant-full", "sess-full-001", 12)))

	// Set 不落盘：L1.5 里仍是预置的旧值（full 不写 L1.5）
	fcState, err := fc.Get("tenant-full", "sess-full-001")
	require.NoError(t, err)
	assert.Equal(t, 11, fcState.LastTurnNo, "full 模式 Set 不得写 L1.5")

	// L1 命中走 L1；清掉 L1 后：full 不得读 L1.5 → 回源 L3（nil-db）→ nil
	assert.NotNil(t, c.l1.Get("tenant-full", "sess-full-001"))
	c.l1.Delete("tenant-full", "sess-full-001")
	got, err := c.Get(ctx, "tenant-full", "sess-full-001")
	require.NoError(t, err)
	assert.Nil(t, got, "full 模式读路径不得命中 L1.5")

	// Invalidate 不因 L1.5 非空而出错
	require.NoError(t, c.Invalidate(ctx, "tenant-full", "sess-full-001"))
}

// TestSessionCacheV2ZeroValueModeEquivalence 零值 mode 与 full 等价，且
// l1_5 为 nil 时一切路径都不出错（兼容历史 NewSessionCacheV2 行为）。
func TestSessionCacheV2ZeroValueModeEquivalence(t *testing.T) {
	c := NewSessionCacheV2WithMode(nil, "", 0, "", nil) // mode 为零值
	assert.Equal(t, storage.StorageModeFull, c.effectiveMode())
	assert.Nil(t, c.l1_5)
	ctx := context.Background()

	state := newIntegrationState("tenant-zero", "sess-zero-001", 1)
	require.NoError(t, c.Set(ctx, state))
	assert.NotNil(t, c.l1.Get("tenant-zero", "sess-zero-001"))

	got, err := c.Get(ctx, "tenant-zero", "sess-zero-001")
	require.NoError(t, err)
	assert.NotNil(t, got)

	require.NoError(t, c.Invalidate(ctx, "tenant-zero", "sess-zero-001"))
	assert.Nil(t, c.l1.Get("tenant-zero", "sess-zero-001"))

	// 历史 NewSessionCacheV2 构造的实例 mode 同样为零值 → full 语义
	legacy := NewSessionCacheV2(nil, "", 0)
	assert.Equal(t, storage.StorageModeFull, legacy.effectiveMode())
	assert.Nil(t, legacy.l1_5)
}

// TestSessionCacheV2Full_L3HitNoError full 模式（disabled L2 + nil L1.5）下
// L3 冷启动正常返回并回填 L1，L2 回填走 disabled no-op 不报错。
func TestSessionCacheV2Full_L3HitNoError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`WITH latest AS \(`).
		WithArgs("tenant-full", "sess-l3-full").
		WillReturnRows(pgxmock.NewRows([]string{
			"turn_no", "ts", "compression_strategy", "compression_meta",
			"prompt_tokens", "completion_tokens", "injection_verdict", "output_verdict",
		}).AddRow(6, time.Now(), "", nil, 60, 6, "skip", "skip"))

	c := &SessionCacheV2{
		l1: NewCompressionMetaCache(10),
		l2: NewRedisGovernanceCache("", 0, 0), // disabled（addr 为空，fail-open）
		l3: newSessionTurnsReader(mock),
		// mode 零值 → full；l1_5 为 nil
	}

	got, err := c.Get(context.Background(), "tenant-full", "sess-l3-full")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 6, got.LastTurnNo)
	assert.NotNil(t, c.l1.Get("tenant-full", "sess-l3-full"))

	require.NoError(t, mock.ExpectationsWereMet())
}
