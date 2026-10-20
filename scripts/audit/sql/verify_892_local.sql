-- §143.35 / P11 迁移 892 的本地真库实测脚本（一次性库，退出即 DROP）
--
-- 目的：证明
--   ① 迁移 892 能在真库上跑通（建表 + 建函数 + **首次回填**）；
--   ② ★★ 填充函数的产出与「直接算 base_buckets」**判定等价**（零语义风险的核心断言）；
--   ③ 幂等：重跑两次，行数与内容都不变；
--   ④ 首次回填让表**立刻非空**（否则巡检会失明）；
--   ⑤ 回滚（down）干净。
--
-- ⚠ 只在【一次性库】里跑；不碰任何生产库。

\set ON_ERROR_STOP on

DROP TABLE IF EXISTS public.ursm_snapshot_slot_daily;
DROP FUNCTION IF EXISTS public.refresh_ursm_snapshot_slot_daily(integer, text);
DROP TABLE IF EXISTS public.ursm_node_snapshot_min;

-- ── 造一个与生产同形的最小源表（列名/类型对齐 453 + 818） ────────────────
CREATE TABLE public.ursm_node_snapshot_min (
    id           bigserial PRIMARY KEY,
    snapshot_ts  timestamptz NOT NULL,
    node_id      text,
    tenant_id    text,
    payload      jsonb,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- 造 10 天的数据：每天 24 个小时位，每小时位 2 个桶，每个桶 10 行
INSERT INTO public.ursm_node_snapshot_min (snapshot_ts, node_id)
SELECT ts, 'n' || (g % 7)
  FROM generate_series(
         (now() - interval '10 days')::timestamptz,
         now() - interval '1 hour',
         interval '30 minutes'
       ) AS ts,
       LATERAL generate_series(1, 10) AS g;

SELECT 'source_rows' AS k, count(*)::text FROM public.ursm_node_snapshot_min
UNION ALL SELECT 'source_days', count(DISTINCT (snapshot_ts AT TIME ZONE current_setting('TimeZone'))::date)::text
         FROM public.ursm_node_snapshot_min;

-- ══ 应用迁移 892（含首次回填） ═══════════════════════════════════════════
-- ★ 本文件用占位符，因为容器里没有仓库路径 ⇒ 由 verify_892_local.sh 展开。
-- >>>MIGRATION_UP<<<

-- ── 断言 1：表与函数都在 ────────────────────────────────────────────────
SELECT 'obj_table' AS k, to_regclass('public.ursm_snapshot_slot_daily')::text
UNION ALL SELECT 'obj_func',
       to_regprocedure('public.refresh_ursm_snapshot_slot_daily(integer, text)')::text;

-- ── 断言 4（★ 最重要）：首次回填后表必须【非空】──
-- ★ 若表是空的 ⇒ 巡检读到「没有基线」⇒ 所有桶 NO-BASELINE ⇒ 巡检失明。
SELECT 'backfill_nonempty' AS k,
       CASE WHEN (SELECT count(*) FROM public.ursm_snapshot_slot_daily) > 0
            THEN 'PASS' ELSE 'FAIL' END;
SELECT 'backfill_days' AS k, count(DISTINCT day)::text FROM public.ursm_snapshot_slot_daily;

-- ── 断言 2：按天汇总与源表逐行相同（只比较【表覆盖范围内的日】）──
-- ★★★ 注意两个边界（实测踩过，断言写错会得到假 FAIL）：
--   ① 填充范围是 [today - p_days_back, today)，**不含今天**
--      ⇒ 源表里「今天」那部分必然不在汇总表里；
--   ② 保留期是 9 天 ⇒ **比保留期更早的源数据也不在汇总表里**
--      ⇒ ★★ 比较时必须【两边都限定在同一窗口内】，否则会得到「窗口外的天」这一假差异。
--   ⇒ ⇒ 正确写法：**以汇总表覆盖的日期集合为准**，逐日比对源表。
WITH agg_by_day AS (
    -- ★ 必须按粒度分开：hour 与 minute 两套 ⇒ 不分组会把行数翻倍
    SELECT day, sum(rows) AS rows FROM public.ursm_snapshot_slot_daily
     WHERE granularity = 'hour' GROUP BY 1
),
src_by_day AS (
    SELECT (snapshot_ts AT TIME ZONE current_setting('TimeZone'))::date AS day, count(*) AS rows
      FROM public.ursm_node_snapshot_min
     WHERE (snapshot_ts AT TIME ZONE current_setting('TimeZone'))::date IN (SELECT day FROM agg_by_day)
     GROUP BY 1
)
SELECT 'per_day_match' AS k,
       CASE WHEN NOT EXISTS (
           SELECT 1 FROM src_by_day s
           FULL JOIN agg_by_day a ON a.day = s.day
           WHERE s.rows IS DISTINCT FROM a.rows OR s.day IS NULL OR a.day IS NULL
       ) THEN 'PASS' ELSE 'FAIL' END;

-- ── 断言 2b：★ 汇总表覆盖的天数必须 = 回填窗口（8 天），不多不少 ──────────
SELECT 'covered_days' AS k,
       CASE WHEN (SELECT count(DISTINCT day) FROM public.ursm_snapshot_slot_daily) = 8
            THEN 'PASS' ELSE 'FAIL' END;

-- ── 断言 5：★ 与「巡检侧要用的查询形态」判定等价（base_buckets 的新旧两态）───
-- ★ 巡检的 base_buckets（旧）：按 (slot, 桶) 分组计数，再对每个 slot 取中位数。
-- ★ 巡检的 base_buckets（新）：读汇总表，再对每个 slot 取中位数。
-- ★ 两者在【同一个 7 天窗】上必须给出【同一个 p50】⇒ 这才是「判定输入逐字相同」。
WITH win AS (
    SELECT date_trunc('hour', now()) - interval '3 hours' AS lo, date_trunc('hour', now()) AS hi_excl
),
old_form AS (
    SELECT slot, percentile_cont(0.5) WITHIN GROUP (ORDER BY c) AS p50
      FROM (
        SELECT EXTRACT(hour FROM snapshot_ts AT TIME ZONE current_setting('TimeZone'))::int AS slot,
               count(*) AS c
          FROM public.ursm_node_snapshot_min, win
         WHERE snapshot_ts >= now() - interval '7 days'
           AND snapshot_ts <  (SELECT lo FROM win)
         GROUP BY 1, date_trunc('hour', snapshot_ts)
      ) t
     GROUP BY 1
),
new_form AS (
    SELECT slot, percentile_cont(0.5) WITHIN GROUP (ORDER BY c) AS p50
      FROM (
        SELECT slot, rows AS c
          FROM public.ursm_snapshot_slot_daily
         WHERE granularity = 'hour'
           AND day >= (current_date - 7)
           AND day <  current_date
      ) t
     GROUP BY 1
)
SELECT 'p50_match_hour' AS k,
       CASE WHEN NOT EXISTS (
           SELECT 1 FROM old_form o FULL JOIN new_form n ON n.slot = o.slot
            WHERE o.p50 IS DISTINCT FROM n.p50 OR o.slot IS NULL OR n.slot IS NULL
       ) THEN 'PASS' ELSE 'FAIL' END;

-- ★★ 同一断言的 minute 粒度版（两种粒度必须都等价）
WITH win AS (
    SELECT date_trunc('minute', now()) - interval '3 hours' AS lo, date_trunc('minute', now()) AS hi_excl
),
old_form AS (
    SELECT slot, percentile_cont(0.5) WITHIN GROUP (ORDER BY c) AS p50
      FROM (
        SELECT (extract(hour FROM snapshot_ts)::int * 60 + extract(minute FROM snapshot_ts)::int) AS slot,
               count(*) AS c
          FROM public.ursm_node_snapshot_min, win
         WHERE snapshot_ts >= now() - interval '7 days'
           AND snapshot_ts <  (SELECT lo FROM win)
         GROUP BY 1, date_trunc('minute', snapshot_ts)
      ) t
     GROUP BY 1
),
new_form AS (
    SELECT slot, percentile_cont(0.5) WITHIN GROUP (ORDER BY c) AS p50
      FROM (
        SELECT slot, rows AS c
          FROM public.ursm_snapshot_slot_daily
         WHERE granularity = 'minute'
           AND day >= (current_date - 7)
           AND day <  current_date
      ) t
     GROUP BY 1
)
SELECT 'p50_match_minute' AS k,
       CASE WHEN NOT EXISTS (
           SELECT 1 FROM old_form o FULL JOIN new_form n ON n.slot = o.slot
            WHERE o.p50 IS DISTINCT FROM n.p50 OR o.slot IS NULL OR n.slot IS NULL
       ) THEN 'PASS' ELSE 'FAIL' END;

-- ★ 两种粒度必须【分开存】（不能混）：hour 的桶是小时对齐、minute 是分钟对齐
SELECT 'granularity_separated' AS k,
       CASE WHEN (SELECT count(DISTINCT granularity) FROM public.ursm_snapshot_slot_daily) = 2
             AND NOT EXISTS (
                 SELECT 1 FROM public.ursm_snapshot_slot_daily
                  WHERE granularity = 'hour' AND date_trunc('hour', bucket_ts) <> bucket_ts
             )
             AND NOT EXISTS (
                 SELECT 1 FROM public.ursm_snapshot_slot_daily
                  WHERE granularity = 'minute' AND date_trunc('minute', bucket_ts) <> bucket_ts
             )
            THEN 'PASS' ELSE 'FAIL' END;

-- ── 断言 3：幂等（重跑两次，内容不变） ───────────────────────────────────
CREATE TEMP TABLE snap1 AS SELECT * FROM public.ursm_snapshot_slot_daily ORDER BY day, slot, bucket_ts;

SELECT 'refresh_2' AS k, public.refresh_ursm_snapshot_slot_daily(8, 'hour')::text;
SELECT 'refresh_3' AS k, public.refresh_ursm_snapshot_slot_daily(8, 'hour')::text;

SELECT 'idempotent' AS k,
       CASE WHEN NOT EXISTS (
           SELECT 1 FROM snap1 s
           FULL JOIN (SELECT * FROM public.ursm_snapshot_slot_daily ORDER BY day, slot, bucket_ts) a
             ON a.day = s.day AND a.slot = s.slot AND a.bucket_ts = s.bucket_ts
           WHERE s.rows IS DISTINCT FROM a.rows OR s.day IS NULL OR a.day IS NULL
       ) THEN 'PASS' ELSE 'FAIL' END;

-- ── 断言 6：源表不存在时函数静默返回 0（不炸） ───────────────────────────
ALTER TABLE public.ursm_node_snapshot_min RENAME TO ursm_node_snapshot_min_tmp;
SELECT 'missing_src_ok' AS k,
       CASE WHEN public.refresh_ursm_snapshot_slot_daily(1) = 0 THEN 'PASS' ELSE 'FAIL' END;
ALTER TABLE public.ursm_node_snapshot_min_tmp RENAME TO ursm_node_snapshot_min;

-- ── 断言 7：参数守卫（p_days_back < 1 必须报错） ─────────────────────────
DO $t$
BEGIN
    BEGIN
        PERFORM public.refresh_ursm_snapshot_slot_daily(0);
        RAISE NOTICE 'guard_test: FAIL (no exception raised)';
    EXCEPTION WHEN OTHERS THEN
        RAISE NOTICE 'guard_test: PASS (%)', SQLERRM;
    END;
END;
$t$;

-- ── 断言 8：保留期（表本身不会单调增长） ─────────────────────────────────
SELECT 'retention_ok' AS k,
       CASE WHEN (SELECT count(DISTINCT day) FROM public.ursm_snapshot_slot_daily) <= 9
            THEN 'PASS' ELSE 'FAIL' END;

-- ══ 回滚（down） ═══════════════════════════════════════════════════════
-- >>>MIGRATION_DOWN<<<

SELECT 'after_down_table' AS k, coalesce(to_regclass('public.ursm_snapshot_slot_daily')::text, '(gone)')
UNION ALL SELECT 'after_down_func',
       coalesce(to_regprocedure('public.refresh_ursm_snapshot_slot_daily(integer, text)')::text, '(gone)');

DROP TABLE public.ursm_node_snapshot_min;
