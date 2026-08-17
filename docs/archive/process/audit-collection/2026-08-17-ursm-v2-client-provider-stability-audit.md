# URSM v2 客户端/供应商双端稳定性审计

**日期**：2026-08-17  
**范围**：URSM v2、Router、Executor、SSE/流式桥接、供应商端 identity-bound pool、node-health、pending/durable 交接  
**目标**：确认供应商节点不稳定时是否能够在客户端连接仍存活的前提下切换供应商，并明确客户端 TCP 已断开、首语义帧已提交等不可透明恢复边界。

## 1. 结论摘要

本轮修复和验证后，系统可以合理地保证：

> 对同一条仍存活的客户端 HTTP/SSE 响应连接，在首个客户端可见语义帧尚未提交前，网关可以丢弃供应商 A 的 attempt-local metadata，切换到供应商 B，并继续在同一条客户端连接上输出 B 的完整客户端协议序列。

系统**不能也不应承诺**：

- 客户端已经发送 TCP FIN/RST 或响应写端已经失败后，恢复原物理 TCP 连接；此时只能停止普通 upstream，或按 session/pending/durable 策略脱离原连接继续处理。
- 首个语义文本、tool call 或等价语义帧已经发给客户端后，从头重放到另一供应商而不重复内容或副作用。
- 真实供应商公网网络、代理/TLS/HTTP2、真实 Redis/PG 主从切换已经在本地测试中得到证明。

**当前发布判定**：代码级和本地集成证据为 **有条件通过**；真实 Redis/PG/provider 故障注入仍是发布前门禁，不能把本报告当作生产 GO。

## 2. 端到端调用链

```text
客户端 TCP/HTTP(SSE)
  -> ChatHandler / MessagesHandler / ResponsesHandler
  -> pre-stream StreamSession + SerializedStreamWriter
  -> Router.PlanCandidates
  -> URSM v2 Ready snapshot + NodeMirror/Redis FilterAndScore
  -> Executor.Execute
  -> candidate loop / limiter / identity-bound pool
  -> upstream.Client.DoWithHTTPClient
  -> 供应商节点 TCP/HTTP(SSE)
  -> protocol bridge / IR conversion
  -> AttemptCommitGate
  -> SerializedStreamWriter
  -> 客户端 TCP/HTTP(SSE)
```

状态反馈路径：

```text
Execute outcome
  -> nodehealth OutcomeReducer
  -> URSM request outcome Lua / circuit / binding / candidate cache
  -> Redis invalidation + next request routing
```

客户端连接和供应商连接是两个独立生命周期。供应商 TCP 失败只允许影响当前 attempt；客户端 TCP 失败则由 writer/response context 决定是否继续 detached 工作，不能被当作可重建的客户端连接。

## 3. 已实施的修复

### 3.1 URSM authoritative 冷路径身份

文件：

- `domains/ursm/v2/manager.go`
- `domains/ursm/v2/manager_filter_test.go`

Redis fresh read 的 `NodeQuery` 没有 provider 字段。现在在返回 Router 前用对应 `CandidateSeed` 回填：

- ProviderID
- CredentialID / RawModel / TenantID
- CanonicalName
- price / billing / trust / BaseURL latency

所有 seed/view 映射 key 统一为：

```text
provider_id | credential_id | raw_model
```

因此：

- cold LRU miss 不再把有效节点误判为 provider 0；
- 同一 credential/model 暴露在不同 provider 时不会相互覆盖；
- authoritative filtering 不会因为 Redis 冷读身份缺失而产生间歇性零候选。

### 3.2 稳定性评分

文件：`domains/ursm/v2/manager.go`

评分仍是“越低越优”，稳定性项现在使用失败率惩罚：

```text
score = price_weight * price
      + latency_weight * latency
      + stability_weight * (1 - success_rate) * 1000
```

SR5m：

- 样本不足/缺失：中性 0.5；
- NaN/Inf：中性 0.5；
- 越界：限制到 `[0,1]`。

高成功率节点现在优先于低成功率节点，且价格/延迟排序契约保持不变。

### 3.3 URSM 原子 telemetry 与 source priority

文件：

- `domains/ursm/v2/store/record_request.lua`
- `domains/ursm/v2/store/record_request_test.go`

request outcome Lua 现在原子维护：

- 1m/5m/30m ZSET；
- samples/successes；
- sr_1m/sr_5m/sr_30m；
- lat_ewma_ms。

当当前 `source_priority > Request(10)` 时：

- 仍记录 telemetry；
- 不改变 available、generation、source_priority、last_err 等路由裁决字段；
- manual hold 仍在 Lua 内读取，避免 TOCTOU。

窗口成员兼容旧/新格式，支持滚动升级期间正确重建成功率。

### 3.4 Probe TTL 与 coverage

文件：

- `domains/ursm/v2/store/apply_probe.lua`
- `domains/ursm/v2/probe.go`
- `domains/ursm/v2/probe_test.go`

probe 成功/失败现在都会刷新与 URSM `NodeTTL` 一致的 hash TTL；配置 TTL 为零或负数时回退默认值，避免 `EXPIRE 0` 删除节点。这样 coverage manifest 不会因为 probe 写入路径没有 TTL 而长期指向不可预期的状态。

### 3.5 客户端 heartbeat 生命周期

文件：

- `domains/streaming/handler.go`
- `domains/streaming/messages.go`
- `domains/streaming/responses.go`

生产调用点把 request context 传入 `startPreStreamKeepalive`，因此普通客户端取消后 heartbeat ticker 结束；显式 `Stop` 仍幂等。

重要语义仍保持：pre-stream 会先提交 HTTP 200/SSE headers 和 transport comment，但这不等于语义 commit。AttemptCommitGate 的语义提交仍由首个 content/tool-call 等语义帧决定。

### 3.6 Anthropic passthrough 读写边界

文件：`domains/streaming/anthropic_bridge.go`

native Anthropic passthrough 使用 `readLineWithTimeoutAndCloser`：

- silent upstream/半行 SSE 按 chunk timeout 结束；
- timeout 时关闭 upstream body，避免阻塞 read goroutine 长期泄漏；
- client write/flush failure 统一为 `client_write_failed`；
- client disconnect 后停止继续写客户端，但仍可按 pending capture 策略消费上游；
- final flush failure 不再无条件吞掉。

### 3.7 供应商连接池与资源

测试覆盖：

- identity/provider/credential 不同 key 使用不同 pool/transport；
- 同一 key 的 keep-alive 复用；
- Executor 真实调用 `Upstream.DoWithHTTPClient -> pool.Client()`，不是只拿 semaphore；
- client cancel 后 Acquire/Release 槽位可重新获取；
- RedisPoolManager acquire/release 上限、TTL、failure/success、Redis error/context cancel。

### 3.8 node-health 去重内存

文件：

- `domains/nodehealth/reducer.go`
- `domains/nodehealth/reducer_test.go`

`OutcomeReducer.seen` 改为默认固定容量环形 FIFO（100000，可测试注入），避免每个 request/attempt/phase 永久留在进程内存。节点当前健康状态独立保留，淘汰 dedup event 不会重置 fail streak/status。

## 4. 切换状态机和不变量

### 首语义帧前

```text
Attempt A: preparing/metadata buffered
  -> transient/timeout/upstream EOF
  -> Gate.Discard()
  -> refresh candidate / backoff
  -> Attempt B: new metadata buffered
  -> first semantic frame
  -> flush B metadata + semantic frame
  -> SemanticCommitted
```

不变量：

- A 的 metadata 不得进入客户端语义输出；
- B 不得复用 A 的 attempt-local buffer；
- heartbeat/comment 可以保持下游连接，但不触发 semantic commit；
- HTTP 200 已提交时，失败只能用合法 SSE terminal/error envelope 表达，不能再恢复 HTTP status。

### 首语义帧后

```text
SemanticCommitted
  -> upstream recoverable failure
  -> ResumeBlocked / terminal
```

不变量：

- 不从头重放到 B；
- 不重复文本/tool call；
- durable post-content disconnect 不自动 replay；
- session/pending 只交付已捕获的客户端协议体，不恢复原 TCP。

### 客户端断开

```text
client context cancel / write error
  -> SerializedStreamWriter detached
  -> ordinary request: stop upstream/recovery
  -> session: optional pending capture
  -> durable pre-content: release to worker
  -> durable post-content: safety blocked, reaper/fencing
```

## 5. 验证结果

### 通过

```text
go test -count=1 ./domains/ursm/v2/...
go test -count=1 ./domains/streaming/...
go test -count=1 ./domains/streaming/executors/...
go test -count=1 ./pool/...
go test -count=1 ./domains/nodehealth/... ./pending/... ./durable/...
go test -race -count=1 ./domains/ursm/v2/...
go test -race -count=1 ./domains/streaming/... ./domains/streaming/executors/...
go test -race -count=1 ./pool/... ./domains/nodehealth/... ./internal/streamretry/...
go test -tags=integration -count=1 -v ./tests/integration/ -run '^TestFaultInjection$'
```

真实 fault-injection 结果：

- `Redis_Kill_URSMv2Fallback`：PASS，但实现是把 client swap 到不可达地址，不是杀真实 Redis 进程；
- PG 相关 4 个场景：SKIP，`localhost:55432` 无可用 PostgreSQL。

### 全仓普通回归

`go test -count=1 ./...` 大部分包通过，但最终失败于与本次改动无关的既有测试/数据问题：

1. `domains/hooks/handoff/TestConfirmationStoreAcceptsTargetCreatedWithinProposalSecond`：`handoff confirmation expiry must be in the future`；
2. `sql/migrations/startup/TestNumericUpMigrationVersionsAreUnique`：migration version `528` 在两个 SQL 文件中重复。

这两个失败应单独修复，不能据此声称本次稳定性改动全仓绿；本轮没有修改它们。

另一次 targeted 全包运行曾被 `domains/streaming/TestRedisFormatCache_TTL` 的时间敏感断言打断；单独执行一次和 `-count=5` 均通过，判断为环境/时序抖动而非本次改动引入。

## 6. 未完成的发布级验证

P1：

- 真实 Redis 重启/NOSCRIPT/连接池重连；
- Pub/Sub invalidation 断线后的 subscriber 自动重连；
- 多 gateway instance 同时关闭/恢复 Ready 的 fencing；
- 真实 PostgreSQL durable lease、worker takeover、settlement/reaper；
- 真实 provider 网络断流、TLS/HTTP2、代理链和服务端主动 close；
- 完整 `HTTP request -> ChatHandler -> SurvivalCoordinator -> httptest provider A/B -> client SSE` 的跨协议故障矩阵仍需继续扩大。

P2：

- `responses_stream.go` 与 `responses_bridge.go` 双实现仍增加行为漂移风险；
- pending 普通 capture 与 survival/durable owner 的结果写入边界需要继续做 generation/fencing 级联测；
- nodehealth 的 `nodes` map 没有按节点 TTL 淘汰，只解决了无界 `seen` event；
- upstream 默认 `ResponseHeaderTimeout` 与 stream first-byte/runtime timeout 的产品契约仍需统一配置。

## 7. 后续优化建议

1. 将真实 Redis/PG/provider fault injection 设为 CI 发布门禁，缺依赖时禁止 silent skip。
2. 对三种客户端协议（Chat、Anthropic Messages、Responses）统一执行 A 首字节超时/5xx/429/只发 metadata/断流、B 成功以及首语义后断流矩阵。
3. 统一所有 bridge 的 `safeWriteSSE + FlushError + timed reader`，消除直接忽略 write/flush 的旧路径。
4. 为 pending/durable 结果写统一 owner、attempt generation 和 fencing token，禁止旧 attempt 延迟写覆盖新终态。
5. 把 `HTTPCommitted`、`TransportAlive`、`SemanticCommitted`、`DurableCheckpointed` 作为独立可观测状态，而不是用一个 `committed` 布尔值表达全部生命周期。
6. 在部署前先修复全仓现有的 migration `528` 重复和 handoff expiry 测试失败，再做最终发布回归。

## 8. 最终判定

**URSM v2 可用性：代码和本地验证层面可用，发布级证据有条件通过。**

**客户端连接保持：**供应商切换发生在客户端连接仍存活且首语义帧未提交时，可以保持同一客户端连接；客户端 TCP 已断开时只能进入 detached/pending/durable 语义，不能保持原物理连接。

**供应商不稳定切换：**状态管理、路由过滤、候选切换、attempt gate、heartbeat 和 provider pool 已形成闭环；首语义帧后的无损透明重放明确禁止，这是避免重复输出和副作用的稳定性保障。

**不可宣称项：**在真实 Redis/PG/provider 网络环境完成验证前，不应宣称“所有边界已验证”或直接给出无条件生产 GO。
