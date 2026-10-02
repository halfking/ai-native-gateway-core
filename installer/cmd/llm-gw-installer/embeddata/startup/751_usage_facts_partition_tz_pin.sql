-- 751: ensure_usage_facts_daily_partition 时区钉扎（R69 12h 审计轮，2026-09-26）
--
-- 背景（1614 轮审计 O-3 裁决落地）：750 的分区边界转换 start_ts/end_ts
-- 在 DECLARE 初始化器里做 p_date::timestamptz，随**会话时区**求值：
--   - PartitionManager tick 已用 Asia/Shanghai 日历派生正确日期
--     （bg/partition_manager.go partitionTZ / FixedZone）；
--   - db.go boot ensure 传 current_date（随会话时区的日期）；
-- 日期正确但边界**转换**在 UTC 会话下会得到 [00:00Z, 24:00Z) 即
-- [+08 次日 08:00) 的错位窗口——与 rollup/对账的 Shanghai 日边界不对齐，
-- 分区路由窗口错开 8 小时。当前 252/245 会话均 +08 无实际影响，但属
-- 环境敏感：运维用 UTC 会话手动调用、或未来 DSN 显式 TimeZone=UTC
-- 即触发。
--
-- 修法：函数级 GUC 钉扎（ALTER FUNCTION ... SET timezone）。proconfig
-- 在**函数入口**生效、函数退出自动回滚，先于 DECLARE 初始化器执行——
-- 这正是 694 先例教训的对偶：函数体内 SET LOCAL 不覆盖 DECLARE 初始
-- 化器（初始器先于 body 执行，694 因此把月派生全部移出初始器）；函数
-- 级 SET 则覆盖。pg17 scratch 真库实证：UTC 会话下 ensure(date) 的
-- 分区边界仍为 +08（db/db_751_tz_pin_realdb_test.go）。
--
-- 幂等：ALTER FUNCTION SET 可重复执行，不改函数体、不动分区。
-- down 用 RESET timezone 还原（750 形态完整保留）。

ALTER FUNCTION public.ensure_usage_facts_daily_partition(DATE)
    SET timezone = 'Asia/Shanghai';
