# Ollama API 规范

## API 概览

**端点**:
```
POST http://localhost:11434/api/chat (原生接口)
POST http://localhost:11434/v1/chat/completions (OpenAI 兼容)
```

**认证**: 默认无需认证(本地部署)

**说明**: Ollama 提供本地 LLM 部署方案,支持原生接口和 OpenAI 兼容接口。

## OpenAI 兼容接口

### 基础请求结构

```json
{
  "model": "llama3.2",
  "messages": [
    {
      "role": "user",
      "content": "Hello"
    }
  ]
}
```

### 完整请求字段

| 字段 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `model` | string | ✓ | 模型名称 |
| `messages` | array | ✓ | 对话消息数组 |
| `temperature` | float | ✗ | 采样温度 [0, 2], 默认 0.8 |
| `top_p` | float | ✗ | 核采样 [0, 1], 默认 0.9 |
| `max_tokens` | integer | ✗ | 最大生成 tokens |
| `stream` | boolean | ✗ | 是否流式返回 |
| `stop` | string/array | ✗ | 停止词 |
| `frequency_penalty` | float | ✗ | 频率惩罚 [0, 2] |
| `presence_penalty` | float | ✗ | 存在惩罚 [0, 2] |
| `tools` | array | ✗ | 工具定义(部分模型支持) |

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
      "text": "What's in this image?"
    },
    {
      "type": "image_url",
      "image_url": {
        "url": "data:image/jpeg;base64,/9j/4AAQSkZJRg..."
      }
    }
  ]
}
```

**视觉模型**: llava, llava-phi3, bakllava 等

## Ollama 原生接口

### Chat API

**端点**: `POST /api/chat`

```json
{
  "model": "llama3.2",
  "messages": [
    {
      "role": "user",
      "content": "Hello"
    }
  ],
  "stream": false
}
```

### 原生接口特有字段

| 字段 | 类型 | 说明 |
|------|------|------|
| `format` | string | 响应格式 ("json") |
| `options` | object | 模型参数配置 |
| `keep_alive` | string | 模型保持加载时间 (如 "5m", "300s") |
| `template` | string | 自定义提示词模板 |

### Options 参数

```json
{
  "options": {
    "temperature": 0.8,
    "top_k": 40,
    "top_p": 0.9,
    "num_predict": 128,
    "stop": ["\n", "user:"],
    "seed": 42,
    "num_ctx": 2048,
    "repeat_penalty": 1.1,
    "repeat_last_n": 64,
    "mirostat": 0,
    "mirostat_tau": 5.0,
    "mirostat_eta": 0.1,
    "tfs_z": 1.0,
    "num_thread": 8
  }
}
```

**Options 字段说明**:
- `temperature`: 采样温度
- `top_k`: Top-K 采样
- `top_p`: 核采样
- `num_predict`: 最大预测 tokens
- `stop`: 停止序列
- `seed`: 随机种子
- `num_ctx`: 上下文窗口大小
- `repeat_penalty`: 重复惩罚
- `repeat_last_n`: 考虑重复的最后 N 个 tokens
- `mirostat`: Mirostat 采样 (0=关闭, 1=Mirostat, 2=Mirostat 2.0)
- `mirostat_tau`: Mirostat 目标熵
- `mirostat_eta`: Mirostat 学习率
- `tfs_z`: Tail Free Sampling
- `num_thread`: 线程数

### 多模态输入 (原生格式)

```json
{
  "model": "llava",
  "messages": [
    {
      "role": "user",
      "content": "What's in this image?",
      "images": [
        "/9j/4AAQSkZJRg..."
      ]
    }
  ]
}
```

**images 字段**: base64 编码的图片数据

### JSON 模式

```json
{
  "model": "llama3.2",
  "messages": [
    {
      "role": "user",
      "content": "List 3 countries and their capitals in JSON"
    }
  ],
  "format": "json",
  "stream": false
}
```

响应将是有效的 JSON 格式。

## 响应格式

### OpenAI 兼容响应

```json
{
  "id": "chatcmpl-123",
  "object": "chat.completion",
  "created": 1719467431,
  "model": "llama3.2",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Hello! How can I help you today?"
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

### 原生接口响应

```json
{
  "model": "llama3.2",
  "created_at": "2024-07-18T12:34:56.789Z",
  "message": {
    "role": "assistant",
    "content": "Hello! How can I help you today?"
  },
  "done": true,
  "total_duration": 5000000000,
  "load_duration": 1000000000,
  "prompt_eval_count": 10,
  "prompt_eval_duration": 1500000000,
  "eval_count": 25,
  "eval_duration": 2500000000
}
```

**原生响应字段**:
- `done`: 是否完成
- `total_duration`: 总耗时(纳秒)
- `load_duration`: 模型加载耗时(纳秒)
- `prompt_eval_count`: 提示词 token 数
- `prompt_eval_duration`: 提示词评估耗时(纳秒)
- `eval_count`: 生成 token 数
- `eval_duration`: 生成耗时(纳秒)

### 流式响应 (OpenAI 兼容)

```
data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1719467431,"model":"llama3.2","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1719467431,"model":"llama3.2","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1719467431,"model":"llama3.2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

### 流式响应 (原生接口)

```json
{"model":"llama3.2","created_at":"2024-07-18T12:34:56.789Z","message":{"role":"assistant","content":"Hello"},"done":false}
{"model":"llama3.2","created_at":"2024-07-18T12:34:56.790Z","message":{"role":"assistant","content":"!"},"done":false}
{"model":"llama3.2","created_at":"2024-07-18T12:34:56.791Z","message":{"role":"assistant","content":""},"done":true,"total_duration":5000000000,"load_duration":1000000000,"prompt_eval_count":10,"prompt_eval_duration":1500000000,"eval_count":25,"eval_duration":2500000000}
```

每行一个完整的 JSON 对象,最后一个对象 `done: true` 并包含统计信息。

## 工具调用 (实验性)

部分模型支持工具调用(如 llama3.1, mistral):

```json
{
  "model": "llama3.1",
  "messages": [
    {
      "role": "user",
      "content": "What's the weather in Beijing?"
    }
  ],
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get the current weather",
        "parameters": {
          "type": "object",
          "properties": {
            "location": {
              "type": "string",
              "description": "The city name"
            }
          },
          "required": ["location"]
        }
      }
    }
  ]
}
```

响应包含工具调用:
```json
{
  "message": {
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
  }
}
```

## 模型管理 API

### 列出模型

**端点**: `GET /api/tags`

响应:
```json
{
  "models": [
    {
      "name": "llama3.2:latest",
      "modified_at": "2024-07-18T12:34:56.789Z",
      "size": 4661224448,
      "digest": "sha256:abc123...",
      "details": {
        "format": "gguf",
        "family": "llama",
        "families": ["llama"],
        "parameter_size": "3B",
        "quantization_level": "Q4_0"
      }
    }
  ]
}
```

### 拉取模型

**端点**: `POST /api/pull`

```json
{
  "name": "llama3.2",
  "stream": true
}
```

流式返回下载进度:
```json
{"status":"pulling manifest"}
{"status":"downloading digestname","digest":"sha256:...","total":2142590208,"completed":241172480}
{"status":"verifying sha256 digest"}
{"status":"writing manifest"}
{"status":"success"}
```

### 删除模型

**端点**: `DELETE /api/delete`

```json
{
  "name": "llama3.2"
}
```

### 显示模型信息

**端点**: `POST /api/show`

```json
{
  "name": "llama3.2"
}
```

响应包含模型详细信息、Modelfile、参数等。

## Generate API (补全模式)

**端点**: `POST /api/generate`

```json
{
  "model": "llama3.2",
  "prompt": "The capital of France is",
  "stream": false
}
```

响应:
```json
{
  "model": "llama3.2",
  "created_at": "2024-07-18T12:34:56.789Z",
  "response": " Paris.",
  "done": true,
  "context": [1, 2, 3, ...],
  "total_duration": 5000000000,
  "load_duration": 1000000000,
  "prompt_eval_count": 5,
  "prompt_eval_duration": 1500000000,
  "eval_count": 2,
  "eval_duration": 1000000000
}
```

**context 字段**: 用于继续对话的上下文 tokens。

## Embeddings API

**端点**: `POST /api/embeddings` (原生)
**端点**: `POST /v1/embeddings` (OpenAI 兼容)

### 原生格式

```json
{
  "model": "nomic-embed-text",
  "prompt": "The quick brown fox jumps over the lazy dog"
}
```

响应:
```json
{
  "embedding": [0.123, -0.456, 0.789, ...]
}
```

### OpenAI 兼容格式

```json
{
  "model": "nomic-embed-text",
  "input": "The quick brown fox jumps over the lazy dog"
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
  "model": "nomic-embed-text",
  "usage": {
    "prompt_tokens": 10,
    "total_tokens": 10
  }
}
```

## Modelfile

自定义模型配置文件:

```
FROM llama3.2

PARAMETER temperature 0.7
PARAMETER top_p 0.9
PARAMETER stop "<|end|>"

SYSTEM """
You are a helpful assistant.
"""

TEMPLATE """
{{ if .System }}System: {{ .System }}{{ end }}
User: {{ .Prompt }}
Assistant:
"""
```

**创建自定义模型**:
```bash
ollama create mymodel -f Modelfile
```

## 常见模型

| 模型 | 参数量 | 特性 |
|------|--------|------|
| llama3.2 | 3B | 轻量级通用 |
| llama3.1 | 8B/70B/405B | 工具调用、长上下文 |
| mistral | 7B | 高性能 |
| mixtral | 8x7B | MoE 架构 |
| qwen2.5 | 7B/14B/32B/72B | 中文优化 |
| deepseek-coder-v2 | 16B/236B | 代码生成 |
| llava | 7B/13B/34B | 视觉理解 |
| nomic-embed-text | - | 文本嵌入 |

## 错误响应

```json
{
  "error": "model 'llama3.2' not found, try pulling it first"
}
```

**常见错误**:
- 模型未找到: 需要先 `ollama pull <model>`
- 连接失败: Ollama 服务未启动
- 内存不足: 模型太大,需要更多 RAM/VRAM

## 部署配置

### 环境变量

- `OLLAMA_HOST`: 监听地址,默认 `127.0.0.1:11434`
- `OLLAMA_MODELS`: 模型存储路径
- `OLLAMA_NUM_PARALLEL`: 并行请求数
- `OLLAMA_MAX_LOADED_MODELS`: 同时加载的最大模型数
- `OLLAMA_KEEP_ALIVE`: 模型保持加载时间

### 启动服务

```bash
# 默认启动
ollama serve

# 自定义监听地址
OLLAMA_HOST=0.0.0.0:11434 ollama serve
```

### 使用 OpenAI SDK

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:11434/v1",
    api_key="ollama"  # 必填但不验证
)

response = client.chat.completions.create(
    model="llama3.2",
    messages=[
        {"role": "user", "content": "Hello"}
    ]
)
```

## 私有扩展总结

Ollama 的特色功能:

1. **本地部署**:
   - 无需网络连接
   - 数据隐私保护
   - 免费使用

2. **双接口模式**:
   - 原生接口: `/api/*`
   - OpenAI 兼容: `/v1/*`

3. **丰富的模型参数**:
   - `options` 对象提供细粒度控制
   - `num_ctx`, `repeat_penalty`, `mirostat` 等

4. **性能统计**:
   - 详细的耗时信息(加载、评估、生成)
   - Token 数量统计

5. **模型管理**:
   - pull/delete/show 等管理 API
   - Modelfile 自定义模型

6. **多模态支持**:
   - llava 等视觉模型
   - base64 图片输入

7. **灵活配置**:
   - `keep_alive` 控制模型驻留
   - `template` 自定义提示词格式
