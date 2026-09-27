# D07+D10 usage_facts 分区链 子代理报告（窗口：092ab1b61..908256008）

> 主代理复核结论（R69 收口时回填）：发现#1 实锤成立→本轮 F1 修复（750 函数级 SET 钉扎 + 上海日历预建）；发现#2/#3/#4/#5 记录不处置。健康面维持。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2（环境敏感，待复核） | 750 迁移文件的预建调用仍在 **751 钉扎生效之前** 执行，且用会话时区 `current_date`：双通道都是先跑 750 后跑 751，此窗口内函数未钉扎。若安装/升级通道会话时区为 UTC（installer compose 的 kx-citus 服务未设 TZ/PGTZ，镜像默认时区待实测），750:115-116 会以 UTC 日历预建当日/次日两个**边界错位 8h** 的日分区；此后 boot ensure（上海日历派生，db.go:1305-1311）与 tick（partitionTZ 派生，partition_manager.go:341-353）按分区**名**幂等短路（750:61-69），错位边界永久无法纠正；且预建窗口过后第一个上海日（其上海边界与 UTC 边界分区重叠）的 ensure ATTACH 必撞 overlap 失败（750:105-107，事务整体回滚），该日行永久滞留 DEFAULT（无 TTL）。触发路径：全新安装（installer dbinit → 750 → 751 → 网关首启）在 UTC 会话下必踩；252/245 存量（集群默认 +08）无实际影响 | sql/migrations/startup/750_usage_facts_daily_partition.sql:115-116；installer/internal/dbinit/runner.go:288→296（750 先于 751）、:357-373（applySQL 无 PGTZ）；installer/templates/compose.yml:23-37（citus 无 TZ）；scripts/apply-db-revision-sequence.sh:607→614；db/db.go:1305-1311 | 在 installer 容器实测 `SHOW timezone`；若镜像默认 UTC，趁 750「未部署消费」（db-changelog 无 750 行，1dfe88c08 已原地修订过一次）把 750:115-116 改为 db.go 同款 `(now() AT TIME ZONE 'Asia/Shanghai')::date` 派生并重验契约测试；否则降级为口径注记 |
| 2 | P2（待复核，需 252 实测） | tick 路径的每日首建分区要付 **ATTACH 对 DEFAULT 的约束校验全扫**，而该路径预算仅 30s（ensureNextMonthPartitions 每语句 ctx 30s；ensure 循环 24h 一拍）。DEFAULT 内压着全部 750 之前的 usage_facts 历史（无 TTL，R68 §四 P2 已登记），扫耗随历史线性增长；252 角色级 statement_timeout=30s 会把每日首个需 ATTACH 的 ensure 击杀 → 该日行落 DEFAULT → 下次成功要等 24h 后 tick 或下次重启（boot ensure 有 5min 迁移期钉扎，db.go:150-160）。分区建好后短路无此代价，但 DEFAULT TTL 落地前每个新日首建都重付扫描。触发路径：252 存量升级后首次跨日 tick（或任何 DEFAULT 大的库） | bg/partition_manager.go:355（30s ctx）、:237+:88（24h tick）、:359-364（失败仅记日志 continue）；sql/migrations/startup/750_usage_facts_daily_partition.sql:44-47（自认 ATTACH 校验扫描代价，但只给 boot 路径配了 5min）；db/db.go:150-160 | 用 252 scratch 对现状 DEFAULT 跑一次 `EXPLAIN ANALYZE SELECT * FROM usage_facts_default WHERE occurred_at >= ...` 量化扫描耗时；超 30s 则给 tick 的 usage_facts 条目单独抬预算或把 DEFAULT TTL 提前 |
| 3 | P3 | Go boot/migrate 通道对 750 是"只调用不自建"（有意偏离 749 的自建孪生惯例）：函数不存在的库上 `ALTER FUNCTION` 42883 直接 return err → ApplyMigrations 失败 → no-DB 模式。所有在档投递流程（installer dbinit / 升级 sequence 脚本）都先于网关启动跑文件通道，故当前无实际触发面；但"只换二进制不跑升级脚本"的部署偏离会炸 boot。触发路径：新二进制 + 未跑 apply-db-revision-sequence.sh 的旧库 | db/db.go:1292-1300（42883 即返错，注释自述"让 750 升级通道先走"）；对照 749 自建式 db/db.go:1202 | 维持现状可接受；可选：在 return err 前探测 42883 给出指向升级脚本的显式错误文案 |
| 4 | P3（记账提醒） | db-changelog 尚无 750/751 行（1dfe88c08 自述"未部署消费，可原地修订；下次部署登记新 SHA"）；本轮 f936779d1 只同步了 9 个旧迁移 SHA。若 750 因发现#1 再次原地修订，登记时须以最终盘上内容为准 | docs/db-changelog.md（尾部最新仅 745/746/747/800，无 750/751 行）；1dfe88c08 commit message | 下次部署登记时补 750/751 两行（SHA 按最终内容重算） |
| 5 | P3（口径注记） | D10 rollup 日桶是 **UTC 日**（rollup.go:143-147 显式注释、worker:115 UTC 截断；对账 reconciliation.go:287 `day_utc` 同口径，两者自洽），而 750 日分区边界是**上海日**——一个 UTC 日窗横跨 2 个上海日分区（pruning 仍有效，仅从 1 变 2），数据正确性无损；但未来若有仪表盘按"上海日"解读 report_snapshots.report_date 会错 8h。触发路径：消费 report_snapshots 的新读面 | domains/reportrollup/rollup.go:143-147、:705-719；bg/report_rollup_worker.go:113-117；domains/stats/reconciliation.go:287 | 在 DASHBOARD 设计文档/report API 注释里显式钉 UTC 日口径（D10 清单#4"显式钉扎"已满足一半，读侧缺注记） |

## 二、核实为健康的面

- **750 move-then-attach 函数体全链**：advisory xact 锁（hashtext 键名）→ 锁后复查 → `LIKE INCLUDING DEFAULTS INCLUDING INDEXES` 承接表 → AE 锁 DEFAULT → `DELETE...RETURNING` 搬界内行 → ATTACH，全步骤同一函数事务 —— 750:53-108；DELETE 谓词与 ATTACH 边界用同一对 start_ts/end_ts（750:96-107）；幂等双重短路（750:61-69、76-84）；有数据 DEFAULT 环境不再 boot 阻断，且有真库回归钉桩 DefaultOverlap（db/db_750_ensure_realdb_test.go:162-278）。
- **751 时区钉扎机制正确**：函数级 GUC 先于 DECLARE 初始化器（751:24-25）；down 仅 RESET（无 DROP）；boot 链每次先执行同一 ALTER 再调用（db.go:1292-1300）；boot 日期派生已消除 current_date 会话依赖（db.go:1306/1310，契约 P5 钉死 migration_751_test.go:119-124）。UTC 会话实证钉在 db/db_751_tz_pin_realdb_test.go:71-121。
- **系统级双源核验为同源一致**：tick 侧日期由 Go 的 `partitionTZ = FixedZone(+8*3600)`（partition_manager.go:33,341）派生并以字面日期串传参（:351-354），`'YYYY-MM-DD'::date` 文本转换不读会话时区；边界换算由函数级 GUC 兜住。两个日历源均为上海日历；tick 的 AddDate(0,0,offset) 在固定 +08 区无 DST 边缘。
- **双通道登记无撞号且顺序正确**：750/751 文件名全仓唯一；runner.go 顺序 748→750→751（:288/:296）；sequence 脚本 749→750→751→800（:600/:607/:614/:620）；749 有意不经 installer（CONCURRENTLY 容不下 --single-transaction）；远端 ls-remote main = 908256008 全等。
- **契约测试与迁移文件逐条对得上**：migration_750_test C1-C8 与 750 文件实文一致；migration_751_test P1-P5 与 751 文件及 db.go 实文一致。
- **realdb 测试 skip 门控正确**：无 TEST_DATABASE_URL/TEST_DB_URL 时 t.Skip（db_750:28-34/163-175、db_751:30-43）。
- **ensureRoutingRecentSuccessRate proargtypes 修复正确**：`p.proargtypes = '20 25 23 23'::oidvector`（db.go:5665-5674）与 4 参签名匹配；修复后命中签名即跳过 DROP。
- **D07 写路径不变量（usage_facts 族）**：全仓生产代码对 usage_facts 仅一处 INSERT（domains/stats/inbox_consumer.go:507，ON CONFLICT 幂等 :516）；无生产 UPDATE/DELETE；537 分区均为 heap（无 columnar USING），"columnar 母表不可 UPDATE/DELETE"不变量对本族不适用；usage_facts 无 _hot 孪生、不在 promoteSpecs/archiveSpecs（partition_manager_test.go:77-99/:107-127 双覆盖钉死清单），直写父表是 537 起既定形态、非本轮回归。
- **D10 聚合吃到 749 索引 + 750 裁剪**：rollup 五查询与对账全是纯 occurred_at 范围条件；防双计靠 ON CONFLICT 四键 upsert（rollup.go:608）+ MissingRollupDates 有界回追；单日独立事务、panic recover（worker:105-112）。
- **窗口内 D10 相邻修复健康**：splitInternalPersonScopeKey 越界/负长 panic 守卫 + 7 案钉桩（96a6629c2，rollup.go:41-58）；rollup worker 超时注释与 749 索引实态同步（67ce8dc16）。

## 三、未覆盖项与原因

- 252/245 真库实测（DEFAULT 行数、ATTACH 校验扫描耗时、SHOW timezone）——无库凭据。
- go test / go vet 三门复跑——只读审计不执行构建。
- 窗口内 admin session identity 系列提交——非 D07/D10 边界。
- 远端全部引用枚举——仅 ls-remote main 与 HEAD 全等。
