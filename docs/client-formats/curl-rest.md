# curl / REST 直连请求格式要求

## 概述

除 SDK 客户端外，开发者常使用 curl 或 Postman 直连网关 API。本文档列出常见的手工请求格式。

## OpenAI Chat Completions（curl）

### 非流式

```bash
curl -X POST https://gateway.example.com/v1/chat/completions \
  -H "Authorization: Bearer any-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Hello"}
    ],
    "temperature": 0.7,
    "max_tokens": 1024
  }'
```

### 流式

```bash
curl -X POST https://gateway.example.com/v1/chat/completions \
  -H "Authorization: Bearer any-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }' --no-buffer
```

流式响应：

```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hel"}}]}

data: [DONE]
```

## Anthropic Messages（curl）

```bash
curl -X POST https://gateway.example.com/v1/messages \
  -H "x-api-key: any-key" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet-20241022",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "Hello"}],
    "system": "You are a helpful assistant."
  }'
```

### 流式

```bash
curl -X POST https://gateway.example.com/v1/messages \
  -H "x-api-key: any-key" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet-20241022",
    "max_tokens": 1024,
    "stream": true,
    "messages": [{"role": "user", "content": "Hello"}]
  }' --no-buffer
```

流式事件：

```
event: message_start
data: {"type":"message_start","message":{"id":"msg_xxx","role":"assistant","content":[],"model":"claude-3-5-sonnet-20241022","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: message_stop
data: {"type":"message_stop"}
```

## OpenAI Responses（curl）

```bash
curl -X POST https://gateway.example.com/v1/responses \
  -H "Authorization: Bearer any-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5",
    "input": [
      {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Hello"}]}
    ]
  }'
```

## Gemini generateContent（curl）

```bash
curl -X POST "https://gateway.example.com/v1beta/models/gemini-1.5-pro:generateContent?key=any-key" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [
      {"role": "user", "parts": [{"text": "Hello"}]}
    ]
  }'
```

## 常见直连场景

### 1. 健康检查

```bash
curl -i https://gateway.example.com/healthz
```

### 2. 模型列表

```bash
curl -H "Authorization: Bearer any-key" https://gateway.example.com/v1/models
```

### 3. 余额查询

```bash
curl -H "Authorization: Bearer any-key" https://gateway.example.com/v1/dashboard/billing/credit_grants
```

### 4. 多模态上传

```bash
curl -X POST https://gateway.example.com/v1/chat/completions \
  -H "Authorization: Bearer any-key" \
  -H "Content-Type: application/json" \
  -d @request.json
```

`request.json`：
```json
{
  "model": "gpt-4o",
  "messages": [{
    "role": "user",
    "content": [
      {"type": "text", "text": "What's in this image?"},
      {"type": "image_url", "image_url": {"url": "https://example.com/image.png"}}
    ]
  }]
}
```

## 网关对 curl 直连的支持

### 1. CORS

如果浏览器前端通过 curl / fetch 直连网关，需要网关开启 CORS：

```
Access-Control-Allow-Origin: *
Access-Control-Allow-Methods: GET, POST, OPTIONS
Access-Control-Allow-Headers: Authorization, Content-Type, x-api-key, anthropic-version
```

### 2. TLS

curl 客户端期望 HTTPS（除非内网部署）。

### 3. 错误响应

curl 客户端通常直接读 HTTP 状态码，所以网关必须正确设置：

- 4xx：客户端错误（认证、参数、超额）
- 5xx：上游错误
- 200 + `error` 字段：业务错误（如 OpenAI 的 rate_limit）

### 4. 大 body

curl 默认无 body 大小限制，但如果代理（nginx）有限制，网关需要配置 `client_max_body_size`。
