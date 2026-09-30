-- 763 down: 撤回 provider_events 契约对象（保留表与数据）。
--
-- ⚠️ 撤回后回 252 修复前漂移态（id 无默认/无 PK）：writer 显式供 id 仍可写，
--    但并发双写重新回到"静默重复"形态。本 down 仅供回滚演练，不要在生产执行。
-- ⚠️ 758 纪律：顶层语句，不包 DO EXECUTE。

ALTER TABLE public.provider_events
    DROP CONSTRAINT IF EXISTS provider_events_pkey;
ALTER TABLE public.provider_events
    ALTER COLUMN id DROP DEFAULT;
DROP INDEX IF EXISTS public.idx_provider_events_credential_ts;
DROP SEQUENCE IF EXISTS public.provider_events_id_seq;
