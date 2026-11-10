-- 957: self_check_runs.error_type 的 CHECK 闭集再补一个 kind —— `rate_limit_observed`。
--
-- 缺口本体（2026-11-10 审计 F50 轮，本轮实测）：
--   ① `errorsx.AllErrorKinds()` 闭集现在是 **34** 个 kind（930 落地时是 33）；
--   ② 930（`930_self_check_error_type_kind_closure_model_unavailable.sql`）把
--      CHECK 闭集对齐到 **38** 项（33 kind + 5 个 worker 专用字面量）
--      ⇒ 闭集少 **1** 项：`rate_limit_observed`；
--   ③ 少的那一项是 **2026-11-10** 的审计 F50 轮加的
--      `errorsx.KindRateLimitObserved`（`errorsx/classify.go:51`）
--      —— 「上游用**请求侧 4xx** 回了一个**限流形态**的正文」
--      （例：HTTP 400 + {"error":{"message":"rate limit exceeded"}}）
--      ⇒ 归因到「**不惩罚节点**」的独立 kind（`PenaltyOf` 走 `PenaltyNone`，
--      与 `KindRateLimit` 的 `PenaltyBinding`——拉黑 + 连败累计——**明确不同**）
--      ⇒ ★ 这正是 930 注释里写的「同一个『镜像面没跟上』家族的**第三次复发**」
--      （第一次 929 补 6 个、第二次 930 补 `model_unavailable`、第三次本条）；
--   ④ 写侧写的就是 kind 名（`bg/credential_selfcheck.go:844`：
--      `r.ErrType = string(errorsx.ClassifyErrorWithBody(resp.StatusCode, body))`）
--      ⇒ 一旦上游 4xx 落到 `rate_limit_observed`，`finalizeRun`（:896）的 UPDATE
--      被 DB 以 **23514** 拒绝 ⇒ **整条 UPDATE 回滚** ⇒ run 行停在
--      `status='running'` / `error_type=NULL` / `completed_at=NULL` 的**半写态**
--      ⇒ `auditToSystemProbeRuns` 与 `publishSelfcheck` 都不执行
--      ⇒ ★ **这次自检在系统里根本没发生过**，但调度器以为发生过。
--
-- ★★ 本迁移存在的意义（与 929/930 同族，但要强调「**第三次**复发」这一面）：
--   929 的门 `sql/migrations/startup/migration_929_error_type_closure_test.go`
--   判据是「闭集对拍」（`errorsx.AllErrorKinds()` ⊆ DB CHECK 字面量集）
--   ⇒ ★ **门抓到了**（它当场红 ⇒ 就是本轮的发现）⇒ ★★ 但**门只报警不施救**：
--   门说「新增一条 startup 迁移重建该 CHECK」，**迁移要人来写** ⇒ ⇒
--   ⇒ ★ **「加一个 kind」的系统成本 = 一次跨仓的人工动作** ⇒ 而这道动作
--     **每次都会晚**（上游加 kind 的那一笔不会顺手改已冻结的迁移字节）⇒
--     ⇒ ★★ ⇒ **「门能抓到」不等于「门会自动修好」** ⇒ ⇒ 这是本仓的**结构性
--     成本**，不是某一次的手滑 ⇒ ⇒ 判据：**每加一个 kind 都要预留一次
--     「新增迁移」的跨仓动作**（本条是那笔动作的第二次执行记录，930 是第一次）。
--
-- ★ 为何是**新迁移**而不是改 930 的字节：930 已进部署台账（content_sha256）
--   ⇒ 改字节会走 content replay 通道并连锁 break（914/928 教训）⇒ 同 929/930 的处置。
--
-- 修法：重建该 CHECK，把 errorsx kind 闭集**整份**纳入（34 kind），
--   并保留 `http_%` 通配（旧装机的历史诊断值仍可读）⇒ 合计 **39** 项。
--
-- 幂等：先读 `pg_get_constraintdef`，若已覆盖全部 39 个必需字面量 ⇒ 直接跳过。
--   可重入 ⇒ 重复执行不重建约束。
--
-- 锁面：DROP CONSTRAINT + ADD CONSTRAINT 各拿一次 ACCESS EXCLUSIVE。
--   self_check_runs 是小表（凭据自检 run 台账），锁持有时间在毫秒级；
--   自带 lock_timeout ⇒ 拿不到锁就快速失败，不排队堵住写路径。
--   ★ NOT VALID 语义：只拦**新行**、不校验存量 ⇒ 存量历史 run 行不必重洗。

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

DO $do$
DECLARE
    -- ★ 本数组 = errorsx.AllErrorKinds()（**34** 个 kind）
    --   ∪ worker 专用字面量（5 个，**不是** kind，是 self-check worker 直接写的字符串）
    --   = **39** 个必需字面量（930 的 38 项 + 本次新增的 'rate_limit_observed'）。
    -- ★ 跨门（migration_929_error_type_closure_test.go）会把本数组
    --   与下方 CHECK 里的 ARRAY 逐项对拍，并要求两者都 ⊇ errorsx 闭集。
    v_required text[] := ARRAY[
        'transient', 'timeout', 'network', 'rate_limit', 'auth', 'quota',
        'upstream_down', 'canceled', 'client_bug', 'request_rejected',
        'model_unavailable', 'rate_limit_observed', 'concurrent',
        'auth_revoked', 'quota_periodic', 'quota_balance', 'quota_permanent',
        'model_not_found', 'stream_timeout', 'tool_call_id_mismatch',
        'context_length_exceeded', 'unsupported_feature',
        'model_deprecated', 'content_filter', 'empty_response',
        'conversion_error', 'upstream_context_loss', 'upstream_overloaded',
        'no_available_channel', 'circuit_open', 'fp_slot_saturated',
        'first_byte_timeout', 'reply_loop', 'account_restricted',
        -- —— worker 专用字面量（5）：不在 errorsx 闭集里，但 worker 直接写 ——
        --    'none'          （bg/self_check_worker.go:550 / credential_selfcheck.go:388）
        --    'internal'      （bg/self_check_worker.go:634 / credential_selfcheck.go:813）
        --    'parse_error'   （bg/self_check_worker.go:690）
        --    'unattributed'  （bg/credential_selfcheck.go:808）
        --    'upstream_fail' （历史值，见 644 的 canonical 清单）
        'none', 'internal', 'parse_error', 'unattributed', 'upstream_fail'
    ];
    v_def     text;
    v_missing text[] := ARRAY[]::text[];
    v_val     text;
BEGIN
    IF to_regclass('public.self_check_runs') IS NULL THEN
        RAISE NOTICE 'Migration 957: public.self_check_runs not present, skipping';
        RETURN;
    END IF;

    SELECT pg_get_constraintdef(c.oid)
      INTO v_def
      FROM pg_constraint c
     WHERE c.conrelid = 'public.self_check_runs'::regclass
       AND c.conname = 'self_check_runs_error_type_check';

    IF v_def IS NOT NULL THEN
        FOREACH v_val IN ARRAY v_required LOOP
            -- ★ 带引号匹配 ⇒ 'rate_limit' 不会被 'rate_limit_observed' 误命中
            --   （pg_get_constraintdef 的字面量渲染成 'x'::text）。
            IF position('''' || v_val || '''' IN v_def) = 0 THEN
                v_missing := v_missing || v_val;
            END IF;
        END LOOP;

        IF array_length(v_missing, 1) IS NULL THEN
            RAISE NOTICE 'Migration 957: self_check_runs_error_type_check already covers the full errorsx kind closure, skipping';
            RETURN;
        END IF;

        RAISE NOTICE 'Migration 957: rebuilding self_check_runs_error_type_check (missing: %)',
            array_to_string(v_missing, ', ');
    END IF;

    ALTER TABLE public.self_check_runs
        DROP CONSTRAINT IF EXISTS self_check_runs_error_type_check;

    ALTER TABLE public.self_check_runs
        ADD CONSTRAINT self_check_runs_error_type_check CHECK (
            error_type IS NULL
            OR error_type LIKE 'http_%'
            OR error_type = ANY (ARRAY[
                -- —— errorsx kind 闭集（34）：运行时写侧写的就是这些名字 ——
                'transient', 'timeout', 'network', 'rate_limit', 'auth',
                'quota', 'upstream_down', 'canceled', 'client_bug',
                'request_rejected', 'model_unavailable',
                'rate_limit_observed', 'concurrent', 'auth_revoked',
                'quota_periodic', 'quota_balance', 'quota_permanent',
                'model_not_found', 'stream_timeout',
                'tool_call_id_mismatch', 'context_length_exceeded',
                'unsupported_feature', 'model_deprecated',
                'content_filter', 'empty_response', 'conversion_error',
                'upstream_context_loss', 'upstream_overloaded',
                'no_available_channel', 'circuit_open',
                'fp_slot_saturated', 'first_byte_timeout', 'reply_loop',
                'account_restricted',
                -- —— worker 专用字面量（5）——
                'none', 'internal', 'parse_error', 'unattributed',
                'upstream_fail'
            ])
        ) NOT VALID;

    COMMENT ON CONSTRAINT self_check_runs_error_type_check ON public.self_check_runs IS
        'Canonical errorsx taxonomy: errorsx.AllErrorKinds() closure (34 kinds) + worker-only literals (none/internal/parse_error/unattributed/upstream_fail); legacy http_* diagnostics remain readable. NOT VALID: historical rows are not re-validated.';
END
$do$;

COMMIT;
