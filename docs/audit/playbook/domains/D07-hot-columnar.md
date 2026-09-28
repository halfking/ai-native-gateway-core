# D07 — hot + columnar 分区存储不变量

> 领域编号: D07 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：所有大数据表的 hot 表 + 分区（columnar）母表双层数构；"更新/删除只在 hot 表"不变量；hot 只保留 8 小时数据；批量 promote 任务（hot → 分区表）；promote 的时区钉扎与幂等。
**不管**：full/lite 模式开关（D06）；supplier_errors 特定 TTL（D08 提及、本域核对机制）。

## 2. 参考基线

设计文档（**注意口径**：HOT_TABLE_OPTIMIZATION 原文写 7 天 promote，`storage-optimization-plan.md` v2（2026-09-14）已改为 session 六表族唯一事实源 + promote 幂等重写，**审计以 v2 为准**，hot 保留窗口以运行时配置为准核对）：
- `docs/03-design/04-data-design/storage-optimization-plan.md` — v2 存储优化（706/707/708 promote 重写）
- `docs/03-design/04-data-design/partition/HOT_TABLE_OPTIMIZATION.md` — 热表独立 + promote 函数
- `docs/03-design/04-data-design/partition/partition-architecture.md`、`partition-standards.md` — 读写规范
- `docs/03-design/04-data-design/partition/OPERATIONS_RUNBOOK.md`、`MONTHLY_CHECKLIST.md`
- `docs/03-design/04-data-design/storage-observation-ledger.md` — 每日观察账本（GLOBAL_G2 监控）
- `docs/03-design/02-feature-design/design/COLUMNAR_BODY_SPLIT_PLAN.md`

代码入口：
- `sql/migrations/startup/`（迁移编号当前已至 714+，703=supplier_errors promote、714=时区钉扎收尾）
- `scripts/apply-db-revision-sequence_test.sh` — 迁移登记通道契约
- `internal/dbx/`、promote 调度（`bg/`）

## 3. 检查清单

1. **写路径唯一性**：每张大数据表的 INSERT/UPDATE/DELETE 只落在 `_hot` 表；grep 窗口内新增 SQL，直写分区母表的写入点 = P1。
2. **读路径联合**：读侧为 hot ∪ 母表联合（NOT EXISTS 兜 promote 残留双行）；新增读端点不遗漏母表历史数据。
3. **hot 时限**：hot 表保留窗口（当前目标 8 小时）由配置/任务执行兑现；promote 任务批量转移成功后清理 hot，hot 无界增长 = P2。
4. **promote 幂等**：promote 重放不产生重复行（ON CONFLICT/唯一键兜底）；promote 残留行的 UPDATE 必然失败的问题由"写只在 hot"不变量防护。
5. **时区钉扎**：所有 promote/ensure 类函数 `SET LOCAL` 钉扎时区（473 族缺陷，714 收尾后仍需对新增函数核对）；月份分组函数不依赖会话时区。
6. **历史分区 TTL**：各表历史分区有 drop 清单（supplier_errors 90d 基准），新增大表若无 TTL = P2。
7. **columnar 适配**：columnar 母表不执行 UPDATE/DELETE（引擎限制即不变量）；新表选择 columnar 时核对适用性。
8. **迁移纪律**：新增迁移编号 fetch 远端核对无冲突，登记 revision-sequence。

## 4. 历史回归点（轮末回注区）

- [R30] session_tools 直写分区母表 → hot/promote 空转死设备、promote 后 UPDATE 必败（不变量破坏）— 修复 2b026cf18；写链切 hot + hot∪母表联合读
- [R30] 473 类时区缺陷漏网 6 函数（4 ensure + 579/580 promote 分组）— 修复 714（6da2c4f72）；新增函数必须钉扎
- [R30] supplier_errors 历史分区无 drop 清单（无限增长）— 修复 f19ba5d5a；TTL 90d
- [09-16] 每日观察 GLOBAL_G2=0 连续归零（Day 1/7）——观察账本基准，轮审计应读取当日账本

- [R36] 01-schema 列型漂移（hot 10 列 vs 母表，6 硬不兼容）× 680 动态交集 UNION = 全新安装 42804 启动链中止，602 promote customer_id 同炸；迁移 717 对齐 + 守卫升级为列型比对（TestBaselineRequestLogsHotColumnTypesMatchMother 129 共享列 ×3 baseline + 10 列目标型钉桩）——改 baseline 必须三份同步并过该守卫；手工对齐体待真库 dump-schema 重导替换
- [R37] 717 USING 表达式在"已对齐 baseline"形态下 42883（R36 修复引入的全新安装回归，e0f94a799 预警）：ALTER USING 看到的是当前列型，`col ~ 正则`/`IN ('true',…)`/`= ''` 在 bigint/boolean/jsonb 列上全是算子错误；jsonb 预筛正则字符类 [0-9tfn-] 放行 not-json 词首再炸 22P02。修复=USING 列引用全 ::text 化 + 字面量交替；**迁移类修复必须双态（漂移存量/全新安装）真库验证**（TestMigration717HotColumnAlignmentBothColumnStates 钉桩），静态注册门禁不覆盖 USING 算子合法性

- **R44 | 人工/分析 SQL 读面必须 hot∪母表**：只查 request_logs 母表 = 8h 盲区（本机实测 24h 窗母表少 13.6% 行，possibly_stalled 型判据对每行恒真）；读面用 request_logs_with_current_month（448/510/710 维护重建）或显式 UNION ALL。正面模板 = scripts/analysis/*.sql（R44 重写版）。
- **R44 | .sql 模板 commit 前必须真库实跑**（迁移纪律#1 扩展到分析模板）：providers.name 42703 型硬失败与母表盲区型软失败都在真库首跑当场暴露；Go 侧有三门兜底，SQL 模板的等价门就是 psql -f 实跑。

## 5. 子代理派发提示词

```text
你是 D07（hot+columnar 分区不变量）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D07-hot-columnar.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：窗口内新增/修改的 SQL 与迁移是否破坏"写只在 hot、读 hot∪母表、promote 幂等、时区钉扎"四不变量。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```

### R42 回注（2026-09-18，分区不变量抽检 + PG 版本前置）
- 723/722/721 与分区拓扑核为健康（durable 族非分区、supplier_errors_hot policy 分写无跨层遗漏、8h 轮转 worker 零触碰）；723 对 cfl_columnar_old 的 ALTER 已加 to_regclass 守卫（D16 R42 回注）。
- **PG<15 分区父表 RLS policy 不下推**：Phase 3 FORCE 前必须对 installer 栈（citus:11.3 可能 PG14、quickstart postgres:14-alpine）`SELECT version()` 钉桩，或出逐分区 policy 方案（R42 §五#1）。

### R43 回注（2026-09-18，726 收口 + 真跑证据补记）
- 726 五点同步已收口（本轮 P0）：runner.go/main.go/sequence/parity map 四点补齐，双契约测试转绿；本地真库核实 `idx_credential_model_index_hot_unique (bucket,credential_id,raw_model)` 已带外应用且形态与 726 一致——迁移纪律#1 真跑证据补记（154 头注原只有只读核实）。
- **promote 保留期两套机制澄清**：request_logs 族 hot→母表 promote 由 341 `p_retention DEFAULT '7 days'` 管辖；partition_manager 的 `DefaultRetentionWindow = 8h` 是 *_default 分区兜底清理常量——涉及"hot 保留多久"的判断先分清是哪族表。
- api_key_auto_profile 类"ensure 修复链普通表"不是分区族成员，"写只在 hot"约束不适用（census 判断时勿误报）。

### R45 回注（2026-09-19，视图 NULL 投影读面陷阱）
- **读 `request_logs_with_current_month` 的分析 SQL 必须用 `COALESCE(request_type,'main')` 口径**：视图的 session_turns_hot/session_turns 段把 request_type 投影为 `NULL::text`（session_turns 表无该列），裸 `='main'` 会漏掉全部业务行只余探测流量（R44 重写踩坑、R45 真库实测 24h 漏 99.3%/7d 漏 65%）。admin 读端 COALESCE 惯例是既定 SSOT。任何新分析 SQL commit 前真库实跑须验证**读面行数量级**而非只验"能跑通"。
- **自跑通过 ≠ 语义正确**：R44 自验只发现不了该缺陷（无硬失败），独立子代理真库实跑才暴露——迁移三纪律#3 从迁移扩展到一切"我方自验"结论。

### R52 回注（2026-09-22，735 × admin 写路径 + lite 门控装配批）
- **新唯一索引上线时必须穷举"新失败面"的写路径**：735 active 折叠唯一索引只给 createModel 加了 23505→409，admin updateModel re-enable 分支 //nolint:errcheck 裸吞 → 空壳成功（R52-F4）。迁移引入新约束的同一轮，grep 该表全部 UPDATE/INSERT 调用点逐一裁决错误处理。
- **门控开关在 lite 形态的装配缺口**：specs 只在 dbConn.Enabled() 分支注册，lite 进程 Global.Spec(key)==nil → GetPlatformBool 恒回落默认——"双模式同门控"必须在 lite 装配路径显式注册 specs（settings.Init(nil) 仅接 env backend，R52-F8）；测试环境手工注册 registry 会掩盖装配缺口，门控行为测试要按真实装配形态写。
- **F19 每表保底后预算语义**：批数硬顶变软顶（最坏 +specs-1 批），墙钟仍被 promoteCycleTimeout 兜住；时间型饥饿（前序大表耗尽墙钟）为已知残余，观察 llm_gateway_hot_table_backlog_rows / oldest_row_age_seconds 两 gauge。

### R47 回注（2026-09-20，读面纪律机制化 + 守卫测试）
- **裸读守卫已上**：`internal/sqlreadguard/guard_test.go`（TestNoBareRequestLogsMotherReads + TestSQLReadGuardWhitelistCurrent）。扫描生产 Go 面（admin/autoroute/bg/cmd/db/discovery/domains/internal/provider/proxy/taskprofile，排除 *_test.go）+ sql/objects 与 scripts/analysis 的 .sql；正则 `\b(FROM|JOIN)\s+(public\.)?request_logs\b`（\b 天然排除 _hot/_with_/_without_ 视图引用）；Go `//` 与 SQL `--`（含原始字符串内注释行）注释提及自动豁免。新增裸读 = 红；豁免三通道：白名单文件级（LEGIT/DEBT(R47)/TOOLING 前缀）、行内 `sqlreadguard:allow`、双腿形态本身不再命中。
- **白名单自清洁**：文件已无命中（删除或已双腿化）而白名单条目还在 → 红，强制移除。防"守卫通过即债务隐身"。清偿路径：逐文件双腿化 → 删条目 → 守卫确认。
- **admin 读面债基线**（R47 初始 DEBT 白名单 25 Go + 6 SQL）：既有残留裸读并非 D07 一次性盘点所得，而是守卫机械扫描重新生成——人工清单覆盖半径不足（本轮实差：子代理报 13 文件，机械扫出 53 文件）。
- **assertTaskInTenant 已双腿化**（hot∪母表双 EXISTS）：tenant 边界判定这类**安全闸**读面同样适用 8h 盲窗——闸判定错误直接变成跨租户 404 误伤。

### R53 回注（2026-09-22，727/730 契约测试 + installer 结构性缺席定性）
- `migration_727_test.go`：F-A 索引绑 retention.go 谓词、F-B 函数索引表达式与 710 视图投影等价绑定（归一化剥别名/`::TEXT`）+ 三段式结构（分区 gexec/hot 独立腿/ONLY+ATTACH）+ 禁 BEGIN/COMMIT（CONCURRENTLY 不能进事务；DO 块内无分号 BEGIN 不误报）+ down 恰 3 索引。
- `migration_730_test.go`：CHECK 枚举**编译期 import autoroute** 绑定 AllAgentRoles/AllTaskKinds；UNIQUE NULLS NOT DISTINCT 钉扎（252 重放翻倍 48→96）；部分索引 WHERE 门；embeddata byte-identical（730 五点同步已随 R48 完成）。
- 负例纪律复验：第一轮 727 单点变异被 fallback 正则掩盖漏报，全局变异复验双断言 FAIL——**钉桩自身的 fallback 分支也是被钉语义的一部分**。
- R53-D1（登记）：727/728/729 全 CONCURRENTLY，installer runner 一律 --single-transaction 结构性装不下 → embeddata 缺席属结构性排除；fresh install 缺三索引（gateway ensure 链亦无兜底）仅性能回归。后续 installer 非事务通道或 ensure 兜底。
- R69 回注（2026-09-27）：**迁移文件自带的预建/初始化调用必须与函数级钉扎同文件生效**——751 的 ALTER 钉扎救不了 750 文件自己在 751 之前执行的预建 SELECT（current_date 与 DECLARE 初始器双双随会话时区，UTC 会话预建错位 8h 分区后按名幂等短路永不自愈，相邻日 ATTACH overlap 永锁）；修法=函数定义直接带 `SET timezone` 子句（proconfig 先于初始器、单文件自洽）+ 日期参数显式上海日历派生。钉桩=migration_750_test.go C9（SET 子句存在/上海派生×2/禁 current_date）。

### R71 回注（2026-09-27，installer 五点断裂 + 守卫失效双案）
- **installer 五点同步守卫必须进每轮验证命令清单**：750/751/752 在 StartupFiles 登记但 embeddata/var/map 三点全缺（全新安装 dbinit 硬中止），TestStartupFilesAreAllEmbedded 恒红两轮未被发现——近期各轮只跑 sql/migrations+db 包。F1 修复（三文件+753 同款补齐）；749 补登 psqlConcurrencyRequired 豁免（CONCURRENTLY 家族 727/728/729/744/749）。
- R71-D1（登记 L1）：mock_probe_history（752 新分区族）无 TTL/drop 清单，历史日分区无界累积——与 supplier_errors/usage_facts DEFAULT TTL 同病类，owner 拍板保留期。
- R71-D2（登记 L8）：752 缺 migration_752_test.go 契约钉桩（对偶 C9：SET timezone/上海派生/禁 current_date/move-then-attach 骨架）。

### R72 回注（2026-09-27，753 首扫无界 DELETE + 初稿语义漂移五处 + 752 契约钉桩补齐）
- **"首次接上清扫"的迁移必须自带分批**：753 的 DELETE 无 LIMIT，而部署 boot 段同步触发首扫（该表生产从未清理过）——大积压=长事务持行锁+WAL 尖峰，5min 语句超时整批回滚且下次重试 24h 后，永不收敛。修法：函数改 `LIMIT p_batch_size`（主键选批，默认 10000）+ Go 调用方 drain 循环（每批独立语句，超时最多损失当前批）。753 在任何真库应用前原位修订（252 台账核实无 752/753）；已应用迁移禁止原位改，走 sequence 指纹重放通道。
- **批判式审计改写终稿后，初稿语义的注释/文案必须逐处回改**：753 的 spec DescriptionLong、partition_manager 注释、sequence 登记、dbinit runner 登记、migration_753_test 头注 C1-C6 五处仍是初稿事实（2×TTL 谓词/GREATEST/NULL→24/pg_proc 短路/"补 idx"/"runCleanup 第 11 项"）——"钉桩自身的叙述也是被钉语义"（R53）的反面教训，本轮全部按终稿改写。
- L8 收口：migration_752_test.go 契约钉桩补建（SET timezone proconfig/上海派生/禁 current_date/move-then-attach 骨架/pg_inherits 双检+advisory lock/mockprobe 双调用点）。
- 753 真库预勘（252，R72-L3）：752/753 均未应用、session_turn_logs 0 行（首扫积压为零）、usage_facts 分区边界 +08 全对齐（036 手工期错位残留为零）。

### R73 回注（2026-09-28，754 归档接线 + 归档月表治理）
- **纯函数测试钉不住接线**：hour 门在函数体内被调用的断言全绿 ≠ 调用点在正确的 ticker 循环——E1a 事故形态（移回 24h 定相 run()）可静默复发。TestArchiveOldRequestLogs_CalledFromHourlyCleanupLoop 钉死「调用点必须在 runCleanup、不得在 run/archiveOldPartitionsIfNeeded」；新 cleanup 任务接线时套用同款双向源码范围断言。
- 归档月表（request_logs_archive_YYYY_MM）两语义依赖钉在 754 头注：①源分区必须直查分区名（走父表会被 FORCE RLS 按 bg 会话 default 租户静默漏读）②月表加 RLS 必须同步设计读方角色。加 RLS 前先真库 EXPLAIN 实证直查分区与父表的 policy 适用差异。
- 752 mock_probe_history 日分区只建不删（保留期无界，≈4.4M 行/年）——752 头注已显式登记；与 750 usage_facts TTL 同批 owner 拍板。

### R75 回注（2026-09-28，754 调用方 statement_timeout + 注释失真收口）
- **集合返回函数 = 单条驱动层语句，函数内批游标不稀释 statement_timeout**：754 的 1000 行批 LOOP 看似"分批"，但整个 `SELECT * FROM archive_request_logs_default($1)` 对服务端是一条语句——角色级 30s（252 rolconfig）会在大积压首跑击杀整调用并整批回滚，次日重试同批=活锁（与 R72 753 首扫同型）。**SET LOCAL 必须在调用方事务内**（Go ctx 预算不覆盖服务端 GUC）；同文件 promote 60s / analyze 10min 是先例锚点。钉桩 TestArchiveOldRequestLogs_StatementTimeoutPinnedInsideTx 用 Begin<SET LOCAL<call<Commit 顺序断言。教训一般化：**凡新接 pg 函数族调用，先问"函数内分批是否被服务端视为单语句"**——PL/pgSQL 循环不切分 statement_timeout。
- 754 头注「Go 侧 30min ctx 预算兜底」为书面错误断言（十六轮订正引入），已改写指向调用方 SET LOCAL；批终止注释「两条件同时成立才退出」与代码（任一即退）相反已订正。**注释勘误走 embeddata 字节同步 + 三守卫复跑**（754 已应用不重跑台账）。
- 754 归档首跑实捕待部署（L-1）：修复部署后首个 03:00-03:59 窗口验证 request_logs_archive 首次生成 + archived 日志。

### R79 回注（2026-09-29，S-01 真库 EXPLAIN/大分区实测 —— 推翻三轮「已接线可用」结论）

S-01 原标注「环境未提供」，实测时本机 PG 17.10 已健康运行 34 小时，标注过期。
改为实做后在 `request_logs` 单族上找到**两个各自足以让归档完全不可用的缺陷**：

- **P1-a 42703 首跑即死**：754 的 `candidates` CTE 投影 `session_id` / `status_code`，
  而真表是 `gw_session_id` / `upstream_status_code`（基线 137 列 + 真库 4 个分区双源核对）。
  函数体是 `format()+EXECUTE` 动态 SQL ⇒ CREATE 不校验 ⇒ 安装、台账、升级通道全绿；
  D-01「形状核对」与 SF-01「静态守卫」都是对文本断言 ⇒ 六轮审计全绿。
  **该函数自落地起从未成功执行过一次**，`request_logs_archive_*` 从未有过一行。
  已修（只改源投影 2 个列名；归档表自身列名不动），canonical + delivery 双副本字节同步。

- **P1-b 游标列无索引 → N²**：754 的批游标 `WHERE id > :last ORDER BY id LIMIT 1000`
  被头注称作「主键游标」，但 `request_logs` **无主键、也无任何以 id 为首列的索引**
  （48 个索引逐个核过；唯一索引只有 `(request_id, ts)`）。每批次退化为全分区并行
  顺序扫描：EXPLAIN 实测取 1000 行读 **531,262** 缓冲块（≈4.2 GB），
  2126 批 ≈ 8.9 TB 缓冲读，成本 O(rows²/1000)。
  **端到端实测：30:00.028 被 statement_timeout 击杀、整笔 ROLLBACK、0 行归档。**
  已修：新增迁移 **756** 在分区父表上建 `request_logs(id)`，下发到全部分区。
  修复后同分区冷归档 **25.963s / 2,125,857 行**，热重跑 **8.179s / 0 新增**，
  单批计划 Index Scan 230 buffers / 1.294ms（缓冲块降 2312×）。

- **两处必须一起上线的耦合**：只修 42703 不修游标，链路会从「毫秒级失败」退化成
  「每晚 30 CPU 分钟、被击杀、整笔回滚、次日重来」的**永久活锁**——R72 对 753 首扫的
  诊断在生产规模上的复刻，而 R73 把预算从 30s 抬到 30min 只是把墙推远了。

- **新增 7 道门（全部过变异检验）**：data 侧 3 道（基线 DDL × 754 投影跨源列名交叉校验、
  投影错位、双副本字节一致）；stress 侧 4 道（scratch 库真跑 754、**计划形状**、
  幂等、留存联锁）。计划形状门**自带对照**：建 756 前必须 Seq Scan、建 756 后必须
  Index Scan——没有对照的「断言计划里有 Index」正是本会话反复吃亏的空洞门形态。

- **顺带订正 754 头注的一处自欺**：原文「本 SQL 从未在真实 PostgreSQL 上执行过，
  不宜在此盲改」——实测后证明不是「不宜盲改」，是**根本没跑过**，而这个判断本身就
  出自没跑过的人。凡以「没跑过所以先不改」为理由保留的缺陷，都在欠一次实跑。

- **登记未修（owner 裁决）**：① 归档月表 `request_logs_archive_YYYY_MM` 是独立 heap 表，
  未 ATTACH 到父表 `request_logs_archive`；该父表 `PARTITION BY RANGE (ts)` 但**分区数 0**
  ——唯一会 ATTACH 的旧函数 `archive_request_logs(date)` 已被 331 移除，而 **331 本身未进
  installer startup 通道**（embeddata/startup 下无 331），基线又把父表建了回来。
  后果：未来读方写 `SELECT ... FROM request_logs_archive` 静默得 0 行而非报错。
  ② 归档无 ledger，每晚重扫全窗（热重跑实测 8.18s / 2.1M 行，单调增长不自收敛）。

- **未纳入本轮**：245/252 真机未连；其余分区族（candidate_failure_logs / usage_facts /
  mock_probe_history 等）的同类「游标列是否有索引」问题未逐族检查——方法可直接套用。

### R79 续回注（2026-09-29，promote_* 全族普查 + 一条被证伪的归因）

- **Gate A｜活函数批游标首列索引**：真库 29 个 `promote_*` / 27 个批游标 / 18 个注册在
  `promoteSpecs()`。**16 个活函数索引齐全，2 个缺**：`candidate_failure_logs_hot(ts)`
  无任何 ts 首列索引；`auto_route_selections_hot(ts)` 仅有 promote 谓词推不出的部分索引。
  **定级 P2 潜在而非 P1**——实测摄入速率 24 行/h 与 359 行/h，8h 窗口只有 ~190 / ~2,900 行，
  **都不到一个 5000 行批次**，二次方代价今天没被支付。写 P1 属虚报。
- **Gate B｜死函数引用不存在的表**：3 个 `promote_*_default_batch` 零 Go 调用方，
  父表无 DEFAULT 分区（nothing to drain）。P3 文档债：上了膛的枪，接线即 42P01。
  **登记不删**——删 schema 对象是迁移决策，本轮不替 owner 做。
- **一条被证伪的假设（值得单独记）**：初见 `candidate_failure_logs_hot` 有 8h47m 的行超出
  8h 保留窗仍在 hot，判「8h hot 不变式已破」。追 promote 日志后确认：上一轮跑于 23:04:50，
  截止线 15:04:50，当时最老行 15:05:32 **尚未到期**，且该表 23:04:50 正常排了 36 行；
  按小时周期运行的滞留上界本就是 8h+1h。**不变式没破，是我在验证前就开始归因。**
  一般化：**看到一个超期样本就宣告不变式破裂之前，先确认该样本在上一次执行时是否已到期。**
- **写这类门时它自己红过三次，每次都是门有缺陷**：① 正则用字面空格而 `prosrc` 保留换行缩进，
  只匹配上 7/26 ——**若写成「解析不出就跳过」，这道门会立刻变成覆盖 27% 的真空门**，
  故改 fail-closed；② 源表误报三连（首个 FROM 命中 `NOT EXISTS` 子查询 / 最后一个 FROM 命中
  更早语句的 `pg_partitioned_table` / 首个 ORDER BY 命中列清单推导的 `ORDER BY attnum`）；
  ③ **把部分索引算成「有索引」**——首列匹配 ≠ 能用，部分索引只在查询谓词蕴含其谓词时可用，
  这必须由 EXPLAIN 背书。`livePromoteFunctions` 也曾把 `ensure_*`/`archive_*`/`drop_*`
  （另三个 spec 列表）算成活函数，改为按花括号匹配只取 `promoteSpecs()` 函数体并剔除注释行。
- **变异检验**：白名单塞假条目 → 自收缩检查红；Gate A 索引查找改 `WHERE false` → 报出
  全部 16 个活函数，证明不是象征性抽查。
- **未做**：非 hot 的其他分区族是否存在同型游标，本轮只普查了 `promote_*`。

### R79 续二回注（2026-09-29，批游标普查从 promote_* 专项升到全库）

- **范围**：`promote_*` 是专项普查，本轮扫**整个 `pg_proc`**，非 promote 族只剩 **4 个**批游标，
  **无新增 P1**。四个分三种性质，**混在一起就会误判**：
  - `archive_request_logs_default(id)` — 活函数，`FROM %I` 动态源表。R79 已修（迁移 756 建 `(id)` 索引）。
  - `archive_request_wal(created_at)` — **死函数**。`request_wal` 确实无 `created_at` 首列索引，
    但**正确修法是删函数不是补索引**：迁移 331 已声明要删它，只是 331 不在 installer startup 通道、
    本机 `schema_migrations` 只到 V359，它至今仍活在真库里。补索引等于优化一个没人调的函数。
    P3 深化，**登记不删**（删 schema 对象是迁移决策）。
  - `ensure_request_logs_partition(ctid)` / `repair_request_logs_detached_partitions(ctid)` —
    **非缺陷**。ctid 是系统列，`CREATE INDEX` 建不了；物理序 + `EXIT WHEN drained = 0` 的收敛保证
    本就是合法策略。
- **规则用错对象就是误报，必须单列豁免**：ctid 归入「不可索引」第三类，**只登记不判红并在输出里
  写明理由**。不写理由就等于把它悄悄算进「已覆盖」——与「把部分索引算成有索引」是同一类自欺。
- **泛化门与专项门范围必须互斥**：泛化门第一版没排除 `promote_*`，用它更粗的可达性判据
  （仓库全量 `.go` grep）把专项门已精确管理的 **6 项债原样重报一遍**——同样的 6 项、不同措辞，
  后果是这道门**永久红，红在别人管理的债上**。改为显式排除 + 注释写明「谁负责什么」。
  共用解析辅助函数（`unqualify` / `sourceTableForBatch` / `resolveCursor`）**只保留一份实现**，
  两份各自演化必然分叉。
- **fail-closed 第四次生效**：泛化门首版对 `archive_request_logs_default` 报「无法解析源表」，
  因为它的批次是 `FROM %I`，源表运行时才由 `pg_inherits` 决定。新增「动态源表」类，
  **要求写明 justification**（这张表实际是什么、为什么它的游标有索引支撑）否则判红。
  这正是 fail-closed 的价值：**唯一真正出过事的那个函数恰在它最该查的地方**。
- **变异检验 2 处红转绿**：撤掉动态源表 justification → 红并指名；撤掉 ctid 豁免 → 红，
  且正确区分 `ensure_request_logs_partition`（引用中）判缺陷 vs `repair_...`（未引用）仅记录。
- **仍未做**：非游标形态的查询面（按 ts 范围做对账/报表的接口）未做计划形状普查；
  本轮只覆盖「批游标」这一形态。

### R79 续三回注（2026-09-29，非游标查询面 + DEFAULT 分区普查）

靶子从「批游标」换成查询面，查分区裁剪是否真的生效。

- **P2｜`stats_event_inbox` 名义分区、实际零分区**：声明 `PARTITION BY RANGE (occurred_at)`
  却只有 DEFAULT 一个子分区，**1,419,612 行 / 1298 MB** 全在里面（跨 41 天）。
  `bg/partition_manager.go` 的 `ensureSpecs()` 无任何条目引用它（`bg/` 整包零引用）；
  全仓**零处** DELETE/TRUNCATE——消费者只 `markProcessed`，`replaySQL` 刻意保留已处理行，
  是一本只增不减的重放账本。**定 P2 不定 P1**：声明查询有部分索引兜底
  （`WHERE processed_at IS NULL`），今天不慢；真实代价是无界增长（10–26K 行/天，尖峰 150K）。
  待 owner 决策：(a) 接入 `ensureSpecs()` + 保留期清理；(b) 若不需要分区就去掉 `PARTITION BY`，
  别让下一个人以为有裁剪。
- **P3｜一处过度声明的注释**：`bg/partition_manager.go:1258` 写「partition pruning 对 WHERE
  范围查询仅扫命中分区」，对 2026-09-26 之前的 36 天数据**不成立**（那 1,255,179 行全在 DEFAULT）。
- **三条被证伪的假设**（每条都曾差点写进结论）：
  1. `session_turns` 的索引全带 `ON ONLY` → 若 PG 不递归则 6.2GB 热分区缺 `request_id` 索引，
     视图里逐行 `NOT EXISTS` 会退化成每行一次全分区顺序扫。**实测 5 个分区逐个查 `pg_index`，
     每个都有 `*_request_id_idx`。** 证伪。
  2. 计划里 `Seq Scan on request_logs_2026_07/08` 疑似缺索引 → **实测这两个分区是 0 行**，
     顺序扫是对的。本地空表假象，生产有数据时规划器会用索引。证伪。
  3. 「DEFAULT 装了 1168 MB，分区裁剪全废了」→ EXPLAIN 三次复跑否掉：窗口被显式兄弟分区
     **完整覆盖**时 PG 17.10 会裁掉 DEFAULT（`SET enable_partition_pruning=off` 可复现为 Append）。
     **未读规划器源码确认判定路径，故只登记实测行为、不登记机制解释。**
  一般化：**看到计划里不理想的形状，先查被扫描对象的真实体量和索引的真实分布，再归因。**
- **普查脚本的过滤条件会同时充当「筛选」和「掩盖」**：手工 census 带了
  `AND (子分区数) > 1`，而 `stats_event_inbox` 恰好只有一个子分区（DEFAULT 自己），
  被整条抹掉——**恰恰因为它退化，才不会被那个条件选中**。写成门后同一条查询没有该过滤，
  立刻报出 2 张。**「我想要的那些对象」与「我筛选之后还剩的对象」不是一回事。**
- **门的判据不是「DEFAULT 必须为空」**：那会红在一个**有文档的正确设计**上
  （`partition_manager.go:1256`「DEFAULT 保留作历史 catch-all」）。
  真正要抓的是**静默堆积**——分区创建滞后或从未接入，写入一路落进 DEFAULT 而无人察觉。
  故判据是**「有数据但没人登记过」**，并配三条自收缩断言（未登记即红 / 登记失效即红 /
  幽灵条目即红）。变异 3 处红转绿。

### R79 续四回注（2026-09-29，P1：分页的 LIMIT 没有约束工作量）

R79 在存储函数侧抓到「批游标列无索引 → O(rows²)」。本轮在**查询面**抓到同型误解：
`... FROM <view> WHERE <ts 范围> ORDER BY ts DESC LIMIT <page_size>` 读起来像「只要一页」，
实测页大小一点也没约束工作量。

- **受控对照**（同一天窗口、同一条 `ORDER BY ts DESC LIMIT 10`，**只改 FROM 来源**）：
  直查 `request_logs` **0.288ms**（Index Scan，下推）｜内层嵌套视图（含 LATERAL，
  **不含**两个反连接）**0.120ms**（`Merge Append` + `Limit loops=10`，下推）｜
  完整视图 `request_logs_with_current_month` **12390ms**
  （`Append actual rows=404794`，下推失效）。
  第二、三行**只差两个相关反连接**——视图体末尾对 `session_turns_hot` / `session_turns`
  按 `request_id` 的 `NOT EXISTS`。加上去之后规划器必须先判定每行能否通过反连接，
  无法对 UNION ALL 各分支预截断，只能全部物化、两轮索引探测、再排序。
- **直接证据**：`LIMIT 10` 与 `LIMIT 1000` 的 Append 行数**完全相同**（均 404794）；
  缓冲区读 **9,216,949 block**（≈70GB 逻辑读）换回 **10 行**。
  **所以不是深翻页问题**——第 1 页和第 500 页一样贵，贵的部分在 LIMIT 之前就付完了。
- **命中面**：`admin/logs.go` ctx 预算 30s（`:470`），默认窗口就是**一天**（`:474-475`），
  即一天窗口已经 12.4s；`page` 无上限（`:478-481` 只夹下界），
  窗口靠 R37 的 366 天上限兜着。
- **P1 但不由本轮修**：视图被 `admin/logs.go`、`bg/stats_minute_rollup.go`、
  `domains/routeincident/store.go`、`db/probe_views_unified.go`、`maas/usage.go` 共用，
  另有自愈重建链 + 迁移 575/577/680/696/700/717 + 视图 113/115 列冻结契约测试。
  **改视图是迁移 + schema 契约决策。**
- **门自己红过三次，每次都是门有缺陷，且同属一个家族**：
  ① 按字符串字面量判分页——`admin/logs.go` 用 `fmt.Sprintf` 拼装，FROM 源与 ORDER BY
  分处不同字面量，于是对**全树最被分页的那个查询**报「已无分页引用」；
  ② 只认纯标识符——SQL 源都带别名（`"... AS r"`），真实用法几乎全漏，
  改为抽字面量内标识符 token 再与**目录里的视图名**求交（用目录当词表）；
  ③ **门把自己的源码当成了消费方**——门文件里每个 allowlist 视图名都是字符串字面量、
  注释里还有 ORDER BY+LIMIT，删掉一条登记会让交集归零而**误触发真空守卫**。
  **这是最坏的门的失效形态：它因为一个与被审代码毫无关系的理由保持绿色。**
  修法：**门必须排除自己的目录**。
- **可迁移判定**：**「LIMIT」出现在代码里不等于它约束了工作量。**
  只有 EXPLAIN 显示下推（或 top-N）才算；`LIMIT 10` 与 `LIMIT 1000` 的 Append 行数相同
  就是它没约束的证据。受控对照要**只差一个变量**——本轮能定位到「那两个反连接」，
  靠的就是让两次查询只差反连接这一项，否则「视图很慢」只能停在抱怨层。
- **假阳性也写进登记并注明理由**（本门仓内扫描是文件级粒度，天然有假阳性）：
  `v_routable_credential_models` 经核实无生产查询读取；
  `v_task_model_ranking` 确为真阳性（`admin/auto_route.go:1084` 的 `ORDER BY … LIMIT $4`）但未实测；
  `session_turns_with_current_month` 混合（两处读无 LIMIT，一处经 CTE 间接 `LIMIT 20`）。

### R79 续五回注（2026-09-29，P1：有测试断言其文本的 SQL，从未被执行过一次）

- **P1｜`session_turns_with_current_month` 漏投影 `origin_actor`**：
  `domains/sessionsummary/message_source_v2.go` 的 `v2SessionBodiesBaseQuery`
  引用 `t.origin_actor`，而该视图的 65 列定值投影里没有这一列
  （基表 `session_turns` 上**有**，attnum 99；`db/db.go:2990` 只保证 `request_logs*` 表；
  全仓无任何 SQL 把它投进 turns 视图）。源码常量**原文实跑**：
  `ERROR: column t.origin_actor does not exist`；去掉该谓词则正常返回 53,851 行。
  **唯一相关的测试只断言文本包含某个 JOIN**——查询能否执行无人看。
  **两条设置路径都中招，这是定 P1 的理由**：`main_pipeline.go:1416` 在
  `sessions_v2_compression_read`（默认 true）下
  `SetMessageSource(NewPerTurnDigestSource(pool))`，而
  `NewPerTurnDigestSource = gated{digest: perTurnDigestSource, fallback: v2SessionBodiesSource}`
  ——开关开走 digest（同样缺该列）、开关关（**默认**）走 fallback（同样缺该列）。
  唯一可用配置是 `sessions_v2_compression_read=false` 退回 V1。
  消费面：会话摘要输入读取（`GenerateSummary` / `GenerateRollingSummary`）。
- **P1 候选｜`diagnostic_runs.route_key` / `routing_audit_log.reason` 缺列**：
  `domains/routeincident` 共 8 处查询；**基线 `01-schema.sql:7955` 与真库双缺**，
  全仓无任何迁移添加。`route_key` 只存在于 390 的 `routing_audit_log`——**另一张表**，疑串表。
- **新门：把仓内 SQL 常量对真库 PREPARE 一遍**（`TestData_GoSQLConstants_PrepareAgainstRealDB`）。
  抽取 160 条无 fmt 占位符的完整 DML 常量逐条 `PREPARE`——**只规划不执行**，
  故与「主库全程只读」的硬约束无冲突；`column … does not exist` 正是规划期错误。
  **错误必须按 SQLSTATE 分层，不能一律判红**：`42703`（列真的不存在，已翻遍仓内迁移确认）判红；
  `42P01/42704/3F000/42501`（本机库比代码旧、只读角色权限受限）只记录；其他 fail-otherwise。
  结果：133 成功 / 9 已定性 / 9 backlog / 9 本机无法验证（`outbox_events` 本机无表）。
- **门自己红了四次，全是「判错对象」家族**：
  ① 抽取器只判「含 SELECT」，把 **SQL 片段**（`requestLogsJoins` 是 JOIN 块、
  `sessionSummarySelectCols` 是列清单）与 DDL 常量也收进来 → 48 条假语法错；
  ② 没排除 **SQLite** 目录——换方言问错服务器；
  ③ **登记键 `file::name` 不唯一**：`action_infra.go` 有 **6 个同名 `sql` 常量**，
  自收缩检查命中其中一个后 `delete()` 抹掉登记，**把另外 5 个失败项的理由一起带走**，
  它们随即红在一个与自身 SQL 毫无关系的理由上。**一道门为了维护自己的白名单，
  把白名单改坏了。**
  ④ 同上。修法：键加**行号 + 内容哈希**；且**只报告、绝不在遍历中改注册表**。
- **backlog 用棘轮管，不用永久红**：9 条未分诊项登记而不判红——
  **一道长期红的门会让人习惯性忽略它**，此后它连自己抓到了什么都不再有人看。
  配三条断言：新增未登记的无法规划 SQL → 红；任一登记项开始能正常 PREPARE → 红（该收缩了）；
  `len(backlog) != expectedUntriagedBacklog` → 红（有人动了 backlog 却不改常量）。
  **这道门今天绿，是因为已知的 9 条被记账了；它不会因为记账而变瞎。**

### R79 续六回注（2026-09-29，backlog 清空，又挖出 2 个 P1）

上一轮 9 条无法规划的 SQL 逐条查证完毕，backlog 清空（机制保留）。

- **P1-3｜`tool_usage_stats` 列名漂移**：基线 `01-schema.sql` 与真库 `pg_attribute`
  **两个独立 SSOT 逐列一致**（`tool_id`/`usage_date`，**无** `tool_name`/`date`），
  全仓无任何改名迁移；而 `domains/toolexecution/postgres_store.go` 直接
  `INSERT INTO tool_usage_stats_hot (… tool_name, date, …) ON CONFLICT (tool_name, date)`。
  活接线 `cmd/gateway/tool_execution_integration.go:40` → **工具调用用量统计写入不可执行**。
- **P1-4｜`model_aliases.alias` 应为 `raw_name`**：基线该表只有 `raw_name`
  （`admin/logs.go:1249` 另有注释佐证），而 `internal/reasoncap/pgsource.go:59` 写 `ma.alias = $1`。
- **一条自我纠正**：我此前把 P1-4 误读成 modelcatalog 的问题——用 `paste` 把「键」行与
  下一行错误消息配对时**错位了一行**。**教训：错误信息与标识符必须由同一处成对输出，
  靠 shell 的 `paste` 拼两段不同来源的输出就是给自己制造假证据。**
- **其余 5 条的定性**：1 条是设计内模板占位符（`__mo_modality__` 启动探测后替换）；
  2 条是无 FROM 的列清单片段；1 条是 Go 字符串拼接体（正则只抓到第一段反引号）——
  这 4 条都是**抽取器判错对象**；1 条是 PREPARE 的推断限制（`42P08`，`$8` 只在 CTE 内被引用，
  运行时驱动会传类型）。
- **分类器新增 `42P08` 一档**：归「本机无法验证」，不判红。
- **自收缩检查补上缺失的一侧**：原检查只覆盖「登记项现在能正常 PREPARE」（bug 被修好），
  **不覆盖「抽取器不再采集它」**——补 FROM/拼接过滤后 4 条登记项悄无声息离开语料而门一直绿。
  **一份能持有不可达键的登记表，等于把自己的 backlog 藏起来。**
  现断言：**每条登记键都必须对应本轮语料里的一个候选**。
- 语料 155 条：**131 规划成功 / 14 已定性登记 / 10 本机无法验证**（9 条 `outbox_events` 本机无表 + 1 条 42P08）。

### R79 续七回注（2026-09-29，「P1 候选」量成 P1）

「缺列」是事实，「会不会真断」是另一回事——本轮量完可达性，8 处缺列由 **P1 候选升 P1**。

- **缺列三方一致**：基线 `01-schema.sql:7955` 无 `route_key`；真库 `pg_attribute` 无；
  全仓仅有的两处 `ALTER TABLE diagnostic_runs`（445 / 391）都只加 `created_at/updated_at`。
  `route_key` 全仓只出现于 **390 的 `routing_audit_log`**（另一张表，疑串表）。
- **可达性链**：`cmd/gateway/main.go:3539/3545` `NewStore` + `NewObserver` →
  `telemetryClient.AddOnRequestLogPersisted(observer.AsHook())`（**每条落库请求日志**）→
  `Observer.Transition` → `writeAudit` → `persistRunInTx` → `INSERT … route_key` → 42703。
  admin 侧 `NewRouteIncidentsHandler` 无条件接线，`DiagnosticRunsList` / `AuditLogListByRun` 同样失败。
- **加重因素**：`observer.go` 的重试循环按瞬时冲突设计（注释：「Transient: lock conflict,
  transient deadlock… the next persisted row will catch up」），但 `42703` 是**永久性**错误，
  每次重试与每条后续请求都必然同样失败。`maxRetries: 4` ⇒ **每条请求日志触发 5 次注定失败的查询**，
  退避后只记 warning。**重试机制对永久性 SQL 错误零收益，只把热路径的失败成本放大 5 倍。**
- **可迁移判定**：定级不能停在「缺一列」。**要问的是「这条路径默认开启吗、落在什么频率上、
  失败会被放大吗」**——三问都命中才是 P1。与 R79 续五给 `origin_actor` 定 P1 同源。

### R79 续九回注（2026-09-29，四个非 hot 族普查）

- **P2｜`request_wal` 完全没有保留期机制**，三条独立证据：
  ① 唯一该清理的 `archive_request_wal` 是死函数（R79 续二 已登记，331 未应用）；
  ② `drop_old_state_partitions` 函数体**根本没提 request_wal**，Go 侧零 DELETE/DROP/TRUNCATE；
  ③ **`lifecycle.request_wal_ttl_days` 全仓只命中它自己的声明，零消费方**——
  一个可热更新、界面可见、描述明确、默认 1 天的平台设置，**改了没效果且不报错**。
  代价实测 975,156 行 / 375 MB / 26 天；**1 天 TTL 生效时应只留约 1 万行 / 4 MB，
  实际是声明意图的约 80–100 倍**；且旧月分区也不被 drop，分区数本身在涨。
- **修法被 columnar 挡住（最实用的一条）**：直觉修法是补 DELETE，但
  `EXPLAIN DELETE FROM request_wal` 直接报
  `ERROR: UPDATE and CTID scans not supported for ColumnarScan`——
  `request_wal_2026_08` 是 citus columnar（am 359239），`_2026_09` 是 heap。
  **修复必须先处理 columnar 分区**（转回 heap，或改走 `DETACH + DROP TABLE`）。
- **P3｜`sessions` 保留期删除全分区 Seq Scan**：`bg/lite_retention_worker.go:131`
  `DELETE FROM sessions WHERE updated_at < ?` → 5 个分区全 Seq Scan（09 估 865,385 行）。
  `idx_sessions_status (status, updated_at DESC)` 用不上——首列 `status` 未被谓词约束。
- **一条被证伪**：我怀疑每月 1 日调度的 `archive_routing_decision_log` 会因 columnar 失败。
  **证伪**——它末尾是 `ALTER TABLE … DETACH PARTITION` + `DROP TABLE`，**不走 DELETE**，
  columnar 安全，该族被正常清理。**限制作用于操作类型，不是表**：
  「同样是 columnar 表」不等于「同样受同一限制」。
- 四族分区结构都健康（DEFAULT 空、边界正确）；但**仍是月分区**（`usage_facts` 已按 R68
  改日分区），月内 ts 范围查询不能裁剪。

### R79 续十 · 保留期删除的索引普查（1 个 P1 + 2 个 P2 + 1 个 P3）

**新判据：只认索引首列。** `candidate_failure_logs` 分区有 6 条索引，`ts` 是其中 4 条的
**第二列**；`armor_judgments` 的 `created_at` 只作 `(tenant_id, created_at DESC)` 第二列。
这是「索引首列匹配 ≠ 索引可用」的**镜像**——上一轮的规则只覆盖了「索引没这列」，
「索引里有这列但不是首列」这一半本轮才补上。

- **P1｜`armor_judgments` 删除吞吐追不上写入吞吐**（不只是缺一列）：
  804,253 行 / 327 MB，跨度 **86.94 天 < 90 天保留期 ⇒ 当前每轮删 0 行**，
  但仍要扫全表 + `Sort rows=378,463`（EXPLAIN cost 94,538，每 24h 一次）。
  写入 **6,318 行/天** > 删除硬上限 **5,000 行/天** ⇒ 净增 1,318 行/天，无界增长。
  写入方是 `security/armor/logger.go`（每条流式请求一条 observe-only 审计）。
  删除上限与保留期都是硬编码常量，**没有提速手段**。
  ⚠️ **修法不是补索引**——续十一生产规模对照实测（详见 reports/latest.md 续十一）：
  补 `(created_at)` 索引 763ms → 732ms，**落在无索引自身波动区间内（756–796ms）**，
  收益 ≈ 0 且给「每条请求写一条」的表加写入维护成本；**cost 差 3.8 倍而执行时间差 ≈ 0**
  （LIMIT 让 Seq Scan 提前终止，cost 假设扫完全部匹配行）。
  **正确修法 = 提高批量上限**：实测批量 ×20（5,000→100,000）耗时 966ms → 1,728ms（×1.8），
  吞吐却提升 20 倍，直接解除倒挂。
- **P2｜`journal_snapshot_receipts`**：94,657 行 / 57 MB，三条索引首列分别是
  `claim_until`(partial)、`tenant_id`、identity 复合，**无 `updated_at` 首列**（Seq Scan
  cost 5,020）；且 `domains/requestjourney/retention.go:143` **无 LIMIT 无分批**。
- **P2｜`candidate_failure_logs`**：81%（67,580 行）早已过期，积压 13.5 天。
  **只定 P2**——写入 318 行/24h ≪ 删除能力 5,000/天，积压可消化。
  **与 armor_judgments 结论相反，靠的是速率对比不是峰值**。
  ⚠️ 续十曾把「每轮扫 67,826 行只为删 5,000 行（13.6x 放大）」当成已证实成本，
  **那是 cost 估算、未实测执行时间**，同规模实测显示 LIMIT 会提前终止；已在原文加注更正。
- **P3｜`model_iq_runs`**：0 行，365 天保留期未到触发规模，登记而非豁免。
- **P3｜一条注释已与现实不符（已修）**：`internal/trace/stage_events_retention.go`
  原写「该表没有 created_at 单列索引…再补 created_at 索引迁移」，实测
  `idx_stage_events_created_at` 存在且被采用。**全仓唯一写着要补该索引的地方**，
  owner 照做会做一次已完成的迁移。
- **证伪｜`request_state_transitions` 的「无 LIMIT 全量 DELETE」不是缺陷**：
  形状上正该红，实测跨度 **7.01 天**（= 7 天保留期，说明保留期生效）、每 tick 1,103 行、
  索引在用。记下来免得下一轮重新怀疑。

**门 `TestData_RetentionTrim_PredicateColumnHasLeadingIndex`** 覆盖 37 条保留期 DELETE，
三分桶（`retentionSmallTables` 9 张无害小表 / `knownRetentionDefects` 4 张已知缺陷 + 棘轮 /
其余新判红）。**不让已知缺陷一直红**——长期红的门会被人习惯性忽略。
三条 fail-closed：扫描器失效 `Fatalf`、过期登记判红、报错指名对东西
（第一版正则把 `DELETE FROM public.x` 的表名报成 `public`，由首跑结果暴露）。

### R79 续十一 · 「cost ≠ 执行时间」：补索引的建议被自己的对照实验否掉

续十给 `armor_judgments` 提的修法是补 `(created_at)` 索引。拿回生产规模
（804,253 行）TEMP TABLE 真删真计时后，**该修法收益 ≈ 0 且是净损失**：

| 场景 | 方案 | 实测(ms) |
|---|---|---|
| 0% 过期（生产现状） | 无索引 LIMIT 5000 | **763**（波动 756/763/796） |
| 0% 过期 | 补索引 LIMIT 5000 | **732**（落在波动区间内） |
| 0% 过期 | 无索引 LIMIT 500000 | **871** |
| 97% 过期 | 无索引 LIMIT 5000 | **966** |
| 97% 过期 | 补索引 LIMIT 5000 | **915** |
| 97% 过期 | 无索引 LIMIT 100000 | **1,728** |

**EXPLAIN cost 差 3.8 倍（27,644 → 7,294），执行时间差 ≈ 0。**
原因：cost 假设 Seq Scan 扫完全部匹配行，而 `LIMIT` 让它**提前终止**——
97% 行过期时扫到第 5,000 个就停（20 万行样本上耗时随 LIMIT 线性：
5,000→237ms / 40,000→355ms / 200,000→1,070ms）。

**这是「索引首列匹配 ≠ 索引可用」的第四种形态：cost 估算 ≠ 执行时间。**
只看 EXPLAIN 的 cost 就写修法，等于没测过。

`armor_judgments` 仍是 **P1**，但理由是**吞吐倒挂、不是缺索引**，修法是
**提高批量上限**（`bg/audit_trimmer.go` 常量），不是加迁移。
`journal_snapshot_receipts` 的修法不受影响——它那条 DELETE **无 LIMIT**，
没有提前终止可剪枝，补索引 + 分批仍成立。

### R79 续十二 · DELETE 形态才是瓶颈（零迁移，124 倍）

续十一否掉「补索引」之后，真正瓶颈浮出水面——**在 DELETE 形态上**。生产规模
（804,253 行）TEMP TABLE 对照：

| 方案 | 实测 |
|---|---|
| A 现状 `id IN (SELECT id … ORDER BY … LIMIT 5000)` | **870.7ms** |
| B `id IN` 去掉 `ORDER BY` | 695.6ms |
| C **`ctid IN (SELECT ctid … LIMIT 5000)`** | **7.0ms**（124 倍） |
| D C + 补 `(created_at)` 索引 | 6.8ms（无用） |

`id IN` 让 DELETE 主体**全表扫**做 Hash Join（`Seq Scan actual rows=804306`）；
`ctid IN` 用 **Tid Scan** 按物理地址定位，外层不扫。**本仓已有两处同形态先例**
（`bg/session_summaries_trimmer.go:122`、`domains/attachments/repository.go:294），
改法不需要任何迁移。`candidate_failure_logs` 同型：411ms → 19.5ms（22 倍），
瓶颈是 `ORDER BY` 逼出的 top-N heapsort 全量 Sort，不是索引。

**一条反例（不能整类推广）**：`session_aggregate_outbox`（1,157,822 行）
改形态**无差别**（6,216ms → 6,278ms）——它的 `completed_at` 本来就是索引首列，
内层已走索引，6.2 秒是 5,000 次随机回表到 1.5GB 宽表的 I/O。
**ctid 形态的收益只来自消除外层全表 Hash Join；外层本来就不贵的表改了等于没改。**

**测量纪律**：中途出现过「大表比小表快 7 倍」的自相矛盾结果——因为前一个表已被前几轮
实验扫描、全在 shared_buffers 里。**单看耗时不可靠，扫描行数（`actual rows`）才可靠**。

### R79 续十三 · 最后一条待实测：journal_snapshot_receipts 的 P2 被降级

测的时候先撞上事实：该表**没有 `id` 列也没有主键**（唯一键
`(tenant_id, request_id, snapshot_version)`）——`id IN` 形态对它不适用，
写出来会 42703。续十二的修法要 ctid 形态 + `updated_at` 索引配合。

实测（92,862 行，匹配仅 ~556 行，重复 4 次取中位）：

| 方案 | 实测 |
|---|---|
| **现状**（无 LIMIT、无索引） | **98ms**（94.1/94.5/101.6/127.2） |
| 补 `(updated_at)` 索引 | **285ms** ← 无收益 |
| `ctid IN` + `LIMIT 500` | **30ms**（3.2×） |

**P2 → P3，降级理由是绝对成本不是相对倍数**：本表 98ms×24 次/天 = 2.4 秒/天，
而 `armor_judgments` 是 870ms×1 次/天 = 0.87 秒/天——**单位时间成本几乎相同**
（0.040 vs 0.036 ms/s），但绝对值小两个量级，收益是「每天省 1.6 秒」。

**本轮第三次纠正定级**（session_bodies P2→P3、armor 补索引作废、本条 P2→P3），
三次共性是同一个错误：**把相对倍数或 cost 估算当成了实际代价**。

形态风险（无 LIMIT 无分批在积压时可能超量删、撞 30s ctx 回滚且只 Warn）仍登记为
潜在风险，但实测跨度 7.0 天 = 7 天保留期说明积压不存在，故不占现实成本。

**顺带印证**：`retention_test.go:172` 只做 `strings.Contains` 文本断言、不校验列存在性；
真正守住这条 SQL 的是续五的 PREPARE 真库门。

### R79 续十四 · tool_usage_stats：代码里写着 ≠ 会执行（P1 降 P2）

核可达性时发现：**`AggregateDaily` / `SaveStats` / `ListToolNamesWithActivity` 各 0 个生产调用方**。
那 12 个错列**从未被 PostgreSQL 执行过**——不是线上故障，是一段从未跑过的代码，P1 属虚报。

`grep AggregateDaily` 出的全是 `AggregateDailyProfiles`——providerprofile 的**另一个同名方法**，
名字相似极易误判成有调用方。

错列范围也比先前记录的大：INSERT 用 15 个列名，真库只有 11 列，**12/15 不存在**
（`p50/p95/p99_duration_ms`、`unique_users/unique_sessions/top_users` 三个分位数与三个去重维度
在真库**根本没有**）。`ToolUsageStats` 结构体有 17 个字段依赖这 6 个维度，所以修法不是改名，
是路线选择：**加 8 列迁移 vs 改代码降级**（后者丢掉 6 个统计维度）——属 owner 决策。

**缺陷仍是真的且更阴险**：`AggregateDaily` 每条失败只 `slog.Error` 计数、**返回 nil**。
一旦有人补定时任务，每条 INSERT 都 42703 而调用方看到 `err == nil`，**以为聚合成功**。

**两条证据必须分开**：`AggregateDaily` 零调用方 ✅（代码事实，与流量无关）；
`tool_usage_stats`/`session_tools` 都是 0 行 ❌（本机是无流量开发库，空表属正常）。
**「表是空的」在开发库上不是发现。**

**本轮第四次定级纠正**（session_bodies P2→P3 把峰值当分布 / armor 补索引作废 把 cost 当执行时间 /
journal_snapshot_receipts P2→P3 把相对倍数当绝对成本 / **本条 把形态相似当可达**）。
**代码里写着某段 SQL，只说明它被写下来了，不说明它会执行——可达性优先于形态。**

### R79 续十五 · 可达性登记门

`TestData_SchemaMismatch_ReachabilityIsStillAccurate`（不连库，只读源码）把
「schema 不匹配」类发现变成**可机械复核的登记**：每条带一个 AST 层面的
`pkg.Symbol` 锚点 + 实测调用点数。当前 4 条 = 活接线 2 + 潜伏 2。
**它守的是「潜伏缺陷被接线的那一刻」**——那正是续十四那两个陷阱被引爆的时点。

**门自己暴露三个问题**（首跑 + 变异检验抓出，非 review）：
① **假绿**——`for _, sym := range needles` 取 value（登记名）而非 key（symbol），
所有匹配恒为 0，两条 ORPHAN「碰巧对了」；**门在自己身上制造了它最该防的那类假绿**。
② **假阳性**——文本匹配把注释当调用点（仓内大量注释提到函数名）。
③ **假阴性**——用正则剥注释更糟：`//`/`/*` 会在字符串字面量里出现
（`"https://..."`、SQL 注释），正则一路吞到行尾把真代码删掉。
**修法：判调用就在 AST 层面判**（`go/parser` + `SelectorExpr`），注释和字符串不进 AST。

**锚点必须指向「接线」而不是「构造」**：第二次变异没红——我用构造函数
`routeincident.NewObserver` 当锚点，摘掉 `AsHook()` 注册后 `main.go:3545` 仍在。
**构造了不等于挂上了**；改用注册点 `incidentObserver.AsHook` 后两种变异都精确转红。

顺带：AST 的 fail-closed 抓到 7 个并发会话遗留的 `<<<<<<<` 冲突文件，
门把「工作区冲突态」与「代码解析失败」分成两条红因——前者不是缺陷，但覆盖率缺口必须可见。
**工具陷阱**：`grep | head -N` 的截断会制造假零命中（我据此误判 `AsHook()` 零调用）。
**判「零命中」前先确认没有截断。**

### R79 续十六 · 修掉一个 P1：origin_actor 投影（真库事务内验证）

`session_turns_with_current_month` 漏投影 `origin_actor`（基表 `session_turns` /
`session_turns_hot` 都有，attnum 99）。两条生产 SQL 引用它、两条设置路径都踩
（`main_pipeline.go:1184` V2 源 + `:1416` digest 源，digest/fallback 两条都引用）。

**运行时证据**：`session_analysis_metadata` 147,358 行**全是 `status='provisional'`，
`final` 0 行**，而 close hook 只写 final。表不空但「只有坏路径才写的状态是零行」——
比空表强。失败被 `session_metadata_close_hook.go:47-53` 吞成 Warn + return nil
⇒ 每次会话关闭都失败且静默。读侧退回 provisional，属**优雅降级**。

修法（迁移 757）在真库 `BEGIN … ROLLBACK` 内完整跑过：53→55 列、两条 PREPARE 均无
ERROR、回滚后 54 列且 `origin_actor=0`（**零持久变更**，不违反只读契约）。

**迁移写法两个要点**：
1. **别硬编码旧列数**——我用正则数得 53，真库权威值 **54**（`SELECT hot.id,` 行首不是
   空白被漏掉）。改为执行前动态取 `old_cols`。
2. **DDL 必须顶层执行**——`CREATE OR REPLACE VIEW` 包在
   `DO $$ … EXECUTE $ddl$…$ddl$; $$` 里**静默无效**：不报错、视图不变、
   **前后自校验全部照常通过**（顶层 55 列 vs EXECUTE 内 54 列，同一事务实测）。
   **一个「有前后自校验的迁移」可以完全什么都没做而不被发现。**
   （`DROP VIEW CASCADE` + `CREATE VIEW` 在 DO 块内有效，但 CASCADE 会级联删依赖视图。）

门 `TestData_OriginActorFix_MigrationIsShippedAndNotSilentlyDisabled` 守住修复本身：
UNION 两半各一处 `origin_actor` / 顶层 DDL 存在且未被包进 `EXECUTE $ddl$` / 后置断言存在。
变异 2 处全部指名（改回 DO 包装 → 红「请改回顶层执行」；漏一半投影 → 红「出现 0 次，期望 1」）。
**门自己又踩一次同一个坑**：第一版用 `strings.Contains(up,"EXECUTE $ddl$")`，
而迁移文件头「踩坑记录」里**提到**该坏写法 ⇒ 误报。改为先剥 SQL 注释再检查。
（两天内第二次「文本匹配不区分代码与注释」：可达性门拿注释当调用点，这次拿注释当坏写法。）

### R79 续十七 · 修掉第二个 P1：routeincident 四处缺列（迁移 758）

缺的不只先前记的 2 列，是 **4 列**：`diagnostic_runs` 缺 `route_key`/`parameters`/`result`
（代码 INSERT 用 12 列，真库 13 列；真库另有 `heartbeat_at`/`trigger_source`/`error`/`summary_json`
代码没用到），`routing_audit_log` 缺 `reason`。三方一致：基线 / 真库 / 迁移链
（`grep ADD COLUMN` 只命中别的表的 `compression_reason`/`handoff_reason` 等）。

可达性：`main.go:3554` → `Transition` → `writeAudit` → `persistRunInTx`，
**每条落库请求日志都走**；`MaxRetries: 4` 使 42703 被重试满 5 次，而 42703 是
**永久性错误**、重试纯属浪费，耗尽只 `slog.Warn`。

**修法是纯加列、代码零改动，这一点靠实测而非推断**：三列是 `map[string]any`，
代码传 `json.Marshal` 的 `[]byte`，而 SQL 无 `::jsonb`、靠 PG 从目标列反推参数类型。
用 pgx v5 在 TEMP TABLE 上复刻真实 INSERT 形态实测成功（回读 `{"model":"glm-5.3",…}`）⇒ 不必改代码。
`reason` 用 `varchar(256)`：代码 `MaxReasonLen=256` 且按 **rune** 截断，
PG 的 `varchar(n)` 同样按**字符**计，语义一致。

真库事务内验证：13→16 / 21→22，两条 INSERT 的 PREPARE 均无 ERROR，回滚后 13/21。
**顺带纠一个我自己记错的数字**：此前文档写「routing_audit_log 22 列」，实测 **21 列**
（22 是加完 reason 之后）。本会话第 5 次自我纠正（4 次定级 + 1 次数字）。

门 `TestData_RouteIncidentFix_MigrationIsShippedAndNotSilentlyDisabled` 四类断言，
变异 4 处全指名：删列 → 红「仍会 42703」/ 包进 DO 块 → 红「静默无效，请改回顶层」/
`route_key` 落成 text → 红「[]byte 绑不上了」/ 删后置断言 → 红「缺少 post-check」。
**第四个断言的依据直接来自续十六**：只 `RAISE NOTICE` 的自校验正是让静默无效 DDL
畅通无阻的原因，所以要求它**读 `information_schema.columns`**。

### R79 续十八 · 收口：757 未破坏 113/115 列冻结契约

续十六改的是 `session_turns_with_current_month`（54→55），冻结契约管的是
`request_logs_with_current_month`（**同名相近，容易误当成同一件事**），故实跑验证。

**① 自愈链会不会撤销加列**：`db/request_logs_view_schema.go` 的自愈重建链目标是
`request_logs_with_current_month` 及其两个包装视图，**不含 turns 视图**；全仓 Go 侧
`CREATE … VIEW … session_turns` **零命中**。⇒ 757 只由迁移链决定，不被应用侧回滚。

**② 列序列数没被动**：真库事务内迁移前后各取一次 115 列视图完整清单，
**两次逐字节完全相同**；同时 turns 视图 54→55；ROLLBACK 后复原。

**③ 意外旁证**：115 列视图**自己就有 `origin_actor`**（`is_final_success` 之后）。
`db/db.go:2990`「只保证 request_logs 有该列」属实，**turns 视图是唯一漏的那个**
⇒ 修复不是「引入新列」而是「补上同族视图早已有的投影」，与既有契约同形。

顺带：冻结契约在 `request_logs_view_schema.go:174-179` 有**运行时守卫**
（列数不在 {113,115} 就 keeping v1 body），不只活在测试里。
