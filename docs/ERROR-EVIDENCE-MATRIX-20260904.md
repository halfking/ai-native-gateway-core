# LLM Gateway 14类错误证据矩阵

**生成时间**: 2026-09-04  
**工作树状态**: d1d2e17f (已有未提交版本差异)  
**审计范围**: 文档、代码、测试；无近期运行时请求日志

---

## 错误证据矩阵

| # | 错误签名 | ErrorKind | 检测点 | 可重试 | 候选/节点动作 | Commit状态影响 | 日志字段 | 运行时请求ID |
|---|---------|-----------|--------|--------|--------------|--------------|---------|-------------|
| 1 | `empty_model_response` | `KindEmptyResponse` | `errorsx/classify.go:96`<br>`streaming/stream.go:440-449` | ✅ Resumable failover | `TaskActionRetryNow`<br>不冷却凭据 | uncommitted→透明重试 | `reason="empty_stream_no_content"`<br>`Resumable=true` | ❌ 无（仅文档） |
| 2 | `gateway_survival_resume_blocked`<br>`committed_output` | (various retryable) | `streaming/attempt_outcome.go:207-208, 352-353` | ⛔ **预期阻止** | `TaskActionResumeBlocked` | **committed→阻止**<br>防止重复输出 | `reason="committed_output"`<br>`CommitState>=Content` | ✅ `499a5fb6...` (handoff doc) |
| 3 | `Network connection failed` | `KindNetwork` | `errorsx/classify.go:716-721` | ✅ Yes | `TaskActionRetryNow`<br>切换节点 | 任意 | `IsRetryable=true` | ❌ 无 |
| 4 | `Our servers are currently overloaded` | `KindUpstreamOverloaded` | `errorsx/classify.go:158, 594-601`<br>`upstream/client_test.go:495` | ✅ Yes | `TaskActionRetryNow`<br>同credential优先 | 任意 | `Scope=ScopeModel`<br>`RetryAfter=5s` | ❌ 无（测试用例2026-08-08） |
| 5 | `temporarily unavailable` | `KindUpstreamDown` | `errorsx/classify.go:793-794` | ✅ Yes | `TaskActionWaitRecovery` | 任意 | `EnqueueProbe=true`<br>`RetryAfter=2s` | ❌ 无 |
| 6 | `Tool result is missing` | `KindUpstreamDown` | `streaming/tool_call_validator.go`<br>`anthropic_bridge.go:269` | ✅ Resumable | `incomplete_tool_call_interrupted` | uncommitted→透明重试 | `Resumable=true` | ✅ `69366bb4...` (handoff 2026-08-29) |
| 7 | `prompt exceeds gateway budget` | `KindClientBug` | `streaming/handler.go:2206`<br>`streaming/responses.go:273` | ⛔ No (400) | `TaskActionFailTerminal` | 任意 | HTTP 400<br>`LLM_GATEWAY_MAX_PROMPT_TOKENS` | ❌ 无 |
| 8 | `textual <tool_call>` (Minimax) | (成功或转换) | `streaming/tool_call_xml.go:12-287` | N/A (content转换) | 无错误动作<br>XML→JSON coerce | N/A | 无kind<br>静默转换 | ❌ 无 |
| 9 | `stream_read_error` | `KindStreamTimeout`<br>`KindUpstreamDown`<br>`KindCanceled` | `streaming/stream.go:323-334`<br>`handler.go:7746-7748` | ✅ Yes (context-dep) | `TaskActionRetryNow` | uncommitted→重试 | `reason="stream_timeout"`<br>`eof_without_done`<br>`client_cancel` | ❌ 无 |
| 10 | `KindAuth` / `KindAuthRevoked` | `KindAuth`<br>`KindAuthRevoked` | `errorsx/classify.go:18-29` | ✅ Auth (retry)<br>⛔ Revoked (terminal) | `Scope=ScopeCredential`<br>`Fuse=true` (revoked) | 任意 | `Permanent=true` (revoked) | ❌ 无 |
| 11 | `KindQuota*` | `KindQuotaPermanent`<br>`KindQuotaPeriodic`<br>`KindQuotaBalance` | `errorsx/classify.go:409-474` | ⛔ Permanent (terminal)<br>✅ Periodic (wait) | `TaskActionWaitRecovery` (periodic)<br>`TaskActionFailTerminal` (perm) | 任意 | `RetryAfter` 解析 | ❌ 无 |
| 12 | `KindContextLength` | `KindContextLength` | `errorsx/classify.go:225-243` | ✅ 单次trim重试 | trim+retry (executor)<br>失败→400 | 任意 | `IsClientBug` 元素 | ❌ 无 |
| 13 | `KindModelNotFound`<br>`KindModelDeprecated` | `KindModelNotFound`<br>`KindModelDeprecated` | `errorsx/classify.go:267-275, 317-325` | ⛔ NotFound (短路)<br>⛔ Deprecated (30天冷却) | `TaskActionFailTerminal`<br>路由短路 | 任意 | `Permanent=true` | ❌ 无 |
| 14 | `KindToolCallIdMismatch` | `KindToolCallIdMismatch` | `errorsx/classify.go:42` | ⛔ No (client bug) | `TaskActionFailTerminal`<br>不惩罚凭据 | 任意 | `IsClientBug=true` | ❌ 无 |

---

## 证据来源汇总

### 文档证据
1. `/docs/error-analysis-and-fixes.md` (2026-09-04) - 14类错误根因
2. `/docs/COMPLETION-REPORT-20260904.md` (2026-09-04) - 日志增强完成报告
3. `/.handoff/gateway-error-analysis-20260901.md` (2026-09-01) - 初步分析
4. `/.handoff/2026-08-29-tool-result-missing-analysis.md` (2026-08-29) - 工具调用分析，含请求ID

### 代码证据
1. `errorsx/classify.go` - 错误分类系统（1351行）
2. `errorsx/failover_policy.go` - 路由决策（141行）
3. `domains/streaming/attempt_outcome.go` - 任务聚合（478行，含增强日志）
4. `domains/streaming/survival_coordinator.go` - 恢复循环（1029行）
5. `domains/streaming/attempt_commit_gate.go` - Commit状态机（1136行）
6. `domains/streaming/tool_call_validator.go` - 工具完整性（230行）
7. `domains/streaming/tool_call_xml.go` - Minimax XML转换（287行）

### 测试证据
1. `domains/streaming/task_outcome_aggregator_test.go` - 聚合测试
2. `domains/streaming/stream_early_empty_test.go` - 空响应测试
3. `domains/streaming/tool_call_validator_integration_test.go` - 工具调用测试
4. `upstream/client_test.go` - 过载检测测试（2026-08-08）
5. `domains/streaming/overload_retry_after_test.go` - overload重试测试

### 本地日志证据
- ❌ **无近期网关请求日志**
- `~/kaixuan/llm-gateway-go/run/gateway-migrate.log` - 仅DB迁移日志
- `~/kaixuan/llm-gateway-go/bin/` - 历史release (1919-1931)

---

## 关键发现

### ✅ 已正确实现
1. **Empty response透明failover** - `Resumable=true` + 不冷却凭据
2. **Committed output保护** - 正确阻止重复输出，**不应改为重试**
3. **工具调用完整性验证** - 检测不完整tool_use/tool_result对
4. **Minimax XML工具转换** - 支持双regex（Xiaomi + MiniMax M2.7）
5. **Overload独立分类** - 与KindConcurrent区分，同credential重试优先
6. **Survival决策日志** - `survival_decision_aggregate` (c5831a20f, 2026-09-04)

### ⚠️ 确认需修复
1. **XML工具调用溢出无日志** (`tool_call_xml.go:144-145`) - 64KB上限，静默丢弃
2. **候选详情日志字段不完整** - `fold_candidate_outcomes_complete`缺provider/credential/model
3. **旧聚合函数仍在使用** - `durable_recovery_worker.go:431`用旧版无历史聚合

### ❌ 不应修复（预期行为）
1. **gateway_survival_resume_blocked** - 防止重复输出的正确保护
2. **Holdback与immediate mode** - 测试已隔离，无冲突（immediate测试不启用holdback）
3. **Multi-candidate terminal优先级** - 现有逻辑：unknown>terminal>blocked>committed+retry>retry>wait

---

## 日志证据缺口

### 运行时请求ID
- **仅2个签名有**：`committed_output` (499a5fb6...), `Tool result missing` (69366bb4...)
- **12个签名无**：依赖文档、代码、测试推断

### 近期生产日志
- ❌ 本地无154/245运行时日志访问权限
- ✅ 部署后可通过`journalctl`或`health-check-154.sh`采样
- ✅ 文档记录了快速检查命令（154-service-status-and-monitoring.md）

---

## 后续验证计划

### 本地验证
1. 编译、格式化、单元测试
2. Dry-run部署脚本契约
3. 生成不含密钥的验证报告

### 远程验证（部署后）
1. 245健康/版本/认证验证 → gate.json
2. 154部署（仅gate通过后）
3. 读取非敏感错误分类和新日志
4. 采样14类错误的实际日志样本

---

**矩阵完成时间**: 2026-09-04  
**下一步**: 代码修复（XML溢出日志、候选详情补全、旧聚合迁移）
