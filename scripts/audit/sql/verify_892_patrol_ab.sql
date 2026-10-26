-- §143.35 / P11 巡检脚本改造的 A/B 实测（本地真库，一次性库）
--
-- ★★★ 这是「零语义风险」这个结论的**唯一硬证据**：
--   巡检脚本的 base_buckets 有两条路：
--     新路 = 读每日 slot 汇总表 ursm_snapshot_slot_daily
--     旧路 = 扫源表 ursm_node_snapshot_min
--   ⇒ ★ 两条路必须给出【逐字相同的行集】（同一 slot、同一 c）。
--
-- ⚠⚠ 方案演进（第一次 A/B 实测 FAIL 逼出来的，重要）：
--   第一版新路 = 「汇总表覆盖整个 7 天窗」
--   ⇒ 实测 FAIL：**16 行 OLD_ONLY，全部是【今天】**。
--   ⇒⇒ 根因：★ **汇总表的填充范围是 [today - N, today)，【不含今天】**
--     （今天的数据还在写，不能算「每天一次」的稳定值）
--     ⇒ 而旧路读 `now - 7 days` 起 ⇒ 含今天 ⇒ ⇒ 两路键集合不同。
--   ⇒⇒ ★ 修正 = **方案 B**：
--     · 【过去的完整天】→ 汇总表（每天一次，稳定）
--     · 【今天】→ 仍读源表（今天还在变，本来就该实时读）
--     ⇒⇒ 两路用【同一个桶对齐窗口】+ 同一个「天」的划分 ⇒ 逐字相同。
--
-- ⚠ 夹具必须【有逐日波动】：若每天行数完全相同，窗口边界的差异会被平均掉
--   ⇒ 测不出任何问题 ⇒ 那是假绿。
--   ⇒ 本夹具按「昼夜节律 + 按天周内波动」的形态造数。

\set ON_ERROR_STOP on

DROP TABLE IF EXISTS public.ursm_snapshot_slot_daily;
DROP FUNCTION IF EXISTS public.refresh_ursm_snapshot_slot_daily(integer, text);
DROP TABLE IF EXISTS public.ursm_node_snapshot_min;

CREATE TABLE public.ursm_node_snapshot_min (
    id          bigserial PRIMARY KEY,
    snapshot_ts timestamptz NOT NULL,
    node_id     text,
    tenant_id   text,
    payload     jsonb,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- ★ 造 10 天的数据：每天 24 个小时位，每小时位 2 个桶（30 分钟一个），
--   每天每桶的行数随「天 + 小时位」变化（10~90 行）⇒ 逐日波动 ⇒ 测得出边界差异。
INSERT INTO public.ursm_node_snapshot_min (snapshot_ts, node_id)
SELECT ts, 'n' || (g % 7)
  FROM generate_series(
         (now() - interval '10 days')::timestamptz,
         now() - interval '1 hour',
         interval '30 minutes'
       ) AS ts,
       LATERAL generate_series(
         1,
         10 + ((EXTRACT(doy FROM ts)::int * 7 + EXTRACT(hour FROM ts)::int * 3) % 80)
       ) AS g;

SELECT 'source_rows' AS k, count(*)::text FROM public.ursm_node_snapshot_min;

-- ══ 应用迁移 892（含回填） ════════════════════════════════════════════════
-- ★ 本文件用占位符（容器里没有仓库路径）⇒ 由 verify_892_patrol_ab.sh 展开。
-- >>>MIGRATION_UP<<<

-- ══ 公共 CTE ═════════════════════════════════════════════════════════════
CREATE TEMP VIEW win AS
    SELECT date_trunc('hour', now()) - make_interval(hours => 3) AS lo,
           date_trunc('hour', now()) AS hi_excl;

-- ══ 旧路（脚本改造前的形态，窗口改成桶对齐） ═════════════════════════════
CREATE TEMP VIEW old_rows AS
    SELECT EXTRACT(hour FROM date_trunc('hour', snapshot_ts))::int AS slot,
           count(*) AS c,
           (snapshot_ts AT TIME ZONE current_setting('TimeZone'))::date AS day,
           date_trunc('hour', snapshot_ts) AS bucket_ts
      FROM public.ursm_node_snapshot_min, win
     WHERE snapshot_ts >= date_trunc('hour', now() - make_interval(days => 7))
       AND snapshot_ts <  (SELECT lo FROM win)
     GROUP BY 1, 3, 4;

-- ══ 新路（方案 B：过去天走汇总表 + 今天走源表） ══════════════════════════
CREATE TEMP VIEW new_rows AS
    SELECT d.slot AS slot, d.rows AS c, d.day AS day, d.bucket_ts AS bucket_ts
      FROM public.ursm_snapshot_slot_daily d, win
     WHERE d.granularity = 'hour'
       AND d.bucket_ts >= date_trunc('hour', now() - make_interval(days => 7))
       AND d.bucket_ts <  (SELECT lo FROM win)
       AND d.day <  (now() AT TIME ZONE current_setting('TimeZone'))::date
    UNION ALL
    SELECT EXTRACT(hour FROM date_trunc('hour', snapshot_ts))::int AS slot,
           count(*) AS c,
           (snapshot_ts AT TIME ZONE current_setting('TimeZone'))::date AS day,
           date_trunc('hour', snapshot_ts) AS bucket_ts
      FROM public.ursm_node_snapshot_min, win
     WHERE snapshot_ts >= date_trunc('hour', now() - make_interval(days => 7))
       AND snapshot_ts <  (SELECT lo FROM win)
       AND (snapshot_ts AT TIME ZONE current_setting('TimeZone'))::date
           >= (now() AT TIME ZONE current_setting('TimeZone'))::date
     GROUP BY 1, 3, 4;

SELECT 'rows_new' AS k, count(*)::text FROM new_rows
UNION ALL SELECT 'rows_old', count(*)::text FROM old_rows;

-- ★★★ 核心断言 1：行集与 c 必须逐字相同 ═════════════════════════════════
SELECT 'ab_identical' AS k,
       CASE WHEN NOT EXISTS (
           SELECT 1 FROM new_rows n FULL JOIN old_rows o
             ON o.slot = n.slot AND o.day = n.day AND o.bucket_ts = n.bucket_ts
            WHERE n.c IS DISTINCT FROM o.c OR n.slot IS NULL OR o.slot IS NULL
       ) THEN 'PASS' ELSE 'FAIL' END;

-- 差异明细（若有）
SELECT 'DIFF' AS k,
       coalesce(n.slot, o.slot) AS slot,
       coalesce(n.day, o.day) AS day,
       coalesce(n.bucket_ts, o.bucket_ts) AS bucket_ts,
       n.c AS new_c, o.c AS old_c,
       CASE WHEN n.bucket_ts IS NULL THEN 'OLD_ONLY'
            WHEN o.bucket_ts IS NULL THEN 'NEW_ONLY'
            ELSE 'BOTH_DIFF' END AS kind
  FROM new_rows n FULL JOIN old_rows o
    ON o.slot = n.slot AND o.day = n.day AND o.bucket_ts = n.bucket_ts
 WHERE n.c IS DISTINCT FROM o.c OR n.slot IS NULL OR o.slot IS NULL
 ORDER BY 6, 4 LIMIT 25;

-- ★★★ 核心断言 2：判定层的中位数（p50）必须逐字相同 ═════════════════════
CREATE TEMP VIEW old_p50 AS
    SELECT slot, percentile_cont(0.5) WITHIN GROUP (ORDER BY c) AS p50
      FROM (SELECT slot, c FROM old_rows) t GROUP BY 1;
CREATE TEMP VIEW new_p50 AS
    SELECT slot, percentile_cont(0.5) WITHIN GROUP (ORDER BY c) AS p50
      FROM (SELECT slot, c FROM new_rows) t GROUP BY 1;

SELECT 'p50_identical' AS k,
       CASE WHEN NOT EXISTS (
           SELECT 1 FROM new_p50 n FULL JOIN old_p50 o ON o.slot = n.slot
            WHERE n.p50 IS DISTINCT FROM o.p50 OR n.slot IS NULL OR o.slot IS NULL
       ) THEN 'PASS' ELSE 'FAIL' END;

-- ★★★ 核心断言 3：★ 判定【结论】必须相同（不是只比中位数）═══════════════
-- ★ 这是最强的一条：把两路喂进巡检真正用的那个 CASE 表达式，
--   看每个桶的判定（ok / VOID / SLOW / SPARSE / NO-BASELINE）是否一致。
CREATE TEMP VIEW verdict AS
SELECT n.slot, n.c AS cur_c,
       CASE WHEN o.p50 IS NULL OR o.p50 <= 0 THEN 'NO-BASELINE'
            WHEN n.c = 0                    THEN 'VOID'
            WHEN n.c >= 0.5 * o.p50        THEN 'ok'
            ELSE 'SLOW' END AS v_old,
       CASE WHEN p.p50 IS NULL OR p.p50 <= 0 THEN 'NO-BASELINE'
            WHEN n.c = 0                    THEN 'VOID'
            WHEN n.c >= 0.5 * p.p50        THEN 'ok'
            ELSE 'SLOW' END AS v_new
  FROM new_rows n
  LEFT JOIN old_p50 o ON o.slot = n.slot
  LEFT JOIN new_p50 p ON p.slot = n.slot;

SELECT 'verdict_identical' AS k,
       CASE WHEN NOT EXISTS (SELECT 1 FROM verdict WHERE v_old IS DISTINCT FROM v_new)
            THEN 'PASS' ELSE 'FAIL' END;

-- ★ 先删掉依赖源表的临时视图，再删源表（否则 DROP 报依赖错误）。
DROP VIEW IF EXISTS verdict, new_p50, old_p50, new_rows, old_rows, win;
DROP TABLE public.ursm_node_snapshot_min;
