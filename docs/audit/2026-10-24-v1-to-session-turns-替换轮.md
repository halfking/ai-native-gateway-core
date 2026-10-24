# 2026-10-24 · v1 `_request_logs` → `session_turns` 全量替换轮

> **目标**：全面检查代码中与 `_request_logs` 相关的操作，全部替换为 `session_turns`。
> **对生产的写入**：**0**（本轮全部改动落在仓内代码 / 门 / 文档；未应用任何迁移，未部署）。
> **分支**：`main`（工作树存在并行会话的其它改动，本轮**未触碰**那些文件）。
> **工具链**：本机无 Go 运行时 ⇒ 本轮下载 `go1.27.2.darwin-arm64` 到 `/tmp` 局部使用（`go.mod` 要求 `go 1.27.2`）。

---

## 0. 口径声明（先说清楚「怎么数的」）

| 口径 | 取法 | 为什么这么取 |
|---|---|---|
| **「操作」的判定** | v1 关系名**出现在 SQL 的操作数位置**（`FROM/JOIN/INTO/UPDATE/ALTER TABLE/INSERT INTO/DELETE FROM/TRUNCATE/COPY/DROP/ON (...)/REFERENCES` 之后），**或** v1 函数名被**调用** | ★ 只看「文本里出现过 v1」会把 8000+ 处历史注释全算成操作 ⇒ 结论无意义。★ 关键：**读点**与**写点**是两个总体 —— 读点早被 `admin/session_family_sql_shape_test.go` 的 `TestRetiredV1ReferencesAreAllAccountedFor` 钉在 0；**写/DDL 点**此前**无人管** ⇒ 本轮补的就是这一维。 |
| **Go 代码的扫描方式** | `go/ast` 取**字符串字面量**（`*ast.BasicLit`） | ★ 逐行正则 + 「行首是 `//` 就算注释」会把**多行 raw string 里的 SQL**（行首没有 `//`）误判；而 `go/ast` 能区分「注释里的 SQL」与「会被执行的 SQL」⇒ **零误报面**。 |
| **迁移的「退役之后」判定** | **字典序**（不是「迁移号 > 890」） | ★ 启动迁移目录里混着 `NNN_…` 与 `2026-07-13-…`（日期名）两类命名；把日期名当迁移号会读成「2026 > 890 ⇒ 跑在退役之后」⇒ **误判**。★ 实测：各投递通道（`scripts/init-local-db.sh` 的 `for f in …/[0-9]*.sql`、`migrate-db-kaixuan1.sh`、installer 的 `StartupFiles` 显式列表）的真实顺序都与**字典序**一致 ⇒ 字典序就是投递序。 |

---

## 1. 全面检查的结论：四类，逐类处置

对全仓 `_request_logs` 做了完整分类（生产 Go 字符串字面量 179 处 / 48 文件；启动迁移 162 个文件带 v1 DDL）：

| 类 | 是什么 | 实测数量 | 处置 |
|---|---|---|---|
| **A. 活着的 v1 写/DDL 操作** | boot 路径上对 v1 族做 `ALTER` / `CREATE INDEX` 的自愈链 | **6 处**（下节逐处） | ★ **全部删除** ⇒ 落成 **0** |
| **B. 读点** | `FROM`/`JOIN` 一个 v1 关系 | **0**（仓库自证） | 保持 0（既有门 `TestRetiredV1ReferencesAreAllAccountedFor`） |
| **C. 历史证据** | 注释、迁移文件名、installer's `StartupFiles` 台账、`partguard` 的 DDL 形状清单 | 大量 | ★ **保留** —— 删掉等于删掉判据的依据（详见 §4） |
| **D. 契约绑定项** | `data_lifecycle` 的表名清单（UI 仍列该表名）、`sql_validator` 的表名键 | 2 处 | ★ **保留**并加注释说明为什么（详见 §4） |

---

## 2. 本轮删除的 6 处活操作（逐处 + 为什么这么改）

v1 族已随**迁移 890** 物理删除 ⇒ 任何仍 `ALTER` 它的代码在族不在的库上必然 **42P01** ⇒ `db.Open` 失败 ⇒ 网关「就绪但没有数据库」（`readyz` 在 `SetDepsOptional(true)` 下仍返 200 ⇒ 部署安全网放行）。它们此前都被一道「显式存在性闸」或「catalog 短路」挡着（死代码），但闸是**巧合式安全**：它依赖 `columnsAllPresent`「表不存在 ⇒ 返回 true」这条**注释与实现相反**的语义。

| # | 位置 | 原操作 | 处置 |
|---|---|---|---|
| 1 | `db/db.go` `ensureRequestLogSchema` | v1 族 31 列 + 13 索引的自愈链（322 行） | **整函数删除** ⇒ 承重转移见 §3 |
| 2 | `db/db.go` `ensureBillingUnitAndAudioSeconds` | 给 `_request_logs(_hot)` 补 `audio_seconds` | 删除 v1 腿；保留 `cmb.billing_unit`（非 v1）+ CHECK + `848` 台账 |
| 3 | `db/db.go` `ensureRoutingAnalyticsColumns` | 给 v1 族补 `task_type`/`origin_stage`/`origin_actor` | **整函数删除**（三列由会话族迁移链提供） |
| 4 | `db/db.go` `ensureWorkTypeSchema` | 给 `_request_logs` 补 `work_type` + 索引 | 删除 v1 腿；保留 `workTypeSchemaSQL`（两张配置小表） |
| 5 | `db/db.go` `ensureRoutingRecentSuccessRate` | 给 `_request_logs_hot` 补 `task_type`/`origin_stage` | 删除 v1 腿（函数体早已只读 `session_turns_hot`） |
| 6 | `db/maas_schema.go` `EnsureMaasSchema` | 给 `_request_logs` 补 `credits_charged` + 索引 | 删除 v1 腿；保留定价表批次 |

### 2.1 ★ 这 6 处不是「无害的死代码」——它们是**热表上的锁源**

`ALTER TABLE … ADD COLUMN IF NOT EXISTS` **不省锁**：PG 必须先对父表和**每一个分区**取 `ACCESS EXCLUSIVE` 才能判断列在不在 ⇒ 一个已存在几个月的列照样付一次全量独占扫描。生产实测（runbook §10.97）：420 次调用、均值 2,289 ms、**最长单次 104,322 ms**，对象是库里最忙的表。

⇒ 这 6 处是「每次 boot 对最热表取独占锁」的**最后一组来源**；删掉它们 = 少一条已知事故链。

### 2.2 ★ 为什么**不是**把这条链「改写成会话族版本」

那看起来对称，实测是最差的选择：把它搬到 `session_turns`（**更热、分区更多**）＝**把一次已知锁事故搬到一个更大的对象上**。

⇒ 正确形态是「**迁移链建列 + CI 门证明投影 ⊆ 迁移产物**」：运行时零 DDL、零锁；漂移在 CI 里就红。这条门就是 §3 的 `db/session_family_contract_test.go`。

---

## 3. ★ 承重论证：删掉自愈链之后，「那一列谁来保证存在」换了主人

**旧**：`boot ensure 链`（`ALTER … ADD COLUMN`）给 v1 族补列。
**新**：**启动迁移链 + canonical 视图的 PostgreSQL 依赖跟踪**。

两条硬事实：

1. **依赖跟踪**：视图引用了 `session_turns.origin_stage` ⇒ `DROP COLUMN session_turns.origin_stage` 会被拒（cannot drop column because other objects depend on it）⇒ **视图存在 ⇒ 它引用的列必然在**。
2. **CREATE 时校验**：canonical 视图的体由 `canonicalSessionOnlyDDL` 现场生成，引用 `t.<col>` / `d.<col>`；列不在 ⇒ `CREATE VIEW` 阶段就 42703 ⇒ boot **响亮失败**（而不是「偷偷补一列」）。

⇒ 失败模式从「静默自愈」变成「响亮报错」，且省掉热表上的锁等待。

### 3.1 新门 `db/session_family_contract_test.go`（承重判据）

**判据**：canonical 投影引用的每一个会话族列，**必须**由「基线 `sql/schema/01-schema.sql` ∪ `sql/migrations/startup/*.sql`」建出来。

**方向是「投影 ⊆ 迁移产物」**（不是反过来）：少一侧 ⇒ 有人给投影加了列却没写迁移 ⇒ 新装库上视图建不出来 ⇒ **红**。

**实测读数**：投影引用 **78** 个 `session_turns` 列 + **30** 个 `session_turn_details` 列 ⇒ **缺失 = 0**（全部由基线/迁移链建出）⇒ 这道门现在是绿的，且**它有牙**（阳性对照：能看见不存在的列 / 能看见迁移链补的 `request_status`）。

---

## 4. ★ 刻意**保留**的 v1 引用（每条都有理由，不是漏网）

| 保留项 | 理由 |
|---|---|
| 历史注释（数百处） | ★ **承重证据**：下一个读代码的人要知道「这一跳去哪了」。把注释清掉 = 把判据的依据从代码里删掉 ⇒ 半年后有人会把同样的链再加回来。⇒ 门因此一律**只扫可执行部分**（字符串字面量 / DDL），**不许**把注释算成操作。 |
| `installer/internal/dbinit/runner.go` + `embeddata` 的 `StartupFiles` 台账（74 处） | 那是**迁移链的登记表**：v1 迁移必须先跑（建族）再由 890 删族。删登记项 = 断链。 |
| `sql/migrations/startup/**` 中编号 ≤ 890 的 162 个文件 | 它们**跑在退役之前** ⇒ 写 v1 是正确的。放行依据是**投递序**（字典序），不是「内容像不像历史」。 |
| `sql/schema/01-schema.sql` | 基线建出 v1 族（历史迁移依赖它）⇒ 890 之后才删。★ **R64 实证**：基线时点 `session_turns_hot` **不存在** ⇒ 基线里**不能**写会话族形态 ⇒ v1 fallback 体是必需的。 |
| `internal/partguard/parents.go` 的 v1 名 | 反向门 `TestPartitionParentsAreExhaustive` 从仓内 DDL 文本抽 `PARTITION BY` 声明，而历史迁移文本仍声明这些父表 ⇒ 删掉清单项会让那道门红。⇒ 它们现在是「**历史 DDL 形状**的登记项」，不是「线上活着的表」。已加注释订正。 |
| `admin/data_lifecycle_partition.go` 的 `_request_logs` 清单项 | 端点契约：UI 仍列出该表名；函数已探表在不在，不在 ⇒ 返回 `Retired=true`。 |
| `admin/data_lifecycle_hot_partition.go` 的 `retiredV1HotTables` + promote 函数名 | ★ 这是**拒绝名单**：端点据此对 v1 热表返回 409「已退役，不许 promote」。它的职责就是**点名** v1 ⇒ 不点名就拒绝不了。 |
| `bg/partition_manager.go` 的 `archiveOldRequestLogs` | 归档体被 `v1LifecycleRetired()`（**恒 true**）无条件挡住 ⇒ 不可达；且**刻意保留**：它的形状（每日节奏门 + 显式事务 + 30min statement_timeout）是 F-11/F-12/R73 三条审计结论的依据，由 `bg/partition_manager_request_logs_archive_cadence_test.go` 钉住。 |

---

## 5. 新增 / 翻转的门

### 5.1 新门 `admin/v1_family_purge_gate_test.go`（全仓，零容忍）

| 门 | 判据 |
|---|---|
| `TestNoLiveV1WriteOperations` | 生产 `.go` 的**字符串字面量**里，v1 关系名出现在 **SQL 操作数位置** ⇒ **一个都不许有**；且**退役之后**的启动迁移（含 installer's `embeddata` 孪生副本）同样零容忍 |
| `TestNoLiveV1FunctionCalls` | 补上「关系名扫描看不见的那一维」：v1 **函数**调用 ⊆ 具名例外台账（基线 **2 条 → 收窄到 1 条**，**只许缩小**；收窄过程见 §8.1 末段） |
| `TestV1WriteGateScannerHasDiscriminatingPower` | ★ **阳性对照**：真 v1 写点必须命中；canonical 视图与会话族必须**不**命中；英文散文（"Derived from terminal _request_logs rows"）必须不命中 |
| `TestInstallerStartupFilesStillRetiresTheV1Family` | ★ 钉住「**退役这一步本身不许丢**」：890 必须在 installer's `StartupFiles` + `embeddata` 孪生副本里 ⇒ 否则新装库永远留着 v1 族 |

### 5.2 新门 `db/session_family_contract_test.go`（§3）+ 阳性对照

### 5.3 ★ 5 道既有门的**极性翻转**（从「DDL 必须被守卫」→「**DDL 不许存在**」）

`db/maas_request_logs_guard_test.go`、`db/work_type_request_logs_guard_test.go`、`db/db_848_v1_family_guard_test.go`、`db/r1039_request_logs_provider_id_column_test.go`、`db/startup_ddl_guards_earlier_realdb_test.go`

**为什么翻转**：原判据允许「守卫写对了 ⇒ DDL 照跑」（只是不白锁）；新判据是**零 DDL** ⇒ 严格更强。

**★ r1039 的教训被保留并转移**：原门钉的是「一个 DDL 引用了某列 ⇒ 那一层必须保证这一列存在」。这条教训是**普适**的 ⇒ 落到会话族后，它由 §3.1 的门承载（点名 `provider_id` ∈ details、`credits_charged` ∈ turns）。★ 两个宿主的**归属**不同（实测投影文本 `d.provider_id` / `t.credits_charged`），搞混会让这道门恒绿。

### 5.4 `sql/migrations/startup/migration_632_test.go` 的「wired_into_go_migration_engine」改写

承重不变式**没变**（「依赖的列必须先在位」），变的是**载体**：从「v1 补列 ensure 在物化视图之前」改成「**canonical 视图的 ensure 在 routing analytics 物化视图之前**」+「物化视图的源必须读 canonical 视图、不许读 v1」。

---

## 6. 标签与契约修正（运营/前端读得到的地方）

| 位置 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `admin/credential_monitor.go` + `credential_monitor_sliding_window.go` | `source = "_request_logs"` | `source = "session_turns"` | 回退腿早已读会话族，值却指向一张**不存在的表** |
| `admin/probe_history.go` | JSON 键 `"_request_logs"` | `"request_logs"` | ★ 前端 `web/src/api/provider-probe.ts` 的类型写的就是 `sources.request_logs` ⇒ **旧键名与前端对不上**（前端读到 `undefined`）⇒ 改键名把两端对齐 |
| `admin/session_list.go` | JSON 键 `"_request_logs"` | `"request_logs"` | 该字段的行全部来自会话族；前端未消费 ⇒ 改名无破坏面 |
| `domains/requestdetail/types.go` | `SourceRequestLogs = "_request_logs"` | `"request_logs"` | ★ 前端 `useRequestDetailLoader.ts` 用的就是 `'request_logs'` ⇒ 同样是对不上的旧值 |
| `bg/vacuum_worker.go` | 目标 `_request_logs_bodies_hot` | **`session_bodies_hot`** | ★ **本轮唯一的活行为变更**（见 §7 诚实边界） |
| `settings/spec_*.go`（6 个文件，13 处描述） | 描述一张**已删除**的表 | 描述会话族 / 标注该腿已不可达 | 运营在 admin 设置页读到的就是这些描述 |

---

## 7. ★ 诚实边界（必须随改动一起读）

1. **★ 本轮唯一的活行为变更 = `bg/vacuum_worker.go` 的目标表**。原目标 `_request_logs_bodies_hot` 已随 890 删除 ⇒ 该 worker 每周只打一条「目标表不存在，跳过」的日志；现改指会话族的 `session_bodies_hot`（普通表、heap ⇒ `VACUUM (ANALYZE)` 安全，不取 `ACCESS EXCLUSIVE`）。⇒ 它也是 §143.96 登记的「`session_bodies_hot` 同款 TOAST 病灶未修」的现成载体。**风险**：从「每周空转」变成「每周真干活」⇒ 有一周一次的 I/O 开销（普通 VACUUM，无锁）。**属主可否决**（改回常量即可）。
2. **对生产的写入 = 0**：本轮**没有应用任何迁移、没有部署**。生产上的 v1 族仍由既有 890 负责删除；本轮只是把**代码侧**的最后几处 v1 操作清掉。
3. **未在生产验证**：本轮全部读数来自仓内源码分析 + 一次性库/门实跑；**没有**连生产库做只读取证。
4. **真库门未跑**：带 `TEST_DATABASE_URL` 的真库判据（`TestR1039RecentSuccessRateEnsureRealDB`、`TestR1039BillingUnitEnsureSurvivesRetiredFamily`、`TestStartupDDLGuards_EarlierOnes_RealDB`）在本机**没有 DSN** ⇒ 走的是「无 DSN ⇒ 显式 SKIP」路径，**未实跑**。
5. **并行会话**：本轮工作树上存在**并行会话**的改动（`admin/dashboard_board_fallback.go`、`admin/data_lifecycle_attachments.go`、`admin/probe_request_info.go` 等 7 个 `admin/` 文件 + 2 份 audit 文档，形态为 `gofmt` 重排）。本轮**未触碰**那些文件，也未试图合并。
6. **`audio_seconds` 在会话族没有归宿** —— 不是「漏搬」，是**登记过的结构缺口**（`domains/streaming/audio_usage_log.go` 的 `LogAudioUsage` 是有文档的 no-op，由 `domains/streaming/audio_usage_gate_test.go` 钉住）。
7. **安装器台账缺口仍在**：211 的迁移水位落后（§143.20 记录），⇒ 211 上 v1 族**尚未**被 890 删除 ⇒ 本轮删掉的自愈链在 211 上**本来就不跑**（族级闸）⇒ 无行为差异。
8. **★ 顺带修掉一处**既有** gofmt 漂移（非本轮引入）**：`settings/spec_sessions_v2.go` 在 **HEAD 就是不 gofmt-clean 的**（`git show HEAD:settings/spec_sessions_v2.go | gofmt -l` 会报出来）⇒ 成因是 2026-10-20 的提交 `16ce69db89` 在 `write_timeout_ms` 那条 spec 里插入了多行长注释 ⇒ 它把 gofmt 的**对齐组切断了** ⇒ 该组里的 `Key:/Type:/Scope:/Category:` 与 `Default:/Description:/Unit:/…` 两段的列宽不再一致。⇒ 该文件本轮**由本轮独占修改**（并行会话未触及）⇒ 直接 `gofmt -w` 抹平，并做了**去空白后逐字一致**的校验 ⇒ **纯空白变更、零语义变更**。⚠ 仓内**没有** gofmt 门（`Makefile` 与 `.github/workflows/` 均无）⇒ 这类漂移会一直累积而无人拦 ⇒ **登记为待办**（本轮未新增门，见 §9 通用式第 8 条）。
9. **★ 全仓 `go test ./... -timeout=1800s` 的读数：336 个包绿、4 个包红，**4 处全部与本轮无关**，逐条判据如下：
   | 包 / 用例 | 读数 | 归属判据 |
   |---|---|---|
   | `cmd/tools/sql_source_indirection_audit` / `TestDocumentedSnapshotMatchesMeasurement` | `拼接点 142→145`、`canonical 122→125` | ★ **干净 HEAD worktree 以完全相同的读数失败** ⇒ **tip 提交自带**；且 `git diff HEAD -- cmd/tools/` **为空** |
   | `domains/nodestatecache` / `TestSelectP99Under1ms` | `p99=4.74ms`（门槛 1ms） | ★ **干净 HEAD worktree 同样红**（HEAD `p99=4.36ms`）⇒ **这台机器的绝对性能**，非回归；`git diff HEAD -- domains/nodestatecache/` **为空** |
   | `scripts/ursmcheck` / `TestSectionNumbersAreUniqueBeyondKnownBaseline` + `TestSectionNumberDuplicatePairCountIsPinned` | `§302：出现 2 次`；`重复节号对数 = 38，期望 37` | ★ **干净 HEAD worktree 以完全相同的读数失败** ⇒ **`LESSONS.md` 里 `§302` 在 HEAD 就重复了 2 次**；★ 本轮追加的 `§358` / `§359` **各只出现 1 次**（已实测）⇒ **不是本轮引入**。⚠ 该门自带处置指引：「若是【存量被修掉】⇒ 请更新 exemptedSectionCounts 的登记次数」—— ★ **本轮不代改**（那是别的轮的账） |
   | `tests/48h-audit/D01-ir-lifecycle/stress` / `TestStress_IRSerialize_Concurrency50_P99` | `p99=23.04ms`（门槛 10ms） | ★ 单独跑 **3/3 全绿**（0.54s / 0.35s / 0.33s）⇒ **负载型 flaky**（该轮同时有另一会话的 `docker run` 抢 CPU）⇒ 与 §143.7.9 登记的家族同族 ⇒ **不加门**（加了要么恒绿、要么变成新的 flaky 源） |
   ⇒ ★ **本轮触及的 8 个包在全仓跑里全绿**：`admin`(316s) / `bg`(47s) / `cmd/gateway`(15s) / `db`(11s) / `domains/requestdetail`(2s) / `internal/partguard`(19s) / `settings`(3s) / `sql/migrations/startup`(25s)。
   ⇒ ★★ **归因纪律**（本轮最值得记的一条）：★ **「读数不对」与「我引入的」是两个命题**；判归属的**唯一强证据**是「**在干净 HEAD 上复现出同一个读数**」，且更便宜的判据是「**该包相对 HEAD 的 diff 为空**」。★ 归因之后还有第二问：**既有失败该不该顺手修？⇒ 不该** —— 它属于别的轮的账，替他改数字/改阈值会把别的轮的口径改掉 ⇒ 本轮 4 处全部「归因 + 登记，不代修」。

---

## 8. 本轮验证读数

| 项 | 读数 |
|---|---|
| `go build ./...` | ✅ 绿 |
| `go vet ./...` | ✅ 绿（exit 0） |
| `go test ./db/ ./settings/ ./domains/requestdetail/ ./internal/partguard/` | ✅ 全绿 |
| `go test ./bg/ ./cmd/gateway/ ./sql/migrations/startup/` | ✅ 全绿 |
| `go test ./admin/` | ✅ 全绿（316.3s） |
| ★ **`go test ./... -count=1 -timeout=1800s`（全仓）** | ★ **336 个包绿 / 4 个包红**，★ 4 处**全部既有或负载型**（逐条判据见 §7 第 9 条，★ 3 处用**干净 HEAD worktree** 复现出同一读数） |
| ★ 本轮触及的 8 个包在全仓跑里的读数 | ✅ **全绿**（`admin` 316s / `bg` 47s / `cmd/gateway` 15s / `db` 11s / `domains/requestdetail` 2s / `internal/partguard` 19s / `settings` 3s / `sql/migrations/startup` 25s） |
| 生产 Go 里的**活 v1 操作** | **0**（AST 口径，实测） |
| 退役之后的启动迁移里的 v1 DDL | **0**（字典序口径，实测 26 个后继文件全清） |
| 投影 ⊆ 迁移产物 | ✅ 缺失 **0**（78 + 30 列） |
| 新门 | ✅ 4 门全绿（含阳性对照） |
| 极性翻转的 5 道既有门 | ✅ 全绿 |
| `gofmt -l`（本轮触及的文件） | ✅ 空（★ 收尾时撞出 `settings/spec_sessions_v2.go` 在 **HEAD 就**不干净 ⇒ 已 `gofmt -w` 抹平并做「去空白后逐字一致」校验 ⇒ **纯空白、零语义**；详见 §7 第 8 条） |

### 8.1 ★ 变异探针（3 臂全红，且**按正确的原因**红）

| 臂 | 注入 | 期望 | 实测 |
|---|---|---|---|
| **M1** | 往生产 `.go` 的字符串字面量里塞一条 `ALTER TABLE _request_logs ADD COLUMN …` | `TestNoLiveV1WriteOperations` 红 | ✅ **红**（报出文件 + 行 + 「890 已把这张表删了 ⇒ 42P01 ⇒ boot 中止」） |
| **M2** | 塞一次 `archive_request_logs_default(30)` 调用 | `TestNoLiveV1FunctionCalls` 红 | ✅ **红**（报出「这些函数已从库里删了 ⇒ 42P01」） |
| **M3** | 给 `detailsProjectionColumns` 加一列**没有任何迁移建过**的列 | `TestSessionFamilyProjectionColumnsAreMigrationCreated` 红 | ✅ **红**（报出 `mutation_probe_col` + 「新装库上 canonical 视图会 42703 ⇒ boot 中止」） |

★ **还原逐字校验**：3 个文件全部 `md5` 一致。

★ ★★ **第一轮探针是「假红」，已作废并重做**（★ 这本身是本轮最值得记的一条）：
★ 第一版 M1/M2 的注入写成了 `d.pool.Exec(...)`，而 `applyMigrationsOnce` 的接收者是 **`db`** ⇒ **编译不过** ⇒ 门报的是 **build failed**，**不是**「检测到 v1 操作」⇒ **假红**；M3 的锚点字符串与 `gofmt` 后的实际文本不符 ⇒ **注入根本没生效** ⇒ 门保持绿 ⇒ 「M3 没红」。
★ ⇒ ★ **「门红了」不等于「门起作用了」**：★ **变异臂本身必须先编译通过、且注入必须真的落地**，否则读到的是**量具坏了**，不是**被测坏了**（§330 同族）。★ 修法：注入前先断言锚点**恰好命中一次**，并在注入后**先跑一次编译**。

★ ★★ **M2 的运行日志还自己暴露了一处「假凭据」**：例外台账里原本挂着 `admin/data_lifecycle_hot_partition.go`（promote 函数名映射）—— 但那两处是**映射表的取值**（`"promote_request_logs_hot_to_partition"`），**不是调用**（没有紧跟的左括号）⇒ 在「调用」口径下**根本不会命中** ⇒ 台账里挂一条**永不命中**的例外 = 一条假凭据（会让人以为「这里仍有一处 v1 引用被登记着」）⇒ 按「**只许缩小**」纪律**删掉**，基线 2 → **1**。
★ ⇒ 顺带厘清：那两个名字**仍在**生产代码里，但它们是**拒绝名单的取值**（端点据此对 v1 热表返回 409「已退役，不许 promote」）⇒ **不是调用**；它们的正确守门人是既有的 `admin/data_lifecycle_v1_retirement_test.go`（钉住「退役族必须仍在 `hotPromoteTableMap` / `retiredV1HotTables` 里」）。

---

## 9. 通用式（本轮沉淀）

1. ★ **「读点已清零」不等于「写点已清零」** —— 两个总体，两个门。本轮之前，读侧有门（零容忍），写/DDL 侧**无人管** ⇒ 6 处热表锁源在仓里躺了很久。⇒ **每次退役一个族，要分别问「谁读它」「谁写它」「谁 DDL 它」。**
2. ★ **删除一条自愈链，必须先回答「它护的那个不变量现在由谁保证」** —— 否则就是悄悄拆掉安全网。本轮的答案是「迁移链 + 视图的依赖跟踪」，并由一道 CI 门**证明**它成立（不是相信它成立）。
3. ★ **把自愈链「改写成新对象版本」通常是最差选择** —— 它把「已知锁事故」搬到一个**更大的对象**上。正确形态是「迁移建 + CI 证」。
4. ★ **门必须用 AST 取字符串字面量，不能用逐行正则** —— 逐行口径要么漏（多行 raw string 里的 SQL），要么把注释当代码（靠「行首是 `//`」猜）。
5. ★ **「退役之后」要按**投递序**判定，不能按文件名里的数字** —— 目录里混着日期名与迁移号两种命名。
6. ★ **历史注释不许被「清理」** —— 它们是判据的依据。门因此只扫**可执行部分**。
7. ★ **极性可以翻转，但**不变式**必须保留** —— 「DDL 必须被守卫」→「DDL 不许存在」是加强；「补列 ensure 在物化视图之前」→「canonical 视图 ensure 在物化视图之前」是换载体而不换不变式。
8. ★ **「没有门的性质会静默漂移」** —— 本轮在**自己触及的文件**上跑 `gofmt -l`，撞出一处**早在 HEAD 就存在**的漂移（成因是 2026-10-20 的一次注释插入切断了 gofmt 的对齐组）。⇒ 通用式：**凡「本轮要交付的文件」都该过一遍 `gofmt -l`**，且**要区分「我引入的」与「既有的」**（后者用 `git show HEAD:<file> | gofmt -l` 判定）—— 两者处理方式不同：既有的**要么顺手抹平**（若该文件归本轮独占），要么**登记为待办**（若不属于本轮）。★ 本仓**至今没有 gofmt 门** ⇒ 这类漂移只能靠人记得跑 ⇒ **建议单独补一道「本仓新增/修改文件必须 gofmt-clean」的门**（本轮未加，见 §7 第 8 条）。
