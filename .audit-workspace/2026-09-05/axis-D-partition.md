# 审计报告 · 轴 D：大数据 hot + 分区（columnar）存储生命周期闭环

- 仓库：`/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5`（只读审计，未改任何源文件）
- 日期：2026-09-05
- 范围：24h 内合入的 656（auto_route_selections 8h 热表＋月分区）、655（session_summaries 对账）、`bg/partition_manager.go`、`admin/data_lifecycle_hot_partition.go`、`bg/auto_route_settle_worker.go`、`bg/auto_route_affinity_worker.go`、`db/session_summaries_schema.go`、installer embeddata、`scripts/apply-db-revision-sequence.sh`，以及全仓对分区父表的 UPDATE/DELETE 扫描。

## 一、发现清单（P0–P3，按严重度排序）

| # | 级别 | 摘要 | 证据 |
|---|------|------|------|
| 1 | **P1** | **settle worker 直接 UPDATE 分区父表 `auto_route_selections`，违反"更新/删除只在 hot 表"基线**。`writeReward`/`abandon` 按 `storageTier` 路由：`parent` 行落到父表 `UPDATE auto_route_selections SET settled_at=NOW()...`。结构性根因：abandon 窗口 24h（`settleAbandonAfter`）> hot 保留 8h，任何 8h 内未结算的行被 promote 进父表后**必然**需要父表 UPDATE。当前能跑通仅因 auto_route_selections 的分区是 heap（478/656 建 `PARTITION OF` 未带 `USING columnar`）；一旦按 request_logs 先例（399：request_logs 分区 ALL columnar）切 columnar，这些 UPDATE 将全部失败，且失败行每 5 分钟重试、`LIMIT 500` 的 pending 批会被父表积压挤占。 | `bg/auto_route_settle_worker.go:475-507`（table 路由 + UPDATE）、`:54`（24h）、`:319-325`（parent tier 扫描）；对照 `sql/migrations/startup/478_auto_route_affinity.sql:166-201`、`656_auto_route_selections_hot.sql:76-79`（heap 分区） |
| 2 | **P2** | **settle 的 outcome join 只读 `request_logs_hot`，与 8h promote 窗口冲突**：请求日志 8h 后被 promote 进 columnar 父表，8h–24h 之间仍未结算的 selection 匹配不到 outcome（`success=nil`），到 24h 被 abandon 成 NULL reward——即使 outcome 数据真实存在于 request_logs 分区。代码注释"request_logs_hot retention ~7 days, migration 399"已过时：运行时默认是 8h。学习闭环数据被系统性污染（有界：仅覆盖延迟 >8h 的请求）。 | `bg/auto_route_settle_worker.go:190-211`（hot-only 注释）、`:305-343`（`LEFT JOIN request_logs_hot`）、`:373`（24h abandon）；对照 `bg/partition_manager.go:35,1119`（8h 默认）、`settings/spec_lifecycle.go:7`（默认 8，Max 720） |
| 3 | **P2** | **656 无网关侧 ensure，部署强耦合 installer/DBA 脚本**：655 有 `ensureSessionSummariesCanonical` 随 `ApplyMigrations` 启动生效，而 656 的 hot 表/ensure/promote 函数只存在于 installer 与 revision 脚本（脚本注释自认"no db.go ensure covers it"）。只升级二进制、不重跑 installer/脚本的存量库：`auto_route_selections_hot` 缺失 → selection_writer 整批 INSERT 失败静默丢弃（仅 Warn 计数），PartitionManager 每小时 promote/ensure 报错——AUTO 路由遥测全量丢失。 | `db/db.go:142-148`（655 有 ensure）vs 全 `db/` 无 656 ensure（grep 为空）；`scripts/apply-db-revision-sequence.sh:61-62,72`；`domains/hooks/observability/telemetry/selection_writer.go:254,183-188` |
| 4 | **P2** | **656 行为测试未接入任何自动执行路径**：`sql/tests/auto_route_selections_hot_tests.sql` 只能手工 psql；CI（integration-testcontainers-ci）只跑 Go 测试，不执行 psql 脚本；统一入口 `apply-hot-table-migrations.sh` 也只跑 `partition_hot_table_tests.sql`，未加入 656 的新测试。回归防护实际只剩字符串断言（见 #11）。 | `scripts/apply-hot-table-migrations.sh:12,105`；`.github/workflows/integration-testcontainers-ci.yml`（无 psql 步骤） |
| 5 | **P2** | **`ensureSessionSummariesCanonical` 裸 ALTER、无表存在性守卫**：若 memora 侧干扰升级为 DROP/重建整表（本次事故是改 shape，下次未必），首条 `ALTER TABLE` 报 42P01 → `applyMigrationsOnce` 失败 → `Open()` 失败 → 网关拒绝启动，与"对账自愈"意图相悖（沿用了 645 ensure 的旧模式）。 | `db/session_summaries_schema.go:39-42`；`db/db.go:96-115,146-148`（失败即启动失败）；对照 `db/goal_client_signal_schema.go:24`（同类旧模式） |
| 6 | **P3** | **promote 的 `ON CONFLICT DO NOTHING` 静默丢弃冲突 hot 行**：父表已有同 `request_id` 的 legacy 行时，hot 行被 DELETE 且 INSERT 被跳过——回放行的 outcome/reward 丢失（注释声明是有意权衡，测试 #6 固化该行为）。属于文档化的窄路径静默数据丢弃。 | `sql/migrations/startup/656_auto_route_selections_hot.sql:142-146`；`sql/tests/auto_route_selections_hot_tests.sql:146-164` |
| 7 | **P3** | **SQL 侧函数默认 retention 与 8h 基线漂移**：`promote_request_logs_hot_to_partition` SQL 默认 `'7 days'`，Go 调用方显式传 8h 所以运行时正确，但任何无参手动/外部 cron 调用会静默按 7d 执行。656 的新函数默认 8h 是对的，两代函数口径不一致。 | `sql/objects/functions/promote_request_logs_hot_to_partition_interval_integer.sql:5` vs `656_auto_route_selections_hot.sql:112` |
| 8 | **P3** | **Go ensure 与 SQL 655 双份手工维护、无漂移防护**：本次核对 54 列 + 3 约束 + 6 索引完全一致（脚本 diff 为空），但没有任何测试或运行时检测防止未来发散；且 `ADD COLUMN IF NOT EXISTS` 对"已存在但定义不同"的列静默跳过，类型漂移不可见。 | `db/session_summaries_schema.go` vs `sql/migrations/startup/655_session_summaries_schema_reconcile.sql`（无对照测试：grep `_test.go` 中 "655_session" 为空） |
| 9 | **P3** | **655 down 迁移不完整**：step 0 补的 `tenant_id` 不在 down 的 DROP 列表中（minimal-shape 库回滚后残留 655 新增列）；头部已自警 canonical 库禁用本 down（会误删 canonical 原生列）。 | `sql/migrations/startup/655_session_summaries_schema_reconcile.down.sql:26-79`（无 tenant_id）vs `655_session_summaries_schema_reconcile.sql:40-41` |
| 10 | **P3** | **编号簿记不一致**：656 写 `schema_migrations('656')` 而 655 不写（650 写）；installer embeddata 带 655 down 却缺 656 down（651–654 均带 down）；重编号（9f0d3844b）前已应用旧 655_hot 的库会残留一条描述为"auto_route_selections hot heap..."的 `'655'` 行，与新 655 语义混淆。 | `656_auto_route_selections_hot.sql:165`；655 无 schema_migrations 语句；`installer/cmd/llm-gw-installer/embeddata/startup/`（有 655 down、无 656 down）；`git log 9f0d3844b` |
| 11 | **P3** | **migration_656_test.go 是字符串包含式断言，且函数名仍是 `TestMigration655...`（重编号残留）**：能钉住 SKIP LOCKED / advisory lock / DETACH+ATTACH default 等关键标记（有价值），但断言不了语义（如 `ON CONFLICT DO NOTHING`、retention 参数传递、schema_migrations 记录），防"漂移类问题复发"能力有限。 | `sql/migrations/startup/migration_656_test.go:10,24-58` |
| 12 | **P3** | **auto_route_selections 月分区无出口（TTL/归档均未接入）**：`drop_old_state_partitions`、`archiveSpecs()` 均不含该表，分区只进不出、无限增长；DEFAULT 分区行数也无监控（rule 33 安全网被长期忽略时会静默积压）。 | `bg/partition_manager.go:835-841`（archiveSpecs）、`:407-446`（drop_old_state_partitions 表清单无 auto_route_selections） |

**已验证为正确的关键点（无发现）**：

- **promote 原子性/幂等/崩溃安全**：单条 data-modifying CTE（batch→DELETE hot RETURNING→INSERT parent→count），Go 侧再包显式事务；崩溃=整批回滚，不丢不重；排干后重复调用返回 0（`656_auto_route_selections_hot.sql:121-147`；`bg/partition_manager.go:907-1015`；行为测试 #6/#7 覆盖）。
- **双实例并发防护**：Go 侧 `pg_try_advisory_xact_lock(FNV-1a(label))` 每批获取、抢不到即跳过（`bg/partition_manager.go:900-905,946-971`）；SQL 侧 ensure 内 `pg_advisory_xact_lock(hashtext(...))` 串行化 DDL（656 sql:56）。admin 手动 promote 不走 Go 锁，但 CTE 原子性 + `FOR UPDATE SKIP LOCKED` 使并发安全（行集不相交）。
- **8h 保留语义（656 表）**：默认走 `lifecycle.hot_retention_hours=8`（下限 1h），promote 周期 1h，retention≤0 / batch 越界在 SQL 与 admin 双侧拒绝（`656 sql:116-117`；`admin/data_lifecycle_hot_partition.go:261-263,589-592`；`bg/partition_manager.go:1118-1132`）。
- **写路由**：业务写入只落 `_hot`（`selection_writer.go:254`）；`auto_route_selections_all` 是 UNION ALL 普通视图（不可写），代码无任何对视图的写，读写分离正确；affinity 聚合读视图两 tiers 全覆盖（`bg/auto_route_affinity_worker.go:204`）。
- **Go 代码无对分区父表的 DELETE**（grep 全仓为空；唯一父表写是 #1 的 UPDATE 与 registry/usage_stats.go:60-72 的遗留 fallback upsert，后者不在本次 24h 范围）。
- **655 vs 656 重编号干净**：当前树无 `655_auto_route_selections_hot` 残留引用；两迁移创建对象不相交；installer embeddata 655/656 与 `sql/migrations/startup` **字节级一致**（cmp 通过）；runner 顺序 655 先于 560、656 在 478 之后，依赖正确（`installer/internal/dbinit/runner.go:50,97`；`TestStartupFilesAreAllEmbedded` 防漏 embed）。
- **655 对账幂等**：minimal shape（session_id PK + summary_json）与 canonical shape 双向可重跑——全部 `ADD COLUMN IF NOT EXISTS` + 先查 `pg_constraint/pg_indexes` 再建；GENERATED 列依赖顺序正确（search_vector 在 title/summary 之后）；唯一索引失败降级 WARNING 不阻塞启动。
- **656 down 完整**：先拷 hot 行回父表（显式列 + ON CONFLICT DO NOTHING）再 drop，删 schema_migrations 行，restore 语义正确。
- **ensure 多语句可行**：pgx pool 配置 `QueryExecModeSimpleProtocol`（`db/db.go:54`），多语句 ensure 单次 Exec 隐式事务、原子的。

## 二、「hot 生命周期需求 → 实现现状」对照表

| 需求基线 | 实现现状 | 判定 |
|---|---|---|
| 大数据表 = hot 表 + 分区（columnar）结构 | auto_route_selections：`_hot` 堆表（656）+ `PARTITION BY RANGE(partition_date)` 父表（478）+ ensure 月分区 + DEFAULT 安全网。注意：该表的月分区是 **heap** 而非 citus columnar access method（request_logs 才是），"columnar 层"是名义口径 | 基本满足（口径差异） |
| 更新/删除只允许在 hot 表 | 业务写入 ✔ 只写 hot；无代码 DELETE 分区父表；**但 settle worker 对 parent tier 行直接 UPDATE**（#1），24h abandon 窗口 > 8h hot 窗口使父表 UPDATE 成为设计内常态 | **违反（P1）** |
| hot 只保留 8 小时 | `lifecycle.hot_retention_hours` 默认 8、promote 每小时、双侧 guard；656 表完全合规。例外面：request_logs_bodies 24h、model_probe_runs 纯 TTL DELETE（文档化豁免）；SQL 函数默认 7d 漂移（#7） | 满足（有小漂移陷阱） |
| 后台任务批量转移（promote） | 15 张表统一 `promote_*_hot_to_partition`，1h 周期、批量 5000、100 批/周期预算、逐表 60s 超时、失败不饿死其他表；656 表已接入 promoteSpecs/ensureSpecs 并有测试钉住 | 满足 |
| promote 幂等、事务性、崩溃安全 | 单语句 CTE + 显式事务 + SKIP LOCKED；崩溃回滚不丢不重；排干判定终止 | 满足 |
| advisory lock 防多实例并发 | Go `pg_try_advisory_xact_lock`（每表每批）+ SQL `pg_advisory_xact_lock`（ensure DDL）双层 | 满足 |
| 统一视图写路由落 hot | 视图只读（UNION ALL 不可自动更新），写路径直插 `_hot`，无视图写 | 满足 |
| 655/656 重编号无冲突、down 完整、installer 字节一致 | 无同名对象冲突；embeddata 字节一致；656 down 完整；655 down 缺 tenant_id、簿记不一致（#9/#10） | 基本满足 |
| session_summaries 对账幂等、Go/SQL 列集一致 | minimal/canonical 双向幂等；54 列 + 3 约束 + 6 索引当前一致；无漂移防护、缺表场景脆弱（#5/#8） | 基本满足 |
| 迁移测试防漂移复发 | 合同测试钉住关键安全标记（有价值）但为字符串级；行为 SQL 测试未自动化（#4/#11） | 部分满足 |

## 三、总体结论

**主链路扎实，闭环有一个结构性违规点和两个部署/验证缺口。** 656 的 promote 机制（原子 CTE、SKIP LOCKED、双层 advisory lock、retention/batch 双侧守卫、DEFAULT 分区搬迁）是全仓 hot 生命周期实现里质量较高的一版，655→656 重编号执行干净、installer 字节一致。核心问题：

1. **P1-#1**："更新只在 hot"的不变量被 settle worker 破坏——这是 24h abandon 窗口与 8h promote 窗口的结构性冲突，不是笔误；修法二选一：把 abandon/未结算兜底压缩到 8h 内完成，或为 parent tier 提供经 hot 中转的回写路径（不允许直写分区父表）。
2. **P2-#3/#4**：656 走"installer/DBA 脚本 only"路线且行为测试未自动化，升级路径上存在遥测静默丢失的实际风险；建议给 656 补一个 `db/db.go` 侧 ensure（或至少启动时 `to_regclass` 探测 + 显式 Fatal），并把 `auto_route_selections_hot_tests.sql` 挂进 `apply-hot-table-migrations.sh`。
3. **P2-#2**：settle 的 request_logs_hot-only join 需要按 8h promote 现实重审（注释已过时），否则 8h–24h 的 outcome 匹配空洞会持续污染 affinity 学习数据。

其余为簿记/测试强度/漂移防护类 P3，不阻塞但建议随下个迁移窗口清理。
