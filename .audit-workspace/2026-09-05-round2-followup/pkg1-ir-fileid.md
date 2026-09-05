# A-#18 修复报告：IR 层 file_id 三缺口收口

- 分支 main（HEAD cab1bbf8c），改动仅限 `internal/ir/`（未 commit，遵守文件权限互斥）。
- 审计依据：`.audit-workspace/2026-09-05-round2/axis-A-ir-protocol.md` A-#18 (a)(b)(c)。

## 缺口 a：顶层 documents 不读/不写 file_id

- `internal/ir/parse_anthropic.go:660`（parseAnthropicDocuments）：source 解析补读 `file_id` → `DocumentSource.FileID`，与 message 级 parseAnthropicDocumentBlock 同型。
- `internal/ir/serialize_anthropic.go:848-905`（serializeAnthropicDocuments）：重写 source 序列化为与 message 级 document 块一致的 switch——`type=="file"|"file_id"` 或 FileID 非空时输出 `{"type":"file","file_id":...}`；base64 输出 data；url 输出 url（Data 兜底，保持 pre-canonical 行兼容）；text/默认走原 data/url 投影。不再出现残缺的 `{"source":{"type":"file"}}`。

## 缺口 b：OpenAI/Anthropic 双轨编码冲突

- `internal/ir/parse_openai.go:783-793`（parseOpenAIFileBlock）：file_id 现在写 `DocumentSource.FileID`（与 Anthropic 解析统一）；同时保留 `Data=fid` 作为 legacy 投影——注释参照同函数 URL→Data 投影惯例。保留原因：`domains/session/v2/ir_message_adapter.go:1123,1141`（无权限修改）把 FileID 折叠进 Data 且重建 DocumentSource 时只回填 Data。
- `internal/ir/serialize_anthropic.go:656-670`（message 级 document source switch）：case 条件补 `Type=="file_id"`，file_id 取值加 Data 兜底——session 恢复行（FileID 空、Data=id）也能输出完整 `{"type":"file","file_id":...}`，OpenAI 来源 file 文档正确输出。
- `internal/ir/serialize_openai.go:690-699`（serializeOpenAIDocumentBlock case "file_id"）：改为 FileID 优先、Data 兜底。
- `internal/ir/serialize_responses.go:404-411`（buildResponsesInputFile case "file_id"）：同上 FileID 优先、Data 兜底。

## 缺口 c：file_id 图片输出空 image_url

- `internal/ir/serialize_responses.go:323-357`（buildResponsesInputImage）：FileID 非空时原生输出 `{"type":"input_image","file_id":...}`（+detail），与 parseResponsesInputImage（parse_responses.go:506-509）对称；同协议 Responses roundtrip 不再损坏。FileID 优先级与解析端一致（解析时 file_id 覆盖 Type）。不再输出 `image_url:""`。
- `internal/ir/serialize_openai.go:375-405`（serializeOpenAIMessageContent image case）：URL/Data 均无法重建 url 且 FileID 非空时 `continue` 跳过该块——Chat Completions 的 image_url 无法表达 file_id，绝不输出 `image_url:""`。
- `internal/ir/serialize_openai.go:737-757`（reportSerializeOpenAILosses 消息循环）：补 `image.file_id` 的 ReportProtocolLoss（FieldPath `messages[i].content[j].image.file_id`，target=openai-chat，reason=loss）。不做同协议跳过的理由（注释已写明）：parseOpenAIImageBlock 从不产生 FileID 图片，出现即跨协议或 session 恢复行，两种情况下丢弃都是真实损失，上报不是误报。Gemini 序列化器维持既有 file_id-only 返回 nil 的行为（multimodal_fix_test.go 已钉住），未动。

## 向后兼容

- 旧 Data 双轨编码读取：所有 file_id 消费点（serialize_anthropic 顶层+message 级、serialize_openai、serialize_responses）均 FileID 优先、Data 兜底；TestAnthropicTopLevelDocumentsLegacyDataFileID 钉住恢复行行为。
- base64/url/text 文档与 URL/base64 图片路径无行为变化（既有 anthropic_document_test.go、image_base64_test.go、serialize_openai_test.go 全部原样通过，无需修改任何存量测试）。
- 未知字段/降级路径沿用既有 ReportUnknownField/ReportProtocolLoss 约定，未新增静默丢弃。

## 新增/修改测试

新增 `internal/ir/ir_fileid_gap_test.go`（风格参照 anthropic_file_source_test.go / anomaly_test.go 的 resetDedupAndInstall 捕获器）：

1. TestAnthropicTopLevelDocumentsFileIDRoundTrip — 顶层 documents file_id Anthropic parse→serialize roundtrip（含 base64 兄弟文档不回归）。
2. TestAnthropicTopLevelDocumentsLegacyDataFileID — 旧 Data 编码（Type=file_id/Data=id/FileID 空）顶层文档序列化兜底。
3. TestOpenAIFileDocumentFileIDToAnthropic — OpenAI file 文档（file_id）→ IR FileID → Anthropic 输出含 file_id（双轨统一验证）。
4. TestOpenAIFileDocumentFileIDResponsesRoundTrip — Responses input_file file_id 同协议 roundtrip。
5. TestResponsesFileIDImageRoundTrip — file_id 图片 Responses parse→serialize→re-parse 同协议 roundtrip 不丢。
6. TestOpenAIChatFileIDImageLoss — file_id 图片在 OpenAI Chat 序列化产出 loss 事件（messages[0].content[0].image.file_id）且不输出空 image_url、块被跳过。
7. TestOpenAIChatURLImageUnaffected — URL 图片不回归。

存量测试：无需修改（无测试依赖 `image_url==""` 或旧双轨编码）。

## 验证结论

```
go build ./...                      → 通过（BUILD OK）
go vet ./internal/ir/...            → 通过（VET OK）
go test ./internal/ir/...           → ok，471 个用例全绿（含 7 个新增）
go test ./domains/session/... ./domains/transformation/... → ok（IR 下游消费方无回归）
```

## 遗留 / 取舍

- parse_openai 保留 `Data=fid` legacy 投影而非只写 FileID：`domains/session/v2/ir_message_adapter.go`（权限外）会把 FileID 折叠进 Data 且重建时不回填 FileID，双写保证 session 恢复链路无损；序列化端已统一 FileID 优先，双轨读取收口完成。
- 全空（无 FileID/URL/Data）的 image 块在 OpenAI/Responses 序列化仍输出空载体——A-#18 范围仅 file_id 图片，未扩圈；如需可另立条目。
- serialize_gemini 的 document case "file_id"（file_id-only 返回 nil）维持既有设计（跨协议需 Files-API registry，已有注释与测试钉住），未改动。
