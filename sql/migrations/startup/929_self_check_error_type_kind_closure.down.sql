-- 929 down: 把 self_check_runs_error_type_check 退回 644 时代的宽集。
--
-- ★ 诚实边界：本 down **不删数据**。若表里已有落在这 6 个 kind 上
--   （request_rejected / circuit_open / fp_slot_saturated /
--     first_byte_timeout / reply_loop / account_restricted）的 run 行，
--   重建旧约束会撞 23514 ⇒ 事务回滚 ⇒ **迁移停在这里**。
--   ⇒ 这是刻意的：静默删行去迁就约束 = 销毁审计证据。
--   ⇒ 处置顺序：① 先导出这些行（error_type / id / started_at）；
--     ② 人工确认处置口径（归档到别处，还是接受它们并**放弃**回滚）；
--     ③ 再跑本 down。
--
-- 幂等：若约束已是 644 时代的集合 ⇒ 直接跳过（不重建、不报错）。

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

DO $do$
DECLARE
    -- ★ 929 新增的 6 个 kind（= 本 down 要退回掉的那部分）。
    v_new_only  text[] := ARRAY[
        'request_rejected', 'circuit_open', 'fp_slot_saturated',
        'first_byte_timeout', 'reply_loop', 'account_restricted'
    ];
    v_def       text;
    v_offenders text;
    v_has_new   boolean;
    v_val       text;
BEGIN
    IF to_regclass('public.self_check_runs') IS NULL THEN
        RAISE NOTICE 'Migration 929 down: public.self_check_runs not present, skipping';
        RETURN;
    END IF;

    -- ① 先看有没有会顶住旧约束的行 ⇒ 有就**响亮报错**，不静默删。
    SELECT string_agg(DISTINCT error_type, ', ')
      INTO v_offenders
      FROM public.self_check_runs
     WHERE error_type = ANY (v_new_only);

    IF v_offenders IS NOT NULL THEN
        RAISE EXCEPTION
            'Migration 929 down aborted: self_check_runs holds rows with error_type in (%) that the pre-929 constraint rejects. '
            'This down does not delete audit rows. Export and clear them deliberately, then re-run.',
            v_offenders;
    END IF;

    -- ② 已是 644 时代的集合 ⇒ 跳过。
    --
    -- ★ 判据是「**不含任何一个 929 新增的 kind**」，而不是「包含旧集的全部值」。
    --   ⇒ 因为新集是旧集的**超集**：用「包含旧集全部值」判定 ⇒ 新集也会被判成
    --   「已是旧集」⇒ down **静默跳过** ⇒ 约束根本没退回去。
    --   ⇒ ★ 这是 211 临时库真跑出来的缺陷（第一版就是「包含旧集全部值」），
    --     静态读代码看不出来 ⇒ 真库实证不是形式主义。
    SELECT pg_get_constraintdef(c.oid)
      INTO v_def
      FROM pg_constraint c
     WHERE c.conrelid = 'public.self_check_runs'::regclass
       AND c.conname = 'self_check_runs_error_type_check';

    IF v_def IS NOT NULL THEN
        v_has_new := false;
        FOREACH v_val IN ARRAY v_new_only LOOP
            IF position('''' || v_val || '''' IN v_def) > 0 THEN
                v_has_new := true;
                EXIT;
            END IF;
        END LOOP;

        IF NOT v_has_new THEN
            RAISE NOTICE 'Migration 929 down: self_check_runs_error_type_check already matches the pre-929 set, skipping';
            RETURN;
        END IF;
    END IF;

    ALTER TABLE public.self_check_runs
        DROP CONSTRAINT IF EXISTS self_check_runs_error_type_check;

    ALTER TABLE public.self_check_runs
        ADD CONSTRAINT self_check_runs_error_type_check CHECK (
            error_type IS NULL
            OR error_type LIKE 'http_%'
            OR error_type = ANY (ARRAY[
                'none', 'timeout', 'network', 'transient', 'rate_limit', 'auth', 'auth_revoked',
                'quota', 'quota_periodic', 'quota_balance', 'quota_permanent', 'upstream_down',
                'upstream_overloaded', 'concurrent', 'stream_timeout', 'model_not_found',
                'model_deprecated', 'unsupported_feature', 'context_length_exceeded',
                'content_filter', 'tool_call_id_mismatch', 'empty_response', 'conversion_error',
                'upstream_context_loss', 'no_available_channel', 'canceled', 'client_bug',
                'parse_error', 'internal', 'unattributed', 'upstream_fail'
            ])
        ) NOT VALID;

    COMMENT ON CONSTRAINT self_check_runs_error_type_check ON public.self_check_runs IS
        'Canonical errorsx taxonomy per deploy/sql/migrations/V361__self_check_runs_canonical_taxonomy.sql; legacy http_* values remain readable.';
END
$do$;

COMMIT;
