-- ===========================================================================
-- File:          sql/migrations/startup/894_rebind_candidate_failure_logs_diagnosis_view.sql
-- Migration:     894
-- Database:      llm_gateway
-- Purpose:       把 `v_candidate_failure_logs_diagnosis` **重新绑回活表**
--                `public.candidate_failure_logs`（修 392 的改名副作用）。
--
-- 缺陷机制（2026-10-21 R72 轮实测，★ 一句话版）：
--   **视图绑定的是关系 OID，不是名字** ⇒ 迁移 392 把老堆表
--   `candidate_failure_logs` **改名**成 `candidate_failure_logs_columnar_archive`
--   （保留旧数据）并**新建**同名空分区表时，
--   **既有的视图不会跟着换表** —— 它仍绑在老表的 OID 上，
--   而 `pg_get_viewdef` 打印的是那个 OID 的**当前名字**
--   ⇒ 视图的 FROM 变成 `candidate_failure_logs_columnar_archive`
--   ⇒ **新形态的数据对它不可见**。
--
--   本仓两处真相源（基线 `sql/schema/01-schema.sql` 与对象源文件
--   `sql/objects/views/v_candidate_failure_logs_diagnosis.sql`）
--   **都写 `FROM public.candidate_failure_logs`**
--   ⇒ 也就是说：**链终态 ≠ 真相源**。
--
-- 实测读数（本轮，2026-10-21）：
--   · 一次性库（基线 + 392）复现：392 之前视图读 LIVE；392 之后自动变 ARCHIVE
--     ⇒ 脱靶是 392 造成的，不是随机的。
--   · 本地（走完整新装链）：视图读 ARCHIVE ⇒ 0 行；活表 198 行。
--   · 生产 211：视图读 ARCHIVE ⇒ **0 行**；活表 **91,935 行**（2026-10-12~10-20）。
--   · 生产 252：视图读 LIVE ⇒ 187,907 行（252 的 392 走了「已分区则跳过」分支，
--     从未改名 ⇒ 老表没被隔离出来）。
--   ⇒ **同一个视图在两个生产实例返回完全不同的数据集** ⇒ 违反「多个环境同样标准」。
--
-- 本迁移做什么：**只重绑视图**，不改表、不动数据、不删 archive 表。
--   目标形态 = 仓内两处真相源逐字同形（基线与对象源文件本身完全一致）。
--
-- 为什么重绑到「活表」而不是「archive ∪ 活表」：
--   · 真相源（基线 / 对象源文件）写的就是「活表」⇒ 重绑到它 = 链终态对齐真相源；
--   · **实测三处 archive 表都是 0 行**（本地 0 / 211 0）⇒ 重绑**不丢任何历史**；
--   · archive 表本身**一行不删**（保留是 392 的显式设计）⇒ 随时可查。
--
-- 影响面：全仓**无任何生产代码读这个视图**（`grep -rn` 只命中 4 份定义副本本身）
--   ⇒ 这是**诊断视图**的跨环境口径修复，不影响任何端点响应。
--
-- 幂等：CREATE OR REPLACE VIEW 逐字可重放；重放后定义不变。
-- 依赖：392（archive 表与新分区表的来源）。
-- Down: 894_rebind_candidate_failure_logs_diagnosis_view.down.sql
--
-- ⚠ 列名/列序不可改：CREATE OR REPLACE VIEW 要求新列清单与既有视图一致。
--   实测三实例的该视图列清单逐字相同（16 列，id…classification_disagrees）
--   ⇒ 本迁移体与基线同文 ⇒ OR REPLACE 安全。
-- ===========================================================================

BEGIN;

-- ── 重绑：视图体与基线 / 对象源文件逐字同形 ──────────────────────────────
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
   FROM public.candidate_failure_logs cfl;

COMMENT ON VIEW public.v_candidate_failure_logs_diagnosis IS '2026-06-30 (migration 057). Computes the post-P2 classifier
     output (diagnosed_error_kind) for every candidate_failure_logs
     row, recovering the upstream HTTP status code from
     error_message via the "upstream NNN:" regex. Side-by-side
     legacy_kind vs diagnosed_error_kind for incident review.
     Companion to v_request_failures_diagnosis (migration 056).';

-- ── 自检：视图必须绑到活表，且列清单不变 ────────────────────────────────────
DO $$
DECLARE
    vdef text;
    ncols int;
BEGIN
    vdef := pg_get_viewdef('public.v_candidate_failure_logs_diagnosis'::regclass, true);

    IF vdef LIKE '%candidate_failure_logs_columnar_archive%' THEN
        RAISE EXCEPTION '894 failed: view still bound to the renamed archive table (392 side effect not repaired)';
    END IF;

    IF vdef NOT LIKE '%candidate_failure_logs%' THEN
        RAISE EXCEPTION '894 failed: view does not reference candidate_failure_logs at all';
    END IF;

    SELECT count(*) INTO ncols
    FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'v_candidate_failure_logs_diagnosis';
    IF ncols <> 16 THEN
        RAISE EXCEPTION '894 failed: unexpected view column count % (expected 16)', ncols;
    END IF;

    RAISE NOTICE '894: v_candidate_failure_logs_diagnosis rebound to public.candidate_failure_logs';
END $$;

-- Ledger self-registration（710 惯例）
INSERT INTO public.schema_migrations (version, description)
VALUES ('894', 'rebind v_candidate_failure_logs_diagnosis to the live candidate_failure_logs table: migration 392 renamed the old heap table to candidate_failure_logs_columnar_archive and PostgreSQL views bind to relation OIDs, so the view silently kept reading the renamed-away table (211: 0 rows vs 91,935 live rows); view body is byte-identical to the two in-repo sources of truth (sql/schema/01-schema.sql and sql/objects/views/v_candidate_failure_logs_diagnosis.sql)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
