# D04/D06 子代理报告（窗口：949ec2f70..24c5c545a，origin/main HEAD）

负责改动面：8a34eab76（FIFO poll + merge-on-flush + delete-by-id）、5b558deab（TTL 设置化 + 迁移 753）、14d34867f（TTL 语义修正）；对象文件 `cmd/gateway/turn_logs_aggregator.go`、`turn_logs_aggregator_flush_test.go`、`turn_logs_aggregator_poll_test.go`、`cmd/gateway/main.go`（:1279-1313、:4699）、`domains/session/v2/turn_logs_writer.go`（+ttl 测试）、`settings/spec_lifecycle.go`、`bg/partition_manager.go`、`sql/migrations/startup/753_*`。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P2** | **merge 是 per-turn_N 键整体替换，跨 flush 边界的轮次静默丢早期 stage**。jsonb `\|\|` 只做顶层键浅合并；`turn_5` 键一旦已存在，新值整体替换其 `stages` 数组。触发路径：①单实例——turn N 的 stage 行是在 turn+bodies 事务提交后的一个串行循环里逐条 INSERT（session_writer_v2.go:707-727），5 分钟 tick 恰落在该循环内时，tick1 聚合并删除 turn N 的前半 stage 行（summary 写入 `turn_5={stages:[前半]}`），tick2 只读到后半行，`turn_5` 被替换为 `{stages:[后半]}`，前半从此无处可寻；②双实例——两实例按全序 poll 出**同一批** session，A 的 SELECT 在 B 写入新行前发生，B merge 超集并按 id 删除后，A 的**陈旧子集** merge 后到，把该 turn 键覆盖回子集，行已删，永久丢失。commit 内收敛声明"worst case a turn is written twice with an equivalent value"仅在读集相同时成立，读集不同（差一行）即不成立。flush 循环还确认无 `stage_status` 过滤（in-flight 的 pending/running 行照常聚合删除） | cmd\gateway\turn_logs_aggregator.go:138-141（`COALESCE(...) \|\| $1`）、:171-174（收敛注释与事实不符）、:239、:249；触发源 domains\session\v2\session_writer_v2.go:707-727 | 修法候选：merge 降到 stages 数组级（SQL 侧 `\|\|` 改为对 `turn_N.stages` 的 jsonb 自定义合并/`jsonb_set` 追加），或 flush SELECT 排除未终态（`stage_status IN ('success','failed','skipped')` 且轮内无 in-flight 行）的轮，或按 completed_at 水位聚合。修复后把 :171-174 注释一并改真 |
| 2 | **P3** | **双实例无 leader 选举，poll 全序使 N 实例重复消费同一前 100**：无工作分区，每实例每 tick 都对同一批 session 各做一次 SELECT+merge+DELETE（内容幂等、DB 收敛成立，D04 清单#5"二选一+注明"形式满足——选了"DB 幂等收敛"且代码注明），但吞吐不随实例扩展，且每实例重复 5 分钟一次全未过期集 GROUP BY；同时双实例把发现 1 的子集覆盖窗口从"tick 落在写循环内"放大到"两实例 SELECT 间隔内" | cmd\gateway\main.go:1286-1311（goroutine 无 DistLock，对照同文件 :5124/:5310 的 R31 token-bucket 惯例）；cmd\gateway\turn_logs_aggregator.go:104-124 | 可接受现状（幂等收敛已注明）；若接 DistLock 按惯例走 `distlock.NewRedisManager(fpSlotRedis)`；或 poll 加实例偏移（OFFSET by instance hash）做廉价分区 |
| 3 | **P3** | **settings spec 的 DescriptionLong 仍是 14d34867f 证伪前的初版语义**：正文称"删除 expires_at 早于「当前时刻 − 本值」的行"（F-1 修正后谓词是纯 `expires_at < NOW()`，p_ttl_hours 不参与谓词）、"函数内对入参做 GREATEST(...,1) 与 NULL→24 兜底"（实际最终版是越界 RAISE EXCEPTION 的 fail-closed 联锁，**NULL 也 RAISE**，与迁移内 COMMENT 直接矛盾）。这是管理员可见文案，会误导对联动机制的判断 | settings\spec_lifecycle.go:50 ↔ sql\migrations\startup\753_session_turn_logs_ttl.sql:61-74 | 修正 DescriptionLong 为最终语义（写入时烘焙 + 到期即删 + [1,168] RAISE 联锁） |
| 4 | **P3** | **revision-sequence 登记注释仍是初版事实**："按入参可调（默认仍 24h，不破既有行为）"（F-1/F-2 证伪：入参不调阈值、"既有行为"本不存在）并称"补 idx_session_turn_logs_expires_at 兜底清理路径"——该重复索引已在 14d34867f F-4 移除，最终迁移无此索引 | scripts\apply-db-revision-sequence.sh:628-636 | 注释改为最终语义，防下一轮以文件为准误判（R43 教训：不以字面量为准） |
| 5 | **P3** | **清扫调用点被一致地误名为 runCleanup，实际节奏是 24h 主 tick**：`cleanupSessionTurnLogsByTTL` 唯一调用点是 `archiveOldPartitionsIfNeeded` 第 11 项，跑在 `run()` 的 `pm.interval`（main.go:4699 传 24*time.Hour，启动即跑一次）上；而真正的 `runCleanup()`（partition_manager.go:290）是 1h `providerErrorCleanupInterval` 的另一 goroutine，只含 providerError 等 4 项。spec、迁移 COMMENT、commit message 均写"runCleanup 第 11 项"，运维按文档推算的清扫延迟差 24 倍（过期行最多再滞留 ~24h 才物理删除；期间 poll/flush 已不可见，但 `GetStageLogs` 不过滤 expires_at，admin 读路径在过期后仍可读最多 ~24h）。migration_753_test.go:210 的"runCleanup 接线"断言只是 grep 函数名，抓不住该误名 | bg\partition_manager.go:465（真实调用点）、:226-237 与 :290-292（两条不同节奏的 goroutine）、:57（1h 常量）；cmd\gateway\main.go:4699（24h interval）；settings\spec_lifecycle.go:47-49、753 SQL:94 | 改注释为"archiveOldPartitionsIfNeeded（pm.interval=24h tick）"；顺手确认 24h 节奏是否为本意（同为 item 5 bodies 分区的"同一节奏"，大概率是有意的，属命名漂移而非行为错） |
| 6 | **P3** | **积压零观测 + poll 成本随未过期行数线性增长**：吞吐上限 100 session/5min=1200/h（代码注释自认 28800 session headroom），到达率超阈值时积压只以"最老行到期被清"方式静默 shedding（恰是 F-7 的失效形态回归为过载丢数），全程无 error、无 gauge/metric（对照 R47 为 promote 加的 `llm_gateway_hot_table_backlog_rows` 惯例）。poll 查询仅有 `idx_session_turn_logs_expires` 可依托，每 5 分钟对全量未过期行 GROUP BY+MIN 排序，无 (expires_at,tenant_id,session_id,started_at) 覆盖索引。另注：单个持续 flush 失败的 session 会常驻队头占用 1/100 配额（靠 TTL 自愈） | cmd\gateway\turn_logs_aggregator.go:52-59、:97-103；cmd\gateway\main.go:1299 | 加 pending-session 数 gauge + 最老 pending 行龄 gauge；覆盖索引属可选优化，先测后加 |

内存背压问题（任务重点 2）结论：**无无限内存增长**——poll 每 tick LIMIT 100（且 :63-68 归一化防 LIMIT 0/负数 SQL 错），flush 串行、失败仅 WARN 继续；无界增长只发生在 DB 侧积压并被 TTL 截断（见发现 6），不涉进程内存。

## 二、核实为健康的面

- **FIFO 保序（任务重点 1/2）**：`ORDER BY MIN(started_at) ASC, tenant_id ASC, session_id ASC` 构成全序；行在 flush 后即删，被继续写入的 session 其新行 started_at 更新、排到队尾，单实例下无饥饿；flush 后删除使候选集收敛为 FIFO。证据：turn_logs_aggregator.go:52-59、:91-95；变异钉桩 turn_logs_aggregator_poll_test.go:32-56（字符串断言，逻辑上删 ORDER BY 必红）。
- **delete-by-id 与读集一致 + 失败路径重试安全**：ids 只来自同函数内 tenant/session/expires 三重过滤的 SELECT（:130-135、:183-197）；merge Exec 失败则不 DELETE（:239-241 先于 :249），进程死在 UPDATE 与 DELETE 之间时下轮重聚合同 id 集、按同键覆盖等值内容，单实例幂等；空读集分支无 DELETE（:205-207），死代码清理正确。
- **COALESCE 首写 NULL 防护**：`COALESCE(turn_logs_summary,'{}') || $1`，`NULL || x`=NULL 的坑已封（:140；测试 ：37-39）。
- **RLS 交互**：session_turn_logs **无 RLS**（430 只对 sessions/session_turns/session_bodies ENABLE，430_sessions_v2_schema.sql:83/171/226），id-only DELETE 无策略交互；且 id 出处即同租户 SELECT，无跨租户面。
- **TTL 语义一致性（任务重点 3）**：唯一活 TTL 在写入侧 bake（turn_logs_writer.go:64-73、:106），Go 夹取 [1,168] == SQL 联锁 [1,168] == spec Min/Max [1,168]，三处对齐并有双向测试（turn_logs_writer_ttl_test.go:38-44、partition_manager_session_turn_logs_ttl_test.go）；poll 谓词 `expires_at > NOW()` 与清扫谓词 `expires_at < NOW()` 互补无缝隙；completed/failed 轮无独立 TTL 起点——TTL 按每行写入时刻起算（设计固有，注释已写明运维需知）；改设置仅影响新行，旧行按原 expires_at 清扫；每次 tick 重读设置，热重载成立。
- **TTL 清扫多实例幂等**：`cleanup_session_turn_logs_by_ttl` 是单语句 DELETE，N 实例并发互为 no-op；Go 侧先夹取后传参，RAISE 联锁对活调用方不可达；db==nil no-op 有测试。
- **D06 开关收口**：lite（storageRt!=nil）→ 空 DB URL → dbConn nil（main.go:494-502、main_helpers.go:117），聚合器（main.go:1282）、V2 写 hook（main.go:2553）、partition manager 全部不启动；753 是纯 PG 函数，lite 无 session_turn_logs 等价物（v2 writer 依赖 pgxpool），能力诚实缺席，与 D06 清单#1/#2/#5 相符；窗口内未引入任何 lite 下误启动的调用（清单#4）。
- **753 五点同步**：canonical SQL、installer embeddata 副本（installer\cmd\llm-gw-installer\embeddata\startup\753_session_turn_logs_ttl.sql）、revision-sequence 登记+顺序断言（apply-db-revision-sequence.sh:636；migration_753_test.go:180-198）、partition_manager 接线断言（migration_753_test.go:205-215）齐备；installer embed 断裂已由窗口内 8b6fbcc10 收口。turn_logs_summary 全仓单写者即本聚合器（session_aggregator.go:332 是读），5b558deab"不双写"前提成立。

## 三、未覆盖项与原因

- **753/聚合器在真实 PostgreSQL 上的集成行为**——无真库凭据；commit message 自认"753 迁移未经真实 PostgreSQL 执行"，建议按迁移三纪律#1 在存量真库实跑后定稿。
- **双实例竞争的实机复现**（发现 1 场景②）——需真机双副本环境；本报告为代码推演。
- **测试文件自称的变异验证**（删 ORDER BY/删 merge/删 id-delete → FAIL）——我未重跑变异，仅逻辑复核断言必然命中；主代理如需可 5 分钟复现。
- **D04 清单 #1/#2/#3/#7**（Governor 链、队列满行为、权重 LB、retry budget）——窗口改动面不含任何出口调用点/队列入口，无新增面可查，未重审既有面。
- **生产库侧是否残留 cleanup_expired_session_turn_logs() 的 DB 级调度（pg_cron/外部 cron）**——repo 内确认无调用方，库侧调度无法从仓库取证。
