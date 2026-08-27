# 请求记录、IR 与 Session V2 持久化重构最终方案

> 状态：代码审计后方案版
> 日期：2026-08-26
> 范围：请求事实、IR/响应结构、request_logs 与 session_turns 双写、正文存储、Redis 队列/缓存、统计投影、durable PG 恢复、迁移与验证。

## 1. 目标

建立清晰、可恢复、可对账的请求保存体系：

```text
请求入口
  -> IR/运行期上下文归一化
  -> request_logs 主事实事务
  -> durable projection event
  -> session_turns/session_bodies 投影
  -> stats/Redis/看板投影
```

核心原则：

- `request_logs` 短期继续作为请求审计主事实；
- `session_turns + session_bodies` 是会话投影，不能因 shadow write 失败阻塞业务请求；
- `durable` PostgreSQL 是跨进程、跨重启的请求执行快照和终态恢复源；
- Redis 不保存完整 request/response body、IR 或 opaque `QueuedRequest.Payload`；
- 所有投影都必须可重放、幂等、有状态、有告警；
- schema、代码、迁移、installer、视图和真实部署版本必须同一契约；
- 不在一个阶段同时更改事实 owner、表结构、读路径和部署开关。

## 2. 当前真实结构

### 2.1 HTTP/Transport 到 IR

```text
HTTP body + headers
        |
        v
TransportContext
  - HTTP request/writer
  - raw BodyBytes
  - client/upstream protocol
  - model/provider/catalog IDs
  - ExtensionsBag
        |
        v
IRTransport.Convert
        |
        +-- ParseOpenAI / ParseAnthropic / ParseGemini / ParseResponses
        |       |
        |       v
        |   InternalRequest
        |     - Model
        |     - SystemPrompt
        |     - Messages[]
        |         - ContentBlock[]
        |           - text/image/audio/video/document/input_audio
        |           - tool_use/tool_result/thinking/redacted_thinking
        |           - cache_control/index/RawContent
        |         - ToolCalls[] / ToolChoice
        |     - sampling/reasoning/thinking
        |     - provider-specific fields
        |     - Extensions
        |     - TargetProvider
        |
        +-- Serialize target provider
                |
                v
        upstream HTTP request

upstream response / SSE
        |
        v
Parse response / stream
        |
        +-- InternalResponse
        |     - content blocks
        |     - tool calls/raw input
        |     - reasoning/thinking/signature
        |     - finish reason
        |     - usage/cache/multimodal tokens
        |     - Extensions
        |
        +-- StreamChunk / StreamDelta / StreamUsage / StreamError
                |
                v
        Serialize client protocol
```

真实代码：

- `internal/ir/types.go:34-191`：`InternalRequest`。
- `internal/ir/types.go:219-279`：`Message`、`ContentBlock`。
- `internal/ir/types.go:356-427`：tool use/tool call/tool result/tool definition。
- `internal/ir/response.go:19-123`：`InternalResponse`、`ResponseUsage`。
- `internal/ir/stream.go:20-145`：流式 IR。
- `domain/transport.go:7-56`：`TransportContext` 与扩展字段。
- `domains/transformation/ir_transport.go:124-607`：请求、响应、SSE 转换。
- `internal/irconv/converter.go:7-25`：统一 Converter 接口。

### 2.2 运行期会话复合快照

`domains/session/context.go:35-59` 同时保存：

```text
ClientRawBody
ClientIR
UpstreamBody
UpstreamIR
LLMRawResponse
LLMResponseIR
ClientResponseIR
AttachmentMetadata
```

所以一个可恢复请求不是单一 body，而是客户端输入、归一化 IR、实际上游请求、上游响应、客户端响应、流式摘要、附件和路由/压缩扩展的组合。

### 2.3 Session V2 结构

生产 V2 owner 是 `sessionv2mirror.PersistHook`，当前并不是直接保存 `InternalRequest`：

```text
telemetry.RequestLogEntry
        |
        v
entryToProcessedRequest
        |
        +-- request/response/outbound raw JSON 重新解析
        +-- IRMessagesFromJSON
        +-- IRMessagesToV2
        +-- attachment JSON -> AttachmentRef
        |
        v
v2.ProcessedRequest
        |
        +-- TurnRecord -> session_turns_hot/session_turns
        +-- BodiesRecord -> session_bodies
        |     - request_delta
        |     - response_delta
        |     - outbound_body
        |     - attachment refs
        +-- session_turn_logs（事务外 best-effort）
        +-- sessions snapshot（异步 aggregate）
```

代码：

- `internal/sessionv2mirror/hook.go:46-127,129-250,283-371`。
- `domains/session/v2/ir_message_adapter.go:39-70,103-212,343-408`。
- `domains/session/v2/session_writer_v2.go:233-396,398-475`。
- `domains/session/v2/bodies_writer.go:44-176,179-331`。
- `domains/session/v2/outbound_builder.go:40-138`。

`v2.Message` 通过 dual-shape contract 兼容旧 string content，同时使用 `$ir` envelope 保存多模态、工具调用、thinking、signature、provider raw 等内容。该 adapter 是持久化边界，不能被新的业务 DTO 直接绕过。

### 2.4 URSM v2 的边界

实际目录是 `domains/ursm/v2`，不是 `usrm/v2`。URSM 是路由健康/容量状态面：

```text
NodeView
  - provider/credential/raw_model/canonical/tenant
  - available/reason/health/fail streak/cool
  - success windows/latency/score/capacity
  - generation/source priority

RequestOutcome
  - credential/raw model/canonical/tenant
  - success/latency/error/request ID
  - dedup key/terminal/billing/health/admin hold
```

代码：`domains/ursm/v2/api/types.go:77-149`、`domains/ursm/v2/manager.go:325-577,700-1101`、`domains/ursm/v2/store/record_request.go:26-160`。

URSM 不保存 prompt、response、IR 或完整 session。请求前它参与候选过滤/排序，请求后记录健康结果。URSM Redis dual schema 是状态键迁移，不是 request_logs/session_turns 双写。

## 3. 数据层与职责矩阵

| 层 | 代表结构 | 保存内容 | 权威性 | 丢失后的恢复 |
|---|---|---|---|---|
| 运行期内存 | `SessionContext`、telemetry entry、dispatch `QueuedRequest` | 当前请求元数据、阶段指针、短时 IR | 进程内 | 不可跨重启恢复 |
| 本机归档文件（目标新增） | `RequestArchiveEnvelope` | 未完成/处理中/终态待归档请求的完整内容 | 节点本地恢复源 | 启动扫描重入归档 |
| 请求审计 | `request_logs_hot` / `request_logs` | request/session/tenant/status/routing/usage/timeline 元数据 | 短期主事实 | 数据库查询/分区归档 |
| 正文侧表 | `request_logs_bodies_hot` / `request_logs_bodies` | request/response/outbound JSONB 与 body references | 正文历史事实 | 数据库查询 |
| Session V2 元数据 | `session_turns_hot` / `session_turns` | turn、status、usage、routing、compression、quality、T0-T9 | 会话投影事实 | 重放 projection |
| Session V2 内容 | `session_bodies` | request_delta、response_delta、outbound、attachment refs | 会话内容投影 | 重放/历史重建 |
| Session 聚合 | `sessions` | turn/cost/token/last summary snapshot | 可重建投影 | 从 turns 重建 |
| Durable PG | `durable_llm_tasks` | 加密请求 snapshot、lease、checkpoint、终态结果 | 跨重启执行事实 | recovery worker |
| Redis 执行队列 | systemmonitor lists/hash/lease | 任务调度元数据、租约、attempt、诊断 | 执行调度状态 | reclaim/fallback PG |
| Redis 观测投影 | QueueMirror、boardcache、minute stats、body-size | depth、inflight、retry_at、聚合摘要 | 非权威、可丢 | PG baseline/rebuild |
| 统计事实 | `stats_event_inbox`、`usage_facts` | body-free terminal event、token/cost/status | 统计事实/重放入口 | inbox retry/DLQ/replay |

## 4. 当前确认的问题

### P1：V2 mirror failure 无 durable 补偿

`internal/sessionv2mirror/hook.go:104-124` 失败只进 `backlog.go:16-25,96-145` 的进程内 FIFO，重启或超过 10000 条会丢失。V1 事务成功但 V2 缺行无法自动恢复。

### P1：fallback/WAL replay 可能只补 V1

正常 `onPersisted` 只在 `client.go:874-905` 主写成功后触发；fallback/replay 直接调用底层 insert/update 时可能绕过 hook。必须统一成 post-persist dispatcher，确保 replay 同时触发 V2 与统计投影，且按 request ID 幂等。

### P1：V2 正文转换失败可能静默变空

`parseProtocolMessages` 多处返回 nil；`safeJSONMarshal` 在失败时返回空数组/对象；`SessionWriterV2.Write` 在读取 previous body 失败后仍继续按无历史计算 delta。相关代码：`internal/sessionv2mirror/hook.go:283-340`、`domains/session/v2/bodies_writer.go:199-226`、`domains/session/v2/session_writer_v2.go:263-287`。

### P1：V2 后置投影只日志失败

turn+bodies 同事务，但 turn logs 在 `session_writer_v2.go:398-429` 独立 best-effort，sessions aggregate 在 `:431-500` 仅有限重试，无 durable child projection。

### P1：SystemMonitor fallback 和 requeue 不闭环

- fallback task ID 可能为 0，违反 `V352` 的唯一约束，导致多任务丢失；
- fallback JSON 与 Lua `*_at_ms` payload 不一致；
- Requeue 多步非原子；
- reclaim 后到新 owner claim 前存在 fencing 空窗；
- lease 无 heartbeat；
- malformed payload 无 DLQ；
- fallback drain 的 PG→Redis 双写可能重复。

关键代码：`bg/systemmonitor/monitor.go:263-413`、`bg/systemmonitor/redis_queue.go:90-317`、`bg/systemmonitor/lua/*.lua`。

### P1：EventWriter 失败最终丢事件

`domains/stats/event_writer.go:72-100,103-158` queue 满时同步 fallback，连续失败后只增加内存 deadLettered 并清空 batch。真正 durable DLQ 在 InboxConsumer，但 EventWriter 本身在 PG 长故障时未必能把事件送入 inbox。

### P1：stats revision/correction 未闭环

`usage_facts` schema 允许 revision，但生产写入固定 revision=1，EventID 固定 terminal ID；rollup/reconciliation 必须共享 latest revision 选择规则。相关代码：`domains/stats/event.go:106-165`、`domains/stats/inbox_consumer.go:504-527`、`domains/stats/daily_monthly_rollup.go:194-200`、`domains/stats/reconciliation.go:192-297`。

### P1：schema/部署契约漂移

近期 request_logs 事故已证明 migration、view、hot/parent 分区和运行二进制漂移会直接导致主写失败或 promotion 丢数据。必须遵守 `docs/standards/database-change-and-real-verification.md` 的 schema truth、显式列清单、原子搬运、真实 API 和版本确认流程。当前未注册的迁移文件不得视为已部署。

## 5. 推荐目标架构

### 5.1 CanonicalRequestFact

请求终态只构造一次版本化保存事实，不直接等同于任意数据库表：

```text
identity: tenant/request/session/turn/task/parent
lifecycle: status/success/error/deadline/created/started/completed
routing: client/outbound/provider/credential/canonical
request: raw body + InternalRequest + protocol/extensions
upstream: serialized body + outbound IR/extensions
response: raw response + InternalResponse + stream summary/chunks
content: messages/tool calls/tool results/thinking/signature/multimodal
usage: token/cost/cache/reasoning/provider dimensions
timeline: T0-T9/latency/TTFT
extensions: compression/submit/governance/attachments/URSM outcome
quality: payload_version/conversion_path/serialization warnings
integrity: payload_hash/body_hash
```

对外提供：

- `ToRequestLogProjection`
- `ToSessionTurnProjection`
- `ToSessionBodyProjection`
- `ToStatsEvent`
- `ToRedisMetadataProjection`
- `ToDurableTaskSnapshot`

所有 mapper 返回 `projection + error/warning`。核心 IR 或正文失败不得转为空 JSON。

### 5.2 LocalRequestArchive

目标新增本机层：

```text
ActiveRequestRegistry（内存）
  request_id -> tenant/session/status/file path/persist state

LocalRequestArchive（文件）
  <request_id>.json
  - schema/payload version
  - ClientRawBody/ClientIR
  - UpstreamBody/UpstreamIR
  - LLMRawResponse/LLMResponseIR
  - ClientResponseIR
  - stream summary/T0-T9/usage/error
  - attachments/extensions
  - archive state/attempts/last error/hash
```

写入采用 tmp + fsync + rename；启动扫描 active/terminal-pending；损坏文件进入 quarantine；只有数据库确认归档成功后清除文件和内存元数据。

本机文件只负责节点内活跃请求和终态待归档，不替代 durable PG，也不承诺跨节点故障恢复。

### 5.3 Durable projection outbox

在 request_logs 主事务中写入 body-free projection event：

```text
projection_event_id
request_id / tenant_id / session_id
projection_kind / payload_version / payload_hash
payload 或本地文件引用
status / attempts / available_at / lease_until / worker_id / last_error
processed_at / dead_lettered_at
```

worker claim -> lease -> 投影 -> ack；失败 retryable，超过上限进入 DLQ。`onPersisted` 只保留实时通知/cache invalidation，不再承担可靠 V2 写入。

### 5.4 Redis 与 durable PG

统一区分：

1. SystemMonitor Redis：调度元数据 + processing/lease/reclaim，payload 不带完整 body/IR。
2. durable PG：完整加密 request snapshot/terminal result，跨重启执行恢复。
3. Dispatch QueueMirror：只做 depth/inflight/retry_at 观测，丢失只影响看板。
4. pending/boardcache/minute/body-size：短期结果或统计投影，Redis 丢失可回源/重建。

## 6. 分阶段实施

### Phase 0：契约冻结与字段矩阵

1. 固定 owner：telemetry=request_logs；V2=单一 mirror owner；URSM=健康状态；stats=EventWriter/Inbox；Redis mirror=非权威。
2. 建立字段矩阵：`TransportContext -> InternalRequest/Response/StreamChunk -> SessionContext -> RequestLogEntry -> ProcessedRequest -> TurnRecord/BodiesRecord -> stats/durable/Redis`。
3. 标注 required/optional/derived/raw-preserved/loss-reported，并固定 payload/codec/projection event version。
4. 建立 V1/V2/body/stats drift SQL、指标和 failure matrix，不改变线上行为。
5. 做 migration/installer/deploy manifest inventory；603/604 或未注册 migration 只可列为待审核，不能直接执行。

### Phase 1：统一 codec

1. 复用现有 `internal/ir` parser/serializer、session v2 dual-shape adapter、durable snapshot 加密边界。
2. 新增 `RequestArchiveEnvelope` codec，保存 raw bytes、IR、extensions、标准化 messages、hash、版本和 warning。
3. golden tests 覆盖 text/multimodal/tool/thinking/signature/provider Raw/unknown block/SSE。
4. parser/marshal failure 返回结构化错误；可选字段丢失记录 warning，核心正文进入 retry/DLQ。

### Phase 2：本机活跃内存+文件

1. registry 只存元数据和路径；完整内容写本机文件。
2. 请求开始、IR/upstream/response/stream terminal 变化时更新快照。
3. 启动恢复未完成和 terminal-pending 文件。
4. DB 成功确认后清理；清理失败进入 cleanup_pending。
5. 覆盖磁盘满、损坏文件、权限失败、并发写、重复恢复和 shutdown。

### Phase 3：Outbox 驱动 V2 与派生投影

1. request_logs 主事务写 projection outbox。
2. worker 使用 claim/lease/retry/DLQ/replay。
3. V2 turn+bodies 继续同事务，保留 tenant/session/request advisory lock 和 aggregate claim。
4. replay/WAL/本机归档统一进入 post-persist dispatcher，避免只补 V1。
5. body read failure 不按空历史继续；进入 partial/retry/DLQ。

### Phase 4：Redis 队列可靠性

1. fallback task ID 改为稳定唯一；fallback payload 统一使用 Lua queue schema。
2. Requeue 改为单 Lua 状态迁移；补严格 fencing、heartbeat、DLQ。
3. fallback drain 使用可重复投递 + Redis 幂等键或 PG drain state。
4. pending pipeline/sweeper 改为 Lua/CAS 或 owner/version 条件更新。
5. QueueMirror 保持 observation-only。

### Phase 5：Stats 收敛

1. EventWriter 不再以进程内 deadLettered 代替 durable DLQ。
2. correction 事件携带 revision，定义 latest facts view/CTE。
3. rollup/reconciliation 统一 latest revision 口径。
4. 修复 phantom/missing diff/finishRun partial 状态。
5. 接入 ReplayDLQ 管理入口、指标和告警。

### Phase 6：Schema/cutover

1. schema truth preflight：parent/hot/default/month/view/function/trigger/index/RLS/Go Scan。
2. 迁移、down migration、installer embed/copy、checksum 和 deploy manifest 同步。
3. 修复 tenant unique/conflict target/reader filter。
4. session_bodies 可写分区必须 heap；promotion 使用显式列清单+原子 CTE+错误上抛。
5. expand -> backfill -> dual-read -> cutover -> rollback window -> contract。

## 7. 测试验收

### IR/codec

- 四协议请求/响应和 SSE round-trip；
- multimodal/tool/thinking/signature/provider Raw/Extensions 不丢失；
- archive version/hash/unknown field 兼容；
- 核心序列化失败不产生空正文。

### 双写与本机归档

- V1 成功/V2 失败可由 outbox 重放；
- fallback/WAL replay 同时补 V2/stats 且不重复；
- turn+bodies rollback、late enrichment、tenant isolation；
- 本机文件 kill/restart 恢复、DB 失败保留、DB 确认后清理、corrupt quarantine。

### Redis

- unknown submit result 不重复；
- fallback 多任务 ID、payload schema、requeue/drain/reclaim crash window；
- heartbeat、stale complete、malformed/max attempts DLQ；
- QueueMirror 丢失不影响执行恢复。

### Stats

- EventWriter DB down/queue full/restart/DLQ replay；
- Inbox fencing、usage facts idempotency；
- revision correction/latest dedup/reconciliation；
- phantom/missing projection/closed month/tenant；
- Redis stats 丢失可从 PG baseline 重建。

### Schema/发布

- information_schema、pg_inherits、pg_class.relam、index/RLS/view parity；
- 真实 insert/update/list/detail/body/session/stats API 闭环；
- 服务版本、二进制 hash、x-request-id、耗时记录；
- rollback 后 schema、迁移账本和数据无漂移。

## 8. 实施前置决策

在进入 Phase 1 前必须明确：

1. 本机文件根目录、最大容量、保留窗口、磁盘满策略；
2. `RequestArchiveEnvelope` 是否保存完整 IR 还是只保存可恢复子集和 raw reference；
3. outbox payload 是完整 envelope、加密快照引用还是本地文件引用；
4. 统计 revision/correction 的业务定义；
5. SystemMonitor fallback 的稳定 task identity 和跨 Redis/PG drain 语义；
6. session_summaries tenant-key cutover 和历史冲突清理方案；
7. 603/604 等迁移的正式注册、应用顺序和真实环境验证窗口。

## 9. 参考文档

- `docs/audit/2026-08-25-request-logs-incident-audit.md`
- `docs/standards/database-change-and-real-verification.md`
- `docs/04-implementation/plan/2026-08-24-request-body-storage-optimization-plan.md`
- `docs/04-implementation/plan/2026-08-24-session-queue-memory-optimization-plan.md`
- `docs/04-implementation/changes/ICR-20260825-redis-audit-optimization.md`
- `docs/03-design/01-architecture/architecture/runtime-request-flow.md`
- `docs/03-design/02-feature-design/会话优化v4/14-URSM Redis delimiter-safe key兼容迁移冻结决策.md`
- `docs/adr/ADR-0001-handoff-goal-state-at-rest-encryption.md`
