-- Migration 992: 添加CST（中国标准时间，UTC+8）业务函数族
-- 目标：在UTC存储基础上提供CST边界函数，满足东8区业务需求
--
-- 设计决策：
-- 1. 数据库存储保持UTC（timestamptz标准实践）
-- 2. 创建CST函数族供业务层使用（cst_today/cst_days_ago/cst_day_start）
-- 3. 保留UTC函数供内部系统使用
-- 4. 清晰的命名（cst_* vs utc_*）避免混淆
--
-- 业务语义：
-- - "今日"指CST零点（北京时间00:00，等价于UTC前一日16:00）
-- - "最近7天"指从CST今日零点回溯7个CST日
-- - 与用户认知的"今天""昨天"对齐
--
-- 技术实现：
-- - 使用 'Asia/Shanghai' 时区（UTC+8，无夏令时）
-- - 函数标记为 STABLE（依赖now()）而非IMMUTABLE
-- - PARALLEL SAFE 支持并行查询优化

-- ============================================================================
-- 辅助函数：CST 日边界计算
-- ============================================================================

-- cst_day_start: 返回给定时间戳的CST日起点（00:00 Asia/Shanghai）
CREATE OR REPLACE FUNCTION cst_day_start(ts timestamptz)
RETURNS timestamptz
LANGUAGE SQL IMMUTABLE PARALLEL SAFE
AS $$
    SELECT date_trunc('day', ts AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai';
$$;

COMMENT ON FUNCTION cst_day_start IS 
'返回时间戳的CST日起点（00:00:00 Asia/Shanghai），用于东8区业务统计。
示例：cst_day_start(''2026-11-19 10:30:00+08'') → ''2026-11-19 00:00:00+08''';

-- cst_today: 返回当前CST日起点
CREATE OR REPLACE FUNCTION cst_today()
RETURNS timestamptz
LANGUAGE SQL STABLE PARALLEL SAFE
AS $$
    SELECT date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai';
$$;

COMMENT ON FUNCTION cst_today IS 
'返回当前CST日起点，等价于 cst_day_start(now())。
用于查询"今日"（北京时间）的数据：WHERE created_at >= cst_today()';

-- cst_days_ago: 返回N天前的CST日起点
CREATE OR REPLACE FUNCTION cst_days_ago(n int)
RETURNS timestamptz
LANGUAGE SQL STABLE PARALLEL SAFE
AS $$
    SELECT date_trunc('day', (now() AT TIME ZONE 'Asia/Shanghai') - (n || ' days')::interval) 
           AT TIME ZONE 'Asia/Shanghai';
$$;

COMMENT ON FUNCTION cst_days_ago IS 
'返回N天前的CST日起点，用于时间范围查询。
示例：WHERE occurred_at >= cst_days_ago(7)  -- 最近7个CST日';

-- cst_date: 返回给定时间戳的CST日期（date类型）
CREATE OR REPLACE FUNCTION cst_date(ts timestamptz)
RETURNS date
LANGUAGE SQL IMMUTABLE PARALLEL SAFE
AS $$
    SELECT (ts AT TIME ZONE 'Asia/Shanghai')::date;
$$;

COMMENT ON FUNCTION cst_date IS 
'返回时间戳的CST日期（date类型），用于日期分组和比较。
示例：GROUP BY cst_date(created_at)';

-- ============================================================================
-- 示例用法（供开发者参考）
-- ============================================================================

-- 1. 查询今日（CST日）的会话
-- SELECT * FROM sessions WHERE first_request_at >= cst_today();

-- 2. 查询最近7个CST日的请求
-- SELECT * FROM request_logs WHERE occurred_at >= cst_days_ago(7);

-- 3. 查询特定CST日范围 [start, end)
-- SELECT * FROM request_logs 
-- WHERE occurred_at >= cst_day_start('2026-11-18 00:00:00+08'::timestamptz)
--   AND occurred_at < cst_day_start('2026-11-19 00:00:00+08'::timestamptz);

-- 4. 按CST日期分组统计
-- SELECT cst_date(occurred_at) as date, COUNT(*) 
-- FROM request_logs 
-- WHERE occurred_at >= cst_days_ago(30)
-- GROUP BY cst_date(occurred_at)
-- ORDER BY date DESC;

-- ============================================================================
-- UTC vs CST 边界对比
-- ============================================================================

-- 示例：2026-11-19 北京时间23:30（即将跨日）
-- CST Today: 2026-11-19 00:00:00+08 (UTC 2026-11-18 16:00:00+00)
-- UTC Today: 2026-11-19 00:00:00+00 (CST 2026-11-19 08:00:00+08)
-- 差异：CST的"今日"比UTC的"今日"早8小时开始

-- 业务含义：
-- - CST函数：与用户认知的"今天""昨天"对齐
-- - UTC函数：与国际标准时间对齐，用于跨时区系统

-- ============================================================================
-- 迁移策略
-- ============================================================================

-- 本迁移创建CST函数族，后续步骤：
-- 1. Go代码审计：识别所有使用 CURRENT_DATE 和日期范围的查询
-- 2. 统一替换：将本地时区查询改为 cst_today()/cst_days_ago()
-- 3. 代码规范：明确标注时区语义（注释/变量名）
-- 4. 测试验证：跨日边界测试（23:50-00:10），确保数据归属正确
-- 5. 文档更新：API文档注明时区语义（"北京时间""东8区"）

-- ★ 双账本纪律：≥952 的迁移必须自登记进 schema_migrations（960 立的纪律门）。
--   原写法 INSERT INTO migration_metadata —— 该表在本库不存在，真跑必炸在最后一步
--   （与 991 同型缺陷，2026-11-19 文档一致性轮订正）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('992', 'add_cst_timezone_functions: CST 业务时区函数族（cst_day_start/cst_today/cst_days_ago 等，Asia/Shanghai UTC+8，与 991 的 utc_* 族并存）')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
