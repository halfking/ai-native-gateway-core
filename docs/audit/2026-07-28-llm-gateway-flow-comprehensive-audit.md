# LLM Gateway Go 客户端↔供应商 全链路流程审计（2026-07-28）

> **审计范围**：客户端请求 → 网关 → LLM 供应商 → 网关 → 客户端 的完整数据通路  
> **审计目标**：识别关键风险点并提出改进方案；不直接修代码，先写完方案由老板决策  
> **审计日期**：2026-07-28  
> **审计方法**：静态代码阅读 + 历史审计报告交叉验证 + git log 追溯  
> **关联文档**：`AUDIT_CONCURRENCY_HARDENING_20260727.md` / `AUDIT_URSMV2_CONCURRENCY_20260728.md` / `AUDIT_24H_SUMMARY_20260728.md` / `ANALYSIS-client-cancel-root-cause-2026-07-24.md`

---

## 目录

- [1. 总览](#1-总览)
- [2. 客户端识别与请求标记](#2-客户端识别与请求标记)
- [3. 请求记录与双写](#3-请求记录与双写)
- [4. 请求格式转换](#4-请求格式转换)
- [5. 请求路由与数据转发](#5-请求路由与数据转发)
- [6. 异常处理与返回数据修补](#6-异常处理与返回数据修补)
- [7. 路由信息与节点状态并发更新](#7-路由信息与节点状态并发更新)
- [8. 全链路并发风险](#8-全链路并发风险)
- [9. 总结与优先级](#9-总结与优先级)
- [10. 自审：与老板关注点对齐情况](#10-自审与老板关注点对齐情况)

---

## 1. 总览

LLM Gateway Go 是企业级 LLM 代理网关，单进程 Go 服务（约 200+ 包、4500+ 行 main.go）。客户端 6 个路由（chat / completions / messages / responses / embeddings / models）经过 4 种协议（OpenAI Chat / Anthropic Messages / OpenAI Responses / Gemini 原生 + 兼容形态）转换、路由、转发到 ~30 家 LLM 供应商。

### 1.1 完整链路（按 call site）

```
[Client] ──┐
           │ POST /v1/chat/completions
           │ Authorization: Bearer sk-...
           │ X-Request-Id: client-supplied-uuid
           ▼
┌─────────────────────────────────────────────────────┐
│  cmd/gateway/main.go:131  main()                    │
│  ├─ main.go:4182-4191  中间件链（9 个）                │
│  │  Recovery → RequestID → Locale → CORS → Prometheus │
│  │  → Auth(admin only) → Origin → Logging → SecHdr    │
│  ├─ main.go:3500-3517  mux 注册 6 个客户端路由          │
│  └─ main.go:3543-3549  v2 Pipeline 覆盖（feature flag） │
└─────────────────────────────────────────────────────┘
           │
           ▼
┌─────────────────────────────────────────────────────┐
│  domains/streaming/handler.go:912  ChatHandler      │
│  ├─ 930-934  读 X-Request-Id（兜底 generateRequestID）│
│  ├─ 935-942  读 X-Gw-Client-Request-Id                │
│  ├─ 944     新建 RequestLogContext（安全网）            │
│  ├─ 997-1010  requestLogger.CreateInitial（provisional）│
│  ├─ 1256    extractBearerToken → keyVerifier.Verify   │
│  │          ↓                                         │
│  │          verifier.go:206  KeyInfo{TenantID, ...}   │
│  ├─ 1410-1530  body 解析 + FormatDetector 自动修复    │
│  ├─ 1848    auto_route.Decider（model=="auto"）       │
│  ├─ 2057    resolveCandidatesForRequest ──→ 路由决策   │
│  ├─ 2180+   pickStickyCredentialID（多级 sticky）     │
│  ├─ 2402    Request WAL CreateInitial（详细）         │
│  ├─ 2426    recordInitialRequestLog（入 request_logs） │
│  └─ 2668    executor.Execute(ExecParams{...})         │
└─────────────────────────────────────────────────────┘
           │
           ▼
┌─────────────────────────────────────────────────────┐
│  domains/streaming/executors/executor.go:1666       │
│  ├─ 1683-1714  continuation/retry 关键字检测           │
│  ├─ 1720-1742  identity pool acquire                  │
│  ├─ 1751+     多级 sticky 查找                       │
│  ├─ 2160-2350  候选循环：                             │
│  │   ├─ executeAnthropic (executor_anthropic.go:528) │
│  │   └─ executeOpenAI    (executor_chat.go:217)       │
│  │        └─ 503  e.Upstream.Do(req) ← HTTP 出口     │
│  ├─ 2943-2971  单节点立即 probe (asyncDepth<3)         │
│  └─ 2973+     sync_retry loop（≤ 3 轮）               │
└─────────────────────────────────────────────────────┘
           │
           ▼  HTTP
[Provider upstream]
           │
           ▼  SSE / JSON
┌─────────────────────────────────────────────────────┐
│  upstream.Client.Do()                                │
│    → 流式：StreamChatWithPendingCaptureAndDiagnostics │
│           (stream.go:349)                            │
│    → 非流式：result.ResponseBody 直接写回 w            │
│  回调：OnStreamCompleted → trace.StreamComplete      │
└─────────────────────────────────────────────────────┘
           │
           ▼
[Client] ── SSE 流 / JSON body
```

### 1.2 关键事实

| 维度 | 现状 |
|---|---|
| **协议支持** | OpenAI Chat / Anthropic Messages / OpenAI Responses / Gemini 原生 + 兼容；IR 3 层模型（`internal/ir/`） |
| **入口数量** | 6 个客户端路由；`/v1/embeddings` 独立链路；其余 4 个最终走 `chatHandler.executor.Execute` |
| **认证** | API key 校验在 handler 内联（handler.go:1256-1261），**不**走全局 Auth 中间件；admin 路径走另一套 |
| **路由决策** | 在 `handler.go:2057 resolveCandidatesForRequest` 内完成（**进入 executor 之前**），不在 executor 内部 |
| **流式响应** | 通过 `StreamChat` 闭包直接写 w（main.go:860-884 注入） |
| **审计/日志** | 多层并存：访问 slog + zap + DB WAL + AsyncRawData + RingBuffer + File Backup |
| **当前版本** | v2.4.8（VERSION 文件） |

---

## 2. 客户端识别与请求标记

### 2.1 API key 解析（handler.go:1256-1261 + 5080-5093）

**顺序**：
1. `Authorization: Bearer xxx` / `bearer xxx`
2. `x-api-key: xxx` header
3. 返回 `""` → 401 missing_key

**校验流程**（verifier.go:206-235）：
1. 必须 `sk-` 前缀（rule 20 §2 强制）
2. 命中内存 cache → 直接返回
3. miss → `singleflight.Do("key:"+rawKey, callVerifyDB)`
4. `callVerifyDB` 内部：`hashAPIKey(secret, rawKey)` → SQL JOIN `api_keys ak JOIN applications app` → 返回 `KeyInfo{ID, TenantID, ApplicationID, CustomerID}`

**KeyInfo 字段**（被后续所有限流/路由/计费消费）：
```go
type KeyInfo struct {
    ID           int64
    TenantID     int64
    ApplicationID int64
    CustomerID   *int64  // 407 迁移
    // ...
}
```

### 2.2 请求标记

| 标记 | 类型 | 生成点 | 传递路径 | 用途 |
|---|---|---|---|---|
| **X-Request-Id** | 服务端 UUID | `requestid_mw.go:46 generateRequestID()` | `w.Header()` + `r.Header` → ctx → 全链路 | 网关侧主 trace key |
| **X-Gw-Client-Request-Id** | 客户端原值 | `requestid_mw.go:50-52`（从 `X-Request-Id` 复制） | handler 读入 `logCtx.ClientRequestID` | 客户端对账 |
| **X-Gw-Session-Id** | 业务会话 ID | `handler.go:976 ensureSessionID()`（provisional）→ `handler.go:1585+`（正式）| 路由、限流、Live Stream | 业务会话亲和性 |
| **X-Gw-Auto-Decision** | auto-route 决策记录 | `handler.go:1875 writeAutoDecisionHeader` | 响应头回写 | auto-route 可观测 |
| **X-Gw-Prefix-Stabilized** | prefix cache 触发 | `handler.go:2405` | 响应头回写 | 缓存命中率调试 |

### 2.3 风险点

| # | 风险 | 现状 | 影响 |
|---|---|---|---|
| **R-2.1** | API key 走 `singleflight`，但 cache miss 路径无 TTL 上限的 fallback | 单次 DB 慢会阻塞所有相同 key 的请求 | 单 key 高并发下出现排队毛刺 |
| **R-2.2** | `X-Request-Id` 由服务端强制生成新 UUID，**永不重用**客户端传的 `X-Request-Id` | 客户端日志关联断裂（必须用 X-Gw-Client-Request-Id） | 排查时容易混淆；客户端团队需要适配 |
| **R-2.3** | session ID provisional → 正式的两段式逻辑，如果中间失败，最终落库的是 provisional | `request_wal` 可能带 `provisional=true` | Live Stream 看到"幽灵 session" |
| **R-2.4** | KeyInfo 没有"key 来源"字段（admin / user / tenant-internal） | 审计无法区分人类调用 vs 系统调用 | 安全审计盲区 |

### 2.4 评价

- ✅ API key 校验严格（必须 `sk-` 前缀 + singleflight 去重 + DB JOIN）
- ✅ session ID 多级 fallback 健壮
- ⚠️ 标记体系清晰但**未贯穿所有日志条目**（见 §3 R-3.5）
- ⚠️ KeyInfo 缺少来源标签，影响审计

---

## 3. 请求记录与双写

### 3.1 日志架构总览

| 层 | 实现 | file:line | 输出 | 默认 |
|---|---|---|---|---|
| 访问日志 | `slog` 1 条/请求 | `middleware/logging_mw.go:66-123` | stdout JSON | ON |
| 应用结构化 | `zap.Logger` | `logging/logger.go:55-106` | stdout JSON | ON |
| DB 请求日志 | `telemetry.Client.EmitRequestLog` 异步 | `telemetry/client.go:460-505,716+` | `request_logs_hot` + `request_logs_bodies_hot` | ON |
| DB WAL | `RequestLogger` sync-INSERT + async UPDATE | `telemetry/request_logger.go:128+` | `request_wal` + `request_logs_hot` UPDATE | ON |
| 原始审计 JSONL | `AsyncRawDataLogger` lock-free 队列 + flush worker | `internal/logging/async_raw_logger.go:127+,149-175` | `<logDir>/raw_data_<ts>.jsonl`，轮转 200MB × 5 | ON |
| OpenTelemetry | `tracer.NewTracer` + OTLP gRPC | `tracing/tracer.go:49-96` | `localhost:4317` | ⚠️ **未在 `cmd/gateway/main.go` 初始化** |
| 降级备份 | `MultiBackupWriter(FileWriter, RingBuffer)` | `dbdegradation/multi_writer.go:41+` | `<backupDir>/*.jsonl.gz` + 内存 ring（cap 10000） | 仅 DB 降级时触发 |

### 3.2 DB 请求级字段（113 列，telemetry/client.go:117-227）

`request_id, ts, tenant_id, application_id, api_key_id, client_model, outbound_model, canonical_model, provider_id, credential_id, prompt_tokens, completion_tokens, cache_*, multimodal_*, cost_usd, latency_ms, success, request_status, error_kind, request_body, response_body, stream_first_chunk_ms, stream_chunk_count, gw_session_id, gw_task_id, client_request_id, attachments, routing_attempts, agent_name`

流式只记 `stream_first_chunk_ms / stream_chunk_count / stream_done_received / stream_interrupted`，**不记每 chunk**。

### 3.3 双写清单

| 写点 | file:line | 目标 | 一致性 | 失败处理 |
|---|---|---|---|---|
| **request_logs_hot（主）** | `client.go:793+` | PG 主热表 0-7d | 事务原子提交 | `client.go:491-503` 同步降级 + 计数 |
| **request_logs_bodies_hot** | `client.go:1541` | body 独立表 7d TTL | 同事务 | 同上 |
| **request_logs（冷月度分区）** | partition manager `main.go:2577` | columnar 月分区 | 异步 promote | — |
| **request_wal** | `request_logger.go` EmitInitial/PersistUpdateInTx | 7d 热表 + 月分区 | 同步初始 + 异步 batch UPDATE（100ms/50batch） | 失败 → fallback `WriteRequestWAL` |
| **Live Stream SSE** | `main.go:1504` hook | dashboard | onPersisted post-commit | panic recover only |
| **request_attachments** | `internal/attachmentmirror/hook.go:35` | migration 401 | best-effort（500ms ctx） | `slog.Warn` 吞掉，**永不阻塞主写** |
| **gateway.sessions V2** | `internal/sessionv2mirror/hook.go:36` | migration 430 | 500ms ctx，flag 开关 | `slog.Warn` 吞掉 |
| **FileWriter JSONL 备份** | `domains/dbdegradation/fallback.go:76+` | `<backupDir>/<prefix>.jsonl.gz` | degraded 模式触发 | 自动 replay |
| **RingBuffer 备份** | `dbdegradation/ring_buffer.go:84` | 内存 10000 条 | 同上 | — |
| **原始 audit JSONL** | `internal/logging/raw_data_logger.go:105-204` | JSONL 200MB × 5 | queue 满 → 占位 + 限流 warn（10s/次） | 永不阻塞 |

### 3.4 双写策略评价

**优点**：
- 主写强一致（`request_logs_hot` + `request_logs_bodies_hot` 同事务）
- 镜像 fire-after-commit + 永不阻塞主路径
- RingBuffer + FileWriter 双重降级备份
- WAL 双阶段（provisional → rich）合并用 `ON CONFLICT (request_id) COALESCE`

**缺点**：
- onPersisted hook 失败仅 `slog.Warn`，无重试队列、无独立 metric
- RingBuffer cap 10000 无 counter，突发丢行无告警
- 原始 audit 写盘失败仅 warn，无 anomaly 驱动

### 3.5 trace_id 流转盲区

| 链路 | 状态 |
|---|---|
| `RequestIDMiddleware` 生成 UUID → ctx → handler | ✅ 正常 |
| OTel span context → trace_id | ⚠️ **`tracer.NewTracer` 未在 `cmd/gateway/main.go` 初始化**（grep 0 hit） |
| `logging.WithContext` 从 ctx 读 trace_id | ⚠️ **ctx 写入点未定位** |
| `request_logs` 表是否含 trace_id 列 | ❌ **没有持久化列**，无法从 DB 反查 OTel span |
| `RawDataEntry.TraceID` 来源 | envelope `env.TraceID`（OTel span） |

**结论**：当前 trace 反查只能靠 `request_id` + `gw_session_id` + `gw_task_id`，与 OTel 后端无 join key。

### 3.6 风险点

| # | 风险 | 现状 | 影响 |
|---|---|---|---|
| **R-3.1** | OTel tracer 未初始化 | tracing 包实现完整但 main.go 未启用 | 与外部 OTel 后端无法联通 |
| **R-3.2** | `request_logs` 无 trace_id 列 | 反查链路只能靠 request_id | 跨服务追踪失效 |
| **R-3.3** | onPersisted hook 失败无 metric | 仅 `slog.Warn`，无 `shadow_write_failed_total` 指标 | DB 抖动期间默默丢行 |
| **R-3.4** | RingBuffer cap 10000 无 counter | 突发 > 10K/分钟触发丢，无告警 | 真实丢行数不可观测 |
| **R-3.5** | WAL provisional → rich 合并依赖 `ON CONFLICT COALESCE` | 若中途 panic / kill，provisional 带空 tenant_id | 落库行字段可能不完整 |
| **R-3.6** | 原始 audit JSONL 单点丢失 = 不可恢复 | 200MB × 5 文件，无跨机同步 | 审计合规风险 |

### 3.7 评价

- ✅ 多层日志冗余 + 强一致主写 + best-effort 镜像
- ✅ WAL 两阶段合并策略合理
- ⚠️ OTel 未启用、`request_logs` 无 trace_id 列 — **核心反查能力受限**
- ⚠️ 所有"软"写点（hook / RingBuffer / raw audit）失败无 metric，**真实丢行数不可知**

---

## 4. 请求格式转换

### 4.1 IR 3 层架构

```
Client (OpenAI/Anthropic/Gemini/Responses)
    ↓ DetectProtocol + Parse → IR.InternalRequest
    ↓ Serialize → Upstream Provider
    ↑ Provider Response → IR.InternalResponse
    ↑ Serialize → Client Format
```

### 4.2 关键文件

| 组件 | file:line |
|---|---|
| 协议识别 | `internal/ir/detect.go:30 DetectProtocol(body)` |
| 入站 OpenAI 解析 | `internal/ir/parse_openai.go:14 ParseOpenAI` |
| 入站 Anthropic 解析 | `internal/ir/parse_anthropic.go:9 ParseAnthropic` |
| 入站 Gemini 解析 | `internal/ir/parse_gemini.go` |
| 出站 OpenAI 序列化 | `internal/ir/serialize_openai.go:9 SerializeOpenAI` |
| 出站 Anthropic 序列化 | `internal/ir/serialize_anthropic.go:9 SerializeAnthropic` |
| 响应 OpenAI 解析 | `internal/ir/response.go:235 ParseOpenAIResponse` |
| 响应 Anthropic 解析 | `internal/ir/response.go:128 ParseAnthropicResponse` |
| 流式 OpenAI | `internal/ir/stream.go:140 ParseOpenAIStreamChunk` |
| 流式 Anthropic | `internal/ir/stream.go:354 ParseAnthropicStreamEvent` |
| 包装层 | `domains/transformation/ir_converter.go:54 TransportIRConverter`（加 Extensions 提取/还原 + CircuitBreaker） |

### 4.3 Tools 处理

- **入站**：`parse_openai.go:432-493`、`parse_anthropic.go:507-565`
- **schema**：`tools` 始终 array；`Parameters / input_schema` 用 `json.RawMessage` 保留原始 JSON
- **多态类型**：OpenAI 的 `web_search` / `code_interpreter` / `file_search`、Anthropic 的 `computer_20250124` / `bash_20250124` 走 `Type + Raw` 透传（`types.go:381-398`，2026-07-27 F-1 修复）
- **tool_choice 映射**：`required ↔ any` 双向（`serialize_openai.go:498-500`、`serialize_anthropic.go:678-679`，2026-07-27 F-3 修复）
- **Anthropic `required` 字段 sanitization**：`serialize_anthropic.go:618-667 sanitizeInputSchema`（2026-07-18 修复）

### 4.4 JSON 解析策略

- **两阶段**：先 `json.Unmarshal(body, &rawMap)` 捕获所有字段，再 unmarshal 到结构体
- **未知字段提取到 `req.Extensions`**：保证同 protocol 透传无损；跨 protocol 禁用（`ir_converter.go:206-218` 强制）
- **嵌套 JSON**：`messages` / `tool_calls` 先 `json.RawMessage` 拿到数组，再逐条解析
- **流式 tool_calls delta**：`json.RawMessage` 先存（`stream.go:181`），再二次 unmarshal 成结构体数组（`stream.go:295-318`）

### 4.5 流式 tool_calls 累积

- IR 层只搬运增量（`stream.go:295-318`）
- **实际拼接在 bridge 层**：
  - OpenAI→Responses：`responses_bridge.go:567-590` 有 `toolCallIDs = make(map[int]string)` 缓存 index→ID
  - OpenAI→Anthropic：`anthropic_stream.go:364-420` 每块立刻 emit + 累加 `input_json_delta`

### 4.6 易丢字段清单

| 字段 | OpenAI→OpenAI | Anthropic→Anthropic | OpenAI→Anthropic | Anthropic→OpenAI |
|---|---|---|---|---|
| `parallel_tool_calls` | ✅ | N/A | ❌ **丢失** | ❌ **丢失** |
| `response_format.json_schema` | ✅ | N/A | ❌ 部分丢 | ❌ 部分丢 |
| `structured_outputs` strict | ✅ | N/A | ❌ 丢弃 | ❌ 丢弃 |
| `previous_response_id` | ✅ | N/A | ❌ | ❌ |
| `prompt_cache_key` | ✅ | N/A | ❌ | ❌ |
| `truncation` | ✅ | N/A | ❌ | ❌ |
| `refusal` | ⚠️ 靠 Extensions | N/A | ⚠️ | ⚠️ |
| `reasoning_content` | ✅ Extensions | thinking block | ⚠️ 部分 | ⚠️ 部分 |
| `safety_identifier` | ✅ | N/A | ❌ | ❌ |
| `service_tier` | ✅ | N/A | ❌ | ❌ |

### 4.7 风险点

| # | 风险 | 现状 | 影响 |
|---|---|---|---|
| **R-4.1** | `parallel_tool_calls` 跨协议丢失 | Anthropic 无对应字段 | 客户端设 false 时被忽略 |
| **R-4.2** | `refusal` 字段靠 Extensions 透传 | 未在 `InternalResponse` 显式建模 | 跨协议可能丢 |
| **R-4.3** | 流式 tool_calls 累积逻辑分散 | `responses_bridge.go` 与 `anthropic_stream.go` 各自实现 | 重复代码；易出 bug |
| **R-4.4** | format detection 仅靠 model 字段猜测（body 为空时） | `detect.go:222-230` | Gemini 误判概率较高 |
| **R-4.5** | `validateToolCallIntegrity` `len(messages) > 2` gate | `serialize_anthropic.go:71-75` | 1-2 message 续传请求的 orphan tool_result 不拦截 |
| **R-4.6** | `unified adapter` 与 IR 适配器并存 | `adapter/unified/registry.go:144-147` init() 自动注册 | 死代码风险 |
| **R-4.7** | OpenAI Realtime API 未覆盖 | 仅 Chat + Responses | WebSocket 客户端不可用 |

### 4.8 评价

- ✅ IR 3 层模型 + Extensions 透传（同 protocol）
- ✅ 多态 tool 类型支持完整
- ✅ JSON 兼容性强（`json.RawMessage` 兜底）
- ⚠️ 跨协议字段丢失点较多（`parallel_tool_calls` 等）
- ⚠️ 流式 tool_calls 累积逻辑应统一到 IR 层

---
## 5. 请求路由与数据转发

### 5.1 路由决策位置（重要！）

**路由决策不在 executor 内部**，发生在 `handler.go:2057 resolveCandidatesForRequest`：

```
handler.go:2057  candidates, policy, requestModality, err := 
                 resolveCandidatesForRequest(ctx, h.provider, model, profile, tenantID, bodyBytes)
                   ↓
domains/streaming/candidate_modality.go:11  resolveCandidatesForRequest
  ├─ modality_detect.go:40  模态检测（text/vision/audio/video/multimodal）
  ├─ provider/client.go:394 GetCandidatesByModality
  │     ├─ SQL JOIN api_keys JOIN providers（filter 健康/额度）
  │     ├─ maybeExitSuspicious（可疑检测）
  │     ├─ P2C 加权负载均衡（planSet / orderMap）
  │     └─ RevealAPIKey(providerID, credentialID) 解密
  └─ 返回排序后的 []Candidate + *Policy
```

**这意味着 executor 拿到的已经是预排序的候选列表**。executor 内部只做：
- sticky 命中（executor.go:1751+ 多级 sticky）
- 候选循环 + sync_retry
- 单节点立即 probe（asyncDepth<3）
- 同步重试 ≤ 3 轮（maxSyncRetryRounds=3，外层 +3 = 4 轮总）

### 5.2 路由算法（5 层叠加）

| 层 | 位置 | 作用 |
|---|---|---|
| 1. URSM v2 FilterAndScore | `ursm/v2/manager.go:200 filterAndScore` | 先吃 LRU 镜像 → 命中走 fail-open；miss 走 Redis pipeline，按 price 0.4 + latency 0.4 + stability 0.2 加权排序。**生产默认 ModeOff** |
| 2. stateBackend.FilterAvailable | `router.go:175-176` | 过滤 Available=true |
| 3. filterHealthyNodes | `router.go:575-613` | 按 FpSlots.GetNodeState 健康度过滤；全失败 → fail-open |
| 4. splitByBillingRound → planByTier | `router.go:450-512` | 按 token_plan/code/agent vs PAYG 分两轮；按 Tier 分桶；桶内 Bandit 或 P2C；同分 `rrCounter.Add` 轮询 |
| 5. prioritizeSticky | `router.go:270-272` | 把 sticky 凭据置顶 |

**P2C 评分函数** `calculateLoadScore`（`router_scoring.go:43`）= concurrency(0.4) + identity(0.1) + latency_penalty(0.3) + quality(0.2) + headroom(env 0.05)。

### 5.3 客户端协议 → 上游协议的转换

| ClientProtocol | 上游为 OpenAI 协议 | 上游为 Anthropic 协议 |
|---|---|---|
| openai-completions | `e.StreamChat` 直接透传 | `e.OpenAIToAnthropicStream` 转换 |
| anthropic-messages | `e.OpenAIToAnthropicStream` 转换 | `e.AnthropicPassthroughStream` 透传 |
| openai-responses | `e.OpenAIToResponsesStream` 转换 | `e.AnthropicPassthroughStream` 透传 |

非流式由 executor_chat.go:1037+ 直接 `json.Encode` 后 `w.Write(result.ResponseBody)`；MessagesHandler 与 ResponsesHandler 有专属非流式落盘路径（messages.go:1052 / responses.go:965）。

### 5.4 多流程分支

| 入口 | 路径 |
|---|---|
| `/v1/chat/completions` | handler.go:912 直入 |
| `/v1/messages` | messages.go:649+ convertToChatBody → executor.Execute |
| `/v1/responses` | responses.go:ConvertResponsesToChatBody → executor.Execute |
| `/v1beta/models/{m}:generateContent` | handler_gemini.go IR 转换 → 合成请求 → chatHandler |
| `/v1/embeddings` | embeddings.go:88 独立链路（不依赖 ChatHandler） |
| v2 Pipeline 包装 | main.go:3543-3549 `v2DispatchHandler`（feature flag）preflight→fallback→postflight |

**v2 DispatchHandler 关键点**：它**不会阻断 fallback**（main_pipeline.go:705），即使 preflight 失败也会进入 chatHandler。

### 5.5 数据转发"线处理"完整性检查

| 检查项 | 现状 | 评价 |
|---|---|---|
| 客户端 body 全字段捕获 | ✅ `Extensions` 提取所有未知字段 | 同 protocol 无损 |
| 客户端 body 解析错误 | ✅ 4xx 直接返回（`parse_openai.go:17-19`） | 无 fallback 机制（可能过严） |
| 上游 body 构造 | ⚠️ `finalizeOpenAIUpstreamBody`（compression / normalize / 协议转换） | 单点 |
| 上游响应 body 写入 | ✅ 流式走闭包；非流式 `json.Encode` | OK |
| 流式 chunk 累积 | ⚠️ 分散在 `responses_bridge.go:572` 和 `anthropic_stream.go:364` | 重复逻辑 |
| `[DONE]` sentinel | ✅ `responses_bridge.go:659-661` 立即结束 | OK |
| 早返回（EOF before DONE）| ✅ 仍写 final events + 标记 `eof_without_done` | OK |
| chunk_timeout | ✅ 写 final + 标记 `chunk_timeout` | OK |
| panic 恢复 | ✅ 三层 defer recover，标记 `stream_panic` | OK |

### 5.6 风险点

| # | 风险 | 现状 | 影响 |
|---|---|---|---|
| **R-5.1** | 双层 sticky 选择（chatHandler.pickStickyCredentialID + executor sticky）| `handler.go:2180+` 与 `executor.go:1751+` 各自实现 | 决策不一致风险 |
| **R-5.2** | v2 Pipeline feature flag 状态不明 | `v2UsePipeline()` flag 读取点未定位 | 生产是否启用不确定 |
| **R-5.3** | `/v1/embeddings` 路径在 `SetAuth` 之前注册？ | main.go:1401 vs 3504 顺序待确认 | 可能无认证 |
| **R-5.4** | `/v1/sessions` 走 `sessionAuthAdapter` 与 chatHandler 认证**不共享** KeyInfo | main_types.go:74-83 | 字段覆盖差异 |
| **R-5.5** | autoroute.Decider 决策是否真传到 executor | handler.go:3428 `ChosenCredentialID: intPtr(result.Candidate.CredentialID)` | 需要运行时验证 |
| **R-5.6** | `executor.StreamWrapper` 字段未 grep 到 | 可能已废弃 | 死代码 |

### 5.7 评价

- ✅ 路由决策清晰分层（5 层叠加）
- ✅ 数据转发"线处理"基本完整（chunk 累积、EOF、timeout、panic）
- ⚠️ 双层 sticky + autoroute 与 executor 链路需验证
- ⚠️ v2 Pipeline flag 启用状态需确认

---

## 6. 异常处理与返回数据修补

### 6.1 Error Kind 分类（errorsx/classify.go:14-78）

**永久 fatal**（连续 ≥2 直接破阈值）：
- `KindAuth` / `KindAuthRevoked` / `KindQuotaPermanent` / `KindQuotaBalance` / `KindModelNotFound`

**临时 transient**（按重试策略续试）：
- `KindTransient` / `KindTimeout` / `KindStreamTimeout` / `KindNetwork` / `KindRateLimit` / `KindUpstreamDown` / `KindConcurrent`

**中性**（不计入熔断）：
- `KindCanceled` / `KindContextLength` / `KindToolCallIdMismatch` / `KindContentFilter` / `KindEmptyResponse` / `KindUnsupportedFeature`

### 6.2 上游→客户端 HTTP 映射（errorsx/classify.go:373-391）

| 上游状态码 | Error Kind | 客户端返回 |
|---|---|---|
| 401 / 403 | KindAuth | 401 |
| 402 | KindQuota | 402 |
| 429 | KindRateLimit（body 是 overload → KindConcurrent） | 429 |
| 500 / 502 / 504 | KindUpstreamDown | 502 / 504 |
| 503 / 529 | KindConcurrent | 503 |
| 通用 4xx | KindTransient | 400 |

### 6.3 重试策略

| 条件 | 动作 |
|---|---|
| transient 错误 | `continue` 下一个候选 |
| fatal 错误 | `continue` 下一个候选（**不重试**） |
| `model_not_found` / `context_length_exceeded` / `client_cancel` | 直接 fallback 错误响应 |

**重试防护**：
- **会话级黑名单**（executor.go:2109 `sessionBlacklist`）：同 cred 失败 ≥2 跳过
- **同步重试 ≤3 轮**（executor.go:2977 `maxSyncRetryRounds=3`，外层 +3 = 4 轮总）
- **单节点立即 probe**（executor.go:2943-2971）：`asyncDepth.Load() < 3` 防无限递归
- **客户端断开自动停**：`SyncRetryTimeout` 绑定 ctx，cancel 后自动退出

### 6.4 超时分层（stream_runtime.go:12-23）

| 阶段 | 默认 | env |
|---|---|---|
| UpstreamTimeout | 120s | LLM_GATEWAY_UPSTREAM_TIMEOUT |
| StreamTimeout | 900s | LLM_GATEWAY_STREAM_TIMEOUT |
| StreamChunkTimeout | 300s | LLM_GATEWAY_STREAM_CHUNK_TIMEOUT |
| FirstByteTimeout | 120s（2026-07-23 由 60→120） | LLM_GATEWAY_FIRST_BYTE_TIMEOUT |
| KeepaliveInterval | 15s | LLM_GATEWAY_KEEPALIVE_INTERVAL |

### 6.5 流式取消处理（2026-07-24 修复后）

- server `WriteTimeout=0`（cmd/gateway/main.go:3945 确认无 server 主动超时）
- 客户端断开主要来源：Nginx/ALB（默认 60s）+ Copilot 客户端（30-60s）
- 修复了 probe 日志（`BUGFIX-client-cancel-logging-2026-07-24.md`）：probe 记录补 RequestBody/RequestPreview/APIKeyID/LatencyMs

### 6.6 返回数据格式校验与修补策略

**检测器**（format_detector.go:76 Detect）：三路检测（UA hint → 结构匹配 → 启发式）

**修补器**（format_detector.go:308 Fix）：模式驱动，按 `FormatFix.FixFunc` 改 JSON body。典型：
- 空 choices 帧保留（`stream.go:892 shouldDropEmptyChoicesFrame`）：解析 choices=[] 仅有 usage/prompt_annotations 等保留；parse 失败 → **forward unchanged**（best-effort，不丢数据）
- 解析失败继续（`responses_bridge.go:428-439`）：`perr != nil` → `reportConversionAnomaly + continue`，**不中断流**
- OpenAI 格式被误标 → 跳过（`responses_bridge.go:419-425 isOpenAIFormatData`）

**核心策略**：**解析失败 = 警告 + 继续**（best-effort 永不丢 SSE 数据），同时上报到 `response_format_anomalies` 表。

**流式超时分类**（stream_errors.go:19-36 `classifyStreamReadError`）：
- EOF → streamReadEOF
- ctx canceled → streamReadCanceled
- 含"timeout" → streamReadTimeout
- 其余 → streamReadFailed

### 6.7 风险点

| # | 风险 | 现状 | 影响 |
|---|---|---|---|
| **R-6.1** | fatal 错误不重试，但 model_not_found 在某些情况下属于可重试（用户传错 vs 临时缺货） | 错误归类单一 | 部分请求不该失败却失败 |
| **R-6.2** | 修补器只覆盖 `empty_object / missing_field` 等几类 | 协议漂移边界修补不完整 | 跨协议格式异常时无法自动恢复 |
| **R-6.3** | 流式 best-effort 永不丢数据，但**anomaly 表是否被巡检？** | `response_format_anomalies` 写入但消费链路不明 | 异常堆积无法告警 |
| **R-6.4** | Keepalive 注释 `15s`，但 nginx/ALB 默认 60s | keepalive 频率 vs 上游超时未对齐 | 客户端看似"静默"断开 |
| **R-6.5** | sync_retry 4 轮总 + 客户端取消 ctx 可能仍然堆积 | `executor.go:2978-2986` ctx cancel | 高 cancel 场景下 executor 仍跑满 4 轮 |

### 6.8 评价

- ✅ Error Kind 7 类划分清晰
- ✅ 重试防护 4 道防线（黑名单 + 轮数 + 异步深度 + ctx）
- ✅ 超时分层 + keepalive
- ✅ 流式 best-effort 永不丢数据
- ⚠️ 修补器覆盖度有限，协议漂移需手动识别
- ⚠️ `response_format_anomalies` 消费链路不明确

---

## 7. 路由信息与节点状态并发更新

### 7.1 两套并存的状态系统

#### A. URSM v2（新一代，目标态，未切换 authoritative）
- **状态字段**：`Available / Reason / HealthStatus / FailStreak / CoolUntil / SR1m/5m/30m / LatEWMA`（`ursm/v2/api/types.go:41-76`）
- **持久化**：Redis Hash + Lua 原子写（`store/record_request.lua`）
- **三层冷却**：`SourcePriority` Seed=0 → Request=10 → Probe=20 → Recover=30 → Admin=40，**Admin 永远最优先**
- **传播**：Lua 写成功 → `NodeMirror.ApplyFromAPI` 回填进程 LRU；LRU **16 分片**（`cache/nodemirror.go:48`）每 shard 独立 mutex
- **生成单调契约**：写前必须 `cur_gen > in_gen or (== && cur_pri > in_pri)`

#### B. legacy credentialstate（现行生产）
- **状态字段**：`Available / HealthStatus / ConsecutiveFails / RecoverAt / Disabled`（`state.go:6-22`）
- **状态分类**：healthy / degraded / unreachable
- **转移路径**（`manager.go:208-495`）：
  - 永久错误连续 ≥2 → Available=false, RecoverAt=now+15min，**失效候选缓存**
  - 临时错误连续 ≥3 → Available=false, RecoverAt=now+5min
  - **免费凭据 + transient**：不冷却，仅靠 RecentSuccessRate 软降权（"50% 成功率免费凭据有总比没有强"）
  - Success：翻 Available=true, ConsecutiveFails=0
  - 探测恢复：DB Prober + ActiveProbeWorker（30s/2m/5m 退避）
- **存储**：sync.Map(memCache) + Redis(llmgw:credstate:*) + DB(node_probe_state) 三层

### 7.2 状态传播时延

| 层 | 时延 |
|---|---|
| 内存（同 goroutine） | 0（read-modify-write 已加 per-key mutex） |
| Redis（异步 goroutine + detached ctx） | ms 级 |
| 候选缓存失效 | 30s TTL，显式 `invalidateCandidateCache` 立即生效 |
| LRU 软过期 | `softTTL` 默认几分钟，Get miss 触发回源 |

### 7.3 路由信息更新

- **频次**：每个请求的成功 / 失败 → `stateBackend.UpdateOnSuccess/UpdateOnFailure`
- **写路径并发安全**：
  - legacy `cache.go:121-125 lockFor`（per-key mutex via LoadOrStore）✅
  - URSMv2 `cache/nodemirror.go:71-89` 16 shard mutex ✅
- **写频率控制**：batch_writer 缓冲 + 异步 flush

### 7.4 风险点

| # | 风险 | 现状 | 影响 |
|---|---|---|---|
| **R-7.1** | URSM v2 与 legacy 双轨运行 | 生产 ModeOff → legacy 承担 | 维护成本翻倍，状态可能不一致 |
| **R-7.2** | Admin override 永远最优先 | `api/types.go:33-39` | 紧急时 admin 可强制开/关 |
| **R-7.3** | `domain/credentialstate/popularity_tracker.go` Ticker 已修 Start/Stop 守卫 | AUDIT_HARDENING:34 已闭环 | OK |
| **R-7.4** | `domain/credentialstate/batch_writer.go` UpdateOnSuccess/Failure 持续投递 | buffer 满 / ctx cancel 路径未审计 | 高压下可能丢事件 |
| **R-7.5** | 单节点立即 probe 与 ActiveProbeWorker 双轨 | 30s/2m/5m + 5s 立即 | 可能产生探测洪峰 |
| **R-7.6** | legacy state 与 URSMv2 数据未对齐 | 切换时无数据迁移脚本 | URSMv2 切 authoritative 时丢历史 |

### 7.5 评价

- ✅ 三层存储（mem + Redis + DB）冗余
- ✅ 16 shard LRU + per-key mutex（最近大修）
- ✅ Admin 永远最优先（紧急控制能力）
- ⚠️ 双轨运行未收敛
- ⚠️ 探测洪峰与去重待验证

---

## 8. 全链路并发风险

### 8.1 历史审计成果

| 审计 | 范围 | 结论 |
|---|---|---|
| `AUDIT_CONCURRENCY_HARDENING_20260727.md` | 全仓 77 文件大修 | ✅ `go test -race` 61 包 / 0 race |
| `AUDIT_URSMV2_CONCURRENCY_20260728.md` | URSM v2 收尾 | ✅ 13 包 / 0 race |
| `AUDIT_24H_SUMMARY_20260728.md` | 24h 87 commits | ⚠️ 仍有 S-3/authoritative 纯度未闭环 |

### 8.2 已修复 P0

1. **credentialstate data race + lost-update**（AUDIT_HARDENING:18）
   - 原：`getFromMemCache` 返回共享 `*State`，并发 `ConsecutiveFails++` 既 race 又丢更新
   - 修复：copy-on-write 返回 + per-key mutex 串行化 RMW（`cache.go:36-45`、`manager.go:213-214`）
   - 回归：`manager_concurrency_test.go:27-79` 200 goroutine 0 丢失

2. **URSM v2 写入路径 TOCTOU**（AUDIT_URSMV2:38-82）
   - 原：`HGet manual_hold` + `Lua Run` 是两次独立 RTT，admin 翻转可穿透
   - 修复：把 manual_hold 读迁入 Lua 内部，消除 race window + 省一次 RTT

3. **NodeMirror 单 mutex 热点**（AUDIT_HARDENING:90-126）
   - 原：100K 容量全请求串行化在同一把锁
   - 修复：16 shard FNV-1a 分片

4. **URSM v2 Plan() ModeOff 多发 GET**（AUDIT_URSMV2:127-141）
   - 修复：入口 ModeOff 短路

### 8.3 现状并发原语使用

| 模块 | 模式 | 评价 |
|---|---|---|
| `circuit/breaker.go:98-119` | 全 atomic | ✅ |
| `circuit/window.go:39-55` | Lock + 切桶（已修 lock-held-across-I/O） | ✅ |
| `domains/credential/breaker.go:132-147` | atomic + 单 Mutex | ✅ |
| `domains/credential/breaker.go:413-433` | RWMutex double-check | ✅ |
| `domains/credentialstate/cache.go:121-125` | per-key mutex via LoadOrStore | ✅（C-1 修复后） |
| `domains/ursm/v2/cache/nodemirror.go:71-89` | 16 shard LRU | ✅ |
| `ratelimit/redis_sliding.go:104-108` | l.mu 替换 sync.Once | ✅（已修复 race） |
| `domains/streaming/executors/sticky.go:86` | RWMutex + map + sweepLoop | ✅ |
| `domains/streaming/executors/executor.go:765` | asyncDepth atomic.Int32 | ✅ |
| `domains/streaming/executors/executor.go:1578` | fpReleaseQueue chan(1024) + Once worker | ✅ |
| `pool/pool.go:82-96` | state/failCount atomic + mu 仅锁 slice | ✅ |
| `autoroute/feature_flags.go:162` | `atomic.Pointer[FeatureFlags]` | ✅ |
| `metrics/interface.go Global()` | atomic.Pointer 函数 | ✅ |
| `domains/streaming/stream_runtime.go:25` | `atomic.Pointer[config.Store]` | ✅ |

### 8.4 剩余风险

| # | 风险 | 严重度 | 说明 |
|---|---|---|---|
| **R-8.1** | URSM v2 与 legacy 双轨 | P1 | 状态机未收敛；迁移 URSMv2 期间存在两套并发模型同时维护 |
| **R-8.2** | executor.go:765 asyncDepth 单实例 singleton | P2 | 若 executor 变多实例需要重构 |
| **R-8.3** | credentialstate/batch_writer.go 高压路径未审计 | P2 | buffer 满 / ctx cancel 可能丢事件 |
| **R-8.4** | ANOMALY_REPORTER + RawDataLogger opt-in 异步线程 | P3 | 已有 sync.Once + started 守卫；opt-in 路径生产未启用 |
| **R-8.5** | 双层 sticky 选择并发安全未独立测试 | P1 | handler.go:2180+ 与 executor.go:1751+ 并发时是否一致未知 |
| **R-8.6** | autoroute.Decider 与 executor 链路并发交叉 | P1 | `ChosenCredentialID` 覆盖是否原子可见 |
| **R-8.7** | request_wal 双阶段合并并发 | P2 | 多 goroutine 同时创建 provisional + rich 行的 merge 竞争 |
| **R-8.8** | Live Stream SSE hook 与主写并发 | P3 | onPersisted post-commit；可能短窗口 hook 早于 dashboard 可见 |

### 8.5 评价

- ✅ 经过 2 轮系统性 hardening，`go test -race` 74+ 包 0 race
- ✅ 主要 P0 已闭环
- ⚠️ URSM v2 未切 authoritative，**剩余最大风险是状态机双轨**
- ⚠️ 双层 sticky + autoroute 链路并发安全性需独立验证

---

## 9. 总结与优先级

### 9.1 8 个关注点对账

| 关注点 | 现状 | 风险编号 | 优先级 |
|---|---|---|---|
| **1. 客户端识别与标记** | API key `sk-` 前缀 + singleflight + DB JOIN；标记体系清晰 | R-2.1~R-2.4 | 低（基础已完善） |
| **2. 请求记录与双写** | 8 个写入点，主写强一致 + 镜像 fire-after-commit | R-3.1~R-3.6 | **高**（OTel 未启用 + trace_id 列缺失） |
| **3. 请求格式转换** | IR 3 层 + json.RawMessage + Extensions 透传 | R-4.1~R-4.7 | 中（跨协议丢字段） |
| **4. 路由/数据转发** | 5 层路由 + 4 种协议转换；线处理基本完整 | R-5.1~R-5.6 | **高**（双层 sticky + v2 flag 不明） |
| **5. 异常处理** | 7 类 errorsx.Kind + 4 道重试防护 | R-6.1~R-6.5 | 中 |
| **6. 返回格式检查** | 解析失败 best-effort + anomaly 表 | R-6.2~R-6.3 | 中（修补器覆盖度有限） |
| **7. 路由信息/节点状态并发** | 三层存储 + Admin override 最高优先级 | R-7.1~R-7.6 | **高**（双轨未收敛） |
| **8. 全链路并发** | 2 轮大修完成，P0 闭环 | R-8.1~R-8.8 | **高**（URSM v2 + 双层 sticky 待验证） |

### 9.2 P0（必须解决，1-2 周）

1. **R-3.1 / R-3.2 OTel 启用 + trace_id 列**：反查能力补齐
2. **R-3.3 / R-3.4 onPersisted hook + RingBuffer 失败 metric**：让丢行可观测
3. **R-7.1 / R-8.1 URSM v2 切换 plan**：定时间表 + 数据迁移脚本
4. **R-5.1 / R-8.5 双层 sticky 统一**：明确单一决策点
5. **R-5.2 v2 Pipeline flag 状态确认**：文档化或启用

### 9.3 P1（建议改进，1 个月）

1. **R-4.1 `parallel_tool_calls` 跨协议映射**
2. **R-4.3 流式 tool_calls 累积统一到 IR 层**
3. **R-4.5 `validateToolCallIntegrity` 扩展 1-2 message 场景**
4. **R-6.2 修补器协议漂移覆盖度**
5. **R-2.3 session ID 落库完整性保障**

### 9.4 P2（优化，季度内）

1. **R-2.4 KeyInfo 增加 key 来源标签**
2. **R-3.6 原始 audit JSONL 跨机同步**
3. **R-4.6 unified adapter 清理**
4. **R-4.7 OpenAI Realtime API 覆盖**
5. **R-5.6 StreamWrapper 字段清理**
6. **R-7.6 URSM v2 数据迁移脚本**

### 9.5 P3（探索，半年）

1. `response_format_anomalies` 自动告警链路
2. AI 驱动格式异常检测

---

## 10. 自审：与老板关注点对齐情况

### 10.1 八大关注点覆盖检查

| 老板关注点 | 本审计对应章节 | 评价 |
|---|---|---|
| 客户端的识别与标记，key用用户识别，需要与请求标记 | §2 | ✅ 覆盖 API key 解析 + 5 类请求标记 |
| 请求记录，只要收到请求就要记录，要全程 log 并进行详细的记录，确定可以将来快速跟踪问题。并且注意双写的情况及逻辑正确性 | §3 | ✅ 覆盖 8 个双写点 + 6 项风险 |
| 客户端请求的格式转换与供应商模型的格式转换，确认成功正确，需要与官网的格式进行核对，不要丢失或转换信息，特别对 tools 相关的检查，要支持单个、多个 tools 和不同的格式。对 json 的解析需要兼容性强 | §4 | ✅ 覆盖 IR 架构 + tools 处理 + 易丢字段表 |
| 请求路由及数据转发的多个流程及过程中的处理，不含模块的线处理是否完整且正确，不要在过程中丢失数据 | §5 | ✅ 覆盖 5 层路由 + 6 项线处理检查 |
| 异常的处理及格式正确的处理 | §6 | ✅ 覆盖 7 类 error kind + 重试防护 + 修补策略 |
| 对返回的数据格式进行检查，并确定是要继续等待还是修补 | §6.6 | ✅ 覆盖 best-effort + anomaly 表 + 修补器 |
| 路由信息与节点状态信息的完整性及可用性，更新的方式及正确及时，特别是并发的处理 | §7 | ✅ 覆盖两套状态系统 + 传播时延 + 6 项风险 |
| 全面检查整个链路过程中的并发处理，确保不会因为多线程导致数据出错 | §8 | ✅ 覆盖 2 轮审计 + 14 个并发原语检查 + 8 项剩余风险 |

### 10.2 输出产物

- **本分析报告**：`docs/audit/2026-07-28-llm-gateway-flow-comprehensive-audit.md`
- **配套实施方案**：`docs/design/2026-07-28-llm-gateway-flow-improvements.md`（另文）

### 10.3 下一步建议

1. 老板 review 本报告 + 实施方案
2. 决策 P0 项目是否纳入排期（建议）
3. URSM v2 切换 authoritative 的时间表 + 风险评估
4. P1 项目按季度排期
5. 半年后再次审视 P2/P3

---

## 附录 A：完整 file:line 索引

### A.1 入口与主干
- `cmd/gateway/main.go:131` — main()
- `cmd/gateway/main.go:3500-3517` — mux 路由注册
- `cmd/gateway/main.go:4182-4191` — 中间件链
- `cmd/gateway/main.go:3543-3549` — v2 Pipeline 覆盖
- `cmd/gateway/main.go:1350` — chatHandler.SetExecutor
- `cmd/gateway/main.go:860-884` — StreamChat 闭包注入
- `domains/streaming/handler.go:912` — ChatHandler.ServeHTTP
- `domains/streaming/handler.go:1188` — serveWithExecutor
- `domains/streaming/handler.go:5080` — extractBearerToken
- `domains/streaming/handler.go:976` — ensureSessionID
- `domains/streaming/executors/executor.go:1666` — Executor.Execute
- `domains/streaming/executors/executor_chat.go:503` — 上游 HTTP 出口
- `domains/streaming/executors/executor_anthropic.go:528` — Anthropic 协议出口
- `domains/streaming/stream.go:349` — StreamChatWithPendingCaptureAndDiagnostics

### A.2 认证与 KeyInfo
- `domains/authentication/verifier.go:167` — NewKeyVerifier
- `domains/authentication/verifier.go:206` — Verify
- `domains/authentication/verifier.go:300` — hashAPIKey

### A.3 中间件
- `middleware/requestid_mw.go:41` — RequestIDMiddleware
- `middleware/requestid_mw.go:46` — generateRequestID
- `middleware/logging_mw.go:66` — LoggingMiddleware
- `middleware/logging_mw.go:94-111` — 字段

### A.4 IR 与格式转换
- `internal/ir/detect.go:30` — DetectProtocol
- `internal/ir/parse_openai.go:14` — ParseOpenAI
- `internal/ir/parse_openai.go:432-493` — parseOpenAITools
- `internal/ir/parse_anthropic.go:9` — ParseAnthropic
- `internal/ir/parse_anthropic.go:507-565` — parseAnthropicTools
- `internal/ir/serialize_openai.go:9` — SerializeOpenAI
- `internal/ir/serialize_openai.go:457-482` — serializeOpenAITools
- `internal/ir/serialize_anthropic.go:9` — SerializeAnthropic
- `internal/ir/serialize_anthropic.go:618-667` — sanitizeInputSchema
- `internal/ir/response.go:128` — ParseAnthropicResponse
- `internal/ir/response.go:235` — ParseOpenAIResponse
- `internal/ir/stream.go:140` — ParseOpenAIStreamChunk
- `internal/ir/stream.go:295-318` — 流式 tool_calls 增量
- `domains/transformation/ir_converter.go:54` — TransportIRConverter

### A.5 日志与 WAL
- `logging/logger.go:55-106` — zap 包装
- `logging/logger.go:148-153` — WithContext trace_id 注入
- `telemetry/client.go:117-227` — DB 请求字段
- `telemetry/client.go:460-505` — EmitRequestLog
- `telemetry/client.go:716+` — insertRequestLog
- `telemetry/client.go:1541` — insertRequestBody
- `telemetry/request_logger.go:128+` — RequestLogger WAL
- `internal/logging/async_raw_logger.go:127+,149-175` — AsyncRawDataLogger
- `internal/logging/raw_data_logger.go:67,256` — TraceID 字段
- `internal/attachmentmirror/hook.go:35` — request_attachments hook
- `internal/sessionv2mirror/hook.go:36` — sessions_v2 hook
- `domains/dbdegradation/multi_writer.go:41+` — MultiBackupWriter
- `domains/dbdegradation/fallback.go:76+` — FileWriter
- `domains/dbdegradation/ring_buffer.go:84` — RingBuffer cap 10000

### A.6 路由与状态
- `domains/streaming/handler.go:2057` — resolveCandidatesForRequest
- `domains/streaming/candidate_modality.go:11` — 实现
- `provider/client.go:394` — GetCandidatesByModality
- `provider/client.go:1157-1198` — SQL JOIN + 解密
- `domains/streaming/executors/router.go:93` — PlanCandidatesWithContext
- `domains/streaming/executors/router.go:175-176` — FilterAvailable
- `domains/streaming/executors/router.go:270-272` — prioritizeSticky
- `domains/streaming/executors/router.go:450-512` — splitByBillingRound
- `domains/streaming/executors/router.go:575-613` — filterHealthyNodes
- `domains/streaming/executors/router_scoring.go:43` — calculateLoadScore
- `domains/ursm/v2/manager.go:189` — FilterAndScoreReady
- `domains/ursm/v2/manager.go:200` — filterAndScore
- `domains/ursm/v2/manager.go:441` — record_request
- `domains/ursm/v2/cache/nodemirror.go:48` — 16 shard
- `domains/ursm/v2/cache/nodemirror.go:71-89` — mutex
- `domains/credentialstate/manager.go:208-495` — 状态转移
- `domains/credentialstate/cache.go:121-125` — per-key mutex
- `domains/credential/breaker.go:132-147` — atomic + Mutex
- `domains/credential/breaker.go:413-433` — RWMutex

### A.7 异常与重试
- `errorsx/classify.go:14-78` — Kind 分类
- `errorsx/classify.go:373-391` — ClassifyResponseStatus
- `domains/streaming/executors/executor.go:2109` — sessionBlacklist
- `domains/streaming/executors/executor.go:2903-2930` — 重试策略
- `domains/streaming/executors/executor.go:2943-2971` — 单节点 probe
- `domains/streaming/executors/executor.go:2977` — maxSyncRetryRounds=3
- `domains/streaming/executors/executor.go:2978-2986` — ctx cancel

### A.8 超时与流式
- `domains/streaming/stream_runtime.go:12-23` — 超时分层
- `domains/streaming/format_detector.go:76` — Detect
- `domains/streaming/format_detector.go:308` — Fix
- `domains/streaming/stream_errors.go:19-36` — classifyStreamReadError
- `domains/streaming/stream.go:892` — shouldDropEmptyChoicesFrame
- `domains/streaming/anthropic_stream.go:364-420` — OpenAI→Anthropic 累积
- `domains/streaming/responses_bridge.go:428-439` — 解析失败继续
- `domains/streaming/responses_bridge.go:567-590` — OpenAI→Responses 累积
- `domains/streaming/responses_bridge.go:659-661` — [DONE] sentinel

### A.9 并发原语
- `circuit/breaker.go:98-119` — 全 atomic
- `ratelimit/redis_sliding.go:104-108` — l.mu
- `domains/streaming/executors/sticky.go:86` — RWMutex
- `domains/streaming/executors/executor.go:765` — asyncDepth atomic
- `domains/streaming/executors/executor.go:1578` — fpReleaseQueue chan
- `pool/pool.go:82-96` — atomic + mu
- `autoroute/feature_flags.go:162` — atomic.Pointer
- `metrics/interface.go` — atomic.Pointer
- `domains/streaming/stream_runtime.go:25` — atomic.Pointer

---

**报告字数**：约 9500 字  
**审计完成时间**：2026-07-28  
**下次审视建议**：1 个月后（URSM v2 切换进度 + P0 闭环情况）
