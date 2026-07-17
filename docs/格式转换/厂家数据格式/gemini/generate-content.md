# Google Gemini API 规范

## API 概览

**端点**: 
```
POST https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent
POST https://generativelanguage.googleapis.com/v1beta/models/{model}:streamGenerateContent
```

**认证**: 
```
?key={API_KEY}
或
Authorization: Bearer {ACCESS_TOKEN}
```

## 请求格式

### 基础请求结构

```json
{
  "contents": [
    {
      "role": "user",
      "parts": [
        {"text": "Hello"}
      ]
    }
  ]
}
```

### 完整请求字段

| 字段 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `contents` | array | ✓ | 对话内容数组 |
| `systemInstruction` | object | ✗ | 系统指令 |
| `generationConfig` | object | ✗ | 生成配置 |
| `safetySettings` | array | ✗ | 安全设置 |
| `tools` | array | ✗ | 工具定义(函数调用) |
| `toolConfig` | object | ✗ | 工具配置 |

### Contents 数组格式

```json
{
  "contents": [
    {
      "role": "user",
      "parts": [
        {"text": "文本内容"}
      ]
    },
    {
      "role": "model",
      "parts": [
        {"text": "模型回复"}
      ]
    }
  ]
}
```

**角色类型**:
- `user`: 用户消息
- `model`: 模型消息

### Parts 格式

#### 文本 Part
```json
{
  "text": "这是文本内容"
}
```

#### 内联数据 Part (图片/音频/视频)
```json
{
  "inlineData": {
    "mimeType": "image/jpeg",
    "data": "base64编码的数据"
  }
}
```

**支持的 MIME 类型**:
- 图片: image/png, image/jpeg, image/webp, image/heic, image/heif
- 音频: audio/wav, audio/mp3, audio/aiff, audio/aac, audio/ogg, audio/flac
- 视频: video/mp4, video/mpeg, video/mov, video/avi, video/x-flv, video/mpg, video/webm, video/wmv, video/3gpp

#### 文件数据 Part
```json
{
  "fileData": {
    "mimeType": "application/pdf",
    "fileUri": "gs://bucket/path/to/file.pdf"
  }
}
```

#### 函数调用 Part
```json
{
  "functionCall": {
    "name": "get_weather",
    "args": {
      "location": "Boston"
    }
  }
}
```

#### 函数响应 Part
```json
{
  "functionResponse": {
    "name": "get_weather",
    "response": {
      "temperature": 72,
      "condition": "sunny"
    }
  }
}
```

### System Instruction

```json
{
  "systemInstruction": {
    "role": "system",
    "parts": [
      {"text": "You are a helpful assistant."}
    ]
  }
}
```

### Generation Config

```json
{
  "generationConfig": {
    "temperature": 0.9,
    "topP": 0.95,
    "topK": 40,
    "maxOutputTokens": 8192,
    "stopSequences": ["END"],
    "responseMimeType": "text/plain",
    "responseSchema": {},
    "candidateCount": 1,
    "presencePenalty": 0.0,
    "frequencyPenalty": 0.0
  }
}
```

**字段说明**:
- `temperature`: 采样温度 [0, 2], 默认 1.0
- `topP`: 核采样 [0, 1], 默认 0.95
- `topK`: Top-K 采样, 默认 40
- `maxOutputTokens`: 最大输出 tokens
- `stopSequences`: 停止序列
- `responseMimeType`: 响应格式 (text/plain, application/json)
- `responseSchema`: JSON Schema (配合 application/json 使用)
- `candidateCount`: 候选数量 [1, 8]
- `presencePenalty`: 存在惩罚 [-2, 2]
- `frequencyPenalty`: 频率惩罚 [-2, 2]

### Safety Settings

```json
{
  "safetySettings": [
    {
      "category": "HARM_CATEGORY_HARASSMENT",
      "threshold": "BLOCK_MEDIUM_AND_ABOVE"
    },
    {
      "category": "HARM_CATEGORY_HATE_SPEECH",
      "threshold": "BLOCK_MEDIUM_AND_ABOVE"
    },
    {
      "category": "HARM_CATEGORY_SEXUALLY_EXPLICIT",
      "threshold": "BLOCK_MEDIUM_AND_ABOVE"
    },
    {
      "category": "HARM_CATEGORY_DANGEROUS_CONTENT",
      "threshold": "BLOCK_MEDIUM_AND_ABOVE"
    }
  ]
}
```

**Category 类型**:
- `HARM_CATEGORY_HARASSMENT`: 骚扰
- `HARM_CATEGORY_HATE_SPEECH`: 仇恨言论
- `HARM_CATEGORY_SEXUALLY_EXPLICIT`: 色情内容
- `HARM_CATEGORY_DANGEROUS_CONTENT`: 危险内容

**Threshold 级别**:
- `BLOCK_NONE`: 不阻止
- `BLOCK_LOW_AND_ABOVE`: 阻止低及以上
- `BLOCK_MEDIUM_AND_ABOVE`: 阻止中及以上
- `BLOCK_ONLY_HIGH`: 仅阻止高危

### Tools (函数调用)

```json
{
  "tools": [
    {
      "functionDeclarations": [
        {
          "name": "get_weather",
          "description": "Get the current weather in a given location",
          "parameters": {
            "type": "object",
            "properties": {
              "location": {
                "type": "string",
                "description": "The city and state, e.g. San Francisco, CA"
              },
              "unit": {
                "type": "string",
                "enum": ["celsius", "fahrenheit"]
              }
            },
            "required": ["location"]
          }
        }
      ]
    }
  ]
}
```

### Tool Config

```json
{
  "toolConfig": {
    "functionCallingConfig": {
      "mode": "AUTO",
      "allowedFunctionNames": ["get_weather"]
    }
  }
}
```

**Mode 类型**:
- `AUTO`: 自动选择
- `ANY`: 必须调用函数
- `NONE`: 不调用函数

## 响应格式

### 非流式响应

```json
{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [
          {
            "text": "Hello! How can I help you today?"
          }
        ]
      },
      "finishReason": "STOP",
      "safetyRatings": [
        {
          "category": "HARM_CATEGORY_HARASSMENT",
          "probability": "NEGLIGIBLE"
        }
      ],
      "citationMetadata": {
        "citations": []
      },
      "tokenCount": 12
    }
  ],
  "usageMetadata": {
    "promptTokenCount": 5,
    "candidatesTokenCount": 12,
    "totalTokenCount": 17
  },
  "modelVersion": "gemini-1.5-pro-002"
}
```

### 响应字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `candidates` | array | 候选响应数组 |
| `usageMetadata` | object | Token 使用统计 |
| `modelVersion` | string | 模型版本 |
| `promptFeedback` | object | 提示词反馈(被阻止时) |

### Candidate 结构

| 字段 | 类型 | 说明 |
|------|------|------|
| `content` | object | 内容对象 |
| `finishReason` | string | 完成原因 |
| `safetyRatings` | array | 安全评级 |
| `citationMetadata` | object | 引用元数据 |
| `tokenCount` | integer | Token 数量 |
| `groundingMetadata` | object | Grounding 元数据 |
| `avgLogprobs` | float | 平均对数概率 |

**Finish Reason**:
- `STOP`: 自然停止
- `MAX_TOKENS`: 达到 token 限制
- `SAFETY`: 安全原因停止
- `RECITATION`: 重复内容停止
- `OTHER`: 其他原因
- `BLOCKLIST`: 黑名单
- `PROHIBITED_CONTENT`: 禁止内容
- `SPII`: 敏感个人信息

### 函数调用响应

```json
{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [
          {
            "functionCall": {
              "name": "get_weather",
              "args": {
                "location": "Boston"
              }
            }
          }
        ]
      },
      "finishReason": "STOP"
    }
  ]
}
```

提交函数结果后继续对话:
```json
{
  "contents": [
    {
      "role": "user",
      "parts": [{"text": "What's the weather in Boston?"}]
    },
    {
      "role": "model",
      "parts": [
        {
          "functionCall": {
            "name": "get_weather",
            "args": {"location": "Boston"}
          }
        }
      ]
    },
    {
      "role": "user",
      "parts": [
        {
          "functionResponse": {
            "name": "get_weather",
            "response": {
              "temperature": 72,
              "condition": "sunny"
            }
          }
        }
      ]
    }
  ]
}
```

### 流式响应

使用 `:streamGenerateContent` 端点,返回 SSE 格式:

```
data: {"candidates": [{"content": {"role": "model","parts": [{"text": "Hello"}]},"finishReason": "STOP"}],"usageMetadata": {"promptTokenCount": 5,"candidatesTokenCount": 1,"totalTokenCount": 6}}

data: {"candidates": [{"content": {"role": "model","parts": [{"text": "!"}]},"finishReason": "STOP"}],"usageMetadata": {"promptTokenCount": 5,"candidatesTokenCount": 2,"totalTokenCount": 7}}
```

每个 chunk 都是完整的响应结构,需累积 parts。

### Prompt Feedback

当提示词被阻止时:
```json
{
  "promptFeedback": {
    "blockReason": "SAFETY",
    "safetyRatings": [
      {
        "category": "HARM_CATEGORY_DANGEROUS_CONTENT",
        "probability": "HIGH"
      }
    ]
  }
}
```

**Block Reason**:
- `SAFETY`: 安全原因
- `OTHER`: 其他原因
- `BLOCKLIST`: 黑名单
- `PROHIBITED_CONTENT`: 禁止内容

## 多模态示例

### 图片理解

```json
{
  "contents": [
    {
      "role": "user",
      "parts": [
        {
          "inlineData": {
            "mimeType": "image/jpeg",
            "data": "/9j/4AAQSkZJRg..."
          }
        },
        {
          "text": "What is in this image?"
        }
      ]
    }
  ]
}
```

### 音频理解 (Gemini 1.5+)

```json
{
  "contents": [
    {
      "role": "user",
      "parts": [
        {
          "inlineData": {
            "mimeType": "audio/mp3",
            "data": "SUQzBAAAAAAAI1RTU0U..."
          }
        },
        {
          "text": "Transcribe this audio"
        }
      ]
    }
  ]
}
```

### 视频理解 (Gemini 1.5+)

```json
{
  "contents": [
    {
      "role": "user",
      "parts": [
        {
          "fileData": {
            "mimeType": "video/mp4",
            "fileUri": "gs://my-bucket/video.mp4"
          }
        },
        {
          "text": "Describe what happens in this video"
        }
      ]
    }
  ]
}
```

### PDF 文档 (Gemini 1.5+)

```json
{
  "contents": [
    {
      "role": "user",
      "parts": [
        {
          "fileData": {
            "mimeType": "application/pdf",
            "fileUri": "gs://my-bucket/document.pdf"
          }
        },
        {
          "text": "Summarize this document"
        }
      ]
    }
  ]
}
```

## 结构化输出 (JSON Mode)

```json
{
  "contents": [
    {
      "role": "user",
      "parts": [
        {"text": "List 5 popular cookie recipes"}
      ]
    }
  ],
  "generationConfig": {
    "responseMimeType": "application/json",
    "responseSchema": {
      "type": "object",
      "properties": {
        "recipes": {
          "type": "array",
          "items": {
            "type": "object",
            "properties": {
              "name": {"type": "string"},
              "ingredients": {
                "type": "array",
                "items": {"type": "string"}
              }
            },
            "required": ["name", "ingredients"]
          }
        }
      },
      "required": ["recipes"]
    }
  }
}
```

响应将严格遵守 schema:
```json
{
  "candidates": [
    {
      "content": {
        "parts": [
          {
            "text": "{\"recipes\": [{\"name\": \"Chocolate Chip\", \"ingredients\": [...]}]}"
          }
        ]
      }
    }
  ]
}
```

## 模型列表

| 模型 | 上下文窗口 | 输出限制 | 特性 |
|------|-----------|---------|------|
| gemini-2.0-flash-exp | 1M | 8K | 最新实验版,多模态 |
| gemini-1.5-pro | 2M | 8K | 长上下文,多模态 |
| gemini-1.5-flash | 1M | 8K | 快速,多模态 |
| gemini-1.5-flash-8b | 1M | 8K | 超快,经济 |
| gemini-1.0-pro | 32K | 2K | 文本生成 |

## Thinking Mode (Gemini 2.0 Flash Thinking)

**模型**: `gemini-2.0-flash-thinking-exp-01-21`

响应包含思考过程:
```json
{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [
          {
            "thought": true,
            "text": "Let me think about this problem step by step..."
          },
          {
            "text": "Based on my analysis, the answer is..."
          }
        ]
      }
    }
  ]
}
```

**特点**:
- `thought: true` 标记思考内容
- 自动进行推理链(Chain of Thought)
- 适合复杂问题和数学推理

## Code Execution

模型可以生成并执行 Python 代码:

```json
{
  "tools": [
    {"codeExecution": {}}
  ]
}
```

响应:
```json
{
  "candidates": [
    {
      "content": {
        "parts": [
          {
            "executableCode": {
              "language": "PYTHON",
              "code": "print(2 + 2)"
            }
          },
          {
            "codeExecutionResult": {
              "outcome": "OUTCOME_OK",
              "output": "4\n"
            }
          },
          {
            "text": "The result is 4."
          }
        ]
      }
    }
  ]
}
```

## Grounding (基于 Google 搜索)

```json
{
  "tools": [
    {
      "googleSearchRetrieval": {
        "dynamicRetrievalConfig": {
          "mode": "MODE_DYNAMIC",
          "dynamicThreshold": 0.7
        }
      }
    }
  ]
}
```

响应包含来源:
```json
{
  "candidates": [
    {
      "content": {...},
      "groundingMetadata": {
        "groundingSupports": [
          {
            "segment": {
              "startIndex": 0,
              "endIndex": 100,
              "text": "..."
            },
            "groundingChunkIndices": [0],
            "confidenceScores": [0.9]
          }
        ],
        "webSearchQueries": ["query"],
        "searchEntryPoint": {
          "renderedContent": "..."
        }
      }
    }
  ]
}
```

## 缓存 (Context Caching)

通过 `cachedContents` API 缓存长上下文:

1. **创建缓存**:
```
POST /v1beta/cachedContents
{
  "model": "models/gemini-1.5-pro-001",
  "contents": [...],
  "systemInstruction": {...},
  "ttl": "3600s"
}
```

2. **使用缓存**:
```json
{
  "cachedContent": "cachedContents/abc123",
  "contents": [
    {
      "role": "user",
      "parts": [{"text": "新问题"}]
    }
  ]
}
```

## 错误响应

```json
{
  "error": {
    "code": 400,
    "message": "Invalid request",
    "status": "INVALID_ARGUMENT",
    "details": [...]
  }
}
```

**常见错误码**:
- `400`: 请求格式错误
- `403`: API Key 无效或权限不足
- `404`: 模型不存在
- `429`: 速率限制
- `500`: 服务器错误
- `503`: 服务不可用
