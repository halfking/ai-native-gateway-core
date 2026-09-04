# 存储部署指南（Lite / Full）

本文描述双模式存储架构的部署步骤、配置参数详解、性能调优与磁盘规划。

- 配置定义与加载：`config/storage.go`（`LoadStorageConfigFromYAML` → `ApplyDefaults` → `Validate`）
- 存储工厂：`storage/factory`（子包，`storage/factory/factory.go`；lite 已接线真实实现）
- 配置示例：`config.example.yaml` 的 storage 段

加载语义（重要）：

1. 先解析 YAML，再用 `LLM_GATEWAY_*` 环境变量填充 YAML 未配置（空字符串/零值）的字段；
   **显式配置的 YAML 值优先于环境变量**。
2. 环境变量缺失时，lite 模式由 `ApplyLiteDefaults` 补齐全部默认值；full 模式补
   `max_connections=100`。
3. 嵌套段（`full_storage` / `lite_storage`）缺失但 env 有值时会自动创建对应段，支持纯 env 部署。
4. 主程序装配入口（`cmd/gateway/storage_mode_init.go` 的 `loadStorageConfig`）：有 YAML 路径时
   走 `LoadStorageConfigFromYAML`（解析失败只告警并回退 env-only，不阻塞启动）；无 YAML 路径时
   走 `config.LoadStorageConfigFromEnv()`（env-only 部署）。

---

## 一、Lite 模式部署（SQLite + 本地文件 + 内存）

### 0. 构建要求（重要）：lite 模式必须使用 CGO 构建

SQLite 驱动为 `github.com/mattn/go-sqlite3`，是 **CGO 包**：

- 本机/原生构建：直接 `go build ./cmd/gateway`（macOS 装 Xcode CLT、Linux 装 gcc 即可，CGO 默认开启）。
- Docker 构建：需 `CGO_ENABLED=1` 且构建镜像内含 C 编译器（`golang` 官方镜像自带 gcc）。
- **`CGO_ENABLED=0` 构建在编译期直接失败**（go-sqlite3 的无 cgo 存根缺少
  `SQLiteConn.Exec` 等方法）。主 `Dockerfile` 已于 2026-09-05 改为
  `CGO_ENABLED=1` 并显式安装 `gcc musl-dev`（sqlite3.c 静态编入二进制，
  运行时仅动态依赖 musl libc，与 alpine 兼容）。full 模式行为不受影响——
  该模式下 SQLite 驱动根本不会被打开。
- ⚠️ 仓库中其他自行构建 `./cmd/gateway` 的脚本/文档若写有 `CGO_ENABLED=0`，
  同样会编译失败，需改为 `CGO_ENABLED=1`（`cmd/gateway-v2` 仅依赖纯 Go 的
  storage 接口包，不受影响）。

若后续要求生产镜像保持 `CGO_ENABLED=0`，需评估将驱动切换为纯 Go 的
`modernc.org/sqlite`（DSN 参数与 ConnectHook 机制不同，`storage/sqlite` 需适配），
当前版本未做此切换。

### 1. 环境变量清单（全部存储相关 env）

环境变量均带 `LLM_GATEWAY_` 前缀；下表"默认值"列为 env 与 YAML 均未配置时
`ApplyLiteDefaults` 采用的值。

| 环境变量 | 对应 YAML 键 | 默认值 | 说明 |
|----------|--------------|--------|------|
| `LLM_GATEWAY_STORAGE_MODE` | `storage_mode` | 无（必填，`full` 或 `lite`） | 存储模式；空值在 Validate 报错 |
| `LLM_GATEWAY_SQLITE_PATH` | `lite_storage.sqlite_path` | `./data/llm-gateway.db` | SQLite 数据库文件路径（lite 必填项，缺省由默认值补齐） |
| `LLM_GATEWAY_BODIES_DIR` | `lite_storage.bodies_dir` | `./data/session_bodies` | 会话内容（gzip 轮次文件）根目录 |
| `LLM_GATEWAY_CACHE_DIR` | `lite_storage.cache_dir` | `./data/cache` | L1.5 文件缓存根目录 |
| `LLM_GATEWAY_LOGS_DIR` | `lite_storage.logs_dir` | `./data/request_logs` | 请求日志目录（工厂启动时创建） |

> 注意：`sqlite_pragmas`、`cache_ttl_hours`、`cache_max_size_gb`、`async_writers`、
> `retention.*` 目前**只有 YAML 配置，没有对应环境变量**，需要调整时请使用 YAML。

### 2. YAML 配置示例（与 config.example.yaml 一致）

```yaml
storage_mode: "lite"

lite_storage:
  sqlite_path: "./data/llm-gateway.db"
  bodies_dir: "./data/session_bodies"
  cache_dir: "./data/cache"
  logs_dir: "./data/request_logs"
  sqlite_pragmas:
    journal_mode: "WAL"
    cache_size_kb: 64000
    synchronous: "NORMAL"
    busy_timeout_ms: 5000
  cache_ttl_hours: 24
  cache_max_size_gb: 10
  async_writers: 4
  retention:
    session_bodies_days: 30
    request_logs_days: 7
    cache_hours: 24
```

lite 模式全部字段的默认值（YAML/env 均未配置时）：

| 字段 | 默认值 |
|------|--------|
| `sqlite_path` | `./data/llm-gateway.db` |
| `bodies_dir` | `./data/session_bodies` |
| `cache_dir` | `./data/cache` |
| `logs_dir` | `./data/request_logs` |
| `sqlite_pragmas.journal_mode` | `WAL` |
| `sqlite_pragmas.cache_size_kb` | `64000` |
| `sqlite_pragmas.synchronous` | `NORMAL` |
| `sqlite_pragmas.busy_timeout_ms` | `5000` |
| `cache_ttl_hours` | `24` |
| `cache_max_size_gb` | `10` |
| `async_writers` | `4` |
| `retention.session_bodies_days` | `30` |
| `retention.request_logs_days` | `7` |
| `retention.cache_hours` | `24` |

### 3. 目录准备

工厂 `NewStorageFactory` 在 lite 模式下会自动创建 `bodies_dir`、`cache_dir`、`logs_dir`
三个目录（`0755`），SQLite 父目录也可预先建好：

```bash
mkdir -p ./data
```

### 4. 启动

方式一：启动脚本（推荐，脚本内 env 名与 config/storage.go 一致）：

```bash
./scripts/start-lite.sh                      # 前台启动（go run ./cmd/gateway）
BUILD=1 ./scripts/start-lite.sh              # 先 go build -o bin/gateway 再启动
BIN=/path/to/gateway ./scripts/start-lite.sh # 指定已有二进制
```

脚本会显式 export 四个 lite 目录 env（未设置时用默认值）并 `mkdir -p ./data`；同时清空
`LLM_GATEWAY_DATABASE_URL` / `DATABASE_URL` / `LLM_GATEWAY_POSTGRES_URL` /
`LLM_GATEWAY_REDIS_URL`，避免继承外层 shell 的 full 模式连接串。

方式二：手动环境变量：

```bash
export LLM_GATEWAY_STORAGE_MODE=lite
go run ./cmd/gateway        # 或 go build -o bin/gateway ./cmd/gateway && ./bin/gateway
```

启动期装配（`cmd/gateway/storage_mode_init.go`）：`LLM_GATEWAY_STORAGE_MODE=lite` 时旁路
PG 初始化 → `ApplyLiteDefaults` → `Validate` → 创建存储工厂 → 创建 L1.5 FileCache
（`CacheTTLHours` 小时 / `CacheMaxSizeGB` 换算字节）→ 启动 `bg.CacheTrimmer` 与
`bg.BodiesTrimmer` 两个后台清理任务。SIGTERM/SIGINT 优雅关闭：先取消 trimmers（有界等待
3s），再 `factory.Close()`（排空 bodies 异步写队列保证落盘），已真机 smoke 验证
exit 0。

### 5. 验证

- 启动日志出现 `storage lite 模式已启用`（slog，含 sqlite_path / bodies_dir / cache_dir /
  logs_dir / cache_ttl_hours / cache_max_size_gb / async_writers / retention_* 字段快照）。
- 清理任务日志：`cache trimmer 已启动` 与 `bodies trimmer 已启动`（启动即先执行一次清理，
  不用等一个完整周期）。
- 配置不合法会直接启动失败并给出明确报错（如
  `storage_mode "lite" requires a lite_storage section in yaml`）。
- SQLite 文件生成：`sqlite_path` 对应文件出现，且同目录生成 `-wal` / `-shm` 两个 WAL
  辅助文件。
- Schema 幂等建表：`sessions`、`session_turns`、`request_logs`、`configs` 四张表与配套索引
  全部使用 `IF NOT EXISTS`，重复启动安全。
- 三个数据目录出现：`session_bodies/`、`cache/`、`request_logs/`。
- 健康检查 `/healthz` 返回 200。
- 写入一条请求后，`{bodies_dir}/{tenantID}/{sid前2位}/{sid}/turn_1.json.gz` 与
  `{cache_dir}/{tenantID}/{sid前2位}/{sid}.json` 应出现对应文件。

---

## 二、Full 模式部署（PostgreSQL + Redis）

### 1. 部署步骤

```bash
# 1. 准备外部依赖
#    - PostgreSQL：目标库需可正常建表（schema 由既有迁移/装配流程管理）
#    - Redis：建议独立逻辑 db（config.example.yaml 示例为 /2）

# 2. 配置 + 启动（脚本方式；POSTGRES_URL 未设置时自动回退 LLM_GATEWAY_DATABASE_URL，
#    并把主连接串 LLM_GATEWAY_DATABASE_URL 一并补齐）
LLM_GATEWAY_STORAGE_MODE=full \
LLM_GATEWAY_POSTGRES_URL="postgres://user:password@127.0.0.1:5432/llm_gateway?sslmode=disable" \
LLM_GATEWAY_REDIS_URL="127.0.0.1:6379" \
./scripts/start-full.sh          # 同样支持 BUILD=1 / BIN=

# 或手动环境变量方式
export LLM_GATEWAY_STORAGE_MODE=full
export LLM_GATEWAY_POSTGRES_URL="postgres://user:password@127.0.0.1:5432/llm_gateway?sslmode=disable"
export LLM_GATEWAY_REDIS_URL="127.0.0.1:6379"
# 可选：连接池上限，缺省 100
export LLM_GATEWAY_STORAGE_MAX_CONNECTIONS=100
go run ./cmd/gateway
```

### 2. 配置参数详解

#### storage_mode

| 项 | 说明 |
|----|------|
| YAML | `storage_mode` |
| env | `LLM_GATEWAY_STORAGE_MODE` |
| 默认值 | 无（必填） |
| 说明 | `full` 或 `lite`；空值报 `storage_mode is empty`，其他值报 `invalid storage_mode` |

#### full_storage 段

| 字段 | env | 默认值 | 说明 |
|------|-----|--------|------|
| `postgres_url` | `LLM_GATEWAY_POSTGRES_URL` | 无（full 必填） | pgx 连接串；Validate 要求非空，工厂用 `pgxpool.ParseConfig` 解析，解析失败启动失败 |
| `redis_url` | `LLM_GATEWAY_REDIS_URL` | 无（full 必填） | Validate 要求非空；支持三种形态——`redis://[user:pass@]host[:port][/db]` 与 `rediss://`（TLS）经 `redis.ParseURL` 拆出 Addr/Username/Password/DB，裸 `host:port` 作为 Addr 原样使用（历史行为） |
| `max_connections` | `LLM_GATEWAY_STORAGE_MAX_CONNECTIONS` | `100` | 同时作用于 pgx 连接池 `MaxConns` 与 go-redis `PoolSize`；`<=0` 或非法值不生效（保持默认） |

> 说明：生产 full 模式的双模式工厂分支保留桩（`storage/factory/stubs.go`），实际的
> PostgreSQL/Redis 装配由 cmd/gateway 既有 pgx/redis 路径承担；上述连接串由主配置
> `LLM_GATEWAY_DATABASE_URL` 等既有变量与 `full_storage` 段共同衔接（见 start-full.sh 的
> 回退逻辑）。

#### lite_storage 段

见上文"Lite 模式部署"的 YAML 示例与默认值表。各字段含义：

| 字段 | 说明 |
|------|------|
| `sqlite_path` | SQLite 数据库文件；应使用文件路径而非 `:memory:`（WAL 模式不支持内存库） |
| `bodies_dir` / `cache_dir` / `logs_dir` | 三个本地数据目录，工厂启动时自动创建 |
| `sqlite_pragmas.*` | SQLite 连接级调优，见下文"性能调优" |
| `cache_ttl_hours` | L1.5 文件缓存 TTL（小时）；文件 mtime 超过该值视为过期 |
| `cache_max_size_gb` | L1.5 文件缓存容量上限（GB，按字节计）；超出后按 mtime 从旧到新淘汰 |
| `async_writers` | bodies 异步落盘的写 worker 数 |
| `retention.*` | 各类数据保留期，由后台清理任务执行，见"保留期与磁盘规划" |

---

## 三、性能调优

### 1. SQLite PRAGMA

默认 PRAGMA 集合见 `storage/sqlite.DefaultPragmas()`。实现机制：go-sqlite3 原生支持的参数
直接写入 DSN 查询串（`file:path?_journal_mode=WAL&...`），不支持的参数（如 `temp_store`）
通过带 `ConnectHook` 的自定义驱动逐连接执行，保证连接池中**所有连接**的 PRAGMA 一致，
避免"PRAGMA 只对单个连接生效"的问题。

| PRAGMA | 默认值 | 含义 | 调整建议 |
|--------|--------|------|----------|
| `journal_mode` | `WAL` | 预写日志，写不阻塞读，适合网关并发读写场景 | 不要改为 DELETE/TRUNCATE（失去并发读能力） |
| `synchronous` | `NORMAL` | WAL 模式推荐持久化级别，兼顾性能与安全；断电最多丢最近的事务，不会损坏库 | 对单条数据更敏感可改 `FULL`（写入变慢） |
| `busy_timeout` | `5000`（ms） | 写锁冲突时最多等待 5s 再返回 `SQLITE_BUSY` | 并发写入多、偶发 `database is locked` 时可调大到 10000 |
| `foreign_keys` | `on` | 开启外键约束 | 保持开启 |
| `cache_size` | `-64000`（约 64MB，负数单位 KiB） | 页缓存大小，影响读性能 | 内存充裕可增大到 `-128000`；对应 YAML `sqlite_pragmas.cache_size_kb` |
| `temp_store` | `MEMORY` | 临时表与排序在内存中完成 | 保持默认；注意占用进程内存 |

配置覆盖：YAML `lite_storage.sqlite_pragmas.*` 中显式配置的项按顺序覆盖同名默认项。

### 2. 异步写入（async_writers）

- bodies 落盘经由 `storage/file.AsyncFileWriter`：固定 worker 协程 + 容量 1000 的任务队列，
  "临时文件 + rename" 原子写入；`Write` 阻塞到该文件落盘完成，`WriteAsync` 立即返回结果通道。
- `async_writers`（默认 4）决定 worker 数：`storage.StorageConfig.AsyncWriters` 经主程序装配
  传入工厂，lite 分支据此创建 `FileBodiesStore`；`<=0` 时回落默认 4
  （`storage/factory` 的 `defaultFileWorkers`）。写入吞吐不足（队列堆积）可增大到 8；
  但 worker 过多会加剧磁盘随机 IO 与 SQLite 元数据写竞争，本地 SSD 建议 4~8。
- `FileBodiesStore` 在工厂内为惰性单例（内部持有后台写协程与队列，不可每次新建），
  多次 `NewBodiesStore()` 复用同一实例。
- 队列缓冲固定 1000，不可配置；持续打满说明磁盘 IO 或 worker 数是瓶颈（见
  troubleshooting "写入慢"）。
- 关闭时优雅排空：Close 后不再接受新任务，worker 消费完队列才退出，超时上限 30s；
  `factory.Close()` 会先关 bodies 单例（排空队列保证落盘）再关 SQLite。

### 3. L1.5 文件缓存容量与 TTL 权衡

- `cache_ttl_hours`（默认 24）：TTL 越长，进程重启后可命中的会话越多（L1.5 的核心价值是
  **重启后仍可命中**）；但过期文件在被读之前不会删除，目录文件数会随时间累积。
- `cache_max_size_gb`（默认 10）：按字节计的硬上限；超限按 mtime 从旧到新淘汰（即将写入的
  key 自身绝不淘汰）。容量太小会频繁淘汰热点会话，使命中率下降；太大则占用磁盘且每次
  淘汰触发全目录 Walk，耗时变长。
- 经验值：单会话快照通常为 KB 量级，10GB 可容纳百万级会话；磁盘紧张时可降到 1~2GB。
- TTL 兜底：装配时 TTL `<=0` 时 FileCache 内部使用 30 分钟兜底值，正常部署不会触发。

### 4. gzip 压缩权衡

- 轮次 body 以 gzip 压缩落盘（LLM 请求/响应原文重复度高，通常可节省 70% 以上空间）。
- 代价：写路径多一次压缩（CPU），读路径多一次解压（CPU）。CPU 紧张而磁盘充裕的场景才需要
  权衡；当前实现固定启用 gzip，无开关。

---

## 四、保留期与磁盘规划

### retention 参数（lite_storage.retention）

| 字段 | 默认值 | 作用对象 | 清理方式 |
|------|--------|----------|----------|
| `session_bodies_days` | `30` | `bodies_dir` 下超过保留期的会话目录 | `bg.BodiesTrimmer`（lite 装配自动启动）：默认每 6 小时一轮，会话目录 mtime 超期即整目录删除（先统计体积再 RemoveAll），并顺带清理变空的分片/租户父目录 |
| `request_logs_days` | `7` | 请求日志 | **lite 模式暂无自动清理 worker**（当前 trimmer 只覆盖 cache 与 bodies），SQLite `request_logs` 行需人工或后续任务清理 |
| `cache_hours` | `24` | `cache_dir` 下过期的 L1.5 快照文件 | `bg.CacheTrimmer`（lite 装配自动启动）：默认每 1 小时一轮，按文件 mtime 删除过期缓存文件 |

两个 trimmer 的 `Start(ctx)` 均为阻塞式，由 `storage_mode_init.go` 以协程启动；启动即先执行
一次清理，之后按周期运行；统计快照（删除文件/会话数、释放字节）随日志输出
（`cache_trimmer: 清理完成` / `bodies_trimmer: 清理完成`），供监控读取。

如何选择：

- `session_bodies_days`：bodies 占磁盘大头，按"故障复盘需要回溯多久"取值；本地开发 7 天
  足够，单人部署 30 天。
- `request_logs_days`：仅元数据（SQLite 行），磁盘占用小，7~30 天均可。
- `cache_hours`：与 `cache_ttl_hours` 保持同量级即可（默认均 24）；缓存可随时重建，调小
  只影响重启后首请求的回源次数。

### 磁盘占用估算方法

```
bodies 日增 ≈ 日请求数 × 平均轮次 body 原始大小 × (1 - gzip 压缩率约 0.7)
            + 日请求数 × 轮次元数据行大小（SQLite，通常 <1KB/轮）
cache 上限 ≤ cache_max_size_gb（硬上限，超出自动淘汰）
总占用 ≈ bodies 日增 × session_bodies_days + cache_max_size_gb + SQLite 元数据余量
```

示例：日 1 万轮次、原始 body 平均 100KB、gzip 省 70% → bodies 日增约 0.3GB，
30 天保留约 9GB，加 10GB 缓存上限，规划 25GB 磁盘并预留 20% 余量。

> 注意：`retention.request_logs_days` 在 lite 模式当前没有对应的自动清理 worker
> （只有 cache / bodies 两个 trimmer 随 lite 装配启动），SQLite 请求日志表的增长需人工
> 关注（见 troubleshooting "磁盘空间增长快"）。
