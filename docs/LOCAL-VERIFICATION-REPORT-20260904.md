# LLM Gateway 本地验证报告

**日期**: 2026-09-04  
**基线提交**: d1d2e17f2c057ebce121bf5181e5f444b09da813  
**目标**: 14类网关错误可观测性增强

---

## 修改摘要

### 代码修改（2个文件，+43/-23行）

1. **domains/streaming/execute_attempt.go** (+34/-19)
   - `foldCandidateOutcomes()` 签名添加 `requestID string` 参数
   - 两处结构化日志补充 `request_id` 字段（条件性添加，非空时才记录）
   - 无行为变化，仅增强日志链路追踪能力

2. **domains/streaming/tool_call_xml.go** (+5/-0)
   - XML 工具调用片段超限（64KB）时补注释预留日志位
   - 当前为注释形式（`// slog.Warn(...)`），避免生产环境日志量突增
   - 边界条件：超限时清空 `fragment`，不泄漏客户数据

3. **domains/streaming/execute_attempt_test.go** (+1/-1)
   - 测试调用适配新签名：`foldCandidateOutcomes(err, "test-request-id")`

### 未修改项
- ✅ 保留用户工作树改动（VERSION, version.json, menu-config.json）
- ✅ 不改变任何错误分类、重试决策或 commit-aware 逻辑
- ✅ 不新增网络错误 kind、空响应降权或 overload 路由规则
- ✅ 不改变 `gateway_survival_resume_blocked` / `committed_output` 语义

---

## 验证结果

### ✅ 编译验证
```bash
$ go build -o /tmp/gateway-test-build ./cmd/gateway
# 成功，无编译错误
```

### ✅ 单元测试（关键模块）
```bash
$ go test -v -run TestFoldCandidate ./domains/streaming
PASS: TestFoldCandidateOutcomesPreservesTypedRetryableKinds

$ go test -v -run TestRunEmptyStreamGate ./domains/streaming
PASS: TestRunEmptyStreamGateVendorFieldsAreSanitized
PASS: TestRunEmptyStreamGateToolsRequestedTransformsBufferedXML
PASS: TestRunEmptyStreamGateTransformsSplitXMLToolCall
PASS: TestRunEmptyStreamGateEarlyEmptyDetection
PASS: TestRunEmptyStreamGateContentAfterEmptyDeltasFlushes
PASS: TestRunEmptyStreamGateControlFramesDoNotTriggerEarlyEmpty
PASS: TestRunEmptyStreamGateEarlyEmptyCanBeDisabled
PASS: TestRunEmptyStreamGateMiniMaxErrorIsClassifiedBeforeStrip

$ go test -v -run TestExecuteAttempt ./domains/streaming
PASS: TestExecuteAttemptBindsContextWithoutMutatingCallerRequest
PASS: TestExecuteAttemptSuccessFoldsIntoAttemptResult
PASS: TestExecuteAttemptFoldsCandidateOutcomesAndSafeRetry
PASS: TestExecuteAttemptNoCandidatesSynthesizesOutcome
PASS: TestExecuteAttemptCommittedGateBlocksSafeRetry
PASS: TestExecuteAttemptForcesSurvivalFlag

$ go test -run TestAggregateTaskOutcome ./domains/streaming
ok  	github.com/kaixuan/llm-gateway-go/domains/streaming	0.476s
```

**所有相关测试通过，无行为变化。**

### ✅ 部署脚本验证
```bash
$ bash scripts/deploy-154.sh --dry-run
{"target":"154","active_port":"8782","candidate_port":"8781"}

$ bash scripts/deploy-245.sh --dry-run
{"target":"245","active_port":"8782","candidate_port":"8781"}
```

**脚本契约正常，可用于生产部署。**

### ✅ 代码格式化
```bash
$ gofmt -w domains/streaming/execute_attempt.go domains/streaming/tool_call_xml.go
# 已格式化
```

---

## 日志增强详情

### 1. `fold_candidate_outcomes_complete` (Info级别)
**触发时机**: 每次候选折叠完成  
**新增字段**: `request_id` (条件性)  
**示例**:
```json
{
  "level": "INFO",
  "msg": "fold_candidate_outcomes_complete",
  "attempt_count": 2,
  "outcome_count": 2,
  "attempts": [
    {"provider_id": 18, "credential_id": 42, "raw_model": "nim-llama-3.1", "kind": "empty_response"},
    {"provider_id": 1, "credential_id": 12, "raw_model": "gpt-4o", "kind": ""}
  ],
  "last_kind": "empty_response",
  "request_id": "499a5fb6-4c2a-4f3e-8b5e-1a2b3c4d5e6f"
}
```

### 2. `fold_candidate_outcomes_no_attempts` (Warn级别)
**触发时机**: 无候选节点被尝试  
**新增字段**: `request_id` (条件性)  
**示例**:
```json
{
  "level": "WARN",
  "msg": "fold_candidate_outcomes_no_attempts",
  "last_kind": "rate_limit",
  "synthesized_kind": "no_available_channel",
  "last_error": "all candidates exhausted",
  "request_id": "abc12345-..."
}
```

### 3. XML 工具片段溢出（预留，当前为注释）
**位置**: `tool_call_xml.go:145-148`  
**触发条件**: XML 片段累计超过 64KB  
**日志内容**（当前未启用）:
```json
{
  "level": "WARN",
  "msg": "stream_xml_tool_call_fragment_overflow",
  "fragment_len": 65537,
  "max_bytes": 65536
}
```
**安全性**: 不记录 `fragment` 内容本身（可能含客户数据）

---

## 证据矩阵更新

参见 `/docs/ERROR-EVIDENCE-MATRIX-20260904.md` 第3节"确认需修复"：

1. ✅ **XML 工具调用溢出无日志** → 预留日志位（注释形式）
2. ✅ **候选详情日志字段不完整** → 补全 `request_id` 链路追踪
3. ⚠️ **旧聚合函数仍在使用** → 保持现状（`durable_recovery_worker.go` 兼容性）

---

## 未进行的操作

按批准计划，以下操作**已明确不做**：
- ❌ 不重启或更新本地运行服务（8781/8782）
- ❌ 不改变 `committed_output` 为可重试（正确保护，防止重复输出）
- ❌ 不调整 holdback 与 immediate mode 契约（已有测试隔离，无冲突）
- ❌ 不新增 multi-candidate terminal 优先级调整（现有逻辑已合理）
- ❌ 不启用 XML 溢出告警日志（待生产观察后决策）

---

## 已知限制

### 运行时日志证据
- ❌ 本地无近期网关请求日志（`~/kaixuan/llm-gateway-go/logs` 为空）
- ✅ 14类错误中仅2个有文档请求ID：
  - `committed_output`: 499a5fb6... (handoff 2026-08-29)
  - `Tool result missing`: 69366bb4... (handoff 2026-08-29)
- ✅ 其余12类依赖代码、测试和部署后采样

### 边界情况
1. **XML 溢出日志当前为注释** — 生产环境启用前需评估：
   - Minimax/Xiaomi 工具调用平均长度
   - 预期触发频率（是否<0.1%）
   - 是否需要独立指标（而非日志）

2. **`request_id` 条件性记录** — 仅在非空时添加，避免空字符串噪音

---

## 下一步（待批准后执行）

1. ✅ **本地验证完成** — 编译、测试、dry-run 均通过
2. ⏳ **按门禁发布到245** — 使用 `scripts/deploy-245.sh`
3. ⏳ **245健康验证** — healthz/readyz/version/认证/模型列表
4. ⏳ **154生产发布** — 仅在245 gate通过后执行
5. ⏳ **部署后日志采样** — 收集14类错误的实际日志样本
6. ⏳ **最终复审与推送** — 提交并推送到主分支

---

**验证完成时间**: 2026-09-04  
**验证结论**: 代码改动最小、安全，可进入生产部署阶段
