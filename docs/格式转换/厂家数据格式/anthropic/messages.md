# Anthropic Messages API 规范

## API 概览

**端点**: `POST https://api.anthropic.com/v1/messages`

**认证**:
```
x-api-key: {API_KEY}
anthropic-version: 2023-06-01
```

## 请求格式

### 基础请求结构

```json
{
  "model": "claude-3-5-sonnet-20241022",
  "max_tokens": 1024,
  "messages": [
    {
      "role": "user",
      "content": "Hello, Claude"
    }
  ]
}
```

### 完整请求字段

| 字段 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `model` | string | ✓ | 模型标识符 (claude-3-opus, claude-3-5-sonnet, claude-3-haiku) |
| `messages` | array | ✓ | 对话消息数组 |
| `max_tokens` | integer | ✓ | 最大生成 tokens 数 |
| `system` | string/array | ✗ | 系统提示词 |
| `temperature` | float | ✗ | 采样温度 [0, 1], 默认 1.0 |
| `top_p` | float | ✗ | 核采样参数 [0, 1] |
| `top_k` | integer | ✗ | Top-K 采样参数 |
| `stop_sequences` | array | ✗ | 停止序列 (最多 4 个) |
| `stream` | boolean | ✗ | 是否流式返回，默认 false |
| `metadata` | object | ✗ | 元数据 (user_id 等) |

### Messages 数组格式

```json
{
  "messages": [
    {
      "role": "user",
      "content": "string or array"
    },
    {
      "role": "assistant",
      "content": "string or array"
    }
  ]
}
```

**角色类型**:
- `user`: 用户消息
- `assistant`: 助手消息

**Content 类型**:
- **文本**: `"content": "Hello"`
- **多模态数组**:
  ```json
  "content": [
    {"type": "text", "text": "What's in this image?"},
    {"type": "image", "source": {
      "type": "base64",
      "media_type": "image/jpeg",
      "data": "..."
    }}
  ]
  ```

### 多模态内容格式

#### 文本块
```json
{
  "type": "text",
  "text": "文本内容"
}
```

#### 图像块
```json
{
  "type": "image",
  "source": {
    "type": "base64",
    "media_type": "image/jpeg",
    "data": "/9j/4AAQSkZJRg..."
  }
}
```

**支持的图像格式**: image/jpeg, image/png, image/gif, image/webp

#### 文档块 (Claude 3.5 Sonnet+)
```json
{
  "type": "document",
  "source": {
    "type": "base64",
    "media_type": "application/pdf",
    "data": "JVBERi0xLjQK..."
  }
}
```

### 工具调用 (Function Calling)

#### 工具定义
```json
{
  "tools": [
    {
      "name": "get_weather",
      "description": "Get the current weather in a given location",
      "input_schema": {
        "type": "object",
        "properties": {
          "location": {
            "type": "string",
            "description": "The city and state, e.g. San Francisco, CA"
          },
          "unit": {
            "type": "string",
            "enum": ["celsius", "fahrenheit"],
            "description": "The unit of temperature"
          }
        },
        "required": ["location"]
      }
    }
  ],
  "tool_choice": {"type": "auto"}
}
```

#### Tool Choice 选项
```json
// 自动选择
{"type": "auto"}

// 必须使用任意工具
{"type": "any"}

// 指定工具
{"type": "tool", "name": "get_weather"}
```

#### 工具调用响应
```json
{
  "role": "assistant",
  "content": [
    {
      "type": "text",
      "text": "Let me check the weather for you."
    },
    {
      "type": "tool_use",
      "id": "toolu_01A09q90qw90lq917835lq9",
      "name": "get_weather",
      "input": {
        "location": "San Francisco, CA",
        "unit": "celsius"
      }
    }
  ]
}
```

#### 工具结果提交
```json
{
  "role": "user",
  "content": [
    {
      "type": "tool_result",
      "tool_use_id": "toolu_01A09q90qw90lq917835lq9",
      "content": "65 degrees"
    }
  ]
}
```

### System Prompt 格式

**简单字符串**:
```json
{
  "system": "You are a helpful assistant."
}
```

**结构化数组** (缓存支持):
```json
{
  "system": [
    {
      "type": "text",
      "text": "You are a helpful assistant.",
      "cache_control": {"type": "ephemeral"}
    }
  ]
}
```

### Prompt Caching

```json
{
  "system": [
    {
      "type": "text",
      "text": "长文档内容...",
      "cache_control": {"type": "ephemeral"}
    }
  ],
  "messages": [
    {
      "role": "user",
      "content": [
        {
          "type": "text",
          "text": "参考资料...",
          "cache_control": {"type": "ephemeral"}
        },
        {
          "type": "text",
          "text": "具体问题"
        }
      ]
    }
  ]
}
```

## 响应格式

### 非流式响应

```json
{
  "id": "msg_01XFDUDYJgAACzvnptvVoYEL",
  "type": "message",
  "role": "assistant",
  "model": "claude-3-5-sonnet-20241022",
  "content": [
    {
      "type": "text",
      "text": "Hello! How can I assist you today?"
    }
  ],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {
    "input_tokens": 12,
    "output_tokens": 25
  }
}
```

### 响应字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | string | 消息唯一标识符 |
| `type` | string | 固定值 "message" |
| `role` | string | 固定值 "assistant" |
| `model` | string | 使用的模型 |
| `content` | array | 内容数组 (text/tool_use) |
| `stop_reason` | string | 停止原因 (end_turn/max_tokens/stop_sequence/tool_use) |
| `stop_sequence` | string | 触发的停止序列 |
| `usage` | object | Token 使用统计 |

### 流式响应

**事件类型**:

1. **message_start**
```json
{
  "type": "message_start",
  "message": {
    "id": "msg_01XFDUDYJgAACzvnptvVoYEL",
    "type": "message",
    "role": "assistant",
    "model": "claude-3-5-sonnet-20241022",
    "content": [],
    "stop_reason": null,
    "stop_sequence": null,
    "usage": {"input_tokens": 12, "output_tokens": 0}
  }
}
```

2. **content_block_start**
```json
{
  "type": "content_block_start",
  "index": 0,
  "content_block": {
    "type": "text",
    "text": ""
  }
}
```

3. **content_block_delta**
```json
{
  "type": "content_block_delta",
  "index": 0,
  "delta": {
    "type": "text_delta",
    "text": "Hello"
  }
}
```

4. **content_block_stop**
```json
{
  "type": "content_block_stop",
  "index": 0
}
```

5. **message_delta**
```json
{
  "type": "message_delta",
  "delta": {
    "stop_reason": "end_turn",
    "stop_sequence": null
  },
  "usage": {"output_tokens": 25}
}
```

6. **message_stop**
```json
{
  "type": "message_stop"
}
```

### 工具调用流式响应

```json
// content_block_start
{
  "type": "content_block_start",
  "index": 1,
  "content_block": {
    "type": "tool_use",
    "id": "toolu_01A09q90qw90lq917835lq9",
    "name": "get_weather",
    "input": {}
  }
}

// content_block_delta (多次)
{
  "type": "content_block_delta",
  "index": 1,
  "delta": {
    "type": "input_json_delta",
    "partial_json": "{\"location\": \"San "
  }
}

// content_block_stop
{
  "type": "content_block_stop",
  "index": 1
}
```

## 错误响应

```json
{
  "type": "error",
  "error": {
    "type": "invalid_request_error",
    "message": "messages: field required"
  }
}
```

**错误类型**:
- `invalid_request_error`: 请求格式错误
- `authentication_error`: 认证失败
- `permission_error`: 权限不足
- `not_found_error`: 资源不存在
- `rate_limit_error`: 速率限制
- `api_error`: API 内部错误
- `overloaded_error`: 服务过载

## 特殊功能

### Extended Thinking (claude-3-7-sonnet)

请求:
```json
{
  "model": "claude-3-7-sonnet-20250219",
  "thinking": {
    "type": "enabled",
    "budget_tokens": 10000
  },
  "messages": [...]
}
```

响应包含思考块:
```json
{
  "content": [
    {
      "type": "thinking",
      "thinking": "Let me think about this..."
    },
    {
      "type": "text",
      "text": "Based on my analysis..."
    }
  ]
}
```

### Vision 能力

支持的模型: Claude 3 Opus, Claude 3.5 Sonnet, Claude 3 Haiku

```json
{
  "messages": [
    {
      "role": "user",
      "content": [
        {
          "type": "image",
          "source": {
            "type": "base64",
            "media_type": "image/jpeg",
            "data": "..."
          }
        },
        {
          "type": "text",
          "text": "What's in this image?"
        }
      ]
    }
  ]
}
```

## 模型列表

| 模型 | 上下文窗口 | 输出限制 | 特性 |
|------|-----------|---------|------|
| claude-3-7-sonnet-20250219 | 200K | 64K | Extended Thinking |
| claude-3-5-sonnet-20241022 | 200K | 8K | Vision, Tools, PDF |
| claude-3-5-haiku-20241022 | 200K | 8K | 快速、经济 |
| claude-3-opus-20240229 | 200K | 4K | 最强能力 |
| claude-3-sonnet-20240229 | 200K | 4K | 平衡 |
| claude-3-haiku-20240307 | 200K | 4K | 快速 |

## Usage 统计

```json
{
  "usage": {
    "input_tokens": 2095,
    "cache_creation_input_tokens": 2000,
    "cache_read_input_tokens": 1800,
    "output_tokens": 503
  }
}
```

**字段说明**:
- `input_tokens`: 常规输入 tokens
- `cache_creation_input_tokens`: 缓存写入 tokens
- `cache_read_input_tokens`: 缓存命中 tokens
- `output_tokens`: 输出 tokens
