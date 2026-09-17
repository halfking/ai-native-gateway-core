# D11 auto 模型子代理报告（R35-R2 口径决策事实枚举；窗口 HEAD=67f78247c）

## 一、发现（候选，待主代理复核）

### 1. llmgw_autoroute_outcome_total 全部发射点

- 指标为 scrape 时投影的 5 个进程内原子计数器，全链无 origin_actor（GW-00 注释明示）。标签 result=stashed/matched/orphan/expired/dropped（autoroute/outcome_metrics.go:27,31,58-65）。
- stashed：stashPendingFeedback ← recordFeedbackAsync ← Decide 决策时（autoroute/outcome_feedback.go:138；optimizer_bridge.go:188-193）。goal 影子轮 body model=auto 必然产生 stash。
- expired：stash 容量/TTL 驱逐 ×2（outcome_feedback.go:137,184）。
- matched/orphan/dropped：ReportRoutingOutcome 内（outcome_feedback.go:232,238,255）。
- **P2 候选**：ReportRoutingOutcome 两个终态发射门槛均只查 reqLog.IsAutoRequest，无 origin_actor 过滤；插入点 handler.go:6196-6198 / request_log_pipeline.go:1131-1133（RequestLogEntry.OriginActor 存在）。

### 2. settle 结算 LATERAL

- mr 块 WHERE 仅 session/canonical 关联，无 origin 过滤；插入点 bg/auto_route_settle_worker.go:394-402（r2.origin_actor 可用）。
- **P2 候选**：loadTaskBaselines 只过滤 is_auto_request=TRUE，影子轮污染 cohort p95/p75 基线；插入点 :294-303。
- 口径注意：selection 行本身在 auto_route_selections_hot（无 origin_actor 列，656:9-21），如需"影子轮完全不 settle"只能按 request_id 反连接 request_logs_hot。
- affinity 回灌读 selections WHERE reward IS NOT NULL，完全继承 settle 口径（auto_route_affinity_worker.go:254-268）。

### 3. work_types / top_models

- **P2 候选**：handleStats 四条查询无 origin_actor 过滤，四处 WHERE 可写（admin/work_types.go:275-285, 311-320, 342-348, 403-415；注意 wtTenantWhere 拼接顺序）。
- 共享谓词 businessRequestFilter 只剔 probe 不含 goal-%（admin/analytics.go:165-179）。

### 4. matched/accuracy 计算链

- 灰度口径由 Prometheus 外部复算，本面=面 1 的发射门槛。
- **P2 候选**：tuning accuracy——影子轮过 emitTuningSignal 门（handler.go:6181 需 IsAutoRequest && TaskType!=nil）写 tuning_signals；5m/daily matview（db/db.go:2501-2544）无 origin 过滤。插入点：发射门或查询侧 join。
- **P2 候选**：admin audit KPI（routing_audit_summary_7d / routing_analytics_source）无 goal-% 过滤，且视图不投影 origin_actor（649:23-58），需先扩视图投影（涉迁移）。

### 5. session_turns 镜像与 orphan

- 剔除逻辑=mirror 钩子 !synthetic && IsInternalAutoEntry → return（hook.go:92-94；replay.go:293；telemetry internal_loopback.go:23-40）。
- goal 影子轮**有意不剔除**（打标不排除，过滤权留查询侧，doc 18 §74,79）。
- goal-% actor 链路完整可对账：followUpSourceActor → X-Gw-Source-Actor → logCtx.OriginActor → request_logs/session_turns 落列（response_interceptor_helpers.go:239-274；turn_writer.go:342,406；707:88,144）。
- orphan 三处计数当前自洽（G2 门与 TaskType 语义自洽）；改口径勿动 X-Gw-Is-Auto 语义。

### 6. 配置默认

- goal.use_autoroute_for_audit Default=true（tenant 域热更）；goal.fallback_audit_model Default="auto"（settings/goal_specs.go:232-267）。
- 进程级：env 缺省回落 preset（minimal=false, balanced/aggressive=true）；fallback 硬编码 "auto"（goal_control.go:217,235；cost_presets.go:85,115,146）。
- triggerAudit 强制 model=auto（mode_hook.go:778-798）；metadata.task_type_hint=code_audit 全仓无消费方（:793 一处写入）。

### 7. origin_actor 可用性

- 可用：request_logs 父表/月分区叶/request_logs_hot（baseline 7348/13034/13182/13544）；promote 全携带（602/688/695/697/698）；canonical 710 视图投影；session_turns(_hot) 707。
- 不可用：routing_analytics_source（649 只投影 origin_stage）；auto_route_selections_hot（656 无列）；tuning_signals（有 request_id 可 join）。
- "v_routable" 实为 v_routable_credential_models（凭据-模型视图），与请求日志视图族无关——R35 表述如指请求日志应改称 710 canonical / with_current_month。

## 二、各面当前口径汇总表

| 面 | 当前 goal-% 过滤 | 影子轮混入 | 可写过滤的列 |
|---|---|---|---|
| 1. llmgw_autoroute_outcome_total | 无 | 混入（stashed/matched 抬升） | 发射门 reqLog.OriginActor |
| 2. settle LATERAL + baselines | 无 | 混入 | r2/rl.origin_actor |
| 2'. selection/affinity 回灌 | 无 | 混入 | 仅 request_id 反连接 |
| 3. work_types/top_models | 无 | 混入 | origin_actor 四处 WHERE |
| 4a. matched/expired/accuracy 复算 | 同面 1 | 混入 | 同面 1 |
| 4b. tuning accuracy | 无 | 混入 | 发射门 logCtx.OriginActor |
| 4c. admin audit KPI | 无（仅 probe 口径） | 混入 | 需先扩 649 视图投影 |
| 5. session_turns 镜像 | 有意不过滤 | 有意保留 | 查询侧 session_turns.origin_actor |

健康面：影子轮 actor 链路完整可对账且与 OriginMiddleware 的 X-LLM-Origin-* 信任链互不干扰；IsInternalAutoEntry 单一判定源被 mirror/replay/final-success-claim 三处共享。

## 三、未覆盖项与原因

- 实库列位验证与实际混入量测算需真机凭据。
- R35 轮文档原文未逐字核对，以 D11 §4 历史回归点与代码事实为准。
- examples/auto_control_integration.go:131 默认 true 为示例进程非生产接线，未深查。
