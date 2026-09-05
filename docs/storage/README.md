# 双模式存储架构（Dual-Mode Storage）

LLM Gateway 支持两种存储后端，通过 `storage_mode` 一次性切换，业务代码只依赖 `storage/` 包中的统一接口：

- **full 模式**：PostgreSQL + Redis，面向生产 / 多实例部署。数据全部落外部数据库，支持水平扩展与蓝绿发布。
- **lite 模式**：SQLite + 本地文件 + 进程内存，面向本地开发与单机轻量部署。零外部依赖，开箱即用。

模式定义见 `storage/interfaces.go`（`storage.StorageModeFull` / `storage.StorageModeLite`），配置加载与校验见 `config/storage.go`，工厂分派见 `storage/factory` 子包（`storage/factory/factory.go`）——根包 `storage` 仅保留接口、数据类型、哨兵错误与 `StorageConfig`，工厂独立成包以避免 import cycle。lite 模式工厂已接线到真实实现（`storage/sqlite` 三个 store + `storage/file.FileBodiesStore` 惰性单例 + `storage/memory.MemoryStateStore` 单例）；full 模式工厂保留桩（见下文说明）。

## 架构总览

### lite 模式数据流（L1 → L1.5 → L3）

```
             请求 (session 读/写)
                     │
                     ▼
   ┌─────────────────────────────────────────────┐
   │ L1  CompressionMetaCache（进程内存 LRU，     │
   │     1024 条，仅存压缩元数据，不含完整 body） │
   └───────────────────┬─────────────────────────┘
                       │ miss
                       ▼
   ┌─────────────────────────────────────────────┐
   │ L1.5 FileCache（本地文件快照，lite 专属）    │
   │   · mtime 超 TTL 过期（读时顺带清理）        │
   │   · 超过 maxSize 按 mtime 从旧到新淘汰       │
   │   · 进程重启后仍可命中                       │
   └───────────────────┬─────────────────────────┘
                       │ miss / 过期 / 损坏
                       ▼
   ┌─────────────────────────────────────────────┐
   │ L3 冷启动回源                                │
   │   · SQLite：sessions / session_turns /       │
   │     request_logs 元数据                      │
   │   · 本地文件：gzip 压缩的轮次 body            │
   └─────────────────────────────────────────────┘
       命中后逐层回填：L3 → 回填 L1 + L1.5
       写路径：L1 + L1.5 同步更新（fail-open）

   运行时状态（StateStore）→ 进程内存 MemoryStateStore
   会话内容大对象（BodiesStore）→ 本地文件（AsyncFileWriter 异步落盘）
```

### full 模式数据流（L1 → L2 → L3）

```
             请求 (session 读/写)
                     │
                     ▼
   ┌─────────────────────────────────────────────┐
   │ L1  CompressionMetaCache（同 lite）          │
   └───────────────────┬─────────────────────────┘
                       │ miss
                       ▼
   ┌─────────────────────────────────────────────┐
   │ L2  RedisGovernanceCache（仅治理 verdicts）  │
   └───────────────────┬─────────────────────────┘
                       │ miss
                       ▼
   ┌─────────────────────────────────────────────┐
   │ L3  SessionTurnsReader（PostgreSQL           │
   │     session_turns 冷启动回源，经 pgx 连接池） │
   └─────────────────────────────────────────────┘
       命中后回填：L3 → 回填 L1 + L2
       写路径：L1 + L2

   运行时状态（StateStore）→ Redis
   会话内容大对象（BodiesStore）→ PostgreSQL
```

说明：

- **生产 full 路径由既有 pgx/redis 装配承担，不经过工厂**（架构决策）：`storage/factory` 的
  full 分支保留 `ErrNotImplemented` 桩，仅为结构占位与分派测试。实际装配在
  `cmd/gateway/storage_mode_init.go`：`LLM_GATEWAY_STORAGE_MODE=lite` 时旁路 PG 初始化、
  创建 L1.5 FileCache、启动 `bg.CacheTrimmer` / `bg.BodiesTrimmer`；`domains/session/v2`
  侧经 `NewSessionCacheV2WithMode` 以 `*pgxpool.Pool` 构建 L2/L3——full/未设置 mode 时走
  历史 `NewSessionCacheV2`，行为零变化。
- **lite 模式的 L3 回源**：db 为 nil（lite 跳过 PG）时 `SessionTurnsReader` 防御性返回
  miss，缓存退化为 L1 + L1.5 两层，不会 panic。
- 模式归一化规则：`SessionCacheV2` 的 mode 零值与未知值一律视为 full（`effectiveMode`）。
- `Invalidate` 与模式无关：只要对应层非空就逐层失效（L1、L1.5、L2），避免模式切换后残留脏数据。
- 缓存层全部 fail-open：下层读/写失败只记日志并回源，不阻断主链路。

## 存储组件清单

| 组件 | 接口 / 类型 | lite 实现 | full 实现 | 代码位置 |
|------|-------------|-----------|-----------|----------|
| 存储模式与接口 | `storage.StorageMode` 及五个 Store 接口 | — | — | `storage/interfaces.go` |
| 存储工厂 | `factory.StorageFactory` | 按 Mode 分派 | 按 Mode 分派 | `storage/factory/factory.go`（子包 `storage/factory`） |
| 会话元数据 | `storage.SessionStore` | SQLite `sessions` 表（`SQLiteSessionStore`） | 工厂桩（生产 full 路径由 cmd/gateway 既有 pgx/redis 装配承担） | `storage/sqlite/session_store.go` |
| 轮次元数据 | `storage.TurnsStore` | SQLite `session_turns` 表（`SQLiteTurnsStore`） | 工厂桩（同上） | `storage/sqlite/turns_store.go` |
| 会话内容（大对象） | `storage.BodiesStore` | 本地文件 gzip + `AsyncFileWriter` 异步落盘（`FileBodiesStore`，工厂内惰性单例） | 工厂桩（同上） | `storage/file/bodies_store.go`、`storage/file/async_writer.go` |
| 请求日志 | `storage.RequestLogStore` | SQLite `request_logs` 表（`SQLiteRequestLogStore`） | 工厂桩（同上） | `storage/sqlite/request_log_store.go` |
| 运行时状态 | `storage.StateStore` | 进程内存 `MemoryStateStore`（工厂内惰性单例） | 工厂桩（Redis 桩，同上） | `storage/memory/state_store.go` |
| SQLite 打开与 Schema | `sqlite.OpenSQLite` / `InitSchema` | DSN 参数 + ConnectHook 配置 PRAGMA，幂等建表 | — | `storage/sqlite/schema.go` |
| L1 内存缓存 | `CompressionMetaCache`（包内类型） | 共用 | 共用 | `domains/session/v2/cache_v2.go` |
| L1.5 文件缓存 | `FileCache`（包内类型） | 仅 lite 读路径使用 | 不使用 | `domains/session/v2/cache_v2_file.go` |
| L2 Redis 治理缓存 | `RedisGovernanceCache` | 不使用（装配时摘除） | 仅 full | `domains/session/v2/cache_v2_redis.go` |
| L3 冷启动回源 | `SessionTurnsReader` | SQLite + 本地文件 | PostgreSQL `session_turns` | `domains/session/v2/turn_reader.go` |
| 多层缓存编排 | `SessionCacheV2` | L1 → L1.5 → L3 | L1 → L2 → L3 | `domains/session/v2/cache_v2.go` |

> 注：`storage/factory/stubs.go` 仅保留 full 模式（PostgreSQL / Redis）的桩实现
> （返回 `storage.ErrNotImplemented`），为结构占位与分派测试保留；lite 模式不使用桩，
> 工厂已直接接线到真实实现包，且对 `FileBodiesStore` / `MemoryStateStore` 做惰性单例
> （二者持有后台协程，不可每次新建），由 `Close` 统一优雅关闭。

## lite 模式目录布局

默认根目录为 `./data/`（各目录均可通过配置覆盖，见 deployment-guide）：

```
./data/
├── llm-gateway.db                  # SQLite 主库（sqlite_path）
│   ├── llm-gateway.db-wal          # WAL 模式自动生成的预写日志
│   └── llm-gateway.db-shm          # WAL 共享内存索引文件
├── session_bodies/                 # bodies_dir：会话内容（gzip）
│   └── {tenantID}/
│       └── {sessionID前2位}/       # 分片目录，避免单目录文件过多
│           └── {sessionID}/
│               └── turn_{N}.json.gz   # 每轮次一个文件
├── cache/                          # cache_dir：L1.5 文件缓存
│   └── {tenantID}/
│       └── {sessionID前2位}/
│           └── {sessionID}.json    # SessionStateV2 JSON 快照
└── request_logs/                   # logs_dir：请求日志目录（工厂启动时创建）
```

关键路径规则（按 rune 切分 sessionID 前 2 位，多字节字符安全）：

- 轮次 body：`{bodies_dir}/{tenantID}/{sessionID前2位}/{sessionID}/turn_{turnNo}.json.gz`
- L1.5 快照：`{cache_dir}/{tenantID}/{sessionID前2位}/{sessionID}.json`

## 快速开始

### lite 模式（本地开发）

```bash
# 方式一：启动脚本（推荐）
./scripts/start-lite.sh                      # 前台启动（go run ./cmd/gateway）
BUILD=1 ./scripts/start-lite.sh              # 先编译到 bin/gateway 再启动
BIN=/path/to/gateway ./scripts/start-lite.sh # 指定已有二进制

# 方式二：手动环境变量（最少只需模式一项，其余字段缺省时自动采用 ./data/ 默认值）
export LLM_GATEWAY_STORAGE_MODE=lite
mkdir -p ./data
go run ./cmd/gateway
```

脚本内部使用与 `config/storage.go` 一致的 `LLM_GATEWAY_*` 环境变量，并会清空 PG/Redis
相关变量避免继承外层 shell 的连接串。配置也可来自 `config.example.yaml` 的
`storage_mode: "lite"` 段，或纯环境变量（`config.LoadStorageConfigFromEnv`，无 YAML 即可）。

lite 模式由 `cmd/gateway/storage_mode_init.go` 装配：旁路 PG 初始化、创建 L1.5 FileCache、
启动 cache/bodies 两个后台清理任务；SIGTERM 优雅关闭（先停 trimmers 再排空异步写队列）。
已通过无 PG/Redis 的真机 smoke 验证：完整启动、`/healthz` 200、SQLite+WAL 落盘、
优雅关闭 exit 0。

### full 模式（生产）

```bash
# 方式一：启动脚本（自动回退 LLM_GATEWAY_DATABASE_URL 作为主连接串）
LLM_GATEWAY_POSTGRES_URL="postgres://user:password@127.0.0.1:5432/llm_gateway?sslmode=disable" \
LLM_GATEWAY_REDIS_URL="127.0.0.1:6379" \
./scripts/start-full.sh

# 方式二：手动环境变量
export LLM_GATEWAY_STORAGE_MODE=full
export LLM_GATEWAY_POSTGRES_URL="postgres://user:password@127.0.0.1:5432/llm_gateway?sslmode=disable"
export LLM_GATEWAY_REDIS_URL="127.0.0.1:6379"
go run ./cmd/gateway
```

full 模式要求 `full_storage.postgres_url` 与 `full_storage.redis_url` 均非空，否则启动时
`Validate` 报错。`redis_url` 支持 `redis://`、`rediss://`（TLS）URL 与裸 `host:port`
三种形态。更多配置项、PRAGMA 调优与保留期规划见
[deployment-guide.md](deployment-guide.md)；故障排查见
[troubleshooting.md](troubleshooting.md)。
