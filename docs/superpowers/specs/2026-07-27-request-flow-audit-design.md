# 客户端→LLM供应商端到端请求链路收敛设计

> 日期：2026-07-27  
> 范围：当前 live v1 data plane；覆盖 Chat、Messages、Responses、Gemini 及现有供应商协议适配。  
> 目标：在不丢失请求数据、不破坏协议语义、可追踪、可并发安全的前提下，收敛客户端请求到供应商再返回客户端的完整链路，并在实施后完成独立审计。

## 1. 决策摘要

本次采用“一次性收敛、保留紧急回滚”的策略：

- 保留现有 HTTP 入口和兼容路由，不重写整个 `ChatHandler`。
- 新增统一请求生命周期上下文和记录协调器，逐步替换分散的身份、日志、终态和 session 镜像入口。
- IR 作为默认协议转换主路径；Legacy 仅作为显式紧急回滚路径。该假设基于用户“请继续”，若部署前改变，应单独更新方案和验收矩阵。
- URSM v2 authoritative 作为 live 路由状态目标；`NodeMirror` 只做读加速，Redis Lua/CAS 是状态写入权威。
- 默认保存脱敏后的完整客户端体、出站体和响应体；超限时保存 hash、长度、预览和截断原因。
- 不长期保留日常双轨逻辑，但保留一个可关闭 IR/authoritative 的紧急开关，回滚时必须记录原因和影响。

## 2. 当前基线与已完成项

当前基线为 `HEAD=f87d29aa`。以下问题已在代码中落地，不作为重复修复项：

- Chat 入口的 WAL early create、按 `request_id` upsert 及重复初始写合并。
- `request_logs_hot` 和 WAL 的基本终态回退保护。
- `credentialstate` per-key lock 与 copy-on-write。
- `choices:[]` usage 终帧保留。
- Anthropic 错误信封。
- IR `ToolDefinition` 的 `Type`/`Raw` provider-specific tool 保留基础能力。
- `stream_options` 通过扩展字段旁路透传。
- Anthropic `tool_choice=any` 到 OpenAI `required` 的映射。
- 流式 tool 参数的 JSON 校验与有限修补。
- URSM v2 NodeMirror 读路径接入、Ready 缓存及 authoritative 分支基础实现。

这些修复仍需通过全链路和真实依赖验证，但不应在新计划中被误报为未实现。

## 3. 范围

### 3.1 In scope

1. 客户端识别、API key/user/tenant 标记及请求 ID 传播。
2. Chat、Messages、Responses、Gemini 入口的统一生命周期和错误路径。
3. WAL、`request_logs_hot`、raw audit、usage/body side table、Session V2 的写入协调、幂等与重放。
4. OpenAI、Anthropic、Gemini、Responses 及现有 provider 扩展的请求/响应/流式转换。
5. 单个/多个 tools、provider-specific tools、tool choice、tool call 参数和 JSON 兼容性。
6. 候选路由、重试/failover、供应商 HTTP 转发、响应校验及客户端返回。
7. URSM v2、NodeMirror、tenant 传播、routing state source 和相关并发。
8. PostgreSQL/Redis 故障注入、`-race`、压力测试、E2E 和完成后审计。

### 3.2 Out of scope

- 未启用的 `gateway-new`、`cmd/gateway-v2` 平行 data plane。
- 非 LLM 请求链路和与本目标无关的管理后台模块。
- 重写 FpSlots、Limiter、RPM、DisguisePool、EgressIdentity 的业务语义。
- 引入 etcd、ZooKeeper 或新的分布式协调系统。
- 跨区域多活和机器学习路由策略。
- 未经业务确认的 provider 字段剥离语义扩展。

## 4. 目标不变量

### 4.1 身份不变量

- 一个业务请求只有一个服务端 `request_id`，入口、日志、trace、上游和响应头复用该 ID。
- `client_request_id` 独立保存，不参与主键和终态覆盖。
- `user_key` 必须来自已认证的用户/API key 归属；不能使用 UA、随机 request ID 或易漂移 identity hash 替代。
- `user_key + client_type` 是 FpSlot holder、sticky、日志和资源统计的统一派生输入。
- `tenant_id` 进入候选查询、URSM state key、路由 seed、日志和 Session V2；禁止 authoritative 路径固定为空。

### 4.2 记录不变量

- 请求进入业务 handler 后，先建立 `received` 记录，再做认证、解析、限流和路由。
- 早期初始写与后续补全写必须按 `request_id` 幂等合并，不能产生孤儿 pending 行。
- 每个请求最多一个主终态；终态不可被迟到的非终态或断开事件覆盖。
- 所有丢失、截断、降级、修补和持久化失败都必须有结构化原因，不能静默吞掉。
- 详细 body 必须脱敏；密钥、授权头、附件敏感内容和策略指定字段不得落日志。

### 4.3 转换不变量

- 标准字段和未知扩展字段都必须有明确的保留、映射或损失记录策略。
- 同协议未知结构优先原样回放；跨协议不可表达时显式 anomaly，不静默丢弃。
- 流式响应不得因中间转换而生成非法 SSE、非法 JSON 或错误协议信封。

### 4.4 路由与并发不变量

- 一次路由计划使用一个一致的状态快照。
- 状态写入先经 Redis Lua/CAS，再 write-through 到 NodeMirror；迟到 generation 不得覆盖新状态。
- Redis、PostgreSQL、队列和后台 worker 失败必须可重放或显式报告。
- 共享状态禁止从缓存取出指针后原地修改；请求局部跨 goroutine 状态使用 atomic/mutex。

## 5. 目标架构

```text
HTTP middleware
  → UnifiedRequestContext
  → RequestRecordCoordinator.CreateReceived
  → protocol parser / IR
  → validation + normalization
  → RouteSnapshot
  → URSM v2 Redis/LRU state read
  → candidate execution / upstream attempt
  → response parser / stream assembler
  → client protocol serializer
  → terminal record + usage/body/session mirror
```

### 5.1 UnifiedRequestContext

统一上下文包含两部分：不可变 `RequestIdentity` 和可变但受保护的 `RequestLifecycle`。

```text
RequestIdentity
  request_id
  client_request_id
  tenant_id
  api_key_id
  api_key_owner_user
  application_id
  user_key
  client_type
  identity_hash
  client_model
  request_mode
  session_id
  task_id

RequestLifecycle
  stage
  attempt_no
  client_protocol
  provider_protocol
  outbound_model
  provider_id
  credential_id
  request_body_snapshot
  outbound_body_snapshot
  response_body_snapshot
  routing_attempts
  quality_flags
  error
  terminal_state
```

`RequestLifecycle` 的终态提交通过单一原子门控制。`logged` 不再是无保护的普通布尔值；显式失败、defer 安全网、客户端断开和成功回调必须竞争同一终态转换。

### 5.2 RequestRecordCoordinator

协调器提供以下接口：

```text
CreateReceived(ctx, identity, bodyMeta)
UpdateStage(ctx, requestID, stage, patch)
RecordAttempt(ctx, requestID, attempt)
Complete(ctx, requestID, snapshot)
Fail(ctx, requestID, snapshot)
MirrorSession(ctx, snapshot)
Flush(ctx)
```

设计要求：

- WAL 与完整日志仍可保留不同表和保留周期，但由同一 snapshot/event 产生。
- `request_logs_hot`、usage ledger、body side table 的同一次主写尽量使用同一 PostgreSQL 事务。
- 跨 WAL、主日志、raw audit、Session V2 无法使用单事务时，使用 request-level event id、状态版本和 replay 表做最终对账。
- RequestLogger 队列满时进入有界 fallback；fallback 满时写入不可丢失的 overflow marker，并暴露计数和告警。
- `flushBatch` 当前失败的记录也必须写 fallback；后续记录不能因批次事务中止而被静默遗漏。
- `Stop()` 幂等；停止顺序为停止接收、排空队列、等待所有 flush goroutine、持久化 overflow、关闭依赖。

## 6. Session V2 收敛

### 6.1 单一写入 owner

保留 telemetry persisted hook 作为唯一生产镜像入口；pipeline hook 不得在真实 upstream response 之前执行 post-response session 写入。若确需 pipeline 使用，必须改为只发事件，不能直接写 V2。

### 6.2 幂等与事务

- Session turn 的幂等键为 `tenant_id + request_id + partition_date`，并评估是否需要跨分区全局 request id 约束。
- 重复 request id 必须读取并返回实际已存在的 turn number。
- turn metadata、request/response body、turn metadata log 在可行范围内使用同一事务；失败时整体可重试。
- session aggregate 更新必须使用 request id 去重，replay 不得重复累加 token、turn count 或成本。
- detached snapshot goroutine 改为受 gateway lifecycle context 管理；停止时等待或进入 replay。
- delta 去重不能仅使用 `role + content 前 100 字符`，必须使用稳定 message id、序号或完整 hash。

### 6.3 镜像失败

Session V2 仍是 shadow 时，镜像失败不阻塞客户端，但必须：

- 记录 request/session/turn/阶段/错误；
- 进入 replay 队列；
- 暴露 backlog、失败率和重放成功率；
- 禁止产生孤儿 turn、孤儿 body 或错误 aggregate。

## 7. 协议转换与 tools

### 7.1 IR 主路径

生产默认打开 IR converter。Legacy 仅在显式紧急开关开启时使用，并在请求日志记录 `conversion_path=legacy` 和回滚原因。

所有协议适配器实现：

```text
Parse → Preserve → Validate → Normalize → Serialize
```

`InternalRequest`/extensions 必须保留：

- `stream_options`、metadata 和未识别扩展；
- tool `type`、原始 JSON、name、description、parameters；
- tool call id、index、arguments fragments；
- multimodal blocks、thinking/reasoning、usage 和 finish reason。

### 7.2 tools 验收矩阵

至少覆盖：

- 0、1、2+ function tools；
- `auto`、`none`、`required`、指定 function、Anthropic `any`；
- OpenAI function/web search/code interpreter/file search；
- Anthropic tool_use/tool_result、computer_use/bash/text_editor；
- Gemini function declaration/call/response；
- 单次和多次 tool call、重复 index、乱序 delta；
- `input_json_delta` 截断、转义、嵌套对象/数组、Unicode；
- usage-only 终帧；
- 同协议 round-trip 和跨协议显式 loss/anomaly。

### 7.3 JSON 和流式规则

- 请求解析使用 `json.RawMessage`、Decoder 和兼容类型检查，避免固定类型强转导致字段丢失。
- tool schema 的数组/对象/布尔/空值形态均须兼容。
- 流式 tool 参数在结束点进入 assembler 并执行 `json.Valid` 和结构约束检查。
- 只有能够证明安全时才补闭括号/闭字符串；无法修补时记录 anomaly 并生成协议正确错误。
- 不得把非法参数原样当成合法 JSON 继续发送。

## 8. 路由与状态

### 8.1 RouteSnapshot

```text
RouteSnapshot
  request_id
  tenant_id
  api_key_id
  client_model
  outbound_model
  client_protocol
  session_id
  work_type
  candidates
  state_source
  state_generation
  planned_at
```

`PlanCandidates` 必须把真实 tenant、request id 和 canonical/model 维度传入 URSM seed 和 state lookup。候选 slice 在规划完成后视为只读；executor 如需排序必须复制。

### 8.2 状态来源

authoritative 目标路径：

```text
NodeMirror hit → Redis read → fail-open original candidates
```

- NodeMirror hit/miss/stale/fallback 都要产生 `routing_state_source`。
- Redis Lua 成功写入后立即 NodeMirror write-through。
- write-through 也服从 generation/source priority 单调规则。
- Redis 和 NodeMirror 都不可用时才 fail-open；fail-open 必须进入 request context、routing attempt、request log、decision log 和 metrics。
- 当前 `ModeOff`/`Canary` 的 legacy fallback 必须在代码和运行文档中明确标识，不能伪装成 authoritative。
- authoritative 下旧 StateManager、Shadow 和 FpSlots NodeState 不得参与健康可用性判定；FpSlots/Limiter/RPM/DisguisePool/breaker 保持各自独立语义。

### 8.3 状态并发

- `credentialstate` 现有 per-key COW 保留，最终 live 路径下沉或不再读写。
- Redis 写入使用 Lua/CAS 和 generation；禁止 detached 无版本覆盖。
- NodeMirror 是只读镜像，不得在 Redis 写失败时先行修改。
- sticky/intent 的 Redis 写入、进程缓存和 DB 归档必须有清晰的 owner、TTL 和 replay 规则。
- 管理员禁用、探测恢复和业务结果更新需要定义 source priority，迟到低优先级事件不得覆盖 manual hold。

## 9. 上游转发与响应

### 9.1 转发

- 每次候选尝试记录 attempt start/end、provider/credential、outbound model、URL protocol、状态码和错误分类。
- 上游请求复用统一 request/session/user 标记；供应商密钥只从 credential 注入，不进入日志。
- body 重写后过滤 `Content-Length`、`Content-Encoding` 和 hop-by-hop headers，重新计算响应长度。
- 客户端 session detached upstream 行为必须有额度、连接和 shutdown 预算，客户端断开不应无限消耗供应商资源。

### 9.2 failover 与响应提交

- 客户端响应头提交前可切换候选。
- 已提交有效 `200`/SSE 内容后禁止透明换供应商。
- 空响应、首字节 timeout、供应商 429/5xx 按 retry policy 分类处理。
- `choices=[]` 帧只有在没有 usage 或其他有效 payload 时才丢弃。
- 供应商错误 body 只作为脱敏诊断，客户端收到目标协议的标准错误信封。

### 9.3 协议错误信封

- OpenAI Chat/Completions：OpenAI error envelope。
- Anthropic Messages：Anthropic `type=error` envelope。
- OpenAI Responses：Responses 对应 error/event 结构。
- Gemini：Gemini 对应错误结构。
- SSE：保持目标 `Content-Type`、事件名、终止事件和 request id。

## 10. 实施步骤

尽管采用一次性 live 收敛，代码实施仍按以下依赖顺序完成，每一步都必须通过门禁后才能进入下一步：

### Step 1：记录可靠性

- 修复 WAL 队列满直接丢更新。
- 修复 batch 当前失败行未进入 fallback。
- 使 RequestLogger Stop/flush 生命周期幂等并可等待全部 goroutine。
- 增加 overflow/replay 指标和测试。

### Step 2：统一入口与终态

- 抽取 Chat/Messages/Responses 共享身份和生命周期初始化。
- 统一 request context、终态原子门、阶段事件和错误模型。
- 核对全局 middleware 拒绝路径的可追踪性边界。

### Step 3：Session V2 单 owner

- 移除或禁用重复 pipeline SessionPersistHook。
- 修复 turn duplicate 返回实际 turn number。
- 将 turn/body/metadata 写入事务化或可重放事件。
- 为 aggregate 增加 request-level 幂等。
- 将 snapshot 和 DBWriter flush 纳入 shutdown lifecycle。

### Step 4：IR 默认与协议验收

- 默认打开 IR converter。
- 保留 Legacy 紧急开关，但禁止无记录切换。
- 完成全协议/tools fixture、round-trip、流式 assembler 和错误信封测试。
- 对跨协议不可映射字段生成明确 anomaly/loss reason。

### Step 5：URSM authoritative

- 补全 tenant/request/canonical 上下文。
- 完成 NodeMirror write-through。
- 完成 `routing_state_source` 全链路传播。
- 明确并验证 off/canary/authoritative 行为。
- 验证 authoritative 下旧状态源零 live 调用；保留紧急回滚开关。

### Step 6：一次性切换与验证

- 先执行数据库迁移和索引校验。
- 运行单元、集成、真实 PG/Redis 故障注入、`-race`、压力和 E2E。
- 用固定 smoke fixtures 对比客户端响应、上游请求和所有记录 sink。
- 发布后立即检查 fallback、replay backlog、session mirror、路由状态来源和错误率。

## 11. 测试与验收

### 11.1 必过测试

- `go test ./domains/streaming/... ./domains/transformation/... ./internal/ir/...`
- `go test ./domains/hooks/observability/telemetry/... ./domains/session/... ./domains/ursm/v2/...`
- `go test -race` 覆盖请求记录、session writer、credential state、NodeMirror、路由 planner 和 shutdown。
- 带真实 PostgreSQL/Redis 的 integration tests，覆盖事务中止、队列满、Redis kill、重放和重复 request id。
- 全协议 E2E：Chat、Messages、Responses、Gemini、stream/non-stream、tools/multimodal/error。

### 11.2 数据完整性门禁

- 早期失败仍有可关联的业务记录；明确记录 middleware 之前拒绝的边界。
- 一个 request id 对应一个主 WAL 行、一个主日志逻辑行和幂等的 Session V2 turn。
- success/failure/client disconnect 不发生终态倒退。
- WAL、主日志、usage、body、raw audit 和 Session V2 的 request/session/provider 关联可对账。
- 队列满、批失败、进程退出不会静默丢掉已接受事件。

### 11.3 协议门禁

- 每个 fixture 的输入、IR、出站、上游响应、客户端响应均可比对。
- tools 数量、顺序、id、参数和未知扩展不发生未记录丢失。
- 非法 JSON 不会作为合法请求发送；错误响应符合客户端协议。

### 11.4 路由与性能门禁

- tenant 隔离和 state key 正确。
- generation 单调；NodeMirror write-through 可验证。
- `routing_state_source` 可在日志和指标中统计。
- authoritative 路径旧状态源零 live 调用。
- fallback、replay backlog、P95、错误率和资源消耗不超过发布阈值。

## 12. 完成后审计

实施完成后进行独立审计，不能只依赖单元测试：

1. **代码审计**：逐项检查身份、生命周期、终态、转换、路由、并发契约是否有唯一 owner。
2. **数据审计**：按 request id 对账 WAL、主日志、raw、usage、body、Session V2 和 routing decision。
3. **协议审计**：按官网公开 schema 和版本记录核对字段、tools、流式事件和错误信封。
4. **并发审计**：`-race`、高并发、重复终态、重复 replay、Redis/PG 故障注入和 shutdown。
5. **运行审计**：检查成功率、P95、fallback、队列溢出、replay backlog、镜像失败、状态来源和数据缺口。
6. **目标一致性审计**：逐项核对用户八个关注点，任何未覆盖项必须明确延期或阻断发布。

## 13. 发布阻断条件

出现以下任一情况不得完成切换：

- 任何已知 P0/P1 数据丢失、协议错误、终态回退、重复 turn 或真实 data race。
- 无法用 request id 关联客户端、上游和主记录。
- tools/扩展字段静默丢失。
- authoritative 路径 tenant 不正确或旧状态源仍参与健康判定。
- 队列满、批失败或 shutdown 导致已接受事件不可重放。
- 真实 PostgreSQL/Redis 故障注入未通过。
- 关键协议 fixture 与官网格式不一致。

## 14. 设计自审结论

- 未保留未定义的 `TODO/TBD` 作为验收条件。
- 已区分当前 `HEAD` 已落地修复与本次新增实施项。
- 已明确一次性收敛不等于取消紧急回滚。
- 已明确 middleware 之前拒绝请求的 WAL 边界，避免承诺“绝对所有 HTTP 请求都有业务 WAL”。
- 已明确 IR 默认主路径是假设；若部署前改变，必须重新评估协议切换范围。
- 已覆盖客户端识别、请求记录、双写、格式转换、tools/JSON、路由转发、异常响应、返回等待/修补、状态完整性和并发八类目标。
- 已定义实现顺序、测试门禁、发布阻断条件和完成后独立审计。
