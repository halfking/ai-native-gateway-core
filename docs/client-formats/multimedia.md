# 多媒体 API 接入标准（音频 / 图片 / 视频）

> 对应设计：`docs/design/2026-10-08-multimedia-unified-gateway-credential-scope.md`  
> 适用：OpenAI 兼容客户端、OpenPocket、curl / REST、自研 SDK

## 1. 原则

网关把语音 / 图片 / 视频厂商差异收敛在运输适配层；**客户端一律按 OpenAI 风格统一入口调用**，不要直连厂商私有路径（如 MiniMax `/v1/speech_to_text`），除非刻意绕过网关做调试。

| 模态 | 统一入口 | 凭据路由域 |
|------|----------|------------|
| 文本 / 工具 | `POST /v1/chat/completions` 等 | `routing_scope ∈ {all, chat_only}` |
| 音频 ASR/TTS | `POST /v1/audio/transcriptions`、`/v1/audio/speech` | `routing_scope ∈ {all, media_only}` |
| 图片生成 | `POST /v1/images/generations` | 同上（modality=image） |
| 视频生成 | `POST /v1/videos/generations` | 同上（modality=video） |

运营侧：媒体资源包专用 key 在 Admin「凭据」里设 `routing_scope=media_only`，标准 chat **不会**选中该凭据。

## 2. 音频转写（ASR）

### 2.1 推荐调用

```bash
curl -X POST https://llm.kxpms.cn/v1/audio/transcriptions \
  -H "Authorization: Bearer <gateway-api-key>" \
  -F "file=@meeting.wav" \
  -F "model=mimo-v2.5-asr" \
  -F "language=zh" \
  -F "response_format=json"
```

也可用网关目录中的 ASR 模型，例如：

- `mimo-v2.5-asr`（小米，chat-audio 桥接由网关内部完成）
- `glm-asr` / `glm-asr-2512`（智谱，multipart 透传）
- `minimax-asr-1.0`（MiniMax；客户端仍打 `/v1/audio/transcriptions`，网关再适配上游 `/v1/speech_to_text`）

### 2.2 客户端必须遵守

1. **Base URL** 指向网关（如 `https://llm.kxpms.cn/v1`），路径固定 `/audio/transcriptions`。
2. **不要**因模型名含 `asr-1.0` 就改打 MiniMax 原生路径——那是上游方言，由网关适配。
3. `language` 用表单字段（OpenAI 语义）；网关会按上游改写。
4. 计费：部分上游按秒；网关落库 `audio_seconds`，MaaS 可用 `billing_unit=audio_second`。
5. 探测若返回 `error.code=no_provider`（HTTP 503），表示该模型无可用媒体凭据，换模型或检查 `media_only` 凭据绑定。

### 2.3 TTS

```bash
curl -X POST https://llm.kxpms.cn/v1/audio/speech \
  -H "Authorization: Bearer <gateway-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"<tts-model>","input":"你好","voice":"alloy"}' \
  --output out.mp3
```

## 3. 图片生成

```bash
curl -X POST https://llm.kxpms.cn/v1/images/generations \
  -H "Authorization: Bearer <gateway-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"<image-model>","prompt":"a red balloon","n":1,"size":"1024x1024"}'
```

当前为透传骨架：字段按上游契约；无可用候选时返回明确错误（非静默 chat 回退）。

## 4. 视频生成

```bash
curl -X POST https://llm.kxpms.cn/v1/videos/generations \
  -H "Authorization: Bearer <gateway-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"<video-model>","prompt":"a short clip of waves"}'
```

异步 poll（`/v1/videos/{id}`）尚未产品化；无上游时可能 `501`。

## 5. OpenPocket 引用约定

OpenPocket STT「仅网关 / auto」通道：

| 项 | 标准 |
|----|------|
| Channel | `gateway` 或 `auto`（优先网关） |
| Transport 默认 | **`transcriptions`**（`POST {gateway}/audio/transcriptions`） |
| Provider 模板 | 网关目标一律按 **OpenAI 兼容层**；勿因模型名 `asr-1.0` 切到 MiniMax 原生路径 |
| MiniMax 直连 | 仅 `channel=minimax` 且 base 为 `api.minimax.cn` / `minimaxi.com` 时使用原生 `/v1/speech_to_text` |
| chat-audio | 仅当探测明确判定网关只有 chat 音频桥接、或用户显式选 `transport=chat-audio` 时使用 |

错误分类：网关 `503` + `code=no_provider` 展示为「网关无可用上游」，不要当成通用探测失败。

## 6. 与文本接入的关系

文本协议见本目录 `openai-sdk.md` / `anthropic-sdk.md` / `curl-rest.md`。多媒体与文本共用同一 API Key，但凭据侧 `routing_scope` 决定该 key 能否进入媒体候选池——客户端无需感知字段名，只需打对路径与模型。
