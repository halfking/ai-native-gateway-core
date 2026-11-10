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
-- ⚠⚠⚠ ★★★ 窗口**必须锚定到 era 切点**（2026-11-10 15:2x 修正，★ 本轮第 4 处量具缺陷）：
--   原先所有 pre/post 对比块的取数窗口写成 `created_at > now() - interval '7 days'`
--   ★★ 这是**滚动窗口** ⇒ ★★ **pre 段的样本随「查询时刻」漂移**，实测两处硬证据：
--     ① Stage 2 模拟（now = 2026-11-17 13:45）：窗口起点 = 2026-11-10 13:45
--        **晚于** era 切点 13:42:30 ⇒ ★★ **pre 段整段为空** ⇒ 实测 `M11b_pre_*` /
--        `M12s_settled_pre_*` / `M14_pre_*` / `M15_pre__*` / `M12p_pending_pre_*`
--        **全部消失** ⇒ Stage 2 的 pre-vs-post 对照会失去 pre 侧。
--     ② Stage 1 模拟（now = 2026-11-11 13:45）：pre 段只剩 **6 天**
--        ⇒ `M14_pre_newly_covered` 的 p90 从锚点值 **360 掉到 47**
--        ⇒ ★★ 复采的 pre 与基线**不同源**（差一个整天的样本量）。
--   ⇒ 改为 `created_at > TIMESTAMPTZ '<切点>' - interval '7 days'`
--     ⇒ ★ pre 恒为「切点前 7 天」、post 恒为「切点 → now」
--     ⇒ ★★ **Stage 1 / Stage 2 / T1 锚点共用同一个 pre 样本** ⇒ 前后可比、基线可复现。
--   ★ 保留滚动的两类（★ 它们是**绝对量**，与 era 无关）：
--     `now() - interval '24 hours'`（M3/M5 探测量）、`now() - interval '6 hours'`（结算成熟度）。
--
-- ⚠ psql 语法坑（已踩 3 次）：★ `GROUP BY 2` 里若 2 是**聚合表达式**会报
--   "aggregate functions are not allowed in GROUP BY" ⇒ 必须「子查询包一层 + 外层聚合」。
--
-- ★★★★ 口径修订（2026-11-10 14:1x，★ **本轮自查抓到的真缺陷**）：
--   原先绑定过滤写成
--     AND b.provider_model_id = (SELECT pm.id FROM provider_models pm
--                                WHERE pm.raw_model_name = n.raw_model_name LIMIT 1)
--   ★★ 但 `provider_models.raw_model_name` 是**一对多**（实测：441 个名有 2 行、
--   `zcode/GLM-5.3` 有 **9 行**）⇒ ★ `LIMIT 1` 只取**任意一行** ⇒ ★ **大量事件被丢弃**：
--   实测免费面 7 天 strict = **4,073** 事件 vs 正确口径 = **13,555**（只统计到 30%）。
--   ⇒ ★ M11 / M12 / M12s / M12p / M14 / M15 全部受影响。
--   ⇒ 改为**半连接（EXISTS）**：`EXISTS (… provider_models pm WHERE pm.raw_model_name = n.raw_model_name …)`
--     ★ 半连接**不会放大行数**（实测 `(credential_id, raw_model_name)` 每个组合只有 1 个绑定，
--       4,053 个组合全部 dup=1）⇒ 增加的事件**全是找回的被丢弃事件**。
--   ⇒ ★★ **修正后的基线**（pre 段 7 天）：M11 namespaced **49 episodes** med=3 p90=360 max=**1791**
--     （修正前 17 / 6 / 511 / 1791 ⇒ ★★ 样本率 2–3/日 ⇒ **7/日**，★ Stage 1 可判了）；
--     bare 36 / med 3 / p90 180 / max 360；M12 ns feat n=437 med=3 **p90=32**、ns not n=1723 med=360 p90=361。
--   ⇒ ★★ **定性结论不变且更强**：ns featured vs 非 featured 的复检间隔差 **20×**（p90 上 11×），
--     且 namespaced 的 max 仍是 **1791 分钟（29.9h）** ⇒ 「~24h 未感知恢复」的尾部症状确认。

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
   FROM node_probe_runs n
   -- ★★ 锚定（★ 本轮第 7 处口径修正）：pre 段固定为「切点前 24h」，post 段为「切点→now」
   --   ⇒ 否则这两个 era 对比块的 pre 侧只在关窗后 24h 内存在（Stage 2 时整段消失）
   WHERE n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '24 hours'
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
   FROM node_probe_runs n
   -- ★★ 锚定（★ 本轮第 7 处口径修正）：pre 段固定为「切点前 24h」，post 段为「切点→now」
   --   ⇒ 否则这两个 era 对比块的 pre 侧只在关窗后 24h 内存在（Stage 2 时整段消失）
   WHERE n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '24 hours'
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
   WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id = b.provider_model_id
                 WHERE b.credential_id = n.credential_id
                   AND b.billing_mode = 'free'
                   AND pm.raw_model_name = n.raw_model_name)
     AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
     -- ★★ era-aware（★ 本轮第 7 处口径修正）：★ 只对 **post** 侧排除「进行中的段」
     --   ⇒ pre 侧全留 ⇒ ★ pre 的「下一次探测」不会被 now 相对过滤吃掉
     --   （否则 pre 样本随查询时刻漂移：实测 M11b_pre 49 ↔ 50）
     AND (n.created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' OR n.created_at < now() - interval '2 hours')   -- ★ 排除进行中的段
 ), seg AS (
   SELECT credential_id, raw_model_name, is_feat_now, is_ns, created_at, success,
          LEAD(success) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_success,
          LEAD(created_at)  OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
 ), rec0 AS (
   SELECT is_feat_now, is_ns, created_at AS fail_at, next_at,
          EXTRACT(EPOCH FROM (next_at - created_at))/60 AS lat,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre_fix' ELSE 'post_fix' END era
   FROM seg
   WHERE success = FALSE AND next_success = TRUE AND next_at IS NOT NULL
 ), rec AS (
   -- ★★ era 纯度（★ 本轮第 6 处口径修正）：pre 段的 episode 必须**整段发生在切点之前**
   --   （下一次成功也要在切点前）⇒ 否则「修复前的恢复感知时延」里混进**修复后**的恢复
   --   （实测 max 可差 1791 分钟）⇒ ★ pre 侧条件钉在**切点**上 ⇒ pre 样本永远冻结
   SELECT * FROM rec0 WHERE era = 'post_fix' OR next_at < TIMESTAMPTZ '2026-11-10 13:42:30+08'
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
   WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id = b.provider_model_id
                 WHERE b.credential_id = n.credential_id
                   AND b.billing_mode = 'free'
                   AND pm.raw_model_name = n.raw_model_name)
     AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
     -- ★★ era-aware（★ 本轮第 7 处口径修正）：★ 只对 **post** 侧排除「进行中的段」
     --   ⇒ pre 侧全留 ⇒ ★ pre 的「下一次探测」不会被 now 相对过滤吃掉
     --   （否则 pre 样本随查询时刻漂移：实测 M11b_pre 49 ↔ 50）
     AND (n.created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' OR n.created_at < now() - interval '2 hours')
 ), seg AS (
   SELECT credential_id, raw_model_name, is_feat_now, is_ns, created_at, success,
          LEAD(success) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_success,
          LEAD(created_at)  OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
 ), rec0 AS (
   SELECT is_feat_now, is_ns, next_at,
          EXTRACT(EPOCH FROM (next_at - created_at))/60 AS lat,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre_fix' ELSE 'post_fix' END era
   FROM seg
   WHERE success = FALSE AND next_success = TRUE AND next_at IS NOT NULL
 ), rec AS (
   -- ★★ era 纯度（见 M11 块同款说明）：pre episode 的「恢复」必须在切点前被观测
   SELECT * FROM rec0 WHERE era = 'post_fix' OR next_at < TIMESTAMPTZ '2026-11-10 13:42:30+08'
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
   WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id = b.provider_model_id
                 WHERE b.credential_id = n.credential_id
                   AND b.billing_mode = 'free'
                   AND pm.raw_model_name = n.raw_model_name)
     AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
), seg AS (
   SELECT credential_id, raw_model_name, is_ns, created_at, success,
          LEAD(created_at) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
 ), rec0 AS (
   SELECT is_ns, created_at AS fail_at, next_at, EXTRACT(EPOCH FROM (next_at - created_at))/60 AS recheck_min,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre' ELSE 'post' END era,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08'
               THEN (lower(btrim(raw_model_name)) IN (SELECT m FROM st_old))
               ELSE (lower(btrim(raw_model_name)) IN (SELECT m FROM st_new)) END feat_at_t
   FROM seg WHERE success = FALSE AND next_at IS NOT NULL
 ), rec AS (
   -- ★★ era 纯度（★ 与 M12s/M14/M15 同款）：pre 侧的下一次探测必须在切点前
   --   ⇒ pre 样本冻结、pre/post 可比（M12 是**未结算**版本 ⇒ post 有右删失，仅作参考）
   SELECT * FROM rec0 WHERE era = 'post' OR next_at < TIMESTAMPTZ '2026-11-10 13:42:30+08'
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
   WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id = b.provider_model_id
                 WHERE b.credential_id = n.credential_id
                   AND b.billing_mode = 'free'
                   AND pm.raw_model_name = n.raw_model_name)
     AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '14 days'
     -- ★★ era-aware（★ 本轮第 7 处口径修正）：★ 只对 **post** 侧排除「进行中的段」
     --   ⇒ pre 侧全留 ⇒ ★ pre 的「下一次探测」不会被 now 相对过滤吃掉
     --   （否则 pre 样本随查询时刻漂移：实测 M11b_pre 49 ↔ 50）
     AND (n.created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' OR n.created_at < now() - interval '2 hours')
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
   WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id = b.provider_model_id
                 WHERE b.credential_id = n.credential_id
                   AND b.billing_mode = 'free'
                   AND pm.raw_model_name = n.raw_model_name)
     AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '14 days'
     -- ★★ era-aware（★ 本轮第 7 处口径修正）：★ 只对 **post** 侧排除「进行中的段」
     --   ⇒ pre 侧全留 ⇒ ★ pre 的「下一次探测」不会被 now 相对过滤吃掉
     --   （否则 pre 样本随查询时刻漂移：实测 M11b_pre 49 ↔ 50）
     AND (n.created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' OR n.created_at < now() - interval '2 hours')
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
   WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id = b.provider_model_id
                 WHERE b.credential_id = n.credential_id
                   AND b.billing_mode = 'free'
                   AND pm.raw_model_name = n.raw_model_name)
     AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
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
WHERE next_at IS NOT NULL
  -- ★★ 成熟度**分 era 钉住**（★ 本轮第 6 处口径修正）：
  --   pre 侧：成熟度 + era 纯度都以**切点**为参照 ⇒ ★★ pre 样本**冻结**
  --     （否则 pre 随查询时刻漂移：实测同一锚点下 pre 事件数 1833 → 332）
  --   post 侧：成熟度相对 now（post 事件要 6h 才结算）⇒ 随时间单调增长（这是有意的）
  AND ( (era = 'pre'  AND fail_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '6 hours'
                     AND next_at < TIMESTAMPTZ '2026-11-10 13:42:30+08')
     OR (era = 'post' AND fail_at < now() - interval '6 hours') )
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
   WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id = b.provider_model_id
                 WHERE b.credential_id = n.credential_id
                   AND b.billing_mode = 'free'
                   AND pm.raw_model_name = n.raw_model_name)
     AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
), seg AS (
   SELECT credential_id, raw_model_name, is_ns, created_at, success,
          LEAD(created_at) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
 ), rec AS (
   SELECT is_ns, created_at AS fail_at, next_at,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre' ELSE 'post' END era,
          -- ★★ **同量纲对照段**（★ 本轮第 5 处口径修正）：
          --   pending 的定义是「该次失败之后**再没有任何探测**」⇒ ★ **窗口越长，
          --   越容易把「之后一直没被探」的对（停放/长退避）算成 pending**
          --   ⇒ 实测锚定 7 天 pre 的 pending_pct = **5.8%**，而 Stage 1 的 post 段只有 24h
          --   ⇒ ★★ **7 天 pre 与 24h post 直接比是不同量纲** ⇒ 另输出「切点前 24h」段，
          --   与 post(24h) 同量纲 ⇒ ★ 护栏才成立。
          -- ★★ 标签与边界一致（★ 同 M14：★ CASE 顺序写反 ⇒ 标签反了）
          -- ★★★ **窗口维度改成四桶**（★ 本轮第 10 处口径修正，★ 详见下方 exp CTE）：
          --   ★★ 原来三桶（'pre7d' 实际只覆盖 [切点-7d, 切点-24h) ⇒ **只有 6 天**）
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08'
               THEN (lower(btrim(raw_model_name)) IN (SELECT m FROM st_old))
               ELSE (lower(btrim(raw_model_name)) IN (SELECT m FROM st_new)) END feat_at_t
   FROM seg WHERE success = FALSE
 ), exp AS (
   -- ★★★ **窗口维度四桶**（★ 本轮第 10 处口径修正）：
   --   ★★ 原来只有三桶：'pre24'（= 切点前 24h ✔）+ 'pre7d'（= [切点-7d, 切点-24h)
   --   ⇒ **实际只有 6 天**，却叫 'pre7d'）★ ⇒ Stage 2 拿「6 天 pre」去比「7 天 post」
   --   ⇒ ★★ **不同量纲**（★ 本轮纪律的核心：判据基线必须与观测同量纲）
   --   ⇒ ★★ 现在四桶：**pre6d**（6 天）/ **pre24**（24h）/ **pre7d**（**整 7 天**）/ **post**
   --   ★★ pre6d 与 pre24 **正好拼成 pre7d** ⇒ 事件数必须满足 **pre7d = pre6d + pre24**
   --      （★ 回归门：可对拍，且能立刻发现标签与边界不符）
   --   ★★ 成熟度过滤是**逐行**的（fail_at < 切点-6h）⇒ 对四桶同时生效 ⇒ 上面的加法恒成立
   SELECT r.*, 'pre6d' win FROM rec r WHERE r.fail_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '24 hours'
   UNION ALL
   SELECT r.*, 'pre24' win FROM rec r WHERE r.fail_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '24 hours'
                                          AND r.fail_at <  TIMESTAMPTZ '2026-11-10 13:42:30+08'
   UNION ALL
   SELECT r.*, 'pre7d' win FROM rec r WHERE r.fail_at <  TIMESTAMPTZ '2026-11-10 13:42:30+08'
   UNION ALL
   SELECT r.*, 'post'  win FROM rec r WHERE r.fail_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08'
 )
SELECT 'M12p_pending_'||win||'_'||CASE WHEN is_ns THEN 'ns' ELSE 'bare' END||'_'||CASE WHEN feat_at_t THEN 'feat' ELSE 'not' END k,
  COUNT(*)::text||' failures_aged6h | pending='||COUNT(*) FILTER (WHERE next_at IS NULL)::text
  ||' | pending_pct='||ROUND(100.0*COUNT(*) FILTER (WHERE next_at IS NULL)/COUNT(*),1)::text v
FROM exp
WHERE next_at IS NOT NULL
  -- ★★ 成熟度**分 era 钉住**（★ 本轮第 6 处口径修正）：
  --   pre 侧：成熟度 + era 纯度都以**切点**为参照 ⇒ ★★ pre 样本**冻结**
  --     （否则 pre 随查询时刻漂移：实测同一锚点下 pre 事件数 1833 → 332）
  --   post 侧：成熟度相对 now（post 事件要 6h 才结算）⇒ 随时间单调增长（这是有意的）
  AND ( (era = 'pre'  AND fail_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '6 hours'
                     AND next_at < TIMESTAMPTZ '2026-11-10 13:42:30+08')
     OR (era = 'post' AND fail_at < now() - interval '6 hours') )
GROUP BY win, is_ns, feat_at_t ORDER BY 1;

-- ═══ M14 ★★★ 直指目标人群：★「那 106 个新覆盖变体」的复检节奏，前 vs 后 ═══
--
-- ★★ 为什么需要它：M12 的 post 段 ns|feat 分组会**同时**包含
--   ① era1 就已是 featured 的（复检快，med≈3 min）
--   ② ★ era2 新纳入的 106 个（此前是 ns|非featured，med≈360 min）
--   ⇒ ★★ **混合后中位会被新纳入的那批「抬高」** ⇒ ★ **「post 中位下降」并不是正确的
--   期望形态**（甚至可能反向）⇒ ⇒ ★★ 必须**把新纳入的那批单独切出来**比。
--
-- ★ 判读形态（★ 已在窗口关闭前写死）：
--   newly_covered 组：pre 段它们属于「ns 非 featured」（med≈360 min）
--                     post 段它们属于「ns featured」⇒ ★★ **期望 med 明显下移（≈180 或更低）**
--   old_feat 组：     两段都应是快节奏（med≈3 min）⇒ 作为对照（★ 不应变慢）
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv_old AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM models_canonical mc JOIN provider_models pm ON pm.canonical_id = mc.id WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 rawv_new AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m JOIN provider_models pm ON pm.canonical_id = mc.id WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st_old AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_old),
 st_new AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_new),
 -- ★ 新覆盖集合 = 在 st_new 的 namespaced 条目里、但不在 st_old 的
 newly AS (SELECT m FROM st_new WHERE m LIKE '%/%' AND m NOT IN (SELECT m FROM st_old)),
 fr AS (
   SELECT n.credential_id, n.raw_model_name, n.created_at, n.success,
          (n.raw_model_name LIKE '%/%') is_ns,
          -- ★★★ **计费口径维度**（★ 本轮第 9 处口径修正）：
          --   ★★ 原来 `fr` 直接限定 `b.billing_mode='free'` ⇒ ★★ **同一个模型名通常同时挂在
          --   free / per_token / token_plan 三种凭据上**（实测：minimaxai/minimax-m2.7 有
          --   free(cred19) + per_token(cred8/18) + token_plan(cred23)）⇒ ★★ 只取 free
          --   ⇒ **静默丢掉另外两类凭据上的探测** ⇒ ★★ 样本量被系统性压缩（★ 与「一对多
          --   取代表行」同类的**不报错、只少报**缺陷）⇒ ★★ 新覆盖人群尤其严重
          --   （★★ post era 实测 5 次新覆盖探测**全部**在 per_token 上 ⇒ free 口径下 post = 0
          --   ⇒ 效应判据在 free 口径下**永远空**，会被误读成「修复没生效」）。
          --   ⇒ ★★ 改为：**全量入池 + 打 has_free 标记** ⇒ 同时产出两个口径：
          --      · scope='free' = 该对**有** free 绑定（★ **症状切片**，与原口径逐字节一致）
          --      · scope='all'  = 该对**已绑定**（★ **模型级全量**，效应判据用它拿样本量）
          --   ★★ **不丢样本的关键**：LEAD 的分区键是 (credential_id, raw_model_name) ⇒
          --   ★★ 换凭据就是**不同分区** ⇒ ★★ 把 free 过滤从「入池前」移到「打标后」，
          --   ★★ **free 切片的 next_at 与原口径逐字节相同**（★ 有回归门可验）
          EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id = b.provider_model_id
                 WHERE b.credential_id = n.credential_id
                   AND b.billing_mode = 'free'
                   AND pm.raw_model_name = n.raw_model_name) has_free
   FROM node_probe_runs n
   WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id = b.provider_model_id
                 WHERE b.credential_id = n.credential_id
                   AND pm.raw_model_name = n.raw_model_name)
     AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
), seg AS (
   SELECT credential_id, raw_model_name, is_ns, created_at, success, has_free,
          LEAD(created_at) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
 ), rec AS (
   SELECT is_ns, has_free, created_at AS fail_at, next_at, EXTRACT(EPOCH FROM (next_at - created_at))/60 AS recheck_min,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre' ELSE 'post' END era,
          CASE WHEN lower(btrim(raw_model_name)) IN (SELECT m FROM newly) THEN 'newly_covered'
               WHEN lower(btrim(raw_model_name)) IN (SELECT m FROM st_old) THEN 'old_feat'
               ELSE 'other' END grp_new
   FROM seg WHERE success = FALSE AND is_ns
 ), exp AS (
   -- ★★★ **窗口四桶 × 计费两口径**（★ 本轮第 9/10 处口径修正）
   --   ★★ 窗口四桶：pre6d（6 天）/ pre24（24h）/ **pre7d（整 7 天）**/ post
   --      （★ 原来 'pre7d' 实际只覆盖 [切点-7d, 切点-24h) ⇒ **只有 6 天**）
   --      ⇒ ★★ pre6d + pre24 **正好拼成 pre7d** ⇒ 事件数必须满足 **pre7d = pre6d + pre24**（★ 可对拍）
   --   ★★ 计费两口径：'free'（有 free 绑定 ⇒ 症状切片）/ 'all'（已绑定 ⇒ 模型级全量）
   SELECT r.*, 'free' scope, 'pre6d' win FROM rec r WHERE r.has_free AND r.fail_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '24 hours'
   UNION ALL
   SELECT r.*, 'free', 'pre24' FROM rec r WHERE r.has_free AND r.fail_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '24 hours'
                                          AND r.fail_at <  TIMESTAMPTZ '2026-11-10 13:42:30+08'
   UNION ALL
   SELECT r.*, 'free', 'pre7d' FROM rec r WHERE r.has_free AND r.fail_at <  TIMESTAMPTZ '2026-11-10 13:42:30+08'
   UNION ALL
   SELECT r.*, 'free', 'post'  FROM rec r WHERE r.has_free AND r.fail_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08'
   UNION ALL
   SELECT r.*, 'all', 'pre6d' FROM rec r WHERE r.fail_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '24 hours'
   UNION ALL
   SELECT r.*, 'all', 'pre24' FROM rec r WHERE r.fail_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '24 hours'
                                          AND r.fail_at <  TIMESTAMPTZ '2026-11-10 13:42:30+08'
   UNION ALL
   SELECT r.*, 'all', 'pre7d' FROM rec r WHERE r.fail_at <  TIMESTAMPTZ '2026-11-10 13:42:30+08'
   UNION ALL
   SELECT r.*, 'all', 'post'  FROM rec r WHERE r.fail_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08'
 )
SELECT CASE WHEN scope = 'all' THEN 'M14x_' ELSE 'M14_' END || win || '_' || grp_new k,
  COUNT(*)::text||' events | med_min='||COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  ||' | p90_min='||COALESCE((percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  ||' | p99_min='||COALESCE((percentile_disc(0.99) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  ||' | max_min='||COALESCE((percentile_disc(1.0) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  -- ★★ 冷簇**速率**（★ 比分位数稳）：★ newly_covered 是双峰群体（热对 med≈3 / 冷对 med≈360）
  --   ⇒ p90 处于**边界**（实测 461 事件里约 16 个冷事件就能把 p90 从 47 顶到 360）
  --   ⇒ ★★ 分位数脆 ⇒ 另报「>300min 的慢事件占比」⇒ 群体层判据用这个
  ||' | slow_gt300_pct='||ROUND(100.0*COUNT(*) FILTER (WHERE recheck_min > 300)/COUNT(*),1)::text v
FROM exp
WHERE next_at IS NOT NULL
  -- ★★ 成熟度**分 era 钉住**（★ 本轮第 6 处口径修正）：
  --   pre 侧：成熟度 + era 纯度都以**切点**为参照 ⇒ ★★ pre 样本**冻结**
  --     （否则 pre 随查询时刻漂移：实测同一锚点下 pre 事件数 1833 → 332）
  --   post 侧：成熟度相对 now（post 事件要 6h 才结算）⇒ 随时间单调增长（这是有意的）
  --   ★★ 成熟度是**逐行**的 ⇒ 对四桶同时生效 ⇒ **pre7d = pre6d + pre24** 恒成立（★ 回归门）
  AND ( (era = 'pre'  AND fail_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '6 hours'
                     AND next_at < TIMESTAMPTZ '2026-11-10 13:42:30+08')
     OR (era = 'post' AND fail_at < now() - interval '6 hours') )
GROUP BY scope, win, grp_new ORDER BY 1;

-- ═══ M15 ★★★ 逐对金丝雀：★「新覆盖人群里那些冷但仍在被探」的对，pre vs post ═══
--
-- ★★ 目标人群（106 个新覆盖变体）的真实活跃度（211 真库，7 天）：**只有 7 个对被探到**、
--   共 96 次事件（≈13.7/日）⇒ ★★ **Stage 1（24h）该人群样本 ≈14 条 ⇒ p90/p99 无意义**。
--   ★★ 且目标人群内部**两极**：`vapeur/gpt-5.5`（热，med 3 min）vs
--   `minimaxai/minimax-m2.7` / `minimaxai/minimax-m3` /
--   `deepseek-ai/deepseek-v4-pro-0813`（冷但在被探，各 14 次、**pre med=360 min**）。
--   ⇒ ★★ **「这批人的 post 中位会整体下移」不是成立的整体命题** ⇒ 必须**逐对**看。
--
-- ★ 判读形态（★ 窗口关闭前写死）：
--   ★ 冷对（pre med ≈ 360 min）在 post 段应**明显下移**（backoff 减半 ⇒ ≈180 min 或更低）
--     —— ★ 这就是对「匹配键修复」在**目标人群**上的直接、可观测检验（金丝雀）。
--   ★ 热对（pre med ≈ 3 min）应保持不变（对照项，不应变慢）。
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv_old AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM models_canonical mc JOIN provider_models pm ON pm.canonical_id = mc.id WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 rawv_new AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m JOIN provider_models pm ON pm.canonical_id = mc.id WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st_old AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_old),
 st_new AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_new),
 newly AS (SELECT m FROM st_new WHERE m LIKE '%/%' AND m NOT IN (SELECT m FROM st_old)),
 fr AS (
   SELECT n.credential_id, n.raw_model_name, n.created_at, n.success,
          (n.raw_model_name LIKE '%/%') is_ns
   FROM node_probe_runs n
   WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id = b.provider_model_id
                 WHERE b.credential_id = n.credential_id
                   AND b.billing_mode = 'free'
                   AND pm.raw_model_name = n.raw_model_name)
     AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
), seg AS (
   SELECT credential_id, raw_model_name, is_ns, created_at, success,
          LEAD(created_at) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
 ), rec AS (
   SELECT raw_model_name, created_at AS fail_at, next_at, EXTRACT(EPOCH FROM (next_at - created_at))/60 AS recheck_min,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre' ELSE 'post' END era
   FROM seg WHERE success = FALSE AND is_ns
     AND lower(btrim(raw_model_name)) IN (SELECT m FROM newly)
 )
SELECT 'M15_'||era||'__'||raw_model_name k,
  COUNT(*)::text||' events | med_min='||COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  ||' | p90_min='||COALESCE((percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  ||' | max_min='||COALESCE((percentile_disc(1.0) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?') v
FROM rec
WHERE next_at IS NOT NULL
  -- ★★ 成熟度**分 era 钉住**（★ 本轮第 6 处口径修正）：
  --   pre 侧：成熟度 + era 纯度都以**切点**为参照 ⇒ ★★ pre 样本**冻结**
  --     （否则 pre 随查询时刻漂移：实测同一锚点下 pre 事件数 1833 → 332）
  --   post 侧：成熟度相对 now（post 事件要 6h 才结算）⇒ 随时间单调增长（这是有意的）
  AND ( (era = 'pre'  AND fail_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '6 hours'
                     AND next_at < TIMESTAMPTZ '2026-11-10 13:42:30+08')
     OR (era = 'post' AND fail_at < now() - interval '6 hours') )
GROUP BY era, raw_model_name ORDER BY 1;

-- ═══ M15x ★★★★ **逐对金丝雀·分层口径**（★ 本轮第 13 处口径修正）═══
--
-- ★★ 为什么必须有它（★ 本轮真踩到，★★ **两个口径混用 ⇒ 结论相反**）：
--   ★ M15 的旧键（`M15_pre__*` / `M15_post__*`）用的是 **free 切片**
--     ⇒ 实测（211 真库，pre7d）：★ **四只金丝雀完全同形** —— 各 13 条事件，
--        其中 **12 条是豁免类**（http_410 等 protocol 形态）⇒ ★ 剔豁免后**只剩 1 条**
--        ⇒ ★★ 「post med ≤180」**结构性不可达**（协议形态设计上钉 6h、且**豁免 featured 乘数**）
--        ⇒ ★★ 且剔豁免后**样本不足** ⇒ ★★ free 切片下**四只全部只能是观察面**
--   ★★ 而**全计费口径**下（★ 与 M14x / M20 同口径）：
--        deepseek-ai/deepseek-v4-pro-0813  normal **41** 条（pre7d）/ **18** 条（pre24）
--          ⇒ ★★ 它是**唯一**一只 normal 样本足够的金丝雀 ⇒ 逐对判据只能落在它身上
--        其余三只 normal 仅 1 / 2 / 1 条 ⇒ 仍是观察面
--   ⇒ ★★ **同一只模型在两个口径下结论相反** ⇒ ★★ 「deepseek normal 47 条」（全计费口径）
--     与「deepseek normal 1 条」（free 切片）是**两个口径** ⇒ ★★ 必须**分行报告**，不得混说
--     （★ 这正是本轮差点写进结论的一个错误）
--
-- ★★ 判据（★ **数据驱动**，★ 不硬编码模型名 ⇒ 换一批金丝雀也成立）：
--   每只金丝雀按**它自己**的 pre 实测决定是「判据」还是「观察面」：
--     · normal 样本 < 10 ⇒ ⚠️ 观察面（★ 剔豁免后样本不足）
--     · 否则 ⇒ 判据行，三个分量（★ 阈值**全部由 pre 实测推导**，★ 不拍脑袋）：
--         ① post normal 复检间隔中位 ≤ pre 的一半（★ 观察层）
--         ② post normal **阶梯中位** ≤ pre 的一半（★ **机制原生**、零余量、无调度噪声）
--         ③ **未减半档位清零**：★ 只对「pre 期该只 normal 实际出现过」的档位（3600/7200/21600）
--            要求 post 计数为 0；★ pre 期计数为 0 的档位 ⇒ 该分量**恒真** ⇒ 判定器必须标注
--            「★ 该档位 pre 计数=0 ⇒ 恒真 ⇒ 不作判据」（★ 否则就是**恒真判据**＝门没牙）
--
-- ★★ **口径一致性要求（★ 关键）**：★ 本块的豁免分类 **必须与 M20 块逐字同款**
--   （★ 两处分类若漂移 ⇒ M15x 的 normal 子集与 M20 的 normal 层会不一致 ⇒ 读数自相矛盾）
--   ⇒ ★★ 改动时**两处必须同步**（★ 本块下方已注明）。
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv_old AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM models_canonical mc JOIN provider_models pm ON pm.canonical_id = mc.id WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 rawv_new AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m JOIN provider_models pm ON pm.canonical_id = mc.id WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st_old AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_old),
 st_new AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_new),
 newly AS (SELECT m FROM st_new WHERE m LIKE '%/%' AND m NOT IN (SELECT m FROM st_old)),
 fr AS (
   -- ★★ **全计费口径**（★ 与 M14x / M20 同口径 ⇒ 样本量 5.6×）
   --   ★★ 注意：M15 旧块是 free 切片 ⇒ ★★ 两个口径**分行报告**，不得混说（★ 本轮第 13 处修正）
   SELECT n.credential_id, n.raw_model_name, n.created_at, n.success, n.next_retry_seconds,
          n.direct_http_status, n.direct_err_code, n.response_body,
          c.quota_state, c.quota_recover_at
   FROM node_probe_runs n LEFT JOIN credentials c ON c.id = n.credential_id
   WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id=b.provider_model_id
                 WHERE b.credential_id=n.credential_id AND pm.raw_model_name=n.raw_model_name)
     AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
     -- ★ 金丝雀白名单（★ 与判定器 CANARIES 一一对应；★ 改动必须**两边同步**）
     AND n.raw_model_name IN ('minimaxai/minimax-m2.7','minimaxai/minimax-m3',
                               'z-ai/glm-5.3','deepseek-ai/deepseek-v4-pro-0813')
), seg AS (
   SELECT credential_id, raw_model_name, created_at, success, next_retry_seconds,
          -- ★★ 豁免分类：**必须与 M20 块逐字同款**（★ 两处漂移 ⇒ 读数自相矛盾）
          CASE WHEN direct_http_status IN (404,405,410,415)
                 OR (direct_http_status IN (400,422)
                     AND NOT (lower(coalesce(response_body,'')) ~ 'insufficient_quota|insufficient balance|quota exceeded|exceeded your current quota|billing|arrears|unpaid|balance is not enough|not enough balance|余额不足|欠费|额度不足|余额已不足|账户余额'))
                 OR direct_err_code IN ('http_404','probe_http_404')
                 OR (quota_state IN ('periodic_exhausted','balance_exhausted','permanently_exhausted')
                     AND quota_recover_at > created_at)
               THEN 'exempt' ELSE 'normal' END AS exemption,
          LEAD(created_at) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) AS next_at,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre' ELSE 'post' END AS era
   FROM fr
), rec AS (
   SELECT raw_model_name, created_at AS fail_at, next_at, next_retry_seconds, exemption, era,
          EXTRACT(EPOCH FROM (next_at-created_at))/60 AS recheck_min
   FROM seg WHERE NOT success AND next_at IS NOT NULL
     AND lower(btrim(raw_model_name)) IN (SELECT m FROM newly)
), exp AS (
   SELECT r.*, 'pre24' win FROM rec r WHERE r.era='pre' AND r.fail_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '24 hours'
   UNION ALL
   SELECT r.*, 'pre7d' FROM rec r WHERE r.era='pre'
   UNION ALL
   SELECT r.*, 'post' FROM rec r WHERE r.era='post'
)
SELECT 'M15x_'||win||'__'||raw_model_name k,
  COUNT(*)::text||' events | exempt='||(COUNT(*) FILTER (WHERE exemption='exempt'))::text
  ||' | normal='||(COUNT(*) FILTER (WHERE exemption='normal'))::text
  ||' | normal_med_min='||COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY recheck_min) FILTER (WHERE exemption='normal'))::int::text,'?')
  ||' | normal_ladder_p50_s='||COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY next_retry_seconds) FILTER (WHERE exemption='normal'))::int::text,'?')
  ||' | normal_top3600_n='||(COUNT(*) FILTER (WHERE exemption='normal' AND next_retry_seconds=3600))::text
  ||' | normal_top7200_n='||(COUNT(*) FILTER (WHERE exemption='normal' AND next_retry_seconds=7200))::text
  ||' | normal_top21600_n='||(COUNT(*) FILTER (WHERE exemption='normal' AND next_retry_seconds=21600))::text
  ||' | normal_ladder_max_s='||COALESCE((max(next_retry_seconds) FILTER (WHERE exemption='normal'))::text,'?') v
FROM exp
WHERE ( (era = 'pre'  AND fail_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '6 hours'
                     AND next_at < TIMESTAMPTZ '2026-11-10 13:42:30+08')
     OR (era = 'post' AND fail_at < now() - interval '6 hours') )
GROUP BY win, raw_model_name ORDER BY 1;

-- ═══ M16 ★★ 口径自检（★ 常驻体检：防止绑定 join 再次静默漂移）═══
-- ★ 背景：2026-11-10 自查抓到 —— 绑定过滤若写成
--   `provider_model_id = (SELECT pm.id … WHERE raw_model_name = … LIMIT 1)`，
--   而 `provider_models.raw_model_name` 是**一对多** ⇒ ★ 静默丢掉 70% 事件
--   （strict 4,073 vs loose 13,555）⇒ ★★ 基线全错、判定计划翻转。
--
-- ★★ 本块的设计要点（★ 第一版这里写错了，被自查抓到）：
--   ★ **守卫本身必须是正确的** ⇒ 决不能拿「旧写法 vs 现写法」来判退化
--   （那会**恒定误报**：旧写法本来就丢 70%）⇒ ★ 正确形态是
--   ★ **「脚本实际使用的口径」vs「另一种独立写法的等价口径」**，两者必须**相等**；
--   旧写法只作为**对照背景**打印，不参与判定。
WITH fr_used AS (   -- ★ 脚本实际使用的口径（半连接 EXISTS）
   SELECT n.id
   FROM node_probe_runs n
   WHERE n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
     AND EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id = b.provider_model_id
                 WHERE b.credential_id = n.credential_id AND b.billing_mode = 'free'
                   AND pm.raw_model_name = n.raw_model_name)
), fr_ref AS (     -- ★ 独立等价写法（显式 JOIN + COUNT(DISTINCT n.id)）
   SELECT DISTINCT n.id
   FROM node_probe_runs n
   JOIN credential_model_bindings b ON b.credential_id = n.credential_id AND b.billing_mode = 'free'
   JOIN provider_models pm ON pm.id = b.provider_model_id AND pm.raw_model_name = n.raw_model_name
   WHERE n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
), fr_legacy AS (  -- ★ 旧写法（★ 仅作背景对照：★ 已知会丢 70% ⇒ 不参与判定）
   SELECT n.id
   FROM node_probe_runs n
   JOIN credential_model_bindings b
     ON b.credential_id = n.credential_id
    AND b.provider_model_id = (SELECT pm.id FROM provider_models pm WHERE pm.raw_model_name = n.raw_model_name LIMIT 1)
   WHERE b.billing_mode = 'free' AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
)
SELECT 'M16_join_caliber_check' k,
  'used='||(SELECT COUNT(*) FROM fr_used)::text
  ||' ref='||(SELECT COUNT(*) FROM fr_ref)::text
  ||' | legacy_背景='||(SELECT COUNT(*) FROM fr_legacy)::text
  ||' | '||CASE WHEN (SELECT COUNT(*) FROM fr_used) = (SELECT COUNT(*) FROM fr_ref)
               THEN '✔ 口径健康（used == ref ⇒ 两种独立写法一致）'
               ELSE '★★ **used != ref ⇒ 量具漂移 ⇒ 读数不可信 ⇒ 先修口径**' END v;

-- ═══ M17 ★★ 事件定义自检（★ 防重复行把间隔算成 0）═══
-- ★ M12/M12s 的事件 = 「一次失败 → 该对的下一次探测」，用 LEAD 取相邻行。
--   若 node_probe_runs 存在同一次尝试的重复行 ⇒ 间隔会被算成 0 ⇒ 人为拉快「快复检」那一侧。
-- ★ 实测（2026-11-10）：同一 (credential_id, raw_model_name, created_at) **无重复**
--   （48,626 组合全部 dup=1）⇒ 零间隔事件 = 0 ⇒ ★ 事件定义干净。
WITH fr AS (
   SELECT n.credential_id, n.raw_model_name, n.created_at, n.success, (n.raw_model_name LIKE '%/%') is_ns
   FROM node_probe_runs n
   WHERE n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
     AND EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id = b.provider_model_id
                 WHERE b.credential_id = n.credential_id AND b.billing_mode = 'free'
                   AND pm.raw_model_name = n.raw_model_name)
), seg AS (
   SELECT credential_id, raw_model_name, is_ns, created_at, success,
          LEAD(created_at) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
 ), rec AS (
   SELECT is_ns, EXTRACT(EPOCH FROM (next_at - created_at))/60 AS gap_min
   FROM seg WHERE success = FALSE AND next_at IS NOT NULL
 )
SELECT 'M17_event_definition_check' k,
  COUNT(*)::text||' events | zero_gap='||COUNT(*) FILTER (WHERE gap_min = 0)::text
  ||' | under_1min='||ROUND(100.0*COUNT(*) FILTER (WHERE gap_min < 1)/COUNT(*),1)::text||'%'
  ||' | '||CASE WHEN COUNT(*) FILTER (WHERE gap_min = 0) = 0 THEN '✔ 无重复行污染'
                ELSE '★★ **存在零间隔 ⇒ 可能有重复行 ⇒ 事件定义需复查**' END v
FROM rec;

-- ═══ H1–H5 ★★ handoff「意图口径」的修正版（★ 原样 SQL 不可执行 ⇒ 这里按它的**意图**重写）═══
--
-- ★★ 为什么有这一节：handoff 的 Phase 2 任务 2.1 要求「**重新执行任务 0.2 的 SQL**」，
--   而任务 0.2 的 4 条基线 SQL 在真库上 **4 条里只有 1 条能跑通**（实测证据：
--   scripts/audit/hand-off-verbatim.sql）：
--     ① `credentials.billing_mode` 不存在（psql HINT：cmb.billing_mode）
--     ② `credentials.provider` 不存在（psql HINT：credentials.provider_id）
--     ③ `node_probe_state.last_probe_at` 不存在（真列名是 `last_attempt_at`）
--     ④ `node_probe_runs.raw_model` 不存在（真列名是 `raw_model_name`）
--   ⇒ ★★ **handoff 的验收流程在当前 schema 上不可执行** ⇒ 本节按它的**指标意图**
--   （unavailable 数 / 免费可用率 / **探测间隔 P95** / **探测覆盖率** / featured 探测频率）
--   用真列名重写 ⇒ ★ 让 Stage 1 报告能**用 handoff 自己的语言**回答它的问题
--   （★ 但它的**成功标准**本身已被判据审计档判为不可用 ⇒ 这节是「参考读数」，不是判据）。
SELECT 'H1_unavailable_count' k,
  (SELECT COUNT(*)::text FROM credential_model_bindings WHERE available = FALSE) v;

SELECT 'H2_free_available_pct' k,
  COUNT(*)::text||' total, avail='||COUNT(*) FILTER (WHERE available)::text
  ||' | pct='||ROUND(100.0*COUNT(*) FILTER (WHERE available)/COUNT(*),1)::text v
FROM credential_model_bindings WHERE billing_mode = 'free';

-- H3：探测间隔分布（★ handoff 指标 3 的修正版：last_probe_at ⇒ last_attempt_at）
SELECT 'H3_probe_interval' k,
  COUNT(*)::text||' rows | p50_min='||COALESCE((percentile_cont(0.50) WITHIN GROUP (
      ORDER BY EXTRACT(EPOCH FROM (NOW() - last_attempt_at))/60))::int::text,'?')
  ||' | p95_min='||COALESCE((percentile_cont(0.95) WITHIN GROUP (
      ORDER BY EXTRACT(EPOCH FROM (NOW() - last_attempt_at))/60))::int::text,'?')
  ||' | p99_min='||COALESCE((percentile_cont(0.99) WITHIN GROUP (
      ORDER BY EXTRACT(EPOCH FROM (NOW() - last_attempt_at))/60))::int::text,'?') v
FROM node_probe_state WHERE last_attempt_at IS NOT NULL;

-- H4：探测覆盖率（★ handoff 指标 4 的修正版）
SELECT 'H4_probe_coverage_24h' k,
  COUNT(*)::text||' total | probed_24h='||COUNT(*) FILTER (WHERE last_attempt_at > NOW() - INTERVAL '24 hours')::text
  ||' | coverage_pct='||ROUND(100.0*COUNT(*) FILTER (WHERE last_attempt_at > NOW() - INTERVAL '24 hours')/NULLIF(COUNT(*),0),1)::text v
FROM node_probe_state;

-- H5：featured 模型的探测频率（★ handoff 任务 2.2-3 的修正版）
--   ★ handoff 用的是**幻觉模型名**（gpt-4o-mini / claude-haiku-3.5 / gemini-flash-2.0）
--   ⇒ 原样必然 0 行 ⇒ 这里改用**真实存在且在 featured 集合内**的 raw 名（Top 10）
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv_new AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m
          FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn
          JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m
          JOIN provider_models pm ON pm.canonical_id = mc.id
          WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>'')
SELECT 'H5_featured_probe_freq' k, raw_model_name||' | probes_24h='||c::text v
FROM (
  SELECT n.raw_model_name, COUNT(*) c
  FROM node_probe_runs n
  WHERE n.created_at > NOW() - INTERVAL '24 hours'
    AND lower(btrim(n.raw_model_name)) IN (SELECT m FROM rawv_new)
  GROUP BY 1 ORDER BY 2 DESC LIMIT 10
) t;

-- ═══ M20 ★★★★ **分层效应指标**：★「该快的更快、不该快的不变」═══
--
-- ★★ 为什么必须有它（★★ 本轮第 11 处口径修正，★★ 差点造成**结构性假阴性**）：
--   ★ M14/M14x 把新覆盖人群的失败**混在一起**取分位数 ⇒ ★★ 实测（211 真库，pre7d 全计费口径
--     新覆盖组 691 条失败事件）其中 **537 条 = 78% 是「豁免类」**：
--     · 协议形态（http 404/405/410/415、400/422 非配额体）
--       ⇒ bg/probe_root_cause.go:113 classifyProbeRootCause ⇒ ProbeRootCauseProtocol
--     · 配额钉住（quota_state ∈ {periodic_exhausted, balance_exhausted, permanently_exhausted}
--       且 quota_recover_at 在未来）⇒ bg/probe_recovery_policy.go:335 quotaResetAlignedBackoff
--   ★★ 而豁免类**在设计上就不该被减半**：
--     · 协议形态 ⇒ probeBackoffForDirectOutcome 把它钉在 modelNotServedRecheckInterval = 6h
--       （bg/node_probe.go:3908）
--     · 且 featured backoff 乘数**显式豁免**它们：bg/probe_service.go:582 的两个例外
--       （isModelNotServedProbeError / rootCause == ProbeRootCauseProtocol）
--   ⇒ ⇒ ★★ **post 的混合 p90 必然仍 ≈360、slow 占比必然仍 ≈25~32%**
--      ⇒ ★★ M14x 的「post p90 ≤180 / slow ≤ pre/2」**结构性不可达** ⇒ ★★ 假阴性
--      （★ 且会被读成「修复没生效」⇒ ★ 真因是**判据不可达**，不是机制没生效）
--   ★★ ⇒ 效应判据必须**按豁免/非豁免分层**：
--       · normal（★ 乘数应当生效的那一类）⇒ **效应判据**
--       · exempt（★ 设计上不变）           ⇒ **不回归守卫**
--
-- ★★ **机制级正控（★ 本轮实测，已进判据审计档）**：
--   · pre 期**同一时刻**、同一形态、同一路径：old_feat 的阶梯值全是**减半值**
--     （150/1800/10800/3600），newly 全是**未减半值**（300/3600/7200/21600）
--     ⇒ ★ 乘数是唯一解释（无 era 混淆、无调度混淆）
--   · post 期 newly 出现 **10800 / 1800**（= 21600 / 3600 的 50%）⇒ ★ 乘数确已作用
--   · 实测调度余量（观察间隔 − 阶梯值）：**中位 0 分钟、p90 1 分钟** ⇒ ★ 观察间隔≈阶梯值
--     ⇒ ★★ 阈值零余量的来源是「阶梯值本身就是 21600」，**不是**调度延迟
--
-- ★ 读数（每个键）：
--   n                事件数
--   med_gap/p90_gap  观察到的复检间隔（分钟）
--   slow_gt300_pct   >300min 的事件占比（★ 速率型，比分位数稳）
--   ladder_p50       阶梯值 next_retry_seconds 的中位（分钟）★ 机制原生读数（无调度噪声）
--   top21600_n       next_retry_seconds = 21600（**未减半**的 6h 顶格）事件数 ★★ 机制指纹
--   ladder_max_s     阶梯值最大值（秒）
WITH rp AS (SELECT DISTINCT lower(btrim(f)) m FROM routing_policy, unnest(featured_models) AS f WHERE tenant_id='default' AND btrim(f)<>''),
 cf AS (SELECT DISTINCT lower(btrim(canonical_name)) m FROM models_canonical WHERE featured=TRUE AND status='active'),
 rawv_old AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM models_canonical mc JOIN provider_models pm ON pm.canonical_id = mc.id WHERE mc.featured=TRUE AND mc.status='active' AND pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 rawv_new AS (SELECT DISTINCT lower(btrim(pm.raw_model_name)) m FROM (SELECT m FROM rp UNION SELECT m FROM cf) fn JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.m JOIN provider_models pm ON pm.canonical_id = mc.id WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name)<>''),
 st_old AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_old),
 st_new AS (SELECT m FROM rp UNION SELECT m FROM cf UNION SELECT m FROM rawv_new),
 newly AS (SELECT m FROM st_new WHERE m LIKE '%/%' AND m NOT IN (SELECT m FROM st_old)),
 fr AS (
   -- ★★ 与 M14x 的 'all' 口径**逐条对齐**（★ 分层必须穷尽 M14x newly ⇒ 可对拍）
   SELECT n.credential_id, n.raw_model_name, n.created_at, n.success, n.next_retry_seconds,
          n.direct_http_status, n.direct_err_code, n.response_body,
          c.quota_state, c.quota_recover_at
   FROM node_probe_runs n LEFT JOIN credentials c ON c.id = n.credential_id
   WHERE EXISTS (SELECT 1 FROM credential_model_bindings b JOIN provider_models pm ON pm.id=b.provider_model_id
                 WHERE b.credential_id=n.credential_id AND pm.raw_model_name=n.raw_model_name)
     AND n.created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
), seg AS (
   SELECT credential_id, raw_model_name, created_at, success, next_retry_seconds,
          -- ★★ 豁免判定（★ 与生产 classifyProbeRootCause + 两个乘数例外同款）
          -- ★★ **已知近似（★ 必须披露）**：★ 配额钉住一项用的是凭据行**当前**的
          --   quota_state / quota_recover_at ⇒ ★ 对**pre 期历史行**是近似
          --   （★ 当时是否钉住无法从现表还原）⇒ ★ 分层边界可能有个位数行的偏差
          --   ⇒ ★ 但它**不影响**判据的主判据（★ top21600_n = 未减半 6h 顶格 ⇒ 与分层无关）
          CASE WHEN direct_http_status IN (404,405,410,415)
                 OR (direct_http_status IN (400,422)
                     AND NOT (lower(coalesce(response_body,'')) ~ 'insufficient_quota|insufficient balance|quota exceeded|exceeded your current quota|billing|arrears|unpaid|balance is not enough|not enough balance|余额不足|欠费|额度不足|余额已不足|账户余额'))
                 OR direct_err_code IN ('http_404','probe_http_404')
                 -- ★★★ 2026-11-10 修正（★★ 第 12 处口径修正，★ 未来时钟模拟抓到的真缺陷）：
                 --   ★ 原来写 `quota_recover_at > now()` ⇒ ★★ **用当前时钟判历史事件**
                 --   ★★ ⇒ 在 Stage 2 的未来时钟（2026-11-17）下分层归属会整体改变
                 --   ★★ （实测 normal 154→459、exempt 537→232，★ 总数 691 不变 ⇒ 纯分层污染）
                 --   ★★ ⇒ ★★ M18 守卫会报「pre 基线漂移」⇒ ★★ **Stage 2 直接跑不了**
                 --   ★★ ⇒ 正确语义 = 「**这条失败发生时**配额是否处于用尽窗口」
                 --   ★★ ⇒ 锚定**事件时点**（fail_at）⇒ ★★ 时钟无关 ✔
                 OR (quota_state IN ('periodic_exhausted','balance_exhausted','permanently_exhausted')
                     AND quota_recover_at > created_at)
               THEN 'exempt' ELSE 'normal' END AS exemption,
          LEAD(created_at) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) AS next_at,
          CASE WHEN created_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' THEN 'pre' ELSE 'post' END AS era
   FROM fr
), rec AS (
   SELECT created_at AS fail_at, next_at, next_retry_seconds, exemption, era,
          EXTRACT(EPOCH FROM (next_at-created_at))/60 AS recheck_min
   FROM seg WHERE NOT success AND next_at IS NOT NULL
     AND raw_model_name LIKE '%/%' AND lower(btrim(raw_model_name)) IN (SELECT m FROM newly)
), exp AS (
   -- ★ 窗口四桶（★ 与 M14 同款 ⇒ pre7d = pre6d + pre24 ⇒ 可对拍）
   SELECT r.*, 'pre6d' win FROM rec r WHERE r.era='pre' AND r.fail_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '24 hours'
   UNION ALL
   SELECT r.*, 'pre24' FROM rec r WHERE r.era='pre' AND r.fail_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '24 hours'
   UNION ALL
   SELECT r.*, 'pre7d' FROM rec r WHERE r.era='pre'
   UNION ALL
   SELECT r.*, 'post' FROM rec r WHERE r.era='post'
)
SELECT 'M20_'||exemption||'_'||win k,
  COUNT(*)::text||' events | med_gap='||COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  ||' | p90_gap='||COALESCE((percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  ||' | slow_gt300_pct='||ROUND(100.0*COUNT(*) FILTER (WHERE recheck_min > 300)/COUNT(*),1)::text
  ||' | ladder_p50='||COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY next_retry_seconds/60.0))::int::text,'?')
  ||' | top21600_n='||COUNT(*) FILTER (WHERE next_retry_seconds = 21600)::text
  ||' | ladder_max_s='||COALESCE(max(next_retry_seconds)::text,'?') v
FROM exp
WHERE ( (era = 'pre'  AND fail_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '6 hours'
                     AND next_at < TIMESTAMPTZ '2026-11-10 13:42:30+08')
     OR (era = 'post' AND fail_at < now() - interval '6 hours') )
GROUP BY exemption, win
UNION ALL
-- ★★★ **era 纯度的存活者偏差自检**（★ 本轮新增，★ 见主档 §5.9.21.13.8）
--   ★★ pre 侧成熟度过滤要求 `next_at < 切点` ⇒ ★★ **凡是被这条规则丢掉的事件，
--   ★★ 其间隔必然跨越切点（≥6h）⇒ ★★ 它们**必然**是「慢事件」⇒ ⇒ ★★ 若丢弃量 > 0，
--   ★★ 则 pre 基线的「慢事件占比」被**系统性低估** ⇒ ★★ 阈值「post ≤ pre/2」**偏严**
--   ⇒ ⇒ ★★ 可能造成**假阴性**（★ 与本轮 §5.9.21.13 那条同族：★ 判据的偏斜必须可观测）
--   ★★ 实测（2026-11-10 211 真库，pre24 窗口）：**丢弃 0 条** ⇒ ★ 无存活者偏差
--   ★★ ⇒ ★ 这两个键是**读数**（★ 不进 M18 冻结对拍：★ 值变化是「数据形态变化」，
--   ★★ 不是「口径漂移」⇒ ★ 不该让整表作废），★ 但 >0 时判定器必须显式提示。
--   ★★ 用 VALUES + LEFT JOIN（★ GROUP BY 空集**不产生行** ⇒ 必须显式造出 0 值行）
SELECT 'M20_era_dropped_'||w.win k, COUNT(e.*)::text||' events dropped_by_era_purity' v
FROM (VALUES ('pre24'),('pre7d')) AS w(win)
LEFT JOIN exp e
       ON e.era = 'pre' AND e.win = w.win
      AND e.next_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08'
      AND e.fail_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '6 hours'
GROUP BY w.win ORDER BY 1;
