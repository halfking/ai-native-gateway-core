-- ===========================================================================
-- File:          sql/migrations/startup/823_session_turns_request_status.down.sql
-- Migration:     823 (down)
-- Database:      llm_gateway
--
-- 回滚顺序与 up 相反：先删 hot 侧列，再删父表列（与 821 down 同序）。
--
-- ⚠ **本 down 会丢弃已贯通的生命周期标签**：
--    request_status IS NOT NULL 的行在回滚后不可恢复——该标签中
--    `rate_limited` 一态无法由 success/error_kind 重推导（这正是 823 的
--    立项理由，ResolveRequestStatus 永不产出 rate_limited）。生产回滚前
--    先确认 request_logs（v1）仍在，标签可从 v1 侧重建；若 v1 已退役，
--    回滚 823 等于永久放弃限流可识别性。
--
-- ⚠ 回填作业依赖：request_status 存量回填后台作业
--    （domains/session/v2/session_request_status_backfill.go）探测目标列，
--    列消失后作业进入终态退出（retired），不会报错——这是设计内行为。
--
-- 幂等：DROP COLUMN IF EXISTS，可安全重放。
-- ===========================================================================
BEGIN;

ALTER TABLE public.session_turns_hot DROP COLUMN IF EXISTS request_status;
ALTER TABLE public.session_turns     DROP COLUMN IF EXISTS request_status;

-- 守卫与 up 对称：确认两侧列都已消失，且 hot/parent 列集合契约仍全等
-- （promote_session_turns_hot_to_partition 在集合分叉时会在运行时抛错）。
DO $$
DECLARE
    v_parent TEXT;
    v_hot    TEXT;
BEGIN
    IF EXISTS (SELECT 1 FROM pg_attribute
                WHERE attrelid IN ('public.session_turns'::regclass,
                                   'public.session_turns_hot'::regclass)
                  AND attname = 'request_status'
                  AND NOT attisdropped) THEN
        RAISE EXCEPTION '823 down: request_status still present on a session_turns surface';
    END IF;
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_parent
      FROM pg_attribute
     WHERE attrelid = 'public.session_turns'::regclass
       AND attnum > 0 AND NOT attisdropped;
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_hot
      FROM pg_attribute
     WHERE attrelid = 'public.session_turns_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;
    IF v_parent IS DISTINCT FROM v_hot THEN
        RAISE EXCEPTION 'session_turns hot/parent column contract has drifted (column set mismatch)';
    END IF;
    RAISE NOTICE '823 down: request_status removed; hot/parent contract intact';
END;
$$;

COMMIT;
