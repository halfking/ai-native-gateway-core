# 修复报告 · pkg1 · internal/ir file_id 双轨收口（对照 audit-pkg1-ir.md）

- 基线：`203021f33`（main）；本修复未 commit，仅工作区改动
- 改动文件（全部在 `internal/ir/` 下）：
  - `internal/ir/serialize_openai.go`（P1-1 / P3-2 / P3-5 / P2-1）
  - `internal/ir/serialize_responses.go`（P1-1 / P3-2）
  - `internal/ir/serialize_anthropic.go`（P1-2）
  - `internal/ir/parse_openai.go`（P3-3，仅注释）
  - `internal/ir/ir_fileid_gap_test.go`（P2-2，新增 7 个用例）

---

## 逐项修法

### P1-1 Anthropic 原生 Type="file" 文档路由 OpenAI Chat / Responses 丢 file_id —— 已修

- `serialize_openai.go:686`（serializeOpenAIDocumentBlock）：`case "file_id"` 改为 `case "file", "file_id"`，取值 `fid := FileID` 优先、空则 `Data` 兜底（:699-702 注释块）。Anthropic 原生 file 文档（parse_anthropic.go:506/:654 原样存 Type="file" 并填 FileID）不再只剩 filename。
- `serialize_responses.go:410`（buildResponsesInputFile）：同款合并 `case "file", "file_id"`（:410-425），Type="file" 不再落入 default 输出空 `file_data:""`。
- 两处语义与 serialize_anthropic 的同构 switch（message 级 :656-689 / 顶层 :862-899）对齐：file/file_id 归一输出、FileID 优先、Data 兜底。

### P3-2（随 P1-1）空值护栏 + 双空身份丢失上报 —— 已修

- 护栏：`serialize_openai.go:700-703` 兜底后 `fid == ""` 时不再输出 `file_id:""`（退回 filename-only 现状行为）；`serialize_responses.go:423-426` 同理省略空字段（不再输出 `file_data:""`）。与 serialize_anthropic 侧 `if fid != ""` 护栏一致。
- 上报（「跳过/丢失必上报、上报必真实丢失」）：
  - `serialize_openai.go:785-801`（reportSerializeOpenAILosses）：`block.Document.Source.Type ∈ {file, file_id}` 且 FileID、Data 双空 → `ReportProtocolLoss`，路径 `messages[i].content[j].document.file_id`，target=OpenAI Chat，metadata 带 message_index/content_index。与上方 image.file_id 检查同款无 same-protocol guard 的写法与 dedup 惯例（dedup key 含 FieldPath，多位置不互相吞并）。
  - `serialize_responses.go:714-728`（reportSerializeResponsesLosses）：同条件同路径，target=OpenAIResponses，落在该函数既有的 per-message 循环内（继承其函数级 same-protocol Responses 提前返回；该退化形状 parser 不可达，不影响同协议 roundtrip）。

### P1-2 serialize_anthropic message 级 document switch 无 default —— 已修

- `serialize_anthropic.go:680-690`：message 级 document switch 补 `default`——`Data != ""` 投影 `source["data"]`、`URL != ""` 投影 `source["url"]`，与本次已在顶层 serializeAnthropicDocuments 实现的 default（:882-889）逐行同构。text 型 source（Anthropic 真实 wire 形态 `{"type":"text","media_type":...,"data":...}`）同协议 roundtrip 不再丢正文；wire 的 source.type 保持解析时的 "text" 原样。

### P3-3 parse_openai 注释指向真实惯例出处 —— 已修

- `parse_openai.go:783-793`：删去不实的「same convention as the URL→Data projection above」（本函数 URL 分支 :772-774 只写 URL 不写 Data），改为指向真实出处：parse_anthropic 的 source.url → Data 投影（parseAnthropicDocumentBlock，parse_anthropic.go:511-514）与 parse_gemini 的 fileUri 处理。行为无变化，纯注释修正。

### P3-5 image loss 条件补 Type 守卫 —— 已修

- `serialize_openai.go:776`：loss 条件加 `block.Type == "image" &&` 前置守卫，防未来非 image 块携带 Image 字段时「wire 原样输出却报丢失」的理论性误报。现有可达路径行为不变（parser/adapter 只在 Type="image" 块上填 Image）。

### P2-1 tool_result 嵌套图片 loss 覆盖 —— 已实现

- 取舍：实现。实现方式是既有 per-block 循环内的一段小型定长嵌套循环（`serialize_openai.go:803-822`），不是通用递归 walk——Anthropic tool_result 只有一层 Content 子块，无需通用递归，loss 循环复杂度无明显上升。
- 行为：`block.ToolResult != nil` 时遍历 `ToolResult.Content`，凡 `cb.Type == "image"`（Chat 三条 tool 路径均只提取 text，嵌套图片 URL/base64/file_id 一律真实丢失）→ `ReportProtocolLoss`，路径 `messages[i].content[j].tool_result.content[k].image`，metadata 带 tool_content_index。嵌套 text 存活、不误报。置于 `src == ProtocolOpenAIChat continue` 之前（同 image.file_id：session 恢复的同协议来源下丢失同样真实）。
- 边界说明：Responses 侧 buildResponsesFunctionCallOutput 同样把 tool_result 折叠为文本，但 P2-1 字面范围与本次实现均限 reportSerializeOpenAILosses；Responses 侧留作后续（预存在行为，未恶化）。

### P2-2 测试补钉 —— 已补（ir_fileid_gap_test.go，新增 7 用例）

| 用例 | 覆盖 |
|---|---|
| TestAnthropicNativeFileDocumentToOpenAIChatAndResponses | a) P1-1 回归钉：Anthropic 原生 Type="file" 文档 → Chat 输出 `file_id`（无 `file_id:""`）；→ Responses 输出 input_file `file_id`（无 `file_data:""`）+ 同协议 re-parse 保真 |
| TestAnthropicTextSourceDocumentRoundTrip | b) P1-2 回归钉：text 型 source 文档 parse→serialize roundtrip，type/media_type/data 三字段全保真 |
| TestOpenAIChatFileIDWithURLImageNoFalseLoss | c) FileID+URL 并存图片 → Chat 输出 image_url 且 anomaly 全零（钉住 :776 守卫与 `URL==""` 条件） |
| TestLegacyDataFileIDDocumentFallbackOnChatAndResponses | d) session adapter 折叠形状（Type="file_id"、Data=id、FileID=""）→ Chat 与 Responses 双双 Data 兜底输出 file_id，且无 loss 误报 |
| TestDocumentFileIDPrecedenceOverData | e) Data+FileID 并存 document → Chat / Responses / Anthropic 三处均 FileID 优先、stale Data 不出现在 wire |
| TestEmptyFileIDDocumentLossReported | P3-2 护栏钉：双空 file 文档两 wire 均无空字段，且 Chat/Responses 各自上报 `messages[0].content[0].document.file_id` loss |
| TestOpenAIChatNestedToolResultImageLoss | P2-1 钉：嵌套 tool_result 图片不出现在 wire 且按嵌套路径上报 loss |

---

## 验证输出

```
$ go build ./... && go vet ./internal/ir/...
（通过，无输出）

$ go test ./internal/ir/... -count=1 | tail -3
ok  	github.com/kaixuan/llm-gateway-go/internal/ir	0.457s

$ go test ./domains/session/... ./domains/transformation/... -count=1 | grep -v "^ok" | head -3
（无输出，全部 ok）

新增 7 用例逐个 -run 验证：全部 PASS（-v 逐条确认 RUN/PASS）。
```

全仓 `go test ./...` 附加跑了一轮：除 `plugin-runtime TestExecCommand_GracefulStopSIGTERM`（进程信号时序 flaky，隔离重跑 `ok 0.861s`，与本修复无关、文件零交集）外无失败。注：工作区同时存在其他并行修复代理对 `storage/`、`config/`、`bg/` 的改动，本修复未触碰这些文件。

## P2-1 取舍结论

已实现（理由见上）：一是实现代价低（一层定长循环，非通用递归）；二是无测试破坏面（全仓无既有 tool_result 图片 fixture）；三是与「丢失必上报、上报必真实丢失」原则一致——嵌套图片在 Chat wire 上确实被丢，上报皆为真实丢失。Responses 侧同型缺口（function_call_output 折叠）超出本轮范围，记录备查。
