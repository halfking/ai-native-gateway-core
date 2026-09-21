# D17 代码卫生与结构 子代理报告（窗口：119981c02..5dc4e9f3b）

> 主代理复核结论（2026-09-22）：#1 留档（提交信息失真，轮文档 §专记）；#2 已 RESERVED 标注（R52-F12）；#3 已清理（maskSensitiveInfoPtr 删除 / bytesContains→bytes.Contains / var _ = json.Marshal 删除）；#4 登记顺延（UA 三副本 SSOT 收敛为低风险重构，zcode 既有裸词跨窗口不动）；#5 登记顺延（弱 UA 名单双副本，注释已互指）；#6 已回改（body_client_marker.go 链序注释按 R52 后实现更新）；#7 已回改（deepseek-code.md 符号 / conversion-audit.md 符号与 UA 方案与收紧语义）。大文件/长函数清单收档下方，作为后续清理候选。

窗口内 15 个非 merge 提交，改动面约 100 文件；HEAD=5dc4e9f3b。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | 提交元数据漂移（本窗口最重发现）：5dc4e9f3b 提交信息与实际 diff 完全不符（声称 reasoning_content，实际 MiniMax 字段提升+CumulativeContent+Ollama 常量+Kind 升级）；ReasoningDelta 符号全仓不存在；TestParseOpenAIReasoningContent 计数 0 | commit 5dc4e9f3b | 轮文档留档；push 前自查 message↔diff |
| 2 | P3 | 零调用接缝 ×2（RESERVED 候选）：StreamChunk.CumulativeContent、ProtocolOllamaChat 全仓零引用 | internal/ir/stream.go:59；internal/ir/types.go:40 | RESERVED(未接线) 头注 |
| 3 | P3 | 测试文件死代码/重复实现（窗口新增）：maskSensitiveInfoPtr 零调用；bytesContains 手写重复 bytes.Contains（注释自称的理由不成立）；var _ = json.Marshal 冗余（json 在文件内本有真实使用） | internal/ir/parse_openai_test.go:557,595-597,635 | 删三处 |
| 4 | P2候选 | UA 识别模式三处副本且口径分叉：client_fingerprint.go 锚定式 / executor.go 镜像 / request_metadata.go 裸子串式；UA 恰为 "minimax-code"（无版本）时 telemetry 与 streaming 两轨道结论不同 | domains/streaming/client_fingerprint.go:32-70；domains/streaming/executors/executor.go:1034-1046；telemetry/request_metadata.go:371-397 | 提取共享匹配函数单点维护或统一口径互指 |
| 5 | P3 | 弱 UA 名单双副本：shouldOverrideWithBodyMarker 与 shouldOverrideAgentName 重复（新副本注释已声明 mirror） | request_log_pipeline.go:598；request_meta.go:149 | 提取到 clienttype 单点 |
| 6 | P3 | 识别链顺序注释互相矛盾：body_client_marker.go 写 header→UA→body→system-prompt；request_log_pipeline.go 写 header→system-prompt→body-marker；EnsureCaptured 实际 system-prompt 在先、body-marker override 在后 | body_client_marker.go:19；request_log_pipeline.go:580 | 以代码为准回改其一 |
| 7 | P3 | client-formats 文档漂移：deepseek-code.md:136 称 `IR.StreamChunk.ReasoningDelta`（符号不存在）；conversion-audit.md:145 `ExtractClientNameFromBody` 与实现 `ExtractClientTypeFromBody` 不符；audit.md:82-83 建议裸 "deepseek-" UA 匹配与实现 6 锚定形式不一致 | docs/client-formats/deepseek-code.md:136；conversion-audit.md:145,82-83 | 按实现回改 |
| 8 | P3候选 | IR 发射依赖跨包守卫：serialize_openai.go 注释自认无条件写入、靠 paramreg extensions_restore 剥离——发射正确性取决于另一模块守卫存在 | internal/ir/serialize_openai.go:156-160 | paramreg 侧补方言剥离钉桩（本轮 R52-F1 已修+钉） |

## 二、清理候选清单（只标记不动）

窗口改动面内超 800 行源文件：executor.go 3683 / node_probe.go 3109 / executor_chat.go 2341 / proxy/manager.go 2030 / messages.go 1583 / ir/stream.go 1446 / installer main.go 1437 / ir/response.go 1344 / serialize_openai.go 1230 / discovery.go 1152 / parse_openai.go 823。

超 150 行函数（粗扫）：executor_chat.go:348 executeOpenAI 1451；messages.go:71 ServeHTTP 764；executor.go:2067 Execute 556；node_probe.go:2028 runOne 323；executor_chat.go:1809 finalizeOpenAIUpstreamBody 313；node_probe.go:1487 ProbeSync 293；serialize_openai.go:920 reportSerializeOpenAILosses 271；manager.go:1319 healthCheckAllNodesInner 265；parse_openai.go:14 ParseOpenAI 225；installer main.go:817 runInstall 225；stream.go:189 ParseOpenAIStreamChunk 224；serialize_openai.go:13 SerializeOpenAI 219；stream.go:460 ParseAnthropicStreamEvent 218；discovery.go:660 upsertModel 184；stream.go:843 SerializeAnthropic 178；response.go:323 ParseOpenAIResponse 166；stream.go:682 SerializeOpenAI 159；admin/telemetry.go:282 persistRequestLog 157；manager.go:320 selectNodeExcluding 154。

## 三、核实为健康的面

- body_client_marker 职责边界总体清晰（body 参数/UA/系统提示词三处分离）；request_metadata.go:425 的 body-aware 包装是复用非重写；唯一重叠即发现 4。
- ReasoningContent 无窗口内新增重复表达；messages.go/responses.go/sanitizer.go 的非标 reasoning_content 路径均窗口外既有。
- docs sweep 无漏项：735 已入 db-changelog（396f708c0 补入，时序正常）、733 已入；count_tokens 已补 architecture.md。
- TODO/FIXME/XXX 新增扫描：窗口 diff 零真实新增标记。
- 窗口内行为变更的注释同步良好（parser.go/coordinator.go/manager.go）。

## 四、未覆盖项与原因

- web/（Vue）不在本域范围；installer embeddata SQL 内容归迁移域；clientTokenOf/ClientTokenOf 双胞胎窗口外既有已互注；大函数行数粗扫 ±数行。
