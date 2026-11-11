-- ★ 02:06:59 部署事件的**等长窗口**对比（★ 同量纲 ⇒ 才可比）
-- ★ 段 A' = [01:51:59, 02:06:59)（部署前 15 分钟）vs 段 B = [02:06:59, 02:21:59)（部署后 15 分钟）
-- ★ 目的：★ 判断「tooldialect 抽象工厂（7b926bfb-dirty）」是否真的改变了探针口径 / featured 状态
-- ★ ★ 与 0107 事件同款形态（★ 同一套度量 ⇒ 可横向对比）
-- ★ ★ 本次的特殊点：★ **bg/ 零改动** ⇒ 探针源码逐字节相同 ⇒ 只剩「运行时行为」这一腿要验
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
  SELECT CASE WHEN created_at <  TIMESTAMPTZ '2026-11-11 02:06:59+08' THEN 'A_0151_0206' ELSE 'B_0206_0221' END AS sg,
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
  WHERE created_at >= TIMESTAMPTZ '2026-11-11 01:51:59+08' AND created_at < TIMESTAMPTZ '2026-11-11 02:21:59+08'
  GROUP BY 1
) t
UNION ALL
-- ★ 全量阶梯谱（等长窗口）⇒ 看阶梯分布是否随构建切换而变
SELECT 'EQLADDER',
       t.sg||' ~ ladder='||COALESCE(t.next_retry_seconds::text,'NULL'),
       t.n::text
FROM (
  SELECT CASE WHEN created_at < TIMESTAMPTZ '2026-11-11 02:06:59+08' THEN 'A_0151_0206' ELSE 'B_0206_0221' END AS sg,
         next_retry_seconds, COUNT(*) AS n
  FROM node_probe_runs
  WHERE raw_model_name LIKE '%/%' AND NOT success
    AND created_at >= TIMESTAMPTZ '2026-11-11 01:51:59+08' AND created_at < TIMESTAMPTZ '2026-11-11 02:21:59+08'
  GROUP BY 1, 2
) t
UNION ALL
-- ★ 分钟级探测量分布（★ 抓「部署瞬间是否停探」）⇒ 按分钟
-- ★ ★ 注意：★ 不能 `GROUP BY 1`（★ 首列里含聚合 ⇒ 聚合不得出现在 GROUP BY ⇒ 非法）
SELECT 'EQMIN',
       t.mn||' ~ runs='||t.runs::text,
       t.models::text
FROM (
  SELECT to_char(created_at, 'YYYY-MM-DD HH24:MI') AS mn,
         COUNT(*) AS runs,
         COUNT(DISTINCT raw_model_name) AS models
  FROM node_probe_runs
  WHERE created_at >= TIMESTAMPTZ '2026-11-11 02:00:00+08' AND created_at < TIMESTAMPTZ '2026-11-11 02:14:00+08'
  GROUP BY 1
) t
ORDER BY 1, 2;
