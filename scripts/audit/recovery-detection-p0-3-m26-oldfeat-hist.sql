-- ★★ 第 26 处取证（★ 2026-11-11 02:3x +08，★ 211 真库）：★ M14x 旧 featured 组的间隔分布直方图
-- ★★ 回答：★ 「post p90=360 vs pre p90=31」是行为变化还是**构成伪影**？
-- ★★ 实测结论：★ pre24（成熟段 18h）= n 226 ＝ 短 208 ＋ **6h 18（8.0%）** ⇒ p90 31
-- ★★   post @真实 now（~6.8h）= n 35 ＝ 短 29 ＋ **6h 6（17.1%）** ⇒ p90 360 ❌
-- ★★   post @as-of 13:42:30（~12.8h）= n 103 ＝ 短 97 ＋ **6h 6（5.8%）** ⇒ p90 31 ✅
-- ★★   ⇒ ★★ **6h 事件数「每批次固定」不随窗口增长，短事件随窗口线性增长 ⇒ 窗口越短占比越高**
--
-- ★★ ★ **单条语句**（★ `WITH` 只作用于紧随其后的那一条语句 ⇒ ★ 本文件必须把三段 UNION ALL 到同一条里）
SET TIME ZONE 'Asia/Shanghai';

WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv_old AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM models_canonical mc JOIN provider_models pm ON pm.canonical_id = mc.id WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 rawv_new AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m JOIN provider_models pm ON pm.canonical_id = mc.id WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st_old AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_old),
 st_new AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_new),
 newly AS (SELECT m FROM st_new WHERE m LIKE '%/%' AND m NOT IN (SELECT m FROM st_old)),
 fr AS (
   SELECT n.credential_id, n.raw_model_name, n.created_at, n.success, n.next_retry_seconds,
          (n.raw_model_name LIKE '%/%') is_ns
   FROM node_probe_runs n
   WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id = b.provider_model_id
                 WHERE b.credential_id = n.credential_id AND pm.raw_model_name = n.raw_model_name)
     AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
), seg AS (
   SELECT credential_id, raw_model_name, created_at, success, next_retry_seconds, is_ns,
          LEAD(created_at) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
), rec AS (
   SELECT is_ns, raw_model_name, created_at AS fail_at, next_at, next_retry_seconds,
          EXTRACT(EPOCH FROM (next_at-created_at))/60 AS recheck_min,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre' ELSE 'post' END era,
          CASE WHEN lower(btrim(raw_model_name)) IN (SELECT m FROM newly) THEN 'newly_covered'
               WHEN lower(btrim(raw_model_name)) IN (SELECT m FROM st_old) THEN 'old_feat'
               ELSE 'other' END grp_new
   FROM seg WHERE success = FALSE AND is_ns
), exp AS (
   SELECT r.*, 'pre24' win FROM rec r WHERE r.fail_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '24 hours'
                                          AND r.fail_at <  TIMESTAMPTZ '2026-11-10 13:42:30+08'
   UNION ALL
   SELECT r.*, 'post' FROM rec r WHERE r.fail_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08'
), mat AS (
   SELECT * FROM exp
   WHERE grp_new = 'old_feat' AND next_at IS NOT NULL
     AND ( (era='pre'  AND fail_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '6 hours'
                        AND next_at < TIMESTAMPTZ '2026-11-10 13:42:30+08')
        OR (era='post' AND fail_at < now() - interval '6 hours') )
)
-- ① 间隔直方图（★ 30 分钟桶）
SELECT 'A_hist' AS sec, win AS k1,
       (round(recheck_min/30.0)*30)::int::text || '-' || (round(recheck_min/30.0)*30+30)::int::text AS k2,
       count(*)::text AS k3,
       count(DISTINCT raw_model_name)::text AS k4,
       '' AS k5
FROM mat GROUP BY win, round(recheck_min/30.0)
UNION ALL
-- ② 两窗口的规模与 6h 事件占比（★ 直接回答「p90 被顶高是不是构成伪影」）
SELECT 'B_summary', win,
       count(*)::text,
       count(DISTINCT raw_model_name)::text,
       count(*) FILTER (WHERE recheck_min > 300)::text,
       'gt300_pct='||ROUND(100.0*count(*) FILTER (WHERE recheck_min > 300)/count(*),1)::text
       ||' | gt300_unhalved21600='||(count(*) FILTER (WHERE recheck_min > 300 AND next_retry_seconds = 21600))::text
       ||' | gt300_halved10800='||(count(*) FILTER (WHERE recheck_min > 300 AND next_retry_seconds = 10800))::text
       ||' | gt300_other='||(count(*) FILTER (WHERE recheck_min > 300 AND next_retry_seconds NOT IN (21600,10800)))::text
       ||' | p90='||percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min)::int::text
       ||' | p99='||percentile_disc(0.99) WITHIN GROUP (ORDER BY recheck_min)::int::text
FROM mat GROUP BY win
UNION ALL
-- ③ post 段按小时槽（★ 看 6h 事件是否集中在批次槽位）
SELECT 'C_post_slots', to_char(date_trunc('hour', fail_at),'MM-DD HH24'),
       count(*)::text,
       count(*) FILTER (WHERE recheck_min > 300)::text,
       percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min)::int::text,
       ''
FROM mat WHERE win='post' GROUP BY 2
ORDER BY 1,2,3;
