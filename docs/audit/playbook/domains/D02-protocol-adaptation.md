# D02 — 协议适配与双向解析

> 领域编号: D02 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：IR 对上游多厂商标准格式（OpenAI/Anthropic/Responses/Gemini 等）与下游客户端/智能体的适配；chat/message/response 等通讯协议端点；流式与非流式两种会话模式；单一请求与多轮会话的**双向解析**（请求→IR→厂商格式；厂商响应→IR→客户端格式）；原始轮次请求与关键文本保留；每轮会话的人类可读摘要。
**不管**：IR 字段族与存储保真（D01）；压缩触发策略（D05）；队列与限流（D04）。

## 2. 参考基线

设计文档：
- `docs/COMPLETE_REQUEST_FLOW_SUMMARY.md`、`docs/REQUEST_FLOW_WITH_EXCEPTIONS.md` — 请求全流程与异常路径
- `docs/design/2026-09-04-tree-state-v2-routing-plan.md` — tree-state 投影与 Sessions V2 路由契约
- `docs/design/2026-09-04-waterfall-overlay-plan.md` — 瀑布图叠加请求流
- `docs/03-design/01-architecture/architecture/runtime-request-flow.md`

代码入口：
- `internal/ir/` 各协议 parser 与 `*_test.go`（roundtrip/fuzz 基线）
- `adapter/`（根目录）、`internal/handlers/`、`domains/streaming/`
- `domains/sessionsummary/`（message_source_v2.go、message_source_digest.go — 轮次摘要消息源）

## 3. 检查清单

1. **双向解析对称**：每条支持的协议×方向（请求入/响应出）都有 roundtrip 测试；窗口内新增的协议特性（字段/块类型）补齐了两侧测试再算完成。
2. **流式桥完整性**：各协议流式桥的空流门矩阵齐备；首帧前错误静默切换不中断、错误文本不泄漏给客户端（terminal 只发 kind 化信封）；integrity breach → incomplete 终态在四协议钉桩。
3. **arg-first hold 有界**（1MB 基准不变），窗口内改动不放大。
4. **原始轮次保真**：原始轮次请求体与关键文本在转换链上保留（可取证），不被中间格式折叠丢失。
5. **每轮摘要人读化**：sessionsummary 消息源能解出所有块形态（纯文本/多模态块数组/工具结果），摘要输出去格式、供人类查看；新增块形态必须同步消息源抽取。
6. **下游匹配**：客户端类型探测（internal/clienttype）与响应格式匹配不因窗口改动产生"格式串台"（OpenAI 客户端收到 anthropropic 形态等）。
7. ** Responses/Gemini 特性**：reasoning.summary 数组形式、遗留 function_call 消息级形态等已知薄弱面（R30 遗留 #7）不进一步恶化。

## 4. 历史回归点（轮末回注区）
- [R35 09-17] 接入新上游协议时必须核对 IR 不变量：工具轮 finish_reason 统一映射为 "tool_calls"——gemini 原生（MALFORMED_FUNCTION_CALL 才映射）与 responses 原生（status=completed 即 stop）当前不产 tool_calls，一旦这两族 parser 接入 chat 端点，goal tool_calls advisory（Path 1.5）将静默漏判

- [R30] sessionsummary V2 消息源 `Content string` 解不了多模态块数组 → 整轮从摘要消失（契约）— 修复 692205664；v2ContentText 抽取 + 回归钉桩
- [R30] anthropic 7 参死桥已退役、六桥空流门矩阵齐备 —— 健康面基准
- [R30 遗留#7] requestfact wire 缺 class/due_at；Responses reasoning.summary 数组静默丢弃；消息级 function_call 无解析——待收口，防止扩大

- [R36] responses 流式桥 openaiFinishReasonIsError 曾缺 refusal（非流式已映射 incomplete/content_filter）→ refusal-only 流式响应渲染成 completed 成功终态 — 修复本轮回注钉桩 TestOpenaiFinishReasonIsError_IncludesRefusal；新协议接入时同步核对 isError 词表
- [R37] 同型 typed-nil 修复必须排查孪生副本（executors/sticky.go 修后 routing/sticky.go 死副本仍在，intent cache 第三处）——修一处 grep 全仓同构 SetRedisStore；survival E2E 用例 c 钉 retry_limit_exceeded 依赖 stub 不消耗 UpstreamAttemptBudget，换真耗预算 stub 需同步预期（attempt_limit_exceeded 先判）；测试内 goroutine 与主 goroutine 共享 bytes.Buffer 必须 -race 验证（streamRead 实抓）

## 5. 子代理派发提示词

```text
你是 D02（协议适配与双向解析）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D02-protocol-adaptation.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
优先读窗口内改动的 parser/桥/摘要源代码及其测试；只读不改。
输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```
