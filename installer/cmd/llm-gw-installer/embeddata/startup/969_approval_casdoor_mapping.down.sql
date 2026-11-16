-- ===========================================================================
-- File:          sql/migrations/startup/969_approval_casdoor_mapping.down.sql
-- Migration:     969 (down)
-- Purpose:       回退 969——删除 approval_casdoor_mapping 表（请求ID ↔ Casdoor
--                任务ID 的本地映射）。
--
-- 回滚后果如实登记：
--   · 表内是**审批映射记录**（request_id ↔ casdoor_task_id ↔ 裁决状态）
--     ⇒ 随表删除不可恢复；已批/已拒的裁决状态丢失 ⇒ 审批链路无法再查回
--     「这条请求批没批」⇒ 待审中的请求会永久停在 pending（无映射可查）。
--   · 删除后 internal/approval.PgxApprovalStore 的 Save/Update 报 42P01
--     ⇒ **Casdoor 审批链路在存储层直接不可用**（P1 面）。
--   · ⚠️ down 会执行 DELETE FROM public.schema_migrations WHERE version='969'
--     ⇒ 回滚不是幂等 no-op，需按部署序列重放 969 才能恢复记账。
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

DROP TRIGGER IF EXISTS trigger_update_approval_casdoor_mapping_timestamp
    ON public.approval_casdoor_mapping;

DROP FUNCTION IF EXISTS public.update_approval_casdoor_mapping_timestamp();

DROP INDEX IF EXISTS public.idx_casdoor_mapping_casdoor_task;
DROP INDEX IF EXISTS public.idx_casdoor_mapping_tenant;
DROP INDEX IF EXISTS public.idx_casdoor_mapping_status;
DROP INDEX IF EXISTS public.idx_casdoor_mapping_session;

DROP TABLE IF EXISTS public.approval_casdoor_mapping;

DELETE FROM public.schema_migrations WHERE version = '969';

COMMIT;
