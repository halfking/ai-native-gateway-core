# D01+D02 合并域子代理报告（窗口：48h = 643735a28^..HEAD；重点增量 = b75c91900..HEAD）

- 审计人：D01+D02 只读审计子代理（R43 轮）
- 意图基线：`docs/audit/2026-09-18-minimax-thinking-incident.md`（含 §7 收尾轮）已全文亲读
- 重点增量域内文件全部亲读：`internal/ir/serialize_openai.go`(+两个 thinking 测试)、`internal/paramreg/{registry,translate,decide}.go(+test)`、`domains/streaming/executors/{executor_chat,intarget...wiring_test,inline_validation}.go`、`domains/streaming/anthropic_bridge.go`、`resolve/resolve.go`
- 纪律声明：本报告全部为线索，主代理须亲读复核后方可登记；未执行任何 go build/test（只读纪律）

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 触发路径 | 建议处置 |
|---|---|---|---|---|---|
| F-1 | **P1** | **Gemini 入向 thinkingConfig 的推理意图在生产路径仍被静默丢弃——P5 补课（7bb1708d5）对 Gemini 实际不闭环**。ir 层测试 `TestSerializeOpenAI_P5_GeminiBudgetRenderedByTargetProvider` 通过仅因手工设了 `TargetProvider`；生产链路中唯一见到该 intent 的 SerializeOpenAI（gemini handler step 6）发生在路由前、`TargetProvider` 必为空，而 budget 形状 Reasoning 无 OpenAI 线格式原生表达，无法像 Extensions 字段那样在 body 往返中存活到 executor 的方言感知重序列化。事故文档 §3 v3 "handler_gemini 有意不补（executor 会带 candidate 重跑出向序列化）"的论证对 Extensions 可还原字段成立，对 budget 形状 Reasoning 不成立。且该丢失无 anomaly 上报（`reportSerializeOpenAILosses` 无 Reasoning-budget 分支），与 P5 "停止静默丢失"的目标相悖。§7.3 受控验证表只测了 openai/anthropic 入向，未测 gemini 入向 | `domains/streaming/handler_gemini.go:284-290`（step 6 序列化，无 TargetProvider）；`internal/ir/parse_gemini.go:528-536,556-562`（thinkingConfig 是 known field → 只进 `ir.Reasoning`，不进 Extensions）；`internal/ir/serialize_openai.go:78-79`（仅 Effort≠"" 才原生出向）；`serialize_openai.go:247-257` + `extensions_restore.go:126-132`（TargetProvider 空 → 方言回退 openai_chat）+ `serialize_openai.go:324-346`（`syntheticReasonCaps(DialectOpenAIChat)`→false→不渲染）；`executor_chat.go:1816-1828`（executor 重 ParseOpenAI 时 intent 已不在 body 中） | 客户端经 `/v1beta/models/minimax-m3:generateContent` 携带 `generationConfig.thinkingConfig.thinkingBudget` → step 6 序列化丢失 intent → executor 重序列化无物可渲染 → MiniMax/DeepSeek/GLM/Ark/Qwen 上游收不到任何 thinking 字段（M3 默认思考，与 §4 表 ❌ 行同型，只是换成了 gemini 协议） | 主代理亲读复核后：在 gemini 链路上补 TargetProvider（如 handler 将 IR 经 context 携带到 executor，或在 step 6 延迟序列化），或最低限度补 budget-Reasoning 的 loss 上报 + gemini 入向 E2E 用例；并将本条回注 D02 §历史回归点 |
| F-2 | P3 | R42 新增的 anthropic_bridge `bad_request_error` 流内分支**无钉桩回归测试**：`TestClassifyAnthropicStreamError` 的用例表既无 `bad_request_error` 也无既有 `invalid_request_error`。违反 conventions §5 "每修一条配钉桩回归测试"；词表未来重构回退时 CI 不会捕获 | `domains/streaming/anthropic_bridge.go:593-603`（修复本体）；`domains/streaming/anthropic_passthrough_error_intercept_test.go:172-190`（用例表缺两值）；对照 `errorsx/classify_minimax_test.go:26`（admission 路径有钉、stream 路径无） | 未来任何人改动 classifyAnthropicStreamError 词表或 default 分支，`bad_request_error→KindTransient→KindUpstreamDown` 的冷却抖动回归静默复发 | 在用例表补 `{"bad_request_error", KindClientBug}` 与 `{"invalid_request_error", KindClientBug}` 两行（一行代码的钉） |
| F-3 | P3 | **同一目标方言、两种线格式语义不对称**：DeepSeek/GLM 目标下，Extensions 路径（openai 入向）对客户端原 thinking 对象**逐字透传（含 budget_tokens）**，而 P5 reasonnorm 路径（anthropic/gemini 入向）对同一目标输出**最小对象（无 budget_tokens）**。同一上游收到两种不同形状的 `thinking` | `internal/paramreg/translate.go:46-48`（非 MiniMax 原样透传）vs `internal/reasonnorm/norm.go:427-442`（renderGenericThinking 仅 `{type}`）；对照 `serialize_openai_thinking_p5_test.go:75-79` 注释自认"P5 生成的对象走最小对象语义" | 同一 DeepSeek 上游：openai 入向客户端发 `{type:enabled,budget_tokens:N}` → 原样送达；anthropic 入向同语义请求 → `{type:enabled}`。若上游严格校验，前者可能 400 而后者通过（行为分叉难排查） | 低危一致性债：在 translateThinking 对 DeepSeek/GLM 也走最小对象语义，或登记为已知不对称 |
| F-4 | P3 | **`adaptive`+`budget_tokens` → MiniMax 不剥 budget_tokens**：translateThinking 仅在 `type=="enabled"` 时改写为最小对象；`{"type":"adaptive","budget_tokens":N}` 原样透传。而代码注释自己的理由（M3 无 budget 概念、残留未知字段可能在严格校验下再 400）同样适用于 adaptive 场景 | `internal/paramreg/translate.go:56-67`（仅 enabled 分支改写；其余 return value 原样）；`decide_test.go:200-205`（adaptive 用例恰为纯 `{"type":"adaptive"}`，未覆盖 adaptive+budget 组合） | 客户端发 `{"type":"adaptive","budget_tokens":1024}` → MiniMax bound body 保留 budget_tokens → 潜在 2013 硬 400（事故仅实证 enabled 被拒；adaptive+budget 是否被拒未真机证实） | 主代理定级时注明"未真机证实"；低成本收口：MiniMax 目标统一输出最小对象（type 归一 adaptive/disabled） |

备注（P3 以下，不占编号）：legacy 断路器兜底回调 `ConvertAnthropicBodyToOpenAI`（`domains/streaming/messages.go:899-906` → `convertToChatBody`）不映射请求级 thinking（文件内 thinking 仅响应侧），仅在 IR breaker OPEN 时触发（`executor_chat.go:1711-1721,1783`），窗口外存量。

## 二、核实为健康的面（带证据）

**Q1 TargetProvider 接线覆盖面**
- chat 出向三分支 + e.IR==nil 路径全部接线：anthropic→openai IR 分支 `executor_chat.go:1751`、legacy_with_ir 主分支 `:1828`、断路器兜底 `:1871`、inline validation `inline_validation.go:41`；且 `executor_target_provider_wiring_test.go` 三条分支逐条钉死（用真实 `ir.ParseOpenAI/ir.SerializeOpenAI`，非 mock 序列化，见 `executor_ir_test.go:17-31`）——事故 §6.2 "executor 层无自动化测试"缺口确已闭环
- anthropic 出向：`executor_anthropic.go:556` 在 SerializeAnthropic 前设 `TargetProvider`（存量）；定时 dispatch 复用 `executeOpenAI`（`executor_dispatch.go:828`）→ 同一接线覆盖；重试路径 `executor_chat.go:1046` 每候选重跑 finalize → 换候选方言正确
- `handler_gemini` 是唯一未接线的出向序列化点 → 即发现 F-1

**Q2 strip 边界**
- 非 MiniMax 目标不剥：`translate.go:47`，decide_test.go:214-224 钉 DeepSeek/Anthropic 透传
- disabled 原样透传、客户端已发 adaptive 保持不变：`translate.go:66` + decide_test.go:207-212
- interleaved/多轮消息级 thinking 块不受 strip 影响：strip 仅作用于 request 级 Extensions["thinking"]；消息级 thinking 块在 openai 出向走 `serializeOpenAIMessageContent` default 分支整体剥离（`serialize_openai.go:607-613`），符合 MiniMax/DeepSeek"历史不回传 reasoning"的要求

**Q3 多轮往返/V2**
- anthropic→anthropic 保真：`serialize_anthropic.go:624-626` 重发 thinking+signature；`parse_anthropic.go:367-380` 双向解析对称
- V2 存储存客户端可见 body，thinking 块入库不丢；逐轮摘要 v2ContentText 对 thinking 块返回空属合理设计（摘要去推理），非本窗口回归
- anthropic 出向桥流式/非流式响应侧均处理 reasoning：流 `anthropic_bridge.go:861-877`（delta.reasoning_content→thinking），非流 `chat_to_anthropic_response.go:73-109`

**Q4 流式/非流式同源**
- 请求侧方言翻译单源：流式与非流式都经 `executeOpenAI → finalizeOpenAIUpstreamBody → SerializeOpenAI`（body 只建一次），无第二套翻译实现
- MiniMax 语义双路径一致（enabled→adaptive 最小对象）：`translate.go:56-66` 与 `norm.go:444-451`（renderMiniMax）语义相同，ir 层两套测试钉桩（`serialize_openai_thinking_dialect_test.go`、`serialize_openai_thinking_p5_test.go`）

**修复本体复核**
- `bad_request_error` 流内分支方向正确：与 c66dbd6c9 admission 路径分类一致（KindClientBug 不 failover）；注释所述旧行为（fallthrough→`ClassifyErrorWithBody(0,…)`→`KindTransient`→升级 `KindUpstreamDown`）与 `anthropic_bridge.go:611-615` 现码相符
- loss 上报条件化正确：已渲染不报、signature/redacted_thinking 仍逐消息上报（`serialize_openai.go:1036-1047,986-1007`）；applyThinking "不覆盖已有键" 防二次表达（`serialize_openai.go:260-285`），openai 入向 effort 防双表达有守卫+测试（`serialize_openai.go:298-301`、p5 测试 197-223）
- `resolve/resolve.go:196-199,237-240`：`lower(raw_name)`+`status='active'` 与 schema 对齐——`status text DEFAULT 'active' NOT NULL`（`sql/schema/01-schema.sql:9571`）+ 既有部分索引 `idx_model_aliases_lower_raw_name_status`（:24057-24060），COALESCE 删除无 NULL 行为回归，且能命中索引

**横向不变量抽检（R30 基准未被本窗口破坏）**
- refusal：`internal/ir/response.go:503-509`（R21）与 `:627-632`（R30 refusal→text）在位，窗口 diff 未触及 refusal 代码
- sessionsummary 多模态块抽取 v2ContentText 在位（`message_source_v2.go:165-205`），窗口未触及该包
- JSONB：窗口内本域无新增 JSONB 写入点（admin `tags…::jsonb` 为存量行）；R30 基准无破例

## 三、未覆盖项与原因

- **go build / go vet / go test 未执行** —— 只读纪律禁止改变系统状态；wiring/P5/decide 测试的通过性仅靠读码确认结构，主代理收尾时应跑三门
- **legacy 兜底回调（AnthropicToOpenAI/ChatToAnthropic 非 IR 路径）的 thinking 全行为** —— 仅断路器 OPEN 时触发的窗口外存量路径，只抽读了请求级 thinking 缺失一点（见备注）
- **/v1/responses 入向 → chat 形态上游全链路**（native_responses / responses dispatch policy）—— 超出窗口改动面；effort 形状经 `reasoning_effort` 原生键往返理论无损，R30 遗留 #7（reasoning.summary 数组、消息级 function_call）窗口未触及、未复查
- **MiniMax 对 `{"type":"adaptive","budget_tokens":N}` 的真实接受度**（F-4 定级依据）—— 需要真机凭据构造受控请求
- **R33–R42 已覆盖的更早 48h 文件** —— 按派发指令只抽检横向不变量（refusal/JSONB/sessionsummary/resolve），未重走全清单
- **gemini→gemini 同协议经 openai body 往返的 thinkingConfig 保真** —— 与 F-1 同一边界，但属窗口外国有设计，未单独核查

**主代理复核结论（R43）**：F-1 成立（P1）→ 本轮已补 `reasoning.budget_tokens` loss 上报 + 双向钉桩（完整 TargetProvider 前送修复登记遗留）；F-2 成立（P3）→ 已补两行钉；F-3/F-4 登记遗留（未真机证实）。
