# 参数兼容静态化轮：跨端点 reasoning 实证（2026-11-01）

> 任务（goal 会话）：在 nvidia（chat 线）之外，取 **OpenRouter**（chat 线且原生认
> `reasoning{effort,max_tokens,exclude,enabled}`）、**volcengine-coding**（方舟
> GLM 家族 coding 端点）、**zhipu 原厂**三条腿的实机请求对照，回答：
> (1) chat 线端点上顶层 reasoning 对象是否普遍被拒？
> (2) 若客户端同时发 `reasoning{effort:E}` 与 `reasoning_effort=E`，能否安全只
> 保留后者（有无供应商只认前者）？
> (3) 结论若成立，按 r1021 教义在 `internal/paramreg` 登记方言级规则并加变异臂。
> 另复核 TTL 再探测成本（nvidia glm 约 5 次/3h）是否需要按 (provider,model) 分级。
>
> 结论先行：**(1) 不普遍被拒——三种行为并存；(2) 成立——四腿全部认下划线，
> 无「只认对象」的供应商；静态登记已落地，变异臂 9/9 检出；TTL 不做静态分级表，
> 改指数退避。**

---

## §0 测试床与凭据来源

- 实机请求：本机直连上游（python urllib，非流式，temperature=0，max_tokens 2048~4096）。
- 凭据：211 生产库 `credentials.secret_ciphertext`（`v1:legacy:` Fernet 信封），
  用仓内 `secret.DecryptAny` + 容器 env 双密钥本地解密（一次性工具，
  `/tmp/paramcompat/decrypt/main.go`；密钥与明文均未入 git，临时文件权限 600）。
- 四条腿与凭据（providers 表）：
  | 腿 | provider (id) | credential | base_url | 实测模型 |
  |---|---|---|---|---|
  | OpenRouter | openrouter (21) | 44 openrouter-kx | https://openrouter.ai/api/v1 | `nvidia/nemotron-3.5-lightning:free`（主）/ `z-ai/glm-5.2`（付费档欠费，仅 schema 校验层读数） |
  | 方舟 coding | volcengine-coding (29/34) | 11 demo-tokenplan | https://ark.cn-beijing.volces.com/api/**coding**/v3 | glm-5-3-flash-260828（**CodingPlan 订阅失效，全臂 400**）|
  | 方舟 normal（替代腿） | volcano-normal (35) | 12 hzx-normal（key 401 未激活）→ **改用 cred 11 打 /api/v3** | https://ark.cn-beijing.volces.com/api/v3 | `glm-5-2-260617` |
  | zhipu 原厂 | zhipu (32) | 62 zipu-max-account2 | https://open.bigmodel.cn/api/**coding**/paas/v4 | `glm-5.3` |
  | nvidia（对照） | nvidia (18) | 8/18/19/23 | https://integrate.api.nvidia.com/v1 | `z-ai/glm-5.3-flash`（**组织级 429，直探不可用**，证据取生产 reqanom 台账）|
- 样本原文存档：`/tmp/paramcompat/samples/*.json`（26 文件，请求/响应全文）。

## §1 问题 (1)：顶层 reasoning 对象在 chat 线**不是普遍被拒**——三种行为并存

### 行为 A：nvidia NIM 硬 400（严格校验器）

直探被组织级 429 挡住（`{"status":429,"title":"Too Many Requests"}`，四凭据全试），
证据取 211 生产 reqanom 台账（redis db5 `llmgw:reqanom:records`，四天连证）：

```
day=2026-10-30 param=reasoning status=400 rec=1 occ=23
  sample={"message":"Validation: Unsupported parameter(s): `reasoning`","type":"Bad Request","code":400}
day=2026-10-31 param=reasoning status=400 rec=1 occ=5
day=2026-10-28 param=enable_thinking,reasoning status=400 occ=17
  sample={"message":"Validation: Unsupported parameter(s): `reasoning`",...}
day=2026-10-29 param=prompt_cache_key status=400 occ=19
  sample={"message":"Validation: Unsupported parameter(s): `prompt_cache_key`",...}
```

同台账四天内 `reasoning_effort` **从未**出现在被拒参数里 ⇒ NIM 接受下划线形态。

### 行为 B：zhipu / 方舟宽容解析但**静默忽略**（200，但对象零效果）

zhipu glm-5.3（coding paas/v4，全臂 200；tokens=completion reasoning_tokens）：

```
underscore_low   ×3: 132 / 489 / 210          （下划线低档带）
underscore_high  ×3: 814 / 1573 / 2365        （下划线高档带，≈低档 5 倍）
object_minimal   ×2: 2645 / 1779              （对象形态与档位无关 = 被忽略）
underscore_minimal ×1: 201                    （minimal 枚举接受且生效）
CONFLICT underscoreHigh+objectLow ×3: 1934 / 2228 / 936   （2/3 落 high 带）
CONFLICT underscoreLow+objectHigh ×3: 119 / 898 / 148     （2/3 落 low 带）
```

方舟 glm-5-2-260617（/api/v3，cred 11）——**最小档判据是确定性的**：

```
underscore_minimal ×2: reasoning_tokens=0 / 0            （确定性归零思考）
underscore_high    ×3: 1074 / 910 / 1082
underscore_low     ×3: 1071 / 1101 / 1692
object_minimal     ×3: 1117 / 2912 / 1892               （对象形态被忽略：思考照常）
CONFLICT underscoreMinimal+objectHigh ×3: 0 / 0 / 0     （下划线赢，确定性）
CONFLICT underscoreHigh+objectMinimal ×2: 1108 / 1644    （下划线赢）
```

### 行为 C：OpenRouter 原生接受且**真生效**

付费模型欠费（402）不影响 schema 校验层读数；取 `nemotron-3.5-lightning:free`
完成效果层（该模型**无参数时思考打满**：baseline ×2 均 `reasoning_tokens=4096`）：

```
A reasoning_effort=high（仅下划线）: 200 rt=339    （下划线把 4096 压到 339 = 生效）
B reasoning={effort:high}（仅对象）: 200 rt=394    （对象同样生效）
C 双表达同值                        : 200 rt=386
D 双表达冲突值: 400 {"error":{"message":"\"reasoning_effort\" and
              \"reasoning.effort\" are both provided with conflicting values","code":400}}
E reasoning={exclude:true}+下划线   : 200 rt=327
F reasoning={enabled:true}          : 200 rt=366
```

另两条硬事实：付费档 `z-ai/glm-5.2` 欠费 402 下 D 臂仍 400（**校验先于计费**，
冲突拒绝是端点级行为，与模型无关）；目录层 14 个 free 模型全部同时声明
`reasoning` 与 `reasoning_effort` 两个 supported_parameters。

### 判读

「普遍被拒」不成立。nvidia 400；zhipu/方舟 200-但-忽略（**意图无声丢失**比 400
更隐蔽）；OpenRouter 原生。同一参数三种行为，正是不做全局静态裁剪、按方言登记
的理由（r1021 教义的适用边界：硬拒才 RejectedBy；静默忽略走翻译）。

## §2 问题 (2)：双表达同发**只保留 `reasoning_effort` 安全**（成立）

| 腿 | 对象形态 | 下划线形态 | 双表达同值 | 双表达冲突 | 只留下划线是否安全 |
|---|---|---|---|---|---|
| nvidia | **400**（硬拒） | 接受 | —（对象先死） | — | **安全且更优**（消 400 往返） |
| zhipu | 200 但忽略 | **生效**（档带 5 倍可分辨） | 200 | 200，**下划线赢**（2/3 判读） | **安全**（对象本来就是死的） |
| 方舟 | 200 但忽略 | **生效**（minimal 确定性归零） | 200 | 200，**下划线赢**（0/0/0 判读） | **安全**（同上） |
| OpenRouter | **原生生效** | **生效**（339 vs 基线 4096） | 200 | **400 端点级拒绝** | **安全**（同值等价；冲突值从 400 变为按下划线 200——消除一整类 400） |

**四腿没有「只认对象形态」的供应商**（任务的直接问句）。注意一个例外条款：
对象可携带下划线表达不了的旋钮（OpenRouter 的 `exclude` / `max_tokens` /
`enabled`）——双表达同发时只能剥对象内的 effort、**旋钮必须保留**，否则
OpenRouter 上 `exclude:true` 这类语义会丢。

## §3 代码改动（r1021 教义：方言级登记进 paramreg）

| 文件 | 改动 |
|---|---|
| `internal/paramreg/dialect.go` | 新方言 `DialectNVIDIA`（"nvidia"，BaseProtocol=openai_chat）+ `catalogToDialect["nvidia"]` + AllDialects。登记前 catalog "nvidia" 恒落 Unknown fail-open，thinking 族硬拒全靠 reqprobe 每 TTL 窗口一轮 400 兜底 |
| `internal/paramreg/registry.go` | `reasoning` 行加 `RejectedBy: []Dialect{DialectNVIDIA}` + 三行为注释；`thinking` / `enable_thinking` 的 RejectedBy **补 NVIDIA**（方言登记后 dst 不再是 Unknown，`RejectedByUnknown` 对 nvidia 失效——不补会把 unknown-relay 时代已裁剪的字段重新放进 nvidia 请求体，回归）；`prompt_cache_key` 加 RejectedBy[NVIDIA] + 注明主线仍走 reqprobe（chat 入向被 parse 消费进 IR，serialize_openai 无方言门控，静态登记只覆盖跨协议 Extensions 腿——序列化侧门控留卡） |
| `internal/paramreg/translate.go` | 新 `ReasoningObjectChatDecision`：硬拒查 registry `RejectedBy`；双表达同发剥对象内 effort（effort-only → ActionSkip；带旋钮 → 剥 effort 留旋钮）；无下划线时翻译方言集 {GLM, Ark, DeepSeek, Grok, OpenAIChat} 把 `reasoning{effort}` 提升为 `reasoning_effort`（zhipu/方舟否则静默吞意图；值域由 paramguard `fixReasoningEffort` 出站钳制——两种载体它都覆盖）；保留方言集（OpenRouter/Unknown/MiniMax/Kimi/Qwen/Mistral/VLLM/Ollama）原样透传零回归 |
| `internal/ir/extensions_restore.go` | chat 线 reasoning 特例（thinking/stream_options 同款模式）：调 `ReasoningObjectChatDecision`，Skip/Drop(loss)/Restore 三态落地。放在特例而非改 Kind 的原因：`IRHandledFields` 驱动 TransportIR 侧 `isStandardField`，改 Kind 会把 reasoning 移出该集合改变 ExtensionsBag 提取面 |
| `internal/reqprobe/types.go` | 学习窗口指数退避：`learnedWindow`/`learnedWindowWithSteps`（base × 2^(RecoveredCount−1)，上限 `LLM_GATEWAY_REQPROBE_LEARNED_TTL_BACKOFF_MAX` 步，默认 4 ⇒ 16×，0=关闭；硬封顶 10 步） |
| `internal/reqprobe/{redis,memory}.go` | 两处 `LearnedParams` cutoff 从固定 `learnedTTL()` 改为按记录 `learnedWindow(rec)` |

新测试：`internal/paramreg/reasoning_object_chat_test.go`（方言登记契约 + 决策矩阵）、
`internal/ir/reasoning_object_chat_restore_test.go`（端到端四臂 + 双表达三臂）、
`internal/reqprobe/learned_backoff_test.go`（窗口步进 + LearnedParams 语义）、
`internal/reqprobe/redis_test.go` 追加 `TestRedisStoreLearningBackoffWindow`（redis 退避接线）。

## §4 变异臂读数（9/9 检出，还原后 md5 逐字一致）

| 臂 | 注入 | 定向测试 |
|---|---|---|
| M1 | reasoning 行摘 `RejectedBy: [NVIDIA]` | TestReasoningRejectedByNVIDIA 红 ✓ |
| M2 | thinking 行摘 NVIDIA | TestThinkingFamilyRejectedByNVIDIA 红 ✓ |
| M2b | enable_thinking 行摘 NVIDIA | 同上红 ✓ |
| M3 | 翻译方言集删 GLM | 决策矩阵 GLM 臂红 ✓ |
| M4a | extensions_restore reasoning 特例整块删除 | 端到端四臂红 ✓ |
| M4b | 下划线存在性探测恒 false | 双表达三臂红 ✓ |
| M5 | 退避指数归零（shift=0） | 窗口 + LearnedParams 臂红 ✓ |
| M6 | memory cutoff 退回固定 TTL | TestLearnedParamsBackoffWindows 红 ✓ |
| M7 | redis cutoff 退回固定 TTL | TestRedisStoreLearningBackoffWindow 红 ✓ |

读数：三包（paramreg / reqprobe / ir）+ paramguard + transformation +
executors（43.9s 全套）全绿；`go build ./...` / `go vet` / gofmt ✅。

## §5 TTL 再探测成本复核：不做 (provider,model) 静态分级表，改指数退避

**复核结论**：
1. 粒度已经就位：`LearnedKey = (providerID, outboundModel, actualWire)`，
   「按 (provider,model) 分级」的载体本来就有，无需新表。
2. 成本的真实形状：窗口一过，**下一个真实用户请求**变成探测器（400+剥参重试，
   时延翻倍、计入上游 4xx）。nvidia glm 生产实测固定窗口下稳定复现
   （10-31 `param=reasoning` occ=5；10-29 事故日 occ=393/493 是放大类旧账）。
   低流量腿的受害面最大：窗口内请求数越少，探测占比越高。
3. 静态分级表（如 per-provider TTL 配置）被否：需要人工维护、无法自适应、
   且与「上游某天支持该参数后要尽快发现」的再确认语义冲突。
4. **落地：指数退避**——同参数族每次「再探测后仍被拒且剥参成功」（
   RecoveredCount 递增）窗口翻倍，1h→2h→4h→8h→16h 封顶（默认 4 步；
   `LLM_GATEWAY_REQPROBE_LEARNED_TTL_BACKOFF_MAX=0` 可关，保留 10-29 轮
   「时效 1h、定期再探测」的初始语义）。被反复证实的拒绝探测频率指数下降
   （nvidia glm 场景 ≈5 次/3h → ≈5 次/天档），上游一旦开始接受该参数，
   首轮 base 窗口内就会重新发现。指纹含 Day，退避自然按日重置。

## §6 未做 / 遗留（别重复排查）

- **nvidia 直探 reasoning_effort**：组织级 429（四凭据全试）。`reasoning_effort`
  被 NIM 接受的判据是间接的（reqanom 四天无一条 reasoning_effort 拒绝 +
  responses→chat 序列化路径恒发 reasoning_effort 无 400 记录）。故 NVIDIA 不进
  翻译方言集（对象直接裁剪，与今日剥参后线体等价，任何假设下都安全）。
- **`prompt_cache_key` 的 serialize 侧方言门控**：chat 入向该字段进 IR，
  `serialize_openai` 无方言门控恒发射，registry RejectedBy 对主线不生效
  （本登记只覆盖跨协议 Extensions 腿）。完整修复需序列化侧按 IRFieldAllowedForDialect
  门控或声明 Dialects 白名单（影响面大，留卡）。
- **volcano 两凭据现状**：coding 端点 CodingPlan 订阅失效（400 + 账户号），
  normal 端点 key 401 未激活（cred 12）——方舟腿本轮用 cred 11 打 /api/v3 替代；
  两腿凭据处置属运维。
- **MiniMax/Kimi/Qwen 等方言的对象效果未实证**：维持对象原样透传（今日语义），
  不进翻译集——枚举未证实就翻译可能把今天 200 的请求变红。
- OpenRouter 付费档欠费（402），效果层读数取 free 腿；付费 GLM 腿待充值后可复验。

## §7 实测读数记录

- OpenRouter free 腿基线：无参数 `reasoning_tokens=4096`（打满 max_tokens）×2。
- zhipu `reasoning_effort=minimal` 200 生效（rt=201，低于 low 带）——GLM 枚举
  接受 minimal（registry「GLM 7 档」的实证补一笔）。
- OpenRouter schema 校验先于计费（402 账户上 D 臂冲突仍 400）。
- 211 生产 `LLM_GATEWAY_TRANSPORT_IR` 未启用（env 实测 0 条）——TransportIR
  侧 `isStandardField` 消费面不在生产路径，但 Kind 保持 IRHandled 仍是对的
  （零风险原则）。
