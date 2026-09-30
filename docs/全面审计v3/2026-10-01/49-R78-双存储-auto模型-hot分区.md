# 49 号报告 · R78：双重存储架构 / auto 模型 / hot+columnar 分区

日期：2026-10-01
轮次：R78（主代理 + 2 个 verifier 子代理，A=双存储、B=分区、C=auto 模型）
基线：`2db97cca7`

---

## 0. 结论摘要

| 域 | 子代理定级 | 主代理复核 | 处置 |
|---|---|---|---|
| A 双重存储（全量/简化） | PARTIAL，3 缺陷 | **全部复核为真** | 1 修代码 + 2 订正注释/文档 |
| C auto 模型 | PARTIAL，5 缺陷 | 1 幽灵码已修，余登记 | 修 1 + 登记 4 |
| B hot+columnar 分区 | FAIL | **成立且比子代理更严重** | 建永久守卫 + 订正 3 处文档失真 |

本轮修掉 1 个 P1 代码缺陷、1 个 P2 幽灵码，新建 1 道永久守卫（拦截 **18 处**红线违规），订正 6 处注释/文档失真。

---

## 1. A 域：双重存储架构（lite / full）

### 1.1【P1 · 已修】lite 模式的 `session_turn_details` 无任何保留期清理

**事实**：`bg/lite_retention_worker.go` 的 `RunOnce` 只删三张表——`request_logs`（按 ts 逐行）、`session_turns`（按超龄会话）、`sessions`。`session_turn_details` **不在其中**。

**为什么是缺陷而不是能力边界**：该表是 `session_turns` 的特征层投影（`storage/sqlite/schema.go:56-79`），每轮追加一行，且与 `session_turns` 之间**没有外键**（PRIMARY KEY / UNIQUE 均不含 FK）。所以删除会话不会连带删掉特征行。更强的证据是它建了 `idx_turn_details_ts`（索引的存在说明设计时就预期按 ts 查询），却没有任何 DELETE 路径。该 worker 的文件头自述其存在理由是「lite 长跑进程的 SQLite 单文件无界增长」——漏掉这张表使该修复目标**部分落空**。

**"0 命中问两次"已执行**：grep 的是**使用方**（`WriteTurnDetails` 的写方、`DELETE FROM session_turn_details` 的删方），不是声明方。写方唯一（`cmd/gateway/lite_telemetry_sink.go:214`），删方全仓为零；`storage/consistency.go` 对账 worker 也不覆盖该表。

**修法**（`RunOnce` 签名扩为 4 个计数）：

1. 特征层清理进同一事务、共用同一 EXISTS 谓词（先删子层）。
2. **额外补一条孤儿回收**：`ts < cutoff AND NOT EXISTS(对应 sessions 行)`。

第 2 条不是锦上添花，是**修法成立的前提**：修复前已经产生的「会话行已删、特征行留下」组合，永远等不到一个可匹配的会话行——只补 EXISTS 谓词等于只挡新垃圾、存量永远不清。写序安全性：`ensureSession` 先于 `WriteTurnDetails`（`lite_telemetry_sink.go:172` → `:214`），故「有特征行必有会话行」是写侧不变量；再加 `ts` 谓词兜底，未来写序变化也只会漏删、不会误删近期数据。走 `idx_turn_details_ts`。

**验证**：3 条新用例 + 3 处变异（删 EXISTS 清理 / 删孤儿回收 / 去掉 ts 安全边界），全部转红且各由对应用例抓住，还原 `cmp` 逐字节一致。`go test ./bg/` 全包绿。

> 一条夹具被证伪的记录：初版我写了一条「跨租户不误删」用例，实测 `sessions.id` 是**全局** PRIMARY KEY（外加冗余的 `UNIQUE(tenant_id,id)`），同 id 跨租户共存**不可表示**。按纪律（第一次报红先假设门是对的，但也要问判据是否过宽）——门是对的、夹具是错的，改写成孤儿回收 + 近期孤儿不回收两条。

### 1.2【P1 · 订正文档】lite 模式 L3 冷启动回源结构性不存在，文档两处声称走 SQLite

`docs/storage/README.md` 的 lite 数据流图与组件表都写 L3 = 「SQLite + 本地文件」。实现是：`SessionTurnsReader.db` 的类型 `sessionTurnsDB` 方法签名是 `QueryRow(...) pgx.Row`，SQL 硬编码 `public.session_turns_with_current_month` 与 PG 专有的 JSONB 运算符 `compression_meta ? 'cut_marker'`——**物理上无法接 SQLite**。lite 装配时传 nil（`main.go:2710`），`LoadState` 恒 miss。

同一 README 第 80 行本就承认「db 为 nil 时防御性返回 miss，缓存退化为 L1 + L1.5」——**与上方图表自相矛盾**。已按实现订正两处，并写明代价：lite 与 full 在**进程重启后**的会话恢复能力不对等（lite 只能靠 L1.5 文件快照，TTL/损坏即失忆）。**未实现 SQLite L3**——属能力缺口，登记待裁决。

### 1.3【P2 · 订正注释】`session_logs_view` 零生产消费者，却被登记为 lite「唯一读路径」

全仓（含所有文件类型扫描）grep `session_logs_view`：Go 代码里只有 `schema.go:84` 的 CREATE VIEW 本身，另有 `lite_telemetry_sink.go:136` 与三份文档的注释声称。实际 lite 读面只有 `/api/lite/sessions`（`admin/lite_sessions.go:22-52`），返回会话列表，**没有任何 request-log 查询端点**；`ListRequests` 生产无调用方。

危害：运维若按 `storage.request_logs_write_enabled=false` 停写，lite 将**既不写也不读** request_logs。已把两处注释改为「零生产消费者，读端待接线」，视图保留为迁移目标。

### 1.4 已核对无问题（带证据）

- **工厂 full 分支是桩，不是漂移**：`storage/factory/stubs.go` 全部返回 `ErrNotImplemented`，`NewProviderStore` 等 4 处 `return nil` 有 TODO 与已文档化的理由。故「PG vs SQLite 同表漂移」在本范围内**不成立为可运行缺陷**——这是子代理最重要的判断，我复核后认同。
- lite 模式**不产生任何 Redis 访问**（4 处独立判据交叉验证）；`effectiveMode` 归一化正确；5 个 lite 判据全部经 `liteMode()`，无一处直判 `storageRt != nil`。
- 关闭顺序幂等；`journaled` 去重集的并发逻辑自洽（按 expiry 淘汰而非 FIFO，注释已论证理由）。
- `dual_mode_parity_test.go` 的 full 半边**不会伪装绿灯**：无 `TEST_PG_URL` 时显式 Skip 并写明「This is NOT evidence of equivalence」，且因为 full 分支是桩，即使设了变量也会在 `require.NoError` 处失败。

### 1.5 能力边界（未实证）

- full 半边等价性未实证（本机 full 分支是桩，真跑必失败）。
- PG 侧 97 列 DDL 未逐列反查（散落 20+ 迁移文件，需活库）。
- 「重启后失忆」对用户的实际可观察后果未运行验证。

---

## 2. C 域：auto 模型

**真实运行闭环已确认**（真库只读）：`auto_route_selections` 1747 行零 NULL、settle 1747/1747、affinity 96 行 11 分钟前更新。

### 2.1【P2 · 已修】`auto_route_unavailable` 幽灵错误码

`adminLLMShouldRetryExplicit` 的第三个分支匹配 `auto_route_unavailable`，而该串**全仓无产生方**——只存在于 `domains/streaming/auto_route.go` 的文件头注释里。真实错误码是 `auto_route_decider_failed`（`handler.go:3097/3103` 等三处产出）。该分支**恒为 false**：内部 LLM 任务（`model:"auto"`）在 auto 路由失败时不会触发显式模型重试。

**为什么长期没被发现**：`admin_llm_task_test.go` 的用例喂的是**同一个幽灵字面量**，函数对假字符串当然返回 true，测试一直是绿的——测试固化了一个不存在的错误码，恰好把真实缺陷盖住。已把用例改成真实错误码并加反向断言。

同时订正 `domains/streaming/auto_route.go` 文件头：原文声称「回落到显式模型路径」且「code=auto_route_unavailable」，两处都与实现相反，且与同文件 612 行自相矛盾。

### 2.2【P2】`candidates_top3` 是「前 3 个渠道」不是「前 3 个模型」

去重键是 `CredentialID`（`autoroute/index.go:267,272`）。真库 203 行里 162 行只有 1 个不同模型重复 3 次。列名与语义相反，**未修**——改名涉及落库语义与读端消费方，须与产品确认口径。

### 2.3 登记未修

- `decider==nil` 路径落库只写 `is_auto_request`（当前不可达，`main.go:5126` 是裸 `{}` 块）。
- `auto_confidence` 恰为 0 时被 `nilIfZeroF64` 丢弃。
- `tuning_signals.quality_score` 全表恒 0.75 ⇒ `tuning_proposals` 恒 0 行 ⇒ AutoTuningView 提案列表恒空（**未验证**，需真库确认）。

---

## 3. B 域：hot + columnar 分区

### 3.1 红线违规远多于子代理初报：18 处 / 10 文件（真库 + 静态双向核实）

objective 红线是「更新、删除只能在 hot 表中进行」。子代理报 3 处；本轮新建的 `partguard` 静态扫描在**真库对齐 32 个分区父表**后，扫出 **18 处 Go 侧父表直写，分布在 10 个文件**：

| 文件 | 表 | 处数 |
|---|---|---|
| `domains/stats/inbox_consumer.go` | `stats_event_inbox` | 4 |
| `cmd/gateway/turn_logs_aggregator.go` | `sessions` | 2 |
| `domains/session/v2/session_aggregator.go` | `sessions` | 2 |
| `internal/titlestore/store.go` | `sessions` | 2 |
| `admin/session_turns_v2.go` | `sessions` | 1 |
| `bg/opslog_trimmer.go` | `candidate_failure_logs` | 1 |
| `cmd/tools/validate_sessions_v2/repair.go` | `session_bodies` / `session_turns` / `sessions` | 3 |
| `db/db.go` | `session_bodies` | 1 |
| `domains/session/v2/session_aggregator.go` | `session_turns` | 1 |
| `domains/stats/event_writer.go` | `stats_event_inbox` | 1 |

**全部 10 个文件都 import pgx、0 个 import database/sql**，即均为 PostgreSQL 侧，非同名 SQLite 表。其中多处带**手写分区 pin 绕行**，注释自认，例如 `admin/session_turns_v2.go:850`：

> `// Partitioned sessions table: UPDATE ... ORDER BY/LIMIT is invalid in PostgreSQL.`
> `// Pin the newest partition via MAX(partition_date), same pattern as titlestore.`

**这些站点今天能跑，只因为涉及的父表恰好还是 heap。** 按 objective 列存化后，citus-columnar 引擎直接拒绝 UPDATE/DELETE。

### 3.2 列存覆盖面：32 个父表，20 个分区全为 heap（真库实测）

`pg_inherits` + `pg_am` 聚合查询结果：仅 `routing_decision_log` / `handoff_logs` / `request_logs_bodies` / `supplier_errors` 四个父表分区全 columnar。`request_logs`(5.0GB)、`session_bodies`(8.4GB)、`session_turns`(6.4GB) 等大表分区为 heap。

成因（非偶然）：只有 6 个 `ensure_*_partition` 函数生成 `USING columnar`；运行时兜底的事件触发器白名单**只硬编码 2 个父表**（`sql/scripts/phase-23-columnar-invariant/02-event-trigger.sql:81`）。

**这与 3.1 互为因果**：不能在不先解决 18 处父表直写的前提下强行列存化。故本轮**不做列存化改造**（架构决策），改为建守卫挡住第 19 处。

### 3.3【文档失真 · 已订正】session_* 父表无 TTL，但最新审计文档称「按自身 TTL 退役」

`docs/audit/2026-10-01-bodies-columnar-followup.md` §五写「session_bodies_2026_09（20GB）按自身 TTL 退役」。实测**无任何 TTL**：

- `bg/partition_manager.go` 的 `stateTableTTLSpecs()` 7 项不含 session_* 族；
- SQL 侧 `drop_old_state_partitions` 的 `target_tables` 不含；
- `settings/` 无对应项（`retention_session_bodies_days` 是 **lite 文件系统热区**配置，与 PG 分区无关）。

即 `session_bodies` / `session_turns` / `session_turn_details` 三个父表历史分区**无界增长**。已在原文档就地加订正块。**是否补 TTL 属产品裁决**（涉及保留期与合规口径）。

### 3.4 其它订正

- `partition_manager.go:37-38` 称 promote 函数「installed in migration 336」，但 336 的三个文件在本机均为 `.skip`，该迁移从未在本环境执行。
- `partition_manager.go:44-45` 的 `model_probe_runs` 例外说明让人以为分区结构已废弃，实际父表仍保留 4 个月度分区（3 个 columnar），只是无任何写入方、`count(*)=0`——空壳结构占 152 kB。

### 3.5 已核对无问题（带证据）

- **20/21 个 promote 函数遵守红线**：对真库全部 `promote_*` / `archive_*` 函数体做标识符边界排除的复核，仅 `promote_session_bodies_hot_to_partition` 命中父表。
- **promote 不丢行**：单条 data-modifying CTE，`DELETE ... RETURNING` → `INSERT`，原子可见；`batch` CTE 用 `FOR UPDATE SKIP LOCKED`。
- RLS 旁路正确：promote 事务内 `set_config('app.bypass_rls','true',true)`（`is_local=true`，pooled 连接不留提权）。
- 事务预算齐备：每批独立事务 + 60s statement_timeout + 跨实例 `pg_try_advisory_xact_lock` + 每表保底 1 批（R51 starvation 修复）。
- citus 13.3 的 `XX000 cache lookup failed` 踩点**当前无残留**：全仓非测试 Go 代码无 `SELECT *` 读 unified 视图；`session_bodies_unified` 是显式 13 列投影、无合成列，6 处生产消费点均显式列名。
- 8 个 promote 指标**无沉睡**（逐一确认 recorder 在 promote 路径被调用）。

---

## 4. 新增永久守卫：`internal/partguard`

把「更新/删除只在 hot 表」从文档变成常驻属性，已接进 `GUARD_PACKAGES`（9 项，`guards-sync` 通过，5.7s）。

**判据形态**：只扫**非测试** .go 文件里的**字符串字面量**（SQL 就在其中），不扫注释、不扫 SQL 迁移文件。走 `go/ast` 取字面量而非全文正则，因为注释里提到表名会造成假警报，而正则无法可靠区分「注释里的 UPDATE」与「语句里的 UPDATE」。

**五条判据，任一不满足即红**：

1. 集合必须恰好等于登记的 18 处（多 = 新违规；少 = 登记失效）；
2. 每处**条数**必须相等（同一文件同表再加一条会被计数抓到——只比集合的话第二条会被第一条吸收掉）；
3. 登记理由必填且 ≥20 字（写「同上」即红）；
4. 父表清单每个名字必须在仓内 DDL 中确实以 `PARTITION BY` 声明（防拼错/防把不存在的表写进清单）；
5. `sqliteBacked` 排除项必须仍在承重（SQLite 侧同名表写入若消失，排除项就是死规则）。

### 4.1 判别力：5 处变异全部转红

| 变异 | 结果 |
|---|---|
| 已登记文件同表再加一条 UPDATE | 红：`条数 3≠登记的 2` |
| 全新文件出现父表 UPDATE | 红：`发现未登记的分区父表写操作（1 项）` |
| 白名单多一条不存在的登记 | 红：`白名单登记项已失效（1 项）` |
| 登记理由写成「同上」 | 红：`登记理由过短（2 字）` |
| 父表名改成不存在的 `supplier_errorz` | 红：`在仓内 DDL 中找不到 PARTITION BY 声明` |

**本门读磁盘，`-overlay` 对它无效**，故另配 3 条 `t.TempDir()` 合成夹具自测，把判别力固化成永久属性：正控（3 种违规形态必须被抓）+ **反向对照 8 条**（`_hot` 后缀、非分区表、INSERT、SELECT、注释里提及、_test.go 均不得报）+ 多行 SQL 字面量 + SQLite 路径排除。

### 4.2 一次「0 命中」的假阴性

扫描器第一版给 `idBoundary` 赋了 `(?:[A-Za-z0-9_])`，本意是零宽右边界，实际是**消耗式分组**——等于要求表名后面还得再有一个标识符字符，于是几乎永不匹配，首跑 `TOTAL=0`，差点把 18 处真实违规当成「仓库干净」。

这是子代理踩过的同一个错的更糟版本（它用 `\b`，误把 `request_logs_hot` 判成父表违规）。Go 的 RE2 不支持 lookahead `(?![...])`，想写零宽断言也写不了。正解是什么都不加：贪婪的 `[a-z0-9_]*` 吃到标识符自然结束，`request_logs_hot` 被整体捕获成 `request_logs_hot`、查不在父表集合而正确跳过。两个坑都写进了源码注释。

---

## 5. 对 `admin/retry_classifier_contract_test.go` 的自我纠错

这道门本轮自己也踩了两个坑，都写进了文件注释：

**坑一 · 仓库根定位错了。** 初版用 `filepath.Abs("../..")`。`admin/` 只有一层深，`../..` 越过了仓库根，扫到的是 `llm-gateway` 这一层的**全部兄弟项目**。后果不是变红，而是**假通过**：要证明「`no_candidate` 有产生方」，命中的却是 `ai-native-maintain/internal/httpapi/upgrade_policies.go`——一个与本仓无关的字符串。方向是「扫得更广」所以不会报错，只会安静地给出假证据。已改为按 `go.mod` 的模块路径向上定位。（同族的 `internal/partguard` 当初是对的——它在两层深处，纯属目录深度不同，不是写法更小心。）

**坑二 · 判据是恒真的。** 第二版用「该串在非测试代码里出现过」判有产生方，而 `admin/admin_llm_task.go`——分类器自己所在的文件——本身就含这三个待匹配的字面量。于是**把真实码改回幽灵码后门依然全绿**，「有产生方」的证据正是那个幽灵码所在的同一行。

**坑三 · 手写清单与实现脱节。** 第三版仍留着手写清单，于是「改实现不改清单」时门看不见——把分类器改成幽灵码，真实码在 `domains/streaming/*` 确实还有产生方，门照样绿。最终改为**从 `adminLLMShouldRetryExplicit` 函数体 AST 抽取** `strings.Contains` 的字面量，清单无法与实现脱节；取不到函数体或抽不到字面量时门直接报红（被测对象消失 = 门失效）。

两处变异复核：改回幽灵码 → 红；新增一个无产生方的串 → 红。

> 三个坑是同一句话的三次复现：**判据量的必须是语义量，不是代理量**。区别只在缺陷形态下才显形——而缺陷形态正是唯一需要这道门的形态。

---

## 6. 验证汇总

```
go build ./...                                   exit 0
go test ./bg/ ./storage/sqlite/ ./cmd/gateway/    全绿
go test ./admin/                                 ok 67.066s
go test ./internal/partguard/                    7 tests, 2.6s
make guards                                      9 包全绿
make guards-sync                                 ✅ 6 个守卫包已登记（GUARD_PACKAGES 内 9 项）
gofmt                                            clean（本轮触碰的文件）
```

真库（本机 PG 17.10 + citus 13.3，只读）用于：父表/列存覆盖面核实（32 父表）、父表清单与 DDL 交叉验证、R78-C 的 auto 链路统计。

---

## 7. 待裁决（本轮新增 2 条，累计 10 条）

1. **session_* 父表无 TTL**：`session_bodies`(8.4GB) / `session_turns`(6.4GB) / `session_turn_details` 历史分区无界增长。补 TTL 涉及保留期与合规口径。
2. **列存化 vs 18 处父表直写**：objective 要求大表列存化，但 18 处 Go 侧父表直写会全部失败。需先决定改造路径（每张表逐一改 hot 化，还是接受这些表不列存）。
3. **lite 模式 L3 缺口**：是否实现 SQLite 版 L3（进程重启后的会话恢复能力目前 lite ≠ full）。
4. `session_logs_view` 读端：接线还是下线。
5. `candidates_top3` 口径：改名还是改去重键（见 §2.2）。
6. `tuning_signals.quality_score` 恒 0.75 是否属实（**未验证**）。

沿用前 8 条（A: n>1 choice / B: 缓存+TTL 含义 / C: route_incidents 503 / D: handoff.enabled / E: /new 命名 / F: 1M 阈值 / G: provider_error_details 下线或接线 / H: handoff 冷却前移）。
