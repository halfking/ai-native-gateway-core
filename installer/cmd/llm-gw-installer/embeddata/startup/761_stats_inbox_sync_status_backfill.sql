-- 761: stats_event_inbox 同步投影行 processing_status 回填（R28-HC-10）
--
-- 根因（2026-09-30 二十八轮诊断，本地真库实证）：
--   EventWriter.persist 同步投影分支（LLM_GATEWAY_STATS_INBOX_CONSUMER 未开启时）
--   的 UPDATE 只设 processed_at/processing_owner，漏翻 processing_status——
--   行保持 'pending' 但实际已投影落 usage_facts。本地库实测 1,195,246 行
--   「pending」中 processed_at 全部非空（真未处理仅 4 行）。
--
-- 后果：
--   · idx_stats_event_inbox_claimable 部分索引持续膨胀（119 万行），每次
--     写入都在维护一个永远不该存在的热索引；
--   · 监控把积压误读为 119 万待处理；
--   · 一旦启用异步消费者（flag=1），claim 扫描会把这 119 万已处理行当
--     待处理整批重放（幂等投影也扛不住这种风暴）。
--
-- 修复：代码侧（domains/stats/event_writer.go）UPDATE 已补 processing_status
-- = 'processed'；本迁移一次性回填历史行。谓词只命中 processed_at 非空的行，
-- 真未处理行（processed_at IS NULL）不受影响。
--
-- 超时预算（本迁移修复的核心，2026-09-30 十五时四十轮 deploy-245 实证）
--   首版把这条 UPDATE 当成「可接受单语句 WAL 峰值」的一次性修复，没有抬高
--   statement_timeout。deploy-245.sh 在 [6.5/9] 切换前 pending 迁移阶段实测
--   失败：
--     → 761_stats_inbox_sync_status_backfill.sql:24:
--       ERROR:  canceling statement due to statement timeout
--   245 与 154 共享 252 PostgreSQL，`SHOW statement_timeout` = 30s
--   （该集群对网关运行时是全局默认值，见 649/689/750 头注同型记录）。
--
--   规模与耗时（245 真库，2026-09-30 实测，事务内执行后 ROLLBACK）：
--     stats_event_inbox 全表 329,005 行，全部命中待回填谓词
--     （to_fix=329,005 / truly_pending=0 / total=329,005），
--     单分区 stats_event_inbox_default，无并发网关写入干扰：
--       UPDATE 329006  时间：18928.235 ms (18.9s)
--   154 是同一套 252 库、同一量级（329,024 行），一次测量覆盖两端。
--
--   18.9s 对 30s 只有 1.6 倍余量：迁移期间网关仍在写、ensure 链与索引维护
--   叠加，共享库争用下必然越界——实测即如此。**余量不足才是本次失败的
--   真正原因，不是「回填太慢」。**
--
--   修法与 649/689/632 同一惯例：`SET LOCAL statement_timeout`，只对本事务
--   生效、不污染集群全局，也不留下「回填中途超时→事务回滚→部署中止后
--   重跑再超时」的活锁。对实测 18.9s 给出约 20 倍余量，覆盖共享库争用波动。
--
--   为什么仍保持单语句而不是分批：18.9s 里绝大部分是两个部分索引的维护
--   （从 idx_stats_event_inbox_claimable 删除、从 idx_stats_event_inbox_
--   terminal_old 插入各 32.9 万条），总量与批次数无关；而分批若放进单个
--   DO 块，statement_timeout 仍作用于整块、并不缩短超时预算，得不到任何
--   实质收益（要真正分批必须逐批提交，那会破坏本迁移「要么全回填要么不动」
--   的原子性）。抬超时是此处唯一既有效又不牺牲原子性的做法。
--
-- 锁
--   单条 UPDATE 持有 32.9 万行的行锁约 19s。stats_event_inbox 是统计投影
--   收件箱，不在请求热路径上；网关对该表的写入面仅为 append 与同步投影
--   UPDATE，故 19s 锁窗不影响请求转发。
--
-- 幂等
--   谓词 processing_status = 'pending' 随每次运行自然收敛；已回填行不再命中，
--   重跑为 0 行 UPDATE。
--
-- 降级：见 761_stats_inbox_sync_status_backfill.down.sql（回填不可逆）。

BEGIN;

-- 252 共享 PG 默认 statement_timeout=30s；本事务的回填实测 18.9s，
-- 无余量可言。与 632/649/689 同一惯例，仅对本事务抬高。
SET LOCAL statement_timeout = '10min';

UPDATE stats_event_inbox
SET processing_status = 'processed'
WHERE processed_at IS NOT NULL
  AND processing_status = 'pending';

COMMIT;
