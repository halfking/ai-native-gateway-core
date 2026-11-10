-- ═══ P0-3 观测窗口的**部署分段诊断**（★ 2026-11-10 19:01 事件）═══
-- ★ **为什么需要它**：★ era2（build_seq 2995 / sha 8028459d）在 **2026-11-10 19:01:04 +08** 被 SIGKILL(137)
--   ★★ 停掉，由 **build_seq 2996 / sha b92313da**（★ P0-B streaming 修复）接管 ⇒ ★★ **post 窗口跨越两个构建**
--   ★★ ⇒ ★ 若 post 段出现未减半 6h 顶格 ⇒ ★ 必须知道**是哪个构建**产生的 ⇒ ★ 本脚本做分段对拍。
-- ★ **口径**：★ 与 `recovery-detection-p0-3-metrics.sql` 的 M20 **完全同款**（newly 集合 / 豁免分类 / fp 视图口径）
--   ⇒ ★ 唯一区别：★ 按部署时刻切段。
-- ★ **用法（★ 在 211 上）**：
--   scp -q scripts/audit/recovery-detection-p0-3-deploy-split.sql 211:/tmp/ds.sql
--   ssh 211 'docker cp /tmp/ds.sql acc-pg17:/tmp/ds.sql && docker exec acc-pg17 psql -U acc_super -d llm_gateway -tA -F"~" -v ON_ERROR_STOP=1 -f /tmp/ds.sql'
-- ★★ **分隔符用 `~` 而不是 `|`**（★ 值里含 `|` 会串列 ⇒ ★ 本轮踩过）。
-- ★ 部署前后分段对比：★ era2 段（13:42:30 → 19:01:04）vs 新部署段（19:01:04 → now）
-- ★ 目的：★ 新构建（build_seq 2996 / b92313da = P0-B streaming 修复）**有没有动机制**（featured 集 / 乘数）
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv_old AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM models_canonical mc JOIN provider_models pm ON pm.canonical_id = mc.id WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 rawv_new AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m JOIN provider_models pm ON pm.canonical_id = mc.id WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st_old AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_old),
 st_new AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_new),
 newly AS (SELECT m FROM st_new WHERE m LIKE '%/%' AND m NOT IN (SELECT m FROM st_old)),
 ev_set AS (SELECT v FROM (VALUES (7),(90),(150),(450),(1800),(10800)) t(v)),
 fr AS (
   SELECT n.created_at, n.success, n.raw_model_name, n.next_retry_seconds, n.direct_http_status, n.direct_err_code, n.response_body,
          c.quota_state, c.quota_recover_at
   FROM node_probe_runs n LEFT JOIN credentials c ON c.id = n.credential_id
   WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id=b.provider_model_id
                 WHERE b.credential_id=n.credential_id AND pm.raw_model_name=n.raw_model_name)
     AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
), seg AS (
   SELECT created_at, next_retry_seconds,
     CASE WHEN direct_http_status IN (404,405,410,415)
            OR (direct_http_status IN (400,422)
                AND NOT (lower(coalesce(response_body,'')) ~ 'insufficient_quota|insufficient balance|quota exceeded|exceeded your current quota|billing|arrears|unpaid|balance is not enough|not enough balance|余额不足|欠费|额度不足|余额已不足|账户余额'))
            OR direct_err_code IN ('http_404','probe_http_404')
            OR (quota_state IN ('periodic_exhausted','balance_exhausted','permanently_exhausted')
                AND quota_recover_at > created_at)
          THEN 'exempt' ELSE 'normal' END AS exemption
   FROM fr WHERE NOT success AND raw_model_name LIKE '%/%' AND lower(btrim(raw_model_name)) IN (SELECT m FROM newly)
), segd AS (
   SELECT s.*, CASE WHEN s.created_at < TIMESTAMPTZ '2026-11-10 19:01:04+08' THEN 'A_era2_2995' ELSE 'B_new_2996' END AS seg
   FROM seg s WHERE s.created_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08'
)
SELECT 'LADDER' AS k,
       seg||' ~ '||exemption||' ~ ladder='||COALESCE(next_retry_seconds::text,'NULL') AS v,
       COUNT(*)::text AS n
FROM segd GROUP BY seg, exemption, next_retry_seconds
UNION ALL
SELECT 'SUMMARY', seg||' ~ '||exemption||' ~ n='||COUNT(*)::text
      ||' | top21600_n='||COUNT(*) FILTER (WHERE next_retry_seconds=21600)::text
      ||' | mul_evidence_n='||COUNT(*) FILTER (WHERE next_retry_seconds IN (SELECT v FROM ev_set))::text
      ||' | top10800_n='||COUNT(*) FILTER (WHERE next_retry_seconds=10800)::text, '1'
FROM segd GROUP BY seg, exemption
UNION ALL
SELECT 'FEATURED_SET',
  (SELECT COUNT(*)::text FROM models_canonical WHERE featured=TRUE)||' canon_featured | '
  ||(SELECT COUNT(*)::text FROM models_canonical WHERE featured=TRUE AND status='active')||' active | '
  ||(SELECT COUNT(*)::text FROM (SELECT DISTINCT lower(btrim(f)) FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>'') x)||' rp_names',
  '1'
UNION ALL
SELECT 'RP_RAWLIST', (SELECT COUNT(*)::text FROM rawv_old)||' old_raw | '||(SELECT COUNT(*)::text FROM rawv_new)||' new_raw', '1'
ORDER BY 1, 2;
