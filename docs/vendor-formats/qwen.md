# 通义千问 / DashScope 方言规范

## 概述

- Base protocol: `openai-chat`（兼容模式）
- 方言: `DialectQwen`
- 官方文档: https://help.aliyun.com/zh/model-studio/developer-reference/api-overview
- 兼容端点: `POST /compatible-mode/v1/chat/completions`
- 官方端点: `POST /api/v1/services/aigc/text-generation/generation`（dashscope 原生）

## 兼容模式与原生模式的差异

### 兼容模式（推荐）

```
POST /compatible-mode/v1/chat/completions HTTP/1.1
Host: dashscope.aliyuncs.com
Authorization: Bearer {DASHSCOPE_API_KEY}
Content-Type: application/json
```

完全兼容 OpenAI Chat Completions 协议，仅在以下字段上扩展：

#### `enable_search` 与 `search_options`

```json
{
  "enable_search": true,
  "search_options": {
    "forced_search": true,
    "search_strategy": "max",
    "enable_source": true,
    "enable_citation": true
  }
}
```

启用联网搜索，会在响应 `choices[].message.search_info` 中返回搜索结果。

#### `vl_high_resolution_images`

```json
{"vl_high_resolution_images": true}
```

视觉模型使用高分辨率图片（Qwen-VL）。

### 原生模式（DashScope 私有协议）

```
POST /api/v1/services/aigc/text-generation/generation HTTP/1.1
```

请求结构完全不同：

```json
{
  "model": "qwen-turbo",
  "input": {
    "messages": [
      {"role": "system", "content": "..."},
      {"role": "user", "content": "..."}
    ]
  },
  "parameters": {
    "temperature": 0.7,
    "top_p": 0.9,
    "max_tokens": 2048,
    "result_format": "message"
  }
}
```

响应结构也不同（`output.choices[].message.content` 而非 `choices[].message.content`）。

**网关选择**：当前实现只走兼容模式路径，原生模式留给高级用户。

## 与 OpenAI 的差异

### 1. content 数组形态（已修复 P1-Qwen-1）

Qwen 响应的 content 数组可能没有 `type` 字段：

```json
{
  "choices": [{
    "message": {
      "role": "assistant",
      "content": [
        {"text": "hello"}
      ]
    }
  }]
}
```

IR 解析器 `internal/ir/parse_openai.go` 已支持将无 `type` 文本块映射为标准 IR text block。

### 2. 联网搜索响应

```json
{
  "choices": [{
    "message": {
      "role": "assistant",
      "content": "...",
      "search_info": {
        "search_results": [
          {"index": "1", "title": "...", "url": "...", "content": "..."}
        ]
      }
    }
  }]
}
```

`search_info` 当前在解析时被 strip（不影响主内容），但应保留作为可选元数据。

### 3. 思考内容

部分 Qwen 思考模型（如 Qwen3）在响应中包含 `reasoning_content`：

```json
{
  "message": {
    "content": "...",
    "reasoning_content": "..."
  }
}
```

### 4. `thinking_budget`

Qwen 特有的思考预算控制：

```json
{
  "thinking_budget": 1000
}
```

### 5. 多轮对话中的 function call 顺序

与 OpenAI 一致。

### 6. 工具调用格式

与 OpenAI 完全一致。

### 7. 错误码

与 OpenAI 一致：HTTP 状态码 + `{"error": {"code": "...", "message": "..."}}`。

## IR 映射

| Qwen 字段 | IR 字段 | 备注 |
|-----------|---------|------|
| `enable_search` | `Request.EnableSearch` | 兼容模式扩展 |
| `search_options` | `Request.SearchOptions` | 嵌套对象 |
| `thinking_budget` | `Request.ThinkingBudget` | 数值 |
| `delta.text` (无 type) | `StreamChunk.ContentDelta` | 流式 content 数组 |
| `search_info.search_results` | `Message.SearchInfo` | 可选元数据 |
| `reasoning_content` | `Response.ReasoningContent` | 思考模型 |

## 序列化注意

1. `enable_search` 与 `search_options` 是 Qwen 私有字段，仅在 `target_protocol` 是 DashScope/Qwen 时输出。
2. 多模态 content（`image_url`）需要 base64 内联或公网 URL，Qwen-VL 模型支持。
3. `stream_options.include_usage` 与 OpenAI 一致。
