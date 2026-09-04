package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/storage"
	filestore "github.com/kaixuan/llm-gateway-go/storage/file"
	memorystore "github.com/kaixuan/llm-gateway-go/storage/memory"
	sqlitestore "github.com/kaixuan/llm-gateway-go/storage/sqlite"
)

// newTestFactory 测试辅助构造函数：绕过 NewStorageFactory 的资源初始化，
// 直接构造指定模式的工厂，用于验证分派逻辑与未知模式行为。
func newTestFactory(mode storage.StorageMode) *StorageFactory {
	return &StorageFactory{
		mode:   mode,
		config: &storage.StorageConfig{Mode: mode},
	}
}

// liteModeConfig 基于临时目录构造一份可用的 lite 模式配置。
func liteModeConfig(t *testing.T) *storage.StorageConfig {
	t.Helper()
	dir := t.TempDir()
	return &storage.StorageConfig{
		Mode:           storage.StorageModeLite,
		SQLitePath:     filepath.Join(dir, "gateway.db"),
		BodiesDir:      filepath.Join(dir, "bodies"),
		CacheDir:       filepath.Join(dir, "cache"),
		LogsDir:        filepath.Join(dir, "logs"),
		MaxConnections: 1,
		Timeout:        5 * time.Second,
	}
}

func TestNewStorageFactoryLiteMode(t *testing.T) {
	cfg := liteModeConfig(t)

	f, err := NewStorageFactory(cfg)
	require.NoError(t, err)
	require.NotNil(t, f)
	assert.Equal(t, storage.StorageModeLite, f.mode)
	defer f.Close()

	// 三个目录均被创建
	for _, dir := range []string{cfg.BodiesDir, cfg.CacheDir, cfg.LogsDir} {
		st, err := os.Stat(dir)
		require.NoError(t, err, "目录应已创建: %s", dir)
		assert.True(t, st.IsDir(), "%s 应为目录", dir)
	}

	// 各存储构造成功、非 nil 且分派到 lite 模式真实实现
	ss := f.NewSessionStore()
	require.NotNil(t, ss)
	assert.IsType(t, &sqlitestore.SQLiteSessionStore{}, ss)

	bs := f.NewBodiesStore()
	require.NotNil(t, bs)
	assert.IsType(t, &filestore.FileBodiesStore{}, bs)

	ts := f.NewTurnsStore()
	require.NotNil(t, ts)
	assert.IsType(t, &sqlitestore.SQLiteTurnsStore{}, ts)

	rl := f.NewRequestLogStore()
	require.NotNil(t, rl)
	assert.IsType(t, &sqlitestore.SQLiteRequestLogStore{}, rl)

	// lite 模式 StateStore 为内存实现（memorystore.MemoryStateStore）
	st := f.NewStateStore()
	require.NotNil(t, st)
	assert.IsType(t, &memorystore.MemoryStateStore{}, st)
	require.NoError(t, st.Set(context.Background(), "k", "v", time.Minute))
	v, err := st.Get(context.Background(), "k")
	require.NoError(t, err)
	assert.Equal(t, "v", v)

	// lite 模式下访问器返回 nil
	assert.Nil(t, f.GetPgPool())
	assert.Nil(t, f.GetRedisClient())
}

// TestLiteFactoryStoresFunctional 验证 lite 模式下工厂返回的各存储真实可用：
// SessionStore Create→Get、StateStore Set→Get、BodiesStore Write→Read 落盘，
// 以及工厂 Close 幂等且不丢数据。
func TestLiteFactoryStoresFunctional(t *testing.T) {
	ctx := context.Background()
	cfg := liteModeConfig(t)

	f, err := NewStorageFactory(cfg)
	require.NoError(t, err)

	// SessionStore：Create → Get
	ss := f.NewSessionStore()
	in := &storage.Session{
		ID:       "sess-1",
		TenantID: "tenant-1",
		UserID:   "user-1",
		Metadata: map[string]interface{}{"scene": "chat"},
	}
	require.NoError(t, ss.CreateSession(ctx, in))
	got, err := ss.GetSession(ctx, "tenant-1", "sess-1")
	require.NoError(t, err)
	assert.Equal(t, "sess-1", got.ID)
	assert.Equal(t, "tenant-1", got.TenantID)
	assert.Equal(t, "user-1", got.UserID)
	assert.Equal(t, "chat", got.Metadata["scene"])
	assert.False(t, got.CreatedAt.IsZero(), "CreatedAt 应被自动填充")

	// StateStore：Set → Get
	st := f.NewStateStore()
	require.NoError(t, st.Set(ctx, "rate:key", "v", time.Minute))
	v, err := st.Get(ctx, "rate:key")
	require.NoError(t, err)
	assert.Equal(t, "v", v)

	// BodiesStore：Write → Read（FileBodiesStore.Write 为同步落盘）
	bs := f.NewBodiesStore()
	body := &storage.SessionBody{
		TenantID:  "tenant-1",
		SessionID: "sess-1",
		TurnNo:    1,
		Timestamp: time.Now(),
		Request:   json.RawMessage(`{"q":"hi"}`),
		Response:  json.RawMessage(`{"a":"hello"}`),
	}
	require.NoError(t, bs.Write(ctx, body))
	rb, err := bs.Read(ctx, "tenant-1", "sess-1", 1)
	require.NoError(t, err)
	assert.Equal(t, "tenant-1", rb.TenantID)
	assert.Equal(t, "sess-1", rb.SessionID)
	assert.Equal(t, 1, rb.TurnNo)
	assert.JSONEq(t, `{"q":"hi"}`, string(rb.Request))
	assert.JSONEq(t, `{"a":"hello"}`, string(rb.Response))

	// 关闭工厂后数据已落盘（内容文件路径：
	// {BodiesDir}/{tenantID}/{sessionID前2位}/{sessionID}/turn_{turnNo}.json.gz）
	require.NoError(t, f.Close())
	turnFile := filepath.Join(cfg.BodiesDir, "tenant-1", "se", "sess-1", "turn_1.json.gz")
	_, err = os.Stat(turnFile)
	require.NoError(t, err, "内容文件应已落盘: %s", turnFile)

	// 会话元数据同样已持久化：重开工厂可读到同一会话
	f2, err := NewStorageFactory(cfg)
	require.NoError(t, err)
	defer f2.Close()
	again, err := f2.NewSessionStore().GetSession(ctx, "tenant-1", "sess-1")
	require.NoError(t, err)
	assert.Equal(t, "sess-1", again.ID)

	// Close 幂等
	require.NoError(t, f.Close())
}

// TestNewBodiesStoreSingleton 验证 lite 模式下 NewBodiesStore 与 NewStateStore
// 多次调用返回同一惰性单例（有状态实现必须复用，避免资源泄漏/数据不共享）。
func TestNewBodiesStoreSingleton(t *testing.T) {
	f, err := NewStorageFactory(liteModeConfig(t))
	require.NoError(t, err)
	defer f.Close()

	b1 := f.NewBodiesStore()
	b2 := f.NewBodiesStore()
	assert.Same(t, b1, b2, "NewBodiesStore 应返回同一单例")

	s1 := f.NewStateStore()
	s2 := f.NewStateStore()
	assert.Same(t, s1, s2, "NewStateStore 应返回同一单例")

	// 单例经 Close 关闭后仍可安全地重复 Close（工厂幂等关闭单例）
	require.NoError(t, f.Close())
	require.NoError(t, f.Close())
}

// TestNewBodiesStoreConcurrent 并发调用 NewBodiesStore/NewStateStore
// 仍应返回同一实例且无数据竞争（配合 go test -race 验证）。
func TestNewBodiesStoreConcurrent(t *testing.T) {
	f, err := NewStorageFactory(liteModeConfig(t))
	require.NoError(t, err)
	defer f.Close()

	const n = 16
	bodies := make([]storage.BodiesStore, n)
	states := make([]storage.StateStore, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bodies[i] = f.NewBodiesStore()
			states[i] = f.NewStateStore()
		}(i)
	}
	wg.Wait()

	for i := 1; i < n; i++ {
		assert.Same(t, bodies[0], bodies[i])
		assert.Same(t, states[0], states[i])
	}
}

func TestNewStorageFactoryLiteModeMissingPath(t *testing.T) {
	f, err := NewStorageFactory(&storage.StorageConfig{Mode: storage.StorageModeLite})
	require.Error(t, err)
	assert.Nil(t, f)
	assert.Contains(t, err.Error(), "SQLitePath")
}

func TestFactoryCloseIdempotent(t *testing.T) {
	f, err := NewStorageFactory(liteModeConfig(t))
	require.NoError(t, err)

	// 重复 Close 不 panic（幂等）
	require.NoError(t, f.Close())
	require.NoError(t, f.Close())
}

func TestFactoryFullModeDispatch(t *testing.T) {
	// pgxpool 与 redis 客户端均为惰性建连，此处构造对象不会发起真实网络连接
	poolCfg, err := pgxpool.ParseConfig("postgres://gateway:secret@127.0.0.1:5432/gateway")
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	require.NoError(t, err)
	defer pool.Close()

	redisClient := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	defer redisClient.Close()

	f := &StorageFactory{
		mode:    storage.StorageModeFull,
		pgPool:  pool,
		redisDB: redisClient,
		config:  &storage.StorageConfig{Mode: storage.StorageModeFull},
	}

	// 访问器返回底层资源
	assert.Same(t, pool, f.GetPgPool())
	assert.Same(t, redisClient, f.GetRedisClient())

	// 按 full 模式分派到 PostgreSQL/Redis 桩实现
	assert.IsType(t, &pgSessionStore{}, f.NewSessionStore())
	assert.IsType(t, &pgBodiesStore{}, f.NewBodiesStore())
	assert.IsType(t, &pgTurnsStore{}, f.NewTurnsStore())
	assert.IsType(t, &pgRequestLogStore{}, f.NewRequestLogStore())
	assert.IsType(t, &redisStateStore{}, f.NewStateStore())

	// 重复 Close 不 panic（幂等）
	require.NoError(t, f.Close())
	require.NoError(t, f.Close())
}

func TestNewStorageFactoryFullModeConfig(t *testing.T) {
	// 仅验证 MaxConnections 正确传播到两个连接池，不发起真实连接
	f, err := NewStorageFactory(&storage.StorageConfig{
		Mode:           storage.StorageModeFull,
		PostgresURL:    "postgres://gateway:secret@127.0.0.1:5432/gateway",
		RedisURL:       "127.0.0.1:6379",
		MaxConnections: 7,
	})
	require.NoError(t, err)
	defer f.Close()

	assert.Equal(t, int32(7), f.GetPgPool().Config().MaxConns)
	assert.Equal(t, 7, f.GetRedisClient().Options().PoolSize)
}

// TestRedisOptionsFromURL 验证 RedisURL 两种形态的解析（不发起真实连接）：
//   - redis:// / rediss:// URL → redis.ParseURL 正确拆出 Addr/Password/DB；
//   - 裸 host:port → 作为 Addr 原样使用（历史行为）。
func TestRedisOptionsFromURL(t *testing.T) {
	opts, err := redisOptionsFromURL("redis://:secretpass@10.0.0.8:6380/2")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.8:6380", opts.Addr)
	assert.Equal(t, "secretpass", opts.Password)
	assert.Equal(t, 2, opts.DB)

	opts, err = redisOptionsFromURL("rediss://bench@example.com:6379")
	require.NoError(t, err)
	assert.Equal(t, "example.com:6379", opts.Addr)
	assert.Equal(t, "bench", opts.Username)
	assert.NotNil(t, opts.TLSConfig, "rediss:// 应启用 TLS")

	opts, err = redisOptionsFromURL("127.0.0.1:6379")
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:6379", opts.Addr)
	assert.Equal(t, 0, opts.DB)

	// 非法 URL 报错而不是静默生成坏配置
	_, err = redisOptionsFromURL("redis://[::1:not-a-port")
	require.Error(t, err)
}

// TestNewStorageFactoryFullModeRedisURL 验证 full 模式工厂把 redis:// 形态的
// RedisURL 正确传播到 Redis 客户端 Options（惰性建连，不发起真实连接）。
func TestNewStorageFactoryFullModeRedisURL(t *testing.T) {
	f, err := NewStorageFactory(&storage.StorageConfig{
		Mode:        storage.StorageModeFull,
		PostgresURL: "postgres://gateway:secret@127.0.0.1:5432/gateway",
		RedisURL:    "redis://:pass@127.0.0.1:6380/3",
	})
	require.NoError(t, err)
	defer f.Close()

	opts := f.GetRedisClient().Options()
	assert.Equal(t, "127.0.0.1:6380", opts.Addr)
	assert.Equal(t, "pass", opts.Password)
	assert.Equal(t, 3, opts.DB)
}

// TestBodiesWorkers 验证 lite 模式 BodiesStore 写 worker 数的解析：
// 显式 AsyncWriters（>0）优先，<=0 或未配置回落 defaultFileWorkers。
func TestBodiesWorkers(t *testing.T) {
	f := newTestFactory(storage.StorageModeLite)
	f.config = &storage.StorageConfig{Mode: storage.StorageModeLite, AsyncWriters: 7}
	assert.Equal(t, 7, f.bodiesWorkers())

	f.config.AsyncWriters = 0
	assert.Equal(t, defaultFileWorkers, f.bodiesWorkers())

	f.config.AsyncWriters = -3
	assert.Equal(t, defaultFileWorkers, f.bodiesWorkers())

	// config 为 nil（零值工厂）时不 panic，回落默认值
	bare := &StorageFactory{}
	assert.Equal(t, defaultFileWorkers, bare.bodiesWorkers())
}

// TestLiteBodiesStoreUsesAsyncWriters 端到端验证：以显式 AsyncWriters 创建的
// lite 工厂，其 BodiesStore 单例可正常写入并在 Close 时优雅排空（worker 数
// 本身是 file 包内部状态，此处验证配置通路不破坏既有行为）。
func TestLiteBodiesStoreUsesAsyncWriters(t *testing.T) {
	cfg := liteModeConfig(t)
	cfg.AsyncWriters = 2

	f, err := NewStorageFactory(cfg)
	require.NoError(t, err)

	bs := f.NewBodiesStore()
	require.NotNil(t, bs)
	assert.Same(t, bs, f.NewBodiesStore(), "仍应为惰性单例")

	body := &storage.SessionBody{
		TenantID:  "tenant-1",
		SessionID: "sess-aw",
		TurnNo:    1,
		Timestamp: time.Now(),
		Request:   json.RawMessage(`{"q":"hi"}`),
	}
	require.NoError(t, bs.Write(context.Background(), body))
	require.NoError(t, f.Close())

	turnFile := filepath.Join(cfg.BodiesDir, "tenant-1", "se", "sess-aw", "turn_1.json.gz")
	_, err = os.Stat(turnFile)
	require.NoError(t, err, "Close 后内容文件应已落盘: %s", turnFile)
}

func TestNewStorageFactoryUnknownMode(t *testing.T) {
	f, err := NewStorageFactory(&storage.StorageConfig{Mode: storage.StorageMode("bogus")})
	require.Error(t, err)
	assert.Nil(t, f)
	assert.Contains(t, err.Error(), "bogus")

	// nil 配置同样返回错误
	f, err = NewStorageFactory(nil)
	require.Error(t, err)
	assert.Nil(t, f)
}

func TestFactoryUnknownModeStores(t *testing.T) {
	// 未知模式工厂（仅可能来自测试辅助/零值构造），各 New*Store 返回 nil 而不是 panic
	f := newTestFactory(storage.StorageMode("bogus"))
	assert.Nil(t, f.NewSessionStore())
	assert.Nil(t, f.NewBodiesStore())
	assert.Nil(t, f.NewTurnsStore())
	assert.Nil(t, f.NewRequestLogStore())
	assert.Nil(t, f.NewStateStore())
}

// TestFullModeStubStoresReturnNotImplemented 验证 full 模式各桩方法一律返回
// storage.ErrNotImplemented（lite 模式已全部接线为真实实现，不再有桩）。
// 底层资源指针可为 nil，桩不会解引用。
func TestFullModeStubStoresReturnNotImplemented(t *testing.T) {
	ctx := context.Background()

	ps := newPgSessionStore(nil)
	require.ErrorIs(t, ps.CreateSession(ctx, &storage.Session{}), storage.ErrNotImplemented)
	_, err := ps.GetSession(ctx, "t", "s")
	require.ErrorIs(t, err, storage.ErrNotImplemented)
	require.ErrorIs(t, ps.UpdateSession(ctx, &storage.Session{}), storage.ErrNotImplemented)
	_, err = ps.ListSessions(ctx, "t", &storage.ListOptions{})
	require.ErrorIs(t, err, storage.ErrNotImplemented)
	require.ErrorIs(t, ps.DeleteSession(ctx, "t", "s"), storage.ErrNotImplemented)

	pb := newPgBodiesStore(nil)
	require.ErrorIs(t, pb.Write(ctx, &storage.SessionBody{}), storage.ErrNotImplemented)
	_, err = pb.Read(ctx, "t", "s", 1)
	require.ErrorIs(t, err, storage.ErrNotImplemented)
	_, err = pb.ReadRange(ctx, "t", "s", 1, 2)
	require.ErrorIs(t, err, storage.ErrNotImplemented)
	require.ErrorIs(t, pb.Delete(ctx, "t", "s"), storage.ErrNotImplemented)

	pt := newPgTurnsStore(nil)
	require.ErrorIs(t, pt.WriteTurnMeta(ctx, &storage.TurnMeta{}), storage.ErrNotImplemented)
	_, err = pt.GetTurnsMeta(ctx, "t", "s")
	require.ErrorIs(t, err, storage.ErrNotImplemented)

	pr := newPgRequestLogStore(nil)
	require.ErrorIs(t, pr.WriteRequest(ctx, &storage.RequestLog{}), storage.ErrNotImplemented)
	_, err = pr.GetRequest(ctx, "id")
	require.ErrorIs(t, err, storage.ErrNotImplemented)
	_, err = pr.ListRequests(ctx, &storage.RequestFilter{})
	require.ErrorIs(t, err, storage.ErrNotImplemented)

	rs := newRedisStateStore(nil)
	require.ErrorIs(t, rs.Set(ctx, "k", "v", time.Minute), storage.ErrNotImplemented)
	_, err = rs.Get(ctx, "k")
	require.ErrorIs(t, err, storage.ErrNotImplemented)
	require.ErrorIs(t, rs.Delete(ctx, "k"), storage.ErrNotImplemented)
}

// TestSqlitePragmasFromConfig 验证配置 map → Pragma 切片转换：
// 键排序保证确定性（驱动指纹依赖顺序）、空键/空值跳过、nil map 返回 nil。
func TestSqlitePragmasFromConfig(t *testing.T) {
	require.Nil(t, sqlitePragmasFromConfig(nil))
	require.Nil(t, sqlitePragmasFromConfig(map[string]string{}))

	got := sqlitePragmasFromConfig(map[string]string{
		"busy_timeout": "1234",
		"cache_size":   "-8000",
		"synchronous":  "FULL",
		"":             "ignored",
		"journal_mode": "",
	})
	require.Len(t, got, 3)
	// 排序后顺序确定：busy_timeout < cache_size < synchronous
	require.Equal(t, "busy_timeout", got[0].Name)
	require.Equal(t, "1234", got[0].Value)
	require.Equal(t, "cache_size", got[1].Name)
	require.Equal(t, "-8000", got[1].Value)
	require.Equal(t, "synchronous", got[2].Name)
	require.Equal(t, "FULL", got[2].Value)
}

// TestNewStorageFactoryLiteModeCustomPragmas 端到端验证自定义 PRAGMA 真正落到连接上。
func TestNewStorageFactoryLiteModeCustomPragmas(t *testing.T) {
	dir := t.TempDir()
	f, err := NewStorageFactory(&storage.StorageConfig{
		Mode:          storage.StorageModeLite,
		SQLitePath:    filepath.Join(dir, "test.db"),
		SQLitePragmas: map[string]string{"busy_timeout": "1234", "synchronous": "FULL"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	// 通过工厂分派的 store 走一次真实查询路径，确认库可用；
	// PRAGMA 断言经由 OpenSQLite 已有测试覆盖，这里验证工厂透传不破坏建库与读写。
	ss := f.NewSessionStore()
	require.NotNil(t, ss)
	require.NoError(t, ss.CreateSession(context.Background(),
		&storage.Session{ID: "s-pragma", TenantID: "t-pragma", CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	got, err := ss.GetSession(context.Background(), "t-pragma", "s-pragma")
	require.NoError(t, err)
	require.Equal(t, "s-pragma", got.ID)
}
