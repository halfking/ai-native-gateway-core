# reasoning_effort 收窄链路审计（2026-09-28）

> 范围：`internal/reasonnorm` / `internal/paramguard` / `internal/reasoncap` / `internal/paramreg`
> 起因：`llmgateway.internal.example.com` auto 路由「长时间只有思考/补全、没有正常响应」报障的批判式复审。
> 结论提要：**本轮修的是潜伏缺陷，不是报障的直接原因**。报障根因见 §5。

## 1. 报障现象（本机 8782 实例实测）

用户提示词：*「请检查48小时内1小时前所有没有合并的不活跃的子分支…」*（agentic 任务）

| 请求形态 | 选中模型 | 耗时 | 结果 |
|---|---|---|---|
| 无 `tools[]`（流式） | `glm-5.2` | 85s（首字 12.4s） | 思考 6850 字符 vs 正文 3478 |
| 无 `tools[]`（非流式） | `glm-5.2` | 27.3s / 34.9s | 把工具调用**当纯文本臆造**，无 `tool_calls` 字段 |
| **带 `tools[]`** | `deepseek-v4-flash` | **8.8s** | **2 个结构化 `tool_calls`** |

## 2. 缺陷一：`ClampEffort` 把「关闭思考」静默反转为开启（潜伏）

`internal/reasonnorm/norm.go` 的 `ClampEffort` 对无法识别的取值走
`effortIndex` 兜底档（index 3 = `medium`），再做「就近映射」：

```
ClampEffort("disabled",  glm-5 档位 {max,xhigh,high,medium,low,minimal,none}) = "medium"
ClampEffort("off",       deepseek-v4 档位 {low,high,max})                      = "high"   ← 并列取高
ClampEffort("bogus",     glm-5 档位)                                            = "medium"
```

`disabled` / `off` 是**关闭推理**的跨供应商通用写法，不是"选一个推理档位"。
修复前它们被解析成 **medium（glm）/ high（deepseek）**——即
**要求关闭思考，却拿到中档 / 高档思考**。

更糟的是它**看起来是正常的**：paramguard 会把这次改写记成一次
`Action: "clamp"` 的合法收窄，paramledger 报告显示
`original=disabled sent=medium`，读起来像"已按模型能力收窄"，
而不是"方向被翻转"。

### 修复

新增 `disableIntentEfforts` 词表（`disabled/disable/off/false/none/no/0`），
在就近映射**之前**拦截，一律解析为该模型支持的**最便宜档**
（新增 `cheapestEffort`，按规范档位序取最低，不按切片顺序）。

未知值（`bogus_value` 等）维持既有 medium 兜底，**不改变**——
避免波及 grok/mistral 等既有路径。

## 3. 缺陷二：方言门漏掉 GLM / DeepSeek / Ark（缺陷一的成因）

`internal/paramguard/guard.go` 的 `reasonDialectMatchesParamDialect`
只覆盖 OpenAI / Grok / Mistral / KimiEffort，其余走
`default: return false` —— 于是 `fixReasoningEffort` 对
**GLM / DeepSeek / Ark 三个家族整体空转**。

这三族都在 OpenAI 线上暴露 `reasoning_effort` 字段，且 `reasoncap`
能力表里 `Efforts` 非空（已实测 `Resolve("glm-5.2")` →
`{max,xhigh,high,medium,low,minimal,none}`），完全具备收窄条件。

这违反了 `internal/paramreg/registry.go:206` 自己写下的契约：

> `Note: "枚举各厂商不一致：DeepSeek low|high|max / GLM 7 档 / Grok …。须按目标能力收窄"`

后果：客户端发来的 `reasoning_effort` **原样出站，既不收窄也不报错**。

### 修复

`reasonDialectMatchesParamDialect` 增加 GLM / DeepSeek / Ark 三个 case。
无线上 effort 字段的家族（Anthropic / Gemini / Qwen / MiniMax / Ollama）
**继续不列入**——它们的能力表 `Efforts` 本就为空，`fixReasoningEffort`
开头即早退，不受影响。

### 两处必须一起改

**这是本轮最关键的一点**：若只放宽方言门而不修 `ClampEffort`，
`disabled` 会从"原样透传给上游"变成"被改写成 medium/high"——
**从静默失效变成静默反转，更糟**。两处互为前提，必须同批。

## 4. 实测与验证

### 4.1 变异检验（证明测试能抓住 bug，非"声明式通过"）

| 变异 | 结果 |
|---|---|
| 摘掉 `disableIntentEfforts` 分支 | `reasonnorm` 5 条 + 2 组 FAIL |
| 摘掉 GLM/DeepSeek/Ark 三个 case | `paramguard` 3 条 + 4 组 FAIL |

### 4.2 回归

```
go test ./internal/reasonnorm/... ./internal/paramguard/... \
        ./internal/reasoncap/... ./internal/paramreg/...
→ 全部 ok
```

## 5. 诚实边界：本轮**没有**修掉报障本身

必须写明，避免读者误以为报障已闭环：

- **缺陷二的 live 影响**在本轮落地环境的真机 A/B 中**为 0 条**（已证实），且
  原因是**结构性不可达**而非"巧合无影响"。本机 8782 在本轮验证窗口
  （2026-09-28 17:07–17:11Z，n=12，NEW+OLD 各 6 条）采到的真实候选路径：
  - `glm-5.2` → provider 36（vapeur, `openai-responses`）
  - `deepseek-v4-flash` → provider 33089（sensenova, `openai-completions`）
  两者的 catalog code 均不在 `internal/paramreg/dialect.go:catalogToDialect`
  内，调用 `Resolve(code, protocol)` 回退协议得到 `DialectOpenAIChat` / 不进
  paramguard-effort 路径。D01 playbook R45（2026-09-19）已登记
  `volcengine-coding`/`volcano-normal`/`volcano-tokenplan`/`azure-openai`/
  `google-gemini` 五处 `catalogToDialect` 缺口并明确"逐上游真机验证接受度，
  勿盲登"。本轮新加的三个 case（GLM/DeepSeek/Ark）在方舟同源 code 上仍是
  该立场下的"未来守势"，不构成本轮 live 受益。
  **2026-09-30 更新**：该立场的**守势部分已被推翻**——见 §8，直连方舟取得
  真实接受度证据（Ark 确实暴露 `reasoning_effort`，且按值校验），
  并因此发现能力表本身与上游不符的真缺陷。仍成立的部分只有
  `catalogToDialect` 缺口未补这一点。
- **本轮修的是潜伏缺陷 / 拆雷**，不是性能修复。若上游日后补登
  `code=zhipu/code=deepseek/code=doubao` 等的活跃凭据，方言门立即生效。
  验证手段：见 §7 三态证据表。
- **报障根因是另一条链**：客户端未声明 `tools[]` → auto 选了非工具型
  思考模型 `glm-5.2` → 模型把工具调用当纯文本臆造，响应无 `tool_calls`
  字段 → 客户端无从执行，用户看到"一直在思考"。
  **修复在客户端侧（补 `tools[]`）**，网关的选模逻辑本身是对的
  （补上 `tools[]` 后 auto 换模型、8.8s 返回规范 tool_calls）。
- **另一条 live 缺陷（未修，另立）**：`glm-5.2` 思考预算默认开启且开销极大。
  `1+1等于几？` 逐档实测思考占 95~100% 输出 token（n=15，本轮实测见 §7
  表 2）；真实边界是 `max_tokens < 本次思考实际消耗`（约 100~200 token），
  **非确定性"128 必空"**：64 档 3/3 空、128 档 1/3 空、≥256 档 0/3 空。
  正规开关是 `thinking:{"type":"disabled"}`（实测 reasoning_tokens=0、0.9s），
  而**不是** `reasoning_effort`。`reasoning_effort:"disabled"` 在 GLM/方舟上
  **不是关闭**，是档位调节器——方舟的真正关闭通道是 `thinking{type}`，已在
  `internal/paramguard/reason_effort_dialect_test.go:TestArk_EffortIsTierNotKillSwitch`
  守门。

## 6. 两次被实测推翻的错误归因（留档）

1. **`reasoning_effort:"disabled"` 导致 503** → 复跑 3 次全 200。真实原因是
   上游常态过载。**单次 503 不足以支撑归因。**
2. **`reasoning_effort:"disabled"` 导致 120s 挂起** → 间隔 6s 复跑 n=3
   全 200（5s/19s/4s），无挂起。同上，为上游抖动。
3. **`max_tokens=512` 带 `reasoning_effort` 大面积失败** → 连发 48 次触发限流，
   0.0s 快速失败是限流而非参数效应。**压测必须控制请求速率。**

判「参数有效/无效」需满足：多次采样 + 控制间隔 + 有明确对照组。
单样本落在噪声带里得出的"无效应"结论同样不可信（首轮即犯过）。

## 7. 真机 A/B 与边界实测（2026-09-29 旁挂实例复审）

构建链：`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/gateway`，
增量镜像 `kx-llm-gateway-local:reasonfix-20260929`（vcs.revision `0d3515ffa`，
vcs.time 2026-09-28T15:54:28Z）。对照组 8782 镜像 `2.5.6.2308`，构建时间
2026-09-28 23:04:29（CST），**早于**修复提交 `29d326f10`（23:54:18）。
8782 在 01:13:57Z 被另一流程升级到 `2.5.6.2314`，本表 NEW/OLD 12 条均采集
于 17:07–17:11Z，**早于**该升级，对照组有效性成立。

| 维度 | 数据 |
|---|---|
| NEW 侧 12 请求无一条 paramledger 记录 | TTL 15min，请求后立即 GET，**非过期所致** |
| OLD 侧 12 请求同样无记录 | 修复前 `disabled` 在方言门外透传，paramguard 未触发 effort 路径 |
| 新增 3 case 在本部署不可达 | 走的是 sensenova(openai-completions) 与 vapeur(openai-responses) |
| `glm-5.2` 实际落点 | provider 36（vapeur），catalog code 不在 `catalogToDialect` |
| `deepseek-v4-flash` 实际落点 | provider 33089（sensenova），同上 |
| `code∈{zhipu,glm,bigmodel,zai,deepseek,doubao,volcengine,volcano,ark}` 活跃凭据数 | 0（deepseek/doubao），6（zhipu 但走 anthropic-messages，不经 paramguard） |

表 2：`glm-5.2`「1+1等于几？」逐档 max_tokens 实测（n=3，间隔 5s）

| max_tokens | finish | content_len | 空回复率 |
|---|---|---|---|
| 64 | length (3/3) | 0 | **3/3** |
| 128 | length 1/3, stop 2/3 | 0 或 7 | **1/3** |
| 256 | stop (3/3) | 7 | 0/3 |
| 512 | stop (3/3) | 7 | 0/3 |
| 1024 | stop (3/3) | 7 | 0/3 |

每档 `reasoning_content` 均 100~300 字符。**真实边界**：max_tokens 小于
本次思考实际消耗（约 100~200 token），不在确定性 128 位置。

表 3：三态归档

| 结论 | 状态 |
|---|---|
| 两处修复无回归，全仓测试通过（除 2 个既存环境 FAIL） | **已证实** |
| 本部署下三个新 case 结构性不可达，live 影响恒为零 | **已证实** |
| 「max_tokens=128 必空回复」 | **已推翻**（1/3 命中，真边界在 ~100–200 token） |
| 「64 档必空、≥256 档不空」 | **已证实**（3/3 与 0/3） |
| 修复在真机上有效 | **未证实**（未触达，且无阳性对照） |
| Ark 在线暴露 reasoning_effort | **已证实（2026-09-30 补测推翻前一轮"无法探针"的保留）**，见 §8 |
| 报障根因仍是客户端未声明 `tools[]` | 维持原判，本轮未触及 |

## 8. 直连方舟真机取证（2026-09-30，纠正前一轮的错误归因）

### 8.1 先说被推翻的结论

前一轮（旁挂实例复审）曾据**一次** `upstream_status=400` + reqprobe 日志
`stripped="reasoning_effort"`，判定「方舟不接受 `reasoning_effort` 字段」，
并据此建议**回退** `reasoncap.DialectArk → paramreg.DialectArk` 这个 case。

**该归因错误，本轮已推翻。** 两点错因：

1. **单样本归因**。上游 400 可能是「值非法」而不是「字段不支持」，
   两者在响应上不可区分，只有错误消息能区分。
2. **绕过了 reqprobe 就以为是上游原话**。reqprobe 的
   `stripped=reasoning_effort` 是**网关自己的探测结论**（它试剥了参数、
   仍失败，于是归因为该参数），不是上游对「字段是否存在」的表态。

直连方舟后，上游错误消息把真相直接说出来了：

```
400 InvalidParameter
  "The parameter `reasoning_effort` specified in the request are not valid:
   value `disabled` is invalid."
```

字段被**识别并按值校验**。若字段不存在，错误会是
`unknown parameter` / `unrecognized request argument` 一类。**因此 Ark case
必须保留，回退建议作废。**

### 8.2 实验设计

- 端点：`https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions`
- 凭据：provider 34（`volcano-tokenplan`，`catalog_code=volcengine-coding`），
  明文经 `secret.DecryptAESGCM` 在本机解出，**不出本机、不入仓、不入日志**。
  provider 35 凭据实测 `401 API key status is not active`（已失效），
  基线不通的实验全部作废。
- 采样纪律：间隔 **7s**；`none` / `low` 各 **n=3 复验**（确认组），
  其余档 **n=1** 全扫（探索组）。
- 对照组设计：把「合法档位」与「非法值」分两组跑。上一轮的错误正在于
  只跑了非法值（`disabled`）就下"字段不支持"的结论。

### 8.3 表 4：`glm-5-2-260617` 七档全扫（= 网关 `glm-5.2` 的实际出站名）

| effort | HTTP | reasoning_content | 语义 |
|---|---|---|---|
| `none` | 400 `none is not supported by this model` | — | **被拒**（n=3 复验） |
| `minimal` | 400 `none is not supported by this model` | — | **被拒**（方舟把 minimal 归一化为 none） |
| `low` | 200 | **0 字符** | **关闭档**（n=3 复验） |
| `medium` | 200 | 0 字符 | 关闭 |
| `high` | 200 | 0 字符 | 关闭 |
| `xhigh` | 200 | 759 字符 | 真开启 |
| `max` | 200 | 937 字符 | 真开启 |

对照组（同端点同模型）：`kimi-k2-thinking-251104` 对
`minimal`/`low`/`high`/`disabled`/`zzz_bogus` **全部 200**（12/12），
说明方舟是「收下但按模型能力处理」，不是「拒绝字段」。

### 8.4 本轮据此修的缺陷（真实、可复现）

`internal/reasoncap/reasoning_defaults.go` 的 `glm-5` 条目原声明 7 档
`{max,xhigh,high,medium,low,minimal,none}`。表 4 证明 `none`/`minimal`
被上游拒绝，而 `low` 才是关闭档。

**危害链**：`ClampEffort` 的 `cheapestEffort` 取能力表最低档 →
关闭类写法（`disabled`/`off`/`none`/`no`/`false`）被改写成 `none` → 上游 400。
即：**修复前是"原样透传给上游被拒"（网关没动手，责任在上游），
修复后是"网关主动改写成一个自己能力表宣称合法、实则被拒的值"（责任转移到
网关）**。而且 paramledger 会把它记成一次合法的 `clamp` 收窄，
调用方从账本上看不出任何异常。

这是 §3「两处互为前提」的第三层：**放宽方言门之前，能力表本身得先跟上游
对齐**，否则修复会把上游的拒绝搬进网关内部。

修法：`glm-5` 档位改为 `{max,xhigh,high,medium,low}`。
`glm-4.5` / `glm-z1` **保留原 7 档**——真机只验到 `glm-5-2-260617`，
凭同族推断删档等于把未验证的判断写成事实。智谱原生端点
（`api/paas/v4`）在方舟凭据下 404，无法交叉验证。

### 8.5 守门与变异验证

新增 `internal/paramguard/glm_effort_upstream_contract_test.go`（3 条），
改 `internal/paramguard/reason_effort_dialect_test.go`（2 条改期望 + 1 条新增）。

| 变异 | 结果 |
|---|---|
| 把 `none`/`minimal` 加回 `glm-5` 能力表 | 新门 **FAIL**（出站 `none`，被真机证实会 400） |
| 摘掉 `DialectGLM` 方言门（使 `none` 原样透传） | **FAIL**（`none`/`minimal` 不得原样透传 + 缺 clamp 报告） |
| 两处还原 | 全绿 |

### 8.6 本轮未闭合的边界

- **`catalogToDialect` 缺口仍在**（`volcengine-coding` 未登记）。
  机制已查实：provider 34/35 的 `catalog_code` 是 `volcengine-coding`，
  不在表内 → `Resolve` 回退协议得 `DialectOpenAIChat` → 方言门不开。
  **维持 R45 立场（登记不修 + 逐上游真机验证接受度，勿盲登）**——
  本轮已在方舟侧取得接受度证据，但补登记会影响 thinking 渲染路径，
  超出本轮范围。
- `doubao-pro-thinking`（能力表里 Ark 的唯一条目）**在本环境不存在**：
  provider 34/35 的模型清单里只有 `doubao-1-5-thinking-*` /
  `doubao-seed-1-6-thinking-*` / `kimi-k2-thinking-*`。
  即 `TestArk_EffortIsTierNotKillSwitch` 钉的那条能力表在真机上是**不可达的**。
- GLM 家族的真机覆盖只有 `glm-5-2-260617` 一条路径（方舟 coding 端点）。
  智谱原生 `api/paas/v4` 未能取证。

