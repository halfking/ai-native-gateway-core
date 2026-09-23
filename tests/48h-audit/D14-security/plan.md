# D14 — 场景安全（含敏感信息处理）

> 域知识库：[docs/audit/playbook/domains/D14-security.md](../../../docs/audit/playbook/domains/D14-security.md)
> 48h 改动面（截至 R58）：`security/sanitize/smart_sani_guard.go`（restoreResponseBody / restoreStreamOpenAIDelta / restoreStreamAnthropicDelta 增加 tool_calls/tool_use 还原）+ `tests/48h-audit/D14-security/{business,data,stress,safety}/*`（新增 mock 套件）
> 状态：进行中（T2 入口 · mock 全面验证）

## 1. 审计要点

- 敏感信息链路端到端：SanitizeInputMiddleware → 上游 LLM → OutputCompliance → SanitizeRestoreInterceptor；任何中间点都不能泄漏真实 PII，也不能让 raw 占位符文本泄漏到客户端。
- 协议覆盖：OpenAI chat / OpenAI Responses / Anthropic Messages 三协议，每种都要覆盖 `content` + `tool_calls.arguments`（或 `tool_use.input`）。
- 工具调用还原：网关把上游响应里的 `tool_calls.arguments` 还原后，交给插件/工具执行；这一步必须拿到真实敏感值，否则下游逻辑会按占位符文本误判（`{SENSITIVE:phone:1}` 当作字符串，无法拨号、查号、支付）。
- 未知占位符 mask：LLM 在响应中伪造 `{SENSITIVE:type:99}`（sm 中没有）时，必须 mask 为 `[REDACTED]`，不能透传——这是上游注入面（v1 见 `TestRestoreResponseBody_UnknownPlaceholderMasked`；本域补 tool_calls 同款）。
- 跨轮次不撞号：sm 持久化到 Redis + offset hash，每类 type 自增；新轮 `SanitizeInputMiddleware` 必须读 offset，避免连续两轮都是 `phone:1` 但 sm 后写覆盖前写（生产 bug 锚见 R35 跨租户实测）。
- 跨 chunk 占位符：流式 SSE 单 chunk 拆帧时占位符跨边界——已知缺口钉在 `TestSanitizeRestoreInterceptor_StreamChunk_FrameSplitAcrossChunks`；修复路径在 `streaming.handler` SSE 帧缓冲（follow-up）。
- 资源/锁安全：`SanitizeInputMiddleware.stateMu` + `acquireOffsetsLock` 串行化 read-modify-write，防止跨进程 race（R35 历史教训）。
- 不可达降级：Redis 不可达时映射表持久化失败 → 还原降级为单轮；日志 warn 但不阻断。

## 2. 业务测试（business/）

- [x] B-01：OpenAI chat 完成式响应 + `message.tool_calls[*].function.arguments` 还原 → 钉住 `RestoreOutputOrMask` 在 JSON 字符串路径上做逐字段还原
- [x] B-02：OpenAI chat 流式 + `delta.tool_calls[*].function.arguments` 还原 → 流式 chunk 内必须命中
- [x] B-03：未知占位符 → `[REDACTED]` mask，覆盖 `tool_calls.arguments` 路径

## 3. 数据测试（data/）

- [x] D-01：`restoreJSONRecursive` 浅遍历深度：字符串 → map → 数组 → 嵌套 map，命中所有分支
- [x] D-02：arguments 不是合法 JSON（罕见但存在）→ 退化为字符串路径占位符替换

## 4. 压力测试（stress/）

- [x] S-01：1000 轮并发还原（miniredis 内嵌）→ 验证映射表读写的并发稳定性（无 race）
- [x] S-02：10000 次 tool_calls.arguments 还原在 b.N 循环里跑 P99

## 5. 安全测试（safety/）

- [x] SF-01：伪造占位符（`{SENSITIVE:phone:99}` 不在 sm）→ mask；`metrics.SanitizePlaceholderTamperingTotal` 计数自增
- [x] SF-02：跨租户（X-Gw-Tenant-Id A vs B）→ A 的 sm 不能被 B 还原（已有 `TestSanitizeRestoreInterceptor_CrossTenant_DoesNotLeakMap`，本域加 tool_calls 同款）

## 6. 验收门

```bash
go build ./...
go vet ./...
go test -race -timeout 120s ./tests/48h-audit/D14-security/...
go test -race -timeout 120s ./security/sanitize/...   # 补的实现依赖此层
```

## 7. 与方案文档的对齐

- RFC：docs/全面测试/README.md §敏感信息处理（占位符 → 真实值）契约
- 上轮挂账：R52 §安全开关权限档批 / R35 §跨轮次占位符撞号（生产实测）
- 实现变更：`security/sanitize/smart_sani_guard.go` 新增 `restoreToolCallsArgs` + `restoreJSONRecursive`；`restoreResponseBody` / `restoreStreamOpenAIDelta` / `restoreStreamAnthropicDelta` 接入工具调用还原。

## 子代理派发提示词

```
你是 D14（场景安全 + 敏感信息）只读 + 测试可执行子代理。
知识库入口：docs/audit/playbook/domains/D14-security.md + docs/全面测试/README.md §敏感信息契约。
模板：tests/48h-audit/TEMPLATE-domain.md。
必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```