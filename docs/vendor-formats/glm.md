# 智谱 GLM 方言规范

## 概述

- Base protocol: `openai-chat`（兼容模式）
- 方言: `DialectGLM`
- 官方文档: https://bigmodel.cn/dev/api/normal-model/glm-4
- 端点: `POST /api/paas/v4/chat/completions`
- 别名 catalog code: `zhipu`、`glm`、`bigmodel`、`zai`

## HTTP 形态

```
POST /api/paas/v4/chat/completions HTTP/1.1
Host: open.bigmodel.cn
Authorization: Bearer {ZHIPU_API_KEY}
Content-Type: application/json
```

## 与 OpenAI 的差异

### 1. 私有参数 `do_sample`

```json
{"do_sample": true}
```

GLM 4.6+ 模型使用，启用采样。`temperature: 0` 与 `do_sample: false` 组合确保确定性输出。

### 2. 工具调用格式

与 OpenAI 完全一致。

### 3. 推理内容（GLM-Z1 系列）

```json
{
  "choices": [{
    "message": {
      "role": "assistant",
      "content": "最终答案",
      "reasoning_content": "思考过程..."
    }
  }]
}
```

流式通过 `delta.reasoning_content`。

### 4. 特殊 finish_reason 值（P1-GLM-2 已修复）

GLM 在非流式响应中可能返回异常 `finish_reason`：

```json
{"finish_reason": "network_error"}
{"finish_reason": "sensitive"}
{"finish_reason": "model_context_window_exceeded"}
```

`internal/ir/parse_openai.go` 已将这些值映射到 IR 的 `errorsx.UpstreamError`：
- `network_error` → `KindNetwork`
- `sensitive` → `KindContentFilter`
- `model_context_window_exceeded` → `KindContextLength`

流式路径在 `ae1ecaedf` 提交中已修复。

### 5. Web 搜索

GLM 支持 `web_search` 工具，由平台提供，需要在请求中启用：

```json
{
  "tools": [
    {
      "type": "web_search",
      "web_search": {
        "enable": true,
        "search_query": "今天的新闻"
      }
    }
  ]
}
```

### 6. 用户标识

与 OpenAI `user` 字段一致。

### 7. 错误码

```json
{
  "error": {
    "code": "...",
    "message": "..."
  }
}
```

典型 code：`1001`（认证）、`1002`（余额）、`1214`（上下文过长）、`1300`（系统）。

## IR 映射

| GLM 字段 | IR 字段 |
|----------|---------|
| `do_sample` | `Request.DoSample` |
| `reasoning_content` | `Response.ReasoningContent` |
| `web_search` (工具) | `Request.WebSearch` |
| `finish_reason: "network_error"` | `UpstreamError{KindNetwork}` |
| `finish_reason: "sensitive"` | `UpstreamError{KindContentFilter}` |
| `finish_reason: "model_context_window_exceeded"` | `UpstreamError{KindContextLength}` |

## 序列化注意

1. `do_sample` 仅在 GLM 方言时输出。
2. 特殊 finish_reason 必须被识别为错误，不能当成正常结束。
3. `web_search` 工具类型与 OpenAI function 工具并存，需要 IR 兼容。
