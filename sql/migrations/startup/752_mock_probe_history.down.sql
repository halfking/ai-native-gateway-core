-- 752 down: 回滚 mock_probe_history 历史表（连带分区与函数）。
-- 探测历史是可再生的观测数据（30s 一轮重建），DROP 不影响真实业务表。

DROP TABLE IF EXISTS mock_probe_history CASCADE;
DROP FUNCTION IF EXISTS mock_probe_history_daily_partition();
