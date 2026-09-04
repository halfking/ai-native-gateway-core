// 本文件保留 full 模式（PostgreSQL / Redis）的桩实现：所有方法一律返回
// storage.ErrNotImplemented。
//
// 架构决策：生产环境的 full 模式路径由 cmd/gateway 基于现有 pgx/redis 直接
// 装配完成，不经过本工厂，因此这里的桩保持不动；full 分支仅为结构占位与
// 工厂分派逻辑测试而保留，后续如需将 full 装配收敛到本工厂，再以真实实现
// 替换这些桩。
//
// lite 模式不使用桩：工厂已直接接线到真实实现包
// （storage/sqlite、storage/memory、storage/file），见 factory.go。
package factory

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/storage"
)

// 编译期保证各桩实现满足对应存储接口。
var (
	_ storage.SessionStore    = (*pgSessionStore)(nil)
	_ storage.BodiesStore     = (*pgBodiesStore)(nil)
	_ storage.TurnsStore      = (*pgTurnsStore)(nil)
	_ storage.RequestLogStore = (*pgRequestLogStore)(nil)
	_ storage.StateStore      = (*redisStateStore)(nil)
)

// ---- PostgreSQL 桩实现（full 模式） ----

// pgSessionStore PostgreSQL 会话元数据存储桩。
// 无状态（仅持有连接池引用），工厂可安全地每次新建。
type pgSessionStore struct {
	pool *pgxpool.Pool
}

func (s *pgSessionStore) CreateSession(ctx context.Context, session *storage.Session) error {
	return storage.ErrNotImplemented
}

func (s *pgSessionStore) GetSession(ctx context.Context, tenantID, sessionID string) (*storage.Session, error) {
	return nil, storage.ErrNotImplemented
}

func (s *pgSessionStore) UpdateSession(ctx context.Context, session *storage.Session) error {
	return storage.ErrNotImplemented
}

func (s *pgSessionStore) ListSessions(ctx context.Context, tenantID string, opts *storage.ListOptions) ([]*storage.Session, error) {
	return nil, storage.ErrNotImplemented
}

func (s *pgSessionStore) DeleteSession(ctx context.Context, tenantID, sessionID string) error {
	return storage.ErrNotImplemented
}

// pgBodiesStore PostgreSQL 会话内容存储桩。
type pgBodiesStore struct {
	pool *pgxpool.Pool
}

func (s *pgBodiesStore) Write(ctx context.Context, body *storage.SessionBody) error {
	return storage.ErrNotImplemented
}

func (s *pgBodiesStore) Read(ctx context.Context, tenantID, sessionID string, turnNo int) (*storage.SessionBody, error) {
	return nil, storage.ErrNotImplemented
}

func (s *pgBodiesStore) ReadRange(ctx context.Context, tenantID, sessionID string, startTurn, endTurn int) ([]*storage.SessionBody, error) {
	return nil, storage.ErrNotImplemented
}

func (s *pgBodiesStore) Delete(ctx context.Context, tenantID, sessionID string) error {
	return storage.ErrNotImplemented
}

// pgTurnsStore PostgreSQL 会话轮次元数据存储桩。
type pgTurnsStore struct {
	pool *pgxpool.Pool
}

func (s *pgTurnsStore) WriteTurnMeta(ctx context.Context, meta *storage.TurnMeta) error {
	return storage.ErrNotImplemented
}

func (s *pgTurnsStore) GetTurnsMeta(ctx context.Context, tenantID, sessionID string) ([]*storage.TurnMeta, error) {
	return nil, storage.ErrNotImplemented
}

// pgRequestLogStore PostgreSQL 请求日志存储桩。
type pgRequestLogStore struct {
	pool *pgxpool.Pool
}

func (s *pgRequestLogStore) WriteRequest(ctx context.Context, req *storage.RequestLog) error {
	return storage.ErrNotImplemented
}

func (s *pgRequestLogStore) GetRequest(ctx context.Context, requestID string) (*storage.RequestLog, error) {
	return nil, storage.ErrNotImplemented
}

func (s *pgRequestLogStore) ListRequests(ctx context.Context, filter *storage.RequestFilter) ([]*storage.RequestLog, error) {
	return nil, storage.ErrNotImplemented
}

// redisStateStore Redis 运行时状态存储桩。
type redisStateStore struct {
	client *redis.Client
}

func (s *redisStateStore) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	return storage.ErrNotImplemented
}

func (s *redisStateStore) Get(ctx context.Context, key string) (interface{}, error) {
	return nil, storage.ErrNotImplemented
}

func (s *redisStateStore) Delete(ctx context.Context, key string) error {
	return storage.ErrNotImplemented
}

// ---- 桩构造函数（工厂 full 分支按接口分派调用） ----

func newPgSessionStore(pool *pgxpool.Pool) storage.SessionStore {
	return &pgSessionStore{pool: pool}
}

func newPgBodiesStore(pool *pgxpool.Pool) storage.BodiesStore {
	return &pgBodiesStore{pool: pool}
}

func newPgTurnsStore(pool *pgxpool.Pool) storage.TurnsStore {
	return &pgTurnsStore{pool: pool}
}

func newPgRequestLogStore(pool *pgxpool.Pool) storage.RequestLogStore {
	return &pgRequestLogStore{pool: pool}
}

func newRedisStateStore(client *redis.Client) storage.StateStore {
	return &redisStateStore{client: client}
}
