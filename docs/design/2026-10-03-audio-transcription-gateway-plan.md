# 录音转写网关方案（实时 + 高精全量）——2026-10-03 音频端点轮

> 状态：已实现并本地端到端验证（local 8782，build 2.5.8.2432）。
> 范围：llm-gateway-go 音频数据面（/v1/audio/transcriptions、/v1/audio/speech、
> /v1/mcp）+ 调度/探针/modality 三处根修 + openpocket 集成讨论。

## 1. 需求（完善后）

原始诉求：检查所有模型（特别是小米的实时转写与发音模型）并测试验证；
提供录音实时转写 + 整段高精转写能力；API 以网关特殊协议或 MCP 供 app
使用；讨论 openpocket 是否可以直接内置（本地快速转写 + 网络高精全转）。

完善为五条可验收的子需求：

| # | 需求 | 验收口径 |
|---|---|---|
| R1 | 小米 ASR/TTS 模型可用性实测 | 直连上游真实调用矩阵（§2） |
| R2 | 网关提供 OpenAI 兼容转写/合成端点 | multipart 契约 + SSE 流式（§4/§7） |
| R3 | 调度层让音频模型真正可路由 | modality/探针/URSM 三处根修（§3） |
| R4 | app 可用「特殊协议或 MCP」接入 | /v1/audio/* + /v1/mcp（§5） |
| R5 | openpocket 内置形态论证 | 本地粗翻 + 网关高精全转（§6） |

## 2. 小米上游实测矩阵（token-plan-cn.xiaomimimo.com，2026-10-03）

凭据：本地库 xiaomi-token-plan（cred 9）/ xiaomi-mimo（cred 24），
provider id=1，协议 openai-completions。

| 项目 | 结果 |
|---|---|
| /v1/models 音频模型 | mimo-v2.5-asr、mimo-v2.5-tts、-voiceclone、-voicedesign |
| OpenAI 式 /v1/audio/transcriptions | ❌ 404（openresty）——**小米没有该端点** |
| chat/completions + input_audio（ASR） | ✅ 转写与原文一字不差（8.2s/35s 均验证） |
| chat 流式（stream=true） | ✅ SSE 逐词 delta.content |
| ASR 音频格式 | 仅 wav/mp3；webm/m4a 400（错误体明示） |
| ASR 带 text part | ❌ 400 "must not include text parts"（language/prompt 无法透传） |
| ASR 纯文本请求 | ❌ 400 "requires a user message with input_audio content" |
| 计费 | usage.seconds 按音频秒数（8.2s→9s 向上取整；35s→35s） |
| TTS 形态 | chat + modalities:["text","audio"] + **assistant 消息**承载文本；audio.voice 可省略（默认音色） |
| TTS 音色 | mimo_default/冰糖/茉莉/苏打/白桦/Mia/Chloe/Milo/Dean；非法名 400（错误体列出全集） |
| TTS 输出 | message.audio.data = base64 WAV 24kHz16bit；另有 id/transcript 字段 |

关键结论：**小米的音频面挂在 chat 协议上**，必须桥接而非透传。

## 3. 网关侧三层根修（音频模型 503 no_candidate 的完整因果链）

修复前，目录里有模型但全 503，因果链有三层，逐层卡死：

1. **modality 错标**（modelname/modality_defaults.go）
   旧规则只有前缀 asr-/tts- 与包含 -asr-/-tts-，后缀形态的
   mimo-v2.5-asr/-tts 落回 text 默认值；audio 请求候选过滤
   （modality IN ('audio','multimodal')）将其排除。
   修：新增后缀 -asr/-tts、包含 -asr-/-transcri 规则，前缀
   gpt-4o-transcribe/gpt-4o-mini-transcribe/gpt-transcribe
   （防止被 gpt-4o- vision 前缀抢占）。
2. **探针形态**（bg/node_probe.go）
   direct/gateway 两轮探针都发纯文本 "ping"，小米 ASR 400
   （"requires input_audio"）、TTS 400（"must contain an assistant
   role"）→ 探针判失败 → URSM available=0 / node_probe_state
   打黑 → 路由永久排除（上游本身完全健康）。
   修：directProbeBody 按 InferModality 分发——ASR/audio-chat 发
   400ms 静音 WAV 的 input_audio（实测小米 200）；TTS 发 assistant
   短文本 + modalities（免 voice，实测 200）；text 模型不变。
   probeGateway 复用 directProbeBody（网关面即 openai 形态）。
3. **存量数据**（sql/migrations/startup/820 + db.ensureAudioModalityBackfill）
   已错标的行回填 text→audio（幂等，只升不降；down 为有意空操作）。
   双侧同步：SQL 迁移 + db/db.go ensure 链镜像（二进制部署只跑
   ensure 链）。

## 4. 网关音频数据面（新增）

### 4.1 传输形态适配（先例调研结论的落地）

网络调研（LiteLLM/new-api/WeKnora 等）确认行业统一模式是「OpenAI
契约做出口 + 上游 adapter 桥接，SSE 事件名各上游不一致需网关归一」。
本实现的两形态：

- **multipart 透传**（transcriptions）：POST {base}/audio/transcriptions
  原样转发（openai/groq/openrouter/智谱 /v4 全兼容）；404/405/415 时
  同候选回落桥接。
- **chat-audio 桥接**（chat-audio）：chat/completions + input_audio
  base64（小米形态）；xiaomi catalog_code 直接走桥接避免 404 往返。
  TTS 桥接：modalities:["text","audio"] + assistant 消息 + 音色归一
  （xiaomi 之外的 voice 归一 mimo_default，客户端传 alloy 也能出声）。

### 4.2 端点契约

`POST /v1/audio/transcriptions`（multipart：file/model/language/prompt/
response_format/temperature/stream）
- 非流式：`{"text":..., "model":..., "duration":...}`；response_format
  text/srt/vtt 回纯文本；透传形态回上游原文（保留 segments）。
- 流式（stream=true）：SSE `transcript.text.delta`/`transcript.text.done`
  + `data: [DONE]`（与 OpenAI gpt-4o-transcribe、智谱 glm-asr 事件名
  一致，openpocket TransportSSE 无需新适配）。
- 响应头：X-Gw-Audio-Transport（transcriptions|chat-audio）、
  X-Gw-Upstream-Model、X-Gw-Audio-Seconds（小米计费秒数）。
- language/prompt 在 chat-audio 形态下被忽略（小米拒收 text part，
  §2 实测）——已在端点文档注明。

`POST /v1/audio/speech`（JSON：model/input/voice/response_format/speed）
- 响应 audio/* 二进制（小米 24kHz WAV）；voice 归一如上。

`POST /v1/mcp`（MCP streamable HTTP，JSON-RPC 2.0）
- initialize / notifications/*（202）/ tools/list / tools/call
  （transcribe_audio、synthesize_speech）/ ping；未知方法 -32601；
  GET 405（规范允许无推送通道）；单 POST 纯 JSON 应答合法
  （2025-06-18 spec）。
- 与 HTTP 端点同一 AudioService 执行路径、同一鉴权（Bearer sk-*，
  KeyVerifier + 限流 + 预算）。

### 4.3 与 chat 面的关系

- 候选解析复用 provider.GetCandidatesByModality(model,"audio")，逐候选
  failover（embeddings 同款循环），**不经过 URSM 权威视图**——音频端点
  自带候选重试，与探针修复互不阻塞。
- chat+input_audio 路径（gpt-4o-audio 类模型 + 小米 ASR）同样受益于
  §3 根修：实测 mimo-v2.5-asr 经 chat 面 200（URSM 翻绿）。
- 已知边界：向 ASR-only 模型发**纯文本** chat（如某些探测脚本的
  ping）会 400 并短暂打黑节点健康度——这是既有健康度行为；新探针
  形态会在下个探测周期自愈（旧代码是永久红）。

## 5. app 接入方式

1. **OpenAI 兼容（推荐，openpocket 已天然支持）**：openpocket 的
   discovery.go 按 multipart+language 探测 /audio/transcriptions，
   200 即 ProbeOK。本轮实测其官方脚本 verify-gateway-audio.mjs 判定
   「/audio/transcriptions 可用」。
2. **MCP**：app 内置 MCP 客户端（openpocket 已有 mcp client 基建）
   指向 {gateway}/v1/mcp，tools/list 可发现两个音频工具。

## 6. openpocket 内置形态论证（结论：可以，且已是最优结构）

openpocket 现状（本轮勘察）：本地 sherpa-onnx Paraformer（移动端一等
公民，业界共识选型；全量高精可同栈加 SenseVoice int8 ≈155MB，见
调研）+ 云端兜底 + 静音切分全量聚合（full.go）+ VAD 分段增量。

建议的目标结构（两层转写金字塔，与「本地粗翻最好」的要求一致）：

```
录音 ──► 本地 sherpa-onnx（粗翻/实时字幕，离线、零成本、隐私）
   └──► 录音完成 ──► 网关 /v1/audio/transcriptions（高精全转）
                       ├─ 小米 mimo-v2.5-asr（token-plan 计费，按秒）
                       ├─ 未来：智谱 glm-asr / OpenRouter whisper 等
                       │   （multipart 透传形态已就绪，接入零代码）
                       └─ stream=true 可逐词出字
```

落地要点：
- openpocket **无需改代码**即可切换云端目标到网关（探测逻辑自动识别）；
  需要注意两点：a) 录音格式——MediaRecorder 的 webm 小米不收，上传前
  转码 wav/mp3（sherpa 采集链路已是 PCM，编码成 wav 即可）；
  b) 长音频按 openpocket full.go 的静音切分逐段上送（网关单请求上限
  32MiB body；上游各自的时长/大小限制由上游错误透出）。
- 「直接内置」的另一种解读（把转写内核内置进 openpocket 后端进程）：
  不建议。sherpa-onnx 已在端侧覆盖粗翻；服务端再嵌推理内核会引入
  模型分发/算力/升级成本，而网关多上游 failover + 计费审计的能力
  是端侧给不了的。正确分层：**端=快，云=准，网关=路由/审计/归一**。

## 7. 验证证据（本地 8782，build 2.5.8.2432）

| 验证项 | 结果 |
|---|---|
| 迁移 820 | 四个 mimo 音频模型 modality 全部 audio |
| /v1/audio/transcriptions 非流式（8.2s 中文 wav） | 200，转写逐字正确，X-Gw-Audio-Seconds=9 |
| 35s 长音频（直连上游） | 全文正确，按 35s 计费 |
| stream=true | SSE 逐词 delta + done + [DONE] |
| /v1/audio/speech（voice=茉莉） | 200 audio/wav 24kHz 2.7s |
| /v1/mcp initialize/tools/list/tools/call | 全通过（mp3 输入也正确转写） |
| chat+input_audio 经网关 | 200（探针修复后 URSM 翻绿） |
| openpocket verify-gateway-audio.mjs | 「/audio/transcriptions 可用」 |
| 单测 | streaming/bg/modelname/provider/db/upstream 全绿（streaming 90s 全量） |
| 探针形态直发小米 | ASR 形态 200（静音 WAV→"Yeah."）、TTS 形态 200 |

## 8. 已知边界与后续候选

- 小米 ASR 不支持 language/prompt 透传（拒收 text part）；流式语种
  由模型自判（实测中文正确）。
- 小米 ASR 仅 wav/mp3；客户端负责转码（openpocket 采集链路已是 PCM）。
- 透传形态的流式是「收完再转发」的伪流式（智谱 30s 限长下可接受）；
  真流式透传需要 handler 级 io.Pipe，列为后续候选。
- MiniMax asr-1.0 的非标路径 /v1/speech_to_text 与事件形状未适配
  （其 SSE 是 {index,delta,finish} 私有形状）；如需接入加一个
  transport adapter 即可。
- 长音频切分聚合保留在客户端（openpocket full.go 已有）；网关侧
  内置切分列为后续候选（需考虑音频解码依赖，倾向不做）。
- 音频端点暂不写 request_logs 审计（与 embeddings 同款现状）；
  计量口径可先用上游 usage.seconds（响应头已透出）。
