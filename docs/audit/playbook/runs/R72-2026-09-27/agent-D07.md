# D07 hot/columnar 分区与迁移 子代理报告（窗口：949ec2f70..24c5c545a，origin/main HEAD = 24c5c545a）

改动面（本域子集）：`sql/migrations/startup/753_session_turn_logs_ttl.sql`(+`.down.sql`)、`sql/migrations/startup/migration_753_test.go`、`bg/partition_manager.go`(+`bg/partition_manager_session_turn_logs_ttl_test.go`)、`settings/spec_lifecycle.go`；横向对照 `domains/session/v2/turn_logs_writer.go`、`installer/`（embeddata/main.go/dbinit/runner.go/stats_migrations_test.go）、`scripts/apply-db-revision-sequence.sh`、`sql/migrations/startup/752_mock_probe_history.sql`。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P2** | **753 首扫是"无界单语句 DELETE"，且在开机路径同步执行**：`cleanup_session_turn_logs_by_ttl` 的 DELETE 无 LIMIT/分批；而迁移头注自证"session_turn_logs 在生产上从未被清理过——表无界增长"（每 turn 写 7 行的热写表）。触发路径：新二进制部署到存量真库 → `Start()` 启动即调 `archiveOldPartitionsIfNeeded`（boot 同步段，先于 `close(pm.ready)`）→ 首扫一口气 DELETE 全部过期积压：长事务持行锁 + WAL 尖峰 + 表膨胀；若超过 `context.WithTimeout(5*time.Minute)`，语句被取消整批回滚，**下次重试要等 24h**（`main.go:4699` 传 24h interval），大积压下清理永不收敛（livelock）。同仓惯例是分批（SessionSummariesTrimmer 单批 ≤5000，spec_lifecycle.go:58；credential_probe_model_log 走 batch cleanup） | sql/migrations/startup/753_session_turn_logs_ttl.sql:73-74；bg/partition_manager.go:233（boot 调用）、518（5min 超时）；cmd/gateway/main.go:4699（24h tick） | DELETE 加 `LIMIT n` 循环分批（对齐 SessionSummariesTrimmer 惯例），或部署 runbook 增加"首启前手工预清/分批观察"；至少把 5min 超时与 24h 重试节奏的失配写进 OPS runbook |
| 2 | P3 | **spec DescriptionLong 描述的是初稿语义**：①"删除 expires_at 早于「当前时刻 − 本值」的行"= 被批判式审计证伪的 2×TTL 谓词（终稿是纯到期 `expires_at < NOW()`）；②"函数内对入参做 GREATEST(...,1) 与 NULL→24 兜底"= 终稿已改为 RAISE EXCEPTION fail-closed，无 GREATEST、无 NULL→24。运维在管理界面读到与实现相悖的语义 | settings/spec_lifecycle.go:50（对照 sql/migrations/startup/753_session_turn_logs_ttl.sql:61-74） | 更正 DescriptionLong 两处 |
| 3 | P3 | **partition_manager 注释与同文件修正叙述自相矛盾**：461-464 行仍写"430 schema 写死…清理阈值同样硬编码…默认 24h —— 与原行为逐字节一致"，而 493-505 行（修正后叙述）明确 430 清理"全仓无任何调用方"、部署是"第一次真正接上清理"——即上线**恰恰改变**行为（首次大删除）。"逐字节一致"是审计已否决的说法残留 | bg/partition_manager.go:461-464 vs 493-505 | 注释更正，指向 490 行起的修正叙述 |
| 4 | P3 | **revision-sequence 通道 753 登记注释漂移**："把 430 schema 里 24h 硬编码的 expires_at 清理阈值改为按入参可调"+"并补 idx_session_turn_logs_expires_at 兜底清理路径"——两项均初稿语义（终稿：入参仅联锁不参与谓词；**未建任何索引**） | scripts/apply-db-revision-sequence.sh:629-632 | 注释更正 |
| 5 | P3 | **dbinit runner.go 753 登记注释漂移**："入参 NULL→24、下限 GREATEST(...,1) 防误配清空整表；幂等（OR REPLACE + pg_proc 短路）"——NULL→24/GREATEST 为初稿；且终稿**无 pg_proc 短路**（幂等靠 OR REPLACE 本身） | installer/internal/dbinit/runner.go:306-311 | 注释更正 |
| 6 | P3 | **migration_753_test.go 头注 C1–C6 契约叙述是初稿的**："pins an expires_at index alongside…"（C2 前文）、"GREATEST(...,1) floor"（C3）、"drops exactly the function and **the index**"（C4）、"CREATE OR REPLACE FUNCTION + **CREATE INDEX IF NOT EXISTS**"（C5）。断言体本身已按终稿更新且正确，但头注作为"契约文档"与断言相反，后来者按头注理解契约必被误导 | sql/migrations/startup/migration_753_test.go:9-31 | 头注重写为终稿契约（与 R53"钉桩自身叙述也是被钉语义"纪律对齐） |
| 7 | P3/承接 | **753 终稿（14d34867f 批判式审计改写后）无存量真库实跑记录，迁移纪律#1 未满足**：docs/db-changelog.md 无 753 行（752 也仅 pending deploy），仓内无任何"753 实跑"证据。R71-L3 已登记"752/753 在 245/252 的部署验证"承接，窗口末仍未清 | docs/db-changelog.md:721-728（751 applied、752 pending、753 缺席）；docs/audit/2026-09-27-r71-24h-audit-round.md L3 | 部署前在 245/252 实跑终稿后补登台账；与 #1 首扫风险同窗处置 |

## 二、核实为健康的面

- **753 无时区钉扎需求**：函数体仅 `NOW()`（timestamptz 比较，时区无关）+ int 入参，无 `current_date`/`date_trunc`/上海日历派生——D07 清单#5 无缺口；`public.session_turn_logs`/`public.schema_migrations` 均 schema 限定（search_path 安全）。753 与 752 的差异是**本质的**（752 有日历边界所以必须钉扎），非遗漏。
- **752（同窗口对照族）正确复用 R69 全套教训**：函数级 `SET timezone = 'Asia/Shanghai'`（752:68，proconfig 先于 DECLARE 初始器）、上海日历派生 `(now() AT TIME ZONE 'Asia/Shanghai')::date`（752:70）、无 current_date、advisory lock + 双检幂等短路（752:82-105）、move-then-attach 含 DEFAULT 行搬移（752:109-130）；文件末自建 SELECT（752:136）经函数 proconfig 钉扎，单文件自洽——R69 C9 陷阱已规避。
- **753 幂等与双环境兼容**：CREATE OR REPLACE + 台账 upsert 带 `to_regclass` 守卫（TEST_PG_URL 裸库可执行）；`ON CONFLICT (version) DO UPDATE`。
- **down 可重入且对称**：`DROP FUNCTION IF EXISTS` + 守卫删除台账行，BEGIN/COMMIT 原子；终稿无索引故 down 正确不 DROP INDEX（并留了初稿残留清理指引）；"回退即回到无清理状态"语义在头注留档。
- **三层钳取一致 [1,168]**：SQL RAISE EXCEPTION（753:64-68）↔ bg `clampSessionTurnLogsTTLHours`（bg/partition_manager.go:480-488，`TestClampSessionTurnLogsTTLHours_NeverZero` 钉 0 值不可达）↔ writer `sessionTurnLogsTTL`（turn_logs_writer.go:64-73）。写侧烘焙 expires_at 单一真相 + 清侧纯到期谓词，语义自洽。
- **契约测试实跑绿（本机亲跑）**：`go test ./sql/migrations/startup/ -run TestMigration753` ok；断言含 C2b（writer 24h 硬编码移除——真正"接线生效"的钉）、禁 `NOW()-MAKE_INTERVAL`、禁重复索引、C6 sequence 顺序。
- **753 四点 + parity 登记全齐**：sequence 脚本：638 ✓；installer StartupFiles（main.go:556-557 go:embed var + 557 map 经 stats_migrations_test.go:234-238）✓；embeddata 与 sql/ 原件 `diff` 字节一致 ✓；dbinit runner.go:309 ✓。**`TestStartupFilesAreAllEmbedded` + `TestCanonicalStartupMigrationsAtOrAbove704AreRegistered` 本机实跑绿**——R71-F1 对 753 的收口已验证。
- **写路径唯一（D07 清单#1）**：session_turn_logs 全仓唯一 INSERT = turn_logs_writer.go:91；聚合器 flush 删除（cmd/gateway/turn_logs_aggregator.go:146）为设计内 merge-on-flush。该表为 430 所建普通 heap 表（非分区族），**不进 promoteSpecs/archiveSpecs 是正确形态**——753 族没有 mock_probe_history 那种"日分区无 drop 清单"病（行级 TTL 即其清单）。
- **spec 注册链活**：`LifecycleSpecs()` 经 settings/specs.go:22 聚合，HotReload: true；`bg` 与 writer 每 tick/每写重读，热重载语义成立。
- **下一可用迁移号 = 754**：origin/main `sql/migrations/startup/` 止于 753（750/751/752/753），无并行占用。

## 三、未覆盖项与原因

- **bg 包两单测（clamp 边界 / nil-pool no-op）未能本机验证** —— `go build ./bg` 在 Windows 宿主失败（bg/storage_retention_worker.go:279 `syscall.Statfs` 仅 Linux；窗口外既有平台限制），需 Linux/CI 侧跑 `-run "TestClampSessionTurnLogsTTLHours|TestCleanupSessionTurnLogsByTTL"`。
- **753 真库行为级验证**（245/252 部署实跑、首扫积压量实测）——需真库凭据，属 R71-L3 承接；与发现 #1 的首扫风险应在同一次实跑中量化（`SELECT count(*) FROM session_turn_logs WHERE expires_at < now()` 预估首扫规模）。
- **752 重放前 252 存量分区边界核对（036 手工期 UTC 错位残留）** —— R71-L3 承接，需真库查询 `pg_get_expr(relpartbound)`。
- **mock_probe_history TTL/drop 清单补齐**（R71-D1 遗留）——本轮窗口内无改动，仍开放；owner 拍板保留期后应在 `archiveSpecs`/dropOldStatePartitions 机制内补清单，本轮仅复核其仍缺（bg/partition_manager.go archiveSpecs() 1057-1063 仅 3 条目）。

**给主代理的一句话摘要**：753 迁移本体、down、四点登记、契约测试均为健康且实跑验证过；唯一实质候选缺陷是 P2 的"首扫无界 DELETE × boot 同步执行 × 5min 超时 × 24h 重试"组合在生产大积压库上的收敛风险；另有 5 处初稿语义的注释/文案漂移（spec DescriptionLong、partition_manager 注释、sequence 注释、runner.go 注释、测试头注）与 1 项迁移纪律#1 承接（753 终稿真库实跑，R71-L3 仍未清）。
