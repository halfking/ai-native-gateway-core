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
FROM node_probe_runs
WHERE created_at > now() - interval '24 hours'
-- ★★★★ 2026-11-11 第 25 处口径修正（★ 穷举审计抓到的**同类第二处**）：★ **post 段窗口的 `now()` 上界**
--   ★★ 形态：★ M5 的 24h 窗口**只有下界**（★ `created_at > now() - 24h`）⇒ ★★ 窗口 = **[now-24h, ∞)**
--     ⇒ ★★ **as-of 替换只改得动下界** ⇒ ★★ 负控里（★ as-of 在过去、真实 now 在现在）窗口变成
--     ★★ **[asof-24h, 真实now]** = **~35h** ⇒ ★★ ⇒ **M5 的负控读数会测错窗口**（★ 与 24h 基线不同量纲 ⇒ 假红）
--   ★★ 为什么必须修：★ M5 是「**爆炸半径**」判据（★ base = pre24 单日快照、limit = base×1.01）
--     ⇒ ★★ 一旦它进负控的「必须判负」清单 ⇒ ★★ 负控拿 ~35h 的量去比 24h 的基线 ⇒ ★★ **必然假红**
--   ★★★ **修法**：★ 加 `created_at <= now()`（★ now 时刻 = 「观测到的全部」⇒ ★ **真实读数逐字节不变**
--     ⇒ ★★ pre 侧不受影响（★ M5 无 pre 侧读数键）⇒ ★★ **M18 冻结面不受影响 ⇒ 不重冻**
--   ★★ ⇒ ★★ 负控/预演里 M5 的窗口 == 恰好 stage 窗口（★ 24h / 168h）⇒ ★★ **与 M14 族同款**
AND created_at <= now();

-- ═══ M5b 探测量分解（★ 2026-11-11 关窗预演补：★ M5 超预算时的**归因证据**）═══
--
-- ★★ 为什么需要它：★ M5 是总量键 ⇒ 超 9334 预算判 ❌ 时**无法分辨**增量来自
--   ★ 「设计内的恢复探测」还是「调度失控」⇒ 关窗预演实测（pre24 vs post 前 ~11h，
--   2026-11-11 00:4x 手算，本键把它固化）：
--   · credential_recovery 240.3 → 237.3/h（**持平**）
--   · periodic            135.7 → 140.7/h（**+3.7%**：era2 的 106 个新覆盖变体
--     入 periodic 调度 ⇒ 设计内）
--   · sync_request          9.3 →  21.4/h（**+131%**：恢复链 F-B re-arm /
--     F-A 回退的再探测 ⇒ 设计内 ⇒ 正是本轮修复的**目的行为**）
--   ⇒ ★★ 预判：关窗读数 ≈9600（仍超 9334）⇒ ❌ 的**增量可完全归因**于设计内行为。
-- ★★ 口径：与 M5 同窗（now()-24h 滚动）⇒ 关窗时刻（now = 切点+24h）读数
--   恰好 = 纯 post 段 ⇒ 与 M5 主键同窗同时刻，归因可直接引用。
-- ★★ 必须 VALUES + LEFT JOIN（★ §667 同款坑：GROUP BY 空集**不产生行** ⇒ 键缺失
--   ⇒ ★ 恰好在最需要它的时候没有它）；★ 枚举 = node_probe_runs_trigger_kind_check
--   闭集（9 值，db/db.go:6212）+ 'other' 兜底（★ 未来新增 kind 不在枚举里 ⇒
--   进 other ⇒ **不会被静默吞掉**）。
SELECT 'M5_probe_runs_24h_by_kind' k,
  string_agg(w.kind||'='||COALESCE(c.n,0)::text, ' | ' ORDER BY COALESCE(c.n,0) DESC, w.kind) v
FROM (VALUES ('credential_recovery'),('periodic'),('sync_request'),('request_failure'),
             ('manual'),('admin'),('integrity_probe_planner'),('selfcheck'),
             ('external_async'),('other')) w(kind)
LEFT JOIN (
  SELECT CASE WHEN trigger_kind IN ('credential_recovery','periodic','sync_request','request_failure',
                                    'manual','admin','integrity_probe_planner','selfcheck','external_async')
              THEN trigger_kind ELSE 'other' END AS kind,
         COUNT(*) AS n
  FROM node_probe_runs
  WHERE created_at > now() - interval '24 hours'
    -- ★★ 2026-11-11 第 25 处：★ **与 M5_probe_runs_24h 同窗口** ⇒ ★★ 必须**一起**有上界
    --   ★★ 否则 ⇒ ★★ M5 的裁决窗口与它的「增量归因（by_kind）」分解**窗口不同**
    --   ★★ ⇒ ★★ 归因读数不可复核（★ 同一行里的两个数字来自不同长度的窗口）
    AND created_at <= now()
  GROUP BY 1
) c ON c.kind = w.kind;

-- ═══ ★★★ 第 22 处口径修正（2026-11-11 00:2x）：★ M5 的「逐日探测量分布」═══
--
-- ★★ **为什么需要它（★ 实测抓到的真缺陷）**：★ M5 的 base = **pre24 的单日快照**（9242）
-- ★   ⇒ ★ 而探测量**按天剧烈波动**（★ 实测 pre7d 逐日：11-03 **1292** / 11-04 2764 / 11-05 7794
-- ★   / 11-06 **9905** / 11-07 8973 / 11-08 **2500** / 11-09 **9683** ⇒ **8× 差**）
-- ★   ⇒ ★★ **pre 期实测 11-09 = 9683 > limit 9334** ⇒ ★★ ⇒ **该阈值在 pre 期就不成立**
-- ★   ⇒ ★★ ⇒ M5 判 ❌ 时**不能**被读成「修复引起爆炸半径」⇒ ★★ 必须把「基线口径问题」摆到行内
-- ★ ★★ **只出读数键、不改 base/limit**（★ 关窗前**不重冻**已验证基线）⇒ ★★ 判定器行内披露
-- ★★ **pre7d 桶锚定切点 ⇒ 不含 now() ⇒ 时钟无关**；★ 键名不匹配 `PRE_KEY_RE` ⇒ **不进冻结对拍**
--   ⇒ ★★ 既不动既有 pre 键的取值，也不触发重冻 ⇒ ★★ 合规且零漂移风险
WITH d AS (
  SELECT to_char(date_trunc('day', created_at),'MM-DD') dy, COUNT(*) c
  FROM node_probe_runs
  WHERE created_at >  TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '7 days'
    AND created_at <= TIMESTAMPTZ '2026-11-10 13:42:30+08'
  GROUP BY date_trunc('day', created_at)
)
SELECT 'M5_pre7d_runs_by_day' k,
  'days='||COALESCE(string_agg(dy||':'||c::text, ',' ORDER BY dy),'-')
  ||' | n_days='||COUNT(*)::text
  ||' | min='||COALESCE(min(c)::text,'0')
  ||' | max='||COALESCE(max(c)::text,'0')
  ||' | total='||COALESCE(sum(c)::text,'0')
  v FROM d;

WITH d AS (
  SELECT to_char(date_trunc('day', created_at),'MM-DD') dy, COUNT(*) c
  FROM node_probe_runs
  WHERE created_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08'
    -- ★★ 2026-11-11 第 25 处：★ post 段**必须被 now() 界定**（★ 本键是 M5 判 ❌ 时的「post 逐日」披露）
    --   ★★ 否则 ⇒ ★★ 窗口 = [切点, ∞) ⇒ ★★ as-of 替换**无效** ⇒ ★★ 负控/预演里它是 [切点, 真实now]
    --   ★★ ⇒ ★★ ⇒ 披露用的 post 逐日**与 M5 的裁决窗口不同量纲** ⇒ ★★ 披露不可复核
    --   ★★ 修法：★ 加 `created_at <= now()` ⇒ ★★ 真实读数逐字节不变（★ now = 观测到的全部）
    AND created_at <= now()
  GROUP BY date_trunc('day', created_at)
)
SELECT 'M5_post_runs_by_day' k,
  'days='||COALESCE(string_agg(dy||':'||c::text, ',' ORDER BY dy),'-')
  ||' | n_days='||COUNT(*)::text
  ||' | min='||COALESCE(min(c)::text,'0')
  ||' | max='||COALESCE(max(c)::text,'0')
  ||' | total='||COALESCE(sum(c)::text,'0')
  v FROM d;

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
     -- ★★★★ 2026-11-11 第 24 处口径修正：★ **post 段的 `now()` 上界**（★ M11 两块原本**完全没有** now()）
     --   ★★ 形态：★ M14/M14x/M15x/M20 族的 post 段都有 `fail_at < now()-6h` ⇒ ★ **post 段被 now() 界定**
     --     ⇒ ★★ as-of 替换（★ 负控/预演）能把它界定成「恰好 stage 窗口」⇒ ★★ 第 23 处的几何对齐对它们生效
     --   ★★ 而 **M11 的两个 fr CTE 没有任何 now()** ⇒ ★★ post 段 = **[切点, ∞)** ⇒ ★★ **无界**
     --     ⇒ ★★ ⇒ as-of 替换**对它无效** ⇒ ★★ 负控/预演里的 M11 窗口**仍不是 stage 窗口**
     --   ★★ 实测（本轮，负控 era + as-of = 切点+24h）：★ 无上界 ⇒ M11b_post_fix_namespaced = **13 ep / med 3**
     --     ⇒ ★★ 有上界 ⇒ **9 ep / med 30** ⇒ ★★ **med 差 10×** ⇒ ★★ **M11 的裁决符号会翻转**
     --     ⇒ ★★ 更要命：★ **无上界时负控里 M11 判 ✅** ⇒ ★★ **M11 在负控里是「恒真」的 ⇒ 它没有牙**
     --     ⇒ ★★ ⇒ Stage 1 的 M11 读数**无法区分「修复生效」与「修复没生效」**
     --   ★★★ **修法**：★ 加 `created_at <= now()`（★ 现在时刻 = 「观测到的全部」⇒ ★ **真实读数逐字节不变**
     --     ⇒ ★★ 但 as-of 读数**被正确界定** ⇒ ★★ 负控/预演里的 M11 窗口 == stage 窗口）
     --   ★ ★★ **pre 侧不受影响**（★ pre = [切点-7d, 切点) ⇒ ★ 全部 < now()）⇒ ★★ **基线不漂 ⇒ M18 不受影响**
     AND n.created_at <= now()
     -- ★★ era-aware（★ 本轮第 7 处口径修正）：★ 只对 **post** 侧排除「进行中的段」
     --   ⇒ pre 侧全留 ⇒ ★ pre 的「下一次探测」不会被 now 相对过滤吃掉
     --   （否则 pre 样本随查询时刻漂移：实测 M11b_pre 49 ↔ 50）
     -- ★★★ 2026-11-10 第 17 处口径修正（★ 本轮实测抓到的**尾部截断偏差**）：
     --   ★★ 原过滤：`(created_at < 切点 OR created_at < now()-2h)`
     --   ★★ ⇒ 它作用在 **fr**（LEAD 窗口函数的输入）⇒ ★★ **post 段的「成功行」也被滤掉**
     --   ★★ ⇒ 恢复发生在**最近 2h 内**的 episode ⇒ 它的成功行没了 ⇒ `next_success` 不再是 TRUE
     --   ★★ ⇒ ⇒ 该 episode 被**整体漏掉** ⇒ ⇒ ★★ **post 的 max / p90 / p99 被系统性拉低**
     --   ★★ ⇒ ⇒ ★★★ **判据「post ≤ pre」的偏差方向恰好是「偏向判绿」⇒ 假绿**
     --   ★★ ⇒ ★★ 而它宣称的用途（「排除进行中的段」）**本来就已经由 `next_success = TRUE` 满足**
     --   ★★   （★ 一个失败段若还没恢复 ⇒ 它的下一次运行不是 success ⇒ 该 episode 本就不会被计入）
     --   ★★ ⇒ ⇒ **本过滤纯属副作用 ⇒ 直接删除**（★ pre 侧逐字节不变：★ 实测 37/50 episodes 全部一致）
     --   ★★ ⇒ ★★ 删除后：★ post 段 = 「恢复已被观测到的 episode」⇒ ★★ 尾部不再被截断
     --   ★★ ⇒ ★★ **post 样本随观测时长增长**（★ 这才是诚实的读数）⇒ ★★ M11 的 n≥5 门会自然放行更多样本   -- ★ 排除进行中的段
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
     -- ★★★★ 2026-11-11 第 24 处口径修正：★ **post 段的 `now()` 上界**（★ M11 两块原本**完全没有** now()）
     --   ★★ 形态：★ M14/M14x/M15x/M20 族的 post 段都有 `fail_at < now()-6h` ⇒ ★ **post 段被 now() 界定**
     --     ⇒ ★★ as-of 替换（★ 负控/预演）能把它界定成「恰好 stage 窗口」⇒ ★★ 第 23 处的几何对齐对它们生效
     --   ★★ 而 **M11 的两个 fr CTE 没有任何 now()** ⇒ ★★ post 段 = **[切点, ∞)** ⇒ ★★ **无界**
     --     ⇒ ★★ ⇒ as-of 替换**对它无效** ⇒ ★★ 负控/预演里的 M11 窗口**仍不是 stage 窗口**
     --   ★★ 实测（本轮，负控 era + as-of = 切点+24h）：★ 无上界 ⇒ M11b_post_fix_namespaced = **13 ep / med 3**
     --     ⇒ ★★ 有上界 ⇒ **9 ep / med 30** ⇒ ★★ **med 差 10×** ⇒ ★★ **M11 的裁决符号会翻转**
     --     ⇒ ★★ 更要命：★ **无上界时负控里 M11 判 ✅** ⇒ ★★ **M11 在负控里是「恒真」的 ⇒ 它没有牙**
     --     ⇒ ★★ ⇒ Stage 1 的 M11 读数**无法区分「修复生效」与「修复没生效」**
     --   ★★★ **修法**：★ 加 `created_at <= now()`（★ 现在时刻 = 「观测到的全部」⇒ ★ **真实读数逐字节不变**
     --     ⇒ ★★ 但 as-of 读数**被正确界定** ⇒ ★★ 负控/预演里的 M11 窗口 == stage 窗口）
     --   ★ ★★ **pre 侧不受影响**（★ pre = [切点-7d, 切点) ⇒ ★ 全部 < now()）⇒ ★★ **基线不漂 ⇒ M18 不受影响**
     AND n.created_at <= now()
     -- ★★ era-aware（★ 本轮第 7 处口径修正）：★ 只对 **post** 侧排除「进行中的段」
     --   ⇒ pre 侧全留 ⇒ ★ pre 的「下一次探测」不会被 now 相对过滤吃掉
     --   （否则 pre 样本随查询时刻漂移：实测 M11b_pre 49 ↔ 50）
     -- ★★★ 2026-11-10 第 17 处口径修正（★ 本轮实测抓到的**尾部截断偏差**）：
     --   ★★ 原过滤：`(created_at < 切点 OR created_at < now()-2h)`
     --   ★★ ⇒ 它作用在 **fr**（LEAD 窗口函数的输入）⇒ ★★ **post 段的「成功行」也被滤掉**
     --   ★★ ⇒ 恢复发生在**最近 2h 内**的 episode ⇒ 它的成功行没了 ⇒ `next_success` 不再是 TRUE
     --   ★★ ⇒ ⇒ 该 episode 被**整体漏掉** ⇒ ⇒ ★★ **post 的 max / p90 / p99 被系统性拉低**
     --   ★★ ⇒ ⇒ ★★★ **判据「post ≤ pre」的偏差方向恰好是「偏向判绿」⇒ 假绿**
     --   ★★ ⇒ ★★ 而它宣称的用途（「排除进行中的段」）**本来就已经由 `next_success = TRUE` 满足**
     --   ★★   （★ 一个失败段若还没恢复 ⇒ 它的下一次运行不是 success ⇒ 该 episode 本就不会被计入）
     --   ★★ ⇒ ⇒ **本过滤纯属副作用 ⇒ 直接删除**（★ pre 侧逐字节不变：★ 实测 37/50 episodes 全部一致）
     --   ★★ ⇒ ★★ 删除后：★ post 段 = 「恢复已被观测到的 episode」⇒ ★★ 尾部不再被截断
     --   ★★ ⇒ ★★ **post 样本随观测时长增长**（★ 这才是诚实的读数）⇒ ★★ M11 的 n≥5 门会自然放行更多样本
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
     -- ★★ 2026-11-11 第 25 处口径修正（★ 穷举审计）：★ **post 段必须被 now() 界定**
     --   ★★ 形态：★ 本块的 era 判定只有**下界**（★ 切点字面量）⇒ ★★ post 段 = **[切点, ∞)**
     --   ★★ ⇒ ★★ as-of 替换**只改得动下界** ⇒ ★★ 负控/预演里窗口 = [切点, 真实now] ⇒ ★★ **不是 stage 窗口**
     --   ★★★ 修法：★ 加 `created_at <= now()`（★ now = 「观测到的全部」⇒ ★ **真实读数逐字节不变**
     --   ★★ ⇒ ★★ pre 侧不受影响（★ pre 全在切点前 ⇒ 全 < now()）⇒ ★★ **基线不漂 ⇒ M18 不受影响**
     AND n.created_at <= now()
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
     -- ★★ 2026-11-11 第 25 处口径修正（★ 穷举审计）：★ **post 段必须被 now() 界定**
     --   ★★ 形态：★ 本块的 era 判定只有**下界**（★ 切点字面量）⇒ ★★ post 段 = **[切点, ∞)**
     --   ★★ ⇒ ★★ as-of 替换**只改得动下界** ⇒ ★★ 负控/预演里窗口 = [切点, 真实now] ⇒ ★★ **不是 stage 窗口**
     --   ★★★ 修法：★ 加 `created_at <= now()`（★ now = 「观测到的全部」⇒ ★ **真实读数逐字节不变**
     --   ★★ ⇒ ★★ pre 侧不受影响（★ pre 全在切点前 ⇒ 全 < now()）⇒ ★★ **基线不漂 ⇒ M18 不受影响**
     AND n.created_at <= now()
     -- ★★ era-aware（★ 本轮第 7 处口径修正）：★ 只对 **post** 侧排除「进行中的段」
     --   ⇒ pre 侧全留 ⇒ ★ pre 的「下一次探测」不会被 now 相对过滤吃掉
     --   （否则 pre 样本随查询时刻漂移：实测 M11b_pre 49 ↔ 50）
     -- ★★★ 2026-11-10 第 17 处口径修正（★ 本轮实测抓到的**尾部截断偏差**）：
     --   ★★ 原过滤：`(created_at < 切点 OR created_at < now()-2h)`
     --   ★★ ⇒ 它作用在 **fr**（LEAD 窗口函数的输入）⇒ ★★ **post 段的「成功行」也被滤掉**
     --   ★★ ⇒ 恢复发生在**最近 2h 内**的 episode ⇒ 它的成功行没了 ⇒ `next_success` 不再是 TRUE
     --   ★★ ⇒ ⇒ 该 episode 被**整体漏掉** ⇒ ⇒ ★★ **post 的 max / p90 / p99 被系统性拉低**
     --   ★★ ⇒ ⇒ ★★★ **判据「post ≤ pre」的偏差方向恰好是「偏向判绿」⇒ 假绿**
     --   ★★ ⇒ ★★ 而它宣称的用途（「排除进行中的段」）**本来就已经由 `next_success = TRUE` 满足**
     --   ★★   （★ 一个失败段若还没恢复 ⇒ 它的下一次运行不是 success ⇒ 该 episode 本就不会被计入）
     --   ★★ ⇒ ⇒ **本过滤纯属副作用 ⇒ 直接删除**（★ pre 侧逐字节不变：★ 实测 37/50 episodes 全部一致）
     --   ★★ ⇒ ★★ 删除后：★ post 段 = 「恢复已被观测到的 episode」⇒ ★★ 尾部不再被截断
     --   ★★ ⇒ ★★ **post 样本随观测时长增长**（★ 这才是诚实的读数）⇒ ★★ M11 的 n≥5 门会自然放行更多样本
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
     -- ★★ 2026-11-11 第 25 处口径修正（★ 穷举审计）：★ **post 段必须被 now() 界定**
     --   ★★ 形态：★ 本块的 era 判定只有**下界**（★ 切点字面量）⇒ ★★ post 段 = **[切点, ∞)**
     --   ★★ ⇒ ★★ as-of 替换**只改得动下界** ⇒ ★★ 负控/预演里窗口 = [切点, 真实now] ⇒ ★★ **不是 stage 窗口**
     --   ★★★ 修法：★ 加 `created_at <= now()`（★ now = 「观测到的全部」⇒ ★ **真实读数逐字节不变**
     --   ★★ ⇒ ★★ pre 侧不受影响（★ pre 全在切点前 ⇒ 全 < now()）⇒ ★★ **基线不漂 ⇒ M18 不受影响**
     AND n.created_at <= now()
     -- ★★ era-aware（★ 本轮第 7 处口径修正）：★ 只对 **post** 侧排除「进行中的段」
     --   ⇒ pre 侧全留 ⇒ ★ pre 的「下一次探测」不会被 now 相对过滤吃掉
     --   （否则 pre 样本随查询时刻漂移：实测 M11b_pre 49 ↔ 50）
     -- ★★★ 2026-11-10 第 17 处口径修正（★ 本轮实测抓到的**尾部截断偏差**）：
     --   ★★ 原过滤：`(created_at < 切点 OR created_at < now()-2h)`
     --   ★★ ⇒ 它作用在 **fr**（LEAD 窗口函数的输入）⇒ ★★ **post 段的「成功行」也被滤掉**
     --   ★★ ⇒ 恢复发生在**最近 2h 内**的 episode ⇒ 它的成功行没了 ⇒ `next_success` 不再是 TRUE
     --   ★★ ⇒ ⇒ 该 episode 被**整体漏掉** ⇒ ⇒ ★★ **post 的 max / p90 / p99 被系统性拉低**
     --   ★★ ⇒ ⇒ ★★★ **判据「post ≤ pre」的偏差方向恰好是「偏向判绿」⇒ 假绿**
     --   ★★ ⇒ ★★ 而它宣称的用途（「排除进行中的段」）**本来就已经由 `next_success = TRUE` 满足**
     --   ★★   （★ 一个失败段若还没恢复 ⇒ 它的下一次运行不是 success ⇒ 该 episode 本就不会被计入）
     --   ★★ ⇒ ⇒ **本过滤纯属副作用 ⇒ 直接删除**（★ pre 侧逐字节不变：★ 实测 37/50 episodes 全部一致）
     --   ★★ ⇒ ★★ 删除后：★ post 段 = 「恢复已被观测到的 episode」⇒ ★★ 尾部不再被截断
     --   ★★ ⇒ ★★ **post 样本随观测时长增长**（★ 这才是诚实的读数）⇒ ★★ M11 的 n≥5 门会自然放行更多样本
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
   SELECT n.credential_id, n.raw_model_name, n.created_at, n.success, n.next_retry_seconds,
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
   SELECT credential_id, raw_model_name, is_ns, created_at, success, has_free, next_retry_seconds,
          LEAD(created_at) OVER (PARTITION BY credential_id, raw_model_name ORDER BY created_at) next_at
   FROM fr
 ), rec AS (
   SELECT is_ns, has_free, raw_model_name, created_at AS fail_at, next_at, next_retry_seconds,
          EXTRACT(EPOCH FROM (next_at - created_at))/60 AS recheck_min,
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
  ||' | slow_gt300_pct='||ROUND(100.0*COUNT(*) FILTER (WHERE recheck_min > 300)/COUNT(*),1)::text
  -- ★★★★ 第 26 处口径修正（★ 2026-11-11 02:0x）：★ 「**::int 取整边界**」伪影 ⇒ post 行追加**亚分钟精度** raw
  --   ★★ **形态（★ 真库实测）**：★ `recheck_min` 是**浮点分钟**（秒级精度）⇒ `::int` 把它截到分钟
  --   ★★ 而判据把**取整后的整数**与「**另一个取整样本**」做**精确比较**（★ 如 `p90 ≤ pre_p90`）
  --   ★★ ⇒ ★★ 真实差只要落在 **[−0.5, +0.5) 分钟** 的取整跨界带里 ⇒ 裁决符号就会翻转
  --   ★★ ⇒ ★★ **实测（M20 豁免组，211 真库）**：★ post raw p90 = **360.5069** min ⇒ `::int` = **361**
  --   ★★   而基线取整值 = **360** ⇒ 基线真值 ∈ [359.5, 360.5) ⇒ ⇒ **真实差 ∈ (0.01, 1.01] 分钟 ⇒ 上界 ≈ 60 秒（★ = 判据的整个分辨率）**
  --   ★★ ⇒ ★★ 「判 ❌」与「差 ≤60 秒」⇒ ★★ **判据分辨率（1 分钟）比被测抖动（秒级）粗 ~60 倍**
  --   ★★ ⇒ ★★ ⇒ **判 ❌ 不可读成「行为回归」**（★ 与第 23 处「M14 p99 361 vs 360」同族、同一形态）
  --   ★★ ★★ **只给 post 行追加**（★ `CASE WHEN win='post'`）⇒ ★★ **pre 键逐字节不变**
  --   ★★   ⇒ ★★ **M18 冻结面不变**（★ **不重冻**）⇒ ★★ 时钟门的 **pre 键逐字节对拍**也不变 ✔
  --   ★★   ⇒ ★★ 字段名后缀固定用 `_raw`（★ 判定器的 `field()` 正则是 `\b名(?:_min)?=` ⇒ `_raw` 后面不是 `=`
  --   ★★     ⇒ ★★ **不会误匹配既有字段**（★ 实测：`\bp90_min=` 不匹配 `p90_min_raw=`））
  || CASE WHEN win='post' THEN
        ' | med_min_raw='||COALESCE(round((percentile_disc(0.5) WITHIN GROUP (ORDER BY recheck_min))::numeric,4)::text,'?')
     || ' | p90_min_raw='||COALESCE(round((percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min))::numeric,4)::text,'?')
     || ' | p99_min_raw='||COALESCE(round((percentile_disc(0.99) WITHIN GROUP (ORDER BY recheck_min))::numeric,4)::text,'?')
     || ' | max_min_raw='||COALESCE(round((percentile_disc(1.0) WITHIN GROUP (ORDER BY recheck_min))::numeric,4)::text,'?')
     || ' | gt360p5_n='||(COUNT(*) FILTER (WHERE recheck_min > 360.5))::text
     ELSE '' END v
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
GROUP BY scope, win, grp_new
UNION ALL

-- ═══ ★★★ 第 21 处口径修正（2026-11-10 19:3x）：★ 「对照组」的**相位/构成**披露键 ═══
--
-- ★★ **为什么需要它（★ 关窗预演抓到的真缺陷）**：
-- ★ 关窗预演（as-of = 切点+24h）里「M14x 对照组」判 **❌ 未达成**（★ post med=60 vs pre med=360）
-- ★ ⇒ ★★ 追查后确认：★ 这是**纯相位效应**，不是行为变化 ⇒ ★★ 机理：
-- ★   · 对照组里 **~80 只**协议形态模型（★ http 404/410 ⇒ 设计上钉 **6h = 21600s** 阶梯）
-- ★     以 **6h 节奏**被探（实测 11-09/11-10：13:30 → 19:30 → 01:30 → 07:30 → 13:30）
-- ★   · era 切点 = 11-10 **13:42:30** ⇒ ★★ 切点**落在两次 6h 批次之间**
-- ★     ⇒ ★★ post 窗口的**前 5.6h 里一个 6h 批次都没有**（★ 下一次是 19:30）
-- ★   · ⇒ ★★ 对照组的 post 段只剩 **11 只**（timeout/429/500 形态 ⇒ 阶梯 3600）⇒ med=60
-- ★   · ⇒ ★★ 而 pre24 含 **4 个** 6h 批次（≈960 条 360min）⇒ med=360
-- ★ ⇒ ★★ **两窗口的「失败形态构成」不同构** ⇒ ★★ 中位数不可直接比 ⇒ ★★ 是**相位假红**
-- ★ ⇒ ★★ 已在 11-10 **19:28–19:30** 实测证实：★ 那 80 只**回来了**（★ 阶梯仍是 21600）⇒ ★★ 相位解释成立
--
-- ★★ **本键的职责（★ 只披露，不改裁决）**：
-- ★   ① 让判定器在对照组行内**说清 post 段的形态构成**（★ 6h 顶格事件数 / 慢事件占比 / 阶梯谱 / 模型数）
-- ★   ② 让读者能判断「post med 变了」是**行为变化**还是**相位/窗口覆盖**
-- ★   ③ ★★ **pre 是 6h 单一形态（med ≥ 300）且 post 的 6h 顶格事件占比 < 20%** ⇒ ★★ 两窗口**不同构** ⇒ ★★ 该 ❌ **不可归因**
-- ★
-- ★★ **为什么只出 post 侧、不出 pre 侧**：★ pre 键进 **M18 冻结对拍**（44 个）
-- ★   ⇒ ★★ 出新的 pre 键就要**重冻基线** ⇒ ★★ **关窗前重冻风险太高**（★ 本轮纪律）
-- ★   ⇒ ★★ ⇒ **只加 post 侧键**；★ pre 侧的形态由**既有冻结键**提供
-- ★   （★ `M14x_pre24_other` = med=360 / p90=360 / max=360 / slow_gt300_pct=74.0 ⇒ 已足够）
-- WARNING: keep this SELECT inside the SAME WITH clause as the main query above.
--   SQL "WITH" only applies to the single statement that follows it; a separate
--   statement cannot see the CTEs (exp, ...) -> psql: relation "exp" does not exist.
--   And WITHOUT -v ON_ERROR_STOP=1 psql swallows that error and still exits 0
--   -> silently fewer keys. So it must be UNION ALL'd into the same WITH.
SELECT CASE WHEN w.scope = 'all' THEN 'M14x_' ELSE 'M14_' END || 'post_other_composition' k,
  'n='||COUNT(e.*)::text
  ||' | models_n='||COUNT(DISTINCT e.raw_model_name)::text
  -- ★★ 「6h 顶格事件数」= ★ 对照组的主导形态（★ 协议形态 404/410 ⇒ 钉 6h）
  ||' | top21600_n='||(COUNT(e.*) FILTER (WHERE e.next_retry_seconds >= 21600))::text
  ||' | slow_gt300_pct='||CASE WHEN COUNT(e.*) = 0 THEN '0.0'
       ELSE ROUND(100.0*COUNT(e.*) FILTER (WHERE e.recheck_min > 300)/COUNT(e.*),1)::text END
  ||' | ladder_spectrum='||COALESCE(string_agg(DISTINCT e.next_retry_seconds::text, ',' ORDER BY e.next_retry_seconds::text),'-')
  v
-- ★★ **必须 VALUES + LEFT JOIN**（★ 本轮第 21 处口径修正的第二个坑）：
-- ★   `GROUP BY scope` 在 post 对照组**空集**时**不产生行** ⇒ ★ 键会缺失
-- ★   ⇒ ★★ 判定器只能显示「相位/构成不可见」⇒ ★★ **恰好在最需要它的时候没有它** ⇒ ★★ 静默失效
-- ★   ⇒ ★★ 用 VALUES 枚举两个计费口径 + LEFT JOIN ⇒ ★ **恒出行**（★ 计数为 0 也出行）
FROM (VALUES ('free'),('all')) w(scope)
LEFT JOIN exp e ON e.scope = w.scope AND e.win = 'post' AND e.grp_new = 'other'
     AND e.next_at IS NOT NULL
     AND e.era = 'post' AND e.fail_at < now() - interval '6 hours'
GROUP BY w.scope
ORDER BY 1;

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
  ||' | normal_ladder_max_s='||COALESCE((max(next_retry_seconds) FILTER (WHERE exemption='normal'))::text,'?')
  -- ★★★★ 第 26 处口径修正（★ 2026-11-11 02:0x）：★ 「**::int 取整边界**」伪影 ⇒ post 行追加**亚分钟精度** raw
  --   ★★ **为什么 M15x 也要**：★ normal 层的判据是 `post normal med ≤ 30`（★ 减半后的 30 分钟）
  --   ★★   而实测 post normal med = **30**（★ **正好压在阈值上**）⇒ ★★ 真实值只要 > 30.5 ⇒ `::int` = 31 ⇒ **判 ❌**
  --   ★★   ⇒ ★★ 这是**硬币另一面**（★ 与 M20 exempt 同源）：★ 阈值 30 是**硬数** ⇒ 真实差 = raw − 30（★ 精确可算）
  --   ★★ ★★ **只给 post 行追加** ⇒ ★★ pre 键逐字节不变 ⇒ **M18 不重冻** ✔
  || CASE WHEN win='post' THEN
        ' | normal_med_min_raw='||COALESCE(round((percentile_disc(0.5) WITHIN GROUP (ORDER BY recheck_min) FILTER (WHERE exemption='normal'))::numeric,4)::text,'?')
     || ' | normal_p90_min_raw='||COALESCE(round((percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min) FILTER (WHERE exemption='normal'))::numeric,4)::text,'?')
     ELSE '' END v
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
  COUNT(*)::text||' total | probed_24h='||COUNT(*) FILTER (WHERE last_attempt_at > NOW() - INTERVAL '24 hours' AND last_attempt_at <= now())::text
  ||' | coverage_pct='||ROUND(100.0*COUNT(*) FILTER (WHERE last_attempt_at > NOW() - INTERVAL '24 hours' AND last_attempt_at <= now())/NULLIF(COUNT(*),0),1)::text v
-- ★★ 2026-11-11 第 25 处口径修正（★ 穷举审计）：★ 「最近 24h 被探过」这个窗口**原来只有下界**
--   ★★ ⇒ ★★ 窗口 = [now-24h, ∞) ⇒ ★★ as-of 替换**只改得动下界** ⇒ ★★ 负控里窗口 = ~35h ⇒ ★★ 与 24h 口径不同量纲
--   ★★ 修法：★ 加 `last_attempt_at <= now()`（★ 快照表 ⇒ 该列不可能是未来 ⇒ ★ **真实读数逐字节不变**）
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
    -- ★★ 2026-11-11 第 25 处（★ 穷举审计）：★ 「最近 24h 探了几次」**原来只有下界**
    --   ★★ ⇒ ★★ as-of 替换只改得动下界 ⇒ ★★ 负控里窗口 = ~35h ⇒ ★★ 与 24h 口径不同量纲
    --   ★★ 修法：★ 加 `created_at <= now()` ⇒ ★★ 真实读数逐字节不变（★ now = 观测到的全部）
    AND n.created_at <= now()
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
 -- ★★★★ 「乘数已生效」的**证据集**（★ 本轮第 16 处口径修正，★★ 根因翻案后的重算）
 --   ★★ **机制真相（★ 实测 + 源码双证，★ 推翻本轮早先的解释）**：
 --   ★★ ① 修复前的 featured 静态集 = rp 各项的 **raw + standardized** 双别名
 --   ★★    ⇒ rp 的 **34 条全部是裸 canonical 名**（实测 0 条 namespaced）⇒ 对 namespaced raw **一律命不中**
 --   ★★    ⇒ ⇒ **「rp 成员的 raw 变体 pre 期就已 featured」是错的** ✘
 --   ★★ ② 真正让 namespaced raw 在 pre 期拿到 featured 的是**用量 top-N**：
 --   ★★    `SELECT rl.raw_model_name … GROUP BY rl.raw_model_name ORDER BY calls DESC LIMIT $2`
 --   ★★    ⇒ ★★ tier 里存的就是 **namespaced raw 名** ⇒ **命不中静态集也能 featured**
 --   ★★ ③ 该 tier **每 10 分钟刷新 + 24h 滚动窗** ⇒ ⇒ ★★ **「pre 期是否 featured」是逐时刻变动的属性**
 --   ★★    ⇒ ⇒ 同一只模型在同一 pre 窗口里**既有减半样本也有未减半样本**（实测 `zcode/GLM-5.3-Flash-Trial`）
 --   ★★    ⇒ ⇒ ★★★ **「人群状态」这一维不可能用成员资格重建**（DB 里没有 tier 的历史）
 --   ★★    ⇒ ⇒ ★★★ **只能用「阶梯值痕迹」反推** ⇒ ★★ 这就是「干净 pre」必须用阶梯值证据集的原因
 --   ★★ **证据集 = 「未减半阶梯空间」取半后的「独占值」**：
 --   ★★   未减半空间 = 三条链 ∪ 第四条链 Network[5,15,30,60] ∪ 固定 300/3600
 --   ★★           = {0,5,10,15,30,60,120,300,900,3600,7200,21600}
 --   ★★   取半 = {0,2.5,5,7.5,15,30,60,150,450,1800,3600,10800}（★ 5s 下限 ⇒ 2.5→5）
 --   ★★   **独占（减半后不再属于未减半空间）= {7,150,450,1800,10800}**
 --   ★★   ★★ 另有**配额对齐**路径产生任意值（实测 30630/30658/180…）⇒ 其减半值也无法枚举
 --   ★★   ⇒ ⇒ ★★ 实测 normal/pre7d 谱里还有 **90**（=180×0.5，★ 配额对齐值被减半）⇒ **必须一并纳入**
 --   ★★   ⇒ ⇒ ★★ **最终证据集 = {7,90,150,450,1800,10800}**
 --   ★★   ⚠️ 早先的 {150,450,1800,10800} **漏了 90** ⇒ 干净 pre 少剔 6 条 ⇒ 阈值偏严 0.5pp
 ev_set AS (SELECT v FROM (VALUES (7),(90),(150),(450),(1800),(10800)) t(v)),
 -- ★★ 「未减半阶梯空间」= 链值 ∪ 固定退避（★ 用来判定**有没有漏掉的减半痕迹**）
 unhalved_set AS (SELECT v FROM (VALUES (0),(5),(10),(15),(30),(60),(120),(300),(900),(3600),(7200),(21600)) t(v)),
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
),
-- ★★★★ **「指纹视图」（fp）**（★ 本轮第 18 处口径修正，★★ 差点造成**尾部失明 ⇒ 假绿**）
--   ★★ **问题（★ 实测）**：★ M20 的 post 段读数带**两道**只对「已结算段」成立的过滤：
--     ① 成熟度过滤 `fail_at < now() - 6 hours` ⇒ ★★ 关窗读数时 post 窗口的**最后 6h 完全不可见**
--     ② `next_at IS NOT NULL`（LEAD 必须找到「下一次探测」）⇒ ★★ 下一次探测还没发生的段被整体漏掉
--   ★★ ⇒ ★★ **尾部 6h 内若发生回归（修复失效），指纹仍会读出 0** ⇒ ★★ **假绿**
--   ★★ ⇒ ★★ 与「第 17 处修正」同族：★ **过滤掉尾部 ⇒ 偏差偏向判绿**
--   ★★ **关键洞察（★ 本轮最可复用的）**：★ M20 的**两个分量对「成熟度」的需求不同**：
--     · **机制指纹**（阶梯值 top21600_n / 阶梯谱）⇒ 只需要**失败那一行**上的 `next_retry_seconds`
--       ⇒ ★ `bg/node_probe.go:3043` 把 `next_retry_seconds` 随失败 run 一起 INSERT
--       ⇒ ★★ **阶梯值在失败时即落库** ⇒ ★★ **不需要等下一次探测** ⇒ ⇒ **可从未成熟段读**
--       ⇒ ⇒ ★★ **尾部 6h 不再失明**
--     · **slow 分量**（失败 → 下一次探测的**间隔**）⇒ **必须等下一次探测发生** ⇒ ★★ 只能读成熟段
--   ★★ ⇒ ★★ 正确修法 = **分视图读取**：★ 指纹读 fp（无过滤）· slow 读成熟视图（保留过滤）
--   ★★ ⇒ ★★ **pre 侧也出 fp 键**，作用有二：
--     ① **量具正控**：★ pre 侧若连「未减半顶格」都读不到 ⇒ ★ 量具**无鉴别力** ⇒ ★ post 的 0 不可解释
--     ② 同量纲对照（★ Stage 1 用 pre24_fp、Stage 2 用 pre7d_fp）
--   ★★ ⇒ ★★ 本视图**不含 now()** ⇒ ★★ 时钟无关 ⇒ ★★ pre 键在三时钟下逐字节一致（★ 三时钟门守着）
fpr AS (
  SELECT created_at AS fail_at, raw_model_name, next_retry_seconds, exemption, era
  FROM seg WHERE NOT success
    AND raw_model_name LIKE '%/%' AND lower(btrim(raw_model_name)) IN (SELECT m FROM newly)
), fpexp AS (
  SELECT r.*, 'pre24' win FROM fpr r WHERE r.era='pre' AND r.fail_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '24 hours'
  UNION ALL
  SELECT r.*, 'pre7d' FROM fpr r WHERE r.era='pre'
  UNION ALL
  SELECT r.*, 'post' FROM fpr r WHERE r.era='post'
), fpm AS (
  -- ★★ 逐模型聚合（★ post 窗口）⇒ ★★ 「摇摆」分量与**逐模型复核**的输入
  SELECT raw_model_name, exemption,
         COUNT(*)::text n,
         COUNT(*) FILTER (WHERE next_retry_seconds IN (SELECT v FROM ev_set))::text mul_evid,
         COUNT(*) FILTER (WHERE next_retry_seconds = 21600)::text top21600,
         COALESCE(string_agg(DISTINCT next_retry_seconds::text, ',' ORDER BY next_retry_seconds::text),'-') ladder
  FROM fpexp WHERE win='post' GROUP BY raw_model_name, exemption
)
SELECT 'M20_'||exemption||'_'||win k,
  COUNT(*)::text||' events | med_gap='||COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  ||' | p90_gap='||COALESCE((percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min))::int::text,'?')
  ||' | slow_gt300_pct='||ROUND(100.0*COUNT(*) FILTER (WHERE recheck_min > 300)/COUNT(*),1)::text
  ||' | ladder_p50='||COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY next_retry_seconds/60.0))::int::text,'?')
  ||' | top21600_n='||COUNT(*) FILTER (WHERE next_retry_seconds = 21600)::text
  ||' | ladder_max_s='||COALESCE(max(next_retry_seconds)::text,'?')
  -- ★★★★ 第 26 处口径修正（★ 2026-11-11 02:0x）：★ 「**::int 取整边界**」伪影 ⇒ post 行追加**亚分钟精度** raw
  --   ★★ **为什么 M20 是首要受害者**：★ M20 exempt 的判据是 `p90_gap ≤ pre_p90`（★ **两个取整样本相比**）
  --   ★★   而豁免组的阶梯**设计上钉 6h = 360 min** ⇒ ★★ 它的间隔分布**众数正好压在 360 附近**
  --   ★★   ⇒ ★★ **取整边界 0.5 恰好落在分布最密处** ⇒ ★★ 最容易跨界
  --   ★★ 实测（211 真库，★ 本轮）：post raw p90 = **360.5069** ⇒ `::int` = **361**；基线取整值 360
  --   ★★   ⇒ 基线真值 ∈ [359.5, 360.5) ⇒ ⇒ **真实差 ∈ (0.01, 1.01] 分钟 ⇒ 上界 ≈ 60 秒** ⇒ ⇒ **假红**
  --   ★★ ★★ 同款「1 分钟之差」在第 23 处已于 M14/M14x 的 `p99 361 vs 360` 上出现 ⇒ ★★ **同一形态**
  --   ★★ ★★ **只给 post 行追加** ⇒ ★★ pre 键逐字节不变 ⇒ **M18 不重冻** ✔
  || CASE WHEN win='post' THEN
        ' | med_gap_raw='||COALESCE(round((percentile_disc(0.5) WITHIN GROUP (ORDER BY recheck_min))::numeric,4)::text,'?')
     || ' | p90_gap_raw='||COALESCE(round((percentile_disc(0.9) WITHIN GROUP (ORDER BY recheck_min))::numeric,4)::text,'?')
     || ' | max_gap_raw='||COALESCE(round((percentile_disc(1.0) WITHIN GROUP (ORDER BY recheck_min))::numeric,4)::text,'?')
     || ' | gt360p5_n='||(COUNT(*) FILTER (WHERE recheck_min > 360.5))::text
     ELSE '' END v
FROM exp
WHERE ( (era = 'pre'  AND fail_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '6 hours'
                     AND next_at < TIMESTAMPTZ '2026-11-10 13:42:30+08')
     OR (era = 'post' AND fail_at < now() - interval '6 hours') )
GROUP BY exemption, win
UNION ALL
-- ★★★★ M20 的**阶梯构成**读数键（★ 本轮第 14 处口径修正 + ★ 第 16 处修正，★★ 判据阈值的「同量纲」问题）
--   ★★ **问题（★ 实测）**：★ 「newly_covered」的定义 = `st_new \ st_old`，而 `st_old` 的
--   ★★ `rawv_old` **只覆盖「models_canonical.featured=TRUE 且 status='active'」的 raw 变体**
--   ★★ ⇒ ★★ 「routing_policy.featured_models 里某个 canonical 的 raw 变体」不在 st_old 里
--   ★★ ⇒ ★★ 而这些 raw 变体在 **pre 期** 是否 featured，★ **取决于逐时刻变动的「用量 top-N」**
--   ★★   （★ 不是取决于 rp 静态集！★ rp 的 34 条全是裸 canonical 名 ⇒ 命不中 namespaced raw）
--   ★★ ⇒ ★★ ⇒ 「pre/post 同一批对」只在**「pre 期未 featured」这一维**上成立
--   ★★ ⇒ ★★ 「pre/2」阈值被污染 ⇒ ★★ **偏严**（污染样本阶梯短 ⇒ 拉低 pre 的 slow）
--   ★★ **实测（★ 扩展证据集后）**：★ M20 normal pre7d **47/154 = 30.5%** 是这类样本
--   ★★   （★ 来源几乎全是 `zcode/GLM-5.3-Flash-Trial` 一只：ladder 150×33 + 1800×8 + 90×6，
--   ★★   出现在 11-05/07/09）；★ pre24 normal **0/21**；★ exempt pre7d **2/537**（ladder 7×2）
--   ★★ ⇒ ★★ **污染只落在 normal 的 pre7d** ⇒ ★★ Stage 2 的 slow 阈值
--   ★★   应按「**干净 pre**」推导（17.8%/2 = 8.9%，而**未修正**的 12.3%/2 = 6.15% ⇒ ★★ **偏严 45%**）
--   ★★ **「干净 pre」= 剔除「乘数已生效」样本后的集合**（★ 证据集见上面的 ev_set）
--   ★★ **完备性自检（★ 本轮第 16 处修正的核心）**：★ 每个观测到的阶梯值必须**可解释**
--   ★★   （∈ 未减半空间 ∪ 证据集）⇒ 否则 `unexplained_n > 0` ⇒ ★★ **阈值可能偏严 ⇒ 判定器必须提示复核**
--   ★★   ⇒ ★★ 这让「证据集不完备」从**一份手写清单**变成**一个可自动检查的字段**（★ 不整表作废，只 fail-closed 提示）
SELECT 'M20_'||exemption||'_'||win||'_composition' k,
  'n='||COUNT(*)::text
  ||' | mul_evidence='||(COUNT(*) FILTER (WHERE next_retry_seconds IN (SELECT v FROM ev_set)))::text
  ||' | clean_n='||(COUNT(*) FILTER (WHERE next_retry_seconds IS NULL OR next_retry_seconds NOT IN (SELECT v FROM ev_set)))::text
  ||' | clean_slow='||(COUNT(*) FILTER (WHERE recheck_min>300 AND (next_retry_seconds IS NULL OR next_retry_seconds NOT IN (SELECT v FROM ev_set))))::text
  ||' | slow='||(COUNT(*) FILTER (WHERE recheck_min>300))::text
  ||' | contam_pct='||ROUND(100.0*COUNT(*) FILTER (WHERE next_retry_seconds IN (SELECT v FROM ev_set))/COUNT(*),1)::text
  ||' | null_ladder_n='||(COUNT(*) FILTER (WHERE next_retry_seconds IS NULL))::text
  ||' | unexplained_n='||(COUNT(*) FILTER (WHERE exemption='normal' AND next_retry_seconds IS NOT NULL
        AND next_retry_seconds NOT IN (SELECT v FROM ev_set)
        AND next_retry_seconds NOT IN (SELECT v FROM unhalved_set)))::text
  ||' | unexplained_values='||COALESCE(string_agg(DISTINCT next_retry_seconds::text, ',' ORDER BY next_retry_seconds::text)
        FILTER (WHERE exemption='normal' AND next_retry_seconds IS NOT NULL
          AND next_retry_seconds NOT IN (SELECT v FROM ev_set)
          AND next_retry_seconds NOT IN (SELECT v FROM unhalved_set)), '-') v
FROM exp
WHERE ( (era = 'pre'  AND fail_at < TIMESTAMPTZ '2026-11-10 13:42:30+08' - interval '6 hours'
                     AND next_at < TIMESTAMPTZ '2026-11-10 13:42:30+08')
     OR (era = 'post' AND fail_at < now() - interval '6 hours') )
GROUP BY exemption, win
UNION ALL
-- ★★★★ M20 的**指纹视图**读数键（★ 本轮第 18 处口径修正，★★ 详见主档 §5.9.21.13.14）
--   ★★ **两个分量的成熟度需求不同 ⇒ 必须分视图读取**（★ 本轮最可复用的洞察）：
--   ★★ · **指纹**（阶梯值）⇒ **不需要结算** ⇒ 读本键（★ 无成熟度过滤、无 next_at 要求）
--   ★★   ⇒ ★★ **post 窗口的尾部 6h 不再失明**（★ 否则尾部若回归 ⇒ 指纹仍读 0 ⇒ ★★ 假绿）
--   ★★ · **slow**（失败→下一次探测的间隔）⇒ **需要结算** ⇒ 读 `M20_<层>_<窗>`（★ 保留 6h 成熟度过滤）
--   ★★ 字段：
--   ★★   n_all         样本量（★ ★ 「0 条未减半顶格」在 n_all=0 时是**平凡真** ⇒ 必须看 n_all）
--   ★★   top21600_n    **未减半 6h 顶格** ⇒ 机制指纹（★ 期望 0）
--   ★★   top10800_n    **减半后的 3h 顶格** ⇒ **正向证据**（★ 10800 ∉ 未减半空间 ⇒ 只能是乘数产生的）
--   ★★   mul_evidence_n 落在「减半独占值证据集」{7,90,150,450,1800,10800}里的事件数 ⇒ **正向证据**
--   ★★   ladder_p50 / ladder_max_s  阶梯谱
--   ★★ **必须用 VALUES + LEFT JOIN**（★ `GROUP BY` 空集**不产生行** ⇒ 键会缺失
--   ★★   ⇒ ★★ 判定器会把「键未产出」读成「没数据」⇒ ★★ 同 §5.9.21.13.13 的教训）
SELECT 'M20_'||w.lay||'_'||w.win||'_fp' k,
  'n_all='||COUNT(e.*)::text
  ||' | top21600_n='||(COUNT(e.*) FILTER (WHERE e.next_retry_seconds = 21600))::text
  ||' | top10800_n='||(COUNT(e.*) FILTER (WHERE e.next_retry_seconds = 10800))::text
  ||' | mul_evidence_n='||(COUNT(e.*) FILTER (WHERE e.next_retry_seconds IN (SELECT v FROM ev_set)))::text
  ||' | ladder_p50='||COALESCE((percentile_disc(0.5) WITHIN GROUP (ORDER BY e.next_retry_seconds/60.0))::int::text,'?')
  ||' | ladder_max_s='||COALESCE(max(e.next_retry_seconds)::text,'?') v
FROM (VALUES ('normal','pre24'),('normal','pre7d'),('normal','post'),
                ('exempt','pre24'),('exempt','pre7d'),('exempt','post')) AS w(lay, win)
LEFT JOIN fpexp e ON e.exemption = w.lay AND e.win = w.win
GROUP BY w.lay, w.win
UNION ALL
-- ★★ M20 的**「post 段切分」**读数键（★ 第 18 处口径修正 ⇒ ★ 用来证明「fp 视图真的更宽」）
--   ★★ window_n = post 段里 **now()-6h 之前**的失败数（★ 成熟视图能看见的那一段）
--   ★★ tail_n   = post 段里 **最后 6h** 的失败数（★ ★ 成熟视图**完全不可见**的那一段）
--   ★★ ⇒ ★ 判定器的不变量（★ 当守卫守）：★ `fp_n_all ≥ window_n + tail_n`
--   ★★   （★ 两个集合**互不相交**（一个 fail_at < now()-6h、一个 ≥）★ 且都 ⊆ fp 视图）
--   ★★ ⇒ ★ 若该不变量破 ⇒ ★ **fp 视图被改回带成熟度过滤** ⇒ ★★ post 窗口的尾部 6h 再次失明
--   ★★   ⇒ ★★ 尾部若发生回归 ⇒ 指纹仍读 0 ⇒ ★★ **假绿** ⇒ ⇒ 整表作废
--   ★★ ⇒ ★ 这就是「分视图读取」这条修正的**牙齿**（★ 否则改回旧口径没有任何门会发现）
--   ★★ ★★ **必须走独立路径**（★ 本轮变异验证抓到的真缺口）：★ 若切分键也从 `fpexp` 取
--   ★★    ⇒ ★★ fp 视图被改窄时切分键**也跟着变窄** ⇒ ★★ tail_n 同步变 0 ⇒ ★★ 守卫抓不到
--   ★★    ⇒ ★★ ⇒ ★★ 切分键必须**直接读 `seg`**（★ 原始段集）⇒ ★ 与 fp 视图链**互不依赖**
--   ★★ ★★ **必须用两个独立标量子查询**（★ 本轮负控抓到的真缺陷：★ 写成两个 LEFT JOIN seg
--   ★★    ⇒ ★★ **行相乘**（|e_win| × |e_tail|）⇒ ★★ 两个计数**恒相等** ⇒ ★★ 负控 era 里
--   ★★    实测 window_n = tail_n = 308（★ 乘法特征）⇒ ★★ 不变量恒真 ⇒ ★★ **守卫形同虚设**）
SELECT 'M20_'||w.lay||'_post_split' k,
  'window_n='||(
     SELECT COUNT(*)::text FROM seg s
     WHERE s.exemption = w.lay AND NOT s.success
       AND s.raw_model_name LIKE '%/%' AND lower(btrim(s.raw_model_name)) IN (SELECT m FROM newly)
       AND s.created_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08'
       AND s.created_at < now() - interval '6 hours')
  ||' | tail_n='||(
     SELECT COUNT(*)::text FROM seg s
     WHERE s.exemption = w.lay AND NOT s.success
       AND s.raw_model_name LIKE '%/%' AND lower(btrim(s.raw_model_name)) IN (SELECT m FROM newly)
       AND s.created_at >= TIMESTAMPTZ '2026-11-10 13:42:30+08'
       AND s.created_at >= now() - interval '6 hours') v
FROM (VALUES ('normal'),('exempt')) AS w(lay)
UNION ALL
-- ★★★★ M20 的**逐模型 fp 阶梯谱**（★ post 窗口）⇒ ★★ 「摇摆」分量的原料 + **逐模型复核**工具
--   ★★ 键名：`M20_<层>_post_fp_model__<raw_model_name>`
--   ★★ 字段：n / mul_evid（★ 落在减半独占值证据集里的条数 ⇒ ★ 该模型**当时是 featured** 的证据）
--   ★★       top21600（★ **未减半**的 6h 顶格条数）/ ladder（★ 阶梯谱）
--   ★★ ★ 用途：★ Stage 1 若指纹判负 ⇒ ★ 能立刻指出「**哪只模型**、谱是什么」⇒ ★ 人工复核
SELECT 'M20_'||exemption||'_post_fp_model__'||raw_model_name k,
  'n='||n||' | mul_evid='||mul_evid||' | top21600='||top21600||' | ladder='||ladder v
FROM fpm
UNION ALL
-- ★★★★ M20 的**「摇摆」**读数键（★ 第 20 处口径修正，★★ 修复前后的判别信号）
--   ★★ 定义：★ post 段 normal 层里，★ **同一模型同时**出现
--   ★★   ① 减半独占值（mul_evid > 0 ⇒ ★ 该模型**当时 featured** ⇒ ★ 阶梯被减半）
--   ★★   ② 未减半 6h 顶格（top21600 > 0 ⇒ ★ 该模型**当时不 featured** ⇒ ★ 阶梯未减半）
--   ★★ ⇒ ★★ **「摇摆」= 该模型的 featured 状态在窗口内不稳定**
--   ★★ ★★ **为什么它是强信号（★ 机制推演 + 真库实测双向确认）**：
--   ★★   · **修复前**：★ newly 覆盖的 namespaced raw 模型**不在 static 集里**（★ static 只有 45）
--   ★★     ⇒ ★★ 只能靠 **usage top-N tier** featured ⇒ ★★ tier 变动 ⇒ ★★ **摇摆普遍**
--   ★★     ⇒ ★★ 实测（★ 负控 era 逐模型谱）：★ `anthropic/claude-opus-4.8` **同时**有 10800 + 21600 ✔
--   ★★   · **修复后**：★ static 展开到 **210**（★ 含 200 个 raw 变体 ⇒ ★ newly 模型**永久 featured**）
--   ★★     ⇒ ★★ **tier churn 不再影响它们** ⇒ ★★ **不应摇摆** ⇒ ★★ 实测：★ 当前 post 该模型**只有 10800** ✔
--   ★★ ⇒ ★★ **⇒ 「摇摆 0 只」= 修复已真正落地（★ 而不只是「碰巧 featured」）**
--   ★★ ⇒ ★★ **⇒ 且它天然免疫「usage tier churn ⇒ 假红」**（★ 因为摇摆的成因已被 static 展开消除）
--   ★★ ⇒ ★★ 判据：★ swing_n == 0 ⇒ ✅；★ > 0 ⇒ ❌（★ 修复前特征 ⇒ featured 状态仍不稳）
SELECT 'M20_normal_post_fp_swing' k,
  'swing_n='||COUNT(*) FILTER (WHERE mul_evid::int > 0 AND top21600::int > 0)::text
  ||' | swing_models='||COALESCE(string_agg(raw_model_name, ',' ORDER BY raw_model_name)
        FILTER (WHERE mul_evid::int > 0 AND top21600::int > 0),'-')
  ||' | models_n='||COUNT(*)::text v
FROM fpm WHERE exemption='normal'
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
