# 豆包 Doubao API 规范

## API 概览

**端点**: `POST https://ark.cn-beijing.volces.com/api/v3/chat/completions`

**认证**: 
```
Authorization: Bearer {API_KEY}
```

**说明**: 豆包 API 完全兼容 OpenAI API,同时提供插件、智能体等私有扩展功能。

## 请求格式

### 基础请求结构

```json
{
  "model": "doubao-pro-32k",
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
| `model` | string | ✓ | 模型端点 ID |
| `messages` | array | ✓ | 对话消息数组 |
| `temperature` | float | ✗ | 采样温度 [0, 1], 默认 0.9 |
| `top_p` | float | ✗ | 核采样 [0, 1], 默认 0.7 |
| `max_tokens` | integer | ✗ | 最大生成 tokens, 默认 4096 |
| `stream` | boolean | ✗ | 是否流式返回 |
| `stop` | string/array | ✗ | 停止词 |
| `frequency_penalty` | float | ✗ | 频率惩罚 [-2, 2] |
| `presence_penalty` | float | ✗ | 存在惩罚 [-2, 2] |
| `tools` | array | ✗ | 工具定义 |
| `tool_choice` | string/object | ✗ | 工具选择策略 |

### Doubao 私有字段

| 字段 | 类型 | 说明 |
|------|------|------|
| `plugins` | array | 插件配置 |
| `bot_id` | string | 智能体 ID |
| `custom_bot_id` | string | 自定义智能体 ID |
| `logprobs` | boolean | 是否返回对数概率 |
| `top_logprobs` | integer | 返回的 top 对数概率数量 |

### Messages 数组格式

```json
{
  "messages": [
    {
      "role": "system",
      "content": "你是豆包,是由字节跳动开发的 AI 人工智能助手"
    },
    {
      "role": "user",
      "content": "你好"
    },
    {
      "role": "assistant",
      "content": "你好!我是豆包,很高兴为你服务。"
    },
    {
      "role": "user",
      "content": "介绍一下你自己"
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

#### 多模态内容 (视觉模型)
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
        "description": "获取指定城市的天气信息",
        "parameters": {
          "type": "object",
          "properties": {
            "location": {
              "type": "string",
              "description": "城市名称,例如:北京"
            },
            "unit": {
              "type": "string",
              "enum": ["celsius", "fahrenheit"],
              "description": "温度单位"
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

### Plugins (插件系统)

豆包支持多种内置插件:

```json
{
  "plugins": [
    {
      "name": "web_search",
      "config": {
        "enable": true
      }
    },
    {
      "name": "image_generation",
      "config": {
        "enable": true,
        "model": "doubao-image-v1"
      }
    }
  ]
}
```

**内置插件**:
- `web_search`: 网络搜索
- `image_generation`: 图片生成
- `code_interpreter`: 代码解释器
- `file_reading`: 文件读取

### Bot 智能体

使用预配置的智能体:

```json
{
  "model": "doubao-pro-32k",
  "bot_id": "bot_abc123",
  "messages": [
    {
      "role": "user",
      "content": "帮我写一篇文章"
    }
  ]
}
```

或使用自定义智能体:

```json
{
  "model": "doubao-pro-32k",
  "custom_bot_id": "custom_bot_xyz789",
  "messages": [...]
}
```

## 响应格式

### 非流式响应

```json
{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "created": 1719467431,
  "model": "doubao-pro-32k",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "你好!我是豆包,很高兴为你服务。有什么可以帮助你的吗?"
      },
      "finish_reason": "stop",
      "logprobs": null
    }
  ],
  "usage": {
    "prompt_tokens": 15,
    "completion_tokens": 30,
    "total_tokens": 45
  }
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

### Choice 结构

| 字段 | 类型 | 说明 |
|------|------|------|
| `index` | integer | 索引 |
| `message` | object | 消息对象 |
| `finish_reason` | string | 完成原因 |
| `logprobs` | object | 对数概率(如果请求) |

**Finish Reason**:
- `stop`: 自然停止
- `length`: 达到长度限制
- `tool_calls`: 需要调用工具
- `content_filter`: 内容过滤

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
              "arguments": "{\"location\": \"北京\", \"unit\": \"celsius\"}"
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
      "content": "北京天气怎么样?"
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
            "arguments": "{\"location\": \"北京\"}"
          }
        }
      ]
    },
    {
      "role": "tool",
      "content": "{\"temperature\": 15, \"condition\": \"晴\"}",
      "tool_call_id": "call_abc123"
    }
  ]
}
```

### 插件响应

使用插件时,响应可能包含插件调用信息:

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": "根据搜索结果,最近的新闻是...",
        "plugin_calls": [
          {
            "name": "web_search",
            "arguments": "{\"query\": \"最近新闻\"}",
            "result": {
              "title": "新闻标题",
              "url": "https://...",
              "content": "新闻内容"
            }
          }
        ]
      }
    }
  ]
}
```

### 流式响应

使用 SSE 格式:

```
data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1719467431,"model":"doubao-pro-32k","choices":[{"index":0,"delta":{"role":"assistant","content":"你好"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1719467431,"model":"doubao-pro-32k","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1719467431,"model":"doubao-pro-32k","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":15,"completion_tokens":30,"total_tokens":45}}

data: [DONE]
```

**Delta 结构**:
- 首个 chunk 包含 `role`
- 后续 chunk 包含增量 `content`
- 最后一个 chunk 包含 `finish_reason` 和 `usage`

### Logprobs 响应

当请求 `logprobs: true` 时:

```json
{
  "choices": [
    {
      "message": {...},
      "logprobs": {
        "content": [
          {
            "token": "你好",
            "logprob": -0.123,
            "bytes": [228, 189, 160, 229, 165, 189],
            "top_logprobs": [
              {
                "token": "你好",
                "logprob": -0.123,
                "bytes": [228, 189, 160, 229, 165, 189]
              },
              {
                "token": "您好",
                "logprob": -2.456,
                "bytes": [230, 130, 168, 229, 165, 189]
              }
            ]
          }
        ]
      }
    }
  ]
}
```

## 多模态功能

### 视觉理解

```json
{
  "model": "doubao-vision-pro",
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
          "text": "描述这张图片"
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

| 模型端点 | 上下文窗口 | 输出限制 | 特性 |
|---------|-----------|---------|------|
| doubao-pro-4k | 4K | 4K | 高性能通用 |
| doubao-pro-32k | 32K | 4K | 长上下文 |
| doubao-pro-128k | 128K | 4K | 超长上下文 |
| doubao-lite-4k | 4K | 4K | 快速经济 |
| doubao-vision-pro | 4K | 4K | 视觉理解 |
| doubao-character-* | 变化 | 变化 | 角色扮演 |

**注意**: 实际模型端点 ID 需要在火山引擎控制台创建并获取。

## Embeddings API

**端点**: `POST https://ark.cn-beijing.volces.com/api/v3/embeddings`

```json
{
  "model": "doubao-embedding",
  "input": "文本内容"
}
```

响应:
```json
{
  "object": "list",
  "data": [
    {
      "object": "embedding",
      "embedding": [0.123, -0.456, 0.789, ...],
      "index": 0
    }
  ],
  "model": "doubao-embedding",
  "usage": {
    "prompt_tokens": 10,
    "total_tokens": 10
  }
}
```

支持批量输入:
```json
{
  "model": "doubao-embedding",
  "input": ["文本1", "文本2", "文本3"]
}
```

## Tokenization API

**端点**: `POST https://ark.cn-beijing.volces.com/api/v3/tokenization`

```json
{
  "model": "doubao-pro-32k",
  "text": "你好,世界!"
}
```

响应:
```json
{
  "tokens": [1234, 5678, 9012],
  "total_tokens": 3
}
```

## 错误响应

```json
{
  "error": {
    "message": "Invalid API key provided",
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
curl https://ark.cn-beijing.volces.com/api/v3/chat/completions \
  -H "Authorization: Bearer {API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "doubao-pro-32k",
    "messages": [
      {"role": "user", "content": "你好"}
    ]
  }'
```

### 使用 OpenAI SDK

```python
from openai import OpenAI

client = OpenAI(
    api_key="YOUR_API_KEY",
    base_url="https://ark.cn-beijing.volces.com/api/v3"
)

response = client.chat.completions.create(
    model="doubao-pro-32k",
    messages=[
        {"role": "user", "content": "你好"}
    ]
)
```

## 特殊功能

### 角色扮演

使用角色模型:

```json
{
  "model": "doubao-character-assistant",
  "messages": [
    {
      "role": "system",
      "content": "你是一个专业的编程助手,擅长 Python 和 Go 语言。"
    },
    {
      "role": "user",
      "content": "如何优化 Go 程序的性能?"
    }
  ]
}
```

### 网络搜索插件

```json
{
  "model": "doubao-pro-32k",
  "plugins": [
    {
      "name": "web_search",
      "config": {
        "enable": true,
        "search_engine": "default"
      }
    }
  ],
  "messages": [
    {
      "role": "user",
      "content": "最近有什么重要新闻?"
    }
  ]
}
```

### 图片生成插件

```json
{
  "model": "doubao-pro-32k",
  "plugins": [
    {
      "name": "image_generation",
      "config": {
        "enable": true,
        "model": "doubao-image-v1"
      }
    }
  ],
  "messages": [
    {
      "role": "user",
      "content": "生成一张日落的图片"
    }
  ]
}
```

响应包含图片 URL:
```json
{
  "choices": [
    {
      "message": {
        "content": "这是为你生成的日落图片:",
        "plugin_calls": [
          {
            "name": "image_generation",
            "result": {
              "image_url": "https://..."
            }
          }
        ]
      }
    }
  ]
}
```

## 私有扩展总结

豆包 API 在 OpenAI 兼容基础上的扩展:

1. **插件系统**:
   - `web_search`: 网络搜索
   - `image_generation`: 图片生成
   - `code_interpreter`: 代码解释器
   - `file_reading`: 文件读取

2. **智能体支持**:
   - `bot_id`: 预配置智能体
   - `custom_bot_id`: 自定义智能体
   - 专业领域优化

3. **增强响应**:
   - `plugin_calls`: 插件调用详情
   - `logprobs`: 对数概率支持
   - `top_logprobs`: Top-N 对数概率

4. **多模态**:
   - 视觉理解(doubao-vision-pro)
   - 图片输入支持

5. **角色模型**:
   - doubao-character-* 系列
   - 专业角色定制

6. **额外 API**:
   - Tokenization API: Token 计数
   - 与 OpenAI 完全兼容的接口设计

7. **区域化部署**:
   - 中国境内优化
   - 低延迟访问
