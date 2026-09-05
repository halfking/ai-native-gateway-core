# 轴H（round2）：auto 模型全量实现审计

## 一、链路闭环图（文字版）

```
客户端 model="auto"
  ├─ /v1/chat/completions  handler.go:2691→2696 → maybeResolveAuto (auto_route.go:326)
  ├─ /v1/messages          messages.go:317→322 → maybeResolveAutoForMessages (auto_route_nonchat.go:123, 受 AutoOnMessages 门)
  ├─ /v1/responses         responses.go:302→304 → maybeResolveAutoForResponses (auto_route_nonchat.go:165, 受 AutoOnResponses 门)
  └─ /v1/embeddings        embeddings.go:135 (RecommendByModality, 轻量直解析, 不进 selection/反馈闭环)
        │
        ▼
autoroute.Decider.DecideWithFeatureFlags (feature_flags.go:320)
  → [V2] DecideV2 (decision_v2.go:15)：session intent cache 重校验 → profile → classify
      (heuristic+tuning；低置信→LLM fallback [默认 DisabledCaller，需 LLMGatewayAutoLLMEndpoint]；embedding shadow 1%)
  → Index.RecommendV2WithHints (recommend_v2.go:48)：硬可用性过滤(DB 实时) → RT-1 IQ 门 → hot-top3 收窄
      → 评分 ScoreWithChannelQuality / ScoreWithAffinity(5 维, shadow 默认, explore 按 requestID hash 10%)
      → popularity 权重(默认 off) → 排序 → preferred/fallback 分层降权(demotion 0.5/0.85) → topN → tie-break(composite→reliability→price)
  → override ban/tier/pin → ErrNoCandidates → 502
        │
        ▼
改写 body.model → 上游调用（原 candidate/executor 链路不变）
  + X-Gw-Auto-Decision 响应头（reason+candidates_top3+filter_reasons, 16KiB cap）
  + logCtx.SetAutoDecision → request_logs.auto_decision / is_auto_request / task_type / fallback 序列
        │
        ▼
recordAutoSelection → telemetry.WriteAutoSelection（异步批 50/200ms，队列 4096，丢弃计数）
  → INSERT auto_route_selections_hot（ON CONFLICT DO NOTHING，幂等）   [selection_writer.go:261]
        │  (2min settleDelay 后)
AutoRouteSettleWorker（5min 周期，批 500，4h abandon，panic guard）
  → 只读 request_logs_hot join（hot-only，D-#1 修复）→ reward=ComputeRoutingReward(成功.45/延迟.20/成本.15/健康.10/重试.10)
  → UPDATE auto_route_selections_hot SET reward,settled_at（settled_at IS NULL 守卫，幂等）
        │  (8h promote，每小时)
promote_auto_route_selections_hot_to_partition（656：FOR UPDATE SKIP LOCKED → DELETE hot → INSERT 父表月分区，advisory lock 单实例）
  ← PartitionManager promoteSpecs()[partition_manager.go:885] + ensureSpecs()[partition_manager.go:816]
        │  (15min 周期，学习窗 14d)
AutoRouteAffinityWorker（panic guard）
  → aggregate FROM auto_route_selections_all（父表为普通 heap 分区[478]，UNION ALL view 无 columnar perminfoindex 风险）
  → EMA(0.15)+ShrinkAffinity(w=n/(n+30))+MinSamples(10)+Decay(5%/天)+rank 重排 → task_model_affinity
        │
        ▼
AffinityStore.Lookup（热路径）→ ScoreWithAffinity → 回到 DecideV2（学习闭环闭合）
```

**闭环结论**：full(PG) 模式下"选型→selection→settle→reward→affinity→再选型"闭环完整无断点；失败路径透明（decider 失败 502 `auto_route_decider_failed`、无候选 `ErrNoCandidates`、decider 未装配降级 fallback 模型）。断点风险集中在配置/部署层面（H-2/H-3/H-4）与 /v1/messages、/v1/responses 的 flag-off 分支（H-1）。

## 二、发现列表

### H-1 [P1] /v1/messages、/v1/responses 的 model=auto 在 feature flag 关闭（默认值）时请求体被置空
- 证据：`autoroute/feature_flags.go:138-139`（AutoOnMessages/AutoOnResponses 默认 false）→ `auto_route_nonchat.go:128-130/169-171` 返回 `(nil, nil, false)` → `domains/streaming/messages.go:334` 与 `domains/streaming/responses.go:313` 无条件 `bodyBytes = newBody` → body=nil、model 仍为 "auto"、`logCtx.IsAutoRequest=true`，最终以误导性的候选解析失败告终，且 request_logs 里记为 body 为空的 auto 请求。chat 路径有 nil 守卫（`handler.go:2714`），两非 chat 路径漏掉。
- 影响：flag-off 配置下（默认即 off）该两个端点的 auto 请求行为损坏 + 遥测污染。
- 最小修复：与 handler.go 一致加 `if newBody != nil` 守卫；flag-off 时直接 503/400 "auto not enabled on this endpoint"。工作量 S。

### H-2 [P2] （D-#3 核实为真）656 无网关侧 ensure，hot 表存在性完全依赖 installer 迁移
- 证据：`db/db.go` ApplyMigrations 为 655/631/632 等均有 Go 侧 ensure（`db/db.go:142-147` ensureSessionSummariesCanonical 等），但 grep 全文 auto_route 仅 3109-3116 触发器，无 auto_route_selections_hot ensure；建表仅存在于 `sql/migrations/startup/656_auto_route_selections_hot.sql:7` 与 installer embed（`installer/internal/dbinit/runner.go:97`）。
- 影响：未重跑 installer 的环境，selection writer 每批 flush 失败丢弃（`selection_writer.go:190-195`，有 WARN+dropped 指标），settle/affinity 每 sweep 失败——学习闭环整体静默失效（不崩溃、有日志可见）。
- 最小修复：ApplyMigrations 增加 ensureAutoRouteSelectionsHot（CREATE TABLE IF NOT EXISTS + 两个函数 + view，幂等）。工作量 M。

### H-3 [P2] "4h abandon < 8h hot retention" 约束只靠默认值成立，无强制耦合
- 证据：`bg/auto_route_settle_worker.go:53-59`（settleAbandonAfter=4h，注释自称 MUST 仍在 retention 内）；`bg/partition_manager.go:1127-1131`：`lifecycle.hot_retention_hours` 走 default 分支，安全下限仅 1h。
- 影响：运维把 retention 配成 1-4h 时，promote 会在 settle/abandon 完成前搬走未结算行，settle worker hot-only 再也触达不了 → 父表永久 unsettled、reward 丢失。
- 最小修复：resolvePromoteConfig 对 auto_route_selections_hot 单列分支施加 ≥5h 下限（settleDelay+settleAbandonAfter 余量），或直接改 promote SQL 条件为"仅搬终态行"（见 H-4）。工作量 S。

### H-4 [P2] promote 不区分 settled/unsettled，settle worker 积压 >8h 时未结算行永久滞留父表
- 证据：`sql/migrations/startup/656_auto_route_selections_hot.sql:121-123`（batch 仅按 `ts < now-retention` 过滤，无 settled_at 条件）；全库无任何针对父表 unsettled 行的回填/对账 job（grep settle 相关 UPDATE 仅指向 _hot）。
- 影响：worker 宕机/积压超过一个 promote 周期后，这批行永远无 reward（affinity 聚合要求 reward IS NOT NULL → 学习样本丢失），`auto_route_selections_all` 视角可见永久 unsettled 脏数据。
- 最小修复：promote 条件改为 `settled_at IS NOT NULL OR ts < NOW() - INTERVAL '7 days'`（7d 兜底防 hot 无限膨胀）+ unsettled 滞留告警。工作量 S。

### H-5 [P2] estimateTokens 对 CJK 系统性低估 3-4 倍，long_context 特征失真
- 证据：`domains/streaming/auto_route.go:222-240`：CJK rune 计 2、ASCII 每字节计 1，最后统一 `/4` → 每个汉字 ≈0.5 token，与同函数注释声称的"1 CJK char ≈ 1.5-2 tokens"矛盾（/4 只该作用于 ASCII 字节计数）。
- 影响：中文/混合请求 EstimatedTokens 低估 → `effectiveLongContextTokens` 门限几乎永远不触发 → long_context 路由类别对 CJK 失效（路由质量缺陷，非崩溃）。
- 最小修复：CJK 计数不参与 /4（如 ascii/4 + cjk*1.5）。工作量 S。

### H-6 [P3] autoclass-bench 评测 prompt 与生产 LLM 分类器 prompt 漂移
- 证据：bench `cmd/autoclass-bench/main.go:325-330`（buildPrompt：纯标签枚举，labels 由样本/flag 派生；种子样本含生产不存在的 "planning" 标签）vs 生产 `autoroute/classifier_llm.go:98-110`（buildClassificationPrompt：固定 10 类带定义）。工具可构建（go build 通过）、有 45 条种子样本（testdata/samples.seed.jsonl）、明确声明只作冒烟不作放量依据（testdata/README.md）。
- 影响：基准通过 ≠ 生产 fallback 行为好；属于守门工具漂移而非生产缺陷。
- 修复：bench prompt 模板改为复用/对齐 buildClassificationPrompt。S。

### H-7 [P3] chat 路径 selection 记录发生在 session 解析前，session 归因常为 NULL
- 证据：`handler.go:2696`（记录在 gateway session assignment 之前）→ `auto_route.go:346-349` 只取请求头 X-Gw-Session-Id/X-Session-Id；settle 的 session health 归因依赖 `s.session_id = rl.gw_session_id` join（`bg/auto_route_settle_worker.go:339-347`）。messages/responses 路径在 session 解析后记录（`messages.go:447-449`、`responses.go:427`）行为更优。
- 影响：不带 session 头的 chat auto 请求拿不到 session health 分量（reward 仍有请求级 4 信号，损失 0.10 权重的归因）。
- 修复：chat 路径同样延后到 session 解析后 recordAutoSelectionFromWire。M。

### H-8 [P3] 请求路径热查询直打 request_logs 分区父表
- 证据：`autoroute/recommend_v2.go:324-335`（loadCorrectionScores 每个带 session 的 auto 请求查一次父表）、`recommend_v2.go:475-484`（getHotTop3 每 2min 查父表 48h）。功能正确（普通 SELECT 不触发 settle 注释所述 columnar UNION ALL 计划错误），但父表扫描随月分区数线性增长。
- 修复：改查 request_logs_hot（2min/校正窗都在 hot 覆盖内）。S。

### H-9 [P3] "auto" 大小写语义不一致
- 证据：dispatch pipeline 接受大小写变体（`domains/dispatch/pipeline.go:1135-1142` isAutoModel），streaming 入口只匹配精确小写 `"auto"`（`auto_route.go:75`、`auto_route_nonchat.go:124/166`）。
- 修复：入口统一用 isAutoModel 或文档声明仅小写。S。

### H-10 [P3] 文档口径：02 称"路由事实只写入现有 auto_route_selections"，实际 writer 只写 _hot 再 promote
- 证据：`docs/auto-model-optimization/02-architecture-design.md`（数据流图）vs `selection_writer.go:261`。语义等价（父表是最终归宿）但措辞过时，易误导新读者。

## 三、文档 vs 实现差距表

| 文档声称（auto-model-optimization/） | 实现 | 结论 |
|---|---|---|
| 02/05：8h 热窗 + 月分区 + auto_route_selections_all 统一视图 | 656 迁移完整实现（hot 表/ensure/promote/view），partition_manager 已接入 ensure+promote | 一致（已实现） |
| 02：路由事实只写入 auto_route_selections | writer 写 _hot，promote 到父表 | 措辞过时（H-10，P3） |
| 02/05：结构化特征列（语言枚举/长度桶/特征版本/hash）落事实表 | auto_route_selections(_hot) 无任何特征列；signals 明确 process-local 不落库（auto_route.go:109-113） | 未实现；roadmap 阶段一未勾选，README 已声明"不代表已实现" |
| 02：结构化特征 ML 模型（08/09 ONNX） | 生产分类器 = heuristic(+tuning) + LLM fallback（默认 DisabledCaller，需 LLMGatewayAutoLLMEndpoint）+ embedding shadow(1%) | 未实现（设计态，已声明） |
| 06/10：免费 LLM 动态基准 + shadow 池 | 仅 autoclass-bench 守门工具（BUILD_OK）；无 runtime 免费池 shadow 接入（OmniFree auto/* 为另一套已存在机制） | 部分实现（工具层） |
| 07：人工标注 workflow | 未发现对应 admin/autoroute 实现 | 未实现（设计态） |
| 05：默认分区写入保护 | 656 ensure 函数含 default 分区 DETACH/搬移/重挂载 | 一致（已实现） |

## 四、已确认闭环（无需整改）

- 本轮提交复核：`auto_route_settle_worker.go` hot-only 修复（文件头注释 D-#1 + 三处查询/写入全部 `_hot`，settleAbandonAfter 24h→4h）到位；两个 worker 与 selection_writer 的 panic guard（G-#1）+ Start/Stop 幂等（stopOnce/started、Stop-without-Start 不死锁，`bg/auto_route_workers_test.go` 覆盖）到位。
- 656 迁移行为验证脚本 `sql/tests/auto_route_selections_hot_tests.sql`（9 项检查、单事务 ROLLBACK、幂等）+ `migration_656_test.go` SQL 文本契约测试；布尔列默认值（2adc48e66）在 656:24-26 确认。
- settle 幂等：`writeReward/abandon` 双条件 `id+partition_date+settled_at IS NULL`；writer `ON CONFLICT DO NOTHING` 对齐 `uq_ars_hot_request`；水位：`idx_ars_hot_unsettled` 部分索引匹配 `settled_at IS NULL AND ts < now-2min ORDER BY ts LIMIT 500`。
- affinity 学习防自增：sample_count=本窗计数（CRITICAL/HIGH-1 修复注释）、EMA 承载历史、read-time MinSamples/钳制/衰减；explore 确定性 hash（fnv+salt）与 A/B 分桶去相关。
- 可观测性：X-Gw-Auto-Decision（reason/alternatives top3/filter_reasons/16KiB 截断保 winner）、request_logs.auto_decision+fallback 序列、admin `handleAffinitySelections`（`admin/auto_route.go:1112`，查 _all 视图）、llmgw_autoroute_settled/reward/lag/affinity_upserts/rank_churn 指标齐全。

## 五、冗余/待清理代码

- `bg/auto_route_affinity_worker.go:258-263`：prevAvgReward 扫出后未使用（查询可少取一列）。
- `domains/streaming/handler_autocombo.go:207/235`：`emptySet` 构建后仅 `_ = emptySet`，为死代码。
- `cmd/autoclass-bench/testdata/samples.seed.jsonl` 含 "planning" 标签，生产标签集不存在该类（与 H-6 一并清理）。
- `autoroute/decision.go:687` buildReason 标记 nolint:unused（遗留）。

## 统计

P0=0，P1=1，P2=4，P3=5（合计 10）。
