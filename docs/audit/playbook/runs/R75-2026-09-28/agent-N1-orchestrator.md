# D06+D07 N1 深审 —— 主代理替补执行报告（窗口：`24c5c545a..3a750b3a7`）

> R75 留档注：Explore 型子代理派发通道在轮内连续 5 次 captcha 超时（user concurrency
> limit / Captcha instance timed out），按预案由主代理亲自执行 D06+D07 域深审（N1 承接项
> 为本轮核心，不可空转）。本文件为该替补执行的完整记录，效力等同域子代理报告（线索
> 均经亲读 file:line 坐实，与子代理报告的区别仅在执行者）。

## 一、发现（均已亲读复核）

| # | 级别 | 发现 | 证据 file:line | 处置 |
|---|---|---|---|---|
| N1-A | **P1** | **754 归档接线失效（24h 定相 ticker + hour==3 日闸 = 永不运行）**。调用链：`archiveOldPartitionsIfNeeded` 只在启动时与 `pm.interval=24h` ticker（main.go:4699 `NewPartitionManager(dbConn.Pool(), 24*time.Hour)`）上执行；`archiveOldRequestLogs` 首行 `if !shouldRunRequestLogsArchive(time.Now()) { return }`，而 `shouldRunRequestLogsArchive` = `now.Hour() == 3`（partition_manager.go:509-511）。24h ticker 触发时刻恒等于进程启动钟点——除非网关恰在 03:00-03:59 启动，`Hour()==3` 永不成立，754 归档流水线**静默零执行**。cadence 测试注释自证设计意图是 1h cleanup ticker（「the cleanup ticker fires at an arbitrary minute offset」）；migration_754_test.go C7 注释同样自证「runCleanup step 12」；982e3191c 提交信息自称「接在 runCleanup step 12」但实际落在 archiveOldPartitionsIfNeeded 第 12 项（R72 F7 runCleanup/24h 混淆同型）；spec_lifecycle.go DescriptionLong「每日 03:00 本地时区扫一次」为失真承诺。**生产实证：245 8781 canary（build 2287）当前 13:19 启动，归档在本实例上永不运行。** | bg/partition_manager.go:546-550（调用点+日闸）, 57(1h 常量未被此路径使用), main.go:4699；cadence_test.go:43（「cleanup ticker」注释） | **已被并行十六轮（3909d56e0 P1）根修**：调用点移入 runCleanup 1h 循环 + A-1 接线守卫。R75 复核确认修复在位（runCleanup:314） |
| N1-B | **P2** | **754 调用方无 statement_timeout 抬升——大积压首跑必被 252 角色级 30s 击杀并活锁**。`archive_request_logs_default` 是 RETURNS TABLE 集合函数：**整个调用是一条驱动层语句**，函数内 1000 行批游标不稀释 statement_timeout；调用方 `pm.db.Query(timeoutCtx, ...)` 裸执行，Go 侧 30min ctx 不覆盖服务端 GUC。252 生产 `llm_gateway` rolconfig statement_timeout=30s（1368-1376 注释先例），首批 >30s 即 kill+整批回滚，次日 03:xx 重试同一批 = 活锁（与 R72 F2 对 753 首扫的诊断同型）。同文件两个先例均有 SET LOCAL：promote 60s（:1376）、analyze 10min（:1524），754 缺位。**754 已于 09-28 02:43 在 252 真库应用**（本轮 L3 核查坐实），一旦 03:xx tick 命中大积压即触雷——但 request_logs_archive 表尚未生成说明 02:43 应用后 03:00 tick 未执行（部署在 02:43 后重启、或当时积压小未超 30s，无从分辨，修复优先）。 | bg/partition_manager.go:557-559（裸 Query）；先例 :1376, :1524 | **R75 收口**：事务化（Begin → SET LOCAL statement_timeout='30min' → Query → rows.Close() 后统一 Rollback/Commit，规避 pgx rows 存活期连接忙错）+ 接线钉桩 TestArchiveOldRequestLogs_StatementTimeoutPinnedInsideTx（Begin<SET LOCAL<call<Commit 顺序断言）+ 754 头注勘误（十六轮遗留的「Go ctx 预算兜底」错误断言改为指向调用方 SET LOCAL）+ embeddata 字节同步 |
| N1-C | P3 | **754 批终止条件注释与代码相反**：注释「两条件同时成立才退出」，代码是两条独立 `EXIT WHEN`（任一即退）；且第二条 `(new_last_id - last_id) < batch_size` 在空源时同样命中（0-last_id<1000），第一条实为冗余保险。防死循环的机制是「任一即退」而非「同时成立」。 | 754 SQL:258-262（修订前行号） | R75 订正注释（可执行语句零变更）+ embeddata 同步 |
| N1-D | P3 | spec_lifecycle.go 新键 DescriptionLong 两处同源漂移：「bg.PartitionManager.runCleanup 周期调用」（修复后为真，十六轮修复前为假）与「每日 03:00 本地时区扫一次」（修复后为真）。随 N1-A 修复自动归真，无需单独改动。 | settings/spec_lifecycle.go | 无需处置（复核确认） |

## 二、核实为健康的面

- **754 迁移本体**（在 §24 本地 PG 契约实测基础上增量复核）：幂等三重（防御性 DROP FUNCTION IF EXISTS + CREATE OR REPLACE + pg_class relnamespace 锚定建表守卫 + ON CONFLICT DO NOTHING）；RAISE [7,365] 与 spec Min/Max、bg clampRequestLogsArchiveDays 三方一致；`pg_advisory_xact_lock(hashtext('archive_request_logs_default:'||yyyy-mm))` 按月串行化多实例；无 DELETE/TRUNCATE（TestMigration754_NeverDeletesFromSource 剥注释钉桩）；down 只删函数不删归档表（owner 资产，R68 纪律）；分区正则 `^request_logs_[0-9]{4}_[0-9]{2}$` 足够严密剔除 DEFAULT/bodies 同族；无时区钉扎需求（纯 month_end date 比较，无 current_date）。**已知未修**（登记）：无 archive ledger → 每日全量重扫 O(过期行)（daily-gate 缓解，ledger 为后续项）；归档月表无 RLS（R73 P2-10 登记不修：零 Go 读路径 + SQL 已应用不可改）。
- **turn_logs_writer 批量写**：WriteStages 单语句多行 INSERT 原子可见（正是 R72 F1 jsonb `||` 浅合并丢 stage 的写侧根治）；TTL 写时烘焙（sessionTurnLogsTTL 与 spec Min1/Max168、753 SQL RAISE、bg clamp 四方一致）；`string(eventDataJSON)` + `$7::text::jsonb` 避开 SimpleProtocol bytea hex 坑（doc §3.2）；latencyMs 负值钳 0；批共享单一 expires_at（聚合器不会观察半过期 turn）。已知取舍：turn 级 all-or-nothing（单坏 payload 拖垮同批，D04#3 登记接受）。
- **753 有界 DELETE 未被窗口后续提交破坏**：`git diff 7230ea1b3..HEAD -- 753_session_turn_logs_ttl.sql` 为空；bg 调用方 drain 循环 + sessionTurnLogsTTLCleanupBatchSize=10000 + MaxBatches=600 在位。
- **installer 五点**：753/754 canonical ↔ embeddata 字节一致（cmp 实测）；三守卫 + Stats 对账全绿（754 注释勘误后复跑仍绿）。
- **storage_degraded 503**（Subtask 4）：分类器保守（ConnectError/网络超时/DeadlineExceeded/ErrNilDatabasePool → 503；Canceled 显式排除、PgError 留 500；R73 P2-6 后 08xxx/53xxx/57P03 白名单加入）；响应体零 err 回显（真实错误仅 slog %T+err）；metrics 对称 Inc/defer Dec（pending gauge 无虚增路径）；component 三档低基数常量。
- **cache_trimmer retention 对齐**（Subtask 6）：resolveCacheTrimRetention 取 cacheTTL 安全上界 + 收敛告警；CacheTrimmer.Retention() 读真实 worker 字段（非快照自证）；config 注释与 example 三点同步。
- **storage_mode_init**：lite 模式装配面窗口内无 lite 不变量破坏（聚合器/V2 hook/partition manager 不启动形态未触碰）。
- **§24 真库实测残留声明的准确性**：「真实 Citus 分布式 partition 行为 + archiveOldRequestLogs 跨实例 advisory lock 集成本机无法覆盖」——核实为真（252 为共享 PG；754 归档表族在真库上首次生成的行为仍待部署修复后的 03:xx tick 实捕）。

## 三、未覆盖项与原因

- **754 首跑实捕**：修复（N1-A 接线 + N1-B 超时抬升）需部署到 245/252 后首个 03:00-03:59 窗口验证 `request_logs_archive_YYYY_MM` 首次生成 + rows_archived 日志——代码侧无法替代，登记下轮部署验证。
- **752 重放前分区边界复核**：已在 L3 核查完成（+08 全对齐），见轮文档。
- **archive ledger / mock_probe_history TTL（L1）**：owner 拍板项，维持挂账。
