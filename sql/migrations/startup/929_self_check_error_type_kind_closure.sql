-- 929: self_check_runs.error_type 的 CHECK 闭集补齐 —— 与 errorsx.ErrorKind 闭集对齐。
--
-- 缺口本体（2026-11-02 12h 审计轮 P1，上游 9 提交引入）：
--   ① `errorsx.AllErrorKinds()` 闭集共 **32** 个 kind；
--   ② `644_tuning_views_selfcheck_and_candidate_failure_cache.sql` 的
--      `self_check_runs_error_type_check` 字面量闭集只有 **31** 个
--      （= 26 个 kind + 5 个 worker 专用字面量），**缺 6 个**：
--        request_rejected / circuit_open / fp_slot_saturated
--        / first_byte_timeout / reply_loop / account_restricted
--   ③ 写侧 `bg/credential_selfcheck.go:844` 写的正是 kind 名：
--        r.ErrType = string(errorsx.ClassifyErrorWithBody(resp.StatusCode, body))
--      ⇒ 一旦上游返回落在那 6 个 kind 上，`finalizeRun`（:896）的 UPDATE
--        被 DB 以 23514 拒绝 ⇒ **整条 UPDATE 回滚** ⇒ run 行停在
--        status='running' / error_type=NULL / completed_at=NULL 的**半写态**，
--        且 `auditToSystemProbeRuns`（:433）与 `publishSelfcheck` 都不执行。
--   ④ 可达路径不是假想：`selfcheckBindings`（:523-540）**无 modality 过滤**
--      ⇒ embedding 绑定会被选中发 chat 形态 ⇒ 命中上游新引入的
--      `modality_mismatch` 400 ⇒ 落 `request_rejected`。
--
-- 真库实证（211 PG17.10，2026-11-02）：
--   临时库上 `UPDATE self_check_runs SET error_type='request_rejected'`
--   ⇒ ERROR: violates check constraint ⇒ 整条 UPDATE 被拒；
--   对照组写 'model_not_found' ⇒ UPDATE 1 ⇒ 拒绝确由该 kind 值导致。
--
-- 修法：重建该 CHECK，把 errorsx kind 闭集**整份**纳入（不是挑几个补），
--   并保留 `http_%` 通配（旧装机的历史诊断值仍可读）。
--   ★ 为何不挑几个补：下一个新 kind 会重犯同一个错 ⇒ 闭集必须由
--     `errorsx.AllErrorKinds()` 驱动 ⇒ 由交叉门
--     `sql/migrations/startup/migration_929_error_type_closure_test.go` 钉死。
--
-- ★ 为何是**新迁移**而不是改 644 的字节：644 已在 211 应用且字节冻结
--   （content_sha256 进台账）⇒ 改字节会走 content replay 通道并连锁 break
--   （见 914/928 教训）。
--
-- 幂等：先读 `pg_get_constraintdef`，若已覆盖全部 37 个必需字面量 ⇒ 直接跳过。
--   可重入 ⇒ 重复执行不重建约束、不刷 NOTICE 噪声以外的副作用。
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
    -- ★ 本数组 = errorsx.AllErrorKinds()（32 个 kind）
    --   ∪ worker 专用字面量（5 个，**不是** kind，是 self-check worker 直接写的字符串）
    --   = 37 个必需字面量。
    -- ★ 跨门（migration_929_error_type_closure_test.go）会把本数组
    --   与下方 CHECK 里的 ARRAY 逐项对拍，并要求两者都 ⊇ errorsx 闭集。
    v_required text[] := ARRAY[
        -- —— errorsx kind 闭集（32）——
        'transient', 'timeout', 'network', 'rate_limit', 'auth', 'quota',
        'upstream_down', 'canceled', 'client_bug', 'request_rejected',
        'concurrent', 'auth_revoked', 'quota_periodic', 'quota_balance',
        'quota_permanent', 'model_not_found', 'stream_timeout',
        'tool_call_id_mismatch', 'context_length_exceeded', 'unsupported_feature',
        'model_deprecated', 'content_filter', 'empty_response', 'conversion_error',
        'upstream_context_loss', 'upstream_overloaded', 'no_available_channel',
        'circuit_open', 'fp_slot_saturated', 'first_byte_timeout', 'reply_loop',
        'account_restricted',
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
        RAISE NOTICE 'Migration 929: public.self_check_runs not present, skipping';
        RETURN;
    END IF;

    SELECT pg_get_constraintdef(c.oid)
      INTO v_def
      FROM pg_constraint c
     WHERE c.conrelid = 'public.self_check_runs'::regclass
       AND c.conname = 'self_check_runs_error_type_check';

    IF v_def IS NOT NULL THEN
        FOREACH v_val IN ARRAY v_required LOOP
            -- ★ 带引号匹配 ⇒ 'auth' 不会被 'auth_revoked' 误命中
            --   （pg_get_constraintdef 的字面量渲染成 'x'::text）。
            IF position('''' || v_val || '''' IN v_def) = 0 THEN
                v_missing := v_missing || v_val;
            END IF;
        END LOOP;

        IF array_length(v_missing, 1) IS NULL THEN
            RAISE NOTICE 'Migration 929: self_check_runs_error_type_check already covers the full errorsx kind closure, skipping';
            RETURN;
        END IF;

        RAISE NOTICE 'Migration 929: rebuilding self_check_runs_error_type_check (missing: %)',
            array_to_string(v_missing, ', ');
    END IF;

    ALTER TABLE public.self_check_runs
        DROP CONSTRAINT IF EXISTS self_check_runs_error_type_check;

    ALTER TABLE public.self_check_runs
        ADD CONSTRAINT self_check_runs_error_type_check CHECK (
            error_type IS NULL
            OR error_type LIKE 'http_%'
            OR error_type = ANY (ARRAY[
                -- —— errorsx kind 闭集（32）：运行时写侧写的就是这些名字 ——
                'transient', 'timeout', 'network', 'rate_limit', 'auth', 'quota',
                'upstream_down', 'canceled', 'client_bug', 'request_rejected',
                'concurrent', 'auth_revoked', 'quota_periodic', 'quota_balance',
                'quota_permanent', 'model_not_found', 'stream_timeout',
                'tool_call_id_mismatch', 'context_length_exceeded', 'unsupported_feature',
                'model_deprecated', 'content_filter', 'empty_response', 'conversion_error',
                'upstream_context_loss', 'upstream_overloaded', 'no_available_channel',
                'circuit_open', 'fp_slot_saturated', 'first_byte_timeout', 'reply_loop',
                'account_restricted',
                -- —— worker 专用字面量（5）——
                'none', 'internal', 'parse_error', 'unattributed', 'upstream_fail'
            ])
        ) NOT VALID;

    COMMENT ON CONSTRAINT self_check_runs_error_type_check ON public.self_check_runs IS
        'Canonical errorsx taxonomy: errorsx.AllErrorKinds() closure (32 kinds) + worker-only literals (none/internal/parse_error/unattributed/upstream_fail); legacy http_* diagnostics remain readable. NOT VALID: historical rows are not re-validated.';
END
$do$;

COMMIT;
