-- ★★ 第 26 处取证（★ 2026-11-11 02:0x +08，★ 211 真库）：★ 「::int 取整边界」伪影
-- ★★ 回答：★ M20 豁免组 post 的 raw p90 到底是多少？★ 取整后差几个单位？★ 真实差的上界是多少？
-- ★★ 实测结论：★ post raw p90 = **360.5069** min ⇒ `::int` = **361**；★ 基线取整值 = 360
-- ★★   ⇒ 基线真值 ∈ [359.5, 360.5) ⇒ ★★ ⇒ **真实差 ∈ (0.01, 1.01] 分钟 ⇒ 上界 ≈ 60 秒**
-- ★★ 用法：★ scp 到 211 → docker cp acc-pg17 → docker exec acc-pg17 psql -U acc_super -d llm_gateway -tA -F"|" -v ON_ERROR_STOP=1 -f /tmp/<本文件>
-- ★★★ M20 豁免组「361 vs 360」精确定证：★ 是真 1 分钟，还是 ::int 取整放大出来的亚分钟差？
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
          n.direct_http_status, n.direct_err_code, n.response_body,
          c.quota_state, c.quota_recover_at
   FROM node_probe_runs n LEFT JOIN credentials c ON c.id = n.credential_id
   WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id=b.provider_model_id
                 WHERE b.credential_id=n.credential_id AND pm.raw_model_name=n.raw_model_name)
     AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
), seg AS (
   SELECT credential_id, raw_model_name, created_at, success, next_retry_seconds,
          CASE WHEN direct_http_status IN (404,405,410,415)
                 OR (direct_http_status IN (400,422)
                     AND NOT (lower(coalesce(response_body,'')) ~ 'insufficient_quota|insufficient balance|quota exceeded|exceeded your current quota|billing|arrears|unpaid|balance is not enough|not enough balance|余额不足|欠费|额度不足|余额已不足|账户余额'))
                 OR direct_err_code IN ('http_404','probe_http_404')
                 OR (quota_state IN ('periodic_exhausted','balance_exhausted','permanently_exhausted')
                     AND quota_recover_at > created_at)
               THEN 'exempt' ELSE 'normal' END AS exemption,
          LEAD(created_at) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) AS next_at,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre' ELSE 'post' END era
   FROM fr
), rec AS (
   SELECT created_at AS fail_at, next_at, next_retry_seconds, exemption, era,
          EXTRACT(EPOCH FROM (next_at-created_at))/60 AS recheck_min
   FROM seg WHERE NOT success AND next_at IS NOT NULL
     AND raw_model_name LIKE '%/%' AND lower(btrim(raw_model_name)) IN (SELECT m FROM newly)
), exp AS (
   SELECT r.*, 'pre24' win FROM rec r WHERE r.era='pre' AND r.fail_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '24 hours'
   UNION ALL
   SELECT r.*, 'pre7d' FROM rec r WHERE r.era='pre'
   UNION ALL
   SELECT r.*, 'post' FROM rec r WHERE r.era='post'
), mat AS (
   SELECT * FROM exp
   WHERE exemption='exempt'
     AND ( (era='pre'  AND fail_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '6 hours'
                        AND next_at < TIMESTAMPTZ '2026-11-10 13:42:30+08')
        OR (era='post' AND fail_at < now() - interval '6 hours') )
)
-- ★ A：★ 原始（未取整）分位 + 取整后的值 + 抖动分布
SELECT 'A_raw' AS sec, win AS k1,
       'n='||count(*)::text||' p50_raw='||round(percentile_disc(0.5) WITHIN GROUP (ORDER BY recheck_min)::numeric,4)::text
       ||' p90_raw='||round(percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min)::numeric,4)::text
       ||' | ::int_p50='||(percentile_disc(0.5) WITHIN GROUP (ORDER BY recheck_min))::int::text
       ||' ::int_p90='||(percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min))::int::text
       ||' | max_raw='||round(max(recheck_min)::numeric,4)::text
       ||' | min_raw='||round(min(recheck_min)::numeric,4)::text
       ||' | gt360_raw='||count(*) FILTER (WHERE recheck_min > 360)::text
       ||' | gt360p5_raw='||count(*) FILTER (WHERE recheck_min > 360.5)::text
       ||' | gt361_raw='||count(*) FILTER (WHERE recheck_min > 361)::text
       ||' | lt360_raw='||count(*) FILTER (WHERE recheck_min < 360)::text
       ||' | sub360_5='||count(*) FILTER (WHERE recheck_min BETWEEN 359.5 AND 360.5)::text AS k2,
       '' AS k3
FROM mat GROUP BY win
UNION ALL
-- ★ B：★ 抖动直方图（按 0.5 分钟桶）
SELECT 'B_hist', win,
       (round(recheck_min/0.5)*0.5)::text||'~'||(round(recheck_min/0.5)*0.5+0.5)::text,
       count(*)::text
FROM mat GROUP BY win, round(recheck_min/0.5)
UNION ALL
-- ★ C：★ post 段按小时槽看（★ 361 是不是集中在 6h 批次落点）
SELECT 'C_hour', to_char(date_trunc('hour', fail_at),'MM-DD HH24'),
       'n='||count(*)::text||' p90_raw='||round(percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min)::numeric,4)::text
       ||' ::int_p90='||(percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min))::int::text
       ||' gt360_raw='||count(*) FILTER (WHERE recheck_min > 360)::text,
       ''
FROM mat WHERE win='post' GROUP BY 2
ORDER BY 1,2,3;
