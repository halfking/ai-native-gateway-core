-- ★ P0-3 观测窗口的**第二次部署分段诊断**（★ 2026-11-11 01:07:37 +08 事件）
-- ★ **为什么需要它**：★ 并行会话把构建 `211-199c20a0` 部署到 211（★ StartedAt = 2026-11-10T17:07:37Z = **11-11 01:07:37 +08**）
--   ★★ ⇒ ★ post 窗口**再次跨越两个构建** ⇒ ★★ 必须按新切点分段
-- ★★ **本轮与 19:01 事件的关键差别**：★ 那次 diff **不碰** featured 路径；★ 这次 diff **碰了**
--   ★★ （★ `bg/featured_sources.go` 新增 +90 行、★ `bg/model_tier.go` -13 行、★ `bg/model_probe.go` -14 行）
--   ★★ ⇒ ★ 必须做「机制连续性三核查」⇒ ★★ 并且量化「watchdog featured 口径变更」对 post 段读数的影响
-- ★ **口径**：★ 与 `recovery-detection-p0-3-metrics.sql` 的 M20 **完全同款**（newly 集合 / 豁免分类 / fp 视图）
-- ★★ **分隔符用 `~`**（★ 值里含 `|` 会串列）
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
   SELECT s.*, CASE WHEN s.created_at < TIMESTAMPTZ '2026-11-11 01:07:37+08' THEN 'A_era2_pre0107' ELSE 'B_new_199c20a0' END AS seg
   FROM seg s WHERE s.created_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08'
)
-- ① 分段：阶梯谱（与 M20 同款口径）
SELECT 'LADDER' AS k,
       seg||' ~ '||exemption||' ~ ladder='||COALESCE(next_retry_seconds::text,'NULL') AS v,
       COUNT(*)::text AS n
FROM segd GROUP BY seg, exemption, next_retry_seconds
UNION ALL
-- ② 分段：摘要（事件量 / 未减半顶格 / 乘数证据 / 减半独占值）
SELECT 'SUMMARY', seg||' ~ '||exemption||' ~ n='||COUNT(*)::text
      ||' ~ top21600_n='||COUNT(*) FILTER (WHERE next_retry_seconds=21600)::text
      ||' ~ mul_evidence_n='||COUNT(*) FILTER (WHERE next_retry_seconds IN (SELECT v FROM ev_set))::text
      ||' ~ top10800_n='||COUNT(*) FILTER (WHERE next_retry_seconds=10800)::text, '1'
FROM segd GROUP BY seg, exemption
UNION ALL
-- ③ 探针连续性：部署前后每分钟的全量 node_probe_runs（★ 找「停探窗口」）
--   ★★ 注意：★ `GROUP BY` 里**不能**含聚合表达式（★ psql 报「aggregate functions are not allowed in GROUP BY」）
--   ★★ ⇒ ★ 必须**子查询包一层**：★ 内层按分钟分组、外层再拼字符串
SELECT 'CONTINUITY',
       t.m||' ~ all_runs='||t.n::text,
       t.f::text
FROM (
  SELECT to_char(created_at, 'MM-DD HH24:MI') AS m,
         COUNT(*) AS n,
         COUNT(*) FILTER (WHERE NOT success) AS f
  FROM node_probe_runs
  WHERE created_at >= TIMESTAMPTZ '2026-11-11 00:55:00+08' AND created_at < TIMESTAMPTZ '2026-11-11 01:20:00+08'
  GROUP BY 1
) t
UNION ALL
-- ④ watchdog 影响面：namespaced 变体（非 canonical 裸名）在部署前后的探测量
--   ★★ 同样：★ `GROUP BY 2` 指向的是**含聚合的输出列** ⇒ 非法 ⇒ ★ 子查询包一层
SELECT 'NAMESPACED_VOLUME',
       t.sg||' ~ namespaced_runs='||t.n::text
       ||' ~ namespaced_fail='||t.f::text
       ||' ~ distinct_models='||t.dm::text,
       '1'
FROM (
  SELECT CASE WHEN created_at < TIMESTAMPTZ '2026-11-11 01:07:37+08' THEN 'A_pre0107' ELSE 'B_post0107' END AS sg,
         COUNT(*) AS n,
         COUNT(*) FILTER (WHERE NOT success) AS f,
         COUNT(DISTINCT raw_model_name) AS dm
  FROM node_probe_runs
  WHERE raw_model_name LIKE '%/%' AND created_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08'
  GROUP BY 1
) t
UNION ALL
-- ⑤ featured 集规模（★ 连续性核查 ①）
SELECT 'FEATURED_SET',
  (SELECT COUNT(*)::text FROM models_canonical WHERE featured=TRUE)||' canon_featured | '
  ||(SELECT COUNT(*)::text FROM models_canonical WHERE featured=TRUE AND status='active')||' active | '
  ||(SELECT COUNT(*)::text FROM (SELECT DISTINCT lower(btrim(f)) FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>'') x)||' rp_names',
  '1'
UNION ALL
SELECT 'RP_RAWLIST', (SELECT COUNT(*)::text FROM rawv_old)||' old_raw | '||(SELECT COUNT(*)::text FROM rawv_new)||' new_raw', '1'
ORDER BY 1, 2;
