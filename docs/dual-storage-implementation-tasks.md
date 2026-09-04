# LLM Gateway 双模式存储架构实施任务

## 项目概述

实现双模式存储架构，支持：
- **全量模式 (full)**: PostgreSQL + Redis（生产环境）
- **简化模式 (lite)**: SQLite + 本地文件 + 内存（本地开发）

## 并行任务组

### 🔵 Task Group 1: 核心抽象层（可并行）

---

#### Task 1.1: 存储接口定义与工厂模式

**优先级**: P0  
**预计时间**: 1天  
**依赖**: 无  
**可并行**: 是

**任务描述**:
在 `storage/` 目录下创建统一的存储抽象接口，支持双模式切换。

**具体要求**:

1. 创建 `storage/interfaces.go`，定义核心接口：
```go
package storage

import "context"

// StorageMode 存储模式
type StorageMode string

const (
    StorageModeFull StorageMode = "full" // PostgreSQL + Redis
    StorageModeLite StorageMode = "lite" // SQLite + File + Memory
)

// SessionStore 会话元数据存储
type SessionStore interface {
    CreateSession(ctx context.Context, session *Session) error
    GetSession(ctx context.Context, tenantID, sessionID string) (*Session, error)
    UpdateSession(ctx context.Context, session *Session) error
    ListSessions(ctx context.Context, tenantID string, opts *ListOptions) ([]*Session, error)
    DeleteSession(ctx context.Context, tenantID, sessionID string) error
}

// BodiesStore 会话内容存储（大对象）
type BodiesStore interface {
    Write(ctx context.Context, body *SessionBody) error
    Read(ctx context.Context, tenantID, sessionID string, turnNo int) (*SessionBody, error)
    ReadRange(ctx context.Context, tenantID, sessionID string, startTurn, endTurn int) ([]*SessionBody, error)
    Delete(ctx context.Context, tenantID, sessionID string) error
}

// TurnsStore 会话轮次元数据存储
type TurnsStore interface {
    WriteTurnMeta(ctx context.Context, meta *TurnMeta) error
    GetTurnsMeta(ctx context.Context, tenantID, sessionID string) ([]*TurnMeta, error)
}

// RequestLogStore 请求日志存储
type RequestLogStore interface {
    WriteRequest(ctx context.Context, req *RequestLog) error
    GetRequest(ctx context.Context, requestID string) (*RequestLog, error)
    ListRequests(ctx context.Context, filter *RequestFilter) ([]*RequestLog, error)
}

// StateStore 运行时状态存储
type StateStore interface {
    Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error
    Get(ctx context.Context, key string) (interface{}, error)
    Delete(ctx context.Context, key string) error
}
```

2. 创建 `storage/factory.go`，实现工厂模式：
```go
package storage

import (
    "database/sql"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/redis/go-redis/v9"
)

type StorageFactory struct {
    mode    StorageMode
    pgPool  *pgxpool.Pool
    sqlDB   *sql.DB
    redisDB *redis.Client
    config  *StorageConfig
}

type StorageConfig struct {
    Mode         StorageMode
    
    // Full mode
    PostgresURL  string
    RedisURL     string
    
    // Lite mode
    SQLitePath   string
    BodiesDir    string
    CacheDir     string
    LogsDir      string
    
    // Common
    MaxConnections int
    Timeout        time.Duration
}

func NewStorageFactory(cfg *StorageConfig) (*StorageFactory, error) {
    factory := &StorageFactory{
        mode:   cfg.Mode,
        config: cfg,
    }
    
    switch cfg.Mode {
    case StorageModeFull:
        // 初始化 PostgreSQL
        pgPool, err := pgxpool.New(context.Background(), cfg.PostgresURL)
        if err != nil {
            return nil, fmt.Errorf("init postgres: %w", err)
        }
        factory.pgPool = pgPool
        
        // 初始化 Redis
        redisDB := redis.NewClient(&redis.Options{Addr: cfg.RedisURL})
        factory.redisDB = redisDB
        
    case StorageModeLite:
        // 初始化 SQLite
        sqlDB, err := sql.Open("sqlite3", cfg.SQLitePath)
        if err != nil {
            return nil, fmt.Errorf("init sqlite: %w", err)
        }
        factory.sqlDB = sqlDB
        
        // 创建必要的目录
        for _, dir := range []string{cfg.BodiesDir, cfg.CacheDir, cfg.LogsDir} {
            if err := os.MkdirAll(dir, 0755); err != nil {
                return nil, fmt.Errorf("create dir %s: %w", dir, err)
            }
        }
    }
    
    return factory, nil
}

func (f *StorageFactory) NewSessionStore() SessionStore {
    switch f.mode {
    case StorageModeFull:
        return newPgSessionStore(f.pgPool)
    case StorageModeLite:
        return newSQLiteSessionStore(f.sqlDB)
    default:
        panic("unknown storage mode")
    }
}

func (f *StorageFactory) NewBodiesStore() BodiesStore {
    switch f.mode {
    case StorageModeFull:
        return newPgBodiesStore(f.pgPool)
    case StorageModeLite:
        return newFileBodiesStore(f.config.BodiesDir, 4) // 4 workers
    default:
        panic("unknown storage mode")
    }
}

func (f *StorageFactory) NewTurnsStore() TurnsStore {
    switch f.mode {
    case StorageModeFull:
        return newPgTurnsStore(f.pgPool)
    case StorageModeLite:
        return newSQLiteTurnsStore(f.sqlDB)
    default:
        panic("unknown storage mode")
    }
}

func (f *StorageFactory) NewStateStore() StateStore {
    switch f.mode {
    case StorageModeFull:
        return newRedisStateStore(f.redisDB)
    case StorageModeLite:
        return newMemoryStateStore()
    default:
        panic("unknown storage mode")
    }
}

func (f *StorageFactory) Close() error {
    switch f.mode {
    case StorageModeFull:
        if f.pgPool != nil {
            f.pgPool.Close()
        }
        if f.redisDB != nil {
            f.redisDB.Close()
        }
    case StorageModeLite:
        if f.sqlDB != nil {
            f.sqlDB.Close()
        }
    }
    return nil
}
```

3. 创建 `storage/types.go`，定义通用数据类型：
```go
package storage

import "time"

type Session struct {
    ID        string
    TenantID  string
    UserID    string
    CreatedAt time.Time
    UpdatedAt time.Time
    Metadata  map[string]interface{}
}

type SessionBody struct {
    TenantID  string
    SessionID string
    TurnNo    int
    Timestamp time.Time
    Request   json.RawMessage
    Response  json.RawMessage
    Metadata  map[string]interface{}
}

type TurnMeta struct {
    TenantID             string
    SessionID            string
    TurnNo               int
    Timestamp            time.Time
    CompressionStrategy  string
    PromptTokens         int
    CompletionTokens     int
}

type RequestLog struct {
    RequestID   string
    TenantID    string
    SessionID   string
    Timestamp   time.Time
    Method      string
    Path        string
    StatusCode  int
    Duration    time.Duration
    Body        json.RawMessage
}

type ListOptions struct {
    Limit  int
    Offset int
    Sort   string
}

type RequestFilter struct {
    TenantID  string
    SessionID string
    StartTime time.Time
    EndTime   time.Time
    Limit     int
}
```

**验收标准**:
- [ ] 接口定义清晰，支持双模式
- [ ] 工厂模式正确实现，能根据配置创建不同存储实现
- [ ] 代码通过 `go build` 编译
- [ ] 添加单元测试验证工厂创建逻辑

**输出文件**:
- `storage/interfaces.go`
- `storage/factory.go`
- `storage/types.go`
- `storage/factory_test.go`

---

#### Task 1.2: SQLite Schema 设计与初始化

**优先级**: P0  
**预计时间**: 1天  
**依赖**: 无  
**可并行**: 是

**任务描述**:
设计并实现 SQLite 数据库 Schema，支持会话、轮次、请求日志的元数据存储。

**具体要求**:

1. 创建 `storage/sqlite/schema.go`：
```go
package sqlite

const SchemaSQL = `
-- 会话表
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    user_id TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    metadata TEXT,  -- JSON
    UNIQUE(tenant_id, id)
);

CREATE INDEX IF NOT EXISTS idx_sessions_tenant ON sessions(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id, created_at DESC);

-- 会话轮次元数据表
CREATE TABLE IF NOT EXISTS session_turns (
    tenant_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    turn_no INTEGER NOT NULL,
    ts INTEGER NOT NULL,
    compression_strategy TEXT,
    prompt_tokens INTEGER DEFAULT 0,
    completion_tokens INTEGER DEFAULT 0,
    metadata TEXT,  -- JSON
    PRIMARY KEY (tenant_id, session_id, turn_no)
);

CREATE INDEX IF NOT EXISTS idx_turns_session ON session_turns(session_id, turn_no);

-- 请求日志元数据表
CREATE TABLE IF NOT EXISTS request_logs (
    request_id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    session_id TEXT,
    ts INTEGER NOT NULL,
    method TEXT NOT NULL,
    path TEXT NOT NULL,
    status_code INTEGER,
    duration_ms INTEGER,
    has_body INTEGER DEFAULT 0,  -- 是否有body文件
    UNIQUE(tenant_id, request_id)
);

CREATE INDEX IF NOT EXISTS idx_logs_tenant_time ON request_logs(tenant_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_logs_session ON request_logs(session_id, ts DESC);

-- 配置表
CREATE TABLE IF NOT EXISTS configs (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
`

func InitSchema(db *sql.DB) error {
    _, err := db.Exec(SchemaSQL)
    if err != nil {
        return fmt.Errorf("init schema: %w", err)
    }
    
    // 设置 SQLite 优化参数
    pragmas := []string{
        "PRAGMA journal_mode=WAL",        // 写不阻塞读
        "PRAGMA synchronous=NORMAL",      // 平衡性能和安全
        "PRAGMA cache_size=-64000",       // 64MB 缓存
        "PRAGMA temp_store=MEMORY",       // 临时表使用内存
        "PRAGMA busy_timeout=5000",       // 5秒锁等待
        "PRAGMA foreign_keys=ON",         // 启用外键
    }
    
    for _, pragma := range pragmas {
        if _, err := db.Exec(pragma); err != nil {
            return fmt.Errorf("exec pragma %s: %w", pragma, err)
        }
    }
    
    return nil
}
```

2. 创建 `storage/sqlite/session_store.go`，实现 SessionStore 接口：
```go
package sqlite

import (
    "context"
    "database/sql"
    "encoding/json"
    _ "github.com/mattn/go-sqlite3"
)

type SQLiteSessionStore struct {
    db *sql.DB
}

func NewSQLiteSessionStore(db *sql.DB) *SQLiteSessionStore {
    return &SQLiteSessionStore{db: db}
}

func (s *SQLiteSessionStore) CreateSession(ctx context.Context, session *Session) error {
    metaJSON, _ := json.Marshal(session.Metadata)
    
    _, err := s.db.ExecContext(ctx, `
        INSERT INTO sessions (id, tenant_id, user_id, created_at, updated_at, metadata)
        VALUES (?, ?, ?, ?, ?, ?)
    `, session.ID, session.TenantID, session.UserID, 
       session.CreatedAt.Unix(), session.UpdatedAt.Unix(), metaJSON)
    
    return err
}

func (s *SQLiteSessionStore) GetSession(ctx context.Context, tenantID, sessionID string) (*Session, error) {
    var session Session
    var metaJSON string
    var createdAt, updatedAt int64
    
    err := s.db.QueryRowContext(ctx, `
        SELECT id, tenant_id, user_id, created_at, updated_at, metadata
        FROM sessions
        WHERE tenant_id = ? AND id = ?
    `, tenantID, sessionID).Scan(
        &session.ID, &session.TenantID, &session.UserID,
        &createdAt, &updatedAt, &metaJSON,
    )
    
    if err != nil {
        return nil, err
    }
    
    session.CreatedAt = time.Unix(createdAt, 0)
    session.UpdatedAt = time.Unix(updatedAt, 0)
    json.Unmarshal([]byte(metaJSON), &session.Metadata)
    
    return &session, nil
}

func (s *SQLiteSessionStore) UpdateSession(ctx context.Context, session *Session) error {
    metaJSON, _ := json.Marshal(session.Metadata)
    
    _, err := s.db.ExecContext(ctx, `
        UPDATE sessions
        SET user_id = ?, updated_at = ?, metadata = ?
        WHERE tenant_id = ? AND id = ?
    `, session.UserID, time.Now().Unix(), metaJSON, session.TenantID, session.ID)
    
    return err
}

func (s *SQLiteSessionStore) DeleteSession(ctx context.Context, tenantID, sessionID string) error {
    _, err := s.db.ExecContext(ctx, `
        DELETE FROM sessions WHERE tenant_id = ? AND id = ?
    `, tenantID, sessionID)
    return err
}

func (s *SQLiteSessionStore) ListSessions(ctx context.Context, tenantID string, opts *ListOptions) ([]*Session, error) {
    if opts == nil {
        opts = &ListOptions{Limit: 100}
    }
    
    rows, err := s.db.QueryContext(ctx, `
        SELECT id, tenant_id, user_id, created_at, updated_at, metadata
        FROM sessions
        WHERE tenant_id = ?
        ORDER BY created_at DESC
        LIMIT ? OFFSET ?
    `, tenantID, opts.Limit, opts.Offset)
    
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    
    var sessions []*Session
    for rows.Next() {
        var session Session
        var metaJSON string
        var createdAt, updatedAt int64
        
        if err := rows.Scan(&session.ID, &session.TenantID, &session.UserID,
            &createdAt, &updatedAt, &metaJSON); err != nil {
            return nil, err
        }
        
        session.CreatedAt = time.Unix(createdAt, 0)
        session.UpdatedAt = time.Unix(updatedAt, 0)
        json.Unmarshal([]byte(metaJSON), &session.Metadata)
        
        sessions = append(sessions, &session)
    }
    
    return sessions, nil
}
```

3. 创建集成测试 `storage/sqlite/session_store_test.go`

**验收标准**:
- [ ] Schema 创建成功，包含所有必要的索引
- [ ] PRAGMA 优化参数正确设置
- [ ] SessionStore 所有方法实现并通过测试
- [ ] 支持并发读写（通过 WAL 模式）

**输出文件**:
- `storage/sqlite/schema.go`
- `storage/sqlite/session_store.go`
- `storage/sqlite/turns_store.go`
- `storage/sqlite/session_store_test.go`

---

#### Task 1.3: 内存状态存储实现

**优先级**: P1  
**预计时间**: 0.5天  
**依赖**: Task 1.1  
**可并行**: 是

**任务描述**:
实现基于 sync.Map 的内存状态存储，替代简化模式下的 Redis。

**具体要求**:

1. 创建 `storage/memory/state_store.go`：
```go
package memory

import (
    "context"
    "sync"
    "time"
)

type item struct {
    value      interface{}
    expiration time.Time
}

type MemoryStateStore struct {
    data    sync.Map
    cleaner *time.Ticker
    stop    chan struct{}
}

func NewMemoryStateStore() *MemoryStateStore {
    store := &MemoryStateStore{
        cleaner: time.NewTicker(1 * time.Minute),
        stop:    make(chan struct{}),
    }
    
    // 启动清理协程
    go store.cleanExpired()
    
    return store
}

func (s *MemoryStateStore) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
    exp := time.Now().Add(ttl)
    s.data.Store(key, &item{
        value:      value,
        expiration: exp,
    })
    return nil
}

func (s *MemoryStateStore) Get(ctx context.Context, key string) (interface{}, error) {
    val, ok := s.data.Load(key)
    if !ok {
        return nil, ErrNotFound
    }
    
    item := val.(*item)
    if time.Now().After(item.expiration) {
        s.data.Delete(key)
        return nil, ErrExpired
    }
    
    return item.value, nil
}

func (s *MemoryStateStore) Delete(ctx context.Context, key string) error {
    s.data.Delete(key)
    return nil
}

func (s *MemoryStateStore) cleanExpired() {
    for {
        select {
        case <-s.cleaner.C:
            now := time.Now()
            s.data.Range(func(key, value interface{}) bool {
                item := value.(*item)
                if now.After(item.expiration) {
                    s.data.Delete(key)
                }
                return true
            })
        case <-s.stop:
            return
        }
    }
}

func (s *MemoryStateStore) Close() error {
    s.cleaner.Stop()
    close(s.stop)
    return nil
}
```

2. 添加完整的单元测试，包括并发测试

**验收标准**:
- [ ] 支持 Set/Get/Delete 操作
- [ ] TTL 过期自动清理
- [ ] 并发安全（通过 sync.Map）
- [ ] 通过压力测试（1000并发读写）

**输出文件**:
- `storage/memory/state_store.go`
- `storage/memory/state_store_test.go`

---

### 🟢 Task Group 2: 文件存储实现（可并行）

---

#### Task 2.1: 异步文件写入器

**优先级**: P0  
**预计时间**: 1.5天  
**依赖**: 无  
**可并行**: 是

**任务描述**:
实现高性能的异步文件写入器，支持批量写入、错误重试、优雅关闭。

**具体要求**:

1. 创建 `storage/file/async_writer.go`：
```go
package file

import (
    "context"
    "fmt"
    "os"
    "path/filepath"
    "sync"
    "time"
)

type WriteTask struct {
    Path string
    Data []byte
    Done chan error
}

type AsyncFileWriter struct {
    queue      chan *WriteTask
    workers    int
    wg         sync.WaitGroup
    ctx        context.Context
    cancel     context.CancelFunc
    
    // 统计信息
    totalWrites  uint64
    totalBytes   uint64
    failedWrites uint64
    mu           sync.Mutex
}

func NewAsyncFileWriter(workers int) *AsyncFileWriter {
    ctx, cancel := context.WithCancel(context.Background())
    
    writer := &AsyncFileWriter{
        queue:   make(chan *WriteTask, 1000), // 缓冲1000个任务
        workers: workers,
        ctx:     ctx,
        cancel:  cancel,
    }
    
    // 启动 worker 协程
    for i := 0; i < workers; i++ {
        writer.wg.Add(1)
        go writer.worker(i)
    }
    
    return writer
}

func (w *AsyncFileWriter) Write(path string, data []byte) error {
    task := &WriteTask{
        Path: path,
        Data: data,
        Done: make(chan error, 1),
    }
    
    select {
    case w.queue <- task:
        // 等待写入完成
        return <-task.Done
    case <-w.ctx.Done():
        return fmt.Errorf("writer closed")
    }
}

func (w *AsyncFileWriter) WriteAsync(path string, data []byte) <-chan error {
    task := &WriteTask{
        Path: path,
        Data: data,
        Done: make(chan error, 1),
    }
    
    w.queue <- task
    return task.Done
}

func (w *AsyncFileWriter) worker(id int) {
    defer w.wg.Done()
    
    for {
        select {
        case task := <-w.queue:
            err := w.doWrite(task)
            task.Done <- err
            close(task.Done)
            
        case <-w.ctx.Done():
            return
        }
    }
}

func (w *AsyncFileWriter) doWrite(task *WriteTask) error {
    // 确保目录存在
    dir := filepath.Dir(task.Path)
    if err := os.MkdirAll(dir, 0755); err != nil {
        w.recordFailure()
        return fmt.Errorf("mkdir %s: %w", dir, err)
    }
    
    // 写入临时文件，然后原子性重命名
    tmpPath := task.Path + ".tmp"
    if err := os.WriteFile(tmpPath, task.Data, 0644); err != nil {
        w.recordFailure()
        return fmt.Errorf("write tmp file: %w", err)
    }
    
    if err := os.Rename(tmpPath, task.Path); err != nil {
        os.Remove(tmpPath)
        w.recordFailure()
        return fmt.Errorf("rename file: %w", err)
    }
    
    w.recordSuccess(len(task.Data))
    return nil
}

func (w *AsyncFileWriter) recordSuccess(bytes int) {
    w.mu.Lock()
    w.totalWrites++
    w.totalBytes += uint64(bytes)
    w.mu.Unlock()
}

func (w *AsyncFileWriter) recordFailure() {
    w.mu.Lock()
    w.failedWrites++
    w.mu.Unlock()
}

func (w *AsyncFileWriter) Stats() map[string]uint64 {
    w.mu.Lock()
    defer w.mu.Unlock()
    
    return map[string]uint64{
        "total_writes":  w.totalWrites,
        "total_bytes":   w.totalBytes,
        "failed_writes": w.failedWrites,
    }
}

func (w *AsyncFileWriter) Close() error {
    w.cancel()
    
    // 等待所有 worker 完成
    done := make(chan struct{})
    go func() {
        w.wg.Wait()
        close(done)
    }()
    
    select {
    case <-done:
        return nil
    case <-time.After(30 * time.Second):
        return fmt.Errorf("timeout waiting for workers to finish")
    }
}
```

2. 添加压力测试 `async_writer_bench_test.go`

**验收标准**:
- [ ] 支持异步写入，主线程不阻塞
- [ ] 原子性写入（临时文件+重命名）
- [ ] 优雅关闭，等待所有任务完成
- [ ] 性能测试：4 workers 下 >5000 writes/s

**输出文件**:
- `storage/file/async_writer.go`
- `storage/file/async_writer_test.go`
- `storage/file/async_writer_bench_test.go`

---

#### Task 2.2: Bodies 文件存储实现

**优先级**: P0  
**预计时间**: 2天  
**依赖**: Task 2.1  
**可并行**: 部分（可先实现接口，等 Task 2.1 完成后集成）

**任务描述**:
实现基于本地文件的 BodiesStore，支持 gzip 压缩、分层目录、异步写入。

**具体要求**:

1. 创建 `storage/file/bodies_store.go`：
```go
package file

import (
    "bytes"
    "compress/gzip"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "path/filepath"
)

type FileBodiesStore struct {
    baseDir string
    writer  *AsyncFileWriter
}

func NewFileBodiesStore(baseDir string, workers int) *FileBodiesStore {
    return &FileBodiesStore{
        baseDir: baseDir,
        writer:  NewAsyncFileWriter(workers),
    }
}

// buildPath 构建文件路径: {baseDir}/{tenantID}/{sessionID[:2]}/{sessionID}/turn_{turnNo}.json.gz
func (s *FileBodiesStore) buildPath(tenantID, sessionID string, turnNo int) string {
    prefix := sessionID
    if len(sessionID) >= 2 {
        prefix = sessionID[:2]
    }
    
    return filepath.Join(
        s.baseDir,
        tenantID,
        prefix,
        sessionID,
        fmt.Sprintf("turn_%d.json.gz", turnNo),
    )
}

func (s *FileBodiesStore) Write(ctx context.Context, body *SessionBody) error {
    // JSON 序列化
    data, err := json.Marshal(body)
    if err != nil {
        return fmt.Errorf("marshal body: %w", err)
    }
    
    // gzip 压缩
    var buf bytes.Buffer
    gw := gzip.NewWriter(&buf)
    if _, err := gw.Write(data); err != nil {
        return fmt.Errorf("gzip write: %w", err)
    }
    if err := gw.Close(); err != nil {
        return fmt.Errorf("gzip close: %w", err)
    }
    
    // 异步写入文件
    path := s.buildPath(body.TenantID, body.SessionID, body.TurnNo)
    return s.writer.Write(path, buf.Bytes())
}

func (s *FileBodiesStore) Read(ctx context.Context, tenantID, sessionID string, turnNo int) (*SessionBody, error) {
    path := s.buildPath(tenantID, sessionID, turnNo)
    
    // 读取文件
    data, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            return nil, ErrNotFound
        }
        return nil, fmt.Errorf("read file: %w", err)
    }
    
    // gzip 解压
    gr, err := gzip.NewReader(bytes.NewReader(data))
    if err != nil {
        return nil, fmt.Errorf("gzip reader: %w", err)
    }
    defer gr.Close()
    
    decompressed, err := io.ReadAll(gr)
    if err != nil {
        return nil, fmt.Errorf("gzip read: %w", err)
    }
    
    // JSON 反序列化
    var body SessionBody
    if err := json.Unmarshal(decompressed, &body); err != nil {
        return nil, fmt.Errorf("unmarshal body: %w", err)
    }
    
    return &body, nil
}

func (s *FileBodiesStore) ReadRange(ctx context.Context, tenantID, sessionID string, startTurn, endTurn int) ([]*SessionBody, error) {
    var bodies []*SessionBody
    
    for turn := startTurn; turn <= endTurn; turn++ {
        body, err := s.Read(ctx, tenantID, sessionID, turn)
        if err != nil {
            if err == ErrNotFound {
                continue // 跳过不存在的 turn
            }
            return nil, err
        }
        bodies = append(bodies, body)
    }
    
    return bodies, nil
}

func (s *FileBodiesStore) Delete(ctx context.Context, tenantID, sessionID string) error {
    // 删除整个会话目录
    prefix := sessionID
    if len(sessionID) >= 2 {
        prefix = sessionID[:2]
    }
    
    sessionDir := filepath.Join(s.baseDir, tenantID, prefix, sessionID)
    return os.RemoveAll(sessionDir)
}

func (s *FileBodiesStore) Close() error {
    return s.writer.Close()
}
```

2. 添加集成测试，包括：
   - 写入 → 读取验证
   - 压缩率测试（应达到 70%+）
   - 并发读写测试

**验收标准**:
- [ ] 正确的分层目录结构
- [ ] gzip 压缩率 >70%
- [ ] 支持单条和范围读取
- [ ] 删除操作清理整个会话目录
- [ ] 通过并发测试（100并发写入）

**输出文件**:
- `storage/file/bodies_store.go`
- `storage/file/bodies_store_test.go`

---

#### Task 2.3: L1.5 本地文件缓存实现

**优先级**: P1  
**预计时间**: 1.5天  
**依赖**: 无  
**可并行**: 是

**任务描述**:
实现 L1.5 本地文件缓存层，介于内存 L1 和数据库 L3 之间，支持 TTL、空间限制、LRU 清理。

**具体要求**:

1. 创建 `domains/session/v2/cache_v2_file.go`：
```go
package session

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "sync"
    "time"
)

type FileCache struct {
    baseDir  string
    ttl      time.Duration
    maxSize  int64  // 最大空间（字节）
    sizeUsed int64  // 当前使用空间
    mu       sync.RWMutex
}

func NewFileCache(baseDir string, ttl time.Duration, maxSize int64) (*FileCache, error) {
    if err := os.MkdirAll(baseDir, 0755); err != nil {
        return nil, fmt.Errorf("create cache dir: %w", err)
    }
    
    fc := &FileCache{
        baseDir: baseDir,
        ttl:     ttl,
        maxSize: maxSize,
    }
    
    // 计算当前使用空间
    fc.calculateSize()
    
    return fc, nil
}

func (fc *FileCache) buildPath(tenantID, sessionID string) string {
    prefix := sessionID
    if len(sessionID) >= 2 {
        prefix = sessionID[:2]
    }
    
    return filepath.Join(fc.baseDir, tenantID, prefix, sessionID+".json")
}

func (fc *FileCache) Get(tenantID, sessionID string) (*SessionStateV2, error) {
    path := fc.buildPath(tenantID, sessionID)
    
    // 检查文件是否存在
    stat, err := os.Stat(path)
    if err != nil {
        if os.IsNotExist(err) {
            return nil, ErrNotFound
        }
        return nil, err
    }
    
    // 检查 TTL
    if time.Since(stat.ModTime()) > fc.ttl {
        os.Remove(path)
        fc.mu.Lock()
        fc.sizeUsed -= stat.Size()
        fc.mu.Unlock()
        return nil, ErrExpired
    }
    
    // 读取文件
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, err
    }
    
    var state SessionStateV2
    if err := json.Unmarshal(data, &state); err != nil {
        return nil, err
    }
    
    return &state, nil
}

func (fc *FileCache) Set(state *SessionStateV2) error {
    data, err := json.Marshal(state)
    if err != nil {
        return err
    }
    
    path := fc.buildPath(state.TenantID, state.SessionID)
    
    // 检查空间并清理
    fc.ensureSpace(int64(len(data)))
    
    // 创建目录
    dir := filepath.Dir(path)
    if err := os.MkdirAll(dir, 0755); err != nil {
        return err
    }
    
    // 写入文件
    if err := os.WriteFile(path, data, 0644); err != nil {
        return err
    }
    
    fc.mu.Lock()
    fc.sizeUsed += int64(len(data))
    fc.mu.Unlock()
    
    return nil
}

func (fc *FileCache) Delete(tenantID, sessionID string) error {
    path := fc.buildPath(tenantID, sessionID)
    
    stat, err := os.Stat(path)
    if err != nil {
        return nil // 文件不存在，忽略
    }
    
    if err := os.Remove(path); err != nil {
        return err
    }
    
    fc.mu.Lock()
    fc.sizeUsed -= stat.Size()
    fc.mu.Unlock()
    
    return nil
}

func (fc *FileCache) ensureSpace(needed int64) {
    fc.mu.Lock()
    defer fc.mu.Unlock()
    
    // 如果空间充足，直接返回
    if fc.sizeUsed+needed <= fc.maxSize {
        return
    }
    
    // 需要清理旧文件
    var files []struct {
        path    string
        modTime time.Time
        size    int64
    }
    
    // 遍历所有缓存文件
    filepath.Walk(fc.baseDir, func(path string, info os.FileInfo, err error) error {
        if err != nil || info.IsDir() {
            return nil
        }
        
        files = append(files, struct {
            path    string
            modTime time.Time
            size    int64
        }{path, info.ModTime(), info.Size()})
        
        return nil
    })
    
    // 按修改时间排序（最旧的在前）
    sort.Slice(files, func(i, j int) bool {
        return files[i].modTime.Before(files[j].modTime)
    })
    
    // 删除最旧的文件，直到空间足够
    for _, f := range files {
        if fc.sizeUsed+needed <= fc.maxSize {
            break
        }
        
        os.Remove(f.path)
        fc.sizeUsed -= f.size
    }
}

func (fc *FileCache) calculateSize() {
    var totalSize int64
    
    filepath.Walk(fc.baseDir, func(path string, info os.FileInfo, err error) error {
        if err != nil || info.IsDir() {
            return nil
        }
        totalSize += info.Size()
        return nil
    })
    
    fc.mu.Lock()
    fc.sizeUsed = totalSize
    fc.mu.Unlock()
}

func (fc *FileCache) Stats() map[string]interface{} {
    fc.mu.RLock()
    defer fc.mu.RUnlock()
    
    return map[string]interface{}{
        "size_used_bytes": fc.sizeUsed,
        "max_size_bytes":  fc.maxSize,
        "usage_percent":   float64(fc.sizeUsed) / float64(fc.maxSize) * 100,
    }
}
```

2. 添加单元测试和空间限制测试

**验收标准**:
- [ ] 支持 Get/Set/Delete 操作
- [ ] TTL 过期自动清理
- [ ] 空间限制生效，超出时 LRU 清理
- [ ] 通过压力测试（10000个会话缓存）

**输出文件**:
- `domains/session/v2/cache_v2_file.go`
- `domains/session/v2/cache_v2_file_test.go`

---

### 🟡 Task Group 3: 缓存层集成（依赖 Group 1 & 2）

---

#### Task 3.1: SessionCacheV2 多层缓存集成

**优先级**: P0  
**预计时间**: 2天  
**依赖**: Task 1.1, Task 2.3  
**可并行**: 否

**任务描述**:
修改 `SessionCacheV2`，集成 L1.5 本地文件缓存，实现 L1 → L1.5 → L2 → L3 的多层缓存架构。

**具体要求**:

1. 修改 `domains/session/v2/cache_v2.go`：
```go
package session

import (
    "context"
    "time"
)

type SessionCacheV2 struct {
    l1   *CompressionMetaCache  // L1: 内存 LRU (1024条)
    l1_5 *FileCache              // L1.5: 本地文件缓存 (10K+条) - 新增
    l2   RedisCache              // L2: Redis (全量模式)
    l3   *SessionTurnsReader     // L3: 数据库/文件冷启动
    
    mode StorageMode
}

func NewSessionCacheV2(l1 *CompressionMetaCache, l1_5 *FileCache, l2 RedisCache, l3 *SessionTurnsReader, mode StorageMode) *SessionCacheV2 {
    return &SessionCacheV2{
        l1:   l1,
        l1_5: l1_5,
        l2:   l2,
        l3:   l3,
        mode: mode,
    }
}

func (c *SessionCacheV2) Get(ctx context.Context, tenantID, sessionID string) (*SessionStateV2, error) {
    // L1: 内存 LRU
    if state := c.l1.Get(tenantID, sessionID); state != nil {
        return state, nil
    }
    
    // L1.5: 本地文件缓存（仅在 lite 模式下使用）
    if c.mode == StorageModeLite && c.l1_5 != nil {
        state, err := c.l1_5.Get(tenantID, sessionID)
        if err == nil {
            // 回填 L1
            c.l1.Set(state)
            return state, nil
        }
        // L1.5 未命中或过期，继续查找
    }
    
    // L2: Redis（仅在 full 模式下使用）
    if c.mode == StorageModeFull && c.l2 != nil {
        state := c.l2.Get(ctx, tenantID, sessionID)
        if state != nil {
            // 回填 L1 和 L1.5
            c.l1.Set(state)
            if c.l1_5 != nil {
                c.l1_5.Set(state)
            }
            return state, nil
        }
    }
    
    // L3: 数据库/文件冷启动
    state, err := c.l3.LoadState(ctx, tenantID, sessionID)
    if err != nil {
        return nil, err
    }
    
    // 回填所有缓存层
    c.l1.Set(state)
    if c.mode == StorageModeLite && c.l1_5 != nil {
        c.l1_5.Set(state)
    }
    if c.mode == StorageModeFull && c.l2 != nil {
        c.l2.Set(ctx, state)
    }
    
    return state, nil
}

func (c *SessionCacheV2) Set(ctx context.Context, state *SessionStateV2) error {
    // 更新所有层
    c.l1.Set(state)
    
    if c.mode == StorageModeLite && c.l1_5 != nil {
        if err := c.l1_5.Set(state); err != nil {
            // L1.5 失败不影响主流程，仅记录日志
            log.Warnf("L1.5 cache set failed: %v", err)
        }
    }
    
    if c.mode == StorageModeFull && c.l2 != nil {
        c.l2.Set(ctx, state)
    }
    
    return nil
}

func (c *SessionCacheV2) Delete(ctx context.Context, tenantID, sessionID string) error {
    c.l1.Delete(tenantID, sessionID)
    
    if c.mode == StorageModeLite && c.l1_5 != nil {
        c.l1_5.Delete(tenantID, sessionID)
    }
    
    if c.mode == StorageModeFull && c.l2 != nil {
        c.l2.Delete(ctx, tenantID, sessionID)
    }
    
    return nil
}

func (c *SessionCacheV2) Stats() map[string]interface{} {
    stats := map[string]interface{}{
        "l1": c.l1.Stats(),
    }
    
    if c.mode == StorageModeLite && c.l1_5 != nil {
        stats["l1_5"] = c.l1_5.Stats()
    }
    
    return stats
}
```

2. 添加集成测试，验证多层缓存回填逻辑

**验收标准**:
- [ ] L1 → L1.5 → L2 → L3 查询链路正确
- [ ] 缓存回填逻辑正确（下层命中后回填上层）
- [ ] 双模式下缓存层选择正确
- [ ] 通过集成测试

**输出文件**:
- `domains/session/v2/cache_v2.go`（修改）
- `domains/session/v2/cache_v2_integration_test.go`

---

### 🟣 Task Group 4: 配置与启动集成（依赖 Group 1, 2, 3）

---

#### Task 4.1: 配置文件设计与加载

**优先级**: P0  
**预计时间**: 1天  
**依赖**: Task 1.1  
**可并行**: 否

**任务描述**:
设计双模式配置结构，支持 YAML/ENV 配置，实现配置加载与验证。

**具体要求**:

1. 创建 `config/storage.go`：
```go
package config

import (
    "fmt"
    "time"
)

type StorageMode string

const (
    StorageModeFull StorageMode = "full"
    StorageModeLite StorageMode = "lite"
)

type StorageConfig struct {
    Mode string `yaml:"storage_mode" env:"STORAGE_MODE" envDefault:"full"`
    
    Full *FullStorageConfig `yaml:"full_storage"`
    Lite *LiteStorageConfig `yaml:"lite_storage"`
}

type FullStorageConfig struct {
    PostgresURL    string `yaml:"postgres_url" env:"POSTGRES_URL"`
    RedisURL       string `yaml:"redis_url" env:"REDIS_URL"`
    MaxConnections int    `yaml:"max_connections" env:"MAX_CONNECTIONS" envDefault:"100"`
}

type LiteStorageConfig struct {
    SQLitePath string `yaml:"sqlite_path" env:"SQLITE_PATH" envDefault:"./data/llm-gateway.db"`
    BodiesDir  string `yaml:"bodies_dir" env:"BODIES_DIR" envDefault:"./data/session_bodies"`
    CacheDir   string `yaml:"cache_dir" env:"CACHE_DIR" envDefault:"./data/cache"`
    LogsDir    string `yaml:"logs_dir" env:"LOGS_DIR" envDefault:"./data/request_logs"`
    
    SQLitePragmas struct {
        JournalMode  string `yaml:"journal_mode" envDefault:"WAL"`
        CacheSizeKB  int    `yaml:"cache_size_kb" envDefault:"64000"`
        Synchronous  string `yaml:"synchronous" envDefault:"NORMAL"`
        BusyTimeout  int    `yaml:"busy_timeout" envDefault:"5000"`
    } `yaml:"sqlite_pragmas"`
    
    CacheTTLHours   int   `yaml:"cache_ttl_hours" envDefault:"24"`
    CacheMaxSizeGB  int   `yaml:"cache_max_size_gb" envDefault:"10"`
    AsyncWriters    int   `yaml:"async_writers" envDefault:"4"`
    
    Retention struct {
        SessionBodiesDays int `yaml:"session_bodies_days" envDefault:"30"`
        RequestLogsDays   int `yaml:"request_logs_days" envDefault:"7"`
        CacheHours        int `yaml:"cache_hours" envDefault:"24"`
    } `yaml:"retention"`
}

func (c *StorageConfig) Validate() error {
    mode := StorageMode(c.Mode)
    
    switch mode {
    case StorageModeFull:
        if c.Full == nil {
            return fmt.Errorf("full_storage config required for full mode")
        }
        if c.Full.PostgresURL == "" {
            return fmt.Errorf("postgres_url required for full mode")
        }
        if c.Full.RedisURL == "" {
            return fmt.Errorf("redis_url required for full mode")
        }
        
    case StorageModeLite:
        if c.Lite == nil {
            return fmt.Errorf("lite_storage config required for lite mode")
        }
        if c.Lite.SQLitePath == "" {
            return fmt.Errorf("sqlite_path required for lite mode")
        }
        
    default:
        return fmt.Errorf("invalid storage_mode: %s (must be 'full' or 'lite')", c.Mode)
    }
    
    return nil
}
```

2. 创建配置示例文件 `config.example.yaml`：
```yaml
# 存储模式: full (生产) 或 lite (本地)
storage_mode: lite

# 全量模式配置
full_storage:
  postgres_url: "postgres://user:pass@localhost:5432/llm_gateway"
  redis_url: "localhost:6379"
  max_connections: 100

# 简化模式配置
lite_storage:
  sqlite_path: "./data/llm-gateway.db"
  bodies_dir: "./data/session_bodies"
  cache_dir: "./data/cache"
  logs_dir: "./data/request_logs"
  
  sqlite_pragmas:
    journal_mode: WAL
    cache_size_kb: 64000
    synchronous: NORMAL
    busy_timeout: 5000
  
  cache_ttl_hours: 24
  cache_max_size_gb: 10
  async_writers: 4
  
  retention:
    session_bodies_days: 30
    request_logs_days: 7
    cache_hours: 24
```

**验收标准**:
- [ ] 配置结构支持双模式
- [ ] 支持 YAML 和环境变量
- [ ] 配置验证逻辑完整
- [ ] 提供配置示例文件

**输出文件**:
- `config/storage.go`
- `config/storage_test.go`
- `config.example.yaml`

---

#### Task 4.2: 主程序启动集成

**优先级**: P0  
**预计时间**: 1.5天  
**依赖**: Task 4.1, Task 3.1  
**可并行**: 否

**任务描述**:
修改主程序，根据配置初始化对应的存储工厂和缓存层，启动后台清理任务。

**具体要求**:

1. 修改 `cmd/gateway/main.go`：
```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"
    
    "llm-gateway/config"
    "llm-gateway/storage"
    "llm-gateway/domains/session"
    "llm-gateway/bg"
)

func main() {
    // 加载配置
    cfg, err := config.Load()
    if err != nil {
        log.Fatalf("load config: %v", err)
    }
    
    // 验证配置
    if err := cfg.Storage.Validate(); err != nil {
        log.Fatalf("invalid config: %v", err)
    }
    
    log.Printf("Starting in %s storage mode...", cfg.Storage.Mode)
    
    // 初始化存储工厂
    factory, err := storage.NewStorageFactory(&storage.StorageConfig{
        Mode:         storage.StorageMode(cfg.Storage.Mode),
        PostgresURL:  cfg.Storage.Full.PostgresURL,
        RedisURL:     cfg.Storage.Full.RedisURL,
        SQLitePath:   cfg.Storage.Lite.SQLitePath,
        BodiesDir:    cfg.Storage.Lite.BodiesDir,
        CacheDir:     cfg.Storage.Lite.CacheDir,
        LogsDir:      cfg.Storage.Lite.LogsDir,
    })
    if err != nil {
        log.Fatalf("init storage factory: %v", err)
    }
    defer factory.Close()
    
    // 初始化存储实例
    sessionStore := factory.NewSessionStore()
    bodiesStore := factory.NewBodiesStore()
    turnsStore := factory.NewTurnsStore()
    stateStore := factory.NewStateStore()
    
    // 初始化缓存层
    var cacheV2 *session.SessionCacheV2
    
    if cfg.Storage.Mode == "lite" {
        // 简化模式: L1 → L1.5 → L3
        l1 := session.NewCompressionMetaCache(1024, 24*time.Hour)
        
        l1_5, err := session.NewFileCache(
            cfg.Storage.Lite.CacheDir,
            time.Duration(cfg.Storage.Lite.CacheTTLHours)*time.Hour,
            int64(cfg.Storage.Lite.CacheMaxSizeGB)*1024*1024*1024,
        )
        if err != nil {
            log.Fatalf("init L1.5 cache: %v", err)
        }
        
        l3 := session.NewSessionTurnsReader(sessionStore, bodiesStore, turnsStore)
        cacheV2 = session.NewSessionCacheV2(l1, l1_5, nil, l3, storage.StorageModeLite)
        
        // 启动后台清理任务
        cacheTrimmer := bg.NewCacheTrimmer(
            cfg.Storage.Lite.CacheDir,
            time.Duration(cfg.Storage.Lite.Retention.CacheHours)*time.Hour,
        )
        go cacheTrimmer.Start(context.Background())
        
        bodiesTrimmer := bg.NewBodiesTrimmer(
            cfg.Storage.Lite.BodiesDir,
            time.Duration(cfg.Storage.Lite.Retention.SessionBodiesDays)*24*time.Hour,
        )
        go bodiesTrimmer.Start(context.Background())
        
        log.Printf("Lite mode initialized: L1=%d, L1.5=%dGB, L3=SQLite+File",
            1024, cfg.Storage.Lite.CacheMaxSizeGB)
        
    } else {
        // 全量模式: L1 → L2 → L3
        l1 := session.NewCompressionMetaCache(1024, 24*time.Hour)
        l2 := session.NewRedisCache(factory.GetRedisClient())
        l3 := session.NewSessionTurnsReader(sessionStore, bodiesStore, turnsStore)
        
        cacheV2 = session.NewSessionCacheV2(l1, nil, l2, l3, storage.StorageModeFull)
        
        log.Printf("Full mode initialized: L1=%d, L2=Redis, L3=PostgreSQL", 1024)
    }
    
    // 初始化 HTTP 服务
    server := initHTTPServer(cfg, cacheV2, stateStore)
    
    // 优雅关闭
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    
    go func() {
        if err := server.Start(); err != nil {
            log.Fatalf("server error: %v", err)
        }
    }()
    
    <-sigChan
    log.Println("Shutting down...")
    
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    
    if err := server.Shutdown(ctx); err != nil {
        log.Printf("shutdown error: %v", err)
    }
    
    log.Println("Shutdown complete")
}
```

2. 添加启动脚本 `scripts/start-lite.sh`：
```bash
#!/bin/bash
set -e

export STORAGE_MODE=lite
export SQLITE_PATH=./data/llm-gateway.db
export BODIES_DIR=./data/session_bodies
export CACHE_DIR=./data/cache

mkdir -p ./data

echo "Starting LLM Gateway in lite mode..."
./llm-gateway
```

**验收标准**:
- [ ] 根据配置正确初始化存储工厂
- [ ] 双模式下缓存层正确创建
- [ ] 后台清理任务正确启动（仅 lite 模式）
- [ ] 优雅关闭逻辑完整
- [ ] 通过手动启动测试

**输出文件**:
- `cmd/gateway/main.go`（修改）
- `scripts/start-lite.sh`
- `scripts/start-full.sh`

---

### 🔴 Task Group 5: 后台任务与监控（依赖 Group 4）

---

#### Task 5.1: 缓存清理 Worker

**优先级**: P1  
**预计时间**: 0.5天  
**依赖**: Task 4.2  
**可并行**: 是

**任务描述**:
实现后台缓存清理 Worker，定期清理过期的 L1.5 缓存文件。

**具体要求**:

1. 创建 `bg/cache_trimmer.go`：
```go
package bg

import (
    "context"
    "log"
    "os"
    "path/filepath"
    "time"
)

type CacheTrimmer struct {
    cacheDir  string
    retention time.Duration
    interval  time.Duration
}

func NewCacheTrimmer(cacheDir string, retention time.Duration) *CacheTrimmer {
    return &CacheTrimmer{
        cacheDir:  cacheDir,
        retention: retention,
        interval:  1 * time.Hour, // 每小时清理一次
    }
}

func (t *CacheTrimmer) Start(ctx context.Context) {
    ticker := time.NewTicker(t.interval)
    defer ticker.Stop()
    
    log.Printf("Cache trimmer started: dir=%s, retention=%v", t.cacheDir, t.retention)
    
    // 立即执行一次
    t.trimOnce(ctx)
    
    for {
        select {
        case <-ticker.C:
            t.trimOnce(ctx)
        case <-ctx.Done():
            log.Println("Cache trimmer stopped")
            return
        }
    }
}

func (t *CacheTrimmer) trimOnce(ctx context.Context) {
    cutoff := time.Now().Add(-t.retention)
    var deletedCount int
    var deletedBytes int64
    
    err := filepath.Walk(t.cacheDir, func(path string, info os.FileInfo, err error) error {
        if err != nil || info.IsDir() {
            return nil
        }
        
        // 检查是否过期
        if info.ModTime().Before(cutoff) {
            if err := os.Remove(path); err != nil {
                log.Printf("Failed to remove expired cache file %s: %v", path, err)
            } else {
                deletedCount++
                deletedBytes += info.Size()
            }
        }
        
        return nil
    })
    
    if err != nil {
        log.Printf("Cache trim error: %v", err)
    }
    
    if deletedCount > 0 {
        log.Printf("Cache trim completed: deleted %d files, freed %.2f MB",
            deletedCount, float64(deletedBytes)/(1024*1024))
    }
}
```

**验收标准**:
- [ ] 定期清理过期文件
- [ ] 支持优雅关闭
- [ ] 记录清理统计信息

**输出文件**:
- `bg/cache_trimmer.go`
- `bg/cache_trimmer_test.go`

---

#### Task 5.2: Bodies 保留策略 Worker

**优先级**: P1  
**预计时间**: 0.5天  
**依赖**: Task 4.2  
**可并行**: 是（与 Task 5.1 并行）

**任务描述**:
实现后台 Bodies 清理 Worker，定期删除超过保留期的会话内容。

**具体要求**:

1. 创建 `bg/bodies_trimmer.go`：
```go
package bg

import (
    "context"
    "log"
    "os"
    "path/filepath"
    "time"
)

type BodiesTrimmer struct {
    bodiesDir string
    retention time.Duration
    interval  time.Duration
}

func NewBodiesTrimmer(bodiesDir string, retention time.Duration) *BodiesTrimmer {
    return &BodiesTrimmer{
        bodiesDir: bodiesDir,
        retention: retention,
        interval:  6 * time.Hour, // 每6小时清理一次
    }
}

func (t *BodiesTrimmer) Start(ctx context.Context) {
    ticker := time.NewTicker(t.interval)
    defer ticker.Stop()
    
    log.Printf("Bodies trimmer started: dir=%s, retention=%v", t.bodiesDir, t.retention)
    
    for {
        select {
        case <-ticker.C:
            t.trimOnce(ctx)
        case <-ctx.Done():
            log.Println("Bodies trimmer stopped")
            return
        }
    }
}

func (t *BodiesTrimmer) trimOnce(ctx context.Context) {
    cutoff := time.Now().Add(-t.retention)
    var deletedSessions int
    var deletedBytes int64
    
    // 遍历租户目录
    tenants, _ := os.ReadDir(t.bodiesDir)
    for _, tenant := range tenants {
        if !tenant.IsDir() {
            continue
        }
        
        tenantDir := filepath.Join(t.bodiesDir, tenant.Name())
        
        // 遍历前缀目录
        prefixes, _ := os.ReadDir(tenantDir)
        for _, prefix := range prefixes {
            if !prefix.IsDir() {
                continue
            }
            
            prefixDir := filepath.Join(tenantDir, prefix.Name())
            
            // 遍历会话目录
            sessions, _ := os.ReadDir(prefixDir)
            for _, session := range sessions {
                if !session.IsDir() {
                    continue
                }
                
                sessionDir := filepath.Join(prefixDir, session.Name())
                info, _ := os.Stat(sessionDir)
                
                // 检查是否过期
                if info.ModTime().Before(cutoff) {
                    size := dirSize(sessionDir)
                    if err := os.RemoveAll(sessionDir); err != nil {
                        log.Printf("Failed to remove expired session %s: %v", sessionDir, err)
                    } else {
                        deletedSessions++
                        deletedBytes += size
                    }
                }
            }
        }
    }
    
    if deletedSessions > 0 {
        log.Printf("Bodies trim completed: deleted %d sessions, freed %.2f MB",
            deletedSessions, float64(deletedBytes)/(1024*1024))
    }
}

func dirSize(path string) int64 {
    var size int64
    filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
        if err != nil || info.IsDir() {
            return nil
        }
        size += info.Size()
        return nil
    })
    return size
}
```

**验收标准**:
- [ ] 定期清理过期会话目录
- [ ] 支持优雅关闭
- [ ] 记录清理统计信息

**输出文件**:
- `bg/bodies_trimmer.go`
- `bg/bodies_trimmer_test.go`

---

#### Task 5.3: 监控指标暴露

**优先级**: P2  
**预计时间**: 1天  
**依赖**: Task 4.2  
**可并行**: 是

**任务描述**:
实现监控指标接口，暴露存储层的关键指标（缓存命中率、存储空间使用等）。

**具体要求**:

1. 创建 `monitoring/storage_metrics.go`：
```go
package monitoring

import (
    "sync/atomic"
    "time"
)

type StorageMetrics struct {
    // L1 缓存指标
    l1Hits   uint64
    l1Misses uint64
    
    // L1.5 缓存指标
    l1_5Hits   uint64
    l1_5Misses uint64
    
    // L2 缓存指标
    l2Hits   uint64
    l2Misses uint64
    
    // L3 查询指标
    l3Queries   uint64
    l3Latency   uint64  // 总延迟（微秒）
    
    // 写入指标
    writes      uint64
    writesBytes uint64
    writeErrors uint64
    
    startTime time.Time
}

func NewStorageMetrics() *StorageMetrics {
    return &StorageMetrics{
        startTime: time.Now(),
    }
}

func (m *StorageMetrics) RecordL1Hit()   { atomic.AddUint64(&m.l1Hits, 1) }
func (m *StorageMetrics) RecordL1Miss()  { atomic.AddUint64(&m.l1Misses, 1) }
func (m *StorageMetrics) RecordL1_5Hit() { atomic.AddUint64(&m.l1_5Hits, 1) }
func (m *StorageMetrics) RecordL1_5Miss() { atomic.AddUint64(&m.l1_5Misses, 1) }
func (m *StorageMetrics) RecordL2Hit()   { atomic.AddUint64(&m.l2Hits, 1) }
func (m *StorageMetrics) RecordL2Miss()  { atomic.AddUint64(&m.l2Misses, 1) }

func (m *StorageMetrics) RecordL3Query(latency time.Duration) {
    atomic.AddUint64(&m.l3Queries, 1)
    atomic.AddUint64(&m.l3Latency, uint64(latency.Microseconds()))
}

func (m *StorageMetrics) RecordWrite(bytes int, err error) {
    atomic.AddUint64(&m.writes, 1)
    atomic.AddUint64(&m.writesBytes, uint64(bytes))
    if err != nil {
        atomic.AddUint64(&m.writeErrors, 1)
    }
}

func (m *StorageMetrics) Snapshot() map[string]interface{} {
    l1Total := atomic.LoadUint64(&m.l1Hits) + atomic.LoadUint64(&m.l1Misses)
    l1_5Total := atomic.LoadUint64(&m.l1_5Hits) + atomic.LoadUint64(&m.l1_5Misses)
    l2Total := atomic.LoadUint64(&m.l2Hits) + atomic.LoadUint64(&m.l2Misses)
    
    var l1HitRate, l1_5HitRate, l2HitRate float64
    if l1Total > 0 {
        l1HitRate = float64(atomic.LoadUint64(&m.l1Hits)) / float64(l1Total) * 100
    }
    if l1_5Total > 0 {
        l1_5HitRate = float64(atomic.LoadUint64(&m.l1_5Hits)) / float64(l1_5Total) * 100
    }
    if l2Total > 0 {
        l2HitRate = float64(atomic.LoadUint64(&m.l2Hits)) / float64(l2Total) * 100
    }
    
    l3Queries := atomic.LoadUint64(&m.l3Queries)
    var l3AvgLatency float64
    if l3Queries > 0 {
        l3AvgLatency = float64(atomic.LoadUint64(&m.l3Latency)) / float64(l3Queries) / 1000.0  // ms
    }
    
    return map[string]interface{}{
        "uptime_seconds": time.Since(m.startTime).Seconds(),
        "l1": map[string]interface{}{
            "hits":     atomic.LoadUint64(&m.l1Hits),
            "misses":   atomic.LoadUint64(&m.l1Misses),
            "hit_rate": l1HitRate,
        },
        "l1_5": map[string]interface{}{
            "hits":     atomic.LoadUint64(&m.l1_5Hits),
            "misses":   atomic.LoadUint64(&m.l1_5Misses),
            "hit_rate": l1_5HitRate,
        },
        "l2": map[string]interface{}{
            "hits":     atomic.LoadUint64(&m.l2Hits),
            "misses":   atomic.LoadUint64(&m.l2Misses),
            "hit_rate": l2HitRate,
        },
        "l3": map[string]interface{}{
            "queries":        l3Queries,
            "avg_latency_ms": l3AvgLatency,
        },
        "writes": map[string]interface{}{
            "total":        atomic.LoadUint64(&m.writes),
            "total_bytes":  atomic.LoadUint64(&m.writesBytes),
            "errors":       atomic.LoadUint64(&m.writeErrors),
        },
    }
}
```

2. 添加 HTTP 接口 `GET /metrics/storage`

**验收标准**:
- [ ] 正确记录各层缓存命中率
- [ ] 暴露 HTTP 接口
- [ ] 并发安全（使用 atomic）

**输出文件**:
- `monitoring/storage_metrics.go`
- `monitoring/storage_metrics_test.go`
- `api/metrics_handler.go`

---

### 🟤 Task Group 6: 测试与文档（依赖所有前置任务）

---

#### Task 6.1: 端到端集成测试

**优先级**: P0  
**预计时间**: 2天  
**依赖**: Task 4.2  
**可并行**: 否

**任务描述**:
编写端到端集成测试，验证双模式下的完整读写流程。

**具体要求**:

1. 创建 `tests/integration/dual_mode_test.go`：
   - 测试 full 模式：会话创建 → Bodies 写入 → 缓存命中 → 读取验证
   - 测试 lite 模式：会话创建 → Bodies 写入 → L1.5 缓存 → 读取验证
   - 测试模式切换：数据迁移一致性
   - 测试并发场景：100并发写入 + 100并发读取

2. 添加性能基准测试：
   - 缓存命中延迟
   - Bodies 写入吞吐量
   - 并发读写性能

**验收标准**:
- [ ] 双模式下所有测试通过
- [ ] 并发测试稳定无死锁
- [ ] 性能基准达标（lite 模式 <100 QPS）

**输出文件**:
- `tests/integration/dual_mode_test.go`
- `tests/integration/benchmark_test.go`

---

#### Task 6.2: 操作文档编写

**优先级**: P1  
**预计时间**: 1天  
**依赖**: Task 4.2  
**可并行**: 是

**任务描述**:
编写完整的操作文档，包括配置说明、部署指南、故障排查。

**具体要求**:

1. 创建 `docs/storage/README.md`（本文档）
2. 创建 `docs/storage/deployment-guide.md`：
   - Lite 模式部署步骤
   - Full 模式部署步骤
   - 配置参数详解
   - 性能调优建议

3. 创建 `docs/storage/troubleshooting.md`：
   - 常见问题排查
   - 性能瓶颈分析
   - 磁盘空间管理

**验收标准**:
- [ ] 文档完整覆盖部署和运维场景
- [ ] 包含配置示例和命令示例
- [ ] 新人能根据文档独立部署

**输出文件**:
- `docs/storage/README.md`
- `docs/storage/deployment-guide.md`
- `docs/storage/troubleshooting.md`

---

## 任务执行建议

### 并行执行方案

**阶段 1** (Week 1)：并行执行 Group 1 和 Group 2
- Team A: Task 1.1, Task 1.2, Task 1.3
- Team B: Task 2.1, Task 2.2, Task 2.3

**阶段 2** (Week 2)：执行 Group 3 和 Group 4
- Team A: Task 3.1
- Team B: Task 4.1, Task 4.2

**阶段 3** (Week 3)：执行 Group 5 和 Group 6
- Team A: Task 5.1, Task 5.2, Task 5.3
- Team B: Task 6.1, Task 6.2

### 关键里程碑

- **Day 5**: 完成存储抽象层和 SQLite 实现
- **Day 10**: 完成文件存储和缓存集成
- **Day 15**: 完成主程序集成和后台任务
- **Day 20**: 完成测试和文档

---

## 风险与缓解

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| SQLite 并发性能不足 | 中 | 高 | WAL 模式 + 异步队列 + 批量提交 |
| 文件数过多导致性能下降 | 中 | 中 | 分片目录 + 定期清理 |
| L1.5 缓存空间耗尽 | 低 | 中 | 水位监控 + LRU 自动清理 |
| 测试覆盖不足 | 中 | 高 | 强制集成测试 + Code Review |

---

## 验收清单

- [ ] 所有单元测试通过（覆盖率 >80%）
- [ ] 集成测试通过（双模式）
- [ ] 性能测试达标（lite <100 QPS）
- [ ] 代码 Review 完成
- [ ] 文档完整
- [ ] 部署脚本可用
- [ ] 监控指标正常

---

## 附录：快速启动命令

### Lite 模式
```bash
# 1. 配置
export STORAGE_MODE=lite
export SQLITE_PATH=./data/llm-gateway.db

# 2. 初始化
mkdir -p ./data

# 3. 启动
./llm-gateway
```

### Full 模式
```bash
# 1. 配置
export STORAGE_MODE=full
export POSTGRES_URL="postgres://..."
export REDIS_URL="localhost:6379"

# 2. 启动
./llm-gateway
```
