# IR 与路由域边界（internal/ir ⇄ domains/dispatch + executors）

> 定位：验收基准文档，供下一轮审计判定「IR 定义清晰」使用。
> 事实来源：全部论断来自对当前工作树的 grep/read 亲核（2026-10-01，基线 HEAD 0e4b4b414 + 工作区改动），每条带 file:line。行号会漂移，结构名/function 名是主锚点。

---

## 0. 这份文档为什么存在

审计 objective 要求「IR 定义清晰，包括路由数据/流程跟踪/调度瀑布/凭据」。R72 §4（docs/全面审计v3/2026-10-01/42-R72-IR核心数据结构与多协议转换.md:210-224）实测：**`internal/ir` 里没有 tenant、没有 credential、没有调度瀑布、没有轮次索引**——它们在 `domains/dispatch` + `domains/streaming/executors`。R72 的判断是：

> 这本身是合理分层（把协议转换与网关内路由可观测性混在一个结构里反而是灾难），但没有一份文档说明两者的边界，因此「IR 定义清晰」这个验收项无法判定。

本文补上这份边界说明。**验收口径**：「IR 定义清晰」应按 `internal/ir` 的协议表示职责判定（§2）；路由数据/流程跟踪/调度瀑布/凭据不在 IR 内**是设计而非缺失**（§6），它们的验收落在 `domains/dispatch` 与 executors（§3）。

---

## 1. 一句话边界

- **`internal/ir` = 协议表示层**：跨供应商不变的消息/流式/响应中间表示，加上协议转换必需的载体字段（源协议、非标参数 Extensions、请求类别、异常上报钩子）。它**不知道**网关内部的存在——不知道租户、凭据、队列、瀑布。
- **`domains/dispatch` + `domains/streaming/executors` = 网关内路由层**：调度瀑布、凭据/负载均衡、准入 Governor、分维索引、执行轨迹 journal。
- **依赖方向**：`internal/ir` 只 import `errorsx`、`internal/{jsoncol,paramreg,reasoncap,reasonnorm}`、`modelname`，**零 domains 依赖**；`domains/dispatch` 反向**禁止** import `internal/ir`（E14，domains/dispatch/journal.go:76-80，由 journal_mirror_test.go:16-17 钉住），两侧只通过镜像常量与标量字段交接。

---

## 2. internal/ir 的职责清单（协议表示层）

| 结构 | 位置 | 职责 |
|---|---|---|
| `InternalRequest` | internal/ir/types.go:57 | 入站请求统一表示；字段集 = OpenAI Chat Completions ∪ Anthropic Messages 的超集（types.go:59-61 头注）。携带 `SourceProtocol`（types.go:209）与 `Extensions`（types.go:219，客户端非标顶层字段无损往返载体） |
| `Message` / `ContentBlock` | internal/ir/types.go:268 / :282 | 统一消息与内容块（文本/图片/工具调用/思考块等） |
| 协议常量（5 个） | internal/ir/types.go:31-54 | `openai-chat` / `anthropic-messages` / `gemini-generate` / `openai-responses` / `ollama-chat`；R72 更正过「Gemini/Ollama 未接入」的过期注释（types.go:26-28、:47-53） |
| `InternalResponse` | internal/ir/response.go:24 | 上游响应统一表示，同样携带 `SourceProtocol`（response.go:33，标记「从哪个上游解析而来」） |
| `StreamChunk` | internal/ir/stream.go:21 | 流式块统一表示（delta/usage/done/error 四型，stream.go:103）；`StopReason` 保留源协议原生终止原因以保证 Anthropic→Anthropic 无损（stream.go:39-47） |
| `RequestClass` / `ClassOf` | internal/ir/class.go:15 / :36 | 网关内请求类别（immediate/scheduled）**以 Go 结构体元数据形式**搭 IR 便车；四个协议序列化器显式字段化、绝不外发（class.go:19-21，`TestSerializersDoNotLeakRequestClass` 钉住，class_test.go:40） |
| `ParseError` | internal/ir/parse_error.go:14 | 解析错误的分类包装，`Kind` 直接复用 `errorsx.ErrorKind`——IR 层与错误分类体系在此对齐 |
| `AnomalyEvent` / `AnomalyReporter` | internal/ir/anomaly_reporter.go:79 / :129 | 格式异常上报钩子（parse/serialize 各 report\* 调用）；生产侧由 `logging.LockFreeAnomalyReporter` 实现（cmd/gateway/main.go:264、:1724） |
| `Metadata` | internal/ir/types.go:545 | 网关内部元数据载体（RequestID 等）；**序列化器只透出 user_id，RequestID 永不进上游请求体**（domains/streaming/executors/ir_request_stamp.go:1-13 头注） |

Parser/Serializer 按 3 层架构组织（types.go:8-24 的图）：入站 N 个 Parser → IR → 出站 N 个 Serializer，新增协议只需一对 Parser+Serializer（O(N) 而非 O(N²)）。IR 层不感知任何路由语义。

**IR 里刻意没有的东西**：tenant、credential、调度瀑布、轮次索引、队列深度——见 §6。

---

## 3. 网关内路由层的职责清单（domains/dispatch + executors）

| 概念 | 位置 | 职责 |
|---|---|---|
| 调度瀑布 | domains/dispatch/waterfall.go:13（`WaterfallRequest`） | 单请求 9 段时间线（arrive→total 队列→model 队列→cred 队列→forward→response），服务 admin waterfall API；pipeline.go 在各转移点采样。相关投影还有 journey.go / queue_projection.go |
| 凭据/权重负载均衡 | domains/streaming/executors/router.go:1071（`planByTier`） | P2C（:1093 `p2cOrder`）+ 平滑加权轮询首跳（:1128/:1134 `promoteWeightedCandidateWithWeights`）；权重来源 provider/client.go:1920 的 `applyCapacityWeightedLB`（R73 §2 证实此链路生产生效）。**dispatch 侧按设计不重复打分**——拿到排序后的候选取第一个（R73 §2） |
| 准入 Governor | domains/dispatch/governor.go:24（`Governor` 接口） | per-credential 并发/RPM/TPM 准入；`concurrencyGovernor` 是标量 cap/used 计数器，无权重概念（governor.go:77 段注释，2026-10-01 订正） |
| 队列与集群准入面 | domains/dispatch/queue_backend.go:8-32 | Tier-0 等待室 + lane 容量 + due ZSET，memory/Redis 双后端；fail-open 方向与 Governor 相反是刻意设计（queue_backend.go:24-32「Do not unify」） |
| 分维索引 | domains/dispatch/dimension_index.go:112（`DimensionIndex`）/ :40（`DimensionEntry`） | 按 model/credential/provider 维度的**请求成员索引**，「NOT an execution queue: it never gates scheduling」（dimension_index.go:10-12）——纯可观测性 |
| 执行轨迹 journal | domains/dispatch/journal.go:177（`JournalSnapshot`） | 每请求 attempt 级执行轨迹（环形缓冲 128 条，journal.go:70-75），journey/waterfall 投影的数据源 |
| 执行编排 | domains/streaming/executors/executor_dispatch.go | 把路由候选喂给 dispatch（:83 注入 `RouteFunc`）、执行 forward、把结果按 `ForwardOutcome` 交回 |

---

## 4. 交接点（grep 亲核的交接面）

两侧**不共享结构**（E14），交接全部走标量字段、镜像常量与回调：

1. **`domain.TransportContext`（domain/transport.go:10）是主交接载体**。executor 每次尝试填 `ClientProtocol` / `UpstreamProtocol`（transport.go:21-22）、`ClientCatalogCode` / `UpstreamCatalogCode` / `ProviderID`（transport.go:27-40）、`RequestClass` / `DueAt`（transport.go:42-44）；`TransportIRConverter.Parse*` 把它们盖到返回的 IR 上（domains/transformation/ir_converter.go:206、:221、:236、:370 的 `stampRequestClass`；ParseOpenAI :360、ParseAnthropic :376）。
2. **请求类别镜像常量**：`ir.RequestClass`（class.go:15）⇄ `dispatch.RequestClassImmediate/Scheduled`（journal.go:77-80）。dispatch 不 import ir，靠 journal_mirror_test.go:16-17 钉住两侧字面量一致；executor 侧经 `params.DispatchDueAt` 写回 `qr.DueAt`（executor_dispatch.go:456）。
3. **`ForwardOutcome`（domains/dispatch/queued_request.go:34）是执行结果交接**：`Result any` 对 dispatch 不透明（「dispatch never inspects it」，queued_request.go:35-37，实际载荷是 `*executors.ExecuteResult`）；`ErrorKind string` 是 executor 预分类的诊断字段（queued_request.go:52-54），生产填充点 executor_dispatch.go:838（circuit_open → `errorsx.KindCircuitOpen`）与 :1136；`FatalCredential` 由 executor 经 `errorsx.IsCredentialFatal` 预计算，使 dispatch 无需依赖 errorsx 之外的语义（queued_request.go:43-51）。
4. **路由回调注入**：executor 构造 Pipeline 时注入 `RouteFunc: e.dispatchRoute`（executor_dispatch.go:83）；`dispatchRoute`（:101）→ `Router.PlanCandidatesPinned`（:114）→ 软排序 `dispatchRouteSoftRank`（:143）→ 返回 `[]dispatch.CredentialRef`。`CredentialRef`（queued_request.go:21-31）是路由结果的最小标量投影（ID/限额/vendor），不含凭据密钥与对象。
5. **异常上报对账**：executor 把网关 request id 盖进 `ir.Metadata.RequestID`（ir_request_stamp.go:21 `stampIRRequestID`，全序列化面），使 IR 层 format-anomaly 事件能与 request_logs/泳道对账——IR 负责报，路由层负责给身份。

---

## 5. 为什么这样分层

1. **变更轴不同**。协议轴的变更（新协议、新内容块、非标参数）发生在供应商生态；路由轴的变更（配额、瀑布阶段、故障转移）发生在网关运营。混在一个结构里，任何一侧变更都要动同一个类型——R72 §4 的原话：「把协议转换与网关内路由可观测性混在一个结构里反而是灾难」。
2. **E14 依赖环约束**。dispatch 需要被 executors 注入路由回调，而 executors 需要 ir 做协议转换；若 dispatch 再 import ir，则 ir↔dispatch↔executors 形成环。domains/dispatch/journal.go:76 明写「dispatch must NOT import internal/ir (E14)」，镜像常量 + 测试钉住是既定解法。
3. **序列化面必须可证伪地「干净」**。IR 是唯一会被序列化进上游请求体的层；把网关内部字段（凭据、租户、队列状态）塞进 IR，每加一个字段都要重新证明「不会泄漏给供应商」。现在的答案收敛为两条可测试的不变式：序列化器显式字段化（`TestSerializersDoNotLeakRequestClass`，class_test.go:40）+ `Metadata.RequestID` 永不外发（ir_request_stamp.go:12-13）。
4. **无损失真换有界复杂度**。协议超集字段（如 `StopReason`，stream.go:39-47）服务无损往返；路由语义若进 IR，只会让「超集」失去边界。新增协议 O(N) 的复杂度承诺（types.go:29-31）依赖 IR 字段集封闭。

---

## 6. R72/R73 两轮审计已确认的边界事实（设计而非缺失）

1. **`internal/ir` 无 tenant / credential / 调度瀑布 / 轮次索引**——纯协议表示层，R72 §4 实测并认定为合理分层（42-R72-…md:212-214）。
2. **dispatch 与 ir 之间无 import**（E14），常量镜像由 journal_mirror_test.go 钉住（domains/dispatch/journal.go:76-80）。
3. **分维索引不是执行队列**：它回答「这个凭据/模型/供应商上最近跑过（或还在等）哪些请求」，从不门禁调度（dimension_index.go:10-12）；成员靠 TTL/容量淘汰而非终态移除（dimension_index.go:14-16）。
4. **`QueuedRequest` 不可序列化**：它携带 goroutine、客户端连接与 ResultCh，Redis 队列后端只做集群级准入/容量/due 可见性，**从不在实例间搬运执行**；跨实例工作窃取属于默认关闭的 durable lane（queue_backend.go:15-21「CONNECTION AFFINITY (non-negotiable)」）。
5. **准入面 fail-open 与限流面 fail-closed 方向相反是刻意的**（queue_backend.go:23-32，`TestRedisQueueBackendFailOpen` / `TestPipelineQueueBackendFailOpen` 固化；R73 §4 第 1 条）。
6. **权重负载均衡不在 dispatch**：完整链路在 provider/client.go `applyCapacityWeightedLB` → executors/router.go `planByTier`（P2C + 平滑 WRR + 四维惩罚彩票）；dispatch 只取排序后第一个有容量的候选（R73 §2，含对其初查误判的更正）。`pool/scheduler` 的 WRR 与 `domains/credential` 的 BanditScorer 是**有登记的死代码**（bg/bandit_flusher.go:1-3、router.go 头注），不是缺失的机制。
7. **凭据选择相关的已知债在路由层登记，而非 IR**：R73 §3 的 lease 续期缺失（`Renew` 零生产调用者）、queueDueKey 无 TTL、mirror 键无 instance 段等，全部属于 domains/dispatch 的待办（2026-10-01 已把对应注释订正为实现事实陈述）。
8. **IR 侧「按项目/任务聚合观测」被 ADR-0003 阻塞**：`ir.Metadata.Project/Tags/TurnTotal` 与 requestfact/requestarchive 子系统是「契约就绪 / 生产待接线」，非死接线（R72 §5，42-R72-…md:226-243）。

---

## 7. 审计操作指引

- 判「IR 定义清晰」：以 §2 清单核对结构职责与字段语义（含 §6 第 1 条的「刻意没有」清单）。
- 判「路由数据/流程跟踪/调度瀑布/凭据」：以 §3 清单核对 domains/dispatch + executors，不要拿它们去 internal/ir 里找。
- 判交接正确性：以 §4 的五个交接点为面，任何一侧新增跨域字段都应落在这五个点上，否则先过 E14 评审。
