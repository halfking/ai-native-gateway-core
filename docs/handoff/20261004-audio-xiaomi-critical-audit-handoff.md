# handoff：小米 ASR/TTS 音频面批判式审计（2026-10-04 第三轮）

> 正文见 [`docs/design/2026-10-03-audio-transcription-gateway-plan.md`](../design/2026-10-03-audio-transcription-gateway-plan.md)
> 的 **§4.6**（官方文档复核）与 **§7.3**（本轮审计）。本文件只写交接需要的部分。
>
> 前两轮落点：`11bc7def2`（复测修 5 处）、`67da11864`（no_provider typed 化）。
> 本轮见文末「提交落点」。

## 结论

一句话：**前两轮的 22 项直连 + 12 项端到端全绿，掩盖了三个从未被请求过的参数**
（`response_format=opus`、`prompt`、`voice=非内置音色`）。绿灯只覆盖了
「我们测过的」，没覆盖「上游声明的」。

| # | 发现 | 性质 | 状态 |
|---|---|---|---|
| 1 | MiMo TTS `audio.format` 只收 `wav/mp3/pcm/pcm16`，网关用的是 OpenAI 全量口径 → 客户端按 OpenAI 契约请求 `opus` 时被原样发给上游 | **真缺陷**（白跑一轮 + 上游 400） | 已修 |
| 2 | ASR 侧 `prompt`/`temperature`/`language`(归一落空) **完全静默**丢弃，`TranscribeResult` 连字段都没有 | **真缺陷**（同一原则只用在 TTS speed 上） | 已修 |
| 3 | `voice` 被替换成 `mimo_default` 时静默——`alloy` 与 `茉莉` 拿到同一个声音 | **真缺陷** | 已修 |
| 4 | voiceclone 样本只收 wav/mp3（官方），本地只校验了 `data:` 前缀 | 不对称（ASR 侧已做前置校验） | 已修 |
| 5 | 文档写「新增单测 17 个」，实际 20 → 现 34 | 数字腐烂 | 已标注 |
| 6 | 官方文档复核：5 条既有结论**全部证实** | 反向结论（有价值） | 见 §4.6 |

**没做的**：不动 `audioTTSEffectiveFormat` 的透传口径（opus/aac/flac 对
OpenAI 兼容上游仍合法），不动 MCP 工具 schema（它本来就不声明 speed/prompt/
temperature，客户端传不出来，也就不需要回执）。

## 根因

两条，同源：

1. **覆盖矩阵按「我们测过的」开，不按「上游声明的」开。** 没有一条用例会
   去请求 opus——不是漏跑，是矩阵里根本没有这一格。而 `response_format` 是
   OpenAI 契约里最常用的参数之一。
2. **同一条原则只落在它被想到的那一处。** 「参数兑现不了要回执」在写
   `speed` 时想到了，写 `voice`/`prompt`/`temperature` 时没回头扫。

## 改动文件与关键行为

| 文件 | 改动 |
|---|---|
| `domains/streaming/audio_service.go` | `TranscribeResult.IgnoredParams` 字段；`ignoredChatAudioASRParams`（prompt/temperature/language 落空）；`xiaomiTTSFormats` + `ttsFormatsOpenAIOnly` + `normalizeTTSFormatForCandidate`（pcm16→pcm，opus/aac/flac 本地 400）；`buildChatAudioTTSPayload` 多返回「被替换的参数」；`voiceCloneSampleFormats` + `dataURLMediaType`；`ignoredChatAudioTTSParams` 合并 voice |
| `domains/streaming/audio_transcriptions.go` | 非流式 `X-Gw-Audio-Ignored-Params` 响应头（WriteHeader 之前设置）；流式 `transcript.ignored_params` SSE 事件 |
| `domains/streaming/audio_xiaomi_variants_test.go` | §8 新增 11 个 Test（含 1 条反向守卫） |
| `docs/design/2026-10-03-audio-transcription-gateway-plan.md` | §4.2/§4.3/§4.6/§7.2/§7.3/§8 更新 |

**关键行为变化（对外可见）**：客户端对 MiMo TTS 请求 `response_format=opus|aac|flac`
从「打到上游必然 400（此前还回 502）」变成**本地 400**，错误体 code=
`invalid_audio_request`，message 点名支持的格式。依赖「请求 opus 拿回 wav」的
客户端会开始报错——那本来也不是契约。

## 测试命令与结果

```bash
go build ./...                                   # OK
go vet ./domains/streaming/                      # OK
go test ./domains/streaming/                     # 全量，见提交消息
go test ./domains/streaming/ -run 'TestTTSBridgeRejects|TestTTSBridgeAcceptsOfficial\
|TestNonBridgeCandidateStillAcceptsOpus|TestVoiceCloneRejects|TestVoiceCloneAcceptsWav\
|TestTTSVoiceSubstitution|TestTTSKnownVoice|TestASRBridgeDiscloses|TestASRPassthroughDoesNot\
|TestASRStreamDiscloses' -v                     # 11 个 PASS（含子用例）
```

**判据有牙的证明**（光「新测试全绿」不算证据）。5 条变异逐个撤掉修复，
每条要求「包仍能编译、只有门转红」，跑完 `cmp` 逐字节还原：

| 变异 | 结果 |
|---|---|
| 撤销 opus/aac/flac 本地拦截 | ✓ 转红（3 子用例） |
| 撤销 `pcm16`→`pcm` 别名 | ✓ 转红 |
| 撤销 voice 替换回执 | ✓ 转红 |
| 撤销 ASR 三项回执 | ✓ 转红（2 用例） |
| 撤销 voiceclone 样本格式校验 | ✓ 转红 |

缺陷**先证明存在**再修：修复前用 `opus` 实测桩确认上游确实收到
`audio.format="opus"`（`upstream received audio.format = "opus" ; gateway status=200`）。

## 遗留风险

1. **实网未复验**。本轮没用 `tp-` key，判据是「不再发出上游不收的格式」+
   「回执如实」，不是「实网 200」。要复验：
   `LLM_GATEWAY_LIVE_XIAOMI_KEY=<tp- key> go test ./domains/streaming/ -run TestLiveXiaomi`，
   并补一条请求 `response_format=opus` 的实网用例（应本地 400）。
2. **opus 拦截是行为变化**。见上「关键行为变化」。
3. **音色清单仍是硬编码的 9 个**（来自上游 400 错误体，官方文档一致）。上游
   将来加音色时，网关会把新名字当未知值归一成 `mimo_default`——**会静默换
   音色**。本轮的回执能让调用方看见，但更好的做法是探测/缓存上游清单，
   未做。
4. **部署后的二进制仍未复验**。本机 `deploy-local.sh` 卡在
   `host.docker.internal` 被代理劫持（fake-IP 198.18.x.x），非代码问题。
5. `optimize_text_preview`（voicedesign 专用播报文本润色）未接入。

## 下一轮提示词

> 复核信源要独立于实现。上一轮 22 项直连 + 12 项端到端全绿，仍然漏掉了
> `response_format`——因为没有一条用例会去请求 opus。覆盖矩阵按「我们测过
> 的」开，不按「上游声明的」开。
>
> 具体动作：
> 1. 拿 `tp-` key 跑 `LLM_GATEWAY_LIVE_XIAOMI_KEY=<key> go test ./domains/streaming/ -run TestLiveXiaomi`，
>    并在 `audio_xiaomi_live_test.go` 里加一条**新发现类**的实网用例
>    （如 opus 应本地 400、`pcm16` 应 200 且回 pcm）。判据是差分对照：
>    同一个上游拒收的值，直连断言 400、经网关断言 400/本地拦截。
> 2. 把 §4.6 的「官方文档复核」扩成**清单式对账**：把官方 ASR/TTS 请求体
>    的每个字段列出来，逐个标「网关透传 / 网关归一 / 网关丢弃+回执 / 未接」，
>    做成一张表。当前只对账了 3 个字段。
> 3. 音色清单硬编码（9 个）改成启动时探测或带 TTL 缓存，消除「上游加音色
>    → 静默换音色」。做完记得回执逻辑要相应去掉 voice 那一条。
> 4. 找同类问题的其余实例：仓库里还有哪些地方按错误字符串/固定清单做判定，
>    而上游契约在变？
