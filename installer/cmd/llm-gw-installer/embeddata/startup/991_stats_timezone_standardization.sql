-- Migration 991: 统计API时区标准化
-- 目标：统一所有统计API使用UTC日边界，消除时区不一致导致的数据差异
--
-- 问题背景（详见 docs/data-consistency-analysis.md）：
-- 1. session_analytics 使用 CURRENT_DATE（本地时区，中国=UTC+8）
-- 2. user_usage 使用 now() - N days（滑动窗口，UTC）
-- 3. report_rollup 使用显式UTC日边界
-- 4. 导致"今日"定义相差8小时，数据对不上
--
-- 修复方案：
-- 1. 创建 UTC 日边界辅助函数
-- 2. 保持向后兼容：新增 _utc 版本函数，逐步迁移
-- 3. 前端需更新文案说明时区语义

-- ============================================================================
-- 辅助函数：UTC 日边界计算
-- ============================================================================

-- utc_day_start: 返回给定时间戳的UTC日起点（00:00 UTC）
CREATE OR REPLACE FUNCTION utc_day_start(ts timestamptz)
RETURNS timestamptz
LANGUAGE SQL IMMUTABLE PARALLEL SAFE
AS $$
    SELECT date_trunc('day', ts AT TIME ZONE 'UTC') AT TIME ZONE 'UTC';
$$;

COMMENT ON FUNCTION utc_day_start IS 
'返回时间戳的UTC日起点（00:00:00 UTC），用于统计API时区标准化';

-- utc_today: 返回当前UTC日起点
CREATE OR REPLACE FUNCTION utc_today()
RETURNS timestamptz
LANGUAGE SQL STABLE PARALLEL SAFE
AS $$
    SELECT date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC';
$$;

COMMENT ON FUNCTION utc_today IS 
'返回当前UTC日起点，等价于 utc_day_start(now())';

-- utc_days_ago: 返回N天前的UTC日起点
CREATE OR REPLACE FUNCTION utc_days_ago(n int)
RETURNS timestamptz
LANGUAGE SQL STABLE PARALLEL SAFE
AS $$
    SELECT date_trunc('day', (now() AT TIME ZONE 'UTC') - (n || ' days')::interval) AT TIME ZONE 'UTC';
$$;

COMMENT ON FUNCTION utc_days_ago IS 
'返回N天前的UTC日起点，用于时间范围查询：WHERE occurred_at >= utc_days_ago(7)';

-- ============================================================================
-- 示例用法（供开发者参考）
-- ============================================================================

-- 1. 查询今日（UTC日）的会话
-- WHERE first_request_at >= utc_today()

-- 2. 查询最近7个UTC日的请求
-- WHERE occurred_at >= utc_days_ago(7)

-- 3. 查询特定UTC日范围 [start, end)
-- WHERE occurred_at >= utc_day_start('2026-11-18'::timestamptz)
--   AND occurred_at < utc_day_start('2026-11-19'::timestamptz)

-- ============================================================================
-- 向后兼容说明
-- ============================================================================

-- 本迁移**不修改**现有查询，仅提供新工具函数。
-- 后续步骤：
-- 1. Go代码更新：将 CURRENT_DATE 替换为 utc_today()
-- 2. 前端UI更新：时间选择器说明"UTC时间"
-- 3. API文档更新：注明时区语义变更
-- 4. 监控数据变化：对比迁移前后的统计差异（预期中国用户"今日"数据延后8小时）

-- ★ 锁预算声明（>870 启动迁移纪律，installer/cmd/llm-gw-installer/
--   startup_ddl_lock_guard_gate_test.go）：本件只有 CREATE OR REPLACE FUNCTION
--   （无强锁 DDL 动词），预算按惯例 5s/10min 声明，抢不到锁快速失败、不半应用。
SET lock_timeout = '5s';
SET statement_timeout = '10min';

-- ============================================================================
-- 台账自登记（≥952 纪律：与 971/977/980 同款——schema_migrations 本件自登记）
-- ============================================================================
INSERT INTO public.schema_migrations (version, description)
VALUES ('991', 'stats_timezone_standardization: utc_day_start/utc_today/utc_days_ago UTC 日边界辅助函数')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

-- ============================================================================
-- migration_metadata 登记表自建（隐含依赖收口：全仓此前零建表方——991 首版
-- 的裸 INSERT 在任何没有这张表的库上 42P01；installer 增量模式每次 boot 都会
-- 撞这一发。幂等：CREATE TABLE IF NOT EXISTS + INSERT ON CONFLICT DO NOTHING）
-- ============================================================================
CREATE TABLE IF NOT EXISTS migration_metadata (
    version     BIGINT PRIMARY KEY,
    description TEXT,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO migration_metadata (version, description, applied_at)
VALUES (991, 'Add UTC time boundary helper functions for statistics standardization', NOW())
ON CONFLICT (version) DO NOTHING;
