# 客户端→LLM 供应商端到端请求链路审计报告

> 生成日期: 2026-07-27 ｜ 对照 spec: `2026-07-27-routing-state-architecture.md`（URSM v2 单源）
> 方法: 6 个探索 agent 对 8 个关注域逐一核实代码事实（file:line 可回溯）。**结论基于代码，非文档**。

## 0. 端到端链路全景

```
HTTP 请求
  │
  ▼ middleware 链 (main.go:4053-4064)
  │   Recovery → RequestID(生成 X-Request-Id) → Locale → CORS → Prometheus
  │   → Auth(全局静态 key, 可选) → Origin → Logging → SecurityHeaders
  │
  ▼ http.ServeMux (main.go:3427-3430, 可选 v2 overlay :3452-3477)
  │   /v1/chat/completions, /v1/completions → chatHandler
  │   /v1/messages → messagesHandler(包裹 chatHandler)
  │   /v1/responses → responsesHandler(包裹 chatHandler)
  │   若 LLM_GATEWAY_USE_V2_PIPELINE=true → v2DispatchHandler 包裹上述 handler
  │      (main_pipeline.go:705-876: 预检 pipeline + KeyVerifier.Verify +
  │       identity.WithComputedIdentity 注入 ctx + 发布 request.completed)
  │
  ▼ streaming.ChatHandler.ServeHTTP (handler.go:896-5678, 单体 5678 行)
  │   1. 派生 requestID/sessionID(handler.go:914-957) + ensureSessionID
  │   2. defer 安全网 (handler.go:958): panic→EmitFailure / 未记→EmitFailure
  │   3. 提取 key → KeyVerifier.Verify(handler.go:1196-1238)
  │   4. 限流 → 会话 → 模型策略 → body 解析(handler.go:1432-1545)
  │   5. 幂等检查(handler.go:2363) → preStream keepalive(:2420)
  │   6. serveWithExecutor(handler.go:1134): buildRouteStickyKey → executor.Execute
  │
  ▼ executors.Executor.Execute (executor.go:1476-2926)
  │   Router.PlanCandidates(router.go:82): dedupe → URSMv2 FilterAndScore(fail-open)
  │     → selectStateBackend → pressure penalty → tier+Bandit/P2C → sticky
  │   for cand in candidates (executor.go:1927):
  │     upstreamContext (executor_chat.go:1507): 流式/会话用 WithoutCancel(不随客户端断开)
  │     FpSlots.Acquire(holder = userKey|clientType, executor.go:1801)
  │     executor_chat / executor_anthropic: 构造出站 HTTP → 发送 → 读响应
  │     成功: recordStickySuccess/FpSlots/Bandit/credentialstate/URSMv2
  │     失败: 分类 → 可重试则 continue 到下一 cand / 同步重试 round
  │
  ▼ 转发回客户端
  │   流式: stream.go:338 StreamChatWithPendingCaptureAndDiagnostics (SSE 逐行转发+变换)
  │   非流式: executor_chat.go:1000-1112 (读全 body → 变换 → WriteHeader+Write)
  │
  ▼ 记录
      request_wal_hot (RequestLogger)  ⇄  request_logs_hot (TelemetryClient)
      → sessions_v2 镜像 → analysis_events → audit.Sink(slog)
```

**关键事实**: 真实数据面在 `domains/streaming/`（v1）。`gateway-new/`/`cmd/gateway-v2/` 是未启用的平行实现。v2 dispatch 只是 v1 外面包了一层 Hook pipeline，真正的 LLM 调用仍委托给同一个 `chatHandler`。

---

## 1. 8 个关注域逐一核实结论

### 1.1 客户端识别与标记 ✅ 基本健全，但有几个传播缺口

| 项 | 现状 | 结论 |
|---|---|---|
| API key 提取 | `extractBearerToken` (handler.go:4845) + v2 的 `pipelineAPIKey` (main_pipeline.go:912)。两者 fallback header 名不同（`x-api-key` vs `X-API-Key`），但 `http.Header.Get` 大小写不敏感，实际等价 | 小瑕疵 |
| KeyVerifier | HMAC-SHA256 哈希 + DB join 查询 + 60s 缓存 + singleflight (verifier.go:151-360)。`fire-and-forget` 更新 `last_used_at` | 健全 |
| **FpSlot holder** (userKey\|clientType) | commit 127503c4: `holder = clientTokenOf(StickyKey, clientType)` (executor.go:1801-1827)。`StickyKey = {tenant}:{app}:{apiKeyID}:{profile}` (handler.go:2386)。**刻意不用 IdentityHash（会漂移）**。`clientType` 从 `X-Gw-Client-Type`/User-Agent 推断 (client_fingerprint.go:22-104) | 健全，设计正确 |
| requestID 生成与回写 | `crypto/rand` 32 hex (requestid_mw.go:63)。中间件覆盖 `X-Request-Id` 到 req+resp header，原 client id 存 `X-Gw-Client-Request-Id` | 健全 |
| requestID 上行传播 | 出站 HTTP 设 `X-Request-Id` + `X-Gw-Session-Id` (executor_chat.go:365-376, executor_anthropic.go:702) | 健全 |
| requestID 下行传播 | 所有 handler 重写响应 header (handler.go:917, messages.go:84, responses.go:88) | 健全 |

**🟡 缺口（建议修）**:
- **G-ID-1 (中)**: v2 wrapper 读 session header 用 `X-Session-ID` (main_pipeline.go:737)，**不匹配**网关规范的 `X-Gw-Session-Id`（首选）。导致 `env.SessionID` 丢失网关会话 id，发布的 analysis_events SessionID 为空。
- **G-ID-2 (中)**: v2 wrapper 在中间件被绕过时 fallback `requestID="v2pipe-<nanos>"` 但**不写回 header** (main_pipeline.go:722)，下游 chatHandler 重新派生，**同一请求两个 request id → 审计分裂**。仅测试路径触发。
- **G-ID-3 (低)**: 两套 key 提取器并存；v2 开启时 `Verify` 跑两次（靠 60s 缓存 + singleflight 缓解）。
- **G-ID-4 (中)**: 见 §1.2，幂等键用服务端随机 requestID，导致 IdempotentCache 对客户端重试失效。

### 1.2 请求记录 ✅ 有安全网但双写严重、WAL 覆盖不全

**记录写入点**（共 6+ sink）:

| 时机 | Sink | 同步/异步 |
|---|---|---|
| 收到后 | `request_logs_hot` (status=in_progress) | 异步批量 (50/200ms) |
| 收到后 | `request_wal_hot` (CreateInitial, handler.go:2302) | **同步** 5s |
| 收到后 | `request_context_attrs` | 异步 |
| 收到后 | trace `receive_request` | 异步 |
| 成功 | WAL Update / logs Update / routing_decision_log / **sessions_v2** / analysis_events | 异步 |
| 失败 | `EmitFailure` → logs / WAL UpdateSync(同步2s) | 混合 |
| 客户端断开 | `probe-` 前缀的第二条 logs 行 (handler.go:4017) | 异步 |

**🟢 安全网健全**: ServeHTTP defer (handler.go:958) 兜底 panic + 未记失败 → EmitFailure。绝大多数早期失败（405/key 缺失/body 过大/json 解析）都有 logs 行。

**🔴 BLOCKER 级问题**:

- **L-1 (高, 数据完整性)**: `request_wal_hot` 的 `CreateInitial` 在 handler.go:2302 —— **在 auth/body 解析/会话/模型之后**。所以所有 pre-routing 失败（`missing_key`/`invalid_key`/`body_too_large`/`json_parse_error`/`rate_limit_exceeded`/`model_forbidden` 等）**只有 logs 行，没有 WAL 行**。WAL 表对这些请求"看不见"。
- **L-2 (高, 双写分裂脑)**: WAL(request_wal_hot) ⇄ logs(request_logs_hot) 两个独立写入器、独立失败路径、独立 fallback。**无跨表事务**。WAL 说 success 而 logs 说 failure 完全可能。终端态守卫（拒绝把已 success 的 WAL 行改回 failure, request_logger.go:400-425）**只在 WAL 侧有**，logs 侧 `ON CONFLICT DO UPDATE` 无条件覆盖 → logs 侧可被回退。
- **L-3 (中, sessions_v2 双写)**: 同一会话 turn 被写两次 —— telemetry 的 `onPersisted` hook (sessionv2mirror.PersistHook) **和** v2 pipeline 的 `SessionPersistHook`。两路径用不同 `ProcessedRequest` 构造，字段级可能发散，靠 PK 幂等兜底。
- **L-4 (中, 镜像丢失)**: sessions_v2 镜像在 telemetry worker goroutine commit 后才跑 hook (hook.go:41)，进程此时挂掉 → 主行存在、镜像丢失。`session_writer_v2.go:245` 快照用 `go func(){ context.Background() }`，无生命周期绑定。
- **L-5 (低)**: 断开探针写 `probe-` 前缀**独立行**（不是更新主行），1 逻辑请求产生 2 行，易被误读为重复。
- **L-6 (文档失实)**: docs/REQUEST_LOGGING_DATA_FLOW_ARCHITECTURE.md 多处行号过时（CreateInitial 实际 2302 非 1592），且"无 request_records 表"（grep 全仓为零）——该表不存在。

**并发（核实后）**:
- `audit.StreamCapture` mutex 保护 ✅
- `RequestLogger`/`TelemetryClient` 单 worker + channel ✅
- `RoutingAttemptsTracker` 全方法 `sync.Mutex` ✅（routing_tracker.go:24-126）
- **`RequestLogContext` 无锁无原子** (request_log_pipeline.go:30-120) —— 但核实后**当前是单 goroutine 访问**（probe 回调 inline 在 executor 同步路径 executor.go:1737；流式 path 改 `capture.QualityFlags` 而非 `logCtx`；stream.go 唯一 `go func` 在 898 只发 channel）。**今日无 data race**，但是脆弱设计——任何把日志/回调挪到后台 goroutine 的改动都会引入竞争。**建议加锁防御**。

### 1.3 格式转换与 tools ⚠️ tools 有静默丢失，JSON 不够鲁棒

**两套转换系统并存**: IR 路径（`TRANSPORT_LAYER_IR_ENABLED`，**默认关**）+ Legacy 路径（默认）。两者都需审计。

**🟢 已修复（文档过时，实际已 OK）**: `input_schema.required` 数组强转（`sanitizeInputSchema` 在 parse+serialize 双侧跑）；tool 参数双发安全守卫（`initialArgsSent`）；MiniMax `tool_call_id` 集中化（provider_field_mapping.go）；tool role 序列化双向。

**🔴 BLOCKER 级问题**:

- **F-1 (高, 数据丢失)**: **provider 专用 tool 类型被静默丢弃**。`ToolDefinition` 只有 `Name/Description/Parameters` 3 字段（internal/ir/types.go:365），无 Type/raw。所以 Anthropic 的 `computer_use`/`bash`/`text_editor`、OpenAI 的 `web_search`/`code_interpreter`/`file_search`（无 name 或非 function 形）→ 产出空 name 工具或被丢弃。**对 agentic 客户端影响大**。
- **F-2 (高)**: **`stream_options` 在 IR 路径被静默丢弃**。它列在 `fields.go:31` 的 standardRequestFields（所以 extension 提取器认为"IR 处理了"不去捕获），但 internal/ir 既不 parse 也不 serialize。结果 IR 路由下客户端 `stream_options:{include_usage:true}` 永不到达上游 → 流式 usage 丢失。（Legacy OpenAI→OpenAI 字节透传则存活。）
- **F-3 (中)**: **Anthropic `tool_choice:"any"` 原样发给 OpenAI 上游** (serialize_openai.go:476)，OpenAI 只认 `auto/none/required/{type:function}`，会 **400 拒绝**。
- **F-4 (中, 信息丢失)**: `response_format`、`parallel_tool_calls` 在 →Anthropic 方向被丢（Anthropic 无对应，可接受，但应记 anomaly）；Anthropic `metadata` 只读 `user_id`，其余丢。
- **F-5 (中, 鲁棒性)**: **无流式 JSON 拼装器**。tool 参数分片（`input_json_delta`）按字符串拼接直接转发，**从不校验是否合法 JSON**。3 处 lenient fallthrough（serialize_anthropic.go:366/457 把非法 args 当裸字符串；chat_to_anthropic.go:153 把非法 args 替换为 `{}`——**直接丢弃原参数**）。截断/畸形分片 → 客户端收到非法 JSON。全仓零 `json.Decoder`。
- **F-6 (低)**: 完整性校验器对 ≤2 条消息的请求跳过 (serialize_anthropic.go:58, serialize_openai.go:190)——短上下文里的孤儿 `tool_result` 会到达上游触发 provider 报错（如 MiniMax 2013）。
- **F-7 (低, 误删数据)**: `strip_zhipu_fields.go` 删 `web_search_results`/`retrieval_documents`——这是客户端**可能要展示的语义搜索输出**，非内部元数据。`strip_doubao/minimax` 删通用 `request_id`——丢失客户端可用的追溯字段。

**vendor 字段剥离**: 仅剥顶层；嵌套不动（audit-09 已定）。任何解析错误回退原 body（不损坏）。整体方向对，但有上述误删。

### 1.4 路由与数据转发 ✅ 主链路完整，有 3 个数据丢失点

**路由**: `PlanCandidates`（router.go:82）单次同步执行：dedupe → URSMv2 FilterAndScore(fail-open) → selectStateBackend → pressure → tier(Bandit/P2C) → sticky。失败重试在 executor 单 `for cand` 循环 (executor.go:1927) + 同步重试 round (maxSyncRetryRounds=3, executor.go:2783)。**注意**: 旧的 `r.URSM`(v1 Manager) 已是死代码 (router.go:380-404 注释)，唯一 live 入口是 `URSMv2`。

**数据转发**:
- 非流式: `io.LimitReader(resp.Body, maxBodySize)` 读全 → 变换 → Write (executor_chat.go:1000-1112)。
- 流式: stream.go:338 逐行读 → 变换 → flush。`readLineWithTimeoutAndCloser` 超时关 body 解锁 reader goroutine (stream.go:869)。

**🟡 数据丢失点**:
- **D-1 (中)**: `stream.go:810-825` **无条件** `continue` 丢弃 `"choices":[]` 的 SSE 帧。这会**误删合法的 usage-only 终帧**（OpenAI `stream_options.include_usage` 规范就是发 `{"choices":[],"usage":{}}`）。客户端 token 计费会少算。（原为修 glm-5.2 让 SDK 崩溃的问题，但谓词太粗。）
- **D-2 (低)**: 4xx body 读前 4096 字节后 `io.Copy(io.Discard)` 排空剩余 (executor_chat.go:586, executor_anthropic.go:810)。前 4096 存 `upstreampkg.Error.Body`。**OpenAI 路径**: 客户端永远收合成信封（正确）。**Anthropic 路径**: executor_anthropic.go:869-878 把 vendor 原始 status+前 4096 字节透传客户端——**vendor 4xx body >4KB 时客户端收到截断的非法 JSON**。
- **D-3 (低, 资源泄漏)**: 流式客户端断开 + 无 pending capturer 时，读循环继续消费上游直到 EOF/timeout（ctx 是 `WithoutCancel`）。非客户端可见丢失，但浪费 vendor 配额。

### 1.5 异常处理与返回格式 ✅ 分类清晰，但 Anthropic 错误信封不对

- **isRetriableError** (handler.go:263): 网络/超时/上游宕/限流/瞬时/模型未找到/并发 → 重试；auth/content_filter/context_length/quota → 不重试直达客户端。指数退避 + ±20% 抖动 (handler.go:339)。**分类合理**。
- **🔴 E-1 (中)**: **错误信封永远是 OpenAI 形** `{"error":{...}}` (handler.go:5058-5129)。Anthropic 协议客户端应收到 `{"type":"error","error":{...}}`，网关不翻译错误信封 → Anthropic SDK 可能拒收/误解析。
- **客户端断开**: 不取消上游（流式/会话用 `WithoutCancel`，executor_chat.go:1507-1512）——**刻意设计**为 pending store 可重放。非流式则正常取消。部分结果通过 `probe-` 行记录。**方向正确**。
- **429/5xx → 凭证冷却**: Limiter.Shrink(0.7) + 熔断器 RecordFailure + credentialstate 3 次连续失败冷却 5min + URSMv2 record_request.lua。**机制齐全**（但见 §1.7 多源不协调）。
- **响应格式检查**: `FormatDetector/Fixer` 是**请求侧**（client→gateway）格式纠偏 (handler.go:1455)；`isNonStreamEmptyResponse` 把空内容 200 转可重试 (executor_chat.go:1020)。**无"等待/重试"循环**——坏响应要么 failover 要么报错，靠字节改写修补（quality-fix/normalize/strip/IR）。**可接受**。

### 1.6 路由信息与节点状态完整性 ⚠️ 多源并存未收敛，spec 待实施

**核实结论**: spec（routing-state v2）描述的"4 套状态后端并存"**完全属实且今日仍是默认**。**生产默认 `URSM_V2_MODE=off`**（config.go:57，env 甚至不在 .env.example），此时:

| 后端 | 读 live? | 写 live? |
|---|---|---|
| URSM v2 (`ursm:v2:`) | ❌ 死路径(mode 守卫) | ❌ off 时 no-op (manager.go:312) |
| credentialstate.Manager (`llmgw:credstate:`) | ✅ LegacyStateBackend | ✅ executor.go:2162,2412,2649 |
| routingstate.ShadowObserver | n/a 观察者 | ✅ executor.go:2172,2423 |
| credentialfpslot.NodeState (`llmgw:cred_fp_node:`) | ✅ filterHealthyNodes router.go:536 | ✅ executor.go:2101,2401,2642 |
| 进程内熔断器 (breaker.go) | ✅（第 5 个，spec 未计入） | ✅ executor.go:2463,2654 |

**即默认热路径上 live 的状态系统是 4 + 熔断器，spec 的收敛(M1-M4)尚未开始。** spec 自查的 Context §1-6 全部核实属实。

**🔴 BLOCKER 级**:
- **S-1 (高, 分裂脑)**: 3 个 Redis 后端（`ursm:v2:node:` / `llmgw:credstate:` / `llmgw:cred_fp_node:`）+ 进程熔断器，**各自独立写冷却态，无任何调和**。一个成功另一个失败 → "凭证是否可路由"三套答案。`apply_decision.lua` 的 gen CAS 只在 `ursm:v2:` 命名空间内有效，跨命名空间无版本契约。
- **S-2 (高, 不一致判定)**: router 先问 StateManager (state_backend.go:79-101) 再单独问 FpSlots (filterHealthyNodes, router.go:531-569)，**无一致快照**——凭证可通过一个 fail 另一个。
- **S-3 (中, 可观测缺失)**: `routing_state_source` 字段**不存在**（spec 验收 #3）。fail-open 静默发生，无 Grafana 可见的 fallback 占比。
- **S-4 (低)**: `executor.go:1073` `legacyWritersEnabled()` 每次调 `isURSMv2Authoritative()` 做 **10ms Redis Ready() 检查**，每请求调用 11+ 次 → authoritative 模式下 ~110ms/请求 的 Redis 往返（注释自承"浪费"）。

**generation/迟到回放**: Redis Lua 内强单调（`apply_decision.lua:25` 拒绝 stale gen）✅。但 spec 的进程 LRU 镜像（`lrumirror.go`）**尚未实现**（`domains/ursm/v2/cache/` 不存在）。

### 1.7 并发处理 ✅ 多数安全，credentialstate 有真实竞争

**逐项核实结论**（agent 双重验证）:

| 位置 | 风险 | 结论 |
|---|---|---|
| **credentialstate.Manager** (manager.go:256-275) | `*State` 指针从 sync.Map 取出后**原地改 `state.ConsecutiveFails++`**，无 per-key 锁，setToMemCache 存回同一指针 | **🔴 真实 data race + 丢失更新**。`go test -race` 会报。URSMv2/FpSlots 用 Lua HINCRBY 原子，legacy 路径严格更差 |
| RequestLogContext (request_log_pipeline.go:30) | 无锁无原子 | **今日 SAFE**（单 goroutine），但脆弱，建议加锁 |
| RoutingAttemptsTracker | 全方法 sync.Mutex | ✅ SAFE |
| StickyCache (sticky.go) | RWMutex；`go dbSetMultiLevel` 在 unlock 后启动、按值传参 | ✅ SAFE |
| SessionIntentCache (session_intent_cache.go) | RWMutex；Redis 构造器是 stub（spec M1 未做） | ✅ SAFE（但进程内、重启丢） |
| Limiter 4 层 | atomic.Int64 + RWMutex map | ✅ SAFE |
| FpSlots Acquire/Release | bf8a1bcc 已修：releaseFpLease 走 1024 队列 + 单 worker + Background ctx | ✅ SAFE（Redis 降级时退化为同步 1s，可接受） |
| session_v2 快照 go func | sessionAggregator 无内存态，DB upsert 计数器累加安全 | ✅ SAFE（last_* 替换字段 DB 层 last-writer-wins，已知限制） |
| candidate slice mutation | applyPressurePenalty 原地改 + 重排 | ✅ SAFE（单 goroutine），但重构易碎 |

**🔴 C-1 (高)**: `credentialstate.Manager` 的 `*State` 原地改 + sync.Map 无 CAS = 真实竞争。这是全链路唯一明确的并发 bug。

---

## 2. 问题分级汇总（按优先级）

### 🔴 BLOCKER（影响正确性/数据完整性，必须先修）

| ID | 问题 | 位置 | 修复要点 |
|---|---|---|---|
| S-1 | 3 个 Redis 状态后端 + 熔断器无调和，分裂脑 | executor.go 状态写多处 | 实施 spec M1-M4：URSM v2 单源 |
| S-2 | router 跨后端无一致快照 | router.go:236, 531 | 同上（M3 selectStateBackend 收敛） |
| F-1 | provider 专用 tool 类型静默丢失 | internal/ir/types.go:365 | ToolDefinition 加 `Type`/raw 保留 |
| F-2 | `stream_options` 在 IR 路径静默丢 | internal/ir parse/serialize | 补 parse+serialize 或移出 standardFields |
| L-1 | WAL 对 pre-routing 失败不覆盖 | handler.go:2302 | CreateInitial 前移到收到即写 |
| C-1 | credentialstate `*State` 原地改竞争 | manager.go:256-275 | per-key 锁或迁 URSMv2 |

### 🟡 IMPORTANT（应修，非阻塞但影响体验/成本）

| ID | 问题 | 位置 |
|---|---|---|
| D-1 | empty-choices SSE 帧误删 usage 终帧 | stream.go:810-825 |
| L-2 | WAL⇄logs 分裂脑，logs 侧无终态守卫 | client.go:848 |
| E-1 | 错误信封永远 OpenAI 形，Anthropic 客户端收不对 | handler.go:5058 |
| F-3 | Anthropic `tool_choice:"any"` 原样发 OpenAI | serialize_openai.go:476 |
| F-5 | 无流式 JSON 拼装器，tool 参数畸形直传 | stream.go + serialize |
| G-ID-1 | v2 wrapper 读错 session header | main_pipeline.go:737 |
| G-ID-2 | v2 fallback requestID 不写回 header | main_pipeline.go:722 |
| G-ID-4 | 幂等键用服务端随机 requestID，重试失效 | handler.go:2363 + requestid_mw.go:46 |
| S-3 | 无 routing_state_source 可观测 | — |
| F-6 | 完整性校验器跳过 ≤2 消息请求 | serialize_anthropic.go:58 |
| D-2 | Anthropic 4xx body >4KB 截断透传 | executor_anthropic.go:869 |

### 🟢 NIT（低优先/防御性/清理）

| ID | 问题 |
|---|---|
| L-3 | sessions_v2 双写（telemetry hook + pipeline hook） |
| L-4 | sessions_v2 镜像 worker commit 后 hook 易丢 |
| L-5 | 断开探针独立行易误读 |
| F-4 | response_format/parallel_tool_calls →Anthropic 丢（应记 anomaly） |
| F-7 | strip_zhipu 误删 web_search_results；strip_* 误删通用 request_id |
| D-3 | 流式断开无 capturer 时浪费 vendor 配额 |
| S-4 | legacyWritersEnabled 每请求 11+ 次 10ms Ready 检查 |
| L-6 | docs 行号过时 + request_records 表不存在 |
| G-ID-3 | 两套 key 提取器；Verify 跑两次 |
| C-def | RequestLogContext 加锁防御 |
| C-latent | candidate slice 原地改（重构易碎） |

---

## 3. 实施方案设计

### 3.1 分阶段、与 spec 对齐

本审计的根因问题（S-1/S-2/C-1）正是 routing-state spec 要解决的。因此**实施主干 = 推进 spec 的 M1-M4**，同时穿插本审计发现的**链路正确性 hotfix**（不在 spec 范围内，须独立修）。

### 3.2 阶段 0 — 链路正确性 hotfix（1-2 天，独立可上线，不依赖 spec）

> 这些是数据丢失/格式正确性问题，与路由状态收敛正交，应先修、可独立发布、风险低。

**Commit P0-1: 修 empty-choices SSE 误删（D-1）**
- `domains/streaming/stream.go:810-825`：谓词从"含 `choices:[]` 即丢"改为"含 `choices:[]` **且无 `usage`/`prompt_annotations` 等有效 payload** 才丢"。保留原 glm-5.2 修复，但放行合法 usage 终帧。
- 测试：构造 `{"choices":[],"usage":{...}}` 断言被转发；`{"choices":[]}` 空帧断言被丢。

**Commit P0-2: 修 stream_options IR 丢失（F-2）**
- `internal/ir/parse_openai.go` 补 `stream_options` 字段；`serialize_openai.go` 补序列化。**或**把它从 `domains/transformation/fields.go:31` standardRequestFields 移除，让 extension 提取器捕获它（更省，因它是透传字段）。
- 测试：OpenAI 客户端带 `stream_options:{include_usage:true}` 经 IR 路由断言到达上游 body。

**Commit P0-3: 修 Anthropic tool_choice any→OpenAI（F-3）**
- `internal/ir/serialize_openai.go:476`：`any`→`required`（语义最接近，OpenAI 接受）。

**Commit P0-4: 修 v2 wrapper session/requestID 传播（G-ID-1/2）**
- `cmd/gateway/main_pipeline.go:737`：`env.SessionID = r.Header.Get("X-Gw-Session-Id")`（优先）再 fallback `X-Session-Id`。
- `:722`：fallback requestID 写回 `r.Header.Set("X-Request-Id", requestID)`。

**Commit P0-5: Anthropic 错误信封翻译（E-1）**
- `domains/streaming/handler.go` 新增 `writeErrorAnthropic(w, ...)` 输出 `{"type":"error","error":{"type":..., "message":...}}`；当 `isAnthropicMessagesPath` 时用之。

**Commit P0-6: ~~完整性校验器去 ≤2 跳过（F-6）~~ → 移至阶段 2**
- 原计划移除 `serialize_anthropic.go:58` / `serialize_openai.go:190` 的 `len(messages) > 2` 守卫。
- **实施时发现真实冲突，降级到阶段 2**：直接移除会破坏 4 个 orphan 检测测试（`TestSerializeAnthropic_OrphanedToolResult` 等要求"无 assistant 消息时也拒绝 orphan tool_result"）**和** 4 个 serializer 片段测试（`TestAnthropicToolResultToOpenAITool` 等构造单消息片段）。两者契约互相矛盾：
  - orphan 测试认为"任何 orphan tool_result 都该拒绝"（即使无 assistant 消息）。
  - 片段测试构造"只有 tool_result 一条消息"的合法序列化场景。
  - validator 当前 `> 2` 跳过恰好掩盖了这个矛盾。
- **阶段 2 正确修法**：把校验从 Serialize（通用序列化器）上移到 transformation/请求边界（那里有完整请求上下文），并在 validator 内区分"完整请求里有 assistant 但缺对应 tool_use"（真 orphan，拒绝）vs"请求片段"（放行）。需要重写 validator 契约 + 同步修两类测试。不在 P0 hotfix 范围。

**Commit P0-7: Anthropic 4xx body 不截断（D-2）**
- `executor_anthropic.go:860-879`：raw 透传前用 `io.ReadAll(io.LimitReader(resp.Body, maxBodySize))` 读全再转发，而非固定 4096。

### 3.3 阶段 1 — WAL 全覆盖 + credentialstate 竞争修复（2-3 天）

**Commit P1-1: WAL CreateInitial 前移（L-1）**
- `handler.go`：把 `requestLogger.CreateInitial` 从 2302 前移到 ServeHTTP 早期（session 派生之后、auth 之前），body 字段先留空、后续 Update 补。确保所有 pre-routing 失败都有 WAL 行。

**Commit P1-2: credentialstate `*State` 加锁（C-1）**
- `domains/credentialstate/manager.go`：引入 per-key `sync.Mutex`（或 `singleflight`），UpdateOnSuccess/Failure 改为 Load→**复制** State→改副本→Store 副本（COW），杜绝原地改。
- `go test -race ./domains/credentialstate/...` 必须过。

**Commit P1-3: logs 侧终态守卫（L-2）**
- `domains/hooks/observability/telemetry/client.go:848`：`ON CONFLICT DO UPDATE` 加 `WHERE status NOT IN ('success','failure')` 条件（或等价的 terminal-state 守卫），与 WAL 侧对齐。

### 3.4 阶段 2 — provider tool 类型保留（3-5 天，F-1）

**Commit P2-1: IR ToolDefinition 加 raw/type 保留**
- `internal/ir/types.go:365`：加 `Type string` + `Raw json.RawMessage`（透传未知 tool 形）。
- `parse_openai.go:432`/`parse_anthropic.go:508`：捕获原始 type + raw bytes。
- `serialize_openai.go`/`serialize_anthropic.go`：非 function 类型原样回放（同协议）+ 跨协议尽力映射（如 Anthropic computer_use ↔ OpenAI 无对应则记 anomaly 而非静默丢）。
- `anthropic_toolcall_integrity_test.go` 扩展覆盖 computer_use/bash/text_editor/web_search。

### 3.5 阶段 3 — 流式 JSON 鲁棒性（3-5 天，F-5）

**Commit P3-1: 引入流式 JSON 拼装器**
- 新增 `domains/transformation/json_stream_assembler.go`：基于状态机的增量 JSON 拼装（开括号/闭括号计数），在 `content_block_stop` 时校验完整性；不完整则记 anomaly + 尽力修补（补 `}`）或标位转发。
- 接入 IR stream.go 与 legacy `anthropic_to_openai_stream.go` 的 bufferedToolArgs 路径。
- 测试：截断/畸形分片场景。

### 3.6 阶段 4 — 推进 routing-state spec M1-M4（与 spec 同步，7 天+）

> 这部分**完全采用 spec `2026-07-27-routing-state-architecture.md`** 的 Rollout Plan，不重述。审计确认 spec 的 Context/Goals/Decisions 与代码事实一致，可直接执行。关键里程碑：

- **M1（3-5 天）**: `ur:*` 命名空间 + `domains/ursm/v2/cache/lrumirror.go`（自研 LRU，gen 单调契约 applyToLRU）+ sticky_redis/intent_redis 双写。**一次性迁移 `llmgw:cred_fp_node:` → `ur:cred:`**（按 spec §1.1 字段映射表，注意秒→毫秒单位转换）。
- **M2（3-5 天）**: lrumirror 接入 FilterAndScore/sticky/intent 热路径。性能基准 P95 ≤ 0.5ms。
- **M3（5-7 天）**: router 移除旧分支（S-2 解决）；`_to-be-deprecated/routingstate`、`credentialstate` 下沉（C-1 根治）；feature flag 9→1 收敛。
- **M4（7 天+）**: canary 7 天 → authoritative。**默认 `URSM_V2_MODE` 改 authoritative**（S-1 解决）。

**阶段 4 内的并发清理**: M3 下沉 credentialstate 后 C-1 自然消失；同时引入 `routing_state_source` 字段（S-3）。

### 3.7 不做（Out of Scope）

- 不重写 FpSlots/Limiter/RPM/DisguisePool（防封锁机制独立，spec 明确）。
- 不改 sticky key 命名格式（跨 245/154 兼容）。
- 不引入 etcd/zookeeper（只用既有 Redis）。
- 不动 `gateway-new/`/`cmd/gateway-v2/` 平行实现（未启用）。
- vendor 字段 strip 的语义调整（F-7）留待业务确认后再做，避免误判。

---

## 4. 审计：方案 vs 目标一致性

逐条核对用户原始 8 个关注点是否被方案覆盖：

| 用户关注点 | 覆盖的 Commit | 状态 |
|---|---|---|
| ① 客户端识别与标记（key→用户，与请求标记） | P0-4 (G-ID-1/2) | ✅ 主干健全，修传播缺口 |
| ② 请求记录全程 + 双写正确性 | P1-1 (L-1), P1-3 (L-2) | ✅ WAL 全覆盖 + 终态守卫 |
| ③ 格式转换/tools（单/多 tools、不同格式、JSON 兼容） | P0-2 (F-2), P0-3 (F-3), P2-1 (F-1), P3-1 (F-5) | ✅ 全覆盖 |
| ④ 路由及数据转发多流程完整性 | P0-1 (D-1), P0-7 (D-2) + spec M1-M4 | ✅ 转发丢点 + 状态收敛 |
| ⑤ 异常处理与返回格式 | P0-5 (E-1), P0-6 (F-6) | ✅ 错误信封 + 完整性 |
| ⑥ 返回数据格式检查（等待 vs 修补） | 已确认：failover/字节改写修补，无等待循环（可接受） | ✅ 无需大改 |
| ⑦ 路由/节点状态完整性与并发 | spec M1-M4 (S-1/S-2), P1-2 (C-1), S-3 可观测 | ✅ 根因对齐 spec |
| ⑧ 全链路并发检查 | P1-2 (C-1)；其余核实 SAFE | ✅ 唯一真实 bug 已定位 |

**与 routing-state spec 的一致性**: 本方案的"阶段 4"直接复用 spec 的 M1-M4，未发明新设计。审计确认 spec 的所有 Context 前提（4 源并存、sticky 异步 DB、intent 纯内存、9 个 flag、Redis 稳态方向）**全部经代码核实属实**，spec 可直接执行。

**风险**: 阶段 0-3 是独立 hotfix，可分批上线、低风险；阶段 4 依赖 canary 灰度，需 Grafana 监控 P95/fallback 占比，超阈值回滚（spec 的 Rollback 章节已覆盖）。

---

## 附录 A：关键 file:line 索引

- 中间件链: `cmd/gateway/main.go:4053-4064`
- requestID: `middleware/requestid_mw.go:41-71`
- mux 注册: `cmd/gateway/main.go:3427-3430`；v2 overlay `:3452-3477`
- v2DispatchHandler: `cmd/gateway/main_pipeline.go:705-876`
- ChatHandler.ServeHTTP: `domains/streaming/handler.go:896`
- WAL CreateInitial: `handler.go:2302` ｜ 安全网 defer: `handler.go:958`
- KeyVerifier.Verify: `domains/authentication/verifier.go:206`
- FpSlot holder: `domains/streaming/executors/executor.go:1801-1827`
- Router.PlanCandidates: `domains/streaming/executors/router.go:82`
- selectStateBackend: `domains/streaming/executors/state_backend.go:138`
- upstreamContext (WithoutCancel): `executor_chat.go:1507-1512`
- 流式转发: `domains/streaming/stream.go:338`（empty-choices drop `:810-825`）
- 错误信封: `handler.go:5058-5129`
- credentialstate 竞争: `domains/credentialstate/manager.go:256-275`
- IR ToolDefinition: `internal/ir/types.go:365`
- URSM v2 FilterAndScore: `domains/ursm/v2/manager.go:163` ｜ record_request.lua: `domains/ursm/v2/store/record_request.lua`
