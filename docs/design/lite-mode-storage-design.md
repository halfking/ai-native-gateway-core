# LLM Gateway 双模式存储架构设计文档

## 文档版本
- **版本**: v1.0
- **日期**: 2026-09-04
- **作者**: LLM Gateway Team
- **状态**: Draft

---

## 1. 背景与目标

### 1.1 当前问题
llm-gateway-go 目前依赖 PostgreSQL + Redis 的全量部署模式，对于以下场景不够友好：
- 开发环境：需要安装和维护 PostgreSQL + Redis
- 边缘设备：资源受限，无法运行完整数据库
- 离线环境：完全断网的内网部署
- 小规模生产：QPS < 1000 的单机场景

### 1.2 设计目标
1. **保持全量模式不变**：PostgreSQL + Redis 的生产部署模式保持现状
2. **新增单机简化模式**：SQLite + 文件存储 + 内存状态
3. **零侵入改造**：通过接口抽象，业务代码无需修改
4. **配置驱动切换**：通过环境变量或配置文件选择部署模式

### 1.3 核心原则
- **接口抽象优先**：统一的存储接口层
- **数据分类存储**：根据数据特征选择最优存储方案
- **性能合理降级**：单机模式性能略低但资源占用小
- **渐进式迁移**：按模块逐步实施，风险可控

---

## 2. 核心发现：会话内容存储特性

### 2.1 现有会话存储架构
通过代码探索发现：

**表结构**：
- `sessions` - 会话快照（轻量元数据）
- `session_turns` - 轮次元数据（不含正文）
- `session_bodies` - 正文内容（**核心问题**）
  - 使用增量 delta 存储避免指数增长
  - 单个 turn 可包含：多轮对话历史 + 工具调用 + 附件引用
  - JSONB 类型存储：`request_delta`, `response_delta`, `outbound_body`

**数据量特征**：
- 短对话：每个 turn < 10KB
- 中等对话：每个 turn 10-100KB
- **长对话**：每个 turn **数百 KB 到数 MB**（工具调用密集场景）

### 2.2 SQLite 的局限性
虽然 SQLite 理论上支持大 BLOB，但：
- ❌ 单个 JSONB 字段过大影响查询性能
- ❌ 频繁更新大字段导致 WAL 文件膨胀
- ❌ 不适合存储数百 MB 级别的会话历史
- ❌ VACUUM 操作耗时长

**结论**：**session_bodies 不适合存储在 SQLite**，需要文件存储方案。

---

## 3. 数据分类与存储策略

### 3.1 数据分类表

| 数据类型 | 当前存储 | 典型大小 | 访问模式 | 全量模式 | 单机模式 | 理由 |
|---------|---------|---------|---------|---------|---------|------|
| **配置数据** | | | | | | |
| providers | PG | < 100 条 | 启动加载，很少变更 | PostgreSQL | **SQLite** | 结构化查询，事务支持 |
| credentials | PG | < 1000 条 | 高频读（鉴权），低频写 | PostgreSQL | **SQLite + 内存缓存** | 需要加密存储 |
| models | PG | < 500 条 | 只读配置 | PostgreSQL | **SQLite** | 静态配置 |
| projects | PG | < 10000 条 | CRUD | PostgreSQL | **SQLite** | 关联查询 |
| tasks | PG | < 50000 条 | CRUD | PostgreSQL | **SQLite** | 关联查询 |
| routing_rules | PG | < 1000 条 | 热重载 | PostgreSQL | **SQLite + fsnotify** | 需要事务 |
| **会话数据** | | | | | | |
| sessions | PG (分区) | 轻量元数据 | 追加写 | PostgreSQL | **SQLite 索引** | 仅存储快照 |
| session_turns | PG (分区) | 轻量元数据 | 追加写 | PostgreSQL | **SQLite 索引** | 不含正文 |
| **session_bodies** | **PG JSONB (分区)** | **数百 KB - MB** | 追加写，少读 | PostgreSQL | **文件存储（gzip）** | ⚠️ 过大，不适合 SQLite |
| **请求日志** | | | | | | |
| request_logs_hot | PG (8小时窗口) | 中等（无 body） | 实时写入 | PostgreSQL | **SQLite 索引** | 仅元数据 |
| request_bodies | PG | 大（数十 KB） | 归档查询 | PostgreSQL | **文件 gzip** | 大对象 |
| **运行时状态** | | | | | | |
| Redis (ursm 状态) | Redis Lua | 实时更新 | 毫秒级 | Redis | **内存 sync.Map** | 单机无需分布式 |
| Redis (限流) | Redis zset | 实时计数 | 毫秒级 | Redis | **内存滑窗** | 重启重建 |
| Redis (队列) | Redis list | 实时入队出队 | 高频 | Redis | **Go channel** | 单机队列 |
| Redis (分布式锁) | Redis Lua | 并发控制 | 毫秒级 | Redis | **sync.Mutex** | 单机降级 |

### 3.2 存储策略决策树

```
数据类型
    ├─ 配置数据（小，结构化，需事务）
    │   └─ ✅ SQLite
    │
    ├─ 元数据索引（中等，查询需求）
    │   └─ ✅ SQLite
    │
    ├─ 大内容数据（> 100KB，少查询）
    │   └─ ✅ 文件存储（gzip）
    │
    └─ 运行时状态（高频，可失）
        └─ ✅ 内存（sync.Map / channel）
```

---

## 4. 架构设计

### 4.1 分层架构

```
┌────────────────────────────────────────────────┐
│     Application Layer (业务层 - 不变)          │
│  domains/streaming, admin, bg workers          │
└────────────────┬───────────────────────────────┘
                 │ 依赖接口，不关心实现
┌────────────────┴───────────────────────────────┐
│    Storage Abstraction Layer (新增)            │
│  storage/                                       │
│  ├── config_store.go      # 配置接口           │
│  ├── session_store.go     # 会话接口           │
│  ├── request_store.go     # 请求接口           │
│  └── state_store.go       # 状态接口           │
└────────┬──────────────────┬────────────────────┘
         │                  │
    ┌────┴────────┐    ┌────┴──────────┐
    │ Full Mode   │    │ Lite Mode     │
    │ Adapters    │    │ Adapters      │
    ├─────────────┤    ├───────────────┤
    │ PostgreSQL  │    │ SQLite        │
    │ + Redis     │    │ + File        │
    │ (分布式)    │    │ + Memory      │
    │             │    │ (单机优化)    │
    └─────────────┘    └───────────────┘
```

### 4.2 存储接口设计

#### ConfigStore 接口
```go
type ConfigStore interface {
    // Provider operations
    GetProvider(ctx context.Context, id string) (*Provider, error)
    ListProviders(ctx context.Context) ([]*Provider, error)
    SaveProvider(ctx context.Context, provider *Provider) error
    DeleteProvider(ctx context.Context, id string) error
    
    // Credential operations (类似)
    // Model operations (类似)
}
```

#### SessionStore 接口
```go
type SessionStore interface {
    // Metadata operations (SQLite)
    GetSession(ctx context.Context, sessionID string) (*SessionMetadata, error)
    SaveSession(ctx context.Context, session *SessionMetadata) error
    
    // Body operations (File)
    GetSessionBody(ctx context.Context, sessionID string) ([]byte, error)
    SaveSessionBody(ctx context.Context, sessionID string, body []byte) error
}
```

#### StateStore 接口
```go
type StateStore interface {
    // Key-Value operations
    Get(ctx context.Context, key string) (string, error)
    Set(ctx context.Context, key string, value string, ttl time.Duration) error
    
    // Rate limiting
    IncrementCounter(ctx context.Context, key string, window time.Duration) (int64, error)
    
    // Queue operations
    Enqueue(ctx context.Context, queueName string, item string) error
    Dequeue(ctx context.Context, queueName string) (string, error)
    
    // Lock operations (单机简化实现)
    AcquireLock(ctx context.Context, lockKey string, ttl time.Duration) (bool, error)
}
```

---

## 5. 文件存储设计

### 5.1 目录结构

```
{DATA_DIR}/                          # 默认 ./data 或 /var/lib/llm-gateway
├── config.db                        # SQLite 配置数据库
├── sessions/
│   ├── 2026/09/04/
│   │   ├── sess_abc123/
│   │   │   ├── turn_001.json.gz     # 单个 turn 的 bodies
│   │   │   ├── turn_002.json.gz
│   │   │   └── turn_003.json.gz
│   │   └── sess_def456/
│   │       └── turn_001.json.gz
│   └── 2026/09/05/
├── requests/
│   ├── 2026/09/04/
│   │   ├── req_xyz789/
│   │   │   ├── request.json.gz
│   │   │   └── response.json.gz
│   │   └── req_uvw456/
│   └── 2026/09/05/
└── metadata.db                      # SQLite 元数据索引
```

### 5.2 文件存储特性

| 特性 | 设计 | 理由 |
|------|------|------|
| **目录分层** | 年/月/日/ID | 便于归档和清理 |
| **文件格式** | JSON + gzip | 节省 70% 空间 |
| **写入策略** | 异步批量 | 不阻塞主流程 |
| **索引** | SQLite 存储路径 | 快速定位文件 |
| **清理** | 按日期目录删除 | 定期归档旧数据 |

### 5.3 写入流程

```
HTTP 请求到达
    ↓
1. 提取元数据（session_id, turn_no, tokens, ...）
    ↓
2. 写入 SQLite 索引（同步，< 3ms）
    ↓
3. 提交内容到异步队列（非阻塞，< 1ms）
    ↓
4. 返回响应给客户端
    ↓
后台 Worker:
5. 批量压缩写入文件（gzip）
6. fsync 保证持久化
```

### 5.4 读取流程

```
查询请求
    ↓
1. SQLite 查询索引（获取 file_path）
    ↓
2. 读取文件内容
    ↓
3. gzip 解压缩
    ↓
4. JSON 反序列化
    ↓
5. 返回结果
```

---

## 6. 内存状态管理

### 6.1 替代 Redis 的方案

| Redis 功能 | 单机模式实现 | 数据结构 |
|-----------|------------|---------|
| **KV 存储** | sync.Map | `sync.Map{key: {value, expireAt}}` |
| **限流计数** | 滑动窗口 | `map[key][]timestamp` + 定期清理 |
| **队列** | Go channel | `chan string` |
| **分布式锁** | sync.Mutex | `sync.Mutex` (单机无需分布式) |
| **Pub/Sub** | Go channel | `map[topic][]chan Message` |

### 6.2 内存状态特性

- ✅ **无需持久化**：重启后重建
- ✅ **性能极高**：纳秒级操作
- ✅ **零依赖**：纯 Go 实现
- ⚠️ **单机限制**：不支持多实例

---

## 7. 性能评估

### 7.1 性能对比

| 操作 | 全量模式 (PG+Redis) | 单机模式 (SQLite+File+Mem) | 差异 |
|------|---------------------|---------------------------|------|
| **配置读取** | 1ms (PG) | 0.05ms (内存缓存) | **快 20x** |
| **配置写入** | 5ms (PG 事务) | 3ms (SQLite WAL) | **快 40%** |
| **会话元数据写入** | 50ms (PG 事务) | 5ms (SQLite + 异步文件) | **快 10x** |
| **会话内容写入** | 100ms (PG JSONB) | 1ms (异步队列) + 后台写入 | **快 100x** |
| **会话内容读取** | 20ms (PG JOIN) | 10ms (SQLite + 文件) | **快 2x** |
| **状态更新** | 2ms (Redis) | 0.001ms (内存) | **快 2000x** |
| **队列操作** | 2ms (Redis) | 0.001ms (channel) | **快 2000x** |

### 7.2 资源占用

| 指标 | 全量模式 | 单机模式 |
|------|---------|---------|
| **内存** | 500MB + (PG+Redis 外部) | 200MB (全包含) |
| **磁盘 (配置)** | N/A (PG) | ~50MB (SQLite) |
| **磁盘 (日志)** | N/A (PG) | ~1GB/天 (10K 请求，压缩后) |
| **进程数** | 3 (gateway + pg + redis) | 1 (gateway) |

### 7.3 并发能力

| 场景 | 全量模式 | 单机模式 | 瓶颈 |
|------|---------|---------|------|
| 读配置 | 10000 QPS | 50000 QPS | CPU (内存缓存) |
| 写配置 | 1000 TPS | 500 TPS | SQLite 单写入 |
| 会话写入 | 5000 TPS | 3000 TPS | 异步队列 + 磁盘 I/O |
| 会话查询 | 2000 QPS | 1000 QPS | 磁盘 I/O |

**结论**：单机模式适合 **QPS < 1000** 的场景。

---

## 8. 部署模式

### 8.1 配置文件

```yaml
# config.yaml
storage:
  # 模式选择: full | lite
  mode: lite
  
  # 全量模式配置
  full:
    postgres:
      host: localhost
      port: 5432
      database: llm_gateway
      user: postgres
      password: ${PG_PASSWORD}
    redis:
      addr: localhost:6379
      password: ${REDIS_PASSWORD}
      db: 0
  
  # 单机模式配置
  lite:
    # 数据目录
    data_dir: /var/lib/llm-gateway/data
    
    # SQLite 配置
    sqlite:
      db_path: ${data_dir}/config.db
      wal_mode: true
      cache_size: 64MB
    
    # 文件存储配置
    file_storage:
      sessions_dir: ${data_dir}/sessions
      requests_dir: ${data_dir}/requests
      compression: gzip
      async_buffer_size: 1000
    
    # 清理策略
    retention:
      sessions_days: 30
      requests_days: 7
```

### 8.2 环境变量

```bash
# 单机模式
export STORAGE_MODE=lite
export DATA_DIR=/var/lib/llm-gateway/data

# 全量模式
export STORAGE_MODE=full
export PG_HOST=localhost
export PG_PASSWORD=secret
export REDIS_ADDR=localhost:6379
```

---

## 9. 适用场景

### 9.1 ✅ 单机模式适用场景

1. **开发环境**
   - 快速启动，零配置
   - 无需安装 PostgreSQL/Redis
   - 本地调试方便

2. **边缘设备**
   - 树莓派、工控机
   - 内存 < 2GB
   - 单机部署

3. **离线环境**
   - 完全断网的内网
   - 安全隔离环境
   - 无法访问云服务

4. **小规模生产**
   - QPS < 1000
   - 单租户或少量租户
   - 无需高可用集群

### 9.2 ❌ 单机模式不适用场景

1. **多实例集群**
   - SQLite 不支持分布式
   - 无法共享状态

2. **高并发写入**
   - SQLite 单写入限制
   - QPS > 5000 建议全量模式

3. **跨机房部署**
   - 需要数据库复制
   - 需要分布式锁

4. **严格 SLA 要求**
   - 99.99% 可用性
   - 需要主从切换

---

## 10. 实施计划

### 10.1 Phase 1: 存储抽象层（Week 1-2）
- ✅ 创建 `storage/` 包
- ✅ 定义核心接口：ConfigStore, SessionStore, RequestStore, StateStore
- ⏳ 创建工厂模式选择实现

### 10.2 Phase 2: SQLite 配置存储（Week 2）
- ✅ 实现 SQLite ConfigStore
- ⏳ 添加内存缓存层
- ⏳ fsnotify 监听配置变更
- ⏳ 编写单元测试

### 10.3 Phase 3: 文件存储实现（Week 2-3）
- ⏳ 实现分层目录管理
- ⏳ 异步批量写入队列
- ⏳ gzip 压缩/解压缩
- ⏳ SQLite 索引映射
- ⏳ 定期清理旧文件

### 10.4 Phase 4: 内存状态管理（Week 3）
- ⏳ 实现 sync.Map KV 存储
- ⏳ 实现滑窗限流算法
- ⏳ 实现 channel 队列
- ⏳ 实现 Mutex 锁

### 10.5 Phase 5: 部署脚本（Week 3-4）
- ⏳ 单机模式安装脚本
- ⏳ Docker Compose 精简版
- ⏳ 健康检查调整
- ⏳ 编写部署文档

---

## 11. 风险与限制

### 11.1 已知限制

| 限制 | 影响 | 缓解措施 |
|------|------|---------|
| SQLite 单写入 | 并发写入受限 | 异步批量写入 |
| 文件系统性能 | 大量文件影响性能 | 分层目录 + 定期归档 |
| 内存状态易失 | 重启后丢失 | 明确单机模式预期 |
| 无分布式锁 | 多实例冲突 | 明确单机部署限制 |

### 11.2 风险评估

| 风险 | 概率 | 影响 | 应对 |
|------|------|------|------|
| SQLite 性能不足 | 低 | 中 | 性能测试验证 |
| 文件存储损坏 | 低 | 高 | 定期备份 + checksum |
| 内存溢出 | 中 | 高 | 限制缓存大小 + 监控 |
| 磁盘空间不足 | 中 | 高 | 定期清理 + 告警 |

---

## 12. 验收标准

### 12.1 功能完整性
- [ ] 所有 CRUD 操作在两种模式下行为一致
- [ ] 配置热重载在两种模式下均可用
- [ ] 会话查询结果格式一致
- [ ] 文件存储支持压缩和解压缩

### 12.2 性能指标
- [ ] 单机模式配置读取 < 1ms (p99)
- [ ] 会话元数据写入 < 5ms (p99)
- [ ] 文件异步写入不阻塞主流程
- [ ] 内存状态操作 < 1μs

### 12.3 可靠性
- [ ] SQLite 完整性检查通过: `PRAGMA integrity_check`
- [ ] 进程崩溃后 WAL 自动恢复
- [ ] 磁盘满时能告警并降级
- [ ] 定期备份机制可用

### 12.4 部署便利性
- [ ] 一键安装单机模式，无需 PostgreSQL/Redis
- [ ] Docker 镜像 < 200MB (含 SQLite)
- [ ] 配置文件切换模式，无需重新编译
- [ ] 健康检查端点正常工作

---

## 13. 附录

### 13.1 相关文档
- [实施计划](./lite-mode-implementation-plan.md)
- [部署指南](./lite-mode-deployment-guide.md)
- [运维手册](./lite-mode-maintenance-guide.md)
- [迁移指南](./lite-mode-migration-guide.md)

### 13.2 技术参考
- [SQLite WAL Mode](https://www.sqlite.org/wal.html)
- [SQLite Performance Tuning](https://www.sqlite.org/pragma.html)
- [Go database/sql](https://pkg.go.dev/database/sql)
- [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3)

---

**文档结束**
