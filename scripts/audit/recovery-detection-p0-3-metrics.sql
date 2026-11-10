-- recovery-detection-p0-3-metrics.sql
--
-- ★ 上游恢复检测及时性轮（2026-11-10）Phase 2 验收脚本（可复采）。
--
-- 用途：在 211 真库（acc-pg17 / llm_gateway）复采 P0-3 修复前后的同名指标，
--       与 docs/audit/2026-11-10-上游恢复检测及时性-取证与P0实施.md 的
--       §1.2 基线、§4.0 部署终态逐条对拍。
--
-- 执行方式（★ 生产库唯一权威连接来自在役容器 env；不要凭记忆/文档写连接串）：
--   scp -q scripts/audit/recovery-detection-p0-3-metrics.sql 211:/tmp/
--   ssh 211 'docker cp /tmp/recovery-detection-p0-3-metrics.sql acc-pg17:/tmp/ \
--            && docker exec acc-pg17 psql -U acc_super -d llm_gateway -tA -F"|" \
--               -f /tmp/recovery-detection-p0-3-metrics.sql'
--
-- 口径纪律（★ 与基线同口径，★ 改口径前必须先改基线，否则对拍无意义）：
--   · 免费面 = credential_model_bindings.billing_mode='free'
--   · 探测运行 = node_probe_runs（权威表；model_probe_runs 仅 6 行，属另一层）
--   · featured 集合 = routing_policy.featured_models ∪ models_canonical.featured
--     **并按 bg/model_tier.go 的新装载逻辑展开 provider_models.raw_model_name 变体**
--     ⇒ ★ 不展开会把 namespaced 免费对误读成"非 featured" ⇒ **假零**。
--   · 部署时刻 = 2026-11-10 13:16 +08（DB 本地时钟；★ 不是 UTC —— 踩过）。
--
-- ⚠ psql 语法坑（已踩 3 次）：★ `GROUP BY 2` 里若 2 是**聚合表达式**会报
--   "aggregate functions are not allowed in GROUP BY" ⇒ 必须「子查询包一层 + 外层聚合」。

-- ═══ M1 绑定可用性（全库 + 免费面）═══
SELECT 'M1_total_bindings' k,
  COUNT(*)::text||' total, avail='||COUNT(*) FILTER (WHERE available)::text
  ||' | unavailable='||COUNT(*) FILTER (WHERE NOT available)::text v
FROM credential_model_bindings;

SELECT 'M1_free_bindings' k,
  COUNT(*)::text||' total, avail='||COUNT(*) FILTER (WHERE available)::text
  ||' | unavailable='||COUNT(*) FILTER (WHERE NOT available)::text
  ||' | pct='||ROUND(100.0*COUNT(*) FILTER (WHERE available)/COUNT(*),1)::text v
FROM credential_model_bindings WHERE billing_mode='free';

-- ═══ M1b 免费不可用的原因构成（★ 区分 manual 决策 vs probe_ 真故障）═══
WITH fb AS (
  SELECT b.available, b.unavailable_reason FROM credential_model_bindings b WHERE b.billing_mode='free'
), tagged AS (
  SELECT CASE WHEN available THEN 'available'
              WHEN unavailable_reason LIKE 'manual%' THEN 'manual_decision'
              WHEN unavailable_reason LIKE 'probe_%' THEN 'probe_detected'
              WHEN unavailable_reason IS NULL THEN 'no_reason'
              ELSE 'other' END bucket FROM fb
)
SELECT 'M1b_free_unavail_'||bucket k, COUNT(*)::text v FROM tagged GROUP BY bucket ORDER BY bucket;

-- ═══ M2 featured 集合规模（新装载逻辑的真值；修复前 34 rp + 15 canon + 0 raw）═══
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM provider_models pm JOIN models_canonical mc ON mc.id=pm.canonical_id
          WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>'')
SELECT 'M2_featured_static' k,
  (SELECT COUNT(*) FROM rp)::text||' rp + '||(SELECT COUNT(*) FROM cf)::text||' canon + '
  ||(SELECT COUNT(*) FROM rawv)::text||' raw_variant = '
  ||(SELECT COUNT(*) FROM (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv) x)::text v;

SELECT 'M2_featured_canon_count' k, COUNT(*)::text v
FROM models_canonical WHERE featured=TRUE AND status='active';

-- ═══ M3 ★ 核心判据：featured vs 非 featured 的 24h 探测次数（裸名 / namespaced 分组）═══
-- 修复前读数：裸名 featured 中位 48 vs 非 featured 24（≈2×）；
--             namespaced featured 与非 featured **同为 4**（无差异）★ ← 修复目标
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM provider_models pm JOIN models_canonical mc ON mc.id=pm.canonical_id
          WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv),
 runs AS (
   SELECT n.credential_id, n.raw_model_name, count(*) cnt
   FROM node_probe_runs n WHERE n.created_at > now() - interval '24 hours'
   GROUP BY n.credential_id, n.raw_model_name
 ), joined AS (
   SELECT r.cnt, (lower(btrim(r.raw_model_name)) IN (SELECT m FROM st)) is_feat,
          (r.raw_model_name LIKE '%/%') is_ns FROM runs r
 ), tagged AS (
   SELECT CASE WHEN is_feat THEN 'featured' ELSE 'not' END f,
          CASE WHEN is_ns THEN 'namespaced' ELSE 'bare' END ns, cnt FROM joined
)
SELECT 'M3_feat_'||f||'_ns_'||ns k,
  COUNT(*)::text||' pairs | median='||COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY cnt))::int::text,'?')
  ||' | sum='||COALESCE(SUM(cnt)::text,'0') v
FROM tagged GROUP BY f, ns ORDER BY 1;

-- ═══ M3b ★ 机制直接证据：backoff 档位（featured 应为非 featured 的 ~1/2）═══
-- ★ 这是**最直接**的机制读数：它不依赖探测量是否因流量而波动，
--   而直接读 featured_backoff_multiplier（默认 50%）是否真的作用到了 namespaced。
-- 部署后 T0 实测：ns featured http_429/timeout → 1800s；ns 非 featured 同类 → 3600s（2:1）。
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM provider_models pm JOIN models_canonical mc ON mc.id=pm.canonical_id
          WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv),
 tagged AS (
   SELECT (lower(btrim(n.raw_model_name)) IN (SELECT m FROM st)) is_feat,
          (n.raw_model_name LIKE '%/%') is_ns,
          COALESCE(n.last_err_code,'none') ec,
          n.next_retry_seconds rs
   FROM node_probe_state n
 ), agg AS (
   SELECT is_feat, is_ns, ec, COUNT(*) c,
          percentile_disc(0.5) WITHIN GROUP (ORDER BY rs) med
   FROM tagged GROUP BY is_feat, is_ns, ec
 )
SELECT 'M3b_backoff_'||CASE WHEN is_ns THEN 'ns' ELSE 'bare' END||'_'||CASE WHEN is_feat THEN 'featured' ELSE 'not' END||'_err_'||ec k,
  c::text||' rows | median_retry_s='||COALESCE(med::int::text,'?') v
FROM agg ORDER BY k;

-- ═══ M4 免费面的探测覆盖与 gap 分布（★ 恢复检测及时性的直接读数）═══
WITH fb AS (
  SELECT DISTINCT b.credential_id, pm.raw_model_name FROM credential_model_bindings b
  JOIN provider_models pm ON pm.id=b.provider_model_id WHERE b.billing_mode='free'
), nps AS (
  SELECT n.*, (n.next_retry_at - n.last_attempt_at) gap FROM node_probe_state n
  JOIN fb ON fb.credential_id=n.credential_id AND fb.raw_model_name=n.raw_model_name
), tagged AS (
  SELECT CASE WHEN gap <= interval '24h' THEN 'due_24h'
              WHEN gap <= interval '30d' THEN 'due_24h_30d' ELSE 'beyond_30d' END bucket FROM nps
)
SELECT 'M4_free_nps_'||bucket k, COUNT(*)::text||' rows v' FROM tagged GROUP BY bucket ORDER BY bucket;

WITH fb AS (
  SELECT DISTINCT b.credential_id, pm.raw_model_name FROM credential_model_bindings b
  JOIN provider_models pm ON pm.id=b.provider_model_id WHERE b.billing_mode='free'
), nps AS (
  SELECT n.*, (n.next_retry_at - n.last_attempt_at) gap FROM node_probe_state n
  JOIN fb ON fb.credential_id=n.credential_id AND fb.raw_model_name=n.raw_model_name
)
SELECT 'M4b_free_gap_gt24h' k, COUNT(*)::text||' rows | med_gap_min='||
  COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM gap)/60))::int::text,'?') v
FROM nps WHERE gap > interval '24h';

-- ═══ M5 探测量总量（★ 爆炸半径复核：基线 24h 9233 次；修复后增量应 < 1%）═══
SELECT 'M5_probe_runs_24h' k, COUNT(*)::text||' total | free='||
  COUNT(*) FILTER (WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id=b.provider_model_id
    WHERE b.billing_mode='free' AND b.credential_id=node_probe_runs.credential_id
      AND pm.raw_model_name=node_probe_runs.raw_model_name))::text v
FROM node_probe_runs WHERE created_at > now() - interval '24 hours';

-- ═══ M6 ★ 收益面：免费在服对里命中 featured 的对数（修复前 15 → 部署后 34）═══
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM provider_models pm JOIN models_canonical mc ON mc.id=pm.canonical_id
          WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv),
 freeb AS (
  SELECT DISTINCT b.credential_id, pm.raw_model_name, mc.canonical_name, b.available
  FROM credential_model_bindings b JOIN provider_models pm ON pm.id=b.provider_model_id
  JOIN models_canonical mc ON mc.id=pm.canonical_id
  JOIN credentials c ON c.id=b.credential_id JOIN providers p ON p.id=c.provider_id
  WHERE b.billing_mode='free' AND mc.status='active'
    AND COALESCE(p.enabled,FALSE)=TRUE AND COALESCE(c.status,'active')='active'
    AND COALESCE(c.lifecycle_status,'active')='active' AND COALESCE(c.manual_disabled,FALSE)=FALSE
)
SELECT 'M6_free_featured_pairs' k,
  COUNT(*) FILTER (WHERE lower(btrim(raw_model_name)) IN (SELECT m FROM st))::text||' featured / '
  ||COUNT(*)::text||' total_free_eligible' v FROM freeb;

-- ═══ M7 迁移 956 终态 ═══
SELECT 'M7_migration_956' k,
  CASE WHEN EXISTS(SELECT 1 FROM public.schema_migrations WHERE version='956') THEN 'APPLIED' ELSE 'MISSING' END v;
SELECT 'M7_target_featured' k, canonical_name||' featured='||featured::text v
FROM models_canonical WHERE canonical_name='minimax-m3.1-flash';

-- ═══ M8 ★ 部署前后切分（★ 判定"修复后是否真的拉开差距"）═══
-- ★ 部署时刻 = 2026-11-10 13:16 +08（★ DB 本地时钟；用 UTC 切会得到假零）。
-- 用法：窗口未满时看 post 段样本量是否足够；★ 样本不足时**不能**下结论。
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM provider_models pm JOIN models_canonical mc ON mc.id=pm.canonical_id
          WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv),
 r AS (
   SELECT n.credential_id, n.raw_model_name, n.created_at,
          (lower(btrim(n.raw_model_name)) IN (SELECT m FROM st)) is_feat,
          (n.raw_model_name LIKE '%/%') is_ns
   FROM node_probe_runs n WHERE n.created_at > now() - interval '24 hours'
 ), tagged AS (
   SELECT CASE WHEN is_feat THEN 'featured' ELSE 'not' END f,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:16:00+08' THEN 'pre' ELSE 'post' END win,
          credential_id, raw_model_name
   FROM r WHERE is_ns
 ), agg AS (
   SELECT f, win, credential_id, raw_model_name, COUNT(*) cnt FROM tagged GROUP BY f, win, credential_id, raw_model_name
)
SELECT 'M8_ns_'||f||'_'||win k,
  COUNT(*)::text||' pairs | runs='||SUM(cnt)::text
  ||' | median_per_pair='||COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY cnt))::int::text,'?') v
FROM agg GROUP BY f, win ORDER BY 1;
