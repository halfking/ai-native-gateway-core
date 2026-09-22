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
