-- ★ 01:07:37 部署事件的**等长窗口**对比（★ 同量纲 ⇒ 才可比）
-- ★ 段 A' = [00:52:37, 01:07:37)（部署前 15 分钟）vs 段 B = [01:07:37, 01:22:37)（部署后 15 分钟）
-- ★ 目的：★ 判断「watchdog featured 口径变更」是否真的改变了 namespaced 变体的探测密度
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv_old AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM models_canonical mc JOIN provider_models pm ON pm.canonical_id = mc.id WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 rawv_new AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m JOIN provider_models pm ON pm.canonical_id = mc.id WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st_old AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_old),
 st_new AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_new),
 newly AS (SELECT m FROM st_new WHERE m LIKE '%/%' AND m NOT IN (SELECT m FROM st_old)),
 ev_set AS (SELECT v FROM (VALUES (7),(90),(150),(450),(1800),(10800)) t(v))
SELECT 'EQWIN' AS k,
       t.sg||' ~ window=15min ~ all_runs='||t.alln::text
       ||' ~ ns_runs='||t.nsn::text||' ~ ns_fail='||t.nsf::text
       ||' ~ ns_models='||t.nsm::text
       ||' ~ newly_fail='||t.nf::text
       ||' ~ newly_top21600='||t.nt21600::text
       ||' ~ newly_mul_evidence='||t.nmul::text
       ||' ~ newly_top10800='||t.nt10800::text
       ||' ~ ns_success='||t.nss::text,
       '1'
FROM (
  SELECT CASE WHEN created_at <  TIMESTAMPTZ '2026-11-11 01:07:37+08' THEN 'A_0052_0107' ELSE 'B_0107_0122' END AS sg,
         COUNT(*) AS alln,
         COUNT(*) FILTER (WHERE raw_model_name LIKE '%/%') AS nsn,
         COUNT(*) FILTER (WHERE raw_model_name LIKE '%/%' AND NOT success) AS nsf,
         COUNT(*) FILTER (WHERE raw_model_name LIKE '%/%' AND success) AS nss,
         COUNT(DISTINCT raw_model_name) FILTER (WHERE raw_model_name LIKE '%/%') AS nsm,
         COUNT(*) FILTER (WHERE NOT success AND raw_model_name LIKE '%/%' AND lower(btrim(raw_model_name)) IN (SELECT m FROM newly)) AS nf,
         COUNT(*) FILTER (WHERE NOT success AND raw_model_name LIKE '%/%' AND lower(btrim(raw_model_name)) IN (SELECT m FROM newly) AND next_retry_seconds=21600) AS nt21600,
         COUNT(*) FILTER (WHERE NOT success AND raw_model_name LIKE '%/%' AND lower(btrim(raw_model_name)) IN (SELECT m FROM newly) AND next_retry_seconds IN (SELECT v FROM ev_set)) AS nmul,
         COUNT(*) FILTER (WHERE NOT success AND raw_model_name LIKE '%/%' AND lower(btrim(raw_model_name)) IN (SELECT m FROM newly) AND next_retry_seconds=10800) AS nt10800
  FROM node_probe_runs
  WHERE created_at >= TIMESTAMPTZ '2026-11-11 00:52:37+08' AND created_at < TIMESTAMPTZ '2026-11-11 01:22:37+08'
  GROUP BY 1
) t
UNION ALL
-- ★ 全量阶梯谱（等长窗口）⇒ 看 watchdog 变更是否改变了阶梯分布
SELECT 'EQLADDER',
       t.sg||' ~ ladder='||COALESCE(t.next_retry_seconds::text,'NULL'),
       t.n::text
FROM (
  SELECT CASE WHEN created_at < TIMESTAMPTZ '2026-11-11 01:07:37+08' THEN 'A_0052_0107' ELSE 'B_0107_0122' END AS sg,
         next_retry_seconds, COUNT(*) AS n
  FROM node_probe_runs
  WHERE raw_model_name LIKE '%/%' AND NOT success
    AND created_at >= TIMESTAMPTZ '2026-11-11 00:52:37+08' AND created_at < TIMESTAMPTZ '2026-11-11 01:22:37+08'
  GROUP BY 1, 2
) t
ORDER BY 1, 2;
