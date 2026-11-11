-- ═══ ★★★ 第 31 处口径修正（★ 2026-11-11 04:4x +08）：★ M5 的「≥7 天基线」到底会给什么 ⇒ ★★ 关窗前先把 ❌ 的可归因性算清 ═══
--
-- ★★ **为什么需要它**：★ M5 的 base = **9242**（★ 冻结的 pre24 **单日快照**）⇒ ★ limit = floor(9242×1.01) = **9334**
--   ⇒ ★★ 诚实边界 ㉘ 声明「该阈值在 pre 期就不成立 ⇒ ❌ 不可归因于修复」⇒ ★★ **真修法**（★ ≥7 天的均值/中位/分位做基线）
--   **登记为 Stage 2 之后**（★ 关窗前不重冻）。
--   ⇒ ★★★ **但「登记之后」不等于「关窗时不会误读」**：★ 若在**正确的**（★ 等长、同量纲）7 天基线下 post **仍然超**，
--   ⇒ ★★ 那 ㉘ 的「不可归因」就**不成立**（★ ❌ 变成**可归因的真实增量**）⇒ ★★ ⇒ ★★ **这必须在关窗前算清**，
--   ⇒ ★★ 否则关窗报告会**用一条已失效的披露**给 ❌ 背书。
--
-- ★★ **本脚本是「只读诊断」**：★ **不改 base / 不改 limit / 不新增读数键 / 不进 metrics.sql**
--   ⇒ ★★ ⇒ ★★ **不触发 M18 重冻**（★ 与 `M5_pre7d_runs_by_day` 同款定位：★ 只出证据、不动已冻结基线）
--
-- ★★ **口径纪律（本脚本的核心）**：
--   ① ★★ M5 的窗口是 **滚动 24h**（★ `created_at > now()-24h AND created_at <= now()`）
--      ⇒ ★★ 基线必须取**等长的 24h 窗口**（★ 滚动 24h 逐小时采样）⇒ ★★ **同量纲**
--   ② ★★ **不得**用 `M5_pre7d_runs_by_day` 的**日历日桶**均值/中位当 24h 基线
--      ⇒ ★★ 因为 7 天窗口跨 **8 个日历日** ⇒ ★★ 首尾两桶是 **partial day**（★ 11-03 只覆盖 10.3h、★ 11-10 只到 13:42:30）
--      ⇒ ★★ **partial day 必然低估** ⇒ ★★ 会**系统性压低**基线 ⇒ ★★ ⇒ 那是**另一个口径缺陷**（★ 本脚本第 2 段量化它）
--   ③ ★★ 「跨段比必须等长 + 同相位」（★ LESSONS §667 / §679）—— ★ 这里体现为「**长度必须都是 24h**」

-- ── ① 等长滚动 24h 窗口在 pre7d 上的分布（★ n = 145 个逐小时采样点）──
WITH cut AS (SELECT TIMESTAMPTZ '2026-11-10 13:42:30+08' AS t),
samples AS (
  SELECT gs AS t_end
  FROM cut c, LATERAL generate_series(c.t - interval '7 days' + interval '24 hours', c.t, interval '1 hour') gs
),
roll AS (
  SELECT s.t_end,
         (SELECT COUNT(*) FROM node_probe_runs r
          WHERE r.created_at > s.t_end - interval '24 hours'
            AND r.created_at <= s.t_end) AS c
  FROM samples s
)
SELECT 'ROLL24_pre7d' k,
       'n=' || COUNT(*)::text
       || ' | min=' || MIN(c)::text
       || ' | mean=' || ROUND(AVG(c)::numeric, 1)::text
       || ' | p50=' || (percentile_disc(0.50) WITHIN GROUP (ORDER BY c))::text
       || ' | p75=' || (percentile_disc(0.75) WITHIN GROUP (ORDER BY c))::text
       || ' | p90=' || (percentile_disc(0.90) WITHIN GROUP (ORDER BY c))::text
       || ' | p95=' || (percentile_disc(0.95) WITHIN GROUP (ORDER BY c))::text
       || ' | max=' || MAX(c)::text
       || ' | sum=' || SUM(c)::text
       || ' | each_window_hours=24'
  FROM roll;

-- ── ② 日历日桶 + 每个桶**实际覆盖小时数** ⇒ 量化「partial day 低估」──
WITH cut AS (SELECT TIMESTAMPTZ '2026-11-10 13:42:30+08' AS t),
buckets AS (
  SELECT date_trunc('day', r.created_at) AS d0,
         COUNT(*) AS c,
         MIN(r.created_at) AS c0,
         MAX(r.created_at) AS c1
  FROM node_probe_runs r, cut k
  WHERE r.created_at > k.t - interval '7 days'
    AND r.created_at <= k.t
  GROUP BY date_trunc('day', r.created_at)
),
cov AS (
  SELECT b.*,
         EXTRACT(EPOCH FROM (LEAST((b.d0 + interval '1 day'), k.t) - GREATEST(b.d0, k.t - interval '7 days'))) / 3600.0 AS hrs
  FROM buckets b, cut k
)
SELECT 'CALDAY_pre7d_coverage' k,
       string_agg(to_char(d0, 'MM-DD') || ':' || c::text || '(' || ROUND(hrs, 2)::text || 'h'
                  || '⇒24h折算' || ROUND((c / hrs * 24))::text || ')',
                  ',' ORDER BY d0) AS v
FROM cov;

-- ── ③ post 段现状（★ [cut, now]）⇒ 含速率与关窗投影（★ 投影只是投影）──
WITH cut AS (SELECT TIMESTAMPTZ '2026-11-10 13:42:30+08' AS t),
el AS (SELECT EXTRACT(EPOCH FROM (now() - t)) / 3600.0 AS hrs FROM cut),
agg AS (SELECT COUNT(*)::numeric AS c FROM node_probe_runs r, cut k
        WHERE r.created_at > k.t AND r.created_at <= now())
SELECT 'POST_seg_now' k,
       'cut=2026-11-10 13:42:30+08'
       || ' | now=' || to_char(now(), 'YYYY-MM-DD HH24:MI:SS')
       || ' | elapsed_h=' || ROUND(el.hrs::numeric, 2)::text
       || ' | runs=' || agg.c::text
       || ' | rate_per_h=' || ROUND((agg.c / el.hrs)::numeric, 1)::text
       || ' | proj_24h=' || ROUND((agg.c / el.hrs * 24)::numeric, 0)::text
       || ' | proj_23h=' || ROUND((agg.c / el.hrs * 23)::numeric, 0)::text
       AS v
FROM el, agg;

-- ── ④ 候选基线对照：★ base 候选 ⇒ limit = floor(base×1.01) ⇒ 与 post 现读数 / 关窗投影比 ──
WITH cut AS (SELECT TIMESTAMPTZ '2026-11-10 13:42:30+08' AS t),
samples AS (
  SELECT gs AS t_end
  FROM cut c, LATERAL generate_series(c.t - interval '7 days' + interval '24 hours', c.t, interval '1 hour') gs
),
roll AS (
  SELECT s.t_end,
         (SELECT COUNT(*) FROM node_probe_runs r
          WHERE r.created_at > s.t_end - interval '24 hours'
            AND r.created_at <= s.t_end) AS c
  FROM samples s
),
stat AS (
  SELECT ROUND(AVG(c)::numeric, 1) AS mean_c,
         (percentile_disc(0.50) WITHIN GROUP (ORDER BY c))::int AS p50_c,
         (percentile_disc(0.75) WITHIN GROUP (ORDER BY c))::int AS p75_c,
         (percentile_disc(0.90) WITHIN GROUP (ORDER BY c))::int AS p90_c,
         MAX(c)::int AS max_c
  FROM roll
),
cands AS (
  SELECT * FROM (VALUES
    ('frozen_pre24_snapshot', 9242),
    ('roll24_pre7d_mean',     (SELECT mean_c::int FROM stat)),
    ('roll24_pre7d_p50',      (SELECT p50_c FROM stat)),
    ('roll24_pre7d_p75',      (SELECT p75_c FROM stat)),
    ('roll24_pre7d_p90',      (SELECT p90_c FROM stat)),
    ('roll24_pre7d_max',      (SELECT max_c FROM stat))
  ) v(name, base)
),
post_now AS (
  SELECT COUNT(*)::int AS c FROM node_probe_runs WHERE created_at > now() - interval '24 hours' AND created_at <= now()
),
el AS (SELECT EXTRACT(EPOCH FROM (now() - t)) / 3600.0 AS hrs FROM cut),
agg AS (SELECT COUNT(*)::numeric AS c FROM node_probe_runs r, cut k
        WHERE r.created_at > k.t AND r.created_at <= now()),
post_seg AS (SELECT (SELECT c FROM agg) AS c, (SELECT hrs FROM el) AS hrs)
SELECT 'BASE_CANDIDATES' k,
       string_agg(
         c.name || ':base=' || c.base::text
         || ',limit=' || floor(c.base * 1.01)::text
         || ',post_now=' || pn.c::text
         || ',post_proj24=' || ROUND((ps.c / ps.hrs * 24))::text
         || ',now=' || CASE WHEN pn.c <= floor(c.base * 1.01) THEN 'PASS' ELSE 'FAIL' END
         || ',proj=' || CASE WHEN ROUND(ps.c / ps.hrs * 24) <= floor(c.base * 1.01) THEN 'PASS' ELSE 'FAIL' END
         || ',post_vs_base=' || ROUND((pn.c::numeric / c.base) * 100, 1)::text || '%'
       , ' || ' ORDER BY c.base DESC) AS v
FROM cands c, post_now pn, post_seg ps;

-- ── ⑤ 冻结阈值 9334 在 pre7d 等长滚动 24h 分布里的**分位** ⇒ 量化「阈值在 pre 期就不成立」──
WITH cut AS (SELECT TIMESTAMPTZ '2026-11-10 13:42:30+08' AS t),
samples AS (
  SELECT gs AS t_end
  FROM cut c, LATERAL generate_series(c.t - interval '7 days' + interval '24 hours', c.t, interval '1 hour') gs
),
roll AS (
  SELECT s.t_end,
         (SELECT COUNT(*) FROM node_probe_runs r
          WHERE r.created_at > s.t_end - interval '24 hours'
            AND r.created_at <= s.t_end) AS c
  FROM samples s
)
SELECT 'THRESH_RANK' k,
       'n=' || COUNT(*)::text
       || ' | le_9334=' || COUNT(*) FILTER (WHERE c <= 9334)::text
       || ' | gt_9334=' || COUNT(*) FILTER (WHERE c > 9334)::text
       || ' | pct_below=' || ROUND((COUNT(*) FILTER (WHERE c <= 9334)::numeric / COUNT(*) * 100), 1)::text || '%'
       || ' | median=' || (percentile_disc(0.50) WITHIN GROUP (ORDER BY c))::text
       || ' | p75=' || (percentile_disc(0.75) WITHIN GROUP (ORDER BY c))::text
  FROM roll;

-- ── ⑥ pre24 侧 by_kind（★ 锚定切点 ⇒ 时钟无关）⇒ 与 post 侧 by_kind 对拍 ⇒ 增量的来源 ──
WITH cut AS (SELECT TIMESTAMPTZ '2026-11-10 13:42:30+08' AS t),
fr AS (
  SELECT DISTINCT b.credential_id, pm.raw_model_name
  FROM credential_model_bindings b
  JOIN provider_models pm ON pm.id = b.provider_model_id
  WHERE b.billing_mode = 'free'
),
pk AS (
  SELECT CASE WHEN trigger_kind IN ('credential_recovery','periodic','sync_request','request_failure',
                                    'manual','admin','integrity_probe_planner','selfcheck','external_async')
              THEN trigger_kind ELSE 'other' END AS kind,
         COUNT(*) AS n
  FROM node_probe_runs r, cut c
  WHERE r.created_at > c.t - interval '24 hours' AND r.created_at <= c.t
  GROUP BY 1
),
tot AS (SELECT COUNT(*) AS n FROM node_probe_runs r, cut c
        WHERE r.created_at > c.t - interval '24 hours' AND r.created_at <= c.t)
SELECT 'PRE24_BY_KIND' k,
       string_agg(pk.kind || '=' || pk.n::text || '('
                  || ROUND((pk.n::numeric / tot.n) * 100, 1)::text || '%)',
                  ' | ' ORDER BY pk.n DESC) AS v
FROM pk, tot;
