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

## 2. 小米上游实测矩阵（token-plan-cn.xiaomimimo.com，2026-10-03 首测 / 2026-10-04 复测）

凭据：本地库 xiaomi-token-plan（cred 9）/ xiaomi-mimo（cred 24），
provider id=1，协议 openai-completions。

> **凭据形态（2026-10-04 新增，首测就踩过）**：`tp-` / `ttp-` 开头的 key 是
> **Token Plan 专用** key，只能用于 `https://token-plan-cn.xiaomimimo.com/v1`
> （新加坡 `token-plan-sgp`、阿姆斯特丹 `token-plan-ams`）。把它打到按量付费
> 的 `https://api.xiaomimimo.com/v1` 上，两种鉴权头（`api-key` 与
> `Authorization: Bearer`）都回 `401 invalid_key`；官方明确写「两类 key 互相
> 独立、不可互换」。按量付费的 `sk-` key 才走 `api.xiaomimimo.com`。
> 两种鉴权头在 token-plan 主机上都实测可用，代码用 Bearer。

| 项目 | 结果 |
|---|---|
| /v1/models 音频模型 | mimo-v2.5-asr、mimo-v2.5-tts、-voiceclone、-voicedesign |
| OpenAI 式 /v1/audio/transcriptions | ❌ 404（openresty）——**小米没有该端点** |
| OpenAI 式 /v1/audio/speech | ❌ 404（同上） |
| chat/completions + input_audio（ASR） | ✅ 转写与原文一字不差（6.98s 中文 wav / 27.5s / 110.9s 5MiB 均验证） |
| chat 流式（stream=true） | ✅ SSE 逐词 delta.content（22 帧 + `[DONE]`） |
| ASR 音频格式 | 仅 wav/mp3；webm/m4a 400（错误体明示 "must be one of: wav, mp3"） |
| ASR data-URL 形态 | ✅ `data:audio/wav;base64,...` 可省略 `format` 字段 |
| ASR 带 text part | ❌ 400 "ASR request must not include text parts"（**prompt** 无法透传） |
| ASR 纯文本请求 | ❌ 400 "requires a user message with input_audio content" |
| **ASR 语种** | ✅ **`asr_options.language`**（复测新增）：只认 `zh`/`en`/`auto`，**严格**校验——`zh-CN`/`zh_CN`/`en-US`/`ja`/`ko`/`ZH` 一律 400 |
| 计费 | usage.seconds 按音频秒数（6.98s→8s 向上取整；110.9s→112s） |
| TTS 形态 | chat + `modalities:["text","audio"]` + **assistant 消息**承载文本；`modalities` 实测**可省略**（200） |
| TTS 音色 | mimo_default/冰糖/茉莉/苏打/白桦/Mia/Chloe/Milo/Dean；非法名 400（错误体列出全集） |
| TTS 音色大小写 | 仅小写（`mia`）合法；`Mia` 原样透传会被 400（故归一返回小写） |
| TTS 输出格式 | wav / mp3（`fff384c4` 帧头）/ pcm·pcm16 三者均实测 200 |
| TTS 语速 | ❌ **无语速旋钮**：同一文本带不带 `speed` 得到**逐字节相同**的音频 |
| TTS 流式 | ⚠️ 上游回**单行裸 JSON**（无 `data: ` 前缀），非规范 SSE——网关 TTS 端点不流式，暂不涉及 |
| **voicedesign 变体** | 复测新增：必须 `user` 消息（音色描述）+ `assistant`（文本），且**不能**带 `audio.voice`（带则 400）；两者齐备 → 200 |
| **voiceclone 变体** | 复测新增：`audio.voice` 必须是**样本 DataURL**（传音色名 400）；给样本 → 200 |

关键结论：**小米的音频面挂在 chat 协议上**，必须桥接而非透传。
三个 TTS 变体要的请求形状互不相同（见 §4.4）。

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
- **language**（2026-10-04 修）：chat-audio 形态下**可以**透传了，映射为
  `asr_options.language`。小米该字段是严格白名单（§2），所以网关做**归一
  而非直传**：`zh-CN`/`zh_CN`/`ZH`/`zho` → `zh`，`en-US`/`eng` → `en`，
  `detect` → `auto`；**无法映射的（ja/ko/fr-CA…）一律省略该键**回落到
  上游自动识别——直传会把今天能成功的请求变成 400。prompt 仍无法透传
  （上游拒收 text part）。
- **音频格式前置校验**（2026-10-04 修）：chat-audio 形态下本地拦掉非
  wav/mp3 的输入并回 400，不再先 base64 膨胀 4/3 传完整音频才换上游一句
  400。multipart 透传形态不拦（上游可能收 webm/m4a）。
- **被丢弃参数的回执**（2026-10-04 审计补）：chat-audio 形态下 `prompt` 与
  `temperature` 无处安放（上游拒收 text part，官方 ASR 请求体也只有
  messages/model/asr_options/stream 四个键）、`language` 归一后可能落空，
  这三种情况此前**完全静默**。现在非流式走响应头
  `X-Gw-Audio-Ignored-Params`、流式走 `transcript.ignored_params` 事件
  （流式的响应头已随 WriteHeader(200) 发出，进不去——与 transport 同处置）。
  判据：`language` 只有「传了但归一落空」才算丢弃，映射成功的（zh-CN→zh）
  不算——否则回执本身就是谎。透传形态三项都原样上行，不产生回执。

`POST /v1/audio/speech`（JSON：model/input/voice/response_format/speed）
- 响应 audio/* 二进制（小米 24kHz WAV）；voice 归一如上。
- **speed 回执**（2026-10-04 修）：小米 chat 协议无语速旋钮（§2 实测），
  客户端传了 speed 时响应头带 `X-Gw-Audio-Ignored-Params: speed`，如实
  告知未生效，而不是静默忽略。
- **voice 替换回执**（2026-10-04 审计补）：客户端点名了这条上游没有的音色
  （OpenAI 的 `alloy`/`nova` 等）时我们换成 `mimo_default`——拿到的声音与
  请求的不是同一个人，此前同样静默。现在并入同一个回执头。
- **response_format 的上游差异**（2026-10-04 审计补，见 §4.6）：MiMo 的
  `audio.format` 只收 `wav/mp3/pcm/pcm16`，比 OpenAI `/audio/speech` **窄**
  （OpenAI 还收 opus/aac/flac）。此前网关用的是 OpenAI 全量口径，客户端按
  OpenAI 契约请求 opus 时被原样发给小米。
- **三个 TTS 变体的请求形状不同**（2026-10-04 修，见 §4.4）。

### 4.4 MiMo TTS 三变体（2026-10-04）

`mimo-v2.5-tts` / `-tts-voicedesign` / `-tts-voiceclone` 在目录里都是
`modality=audio`，但**上游要的请求形状互不相同**。首版按同一个形状发，
后两个变体必然 400：

| 变体 | messages | audio.voice |
|---|---|---|
| `mimo-v2.5-tts` | assistant(文本) | 内置音色名，未知值归一 `mimo_default` |
| `-voicedesign` | **user(音色描述) + assistant(文本)** | **必须省略**（带则 400） |
| `-voiceclone` | assistant(文本) | 必须是**样本 DataURL**（音色名 400） |

对应到 OpenAI 的 `voice` 字段：voicedesign 把它当**音色设计描述**用
（没给就用兜底描述，上游要求 user 消息非空），voiceclone 把它当**样本
DataURL** 透传（给音色名时本地 400 + 可照改的说明，不打上游）。

`POST /v1/mcp`（MCP streamable HTTP，JSON-RPC 2.0）
- initialize / notifications/*（202）/ tools/list / tools/call
  （transcribe_audio、synthesize_speech）/ ping；未知方法 -32601；
  GET 405（规范允许无推送通道）；单 POST 纯 JSON 应答合法
  （2025-06-18 spec）。
- 与 HTTP 端点同一 AudioService 执行路径、同一鉴权（Bearer sk-*，
  KeyVerifier + 限流 + 预算）。
- 工具 schema 只声明各形态真能兑现的字段：`transcribe_audio` 不声明
  `prompt`/`temperature`（chat-audio 下无处安放），`synthesize_speech` 不声明
  `speed`（小米无语速旋钮）。客户端按 schema 就传不出这些参数，也就不需要
  在 MCP 面上再回执一次。

### 4.6 上游契约的官方文档复核（2026-10-04 批判审计）

§2 的实测矩阵是「我们打了什么、上游答了什么」，它**不能发现我们没打的
参数**。这轮把官方文档当作独立信源复核了一遍（`mimo.mi.com/llms.txt` →
`static/docs/api/audio/Speech-Recognition.md` 与 `.../tts.md`），结论：

**被证实的**（实测与文档一致，代码无需改）：

| 契约 | 文档 | 实测 |
|---|---|---|
| ASR 入口是 `POST /v1/chat/completions` | ✅ 文档的 Request Address 就是它 | ✅ token-plan 上 `/audio/transcriptions` 404 |
| `asr_options` 只有 `language` 一个子键 | ✅（auto/zh/en，默认 auto） | ✅ 严格白名单 |
| ASR 输入只收 mp3 / wav | ✅ | ✅ m4a 400 |
| TTS 三个变体 + voicedesign 需 user 消息、voiceclone 需样本 | ✅ | ✅ |
| 内置音色 9 个（mimo_default/冰糖/茉莉/苏打/白桦/Mia/Chloe/Milo/Dean） | ✅ | ✅ 与错误体清单一致 |

**新查出来的**（实测没覆盖到，文档与代码冲突 → 已修）：

- **TTS `audio.format` 只有 `wav/mp3/pcm/pcm16`**，且 `pcm` 与 `pcm16`
  等价。比 OpenAI `/audio/speech` **窄**——OpenAI 还收 `opus/aac/flac`。
  此前网关的归一口径是 OpenAI 全量集合，于是客户端按 OpenAI 契约请求
  `opus` 时被原样发给小米。修法见 `normalizeTTSFormatForCandidate`：
  桥接形态按官方白名单（`pcm16`→`pcm`），`opus/aac/flac` 本地 400；
  **透传形态不动**（对 OpenAI 兼容上游这三个仍合法）。
- **voiceclone 样本只收 mp3/wav**。此前只校验 `data:` 前缀，m4a 样本能过
  本地校验再被上游 400——与 ASR 侧已做的格式前置校验不对称。
- ASR 的 `input_audio.data` 官方措辞是「data URL」，但同一段又写明「只给
  base64 时 `format` 必填」，两种形态都合法；网关发的是后者，实测 200。

**方法论教训**：22 项实测矩阵与 12 项端到端全部「绿」，仍然漏掉了
`response_format` 这条——因为**没有一条用例会去请求 opus**。覆盖矩阵按
「我们测过的」开，不按「上游声明的」开。复核信源要独立于实现。

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

## 7. 验证证据

### 7.1 首测（本地 8782，build 2.5.8.2432，2026-10-03）

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

### 7.2 复测（2026-10-04，拿到新的 `tp-` key 重新全量打）

直连上游矩阵 22 项（§2）+ 网关端到端 12 项，全部实测留痕。**复测推翻
了首测的一条结论**（language 不支持透传），另外发现 4 个网关侧缺陷并修复
（§4.2/§4.4）：

| 验证项 | 结果 |
|---|---|
| 凭据形态 | `tp-` key 在 `api.xiaomimimo.com` 双鉴权头均 401；在 `token-plan-cn` 两种头均 200（§2 首行） |
| `/v1/models` | 8 个模型，音频四兄弟齐 |
| `/audio/transcriptions`、`/audio/speech` | token-plan 主机上均 404 → 证实必须桥接 |
| 直连 ASR（wav/mp3/16k/data-URL/流式） | 全 200，转写逐字正确；`usage.seconds` 向上取整（6.98s→8、110.9s→112） |
| 直连 `asr_options.language` | `zh/en/auto` 200；`zh-CN/zh_CN/en-US/ja/ko/ZH/auto-4` 全 400 |
| 直连 TTS（茉莉/默认/无 modalities/mp3/pcm/流式） | 全 200；音色名大小写敏感（`Mia` 400，`mia` 200） |
| 直连 TTS `speed` | 200 但输出**逐字节相同** → 确认无语速旋钮 |
| 直连 voicedesign（无 voice + user 描述） | 200；带 voice 或缺 user 描述均 400 |
| 直连 voiceclone（voice=样本 DataURL） | 200；voice=音色名 400 |
| 网关 ASR wav/mp3/stream/text/chat+input_audio | 全 200，转写正确；SSE 36 delta + done + [DONE] |
| 网关 `language=zh` | **修复前** 200 但 language 被丢弃；**修复后** 映射进 `asr_options` |
| 网关 m4a 输入 | **修复前** 502（上游 400）；**修复后** 本地 400，不打上游 |
| 网关 TTS 茉莉/默认/mp3/voice=alloy | 全 200（voice 归一生效） |
| 网关 TTS `speed=1.5` | 200 + `X-Gw-Audio-Ignored-Params: speed` |
| 新增单测 | `audio_xiaomi_variants_test.go`：§7.2 写入时 20 个 Test，§7.3 审计后 34 个（数字以 `grep -c '^func Test' ` 为准；此前此处写「17 个」是从没数过的估值） |
| 新增实网单测 | `audio_xiaomi_live_test.go`：7 个 opt-in Test（`LLM_GATEWAY_LIVE_XIAOMI_KEY` 才跑，默认 SKIP），含 asr_options 的**差分对照** |
| `TestMCPEarlyErrorsUseJsonRPCEnvelope` | 复测发现它在 main 上**本来就是红的**（其失败信息在描述当时行为）——顺手修掉（见 §4.5） |

复测中发现首测的一处**测试盲区**：新增的 400-vs-502 判据第一版就红了，
原因是 multipart 透传形态的错误构造点漏改（变量名不同，批量替换没命中）——
即「三条路径都改了」的假设本身是错的，判据把它抓了出来。

复测还发现**桩测试的固有盲区**：`normalizeASRLanguageForCandidate` 的桩
全绿（含 16 个映射子用例），但「上游只认 zh/en/auto」这条知识是从旧设计
文档继承的。补上实网差分对照后这条才变成真判据（§7.2 的 control/gateway
两行日志）——只断言网关 200 的话，网关把 language 整个丢掉也能过，而那
正是首版的 bug。

### 4.5 MCP 协议级错误的信封（2026-10-04 顺手修）

`TestMCPEarlyErrorsUseJsonRPCEnvelope` 在 main 上是红的：空 body / 批量
请求 / 非法 JSON 三条早失败路径回的是 **OpenAI 形态**错误信封 + HTTP 400，
而 MCP streamable HTTP（2025-06-18）要求协议级错误用 **JSON-RPC 信封 +
HTTP 200**（200 表示「往返成功，错误在信封里」）。MCP 客户端按规范解析
拿不到 `error.code`。

已切到 `writeJSONRPCError`（HTTP 200 + `{"jsonrpc","id":null,"error"}`）。
鉴权/服务不可用/体积超限**不动**——它们发生在协议成立之前，保持传输层
4xx。顺带把该测试里一条无条件打印「当前实现违反协议契约」的 `t.Log` 改成
只在真违反时说话：修好之后它照样原话打印，是会误导人的绿测日志。

### 7.3 批判式审计（2026-10-04 第三轮）

对 §7.2 的成果做「假设它没做对」的一轮复核。三类查法：

**① 找原则没有贯彻到底的地方。** §7.2 建立了「客户端传了但兑现不了的参数
要回执」这条原则，却只用在 TTS `speed` 上。ASR 侧同一形态的 `prompt`、
`temperature`、`language`（归一落空时）仍然是**完全静默**的丢弃，而且
`TranscribeResult` 连 `IgnoredParams` 字段都没有。`prompt` 是领域词汇提示，
被吞掉时转写结果只是「看起来不准」，调用方无从判断是模型能力问题还是提示
词没生效。voice 替换同理：`voice=alloy` 与 `voice=茉莉` 拿到的是同一个声
音，此前静默。→ 已修（§4.2），非流式走响应头、流式走 SSE 事件。

**② 拿独立信源复核前提。** §7.2 自己写了「桩测试抓不到错误的前提」，却没
再去动前提。§4.6 用官方文档复核，证实 5 条、推翻 0 条、**新查出 3 条**——
其中 `response_format` 这条是真缺陷（已用 opus 实测证明上游确实会收到
`audio.format="opus"`）。

**③ 查自己的数字。** §7.2 写「新增单测 17 个」，实际当时 20 个、现在 34 个。
17 这个数从没数过。这类数字腐烂在本仓反复出现，所以本轮把它标成「以
`grep -c` 为准」而不是再写一个新值。

**判据有牙的证明**（光「新测试全绿」不算证据）：5 条变异逐个撤掉修复，每条
都要求「包仍能编译、只有门转红」，跑完 `cmp` 逐字节还原源文件：

| 变异 | 结果 |
|---|---|
| 撤销 opus/aac/flac 本地拦截 | ✓ 转红（3 个子用例） |
| 撤销 `pcm16`→`pcm` 别名 | ✓ 转红 |
| 撤销 voice 替换回执 | ✓ 转红 |
| 撤销 ASR 三项回执 | ✓ 转红（2 个用例） |
| 撤销 voiceclone 样本格式校验 | ✓ 转红 |

反向守卫也补了一条：`TestNonBridgeCandidateStillAcceptsOpus`——若有人把
`audioTTSEffectiveFormat` 的 OpenAI 全量口径改窄，会打断所有非小米 TTS
供应商，这条会红。

**仍未验证的**：这些修正的实网效果没重打（`tp-` key 未在本轮使用）。判据
是「我们不再发出上游不收的格式」+「回执如实」，不是「实网 200」。需要
实网复验时：`LLM_GATEWAY_LIVE_XIAOMI_KEY=<tp- key> go test ./domains/streaming/
-run TestLiveXiaomi`。

## 8. 已知边界与后续候选

- 小米 ASR 的 **prompt** 仍无法透传（拒收 text part，官方请求体也没有等价
  字段）；`temperature` 同理。两者现在会进 `X-Gw-Audio-Ignored-Params` 回执，
  调用方至少知道自己传的东西没生效。`language` 已可透传（走 `asr_options`，
  无法映射时按自动识别处理，且此时也回执）。流式语种由模型自判。
- 小米 ASR 仅 wav/mp3；客户端负责转码（openpocket 采集链路已是 PCM）。
  网关在 chat-audio 形态下本地校验并回 400（不再白跑一轮）。
- 小米 TTS 无语速旋钮，`speed` 如实回执为未生效；要真语速需在网关做
  重采样（会动音质，倾向不做，让调用方自己在客户端变速）。
- 小米 TTS 的 `audio.format` 比 OpenAI 窄（只收 wav/mp3/pcm/pcm16，见
  §4.6）。客户端按 OpenAI 契约请求 opus/aac/flac 现在会被本地 400 拦下——
  **这是行为变化**：这类请求原先会打到上游（必然 400，此前还回 502）。
  有客户端依赖「请求 opus 拿回 wav」的话会开始报错，那本来也不是契约。
- 小米 TTS **流式**回单行裸 JSON（无 `data: ` 前缀），非规范 SSE。网关
  TTS 端点当前不流式（与 OpenAI `/audio/speech` 一致），故未适配；若要
  开放流式 TTS，需要同时兼容这形状。官方还有 `optimize_text_preview`
  （voicedesign 专用的播报文本润色，未接入）。
- `voiceclone` 变体只能经 `/audio/speech` 的 `voice` 字段传样本 DataURL，
  与 OpenAI「voice 是音色名」的契约不同名同实；已在端点与 MCP 工具描述里
  写明。样本容器只收 wav/mp3（官方文档），本地已前置校验。
- 透传形态的流式是「收完再转发」的伪流式（智谱 30s 限长下可接受）；
  真流式透传需要 handler 级 io.Pipe，列为后续候选。
- MiniMax asr-1.0 的非标路径 /v1/speech_to_text 与事件形状未适配
  （其 SSE 是 {index,delta,finish} 私有形状）；如需接入加一个
  transport adapter 即可。
- 长音频切分聚合保留在客户端（openpocket full.go 已有）；网关侧
  内置切分列为后续候选（需考虑音频解码依赖，倾向不做）。
- 音频端点暂不写 request_logs 审计（与 embeddings 同款现状）；
  计量口径可先用上游 usage.seconds（响应头已透出）。
