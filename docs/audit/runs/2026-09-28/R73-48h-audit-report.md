# R73 · 48h 全面审计执行报告

> 日期：2026-09-28（Asia/Shanghai）
> 状态：进行中；D01–D17 完整域审计未完成；F02/F03/F04/F06/F08 已实现并有定向验证；F09 已实现 request-local 跨完整 SSE delta carry 并通过定向 race/writer 集成测试；F10 已修复空映射/Redis 故障时未知占位符透传，并有定向测试。F07 已完成 PostgreSQL 17 隔离容器合成数据验证；Citus/生产兼容版本、真实角色与代表性数据量仍未验证。
> 主审范围：2026-09-25 23:01:34 至 2026-09-27 23:01:34，`78d91b94..c7104b141`。
> 审计起始基线：`main` / `origin/main`，HEAD `95e243b819984804bb8ee567a6289219af1abd8f`。既有 R73 修订及证据更正已合入当前 `main`；本续审最终提交基线为 `9946d75b5adf7eba7a03c15fee83123d5592e844`。该基线上的 `ReconciliationReport.vue` 并行修复不属于本轮变更。本续审 F09/F10 代码修复提交 `9fe387ac8` 已推送；最终 tip 与远端核验记录见交接文档。

## 1. 范围与基线

| 区间 | 非 merge 提交 | 唯一变更文件 | 审计处理 |
|---|---:|---:|---|
| 冻结的用户要求 48h 窗口 `78d91b94..c7104b141` | 89 | 241 | 不移动边界，逐域审计 |
| 审计漂移 `c7104b141..5fe87cadc` | 31 | 185 | 与主窗口分开报告 |
| 后续远端漂移 `5fe87cadc..2d119529f` | 1 | 1 | 纳入当前 HEAD 审计；增加真实上游 Vapeur probe 测试 |
| 当前继续漂移 `2d119529f..95e243b81` | 6 | 73 | 核验 R73/R12 修订、存储观察及 R62 激活安全 handoff |
| 合并观察区间 | 127 | 435 | 作为执行覆盖面，不替代冻结主窗口 |

审计冻结边界仍为原 48h 窗口，不因后续提交而移动。起始 HEAD `95e243b81`，当前续审 HEAD / `origin/main` 为 `b60b12874`。未归属的 Web 依赖/UI 修改及空 `docs/audit/todo-state.json` 本轮不纳入提交并保留原样。

## 2. 工具与构建证据

- Codegraph 3.7.0 native 图谱在审计起始 HEAD `95e243b81` 全量构建成功：6061 个文件、147288 个节点、261668 条边；本续审最终全量刷新解析 6064 文件、147452 节点、262534 条边。图谱用于定位，所有发现必须复核源码与调用方。
- 增量构建多次遇到 SQLite `database disk image is malformed`。损坏缓存已分别留存在 `/tmp/llm-gateway-go-3-codegraph-malformed-20260928-continue`、`/tmp/llm-gateway-go-3-codegraph-malformed-r73-rerun` 和 `/tmp/llm-gateway-go-3-codegraph-malformed-r73-final`；最终全量重建成功。CLI 不支持 `codegraph update` 子命令。
- 当前工作区 `go build ./...` 与 `go vet ./...` 均退出码 0；`git diff --check` 通过。vendor 对 VLA 扩展的警告未构成本轮失败。
- `go test ./admin -run 'TestLiveVapeurResponsesProbe|TestLiveVapeurChatProbeIsNotUsedForResponses' -count=1 -v`：退出码 0。两项测试均因未设置 `VAPEUR_API_KEY` 按设计 skip；测试代码可编译，但此次没有访问 Vapeur，也未验证线上状态或时延。

## 3. 变更关注点

- 当前漂移新增的可选 live probe 依赖环境变量注入凭据，不在 CI/无密钥环境访问供应商。待审核：能力路由是否正确选择 `/v1/responses`、60 秒上下文超时与底层 client timeout 的组合、探针失败是否泄密、第二个观测用例是否会产生非预期费用。
- 前期审计范围重点包括 migrations 748–754、turn/request 日志 TTL/FIFO 与归档、hot+columnar 的读写/归档边界、auto-route/fail-open、协议扩展参数回填、sanitize/compress occurrence identity、双模式存储、provider error/node health、代理、计费与 UI/API 闭环。
- 近 96h 方案文档仍需逐项和当前代码对照；文件存在或方案写明“完成”均不作为实现证据。

## 4. 当前发现状态

当前没有证据支持“整体无问题”结论。三组域审计已经返回；主审复核了关键调用路径，并建立 [R73 修复方案](R73-remediation-plan.md)。以下是当前已确认的问题与验证状态，完整触发条件/验收标准见方案文档。

| ID | 域/级别 | 状态 | 关键证据/影响 | 处置/验证 |
|---|---|---|---|---|
| R73-F01 | admin live probe / P2 | 验证通过 | 失败结果为 nil 时存在测试 panic；chat 探针此前仅凭主 API key 就会额外调用 | 测试 nil-safe、失败先判定、chat 加显式开关；离线定向测试通过，真实供应商未测 |
| R73-F02 | D06 lite telemetry / P2 | 修复并追加复核 | 并发终态写入可能重复分配 turn；二审另发现 reservation FIFO 无界累积且 stale ID entry 可误删新 reservation | `journalBusy` 原子预占/等待、失败释放、24h 有界 reservation；移除辅助 FIFO 并按 expiry 有界淘汰；最终 race 结果见 §6 |
| R73-F03 | D02/D16 session stream / P2 | 验证通过（本地） | executor 把原始 request context 传给 reader，显式 session 客户端断连仍会取消持久化捕获；Ollama 写失败/取消原先会误记 network | reader 使用 upstream response context；OpenAI/Anthropic/Responses/native Responses/Ollama 定向回归通过 |
| R73-F04 | D05 Ollama overflow recovery / P2 | 已修复并定向 race 通过 | Ollama 按候选窗口预压缩；明确 context-length 4xx 触发共享 recovery，context marker 限制最多一轮内部重试；补全 reason/strategy/meta | 定向测试通过；真实 tokenizer/provider 未验证；>1M handoff 仍待客户端契约 |
| R73-F05 | D07 request_logs archive cadence / P2 | HEAD 修复且定向验证通过 | `3909d56e0` 已把 03 点归档门限接到 1h cleanup ticker，解除 24h ticker 的启动相位锁定 | 每日 03:00–04:00 内每天必有一次 tick；定向 bg 测试通过；PG 容量风险归 F07 |
| R73-F06 | D09 selfcheck rate limiting / P2 | 修复并追加复核 | 任意 429 原先合并成一种 worker-wide 状态；二审发现 Embeddings 已 throttled key 分支漏设 gateway 标记 | 全部本地 key-admission 429 标 `shared_key`，provider 错误响应路径过滤保留 header；selfcheck 仅对可信标记建立一轮冷却 |
| R73-F07 | D07 migration 754 / P2 | PG 17 合成数据验证通过；Citus/生产规模待验证 | 15 万合成行首次归档 150,000，幂等重跑 0，源仍 150,000；role 默认 1ms 可取消裸调用，生产调用顺序 SET LOCAL 30min 后成功且 COMMIT 后恢复默认值 | 调用方显式事务 + 30min statement_timeout 与 Go context 对齐，超过 30min 会整次回滚；无 ledger 会在下次日批重扫 | 实际 Citus/生产角色、代表性规模、锁及 WAL/磁盘耗时未测；详见 R76
| R73-F08 | D14 stream safety / P2 | 定向测试通过 | SSE 拦截 writer 原无帧大小上限，`finish()` 还会把不完整尾帧原样写给客户端 | 损坏/恶意上游可导致无界内存增长；尾帧跳过脱敏链 | 本地设 16 MiB 帧上限、支持 LF/CRLF；超限失败关闭，不完整尾帧丢弃并记录安全元数据 |
| R73-F09 | D14 stream sanitization / P2 | 已实现并推送；ShouldBlock 传输闭环待修 | 完整 SSE delta 之间拆开的 marker 原会透传；现按 writer 的 StreamMeta 状态暂存协议 lane 尾片。lane overflow/opaque continuation 返回 ShouldBlock 后 writer 仅丢当前帧；后续帧持续丢弃且没有终态错误/阻断事件 | 保护 marker suffix 的 fail-closed 逻辑可能令客户端只收到部分 SSE 并静默等待 EOF，durable stream 错误/遥测没有该阻断原因 | 每流 64 lanes、每 lane 256 bytes；OpenAI/Anthropic/Responses lane；未知 marker mask、已识别 malformed 局部遮蔽；opaque continuation/lane overflow 阻断 | `go test -race ./domains/streaming ./security/sanitize -count=1` 退出码 0；没有客户端 failure-terminal 集成测试 | `9fe387ac8` | 真实 provider frame 与合法大媒体（16 MiB）影响未测；终态/遥测契约见 R76 |
| R73-F10 | D14 sanitize restore / P2 | 已修复；空映射与 Redis 故障用例通过 | `loadMap` 空表/报错原先直接 passthrough，未知 `{SENSITIVE:...}` 可泄漏；现在无映射时仍执行 mask | 未知内部 token 可能暴露（不是原始 PII） | 空表快速路径检查 marker；stream 空 map 每帧仍解析；loadMap 报错转空映射并遮蔽 | 空映射、关闭 miniredis 模拟 Redis failure 定向测试及 sanitizer package race 通过 | `9fe387ac8` | 已知 PII 映射丢失时无法恢复；真实 Redis 部署未测 |

## 5. 待完成验证与交付

1. 复核近 96h 供应商协议、mock probe、hotzone、Jev、probe 成本、auto-route、session storage 与 R72 方案/报告，登记代码偏差。
2. D01–D17 完整全链路审计仍未完成；本轮只记录 F02–F10 相关范围。D05 SF-01 与 D12 候选选择完整调用链复核仍明确未完成；其他域也不能因计划改写或勾选视作已审计。
3. F09/F10 已实现、定向验证且代码已提交；R76 补充了 F07 的 PostgreSQL 17 合成数据验证和 R62 迁移草案，但生产兼容性/规模及 R62 设计拍板与实施仍未完成。F09 ShouldBlock 的客户端终态/遥测仍是开放项。
4. 运行受影响测试、`-race`、兼容性与可用本地集成测试；本续审只读检查发现本机共享 PG/网关正在运行，local-deploy-test 路径包含共享服务重建及可选远端 schema 同步，因此未执行部署或迁移。现存 `8782/healthz` 返回 200，但版本 SHA 为旧的 `8caf33f4`，不能作为本轮构建验收。
5. 本续审代码修复提交 `9fe387ac8` 已在 `main`；仅本任务确认归属的 Go/审计文件纳入，Web 改动和空 todo-state 保持未纳入。D01–D17 全域审计、F07 生产兼容 PG、完整本地部署和部分外部边界验证仍需后续完成。

## 6. 本轮继续执行证据

| 命令 | 结果 |
|---|---|
| `codegraph build --engine native --no-incremental .` | 起始图：6061 文件 / 147288 节点 / 261668 边；最终续审刷新：6064 文件 / 147452 节点 / 262534 边，退出码 0 | 损坏图数据库分别留存在 `/tmp/llm-gateway-go-3-codegraph-malformed-20260928-continue/`、`...-r73-rerun/`、`...-r73-final/` |
| `go test -race ./cmd/gateway -run 'TestLiteRequestLogSink_(ConcurrentReplayJournalsOnce|JournalClaimSerializesSameRequestID|FailedJournalRetryReusesTurnNumber|TurnReservationsStayBoundedAndRetainNewReservation)' -count=1` | 退出码 0 | 覆盖同 ID 并发、等待唤醒、失败重试复用 turnNo、容量上限与过期 ID 重用 |
| `go test ./domains/streaming/executors -run 'TestExecute(OpenAI|Anthropic).*DetachedUpstreamContext|TestExecuteOllama_StreamReaderUsesDetachedUpstreamContext|TestOllamaStream(CanceledReadErrorIsClientCancellation|ClientWriteFailureIsCanceled)|TestCopyNonStreamResponseHeaders_ReplacesWireHeaders' -count=1` | 退出码 0 | 覆盖 OpenAI chat/Responses、Anthropic adapters、Ollama 读写取消与保留 header 过滤 |
| `go test ./bg -run 'Test(SelfcheckRateLimitAbort|SelfcheckProvider429DoesNotSeedGatewayCooldown|SelfcheckRound429Classification)' -count=1` | 退出码 0 | gateway 标记与 provider 429 分开测试；真实 gateway 到 provider 端到端待集成验证 |
| `go test -race ./domains/streaming -run 'TestEmbeddingsHandlerMarksThrottledAPIKeyAsGateway429' -count=1` | 退出码 0 | API key 已 throttled 的 Embeddings 429 带可信 gateway scope |
| `go test -race ./domains/streaming/executors -run 'TestExecutor_ExecuteOllama_ContextLengthRecoveryRetriesOnceWithSmallerBody' -count=1` | 退出码 0 | Ollama 最多一次内部重试、请求体缩小、成功返回并带压缩 reason/strategy/meta |
| `go test -race ./domains/streaming -run 'TestInterceptingStreamWriter' -count=1` | 退出码 0 | F08 的 LF/CRLF、超大帧 fail-closed、尾帧丢弃测试通过 |
| `go test -race -timeout 120s ./security/sanitize/... -count=1` | 退出码 0 | F09/F10 及其余 sanitizer tests/race 通过，含 map 空/Redis down、跨协议 delta、PII masking、lane 与 request 隔离 |
| `go test -race ./domains/streaming ./security/sanitize -run 'Test(InterceptingStreamWriterRestoresPlaceholderSplitAcrossCompleteEvents|InterceptingStreamWriter|SanitizeRestoreInterceptor_(PlaceholderSplitAcrossCompleteFrames|SplitPlaceholderAcrossProtocolDeltaFields|BoundsUntrustedStreamLaneIdentifiers|BlocksWhenStreamLaneBoundIsExceeded|BlocksOpaqueFrameWhilePlaceholderTailIsPending|MasksInvalidPlaceholderContinuationAndKeepsStreamOpen)|RestoreResponseBody_UnknownPlaceholderMaskedWhenRedisIsUnavailable)' -count=1` | 退出码 0 | 生产 writer 上跨完整 SSE 事件恢复；帧上限/结束边界与 sanitizer malformed/lane tests 通过 |
| `go test ./security/sanitize -run 'TestRestoreResponseBody_UnknownPlaceholderMaskedWhen(RedisIsUnavailable|SessionMapIsEmpty)' -count=1 -v` | 退出码 0 | 空 Redis map 与关闭 miniredis 模拟连接失败；未知 marker 被遮蔽；已知值映射不可恢复 |
| `go test -race -timeout 120s ./tests/48h-audit/D14-security/... -count=1` | 退出码 0 | D14 business/data/safety/stress 历史 mock 子包通过；这不是完整生产端到端审计 |
| `go build ./...` | 退出码 0 | 全仓构建通过 |
| `go vet ./...` | 退出码 0 | 全仓静态检查通过 |
| `git diff --check` | 退出码 0 | 空白与冲突标记检查通过（最终提交前还要再跑） |

| `go test ./... -count=1`（本续审重跑） | 非 0 | `discovery/TestDiscoverGateways` 因 IPv6 mDNS `no route to host` 失败；`upstream/TestDo_DNSFailureReservedTLD` 本机 DNS 实际归类 `transient`、测试期望 `network`。此前另一次全仓 run 的 plugin-runtime 子进程用例失败，随后串行定向重跑通过；原因未证实。 |
| `go test ./bg -run 'Test(RequestLogsArchiveScheduleIndependentOfDailyTickPhase|PartitionManagerSchedulesRequestLogsArchiveIndependently|Migration754_NeverDeletesFromSource|ClampRequestLogsArchiveDays|ArchiveOldRequestLogs|ShouldRunRequestLogsArchive)' -count=1` | 退出码 0 | HEAD 已把每日门限接入 1h cleanup ticker；未连 PG |
| `go test ./domains/streaming -run 'Test.*RateLimit|Test.*rateLimit' -count=1` | 退出码 0（先前记录） | 本轮追加标记接线后将随全包验证复跑 |
| `go test -race ./... -count=1` | 大多数包通过；`discovery/TestDiscoverGateways` 因本机 IPv6 mDNS `no route to host` 失败；`domains/dispatch/TestConcurrentSubmitStopRace` 报既有数据竞争；`upstream/TestDo_DNSFailureReservedTLD` 分类期望 `network` 实际 `transient`；`upstream/TestDo_SlowBodyCancellationPropagates` 在本机 transport 下 headers 未及时建立 | 均未指向本轮 F03/F04/F06 改动；需在隔离网络/DNS fault-injection/dispatch 专项环境复核 |

最终限制：本轮 build/vet 及受影响包测试通过；全仓单测因上述 mDNS/DNS 条件失败。上一轮全仓 race 另有 `domains/dispatch/TestConcurrentSubmitStopRace` 数据竞争、`upstream/TestDo_SlowBodyCancellationPropagates` transport 头超时等失败，需要独立环境复核；没有兼容 PG、Redis 或有效供应商凭据，因此 migration 754、真实 provider probe、full/lite 部署集成仍未验证。

## 7. 近 96h 方案审阅中新确认的凭据风险

R76 补充证据、R62 兼容迁移草案和 ShouldBlock 终态缺口见 [R76 续审记录](R76-continuation-report.md)。

复核窗口内方案及当前 HEAD 后，发现最新 [R62 激活安全交接](../../../handoff/2026-09-28-r62-activation-security-audit-handoff.md) 列出的四项凭据问题仍能在当前源码中定位。当前先登记风险与迁移门禁；refresh token 哈希化及 activation code 加密/迁移需先确认密钥管理、双读回滚和历史数据策略，不以临时字符串替换冒险上线。

| ID | 等级 | 当前证据 | 影响 | 后续方案/验证 |
|---|---|---|---|---|
| R62-H1 | P1 | `licensing/offline.go` 与 notifier 记录明文 `activation_code`；`licensing/store_pgx.go` 明文落库；`admin/ops_overview.go` 返回明文最近记录 | 有日志/DB/运维面读取权者可取得仍有效激活凭据 | 先定 hash 无法再现码时的通知/重取契约，或采用 envelope encryption；掩码日志/API 并迁移遗留行；覆盖 DB/API/日志快照 |
| R62-H2 | P1 | `center/store_pgx.go` 用明文 refresh token 等值查询和更新 | 数据库备份/只读访问可直接重放 refresh token 换 instance token | 采用服务端 pepper 的 HMAC 索引或加密存储；双读/回填/切换/撤销过期明文策略，测试并发轮换和旧客户端 |
| R62-M1 | P2 | `installer/internal/activation/auto_activate.go` 在 trial 路径把完整 `LicenseKey` 写入 `activation.json` | 安装状态备份/诊断包扩大泄漏边界 | 状态文件仅存指纹/掩码；凭据转入 0600 独立文件或注册后删除；验证升级兼容和权限 |
| R62-M2 | P2 | `cmd/license-authority/trial_handler.go` 用 Redis `SETNX` 占邮箱一年；当前查询未发现数据库邮箱唯一约束 | Redis 丢失/淘汰/绕过后，同一邮箱可以重复领取试用 | 加规范化邮箱的 DB 唯一/事务约束，Redis 保留限流功能；验证并发请求、Redis 故障、已有重复数据迁移 |
