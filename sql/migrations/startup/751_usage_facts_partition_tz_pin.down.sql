-- 751 down: 撤回 ensure_usage_facts_daily_partition 的时区钉扎
-- （R69 12h 审计轮，2026-09-26）。
--
-- 仅回滚函数级 GUC 配置；函数体（750 搬移后挂接形态）与已建日分区
-- 全部保留。

ALTER FUNCTION public.ensure_usage_facts_daily_partition(DATE)
    RESET timezone;
