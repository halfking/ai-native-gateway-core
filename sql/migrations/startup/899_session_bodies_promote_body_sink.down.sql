-- Migration 899 down: restore the 708 body of
-- public.promote_session_bodies_hot_to_partition.
--
-- ⚠ 如实登记：这一步回退**回到已知缺陷形态**。
--   708 的晋升走 `ON CONFLICT (id, partition_date) DO NOTHING`（**带仲裁目标**），
--   而父表上还有 session_bodies_tenant_request_partition_key UNIQUE
--   (tenant_id, request_id, partition_date) ⇒ 两者键集不同 ⇒ 23505 不被兜住
--   ⇒ 整批回滚 ⇒ 毒丸行永远晋升不出去 ⇒ hot 窗口被卡死。
--   （2026-09-17 PG 日志审计，本机 llm_gateway 库同键 65 轮 23505 实测；
--   逐字记录在 db/db.go:3965-3972 的注释里。）
--
--   ⇒ down 的语义 = 「回到 899 之前的状态」= 回到那个已知缺陷形态。
--   ⇒ ★ 这不是缺陷，是如实登记（897 的 down 同款做法：重放 869 并注明
--     「回滚 = 回到故障」）。
--
-- ★ 为什么重放 708 而不是 615/626/638：通道链序是
--   615 → 626 → 638 → 708 → 899 ⇒ 链尾是 899 ⇒ 撤销链尾必须回到
--   **紧邻的前一项**（708），否则中间那三版的形态会被留下。
--
-- ★ 为什么函数签名逐字不变：retention_window interval / batch_size integer
--   / RETURNS TABLE(moved_count bigint) ⇒ CREATE OR REPLACE 是替换、不是新增
--   ⇒ 无重载残留、调用方零改动。

BEGIN;

CREATE OR REPLACE FUNCTION public.promote_session_bodies_hot_to_partition(
    retention_window interval DEFAULT '8 hours',
    batch_size integer DEFAULT 5000
)
RETURNS TABLE(moved_count bigint)
LANGUAGE plpgsql
AS $function$
DECLARE
    cutoff_ts timestamptz;
    v_parent_shape TEXT;
    v_hot_shape TEXT;
    v_cols TEXT;
BEGIN
    IF retention_window IS NULL OR retention_window <= interval '0 seconds' THEN
        RAISE EXCEPTION 'retention_window must be positive';
    END IF;
    IF batch_size IS NULL OR batch_size < 1 THEN
        RAISE EXCEPTION 'batch_size must be >= 1';
    END IF;

    IF to_regclass('public.session_bodies_hot') IS NULL
       OR to_regclass('public.session_bodies') IS NULL THEN
        RAISE EXCEPTION 'session_bodies hot and parent tables must both exist';
    END IF;

    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                              || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_parent_shape
      FROM pg_attribute
     WHERE attrelid = 'public.session_bodies'::regclass
       AND attnum > 0 AND NOT attisdropped;
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                              || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_hot_shape
      FROM pg_attribute
     WHERE attrelid = 'public.session_bodies_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;
    IF v_parent_shape IS DISTINCT FROM v_hot_shape THEN
        RAISE EXCEPTION 'session_bodies hot/parent column contract has drifted (column set mismatch)';
    END IF;

    SELECT string_agg(quote_ident(attname), ',' ORDER BY attnum)
      INTO v_cols
      FROM pg_attribute
     WHERE attrelid = 'public.session_bodies_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;

    cutoff_ts := now() - retention_window;

    IF NOT pg_try_advisory_xact_lock(hashtext('public.promote_session_bodies_hot_to_partition')) THEN
        RETURN QUERY SELECT 0::bigint;
        RETURN;
    END IF;

    EXECUTE format(
        'WITH to_move AS (
            SELECT %1$s
            FROM public.session_bodies_hot
            WHERE ts < %3$L::timestamptz
            ORDER BY ts
            LIMIT %2$s
            FOR UPDATE SKIP LOCKED
        ),
        inserted AS (
            INSERT INTO public.session_bodies (%1$s)
            SELECT %1$s FROM to_move
            ON CONFLICT (id, partition_date) DO NOTHING
            RETURNING id, partition_date
        ),
        deleted AS (
            DELETE FROM public.session_bodies_hot h
            WHERE EXISTS (SELECT 1 FROM inserted i
                          WHERE i.id = h.id AND i.partition_date = h.partition_date)
            RETURNING 1
        )
        SELECT count(*) FROM deleted', v_cols, batch_size::text, cutoff_ts::text)
    INTO moved_count;

    RETURN QUERY SELECT moved_count;
END;
$function$;

-- 双账本自登记（695-705 定式）。
DELETE FROM public.schema_migrations WHERE version = '899';

COMMIT;
