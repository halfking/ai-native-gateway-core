-- 760: analysis_events / stats_event_inbox 终态 TTL 删除的支撑索引（R27-HC-1/HC-2）
--
-- 背景（2026-09-30 二十七轮 hot+columnar 逐表盘点，真库实测）：
--   · analysis_events 单表 102 万行 / 864MB，pg_poll 消费后仅置 processed_at，
--     全仓零 DELETE —— 无界增长；全部行已处理（processed_at 非空）。
--   · stats_event_inbox default 分区 138 万行，仅状态流转无删除；
--     其中 processed 23 万行、pending 119 万行（积压另行登记，不动）。
--
-- 本迁移只加两个部分索引，让 partition_manager 的分批 TTL DELETE
-- （bg/partition_manager.go cleanupOldAnalysisEvents /
-- cleanupOldStatsEventInboxTerminal，lifecycle.*_ttl_days 默认 7 天）
-- 走索引下探而非每批全表顺序扫描：
--   · idx_analysis_events_processed_old：只覆盖已处理行（未处理行是
--     待消费工作，永不删除）；
--   · idx_stats_event_inbox_terminal_old：只覆盖终态行（processed /
--     dead_letter；pending / retryable / processing 是活跃队列，永不删除）。
-- stats_event_inbox 是分区父表，父表建索引会级联到现有及未来分区。

CREATE INDEX IF NOT EXISTS idx_analysis_events_processed_old
    ON analysis_events (occurred_at)
    WHERE processed_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_stats_event_inbox_terminal_old
    ON stats_event_inbox (occurred_at)
    WHERE processing_status IN ('processed', 'dead_letter');
