// 双模式存储架构（Task 6.1）端到端集成测试。
//
// 覆盖范围（全部基于 t.TempDir()，无外部服务依赖）：
//  1. lite 模式完整读写链路：工厂 → Session/Bodies/Turns/RequestLog 四类存储
//     读写 → 工厂 Close → 同目录重建工厂再读（持久化一致性 / 模式内重启）；
//  2. 缓存层链路：FileCache（L1.5）+ NewSessionCacheV2WithMode(nil, lite) 的
//     Set → L1.5 回填 → 跨实例命中 → Invalidate 双层失效；
//  3. StateStore 语义：Set/Get/Delete 与 TTL 过期（errors.Is ErrNotFound）；
//  4. 模式切换一致性（lite 侧）：同一数据目录、不同配置参数重开，数据不变；
//  5. 并发场景：100 写 + 100 读交叉（-race 下验证无竞争、无死锁）；
//  6. full 模式：工厂分派到桩实现，各方法一律返回 ErrNotImplemented。
//     架构决策（见 storage/factory/stubs.go）：生产环境 full 路径由 cmd/gateway
//     基于既有 pgx/redis 直接装配，不经过工厂，因此工厂 full 分支仅为结构占位。
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/storage"
	"github.com/kaixuan/llm-gateway-go/storage/factory"
)

// assertBodyEqual 校验读回的会话内容与写入内容逐字段一致。
// Metadata 经 JSON 往返后数值类型会变为 float64，因此用 JSONEq 做语义等价比较。
func assertBodyEqual(t *testing.T, want, got *storage.SessionBody) {
	t.Helper()
	require.NotNil(t, got)
	assert.Equal(t, want.TenantID, got.TenantID)
	assert.Equal(t, want.SessionID, got.SessionID)
	assert.Equal(t, want.TurnNo, got.TurnNo)
	assert.True(t, want.Timestamp.Equal(got.Timestamp),
		"Timestamp 应一致: want %v got %v", want.Timestamp, got.Timestamp)
	assert.JSONEq(t, string(want.Request), string(got.Request), "Request 中文内容应无损往返")
	assert.JSONEq(t, string(want.Response), string(got.Response), "Response 中文内容应无损往返")

	wantMeta, err := json.Marshal(want.Metadata)
	require.NoError(t, err)
	gotMeta, err := json.Marshal(got.Metadata)
	require.NoError(t, err)
	assert.JSONEq(t, string(wantMeta), string(gotMeta), "Metadata 应语义等价")
}

// TestDualModeLiteReadWriteFlow 场景 1：lite 模式完整读写链路与模式内重启持久化。
func TestDualModeLiteReadWriteFlow(t *testing.T) {
	ctx := context.Background()
	cfg := newLiteStorageConfig(t, t.TempDir(), 0)

	f, err := factory.NewStorageFactory(cfg)
	require.NoError(t, err)

	sessionStore := f.NewSessionStore()
	bodiesStore := f.NewBodiesStore()
	turnsStore := f.NewTurnsStore()
	requestLogStore := f.NewRequestLogStore()
	require.NotNil(t, sessionStore)
	require.NotNil(t, bodiesStore)
	require.NotNil(t, turnsStore)
	require.NotNil(t, requestLogStore)

	const sessionID = "sess-flow-01"

	// ---- 会话元数据：Create → Get ----
	createdAt := time.Now()
	require.NoError(t, sessionStore.CreateSession(ctx, &storage.Session{
		ID:       sessionID,
		TenantID: dualTenant,
		UserID:   "user-dual",
		Metadata: map[string]interface{}{"scene": "dual-mode", "来源": "集成测试"},
	}))
	gotSess, err := sessionStore.GetSession(ctx, dualTenant, sessionID)
	require.NoError(t, err)
	assert.Equal(t, sessionID, gotSess.ID)
	assert.Equal(t, "user-dual", gotSess.UserID)
	assert.Equal(t, "dual-mode", gotSess.Metadata["scene"])
	assert.False(t, gotSess.CreatedAt.IsZero(), "CreatedAt 应被自动填充")

	// ---- 会话内容：写 turn 1..5（含中文与 RawMessage）----
	bodies := make([]*storage.SessionBody, 0, 5)
	for turn := 1; turn <= 5; turn++ {
		body := dualBody(sessionID, turn)
		require.NoError(t, bodiesStore.Write(ctx, body), "写入 turn %d 内容", turn)
		bodies = append(bodies, body)
	}

	// ---- 轮次元数据：WriteTurnMeta turn 1..5 ----
	for turn := 1; turn <= 5; turn++ {
		require.NoError(t, turnsStore.WriteTurnMeta(ctx, &storage.TurnMeta{
			TenantID:            dualTenant,
			SessionID:           sessionID,
			TurnNo:              turn,
			Timestamp:           createdAt.Add(time.Duration(turn) * time.Second),
			CompressionStrategy: "none",
			PromptTokens:        100 * turn,
			CompletionTokens:    50 * turn,
		}), "写入 turn %d 元数据", turn)
	}

	// ---- 逐 turn 读回并校验字段一致 ----
	for turn := 1; turn <= 5; turn++ {
		got, err := bodiesStore.Read(ctx, dualTenant, sessionID, turn)
		require.NoError(t, err, "读取 turn %d 内容", turn)
		assertBodyEqual(t, bodies[turn-1], got)
	}

	// ---- ReadRange(2,4)：区间读取，含中文轮次，按轮次升序 ----
	ranged, err := bodiesStore.ReadRange(ctx, dualTenant, sessionID, 2, 4)
	require.NoError(t, err)
	require.Len(t, ranged, 3)
	for i, body := range ranged {
		assert.Equal(t, 2+i, body.TurnNo, "ReadRange 应按轮次升序返回")
		assertBodyEqual(t, bodies[1+i], body)
	}

	// ---- ListSessions：按租户列出 ----
	sessions, err := sessionStore.ListSessions(ctx, dualTenant, &storage.ListOptions{Limit: 100})
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, sessionID, sessions[0].ID)

	// ---- 请求日志：Write → Get ----
	sentAt := time.Now()
	require.NoError(t, requestLogStore.WriteRequest(ctx, &storage.RequestLog{
		RequestID:  "req-flow-0001",
		TenantID:   dualTenant,
		SessionID:  sessionID,
		Timestamp:  sentAt,
		Method:     "POST",
		Path:       "/v1/chat/completions",
		StatusCode: 200,
		Duration:   1234 * time.Millisecond,
		Body:       json.RawMessage(`{"note":"Body 不入库，读回恒为 nil"}`),
	}))
	gotLog, err := requestLogStore.GetRequest(ctx, "req-flow-0001")
	require.NoError(t, err)
	assert.Equal(t, "req-flow-0001", gotLog.RequestID)
	assert.Equal(t, dualTenant, gotLog.TenantID)
	assert.Equal(t, sessionID, gotLog.SessionID)
	assert.Equal(t, "POST", gotLog.Method)
	assert.Equal(t, "/v1/chat/completions", gotLog.Path)
	assert.Equal(t, 200, gotLog.StatusCode)
	assert.Equal(t, (1234 * time.Millisecond).Truncate(time.Millisecond), gotLog.Duration,
		"Duration 以毫秒精度持久化")
	assert.Equal(t, sentAt.Unix(), gotLog.Timestamp.Unix(), "Timestamp 以秒精度持久化")
	assert.Nil(t, gotLog.Body, "RequestLog.Body 不入库，读回应为 nil")

	// 不存在的请求日志返回 ErrNotFound
	_, err = requestLogStore.GetRequest(ctx, "req-not-exist")
	require.ErrorIs(t, err, storage.ErrNotFound)

	// ---- 工厂 Close（幂等）后同目录重建，验证持久化一致性 ----
	require.NoError(t, f.Close())
	require.NoError(t, f.Close(), "重复 Close 应幂等")

	f2, err := factory.NewStorageFactory(cfg)
	require.NoError(t, err)
	defer func() { _ = f2.Close() }()

	// 会话元数据在重启后仍在
	reopenedSess, err := f2.NewSessionStore().GetSession(ctx, dualTenant, sessionID)
	require.NoError(t, err)
	assert.Equal(t, sessionID, reopenedSess.ID)

	// 全部 5 轮内容在重启后仍完整可读（中文无损）
	reopenedBodies := f2.NewBodiesStore()
	for turn := 1; turn <= 5; turn++ {
		got, err := reopenedBodies.Read(ctx, dualTenant, sessionID, turn)
		require.NoError(t, err, "重启后读取 turn %d 内容", turn)
		assertBodyEqual(t, bodies[turn-1], got)
	}

	// 轮次元数据在重启后仍完整且按轮次升序
	reopenedTurns, err := f2.NewTurnsStore().GetTurnsMeta(ctx, dualTenant, sessionID)
	require.NoError(t, err)
	require.Len(t, reopenedTurns, 5)
	for i, meta := range reopenedTurns {
		assert.Equal(t, 1+i, meta.TurnNo)
		assert.Equal(t, 100*(1+i), meta.PromptTokens)
		assert.Equal(t, 50*(1+i), meta.CompletionTokens)
		assert.Equal(t, "none", meta.CompressionStrategy)
	}

	// 请求日志在重启后仍可查
	reopenedLog, err := f2.NewRequestLogStore().GetRequest(ctx, "req-flow-0001")
	require.NoError(t, err)
	assert.Equal(t, "req-flow-0001", reopenedLog.RequestID)
}

// TestDualModeCacheLayerFlow 场景 2：lite 模式缓存层链路（L1 + L1.5）。
// db 传 nil：L3 回源返回空状态而非 panic（v2 包测试已验证该语义）。
func TestDualModeCacheLayerFlow(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fc := newDualFileCache(t, dir+"/l15")

	const (
		sessionID = "sess-cache-01"
		lastTurn  = 7
	)

	// cache1：写入端（lite 模式，L2 已被摘除，写 L1 + L1.5）
	cache1 := v2.NewSessionCacheV2WithMode(nil, "", 0, storage.StorageModeLite, fc)
	state := dualState(sessionID, lastTurn)
	require.NoError(t, cache1.Set(ctx, state))

	// L1.5 回填验证：Set 后同一 FileCache 直接可命中（说明 cache.Set 写穿了 L1.5）
	direct, err := fc.Get(dualTenant, sessionID)
	require.NoError(t, err, "cache.Set 应回填 L1.5 文件缓存")
	require.NotNil(t, direct)
	assert.Equal(t, lastTurn, direct.LastTurnNo)
	assert.Equal(t, "truncate_middle", direct.CompressionMeta.Strategy)
	assert.Equal(t, "总结标记：本轮之前已完成压缩", direct.CompressionMeta.SummaryMarker)

	// 跨实例命中：cache1.Get 命中的是自身 L1；用 cache2 的 L1 冷启动（共享 fc），
	// 能取到状态只能来自 L1.5 文件缓存。
	cache2 := v2.NewSessionCacheV2WithMode(nil, "", 0, storage.StorageModeLite, fc)
	got, err := cache2.Get(ctx, dualTenant, sessionID)
	require.NoError(t, err)
	require.NotNil(t, got, "L1 冷实例应经 L1.5 文件缓存命中")
	assert.Equal(t, lastTurn, got.LastTurnNo)
	assert.Equal(t, sessionID, got.SessionID)
	assert.Equal(t, dualTenant, got.TenantID)

	// Invalidate 双层失效：cache1 与 cache2 各自的 L1 均被删除，L1.5 文件被删除。
	require.NoError(t, cache1.Invalidate(ctx, dualTenant, sessionID))
	require.NoError(t, cache2.Invalidate(ctx, dualTenant, sessionID))

	// L1.5 直接取不到
	_, err = fc.Get(dualTenant, sessionID)
	require.Error(t, err, "Invalidate 后 L1.5 文件缓存应未命中")

	// L1 失效且 L1.5 已空：cache1.Get 只能落到 L3（nil db → 空状态），不 panic、无残留
	got, err = cache1.Get(ctx, dualTenant, sessionID)
	require.NoError(t, err)
	assert.Nil(t, got, "Invalidate 后 cache1 不应再取到状态")

	// 全新第三实例（L1 冷 + L1.5 已删）：同样取不到
	cache3 := v2.NewSessionCacheV2WithMode(nil, "", 0, storage.StorageModeLite, fc)
	got, err = cache3.Get(ctx, dualTenant, sessionID)
	require.NoError(t, err)
	assert.Nil(t, got, "Invalidate 后冷实例不应再经 L1.5 取到状态")
}

// TestDualModeStateStoreSemantics 场景 3：lite 模式 StateStore 语义
// （内存实现，语义对齐 Redis：过期即不存在，ErrNotFound 判定用 errors.Is）。
func TestDualModeStateStoreSemantics(t *testing.T) {
	ctx := context.Background()
	f, err := factory.NewStorageFactory(newLiteStorageConfig(t, t.TempDir(), 0))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	state := f.NewStateStore()
	require.NotNil(t, state)

	// Set（带 TTL）→ Get 命中
	require.NoError(t, state.Set(ctx, "dual:state:live", map[string]interface{}{"v": 1}, time.Minute))
	got, err := state.Get(ctx, "dual:state:live")
	require.NoError(t, err)
	require.NotNil(t, got)

	// Set（无 TTL，永不过期）→ Get 命中
	require.NoError(t, state.Set(ctx, "dual:state:forever", "常驻值", 0))
	got, err = state.Get(ctx, "dual:state:forever")
	require.NoError(t, err)
	assert.Equal(t, "常驻值", got)

	// Delete → Get 返回 ErrNotFound；Delete 幂等
	require.NoError(t, state.Delete(ctx, "dual:state:live"))
	_, err = state.Get(ctx, "dual:state:live")
	require.ErrorIs(t, err, storage.ErrNotFound, "删除后应返回 ErrNotFound")
	require.NoError(t, state.Delete(ctx, "dual:state:live"), "重复 Delete 应幂等")

	// TTL 过期：短 TTL 写入后等待过期，读路径惰性删除并返回 ErrNotFound
	require.NoError(t, state.Set(ctx, "dual:state:ttl", "短命值", 40*time.Millisecond))
	got, err = state.Get(ctx, "dual:state:ttl")
	require.NoError(t, err)
	require.NotNil(t, got, "TTL 内应立即可读")

	time.Sleep(120 * time.Millisecond)
	_, err = state.Get(ctx, "dual:state:ttl")
	require.ErrorIs(t, err, storage.ErrNotFound, "TTL 过期后应返回 ErrNotFound（过期即不存在）")

	// 从未写入的键同样返回 ErrNotFound
	_, err = state.Get(ctx, "dual:state:never-written")
	require.ErrorIs(t, err, storage.ErrNotFound)
}

// TestDualModeLiteConfigSwitchConsistency 场景 4：模式切换一致性（lite 侧）。
// 同一数据目录，先用配置 A（默认参数）写入，再用配置 B（不同 AsyncWriters /
// MaxConnections / Timeout，路径相同）重开读取——验证配置变化不破坏既有数据。
func TestDualModeLiteConfigSwitchConsistency(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// 配置 A：AsyncWriters 零值（工厂回落默认 4），MaxConnections=2
	cfgA := newLiteStorageConfig(t, dir, 0)
	fA, err := factory.NewStorageFactory(cfgA)
	require.NoError(t, err)

	const sessionID = "sess-switch-01"
	require.NoError(t, fA.NewSessionStore().CreateSession(ctx, &storage.Session{
		ID:       sessionID,
		TenantID: dualTenant,
		UserID:   "user-switch",
	}))
	bodiesA := make([]*storage.SessionBody, 0, 3)
	for turn := 1; turn <= 3; turn++ {
		body := dualBody(sessionID, turn)
		require.NoError(t, fA.NewBodiesStore().Write(ctx, body))
		bodiesA = append(bodiesA, body)
	}
	require.NoError(t, fA.NewTurnsStore().WriteTurnMeta(ctx, &storage.TurnMeta{
		TenantID:            dualTenant,
		SessionID:           sessionID,
		TurnNo:              1,
		CompressionStrategy: "lite-default",
		PromptTokens:        11,
		CompletionTokens:    7,
	}))
	require.NoError(t, fA.Close())

	// 配置 B：路径不变，仅调整运行参数（写 worker 数、连接上限、超时）
	cfgB := newLiteStorageConfig(t, dir, 7)
	cfgB.MaxConnections = 8
	cfgB.Timeout = time.Minute
	fB, err := factory.NewStorageFactory(cfgB)
	require.NoError(t, err)
	defer func() { _ = fB.Close() }()

	// 配置 B 下读取配置 A 写入的数据，应完全一致
	gotSess, err := fB.NewSessionStore().GetSession(ctx, dualTenant, sessionID)
	require.NoError(t, err)
	assert.Equal(t, "user-switch", gotSess.UserID)

	bodiesB := fB.NewBodiesStore()
	for turn := 1; turn <= 3; turn++ {
		got, err := bodiesB.Read(ctx, dualTenant, sessionID, turn)
		require.NoError(t, err, "配置 B 下读取 turn %d", turn)
		assertBodyEqual(t, bodiesA[turn-1], got)
	}

	metas, err := fB.NewTurnsStore().GetTurnsMeta(ctx, dualTenant, sessionID)
	require.NoError(t, err)
	require.Len(t, metas, 1)
	assert.Equal(t, "lite-default", metas[0].CompressionStrategy)
	assert.Equal(t, 11, metas[0].PromptTokens)

	// 配置 B 下继续写入不受影响（写 worker 数变化不破坏链路）
	extra := dualBody(sessionID, 4)
	require.NoError(t, bodiesB.Write(ctx, extra))
	got, err := bodiesB.Read(ctx, dualTenant, sessionID, 4)
	require.NoError(t, err)
	assertBodyEqual(t, extra, got)
}

// TestDualModeConcurrentReadWrite 场景 5：并发场景。
// 100 个写协程各写自己的 turn（10 会话 × 10 turn），同时 100 个读协程交叉读取；
// 在 -race 下全部成功、无死锁。外层 time.AfterFunc 看门狗保护，超时报告 t.Error。
func TestDualModeConcurrentReadWrite(t *testing.T) {
	ctx := context.Background()
	f, err := factory.NewStorageFactory(newLiteStorageConfig(t, t.TempDir(), 0))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	const (
		numSessions = 10
		turnsPerSes = 10
		numWriters  = numSessions * turnsPerSes // 100
		numReaders  = 100
	)

	sessionStore := f.NewSessionStore()
	bodiesStore := f.NewBodiesStore()

	// 预建 10 个会话（并发阶段只写 turn，保证写协程之间的目标互不相交）
	for s := 0; s < numSessions; s++ {
		require.NoError(t, sessionStore.CreateSession(ctx, &storage.Session{
			ID:       fmt.Sprintf("sess-conc-%02d", s),
			TenantID: dualTenant,
			UserID:   "user-conc",
		}))
	}

	// 看门狗：60s 内未完成则关闭通道；主测试协程据此 t.Error 并终止子测试
	// （避免真死锁时 wg.Wait 永久挂起拖垮整个测试进程）。
	watchdogFired := make(chan struct{})
	guard := time.AfterFunc(60*time.Second, func() { close(watchdogFired) })
	defer guard.Stop()

	var mu sync.Mutex
	var writeErrs []error
	var wg sync.WaitGroup

	// 100 写协程：协程 i 写会话 i/10 的 turn i%10+1（内容 + 元数据）
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sessionID := fmt.Sprintf("sess-conc-%02d", i/turnsPerSes)
			turn := i%turnsPerSes + 1
			body := dualBody(sessionID, turn)
			if err := bodiesStore.Write(ctx, body); err != nil {
				mu.Lock()
				writeErrs = append(writeErrs, fmt.Errorf("写 %s#%d: %w", sessionID, turn, err))
				mu.Unlock()
				return
			}
			if err := f.NewTurnsStore().WriteTurnMeta(ctx, &storage.TurnMeta{
				TenantID:            dualTenant,
				SessionID:           sessionID,
				TurnNo:              turn,
				CompressionStrategy: "conc",
				PromptTokens:        turn,
				CompletionTokens:    turn * 2,
			}); err != nil {
				mu.Lock()
				writeErrs = append(writeErrs, fmt.Errorf("写 %s#%d 元数据: %w", sessionID, turn, err))
				mu.Unlock()
			}
		}(i)
	}

	// 100 读协程：与写并发交叉读取。读取可能先于写入发生，未写入轮次的
	// ErrNotFound 属合法结果；读到的内容必须字段自洽。
	var readChecked atomic.Int64
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sessionID := fmt.Sprintf("sess-conc-%02d", i%numSessions)
			turn := i%turnsPerSes + 1
			got, err := bodiesStore.Read(ctx, dualTenant, sessionID, turn)
			if err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					return // 尚未写入，合法
				}
				mu.Lock()
				writeErrs = append(writeErrs, fmt.Errorf("读 %s#%d: %w", sessionID, turn, err))
				mu.Unlock()
				return
			}
			if got.TenantID != dualTenant || got.SessionID != sessionID || got.TurnNo != turn {
				mu.Lock()
				writeErrs = append(writeErrs, fmt.Errorf(
					"读到不一致内容: %s#%d got tenant=%s session=%s turn=%d",
					sessionID, turn, got.TenantID, got.SessionID, got.TurnNo))
				mu.Unlock()
				return
			}
			readChecked.Add(1)
		}(i)
	}

	// 等待全部协程：正常完成或看门狗触发（此时统一由主协程报告并退出）
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-watchdogFired:
		t.Error("并发读写测试 60s 内未完成，疑似死锁")
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("看门狗触发后协程仍未退出，判定死锁")
		}
	}

	// 并发阶段收集到的任何读写失败都视为测试失败
	if len(writeErrs) > 0 {
		for _, err := range writeErrs {
			t.Error(err)
		}
		t.Fatalf("并发阶段共 %d 个失败", len(writeErrs))
	}
	t.Logf("并发读阶段交叉命中并校验通过 %d 次（其余为合法的写入前未命中）", readChecked.Load())

	// 收尾校验：写入全部结束后，10 会话 × 10 turn 必须全部完整可读
	for s := 0; s < numSessions; s++ {
		sessionID := fmt.Sprintf("sess-conc-%02d", s)
		for turn := 1; turn <= turnsPerSes; turn++ {
			got, err := bodiesStore.Read(ctx, dualTenant, sessionID, turn)
			require.NoError(t, err, "收尾校验 %s#%d", sessionID, turn)
			assert.Equal(t, turn, got.TurnNo)
		}
		metas, err := f.NewTurnsStore().GetTurnsMeta(ctx, dualTenant, sessionID)
		require.NoError(t, err)
		require.Len(t, metas, turnsPerSes, "%s 应有全部轮次元数据", sessionID)
	}
}

// TestDualModeFullModeStubBehavior 场景 6：full 模式工厂桩行为。
//
// 验证工厂 full 分派到的各 store 方法一律返回 storage.ErrNotImplemented
// （errors.Is 判定）。再次强调架构决策：生产环境 full 模式路径由 cmd/gateway
// 基于既有 pgx/redis 直接装配，不经过本工厂；工厂 full 分支仅为结构占位与
// 分派逻辑测试保留（见 storage/factory/stubs.go 文件头注释）。
//
// 接入点预留：PostgresURL/RedisURL 使用占位连接串即可，pgxpool 与 go-redis
// 均为惰性建连，本测试不发起任何真实网络请求。若未来需要接入真实
// PostgreSQL/Redis 做 full 模式完整链路验证，通过环境变量
// TEST_DUAL_FULL_PGURL 与 TEST_DUAL_FULL_REDISURL 提供连接串——两者同时设置时
// 本桩验证跳过（桩断言对真实服务无意义，届时由读取这两个变量的真实链路测试接管）。
func TestDualModeFullModeStubBehavior(t *testing.T) {
	if os.Getenv("TEST_DUAL_FULL_PGURL") != "" && os.Getenv("TEST_DUAL_FULL_REDISURL") != "" {
		t.Skip("TEST_DUAL_FULL_PGURL 与 TEST_DUAL_FULL_REDISURL 已同时设置：" +
			"预留的真实 full 模式链路测试接入点，桩行为验证跳过")
	}

	ctx := context.Background()
	f, err := factory.NewStorageFactory(&storage.StorageConfig{
		Mode:        storage.StorageModeFull,
		PostgresURL: "postgres://gateway:secret@127.0.0.1:5432/gateway?sslmode=disable",
		RedisURL:    "redis://127.0.0.1:6379/0",
	})
	require.NoError(t, err, "惰性建连，占位连接串不应导致工厂创建失败")
	defer func() { _ = f.Close() }()

	// 各存储分派到 full 模式桩实现
	sessionStore := f.NewSessionStore()
	bodiesStore := f.NewBodiesStore()
	turnsStore := f.NewTurnsStore()
	requestLogStore := f.NewRequestLogStore()
	stateStore := f.NewStateStore()
	require.NotNil(t, sessionStore)
	require.NotNil(t, bodiesStore)
	require.NotNil(t, turnsStore)
	require.NotNil(t, requestLogStore)
	require.NotNil(t, stateStore)

	// SessionStore 桩：全部方法返回 ErrNotImplemented
	require.ErrorIs(t, sessionStore.CreateSession(ctx, &storage.Session{}), storage.ErrNotImplemented)
	_, err = sessionStore.GetSession(ctx, dualTenant, "sess-x")
	require.ErrorIs(t, err, storage.ErrNotImplemented)
	require.ErrorIs(t, sessionStore.UpdateSession(ctx, &storage.Session{}), storage.ErrNotImplemented)
	_, err = sessionStore.ListSessions(ctx, dualTenant, nil)
	require.ErrorIs(t, err, storage.ErrNotImplemented)
	require.ErrorIs(t, sessionStore.DeleteSession(ctx, dualTenant, "sess-x"), storage.ErrNotImplemented)

	// BodiesStore 桩
	require.ErrorIs(t, bodiesStore.Write(ctx, dualBody("sess-x", 1)), storage.ErrNotImplemented)
	_, err = bodiesStore.Read(ctx, dualTenant, "sess-x", 1)
	require.ErrorIs(t, err, storage.ErrNotImplemented)
	_, err = bodiesStore.ReadRange(ctx, dualTenant, "sess-x", 1, 2)
	require.ErrorIs(t, err, storage.ErrNotImplemented)
	require.ErrorIs(t, bodiesStore.Delete(ctx, dualTenant, "sess-x"), storage.ErrNotImplemented)

	// TurnsStore 桩
	require.ErrorIs(t, turnsStore.WriteTurnMeta(ctx, &storage.TurnMeta{}), storage.ErrNotImplemented)
	_, err = turnsStore.GetTurnsMeta(ctx, dualTenant, "sess-x")
	require.ErrorIs(t, err, storage.ErrNotImplemented)

	// RequestLogStore 桩
	require.ErrorIs(t, requestLogStore.WriteRequest(ctx, &storage.RequestLog{}), storage.ErrNotImplemented)
	_, err = requestLogStore.GetRequest(ctx, "req-x")
	require.ErrorIs(t, err, storage.ErrNotImplemented)
	_, err = requestLogStore.ListRequests(ctx, &storage.RequestFilter{})
	require.ErrorIs(t, err, storage.ErrNotImplemented)

	// StateStore 桩
	require.ErrorIs(t, stateStore.Set(ctx, "k", "v", time.Minute), storage.ErrNotImplemented)
	_, err = stateStore.Get(ctx, "k")
	require.ErrorIs(t, err, storage.ErrNotImplemented)
	require.ErrorIs(t, stateStore.Delete(ctx, "k"), storage.ErrNotImplemented)

	// 重复 Close 幂等（full 分支关闭 Redis/PG 客户端，均为惰性资源）
	require.NoError(t, f.Close())
}
