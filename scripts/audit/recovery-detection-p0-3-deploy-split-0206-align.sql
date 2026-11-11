-- ★ 02:06:59 部署事件 · 第二轮取证（★ 2026-11-11 03:4x +08）
-- ★ ★★ 背景：★ 第一轮「等长窗口」显示 部署前段 newly_top21600=0、部署后段=1 ⇒ 看似「部署引起」
-- ★ ★★ 但 ★ n 极小（★ newly_fail 仅 2 条）⇒ **不能凭计数下结论** ⇒ ★★ 必须定位具体对象
-- ★ ★★ 本轮要答三问：
-- ★   ① 那条 21600 是**谁**、**什么失败形态**（★ 与部署前同相位的同类事件**是否同构**）
-- ★   ② **相位对齐**的等长窗（★ 两窗都含 6h 批次的同一相位 ⇒ 才可比）
-- ★   ③ 部署瞬间有没有**探针缺口**（★ 逐分钟）
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv_old AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM models_canonical mc JOIN provider_models pm ON pm.canonical_id = mc.id WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 rawv_new AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m JOIN provider_models pm ON pm.canonical_id = mc.id WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st_old AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_old),
 st_new AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_new),
 newly AS (SELECT m FROM st_new WHERE m LIKE '%/%' AND m NOT IN (SELECT m FROM st_old)),
 ev_set AS (SELECT v FROM (VALUES (7),(90),(150),(450),(1800),(10800)) t(v))
-- ① 同一只模型的三次 21600（★ 14:17 / 20:17 / 02:17 ⇒ 6h 相位）⇒ ★ **失败形态是否同构**
SELECT 'SHAPE' AS k,
       n.created_at::text||' ~ '||n.raw_model_name
       ||' ~ ladder='||COALESCE(n.next_retry_seconds::text,'NULL')
       ||' ~ http='||COALESCE(n.direct_http_status::text,'NULL')
       ||' ~ err='||COALESCE(n.direct_err_code,'NULL')
       ||' ~ detail='||COALESCE(left(n.direct_err_detail,60),'NULL')
       ||' ~ mul_ev='||CASE WHEN n.next_retry_seconds IN (SELECT v FROM ev_set) THEN 'Y' ELSE 'N' END,
       '1'
FROM node_probe_runs n
WHERE n.created_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08' AND n.created_at < now()
  AND NOT n.success AND n.raw_model_name IN ('claude/sonnet-5','anthropic/claude-sonnet-5:batch')
UNION ALL
-- ② 相位对齐的等长窗（★ 两窗都跨同一 6h 批次相位：★ :17 / :28-:36）
--   段 A'' = [01:06:59, 01:21:59)（部署前）vs 段 B = [02:06:59, 02:21:59)（部署后）⇒ ★ 相差正好 1 小时
SELECT 'EQALIGN',
       t.sg||' ~ all_runs='||t.alln::text||' ~ ns_fail='||t.nsf::text||' ~ ns_models='||t.nsm::text
       ||' ~ newly_fail='||t.nf::text||' ~ newly_top21600='||t.nt21600::text||' ~ newly_mul_ev='||t.nmul::text
       ||' ~ newly_top10800='||t.nt10800::text||' ~ newly_top3600='||t.nt3600::text,
       '1'
FROM (
  SELECT CASE WHEN created_at < TIMESTAMPTZ '2026-11-11 02:06:59+08'
              THEN 'A_0106_0121' ELSE 'B_0206_0221' END AS sg,
         COUNT(*) AS alln,
         COUNT(*) FILTER (WHERE raw_model_name LIKE '%/%' AND NOT success) AS nsf,
         COUNT(DISTINCT raw_model_name) FILTER (WHERE raw_model_name LIKE '%/%') AS nsm,
         COUNT(*) FILTER (WHERE NOT success AND raw_model_name LIKE '%/%' AND lower(btrim(raw_model_name)) IN (SELECT m FROM newly)) AS nf,
         COUNT(*) FILTER (WHERE NOT success AND raw_model_name LIKE '%/%' AND lower(btrim(raw_model_name)) IN (SELECT m FROM newly) AND next_retry_seconds=21600) AS nt21600,
         COUNT(*) FILTER (WHERE NOT success AND raw_model_name LIKE '%/%' AND lower(btrim(raw_model_name)) IN (SELECT m FROM newly) AND next_retry_seconds IN (SELECT v FROM ev_set)) AS nmul,
         COUNT(*) FILTER (WHERE NOT success AND raw_model_name LIKE '%/%' AND lower(btrim(raw_model_name)) IN (SELECT m FROM newly) AND next_retry_seconds=10800) AS nt10800,
         COUNT(*) FILTER (WHERE NOT success AND raw_model_name LIKE '%/%' AND lower(btrim(raw_model_name)) IN (SELECT m FROM newly) AND next_retry_seconds=3600) AS nt3600
  FROM node_probe_runs
  WHERE created_at >= TIMESTAMPTZ '2026-11-11 01:06:59+08' AND created_at < TIMESTAMPTZ '2026-11-11 02:21:59+08'
  GROUP BY 1
) t
UNION ALL
-- ③ 部署瞬间的逐分钟探测量（★ 抓「停探窗口」）⇒ 含 01:00–01:10 与 02:00–02:14
SELECT 'MINUTE',
       t.mn||' ~ runs='||t.runs::text||' ~ ns='||t.ns::text,
       t.models::text
FROM (
  SELECT to_char(n.created_at,'YYYY-MM-DD HH24:MI') AS mn,
         COUNT(*) AS runs,
         COUNT(*) FILTER (WHERE n.raw_model_name LIKE '%/%') AS ns,
         COUNT(DISTINCT n.raw_model_name) AS models
  FROM node_probe_runs n
  WHERE ((n.created_at >= TIMESTAMPTZ '2026-11-11 01:00:00+08' AND n.created_at < TIMESTAMPTZ '2026-11-11 01:10:00+08')
      OR (n.created_at >= TIMESTAMPTZ '2026-11-11 02:00:00+08' AND n.created_at < TIMESTAMPTZ '2026-11-11 02:14:00+08'))
  GROUP BY 1
) t
ORDER BY 1, 2;
