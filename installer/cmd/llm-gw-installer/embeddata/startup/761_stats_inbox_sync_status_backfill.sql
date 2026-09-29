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
-- 真未处理行（processed_at IS NULL）不受影响。一次性修复，接受单语句 WAL 峰值
-- （约 1.2M 行 × 状态列，远小于常规月分区搬运量）。

UPDATE stats_event_inbox
SET processing_status = 'processed'
WHERE processed_at IS NOT NULL
  AND processing_status = 'pending';
