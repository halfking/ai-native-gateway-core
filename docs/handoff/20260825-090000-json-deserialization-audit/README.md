# JSON 编解码与容错恢复审计（已通过 · Phase 0 启动批准）

**日期**：2026-08-25
**批准日期**：2026-08-26
**批准人**：halfking
**批准措辞**：Phase 0 启动批准，2026-08-26 by halfking
**范围**：Go 服务端、HTTP/webhook、LLM/provider 与 SSE 流、回放/持久化/配置，以及 `web/` 前端。
**方式**：只读静态审计 + 定向测试；未覆盖或回滚任何既有工作树修改。

## 结论

仓库的 JSON 使用面很大，且解析策略明显分裂。静态基线检索到约 **2,954** 个 Go JSON API 调用、**177** 个前端 JSON 调用，以及 **1,072** 个包含 JSON 入口特征的文件；其中大量是测试、序列化或内部可控数据。生产路径的关键问题不是 `encoding/json` 本身，而是以下不一致：

1. 部分严格协议入口有大小限制与单值验证，部分 HTTP、webhook、管理 API、provider 响应和 SSE 却没有。
2. 业务模块各自决定“解析失败后忽略、使用零值、原样回退或报错”，导致安全行为和数据质量无法预测。
3. 能接受前后自然语言、Markdown 围栏或截断 JSON 的恢复需求尚未与认证、路由、配置、计费和状态更新等严格边界隔离。
4. `sessionmeta` 已具备较严格的单值解析基线，但直接传入消息/用户文本的资源边界以及解析拒绝诊断仍不完整。

**建议结论：不要全仓机械替换 `encoding/json`。** 应建立严格、恢复、流式三种明确模式，先迁移外部协议和高权限操作入口，再处理回放、持久化和展示层。

## 已验证的积极控制

| 控制 | 证据 | 评价 |
|---|---|---|
| sessionmeta 原始 body 有 1 MiB 上限，并拒绝多个顶层值 | `domains/analysis/sessionmeta/extractor.go:152-186` | 良好；适合作为内存字节输入的单值解析基线。 |
| sessionmeta 将 `developer` 统一为 `system`，并保留 instruction + 最新窗口 | `domains/analysis/sessionmeta/extractor.go:189-225`、`314-349` | 修复了与主 IR 语义不一致和最后 user 被挤出的问题。 |
| DingTalk callback 使用 `MaxBytesReader` | `api/dingtalk_callback.go:71-81` | 良好；应提炼复用。 |
| provider credential 的管理请求同时限制 body 并验证第二次 Decode 为 EOF | `admin/provider_credential.go:635-650` | 当前最接近严格 HTTP JSON 标准的实现。 |
| request-journey 契约使用 unknown-field、EOF 与业务校验 | `domains/requestjourney/contract.go:560-586` | 适合作为版本化内部数据契约的基线。 |
| 主流网关 JSON body 路径已有受限读取 | `domains/streaming/request_meta.go:153-175`、`domains/streaming/handler_gemini.go:185-202`、`domains/streaming/embeddings.go:110-129` | 说明系统已有可推广模式。 |

## 发现

### P0 — 条件性高危：WeChat approval callback 的业务 POST 未见验签

**证据**：`api/webhooks/wechat_callback.go:112-145` 读取并解析 JSON；`161-196` 使用客户端提供的 `action`、`approval_id`、`tenant_id`、`user_id` 调用审批/拒绝。XML 分支同样直接执行操作，见 `api/webhooks/wechat_callback.go:251-284`。

**影响**：若该 legacy handler 被注册到可达路由，攻击者可伪造回调改变审批结果；同时 `io.ReadAll(r.Body)` 无 body 上限。

**注意**：本次未确认该 legacy handler 在当前生产 mux 中是否实际注册。因此应在部署前确认路由状态；未注册时这是高危回归面而非已确认在线漏洞。

**整改**：

- 移除未注册的重复 callback 实现，或使其在注册前强制复用统一认证中间件。
- 对 POST JSON/XML 均要求供应商签名、时间窗、nonce/replay 防护，并在验证失败时拒绝。
- 用 `http.MaxBytesReader` 取代裸 `io.ReadAll`；验签后的业务解析再进入严格 JSON 模式。

### P1 — 已注册 Feishu Bot webhook 的认证和资源保护不完整

**证据**：`domains/feishubot/callback.go:69-126` 对公开 callback 读取完整 body（`75`），解析 JSON（`90`），然后根据可选配置决定验签；异常 JSON 记录完整 `body`（`91`）。路由注册见 `cmd/gateway/feishubot_init.go:105`。

**影响**：无界 chunked 请求可放大内存/日志消耗；如果 `signature_required` 启用但密钥为空、或 verify token 为空/缺失时没有 fail-closed，来源认证可被配置错误削弱。完整 body 日志还可能保存敏感字段或恶意大载荷。

**整改**：

- 在启动和热配置加载时强制：启用签名即必须存在可用验签密钥；缺少 token/签名/时间戳一律拒绝。
- 读取 `limit + 1` 字节并明确返回 413；URL verification 和业务事件分别使用小上限。
- 日志只记录 `body_bytes`、哈希、解析阶段和已转义的短预览，绝不记录完整 body。

### P1 — 共享及分散的管理 API JSON 解码无统一上限，也常接受第二个文档

**证据**：共享 `admin.readJSON` 直接调用一次 `Decode`：`admin/handler.go:1292-1299`。相同模式大量存在于 `api/approval_handler.go:194-196`、`258-260`、`cmd/gateway/plugin_installer_init.go:34-40`、`admin/modules.go:863`、`admin/settings.go:245`、`admin/maas_handlers.go:62` 等。

**影响**：未配置最大 body、最大字符串/集合量以及第二次 `Decode == io.EOF`。例如 `{"valid":true} {"ignored":true}` 可被首个 Decode 接受，造成入口、审计、代理与下游对同一 request body 的解释不一致；超大管理员请求可导致内存/CPU 压力。

**整改**：将此类路径迁移到 `DecodeStrictHTTP`：

- `http.MaxBytesReader` / `limit+1`；
- Decode 首个值后要求 EOF；
- 版本稳定的管理 DTO 可启用 `DisallowUnknownFields`，网关 provider DTO 则保留 unknown-field 兼容策略；
- 将 `io.EOF`、过大、语法、类型和多文档错误映射为不同的公共错误码；
- 按接口分类设定上限（确认操作 4–64 KiB、配置/API 1 MiB、明确的导入接口才允许更高上限）。

### P1 — 若干“可选 body”端点吞掉 JSON 解析失败后继续执行默认动作

**证据**：`domains/session/handler.go:216-225` 创建 session 时忽略 Decode 错误；类似模式见 `cmd/gateway/telemetry_fallback_buffer_handler.go:134-138`、`admin/session_health_api.go:429-455`、`admin/request_trace.go:240-244`、`admin/no_topic_session.go:360-363`。

**影响**：畸形 JSON 不会被区分为客户端错误，而会变成零值/default 管理动作；对 replay、clear、recycle 等具有副作用的接口尤其不安全，也损害审计真实性。

**整改**：只允许空 body 显式走 `io.EOF` 分支；非空却无法解析、含第二文档或超限必须返回 400/413，不能使用零值继续。每个“可选 body”接口要声明其空 body 语义。

### P1 — 核心非流式 provider 响应存在无界读取

**证据**：`domains/streaming/executors/executor_chat.go:132-135` 和 `domains/streaming/executors/executor_anthropic.go:151-154` 直接 `io.ReadAll(resp.Body)`；同类无界外部响应读取还包括 `domains/health/inference_checker.go:138`、`domains/sessionaudit/llm_detector_client.go:107`、`domains/feishubot/callback.go:75`。

**影响**：不可信、故障或被错误配置的 provider 能以巨型成功响应触发内存压力；随后 JSON 解析、字符串替换和响应复制进一步放大峰值内存。超时不等同于大小上限。

**整改**：为每类 provider 非流式响应定义 `maxNonStreamUpstreamBodyBytes`，读取 `limit+1` 字节；超限关闭 body，记录 provider/credential/request 标识但不记录 body，返回受控 502。所有升级、插件和内部服务 client 复用同一受限 response decode helper。

### P1 — sessionmeta 的“bounded”保证可被直接结构化输入绕过

**证据**：`domains/analysis/sessionmeta/extractor.go:114-119` 优先接受 `Input.Messages`；`189-210` 在 `cleanText` 完整扫描后才截断；`domains/analysis/sessionmeta/extractor_parse.go:55-63` 的 `UserText` 未限制。历史 loader 可直接传入完整消息，见 `domains/analysis/workers/session_metadata_close_hook.go:47-55`。

**影响**：`MaxInputBytes` 只保护 `RequestBody`，不能保护直接消息、超大 user text 或历史加载输入。极大字符串会在分词、正则、拼接 corpus 和哈希之前消耗 CPU/内存。

**整改**：在任何 text 规范化前实施每字段字节限制；对 `Input.Messages` 限制消息数、单 message 原始字节数和总字节数；对 `SystemPrompt`、`UserText`、repo path 同样限制。截断时采用 rune-safe 但避免先完整 `strings.Fields`。

### P2 — sessionmeta 把多个拒绝原因静默折叠为“空消息”

**证据**：`domains/analysis/sessionmeta/extractor.go:152-171` 对过大、BOM、前后垃圾、拼接文档、协议混用和 malformed JSON 都返回 `nil`；`136` 因空 corpus 生成相同 hash。结果保存去重见 `domains/analysis/sessionmeta/store.go:87-94`。

**影响**：无法区分“原本无消息”和“解析安全拒绝”；解析异常可能被去重掩盖，导致恢复质量和上游协议问题不可观测。

**整改**：不改变严格拒绝语义，但让 `ParseMessages` 返回结构化诊断（source、stage、reason、input bytes、dropped prefix/suffix、recovered），并在 Result.Provenance 或专用 metrics 中标注。日志不写原始 prompt/body。

### P2 — SSE 行读取缺少每帧上限

**证据**：`domains/streaming/stream.go:1310` 使用 `bufio.Reader.ReadString('\n')`；相似读取位于 `domains/streaming/anthropic_event_reader.go:48` 和 `domains/transformation/anthropic/stream_support.go:265`。

**影响**：上游持续发送无换行内容时，单个 frame 可无限增长。现有 timeout/close 可限制时间，但不限制单位时间内的内存增长。

**整改**：提供 frame-size-bounded SSE reader，超过 `maxSSELineBytes` 后关闭 upstream 并标记 protocol violation；按空行而非仅单行完成 SSE event framing，支持多行 `data:`。

### P2 — JSONB/回放路径存在错误吞掉与静默数据空化

**证据**：`fault/store_pgx.go:22,53,84,147,162,192,198,274`、`center/store_pgx.go:212,243,246,277,287`、`domains/session/v2/turn_logs_writer.go:117,163` 忽略 JSON 编解码错误；`hotconfig/hotconfig.go:103-119` 对损坏 JSON key 直接跳过并用新 map 替换；`domains/sessionforensics/replay.go:183-225` 采用 best-effort 解析但未向报告暴露失败。

**影响**：控制面规则、审计数据、持久化状态或热配置可能悄然变为零值/默认值。对安全配置或路由策略尤其危险。

**整改**：

- 关键配置保留最后一个有效值；错误时告警而不是删除 key；
- 业务状态 JSON decode 要返回错误或带 `degraded` 标识；
- 回放工具可保持 best-effort，但输出/指标必须报告 parse-failure count；
- 对 `json.RawMessage` 写入 JSONB、日志或对外协议前执行 `json.Valid` 和上限验证。

### P2 — 前端 JSON 失败处理不统一；流/展示路径存在静默丢帧或渲染异常

**证据**：通用 API wrapper 的 JSON/文本 fallback 较完整，见 `web/src/api/_core.ts:128-152`；但 live SSE 对非法 envelope 仅 warn 后跳过，见 `web/src/composables/liveStreamStore.ts:1279-1299`；chat SSE 按行消费且 EOF 可被视为正常完成，见 `web/src/composables/useChatCompletions.ts:183-195,345-374`；模板直接解析 metadata 的路径见 `web/src/views/ops/FaultManagementView.vue:347`。

**影响**：截断、半帧、多行 SSE data event 或服务端异常 JSON 可能显示为正常完成/丢失更新；格式损坏的历史 metadata 可能使详情视图渲染失败。

**整改**：建立 `safeParseJson`、`readJsonResponse`、`parseSSE` 三个前端基础组件；保留原始受限文本和分类错误；SSE 强制终止标志（例如 `[DONE]` 或协议终止 event），缺失时标记 incomplete，不能当作成功；展示 JSON 使用 fallback formatter。

## 统一设计

### 1. `internal/jsonx/strict`：严格协议模式

只用于 HTTP API、认证/授权、路由、计费、配置写入、数据库状态更新、webhook 与内部命令。

```go
DecodeHTTP(w, r, dst, Options{MaxBytes, UnknownFieldPolicy, RequireObject})
DecodeBytesStrict(raw, dst, Options{MaxBytes, RequireSingleValue: true})
ValidateRaw(raw, Options{MaxBytes, RequireSingleValue: true})
```

强制能力：

- 输入大小限制，读取 `limit + 1` 后可明确识别超限；
- 单顶层 JSON 值，第二次 decode 必须 `io.EOF`；
- 仅接受合法 JSON 空白，拒绝 BOM、日志前缀、Markdown 围栏、尾部垃圾和拼接文档；
- 可选 `DisallowUnknownFields` 与 `UseNumber`；
- 结构化错误分类，不记录 raw body；
- `RawMessage` 二次使用前再次校验。

### 2. `internal/jsonx/recover`：受限恢复模式

只用于 LLM 输出、工具参数展示/流式组装、日志/telemetry 展示、离线回放分析；**不可用于**严格协议模式中的认证、路由、配置、计费、持久化状态变更。

仅允许以下确定性修复：

1. 去 UTF-8 BOM；
2. 去首尾 Unicode 空白；
3. 去完整 Markdown fenced code block；
4. 在字符串状态机中提取第一个完整 object/array，允许前后自然语言；
5. 截断工具参数只在调用方已明确允许、且恢复后 `json.Valid` 时进行保守闭合。

恢复器必须返回诊断而不是只返回 bytes：`Recovered`、`Reason`、`DroppedPrefixBytes`、`DroppedSuffixBytes`、`Truncated`、`Source`、`InputBytes`。不得拼接两个独立 JSON 值，不得猜测字段类型或修复字符串内非法 escape。

### 3. `internal/jsonx/stream`：流式模式

按 SSE event（空行）或协议帧边界处理，而不是随意把每一行当 JSON：

- 最大 line、event、累计 stream bytes；
- 按 event 聚合多个 `data:`；
- 支持 `[DONE]` / 显式结束状态；
- EOF 前缺失结束标志为 `incomplete`；
- 逐 frame 统计 parse/recovery/error，不在日志记录 payload。

## 错误分类与可观测性

建议统一 code：`empty`、`too_large`、`invalid_utf8`、`bom`、`syntax`、`type_mismatch`、`trailing_data`、`multiple_documents`、`unknown_field`、`depth_limit`、`collection_limit`、`truncated`、`recovery_refused`、`provider_protocol_violation`。

指标：

- `json_decode_total{mode,source,result,reason}`
- `json_recovery_total{source,reason}`
- `json_recovery_dropped_bytes{source,side}`
- `json_rejected_total{source,reason}`
- `json_stream_frame_total{protocol,result}`

日志只携带 request/provider/tenant 的安全标识、字节数、错误类别、offset 和 hash；不携带正文、token、prompt 或工具参数。

## 分阶段实施

1. **P0（部署前）**：确认 legacy WeChat callback 路由是否可达；若可达，先停用或接入验签与 body limit。Feishu Bot 强制安全配置并限制/脱敏 body。
2. **P1（第一批）**：创建严格 decoder；替换管理 API 共用 `readJSON`、审批与插件 install、所有 callback、上游非流式 provider response；修复“非空 malformed body 仍执行默认动作”。
3. **P1（第二批）**：为主 SSE/Anthropic stream 提供受限 frame reader；为 JSONB、hotconfig、控制面持久化消除 `_ = json.Unmarshal` 静默降级。
4. **P2**：引入 recover/stream 包并逐步迁移 LLM output、工具参数、sessionforensics 和前端；先 shadow metrics 对比，再启用恢复。
5. **P2/质量门禁**：CI 加规则，禁止生产 HTTP handler 裸 `json.NewDecoder(r.Body).Decode`、禁止 `_ = json.Unmarshal`（经批准的展示/探测路径可标注例外）、禁止 unbounded `io.ReadAll(resp.Body)`，并要求 RawMessage 校验。

## 测试矩阵

| 场景 | 严格 HTTP | 恢复模式 | 流式模式 | 前端 |
|---|---:|---:|---:|---:|
| 空白/合法 UTF-8 | 接受 | 接受 | N/A | 接受 |
| BOM | 拒绝 | 去除后接受 | 按协议 | 明确 fallback |
| 前缀日志/自然语言 | 拒绝 | 提取首个完整值 | 拒绝为坏 frame | 显示错误/原文 |
| 尾部垃圾 | 拒绝 | 仅完整值提取时记录 dropped suffix | 坏 frame | fallback |
| `{} {}` 双文档 | 拒绝 | 拒绝，不拼接 | 作为两个 events 处理 | 不当作单响应 |
| 截断 object/array | 拒绝 | 默认拒绝；仅批准工具参数场景保守闭合 | incomplete | incomplete |
| 控制字符/非法 escape | 拒绝 | 拒绝 | frame error | fallback |
| 深嵌套、大数组、大字符串 | 受限拒绝 | 受限拒绝 | frame/event 限制 | 受限展示 |
| nested JSON string | 先验证外层，再显式二次解析 | 记录二次恢复 | 协议特定 | safe parse |
| unknown field | 按 DTO 策略 | 保留诊断 | 协议特定 | schema fallback |
| 敏感 JSON 错误 | 不记录 body | 不记录 body | 不记录 payload | 不展示 token |

需要新增的测试类型：表驱动边界测试、fuzz（前后垃圾/BOM/逃逸/深嵌套）、max-byte/max-depth/max-collection、HTTP 第二值拒绝、webhook 验签与 body limit、SSE 无换行/多行 data/缺 DONE、RawMessage JSONB 校验、前端 corrupt localStorage 与截断 SSE 回归。

## sessionmeta 当前状态

当前实现已经正确地在原始 request body 上限制 1 MiB、拒绝拼接文档，并将 `developer` 归一化为 `system`：`domains/analysis/sessionmeta/extractor.go:152-186`、`189-250`、`314-349`。相关覆盖包括 oversized 和拼接 JSON：`domains/analysis/sessionmeta/extractor_boundary_test.go:365-372`。

仍需补齐：直接 `Input.Messages` / `Input.UserText` 的预规范化字节上限、拒绝原因诊断、BOM/前后垃圾/截断/仅空白尾随/混合协议形状测试，以及 `MaxContentRunes` 的资源边界测试。
