# MiniMax API 规范

## API 概览

**端点**: 
```
POST https://api.minimax.chat/v1/text/chatcompletion_v2
POST https://api.minimax.chat/v1/messages (Anthropic 兼容)
```

**认证**: 
```
Authorization: Bearer {API_KEY}
```

**说明**: MiniMax API 提供 OpenAI 兼容接口,同时支持私有扩展字段和 Anthropic 风格接口。

## 请求格式 (OpenAI 风格)

### 基础请求结构

```json
{
  "model": "abab6.5s-chat",
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
| `temperature` | float | ✗ | 采样温度 [0.01, 2], 默认 0.9 |
| `top_p` | float | ✗ | 核采样 [0, 1], 默认 0.95 |
| `max_tokens` | integer | ✗ | 最大生成 tokens, 默认 2048 |
| `stream` | boolean | ✗ | 是否流式返回 |
| `tools` | array | ✗ | 工具定义 |
| `tool_choice` | string/object | ✗ | 工具选择策略 |
| `stop` | array | ✗ | 停止词 |
| `frequency_penalty` | float | ✗ | 频率惩罚 [0, 2] |
| `presence_penalty` | float | ✗ | 存在惩罚 [0, 2] |

### MiniMax 私有字段

| 字段 | 类型 | 说明 |
|------|------|------|
| `bot_setting` | array | 角色设定(人设) |
| `reply_constraints` | object | 回复约束 |
| `sample_messages` | array | 样本消息(few-shot) |
| `plugins` | array | 插件列表 |
| `mask_sensitive_info` | boolean | 是否屏蔽敏感信息 |
| `continous_mode` | boolean | 连续模式 |

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

#### 多模态内容 (abab6.5g-chat)
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
- 支持格式: JPEG, PNG, WebP

### Bot Setting (角色设定)

```json
{
  "bot_setting": [
    {
      "bot_name": "专业助手",
      "content": "你是一个专业的技术顾问,擅长解答编程问题。"
    }
  ]
}
```

### Reply Constraints (回复约束)

```json
{
  "reply_constraints": {
    "sender_type": "BOT",
    "sender_name": "助手",
    "glyph": "🤖"
  }
}
```

### Sample Messages (Few-shot 样本)

```json
{
  "sample_messages": [
    {
      "role": "user",
      "content": "什么是 AI?"
    },
    {
      "role": "assistant",
      "content": "AI 是人工智能(Artificial Intelligence)的缩写..."
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

// 必须调用
"tool_choice": "required"

// 指定函数
"tool_choice": {
  "type": "function",
  "function": {"name": "get_weather"}
}
```

### Plugins (插件系统)

MiniMax 提供内置插件:

```json
{
  "plugins": [
    "plugin:web_search",
    "plugin:calculator"
  ]
}
```

**内置插件**:
- `plugin:web_search`: 网络搜索
- `plugin:calculator`: 计算器
- `plugin:code_interpreter`: 代码解释器

## 响应格式

### 非流式响应

```json
{
  "id": "0190abcd1234567890abcdef",
  "object": "chat.completion",
  "created": 1719467431,
  "model": "abab6.5s-chat",
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

### 响应字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | string | 请求 ID |
| `object` | string | 对象类型 |
| `created` | integer | 创建时间戳 |
| `model` | string | 使用的模型 |
| `choices` | array | 生成结果数组 |
| `usage` | object | Token 使用统计 |
| `base_resp` | object | MiniMax 私有响应信息 |

### Choice 结构

| 字段 | 类型 | 说明 |
|------|------|------|
| `index` | integer | 索引 |
| `message` | object | 消息对象 |
| `finish_reason` | string | 完成原因 |

**Finish Reason**:
- `stop`: 自然停止
- `length`: 达到长度限制
- `tool_calls`: 需要调用工具
- `content_filter`: 内容过滤
- `function_call`: 函数调用(旧版)

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

### 流式响应

使用 SSE 格式:

```
data: {"id":"0190abcd","created":1719467431,"model":"abab6.5s-chat","choices":[{"index":0,"delta":{"role":"assistant","content":"你好"},"finish_reason":null}]}

data: {"id":"0190abcd","created":1719467431,"model":"abab6.5s-chat","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: {"id":"0190abcd","created":1719467431,"model":"abab6.5s-chat","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":25,"total_tokens":35}}

data: [DONE]
```

**Delta 结构**:
- 首个 chunk 包含 `role`
- 后续 chunk 包含增量 `content`
- 最后一个 chunk 包含 `finish_reason` 和 `usage`

### MiniMax 私有响应字段

```json
{
  "base_resp": {
    "status_code": 0,
    "status_msg": "success"
  },
  "input_sensitive": false,
  "output_sensitive": false
}
```

## Anthropic 风格接口

### 请求格式

```json
{
  "model": "abab6.5s-chat",
  "messages": [
    {
      "role": "user",
      "content": "你好"
    }
  ],
  "max_tokens": 1024
}
```

### 响应格式

```json
{
  "id": "msg_0190abcd",
  "type": "message",
  "role": "assistant",
  "model": "abab6.5s-chat",
  "content": [
    {
      "type": "text",
      "text": "你好!有什么可以帮助你的吗?"
    }
  ],
  "stop_reason": "end_turn",
  "usage": {
    "input_tokens": 10,
    "output_tokens": 25
  }
}
```

## 多模态 (abab6.5g-chat)

### 图片理解

```json
{
  "model": "abab6.5g-chat",
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

## 语音合成 (TTS)

**端点**: `POST https://api.minimax.chat/v1/t2a_v2`

```json
{
  "model": "speech-01-turbo",
  "text": "你好,这是一段测试语音",
  "voice_id": "male-qn-qingse",
  "speed": 1.0,
  "vol": 1.0,
  "pitch": 0,
  "audio_sample_rate": 24000,
  "bitrate": 128000,
  "format": "mp3"
}
```

**响应**: 返回音频文件的 base64 编码或直接音频流。

## 语音识别 (ASR)

**端点**: `POST https://api.minimax.chat/v1/speech/asr`

```json
{
  "model": "speech-01",
  "file": "base64编码的音频数据",
  "language": "zh"
}
```

**响应**:
```json
{
  "text": "识别出的文本内容",
  "segments": [
    {
      "start": 0.0,
      "end": 2.5,
      "text": "识别出的文本"
    }
  ]
}
```

## 模型列表

| 模型 | 上下文窗口 | 输出限制 | 特性 |
|------|-----------|---------|------|
| abab6.5s-chat | 245K | 8K | 超长上下文 |
| abab6.5g-chat | 245K | 8K | 多模态(文本+图片) |
| abab6.5t-chat | 8K | 8K | 快速推理 |
| abab5.5s-chat | 16K | 8K | 经济版 |
| abab5.5-chat | 6K | 2K | 基础版 |

## 特殊功能

### 连续对话模式

```json
{
  "continous_mode": true,
  "messages": [...]
}
```

在此模式下,模型会更好地维护对话上下文。

### 敏感信息屏蔽

```json
{
  "mask_sensitive_info": true,
  "messages": [...]
}
```

自动屏蔽输出中的手机号、身份证号等敏感信息。

### Web Search 插件

```json
{
  "plugins": ["plugin:web_search"],
  "messages": [
    {
      "role": "user",
      "content": "最近有什么重要新闻?"
    }
  ]
}
```

响应包含搜索结果:
```json
{
  "choices": [
    {
      "message": {
        "content": "根据最新搜索结果...",
        "plugin_call": [
          {
            "name": "web_search",
            "arguments": "{\"query\": \"重要新闻\"}",
            "result": "搜索结果内容"
          }
        ]
      }
    }
  ]
}
```

## 错误响应

```json
{
  "base_resp": {
    "status_code": 1004,
    "status_msg": "Invalid API key"
  }
}
```

**常见错误码**:
- `1000`: 参数错误
- `1004`: 认证失败
- `1008`: 速率限制
- `1013`: 内容安全过滤
- `1027`: 余额不足
- `2013`: 输入内容过长

## 认证方式

### API Key

```bash
curl https://api.minimax.chat/v1/text/chatcompletion_v2 \
  -H "Authorization: Bearer {API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "abab6.5s-chat",
    "messages": [
      {"role": "user", "content": "你好"}
    ]
  }'
```

### Group ID

部分 API 需要额外的 GroupID 参数:

```
https://api.minimax.chat/v1/text/chatcompletion_v2?GroupId={GROUP_ID}
```

## 私有扩展总结

MiniMax API 在 OpenAI 兼容基础上的扩展:

1. **角色设定**:
   - `bot_setting`: 自定义人设
   - `reply_constraints`: 回复约束

2. **增强功能**:
   - `plugins`: 插件系统(web_search, calculator)
   - `sample_messages`: Few-shot 学习
   - `mask_sensitive_info`: 敏感信息屏蔽
   - `continous_mode`: 连续对话模式

3. **多模态**:
   - 图片理解(abab6.5g-chat)
   - 语音合成(TTS)
   - 语音识别(ASR)

4. **响应增强**:
   - `base_resp`: 详细状态信息
   - `input_sensitive`/`output_sensitive`: 敏感信息标记

5. **兼容性**:
   - 支持 OpenAI 风格 API
   - 支持 Anthropic 风格 API
