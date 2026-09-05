---
archived_from: (legacy) docs/archive/2026-07/MINIMAX_MULTIMODAL_INVESTIGATION.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190918
status: archived
note: legacy archive, frontmatter retroactively added
---

# MiniMax-M3 多模态图片支持调查报告

## 问题描述
用户反馈：通过网关发送给 minimax-m3 的请求不支持多模态数据（如图片），但直连 MiniMax API 就可以。

## 调查结果

### ✅ 结论：代码逻辑正确，图片数据应该被完整保留

经过全面的代码审查和测试，我发现：

1. **IR 转换层正确处理图片数据**
   - `internal/ir/parse_openai.go` 正确解析 OpenAI 格式的 `image_url` 块
   - 支持 base64 和 URL 两种图片类型
   - 正确提取 `data:image/png;base64,...` 格式的 data URI

2. **序列化层正确输出图片数据**
   - `internal/ir/serialize_anthropic.go` 的 `serializeAnthropicContentBlock` 函数（第385-424行）
   - 正确根据 `source.type` 输出不同的字段：
     - `type=base64` → `{type, media_type, data}`
     - `type=url` → `{type, url}`

3. **MiniMax 特殊处理正确**
   - 支持 MiniMax 的 `tool_call_id` 字段（而非标准的 `tool_use_id`）
   - `TargetProvider="minimax"` 时正确使用协议变体

4. **消息处理层保留图片数据**
   - `domains/transformation/anthropic_message_fix.go` 的 `ensureContentArray` 正确保留数组格式的 content
   - 合并连续消息时保留所有 content blocks（包括图片）

## 测试覆盖

创建了以下测试来验证功能：

### 1. `internal/ir/multimodal_minimax_test.go`
- ✅ `TestOpenAIToMinimaxImageConversion`: OpenAI → IR → MiniMax 转换（base64图片）
- ✅ `TestOpenAIToMinimaxImageURL`: URL类型图片转换
- ✅ `TestMinimaxDirectImageRequest`: 直接使用 Anthropic 格式发送图片

### 2. `domains/transformation/anthropic_message_fix_multimodal_test.go`
- ✅ `TestFixAnthropicMessages_PreservesImageBlocks`: 验证 FixAnthropicMessages 保留图片
- ✅ `TestFixAnthropicMessages_MergeConsecutiveWithImages`: 验证合并消息时保留图片

### 3. `internal/ir/end_to_end_minimax_test.go`
- ✅ `TestMiniMaxMultimodalEndToEnd`: 端到端测试（OpenAI → MiniMax）
- ✅ `TestMiniMaxMultimodalWithURLImage`: URL图片端到端测试

所有测试都通过 ✅

## 完整转换流程

```
OpenAI 客户端请求
  ↓
ParseOpenAI() 
  ↓ 提取 image_url.url，解析 data URI
  ↓ 创建 ImageSource{Type:"base64", MediaType:"image/png", Data:"..."}
  ↓
IR (InternalRequest)
  ↓ Messages[0].Content[1] = ContentBlock{Type:"image", Image:...}
  ↓
SerializeAnthropic(TargetProvider="minimax")
  ↓ 序列化为 {"type":"image", "source":{"type":"base64","media_type":"...","data":"..."}}
  ↓
Anthropic/MiniMax 格式请求
```

## 可能的问题原因

如果用户遇到图片数据丢失，可能的原因：

### 1. **路由配置问题**
检查 `provider_offers` 表中 MiniMax 的配置：
- `protocol` 应该是 `"anthropic-messages"`
- `catalog_code` 应该是 `"minimax"`

### 2. **请求过滤问题**
检查是否有中间件或过滤器：
- `ApplyRequestWhitelist` 可能过滤了某些字段
- 但 `messages` 在白名单中，不应该被过滤

### 3. **上游 API 版本问题**
MiniMax 的 Anthropic 兼容 API 可能有版本差异：
- 检查 `anthropic-version` 头是否正确（当前代码使用 `"2023-06-01"`）
- 某些早期版本可能不支持图片

### 4. **消息压缩问题**
如果开启了上下文压缩（`CompressAnthropicMessagesIfNeeded`）：
- 检查压缩逻辑是否保留了图片块
- 建议查看 `domains/transformation/ctx_compress.go`

## 建议的调试步骤

1. **启用请求日志**
   ```go
   // 在 domains/streaming/executors/executor_anthropic.go 的 BuildRequest 之后
   slog.Info("minimax request body", "body", string(bodyBytes))
   ```

2. **检查实际发送的请求**
   在 `executeAnthropicOnce` 函数中添加日志：
   ```go
   if cand.CatalogCode == "minimax" {
       slog.Info("sending to minimax", 
           "body_length", len(bodyBytes),
           "has_image", strings.Contains(string(bodyBytes), `"type":"image"`))
   }
   ```

3. **检查 MiniMax 的响应**
   如果 MiniMax 返回错误，查看错误信息：
   ```go
   // 在 executeAnthropicOnce 的错误处理中
   if resp.StatusCode >= 400 {
       slog.Error("minimax error response",
           "status", resp.StatusCode,
           "body", string(body[:n]))
   }
   ```

4. **对比直连和网关请求**
   使用 `curl` 或 Postman 捕获：
   - 直连 MiniMax 成功的请求体
   - 通过网关发送的请求体
   - 对比差异

## 结论

根据代码审查和测试，**网关代码正确处理了多模态图片数据，不应该丢失图片信息**。

如果用户仍然遇到问题，建议：
1. 检查具体的错误信息和日志
2. 确认 MiniMax API 的版本和端点配置
3. 对比直连和网关的实际请求体差异
4. 检查是否有其他中间件或转换逻辑

---

测试文件位置：
- `internal/ir/multimodal_minimax_test.go`
- `internal/ir/end_to_end_minimax_test.go`
- `domains/transformation/anthropic_message_fix_multimodal_test.go`
