# Request Flow 收敛审计报告（Step 1–5）

> 审计日期：2026-08-02
> 审计基线：origin/main @ `7c0aa183`（Step 5 round 1+2 已合并）
> 审计方式：只读代码审计 + 定向测试验证（internal/ir、streaming/executors、ursm/v2、telemetry、session、session/v2、streaming 全部 PASS）
> 关联 spec：docs/superpowers/specs/2026-07-27-request-flow-audit-design.md

## 审计结论

Step 1–5 的核心不变量在代码层**全部成立（PASS）**。存在 3 个非阻断 GAP（helper/gate 已实现但未生产接线），不影响发布，但应在后续 round 补齐以达到 spec §4.1/§4.2 的“单一原子门控”目标。Step 5 round 2（credentialstate 下沉 + authoritative 零调用集成验证）的发布阻断项（spec §13 C-1）当前由 `selectStateBackendWithReady` + `URSMv2Backend` no-op 保证，无 BLOCK。

## 逐 Step 审计

### Step 1 记录可靠性（commit `6cc82f38`）— PASS

| 不变量 | 结论 | 证据 |
|---|---|---|
| L-1 早期 CreateInitial 位置 | PASS | Chat `handler.go:1159`（session 派生 1131，Verify 1418）；Messages `messages.go:93`（identity 81，Verify 173）；Responses `responses.go:132`（identity 120，Verify 197）；Gemini `handler_gemini.go:144`（identity 138）。四入口均在 request_id/session 派生后、Verify 前。 |
| L-2 request_logs_hot 终态 guard | PASS | `telemetry/client.go:1012-1022` WHERE NOT (status='failure' OR (success/success AND NOT incoming-success))；`client.go:1474-1492` UPDATE 0 行后 SELECT EXISTS 区分终态 no-op 与 missing。 |
| WAL guard | PASS | `request_logger.go:630-655` WHERE (status IS NULL OR status='pending')。 |
| 队列满 fallback | PASS | `request_logger.go:372-376` select-default → recordOverflow（400-414）先写原始 LogUpdate，失败才 marker。 |
| flushBatch 整批 fallback | PASS | begin（511）、row（531-551）、commit（554）三种失败均 `fallbackUpdates(batch)` 整批。 |
| Stop 幂等 + drain | PASS | `request_logger.go:686-700` stopOnce + close(done) + wg.Wait；worker `457-497` done 分支 drain asyncQueue 后 flushBatch。 |
| overflow/replay stats | PASS | `request_logger.go:83-89` queueOverflow/fallbackWriteFailure/unrecoverableFallback/replayAttempt/Success/Failure/Marker 全部 atomic。 |
| 主 shutdown 调用 requestLogger.Stop | PASS | `cmd/gateway/main.go:4494-4496` requestLogger.Stop，4497 telemetryClient.Stop，4502-4505 sessionDBWriter.Stop。顺序符合 spec §5.2/§6.3。 |

### Step 2 统一入口与终态（commit `674fcf8c`）— PASS + 2 GAP

| 不变量 | 结论 | 证据 |
|---|---|---|
| InitializeRequestIdentity helper | **GAP（功能存在但未生产接入）** | 已定义 `handler.go:285`，逻辑正确（request_id 回写响应头、session 不回写）。但全仓零生产调用方（仅测试引用）。ChatHandler.ServeHTTP 仍走旧内联路径（`handler.go:1085-1097`）；Messages/Responses/Gemini 用私有 `initializeRequestIdentity`（204），未用导出版。 |
| WithSession header 优先级 | PASS | `domains/session/middleware.go:31-41` 优先 X-Gw-Session-Id，X-Session-Id legacy，命中分支写 X-Gw-Session-Id-Resume（53、99）。 |
| RequestLogContext.SetTerminal CAS | **GAP（门存在但未生产接线）** | `request_log_pipeline.go:527-539` CAS 正确（single winner）。但零生产调用方：EmitFailure（816-837）、EmitRateLimited（846-866）直接 `c.logged=true` 并 emit，未通过 SetTerminal；disconnect probe 也未走。三路竞争目前只靠 DB 层 L-2 + WAL guard 兜底。 |
| middleware 拒绝路径 X-Request-Id 透传 | PASS | `middleware/auth_mw.go:88-90` writeAuthUnauthorized 从 ctx 回写 X-Request-Id。 |

### Step 3 Session V2 单 owner（commit `f7cee3c6`）— PASS

| 不变量 | 结论 | 证据 |
|---|---|---|
| SessionPersistHook no-op | PASS | `pipeline_hook.go:52-63` Enabled 无条件 return false（62），Execute（68-73）no-op。`main_v2_pipeline.go:340-345` 不再构造。 |
| 唯一 owner | PASS | `cmd/gateway/main.go:1601` telemetryClient.AddOnRequestLogPersisted(sessionv2mirror.PersistHook(...))。 |
| TurnWriter 重复 request_id 返回真实 turn_no | PASS | `turn_writer.go:192` ON CONFLICT DO NOTHING；212-222 RowsAffected==0 时 SELECT 回读真实 turn_no。 |
| SessionAggregator request_id 去重 | PASS（附注） | `session_aggregator.go:93-103` 非空 RequestID 先 probe session_turns，命中即 return nil。依赖 session_turns 已先写入；顺序耦合未在事务内强约束（commit 自述“事务化仍待后续 round”）。 |
| DBWriter.Stop 幂等 + ctx 取消也 drain | PASS | `session_db_writer.go:80-88` stopOnce；runFlushLoop（138-163）defer FlushAll(Background())，stopCh（166）与 ctx.Done（168）两路共享。 |

### Step 4 IR 协议（commit `3853ec67` + `567dacdd`）— PASS + 1 GAP

| 不变量 | 结论 | 证据 |
|---|---|---|
| F-5 ToolArgumentsAssembler + AnnotateArgumentsJSON | PASS（生产接入） | `internal/ir/tool_arguments_assembler.go` 完整实现；`stream.go:50-67` AnnotateArgumentsJSON 暴露 Quality+reason。生产接入：`anthropic_bridge.go:624` content_block_stop 调用。 |
| F-1/F-2/F-3/E-1/D-1/D-2 | PASS | F-1 types.go:381-392；F-2 Extensions；F-3 serialize_openai.go:501-502；E-1 handler.go:5600/5646；D-1 stream.go:862/889；D-2 executor_anthropic.go:899。 |
| anomaly 同协议守卫 | PASS | serialize_anthropic.go:262-291、serialize_gemini.go:100-146 等带 src != 目标协议守卫；previous_response_id 在 SourceProtocol 为 Chat/Responses 时跳过（278）。 |
| IRScopedReporter 生产接入 | **GAP（已知，可观测性缺口）** | anomaly_reporter.go 定义完整，但 NewIRScopedReporter/SetAsCurrent 零生产调用方。后果：requestIDFromIR（serialize_openai.go:879-881）恒返回 "unknown"，生产用进程全局 reporterDed map，导致同一 (source,target,field) 异常在整个进程生命周期内只上报一次——跨请求、跨租户全部被 dedup 吞掉。serialize 路径仍走 DefaultAnomalyReporter=slog.Warn 有日志，但无 per-request 归属。 |

### Step 5 URSM（commit `7c0aa183`）— PASS

| 不变量 | 结论 | 证据 |
|---|---|---|
| routing_state_source 全链路传播 | PASS | statesource.go 枚举（Hit/Miss/Stale/Fallback/Off/Canary/Authoritative/Skipped）+ Record 原子计数 + Snapshot。inner：manager.go FilterAndScoreReadyWithSource/PlanReadyWithSource 自动记录。outer：router.go:124-131 recordOuterSource 闭包（stateRecorded 防双计）；authoritative 成功→Authoritative（174）、失败/not-ready→Fallback（172/197）、canary→Canary（324）、off/catch-all→Off（332）。 |
| authoritative 下 StateManager 零调用 | PASS | state_backend.go:146-164 authoritative+ready 返回 URSMv2Backend，其 FilterAvailable（53-63）是 no-op。router.go:239-243 的 StateManager.IsAvailable 仅在 len==0 且 reason=="" 时触发；authoritative-ready 下不可达。TestPlanCandidates_Authoritative_StateManagerUnused（spy available=false/reason="blocked"，断言 spy.calls==0）收紧有效。 |
| 熔断器 breaker 独立语义 | PASS | domains/credential/breaker.go 无 redis/pool/client 引用，纯进程内。 |

## 非阻断 GAP 汇总（建议后续 round 补齐）

1. **Step 2 SetTerminal 未生产接线**：EmitFailure/EmitRateLimited/disconnect probe 仍直接 `c.logged=true`。建议后续 round 在 emit 路径接入 SetTerminal，达成 spec §4.2 “每请求最多一个主终态”的进程内单 winner。
2. **Step 4 IRScopedReporter 未生产接线**：导致 per-request anomaly 被全局 dedup 抑制。建议引入 InternalRequest.RequestID 字段 + 在 IR build 入口 NewIRScopedReporter + SetAsCurrent + defer Unset。
3. **Step 2 InitializeRequestIdentity 无生产调用方**：四入口仍各自内联身份初始化。可后续 round 统一切换。

## Step 5 round 2（credentialstate 下沉）评估

spec §13 将“credentialstate 真实竞争 C-1 回归未过”列为发布阻断。当前状态：
- `credentialstate` 仍在 `domains/credentialstate/`（未进 `_to-be-deprecated/`），被 9 处 import。
- authoritative 下零调用已由 `selectStateBackendWithReady` + `URSMv2Backend` no-op 保证（PASS）。
- 物理下沉包路径（移动 9 个 import）是中-高风险且非发布阻断项；COW/per-key lock 防御性代码必须保留（spec §10 Step 5 明确要求）。

**round 2 最小改动建议**：
1. 收紧 router.go StateManager 短路（显式 guard，避免依赖“不可达”）。
2. 新增 authoritative 零调用真实集成测试（需 TEST_REDIS_URL）。
3. credentialstate 物理下沉推迟到独立 round（高风险，需同步迁移 9 个 import + 保留 COW）。
