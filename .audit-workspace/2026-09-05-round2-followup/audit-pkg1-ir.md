# 审计报告 · pkg1 · IR file_id 双轨收口（A-#18 跟进）

- 审计对象：`203021f33` 中 `internal/ir/` 的改动（`git diff 5378324de..203021f33 -- internal/ir/`）
- 涉及文件：parse_anthropic.go、parse_openai.go、serialize_anthropic.go、serialize_openai.go、serialize_responses.go、ir_fileid_gap_test.go（新增 331 行 / 7 用例）
- 审计方式：逐项读 diff + 周边实现（四协议 parse/serialize 全查、session 恢复链路、request_document_codec、gemini 序列化、anomaly dedup），非仅看报告
- 只读纪律：未修改任何被审计文件，未 commit
- 测试：`go test ./internal/ir/... -count=1` → **ok（全绿）**；7 个新用例逐个 `-run` 验证均 PASS

---

## 一、逐项结论（对照需求基准）

### 需求 1：Anthropic 顶层 documents 的 file 源可解析且序列化完整 —— 达成

- 解析：`parse_anthropic.go:663` 新增 `doc.Source.FileID, _ = source["file_id"].(string)`（顶层 `parseAnthropicDocuments`），与 message 级 `parseAnthropicDocumentBlock`（`parse_anthropic.go:510`）同形。
- 序列化：`serialize_anthropic.go:862-871`（`serializeAnthropicDocuments`）重写为与 message 级同构的 switch，`Type ∈ {file, file_id} || FileID != ""` → `source["type"]="file"` + `file_id`（FileID 优先、Data 兜底）；不再输出截断的 `{"source":{"type":"file"}}`。
- base64 兄弟块隔离验证：`ir_fileid_gap_test.go` TestAnthropicTopLevelDocumentsFileIDRoundTrip 断言 base64 兄弟块不受 file 分支劫持，PASS。
- 调用点确认：`serialize_anthropic.go:142-143`（`len(req.Documents) > 0` 时调用）。

### 需求 2：双轨编码统一 —— 达成（四个具名消费点全部 FileID 优先、Data 兜底）

全部 FileID/Data 读写点核查结果：

| 消费点 | 位置 | FileID 优先 | Data 兜底 |
|---|---|---|---|
| serialize_anthropic message 级 document | serialize_anthropic.go:657-669 | ✓ | ✓（仅当 Type∈{file,file_id}） |
| serialize_anthropic 顶层 documents | serialize_anthropic.go:863-871 | ✓ | ✓（同上） |
| serialize_openai document | serialize_openai.go:686-694 | ✓ | ✓（无条件兜底） |
| serialize_responses input_file | serialize_responses.go:410-418 | ✓ | ✓（无条件兜底） |

- OpenAI 解析写 FileID：`parse_openai.go:783-793`（`src.Type="file_id"; src.FileID=fid; src.Data=fid` 双写）。
- Data 双语义混淆排查：**无混淆**。Data 的语义由 `Type` 判别（url→URL 投影 / file_id→id 投影 / base64,text→payload），四协议所有消费者都先 switch `Source.Type` 再读 Data；不存在任何「不看 Type 直接把 Data 当 URL 或当 base64」的路径。单一 DocumentSource 上 Type 互斥，url 投影（Type="url", Data=url，见 parse_anthropic.go:511-514、parse_gemini.go:356）与 file_id 投影（Type="file_id", Data=fid）不会同时落在同一实例上。gemini 侧 `irDocumentToGeminiPart`（serialize_gemini.go:531-544）对 Type="file_id" 走独立 case（URL 空则返回 nil，不误把 Data 当 fileUri），`irImageToGeminiPart`（381-407）同理。
- 旧 Data 编码（session 恢复）不坏：`domains/session/v2/ir_message_adapter.go:1121-1123` 确实把 `src.Type, src.Data = "file_id", src.FileID` 折叠进 Data（且 `decodeDocumentBlock` 恢复出的 DocumentSource **不含 FileID**，见 1139-1143 的字面构造），四个消费点的 Data 兜底恰好覆盖该形状；注释所述行为属实。新行走 `irBlocksToRaw → marshalOrNil(b.Document) → json.Marshal(DocumentBlock)`，`DocumentSource.FileID` 带 `file_id,omitempty` tag（types.go:507）原生持久化，读回 `irBlocksFromRaw`（ir_message_adapter.go:650-657）原生还原，无折叠。
- request_document_codec：顶层 `req.Documents` 以 `[]Document` 整体直传（request_document_codec.go:243/407），FileID 全保真；仅 `system.PDFs` 走 `pdfSourceWire/PDFSource`（无 FileID 字段，见发现 P3-8）。

### 需求 3：file_id 图片 —— 达成

- Responses 原生输出：`serialize_responses.go:331-343`，`FileID != ""` → `{"type":"input_image","file_id":...,"detail"?}`；与 `parseResponsesInputImage`（parse_responses.go:506-509，file_id 覆盖 Type="file_id"）优先级一致，同协议 roundtrip 双段断言（含 re-parse）测试 PASS。
- OpenAI Chat 跳块：`serialize_openai.go:389-395`，`url == "" && FileID != ""` → `continue`，不再输出 `image_url:""`；测试断言 wire 上无空 image_url、内容只剩 text 块。
- URL/base64 零行为变化：skip 条件要求 `url==""`，而 `url` 由 URL 优先、Data 重建 data URI（380-387），故 URL 图片或 base64 图片（含 FileID 同时存在的边缘）仍走原路径输出 image_url（TestOpenAIChatURLImageUnaffected 钉住）。

### 需求 4：无新增静默丢失 / loss 不误报 —— 基本达成（两处预存在的相邻缺口见发现清单）

- 「跳了但没报」排查：Chat 侧 skip 条件 `url=="" && FileID!=""`，其中 `url==""` ⟺ `URL=="" && Data==""`（Data 会重建 data URI）；loss 条件（serialize_openai.go:762）为 `Image!=nil && FileID!="" && URL=="" && Data==""`。两者在 `block.Type=="image"` 的可达路径上**逻辑等价**，无单侧路径。
- 「报了但没跳」排查：loss 条件不检查 `block.Type=="image"`，理论上 `Type!="image" && Image!=nil && FileID!=""` 的块会「经 RawContent 原样输出却被报丢失」；但四协议 parser 与 session adapter 均只在 Type="image" 的块上填 Image（parse_anthropic.go:478-491、adapter decodeContentBlock ir_message_adapter.go:1024-1043），不可达。仅理论性，记 P3。
- FieldPath 对齐：loss 循环与序列化同源遍历 `req.Messages`/`msg.Content`，且 `serializeOpenAIMessages`（serialize_openai.go:221-238）**不丢弃任何消息**（空 content 消息也保留），`fieldPathMessageContent(i, j, ...)` 与 IR 源索引严格对齐。dedup key 含 FieldPath（anomaly_reporter.go:257-269），多个不同位置的 file_id 图片各自上报，不会被 dedup 吞并。
- 顶层 file_id 文档 → Chat/Responses/Gemini：分别有显式 loss（serialize_openai.go:815-825 documents、serialize_responses.go reportSerializeResponsesLosses documents、serialize_gemini.go:195-202），非静默。

---

## 二、发现清单

### P1-1 Anthropic 原生 Type="file" 文档路由 OpenAI Chat / Responses 仍丢 file_id，Responses 侧还输出空 file_data（预存在，A-#18 同族反向缺口）

- 证据：`parse_anthropic.go:506`（message 级）/`:654`（顶层）把 wire 的 `source.type` 原样存为 **`"file"`** 并填 `FileID`（:510/:663）；全仓无任何 `Type "file"→"file_id"` 归一化（已 grep 确认）。而 `serializeOpenAIDocumentBlock`（serialize_openai.go:669-697）switch 只有 `base64/url/file_id/text` 四个 case，**无 "file" case 也无 default**：fileInner 只剩 filename，`{"type":"file","file":{"filename":...}}` 上游，file_id 静默丢失。`buildResponsesInputFile`（serialize_responses.go:395-425）Type="file" 落 default → `inner["file_data"] = doc.Source.Data`（空）→ 输出 `{"type":"input_file","file_data":""}`，同样丢 id 且带坏字段。两处均**无 loss 事件**（reportSerializeOpenAILosses 对 message 级 document 块无任何检查）。
- 影响：Anthropic 客户端 Files API 文档 → OpenAI/Responses 上游请求坏/丢文件，静默。本次提交只把 serialize_anthropic 两个 switch 补成同时认 `file`/`file_id`，OpenAI/Responses 两个消费点没有做对称补齐。
- 定级说明：预存在、非本次引入、不在四条需求的字面范围内，故 P1（应修）而非 P0；但与本修复同族且方向互补，建议随下轮收口。
- 建议修法：两处 `case "file", "file_id":` 合并，取值 `FileID` 优先、`Data` 兜底（与 serialize_anthropic 完全同构）；或引入统一归一化（parse 后把 Type="file" 规范为 "file_id"）。

### P1-2 serialize_anthropic message 级 document switch 无 default：text/csv 等类型的 payload 静默丢失（预存在；本次修了顶层却没修 message 级，两平行 switch 不一致）

- 证据：`serialize_anthropic.go:656-680` 仅有三个 case（file/file_id/FileID、base64&&Data、url），**无 default**。Anthropic 线上协议的纯文本文档 `{"type":"document","source":{"type":"text","media_type":"text/plain","data":"..."}}` 是真实形态，parse 得 `Type="text", Data=正文`（parse_anthropic.go:506-508），序列化回到 Anthropic 时 source 只剩 `{"type":"text"}`，**正文同协议 roundtrip 丢失**（base64 空 Data 同样穿透所有 case）。对比：本次提交给顶层 `serializeAnthropicDocuments` 加了 default 投影 Data/URL（serialize_anthropic.go:882-889），message 级没加。
- 影响：同协议（Anthropic→Anthropic）文本文档正文丢失，静默；跨协议到 Gemini 时 `irDocumentToGeminiPart` 的 "text" case 读 Data 也拿不到（数据在 parse 后就只在 IR 里，serialize_anthropic 丢的是输出侧，IR 本身未损，其他目标不受影响）。
- 定级说明：预存在（本提交未触碰该缺失分支的覆盖面），P1。
- 建议修法：message 级 switch 补与顶层一致的 `default`（投影 Data/URL）。

### P2-1 嵌套在 tool_result.Content 内的 file_id 图片（及一切图片）被丢且无 loss 事件（「丢了但没报」）

- 证据：Anthropic tool_result 官方支持图片子块。Chat 序列化两条路径都只提取 text：`serializeOpenAIMessage` tool 路径（serialize_openai.go:249-279）与 tool role 路径（:291-308）、`serializeOpenAIMessageContent` 的 tool_result case（:435-450）；而新增 loss 循环只遍历 `msg.Content` **顶层**块（:754-755），不递归 `block.ToolResult.Content`，嵌套 file_id 图片既不出现在 wire 也没有 `ir_protocol_loss`。
- 影响：低频（tool_result 内 file_id 图片罕见）但属本次「显式上报」承诺的覆盖缺口；嵌套 URL/base64 图片的静默丢失是更早的预存在行为，本次未恶化。
- 建议修法：loss 循环递归 ToolResult.Content（索引路径如 `messages[i].content[j].tool_result.content[k]`）；或至少在文档中声明该边界。

### P2-2 测试缺口：四个关键组合未钉住

- a) **FileID+URL 并存图片 → Chat**：应输出 image_url 且**不报 loss**（serialize_openai.go:762 的 `URL==""` 守卫是防误报关键，无回归测试保护；若有人把守卫删掉，现有 7 个用例全绿）。
- b) **Data+FileID 并存 document**：四消费点的 FileID 优先级（如 parse_openai 双写后 FileID 变更/Data 遗留旧值时取谁）无断言。
- c) **serialize_openai / serialize_responses 的 Data 兜底分支**（legacy 行 Type="file_id"、FileID=""，即 session adapter 折叠形状）：仅在 serialize_anthropic 顶层有 legacy 测试（TestAnthropicTopLevelDocumentsLegacyDataFileID），Chat/Responses 两处兜底零覆盖。
- d) **仅含 file_id 图片的消息**：整块被跳后 `content` 序列化为 `[]`（serialize_openai.go:327-328 空 slice），部分上游会拒收空 content 数组；该边界未测、未文档化。
- 建议修法：补 4 个表驱动用例；d) 可考虑全跳时回退输出 text 占位或保留空数组并在注释中钉死契约。

### P3-1 Responses 图片 FileID 优先导致 image_url+file_id 并存时 URL 被静默弃用

- `buildResponsesInputImage`（serialize_responses.go:331-343）FileID 命中即 return，URL 不输出、无 loss 事件。与 parse 优先级对称（parse_responses.go:506-509 file_id 覆盖 Type），且旧代码是对称地静默丢 file_id，属「丢哪半边」的选择而非新增丢失类别；image_url+file_id 并存本身是畸形输入。记录备查。

### P3-2 serialize_openai / serialize_responses 的 file_id 分支缺空值护栏，与 anthropic 侧不一致

- `serialize_openai.go:690-694`、`serialize_responses.go:414-418` 在 FileID、Data 均空时输出 `file_id:""`；serialize_anthropic 两处有 `if fid != ""` 护栏（:667-669、:869-871）。仅程序化构造的退化 IR（Type="file_id" 且无值）可达，parser 与 session adapter 均不会产生。建议补齐同款护栏。

### P3-3 parse_openai.go:787-789 注释指向不存在的「上方 URL→Data 投影」

- 注释称 Data 双写「same convention as the URL→Data projection above」，但 `parseOpenAIFileBlock` 的 URL 分支（parse_openai.go:772-774）**只写 URL 不写 Data**；URL→Data 投影惯例实际在 parse_anthropic.go:511-514 与 parse_gemini（fileURI→Data）。行为正确，注释误导后人，建议改为指向上游惯例出处。

### P3-4 system.PDFs 模型（PDFSource）从未建模 file source；request_document_codec 对其编码时 FileID 无处安放

- `PDFSource`（types.go:225-230）只有 Type/MimeType/Data/URL；`pdfSourceWire`（request_document_codec.go:105-110）与编解码转换（:373-374、:464-465）同样无 FileID。当前无 parser 会往 system.PDFs 塞 file 源，正交缺口。顺带：serialize_anthropic.go:334 系统 PDF 输出的是 `mime_type`（下划线）而非文档块的 `media_type`，疑似更早的 wire 拼写问题，不属本轴，仅记录。

### P3-5 理论性误报路径：非 "image" 类型块携带 Image 时 loss 会误报

- loss 条件（serialize_openai.go:762）不含 `block.Type=="image"` 检查；若未来有 parser 在 Type="raw" 等块上填 Image（经 RawContent 原样输出），会出现「输出了却报丢失」。当前所有 parser/adapter 均不可达，建议加一个 `block.Type == "image"` 守卫作为防御。

---

## 三、需求达成总评

| 需求 | 结论 |
|---|---|
| 1. 顶层 documents file_id 解析+完整序列化 | ✅ 达成，测试钉住 |
| 2. 双轨统一 + 四消费点 FileID 优先/Data 兜底 + 旧编码不坏 | ✅ 达成（具名四点全覆盖；session 恢复形状核实无误） |
| 3. file_id 图片 Responses 原生 + Chat 跳块上报 + URL/base64 零变化 | ✅ 达成（跳块与上报条件逻辑等价，无双报/漏报） |
| 4. 无新增静默丢失 / 不误报 | ✅ 本次改动本身达标；两处 P1 为**预存在**同族缺口，非本次回归 |

`go test ./internal/ir/... -count=1` 全绿。无 P0。
