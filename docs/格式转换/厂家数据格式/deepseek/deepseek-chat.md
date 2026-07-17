# DeepSeek API 规范

## API 概览

**端点**: 
```
POST https://api.deepseek.com/v1/chat/completions
POST https://api.deepseek.com/v1/completions (FIM 模式)
```

**认证**: 
```
Authorization: Bearer {API_KEY}
```

**说明**: DeepSeek API 完全兼容 OpenAI API,可直接使用 OpenAI SDK。同时提供推理模式(R1)扩展。

## 请求格式

### 基础请求结构

```json
{
  "model": "deepseek-chat",
  "messages": [
    {
      "role": "user",
      "content": "你好"
    }
  ]
}
```

### 完整请求字段

| 字段 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `model` | string | ✓ | 模型名称 |
| `messages` | array | ✓ | 对话消息数组 |
| `temperature` | float | ✗ | 采样温度 [0, 2], 默认 1.0 |
| `top_p` | float | ✗ | 核采样 [0, 1], 默认 1.0 |
| `max_tokens` | integer | ✗ | 最大生成 tokens |
| `stream` | boolean | ✗ | 是否流式返回 |
| `stop` | string/array | ✗ | 停止词 |
| `frequency_penalty` | float | ✗ | 频率惩罚 [-2, 2] |
| `presence_penalty` | float | ✗ | 存在惩罚 [-2, 2] |
| `logprobs` | boolean | ✗ | 是否返回对数概率 |
| `top_logprobs` | integer | ✗ | 返回的 top 对数概率数量 [0, 20] |
| `tools` | array | ✗ | 工具定义 |
| `tool_choice` | string/object | ✗ | 工具选择策略 |
| `response_format` | object | ✗ | 响应格式(JSON 模式) |

### Messages 数组格式

```json
{
  "messages": [
    {
      "role": "system",
      "content": "You are a helpful assistant"
    },
    {
      "role": "user",
      "content": "Hello"
    },
    {
      "role": "assistant",
      "content": "Hello! How can I help you today?"
    },
    {
      "role": "user",
      "content": "What's the weather?"
    }
  ]
}
```

**角色类型**:
- `system`: 系统消息
- `user`: 用户消息
- `assistant`: 助手消息
- `tool`: 工具返回结果

### Content 格式

#### 文本内容
```json
{
  "role": "user",
  "content": "文本内容"
}
```

#### 多模态内容 (deepseek-chat v3+)
```json
{
  "role": "user",
  "content": [
    {
      "type": "text",
      "text": "这张图片里有什么?"
    },
    {
      "type": "image_url",
      "image_url": {
        "url": "https://example.com/image.jpg"
      }
    }
  ]
}
```

**图片支持**:
- HTTP/HTTPS URL
- Base64: `data:image/jpeg;base64,...`
- 支持格式: JPEG, PNG, WebP, GIF

### 工具调用 (Function Calling)

#### 工具定义
```json
{
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get the current weather in a given location",
        "parameters": {
          "type": "object",
          "properties": {
            "location": {
              "type": "string",
              "description": "The city name"
            },
            "unit": {
              "type": "string",
              "enum": ["celsius", "fahrenheit"]
            }
          },
          "required": ["location"]
        }
      }
    }
  ]
}
```

#### Tool Choice
```json
// 自动选择
"tool_choice": "auto"

// 不调用工具
"tool_choice": "none"

// 必须调用
"tool_choice": "required"

// 指定函数
"tool_choice": {
  "type": "function",
  "function": {"name": "get_weather"}
}
```

### JSON 模式

```json
{
  "response_format": {
    "type": "json_object"
  }
}
```

使用时需在系统消息中说明 JSON 格式要求。

## 响应格式

### 非流式响应

```json
{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "created": 1719467431,
  "model": "deepseek-chat",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Hello! How can I help you today?"
      },
      "finish_reason": "stop",
      "logprobs": null
    }
  ],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 25,
    "total_tokens": 35,
    "prompt_cache_hit_tokens": 0,
    "prompt_cache_miss_tokens": 10
  },
  "system_fingerprint": "fp_..."
}
```

### 响应字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | string | 请求 ID |
| `object` | string | 对象类型 |
| `created` | integer | 创建时间戳 |
| `model` | string | 使用的模型 |
| `choices` | array | 生成结果数组 |
| `usage` | object | Token 使用统计 |
| `system_fingerprint` | string | 系统指纹 |

### Choice 结构

| 字段 | 类型 | 说明 |
|------|------|------|
| `index` | integer | 索引 |
| `message` | object | 消息对象 |
| `finish_reason` | string | 完成原因 |
| `logprobs` | object | 对数概率 |

**Finish Reason**:
- `stop`: 自然停止
- `length`: 达到长度限制
- `tool_calls`: 需要调用工具
- `content_filter`: 内容过滤

### Usage 字段扩展

DeepSeek 扩展了 usage 字段,支持缓存统计:

```json
{
  "usage": {
    "prompt_tokens": 1000,
    "completion_tokens": 100,
    "total_tokens": 1100,
    "prompt_cache_hit_tokens": 800,
    "prompt_cache_miss_tokens": 200
  }
}
```

**缓存相关字段**:
- `prompt_cache_hit_tokens`: 缓存命中的 tokens(不计费)
- `prompt_cache_miss_tokens`: 缓存未命中的 tokens(计费)

### 工具调用响应

```json
{
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": null,
        "tool_calls": [
          {
            "id": "call_abc123",
            "type": "function",
            "function": {
              "name": "get_weather",
              "arguments": "{\"location\": \"Beijing\", \"unit\": \"celsius\"}"
            }
          }
        ]
      },
      "finish_reason": "tool_calls"
    }
  ]
}
```

#### 提交工具结果
```json
{
  "messages": [
    {
      "role": "user",
      "content": "What's the weather in Beijing?"
    },
    {
      "role": "assistant",
      "content": null,
      "tool_calls": [
        {
          "id": "call_abc123",
          "type": "function",
          "function": {
            "name": "get_weather",
            "arguments": "{\"location\": \"Beijing\"}"
          }
        }
      ]
    },
    {
      "role": "tool",
      "content": "{\"temperature\": 15, \"condition\": \"sunny\"}",
      "tool_call_id": "call_abc123"
    }
  ]
}
```

### 流式响应

使用 SSE 格式:

```
data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1719467431,"model":"deepseek-chat","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1719467431,"model":"deepseek-chat","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1719467431,"model":"deepseek-chat","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":25,"total_tokens":35}}

data: [DONE]
```

**Delta 结构**:
- 首个 chunk 包含 `role`
- 后续 chunk 包含增量 `content`
- 最后一个 chunk 包含 `finish_reason` 和 `usage`

## 推理模式 (DeepSeek-R1)

### R1 模型特性

DeepSeek-R1 支持链式推理(Chain of Thought),响应包含推理过程。

**模型**: `deepseek-reasoner`

### 推理响应格式

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": "Final answer based on reasoning",
        "reasoning_content": "Step 1: Analyze the problem...\nStep 2: Consider alternatives...\nStep 3: Reach conclusion..."
      },
      "finish_reason": "stop"
    }
  ]
}
```

**字段说明**:
- `reasoning_content`: 推理过程(思考链)
- `content`: 最终答案

### 推理流式响应

```
data: {"choices":[{"delta":{"role":"assistant","reasoning_content":"Let me think about this"},"finish_reason":null}]}

data: {"choices":[{"delta":{"reasoning_content":" step by step"},"finish_reason":null}]}

data: {"choices":[{"delta":{"reasoning_content":"","content":"Based on my reasoning"},"finish_reason":null}]}

data: {"choices":[{"delta":{"content":", the answer is..."},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

推理模式下的流式响应:
1. 先流式输出 `reasoning_content`(推理过程)
2. 再流式输出 `content`(最终答案)

### 推理模式示例

```json
{
  "model": "deepseek-reasoner",
  "messages": [
    {
      "role": "user",
      "content": "If a train leaves New York at 3pm traveling at 80mph, and another train leaves Boston at 4pm traveling at 60mph, when will they meet if the cities are 215 miles apart?"
    }
  ]
}
```

响应会包含详细的推理步骤。

## Fill-in-the-Middle (FIM) 模式

用于代码补全场景。

**端点**: `POST https://api.deepseek.com/v1/completions`

### FIM 请求

```json
{
  "model": "deepseek-coder",
  "prompt": "<|fim_begin|>def quick_sort(arr):\n<|fim_hole|>\n    return arr\n<|fim_end|>",
  "max_tokens": 100,
  "temperature": 0
}
```

**FIM 标记**:
- `<|fim_begin|>`: 代码前缀
- `<|fim_hole|>`: 需要补全的位置
- `<|fim_end|>`: 代码后缀

### FIM 响应

```json
{
  "id": "cmpl-abc123",
  "object": "text_completion",
  "created": 1719467431,
  "model": "deepseek-coder",
  "choices": [
    {
      "text": "    if len(arr) <= 1:\n        return arr\n    pivot = arr[len(arr) // 2]\n    left = [x for x in arr if x < pivot]\n    middle = [x for x in arr if x == pivot]\n    right = [x for x in arr if x > pivot]\n    return quick_sort(left) + middle + quick_sort(right)",
      "index": 0,
      "logprobs": null,
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 20,
    "completion_tokens": 80,
    "total_tokens": 100
  }
}
```

## 多模态 (Vision)

### 图片理解

```json
{
  "model": "deepseek-chat",
  "messages": [
    {
      "role": "user",
      "content": [
        {
          "type": "image_url",
          "image_url": {
            "url": "https://example.com/image.jpg"
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

### Base64 图片

```json
{
  "content": [
    {
      "type": "image_url",
      "image_url": {
        "url": "data:image/jpeg;base64,/9j/4AAQSkZJRg..."
      }
    }
  ]
}
```

## 模型列表

| 模型 | 上下文窗口 | 输出限制 | 特性 |
|------|-----------|---------|------|
| deepseek-chat | 64K | 8K | 通用对话、多模态 |
| deepseek-reasoner | 64K | 8K | 推理模式(R1) |
| deepseek-coder | 128K | 8K | 代码生成、FIM |

## Prefix Caching

DeepSeek 支持自动前缀缓存,无需特殊配置:

```json
{
  "model": "deepseek-chat",
  "messages": [
    {
      "role": "system",
      "content": "很长的系统提示词..."
    },
    {
      "role": "user",
      "content": "新问题"
    }
  ]
}
```

重复的前缀内容会被自动缓存,`usage` 字段会显示缓存命中情况:

```json
{
  "usage": {
    "prompt_tokens": 1000,
    "completion_tokens": 100,
    "total_tokens": 1100,
    "prompt_cache_hit_tokens": 950,
    "prompt_cache_miss_tokens": 50
  }
}
```

**缓存规则**:
- 缓存前缀至少 1024 tokens
- 缓存有效期 5 分钟
- 缓存命中的 tokens 不计费

## 错误响应

```json
{
  "error": {
    "message": "Invalid API key",
    "type": "invalid_request_error",
    "code": "invalid_api_key"
  }
}
```

**常见错误码**:
- `invalid_api_key`: API Key 无效
- `invalid_request_error`: 请求格式错误
- `rate_limit_exceeded`: 速率限制
- `context_length_exceeded`: 上下文长度超限
- `content_filter`: 内容过滤
- `server_error`: 服务器错误
- `insufficient_quota`: 余额不足

## 认证方式

### API Key

```bash
curl https://api.deepseek.com/v1/chat/completions \
  -H "Authorization: Bearer {API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-chat",
    "messages": [
      {"role": "user", "content": "Hello"}
    ]
  }'
```

### 使用 OpenAI SDK

```python
from openai import OpenAI

client = OpenAI(
    api_key="YOUR_API_KEY",
    base_url="https://api.deepseek.com/v1"
)

response = client.chat.completions.create(
    model="deepseek-chat",
    messages=[
        {"role": "user", "content": "Hello"}
    ]
)
```

## 私有扩展总结

DeepSeek API 在 OpenAI 兼容基础上的扩展:

1. **推理模式 (R1)**:
   - `reasoning_content`: 推理过程字段
   - 链式推理(Chain of Thought)
   - 适合复杂问题和数学推理

2. **缓存统计**:
   - `prompt_cache_hit_tokens`: 缓存命中
   - `prompt_cache_miss_tokens`: 缓存未命中
   - 自动前缀缓存

3. **FIM 模式**:
   - 代码补全专用端点
   - `<|fim_begin|>`, `<|fim_hole|>`, `<|fim_end|>` 标记

4. **模型特化**:
   - `deepseek-chat`: 通用对话
   - `deepseek-reasoner`: 推理专用
   - `deepseek-coder`: 代码专用

5. **性能优化**:
   - 自动前缀缓存
   - 缓存命中不计费
   - 支持超长上下文(128K)
