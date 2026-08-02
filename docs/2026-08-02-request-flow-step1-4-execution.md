# Request Flow 收敛执行总结（Step 1–4）

> 状态：2026-08-02，spec 修正版落地进展（Step 1 → Step 4.5 已合并到 origin/main）。
> 关联 spec：docs/superpowers/specs/2026-07-27-request-flow-audit-design.md
> 关联 plan：docs/superpowers/plans/2026-07-27-routing-state-convergence.md

## 总体目标

按 spec §10 在不重写 ChatHandler、不触碰 `gateway-new/cmd/gateway-v2`、不引入新设计的前提下，按依赖顺序完成 Step 1 → Step 6 的收敛。本次会话完成 Step 1–4.5。

## 关键提交（origin/main 历史）

| Commit | 范围 | 关键点 |
|---|---|---|
| `6cc82f38` | Step 1 记录可靠性 | L-1/L-2 WAL 与 logs 终态 guard；RequestLogger 队列/flush/Stop 幂等；overflow marker；主 shutdown 顺序 |
| `674fcf8c` | Step 2 统一入口与终态 | 共享 `InitializeRequestIdentity`；`WithSession` `X-Gw-Session-Id` 优先；`RequestLogContext.SetTerminal` 终态 CAS |
| `f7cee3c6` | Step 3 Session V2 单 owner | `SessionPersistHook` 退化为 no-op；唯一 owner 仍为 telemetry onPersisted；TurnWriter 重复 request_id 返回真实 turn_no；SessionAggregator 按 request_id 去重；DBWriter.Stop 幂等 + drain |
| `3853ec67` | Step 4.5 F-5 流式 JSON 拼装器 | 并发安全 `ToolArgumentsAssembler`；`StreamChunk.AnnotateArgumentsJSON` 钩子；Anthropic bridge `content_block_stop` 阶段跑 `json.Valid` 校验 |

## 各步骤验收状态

### Step 1 记录可靠性（spec §4.2 / §5.2 / §10）

- ✅ WAL `CreateInitial` 前移到 request_id/session 派生后、KeyVerifier.Verify 前，覆盖 Chat/Messages/Responses/Gemini 四入口的 pre-routing failure；保留 `Provisional` 标记，最终 session 解析后 WAL merge。
- ✅ `request_logs_hot` 与 `request_wal_hot` 终态 guard 对齐：insert/update 0 rows 后区分终态 no-op 与 missing 才 fallback insert；Logs 失败不覆盖 success/failure。
- ✅ RequestLogger：
  - 队列满先 fallback 写原始 LogUpdate，失败才 marker；
  - marker 脱敏并稳定识别（`request_logger:overflow:<requestID>` + `request_logger_overflow` kind）；
  - `flushBatch` begin/row/commit 三种失败均把整批转 fallback（包含批内中间行 + 当前行）；
  - `Stop()` 幂等（`sync.Once`）并通过 `lifecycleMu` 覆盖同步写路径；
  - 主 shutdown 在依赖关闭前调用 `RequestLogger.Stop()`；
  - stats：queueOverflow / fallbackWriteFailure / unrecoverableFallback / replayAttempt/Success/Failure/Marker。

### Step 2 统一入口与终态（spec §4.1 / §5.1 / §10）

- ✅ 共享身份 helper：`InitializeRequestIdentity` 派生 request_id/client_request_id/provisional session_id 并镜像写回响应头；私有 `initializeRequestIdentity` 继续供 Messages/Responses/Gemini sub-handler 使用。
- ✅ v2 wrapper session header（`domains/session/middleware.go` `WithSession`）：`X-Gw-Session-Id` 优先，`X-Session-Id` 退为 legacy；合法命中分支写回 `X-Gw-Session-Id-Resume`（G-ID-1）。
- ✅ G-ID-2 fallback requestID 写回 header：四入口均在 handler 内显式写 `X-Request-Id`。
- ✅ Middleware 拒绝路径：现有 `AuthMiddleware` 401 路径已从 RequestIDMiddleware 上下文回写 `X-Request-Id`，符合 §4.2 “middleware 拒绝仅 logs + audit，不强制 WAL”。
- ✅ 终态原子门：`RequestLogContext.SetTerminal` 提供 success/failure/disconnect 三路竞争单 winner 接入点；后续 round 接 emit sites。

### Step 3 Session V2 单 owner（spec §6）

- ✅ §6.1 唯一写入 owner：
  - `SessionPersistHook` 退化为 no-op shell；
  - 删除 `EventPublisher`/`SessionV2Event` 抽象与生产链装载；
  - 唯一写入 owner = `telemetryClient.AddOnRequestLogPersisted(sessionv2mirror.PersistHook(sessionV2Writer))`（`cmd/gateway/main.go:1595`）。
- ✅ §6.2 幂等：
  - `TurnWriter.AppendTurn` request_id 重复返回真实 turn_no（依赖 `ON CONFLICT (request_id, partition_date) DO NOTHING` + 冲突后再读）；
  - `SessionAggregator` 非空 `RequestID` 路径 probe `session_turns` 去重，避免累加 token/turn/cost；
  - 空 `RequestID` legacy backfill 路径保留。
- ✅ §6.3 Shutdown 与失败暴露：
  - `session.DBWriter.Stop` 幂等（`sync.Once`），`runFlushLoop` 退出路径共享 `FlushAll` 收尾；
  - 主 shutdown 顺序：`producers → requestLogger → telemetryClient → sessionDBWriter → lim/pools`，DBWriter 在 pool 关闭前完成 drain；
  - mirror 失败路径记录 `request_id/session_id/error` + `shadow_write_failed_total{kind="session_v2"}`。
- ⏳ 后续 round（已记录在 commit message）：
  - turn/body/stage/aggregate 同事务化或可重试；
  - mirror backlog/replay 队列 metrics；
  - aggregator 同 key race 的真实集成测试（`TEST_DB_URL`）。

### Step 4 IR 默认与协议验收（spec §7 / §10）

- ✅ 4.1 默认开关：保持 `TRANSPORT_LAYER_IR_ENABLED=false`，由环境变量与白名单/百分比灰度控制；本次不切默认。
- ✅ 4.2 F-1 IR `ToolDefinition` `Type/Raw` 已存在（先前 commits）。
- ✅ 4.3 F-2 `stream_options` 通过 ExtensionsBag 旁路（`fields.go:11` 已移除）。
- ✅ 4.4 F-3 Anthropic `tool_choice:"any"` → OpenAI `"required"`（`serialize_openai.go:498`）。
- ✅ **4.5 F-5 流式 JSON 拼装器**（本次新增）：
  - 并发安全 `ToolArgumentsAssembler`（`internal/ir/tool_arguments_assembler.go`）：`Append/Finalize/Reset`；
  - 对象/数组/嵌套/Unicode/转义字符串分片拼接；安全补齐 `}`、`]`、字符串结束引号；非法 JSON 返回 `ErrInvalidJSON` 与 reason；
  - `StreamChunk.AnnotateArgumentsJSON` 钩子：暴露 `Quality(verified/partial/rejected)` 与 `ArgumentsJSONReason`，不改变 SSE wire format；
  - Anthropic bridge 在 `content_block_stop` 阶段跑 `json.Valid`，异常/修补时记录结构化 warning，保留既有 `ValidateStreamingToolArgs` 阻断语义。
- ✅ 4.6 E-1 Anthropic 错误信封（`writeErrorAnthropic`，`isAnthropicMessagesPath` 分发）。
- ✅ 4.7 D-2 Anthropic 4xx body >4096B（`executor_anthropic.go:815-903` 读全转发）。
- ✅ 4.8 D-1 empty-choices 谓词（`shouldDropEmptyChoicesFrame` 保留 usage/prompt_annotations/prompt_filter_results）。
- ⏳ 4.9 全协议 fixture round-trip：待 Step 4 round 2。
- ⏳ 4.10 跨协议 anomaly/loss reason：待 Step 4 round 2。
- ⏳ 4.1 切换 IR 默认：待 Step 6 全链路验收后再切。

## 质量门禁（已运行）

```text
go test ./domains/streaming ./domains/credentialstate ./domains/ursm/v2 -race
go test ./domains/hooks/observability/telemetry -race
go test ./domains/session ./domains/session/v2 ./internal/sessionv2mirror -race
go test ./internal/ir -race
go test ./domains/transformation -count=1
go build ./...
```

全部通过。`git diff --check` 通过。

## 与原 spec 的差异声明（commit message 已记录）

- 本次会话未修改 `gateway-new/cmd/gateway-v2`、未重写 `ChatHandler` 主流程、未发明 spec 之外的新设计。
- 跨协议事务化 turn/body/stage/aggregate（spec §6.2 同事务或整体可重试）属于更深层架构改造，留待后续 round。
- aggregator 同 key race 的真实集成测试依赖真实 PG，已在 Step 3 commit message 中声明延后。
- IR 默认切换（spec §10.4.1）按“本次保守不切默认”边界未触发，待 Step 6 验收后再切。