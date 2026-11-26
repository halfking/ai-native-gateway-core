# /v1/audio/converse 级联语音会话端点

> 2026-11-26 语音流式轮交付。网关自定义端点（OpenAI 无此契约），把既有
> ASR → LLM → TTS 三段能力串成一次调用，多轮语音对话不再要求调用方自行
> 编排三次 HTTP 往返。

## 背景

PersonaPlex-7B 类单模型全双工 speech-to-speech 架构经本机实测（M1 Max，
2026-11-26）无法实时（Metal 0.19fps / CPU 1.45fps，实时线 12.5fps），且
英语专用、无标准 API。双语（中英）语音流式服务的可行路线是级联：本端点
是级联路线的网关侧入口。

## 请求

`POST /v1/audio/converse`，JSON 体：

```json
{
  "asr_model":     "whisper-large-v3-turbo",          // 可选，空 = audio 模态默认候选
  "chat_model":    "qwen3:8b",                        // 必填，环回 chat 面路由
  "tts_model":     "tts-macos-say",                   // 可选，空 = 跳过 TTS 只回文本
  "voice":         "alloy",
  "system_prompt": "你是助手",                        // 可选
  "language":      "zh",                              // 可选，透传 ASR
  "history":       [{"role":"user","content":"…"},…], // 可选，≤128 条
  "audio":         {"data":"<base64>","format":"wav"},// 语音输入（与 Text 二选一，Audio 优先）
  "text":          "…",                               // 文本输入
  "stream":        false
}
```

## 响应

非流式（`stream=false`）：单 JSON `{request_id, transcript?, reply, audio?}`。

流式（`stream=true`）：SSE，事件序固定——

```
event: transcript.transport     （有 ASR 段时）
event: transcript.text.done
event: reply.text.delta
event: reply.text.done
event: response.audio           （有 TTS 段时）
data: [DONE]
```

事件名与 `/v1/audio/transcriptions` 流式契约同族（`transcript.*` 复用），
调用方按事件名分发、未知事件按 SSE 规范忽略。一期环回 chat 为非流式调用
（`loopbackChatCompletions` v1 契约），`reply.text.delta` 单段携带全文；
二期换流式环回时只多发几个 delta，事件契约不变。`response.audio` 一次
携带整段 base64 音频（TTS 非流式；句子级切分流式为一期有意取舍）。

## 复用边界（拒绝重写的清单）

| 段 | 复用 | 位置 |
|---|---|---|
| ASR | `AudioService.Transcribe`（候选解析/形态回落/失败台账/时长计费/并发闸） | `domains/streaming/audio_service.go:288` |
| LLM | 环回 `/v1/chat/completions`（`loopbackChatCompletions`，转发调用方 Authorization，成本归属调用方 key） | `domains/streaming/summarize.go:1211` |
| TTS | `AudioService.Synthesize`（chat-audio 桥接/候选回落/计费） | `domains/streaming/audio_service.go:817` |

编排层 `domains/streaming/audio_converse.go` 只做参数校验、三段串行组装
与 SSE 编排——与 refine/analyze 的「拒绝在音频面重新实现一遍 chat 执行器」
原则一致。

## 鉴权与限流

- 数据面鉴权与 `/v1/audio/*` 同口径：Bearer `sk-*` → `authenticate`
  （Verify → key RPM → key 预算 → 租户月度预算闸）。
- 装配**不包** `KeyConcurrentGate`：ASR/TTS 步走 AudioService 自身 8 路
  并发闸，LLM 步经环回 chat 携带同一把 key 已计一次并发；外层再包=双计
  （refine/analyze 同款论证）。key RPM 在本端点计一次 + 环回 chat 再计
  一次 = 一次逻辑调用两次 RPM，与 MCP 面同款取舍（调用频率低，可接受）。

## 错误信封

`writeAudioError` 同一分流：`no_provider` 503 / `audio_capacity` 503 /
上游 4xx 分流（400/413/422 → 400，429 → 429，401/403 → 502）/ 环回 chat
503 no_candidate 带 alternatives。本地前置校验（缺 chat_model、无输入、
base64 非法、history 越界、转写空文本）→ 400 `invalid_request_error`。

## 模型登记要求

- ASR/TTS 模型：登记 `modality='audio'`（whisper-cpp / macos-say 已由
  迁移 `911_local_audio_catalog.sql` 预置）。
- chat 模型：任意可路由的 text 模态模型（走环回 chat 面）。

## 二期预留：/v1/realtime WebSocket（本次未实施）

全双工 speech-to-speech 通道（PersonaPlex/Qwen-Omni 类）需：gorilla/websocket
新依赖 + Upgrade（仅 HTTP/1.1，h2c 回落兼容需实测 252 nginx 链路）+ OpenAI
Realtime 事件方言 + `key_concurrent_gate.go` 长连接显式 Release 语义改造。
触发条件：有实时可用的 speech-to-speech 上游（GPU 机器或云服务）。
