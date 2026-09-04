# 审计报告 · 轴 B：双模式存储闭环（创建/赋值/解析/存储/序列化）

- 日期：2026-09-05
- 仓库：`llm-gateway-go-5`（只读审计，未修改任何源码）
- 审计范围：提交 `035df5f74`（dual-mode storage）+ `db88b7573` merge 引入的 `storage/`（interfaces/types/errors、factory、sqlite、file、memory）、`config/storage.go`、`cmd/gateway/storage_mode_init.go`、`storage_metrics_endpoint.go`、`bg/cache_trimmer.go`、`bg/bodies_trimmer.go`、`monitoring/storage_metrics.go`、`domains/session/v2/cache_v2.go` + `cache_v2_file.go`，以及 `cmd/gateway/main.go` 装配点。
- 前置：已通读 `docs/dual-storage-completion-report.md`（A1–A10 已修项不重复报；「已记录、不在本期修复」清单逐条复核后保持原引用，见文末）。

## 一、发现清单（按严重度排序，≤12 条）

| # | 级别 | 摘要 | 证据（文件:行号） | 引入时间 |
|---|------|------|-------------------|----------|
| B1 | P1 | **lite 模式下 `/healthz` 永远 `ready=false`、`/readyz` 永远 503**。lite 旁路 PG 后 `bootDatabaseURL=""` → `dbConn=nil` → `dbPinger` 不装配；而 `dependenciesReady` 与 `/readyz` 把「db/redis 任一 pinger 为 nil」判为不就绪。K8s readiness 与 `scripts/lifecycle/preflight.sh` 的严格门在 lite 部署下永远不通过（LB 摘流量）。完成报告冒烟只验证了 `/healthz` 返回 200（该端点 fail-open 恒 200，`ready` 字段被忽略），掩盖了此问题 | `cmd/gateway/main.go:432-438`（URL 置空）、`main.go:755-758`（dbPinger 仅在 dbConn 非 nil 时装配）、`domains/streaming/handler.go:7245-7252`（`h.db == nil || h.redis == nil → false`）、`handler.go:7225-7241`（`/readyz` nil → 503） | 24h 引入（装配后果；handler 判空逻辑为存量） |
| B2 | P1 | **lite 工厂产出的五个 store 全部零生产调用方，lite「持久化」实际不可达**。全仓（cmd/gateway 等）无任何 `NewSessionStore/NewTurnsStore/NewRequestLogStore/NewBodiesStore/NewStateStore` 调用：SQLite 启动建库建表后无一行读写；`FileBodiesStore` 惰性单例永不创建 → 请求/响应 body 在 lite 下**不落任何持久层**；`/metrics/storage` 的 writes 恒 0（唯一打点方 `FileBodiesStore.Write` 不可达）；BodiesTrimmer 长期清理空目录。完成报告记录的「lite 请求路径未整体切换到 SQLite（路由/凭证等子系统降级）」未覆盖这一点——是新 store 本身不可达，而非仅旧子系统未迁移 | `storage/factory/factory.go:177-272`（五个 New* 无生产 caller，grep 证实仅 `*_test.go` 引用）、`cmd/gateway/storage_mode_init.go:101-112`（工厂仅被构造、store 未取出）、`storage/file/bodies_store.go:111`（唯一 RecordWrite） | 24h 引入（对已记录边界的补充性新证据，建议升级该记录项的表述） |
| B3 | P2 | **FileCache 记账自愈（A4 修复）残留负漂移**：溢出路径先 `fc.sizeUsed = onDisk` 重置（onDisk 刻意排除 excludePath 的旧文件），随后 Set 仍执行 `fc.sizeUsed += len(data) - oldSize`，多减了一个从未计入的 `oldSize` → 每次溢出覆盖写少记 `oldSize` 字节。后果：`sizeUsed` 被低估 → 容量守卫失效、磁盘占用可超 `maxSize`，直到下一次全树 Walk 自愈才复位；:293 注释声称「excludePath…落盘后按新值累加」与代码行为不符 | `domains/session/v2/cache_v2_file.go:294`（重置不含 excludePath）、`:225`（`+= len(data) - oldSize`）、`:292-293`（注释） | 24h 引入（A4 修复的回归边界） |
| B4 | P2 | **多层失效竞态：Invalidate 窗口内 L1.5 命中把脏数据回填 L1**。Invalidate 顺序为 L1 → L1.5 → L2；并发 Get 在「L1 已删、L1.5 未删」之间命中 L1.5 并执行 `c.l1.Set(state)` 重新播种 → Invalidate 返回后 L1 残留已失效状态，最长存活 `defaultGovernanceTTL`（30min）。L1.5 是本期新增层，该窗口为 24h 引入（建议先删 L1.5/L2 再删 L1，或版本号失效） | `domains/session/v2/cache_v2.go:280-296`（Invalidate 顺序）、`cache_v2.go:182-189`（L1.5 命中 → `c.l1.Set`） | 24h 引入 |
| B5 | P2 | **AsyncFileWriter 同路径并发写共用固定临时文件，可静默落盘损坏数据**：tmp 名固定为 `path+".tmp"`，4 个 worker 可能并发执行同路径两个任务——`os.WriteFile` 互相截断交错后，先完成者 rename 出混合内容并**返回 nil**，后者的 rename ENOENT 报错。同路径覆盖写（如轮次重试重写 `turn_N.json.gz`）即触发。对比：同仓 FileCache 已用 `os.CreateTemp` 唯一 tmp 规避（:200），实现不一致。当前无生产调用方（见 B2），属潜伏雷 | `storage/file/async_writer.go:209`（`tmp := path + tmpSuffix`）、`:210-218`（Write+rename）、对照 `domains/session/v2/cache_v2_file.go:200` | 24h 引入 |
| B6 | P2 | **`ListRequests(nil)` 返回全租户请求日志**。已记录项「`GetRequest` 无租户限定」仅点名 GetRequest；`ListRequests` 在 filter 为 nil 或 `TenantID==""` 时同样跨租户返回（默认 100 / 上限 1000 条，含 tenant_id 字段）。未 API 化前无暴露面，但与 GetRequest 属同族越权面，建议与原记录项合并跟踪 | `storage/sqlite/request_log_store.go:123-153`（conds 仅在有值时追加） | 24h 引入（记录项的同族补充） |
| B7 | P3 | **lite 且配置了可达 Redis 时，SetFileCache 摘除 L2 不 Close，泄漏 Redis 客户端**：构造 `NewSessionCacheV2` 已创建 L2 客户端（PoolSize=10、MinIdleConns=2，启动时还做 2s 阻塞 Ping），lite 下 `SetFileCache` 直接 `c.l2 = nil`，`Close()` 因 `c.l2==nil` 跳过 → 空闲连接存活到进程退出 | `domains/session/v2/cache_v2.go:59-66`（构造 L2）、`:88-97`（置 nil 不 Close）、`:110-115`（Close 判 nil）、`cache_v2_redis.go:63-71` | 24h 引入 |
| B8 | P3 | **原子写无 fsync**：bodies 与 FileCache 均为 write→close→rename，不 fsync 文件与目录。掉电/内核崩溃时 rename 可能未持久（bodies 丢数据）或残留孤儿 tmp；`FileBodiesStore` 的 `.tmp` 无任何清理者（仅会话目录过期时随 BodiesTrimmer 整目录删除）。lite 定位单机开发场景，且当前未接线（B2），降为 P3 | `storage/file/async_writer.go:201-220`、`domains/session/v2/cache_v2_file.go:200-222` | 24h 引入 |
| B9 | P3 | **队列满时 enqueue 持 `enqMu` 阻塞，无丢弃/取消策略**：`w.queue <- task` 在锁内阻塞——队列打满后所有入队方（含 `WriteAsync`）在互斥锁上串行化，`Close` 亦被阻塞；task 无 ctx，慢盘会逐级拖住调用方。正确性无恙（背压而非丢任务、关闭排空语义正确），是吞吐/时延隐患 | `storage/file/async_writer.go:120-133`（锁内阻塞发送）、`:153-174`（Close 抢同一把锁） | 24h 引入 |
| B10 | P3 | **FileCache 全程持锁做全树 Walk 与文件 IO**：Set 在 `fc.mu` 内执行 `filepath.Walk`（10GB/大目录时秒级）+ CreateTemp/Write/Rename，期间 `removeExpired/removeIfUnchanged/Stats(=GET /metrics/storage 的 file_cache 段)/Delete` 全部阻塞。读主路径（Stat+ReadFile）不加锁不受影响 | `domains/session/v2/cache_v2_file.go:183-229`、`:268-310`、`:314-356` | 24h 引入 |
| B11 | P3 | **shard 目录截断实现不一致**：FileCache 按字节切 `shard[:2]`，FileBodiesStore 按 rune 切；多字节 sessionID 在 FileCache 侧会产生非法 UTF-8 目录名（validCacheID 不拦非 UTF-8）。ID 为网关生成的 ASCII 时无实害，属一致性隐患 | `domains/session/v2/cache_v2_file.go:105-111` vs `storage/file/bodies_store.go:47-55` | 24h 引入 |
| B12 | P3 | **配置面小问题（合并）**：a) `NormalizeMode` 文档注释仍写「不做 trim」，与 A9 修复后的 `TrimSpace` 实现矛盾（`config/storage.go:82-88`）；b) `ApplyLiteDefaults` 把 retention 零值兜成默认值 → 无「关闭保留期」的手段，且 `Validate` 不校验 `synchronous=OFF/journal_mode=MEMORY` 等危险显式值与目录重叠（CacheDir==BodiesDir 会交叉清理）（`config/storage.go:129-179, 96-125`） | 见左 | a 为 24h 引入的注释遗留；b 为 24h 引入的校验缺口 |

## 二、分层闭环验证矩阵

符号：✅ 正确接线；🟡 有条件/注意项；❌ 不可达或缺失；（#n = 对应发现编号）

| 层 | 写入 | 读取 | 回填 | 失效 | 打点 |
|----|------|------|------|------|------|
| **L1** CompressionMetaCache（内存 LRU×1024，TTL 30min） | ✅ Set 前 clone，防外部变异（cache_v2.go:475-509） | ✅ Get 返回深拷贝；嵌套 map 经 JSON round-trip（数值统一变 float64，各层一致，cache_v2.go:397-465） | ✅ L1.5 命中与 L3 命中均回填（:187, :229-231） | 🟡 Delete 正确，但存在 B4 重播种窗口 | ✅ Hit/Miss（:172,176） |
| **L1.5** FileCache（lite 专属，tmp+rename 原子写） | 🟡 原子性正确；记账有 B3 负漂移；写路径无打点 | ✅ mtime TTL、损坏 JSON 按未命中处理并安全清理（`removeIfUnchanged` 双检防误删，:337-356）；路径 ID 校验防穿越（:115-120） | ✅ L3 命中回填（:234-238）；与 CacheTrimmer 并行删除经 A4 自愈兜底（B3 为残留缺口） | ✅ Delete 幂等、Invalidate 全层覆盖 | ✅ Hit/Miss（:184,191）；写入无打点 |
| **L2** RedisGovernanceCache（full 专属，HSet+TTL 30min） | ✅ Pipeline HSet+Expire；fail-open（cache_v2_redis.go:146-177） | ✅ SafeHGetAll 防 WRONGTYPE；miss=(nil,nil)（:99-140） | ✅ L3 命中仅回填 GovernanceMeta（:239-243） | ✅ Del；lite 下整层摘除（不产生 Redis 访问）——但 B7 泄漏客户端 | ✅ Hit/Miss（:204,206） |
| **L3** SessionTurnsReader（PG；lite=nil 恒 miss） | —（只读回源；写入走 session_writer，full 既有链路） | ✅ latest+marker_turn 双 CTE；ErrNoRows=(nil,nil)；compression_meta 容错解析（:610-685）。**lite 不触 PG** ✅（nil db 短路 :611-613） | ✅ L3 命中回填 L1+L1.5/L2 | —（源数据层） | ✅ Query 次数+平均延迟（:216-218） |
| **Bodies** FileBodiesStore（gzip 分层目录） | 🟡 gzip Close 时序正确、路径穿越已防（A2）；B5 同路径竞态、B8 无 fsync | ✅ ErrNotFound 哨兵、gzip 头/体损坏分级报错、ReadRange 跳缺失轮次中止损坏（bodies_store.go:122-173） | — | ✅ RemoveAll 幂等；与 BodiesTrimmer 按目录 mtime 删除无记账耦合（FileCache 无账本涉 bodies） | ✅ RecordWrite 已接（:111）——但生产不可达（B2）→ writes 恒 0 |
| **SQLite 三 store**（lite 元数据） | ✅ 参数化 SQL、UPSERT、时间零值兜底；`COALESCE(has_body,0)`（A8） | ✅ ErrNotFound 映射；**请求日志 Body 设计性不落库**，`has_body=1` 但读回恒 nil——在 lite 下与未接线的 Bodies 组合后该标记悬空无解（B2） | — | ✅ DeleteSession 存在但不级联（沿用已记录项） | ❌ 无任何打点（未接线） |
| **State** MemoryStateStore（lite KV） | ✅ TTL+closed 语义（:98-111） | ✅ RWMutex 双检惰性删除，指针比对防误删新值（:116-144） | — | ✅ Delete 幂等；janitor 随 Close 退出 | ❌ 无打点；**未接入 HTTP 装配层**（沿用已记录项，B2 同源） |

**模式旁路验证**：lite 下 PG 完全旁路 ✅（`bootDatabaseURL=""` → `openDBWithBootRetry` 返回 nil，`main_helpers.go:115-118`）；V2 缓存 lite 装配不触 Redis 读写 ✅（L2 已摘）；但 B1（健康探针）与 B2（存储不可达）是旁路的两个直接副作用。

## 三、已记录项复核（全部仍存在，保持原引用，不展开）

- `retention.request_logs_days` 有配置无执行者 ✅ 仍在（`storage_mode_init.go:137-147` 仅装配 cache/bodies 两个 trimmer）。
- `GetRequest` 无租户限定 ✅ 仍在（`request_log_store.go:96, 23-26`）；另见本报告 B6（ListRequests 同族）。
- `DeleteSession` 不级联清理 turns/bodies ✅ 仍在（`session_store.go:154-163` 仅删 sessions 行）。
- StateStore 未接入 HTTP 装配层 ✅ 仍在（全仓无生产调用，见 B2）。
- `sqlite_pragmas` 无 env 变量 ✅ 仍在（`config/storage.go:231-296` applyEnvOverrides 无 pragma 项）。
- trimmer `Start` 阻塞式 ✅ 仍在（`bg/cache_trimmer.go:61`，文件头已注明）；中文错误串 ✅ 仍在（storage/sqlite 各 store）。
- A5 复核：`config.example.yaml:68` 已默认 `storage_mode: "full"` ✅。

## 四、总体结论

1. **核心缓存链路质量良好**：L1→L1.5→L3（lite）与 L1→L2→L3（full）的读/写/回填/失效语义清晰，fail-open 一致，路径穿越与 PRAGMA 注入防护到位，序列化各层类型形态一致（嵌套 map 数值统一 float64，无热/冷行为分叉），打点接线完整（A3 修复有效，仅 L1.5/SQLite/State 写路径无打点，可接受）。
2. **最大风险在装配而非实现**：B1（lite 健康探针恒不就绪，会直接破坏 K8s/preflight 生命周期）与 B2（lite 五个新 store 零调用，持久化承诺名不副实、`/metrics/storage` writes 恒 0）表明「lite 模式可运行」但「lite 模式的存储尚未真正闭环」。两者均建议在下一波次接线时一并解决。
3. **A4 修复引入了残留记账缺口**（B3），且新增 L1.5 层带来经典的失效重播种竞态（B4）；异步写器在并发同路径写（B5）与队列背压（B9）上各有一个潜伏缺陷。以上均为小改动可修复项。
4. 断电恢复：SQLite 侧 WAL+synchronous=NORMAL 的默认组合在崩溃后保证一致性（可能丢最近提交），自动恢复无需干预；文件侧见 B8。
