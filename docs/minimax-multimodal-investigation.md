# MiniMax-M3 多模态图片支持 - 调查总结

## 问题
用户反馈：通过网关发送给 minimax-m3 的请求不支持多模态数据（如图片），但直连就可以。

## 调查结论

✅ **代码逻辑正确，图片数据在转换过程中被完整保留**

经过详细的代码审查和全面测试，确认：
1. OpenAI 格式到 Anthropic/MiniMax 格式的转换正确处理图片
2. 支持 base64 和 URL 两种图片类型
3. MiniMax 特定的协议变体（tool_call_id）得到正确处理
4. 消息合并和修复逻辑保留所有多模态内容块

## 关键代码路径

### 1. 图片解析 (parse_openai.go:334-386)
```go
// parseOpenAIImageBlock 解析 OpenAI image_url 块
// 区分 HTTP(S) URL 和 data URI (base64)
func parseOpenAIImageBlock(block map[string]any) *ImageSource {
    img := &ImageSource{Type: "url"}
    urlObj, ok := block["image_url"].(map[string]any)
    url, _ := urlObj["url"].(string)
    img.URL = url
    
    // 解析 data URI，填充 base64 专用字段
    if mediaType, data, isBase64 := parseOpenAIDataURI(url); isBase64 {
        img.Type = "base64"
        img.MediaType = mediaType  // "image/png"
        img.Data = data            // 纯base64数据（无前缀）
    }
    return img
}
```

### 2. 图片序列化 (serialize_anthropic.go:385-424)
```go
case "image":
    srcType := block.Image.Type
    source := map[string]any{"type": srcType}
    switch srcType {
    case "base64":
        source["media_type"] = block.Image.MediaType
        source["data"] = block.Image.Data
    case "url":
        source["url"] = block.Image.URL
    }
    out["source"] = source
```

### 3. MiniMax 协议变体 (serialize_anthropic.go:272-276, 445-449)
```go
// MiniMax 使用 tool_call_id 而非标准的 tool_use_id
if targetProvider == "minimax" {
    toolResult["tool_call_id"] = msg.ToolCallID
} else {
    toolResult["tool_use_id"] = msg.ToolCallID
}
```

## 测试覆盖

创建了以下测试文件和用例：

### internal/ir/multimodal_minimax_test.go
- ✅ TestOpenAIToMinimaxImageConversion - base64图片完整转换
- ✅ TestOpenAIToMinimaxImageURL - URL图片转换
- ✅ TestMinimaxDirectImageRequest - Anthropic格式直接请求

### internal/ir/end_to_end_minimax_test.go
- ✅ TestMiniMaxMultimodalEndToEnd - 端到端测试
- ✅ TestMiniMaxMultimodalWithURLImage - URL图片端到端

### domains/transformation/anthropic_message_fix_multimodal_test.go
- ✅ TestFixAnthropicMessages_PreservesImageBlocks - 修复逻辑保留图片
- ✅ TestFixAnthropicMessages_MergeConsecutiveWithImages - 合并消息保留图片

**所有测试通过 ✅**

## 转换流程示例

### 输入 (OpenAI 格式)
```json
{
  "model": "gpt-4-vision",
  "messages": [{
    "role": "user",
    "content": [{
      "type": "text",
      "text": "What is in this image?"
    }, {
      "type": "image_url",
      "image_url": {
        "url": "data:image/png;base64,iVBORw0KGg..."
      }
    }]
  }]
}
```

### 输出 (MiniMax/Anthropic 格式)
```json
{
  "model": "minimax-m3",
  "max_tokens": 1024,
  "messages": [{
    "role": "user",
    "content": [{
      "type": "text",
      "text": "What is in this image?"
    }, {
      "type": "image",
      "source": {
        "type": "base64",
        "media_type": "image/png",
        "data": "iVBORw0KGg..."
      }
    }]
  }]
}
```

## 如果用户仍遇到问题

### 可能原因

1. **路由配置问题**
   - 检查 `provider_offers.protocol` 是否为 `"anthropic-messages"`
   - 检查 `provider_offers.catalog_code` 是否为 `"minimax"`

2. **API 端点问题**
   - MiniMax 的 Anthropic 兼容端点可能需要特定的路径
   - 检查 `base_url` 配置是否正确

3. **上下文压缩**
   - 如果开启了消息压缩，检查是否保留了图片块
   - 查看日志中的 `compression_reason` 字段

4. **中间件过滤**
   - 检查是否有自定义中间件过滤了 content 数组
   - 查看 `ApplyRequestWhitelist` 的配置

### 调试建议

1. **添加请求日志**
   ```go
   // 在 domains/streaming/executors/executor_anthropic.go:710
   slog.Info("minimax request", 
       "body_length", len(bodyBytes),
       "has_image", strings.Contains(string(bodyBytes), `"type":"image"`))
   ```

2. **对比请求体**
   - 捕获直连 MiniMax 成功的请求
   - 捕获通过网关发送的请求
   - 使用 `jq` 或在线工具对比 JSON 差异

3. **检查响应错误**
   ```go
   // 如果 MiniMax 返回 4xx 错误
   slog.Error("minimax rejected request",
       "status", resp.StatusCode,
       "body", string(body[:min(n, 500)]))
   ```

4. **验证图片数据完整性**
   - 确认 base64 数据长度
   - 确认 media_type 正确
   - 确认没有额外的前缀或后缀

## 相关文件

### 核心逻辑
- `internal/ir/parse_openai.go` - OpenAI 格式解析
- `internal/ir/parse_anthropic.go` - Anthropic 格式解析
- `internal/ir/serialize_openai.go` - OpenAI 格式序列化
- `internal/ir/serialize_anthropic.go` - Anthropic 格式序列化
- `domains/transformation/anthropic_message_fix.go` - 消息修复逻辑

### 执行层
- `domains/streaming/executors/executor_anthropic.go` - Anthropic 协议执行器
- `domains/streaming/executors/executor.go` - 主执行器

### 测试
- `internal/ir/multimodal_minimax_test.go` ⭐ 新增
- `internal/ir/end_to_end_minimax_test.go` ⭐ 新增
- `domains/transformation/anthropic_message_fix_multimodal_test.go` ⭐ 新增

## 结论

**网关代码正确处理 MiniMax 的多模态图片请求，不应该丢失图片信息。**

如果用户仍遇到问题，建议：
1. 提供具体的错误日志和请求示例
2. 检查 MiniMax API 的端点和版本
3. 对比直连和网关的实际请求差异
4. 检查是否有额外的中间件或配置

---
调查人员: Kiro  
日期: 2026-07-17  
测试状态: ✅ All Pass
