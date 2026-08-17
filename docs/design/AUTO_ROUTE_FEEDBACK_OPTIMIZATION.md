# Auto 模型选择：业务逻辑总结 + 反馈闭环优化方案

> 状态：**已实现并通过审计修复**。2026-08-10 实现，2026-08-11 完成 audit-driven 修复并通过 EXPLAIN + 端到端冒烟测试。migration 478 已应用至本地 PG 与 252（含 2026_08/09/10 月度分区）；migration 478p2 索引已应用。
> 日期：2026-08-10 实现，2026-08-11 审计修复
> 关联文档：`docs/HANDOFF_478_CRITICAL_FIXES.md`（审计发现与修复细节）、`docs/AUTO_SELECTION_SPEC.md`、`docs/CHANNEL_QUALITY_ROUTING_DESIGN.md`、`autoroute/V2_IMPLEMENTATION_STATUS.md`
> 目标：让「任务 → 最佳模型」列表能够**根据统计数据与成功率自动优化**，并反过来影响后续请求的模型分配。

## 实施状态（2026-08-11 更新）

| 组件 | 文件 | 状态 |
|---|---|---|
| 数据模型 | `sql/migrations/startup/478_auto_route_affinity.sql` (+`.down.sql`) | ✅ 本地 + 252，含月度分区 2026_08/09/10 + DEFAULT |
| 索引（gw_session_id + auto-task） | `sql/migrations/startup/478p2_affinity_indexes.sql` | ✅ 本地 + 252 |
| 纯数学（收缩/EMA/衰减/探索/奖励） | `autoroute/affinity.go` | ✅ 18 单测 |
| 亲和度快照存储 | `autoroute/affinity_store.go` | ✅ 11 单测（含租户回退）+ `ShouldExplore` 方法 |
| 第 5 维评分 | `autoroute/scoring_simplified.go::ScoreWithAffinity` | ✅ 7 单测，shadow=逐位相同 |
| 异步匹配记录 | `domains/hooks/observability/telemetry/selection_writer.go` | ✅ 8 单测，非阻塞 |
| 结算 worker | `bg/auto_route_settle_worker.go` | ✅ 6 单测（Start/Stop 守卫）+ EXPLAIN + E2E 通过 |
| 学习 worker | `bg/auto_route_affinity_worker.go` | ✅ 6 单测（Start/Stop 守卫）+ EXPLAIN + E2E 通过 |
| 存储刷新 worker | `bg/affinity_store_refresher.go` | ✅ |
| 决策路径接线 | `autoroute/index.go`、`recommend_v2.go`、`decision_v2.go` | ✅ E2E 单测（`-race` clean）|
| 请求级记录（带 Explore 标签） | `domains/streaming/auto_route.go::recordAutoSelection` | ✅ |
| main.go 启动 | `cmd/gateway/main.go` | ✅ store + 3 个 worker flag-gated |
| Admin API | `admin/auto_route.go::HandleAffinityRanking` + `handleAffinitySelections` | ✅ 读路由，仅 `v_task_model_ranking` + 选定字段 |
| 全量编译/单测 | — | ✅ `go build ./...` + `go vet` + 660+ 单测通过 |

## 审计修复（2026-08-11）

详见 `docs/HANDOFF_478_CRITICAL_FIXES.md` 与 `bg/auto_route_workers_test.go` 中的回归测试。下表是简要状态：

| 审计发现 | 严重程度 | 修复 |
|---|---|---|
| `requestLogSource` UNION ALL 在列存父表上计划失败 (`invalid perminfoindex 0`) | CRITICAL-1 | 改为只读 `request_logs_hot`，加 DO-NOT-UNDO 注释。`explain` 已验证通过 |
| `canonical_id` 永远 NULL 导致 `task_model_affinity` NOT NULL 违反 | CRITICAL-2 Fix A | settle worker 用 `LEFT(rl.tenant_id,64)` + `rl.canonical_id` 回填，UPDATE 用 `COALESCE(canonical_id, $6)` 保留决策时值 |
| 聚合查询带 NULL 触发扫描失败并通过 `continue` 静默截断 | CRITICAL-2 Fix C | 三处 worker 的 `continue` 改为返回错误（pgx v5 扫描失败会终止迭代，必须显式传播） |
| 聚合查询缺少 `canonical_id IS NOT NULL` 守卫 | CRITICAL-2 Fix B | 在 WHERE 中加入 |
| `sample_count = prevSamples + a.sampleCount` 每 15 分钟累加破坏收缩 | HIGH-1 | 改为 `= a.sampleCount`。EMA 仍保留历史信号 |
| `recommend_v2.go` 读 `idx.affinityStore` 不在锁内（`-race` 报警） | HIGH-3 | 移到现有 `RLock` 快照块 |
| `settle/affinity` worker 的 `Stop()` 在未 `Start` 时死锁 | MEDIUM-4 | 套用 `affinity_store_refresher` 的 `stopOnce` + `started.Load()` 模式 |
| `Explore` 标志永远 false，P3 验收对比组无法观察 | MEDIUM-6 | `ScoringBreakdown.Explore`，`recordAutoSelection` 直接从 winner 取 |
| shadow 模式下 `tenantID` 不解析，记录的是平台级而非租户级亲和 | LOW | `tenantID` 在 `mode != off` 时解析 |
| `mode=off` 仍写入 `affinity_score=50` | LOW | shadow 分支按 `mode != off` 守卫 |
| `affinityMinRewardN = 1` 是恒真死常量 | LOW | 移除 |
| LATERAL 的 `IS NOT DISTINCT FROM` 对 NULL 误匹配 | MEDIUM-1 | 改为 `= ... AND s.canonical_id IS NOT NULL` |
| `request_logs_hot(gw_session_id)` 缺索引，LATERAL 全扫 | MEDIUM-2 | migration 478p2 |
| `request_logs_hot(task_type, is_auto_request)` 缺索引，baseline 全扫 | MEDIUM-3 | migration 478p2 |
| `auto_route_selections` 没有 2026_10 分区（默认路由下被 DEFAULT 吞掉） | HIGH-2 | 在 migration 478 内置 `2026_10` 分区（永久幂已），本地+252 已落地 |

## 仍未做（明示范围外）

- **D5（模型级 failover）**：当冠军模型本身不健康时改选候选 #2。当前 `Domains/streaming/executors/` 只在同一模型的不同 credential 之间 failover；要消费 `Decision.CandidatesTopN` 需要跨 executor / decider 边界的接口改造。这是一次功能性变更，不是审计修复。
- **D6（`session_summaries.quality_score` 无写入方）**：0–10 的列与 `health_score` 0–100 是不同尺度但尚未规范。`health_score` 已由 `bg/session_health_worker.go` 写入并被 settle worker 用作 routing-only 子分；`quality_score` 需另立规格再实现。

## 部署后行为

默认 `AUTO_AFFINITY_MODE=shadow`：记录、结算、学习全部运行，但 **affinity 不改变路由**。观察 ≥7 天，查 `v_task_model_ranking` 确认排名与成功率同向后，设 `AUTO_AFFINITY_MODE=on` 放开。回滚设 `off`。

**健康检查指标（必须在观察期监控）**：
- `llmgw_autoroute_settled_total{outcome="rewarded"}` 必须离开零，否则结算 worker 还在失败
- `llmgw_autoroute_affinity_upserts_total` 必须离开零，否则学习 worker 还在失败
- `llmgw_auto_selections_dropped_total` 应该接近 0（队列 4096 + 50/批 + 200ms flush，应该够用）
- `llm_gateway_auto_selections_total{explore="true"}` 与 `explore="false"` 的比例应与 `AUTO_AFFINITY_EXPLORE_RATIO` 接近
| 异步匹配记录 | `domains/hooks/observability/telemetry/selection_writer.go` | ✅ 8 单测，非阻塞 |
| 结算 worker | `bg/auto_route_settle_worker.go` | ✅ SQL 已在 scratch PG 冒烟 |
| 学习 worker | `bg/auto_route_affinity_worker.go` | ✅ SQL 已在 scratch PG 冒烟 |
| 存储刷新 worker | `bg/affinity_store_refresher.go` | ✅ |
| 决策路径接线 | `autoroute/index.go`、`recommend_v2.go`、`decision_v2.go` | ✅ E2E 单测验证 affinity 真实改变排序 |
| 请求级记录 | `domains/streaming/auto_route.go::recordAutoSelection` | ✅ 每次 auto 决策写一行 |
| main.go 启动 | `cmd/gateway/main.go` | ✅ store + 3 个 worker 全部 flag-gated |
| 全量编译/单测 | — | ✅ `go build ./...` + `go vet` + 646 单测通过 |

**D2 修复（奖励基线）已实现并经单测验证**：`TestComputeRoutingReward_CohortBaselineDiscriminates` 确认用任务类型跨模型 p95 作基线能区分快慢模型，而原来用单模型自身 p95 会得到 ~中性。

**与原设计的两处偏差（已在代码中修正）**：

1. **retry 计数**。原设计意图是「同会话同模型的重试占比」，但第一版实现错误地把 `COUNT(*)-1` 当 retry（把正常多轮会话当重试）。已改用 `jsonb_array_length(routing_attempts)-1` —— `routing_attempts` 是 `request_logs` 里每条 upstream 尝试的真实记录，只在真正的 credential 级 failover 时才 >1。
2. **request_logs vs request_logs_hot**。原设计直接读 `request_logs`，但该库有冷热分离：2 分钟结算窗口内的行还在 `_hot` 里。已改为 `UNION ALL` 两者，否则所有刚结算的行都会被误判为「从未记录」而抛弃。

**仍未做（明示）**：
- D5（冠军模型不健康时改选候选 #2 的模型级 failover）—— 本轮范围外，需改 `domains/streaming/executors/` 的执行器。
- D6（`session_summaries.quality_score` 无写入方）—— 独立问题，本轮用 `health_score` 绕过。
- Admin API（`GET /api/admin/auto-route/affinity` 等只读端点）—— 数据已在 `v_task_model_ranking` 视图里可直接查，API 封装为后续工作。
- `task_model_affinity.avg_health` 目前恒为 0（聚合 SQL 占位），待 settle worker 把 health 写回 selections 后启用。

**部署后默认行为**：`AUTO_AFFINITY_MODE=shadow`（默认）。此时每次 auto 决策会记录匹配行、结算 reward、学习 affinity 并写入 `task_model_affinity`，但 **affinity 不改变任何路由选择**。观察 ≥7 天、确认 `v_task_model_ranking` 排名与实际成功率同向后，设 `AUTO_AFFINITY_MODE=on` 放开。一键回滚：`AUTO_AFFINITY_MODE=off`。

---

## 第一部分：现状总结（代码实测，非设计意图）

本节内容全部来自对当前代码的通读，标注了文件与行号。**与既有文档不一致处以代码为准**，不一致点单列在 §1.7。

### 1.1 请求入口：`model=auto` 如何被识别

魔法常量 `autoRequestMagic = "auto"`，定义在 `domains/streaming/auto_route.go:74`。四个入口：

| 端点 | 拦截点 | 开关 | 默认 |
|---|---|---|---|
| `/v1/chat/completions` | `handler.go:2516` → `maybeResolveAuto` (`auto_route.go:304`) | 无 | **常开** |
| `/v1/messages` | `messages.go:311` → `maybeResolveAutoForMessages` | `flags.AutoOnMessages` | 关 |
| `/v1/responses` | `responses.go:294` → `maybeResolveAutoForResponses` | `flags.AutoOnResponses` | 关 |
| `/v1/embeddings` | `embeddings.go:135` → `RecommendByModality`（不分类） | `flags.AutoOnEmbeddings` | 关 |

注意：精确等于 `"auto"` 才走 Decider；`auto/` **前缀**（如 `auto/free-tier`）属于 OmniFree 虚拟路由，在 `handler_autocombo.go:117-129` 被显式排除。

`maybeResolveAuto` 的动作：抽取信号 → 读 `X-Gw-Auto-Profile` / `X-Gw-Task-Hint` / `X-Gw-Session-Id` → `DecideWithFeatureFlags` → 用 `rewriteBodyWithModel` 改写 JSON body 的 `model` 字段。Decider 报错时**不静默兜底**，直接 502 `auto_route_decider_failed`（`handler.go:2522-2537`）。

### 1.2 任务分类（11 种）

`classifier.go:44-83` 定义 8 种，`task_types_ext.go:7-17` 追加 3 种：

```
chat, reasoning, code, agent, creative, long_context, vision, function_call,
code_audit, intent_classification, planning
```

输入指纹 `ClassificationSignals`（`classifier.go:98-140`）：SystemPrompt、MessageCount、EstimatedTokens、ToolCount、HasImages、LastUserPrompt、Language、HasCodeBlock、HasToolResults、ClientType。Token 估算约 4 字符/token，CJK 加权 2×（`auto_route.go:200-218`）。

`HeuristicClassifier.Classify`（`classifier.go:370-575`）是**有序优先级链**，命中即返回：

1. `HasImages` → vision，置信 0.95
2. 强编码信号（代码块 / IDE 指纹 / plan-mode 措辞）→ code，0.90 —— 故意排在 long_context **之前**
3. code_audit 0.85 / intent_classification 0.82 / planning 0.82
4. `EstimatedTokens > 50k` → long_context，0.85
5. `ToolCount>=3 && HasToolResults` → agent 0.85；`1..2` → function_call 0.80
6. 正则模式层（权重 0.55–0.70）
7. 关键词层（每命中 0.4，与模式层取 max）；`HasCodeBlock` 给 code 通道 +0.3
8. 兜底 `chat = 0.1`
9. `pickWinner`，固定优先级打破平局：reasoning > planning > code > agent > creative > function_call > vision > long_context > chat
10. 分数 ≥0.8 再 +0.05，上限 0.95

升级路径在 `Decider.classify`（`decision.go:502-539`）：`X-Gw-Task-Hint` 合法则直接 0.9 短路；否则置信度 < 0.7（可由 TuningStore 动态调整）时走 LLM 兜底分类；LLM 失败返回低置信启发式结果而非报错。

### 1.3 候选池：来自 DB，不是配置文件

`autoroute.Index` 是内存快照，由 `refreshIndexSQL`（`index.go:363-430`）重建，JOIN `credential_model_index`（每 credential×raw_model 的最近 5 分钟桶）+ `credentials` + `providers` + `provider_models` + `credential_model_bindings` + `models_canonical`。载入价格、success_rate、p95 延迟、active_sessions、并发上限、routing_tier、供应商类别、is_free、cost_tier、复杂度上下限、modality、tags。

刷新：每 5 分钟一次（`bg.NewAutoIndexRefresher`），叠加 `LISTEN/NOTIFY` 通道 `auto_route_refresh` 的亚秒级触发（`bg.NewAutoRouteRealtimeListener`）。

tier 与热度在 Go 侧派生（`index.go:500-528`），不落库：`routing_tier<=1` → primary，`<=3` → secondary，其余 fallback。

### 1.4 生产实际打分公式

`DecideWithFeatureFlags`（`feature_flags.go:209-215`）决定走哪条路。由于 `UseChannelQualityRouting` **默认 true**（`feature_flags.go:99`），生产实际运行 **`DecideV2` + `RecommendV2` + 4 维渠道质量打分**。

`ScoreWithChannelQuality`（`scoring_simplified.go:238-272`）：

```
composite = IntentMatch*0.4 + Price*0.2 + ChannelQuality*0.3 + Reliability*0.1 + Correction
```

- `IntentMatch = TaskMatchScore * 100`，`TaskMatchScore` = |required_tags ∩ tags| / |required_tags|（`scoring.go:370-397`），chat 固定 0.5
- `Price = clamp((1000 - avgCost)/10, 0, 100)`
- `ChannelQuality`（`scoring_simplified.go:84-105`）：按 `providers.category` 定基线（official 90 / self_host 80 / aggregator 60 / third_party_relay 50 / unknown 40），再叠加：success>0.95 且 p95<2000ms **+10**；success<0.80 **−20**；<0.60 再 **−30**；p95>5000ms **−15**；免费且 success<0.90 **−25**
- `Reliability`（`:146-169`）：`success_rate*80 + 延迟档位分`（20/12/6/0），冷启动固定 50
- `Correction` ∈ [−10,+10]（`:398-428`）：同会话上次该模型失败 −10，2s 内成功 +5，任务类型变化 0

`RecommendV2` 漏斗（`recommend_v2.go:16-180`）：

1. **活性硬过滤** `filterCurrentlyAvailable`：要求 `credential_model_bindings.available` 与 `provider_models.available` 均为 TRUE 且 `unavailable_reason NOT LIKE 'manual%'`；DB 出错时降级用最多 5 分钟陈旧的快照
2. 逐候选算 `TaskMatchScore`
3. **热池播种** `getHotTop3Canonicals`：48h 内成功请求数 Top3 canonical，2 分钟 TTL
4. 全池打分
5. **分层** `stratifyAndPickTopN`：以 `ChannelQuality>=50` 切分；优选层够数则只用优选层；否则 fallback 层 `composite *= 0.5`（优选层全部饱和时放宽到 0.85）
6. 冠军 `MatchScore < 30` 则丢弃，取 48h 兜底

覆盖层：`OverrideStore.FilterBanned` 先过滤封禁，再 `PromotePins` 强制置顶（`decision_v2.go:105-110`）。

### 1.5 已有的统计与反馈设施

**已经存在且相当完整，只是不自动生效。**

`tuning_signals` 表（`deploy/sql/objects/tables/tuning_signals.sql`）按请求记录：`request_id`、`session_id`、`task_type`、`classifier`、`confidence`、`chosen_model`、`canonical_id`、`success_score`、`latency_score`、`cost_score`、`drift_flag`、`quality_score`、`latency_ms`、`cost_usd`、tokens、`strategy`。物化视图 `tuning_signals_5m` / `tuning_signals_daily`。

写入：`emitTuningSignal`（`handler.go:7142`，唯一调用点 `handler.go:5017`）→ 异步批量写（队列 4096 / 批 50 / 200ms flush），不阻塞热路径。

日分析器 `bg/feedback_analyzer.go` 每日 02:00 UTC 跑 7 天窗口，产出 `tuning_proposals`（`status='pending'`），**明确不自动应用**；人工审批后落 `tuning_params`，由 `TuningStore` 每 5 分钟热加载。

**会话级多指标评分已经有了**：`session_summaries.health_score`（0-100）+ `health_grade`（A-F），由 `admin.ComputeHealth`（`admin/session_health.go:65-190`）计算，扣分项 8 类：error_ended −30、abandoned −15、per_error −3/个（封顶 30）、compliance −10/个（封顶 30）、high_latency −15、frequent_model_switch −10、prompt_injection −20、PII+toxic（封顶 30）。由 `bg/session_health_worker.go` 每 60 分钟扫描 `last_request_at < now-1h AND health_score IS NULL` 补算。

### 1.6 关键连接键

`request_logs` 上同时有 `gw_session_id`、`gw_task_id`、`is_auto_request`、`task_type`、`auto_profile`、`auto_decision`(jsonb)、`auto_confidence`、`task_type_chosen`、`model_chosen`、`canonical_id`、`quality_score`、`success`、`latency_ms`、`cost_usd`。`session_summaries` 以 `session_key` 为主键。二者通过 `request_logs.gw_session_id = session_summaries.session_key` 关联。

### 1.7 现状缺陷（本方案要解决的）

| # | 问题 | 证据 | 影响 |
|---|---|---|---|
| **D1** | **V6 最佳匹配矩阵是死数据**。`task_default_routing` 由 `477_auto_route_v6_defaults.sql` 权威播种 33+11 行，但 `DefaultRoutingStore.Resolve` 只在 v1 `Decide` 路径、且 `UseExplicitDefault=true` 时才被调用；该 flag 默认 **false**，而 `DecideV2` **从不调用** `Resolve` | `decision.go:292`、`feature_flags.go:126`、`decision_v2.go` 全文 | 人工维护的任务→模型表对线上零影响 |
| **D2** | **反馈信号被压平**。调用点硬编码 `drift=false` 且两个 baseline 传 0，导致 `quality = 0.4*success + 0.3*0.5 + 0.2*0.5 + 0.1*1.0 = 0.35 + 0.4*success`，只剩成败二值 | `handler.go:7193-7195` | 延迟/成本/漂移三个维度全部失效 |
| **D3** | **闭环断在人工审批**。分析器只产出 proposal，无任何自动应用路径 | `bg/feedback_analyzer.go:17-18` | 无法「自动优化」 |
| **D4** | **会话评分未参与路由**。`health_score` 算了、存了、只在 admin 展示 | grep 无路由侧读取 | 最强的多指标信号被浪费 |
| **D5** | `CandidatesTopN` 每次都算并落库，但从不用于重试；冠军模型本身不健康时不会改选候选 #2 | 仅 `decisionToWire` 与 `feedback_analyzer.go:280` 读取 | 无模型级 failover |
| **D6** | `session_summaries.quality_score`（0-10）被 5+ 处读取，但**全库无写入方** | grep 无 INSERT/UPDATE | 永久 NULL，读到的都是空 |

---

## 第二部分：优化方案

### 2.0 设计原则

1. **只记 ID，不记内容**。按需求，统计表只存 `request_id` / `session_id` / `canonical_id` 等标识与数值指标，不存 prompt、不存会话明细。
2. **先影子、后生效**。所有新打分维度默认 shadow（只记录不影响选择），由 flag 分级放开。
3. **不推翻现有公式**。affinity 作为**第 5 维**接入既有 4 维公式，而非替换。
4. **可解释**。每次决策为何选它、affinity 贡献多少，都要能从库里查出来。
5. **抗小样本**。样本不足时必须退化为中性，绝不能让 1 次成功把某模型顶上榜首。

### 2.1 数据模型（migration `478_auto_route_affinity.sql`）

#### 表 1：`auto_route_selections` —— 每次自动匹配一行

```sql
CREATE TABLE public.auto_route_selections (
    id              bigserial,
    request_id      text        NOT NULL,
    session_id      text,                       -- 只存 ID
    task_id         text,                       -- 只存 ID
    tenant_id       varchar(64),
    ts              timestamptz NOT NULL DEFAULT now(),

    -- 决策快照
    task_type       text        NOT NULL,
    profile         text        NOT NULL DEFAULT 'smart',
    classifier      text        NOT NULL,       -- heuristic/llm/hint
    confidence      numeric(4,3),
    canonical_id    integer,
    chosen_model    text        NOT NULL,
    candidate_rank  smallint    NOT NULL DEFAULT 1,
    composite_score numeric(6,2),
    affinity_score  numeric(6,2),               -- 本次 affinity 贡献（shadow 期也记）
    affinity_applied boolean    NOT NULL DEFAULT false,
    explore         boolean     NOT NULL DEFAULT false,  -- 是否探索流量
    fallback_used   boolean     NOT NULL DEFAULT false,

    -- 结果回填（由 §2.3 回填）
    success         boolean,
    latency_ms      integer,
    cost_usd        numeric(14,8),
    reward          numeric(4,3),               -- 0-1，见 §2.2
    reward_source   text,                       -- request / session
    settled_at      timestamptz,

    partition_date  date        NOT NULL DEFAULT CURRENT_DATE,
    PRIMARY KEY (id, partition_date),
    UNIQUE (request_id, partition_date)
) PARTITION BY RANGE (partition_date);
```

`UNIQUE(request_id)` 保证重放幂等。按 `partition_date` 分区，复用仓库既有的 `ensure_partition_*` 函数与列存归档惯例（7 天后转列存，参照 `credential_model_index`）。

索引：`(task_type, profile, ts DESC)`、`(session_id, partition_date)`、`(canonical_id, ts DESC)`、`(settled_at) WHERE settled_at IS NULL`（回填扫描用）。

#### 表 2：`task_model_affinity` —— 学习出来的「任务→最佳模型」列表

```sql
CREATE TABLE public.task_model_affinity (
    task_type       text        NOT NULL,
    profile         text        NOT NULL DEFAULT '',
    canonical_id    integer     NOT NULL,
    canonical_model text        NOT NULL,
    tenant_id       varchar(64) NOT NULL DEFAULT '',   -- '' = 平台级

    sample_count    integer     NOT NULL DEFAULT 0,
    success_count   integer     NOT NULL DEFAULT 0,
    success_rate    numeric(5,4),
    avg_reward      numeric(4,3),
    ema_reward      numeric(4,3),               -- 指数移动平均，α=0.15
    avg_latency_ms  integer,
    avg_cost_usd    numeric(14,8),
    avg_health      numeric(5,2),               -- 会话 health_score 均值

    affinity        numeric(6,2) NOT NULL DEFAULT 50,  -- 0-100，收缩后的最终分
    rank            smallint,                   -- 该 (task,profile) 内排名
    confidence      numeric(4,3) NOT NULL DEFAULT 0,   -- 样本充分度 0-1

    first_seen_at   timestamptz NOT NULL DEFAULT now(),
    last_sampled_at timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (task_type, profile, canonical_id, tenant_id)
);
```

这张表就是**需求里的「最佳任务匹配列表」**，由 §2.3 的 worker 自动重算，可通过 admin API 只读查看 + 人工封禁。它与人工维护的 `task_default_routing` 并存：**人工表是硬约束（pin/ban），affinity 表是软权重**。

#### 视图：`v_task_model_ranking`

给 admin 与调试用，把 affinity 表按 `(task_type, profile)` 排序输出 Top-N，JOIN 出模型可读名与当前可用性。

### 2.2 奖励函数（reward）——本方案的核心

**不能直接用 `health_score` 当 reward。** 它包含与模型选择无关的维度：`abandoned`（单轮会话在 API 场景是常态）、`prompt_injection` / `PII` / `toxic`（由用户输入决定）、`compliance`。把这些算进模型的账上会系统性误判。

因此定义**路由归因奖励**，只取模型能负责的维度：

```
reward = 0.45 * success
       + 0.20 * latency_score
       + 0.15 * cost_score
       + 0.10 * health_component
       + 0.10 * (1 - retry_ratio)
```

- `success`：1.0 成功 / 0.0 失败 / 0.5 流中断
- `latency_score = 1 - clamp(latency_ms / p95_baseline(task_type), 0, 1)`，baseline 取 `model_task_index` 的 p95；**必须真实传入，修掉 D2**
- `cost_score = 1 - clamp(cost / p75_baseline(task_type), 0, 1)`
- `health_component`：会话已结算时取 `health_score/100` 但**只计入 error/latency/model_switch 三类扣分**（重算一个 routing-only 子分，见下）；未结算取 0.5 中性
- `retry_ratio`：同 `session_id` 下针对同一 `canonical_id` 的重试占比

`health_component` 的 routing-only 子分：复用 `admin.ComputeHealth` 的入参，但把 `PerCompliancePenalty` / `PromptInjectionPenalty` / `PIIPenalty` / `ToxicOutputPenalty` / `AbandonedPenalty` 置 0，只保留 `error_ended` / `per_error` / `high_latency` / `frequent_model_switch`。实现为 `admin.RoutingHealthConfig()`，与展示用配置分离，互不影响。

**归因规则**：一个会话可能跨多个模型。只有当某 `canonical_id` 承担了该会话 **≥80%** 的请求时，才把会话级 `health_component` 记到它头上（`reward_source='session'`）；否则只用请求级信号（`reward_source='request'`）。这避免把别人的锅扣到自己身上。

### 2.3 统计与聚合链路

```
请求完成
  └─ emitTuningSignal（已存在，修 D2：传真实 baseline + drift）
  └─ recordAutoSelection（新增）→ 异步写 auto_route_selections
                                   （复用 tuning_signal_writer 的批量写模式）

每 5 分钟：AutoRouteSettleWorker
  └─ 扫 settled_at IS NULL 且 ts < now()-2min 的行
     ├─ 从 request_logs 回填 success/latency_ms/cost_usd
     ├─ 会话已结算（session_summaries.health_score IS NOT NULL）→ 算 routing-only health
     ├─ 计算 reward，写回
     └─ settled_at = now()

每 15 分钟：TaskModelAffinityWorker
  └─ 按 (task_type, profile, canonical_id[, tenant_id]) 聚合近 14 天已结算行
     ├─ sample_count / success_rate / avg_reward / avg_latency / avg_cost
     ├─ ema_reward = α*本窗口 avg_reward + (1-α)*旧值，α=0.15
     ├─ 贝叶斯收缩：
     │    w = n / (n + K)，K=30
     │    shrunk = w * ema_reward + (1-w) * 0.5
     │    affinity = shrunk * 100
     ├─ confidence = w
     ├─ 陈旧衰减：last_sampled_at 超 7 天，affinity 每天向 50 回归 5%
     └─ 重算 rank
```

`K=30` 的含义：某模型跑满 30 次该任务，affinity 才有 50% 权重来自实测；跑 3 次只有 9%。这是抗小样本的关键。

### 2.4 反哺路由：affinity 作为第 5 维

改 `ScoreWithChannelQuality`，权重整体缩放 0.85 后腾出 0.15：

```
composite = IntentMatch*0.34 + Price*0.17 + ChannelQuality*0.255
          + Reliability*0.085 + Affinity*0.15 + Correction
```

（0.4/0.2/0.3/0.1 各乘 0.85，相对比例完全不变，只是让出 15% 给 affinity。）

护栏，缺一不可：

1. **样本门槛**：`sample_count < 10` 时 `Affinity = 50`（中性），不参与区分
2. **幅度限制**：affinity 相对中性的偏移量裁剪到 ±40，即 `Affinity ∈ [10, 90]`，防止单一维度独裁
3. **不能凭 affinity 复活**：affinity 高但活性过滤没过、或被 `OverrideStore` 封禁的候选，一律排除。affinity 只在**已通过硬约束**的候选间排序
4. **探索流量**：按 `request_id` 哈希取 10%（`AUTO_AFFINITY_EXPLORE_RATIO`，默认 0.10）跳过 affinity 加权，用原 4 维打分，并标记 `explore=true`。没有探索，排名会锁死在先发优势上，新模型永远拿不到样本
5. **分级放开**：`AUTO_AFFINITY_MODE` = `off` | `shadow` | `on`，默认 **`shadow`**（算 + 记 `affinity_score`，但不进 composite）

同时修 **D1**：把 `DefaultRoutingStore.Resolve` 接入 `DecideV2`。人工矩阵的语义明确为「promote 到候选首位」，优先级高于 affinity —— 人工判断始终能压过统计。

### 2.5 与既有系统的关系

| 既有 | 处置 |
|---|---|
| `tuning_signals` | 保留，修 D2。它是请求级明细，affinity 是聚合结论，互补不重叠 |
| `tuning_proposals` | 保留人工审批语义，用于**分类器关键词与权重**调整；affinity 走自动路径，二者互不干扰 |
| `task_default_routing`（V6 矩阵） | 从死数据变为**硬 promote 层**，优先级 > affinity |
| `session_summaries.health_score` | 保留展示语义不变；新增 routing-only 子分供路由使用 |
| `routing_overrides`（pin/ban） | 不变，仍是最高优先级 |
| `work_type_model_route` | 本次不动。它是另一套 L2 taxonomy，`X-Gw-Work-Type` 目前仅落日志 |

优先级总序：`ban` > `pin` > `task_default_routing` promote > affinity 加权 > 基础 4 维打分。

### 2.6 可观测性

Prometheus：

- `llmgw_autoroute_affinity_applied_total{task_type,mode}`
- `llmgw_autoroute_affinity_score`（histogram）
- `llmgw_autoroute_explore_total`
- `llmgw_autoroute_reward`（histogram，按 task_type）
- `llmgw_autoroute_settle_lag_seconds`
- `llmgw_autoroute_affinity_rank_churn`（排名变动幅度，突增说明不稳定）

Admin API（只读 + 有限干预）：

- `GET /api/admin/auto-route/affinity?task_type=&profile=` —— 当前学习出的排名表
- `GET /api/admin/auto-route/affinity/history?canonical_id=` —— affinity 时序
- `GET /api/admin/auto-route/selections?session_id=` —— 某会话的匹配轨迹
- `POST /api/admin/auto-route/affinity/reset` —— 重置某 (task,profile) 的学习结果

### 2.7 失效与回滚

| 场景 | 行为 |
|---|---|
| affinity 表为空 / 全部低样本 | 全部返回 50，退化为现有 4 维公式 |
| affinity 查询超时 | 用内存快照；快照也没有则中性 50 |
| 学习结果异常（某模型 affinity 冲高但 success_rate 下滑） | `affinity_rank_churn` 告警；`AUTO_AFFINITY_MODE=off` 一键关闭 |
| worker 挂掉 | 陈旧衰减保证 affinity 逐步回归中性，不会长期钉在错误结论上 |
| 需要完全回滚 | `AUTO_AFFINITY_MODE=off` + 停两个 worker；表可保留，无需回滚 migration |

### 2.8 实施顺序

| 阶段 | 内容 | 风险 |
|---|---|---|
| P0 | migration 478；`recordAutoSelection` 写入；修 D2 的 baseline | 低（纯新增 + 修数据质量） |
| P1 | SettleWorker + AffinityWorker；`AUTO_AFFINITY_MODE=shadow` | 低（不影响选择） |
| P2 | Admin API + 视图；观察 ≥7 天，校验 affinity 排名与实际成功率同向 | 无 |
| P3 | 修 D1（接入 `task_default_routing`）；affinity 转 `on` + 10% 探索 | 中（改变选择行为，需灰度） |
| P4 | 视情况处理 D5（模型级 failover）、D6（`quality_score` 写入方或废弃该列） | 中 |

**验收标准**：P3 放开后 7 天内，`auto_route_selections` 中 affinity 生效组的平均 reward 显著高于探索组（同任务类型对比），且 `p95` 延迟与成本无恶化。若不满足，回退到 shadow 并重审奖励函数权重。

---

## 附录：未决问题

1. **多租户粒度**。`task_model_affinity.tenant_id` 已预留，但平台级与租户级如何混合（租户样本不足时回退平台级）需定策略。倾向：租户 `sample_count >= 30` 才用租户级，否则用平台级。
2. **任务分类本身的错误**会污染 affinity —— 若请求被误分类为 code，则 code 任务的 affinity 学到的是错的。缓解手段是 `confidence` 低于阈值的样本降权（乘 `confidence`）计入，需在 P1 实现时确认。
3. **冷启动**新模型无样本，affinity=50 中性，靠 10% 探索流量积累。若上新频繁，可能需要给新模型一段时间的探索加成，暂不做。
