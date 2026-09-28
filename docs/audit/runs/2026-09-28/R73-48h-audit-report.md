# R73 · 48h 全面审计执行报告

> 日期：2026-09-28（Asia/Shanghai）
> 状态：进行中；D01–D17 仅更新了本轮关注点，完整域审计未完成；F02/F03/F04/F06/F08/F09 本地有实现及定向验证记录；本轮二审又修正 F02 reservation、F04 遥测和 F06 Embeddings 标记缺口；F07 仅完成源码/静态契约核验，数据库验证仍未完成。
> 主审范围：2026-09-25 23:01:34 至 2026-09-27 23:01:34，`78d91b94..c7104b141`。
> 当前被审代码：`main` / `origin/main`，HEAD `95e243b819984804bb8ee567a6289219af1abd8f`；本报告包含工作区未提交的 F03/F06 修复。

## 1. 范围与基线

| 区间 | 非 merge 提交 | 唯一变更文件 | 审计处理 |
|---|---:|---:|---|
| 冻结的用户要求 48h 窗口 `78d91b94..c7104b141` | 89 | 241 | 不移动边界，逐域审计 |
| 审计漂移 `c7104b141..5fe87cadc` | 31 | 185 | 与主窗口分开报告 |
| 后续远端漂移 `5fe87cadc..2d119529f` | 1 | 1 | 纳入当前 HEAD 审计；增加真实上游 Vapeur probe 测试 |
| 当前继续漂移 `2d119529f..95e243b81` | 6 | 73 | 核验 R73/R12 修订、存储观察及 R62 激活安全 handoff |
| 合并观察区间 | 127 | 435 | 作为执行覆盖面，不替代冻结主窗口 |

远端同步后 `main` 与 `origin/main` 同为 `95e243b81`。当前工作区保留审计报告/总控文档，以及 F02/F03/F04/F06/F08 修复和回归测试；未覆盖的他人/前序改动不作清理。

## 2. 工具与构建证据

- Codegraph 3.7.0 native 图谱在当前 HEAD 全量构建成功：6061 个文件、147288 个节点、261668 条边；统计报告 1 个文件级环、11 个函数级环。图谱用于定位，所有发现必须复核源码与调用方。
- 增量构建遇到 SQLite `database disk image is malformed`。本轮将损坏缓存移到 `/tmp/llm-gateway-go-3-codegraph-malformed-20260928-continue` 留存后，用 `codegraph build --engine native --no-incremental .` 全量重建成功。CLI 不支持 `codegraph update` 子命令，以本地 `codegraph --help` 列出的 build 命令执行。
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
| R73-F04 | D05 Ollama overflow recovery / P2 | 已修复，补齐压缩遥测后待最终回归重跑 | Ollama 按候选窗口预压缩；明确 context-length 4xx 触发共享 recovery，context marker 限制最多一轮内部重试；二审发现此前成功路径未带压缩遥测 | 定向测试断言 reason/strategy/meta；真实 tokenizer/provider 未验证；>1M handoff 仍待客户端契约 |
| R73-F05 | D07 request_logs archive cadence / P2 | HEAD 修复且定向验证通过 | `3909d56e0` 已把 03 点归档门限接到 1h cleanup ticker，解除 24h ticker 的启动相位锁定 | 每日 03:00–04:00 内每天必有一次 tick；定向 bg 测试通过；PG 容量风险归 F07 |
| R73-F06 | D09 selfcheck rate limiting / P2 | 修复并追加复核 | 任意 429 原先合并成一种 worker-wide 状态；二审发现 Embeddings 已 throttled key 分支漏设 gateway 标记 | 全部本地 key-admission 429 标 `shared_key`，provider 错误响应路径过滤保留 header；selfcheck 仅对可信标记建立一轮冷却 |
| R73-F07 | D07 migration 754 / P2 | 源码结论已确认，数据库待验证 | SQL 注释明确函数内未执行 `SET LOCAL statement_timeout`；Go 侧最长 30m context 调用整组分区扫描，数据库 timeout/容量未知 | 未连接兼容 PG；本轮不以 Go context timeout 代替数据库验证 |
| R73-F08 | D14 stream safety / P2 | 定向测试通过 | SSE 拦截 writer 原无帧大小上限，`finish()` 还会把不完整尾帧原样写给客户端 | 损坏/恶意上游可导致无界内存增长；尾帧跳过脱敏链 | 本地设 16 MiB 帧上限、支持 LF/CRLF；超限失败关闭，不完整尾帧丢弃并记录安全元数据 |
| R73-F09 | D14 stream sanitization / P2 | 已确认，待设计修复 | 同一个 SSE 事件内的多次 `Write` 已正确缓冲；完整事件之间拆开的 placeholder 仍被无状态 interceptor 分片透传 | 用户看到 `{SENSITIVE:...}` 片段且映射值不能恢复 | 需有界、TTL 清理的跨事件 content/tool delta carryover；当前回归测试记录缺口，生产端到端尚未验证 |

## 5. 待完成验证与交付

1. 复核近 96h 供应商协议、mock probe、hotzone、Jev、probe 成本、auto-route、session storage 与 R72 方案/报告，登记代码偏差。
2. D01–D17 完整全链路审计仍未完成；本轮只记录与 F02–F09 相关的范围。D05 SF-01（无原始错误 body 日志捕获）和 D12 候选选择完整调用链复核仍明确未完成；其他域也不能因计划改写或勾选视作已审计。后续每条发现必须提供 `file:line`、触发输入、调用路径、期望/实际结果、影响、测试证据。
3. F04 与 F08 已实施并定向验证；仍需完成 F07 的兼容 PG/statement timeout/数据量验证、F09 的跨 SSE 事件脱敏设计，以及 R62-H1/H2/M1/M2 的独立安全方案与风险裁定。
4. 运行受影响测试、`-race`、兼容性与可用本地集成测试；未具备凭据/PG 环境时逐项标未验证，不接触生产库。
5. 更新本报告、`docs/全面审计.md`、域计划/报告和 playbook；最终复审 diff，再提交、push 并核验远端包含提交。

## 6. 本轮继续执行证据

| 命令 | 结果 |
|---|---|
| `codegraph build --engine native --no-incremental .` | 退出码 0；6061 文件 / 147288 节点 / 261668 边 | 损坏图数据库已移至 `/tmp/llm-gateway-go-3-codegraph-malformed-20260928-continue/` |
| `go test -race ./cmd/gateway -run 'TestLiteRequestLogSink_(ConcurrentReplayJournalsOnce|JournalClaimSerializesSameRequestID|FailedJournalRetryReusesTurnNumber|TurnReservationsStayBoundedAndRetainNewReservation)' -count=1` | 退出码 0 | 覆盖同 ID 并发、等待唤醒、失败重试复用 turnNo、容量上限与过期 ID 重用 |
| `go test ./domains/streaming/executors -run 'TestExecute(OpenAI|Anthropic).*DetachedUpstreamContext|TestExecuteOllama_StreamReaderUsesDetachedUpstreamContext|TestOllamaStream(CanceledReadErrorIsClientCancellation|ClientWriteFailureIsCanceled)|TestCopyNonStreamResponseHeaders_ReplacesWireHeaders' -count=1` | 退出码 0 | 覆盖 OpenAI chat/Responses、Anthropic adapters、Ollama 读写取消与保留 header 过滤 |
| `go test ./bg -run 'Test(SelfcheckRateLimitAbort|SelfcheckProvider429DoesNotSeedGatewayCooldown|SelfcheckRound429Classification)' -count=1` | 退出码 0 | gateway 标记与 provider 429 分开测试；真实 gateway 到 provider 端到端待集成验证 |
| `go test -race ./domains/streaming -run 'TestEmbeddingsHandlerMarksThrottledAPIKeyAsGateway429' -count=1` | 退出码 0 | API key 已 throttled 的 Embeddings 429 带可信 gateway scope |
| `go test -race ./domains/streaming/executors -run 'TestExecutor_ExecuteOllama_ContextLengthRecoveryRetriesOnceWithSmallerBody' -count=1` | 退出码 0 | Ollama 最多一次内部重试、请求体缩小、成功返回并带压缩 reason/strategy/meta |
| `go test -race ./domains/streaming -run 'Test(InterceptingStreamWriter|SanitizeRestoreInterceptor_PlaceholderSplitAcrossCompleteFrames)' -count=1` | 退出码 0 | F08 SSE framing 测试通过；F09 跨完整 delta 事件的占位符拆分测试仍确认缺陷存在 |
| `go build ./... && go vet ./... && git diff --check` | 退出码 0 | 最终版本地 build/vet 和空白检查通过 |
| `go test ./... -count=1` | 非 0 | 多数包通过；`discovery/TestDiscoverGateways` 因 IPv6 mDNS `no route to host` 失败；`upstream/TestDo_DNSFailureReservedTLD` 实际分类 `transient`、期望 `network`；`plugin-runtime/TestExecCommand_StartsRealProcess` 与 `TestExecCommand_GracefulStopSIGTERM` 分别因无 PID 文件/未及时装好信号处理器失败。两条完整测试任务同时运行，资源争用是可能解释但未证实；不能记作全仓 PASS。 |
| `go test ./plugin-runtime -run 'TestExecCommand_(StartsRealProcess|GracefulStopSIGTERM)' -count=1` | 退出码 0 | 上述两个 plugin-runtime 子进程用例在隔离定向重跑时通过；这不改变全仓测试非 0 结论 |
| `go test ./bg -run 'Test(RequestLogsArchiveScheduleIndependentOfDailyTickPhase|PartitionManagerSchedulesRequestLogsArchiveIndependently|Migration754_NeverDeletesFromSource|ClampRequestLogsArchiveDays|ArchiveOldRequestLogs|ShouldRunRequestLogsArchive)' -count=1` | 退出码 0 | HEAD 已把每日门限接入 1h cleanup ticker；未连 PG |
| `go test ./domains/streaming -run 'Test.*RateLimit|Test.*rateLimit' -count=1` | 退出码 0（先前记录） | 本轮追加标记接线后将随全包验证复跑 |
| `go test -race ./... -count=1` | 大多数包通过；`discovery/TestDiscoverGateways` 因本机 IPv6 mDNS `no route to host` 失败；`domains/dispatch/TestConcurrentSubmitStopRace` 报既有数据竞争；`upstream/TestDo_DNSFailureReservedTLD` 分类期望 `network` 实际 `transient`；`upstream/TestDo_SlowBodyCancellationPropagates` 在本机 transport 下 headers 未及时建立 | 均未指向本轮 F03/F04/F06 改动；需在隔离网络/DNS fault-injection/dispatch 专项环境复核 |

最终限制：build/vet 已通过；全仓 race 已执行但被上述环境/既有竞态与 upstream fault 测试失败；没有兼容 PG、Redis 或有效供应商凭据，因此 migration 754、真实 provider probe、full/lite 部署集成仍未验证。

## 7. 近 96h 方案审阅中新确认的凭据风险

复核窗口内方案及当前 HEAD 后，发现最新 [R62 激活安全交接](../../../handoff/2026-09-28-r62-activation-security-audit-handoff.md) 列出的四项凭据问题仍能在当前源码中定位。当前先登记风险与迁移门禁；refresh token 哈希化及 activation code 加密/迁移需先确认密钥管理、双读回滚和历史数据策略，不以临时字符串替换冒险上线。

| ID | 等级 | 当前证据 | 影响 | 后续方案/验证 |
|---|---|---|---|---|
| R62-H1 | P1 | `licensing/offline.go` 与 notifier 记录明文 `activation_code`；`licensing/store_pgx.go` 明文落库；`admin/ops_overview.go` 返回明文最近记录 | 有日志/DB/运维面读取权者可取得仍有效激活凭据 | 先定 hash 无法再现码时的通知/重取契约，或采用 envelope encryption；掩码日志/API 并迁移遗留行；覆盖 DB/API/日志快照 |
| R62-H2 | P1 | `center/store_pgx.go` 用明文 refresh token 等值查询和更新 | 数据库备份/只读访问可直接重放 refresh token 换 instance token | 采用服务端 pepper 的 HMAC 索引或加密存储；双读/回填/切换/撤销过期明文策略，测试并发轮换和旧客户端 |
| R62-M1 | P2 | `installer/internal/activation/auto_activate.go` 在 trial 路径把完整 `LicenseKey` 写入 `activation.json` | 安装状态备份/诊断包扩大泄漏边界 | 状态文件仅存指纹/掩码；凭据转入 0600 独立文件或注册后删除；验证升级兼容和权限 |
| R62-M2 | P2 | `cmd/license-authority/trial_handler.go` 用 Redis `SETNX` 占邮箱一年；当前查询未发现数据库邮箱唯一约束 | Redis 丢失/淘汰/绕过后，同一邮箱可以重复领取试用 | 加规范化邮箱的 DB 唯一/事务约束，Redis 保留限流功能；验证并发请求、Redis 故障、已有重复数据迁移 |
