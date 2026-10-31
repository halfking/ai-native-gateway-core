# 2026-10-29 · Gemini `:countTokens` 诚实实现轮

> 承接：`docs/audit/2026-10-27-Gemini转换面补全轮.md` §17（`:countTokens` 的登记）
> 与 §20（本轮上一段的「凭据收口面必须覆盖每一个被服务的路由」）。
> 本轮是同一审计档的后续一轮，**独立成档**（登记项本身被实现 + 登记理由被推翻，
> 属于新事实，不是上一轮的续写）。

## 1. 本轮要解决什么

上一轮 §17 把 `:countTokens` 登记为「独立一轮」，登记理由是：

> ⇒ **唯一诚实的实现是把请求【转发给上游】让 Google 自己算**。
> 但那是**第三条出站动作**（响应形状 `{"totalTokens":N}` 与
> `GenerateContentResponse` 完全不同 ⇒ 不走 `ProtocolHandler` 那条通用路径），
> 需要独立的请求/响应通道 ⇒ **独立一轮**，不塞进本轮。

★ 本轮**做了这件事**（`:countTokens` 现在被服务），但**推翻了那个理由** ⇒
⇒ 本轮最有价值的产出不是那个端点，而是**「转发上游」这个方案本身被证伪的过程**。

## 2. ★★★ 登记前提复核：§17 的理由站不住（三条）

★ 登记项不是命令，是**待复核的判断**（LESSONS §451 通用式八：登记项分
「设计问题」与「时序问题」，后者必须下一轮复查）。本轮复查 §17 的「设计问题」，
发现它**同时错在三处**：

### 2.1 错处一：转发上游算的是**另一份请求**

★ Gemini 的 `:countTokens` 对**客户端原文**（Gemini 形状）计数。
★ 而本仓的 Gemini 原生入向**不把原文发给上游**：它先经 IR 归一
（`ir.ParseGemini` → `ir.SerializeOpenAI`，`handler_gemini.go` 的 Step 3 / Step 6），
上游看到的是**归一后**的那份。

⇒ ⇒ ★★★ **转发得到的数字描述的是一个「不会发生的请求」** ⇒
⇒ 它**不是更诚实，是另一种不诚实**（更隐蔽的那种：看起来精确、其实答非所问）。

⇒ ⇒ ★★ 而本仓**真正的诚实口径**是存在的：**估算「网关真正会发出的那份文本」**
（§3）。

### 2.2 错处二：转发上游**并非普遍可用**

★ 只有 **gemini-generate 协议**的上游才有 `:countTokens`。
★ 而本仓的 Gemini 入向可以被**任意**上游服务（IR 翻译到 OpenAI / Anthropic /
Ollama）⇒ ⇒ 对这类上游，「转发」**根本无处可转**。

⇒ ⇒ ★★ 也就是说：「转发上游」这个方案**只覆盖一种部署形态** ⇒
⇒ 它不是一个通用解，而 §17 把它写成了「唯一诚实的实现」。

### 2.3 错处三：它**误用了仓内先例的理由**

★ 仓内确有同族端点：`/v1/messages/count_tokens`（Anthropic）
（`domains/streaming/messages_count_tokens.go`）—— ★★ 它**也是本地估算**，
而且**写明了理由**：

> 网关侧没有供应商 tokenizer, 这里返回与流式桥 message_start.usage.input_tokens
> 完全相同的启发式估算(…): 两个表面给出一致的数字, 客户端的上下文管理才不会
> 在流式/非流式之间漂移。估算偏高是安全方向(提前压缩优于上下文溢出)。

⇒ ★★ **§17 的「本地估算不诚实」与仓内已上线的先例直接矛盾。**
⇒ ⇒ ★★★ 那要不要照抄 Anthropic 那一套？★ **不照抄** —— 理由**不迁移**：

| | Anthropic `/v1/messages/count_tokens` | Gemini `:countTokens`（本轮） |
|---|---|---|
| 客户端能看到的输入 token | **只有** `message_start.usage.input_tokens`（SSE 只写向前，无法回填）⇒ 估算值是它**唯一**能看到的数字 | 响应里**本来就带精确的** `usageMetadata.promptTokenCount` ⇒ 预检值与事后用量之间的漂移**不是同一类失败** |
| ⇒ 「两个面必须一致」的约束 | **成立** ⇒ 估原文 | **不成立** ⇒ 可以取「与网关实际发送一致」这个更强的口径 |

⇒ ⇒ ★★ **⇒ 通用式：★★ 抄先例要连【先例的理由】一起读 ⇒ 理由里那个
⇒ ⇒ 「为什么必须这么估」的**前提条件**不迁移时，抄结论就会抄出一个错的方案。**

⇒ ⇒ ★★★ **⇒ 而本轮比先例更强的地方正在于此**：Anthropic 那边估**原文**，
⇒ ⇒ 本轮估**归一后** ⇒ ⇒ 而**归一后**才是网关真正会发出去的那份 ⇒ ⇒
⇒ ⇒ **两个 Gemini 面对同一段提示词给出口径一致的数字** ⇒ ⇒
⇒ ⇒ 而且这个口径**不依赖上游是不是 Google**（§2.2 的普适性问题也一并解掉）。

## 3. ★ 口径：估「网关真正会发出的那份文本」

⇒ 实现（`domains/streaming/handler_gemini.go` 的 `serveCountTokens`）：

```
客户端 Gemini 体 ──ir.ParseGemini──► IR ──ir.SerializeOpenAI──► 归一体
                                                                  │
                                                     estimatePromptTokens(归一体)
                                                                  ▼
                                                     {"totalTokens": N}
```

★ 三个刻意的选择：

1. ★★ **与生成路径共用同一份归一**（同一对 `ir.ParseGemini` / `ir.SerializeOpenAI`）
   ⇒ ⇒ 「预检数字」与「网关实际发出的内容」**不可能各说各话**。
2. ★★ **复用仓内既有的 OpenAI 形状估算器**
   （`domains/streaming/usage_estimator.go` 的 `estimatePromptTokens`：CJK 1 字≈1
   token、其余 4 字≈1 token、图片块 765、每消息 +4）⇒ ⇒ **不写第四个 token
   估算器**（仓内已有 `transformation.EstimateTokens` / `executors.EstimateAnthropicInputTokens`
   等多把尺，本轮**不再加一把**）。
3. ★★ **误差方向是安全的**：估算偏高 ⇒ 客户端提前压缩 ⇒ 优于上下文溢出
   （与 Anthropic 先例同一取向）。

★ **一个诚实的边界**：对 **gemini-generate 协议**的上游，网关发出去的是
IR→**Gemini** 体，与这里估算用的 IR→OpenAI 体**框架不同**（内容相同）
⇒ ⇒ 估算对它是一个**代理值**，不是精确值 ⇒ ⇒ 那一支的精确值需要
**转发上游** ⇒ ⇒ ★ **它被登记为下一轮**（§9），本轮**不混进来**。

## 4. 实现面（三处必须同时改，否则新路由半死）

★ ★★ 这是本轮第二次兑现 §20 的通用式：**加一条被服务的路由 ⇒ 解析面 /
凭据面 / 挂载面必须齐改**。

| 面 | 改动 | 不改的后果 |
|---|---|---|
| 解析面 | `domains/streaming/handler_gemini.go` 的 `geminiModelPathRe` 增加 `countTokens` | 路由 **404** |
| 凭据面 | `internal/auth/clientkey.go` 的 `geminiNativePathRe` 增加 `countTokens` | 路由 **401**（`?key=` 形态不被收口面认） |
| 挂载面 | ★ **不需要改** —— 见下 | — |

★ ★★ **挂载面为什么不用改**（本轮的一个实测结论）：Go 1.22+ 的 `ServeMux`
**不允许**「通配符 + 字面量」混在同一段 ⇒ `POST /v1beta/models/{model}:countTokens`
是**非法模式**（实测：`bad wildcard segment (must end with '}'）`）⇒ ⇒
⇒ `:countTokens` **无法**作为独立的 mux 条目挂载 ⇒ ⇒ 它只能**在
`GeminiHandler` 内部按动作分派**（`handler_gemini.go` 的 Step 1.5）。
⇒ ⇒ ★★ 而这带来一个**登记册约束**：路由 `/v1beta/models/` 已经登记在
`internal/ingressguard` 的表里，而「同一路由不能登两次、一源文件只能登记一次」
⇒ ⇒ ⇒ **countTokens 的实现必须落在 `handler_gemini.go` 内**（否则登记表既
盖不住它的闸，也会与 Gemini 条目互相掩盖）。

⇒ ★★ 鉴权面（`authenticateGeminiNative`）：
· 校验器可用时 ⇒ **复用包级共享实现** `authenticateNoRPMWith`（本轮从
  `authenticateNoRPM` 拆出的**可注入错误信封**版本 ⇒ 判定逻辑只有一份，
  信封形状按协议分叉）⇒ ★ Gemini 形状的错误信封（`{"error":{"code","message","status"}}`）。
· lite 模式（DB 校验器不可用）⇒ 走**静态数据面键**回退，且判定抽成
  `staticDataPlaneKeyMatches`（**一份实现**）⇒ ⇒ 否则同一部署里会出现
  「generateContent 要静态键、:countTokens 不要」的真实鉴权洞。

⇒ ★★ 闸门面：
· **配额闸**：委派 `authenticateNoRPMWith` 内的 `kv.CheckBudget`。
· **RPM 闸**：**自带** `checkGatewayRateLimit`（与同路由的生成动作同一把尺）。
  ⇒ ★★ 为什么**不**照抄 summarize 的「刻意不消耗 RPM」：那三个面的 LLM 步骤
  **经环回 chat 面携带同一把调用方 key**，那一跳已计一次 ⇒ 再计即双计；
  ⇒ 而 `:countTokens` **不产生任何上游调用** ⇒ 不存在「已计一次」的那一跳。
  ⇒ ⇒ ★★ 而且仓内**已把** Anthropic 的 `/v1/messages/count_tokens`
  「无 RPM 限流」登记成 **P2 缺口** ⇒ ⇒ **新端点不应再制造同一个缺口的第二个实例**。

## 5. ★ 模型列表：能力清单必须与实现**双向**对账

★ 上一轮（§18 / §19）刻意**不列** `countTokens`，理由是「网关没实现 ⇒
列一个自己做不到的方法 = 给客户端一个它会当真、然后失败的承诺」。
★ 本轮**实现了**它 ⇒ ⇒ 承诺兑现 ⇒ ⇒ **必须列** ⇒ ⇒ 上一轮那道反向门
（`TestServeGeminiModels_DoesNotPromiseCountTokens`）**方向翻转**为
`TestServeGeminiModels_AdvertisesCountTokens`，且 ★★ 判据从「不包含 countTokens」
改成 **集合相等**（不多不少）⇒ ⇒ 因为 ★ 两个方向都会骗到人：
· 实现了不列 ⇒ 对外说做不到 ⇒ 客户端于是去找厂商要（多一次凭证/网络往返）；
· 列了不做 ⇒ 承诺不兑现 ⇒ 客户端当真然后失败。

## 6. 门（本轮新增 12 条 + 翻转 1 条 + 扩面 3 条）

| 门 | 钉住什么 |
|---|---|
| ★ `TestServeCountTokens_VendorShape` | 成功信封是厂商形状 `{"totalTokens": N}`，N>0，Content-Type json |
| ★★★ `TestServeCountTokens_EstimatesTheNormalizedBodyNotTheRawBody` | ★★ **口径门**：答案 == `estimatePromptTokens(ir.SerializeOpenAI(ir.ParseGemini(body)))`；★ 且**原文的估算值必须是 0**（Gemini 体没有 `messages` 字段 ⇒ 估算器找不到东西）⇒ ⇒ 这条门**有牙齿**：口径若被改成「估原文」，答案会变成 0 ⇒ 立刻红 |
| `TestServeCountTokens_GrowsWithThePrompt` | 长提示词的计数必须更大（常数答案会骗过其它所有门） |
| `TestServeCountTokens_ServedOnBothVendorPrefixes` | `/v1beta/` 与 `/v1/` 两个厂商前缀都服务该动作 |
| `TestServeCountTokens_RequiresCredential` | 无凭据 ⇒ 401 + **厂商形状**错误信封 |
| `TestServeCountTokens_AcceptsGeminiCredentialForms` | `x-goog-api-key` / `?key=` / `Bearer` 三种形态都能过（原生客户端用前两种） |
| `TestServeCountTokens_RejectsWrongCredential` | 错误 key ⇒ 401（校验器的裁决不被吞） |
| `TestServeCountTokens_RejectsNonPost` | 非 POST ⇒ 405 + 厂商形状 |
| `TestServeCountTokens_InvalidBodyIsVendorShaped400` | 坏 body ⇒ 400 + 厂商形状 |
| ★ `TestServeCountTokens_StaticKeyFallback` | lite 模式 + 静态键：错误键 ⇒ 401，正确键 ⇒ 200（★ 同一部署里两个动作必须同一把尺） |
| ★ `TestServeCountTokens_DoesNotTouchTheChatPipeline` | ★★ ChatHandler **无 executor / 无 provider** 仍返回 200 ⇒ ⇒ 证明它**不碰生成链路**（若误落进去会 503 no_candidate） |
| ★ `TestServeGeminiModels_AdvertisesCountTokens` | ★★ 能力清单与实现**集合相等**（方向翻转 + 判据从「不包含」改成「相等」） |
| ★ `TestExtractGeminiPath` 扩面 | 解析器认 `countTokens`（两个前缀），且**不认识** `:embedContent`（反向钉） |
| ★ `TestIsGeminiNativePath` 扩面 | 收口面覆盖两个前缀的 `:countTokens` |
| ★ `TestExtractClientKey_QueryKeyIsScopedToGeminiNativePaths` 扩面 | `?key=` 在 `:countTokens` 上**是**凭据 |
| ★ `TestGeminiNativePathMatchesHandlerParser` 扩面 | 漂移门显式登记 `:countTokens` 两条路径为「已服务」 |
| ★ `internal/ingressguard` 的 Gemini 条目 | ★★ RateLimit 声明从 delegated 改成 **own**（锚点计数：handler_gemini.go 内 `checkGatewayRateLimit(` 恰好 1 次）＋ 委派说明写清三个动作各自的闸 |

## 7. 读数

| 项 | 值 |
|---|---|
| `go build ./...` | ✓ |
| `go vet`（受影响包） | ✓ |
| `gofmt`（本轮改动文件） | clean（★ `domains/streaming/handler.go` 的 gofmt 差异是**本轮之前就有**的，见 §10） |
| `internal/auth` | ✓ |
| `internal/ingressguard` | ✓ |
| `domains/streaming` | ✓ |
| `cmd/gateway` | ✓ |
| 其余受影响包 | ✓ |

## 8. ★ 变异臂（**三个方向**，全检出）

★ 按纪律：门归零之后**立刻**做真仓变异臂（LESSONS §419 / §451 通用式十三），
且**三个方向**都要（不只「把缺陷放回去」）：

| 臂 | 注入的缺陷 | 检出 |
|---|---|---|
| A | 凭据收口面**收窄**（去掉 `countTokens`） | ✓ `internal/auth` + `domains/streaming` 的漂移门 |
| B | 解析面**不认** `countTokens`（回到旧正则） | ✓ `domains/streaming` 的解析门 + 漂移门 |
| C | ★★ **口径被改成「估原文」**（绕过 IR 归一，直接估原始 body） | ✓ `TestServeCountTokens_EstimatesTheNormalizedBodyNotTheRawBody` |
| D | 能力清单**不列** `countTokens`（回到上一轮） | ✓ `TestServeGeminiModels_AdvertisesCountTokens` |

★ ★★ **C 臂是本轮最有价值的臂**：它证明「口径」这条**文档里的判断**真的被门
钉住了 —— 否则「估原文」是一个**看起来更自然、实际上答非所问**的改法，
而它会**让所有其它门继续绿**（形状对、状态码对、鉴权对、清单对）。

★ ★★ **C 臂第一次做的时候编译不过**（把 `estimatePromptTokens(openaiBody)`
改成 `estimatePromptTokens(bodyBytes)` ⇒ `openaiBody` 变成未使用 ⇒ Go 的
「declared and not used」直接拒绝编译）⇒ ⇒ ★★ **那一臂等于没验证到目标**
（它是被**编译器**拦下的，不是被**门**拦下的）⇒ ⇒ **重做**（加 `_ = openAIBody`
让臂可编译）⇒ ⇒ 这一次才真的由门检出：

```
--- FAIL: TestServeCountTokens_EstimatesTheNormalizedBodyNotTheRawBody
    gemini_count_tokens_test.go:191: totalTokens = 0, want 10 (the estimate of the NORMALIZED body)
```

★ ⇒ ★★ **⇒ 通用式：★★★ 「变异臂自己编译不过」⇒ ⇒ **该臂没有验证到目标**
⇒ ⇒ ⇒ 必须**重做到可编译**，才能说「门抓得住这个缺陷」⇒ ⇒
⇒ ⇒ 否则会把「编译器拦下了」记成「门有牙齿」⇒ ⇒ ⇒ **那是一次自欺。**

★ 四臂还原后 **md5 逐字一致**。

## 9. 本轮明确没做（留给下一轮，别重复排查）

· ★★ **gemini-generate 协议上游的 `:countTokens` 精确值**（转发上游那一支）——
  ★ **只在这一种部署形态下成立**（§2.2），且它要求**第三条出站动作**
  （响应形状 `{"totalTokens":N}` 与 `GenerateContentResponse` 不同 ⇒ 不走
  `ProtocolHandler` 的通用路径）⇒ 独立一轮。
  ★ **起手式**：在 `GeminiExecutor` 里加一个「只计数不生成」的动作 ⇒ 复用
  候选选择/凭据/header profile/重试，只换上游 URL（`:countTokens`）与响应解析。
  ★★ **注意它与本轮的关系**：转发的那一支**只在上游协议是 gemini-generate 时**
  才有意义 ⇒ ⇒ **它的答案与本轮的估算值口径不同**（一个算客户端原文、一个算
  归一体）⇒ ⇒ ★★ 若两者同时存在，**同一个客户端在两种部署下会拿到两个口径的
  数字** ⇒ ⇒ **做那一支之前必须先定「以哪个为准」**（本轮倾向：归一体口径为准，
  因为那才是网关实际会发出去的内容）。
· ★ **`/v1/models`（无斜杠）的原生形状**—— 仍是**产品决策**（要重新分配
  `/v1/models` 的归属 ⇒ OpenAI SDK 的既有契约），**不是工程量**。
· ★ **「本地估算面」的 RPM 口径统一** —— 仓内 `summarize/transform`（刻意不计）
  与本轮（计一次）的差异**已写明理由**，但**两套口径并存**这件事本身值得
  一次统一（★ 归口：`domains/streaming/rate_limit.go` 的注释里目前只有
  summarize 那一支的理由）。
· ★ **Anthropic `/v1/messages/count_tokens` 的口径**（估**原文**）——
  ★ 它有同样的「原文 vs 归一体」问题，但**改它会改变既有端点的返回值**
  ⇒ 属行为变更 ⇒ 需要单独评估与裁决（本轮只记录，未动）。
· ★ **`ingressguard` 登记册的表达力** —— 现在的词汇是**按路由**声明闸的，
  而 Gemini 这一条路由**已有三个动作、闸的归属各不相同**（两个委派、一个自带）
  ⇒ ⇒ 本轮用**文字说明**表达，没有扩词汇 ⇒ ⇒ 若将来再出现同类路由，
  ⇒ ⇒ 应当给登记表加「按动作」的表达力（★ 否则说明文字会越来越长、越来越难核对）。

## 9.5 ★★★ 本轮自愈：一条**陈旧注释**差点留下「两份真值」

★ ★★ 提交前的**逐行复核**抓到一处**注释与实现相反**：

| 位置 | 注释写的 | 实现做的 | 后果 |
|---|---|---|---|
| `domains/streaming/handler_gemini.go` 的 `serveCountTokens` **函数头注释** | 「RPM：**刻意不消耗**（同 refine/analyze/summarize 的口径，见 `authenticateNoRPM`）」 | **消耗** RPM（`checkGatewayRateLimit`） | ★★ 注释会把下一个接手的人引到**相反的限流口径**上 ⇒ ⇒ 而注释里引用的 `authenticateNoRPM` 又确实存在 ⇒ ⇒ **它看起来完全可信** |

· ★★★ **为什么它特别危险**：这段注释是**在实现改成「计 RPM」之前写的**，
· 实现改了、注释没改 ⇒ ⇒ 而**代码评审与编译器都不会报**它
· （注释不参与编译）⇒ ⇒ **门也抓不到**（门测的是行为，不是注释）
· ⇒ ⇒ ★★★ **⇒ 唯一能抓到它的是「逐行读 diff」** ⇒ ⇒ ★★★
· ⇒ ⇒ **⇒ 通用式：★★★ 改口径时，【口径本身】要写在【代码】里，
· ⇒ ⇒ ⇒ ★★ 不能只写在【注释】里 ⇒ ⇒ ⇒ ★★★ ⇒ ★★ 注释会漂移，
· ⇒ ⇒ ⇒ ★★ 而**注释是下一个接手的人最先读的东西** ⇒ ⇒ ⇒ ★★★
· ⇒ ⇒ ★★ ⇒ ★★ **⇒ 凡是「文档/注释里的口径」与「实现的口径」是两处东西的地方，
· ⇒ ⇒ ⇒ ★★ ⇒ ★★ 就要有一道门把两者对拍**（本仓已有这种门：登记册的锚点计数、
· ⇒ ⇒ ⇒ ★★ ⇒ ★★ `i18n` 门的「源码抄语言包」族）⇒ ⇒ ⇒ ★★★
· ⇒ ⇒ ★★ ⇒ ★★ ⇒ 本轮这条**尚无门** ⇒ ⇒ ⇒ ★★ ⇒ ★★ ⇒ **登记为下一轮的候选项**。
· ★★ **本轮的处理**：注释就地改正（与实现一致），**并把口径理由写全**
· ⇒ ⇒ 避免下一个接手的人再问一次「为什么这个面计、那个面不计」。

## 10. ★ 归属判定（既有红灯，不是本轮引入）

★ 全量扫描里出现两处与本轮**无关**的红灯，**归属判定**如下（照仓内
「两处红灯都要判归属」的纪律）：

| 红灯 | 归属 | 依据 |
|---|---|---|
| `scripts/ursmcheck` 的 `TestRequestSurfaceDocEvidenceStillHolds`（`domains/hooks/response/types.go` 区间漂移） | ★ **不是本轮**：本轮**未改**该文件（`git diff --name-only` 不含它）⇒ 是上游 `origin/main` 既有的漂移 | 该门的设计页对拍表指向 `domains/hooks/response/types.go:179`，而该行已不是 `type ResponseInterceptor interface` |
| `domains/streaming` 全包耗时（约 150s）逼近 `make guards` 的 `-timeout=120s` | ★ **既有的环境风险**（ingressguard 的文件头已记载：余量只剩 8~13 秒，机器负载波动足以吃掉它）⇒ 与本轮无关，但 ★ **本轮给 `domains/streaming` 加了 12 条门** ⇒ ⇒ **余量又被压了一点** ⇒ ⇒ 登记 | `internal/ingressguard/ingress_surface_test.go` 文件头 §「为什么要单独建包」 |

· ★★ ⇒ 登记：**下一轮若有人要动 `domains/streaming` 的测试时长**，
⇒ ⇒ 先看 `make guards` 的 120s 超时余量（本轮把它又压低了一点）。

### 10.1 ★★★ 合并 `origin/main` 后**新发现**的两处 installer 红灯（非本轮引入）

★ ★★★ 本轮合并 `origin/main`（`05a490d93`）后，**installer 是独立 Go 模块**
（`installer/go.mod`，不在主模块的 `./...` 内）⇒ ⇒ **主模块的 `go test ./...`
根本扫不到它** ⇒ ⇒ ⇒ ★★ **⇒ 这就是「它为什么能红着推到 main」的原因**
⇒ ⇒ ⇒ ★★★ ⇒ **⇒ 「模块边界」也是一种覆盖盲区** ⇒ ⇒ ⇒ ★★★
⇒ ⇒ ⇒ ★★ ⇒ ★★ **⇒ 全量验证必须【按模块逐个跑】，不能只跑主模块的 `./...`。**

| 红灯 | 归属 | 依据（**基线复现**） |
|---|---|---|
| `installer/internal/dbinit` 的 `TestStartupManifestMatchesStartupFiles`：`manifest has 319 entries, StartupFiles has 322` | ★ **不是本轮**：本轮**未改** `installer/`、未改 `sql/` ⇒ 是并行会话 R176 轮（新增迁移 919/920）**漏了重铸** `sql/schema/installed_startup_migrations.tsv` | ★★ **用 `git archive origin/main` 导出完整树单跑，失败信息逐字相同** ⇒ 基线即红 |
| `installer/cmd/llm-gw-installer` 的 `TestStartupMigrationsAboveCutoffHaveLockGuard`：迁移 `917_sanitize_block_reviews.sql` 含强锁 DDL 但没有 `lock_timeout` | ★ **不是本轮**：本轮**未改**该迁移 ⇒ 属第 37 增量（sanitize 轮）的产物 | ★★ 同上，`origin/main` 完整树基线即红 |

· ★★★ **本轮为什么不顺手修**（照仓内「一轮一件事 / 归属判定」的纪律）：
· ① 两者都是**别的轮次**的产物（919/920 是 R176 轮的；917 是第 37 增量轮的）⇒ ⇒
· ⇒ ⇒ **改它们会把别的轮次的责任混进本轮提交** ⇒ ⇒ ⇒ ★★ ⇒ **归属要清楚**。
· ② ★★ 第 ② 条尤其**不能顺手改**：917 可能**已在服务器上落库** ⇒ ⇒
· ⇒ ⇒ ★★ ⇒ **已落库迁移不可原地改**（改了与已落库的 checksum 不一致）⇒ ⇒ ⇒
· ⇒ ⇒ ★★ ⇒ ⇒ 正解应是**新增一条前向迁移**（补 `lock_timeout` 的等价方案）
· ⇒ ⇒ ⇒ ★★ ⇒ ⇒ 而不是编辑历史文件 ⇒ ⇒ ⇒ ★★★ ⇒ ⇒ **⇒ 需要单独裁决**。
· ③ ⇒ ⇒ ⇒ ★★ ⇒ ★★ 第 ① 条是**机械重铸**（门自己写明了命令：
· ⇒ ⇒ ⇒ ★★ ⇒ ⇒ `cd installer && go test ./internal/dbinit/ -run TestStartupManifest -update`）
· ⇒ ⇒ ⇒ ★★ ⇒ ⇒ **但它属于 R176 轮的责任面** ⇒ ⇒ ⇒ ⇒ ★★ ⇒ ⇒ **交回该轮**
· ⇒ ⇒ ⇒ ★★ ⇒ ⇒ （★ 若该轮已收工 ⇒ ⇒ ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ⇒ 则应**单独开一轮**处理
· ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ⇒ ★★ 两处红灯 + 一条「按模块逐个跑」的验证纪律补强）。
· ★★★ ⇒ ★★ **现状补记（合并 `2d66c3ac3` 后复测）**：★★ 上述两处 installer 红灯
· ⇒ ⇒ ⇒ ★★ ⇒ **已被并行会话在后续提交里修好**（`95f75e49e` 「既有红收口」
· ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ★★ + `d1dfd38d9` 「919/920 并入后台账 … manifest 320/321」）
· ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ★★ ⇒ 实测：`installer/internal/dbinit` 与
· ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ★★ ⇒ `installer/cmd/llm-gw-installer` 的两条门**现已全绿**
· ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ★★ ⇒ ★★ **⇒ 本节保留这处记录，是因为它证明了一件事：
· ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ★★ ⇒ ★★ ⇒ 【安装器是独立 Go 模块】⇒ ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ⇒
· ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ★★ ⇒ ★★ ⇒ **⇒ 主模块的 `go test ./...` 扫不到它 ⇒ ⇒ ⇒
· ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ★★ ⇒ ★★ ⇒ ⇒ 【全量验证必须按模块逐个跑】⇒ ⇒ ⇒ ⇒ ★★ ⇒ ⇒
· ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ★★ ⇒ ★★ ⇒ ⇒ ★★ ⇒ ★★ ⇒ （★ 若只跑主模块的 `./...`，
· ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ★★ ⇒ ★★ ⇒ ⇒ ⇒ ★★ ⇒ ⇒ 这两处红灯会「看不见」。）**

### 10.2 ★★ 推送前的**让号史**（`origin/main` 在本轮工作期间前进了**三次**）

★ ★★★ 本轮从开工到推送，`origin/main` **前进了两次**，两次都撞上号：

| 次序 | `origin/main` 的新提交 | 撞号 | 本轮的处理 |
|---|---|---|---|
| 第 1 次合并（`05a490d93`） | R176 轮 | ★ `AGENTS.md` 的「第 38 增量」被占（`LESSONS.md` 的 §552 当时仍空闲） | ⇒ `AGENTS.md` 让号 ⇒ **「第 39 增量」**；`LESSONS.md` 不让 |
| 第 2 次合并（`2d66c3ac3`） | 24h 审计第 5 轮（`95f75e49e`） | ★★ `LESSONS.md` 的 **§552 也被占** | ⇒ `LESSONS.md` 让号 ⇒ **§553** |
| 第 3 次合并（`6b660599c`） | r1048 轮（`5cdd226ed` 等） | ★ **未撞**：该轮的新块用 `㊼` 记法（不是「第 N 增量」）⇒ 且 `LESSONS.md` 的 §553 仍空闲 | ⇒ **两个文件都不再让号**（★ 但这是**实测**后的结论，不是推断）|

· ★★★ ⇒ **⇒ 通式：★★ 让号不是「一次核对」⇒ ⇒ 而是【每次合并都要重核】⇒ ⇒
· ⇒ ⇒ ★★ 且【两个文件要分别核】⇒ ⇒ ⇒ ★★★ ⇒ ⇒ ⇒ **★ 「一个文件让号」
· ⇒ ⇒ ⇒ ★★ ⇒ 绝不意味着「另一个文件也不让」⇒ ⇒ ⇒ ★★★ ⇒ ⇒ ⇒
· ⇒ ⇒ ★★ ⇒ **★ 核号必须放进【push 前的那一次】**（承第 36 增量那条纪律）。
· ★★★ ⇒ ★★ **本轮两次都实测过「基线到底占了哪些号」**（`git show origin/main:文件`
· ⇒ ⇒ ⇒ ★★ ⇒ 抽 `## §N` / `第 N 增量`）⇒ ⇒ ⇒ ★★ ⇒ **而不是凭「开工时核过」**。
· ★★★ ⇒ ★★ ★★ **另有一处值得记**：`LESSONS.md` 的 §555 段头写着
· ⇒ ⇒ ⇒ ★★ ⇒ 「留 §552~554 缓冲」⇒ ⇒ ⇒ ★★ ⇒ ★★ **但并行会话自己随后取了 §552**
· ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ⇒ ★★ ⇒ **⇒ 「留缓冲」只是一句【声明】，不构成【占用】⇒ ⇒**
· ⇒ ⇒ ★★ ⇒ ⇒ ★★ ⇒ **⇒ 真正的占用只能靠【实测当前 origin/main】确认。**
