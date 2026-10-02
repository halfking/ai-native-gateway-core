# ADR: 429/限流剔除出断路器失败计数（保留绑定级排序降权）

- **Status**: Accepted（Wave 1 P0 红线裁决，2026-09-22）
- **Scope**: `domains/credential/breaker.go`（RecordFailure）、`domains/credential/writer.go`（绑定冷却，不变）
- **Related**: 设计文档《llm-gateway-系统功能特性及流程》§5.6（429 边界）、2026-09-21 设计差距审计 A4

## Context

设计红线规定：**429 只回传客户端，不计供应商错误、不动节点状态**。而实现中 429（`KindRateLimit`）一路三处生效：

1. `writer.go` `UpdateOnFailure`：绑定级冷却（`coolingDuration`，Retry-After 优先，默认 3min），只写 `(credential, model)` 绑定行，不污染凭据级可用性——这是"限流即降权"的有意演进，短期限流时把节点往后排有实际收益；
2. `breaker.go` `defaultPolicies` / `freeTierPolicies`：429 计入断路器失败计数，可触发 OPEN→指数退避；
3. executor 侧 sticky/weight-nudge 等软降权观测。

断路器的语义是"节点健康故障的确认与隔离"。429 表示上游**可达但容量受限**：服务是活的，多轮对话中一次突发限流就被计为节点故障并进入分钟级熔断，会 (a) 把可用的计费容量无谓扔掉，(b) 在多凭据同上游限流时同时打开多个断路器放大抖动，(c) 让"节点故障率"指标混入容量噪声。

## Decision

1. **断路器只由真实故障驱动。** `Breaker.RecordFailure` 对 `KindRateLimit` 直接早退：不计失败数、不计连续失败、不触发 OPEN/QUARANTINE。`defaultPolicies`/`freeTierPolicies` 中的 KindRateLimit 策略项随之删除（结构性固化裁决，不留死配置）。
2. **保留短期限流的排序降权。** `writer.go` 绑定级 3min 冷却（Retry-After 优先）原样保留：节点在 resolve/候选列表中依然可见可用，只是排序靠后。sticky 失败阈值、weight-nudge 等软降权机制不受影响。
3. **HALF_OPEN 探针撞 429 = 非失败证据。** 限流不构成"被探针验证的失败"，归还探针槽并保持 HALF_OPEN（对齐 client-bug 早退路径的 ReleaseProbe 语义），避免断路器卡在探针被占的半开态。
4. **设计文档 §5.6 的正式反向修订在 Wave 2 文档轮完成**（本 ADR 为代码侧引用源）。

## Consequences

- 429 风暴不再打开断路器；节点隔离只由 Timeout/Network/UpstreamDown/Overloaded 等真实故障种类驱动。
- 限流期内的容量惩罚完全由绑定级冷却 + 软降权承担，粒度从"凭据级熔断"细化到"(credential, model) 绑定排序"。
- 若上游以 429 长期饱和（软拒绝），节点不会被熔断隔离，依赖排序降权 + sticky 失败阈值消化；运维侧可通过 `candidate_failure_logs` / rate-limit 指标观察。

## Verification

- `TestRateLimitExcludedFromBreaker`（`breaker_test.go`）：429 风暴保持 CLOSED、连续失败为 0；HALF_OPEN 探针撞 429 归还探针槽且状态不变；真实故障（UpstreamDown×2）仍照常 OPEN。
- `TestRateLimitExponentialBackoff`（旧行为钉桩）已由上述测试替代。
- `writer_regression_test.go` 覆盖 KindRateLimit 的绑定级冷却写入路径不回归。
