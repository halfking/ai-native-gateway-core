-- Backfill session_turns.is_final_success from the v1 final-success claim
-- (audit §9.203, decision D32).
--
-- WHY THIS EXISTS
--   `session_turns.is_final_success` was never written by any code path. The
--   column, the INSERT in turn_writer.go and the partial unique indexes
--   (uq_session_turns_hot_final_success / uq_session_turns_final_success /
--   one per monthly partition) were all in place — the uniqueness guarantee was
--   enforced against a set that was always empty. Measured on llm-gateway-pg
--   (2026-10-05): v1 request_logs_hot had 463 rows with is_final_success=TRUE
--   in 7 days; session_turns had 0 of 1,689,308 rows with the column non-NULL
--   (NOT NULL count 0, not TRUE count 0 — the column was never written).
--
--   Consequence: the write path is now fixed (de9be4a8f, final_success_turn.go),
--   but history is not. `admin/session_online.go` moved the session timeline to
--   the session family on 2026-09-30 and derives `final_success` /
--   `superseded_success` from this column, so every existing session renders
--   every successful turn as a plain `success`.
--
--   This script restores the marks for rows already present. It is a ONE-SHOT
--   repair that stops mattering once the v1 tables are retired — run it before
--   `request_logs` is dropped, not after.
--
-- IDEMPOTENT: re-runs are no-ops. The `is_final_success IS NOT TRUE` guard
--   means a second run matches 0 rows, and the per-partition unique index
--   rejects any second winner for a session. Safe to re-run after a partial
--   failure.
--
-- USAGE
--   psql "$DB_URL" -f sql/scripts/backfill_final_success_marks.sql
--
--   Measured on llm-gateway-pg (llm_gateway, 2026-10-05), whole script inside
--   one transaction and then rolled back:
--     request_logs_2026_07 -> session_turns_2026_07:     0 rows
--     request_logs_2026_08 -> session_turns_2026_08:     0 rows
--     request_logs_2026_09 -> session_turns_2026_09: 107,756 rows  76.4s
--     request_logs_2026_10 -> session_turns_2026_10:   2,328 rows   0.50s
--     request_logs_2026_11 / default:                    0 rows
--     request_logs_hot   -> session_turns_hot:           395 rows   0.08s
--     script total (incl. the verification queries):       89.0s
--   110,479 rows marked, cost dominated by index maintenance on the large
--   monthly partition: `is_final_success` is in the PREDICATE of the partial
--   unique index, so every updated row is non-HOT and rewrites every index on
--   the table (0.71 ms/row on 2026_09 vs ~0.20 ms/row on the small faces).
--   This scales with the number of v1 final-success rows, NOT with the size of
--   session_turns — a fresh install does no work at all.
--
--   The script runs as ONE transaction (~89s of locks locally). If your
--   `promote_session_turns_hot_to_partition` runs concurrently it will block
--   for the duration; schedule accordingly. The cost is one-shot: it is
--   dominated by v1 volume, which stops growing when `request_logs` is retired.
--
-- NOTES (column reality, verified against the live database)
--   - v1 `request_logs.is_final_success` is boolean NOT NULL DEFAULT false.
--   - v2 `session_turns.is_final_success` is boolean NULLABLE. Rows never
--     claimed are NULL, not false; the guard uses `IS NOT TRUE` so it covers
--     both false and NULL.
--   - Join is on `request_id` alone, which is the only key present on both
--     faces. It is UNIQUE among v1 final-success rows (verified: 107,794 rows
--     / 107,794 distinct request_id in 2026_09), so UPDATE..FROM is
--     unambiguous.
--   - ⚠ SCHEMA-QUALIFIED ON PURPOSE. This database also carries a `gateway`
--     schema holding empty `session_turns_2026_07/08/09` clones. `search_path`
--     is `public, llm_gateway` so unqualified names happen to resolve to
--     public, but a script that will run against production must not depend on
--     search_path. Every identifier below is explicit.
--   - v1 `request_logs_hot` / v2 `session_turns_hot` are NOT partitions of
--     their mother tables; they are separate tables and are handled explicitly
--     at the end. Forgetting this silently skips the last 8 hours of traffic.
--   - The v1/v2 partition names are derived by prefix substitution, so a
--     partition present on only one face is skipped with a NOTICE rather than
--     failing the run.
--
-- Author: llm-gateway-ops
-- Date: 2026-10-05

\timing on

\echo '=== BEFORE: 已有标记数（应为 0 或接近 0）==='
SELECT 'session_turns_hot' AS face,
       count(*) FILTER (WHERE is_final_success IS NOT NULL) AS marked,
       count(*) AS total
  FROM public.session_turns_hot;

\echo '=== 回填开始（按 v1 分区逐面配对）==='

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
               SET is_final_success = TRUE
              FROM ONLY public.%I v
             WHERE t.request_id = v.request_id
               AND v.is_final_success
               AND t.is_final_success IS NOT TRUE
        $q$, v2_part, v1_part);

        GET DIAGNOSTICS n = ROW_COUNT;
        RAISE NOTICE '% -> %: 标记 % 行', v1_part, v2_part, n;
    END LOOP;

    -- hot 两张脸都不是母表的分区，必须单独处理。
    IF to_regclass('public.request_logs_hot') IS NOT NULL
       AND to_regclass('public.session_turns_hot')  IS NOT NULL THEN
        UPDATE public.session_turns_hot t
           SET is_final_success = TRUE
          FROM ONLY public.request_logs_hot v
         WHERE t.request_id = v.request_id
           AND v.is_final_success
           AND t.is_final_success IS NOT TRUE;
        GET DIAGNOSTICS n = ROW_COUNT;
        RAISE NOTICE 'request_logs_hot -> session_turns_hot: 标记 % 行', n;
    ELSE
        RAISE NOTICE 'SKIP  hot: 两张脸未同时存在';
    END IF;
END
$$;

\echo '=== AFTER: 标记数 ==='
SELECT 'session_turns_hot' AS face,
       count(*) FILTER (WHERE is_final_success IS NOT NULL) AS marked,
       count(*) AS total
  FROM public.session_turns_hot;

\echo '=== 逐会话唯一性核对：不得出现同会话两枚标记 ==='
SELECT count(*) AS sessions_with_two_marks
  FROM (
        SELECT tenant_id, session_id
          FROM public.session_turns
         WHERE is_final_success IS TRUE
         GROUP BY tenant_id, session_id
        HAVING count(*) > 1
       ) d;
\echo '   （期望 0。>0 说明 v1 侧本身就有同会话两个 winner，请人工核对。）'

\echo '=== 残余缺口：v1 有 winner 但 v2 无对应 turn（无法修复，属镜像排除类）==='
SELECT count(*) AS v1_winners_without_v2_turn
  FROM ONLY public.request_logs_2026_09 v
 WHERE v.is_final_success
   AND NOT EXISTS (SELECT 1 FROM ONLY public.session_turns_2026_09 t
                    WHERE t.request_id = v.request_id);
\echo '   （这是下限：internal_loopback / 非终态占位行按设计不镜像，预期几十行。）'
