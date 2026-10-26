-- ===========================================================================
-- File:          sql/migrations/startup/894_rebind_candidate_failure_logs_diagnosis_view.down.sql
-- Migration:     894 (down)
-- Purpose:       还原 894 之前的绑定形态。
--
-- 894 之前，视图绑定的是**被 392 改名走**的那张表：
--   · `candidate_failure_logs_columnar_archive` 存在 ⇒ 脱靶到 archive（392 的改名分支）；
--   · 不存在 ⇒ 视图本就绑在活表上（392 的「已分区则跳过」分支，252 即如此）。
--
-- ⇒ down 的语义是「回到 894 之前**实际**的形态」，而不是「回到某个臆想的形态」。
-- ⚠ 这不是业务语义回滚，而是**绑定关系**回滚：
--   被脱靶的形态本身是 392 的副作用（缺陷），所以 down 之后视图会**再次读不到活表数据**。
--   这属于「故意保留缺陷以保持可逆」的取舍：迁移必须可逆，而可逆的代价由人承担。
--   ——若要真正修好，重新 apply 894 即可。
--
-- 列名/列序同样不可改（CREATE OR REPLACE VIEW 的硬约束）：沿用 894 的同一列清单。
-- ===========================================================================

BEGIN;

DO $$
DECLARE
    has_archive boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public' AND c.relname = 'candidate_failure_logs_columnar_archive'
    ) INTO has_archive;

    IF NOT has_archive THEN
        -- 392 走的是「跳过改名」分支 ⇒ 894 之前视图本来就绑在活表上 ⇒ 无需回退。
        RAISE NOTICE '894 down: candidate_failure_logs_columnar_archive absent (392 skipped the rename) ⇒ view already bound to the live table, nothing to revert';
        RETURN;
    END IF;

    EXECUTE $sql$
        CREATE OR REPLACE VIEW public.v_candidate_failure_logs_diagnosis AS
         SELECT id,
            ts,
            tenant_id,
            credential_id,
            provider_id,
            raw_model_name,
            attempt_index,
            error_kind AS legacy_kind,
            COALESCE(upstream_status_code,
                CASE
                    WHEN (error_message ~ 'upstream [0-9]+:'::text) THEN ("substring"(error_message, 'upstream ([0-9]+):'::text))::integer
                    ELSE NULL::integer
                END) AS extracted_upstream_status_code,
            public.diagnose_failure_kind(COALESCE(upstream_status_code,
                CASE
                    WHEN (error_message ~ 'upstream [0-9]+:'::text) THEN ("substring"(error_message, 'upstream ([0-9]+):'::text))::integer
                    ELSE NULL::integer
                END), COALESCE(NULLIF(upstream_response_body, ''::text), error_message, ''::text)) AS diagnosed_error_kind,
            upstream_status_code AS live_upstream_status_code,
            latency_ms,
            per_attempt_latency_ms,
            retryable,
            error_message,
            (error_kind IS DISTINCT FROM public.diagnose_failure_kind(COALESCE(upstream_status_code,
                CASE
                    WHEN (error_message ~ 'upstream [0-9]+:'::text) THEN ("substring"(error_message, 'upstream ([0-9]+):'::text))::integer
                    ELSE NULL::integer
                END), COALESCE(NULLIF(upstream_response_body, ''::text), error_message, ''::text))) AS classification_disagrees
           FROM public.candidate_failure_logs_columnar_archive cfl
    $sql$;

    RAISE NOTICE '894 down: view rebound to candidate_failure_logs_columnar_archive (restores the pre-894 binding)';
END $$;

DELETE FROM public.schema_migrations WHERE version = '894';

COMMIT;
