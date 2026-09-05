# OpenAI Realtime API - Responses

## 获取说明

**注意**: OpenAI 文档受 Cloudflare 保护，无法通过自动化工具抓取。
本文档基于 OpenAI 公开的 Realtime API 规范整理，需要手动访问以下链接获取最新版本：

- **官方文档**: https://platform.openai.com/docs/api-reference/realtime
- **建议**: 使用浏览器手动访问并导出完整规范

---

## Realtime API Overview

OpenAI Realtime API 是基于 WebSocket 的双向通信协议，用于实时语音和文本交互。

### Connection
```
wss://api.openai.com/v1/realtime?model=gpt-4o-realtime-preview-2024-10-01
```

---

## Client Events

### session.update
更新会话配置

```json
{
  "type": "session.update",
  "session": {
    "modalities": ["text", "audio"],
    "instructions": "string",
    "voice": "alloy" | "echo" | "shimmer",
    "input_audio_format": "pcm16" | "g711_ulaw" | "g711_alaw",
    "output_audio_format": "pcm16" | "g711_ulaw" | "g711_alaw",
    "input_audio_transcription": {
      "model": "whisper-1"
    },
    "turn_detection": {
      "type": "server_vad",
      "threshold": 0.5,
      "prefix_padding_ms": 300,
      "silence_duration_ms": 500
    },
    "tools": [...],
    "tool_choice": "auto" | "none" | "required",
    "temperature": 0.8,
    "max_response_output_tokens": "inf" | integer
  }
}
```

### input_audio_buffer.append
添加音频数据

```json
{
  "type": "input_audio_buffer.append",
  "audio": "base64-encoded-audio"
}
```

### input_audio_buffer.commit
提交音频缓冲区

```json
{
  "type": "input_audio_buffer.commit"
}
```

### conversation.item.create
创建对话项

```json
{
  "type": "conversation.item.create",
  "item": {
    "type": "message",
    "role": "user" | "assistant" | "system",
    "content": [
      {
        "type": "input_text",
        "text": "string"
      },
      {
        "type": "input_audio",
        "audio": "base64",
        "transcript": "string (optional)"
      }
    ]
  }
}
```

### response.create
触发响应生成

```json
{
  "type": "response.create",
  "response": {
    "modalities": ["text", "audio"],
    "instructions": "string (optional)",
    "voice": "alloy" | "echo" | "shimmer",
    "output_audio_format": "pcm16" | "g711_ulaw" | "g711_alaw",
    "tools": [...],
    "tool_choice": "auto" | "none" | "required",
    "temperature": 0.8,
    "max_output_tokens": "inf" | integer
  }
}
```

### response.cancel
取消正在进行的响应

```json
{
  "type": "response.cancel"
}
```

---

## Server Events

### session.created
会话创建确认

```json
{
  "type": "session.created",
  "session": {
    "id": "sess_...",
    "object": "realtime.session",
    "model": "gpt-4o-realtime-preview-2024-10-01",
    "modalities": ["text", "audio"],
    "instructions": "string",
    "voice": "alloy",
    "input_audio_format": "pcm16",
    "output_audio_format": "pcm16",
    "turn_detection": {...},
    "tools": [...],
    "tool_choice": "auto",
    "temperature": 0.8
  }
}
```

### conversation.item.created
对话项创建通知

```json
{
  "type": "conversation.item.created",
  "item": {
    "id": "msg_...",
    "object": "realtime.item",
    "type": "message",
    "status": "in_progress" | "completed",
    "role": "user" | "assistant" | "system",
    "content": [...]
  }
}
```

### response.created
响应开始

```json
{
  "type": "response.created",
  "response": {
    "id": "resp_...",
    "object": "realtime.response",
    "status": "in_progress",
    "output": []
  }
}
```

### response.output_item.added
输出项添加

```json
{
  "type": "response.output_item.added",
  "response_id": "resp_...",
  "output_index": 0,
  "item": {
    "id": "msg_...",
    "type": "message",
    "role": "assistant",
    "content": []
  }
}
```

### response.content_part.added
内容部分添加

```json
{
  "type": "response.content_part.added",
  "response_id": "resp_...",
  "item_id": "msg_...",
  "output_index": 0,
  "content_index": 0,
  "part": {
    "type": "text" | "audio",
    "text": "" | null,
    "audio": "" | null,
    "transcript": "" | null
  }
}
```

### response.audio.delta
音频数据增量

```json
{
  "type": "response.audio.delta",
  "response_id": "resp_...",
  "item_id": "msg_...",
  "output_index": 0,
  "content_index": 0,
  "delta": "base64-encoded-audio-chunk"
}
```

### response.text.delta
文本数据增量

```json
{
  "type": "response.text.delta",
  "response_id": "resp_...",
  "item_id": "msg_...",
  "output_index": 0,
  "content_index": 0,
  "delta": "text-chunk"
}
```

### response.audio_transcript.delta
音频转文本增量

```json
{
  "type": "response.audio_transcript.delta",
  "response_id": "resp_...",
  "item_id": "msg_...",
  "output_index": 0,
  "content_index": 0,
  "delta": "transcript-chunk"
}
```

### response.function_call_arguments.delta
函数调用参数增量

```json
{
  "type": "response.function_call_arguments.delta",
  "response_id": "resp_...",
  "item_id": "msg_...",
  "output_index": 0,
  "call_id": "call_...",
  "delta": "json-chunk"
}
```

### response.done
响应完成

```json
{
  "type": "response.done",
  "response": {
    "id": "resp_...",
    "object": "realtime.response",
    "status": "completed" | "cancelled" | "failed",
    "output": [
      {
        "id": "msg_...",
        "type": "message",
        "role": "assistant",
        "content": [
          {
            "type": "text",
            "text": "complete text"
          },
          {
            "type": "audio",
            "audio": "complete-base64-audio",
            "transcript": "complete transcript"
          }
        ]
      }
    ],
    "usage": {
      "total_tokens": 100,
      "input_tokens": 50,
      "output_tokens": 50
    }
  }
}
```

### error
错误事件

```json
{
  "type": "error",
  "error": {
    "type": "invalid_request_error" | "server_error" | "rate_limit_error",
    "code": "string",
    "message": "string",
    "param": "string (optional)"
  }
}
```

---

## Content Types

### Input Content
```json
{
  "type": "input_text",
  "text": "string"
}
```

```json
{
  "type": "input_audio",
  "audio": "base64",
  "transcript": "string (optional)"
}
```

### Output Content
```json
{
  "type": "text",
  "text": "string"
}
```

```json
{
  "type": "audio",
  "audio": "base64",
  "transcript": "string"
}
```

### Function Call Content
```json
{
  "type": "function_call",
  "call_id": "call_...",
  "name": "function_name",
  "arguments": "json-string"
}
```

```json
{
  "type": "function_call_output",
  "call_id": "call_...",
  "output": "string"
}
```

---

## Tool Integration

### Tool Definition
```json
{
  "type": "function",
  "name": "get_weather",
  "description": "Get current weather",
  "parameters": {
    "type": "object",
    "properties": {
      "location": {
        "type": "string",
        "description": "City name"
      }
    },
    "required": ["location"]
  }
}
```

### Tool Call Flow
1. Server sends `response.function_call_arguments.delta` events
2. Server sends `response.function_call_arguments.done`
3. Client executes function
4. Client sends `conversation.item.create` with `function_call_output`
5. Client sends `response.create` to continue

---

## Audio Formats

- **pcm16**: 16-bit PCM, 24kHz, mono, little-endian
- **g711_ulaw**: G.711 μ-law, 8kHz, mono
- **g711_alaw**: G.711 A-law, 8kHz, mono

---

## Key Features

1. **Bidirectional Streaming**: 实时音频/文本双向传输
2. **Voice Activity Detection (VAD)**: 服务端自动检测语音结束
3. **Audio Transcription**: 自动将音频转为文本
4. **Function Calling**: 支持工具调用流程
5. **Modality Switching**: 可在文本和音频之间动态切换

---

## TODO

- [ ] 手动访问 OpenAI 官方文档获取最新完整规范
- [ ] 补充完整的事件流时序图
- [ ] 验证音频格式和采样率配置
- [ ] 补充 VAD 参数调优建议
