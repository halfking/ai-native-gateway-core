# 通义千问 Qwen API 规范

## API 概览

**端点**:
```
POST https://dashscope.aliyuncs.com/api/v1/services/aigc/text-generation/generation
POST https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions (OpenAI 兼容)
```

**认证**:
```
Authorization: Bearer {API_KEY}
```

**说明**: Qwen API 提供 DashScope 原生接口和 OpenAI 兼容接口两种方式。

## OpenAI 兼容模式

### 基础请求结构

```json
{
  "model": "qwen-plus",
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
| `top_p` | float | ✗ | 核采样 [0, 1], 默认 0.8 |
| `max_tokens` | integer | ✗ | 最大生成 tokens |
| `stream` | boolean | ✗ | 是否流式返回 |
| `stop` | string/array | ✗ | 停止词 |
| `presence_penalty` | float | ✗ | 存在惩罚 [-2, 2] |
| `frequency_penalty` | float | ✗ | 频率惩罚 [-2, 2] |
| `tools` | array | ✗ | 工具定义 |
| `tool_choice` | string/object | ✗ | 工具选择策略 |
| `response_format` | object | ✗ | 响应格式 |

### Messages 数组格式

```json
{
  "messages": [
    {
      "role": "system",
      "content": "你是一个有帮助的助手"
    },
    {
      "role": "user",
      "content": "你好"
    },
    {
      "role": "assistant",
      "content": "你好!有什么可以帮助你的吗?"
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

#### 多模态内容 (qwen-vl-plus, qwen-vl-max)
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
- 本地文件: `file:///path/to/image.jpg`
- OSS: `oss://bucket/path/to/image.jpg`

#### 音频内容 (qwen-audio)
```json
{
  "role": "user",
  "content": [
    {
      "type": "audio",
      "audio_url": {
        "url": "https://example.com/audio.mp3"
      }
    },
    {
      "type": "text",
      "text": "转录这段音频"
    }
  ]
}
```

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
              "description": "城市名称"
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

// 指定函数
"tool_choice": {
  "type": "function",
  "function": {"name": "get_weather"}
}
```

## DashScope 原生接口

### 请求格式

```json
{
  "model": "qwen-plus",
  "input": {
    "messages": [
      {
        "role": "user",
        "content": "你好"
      }
    ]
  },
  "parameters": {
    "temperature": 0.8,
    "top_p": 0.8,
    "max_tokens": 1500,
    "result_format": "message"
  }
}
```

### DashScope 特有字段

| 字段 | 类型 | 说明 |
|------|------|------|
| `input` | object | 输入对象 |
| `parameters` | object | 参数对象 |
| `parameters.result_format` | string | 响应格式 (message/text) |
| `parameters.enable_search` | boolean | 是否启用搜索 |
| `parameters.seed` | integer | 随机种子 |
| `parameters.repetition_penalty` | float | 重复惩罚 [1.0, 2.0] |

### 多模态输入 (DashScope 格式)

```json
{
  "model": "qwen-vl-plus",
  "input": {
    "messages": [
      {
        "role": "user",
        "content": [
          {"image": "https://example.com/image.jpg"},
          {"text": "描述这张图片"}
        ]
      }
    ]
  }
}
```

### 联网搜索

```json
{
  "model": "qwen-plus",
  "input": {
    "messages": [{"role": "user", "content": "最近有什么新闻?"}]
  },
  "parameters": {
    "enable_search": true
  }
}
```

## 响应格式 (OpenAI 兼容)

### 非流式响应

```json
{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "created": 1719467431,
  "model": "qwen-plus",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "你好!很高兴为你服务,有什么可以帮助你的吗?"
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 25,
    "total_tokens": 35
  }
}
```

### DashScope 原生响应

```json
{
  "output": {
    "text": null,
    "finish_reason": "stop",
    "choices": [
      {
        "finish_reason": "stop",
        "message": {
          "role": "assistant",
          "content": "你好!很高兴为你服务,有什么可以帮助你的吗?"
        }
      }
    ]
  },
  "usage": {
    "total_tokens": 35,
    "input_tokens": 10,
    "output_tokens": 25
  },
  "request_id": "fb53c4ec-1c12-4fc2-a6bd-6aaf9c2b32d1"
}
```

### Finish Reason

**OpenAI 兼容**:
- `stop`: 自然停止
- `length`: 达到长度限制
- `tool_calls`: 需要调用工具
- `content_filter`: 内容过滤

**DashScope 原生**:
- `stop`: 正常结束
- `length`: 因长度限制停止
- `null`: 生成中(流式)

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

### 流式响应 (OpenAI 兼容)

```
data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1719467431,"model":"qwen-plus","choices":[{"index":0,"delta":{"role":"assistant","content":"你好"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1719467431,"model":"qwen-plus","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1719467431,"model":"qwen-plus","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":25,"total_tokens":35}}

data: [DONE]
```

### 流式响应 (DashScope 原生)

```
data:{"output":{"choices":[{"finish_reason":"null","message":{"role":"assistant","content":"你好"}}]},"usage":{"total_tokens":12,"input_tokens":10,"output_tokens":2},"request_id":"fb53c4ec"}

data:{"output":{"choices":[{"finish_reason":"null","message":{"role":"assistant","content":"!"}}]},"usage":{"total_tokens":13,"input_tokens":10,"output_tokens":3},"request_id":"fb53c4ec"}

data:{"output":{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":""}}]},"usage":{"total_tokens":35,"input_tokens":10,"output_tokens":25},"request_id":"fb53c4ec"}
```

## 多模态功能

### 视觉理解 (qwen-vl-plus/max)

```json
{
  "model": "qwen-vl-plus",
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

### 音频理解 (qwen-audio)

```json
{
  "model": "qwen-audio",
  "messages": [
    {
      "role": "user",
      "content": [
        {
          "type": "audio",
          "audio_url": {
            "url": "https://example.com/audio.mp3"
          }
        },
        {
          "type": "text",
          "text": "转录这段音频"
        }
      ]
    }
  ]
}
```

### 长文档理解 (qwen-long)

```json
{
  "model": "qwen-long",
  "messages": [
    {
      "role": "user",
      "content": "很长的文档内容...(最长 1000 万 tokens)"
    }
  ]
}
```

## 特殊功能

### JSON 模式

```json
{
  "response_format": {
    "type": "json_object"
  }
}
```

### Seed 固定输出

```json
{
  "parameters": {
    "seed": 42
  }
}
```

使用相同的 seed 和输入可以获得一致的输出(非绝对保证)。

### 重复惩罚

```json
{
  "parameters": {
    "repetition_penalty": 1.5
  }
}
```

控制输出重复度,[1.0, 2.0],越大越少重复。

## 模型列表

| 模型 | 上下文窗口 | 输出限制 | 特性 |
|------|-----------|---------|------|
| qwen-max | 32K | 8K | 最强通用模型 |
| qwen-plus | 128K | 8K | 高性能通用 |
| qwen-turbo | 128K | 8K | 快速经济 |
| qwen-long | 10M | 8K | 超长上下文 |
| qwen-vl-plus | 8K | 2K | 视觉理解 |
| qwen-vl-max | 8K | 8K | 高级视觉 |
| qwen-audio-turbo | 8K | 2K | 音频理解 |
| qwen2.5-72b-instruct | 128K | 8K | 开源版本 |

## 批量推理

**端点**: `POST https://dashscope.aliyuncs.com/api/v1/services/aigc/text-generation/generation`

```json
{
  "model": "qwen-plus",
  "input": {
    "messages": [
      [
        {"role": "user", "content": "问题1"}
      ],
      [
        {"role": "user", "content": "问题2"}
      ]
    ]
  }
}
```

响应返回多个结果。

## 异步调用

**提交任务**:
```
POST /api/v1/services/aigc/text-generation/generation
X-DashScope-Async: enable
```

响应:
```json
{
  "output": {
    "task_id": "task-abc123",
    "task_status": "PENDING"
  },
  "request_id": "..."
}
```

**查询结果**:
```
GET /api/v1/tasks/{task_id}
```

## 错误响应

### OpenAI 兼容格式

```json
{
  "error": {
    "message": "Invalid API key",
    "type": "invalid_request_error",
    "code": "invalid_api_key"
  }
}
```

### DashScope 原生格式

```json
{
  "code": "InvalidApiKey",
  "message": "Invalid API-key provided.",
  "request_id": "fb53c4ec-1c12-4fc2-a6bd-6aaf9c2b32d1"
}
```

**常见错误码**:
- `InvalidApiKey`: API Key 无效
- `InvalidParameter`: 参数错误
- `Throttling.RateQuota`: 速率限制
- `InternalError.Timeout`: 超时
- `FlowNotWhitelist`: 未开通
- `DataInspectionFailed`: 内容安全检查失败

## 认证方式

### API Key (推荐)

```bash
curl https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions \
  -H "Authorization: Bearer {API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen-plus",
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
    base_url="https://dashscope.aliyuncs.com/compatible-mode/v1"
)

response = client.chat.completions.create(
    model="qwen-plus",
    messages=[
        {"role": "user", "content": "你好"}
    ]
)
```

## 私有扩展总结

Qwen API 的特色功能:

1. **双接口模式**:
   - OpenAI 兼容: `/compatible-mode/v1/*`
   - DashScope 原生: `/api/v1/services/aigc/*`

2. **DashScope 特有字段**:
   - `enable_search`: 联网搜索
   - `seed`: 固定随机种子
   - `repetition_penalty`: 重复惩罚
   - `result_format`: 响应格式控制

3. **超长上下文**:
   - `qwen-long`: 支持 1000 万 tokens

4. **多模态支持**:
   - 视觉理解(qwen-vl)
   - 音频理解(qwen-audio)
   - 多种图片源(HTTP/OSS/本地)

5. **批量和异步**:
   - 批量推理支持
   - 异步任务模式

6. **响应格式**:
   - DashScope 原生格式与 OpenAI 格式略有差异
   - `output` vs `choices` 结构
