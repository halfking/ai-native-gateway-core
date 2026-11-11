-- ★ 02:06:59 部署事件：★ 「newly 未减半 6h 顶格」是**部署引起**还是**背景抖动**？
-- ★ 形态（★ 真库实测 2026-11-11 03:3x +08）：★ 等长窗口里 部署前段 newly_top21600 = 0、部署后段 = 1
-- ★ ★★ 但 n 极小（★ newly_fail 段后仅 2 条）⇒ ★★ **不能凭 15 分钟窗口下结论**
-- ★ ⇒ ★★ 必须：① 定位到**具体对象**（★ 是哪只模型、★ 是否同时有减半证据 ⇒ 摇摆特征）
-- ★        ② 看**全 post 窗的时间分布**（★ 标出两次构建边界 01:07:37 / 02:06:59）
-- ★        ③ 看 **pre 窗的背景频率**（★ 6h 周期批量 ⇒ 设计使然）
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv_old AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM models_canonical mc JOIN provider_models pm ON pm.canonical_id = mc.id WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 rawv_new AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m JOIN provider_models pm ON pm.canonical_id = mc.id WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st_old AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_old),
 st_new AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_new),
 newly AS (SELECT m FROM st_new WHERE m LIKE '%/%' AND m NOT IN (SELECT m FROM st_old)),
 ev_set AS (SELECT v FROM (VALUES (7),(90),(150),(450),(1800),(10800)) t(v))
-- ① 定位具体对象：★ 部署后 15 分钟里 newly 的**每一条**失败（★ 含阶梯值与减半证据）
SELECT 'WHO' AS k,
       n.created_at::text||' ~ '||n.raw_model_name||' ~ ladder='||COALESCE(n.next_retry_seconds::text,'NULL')
       ||' ~ mul_evidence='||CASE WHEN n.next_retry_seconds IN (SELECT v FROM ev_set) THEN 'Y' ELSE 'N' END
       ||' ~ canon='||lower(btrim(n.raw_model_name)),
       '1'
FROM node_probe_runs n
WHERE n.created_at >= TIMESTAMPTZ '2026-11-11 02:06:59+08' AND n.created_at < TIMESTAMPTZ '2026-11-11 02:21:59+08'
  AND NOT n.success AND n.raw_model_name LIKE '%/%'
  AND lower(btrim(n.raw_model_name)) IN (SELECT m FROM newly)
UNION ALL
-- ② 全 post 窗：★ newly 失败按 **小时** 分布 + 阶梯分档（★ 标出两次构建边界所在小时）
SELECT 'POSTHOUR',
       to_char(n.created_at,'YYYY-MM-DD HH24:MI')||' ~ model='||n.raw_model_name
       ||' ~ ladder='||COALESCE(n.next_retry_seconds::text,'NULL')
       ||' ~ mul_ev='||CASE WHEN n.next_retry_seconds IN (SELECT v FROM ev_set) THEN 'Y' ELSE 'N' END,
       '1'
FROM node_probe_runs n
WHERE n.created_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08' AND n.created_at < now()
  AND NOT n.success AND n.raw_model_name LIKE '%/%'
  AND lower(btrim(n.raw_model_name)) IN (SELECT m FROM newly)
UNION ALL
-- ③ pre 窗背景频率：★ 每天 newly 的「未减半 6h 顶格」条数（★ ⇒ 6h 周期批量是不是设计使然）
-- ★ ★ 注意：★ 聚合不得出现在 GROUP BY ⇒ ★ 必须先在子查询里聚合
SELECT 'PREDATE',
       t.d||' ~ top21600='||t.t21600::text||' ~ newly_fail='||t.nf::text||' ~ mul_ev='||t.nm::text,
       '1'
FROM (
  SELECT to_char(n.created_at,'YYYY-MM-DD') AS d,
         COUNT(*) FILTER (WHERE n.next_retry_seconds=21600) AS t21600,
         COUNT(*) AS nf,
         COUNT(*) FILTER (WHERE n.next_retry_seconds IN (SELECT v FROM ev_set)) AS nm
  FROM node_probe_runs n
  WHERE n.created_at >= TIMESTAMPTZ '2026-11-03 13:42:30+08' AND n.created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08'
    AND NOT n.success AND n.raw_model_name LIKE '%/%'
    AND lower(btrim(n.raw_model_name)) IN (SELECT m FROM newly)
  GROUP BY 1
) t
ORDER BY 1, 2;
