-- 759 down: 回退 grain 维度列与裁剪索引。
--
-- 只删本轮新增的三列与两个索引；**不回退 scope 行数据**——daily_grain /
-- internal_grain 行写入后，即使列被删也只是读面不可用（scope 是 TEXT 自由
-- 值，删列不会让这些行变成非法数据）。需要彻底回退到 746 形状时，删除
-- grain 行再执行本迁移：
--
--   DELETE FROM report_snapshots WHERE scope IN ('daily_grain','internal_grain');
--
-- 注意：provider_id / tenant_id / raw_model_name 等既有列**不动**——它们是
-- 746 及更早既有的列，本迁移没有触碰。

DROP INDEX IF EXISTS public.idx_report_snapshots_credential_date;
DROP INDEX IF EXISTS public.idx_report_snapshots_api_key_date;

ALTER TABLE public.report_snapshots
    DROP COLUMN IF EXISTS credential_id,
    DROP COLUMN IF EXISTS api_key_id,
    DROP COLUMN IF EXISTS person;

-- scope 列的 COMMENT 已被 759 覆写为含 grain 的版本；down 不做字符串回退
-- （COMMENT 回退需要重写 746 的整段文案，收益低于风险：scope 是 TEXT 自由
-- 值，注释陈旧不影响任何读写路径的正确性）。若确需对齐，见 746 迁移体。
