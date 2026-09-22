# D01 IR 数据结构与生命周期 子代理报告（窗口：119981c02..5dc4e9f3b）

> 主代理复核结论（2026-09-22）：#1/#2/#3 成立并已修复（R52-F1：serialize 发射方言门控 + Decide/KnownFieldsForDialect 守卫恢复 + repetition_penalty Dialects 摘除；R52-F14：bot_setting 畸形输入回退 Extensions）；#4 成立并已修复（R52-F6：SerializeGeminiResponse 补 ReasoningContent→thought）；#5 已 RESERVED 标注（R52-F12）；#6/#7 随修勘误；#8 随钉桩补齐。#2 的 KnownFieldsForDialect 断言经主代理亲读 rejects()/knownBy() 确认成立（rejects 只查 RejectedBy，Kind 升级后 Dialects 约束失效）。

**先决事实（影响全部核查点）**：提交 5dc4e9f3b 的提交信息与实际 diff 完全不符。信息声称"新增 reasoning_content 支持（InternalRequest/InternalResponse.ReasoningContent、StreamChunk.ReasoningDelta、TestParseOpenAIReasoningContent 6 子测试）"，实际 diff 内容是：MiniMax/vLLM 请求参数提升（RepetitionPenalty/MaskSensitiveInfo/BotSetting）、`ProtocolOllamaChat` 常量、`StreamChunk.CumulativeContent` 字段、paramreg 三个字段 Kind 升级。ReasoningContent 早在窗口前就存在（`internal/ir/response.go:40`、`internal/ir/stream.go:109`，字段名为 `StreamChunk.Delta.ReasoningContent`，不存在顶层 ReasoningDelta）。以下核查按**实际 diff** 展开。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P1 | serialize_openai 注释声称的"方言守卫"不存在，MiniMax 专有字段无条件发给所有 openai-chat 上游。注释称 "dialect-aware guard in extensions_restore (paramreg) will strip them"，但 `restoreExtensions` 只遍历 `req.Extensions`，而这三个 key 已被 parse 从 Extensions 剔除——守卫对 IR 字段发射路径**永不生效**。触发路径：openai-chat 客户端发 `mask_sensitive_info`/`bot_setting` → 路由到非 MiniMax 上游 → 字段无条件序列化进上游 body → 严格上游 400 或参数泄漏 | internal/ir/serialize_openai.go:157-176；internal/ir/extensions_restore.go:47-51,72；internal/ir/parse_openai.go:83；对照改前 registry.go（KindDialectOnly） | 发射处补目标方言判定或回退 KindDialectOnly；钉桩回归 |
| 2 | P2 | KindDialectOnly→KindIRHandled 使 `KnownFieldsForDialect` 对所有方言放行 mask_sensitive_info/bot_setting（strict 白名单模式失守）；非 openai 入向时 Extensions 路径 Decide 由 Drop 变 Restore | internal/paramreg/decide.go:209-211、118-134；domains/transformation/sanitizer.go:74 | 同 #1 一并处置 |
| 3 | P2 | bot_setting 保真回归 + 畸形输入静默吞：parse 端 Unmarshal 失败整字段静默丢弃（无 anomaly/loss 上报）且无透传兜底；serialize 端只回写 {bot_name,content} 两键，其余键坍缩 | internal/ir/parse_openai.go:168-172；internal/ir/serialize_openai.go:169-175 | parse 失败走上报/Extensions 兜底；钉桩畸形输入测试 |
| 4 | P2（跨窗口，R30 同构） | ReasoningContent "parse 保留、serialize 丢弃"：SerializeGeminiResponse 不读 ir.ReasoningContent（ParseOpenAIResponse 只存字段不产 thinking 块）→ DeepSeek-R1/GLM 上游 → Gemini 客户端非流式丢 reasoning；流式路径却发射（不对称） | internal/ir/response.go:443-444、1187-1224；对照 :556-557 / :989-995 / stream.go:1146-1152 | 补 ReasoningContent→thought 分支；钉桩 |
| 5 | P3 | 死代码：ProtocolOllamaChat 与 StreamChunk.CumulativeContent 全仓零生产者/零消费者（Ollama 半落地） | internal/ir/types.go:35-40；internal/ir/stream.go:53-59 | RESERVED 标注或补齐 |
| 6 | P3 | 注释漂移三处：parse_openai.go:10-13 包头注释与提升后矛盾；:57-59 称提升目的"type-checked, normalized"无实现；serialize_openai.go:157-160 守卫注释不实 | 同左 | 随 #1 修复一并改正 |
| 7 | P3 | 提交信息与 diff 全面不符（流程/可追溯性）：声称的 ReasoningContent/ReasoningDelta/TestParseOpenAIReasoningContent 均不存在；尾部 "2026-09-21 audit" 字样易误认为审计轮产物 | git show 5dc4e9f3b | 轮文档登记；后续提交信息核验 |
| 8 | P3 | 测试盲区：无畸形 bot_setting 用例；无 mask_sensitive_info/bot_setting Extensions 剔除断言；核心声明"只到达 MiniMax/vLLM 系上游"零测试且为假 | internal/ir/parse_openai_test.go:492-634 | 随钉桩补齐 |

## 二、核实为健康的面

- reasoning 流式桥全通过：空流门把 reasoning 计为语义输出（stream.go:99）；anthropic_stream.go:593、anthropic_bridge.go 五处、responses_bridge.go 六处（有界聚合）、Q2 非流式桥 chat_to_anthropic_response.go:64-109、empty_response.go 双处均消费。
- reasoning 流式四协议序列化器对齐：openai SSE 743-744、anthropic thinking_delta 958-964、gemini thought part 1146-1152、responses thought 1325-1326。
- R30 refusal 修复仍完好（response.go:1224-1231、responses 输出并入 text 流）。
- paramreg 提升无响应侧回显：三字段仅请求侧；paramledger 零引用；无回写路径。
- 聚合与 usage 无不良交互：reasoning token 只来自上游 usage 帧（usage.go:89,94）；responses_bridge 聚合有字节上限。
- 未知字段前向兼容未破：Decide 对 KindIRHandled 刻意 ActionRestore 的 reasoning 反例注释在位；Extensions 剔除+IR 发射无双重发射（有测试钉住 repetition_penalty）。
- JSONB 不变量不受影响（本提交无 DB 写入点）。
- 03b439798 的 ir 触点仅 response.go 归一化小修。

## 三、未覆盖项与原因

- 未执行 go test/go vet 复验（只读约束）——主代理已补跑，全绿。
- 生产 catalog 实际路由验证与严格上游 400 实证——需真机/真库，主代理按 registry RejectedBy 机制与 spec.go 注释定性。
- DeepSeek Code 多轮历史 per-message reasoning_content 回传——D02 客户端格式线索，未逐一核对。
- paramledger F1/F12 全流程未重审——归 D16 复核域（已另行覆盖）。
