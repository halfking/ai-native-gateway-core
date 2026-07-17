# 智谱 GLM API 规范

## API 概览

**端点**: `POST https://open.bigmodel.cn/api/paas/v4/chat/completions`

**认证**: 
```
Authorization: Bearer {JWT_TOKEN}
```

**说明**: GLM API 兼容 OpenAI Chat Completions API,同时提供私有扩展字段。

## 请求格式

### 基础请求结构

```json
{
  "model": "glm-4",
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
| `temperature` | float | ✗ | 采样温度 [0, 1], 默认 0.95 |
| `top_p` | float | ✗ | 核采样 [0, 1], 默认 0.7 |
| `max_tokens` | integer | ✗ | 最大生成 tokens |
| `stop` | string/array | ✗ | 停止词 |
| `stream` | boolean | ✗ | 是否流式返回 |
| `tools` | array | ✗ | 工具定义(函数调用) |
| `tool_choice` | string/object | ✗ | 工具选择策略 |
| `request_id` | string | ✗ | 请求 ID(幂等性) |

### GLM 私有字段

| 字段 | 类型 | 说明 |
|------|------|------|
| `do_sample` | boolean | 是否启用采样,默认 true |
| `meta` | object | 元数据(user_info, bot_info 等) |

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
- `tool`: 工具返回结果(函数调用场景)

### Content 格式

#### 文本内容
```json
{
  "role": "user",
  "content": "文本内容"
}
```

#### 多模态内容 (GLM-4V)
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

**图片 URL 格式**:
- HTTP/HTTPS URL: `https://example.com/image.jpg`
- Base64: `data:image/jpeg;base64,/9j/4AAQSkZJRg...`

**支持的图片格式**: JPEG, PNG, GIF, WebP

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

// 指定函数
"tool_choice": {
  "type": "function",
  "function": {"name": "get_weather"}
}
```

### Web Search (联网搜索)

```json
{
  "tools": [
    {
      "type": "web_search",
      "web_search": {
        "enable": true,
        "search_query": "可选的自定义搜索词"
      }
    }
  ]
}
```

### Retrieval (知识库检索)

```json
{
  "tools": [
    {
      "type": "retrieval",
      "retrieval": {
        "knowledge_id": "知识库ID",
        "prompt_template": "{{knowledge}}\n{{question}}"
      }
    }
  ]
}
```

## 响应格式

### 非流式响应

```json
{
  "id": "8703658188965586199",
  "created": 1709617435,
  "model": "glm-4",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "你好!我是智谱清言,有什么可以帮助你的吗?"
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 20,
    "total_tokens": 30
  }
}
```

### 响应字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | string | 请求 ID |
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

**Finish Reason**:
- `stop`: 自然停止
- `length`: 达到长度限制
- `tool_calls`: 需要调用工具
- `content_filter`: 内容过滤
- `sensitive`: 敏感内容(GLM 私有)
- `network_error`: 网络错误(GLM 私有)

### 工具调用响应

```json
{
  "id": "8703658188965586200",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": null,
        "tool_calls": [
          {
            "id": "call_8703658188965586201",
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
  "model": "glm-4",
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
          "id": "call_8703658188965586201",
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
      "tool_call_id": "call_8703658188965586201"
    }
  ]
}
```

### Web Search 响应

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": "根据搜索结果...",
        "tool_calls": [
          {
            "id": "call_web_search",
            "type": "web_search",
            "web_search": {
              "content": "搜索结果内容",
              "title": "页面标题",
              "url": "https://...",
              "icon": "https://..."
            }
          }
        ]
      }
    }
  ]
}
```

### 流式响应

使用 SSE (Server-Sent Events) 格式:

```
data: {"id":"8703658188965586199","created":1709617435,"model":"glm-4","choices":[{"index":0,"delta":{"role":"assistant","content":"你好"},"finish_reason":null}]}

data: {"id":"8703658188965586199","created":1709617435,"model":"glm-4","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: {"id":"8703658188965586199","created":1709617435,"model":"glm-4","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}

data: [DONE]
```

**Delta 结构**:
- 首个 chunk 包含 `role`
- 后续 chunk 只包含增量 `content`
- 最后一个 chunk 包含 `finish_reason` 和 `usage`

### 工具调用流式响应

```
data: {"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_xxx","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"location\""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":": \"北京\"}"}}]}}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
```

## 多模态 (GLM-4V)

### 图片理解

```json
{
  "model": "glm-4v",
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
        "url": "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQAA..."
      }
    }
  ]
}
```

## 特殊功能

### GLM-4-AllTools

支持自动选择多种工具:
```json
{
  "model": "glm-4-alltools",
  "messages": [...],
  "tools": [
    {"type": "web_search", "web_search": {"enable": true}},
    {"type": "code_interpreter"},
    {"type": "drawing_tool"}
  ]
}
```

### Code Interpreter

```json
{
  "tools": [
    {
      "type": "code_interpreter"
    }
  ]
}
```

响应包含代码执行结果:
```json
{
  "message": {
    "tool_calls": [
      {
        "type": "code_interpreter",
        "code_interpreter": {
          "input": "print(2+2)",
          "outputs": [
            {
              "type": "text",
              "text": "4"
            }
          ]
        }
      }
    ]
  }
}
```

### Drawing Tool (绘图)

```json
{
  "tools": [
    {
      "type": "drawing_tool"
    }
  ]
}
```

响应包含图片 URL:
```json
{
  "message": {
    "tool_calls": [
      {
        "type": "drawing_tool",
        "drawing_tool": {
          "image_url": "https://..."
        }
      }
    ]
  }
}
```

## 模型列表

| 模型 | 上下文窗口 | 输出限制 | 特性 |
|------|-----------|---------|------|
| glm-4 | 128K | 4K | 通用对话 |
| glm-4v | 128K | 4K | 视觉理解 |
| glm-4-air | 128K | 4K | 快速推理 |
| glm-4-airx | 128K | 8K | 高性能 |
| glm-4-flash | 128K | 4K | 免费、快速 |
| glm-4-alltools | 128K | 4K | 全工具支持 |
| glm-3-turbo | 128K | 4K | 经济版 |

## 错误响应

```json
{
  "error": {
    "message": "Invalid authentication credentials",
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

## 认证方式

### JWT Token 生成

使用 API Key 和 Secret 生成 JWT Token:

**Header**:
```json
{
  "alg": "HS256",
  "sign_type": "SIGN"
}
```

**Payload**:
```json
{
  "api_key": "your_api_key",
  "exp": 1719471031,
  "timestamp": 1719467431
}
```

**签名**: 使用 API Secret 进行 HS256 签名

**完整 Token 格式**:
```
{API_KEY}.{PAYLOAD}.{SIGNATURE}
```

### 请求示例

```bash
curl https://open.bigmodel.cn/api/paas/v4/chat/completions \
  -H "Authorization: Bearer {JWT_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-4",
    "messages": [
      {"role": "user", "content": "你好"}
    ]
  }'
```

## 私有扩展总结

GLM API 在 OpenAI 兼容基础上的扩展:

1. **工具类型扩展**:
   - `web_search`: 联网搜索
   - `retrieval`: 知识库检索
   - `code_interpreter`: 代码解释器
   - `drawing_tool`: 绘图工具

2. **Finish Reason 扩展**:
   - `sensitive`: 敏感内容
   - `network_error`: 网络错误

3. **私有字段**:
   - `do_sample`: 采样开关
   - `meta`: 元数据
   - `request_id`: 幂等性 ID

4. **认证方式**:
   - 使用 JWT Token 而非 Bearer Token
