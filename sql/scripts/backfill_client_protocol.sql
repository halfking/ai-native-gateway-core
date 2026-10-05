-- Backfill session_turns.client_protocol from v1 request_logs
-- (audit §9.208, decision D28-c; backfill registered 2026-10-04 by the
--  R43 12h audit, §9.214/L2).
--
-- WHY THIS EXISTS
--   `session_turns.client_protocol` was never written by any code path until
--   6fe47b3f1 (§9.208). The write side is now complete (INSERT + UPDATE claim
--   legs + replay + s1a bridge, realdb gate included), but **history is not**:
--   measured 2026-10-04, the 710 view's client_protocol column is filled by
--   the **v1 arm only** (35,185 of 2,278,971 rows ≈ 1.5% lifetime; those
--   values live in `request_logs.client_protocol`). After v1 retirement both
--   the view arm and the native arm go empty for the historical range at the
--   same moment — same failure shape as D32 (is_final_success), which got a
--   backfill script; until now this column had none, while D28-c read as
--   "已闭合" (closed). It was closed for **increments** only.
--
--   Simpler than the D32 backfill: there is no partial unique index and no
--   same-session-winner question — the join is a plain per-request_id copy.
--
-- IDEMPOTENT: the `t.client_protocol IS NULL` guard makes re-runs no-ops.
--   ONE-SHOT: run before `request_logs` is dropped, not after.
--
-- USAGE
--   psql "$DB_URL" -f sql/scripts/backfill_client_protocol.sql
--
-- NOTES (column reality, verified 2026-10-04)
--   - v1 `request_logs.client_protocol` is varchar, NULL for most rows
--     (35,185/2,278,971 non-NULL lifetime on the 710 view).
--   - v2 `session_turns.client_protocol` is varchar(50) NULLABLE.
--     ⚠ TRUNCATION RISK: if any v1 value exceeds 50 chars this UPDATE will
--     fail loudly (value too long) rather than truncate — that is the desired
--     fail-loud shape; if it fires, inspect the offending rows before
--     deciding to substring (none observed when the projection column was
--     cast to varchar(50) in db/request_logs_view_schema.go:457).
--   - SCHEMA-QUALIFIED ON PURPOSE (same reason as the D32 script: a `gateway`
--     schema with empty clones exists; never depend on search_path).
--   - hot faces are separate tables, not partitions — handled explicitly.
--
-- Author: llm-gateway-ops (R43 audit round)
-- Date: 2026-10-04

\timing on

\echo '=== BEFORE: client_protocol 非空数（历史区间预期接近 0）==='
SELECT 'session_turns_hot' AS face,
       count(client_protocol) AS filled,
       count(*) AS total
  FROM public.session_turns_hot;

DO $$
DECLARE
    v1_part text;
    v2_part text;
    n       bigint;
BEGIN
    FOR v1_part IN
        SELECT c.relname
          FROM pg_class c
          JOIN pg_inherits i   ON i.inhrelid = c.oid
          JOIN pg_class p      ON p.oid = i.inhparent
          JOIN pg_namespace n2 ON n2.oid = c.relnamespace
         WHERE p.relname = 'request_logs'
           AND n2.nspname = 'public'
           AND c.relkind = 'r'
         ORDER BY c.relname
    LOOP
        v2_part := replace(v1_part, 'request_logs_', 'session_turns_');

        IF to_regclass('public.' || v2_part) IS NULL THEN
            RAISE NOTICE 'SKIP  %: v2 面 public.% 不存在（该月份在 session 族无对应面）',
                 v1_part, v2_part;
            CONTINUE;
        END IF;

        EXECUTE format($q$
            UPDATE public.%I t
               SET client_protocol = v.client_protocol
              FROM ONLY public.%I v
             WHERE t.request_id = v.request_id
               AND t.client_protocol IS NULL
               AND v.client_protocol IS NOT NULL
        $q$, v2_part, v1_part);

        GET DIAGNOSTICS n = ROW_COUNT;
        RAISE NOTICE '% -> %: 回填 % 行', v1_part, v2_part, n;
    END LOOP;

    -- hot 两张脸都不是母表的分区，必须单独处理。
    IF to_regclass('public.request_logs_hot') IS NOT NULL
       AND to_regclass('public.session_turns_hot')  IS NOT NULL THEN
        UPDATE public.session_turns_hot t
           SET client_protocol = v.client_protocol
          FROM ONLY public.request_logs_hot v
         WHERE t.request_id = v.request_id
           AND t.client_protocol IS NULL
           AND v.client_protocol IS NOT NULL;
        GET DIAGNOSTICS n = ROW_COUNT;
        RAISE NOTICE 'request_logs_hot -> session_turns_hot: 回填 % 行', n;
    ELSE
        RAISE NOTICE 'SKIP  hot: 两张脸未同时存在';
    END IF;
END
$$;

\echo '=== AFTER: client_protocol 非空数 ==='
SELECT 'session_turns_hot' AS face,
       count(client_protocol) AS filled,
       count(*) AS total
  FROM public.session_turns_hot;

\echo '=== 残余缺口：v1 有值但 v2 无对应 turn（镜像排除类，属设计内）==='
SELECT count(*) AS v1_filled_without_v2_turn
  FROM ONLY public.request_logs_2026_09 v
 WHERE v.client_protocol IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM ONLY public.session_turns_2026_09 t
                    WHERE t.request_id = v.request_id);
\echo '   （internal_loopback / 非终态占位行按设计不镜像，预期少量。）'
