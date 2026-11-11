-- ★ 02:06:59 部署事件 · 第三轮取证（★ 2026-11-11 03:5x +08）
-- ★ ★★ 前一轮的教训（★ 自踩坑，留痕）：★ 我第一版的「相位对齐等长窗」**其实不等长**
-- ★   （★ WHERE 跨了 75 分钟、★ CASE 只在 02:06:59 分段 ⇒ ★ 「A 段」实际是 **60 分钟**）
-- ★   ⇒ ★★ 607 vs 66 是**窗口长度 + 批次覆盖**的产物（★ A 段含 01:28–01:36 那个 6h 大批次）
-- ★   ⇒ ★★ **这正是本项目第 21/23 处纪律针对的形态**：★ 跨段比必须**等长**、且必须**同相位**
-- ★ ★★ 本轮修法：★ 三个**各 15 分钟**的窗口，★ 与部署边界相隔 **6h / 1h / 0h**
-- ★   ⇒ ★★ 三窗都跨**同一个 6h 批次相位**（★ :17 的 sonnet 事件在窗内、★ :28-:36 的大批次在窗外）
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv_old AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM models_canonical mc JOIN provider_models pm ON pm.canonical_id = mc.id WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 rawv_new AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m JOIN provider_models pm ON pm.canonical_id = mc.id WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st_old AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_old),
 st_new AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_new),
 newly AS (SELECT m FROM st_new WHERE m LIKE '%/%' AND m NOT IN (SELECT m FROM st_old)),
 ev_set AS (SELECT v FROM (VALUES (7),(90),(150),(450),(1800),(10800)) t(v)),
 w AS (
   SELECT 'W1_1100_2006_2021' AS sg, TIMESTAMPTZ '2026-11-10 20:06:59+08' AS lo, TIMESTAMPTZ '2026-11-10 20:21:59+08' AS hi
   UNION ALL SELECT 'W2_1101_0106_0121', TIMESTAMPTZ '2026-11-11 01:06:59+08', TIMESTAMPTZ '2026-11-11 01:21:59+08'
   UNION ALL SELECT 'W3_1101_0206_0221', TIMESTAMPTZ '2026-11-11 02:06:59+08', TIMESTAMPTZ '2026-11-11 02:21:59+08'
)
SELECT 'EQ3' AS k,
       t.sg||' ~ len=15min ~ all_runs='||t.alln::text||' ~ ns_fail='||t.nsf::text||' ~ ns_models='||t.nsm::text
       ||' ~ newly_fail='||t.nf::text||' ~ newly_top21600='||t.nt21600::text||' ~ newly_mul_ev='||t.nmul::text
       ||' ~ newly_top10800='||t.nt10800::text||' ~ newly_top3600='||t.nt3600::text
       ||' ~ newly_top21600_models='||COALESCE(t.models21600,'(无)'),
       '1'
FROM (
  SELECT w.sg,
         COUNT(*) AS alln,
         COUNT(*) FILTER (WHERE n.raw_model_name LIKE '%/%' AND NOT n.success) AS nsf,
         COUNT(DISTINCT n.raw_model_name) FILTER (WHERE n.raw_model_name LIKE '%/%') AS nsm,
         COUNT(*) FILTER (WHERE NOT n.success AND n.raw_model_name LIKE '%/%' AND lower(btrim(n.raw_model_name)) IN (SELECT m FROM newly)) AS nf,
         COUNT(*) FILTER (WHERE NOT n.success AND n.raw_model_name LIKE '%/%' AND lower(btrim(n.raw_model_name)) IN (SELECT m FROM newly) AND n.next_retry_seconds=21600) AS nt21600,
         COUNT(*) FILTER (WHERE NOT n.success AND n.raw_model_name LIKE '%/%' AND lower(btrim(n.raw_model_name)) IN (SELECT m FROM newly) AND n.next_retry_seconds IN (SELECT v FROM ev_set)) AS nmul,
         COUNT(*) FILTER (WHERE NOT n.success AND n.raw_model_name LIKE '%/%' AND lower(btrim(n.raw_model_name)) IN (SELECT m FROM newly) AND n.next_retry_seconds=10800) AS nt10800,
         COUNT(*) FILTER (WHERE NOT n.success AND n.raw_model_name LIKE '%/%' AND lower(btrim(n.raw_model_name)) IN (SELECT m FROM newly) AND n.next_retry_seconds=3600) AS nt3600,
         string_agg(DISTINCT n.raw_model_name, ',') FILTER (WHERE NOT n.success AND n.raw_model_name LIKE '%/%' AND lower(btrim(n.raw_model_name)) IN (SELECT m FROM newly) AND n.next_retry_seconds=21600) AS models21600
  FROM w JOIN node_probe_runs n ON n.created_at >= w.lo AND n.created_at < w.hi
  GROUP BY w.sg
) t
ORDER BY 1, 2;
