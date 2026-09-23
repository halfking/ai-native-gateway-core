# R59 48h 审计轮 — S3 组：协议适配 (D02) 审计报告

- 审计员：S3（协议适配：多上游厂商 × 多下游客户端协议、流式/非流式双向）
- 日期：2026-09-23
- 仓库：llm-gateway-go-2 @ main `9f7b0ea3f`
- 审计对象：48h 内协议域三笔改动 `588e3e965`（写边界归一 + doResponsesProbe）、`8a0bd5c95`（协议命名收口至 provider/catalog + 按模型家族推荐）、`634edc2e6`（/v1/messages/count_tokens + 非流式 usage 兜底），以及 D02 协议矩阵与 usage 双向完整性（含 `8cc027eff` D4 对称化落实面）
- 验证：`go build ./...` 通过；`go test ./provider/catalog/... ./provider/... ./internal/paramreg/... ./admin/ ./domains/transformation/...` 全绿；`go test ./domains/streaming -run 'TestUsageVariants|Variant|TestCountTokens|TestDoResponsesProbe|TestNonStream'` 全绿（含 D4 双向推断、vapeur 归一、responses-probe wire 形状回归）

---

## 一、发现清单（按严重度）

### F1（P1，写边界漏网）updateProvider PATCH 写 providers.protocol 完全没有归一/校验
- 证据：`admin/providers.go:1036-1040` —— `if req.Protocol != nil { h.db.Exec(ctx, "UPDATE providers SET protocol = $1 ...", *req.Protocol, id) }`，原始值直落库；`sql/objects/tables/providers.sql:16` `protocol text NOT NULL` 无 CHECK。
- 影响：vapeur 事故的确切拼写 `openai-response` 仍可经 PATCH /api/providers/{id} 落库——`588e3e965` commit message 声称 "handleFreePoolAddKey 与 handleFreePoolRegister 是 the two write paths into providers.protocol"，该断言不成立：updateProvider 是第三条写路径，且是运营最常用的改协议入口。
- 建议：与 createProvider 同样接 `providercatalog.NormalizeProviderProtocol`，非法值 400。

### F2（P1，写边界漏网）free-pool 批量注册与快捷入口绕过归一
- 证据：
  - `admin/routing.go:4952-5058` `handleFreePoolBulkRegister`：`req.Protocol`（:4964）仅做空串默认（:4979-4981），未经归一即塞入 `freeProviderConfig.protocol`（:5004、:5042）；
  - `admin/free_pool_extra.go:966,1147-1150` `handleFreePoolQuickEntry`：同样 `protocol := req.Protocol` 仅默认值，无归一；
  - 两者最终经共享写点 `registerFreeProvider`（`admin/routing.go:5155-5178`）同时 upsert `provider_catalog`（有 CHECK，脏值被拒但 `//nolint:errcheck` 静默吞错，:5161-5167）与 `providers`（无 CHECK，脏值成功落库，:5171-5178）。
- 影响：运营走 bulk-register / quick-entry 传 `protocol: "openai-response"` 时，事故类脏值再次静默持久化；provider_catalog 的 CHECK 报错也被吞，UI 无感知。
- 建议：把归一下沉到 `registerFreeProvider`（或 freeProviderConfig 构造处）单点收口，而不是各 handler 各自为政；registerFreeProvider 内 provider_catalog upsert 的错误至少 slog.Warn。

### F3（P2，半成品）RecommendedProtocolForModel 零生产调用——"按模型家族推荐出站协议"只在测试里活
- 证据：全仓 grep（排除 vendor）`RecommendedProtocolForModel` 仅 `provider/catalog/protocol_normalize.go:108`（定义）+ `protocol_normalize_test.go:65,98,100,103`（测试）。生产唯一推荐入口是 createProvider 未显式指定协议时的 `RecommendedProtocolForBaseURL`（`admin/providers.go:830`）。
- 家族判定表现状（`protocol_normalize.go:108-124`）：gpt-5* / codex* / o1|o3|o4（`isOSeriesModel`，整名或 `oN-` 前缀，:129-136）→ openai-responses；claude* → anthropic-messages；gemini* → gemini-generate；空/其余（DeepSeek/Qwen/GLM/MiniMax/Kimi/Grok/Mistral/LLaMA 等）→ openai-completions + ok=false。未识别家族兜底 openai-completions，方向正确。
- 影响：commit 标题宣称的能力实际只有"按域名推荐"接了线；家族表属"库就绪未接线"（与 S4 报告 F2 同类形态）。无运行时风险，但审计口径上应记半成品。
- 建议：在 createProvider 带 models 场景或 admin UI 模型提示处接线，或明确注释为预留 API。

### F4（P2，读路径缺口）存量脏数据的归一兜底只有出站候选加载一处；admin/bg/discovery 读路径仍拿原始 DB 值自行比较
- 证据：
  - ✅ 已兜底：`provider/client.go:1114` 与 `:1859` 候选加载出口 `NormalizeProviderProtocol`，但 `normErr != nil`（如旧枚举 `azure`/`custom`，不在别名表）时**静默透传原值**（:1854-1858 注释明示由执行器 default 分支兜），无日志；
  - ❌ 未兜底：健康检查 `admin/provider_cred_lifecycle.go:333`（`cred.protocol == "openai-responses"`，cred.protocol 来自 `loadCredentialRowLite` 的 `COALESCE(p.protocol,'')` 原值，`admin/provider_refresh.go:423`）；模型探针 `bg/model_probe.go:915,1334,1627`；发现 `discovery/discovery.go:374`；诊断 `admin/provider_diagnose.go:160,453`；admin 探针默认值 `admin/routing.go:3453` 仍是 `COALESCE(p.protocol,'openai')`（非规范旧枚举）。
- 影响：流量侧已归一（候选层），但**存量脏行（"openai-response" 等）在管理面仍被误判**——vapeur 事故行若未手工修复，运行时可用而健康检查仍按 chat 探测 responses-only relay，事故类误报在 admin UI 延续；`anthropic`/`claude` 拼写的存量行会让 model_probe/discovery 走错分支。无迁移脚本归一存量行。
- 建议：loadCredentialRowLite / model_probe / discovery 的协议读取处统一过 catalog 归一；或加一条 startup 迁移把 providers.protocol 存量脏值归一。

### F5（P2，探针覆盖面）phase-2 探针只有 responses/chat 两分；诊断页无协议分支；探针失败不会 auto-cool（反证项）
- 证据：
  - `admin/provider_cred_lifecycle.go:325-340`：仅 `openai-responses → doResponsesProbe(ResponsesURL)`，**其余全部**（含 anthropic-messages / gemini-generate / ollama-native）走 `doChatProbe(ChatCompletionsURL)` 且只带 `Authorization: Bearer`（`admin/provider_probe.go:168`）；anthropic-messages 上游对仅暴露 /v1/messages + x-api-key 的端点会被误报 401/404；
  - `admin/provider_diagnose.go:160,453`：诊断页恒 `doChatProbe(ChatCompletionsURL)`，无协议分支——responses-only relay 在诊断页仍会收到 chat 探针失败（正是 vapeur 事故的管理面表现，本 commit 未覆盖诊断面）;
  - ✅ 反证（误伤面收敛）：探针失败只置 `health_status='warning'`（`provider_cred_lifecycle.go:352-354,366-371`），不触碰 `availability_state`/quota/熔断；候选路由 SQL 只看 `availability_state`（`provider/client.go:1530`），`health_status` 不参与路由——探针失败不会 auto-cool 误判。
- 建议：探针函数选择表按协议扩展（至少 anthropic-messages → /v1/messages + x-api-key 探针）；诊断页复用 doHealthCheck 的探针选择逻辑。

### F6（P2，contract test 与 runtime 矛盾）contract test 声称 gemini-generate/ollama-native 可路由，dispatch 实际 501/降级
- 证据：`provider/catalog/contract_test.go:21-27` 把五个协议全标 routable，注释称 "gemini-generate / ollama-native 由 IR converter 处理…能路由"；而 `domains/streaming/executors/executor_dispatch.go:847-858` 对 `gemini-generate` 显式返回 `KindUnsupportedFeature` "gemini-generate dispatch not implemented; candidate skipped"（2026-09-09 注释自证无 executor 分支）；`ollama-native` 落 default → `executeOpenAI`，按 openai chat 线格式出站（Ollama 的 /v1/chat/completions 兼容层），native /api/chat 从未被使用。`sql/schema/02-seed.sql` 无 gemini-generate/ollama-native 种子条目（grep 零命中），当前无实际供应商踩中。
- 影响：协议矩阵的"已实现"口径被 contract test 虚假放大；一旦运营给自建条目配 gemini-generate 协议，所有候选被跳过后请求以 no_candidate 告终。
- 建议：contract test 按实际 dispatch 行为分级（routable / rejected-with-unsupported / chat-fallback），或实现 gemini executor 后再标 routable。

### F7（P3，收口不完整）协议字面量收口只覆盖 executor 主分发链，非测试代码仍余 ~163 处裸字面量
- 证据：grep `"anthropic-messages"|"openai-responses"|...`（非测试、非 catalog 包）163 处；其中比较点：`domains/hooks/compression/*`（alignment.go:41、cut_marker.go:361、session_compressor.go:818,986,997,1152、compaction.go:555,652、smart_window.go:330、conversation_text.go:45、diff.go:160）、`domains/streaming/executors/context_summarize.go:362,587,1204,1207,1541`、`domains/transformation/ctx_compress.go:514`、`bg/model_probe.go`、`discovery/discovery.go:374`、`cmd/probe-cred/main.go:105`、`admin/provider_cred_lifecycle.go:333`。
- 影响：值全等（常量 = 字面量），零行为变化（✅ 与 commit 声明一致）；但别名/拼写演进时这些点不随 SSoT 联动，F4 的读路径缺口正源于此。
- 建议：按调用面分批替换；至少 admin/bg/discovery 三个跨包比较点优先。

### F8（P3，误推荐面）RecommendedProtocolForBaseURL 子串匹配可误命中第三方域名
- 证据：`provider/catalog/protocol_normalize.go:146` `strings.Contains(u, "anthropic.com")` 会命中 `myanthropic.com` / `xanthropic.com.community` 等子串；`api.openai.com` 同理（但推荐值与回退相同，无害）。
- 失败表现：createProvider 未显式指定协议时（`admin/providers.go:829-834`）误写 anthropic-messages → 运行时 `executeAnthropic` 把 Anthropic 线格式打向 chat-only relay → 上游 4xx（KindClassified，进入正常 failover，不毒化凭据，但该候选恒不可用且健康检查（chat 探针）与协议（anthropic）信号互相矛盾）。
- 建议：anthropic.com 匹配改 `u == "https://api.anthropic.com"` 前缀或 host 精确匹配。

### F9（P3，行为语义变化，有意但需运营知会）createProvider 协议决策语义变化
- 证据：重构前 `git show 8a0bd5c95^:admin/providers.go`（原 :816-819）——未指定恒 `openai-completions`、未知值静默落库；重构后（`admin/providers.go:821-835`）未指定按 baseURL 推荐、未知值 400。
- 影响：① 旧集成若曾传 `azure`/`custom`（domains/provider 旧枚举，`domains/provider/types.go:21-24`）现在 400，且这两值不在别名表（`protocol_normalize.go:36-67`），存量行在候选层归一也会失败透传（现状执行器 default 按 chat 处理，行为未回归）；② "openai-response" 拼写从"静默按 chat 跑通"变为"归一为 openai-responses 后，若 SupportsNativeResponses 未开则 dispatch 501 failover"（`executor_dispatch.go:833-838` + `executor_chat.go:377-385`）——方向正确（显式暴露错误配置），但对该 relay 上的既有流量是"从能用到报错"的可见变化，需运营把协议改回 openai-completions。

### F10（P3，偏差未标注）count_tokens 是纯本地启发式估算，响应无估算标注
- 证据：`domains/streaming/messages_count_tokens.go:74-77` 直接返回 `{"input_tokens": N}`，N 来自 `executors.EstimateAnthropicInputTokens`（`domains/streaming/executors/input_token_estimate.go:47`），从不代理上游真实 count_tokens；响应体无任何字段/header 标注这是估算。
- 影响：与流式桥 message_start.usage.input_tokens 同源同值（✅ 一致性成立，防上下文漂移的设计目标达成），Claude Code 的压缩决策安全方向（偏高）；但与上游真实计费的偏差只有代码注释，带内不可区分。认证同语义（Bearer 优先 x-api-key 兜底，:44-59）、POST-only 405（:39-42）、413（:69-72）均✅；路由已注册 `cmd/gateway/main.go:6101`（✅）。
- 建议：响应加 `X-Gw-Token-Estimate: true` header（非破坏性），或文档标注。

### F11（P3，兜底无标注 + output 恒 0）非流式 usage 兜底正确但构造路径有盲区
- 证据：`domains/streaming/messages.go:1323-1325,1340-1372` —— `patchAnthropicUsageInput` 仅在 input_tokens 缺失/为 0 时补估算（真值绝不覆盖✅，R51 放宽不再要求 output 为 0✅）；parse/marshal 失败原样返回（✅ 永不劣化主路径）；调用点以客户端原始 Anthropic 体估算（:827）✅。但上游完全无 usage 时构造的 `output_tokens: 0`（:1358-1361）与估算 input 一样无估算标注。
- 建议：与 F10 同一标注机制。

### F12（P3，变体表缺口）usage 变体表与 Responses 真实 wire 不对齐，reasoning/cached 有洞
- 证据：
  - `domains/streaming/usage.go:39-48` `usageCacheReadPaths` 含 `{"input_token_details","cache_read"}`（与任何真实厂商 wire 不匹配——chat 是 `prompt_tokens_details.cached_tokens`✅，Responses 是 `input_tokens_details.cached_tokens`，**后者缺失**）；通用 `ExtractUsageFromChunk` 对 Responses 形 usage 提不出 cached_tokens（native 路径由 `domains/streaming/native_responses_capture.go:53-65` 单独处理✅，泛化表未跟上）；
  - `native_responses_capture.go:53-65` `observeNativeUsage` 只读 input/output/cached，**不读 `output_tokens_details.reasoning_tokens`**——原生 Responses 上游的 reasoning tokens 进不了 capture（计费相邻面缺 reasoning 口径）。
- 建议：变体表补 `{"input_tokens_details","cached_tokens"}`；observeNativeUsage 补 reasoning 槽位。

### F13（P3，静默丢字段）legacy 转换器丢弃 usage 明细，仅 IR 路径保留 cache
- 证据：
  - `domains/streaming/responses.go:1249-1262` `convertChatResponseToResponses`：usage 只搬 prompt/completion/total，`prompt_tokens_details.cached_tokens`、`completion_tokens_details.reasoning_tokens` 丢弃；
  - `domains/streaming/messages.go:1452-1461` `convertChatResponseToAnthropic`：同样只搬 prompt/completion，cached 丢弃（Anthropic 形有 `cache_read_input_tokens` 槽位没用上）；
  - 对比：IR 路径 `internal/ir/response.go:784-787` 与 `internal/ir/stream.go:911-914` 保留 cache 双槽（✅）——同一"chat 上游 × 非 chat 客户端"组合，走 IR（format_conversion.enabled 或 e.IR 接线时）与走 legacy 转换的 usage 明细不一致。
- 建议：两个 legacy 转换器从 usage details 补 cache/reasoning 映射，或统一收敛到 IR。

### F14（P3，静默丢字段）responses_bridge 的 response.completed usage 无明细对象
- 证据：`domains/streaming/responses_bridge.go:419-424` —— 只发 `{"input_tokens","output_tokens","total_tokens"}`；真实 Responses API 有 `input_tokens_details.cached_tokens` / `output_tokens_details.reasoning_tokens`，Responses SDK 客户端读明细恒缺失。
- 建议：桥内已有 CacheRead/Reasoning 槽位数据时补明细对象。

### F15（P3，观测）admin 模型探测 handler 协议默认值仍是旧枚举且恒 chat 探针
- 证据：`admin/routing.go:3453` `COALESCE(p.protocol,'openai')`（'openai' 非规范值，protocol 变量 scan 后实际未参与探针决策，:3516-3521 恒 `ChatCompletionsURL`）。
- 建议：默认值改 catalog 常量并接协议分支（低优先，纯诊断面）。

---

## 二、协议矩阵（D02 核心清单）

入站（5）：`/v1/chat/completions`（`/v1/completions` 同 handler，`cmd/gateway/main.go:6093-6094`）、`/v1/messages`（+ `/v1/messages/count_tokens`，:6095,6101）、`/v1/responses`（:6102）、Gemini 原生 `:generateContent|:streamGenerateContent`（`domains/streaming/handler_gemini.go`，经 IR 转 chat 栈）、`/v1/embeddings`（:6105）。
出站（5）：openai-completions / anthropic-messages / openai-responses（native，`SupportsNativeResponses(Stream)` 能力闸门）/ gemini-generate / ollama-native。
分发点：`executor_dispatch.go:840-860`（anthropic-messages→executeAnthropic、gemini-generate→显式 unsupported、default→executeOpenAI）。

| 入站 ↓ ／ 出站 → | openai-completions | anthropic-messages | openai-responses (native) | gemini-generate | ollama-native |
|---|---|---|---|---|---|
| **chat**（S=流式/N=非流式） | S✅ N✅ 原生直通 | S✅ `AnthropicToOpenAIStream`（main.go:1731） N✅ IR `SerializeOpenAIResponse`（executor_anthropic.go 不适用,此处 executor_chat.go:1657 IR / legacy） | S/N ❌ **501** `native Responses request body is unavailable`（executor_chat.go:393-401：`ResponsesBodyBytes` 仅 /v1/responses 入站设置，responses.go:641）→ 进 failover | S/N ❌ **显式 unsupported**（executor_dispatch.go:848-858）"gemini-generate dispatch not implemented" → candidate skipped→failover，不毒化凭据 | S/N ⚠️ 落 default 按 **chat 线格式**出站（native /api/chat 从不使用；Ollama /v1 兼容层可用） |
| **messages** | S✅ `OpenAIToAnthropicStream`（main.go:1762） N✅ `convertChatResponseToAnthropic` + usage 兜底（messages.go:827,1317-1325）/ IR | S✅ N✅ 直通 | ❌ 同上 501 | ❌ 同上 unsupported | ⚠️ 同上 chat 降级 |
| **responses** | S✅ `OpenAIToResponsesStream`（main.go:1827） N✅ `convertChatResponseToResponses`（responses.go:1193+）/ IR | S✅ `AnthropicToResponsesStream`（main.go:1800，executor_anthropic.go:801-812） N✅ IR `SerializeResponsesResponse`（executor_anthropic.go:242） | S✅ `NativeResponsesStream`（main.go:1824，executor_chat.go:1312） N✅ native 直通（executor_chat.go:1558-1594；body 校验 :1519-1523）。能力未开→501（:377-385） | ❌ 同上 unsupported | ⚠️ 同上 chat 降级 |
| **gemini 原生**（generateContent） | 经 IR→chat 栈，同 chat 行 | 同 chat | ❌ 501 | ❌ unsupported | ⚠️ chat 降级 |
| **embeddings** | ✅ `EmbeddingsURL`（embeddings.go:193） | 跳过候选（embeddings.go:183）✅ 有意 | ⚠️ 不跳过，对 responses-only 候选打 chat 兼容 embeddings 端点，失败后 failover | ⚠️ 同左（不跳过） | ⚠️ chat 兼容 |

矩阵结论：
- **已实现且双向（流式+非流式）**：chat/messages/responses 三入站 × {openai-completions, anthropic-messages} 出站，responses 入站 × openai-responses(native)。
- **显式 501（failover，非静默）**：native-responses 出站 × 非 responses 入站（`ResponsesBodyBytes` 空）；gemini-generate 出站全列。
- **直通不转换**：chat↔openai-completions、messages↔anthropic-messages、responses↔openai-responses(native)。
- **静默降级（最弱格）**：ollama-native → chat 线格式（无任何告警/标记）。
- **TODO/unimplemented 分支登记**：`executor_dispatch.go:852` "gemini-generate dispatch not implemented"（唯一显式 unimplemented）；`executor_chat.go:253` AnthropicExecutor 路径 "Responses API response conversion requires IR converter"（IR 缺位时显式报错，非静默）。未发现"编译通过但运行时 501 未登记"之外的静默 501；两处静默丢字段见 F13/F14（usage 明细维度）。

---

## 三、usage 字段双向完整性（D4 / 8cc027eff 落实面）

**抽取槽位变体表**（`domains/streaming/usage.go:29-48`，Wave4-D4，流式 `ExtractUsageFromChunk` 与非流式 `extractTokensFromResponseBody`（handler.go:7932）共用）：

| 槽位 | 已覆盖 wire 名 | 缺口 |
|---|---|---|
| prompt_tokens | `prompt_tokens`、`input_tokens`（:40） | — |
| completion_tokens | `completion_tokens`、`output_tokens`（:41） | — |
| total_tokens 推断 | 双向 + `total > 已知侧` 守卫：流式 usage.go:165-175（D4 修复✅）、非流式 handler.go:7961-7970（对称✅）；变体测试双向钉桩（usage_variants_test.go，`total fills missing prompt/completion` + `not larger stays untouched` 全 PASS✅） | — |
| cached_tokens | `cache_read_input_tokens`、`cache_read_tokens`、`prompt_tokens_details.cached_tokens`、`input_token_details.cache_read`（:38-43） | ❌ Responses 真 wire `input_tokens_details.cached_tokens` 缺失（F12）；native 路径单独覆盖（native_responses_capture.go:59-61✅） |
| cache_write | `cache_creation_input_tokens`、`cache_write_tokens`、`input_token_details.cache_creation`（:44-48） | — |
| reasoning_tokens | `completion_tokens_details.reasoning_tokens`、`reasoning_tokens`、`reasoning_token_count`（:127-139） | ❌ Responses `output_tokens_details.reasoning_tokens` 缺失（F12） |
| cache miss / provider 专槽 | `prompt_cache_miss_tokens`/`cache_miss_tokens`、`seed_token_usage`（:141-158） | — |

**桥发射面**（上游→客户端方向）：
- anthropic 桥（chat 上游→messages 客户端）：`internal/ir/stream.go:883-936` message_start 带 input/output/cache 双槽（audit-r2 A#2 修复：input+output 两半不再互斥丢弃✅）；legacy 非流式丢 cached（F13）。
- responses 桥：`responses_bridge.go:423` 无 details 明细（F14）。
- openai 直通：原样透传✅。
- 非流式 /v1/messages 兜底：真值保护 + 估算补 0（F11，✅ 实现正确，标注缺失）。

---

## 四、必跑验证结果

| 命令 | 结果 |
|---|---|
| `go build ./...` | ✅ 通过 |
| `go test ./provider/catalog/...` | ✅ ok（含 TestNormalizeProviderProtocol / TestRecommendedProtocolForModel / TestRecommendedProtocolForBaseURL / TestProtocolsMatchExecutors / TestProtocolConstantsAlignWithDBCheck） |
| `go test ./provider/...` | ✅ ok |
| `go test ./internal/paramreg/...` | ✅ ok（protocolToDialect 补录后） |
| `go test ./admin/` | ✅ ok（含 TestDoResponsesProbe_SendsResponsesBodyShape / TestDoResponsesProbe_PropagatesUpstreamErrorBody） |
| `go test ./domains/transformation/...` | ✅ ok |
| `go test ./domains/streaming -run 'TestUsageVariants|Variant|TestCountTokens|TestDoResponsesProbe|TestNonStream'` | ✅ ok |

---

## 五、修复优先级建议

1. **立即（P1）**：F1 updateProvider、F2 bulk-register/quick-entry——把 `NormalizeProviderProtocol` 下沉到 `registerFreeProvider` 与 updateProvider 两点即可覆盖全部写路径。
2. **本迭代（P2）**：F4 读路径归一统一（loadCredentialRowLite/model_probe/discovery + 存量行迁移）；F5 探针协议分支扩展 + 诊断页接线；F3 决定 RecommendedProtocolForModel 接线或标记预留；F6 contract test 口径修正。
3. **排期（P3）**：F7 字面量分批收口；F8 域名精确匹配；F10/F11 估算标注；F12/F13/F14 usage 明细补齐。
