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
--     **并按 bg/model_tier.go 的装载逻辑展开 provider_models.raw_model_name 变体**
--     ⇒ ★ 不展开会把 namespaced 免费对误读成"非 featured" ⇒ **假零**。
--     ★★ era2（2026-11-10 第二次部署）：展开覆盖**两个来源**的 canonical
--     （此前只展开 models_canonical.featured 一侧 ⇒ 漏 106 个 namespaced 变体）。
--   · era 切点（★ DB 本地时钟 +08；★ 不是 UTC —— 用 UTC 切会得到**假零**，已踩）：
--       era1 = 2026-11-10 13:16 +08（canonical 侧展开首次上线，build_seq 2994，static=92）
--       era2 = 2026-11-10 13:42:30 +08（★ **统一展开**上线，build_seq 2995，static=210）
--     ★ 当前在役 era = era2 ⇒ M8 / M11 的前后切分一律按 era2。
--     ★ era2 取「容器启动 13:42:26.9 + 静态集合刷新 13:42:28」之后 2 秒 ⇒ post 段
--       严格是新二进制产出的数据（不含旧槽尾巴）。
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
 -- ★★ era2（2026-11-10 第二次部署）：featured 集合改为**统一展开** —— raw 变体来自
 --   ★ **两个来源**的 canonical（此前只展开 models_canonical.featured 一侧，漏了
 --   ★ routing_policy 侧 ⇒ 实测 106 个 namespaced 变体未覆盖 ⇒ 同一缺陷只修一半）。
 --   ★ 与 bg/model_tier.go 的装载查询保持同一套语义（feat_names UNION ⇒ JOIN 展开）。
 rawv AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn
          JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m
          JOIN provider_models pm ON pm.canonical_id = mc.id
          WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>'')
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
 -- ★★ era2（2026-11-10 第二次部署）：featured 集合改为**统一展开** —— raw 变体来自
 --   ★ **两个来源**的 canonical（此前只展开 models_canonical.featured 一侧，漏了
 --   ★ routing_policy 侧 ⇒ 实测 106 个 namespaced 变体未覆盖 ⇒ 同一缺陷只修一半）。
 --   ★ 与 bg/model_tier.go 的装载查询保持同一套语义（feat_names UNION ⇒ JOIN 展开）。
 rawv AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn
          JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m
          JOIN provider_models pm ON pm.canonical_id = mc.id
          WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
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
 -- ★★ era2（2026-11-10 第二次部署）：featured 集合改为**统一展开** —— raw 变体来自
 --   ★ **两个来源**的 canonical（此前只展开 models_canonical.featured 一侧，漏了
 --   ★ routing_policy 侧 ⇒ 实测 106 个 namespaced 变体未覆盖 ⇒ 同一缺陷只修一半）。
 --   ★ 与 bg/model_tier.go 的装载查询保持同一套语义（feat_names UNION ⇒ JOIN 展开）。
 rawv AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn
          JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m
          JOIN provider_models pm ON pm.canonical_id = mc.id
          WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
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
 -- ★★ era2（2026-11-10 第二次部署）：featured 集合改为**统一展开** —— raw 变体来自
 --   ★ **两个来源**的 canonical（此前只展开 models_canonical.featured 一侧，漏了
 --   ★ routing_policy 侧 ⇒ 实测 106 个 namespaced 变体未覆盖 ⇒ 同一缺陷只修一半）。
 --   ★ 与 bg/model_tier.go 的装载查询保持同一套语义（feat_names UNION ⇒ JOIN 展开）。
 rawv AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn
          JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m
          JOIN provider_models pm ON pm.canonical_id = mc.id
          WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
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
-- ★ era 切点 = 2026-11-10 13:42:30 +08（era2；★ DB 本地时钟；用 UTC 切会得到假零）。
-- 用法：窗口未满时看 post 段样本量是否足够；★ 样本不足时**不能**下结论。
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 -- ★★ era2（2026-11-10 第二次部署）：featured 集合改为**统一展开** —— raw 变体来自
 --   ★ **两个来源**的 canonical（此前只展开 models_canonical.featured 一侧，漏了
 --   ★ routing_policy 侧 ⇒ 实测 106 个 namespaced 变体未覆盖 ⇒ 同一缺陷只修一半）。
 --   ★ 与 bg/model_tier.go 的装载查询保持同一套语义（feat_names UNION ⇒ JOIN 展开）。
 rawv AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn
          JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m
          JOIN provider_models pm ON pm.canonical_id = mc.id
          WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv),
 r AS (
   SELECT n.credential_id, n.raw_model_name, n.created_at,
          (lower(btrim(n.raw_model_name)) IN (SELECT m FROM st)) is_feat,
          (n.raw_model_name LIKE '%/%') is_ns
   FROM node_probe_runs n WHERE n.created_at > now() - interval '24 hours'
 ), tagged AS (
   SELECT CASE WHEN is_feat THEN 'featured' ELSE 'not' END f,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre' ELSE 'post' END win,
          credential_id, raw_model_name
   FROM r WHERE is_ns
 ), agg AS (
   SELECT f, win, credential_id, raw_model_name, COUNT(*) cnt FROM tagged GROUP BY f, win, credential_id, raw_model_name
)
SELECT 'M8_ns_'||f||'_'||win k,
  COUNT(*)::text||' pairs | runs='||SUM(cnt)::text
  ||' | median_per_pair='||COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY cnt))::int::text,'?') v
FROM agg GROUP BY f, win ORDER BY 1;

-- ═══ M9 ★ 展开覆盖完整性（★ 防止后续数据变更让覆盖退化）═══
-- 判据：featured canonical 的**每一个** raw 变体都必须已进 static 集合。
-- ★ T0 实测 = 0（完整覆盖）；若这里 > 0 ⇒ 展开逻辑与数据已脱节。
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 -- ★★ era2（2026-11-10 第二次部署）：featured 集合改为**统一展开** —— raw 变体来自
 --   ★ **两个来源**的 canonical（此前只展开 models_canonical.featured 一侧，漏了
 --   ★ routing_policy 侧 ⇒ 实测 106 个 namespaced 变体未覆盖 ⇒ 同一缺陷只修一半）。
 --   ★ 与 bg/model_tier.go 的装载查询保持同一套语义（feat_names UNION ⇒ JOIN 展开）。
 rawv AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn
          JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m
          JOIN provider_models pm ON pm.canonical_id = mc.id
          WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv)
SELECT 'M9_missing_raw_variant' k,
  COUNT(*)::text||' 个 featured canonical 的 raw 变体未进 static（★ 期望 0）' v
FROM provider_models pm JOIN models_canonical mc ON mc.id=pm.canonical_id
WHERE mc.featured=TRUE AND mc.status='active'
  AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''
  AND lower(btrim(pm.raw_model_name)) NOT IN (SELECT m FROM st);

-- ═══ M10 ★ 数据质量：standardized_name vs canonical_name 的差异面 ═══
-- ★ 差异会让 selfcheck 侧的别名匹配失准（selfcheck 用 raw+standardized 两个别名）。
-- T0 实测：2857 行里 19 行不等；★ 这 19 行的 free_pairs 与 node_probe_state 行数
-- 都极小 ⇒ 当前无实质影响，但差异量本身要持续可观测。
SELECT 'M10_std_vs_canon_diff' k,
  COUNT(*)::text||' / '||COUNT(*)::text||' 行不等 | 其中 featured 的='||
  COUNT(*) FILTER (WHERE mc.featured)::text v
FROM provider_models pm JOIN models_canonical mc ON mc.id=pm.canonical_id
WHERE pm.standardized_name IS NOT NULL AND pm.standardized_name<>''
  AND lower(pm.standardized_name) <> lower(mc.canonical_name);

WITH diff AS (
  SELECT pm.raw_model_name, pm.standardized_name, mc.canonical_name, mc.featured
  FROM provider_models pm JOIN models_canonical mc ON mc.id=pm.canonical_id
  WHERE pm.standardized_name IS NOT NULL AND pm.standardized_name<>''
    AND lower(pm.standardized_name) <> lower(mc.canonical_name)
)
SELECT 'M10_diff_'||raw_model_name k,
  'canon_featured='||featured::text
  ||' | free_pairs='||COALESCE((SELECT COUNT(*) FROM credential_model_bindings b
       JOIN provider_models pm2 ON pm2.id=b.provider_model_id
       WHERE b.billing_mode='free' AND pm2.raw_model_name=diff.raw_model_name),0)::text
  ||' | nps_rows='||COALESCE((SELECT COUNT(*) FROM node_probe_state n WHERE n.raw_model_name=diff.raw_model_name),0)::text v
FROM diff ORDER BY raw_model_name;

-- ═══ M11 ★★★ 恢复感知时延（★ 最直接回答"恢复检测及时性"的指标）═══
-- 定义：从「上游开始失败」到「网关确认它好了」的时延。
-- 方法：node_probe_runs 时序里，连续失败段的最后一条失败 → 之后第一条成功。
-- ★ 排除最近 2 小时（当前仍在进行中的段，避免把"还没恢复"算成"恢复慢"）。
-- ★ 按 raw 名形态分组（namespaced = 免费供应商主体；bare = 裸名）。
-- ★ 按在役 era 切分（era2 = 2026-11-10 13:42:30 +08）⇒ **同一口径的前后对比**。
--
-- ★ 修复前基线（7 天窗口实测，已记入 T0 锚点）：
--   bare       : 19 episodes | med 3 min  | p90 120 min | max  360 min
--   namespaced : 17 episodes | med 6 min  | p90 511 min | max 1791 min（★ 29.9h ⇒
--                  这就是「免费供应商 ~24h 未感知恢复」的量化形态）
-- ★ 续篇判定：post_fix 段的 namespaced p90/max 是否显著低于 pre_fix 段。
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 -- ★★ era2（2026-11-10 第二次部署）：featured 集合改为**统一展开** —— raw 变体来自
 --   ★ **两个来源**的 canonical（此前只展开 models_canonical.featured 一侧，漏了
 --   ★ routing_policy 侧 ⇒ 实测 106 个 namespaced 变体未覆盖 ⇒ 同一缺陷只修一半）。
 --   ★ 与 bg/model_tier.go 的装载查询保持同一套语义（feat_names UNION ⇒ JOIN 展开）。
 rawv AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn
          JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m
          JOIN provider_models pm ON pm.canonical_id = mc.id
          WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv),
 fr AS (
   SELECT n.credential_id, n.raw_model_name, n.created_at, n.success,
          (lower(btrim(n.raw_model_name)) IN (SELECT m FROM st)) is_feat_now,
          (n.raw_model_name LIKE '%/%') is_ns
   FROM node_probe_runs n
   JOIN credential_model_bindings b
     ON b.credential_id = n.credential_id
    AND b.provider_model_id = (SELECT pm.id FROM provider_models pm WHERE pm.raw_model_name = n.raw_model_name LIMIT 1)
   WHERE b.billing_mode = 'free'
     AND n.created_at > now() - interval '7 days'
     AND n.created_at < now() - interval '2 hours'   -- ★ 排除进行中的段
 ), seg AS (
   SELECT credential_id, raw_model_name, is_feat_now, is_ns, created_at, success,
          LEAD(success) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_success,
          LEAD(created_at)  OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
 ), rec AS (
   SELECT is_feat_now, is_ns, created_at AS fail_at,
          EXTRACT(EPOCH FROM (next_at - created_at))/60 AS lat,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre_fix' ELSE 'post_fix' END era
   FROM seg
   WHERE success = FALSE AND next_success = TRUE AND next_at IS NOT NULL
 )
SELECT 'M11_latency_'||era||'_'||CASE WHEN is_ns THEN 'namespaced' ELSE 'bare' END||'_featnow_'||CASE WHEN is_feat_now THEN 'Y' ELSE 'N' END k,
  COUNT(*)::text||' episodes | med_min='||COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY lat))::int::text,'?')
  ||' | p90_min='||COALESCE((percentile_disc(0.9) WITHIN GROUP (ORDER BY lat))::int::text,'?')
  ||' | max_min='||COALESCE((percentile_disc(1.0) WITHIN GROUP (ORDER BY lat))::int::text,'?') v
FROM rec GROUP BY era, is_ns, is_feat_now ORDER BY era, is_ns, is_feat_now;

-- ★ 同口径但按 raw 名的"当前 featured"分组的汇总（★ post_fix 段的读数才是有效对比；
--   pre_fix 段的 featnow 标签是回溯标注，只能当参考）
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 -- ★★ era2（2026-11-10 第二次部署）：featured 集合改为**统一展开** —— raw 变体来自
 --   ★ **两个来源**的 canonical（此前只展开 models_canonical.featured 一侧，漏了
 --   ★ routing_policy 侧 ⇒ 实测 106 个 namespaced 变体未覆盖 ⇒ 同一缺陷只修一半）。
 --   ★ 与 bg/model_tier.go 的装载查询保持同一套语义（feat_names UNION ⇒ JOIN 展开）。
 rawv AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn
          JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m
          JOIN provider_models pm ON pm.canonical_id = mc.id
          WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv),
 fr AS (
   SELECT n.credential_id, n.raw_model_name, n.created_at, n.success,
          (lower(btrim(n.raw_model_name)) IN (SELECT m FROM st)) is_feat_now,
          (n.raw_model_name LIKE '%/%') is_ns
   FROM node_probe_runs n
   JOIN credential_model_bindings b
     ON b.credential_id = n.credential_id
    AND b.provider_model_id = (SELECT pm.id FROM provider_models pm WHERE pm.raw_model_name = n.raw_model_name LIMIT 1)
   WHERE b.billing_mode = 'free'
     AND n.created_at > now() - interval '7 days'
     AND n.created_at < now() - interval '2 hours'
 ), seg AS (
   SELECT credential_id, raw_model_name, is_feat_now, is_ns, created_at, success,
          LEAD(success) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_success,
          LEAD(created_at)  OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
 ), rec AS (
   SELECT is_feat_now, is_ns,
          EXTRACT(EPOCH FROM (next_at - created_at))/60 AS lat,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre_fix' ELSE 'post_fix' END era
   FROM seg
   WHERE success = FALSE AND next_success = TRUE AND next_at IS NOT NULL
 ), agg AS (
   SELECT era, is_ns, COUNT(*) c,
          percentile_disc(0.5) WITHIN GROUP (ORDER BY lat) med,
          percentile_disc(0.9) WITHIN GROUP (ORDER BY lat) p90,
          percentile_disc(1.0) WITHIN GROUP (ORDER BY lat) mx
   FROM rec GROUP BY era, is_ns
 )
SELECT 'M11b_'||era||'_'||CASE WHEN is_ns THEN 'namespaced' ELSE 'bare' END k,
  c::text||' episodes | med='||COALESCE(med::int::text,'?')||' | p90='||COALESCE(p90::int::text,'?')
  ||' | max='||COALESCE(mx::int::text,'?') v
FROM agg ORDER BY era, is_ns;

-- ═══ M12 ★★★ 「失败→下次复检间隔」（★ 高样本量的及时性判据；★ 因果口径）═══
--
-- ★★ 为什么需要它（★ 本轮在窗口关闭前做的预判，2026-11-10）：
--   M11（恢复感知时延）的事件是「失败段 → 下一次成功」，免费面 **24h 只有 1 个**
--   namespaced episode（逐日 2–3 个）⇒ ★★ **24h 窗口内样本严重不足 ⇒ 判不了**。
--   ⇒ 故增加 M12：事件口径放宽为「一次失败 → 该对的下一次探测（不论成败）」，
--     **7 天样本 = 3,842 条（免费面）≈ 550 条/日** ⇒ 24h 窗口即可判定。
--
-- ★★ 因果口径（★ 关键）：featured 标签**按事件发生时点适用的集合**打，
--   而不是「用今天的集合回溯标注历史」（★ 回溯标注会造出假象）：
--     era1（< 2026-11-10 13:42:30 +08）用 st_old（92 条：rp ∪ cf ∪ **canonical 侧**展开）
--     era2（≥ 切点）用 st_new（210 条：rp ∪ cf ∪ **两个来源**统一展开）
--   ⇒ st_old 的 namespaced 条目 = 38；st_new = 144（★ +106 = 此前实测的未覆盖变体数）。
--
-- ★ 实测基线（pre 段，7 天）：ns|featured med=**3 min** vs ns|非featured med=**360 min**
--   ⇒ ★★ **20× 差距** ⇒ 这就是「免费供应商迟迟不感知恢复」的机制形态。
--   ★★ 可检验命题：era2 新纳入的 106 个变体此前落在 ns|非featured（med 360 min）
--     ⇒ 修复后该组复检间隔应显著下移 ⇒ **24h 窗口用 n≈500 的样本判定**。
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv_old AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM models_canonical mc JOIN provider_models pm ON pm.canonical_id = mc.id
          WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 rawv_new AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn
          JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m
          JOIN provider_models pm ON pm.canonical_id = mc.id
          WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st_old AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_old),
 st_new AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_new),
 fr AS (
   SELECT n.credential_id, n.raw_model_name, n.created_at, n.success,
          (n.raw_model_name LIKE '%/%') is_ns
   FROM node_probe_runs n
   JOIN credential_model_bindings b
     ON b.credential_id = n.credential_id
    AND b.provider_model_id = (SELECT pm.id FROM provider_models pm WHERE pm.raw_model_name = n.raw_model_name LIMIT 1)
   WHERE b.billing_mode = 'free'
     AND n.created_at > now() - interval '7 days'
), seg AS (
   SELECT credential_id, raw_model_name, is_ns, created_at, success,
          LEAD(created_at) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
 ), rec AS (
   SELECT is_ns, created_at AS fail_at, EXTRACT(EPOCH FROM (next_at - created_at))/60 AS recheck_min,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre' ELSE 'post' END era,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08'
               THEN (lower(btrim(raw_model_name)) IN (SELECT m FROM st_old))
               ELSE (lower(btrim(raw_model_name)) IN (SELECT m FROM st_new)) END feat_at_t
   FROM seg WHERE success = FALSE AND next_at IS NOT NULL
 )
SELECT 'M12_recheck_'||era||'_'||CASE WHEN is_ns THEN 'ns' ELSE 'bare' END||'_'||CASE WHEN feat_at_t THEN 'feat' ELSE 'not' END k,
  COUNT(*)::text||' events | med_min='||COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  ||' | p90_min='||COALESCE((percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  ||' | p99_min='||COALESCE((percentile_disc(0.99) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  ||' | max_min='||COALESCE((percentile_disc(1.0) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?') v
FROM rec GROUP BY era, is_ns, feat_at_t ORDER BY 1;

-- ═══ M12b ★ 样本量预判（★ 窗口关闭前先算「够不够判定」，别等关窗才发现样本不足）═══
-- ① M11 episode 逐日数量（★ 近 7 天才是当前常态：10-27/28 的批量故障已滑出 7 天窗口）
--    ★ 若 post_fix 段的 episode 数 < 5 ⇒ **明写「样本不足以判定」**，★ 不许外推。
-- ② M12 事件量（★ 约 550/日 ⇒ 24h 窗口足够判定）
WITH fr AS (
   SELECT n.credential_id, n.raw_model_name, n.created_at, n.success,
          (n.raw_model_name LIKE '%/%') is_ns
   FROM node_probe_runs n
   JOIN credential_model_bindings b
     ON b.credential_id = n.credential_id
    AND b.provider_model_id = (SELECT pm.id FROM provider_models pm WHERE pm.raw_model_name = n.raw_model_name LIMIT 1)
   WHERE b.billing_mode = 'free'
     AND n.created_at > now() - interval '14 days'
     AND n.created_at < now() - interval '2 hours'
), seg AS (
   SELECT credential_id, raw_model_name, is_ns, created_at, success,
          LEAD(success) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_success,
          LEAD(created_at)  OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
), rec AS (
   SELECT is_ns, created_at AS fail_at,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre' ELSE 'post' END era
   FROM seg WHERE success = FALSE AND next_success = TRUE AND next_at IS NOT NULL
)
SELECT 'M12b_episodes_'||era||'_'||CASE WHEN is_ns THEN 'ns' ELSE 'bare' END k, COUNT(*)::text v
FROM rec GROUP BY era, is_ns ORDER BY 1;

-- ★ M11 基线的窗口敏感性（★ 为什么 M11 固定用 7 天而不是 14/30 天）：
--   近 14 天 namespaced episode = 251，但 **10-27/28 两天就占 223（89%）**
--   ⇒ 那是一次批量故障的尾巴 ⇒ ★ **加宽窗口会污染基线**，不是让基线更可信。

-- ═══ M12c ★ M11 episode 的逐日分布（★ 证明「7 天窗口」是当前常态、14 天会被污染）═══
-- ★ 实测：10-27 = 32、**10-28 = 191**（一次批量故障的尾巴）⇒ 14 天窗口的 251 个
--   namespaced episode 里 **223（89%）** 来自那两天 ⇒ ★ 加宽窗口 = 污染基线。
WITH fr AS (
   SELECT n.credential_id, n.raw_model_name, n.created_at, n.success,
          (n.raw_model_name LIKE '%/%') is_ns
   FROM node_probe_runs n
   JOIN credential_model_bindings b
     ON b.credential_id = n.credential_id
    AND b.provider_model_id = (SELECT pm.id FROM provider_models pm WHERE pm.raw_model_name = n.raw_model_name LIMIT 1)
   WHERE b.billing_mode = 'free'
     AND n.created_at > now() - interval '14 days'
     AND n.created_at < now() - interval '2 hours'
), seg AS (
   SELECT credential_id, raw_model_name, is_ns, created_at, success,
          LEAD(success) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_success,
          LEAD(created_at)  OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
), rec AS (
   SELECT is_ns, created_at AS fail_at
   FROM seg WHERE success = FALSE AND next_success = TRUE AND next_at IS NOT NULL
)
SELECT 'M12c_episodes_'||to_char(date_trunc('day', fail_at),'MM-DD')||'_'||CASE WHEN is_ns THEN 'ns' ELSE 'bare' END k,
       COUNT(*)::text v
FROM rec GROUP BY 1 ORDER BY 1;

-- ═══ M12s ★★ M12 的「已结算」版本（★ 消除右删失偏差）═══
--
-- ★★ 右删失（censoring）实测（2026-11-10 14:0x，era2 部署后 1 小时）：
--   era2 切点以来免费面 14 次失败（13 bare + 1 ns）⇒ **0 次已复检**（全部待复检）；
--   而部署前 24h：bare 588 次失败 → 564 已复检(96%)、ns 400 → 320 已复检(80%)。
--   ⇒ ★★ **M12 的 post 段会系统性漏掉「复检慢」的对** ⇒ **偏差方向对我们有利**
--      （post 会显得比实际更快）⇒ ⇒ ★ **必须披露并用结算过滤修正**。
--
-- ★ M12s：只取「失败已发生 ≥6h」的事件（复检阶梯上限就是 6h ⇒ 绝大多数已结算）
--   ⇒ 与 M12 同口径、但剔除了最年轻、最容易被删失的那批。
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv_old AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM models_canonical mc JOIN provider_models pm ON pm.canonical_id = mc.id WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 rawv_new AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m JOIN provider_models pm ON pm.canonical_id = mc.id WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st_old AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_old),
 st_new AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_new),
 fr AS (
   SELECT n.credential_id, n.raw_model_name, n.created_at, n.success,
          (n.raw_model_name LIKE '%/%') is_ns
   FROM node_probe_runs n
   JOIN credential_model_bindings b
     ON b.credential_id = n.credential_id
    AND b.provider_model_id = (SELECT pm.id FROM provider_models pm WHERE pm.raw_model_name = n.raw_model_name LIMIT 1)
   WHERE b.billing_mode = 'free'
     AND n.created_at > now() - interval '7 days'
), seg AS (
   SELECT credential_id, raw_model_name, is_ns, created_at, success,
          LEAD(created_at) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
 ), rec AS (
   SELECT is_ns, created_at AS fail_at, next_at, EXTRACT(EPOCH FROM (next_at - created_at))/60 AS recheck_min,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre' ELSE 'post' END era,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08'
               THEN (lower(btrim(raw_model_name)) IN (SELECT m FROM st_old))
               ELSE (lower(btrim(raw_model_name)) IN (SELECT m FROM st_new)) END feat_at_t
   FROM seg WHERE success = FALSE
 )
SELECT 'M12s_settled_'||era||'_'||CASE WHEN is_ns THEN 'ns' ELSE 'bare' END||'_'||CASE WHEN feat_at_t THEN 'feat' ELSE 'not' END k,
  COUNT(*)::text||' events | med_min='||COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  ||' | p90_min='||COALESCE((percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  ||' | p99_min='||COALESCE((percentile_disc(0.99) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  ||' | max_min='||COALESCE((percentile_disc(1.0) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?') v
FROM rec
WHERE fail_at < now() - interval '6 hours'      -- ★ 结算过滤：失败已发生 ≥6h
  AND next_at IS NOT NULL
GROUP BY era, is_ns, feat_at_t ORDER BY 1;

-- ═══ M12p ★ 待复检计数（★ 被删失掉的那些事件 —— ★ 不是一个判据，是一个护栏）═══
-- ★ 用途：M12/M12s 的分布**看不到**「还没被复检」的失败 ⇒ 必须单独报这个数，
--   否则分布会被系统性美化。★ 实测 pre 段（部署前 48h、6h 结算）：
--   bare 935 次失败 → **0** 待复检；ns 581 → **1** 待复检
--   ⇒ ★★ **「失败后没人再去探」这个假设已被证伪**（pending ≈ 0）
--   ⇒ ★ 真正的机制不是「没人去探」，而是「**去探了，但阶梯让 ta 等 ~6 小时**」
--      （M12: ns 非 featured 的复检间隔中位 = 360 min vs ns featured = 3 min）。
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv_old AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM models_canonical mc JOIN provider_models pm ON pm.canonical_id = mc.id WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 rawv_new AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m JOIN provider_models pm ON pm.canonical_id = mc.id WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st_old AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_old),
 st_new AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_new),
 fr AS (
   SELECT n.credential_id, n.raw_model_name, n.created_at, n.success,
          (n.raw_model_name LIKE '%/%') is_ns
   FROM node_probe_runs n
   JOIN credential_model_bindings b
     ON b.credential_id = n.credential_id
    AND b.provider_model_id = (SELECT pm.id FROM provider_models pm WHERE pm.raw_model_name = n.raw_model_name LIMIT 1)
   WHERE b.billing_mode = 'free'
     AND n.created_at > now() - interval '7 days'
), seg AS (
   SELECT credential_id, raw_model_name, is_ns, created_at, success,
          LEAD(created_at) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
 ), rec AS (
   SELECT is_ns, created_at AS fail_at, next_at,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre' ELSE 'post' END era,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08'
               THEN (lower(btrim(raw_model_name)) IN (SELECT m FROM st_old))
               ELSE (lower(btrim(raw_model_name)) IN (SELECT m FROM st_new)) END feat_at_t
   FROM seg WHERE success = FALSE
 )
SELECT 'M12p_pending_'||era||'_'||CASE WHEN is_ns THEN 'ns' ELSE 'bare' END||'_'||CASE WHEN feat_at_t THEN 'feat' ELSE 'not' END k,
  COUNT(*)::text||' failures_aged6h | pending='||COUNT(*) FILTER (WHERE next_at IS NULL)::text
  ||' | pending_pct='||ROUND(100.0*COUNT(*) FILTER (WHERE next_at IS NULL)/COUNT(*),1)::text v
FROM rec WHERE fail_at < now() - interval '6 hours'
GROUP BY era, is_ns, feat_at_t ORDER BY 1;
