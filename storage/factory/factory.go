// Package factory 实现双模式存储工厂：按配置将各类存储接口分派到
// full 模式（PostgreSQL + Redis，当前为桩实现）或
// lite 模式（SQLite + File + Memory，真实实现）。
//
// 架构说明：工厂独立于根 storage 包而存在——根包仅保留存储接口、数据类型
// 与哨兵错误，各实现子包（storage/sqlite、storage/memory、storage/file）反向
// 依赖根包获取这些类型。若把工厂放在根包，根包将无法 import 实现包
// （构成 import cycle），因此工厂单独成包，依赖方向为：
//
//	storage/factory ──► storage（接口/类型/错误）
//	storage/factory ──► storage/sqlite、storage/memory、storage/file（实现）
package factory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/storage"
	filestore "github.com/kaixuan/llm-gateway-go/storage/file"
	memorystore "github.com/kaixuan/llm-gateway-go/storage/memory"
	sqlitestore "github.com/kaixuan/llm-gateway-go/storage/sqlite"
)

// defaultFileWorkers lite 模式下文件内容存储的默认写 worker 数。
const defaultFileWorkers = 4

// 编译期保证：lite 模式接线到的真实实现满足对应存储接口
// （构造函数返回具体类型，此处显式断言以防实现包后续破坏接口契约）。
var (
	_ storage.SessionStore    = (*sqlitestore.SQLiteSessionStore)(nil)
	_ storage.TurnsStore      = (*sqlitestore.SQLiteTurnsStore)(nil)
	_ storage.RequestLogStore = (*sqlitestore.SQLiteRequestLogStore)(nil)
	_ storage.BodiesStore     = (*filestore.FileBodiesStore)(nil)
	_ storage.StateStore      = (*memorystore.MemoryStateStore)(nil)
)

// StorageFactory 存储工厂，按存储模式创建并持有底层连接资源，
// 负责将各类存储接口分派到对应模式的实现。
type StorageFactory struct {
	mode    storage.StorageMode
	pgPool  *pgxpool.Pool // full 模式：PostgreSQL 连接池
	sqlDB   *sql.DB       // lite 模式：SQLite 数据库
	redisDB *redis.Client // full 模式：Redis 客户端
	config  *storage.StorageConfig

	// lite 模式惰性单例（liteMu 保护）：FileBodiesStore 内部持有 AsyncFileWriter
	// 后台写协程，MemoryStateStore 持有 KV 数据与过期清理协程，二者均不可每次
	// 新建，首次创建后复用同一实例，由 Close 统一优雅关闭。
	liteMu      sync.Mutex
	bodiesStore *filestore.FileBodiesStore
	stateStore  *memorystore.MemoryStateStore

	closeOnce sync.Once
	closeErr  error
}

// NewStorageFactory 根据配置创建存储工厂。
// full 模式初始化 PostgreSQL 连接池与 Redis 客户端（均为惰性建连，不会立即发起网络请求）；
// lite 模式创建所需目录并打开 SQLite 数据库（含 Schema 初始化与 PRAGMA 配置）；
// 未知模式返回错误而不是 panic。
func NewStorageFactory(cfg *storage.StorageConfig) (*StorageFactory, error) {
	if cfg == nil {
		return nil, errors.New("storage: 存储配置不能为空")
	}

	f := &StorageFactory{config: cfg}
	switch cfg.Mode {
	case storage.StorageModeFull:
		poolCfg, err := pgxpool.ParseConfig(cfg.PostgresURL)
		if err != nil {
			return nil, fmt.Errorf("storage: 解析 PostgreSQL 连接串失败: %w", err)
		}
		if cfg.MaxConnections > 0 {
			poolCfg.MaxConns = int32(cfg.MaxConnections)
		}
		pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
		if err != nil {
			return nil, fmt.Errorf("storage: 创建 PostgreSQL 连接池失败: %w", err)
		}
		f.pgPool = pool

		redisOpts, err := redisOptionsFromURL(cfg.RedisURL)
		if err != nil {
			return nil, fmt.Errorf("storage: 解析 Redis URL 失败: %w", err)
		}
		if cfg.MaxConnections > 0 {
			redisOpts.PoolSize = cfg.MaxConnections
		}
		f.redisDB = redis.NewClient(redisOpts)

	case storage.StorageModeLite:
		if cfg.SQLitePath == "" {
			return nil, errors.New("storage: lite 模式必须配置 SQLitePath")
		}
		// 先建目录再开库（SQLite 父目录 + 三个数据目录），
		// 任一目录创建失败时无需回滚已打开的数据库。
		dirs := append([]string{filepath.Dir(cfg.SQLitePath)}, cfg.BodiesDir, cfg.CacheDir, cfg.LogsDir)
		for _, dir := range dirs {
			if dir == "" || dir == "." {
				continue
			}
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("storage: 创建目录 %s 失败: %w", dir, err)
			}
		}
		// OpenSQLite 内部完成驱动注册、DSN/ConnectHook 双通道 PRAGMA 配置
		// （WAL、busy_timeout 等）与 Schema 建表，替代此前的裸 sql.Open；
		// 配置中的 SQLitePragmas 显式项逐条覆盖默认 PRAGMA 集合。
		db, err := sqlitestore.OpenSQLite(cfg.SQLitePath, sqlitePragmasFromConfig(cfg.SQLitePragmas)...)
		if err != nil {
			return nil, fmt.Errorf("storage: 打开 SQLite 失败: %w", err)
		}
		if cfg.MaxConnections > 0 {
			db.SetMaxOpenConns(cfg.MaxConnections)
		}
		f.sqlDB = db

	default:
		return nil, fmt.Errorf("storage: 未知的存储模式 %q", cfg.Mode)
	}

	f.mode = cfg.Mode
	return f, nil
}

// redisOptionsFromURL 兼容两种 RedisURL 形态，返回 go-redis 客户端选项：
//   - redis://[user:pass@]host[:port][/db] 与 rediss://（TLS）形态 → redis.ParseURL，
//     正确拆出 Addr/Username/Password/DB（直接塞 Options.Addr 会把整个 URL
//     当主机名，导致拨号失败）；
//   - 裸 host:port → 作为 Addr 原样使用（历史行为）。
//
// 不发起任何网络连接。
// sqlitePragmasFromConfig 把配置中的 PRAGMA 覆盖项（PRAGMA 名 → 值）转换为
// sqlitestore.Pragma 切片。键按字典序排序保证输出顺序确定——OpenSQLite 会按
// PRAGMA 集合指纹注册驱动名，顺序不稳定会导致同一集合注册出不同驱动名。
func sqlitePragmasFromConfig(m map[string]string) []sqlitestore.Pragma {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pragmas := make([]sqlitestore.Pragma, 0, len(keys))
	for _, k := range keys {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(m[k]) == "" {
			continue
		}
		pragmas = append(pragmas, sqlitestore.Pragma{Name: strings.TrimSpace(k), Value: strings.TrimSpace(m[k])})
	}
	return pragmas
}

func redisOptionsFromURL(redisURL string) (*redis.Options, error) {
	u := strings.TrimSpace(redisURL)
	if strings.HasPrefix(u, "redis://") || strings.HasPrefix(u, "rediss://") {
		return redis.ParseURL(u)
	}
	return &redis.Options{Addr: u}, nil
}

// NewSessionStore 创建会话元数据存储。
// full 模式为桩实现；lite 模式为 SQLite 实现（无状态视图，仅持有共享的
// *sql.DB，可安全地每次新建）。mode 在 NewStorageFactory 中已校验，
// 未知模式（仅可能来自零值工厂）返回 nil。
func (f *StorageFactory) NewSessionStore() storage.SessionStore {
	switch f.mode {
	case storage.StorageModeFull:
		return newPgSessionStore(f.pgPool)
	case storage.StorageModeLite:
		return sqlitestore.NewSQLiteSessionStore(f.sqlDB)
	default:
		return nil
	}
}

// NewBodiesStore 创建会话内容存储（大对象）。
// full 模式为无状态桩，可每次新建；lite 模式为惰性单例（见 liteBodiesStore）。
func (f *StorageFactory) NewBodiesStore() storage.BodiesStore {
	switch f.mode {
	case storage.StorageModeFull:
		return newPgBodiesStore(f.pgPool)
	case storage.StorageModeLite:
		return f.liteBodiesStore()
	default:
		return nil
	}
}

// bodiesWorkers 返回 lite 模式 BodiesStore 的后台异步写 worker 数：
// 显式配置 AsyncWriters（>0）优先，否则回落 defaultFileWorkers。
func (f *StorageFactory) bodiesWorkers() int {
	if f.config != nil && f.config.AsyncWriters > 0 {
		return f.config.AsyncWriters
	}
	return defaultFileWorkers
}

// liteBodiesStore 返回 lite 模式会话内容存储单例。
// FileBodiesStore 内部持有 AsyncFileWriter（后台写 worker 协程 + 待写队列），
// 每次新建都会泄漏协程与队列资源，因此首次调用创建、之后复用同一实例；
// 实例由 Close 统一优雅关闭（内部 writer 排空队列后退出）。
func (f *StorageFactory) liteBodiesStore() *filestore.FileBodiesStore {
	f.liteMu.Lock()
	defer f.liteMu.Unlock()
	if f.bodiesStore == nil {
		f.bodiesStore = filestore.NewFileBodiesStore(f.config.BodiesDir, f.bodiesWorkers())
	}
	return f.bodiesStore
}

// NewTurnsStore 创建会话轮次元数据存储。
// SQLite 实现为无状态视图，可安全地每次新建。
func (f *StorageFactory) NewTurnsStore() storage.TurnsStore {
	switch f.mode {
	case storage.StorageModeFull:
		return newPgTurnsStore(f.pgPool)
	case storage.StorageModeLite:
		return sqlitestore.NewSQLiteTurnsStore(f.sqlDB)
	default:
		return nil
	}
}

// NewRequestLogStore 创建请求日志存储。
// SQLite 实现为无状态视图，可安全地每次新建。
func (f *StorageFactory) NewRequestLogStore() storage.RequestLogStore {
	switch f.mode {
	case storage.StorageModeFull:
		return newPgRequestLogStore(f.pgPool)
	case storage.StorageModeLite:
		return sqlitestore.NewSQLiteRequestLogStore(f.sqlDB)
	default:
		return nil
	}
}

// NewStateStore 创建运行时状态存储。
// full 模式使用 Redis（桩，无状态可每次新建）；lite 模式使用进程内内存实现的惰性单例。
func (f *StorageFactory) NewStateStore() storage.StateStore {
	switch f.mode {
	case storage.StorageModeFull:
		return newRedisStateStore(f.redisDB)
	case storage.StorageModeLite:
		return f.liteStateStore()
	default:
		return nil
	}
}

// liteStateStore 返回 lite 模式状态存储单例。
// MemoryStateStore 持有 KV 数据与后台过期清理协程，多实例之间数据不共享且
// 会泄漏协程，因此与会话内容存储一样做惰性单例，由 Close 统一关闭（停止清理协程）。
func (f *StorageFactory) liteStateStore() *memorystore.MemoryStateStore {
	f.liteMu.Lock()
	defer f.liteMu.Unlock()
	if f.stateStore == nil {
		f.stateStore = memorystore.NewMemoryStateStore()
	}
	return f.stateStore
}

// GetPgPool 返回 full 模式下的 PostgreSQL 连接池（lite 模式返回 nil）。
// 供主程序集成等需要直接操作连接池的场景使用。
func (f *StorageFactory) GetPgPool() *pgxpool.Pool {
	return f.pgPool
}

// GetRedisClient 返回 full 模式下的 Redis 客户端（lite 模式返回 nil）。
// 供主程序集成等需要直接操作 Redis 的场景使用。
func (f *StorageFactory) GetRedisClient() *redis.Client {
	return f.redisDB
}

// Close 关闭底层连接资源。可安全地重复调用（幂等）。
//
// 关闭顺序：先关闭 lite 模式两个惰性单例（FileBodiesStore 会排空 AsyncFileWriter
// 队列后优雅退出，保证已提交的写入全部落盘；MemoryStateStore 停止过期清理协程），
// 再关闭 SQLite，最后关闭 Redis 与 PostgreSQL 连接池。
func (f *StorageFactory) Close() error {
	f.closeOnce.Do(func() {
		var errs []error

		if f.pgPool != nil {
			f.pgPool.Close()
		}

		// 快照惰性单例后再关闭，避免与并发创建竞争
		f.liteMu.Lock()
		bodies, state := f.bodiesStore, f.stateStore
		f.liteMu.Unlock()
		if bodies != nil {
			if err := bodies.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if state != nil {
			if err := state.Close(); err != nil {
				errs = append(errs, err)
			}
		}

		if f.sqlDB != nil {
			if err := f.sqlDB.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if f.redisDB != nil {
			if err := f.redisDB.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		f.closeErr = errors.Join(errs...)
	})
	return f.closeErr
}
