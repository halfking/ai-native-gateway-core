# R73 修复方案与执行门禁

> 日期：2026-09-28（Asia/Shanghai）
> 范围：冻结的 48h 审计窗口、其后明确标记的提交漂移，以及 96h 方案文档的代码核对。
> 状态：执行中；F01/F05 已在审计基线修复；F02/F03/F04/F06/F08 已提交并有定向证据；F09 跨 delta carry 与 F10 empty-map/Redis-error masking 已由 `9fe387ac8` 提交并通过定向/race 验证。F07 与 R62 凭据风险需外部数据库/迁移方案验证；D01–D17 全域审计未完成。

## 1. 执行原则

1. 每个缺陷都要有源码调用路径、触发条件、影响、回归测试和复核结果；方案或注释不能代替实现证据。
2. 仅修复对调用方可观察行为有缺陷的代码。涉及协议、存储格式或数据库迁移的改动先核对兼容面；无法安全判源的错误不得猜测分类。
3. 客户端体验优先：供应商重试/压缩应在网关内部完成；若不能恢复，沿现有协议输出可恢复的终态错误，不泄漏凭据、原始敏感内容或内部堆栈。
4. 所有异步路径必须定义取消语义、超时、重试上界、幂等键和失败可观测性；所有定时任务必须按实际 tick 相位验证。
5. 每项验证均记录环境限制。没有 PG/Redis/有效供应商凭据时，不能把 SQLite/mock/编译结果表述为生产集成验证。

## 2. 缺陷与整改项

### R73-F01 · P2 · Vapeur live probe 测试错误路径会 panic，且第二个探针可能意外产生费用

- **证据**：`admin/provider_probe_live_vapeur_test.go` 中两个测试曾在检查 `err` 之前解引用可能为 nil 的 probe 结果；仅设置 `VAPEUR_API_KEY` 就会额外请求 chat endpoint。
- **修复**：加入 nil-safe 日志状态辅助函数；断言测试先判错再检查结果；额外 chat 探针要求 `VAPEUR_LIVE_CHAT_PROBE=1`。
- **状态**：已修。离线 helper/路由测试通过；因没有 `VAPEUR_API_KEY`，真实 Responses/chat 上游请求未运行。
- **验收**：重复执行定向 admin 测试；确认未设置 chat 专用开关时不会产生第二次上游请求。

### R73-F02 · P2 · lite telemetry 的重复终态写入存在 check-then-mark 竞态

- **证据**：`cmd/gateway/lite_telemetry_sink.go` 原来先用单独加锁的 `alreadyJournaled` 检查，再写 session/body/turn，最后才 `markJournaled`。telemetry worker 与队列打满后的同步 fallback 可并发处理同一 RequestID，并分别分配 turnNo。
- **影响**：同一逻辑请求可能重复进入会话轮次；request_logs 的 UPSERT 幂等不能约束多表 journal。
- **修复**：用 `journalBusy` 在 journal 操作前原子预占 request_id；同 ID 并发调用等待结果，失败释放预占；turnNo 以 24h 有界 reservation 绑定到请求 ID，部分写入失败后的重试复用相同轮号。二次审查发现旧 FIFO 会累积已完成 ID，且过期 ID 重用时旧项可能误删新 reservation；已移除辅助 FIFO，按过期时间清理/淘汰，并添加容量与 ID 重用回归。
- **状态**：本地代码已修；并发、锁等待、body 写失败重试、容量/ID 重用和原有幂等路径均通过定向 `-race`。
- **验收**：`TestLiteRequestLogSink_ConcurrentReplayJournalsOnce`、`TestLiteRequestLogSink_JournalClaimSerializesSameRequestID`、`TestLiteRequestLogSink_FailedJournalRetryReusesTurnNumber` 及原有幂等/全链路测试通过。
- **边界**：reservation 与已完成 RequestID 集仅为进程内有界缓存，前者 TTL 24h；跨进程或进程崩溃跨越 body 与 metadata 两步时仍依赖 reconcile，不能宣称数据库级多表原子性。

### R73-F03 · P2 · 显式 session stream 使用可取消的客户端 request context 读取上游

- **证据**：流处理器在显式会话模式设置 `StreamSurvivesClientCancel` 并将上游 HTTP 请求放入脱离客户端取消的 context；但多个 executor 又把 `params.R.Context()` 传给流 reader。客户端断开后 reader 取消，耐久捕获/会话落盘因此中止。Native Responses 还可能把 `context.Canceled` 记成 `UpstreamDown` 并影响 circuit 状态；Ollama scanner 错误也可能被误分类为网络故障。
- **整改**：reader 使用有超时、但不继承客户端断连取消的 upstream context；普通请求仍保持断连即取消；区分客户取消与上游网络故障，不能把用户取消记入供应商失败统计。
- **状态**：本地修复并通过定向测试；OpenAI chat/native Responses/adapters、Anthropic passthrough/translators 和 Ollama stream reader 均使用 upstream response context；Ollama client write/read cancellation 归为 `KindCanceled`。
- **验收**：定向用例模拟已取消客户端请求并断言 reader 获得未取消且有上限的 upstream context；输出继续完成；普通流取消语义由现有 `executor_prestream_test.go` 覆盖。全仓 race 与 durable-store 集成仍待执行。

### R73-F04 · P2 · Ollama 路径缺少上下文预压缩及 provider overflow 内部恢复（已修复）

- **证据**：`domains/streaming/executors/executor_ollama.go` 当前执行路径未调用通用 `CompressMessagesIfNeeded`，其 4xx 响应只分类并返回，没有接入 `handleContextLengthRecovery`。文件中的实现注释明确说明恢复逻辑被后续搁置。
- **影响**：大上下文在可压缩时仍可能直达 provider 并以错误结束；供应商返回上下文超限也未触发有界压缩重试，违背“保持客户端连接并内部恢复”的要求。
- **整改**：先保留原始请求与原始轮次数据；请求前按模型窗口阈值压缩；对明确识别的 context-length 响应最多内部恢复重试一次，重新序列化压缩后的请求，并更新 usage/telemetry/错误阶段；重试不可递归、不吞取消、不重复计费。无法成功压缩时返回原有兼容错误。二审发现成功路径缺少 `CompressionReason/Strategy/Meta`；现已补入预压缩和 overflow recovery 元数据并增加字段断言。
- **状态**：已实施并通过 Ollama httptest 的定向 race 测试；候选窗口预压缩和 provider context-length recovery 共用原始入站 body；context marker 阻止递归；两类压缩路径都记录压缩元数据。
- **验收**：已通过 Ollama httptest：首个 context 4xx 后仅一次压缩重试、第二次请求体变小并成功，结果含 reason/strategy/byte metadata；context marker 防止再次递归。真实 tokenizer/provider probe 仍未验证。
- **产品开关**：>1M token 默认 handoff + `/new` 是客户端交互策略，网关不能自行伪造客户端命令；需先确认 tokenizer/阈值定义、handoff 通道及可配置开关，再实现。审计中先验证网关侧可观测检测和明确的内部响应契约。

### R73-F05 · P2 · request_logs 归档日程受启动时刻相位锁定

- **证据**：生产 `PartitionManager` 主周期为 24h；归档函数只在本地时间 03 时通过 gate。服务在其他小时启动后，后续 tick 每天都落在相同小时，因而可能永远不归档。现有纯函数测试只验证小时门禁，没有模拟生产 tick 相位。
- **修复**：HEAD 的 `3909d56e0` 将 `archiveOldRequestLogs` 从 24h 相位 ticker 移到 1h `runCleanup` ticker；保留 03:00–04:00 的日历小时门限，启动相位每天都会有一次 tick 命中。无需再造独立 timer。
- **状态**：HEAD 已修；源码调用 wiring 和归档 SQL 不 DELETE 守卫定向测试通过。
- **验收**：`go test ./bg -run 'Test(RequestLogsArchiveScheduleIndependentOfDailyTickPhase|PartitionManagerSchedulesRequestLogsArchiveIndependently|Migration754_NeverDeletesFromSource|ClampRequestLogsArchiveDays|ArchiveOldRequestLogs|ShouldRunRequestLogsArchive)' -count=1` 退出码 0。定时器由 1h ticker 生命周期管理；真实 PG 执行仍归 F07。
- **运维限制**：迁移 754 每次都会遍历所有已过期分区并 `ON CONFLICT DO NOTHING`，暂无完成 ledger，因此即使修好定时器，历史分区仍可能重复扫描；这属于独立容量/SQL 优化项。

### R73-F06 · P2 · selfcheck rate-limit 跨轮状态把单凭据限流扩散到其他凭据

- **证据**：每轮只挑一个 due credential；`lastCycleRateLimited` 却是 worker-wide，任何 clean credential 都会清除前一凭据的状态。分类器把任意 HTTP 429 归入同一类别，既无法区分 provider 429，也无法区分 gateway 自身的共享 selfcheck key/RPM 限流。
- **影响**：凭据 A 限流后，凭据 B 可能错误地只探测 primary；或不同 credential 继续重复撞击实际被全局限流的共享 key。
- **整改**：为 gateway 自身共享 key/RPM 拒绝增加不可被上游透传/伪造的来源标记，并用于短时 worker-global cooldown；能明确归属 provider 的 429 只按 credential ID 维护；未知来源只终止当前 credential 当前轮，不写跨轮共享状态。
- **状态**：本地实现并通过定向测试。二次复核发现 Embeddings `keyInfo.Status == throttled` 分支漏设标记，现已补齐并新增 handler 回归；Chat/Responses/Messages/Embeddings 的 gateway 自身拒绝均设置保留响应 header；通用非流式响应拷贝及 Anthropic/Ollama 错误 passthrough 会滤除 provider 同名 header；selfcheck 只对可信 `shared_key` 标记建立一轮 cooldown。
- **验收**：`TestSelfcheckRateLimitAbort` 覆盖 gateway marker；`TestSelfcheckProvider429DoesNotSeedGatewayCooldown` 覆盖未标记 provider 429；通用 response-header copier 测试确保 provider 不能伪造标记。真实完整部署路由端到端仍待集成环境。

### R73-F07 · P2 · migration 754 的 statement timeout 声明/执行边界待数据库核验

- **证据**：迁移头注已订正并明确函数体没有 `SET LOCAL statement_timeout`；Go 侧以一个最长 30min context 调用整个集合函数，函数遍历过期分区与其批次。实际数据库角色/会话 timeout 和生产行数不可从 SQLite/mock 推断。
- **整改**：先确认迁移 SQL、PG role/database statement_timeout、真实分区行数与函数耗时。若没有按批次的 DB 侧边界，设计可恢复、每批独立提交/可续跑的实现；避免在未验证 PG/Citus 环境下盲改迁移。
- **状态**：SQL/调用路径核对已确认 timeout 风险边界；真实兼容 PG 验证待做；不可将 Go context deadline 当成数据库内 `statement_timeout`。
- **验收**：真实兼容数据库在限定数据量下执行并观测单批耗时、锁、超时恢复、重复运行幂等与源数据保留。

### R73-F08 · P2 · SSE 拦截 writer 无界缓冲且不完整尾帧绕过拦截

- **证据**：`interceptingStreamWriter.Write` 原先把所有未出现 `\n\n` 的内容持续追加到 `pending`，没有帧大小边界；`finish()` 则直接将剩余尾字节写到底层 ResponseWriter，不经过响应拦截器。
- **影响**：上游一直不发事件分隔符时，每条并发流可持续占用内存；异常/截断尾事件中的内容可绕过脱敏及其他 response interceptor。
- **修复**：按 LF 或 CRLF SSE 分隔符逐帧处理，完整帧最大 16 MiB；超过上限后清空缓存并返回错误，不把原始帧透传；流结束时丢弃未终止尾帧并记录 request/session ID 与字节数，不记录内容。
- **状态**：已在本地实现；同事件跨 Write、CRLF 跨边界、超大帧拒绝、超大后继帧与不完整尾帧丢弃测试通过。
- **验收**：`go test ./domains/streaming -run 'TestInterceptingStreamWriter' -count=1`；需在全仓测试复核后关闭。
- **边界**：单个合法 SSE 事件超过 16 MiB 会中断该条流；需要代表性音频/图像/工具输出样本确认生产上限是否合适。

### R73-F09 · P2 · 完整 SSE delta 之间拆分的敏感占位符无法还原

- **证据**：生产 writer 只保证每个完整 SSE 事件调用一次 `InterceptStreamChunk`；`SanitizeRestoreInterceptor` 对每个事件单独解析 JSON 并执行正则替换。若模型将 `{SENSITIVE:phone:1}` 切为两个完整 delta 中的前缀/后缀，各事件都不匹配完整占位符，因而原样透传。
- **影响**：客户端看见内部 placeholder 片段，真实映射值没有按原样恢复；同事件内底层网络/Write 拆帧已经由 F08 覆盖，不能解决跨语义事件边界。
- **整改**：已实现 writer/request 生命周期的 `StreamMeta.State` carry，不用全局 map 或 TTL janitor；按 OpenAI choice content/refusal/tool+legacy function-call arguments、Anthropic block index/field、Responses event type/output/content/item id 隔离。每流最多 64 lane、每 lane 256 byte；opaque Responses item id 经 SHA-256 后作为固定长度 lane component。正常文本即刻输出，可能的 marker 尾片暂存。未知完整 marker 遮蔽；已解析到已知字段但 marker continuation 格式失效时遮蔽该字段并继续；不透明 JSON continuation 有待续尾片或 lane 超限时阻断当前及后续帧；stream end 随 writer 生命周期丢弃尾片。
- **状态**：代码与跨协议定向测试已实现；sanitizer targeted race、production writer integration race 通过。生产 writer 串行调用；若其他调用方并发复用同一 meta，须显式共享初始化后的 `StreamState`。
- **验收/限制**：覆盖 OpenAI content/tool arguments、Anthropic text/partial_json、Responses delta、未知 marker/空 map、request/lane 隔离、超限、无效/opaque continuation、未完成尾片丢弃；真实 provider 帧型/网络取消和全域 D14 仍未验证。当前 writer 对 `ShouldBlock` 语义为丢弃帧而非显式终止，需评估客户端终态错误/可观测性。

### R73-F10 · P2 · sanitize map 缺失/Redis 故障时未知 placeholder 透传

- **证据**：`InterceptNonStream` 与 `InterceptStreamChunk` 在 `loadMap` 返回空 map 或 error 时旧逻辑直接 return nil，未调用 `RestoreOutputOrMask`；伪造完整 `{SENSITIVE:...}` 因而会原样离开网关。Redis 故障导致已知值无法恢复可以作为降级，但未知内部 marker 不应裸透传。
- **整改**：空映射时仅当 body/chunk 含 marker 才进入解析；Redis load error 改用空 map 继续 mask。流式空 map 仍走跨事件 marker prefix 处理。
- **状态/验收**：empty-map 与关闭 miniredis 模拟 Redis connection failure 的非流式定向测试通过；stream empty-map split-unknown 测试通过。真实 Redis 故障时延和重试开销仍需部署观测。

## 3. 统一回归门禁

- 修复所属包的定向单测；涉及共享状态、队列、sink 或定时器的并发改动必须运行 `go test -race`。
- 修改传输/协议路径后覆盖流式、非流式、取消、超时、provider 错误和客户端序列化；确保 retries 有上限。
- 修改数据模型/落库/迁移后覆盖序列化往返、重启恢复、幂等、热点表更新删除边界、迁移前后兼容与清理任务。
- 全量阶段再运行 `go test ./...`、`go test -race ./...`（如耗时/环境允许）、`go build ./...`、`go vet ./...`，并执行可用的本地部署集成测试。
- 原 R73 已提交/推送并合流；本续审 F09/F10 代码已提交为 `9fe387ac8`。D01–D17 完整域审计、F07 真实 PG 与本地部署未完成，不因局部修复关闭。

## 4. 最新漂移：R62 凭据安全续审

当前 HEAD 新增的 [R62 激活安全交接](../../../handoff/2026-09-28-r62-activation-security-audit-handoff.md) 描述的四项风险已抽查当前源码并确认仍存在：

| ID | 级别 | 发现与证据 | 执行方案/阻塞 |
|---|---|---|---|
| R62-H1 | P1 | activation code 被写入结构化日志、明文 DB 列和 ops overview 最近记录接口 | 先确定审批后通知、取回和历史未消费码的契约；可选 envelope encryption + API/log mask；需数据迁移和密钥管理方案 |
| R62-H2 | P1 | refresh token 在 `center/store_pgx.go` 明文等值查询/更新 | 设计 pepper-HMAC 或加密方案、双读回填与轮换/撤销流程后实施；需要 PG migration 和安全回归 |
| R62-M1 | P2 | trial license key 明文落本地 `state/activation.json` | 状态 JSON 改存指纹；license key 移至 0600 文件或注册后清除；兼容旧安装状态 |
| R62-M2 | P2 | trial email 唯一性只依赖 Redis `SETNX`，数据库缺唯一约束 | 增加 normalized email 的 DB 事务/唯一约束；先统计及处理历史重复数据，再保留 Redis 限流 |

这些跨主控/installer/DB 的凭据修复不和 F02/F03/F06 混成一个提交；先补专项迁移/回滚与日志/API 合约，再逐项实现和独立验证。
