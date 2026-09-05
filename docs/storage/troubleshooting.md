# 存储故障排查手册（Troubleshooting）

适用范围：双模式存储架构（lite：SQLite + 本地文件 + 内存；full：PostgreSQL + Redis）。
配置项含义与默认值见 [deployment-guide.md](deployment-guide.md)，架构总览见 [README.md](README.md)。

---

## 一、常见问题排查（现象 → 原因 → 处理）

### 1. `database is locked`（SQLITE_BUSY）

- **现象**：日志出现 `database is locked` 或 `SQLITE_BUSY`，写入偶发失败。
- **原因**：SQLite 单写者——同一时刻只允许一个写事务；多个连接同时写且写锁等待超时
  （默认 `busy_timeout=5000ms`）即报错。
- **处理**：
  1. 调大 `lite_storage.sqlite_pragmas.busy_timeout_ms`（如 `10000`），让写锁冲突时多等一会；
  2. 确认 `journal_mode` 为 `WAL`（默认即 WAL）：WAL 下读不阻塞写，可显著缓解锁冲突；
  3. 降低并发写入源头：减少 `async_writers`（bodies 并发落盘 worker 数，默认 4）或让上游
     批量化写入；
  4. 检查是否有其他进程（如手工 `sqlite3` 会话、备份工具）持有写锁。

### 2. 启动报 full 模式缺 postgres_url / redis_url

- **现象**：启动失败，报
  `storage_mode "full" requires full_storage.postgres_url (env LLM_GATEWAY_POSTGRES_URL)`
  或 `... requires full_storage.redis_url (env LLM_GATEWAY_REDIS_URL)`。
- **原因**：`Validate` 要求 full 模式下两个地址均非空；YAML 与 env 均未提供。
- **处理**：
  ```bash
  export LLM_GATEWAY_STORAGE_MODE=full
  export LLM_GATEWAY_POSTGRES_URL="postgres://user:password@127.0.0.1:5432/llm_gateway?sslmode=disable"
  export LLM_GATEWAY_REDIS_URL="127.0.0.1:6379"
  ```
  或在 YAML `full_storage` 段补齐。注意 env 只填充 YAML 未配置（零值）的字段，显式 YAML
  值优先。若只想本地跑通，直接改用 `LLM_GATEWAY_STORAGE_MODE=lite`。

### 3. SQLite 数据库文件损坏

- **现象**：启动报 `sqlite: 连接数据库 ... 失败` 或运行期查询报 `database disk image is
  malformed`。
- **原因**：异常断电/进程被 `kill -9` 时 WAL 未正常落盘、磁盘写坏块、或有人手工删除了
  `-wal` / `-shm` 文件导致状态不一致。
- **处理**：
  1. 先备份现场：`cp data/llm-gateway.db* /tmp/`（连同 `-wal`、`-shm` 一起拷）；
  2. 用 SQLite 自带工具尝试恢复：
     ```bash
     sqlite3 data/llm-gateway.db "PRAGMA integrity_check;"
     sqlite3 data/llm-gateway.db ".recover" | sqlite3 data/llm-gateway-recovered.db
     ```
  3. 恢复后用新文件替换原文件再启动；建表语句全部 `IF NOT EXISTS`，缺失的表会在启动时
     自动补齐；
  4. 无法恢复时：删除三件套（`.db`/`-wal`/`-shm`）重启，网关按全新库冷启动（历史会话
     元数据丢失，bodies 文件仍在但失去元数据索引）。
- **预防**：保持 `synchronous=NORMAL`（默认）不被调低；对 `data/` 目录做周期性文件备份。

### 4. bodies 文件读取返回 ErrNotFound

- **现象**：读轮次内容报 `storage.ErrNotFound`（可用 `errors.Is` 判定）。
- **原因**：按以下顺序排查——
  1. 路径不存在：确认文件确实在
     `{bodies_dir}/{tenantID}/{sid前2位}/{sid}/turn_{N}.json.gz`（sessionID 不足 2 位时
     分片目录为全量 sessionID）；
  2. 轮次从未写入：该轮次请求失败或尚未落盘（异步写队列中的任务在进程崩溃时会丢失）；
  3. retention 清理已删除：会话目录超过 `session_bodies_days` 被整体删除；
  4. 人工/脚本误删（见"磁盘空间管理"）；
  5. 传参错误：tenantID 与写入时不一致（目录名严格区分大小写）。
- **处理**：确认原因后，或让上层重写该轮次，或接受 `ReadRange` 语义——区间内缺失的轮次
  会被自动跳过（仅 ErrNotFound 跳过；gzip 损坏等其他错误会中止返回）。

### 5. 缓存命中率低

- **现象**：请求大量回源 L3（冷启动），延迟升高。
- **分析**：
  1. **重启后 L1 必然冷**：L1 是进程内存 LRU（1024 条），重启后全部回源属预期；lite 模式
     L1.5 的价值正是重启后仍可命中——若重启后命中率持续低，检查 L1.5 是否装配
     （`cache_dir` 目录下是否生成 `{tenant}/{sid前2位}/{sid}.json`）；
  2. **TTL 过短**：`cache_ttl_hours` 太小会导致 L1.5 文件很快过期（mtime 超过 TTL 即视为
     过期并在读时删除）；默认 24h，会话复用周期长可调大；
  3. **容量淘汰过频**：`cache_max_size_gb` 太小会按 mtime 从旧到新频繁淘汰热点文件；
     观察 `cache/` 目录占用是否长期贴着上限；
  4. **会话本身分散**：会话数远超 L1 容量（1024）且访问随机时，L1 命中率天然低，属容量
     权衡而非故障；
  5. **L1.5 单文件覆盖写**：同一会话每次写都会刷新 mtime，活跃会话不易过期；命中率低的
     会话若集中在长尾，属正常分布。
- **注意**：full 模式不使用 L1.5，其命中率问题应分析 L2（Redis）与 L1。

### 6. 磁盘空间增长快

- **现象**：`data/` 目录持续增长，磁盘告警。
- **排查顺序**：
  1. **retention 是否生效**：`retention.cache_hours` / `retention.session_bodies_days` 由
     `bg.CacheTrimmer`（默认 1h 周期）/ `bg.BodiesTrimmer`（默认 6h 周期）清理，两者随
     lite 模式装配自动启动，启动即先执行一次。检查启动日志 `cache trimmer 已启动` /
     `bodies trimmer 已启动`，以及周期性出现的 `cache_trimmer: 清理完成` /
     `bodies_trimmer: 清理完成`（含删除量与释放字节）；日志正常但目录仍涨，按第 2~5 步
     继续排查。注意 `retention.request_logs_days` 当前**没有**对应的自动清理 worker，
     SQLite 请求日志表只增不减；
  2. **cache 是否贴上限**：`du -sh data/cache`；L1.5 有 `cache_max_size_gb` 硬上限（按
     mtime 淘汰），若远小于上限仍在增长，增长主体不是 cache；
  3. **bodies 增长估算**：按 deployment-guide 的估算公式核对日增是否符合预期；不符合时
     检查是否有异常大 body（`du -ah data/session_bodies | sort -rh | head`）；
  4. **gzip 是否启用**：bodies 固定 gzip 落盘（文件后缀 `.json.gz`）。若发现 `.json` /
     `.tmp` 残留文件堆积，说明写入中断过多（`.tmp` 为原子写残留，见下文 FAQ）；
  5. **WAL 文件过大**：`llm-gateway.db-wal` 偶尔会到几百 MB，属 WAL 正常行为，进程正常
     退出时会 checkpoint 回收；长期不收缩可执行 `sqlite3 data/llm-gateway.db
     "PRAGMA wal_checkpoint(TRUNCATE);"`（需停写窗口）。

---

## 二、性能瓶颈分析思路

### 写入慢

1. **async writer 队列满**：任务队列固定缓冲 1000；队列打满时入队阻塞，表现为写延迟上升。
   结合 `AsyncFileWriter.Stats()`（`total_writes` / `total_bytes` / `failed_writes`）判断：
   `failed_writes` 增长快 → 看具体错误（磁盘满/权限）；吞吐上不去 → 加大 `async_writers`。
2. **磁盘 IO 瓶颈**：`iostat`/`iotop` 观察磁盘 util 与 await；机械盘建议换 SSD，或降低
   `async_writers` 减少随机写。
3. **SQLite 单写者**：SQLite 同一时刻只允许一个写事务，所有元数据写（sessions/turns/
   request_logs）在库级串行。WAL 缓解的是"读写互斥"，不消除"写写互斥"。写 QPS 高时
   优先批量化上游请求，而不是简单加并发。

### 读放大（L1.5 miss 率高）

- 每次读 miss 都要回源 L3：读 SQLite 元数据 + 读 gzip body 并解压 + JSON 反序列化，
  成本远高于 L1 内存命中。
- 分析路径：确认 L1.5 文件是否存在且未过期 → 统计回源请求占比 → 按"缓存命中率低"一节
  调 TTL/容量。
- 损坏的 L1.5 JSON 会被当未命中处理并顺带清理（fail-open），不会报错，但会推高 miss 率；
  可检查日志中的 `file cache: unmarshal` 告警。

### SQLite 单写者特性说明

- 写串行是 SQLite 的设计约束：WAL 模式允许读写并发、多读无锁，但写事务全局排队，
  靠 `busy_timeout` 等锁。
- 因此 lite 模式的定位是**本地开发/单机轻量部署**；若压测中 SQLite 写成为瓶颈且无法通过
  批量写缓解，说明该负载应切换到 full 模式（PostgreSQL）。

---

## 三、磁盘空间管理

### 手动清理的安全做法

| 目录 / 文件 | 能否删 | 影响与做法 |
|-------------|--------|-----------|
| `data/cache/` | 可整体删 | L1.5 只是缓存，删除后进程重启或继续运行均可自愈（下次读回源 L3 后重建）；建议停进程后删除。注意 `cache/` 下名称含 `.tmp-` 的隐藏临时文件也可一并清掉 |
| `data/session_bodies/{tenant}/{p2}/{sid}/` | 可按会话删 | 删除某会话目录 = 丢弃该会话全部轮次原文；该会话后续冷启动将拿不到历史 body（L3 回源失败）。**不要删除整个 `session_bodies/`**，除非接受全部历史内容丢失 |
| `data/request_logs/` | 可清 | 请求日志目录（当前元数据落 SQLite `request_logs` 表）；目录本身为空壳时删除后会被工厂重建 |
| `data/llm-gateway.db`（及 `-wal`/`-shm`） | 谨慎 | 删除即重置全部元数据（sessions/turns/request_logs/configs）；只应在故障恢复或确认可丢数据时操作，见"SQLite 文件损坏"一节 |
| `*.tmp` 残留（bodies 目录内） | 可删 | `AsyncFileWriter` 原子写失败时可能残留 `<name>.json.gz.tmp`；进程停止后可安全清理 |

通用流程：**先停进程（保证优雅关闭、排空异步写队列），再清理，再启动**。

### 监控建议指标

- 磁盘：`data/` 总量与分目录（`session_bodies` / `cache` / SQLite 三件套）日增量；
  `cache/` 占用相对 `cache_max_size_gb` 的水位。
- 写入：`AsyncFileWriter.Stats()` 的 `total_writes`、`failed_writes`（失败率突增先看磁盘）。
- 缓存：L1.5 `Stats()` 的 `size_used_bytes` / `max_size_bytes` / `usage_percent`（>90% 预警）。
- SQLite：`.db-wal` 文件大小（长期 >1GB 需 checkpoint）、`integrity_check` 周期巡检。
- 保留期：`cache_trimmer: 清理完成` / `bodies_trimmer: 清理完成` 日志中的删除文件/会话数
  与释放字节；删除量长期为 0 而目录在涨，说明 trimmer 未运行或清理目录与实际写入目录
  配置不一致。

---

## 四、FAQ

**Q1：lite 和 full 之间如何切换？**
改 `storage_mode`（或 env `LLM_GATEWAY_STORAGE_MODE`）后重启生效。从 lite 切 full 前
请先完成数据迁移（sqlite → postgres），否则 full 模式看不到 lite 期间的数据。

**Q2：SQLite 能否用 `:memory:`？**
不能。WAL 模式不支持内存库，`sqlite_path` 应使用文件路径。

**Q3：为什么我配了 YAML 又配了 env，最终生效的是 YAML 的值？**
env 只填充 YAML 未配置（空字符串/零值）的字段，显式 YAML 配置优先。要覆盖 YAML 值请直接
改 YAML（或删掉该 YAML 字段）。

**Q4：`async_writers` 调到多大合适？**
默认 4 适合多数本地场景；SSD 上写入瓶颈时可试 8。再大收益递减且加剧磁盘随机 IO 与
SQLite 写竞争。

**Q5：L1.5 缓存文件是明文吗？安全吗？**
是明文 JSON（`SessionStateV2` 快照），只含压缩/治理元数据，不含 summary 明文与完整
body；body 原文在 `session_bodies/` 下为 gzip。单机部署请像对待日志一样对待 `data/`
目录的访问权限。

**Q6：进程被 kill -9 会丢数据吗？**
可能丢两类：异步写队列中尚未落盘的轮次 body；SQLite 最近事务（`synchronous=NORMAL` 下
断电最多丢最近事务，不会损坏库）。lite 模式下正常停止（SIGTERM/SIGINT）会触发
`storageRuntime.Shutdown`：先取消两个 trimmer（有界等待 3s），再 `factory.Close()`——
内部先排空 bodies 异步写队列保证已提交写入全部落盘，再关 SQLite。该链路已真机 smoke
验证（优雅关闭 exit 0）。

**Q7：`ReadRange` 遇到缺失轮次会报错吗？**
仅 `ErrNotFound` 的轮次会被跳过（支持稀疏区间）；gzip 损坏等其他错误会立即中止并返回。

**Q8：改了 `sqlite_path` / `bodies_dir` 后，历史会话"消失"了？**
不是数据丢了，是打开了一个新的空库/空目录。所有路径都是相对当前工作目录解析的
（默认 `./data/...`），从别的目录启动进程、或改了配置路径，都会定位到新位置。把配置改回
原值或以原工作目录启动即可找回。

**Q9：进程重启后 StateStore（运行时状态）还在吗？**
不在。lite 模式的 `MemoryStateStore` 是纯进程内存实现（带 TTL 过期清理），数据不落盘；
需要持久化的状态不应放进 StateStore。L1.5 文件缓存是唯一"重启后仍可命中"的层。

**Q10：如何查某次请求的元数据？**
lite 模式查 SQLite `request_logs` 表（主键 `request_id`，含 tenant_id、session_id、
ts、method、path、status_code、duration_ms、has_body），例如：

```bash
sqlite3 data/llm-gateway.db \
  "SELECT request_id, ts, status_code, duration_ms FROM request_logs
   WHERE tenant_id = 'my-tenant' ORDER BY ts DESC LIMIT 20;"
```

