# AUTO 路由 v2 · P2 TierSelector 接线设计稿（影子 → 灰度）

- 日期：2026-09-25 轮（定稿于 2026-09-26）
- 状态：**设计稿，未实现**——本稿不含任何已写/已跑的接线代码；文中全部接线内容为实现规格，非已演示结果
- 前驱与依据：
  - `docs/planning/AUTO_ROUTING_CLOSED_LOOP_V2_PLAN.md` §4.6（:212-224）、§六 P2（:257）、附录 A（:285-293）、§五（:239-247）
  - `docs/audit/2026-09-25-auto-routing-v2-p1-252-e2e.md`（252 真库只读 E2E，P1 读面现状）
  - `docs/audit/2026-09-25-auto-routing-v2-p0p1-critical-audit.md`（P0+P1 批判复审）
- 证据级别：文中 path:line 引用来自本轮 P2 取证（只读 grep/实读）+ 同轮独立复核席复现（结论 confirmed=true，详见 §8）；复核纠正的三处次级误差本稿已按复核口径采用。252 侧数字均来自上列取证文档的实跑输出，本稿作者未重连数据库。

---

## 1. 目标与非目标

### 1.1 目标

1. **闭合 G1**：`autoroute.NewTierSelector` 当前**零生产构造点**（全仓 grep `NewTierSelector|SelectTier(` 仅 `autoroute/tier_selector.go:78`（定义）与 `:96`（SelectTier 定义），无任何调用方——连测试都没有；`taskprofile/csv.go:356`、`taskprofile/handler.go:374` 注释原文即为 "zero production constructors"；`cmd/gateway/main.go` 中 TierSelector 零匹配，Decider 注入链只有 `NewDecider:5035`/`SetOverrideStore:5216`/`SetDefaultRoutingStore:5225`/`SetWorkTypeRouteStore:5235`/`SetRoleLLMRouter:5244`/`SetTenantResolver:5251`）。P2 把它接进生产热路径：**影子先行 → 灰度 → 生效**。
2. **零迁移落影子**：would-be tier 记录走 `request_logs.auto_decision` JSONB 既有列，`needsMigration=false`（落点论证见 §4.3，含对规划 §五 plan:243 的勘误）。
3. **建立 tier 过配率基线**（规划 §4.7 plan:233 无现状基线——TierSelector 未生效），口径与"基线怎么算成立"见 §7。
4. **G3 词汇裁决定稿**：升格规划附录 A 并加两处修订（§3）。

### 1.2 非目标（明确不做）

1. **不做 V3 分类器替换**。V3 影子分类（flag `LLM_GATEWAY_AUTO_V3_SHADOW`）是规划 §4.6.3（plan:222-224）的**独立项 P3**，且规划明文"替换本身是独立裁决，不在本计划内完成"（plan:223-224）。本稿只做 TierSelector 接线，不碰 `classifier_v3.go`、不做任何 L1→V3 替换动作。
2. **不统一两套 tier 词汇的 schema**：不合并列、不放松/修改任何一侧 CHECK 约束（危险面见 §3.3）。
3. **本稿不写 Go 代码**：P2 执行是后续独立任务；本轮仅设计与取证（未跑测试套件，未改热路径）。
4. **不做 V1 Decide 孪生点**：V1 的对称挂点在 `autoroute/decision.go:653-674`（winner :653 之后、annotateTreatment 附近），但规划 §六 P2 触点只列 decision_v2.go（plan:257），且生产热路径恒走 V2（§4.1），**不作为本设计范围**。
5. **不清洗第三套词汇污染数据**（provider_models.tier / model_pricing.tier，§3.5）：登记挂账，P2 只在文档层点名。
6. **不动前端**：不改 `web/public/menu-config.json`、不加页面（管理面属 P0 范畴）。

---

## 2. 现状基线（取证结论，均经同轮复核席复现）

| 事实 | 证据 |
|---|---|
| TierSelector 零生产构造、SelectTier 零调用 | `autoroute/tier_selector.go:78,96` 唯一定义处；`cmd/gateway/main.go` 零匹配；`docs/handoff/2026-09-19-session-role-llm-routing-handoff.md:219` 独立佐证 "dormant" |
| 生产热路径 = DecideV2 | `autoroute/feature_flags.go:335-341`：`UseChannelQualityRouting` 默认 true（:142/:182 唯一默认开的 AUTO_* flag）→ `DecideWithFeatureFlags` 恒走 V2；`autoroute/decision_role_wiring_test.go:11` 注释亦注明 "DecideV2（生产默认路径）" |
| `AUTO_TIER_SELECTOR` / `AUTO_V3_SHADOW` 目前不存在于任何 Go 代码 | 全仓 grep 零命中（仅规划文档提及） |
| `auto_route_selections(_hot)` 是纯平表，无 JSONB 列 | `sql/migrations/startup/478_auto_route_affinity.sql:54-88`、`656_auto_route_selections_hot.sql:9-25` 全为 TEXT/NUMERIC/BOOLEAN；写入方 `domains/hooks/observability/telemetry/selection_writer.go:234`（selectionColumnCount=38）+ `:320-334`（INSERT 38 平列全平铺） |
| auto_decision JSONB 实际落 request_logs | `db/request_logs_view_schema.go:413,587`（列定义）；写入链见 §4.3 |
| P1 读面 F1（词表失配）尚未修 | `taskprofile/analyzer.go:392,429,484,509` 仍过滤 `classifier='heuristic'`，252 全库 0 命中（见 252 取证文档 §6 F1）——**P2 影子数据的分析查询必须避免同款失配** |

---

## 3. G3 词汇裁决（规划附录 A 升格定稿）

### 3.1 裁决

**两套词汇并存、仅报表口径映射、运行时不强绑**——规划附录 A（plan:285-293）方向原样采纳并升格为裁决；本节即该附录的定稿版，plan 附录 A 仅保留映射表 + 指回本节（已加指引句）。

| 词汇 | 档位 | 承载列（DB CHECK 钉死） | Go 侧定义 | 运行时状态 |
|---|---|---|---|---|
| **成本档** tier-a/b/c | 按价格带对任务类型/模型的建议性三值分类 | `task_type_tier_config.preferred_tier`（`db/db.go:2217` 自愈 DDL、`deploy/sql/migrations/V370__create_tier_config_table.sql:7-17` 真表所有者；`sql/migrations/202609_02_create_task_type_tier_config.sql:5-22` 已降级为文档桩）+ `fallback_tiers TEXT[]`（V370:11,28） | `taskprofile/types.go:24-26` 三常量；`autoroute/tier_selector.go:379-386` `isValidTier` 仅放行 tier-a/b/c（X-Gw-Model-Tier 头覆盖同走此校验 :108-118）；产出方 `taskprofile/registry.go:38-70`、`taskprofile/suggest.go:96-105` escalateTier(c→b→a)；写面 `taskprofile/csv.go:393-407` | **未接线**（TierSelector 零构造，§2） |
| **路由档** primary/secondary/fallback | 管理员对具体 canonical 模型在特定 work_type 下的偏好排序 | `work_type_model_route.tier`（`sql/migrations/startup/046_task_route_tiers.sql:9-11` CHECK；`deploy/sql/schemas/baseline/01-schema.sql:19277-19287` 同款；002 建表时无 tier 列由 046 补加；**491:11 仅 ADD COLUMN 无 CHECK**——复核纠正，见 §8） | `autoroute/work_type_route_store.go:40`（Tier 字段注释）、:157-166 tierBoost、:395-406 routeTierRank；admin 写面 `admin/work_types.go:770,819-826`、ACC 同步 `admin/acc_work_types.go:218-229`（均白名单校验路由档） | **已接线生效**（`cmd/gateway/main.go:5231-5235` 挂进 Decider，1 分钟 bg 刷新） |

**理由**：两套词汇回答不同问题；且 **primary ≠ tier-a**（general_chat 的 primary 完全可以是经济型模型，强行映射会篡改管理员意图）；两侧 DB CHECK 各自钉死枚举，统一=对在线热路径表做破坏性迁移 + 重写 admin 校验 + 重写 ACC 同步协议。

### 3.2 升格修订一：词汇同名陷阱（附录 A 需加第三列说明）

成本档的 `fallback_tiers`（tier-b/tier-c **列表**，V370:11）与路由档枚举值 `"fallback"`（`work_type_route_store.go:401` routeTierName）**同名不同义**——这是 P2 接线与报表映射时最可能误读的点，裁决文本显式写明。同类命名碰撞：`getFallbacksForTier`（`tier_selector.go:391` 附近）承载成本档列表。

### 3.3 错选（强行统一）的炸点——为什么裁决是"不统一"

- 正向（把 primary 写进 preferred_tier）：被 `db/db.go:2217` CHECK 以 23514 拒绝；`taskprofile/csv.go:379-383` ApplySuggestions 是全量事务，一个类型失败整批回滚，`POST /apply-tier-config` 全量 500。
- 反向（把 tier-a 写进 work_type_model_route.tier）：先被 `admin/work_types.go:770` 以 admin_invalid_tier 400 拒绝；ACC 同步在 `admin/acc_work_types.go:223-224` 整批失败。
- **最危险：松掉两侧 CHECK 硬统一**——`routeTierRank` 对 tier-a 返回默认 rank 3（`work_type_route_store.go:395-406`），所有配置模型掉出 eligible 池；`activeTierRank==4` 时 `ApplyTierPolicy` 静默返回未过滤集（:291-296）——管理员偏好**无声失效**，比响亮的 CHECK 失败更难察觉的路由回归。

### 3.4 配套动作（P2 执行时逐条做，全部为注释/文档）

1. 头注释互引（核心对）：`autoroute/tier_selector.go:12-25`（现仅引 V3 设计文档）↔ `autoroute/work_type_route_store.go:3-14`（路由档语义），互相引用并注明"成本档 fallback_tiers ≠ 路由档 fallback"。
2. 次级：`taskprofile/types.go:24-26` 常量注释、`taskprofile/registry.go:11-16` 头注释、写面 `admin/work_types.go:770` 与 `admin/acc_work_types.go:218-229` 注释指向本节；本节成为唯一映射 SSOT。
3. 顺带登记（不强制本轮修）：路由档排序语义 primary>secondary>fallback 在四处重复、无单一定义点——`work_type_route_store.go:129`（SQL CASE）、:395-406（routeTierRank）、`admin/work_types.go:863,898`——P2 触碰这些文件时统一到 routeTierRank。

### 3.5 升格修订二：第三套已知污染登记（provider_models.tier）

`deploy/sql/migrations/V370__create_tier_config_table.sql:79-94` 把 `model_pricing.tier`（CHECK 为 free/basic/standard/premium/enterprise，`sql/objects/tables/model_pricing.sql:32`——**第三套词汇**）镜像进注释却声称 tier-a/b/c 的 `provider_models.tier`（V370:55-58）；V370:132-134 的验证计数按 `model_pricing.tier='tier-a'` 统计恒为 0。grep 证实该列**无任何 Go 运行时读方**（仅 `bg/model_tier.go:218`、`installer/internal/dbinit/runner.go:192` 注释提及），故未爆。挂账不改值，但：**报表映射严禁把 provider_models.tier 当成本档数据源**。

---

## 4. 影子模式接线规格

### 4.1 flag 设计：两值一旗（非两个独立 flag）

`autoroute/feature_flags.go` 新增两个字段（复刻 AutoOptimizationV3 先例：字段 :108-114 + env :199-200，默认 Enabled=false + ShadowOnly=true）：

| 字段 | env | 默认 | 语义 |
|---|---|---|---|
| `AutoTierSelectorEnabled` | `LLM_GATEWAY_AUTO_TIER_SELECTOR` | false | 总开关。名字与规划 §4.6 plan:218 原文一致（§4.2 plan:135 的 AUTO_TIER_SELECTOR 为层图简写） |
| `AutoTierSelectorShadowOnly` | `LLM_GATEWAY_AUTO_TIER_SELECTOR_SHADOW` | true | on 时是否只记录不过滤 |

三态由组合表达：**off**（Enabled=false）／**影子**（Enabled=true + ShadowOnly=true）／**灰度**（Enabled=true + ShadowOnly=false）。单开总开关安全落入影子而非直接 enforcement——避免两个独立 flag 出现"灰度开着但影子数据没采"的歧义态，也无需新造 tri-state 解析。既有 AUTO_* flag 盘点见 `feature_flags.go:134-166`（DefaultFeatureFlags）与 :174-220（LoadFeatureFlagsFromEnv）；灰度先例：R48 单布尔（装配无条件 + Decide 内查 flag，:207）、V3 双字段（:199-205）。

### 4.2 注入点

- **装配**：`cmd/gateway/main.go` 构造 `autoroute.NewTierSelector`（`tier_selector.go:78-86`，纯内存分配无 I/O）注入 Decider，同款 setter 注入链（先例 `SetRoleLLMRouter` main.go:5244）。**无条件装配本身 inert**（R48 先例 main.go:5240-5244 装配不改变 flag-off 行为），门控核心在 Decide 侧 flag 短路，不在装配。
- **影子挂点（生产热路径 DecideV2）**：`autoroute/decision_v2.go:370` winner 选定、:378-394 Decision 构建完成后，与 `d.annotateTreatment`/`d.populateShadow` 同排（:401-402）加 `d.annotateTierShadow(ctx, apiKeyID, cls, decision, recommended)`。选此槽位的理由：此处 override/work_type 级联已全部完成（FilterBanned:275 → ApplyTierPolicyWithWorkType:297 → role promote:314-319 → PromotePins:337 → routingSource:350-356），winner 与豁免名单 `append(pins, rolePrefs...)`（:297）均可复用，纯注解、不动 recommended 切片。
- **灰度 enforcement 过滤位（占位）**：级联完成后、`decision_v2.go:356` routingSource 定案与 :357 topN 截断之间。语义澄清：现状代码中 L2 前置硬过滤（可用性 `recommend_v2.go:75` + MinStandardIQ :100-115）在 `RecommendV2WithHints` 内、**先于**级联执行，故规划 §4.6 的"级联之后、L2 漏斗前过滤之前"应解读为 enforcement 过滤位的占位描述；灰度开启后的过滤实现仿 `applyTierPolicyWithRoutes`（`work_type_route_store.go:283-310`）：同款 pinned 豁免、保序过滤、不重排。影子挂同一槽位，记录 would-be tier + winner 是否会存活/被豁免，为灰度预测过滤效果。
- **输入可用性**：SelectTier 需 TaskType/Confidence（cls 提供）、TenantID（`d.TenantResolver`，main.go:5251 已接）；`X-Gw-Agent-Depth` / `X-Gw-Model-Tier` 头目前**无任何代码读取方**（全仓 grep；仅 tier_selector.go 注释/Reason 字符串 5 处提及这两个头名——复核措辞见 §8），影子初期按零值记录，或顺带在 `maybeResolveAuto`（`domains/streaming/auto_route.go:345-396`）补头提取（P2 执行时裁决，非必须）。
- **超时纪律**：`SelectTier` 缓存过期时同步查库（`tier_selector.go:199-210` refreshCache/queryConfig；5min 进程内缓存 :82-84），影子调用**必须带短超时**（`populateShadow` 的 100ms 先例 `decision.go:763-764`）；超时/失败记缺省（unknown/不写），绝不影响决策主链。

### 4.3 JSONB 字段：`would_be_tier`（+可选 `would_be_tier_source`）落 `request_logs.auto_decision`

写入链（全部既有，零新写入代码）：`autoroute.Decision` 新字段（omitempty，先例 SessionRole/TaskKind `decision.go:103-104`）→ `decisionToWire`（`auto_route.go:539`，条件映射先例 :562-567）→ 同一 wire JSON 双通道：① `X-Gw-Auto-Decision` 响应头（`auto_route.go:621-631` writeAutoDecisionHeader，16KiB 上限 :85-91）；② `request_logs.auto_decision` JSONB（`domains/streaming/handler.go:2893` SetAutoDecision → `domains/streaming/request_log_pipeline.go:842` jsonMarshal → `domains/hooks/observability/telemetry/client.go:1228` INSERT / :1398 upsert / :2048 `auto_decision = COALESCE($51::text::jsonb,...)`；列定义 `db/request_logs_view_schema.go:413,587`）。

> **对规划 §五的勘误（plan:243-244）**：规划写"auto_route_selections.auto_decision JSONB 内增字段"——该表**没有** auto_decision 列（§2 平表证据），字面执行反而要 ADD COLUMN，与规划自己"避免加列"（plan:244）矛盾。零迁移的 JSONB 路径是 `request_logs.auto_decision`；影子效果分析用 request_id JOIN `auto_route_selections_hot` 的 outcome 列（`uq_ars_hot_request` 唯一键，656:42）。**本稿以此为准。**

### 4.4 flag-off 钉桩（字节级不变，五处）

1. `autoroute/decision.go` Decision 新字段 omitempty + flag-off 时零值（先例 :103-104 SessionRole/TaskKind；:76-79 FilterReasons 注释明言 "flag-off decisions serialise byte-identically"）。
2. decision_v2.go 影子代码块整体跳过、不调 SelectTier、不触 DB（先例 `roleRoutingActive` 集中判定 `decision.go:703-711`）。
3. `decisionToWire` 条件映射 + wire 结构 omitempty（先例 `auto_route.go:114-120`，RoutingSource 仅 role_route 时透出）。
4. 序列化字节：X-Gw-Auto-Decision 头与 `request_logs.auto_decision` JSONB 在 flag-off 时逐字节等于现状（测试先例 `domains/streaming/auto_route_filter_reasons_test.go:12-17`，omitempty payload byte-identical）。
5. 行为级等价：同输入同候选集、同 winner、同 RoutingSource（测试先例 `autoroute/decision_role_wiring_test.go:7-11`："flag 关闭 → 决策结果与无角色信号时完全一致（同候选、同 winner、审计字段零值）"）。

### 4.5 与 MinStandardIQ / work_type 级联的交集语义（取交集，不替代）

TierSelector 过滤是**级联后的第二道独立硬过滤**，与 MinStandardIQ（漏斗内、fail-open `standard_iq_gate.go:34-50`、租户>全局 :64-74、`recommend_v2.go:90-115` 前置硬过滤 + FilterNotes → `decision.go:76-79`）叠加生效：

```
有效集 = (IQ 通过 ∧ 评分 ∧ 级联存活) ∩ (tier 通过 ∨ 豁免(pins + rolePrefs))
```

硬约束（全部有仓内先例）：

- **不得复活** IQ/可用性/级联已排除的候选（AffinityStore 同款 additive-only 纪律，`main.go:5264-5269` 注释）。
- 对 work_type 级联**复用同一豁免名单**（`decision_v2.go:297` `append(pins, rolePrefs...)` 先例）。
- **保序过滤不重排**（`applyTierPolicyWithRoutes` `work_type_route_store.go:283-310` 先例）。
- 尊重文档化次序 **pin > role_route > work_type tier**（`decision.go:604-608`）。
- 两套 tier 词汇在运行时**各自独立判定**（§3 裁决），附录 A 映射仅报表口径。
- MinStandardIQ 门禁语义**不动**（fail-open、租户覆盖、阈值均不因 tier 过滤改变）。

---

## 5. 灰度与回滚

| 阶段 | flag 状态 | 行为 | 准出条件 |
|---|---|---|---|
| off | Enabled=false | 现状字节级不变 | §4.4 五处钉桩全绿（进入影子的前置） |
| 影子 | Enabled=true, ShadowOnly=true（默认组合） | `annotateTierShadow` 记录 would_be_tier，不过滤 | **连续 ≥7 天** + 过配率基线成立（§7 判据） |
| 灰度 | Enabled=true, ShadowOnly=false | enforcement 过滤生效（§4.2 过滤位，仿 applyTierPolicyWithRoutes） | 规划 §六 P2 验收：GRRQ 不回退（auto-testbench 门禁）+ 成本不回退；过配率相对基线 ↓30%（plan:233）；异常即回滚 |

- **回滚 = 关 flag**（Enabled=false，按当期 flag 加载路径生效——env 型 flag 以进程加载为准，重启即回；与规划 §六 P2 "回滚=关 flag" 同义）。影子态可长期驻留（纯注解、超时保护、不影响决策）。
- 灰度收益锚点：路由分级普遍 40-70% 成本节约的公开报告，本仓保守取 ≥30%（plan:109）；灰度验证期同时报告 GRRQ 与 avg cost（plan:235）。
- **依赖提醒**：影子期分析查询（§7）必须用 V2 词表（`heuristic_v2`/`llm_v2`），不得复刻 P1 analyzer 的 F1 失配（§2 末行）。

---

## 6. 测试矩阵

| # | 场景 | 断言 | 先例/参照 |
|---|---|---|---|
| T1 | flag-off 序列化字节 | X-Gw-Auto-Decision 头 + request_logs.auto_decision JSONB 与基线**逐字节一致** | `auto_route_filter_reasons_test.go:12-17` |
| T2 | flag-off 行为等价 | 同输入 → 同候选集、同 winner、同 RoutingSource、审计字段零值 | `decision_role_wiring_test.go:7-11` |
| T3 | 影子记录 | Enabled+ShadowOnly → Decision 带 would_be_tier（SelectTier 结论+来源），recommended/winner 不变 | 新增；挂点 §4.2 |
| T4 | 影子降级 | SelectTier 超时/DB 错 → 字段缺省，决策不变、不 panic、无额外延迟（100ms 超时钉桩） | `decision.go:763-764` 先例 |
| T5 | 交集不替代 | IQ/可用性/级联已排除的候选，tier 过滤不得复活；tier 排除不改变 IQ 结果 | `main.go:5264-5269` additive-only |
| T6 | 豁免名单复用 | pins+rolePrefs 豁免；pin > role_route > work_type tier 次序不破 | `decision_v2.go:297`、`decision.go:604-608` |
| T7 | 灰度过滤保序 | enforcement 过滤保序、不重排、pinned 豁免——表驱动用例 | `work_type_route_store.go:283-310` |
| T8 | flag 组合三态 | off／on+shadow（绝不过滤）／on+no-shadow（过滤）行为表 | `feature_flags.go:108-114,199-205` |
| T9 | 并发 | 并发 Decide 下 TierSelector 缓存刷新无竞态（configCache map） | HEAD 9679ee80b 并发 Decide 三件套 |
| T10 | 零迁移 | selection_writer 列数 38 不变；request_logs.auto_decision 既有列复用，无 ADD COLUMN | `selection_writer.go:234,320-334` |
| T11 | 输入映射 | cls.TaskType/Confidence/TenantID 正确传入；头零值路径；isValidTier 拒绝非成本档词 | `tier_selector.go:379-386` |

---

## 7. 七天影子基线：过配率口径与"基线成立"判据

### 7.1 过配率定义（承接规划 §4.7 plan:233）

> tier 过配率 = 简单任务落 tier-a 的占比；首版"简单任务集合"建议 = **{chat}**（252 真实分布 chat=12858 为最大桶，252 取证文档 §4；是否扩 planning 等低复杂度类，影子报告评审时定）。

- **影子期口径**（基线用）：分子 = `task_type ∈ 简单集合 AND would_be_tier='tier-a'` 的 auto 请求数；分母 = `task_type ∈ 简单集合 AND would_be_tier 非空` 的 auto 请求数；窗口 = 滚动 7 天。
- 数据源：`request_logs.auto_decision->>'would_be_tier'` + `request_logs.task_type`（规划 §二数据落点 plan:63-64）；全程 SELECT，符合 252 共享库只读纪律。
- 效果分析（灰度预测用）：request_id JOIN `auto_route_selections_hot`（`uq_ars_hot_request`）取 outcome/reward 列，对比 would-be 与实际 winner 的存活/被豁免情况。

### 7.2 基线成立的判据（"怎么算过"）

1. **连续 ≥7 天、无采集缺口**——前置检查：252 已观测到 selection 写入 09-20 13:04 → 09-25 23:55 断档（252 取证文档 §6 F4），影子期开始前必须确认 auto_decision 影子字段连续写入；出现断档该日不计入 7 天窗口。
2. **样本量下限**：简单任务集合每日 `would_be_tier` 非空行 ≥100。锚点：chat 现状 ≈12858 行 /（09-07→09-26 约 19 天）≈ 677 行/天（252 取证文档 §4），100/天门槛有 ~6.7× 余量；若某日不足则该日剔除并顺延。
3. **口径冻结留档**：7 日累计过配率 + 日级序列写入 `docs/audit/` 影子报告；基线数值即灰度期 ↓30% 目标的分母（plan:233）。

> 数值稳定性阈值（如日间极差上限）**不在本稿拍板**——由 ≥7 天真实序列出来后的影子报告评审定，避免现在凭空造数。此项标记为**待影子数据裁决**。

---

## 8. 复核口径与未证实项（原样标明）

**同轮独立复核席结论 confirmed=true**：四项特别核对（NewTierSelector 无生产构造／两套词汇分属不同列且附录 A 非运行时绑定／analyzer 已用 `_all`／252 证据文件存在且无密码、无写语句）全部独立复现成立；P2 接线关键事实（JSONB 落点、flag 盘点、影子挂点、IQ 门禁、钉桩先例）抽核吻合。本稿据此采用。

**复核纠正的三处次级误差，本稿已按复核口径采用**：

1. `sql/migrations/startup/491_work_type_default_routes.sql:11` 仅 `ADD COLUMN tier ... DEFAULT 'secondary'`，**无 CHECK 子句**（CHECK 只在 046 与 baseline）——本稿 §3.1 表述已照此（本节作者本轮实读 491 确认）。
2. 658 迁移特征列为 **15** 列（此前一轮口径误为 14）。
3. 引用精度三处：selection_writer.go 实际路径为 `domains/hooks/observability/telemetry/selection_writer.go:234`；annotateTreatment/populateShadow 实际行号 :401-402（非 :400-401）；"X-Gw 头 grep 零命中"应为"**无代码读取方**（tier_selector.go 注释/Reason 字符串 5 处提及头名）"。

**未执行项（原样，不得写成已演示）**：

- **本稿全部接线/过滤/影子内容为设计规格，未写代码、未运行、未产生任何影子数据**；过配率基线尚未存在（§7 是口径定义，不是测量结果）。
- 测试矩阵 T1–T11 为**待执行清单**，一条都未跑（本轮取证与文档轮未跑测试套件）。
- 252 数据库本轮由取证员实跑（见 252 取证文档），本稿作者与复核席均未重连；CHECK 约束拒绝行为（23514 等）由 DDL 文本裁决，未做数据库实测。
- 252 侧 wroteGenerate=false / wroteApply=false（252 取证文档 §7）。
- 本稿作者本轮追加抽核属实的关键引用：`tier_selector.go:78-86,96,379-386`、`decision_v2.go:297,370,385,401-402`、`feature_flags.go:108-114,199-207`、`work_type_route_store.go:291-296,395-406`、`main.go:5035,5231-5244,5264-5269`、`auto_route.go:539,621-631`、`db/db.go:2217`、`annotation_handler.go:349-350`、`analyzer.go:392,531-535`、046/491 SQL CHECK 差异；其余 path:line 引用出自取证/复核材料（ask 所给）。

---

## 9. 开放问题（P2 执行轮裁决）

1. 是否在 `maybeResolveAuto`（`auto_route.go:345-396`）补 X-Gw-Agent-Depth / X-Gw-Model-Tier 头提取（§4.2：影子初期可按零值）。
2. `would_be_tier_source` 是否随首版一起写（可选字段，§4.3）。
3. 过配率"简单任务集合"是否扩类、日间稳定性阈值（§7.2-3，待影子数据）。
4. 路由档排序语义四处重复的收敛（§3.4-3，顺带项）。
