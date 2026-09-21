# D05 超长自动压缩与 overflow 保连重试 子代理报告（窗口：643735a28^..HEAD = 2026-09-17..3e8fd1658，126 commits；重点 b75c91900..HEAD）

**核心结论："主链路零改动"假设不成立。** `domains/hooks/compression/` 本体确实窗口零改动，但压缩/超长重试调用面有 4 处实质改动（R35/R36/R42/c66dbd6c9），逐核如下。

## 一、发现（候选，待主代理复核）
| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | R36 单次机械修剪目标 = `len(body)/4` tokens 窗口 → 80% 目标实际把 body 压到约原 20%，与注释"modest shrink resolves marginal overflows"不符，边际超长也损失约 80% 上下文 | domains/streaming/survival_coordinator.go:1045（触发路径：流中未提交 KindContextLength → RetryNow → 本函数修剪后重发） | 主代理复核修剪幅度是否过激，考虑改为 `estimateTokens` 驱动的边际收缩 |
| 2 | P3 | 同处修剪窗口估算按 4B/token 不分 CJK，与 H-5 已对齐的 `estimateTokens`（ascii/4 + cjk*2）口径漂移：CJK 重请求低估 tokens，一次性机会可能修不彻底即终端 | domains/streaming/survival_coordinator.go:1045 vs domains/streaming/auto_route.go:227-250 | 复用 estimateTokens 做窗口估算 |
| 3 | P3(观察) | 非流式/durable 恢复路径无压缩重试（re-run 原快照无 body-rewrite hook），域文档 §3.4"非流式对等"仍缺——R36 注释明示为有意设计，非新缺陷 | domains/streaming/attempt_outcome.go:466-473 | 登记 P3 债，勿动决策层（防 durable 空转） |
| 4 | P3(清账) | R35 遗留四项现状：estimateTokens CJK 对齐**已完成**（H-5，auto_route.go:234 注释）；7MB 客户端阈值（ZCode 侧，本仓外）、413 响应体增强（可选级）、告警重分类（运维侧）**仍开放** | docs/audit/2026-09-17-prompt-too-large-413-final-report.md:184-187 | 7MB/告警两项转客户端/运维域跟踪 |

## 二、核实为健康的面
- **`domains/hooks/compression/` 窗口零改动**——`git log 643735a28^..HEAD -- domains/hooks/compression/` 为空。
- **overflow 族分类扩展落地**：R35 contextLengthRe 补 `prompt too long`/`context length limit`/`request_too_large` code（45054170c；errorsx/classify.go:226-241）。
- **R36 单次压缩重试闭环已接通**：survival 层 KindContextLength 未提交失败 → `survivalCtxLenCompressRetryDue` 改判 RetryNow + `survivalCompressBodyForRetry` 机械修剪 → 重发；带钉桩测试 survival_ctxlen_compress_retry_test.go（ad68c91ac；survival_coordinator.go:481-495、746-776、1007-1047）。
- **收敛性有界**：one-shot guard（`ctxLenCompressRetried`）+ 修剪不减即终端（`len(trimmed) >= len(body)` → FailTerminal），无死循环。
- **保连接 + 流内提示链路完整**：压缩时经 OnNodeJump 发 SSE `: thinking:` 提示（executors/context_summarize.go:1131-1141）；已预热线程走 `writePrewarmedStreamError` 不炸已发头（handler.go:4806-4808）。
- **进入前压缩窗口对齐**：preflightCompress 只记录估算不裁剪（preflight_compress.go:8-16，明示不以网关预算当压缩窗）；候选解析后按 `cand.ContextWindow` 80% 压缩（executor_chat.go:2001）。
- **413 admission 三协议 kind 化信封一致**（R35 wire-code typo 已修）：handler.go:2243-2253 / messages.go:274-283 / responses.go:270-279，code 均为 `prompt_too_large`。
- **窗口内 errorsx 两处改动不动 overflow 族**：c66dbd6c9（MiniMax thinking.type 2013 → KindClientBug）与 R42 `bad_request_error` 流内分支 + failover_policy KindClientBug case 合并（字段逐一相同，KindContextLength 处置 ScopeModel/no-probe 不变）——9c99f3c40。
- **可观测**：`survival_context_length_compress_retry` 前后字节日志（survival_coordinator.go:752-761）+ context_summarize slog.Info + mergeCompressionMetaV3 折入 compression_meta（45054170c）。

## 三、未覆盖项与原因
- §3.5 策略选择器热生效半套策略——选择器代码窗口零改动，未深审配置热加载路径。
- 7MB 客户端压缩阈值——ZCode 客户端仓库不在本 repo，只读静态审计不可达。
- 告警重分类落地——告警/运维配置在仓库外；代码侧 `EmitFailure("prompt_too_large")`（handler.go:2248）未见重分类痕迹。
- 真机 413/流中 overflow 端到端（245/252 live）——需真实超长请求与凭据，超出只读静态审计。

**主代理复核结论（R43）**：#1/#2 亲读属实（len/4 窗口 ×80% 目标 ≈ 保留 20%，与"modest"注释不符；CJK 低估方向保守）→ 合并登记为一条 P3 行为债（修复需行为变更 + E2E，不属本轮纪律内微修）；#3/#4 登记。健康面采信（压缩域主链路完整、收敛有界、保连接链路在位）。
