-- ★★ 第 26 处取证（★ 2026-11-11 02:4x +08，★ 211 真库）：★ M14x 旧 featured 的**触发类型构成**
-- ★★ 用途：★ ① 查 pre24 vs post 的 trigger_kind 构成 ② **把 now() 换成关窗时钟**看成熟段变大时读数怎么动
-- ★★ 用法（★ 两个时钟对照）：★ sed "s/now() - interval '6 hours'/(TIMESTAMPTZ '2026-11-11 13:42:30+08') - interval '6 hours'/g" 本文件 > /tmp/k.sql
--
-- ★★ ★ **单条语句**（★ `WITH` 只作用于紧随其后的那一条语句 ⇒ ★ 本文件必须把两段 UNION ALL 到同一条里）
SET TIME ZONE 'Asia/Shanghai';

WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv_old AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM models_canonical mc JOIN provider_models pm ON pm.canonical_id = mc.id WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 rawv_new AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m JOIN provider_models pm ON pm.canonical_id = mc.id WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st_old AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_old),
 st_new AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_new),
 newly AS (SELECT m FROM st_new WHERE m LIKE '%/%' AND m NOT IN (SELECT m FROM st_old)),
 fr AS (
   SELECT n.credential_id, n.raw_model_name, n.created_at, n.success, n.next_retry_seconds, n.trigger_kind,
          (n.raw_model_name LIKE '%/%') is_ns,
          LEAD(n.trigger_kind) OVER (PARTITION BY n.credential_id, n.raw_model_name ORDER BY n.created_at) next_kind
   FROM node_probe_runs n
   WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id = b.provider_model_id
                 WHERE b.credential_id = n.credential_id AND pm.raw_model_name = n.raw_model_name)
     AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
), seg AS (
   SELECT credential_id, raw_model_name, created_at, success, trigger_kind, next_kind, next_retry_seconds, is_ns,
          LEAD(created_at) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
), rec AS (
   SELECT trigger_kind, next_kind, raw_model_name, created_at AS fail_at, next_at, next_retry_seconds,
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
-- ① 触发类型构成（★ 失败事件的 trigger_kind + 下一次事件的 trigger_kind）
SELECT 'A_kind' AS sec, win AS k1, trigger_kind AS k2, next_kind AS k3,
       count(*)::text AS k4,
       'gt300='||(count(*) FILTER (WHERE recheck_min > 300))::text
       ||' | gt300_pct='||ROUND(100.0*count(*) FILTER (WHERE recheck_min > 300)/count(*),1)::text AS k5
FROM mat GROUP BY win, trigger_kind, next_kind
UNION ALL
-- ② 两个时钟下的关键量（★ 真实 now vs 关窗 as-of=13:42:30 ⇒ 看成熟段变大时 p90 怎么动）
SELECT 'B_clock', win,
       count(*)::text,
       count(*) FILTER (WHERE recheck_min > 300)::text,
       'gt300_pct='||ROUND(100.0*count(*) FILTER (WHERE recheck_min > 300)/count(*),1)::text
       ||' | p90='||percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min)::int::text
       ||' | p99='||percentile_disc(0.99) WITHIN GROUP (ORDER BY recheck_min)::int::text
       ||' | p50='||percentile_disc(0.5) WITHIN GROUP (ORDER BY recheck_min)::int::text,
       ''
FROM mat GROUP BY win
ORDER BY 1,2,3,4;
