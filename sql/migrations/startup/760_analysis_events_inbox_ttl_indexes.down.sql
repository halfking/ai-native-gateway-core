-- 760 down: 删除 TTL 清扫支撑索引。清扫函数在索引缺失时仍可运行
-- （仅退化为顺序扫描），回滚本迁移不破坏数据契约。

DROP INDEX IF EXISTS idx_stats_event_inbox_terminal_old;
DROP INDEX IF EXISTS idx_analysis_events_processed_old;
