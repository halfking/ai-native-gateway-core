-- 930: self_check_runs.error_type 的 CHECK 闭集**再补齐** —— 加入 model_unavailable。
--
-- 缺口本体（2026-11-02 第二十五轮，**合并 211 生产树时暴露**）：
--   ① `errorsx.AllErrorKinds()` 闭集现在是 **33** 个 kind；
--   ② `929_self_check_error_type_kind_closure.sql`（**已在 211 生产应用**、
--      字节冻结）的 CHECK 字面量集只有 **32** 个 kind
--      ⇒ ★ **缺 `model_unavailable`**；
--   ③ `model_unavailable` 是**本仓**引入的新 kind（4xx 归因轮：上游 4xx
--      归入「请求侧/模型不可用」族，见主档 §六/§七）⇒ 写侧
--      `bg/credential_selfcheck.go` 的 finalizeRun 写的**就是 kind 名**
--      ⇒ 一旦上游 4xx 落到 `model_unavailable`，UPDATE 被 DB 以 **23514** 拒绝
--      ⇒ **整条 UPDATE 回滚** ⇒ run 行停在 status='running' / error_type=NULL
--      的**半写态**，且 auditToSystemProbeRuns 与 publishSelfcheck 都不执行。
--
-- ★ 为什么之前没人发现：**929 的闭集交叉门
--   （sql/migrations/startup/migration_929_error_type_closure_test.go）
--   只存在于 211 生产机上，没在仓库里** ⇒ 本仓引入新 kind 时，
--   那道门**不在本地、也不在 CI 上** ⇒ 闭集与 kind 集合的漂移**无人守**。
--   ⇒ 本轮把 211 的工作取回仓库后，那道门**当场抓到了这个缺口**
--   ⇒ ★ 这就是「门必须住在能跑的地方」这条纪律的直接收益。
--
-- 修法：新增一条 startup 迁移**重建**该 CHECK，把**当前**闭集整份纳入
--   （33 kind + 5 个 worker 专用字面量 = **38** 个必需字面量）。
--   ★ 为何不改 929 的字节：929 已在 211 应用且字节冻结（content_sha256 进台账）
--     ⇒ 改字节会走 content replay 通道并连锁 break（见 914/928 教训）。
--   ★ 为何不挑几个补：下一个新 kind 会重犯同一个错 ⇒ 闭集必须由
--     `errorsx.AllErrorKinds()` 驱动 ⇒ 由同一道交叉门钉死。
--
-- 幂等：先读 `pg_get_constraintdef`，若已覆盖全部 38 个必需字面量 ⇒ 直接跳过。
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
    -- ★ 本数组 = errorsx.AllErrorKinds()（**33** 个 kind）
    --   ∪ worker 专用字面量（5 个，**不是** kind，是 self-check worker 直接写的字符串）
    --   = **38** 个必需字面量。
    -- ★ 跨门（migration_929_error_type_closure_test.go）会把本数组
    --   与下方 CHECK 里的 ARRAY 逐项对拍，并要求两者都 ⊇ errorsx 闭集。
    v_required text[] := ARRAY[
        -- —— errorsx kind 闭集（33）——
        'transient', 'timeout', 'network', 'rate_limit', 'auth', 'quota',
        'upstream_down', 'canceled', 'client_bug', 'request_rejected',
        'model_unavailable',
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
        RAISE NOTICE 'Migration 930: public.self_check_runs not present, skipping';
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
            RAISE NOTICE 'Migration 930: self_check_runs_error_type_check already covers the full errorsx kind closure, skipping';
            RETURN;
        END IF;

        RAISE NOTICE 'Migration 930: rebuilding self_check_runs_error_type_check (missing: %)',
            array_to_string(v_missing, ', ');
    END IF;

    ALTER TABLE public.self_check_runs
        DROP CONSTRAINT IF EXISTS self_check_runs_error_type_check;

    ALTER TABLE public.self_check_runs
        ADD CONSTRAINT self_check_runs_error_type_check CHECK (
            error_type IS NULL
            OR error_type LIKE 'http_%'
            OR error_type = ANY (ARRAY[
                -- —— errorsx kind 闭集（33）：运行时写侧写的就是这些名字 ——
                'transient', 'timeout', 'network', 'rate_limit', 'auth', 'quota',
                'upstream_down', 'canceled', 'client_bug', 'request_rejected',
                'model_unavailable',
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
        'Canonical errorsx taxonomy: errorsx.AllErrorKinds() closure (33 kinds) + worker-only literals (none/internal/parse_error/unattributed/upstream_fail); legacy http_* diagnostics remain readable. NOT VALID: historical rows are not re-validated.';
END
$do$;

COMMIT;
