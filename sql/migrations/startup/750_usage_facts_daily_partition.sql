-- 750: usage_facts 按日分区函数 + 当日/次日预建（R68 24h 审计轮，2026-09-26）
--
-- 背景：usage_facts 是 PARTITION BY RANGE (occurred_at) 的分区父表（537），
-- 但仅 DEFAULT 单分区。749（R67）仅补 occurred_at 前导索引；rollup
-- 五查询（domains/reportrollup/rollup.go providerModelDaySQL/
-- totalDaySQL/internalTenantDaySQL/internalModelDaySQL/
-- internalPersonDaySQL）+ stats 对账（domains/stats/reconciliation.go）
-- 全是纯 occurred_at 范围条件——无按日分区函数时 partition pruning
-- 不可用，新一日数据仍写 DEFAULT 分区，索引扫描退化为 DEFAULT 全表。
-- 共享 PG（252）30s statement_timeout 必杀成批 rollup/对账。
--
-- 修法：ensure_usage_facts_daily_partition(p_date DATE) 函数，幂等建
-- 当日分区。DEFAULT 分区保留作历史 catch-all（新一日数据走日分区，
-- 越界数据落 DEFAULT；partition pruning 对 WHERE 范围查询仅扫命中
-- 分区）。Date 签名（与 ensure_sessions_v2_partitions 同款），由
-- PartitionManager 24h tick 接入 ensureSpecs 用 Asia/Shanghai 日历
-- 派生当日/次日（避免 UTC 会话在 +08 0-8h 把次日误建为今日）。
--
-- TTL：旧 DEFAULT 分区历史数据归属由 owner 拍板（依《存储策略 v2.1》
-- 表 5 推算 R68 §四 登记的 TTL 区间 7-90 天），本迁移仅建日分区函数
-- 不删除任何历史 partition。

CREATE OR REPLACE FUNCTION ensure_usage_facts_daily_partition(p_date DATE)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    pname   TEXT := format('usage_facts_%s', to_char(p_date, 'YYYYMMDD'));
    start_ts TIMESTAMPTZ := p_date::timestamptz;
    end_ts   TIMESTAMPTZ := (p_date + INTERVAL '1 day')::timestamptz;
BEGIN
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS %I PARTITION OF usage_facts FOR VALUES FROM (%L) TO (%L)',
        pname, start_ts, end_ts
    );
END $$;

-- 首次迁移预建当日 + 次日；后续由 PartitionManager 24h tick 接管。
-- 调用期间分区父表已挂 DEFAULT 分区（537）；PG 允许 DEFAULT 与具体
-- RANGE 分区共存——新一日数据走日分区，越界数据落 DEFAULT，partition
-- pruning 对 WHERE 范围查询仅扫命中分区。
SELECT ensure_usage_facts_daily_partition(current_date);
SELECT ensure_usage_facts_daily_partition(current_date + 1);