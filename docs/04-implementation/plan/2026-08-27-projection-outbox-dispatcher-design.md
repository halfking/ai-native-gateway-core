# Phase 3 设计：Projection Outbox + Post-Persist Dispatcher

> 状态：纯设计文档（design-only）。本文档不执行、不注册任何 DDL；所有 SQL 均为**草案，Phase 6 才可评审执行**。
> 日期：2026-08-27
> 上游依据：
> - `docs/04-implementation/plan/2026-08-25-request-session-persistence-final-plan.md` §4（P1 问题清单）、§5.3（durable projection outbox 目标架构）、§6 Phase 3
> - `docs/04-implementation/plan/2026-08-26-request-fact-phase0-contract.md`（Version Contract：`projection_event_version` 已冻结为 1，Phase 0 不发事件）
> - 契约代码：`internal/requestfact/types.go`、`internal/requestfact/codec.go`

## 0. 不建表声明（先读）

本文档只输出设计。**任何 DDL 都不执行、不放进入 installer / migrations / sql 目录**。schema 变更（新表、索引、分区、installer embed/copy、deploy manifest）统一留待 Phase 6，走 `docs/standards/database-change-and-real-verification.md` 的 schema truth preflight、显式列清单、真实环境验证全流程。文中全部 SQL 代码块都标注「草案，Phase 6 才可评审执行」，仅用于语义讨论。

## 1. 背景与定位

Phase 3 的目标是把「V1 事实已落库，但 V2 / stats 派生投影丢失且不可恢复」这类漂移，收敛为一条可重放、幂等、有状态、有告警的异步链路：

```text
request_logs 主事实事务（request_logs_hot + request_logs_bodies_hot + usage_ledger_hot）
    │  同事务 INSERT body-free projection event（outbox）
    ▼
post-persist dispatcher（claim / lease / retry / DLQ / replay）
    ├─ target=session_v2   → sessionv2mirror 转换 + SessionWriterV2（turn+bodies 同事务）
    └─ target=stats        → EventFromTelemetry → stats_event_inbox（沿用 InboxConsumer）
onPersisted 钩子只保留「实时通知 + 缓存失效」
```

固定 Owner（Phase 0 契约）不变：

| Owner | 职责 |
| --- | --- |
| telemetry | `request_logs`(+`request_logs_bodies`) 请求审计主事实 |
| sessionv2mirror | V2 唯一 owner（投影端） |
| URSM | 健康状态，与本设计无关 |
| stats | EventWriter/Inbox（统计事实） |
| Redis mirror | 非权威，不得存完整 body/IR；outbox 不依赖 Redis |
| `internal/requestfact` | 契约唯一 owner；`projection_event_version=1` 已预留（`internal/requestfact/types.go:13`） |

## 2. 现状事实（代码依据）

### 2.1 主事务与钩子发射点

- 主事实事务：`domains/hooks/observability/telemetry/client.go` `insertRequestLog`（:861）在**单个 tx** 内依次写 `usage_ledger_hot`（:891）、`request_logs_hot`（:938，`ON CONFLICT (request_id) DO UPDATE` :1070）、`request_logs_bodies_hot`（`upsertRequestLogBodies` :2158，INSERT :2180）、`api_keys` 累加（:1408 附近）、final-success claim（`claimSessionFinalSuccess` :2087）、session-opened outbox 事件（`insertSessionOpenedEvent` :1471），最后 `tx.Commit`（:1466）。UPDATE 路径为 `updateRequestLog`（:1608）。
- `onPersisted` 钩子只在**主路径成功后**由 `persistRequestLog`（:829-859）触发；注册 API 为 `SetOnRequestLogPersisted` / `AddOnRequestLogPersisted`（:503-527），钩子在 telemetry worker goroutine 上运行。
- 钩子失败不重试、不持久化：`internal/sessionv2mirror/hook.go:104-125` 失败只 `slog.Warn` + 计数 + `appendBacklog`；backlog 是**进程内** FIFO，容量 10000，不落盘（`internal/sessionv2mirror/backlog.go:25`、`appendBacklog` :100-114）。

### 2.2 onPersisted 现有消费者矩阵

| 注册点 | 消费者 | 性质 | Phase 3 去向 |
| --- | --- | --- | --- |
| `cmd/gateway/main.go:1931` | live stream SSE hub | 实时通知 | **保留**（通知类） |
| `cmd/gateway/main.go:2507` | SystemMonitor SSE（probe 行） | 实时通知 | **保留**（通知类） |
| `cmd/gateway/main.go:2630` | `systemmonitor.NewRecentSuccessHook` | 内存去重缓存失效 | **保留**（缓存失效类） |
| `cmd/gateway/main.go:2755` | `routeincident` observer | 事件通知 | **保留**（通知类） |
| `cmd/gateway/main.go:3766` | `statsBoardCache.Record` | Redis 看板缓存 | **保留**（缓存失效类，Redis 非权威） |
| `cmd/gateway/main.go:3775` | `bodyTracker.Record` | 内存统计 | **保留**（缓存失效类） |
| `cmd/gateway/main.go:1983` | `sessionv2mirror.PersistHook`（V2 写 + SessionDim） | **数据库投影** | **移到 dispatcher**（session_v2 target） |
| `cmd/gateway/main.go:3794` | `statsEventWriter.Record` | **数据库事件** | **移到 dispatcher**（stats target） |
| `cmd/gateway/main.go:1966` | `attachmentmirror.PersistHook` | **数据库投影** | **移到 dispatcher**（attachments target，可后置） |

### 2.3 绕过钩子的写入路径（双路径漂移根源）

1. **telemetry 降级 fallback**：DB 失败/队列满时 `fallback.WriteRequestLog` 写 JSONL spool（client.go:596-606、:730-740），钩子不触发；恢复后 `Client.ReplayFallback`（:489-499）直接调 `insertRequestLog`/`updateRequestLog`，仍不触发钩子。
2. **request_wal replay**：`domains/hooks/observability/telemetry/request_logger.go` 的 `ReplayFallback`（:259-308）直接调 `upsertInitial`（:220）/`persistUpdate`（:600）补 `request_wal_hot`，从不触碰 V2/stats。
3. **admin HTTP ingest**：`admin/telemetry.go:341` 在自己的事务里直接 INSERT `request_logs_hot` + `request_logs_bodies_hot`（:436），完全不走 `onPersisted`。

结果：V1 成功而 V2/stats 缺行时没有任何自动补偿——这正是 final plan §4 P1「fallback/WAL replay 可能只补 V1」与「V2 mirror failure 无 durable 补偿」的实测形态。

### 2.4 stats 侧已有 durable 先例

`domains/stats/inbox_consumer.go` 已经在生产运行 claim/lease/fencing/DLQ/replay 全套语义（claimSQL :402-435、markFailedSQL :387-400、replaySQL :437-446、ReplayDLQ :335-354）。但它的入口 `EventWriter.Record`（`domains/stats/event_writer.go:73-101`）是**进程内队列**：队列满走 1 秒同步兜底，仍失败只加 dropped 计数；批量 flush 连续失败 5 次后事件被内存 `deadLettered` 计数吞掉（:125-132），永远到不了 durable inbox。也就是说 stats 有「durable 消费端」却缺「durable 生产端」。本设计的 outbox 正是补这个生产端，并复用其消费端语义。

## 3. Outbox 设计

### 3.1 核心决策 D1：事件 body-free，投影输入由 worker 回读事实表

事件行只携带**投影所需的 key + 版本引用 + 终态摘要**，不携带完整 request/response body，也不携带 IR。投影 worker 认领事件后按 `request_id` 回读 `request_logs_hot`（join `request_logs_bodies_hot` 拿 body）重建 `telemetry.RequestLogEntry`，再复用现有转换链（`sessionv2mirror.entryToProcessedRequest`、`stats.EventFromTelemetry`）。

理由：

1. **契约合规**：`request_logs_bodies(_hot)` 是正文唯一事实侧表；事件里复制 body 会制造第二份正文存储与双写漂移，也违反「Redis/队列不存完整 body」的精神。
2. **单一事实源**：`request_logs_hot` 以 `ON CONFLICT (request_id) DO UPDATE` 支持迟到 enrichment（client.go:1070）。worker 在 dispatch 时回读到的是**最新终态**，天然取到 enrichment 后的数据；若事件内嵌摘要快照，摘要与主表必然漂移（对账噩梦）。
3. **replay/backfill 免费**：幂等补写（§6）只需要 `request_id` 就能从 DB 重建全部输入，不需要进程内存里的 entry——这是把 fallback/WAL replay 统一进来的前提。
4. 事件行足够小（<300B），不显著放大主事务 WAL。

代价：dispatch 时一次回读；且 body 可能已被 `requestBodiesSummaryEnabled()`（client.go:2167）摘要化——live 钩子路径用内存全文、replay 路径用 DB 摘要的既有差异被显性化，列入开放问题 O5。

### 3.2 事件负载字段草案

字段命名对齐 `internal/requestfact/types.go` 的 wire 命名（snake_case、`identity`/`lifecycle`/`routing` 分组语义、`payload_sha256`、`captured_at`），状态机列命名对齐 `stats_event_inbox`（`processing_status`/`processing_owner`/`lease_until`/`fencing_token`/`process_attempts`/`next_attempt_at`/`last_error`/`dead_lettered_at`/`processed_at`），因为该词表已在生产运行且 Phase 5 stats 收敛会共享同一套运维面板。与 final plan §5.3 草图的映射：`available_at ≈ next_attempt_at`、`worker_id ≈ processing_owner`、`status` 拆分为 `processing_status`（调度状态）与 `lifecycle_status`（事实终态摘要）。

Go 侧负载结构（放 `internal/requestfact`，纯结构定义不触 DB）：

```go
// 命名与分组对齐 requestfact.Identity / Lifecycle / Routing（types.go:46-75）
type ProjectionEventPayload struct {
    ProjectionEventVersion int    `json:"projection_event_version"` // = ProjectionEventVersionV1 (types.go:13)
    RequestID              string `json:"request_id"`
    TenantID               string `json:"tenant_id"`
    SessionID              string `json:"session_id,omitempty"`  // stats/attachments target 可空
    TurnID                 string `json:"turn_id,omitempty"`
    TaskID                 string `json:"task_id,omitempty"`
    ParentRequestID        string `json:"parent_request_id,omitempty"`

    ProjectionTarget string `json:"projection_target"` // session_v2 | stats | attachments

    // 终态摘要（body-free，仅调度与快速过滤用；投影数据一律回读事实表）
    LifecycleStatus string    `json:"lifecycle_status"` // 对齐 Lifecycle.Status
    Success         *bool     `json:"success,omitempty"`
    ErrorKind       string    `json:"error_kind,omitempty"`
    CompletedAt     time.Time `json:"completed_at"`

    CapturedAt     time.Time `json:"captured_at"`                       // 事件生成时间（重发比较用）
    PayloadVersion int       `json:"payload_version"`                   // = PayloadVersionV1
    PayloadSHA256  string    `json:"payload_sha256,omitempty"`          // 对账引用，Phase 1 fact 就绪后填
}
```

表列草案（**草案，Phase 6 才可评审执行**；列名与负载一一对应，调度列复用 stats_event_inbox 词表）：

```sql
-- 草案，Phase 6 才可评审执行
CREATE TABLE request_projection_outbox (
    projection_event_id      bigserial PRIMARY KEY,
    request_id               text        NOT NULL,
    tenant_id                text        NOT NULL,
    session_id               text,
    projection_target        text        NOT NULL,   -- 'session_v2' | 'stats' | 'attachments'
    projection_event_version int         NOT NULL DEFAULT 1,
    lifecycle_status         text        NOT NULL,
    success                  boolean,
    completed_at             timestamptz NOT NULL,
    captured_at              timestamptz NOT NULL DEFAULT now(),
    payload_version          int         NOT NULL DEFAULT 1,
    payload_sha256           text,
    processing_status        text        NOT NULL DEFAULT 'pending',
        -- 'pending' | 'processing' | 'processed' | 'dead_letter'（重试不单独建态，
        --  用 next_attempt_at > now() 表达「retryable 待命」，同 stats_event_inbox）
    processing_owner         text,
    lease_until              timestamptz,
    fencing_token            bigint      NOT NULL DEFAULT 0,
    process_attempts         int         NOT NULL DEFAULT 0,
    next_attempt_at          timestamptz NOT NULL DEFAULT now(),
    last_error               text,
    dead_lettered_at         timestamptz,
    dead_letter_reason       text,
    processed_at             timestamptz,
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_projection_outbox_request_target UNIQUE (request_id, projection_target)
);
-- 草案，Phase 6 才可评审执行
CREATE INDEX idx_projection_outbox_claim
    ON request_projection_outbox (next_attempt_at, projection_event_id)
    WHERE processing_status IN ('pending', 'processing');
```

要点：

- **`UNIQUE (request_id, projection_target)` 是 outbox 侧幂等键**。一个请求在每个 target 上至多一条待办事件；迟到终态/重发走 UPSERT 合并而不是插新行（§3.4）。
- `processing_status='processing' AND lease_until < now()` 即可回收（与 inbox claimSQL :409 同构），不需要额外 `locked_at IS NULL` 判别列。

### 3.3 发射点与目标枚举

发射位置：`insertRequestLog` / `updateRequestLog` 的既有事务内、`tx.Commit` 之前（client.go:1466 之前），新增一个 `emitProjectionEvents(ctx, tx, entry)`。发射条件复用两个现成谓词：

- 终态判定复用 `requestLogEntryTerminal`（client.go:1525）——与 sessionv2mirror 只镜像终态的理由一致（hook.go:56-63：in_progress 占位行一旦镜像成 V2 turn 会永久锁死 success=false/status=500，且 V2 turn 首插后不可更新）。
- target 枚举：
  - `stats`：所有终态业务行（EventWriter 今天覆盖的同一集合）；
  - `session_v2`：`entry.GwSessionID` 非空且通过 `isInternalAutoEntry` 过滤（hook.go:530-547）；
  - `attachments`：`len(entry.Attachments) > 0`（迁移期可后置，attachmentmirror 先留在钩子）。

非终态写入不发射事件；终态行的迟到 enrichment（同一 request_id 的再次 UPSERT 且仍为终态）按 §3.4 合并语义处理。

### 3.4 重发/迟到终态合并语义

`request_logs_hot` 的 UPSERT 允许同一 request_id 多次落库（async retry 落同一 id，client.go:934-936）。事件行必须吸收重发而不是堆积。合并规则照抄 `durable_pending_outbox` 的 GREATEST/LEAST 手法（`durable/pending_outbox.go:20-34`）：

```sql
-- 草案，Phase 6 才可评审执行
INSERT INTO request_projection_outbox (
    request_id, tenant_id, session_id, projection_target,
    projection_event_version, lifecycle_status, success, completed_at,
    captured_at, payload_version, payload_sha256
) VALUES ($1, $2, $3, $4, 1, $5, $6, $7, now(), 1, NULLIF($8, ''))
ON CONFLICT (request_id, projection_target) DO UPDATE SET
    lifecycle_status = EXCLUDED.lifecycle_status,
    success          = EXCLUDED.success,
    completed_at     = EXCLUDED.completed_at,
    updated_at       = now(),
    -- 仅当「新终态更晚 且 旧事件已 processed」时重置为 pending 触发重投影；
    -- pending/processing/dead_letter 状态保持不变（dead_letter 只能由 replay 接口复活）。
    processing_status = CASE
        WHEN request_projection_outbox.processing_status = 'processed'
         AND EXCLUDED.completed_at > request_projection_outbox.completed_at
        THEN 'pending' ELSE request_projection_outbox.processing_status
    END,
    process_attempts = CASE
        WHEN request_projection_outbox.processing_status = 'processed'
         AND EXCLUDED.completed_at > request_projection_outbox.completed_at
        THEN 0 ELSE request_projection_outbox.process_attempts
    END,
    next_attempt_at = CASE
        WHEN request_projection_outbox.processing_status = 'processed'
         AND EXCLUDED.completed_at > request_projection_outbox.completed_at
        THEN now() ELSE request_projection_outbox.next_attempt_at
    END;
```

> 该 CASE 语义是设计草案：它定义「迟到更终态触发一次重投影」。V2 turn 首插不可更新（hook.go:57-60），重投影由投影端幂等键吸收为 no-op；stats 端则依靠 `stats_event_dedup` 的 `occurred_at` 前移语义让迟到 success 覆盖早期 failure（event_writer.go:171-178）。是否需要「completed_at 相等但内容不同」的比较维度，见开放问题 O3。

### 3.5 与主事务的原子性论证

outbox INSERT 与 `request_logs_hot`/`request_logs_bodies_hot`/`usage_ledger_hot` 写入在**同一个事务**（client.go:878 `db.Begin` → :1466 `tx.Commit`）：

- 提交前崩溃/回滚：事实行与事件行都不存在，不存在「有事件无事实」的孤儿；
- 提交后崩溃：两行同时可见，dispatcher 恢复后认领，不存在「有事实无事件」；
- 唯一可见性边界是事务本身，由 PostgreSQL 保证，无需两阶段提交或补偿事务。

发射失败的错误分类必须与主事务一致处理：事件列缺列/约束错误属 schema 漂移（会让**业务主写失败**，这是有意的 fail-closed，符合 2026-08 schema 契约漂移事故的教训）；实现期必须为发射增加 `emit_enabled` 开关（§5.3），Phase 6 未铺 schema 前默认关闭。发射本身是单条小 INSERT（无 body），对主事务持锁时长的影响估计 <1ms，但需在 Phase 3 实施时压测确认（风险 R5）。

### 3.6 为什么不直接发 Redis / 进程内队列

1. **崩溃丢失**：进程内 backlog 随进程死亡（sessionv2mirror/backlog.go 明确注释「NOT persisted」）；PG 长故障与 Redis 故障高度相关（同机房、同运维域），故障窗口内 Redis 队列同样不可用或不可信。
2. **契约禁止**：Redis mirror 非权威且不得存完整 body/IR（final plan §1）；事件若要携带投影输入就必须带 body，直接违反契约。
3. **发射点分歧**：钩子只在主路径成功后触发（client.go:843-857）；fallback spool 回放、admin HTTP ingest、WAL replay 都绕过钩子（§2.3）。队列方案需要 N 个发射点各自埋点，漂移不可避免；同事务 INSERT 只有一个发射点（事实事务本身），任何让事实行成功落库的路径自动携带事件。
4. **可观测/可重放**：Redis list 无 DLQ、无 attempts、无审计轨迹；outbox 行本身就是对账与人工 replay 的记录。

## 4. Dispatcher 设计

### 4.1 部署形态与选型边界

dispatcher 是每个网关节点进程内的 goroutine 组（形态对齐 `stats.InboxConsumer.Start` 与 `internal/outbox.Dispatcher.Start`），多实例并发安全由数据库 `FOR UPDATE SKIP LOCKED` 保证。**不引入新中间件**；不与 durable_llm_tasks 恢复 worker 混淆（那是执行恢复，这是投影分发）。

worker 生命周期：`RunOnce`（单轮 claim→project→ack，对齐 inbox_consumer.go:196-219）由 ticker 驱动；`processing_owner` 默认 `projection-outbox-<hostname>-<pid>-<nonce>`（对齐 InboxConfig.Owner 默认，inbox_consumer.go:48-50）。

### 4.2 核心决策 D2：认领用「CTE SELECT ... FOR UPDATE SKIP LOCKED + UPDATE ... RETURNING」批量租约

三个候选：

**方案 A（推荐）：CTE 锁定 + 批量 UPDATE RETURNING**（与 `stats_event_inbox` 生产 claimSQL :402-435 同构）：

```sql
-- 草案，Phase 6 才可评审执行
WITH claimable AS (
    SELECT projection_event_id
    FROM request_projection_outbox
    WHERE processing_status = 'pending'
       OR (processing_status = 'processing' AND lease_until < now())  -- 崩溃 worker 的租约回收
    ORDER BY next_attempt_at, projection_event_id
    FOR UPDATE SKIP LOCKED
    LIMIT $2
)
UPDATE request_projection_outbox AS e
SET processing_status = 'processing',
    processing_owner  = $1,
    lease_until       = now() + $3::interval,
    fencing_token     = e.fencing_token + 1,
    process_attempts  = e.process_attempts + 1,
    updated_at        = now()
FROM claimable
WHERE e.projection_event_id = claimable.projection_event_id
RETURNING e.projection_event_id, e.request_id, e.tenant_id, e.session_id,
          e.projection_target, e.projection_event_version, e.lifecycle_status,
          e.success, e.completed_at, e.process_attempts, e.fencing_token;
```

claim 事务**立即提交**（租约模式），投影工作在事务外进行；lease/fencing 负责正确性。时间基准全部取数据库 `now()`（服务端时钟），从根上消除多节点应用时钟偏移对租约的影响（风险 R2 只剩退避计算的应用侧偏差，见 §4.4）。

**方案 B：裸 `UPDATE ... WHERE processing_status='pending' AND ... RETURNING`**（无 SKIP LOCKED 的 CTE）。否决理由：并发 worker 的 UPDATE 会在同一批热点行上**互相阻塞**（行锁排队），拿不到 SKIP 语义——两个 worker 会串行扫同一前缀，吞吐退化成单 worker + 队头阻塞；且无法保证「跳过他人正在处理的行」。`UPDATE ... RETURNING` 本身不提供 skip 语义，SKIP LOCKED 必须出现在锁定读上。

**方案 C：逐行短事务 `SELECT ... FOR UPDATE SKIP LOCKED LIMIT 1` 后在锁内干活**（`internal/outbox/dispatcher.go:192-292` 的 per-event tx 形态）。否决理由：该形态把 HTTP 投递关进行锁事务是**刻意的**（dispatcher.go:122-137 注释解释了 autocommit 锁释放导致双投递的教训）；我们的投影是本地 DB 写（V2 turn+bodies、stats inbox），行锁持有时长 = 投影时长，V2 需要 advisory lock + previous-body 读（session_writer_v2.go:236-267，5s 预算），长事务持锁放大死锁面，且 worker 崩溃后锁释放即「假装完成」。租约模式（方案 A）把「正在做」与「行锁」解耦，靠 fencing_token 保证只有租约持有者能 ack。

选 A 的额外理由：与 stats_event_inbox 完全同构，运维告警、面板、排障 SOP 可直接复用；Phase 5 stats 收敛后两者是同一套词表。

### 4.3 ack / 失败标记 / DLQ（fencing 全程在环）

投影成功（租约内）：

```sql
-- 草案，Phase 6 才可评审执行
UPDATE request_projection_outbox
SET processing_status = 'processed', processed_at = now(),
    processing_owner = $2, lease_until = NULL, last_error = NULL,
    next_attempt_at = now(), updated_at = now()
WHERE projection_event_id = $1
  AND processing_status = 'processing'
  AND processing_owner = $2 AND fencing_token = $3;
-- RowsAffected != 1 ⇒ 租约丢失：旧 worker 必须丢弃结果、不得重试写（对齐
-- durable.ErrLeaseLost 语义，durable/store.go:38-41 与 inbox markProcessedSQL :378-385）
```

投影失败（可重试错误：超时、连接、advisory lock 冲突、body 读暂时失败）：

```sql
-- 草案，Phase 6 才可评审执行
UPDATE request_projection_outbox
SET processing_status = CASE WHEN $5 >= $6 THEN 'dead_letter' ELSE 'pending' END,
    processing_owner = NULL, lease_until = NULL,
    process_attempts = $5,
    next_attempt_at = now() + make_interval(secs => least(2 ^ ($5 - 1) * 15, 900) + $7),
        -- 指数退避：15s,30s,60s,120s...封顶 15min，$7 为 0~5s 随机抖动防惊群
    last_error = $8,
    dead_lettered_at = CASE WHEN $5 >= $6 THEN now() ELSE NULL END,
    dead_letter_reason = CASE WHEN $5 >= $6 THEN $8 ELSE NULL END,
    updated_at = now()
WHERE projection_event_id = $1
  AND processing_status = 'processing'
  AND processing_owner = $2 AND fencing_token = $3 AND lease_until > now();
-- RowsAffected != 1 ⇒ lease_lost，只计数不写（对齐 markFailedSQL :387-400）
```

参数：`$5 = 本次 attempts`（claim 时已 +1），`$6 = max_attempts`（默认 5，对齐 inbox defaultInboxMaxTries :21 与 outbox Dispatcher 默认）。区别于 stats inbox 的固定 `INTERVAL '1 minute'`（:393）：V2 投影失败常见诱因是 advisory lock 争用，指数退避+抖动更合适；抖动在应用侧生成（仅影响退避精度，不影响正确性）。

**不可重试错误**（entry 解析失败、必填字段缺失、转换契约错误）直接标 `dead_letter`，不消耗重试次数——对齐 final plan §7「核心序列化失败不产生空正文」：body 解析失败**不得**回退为空继续（对比现有 live 路径 `parseProtocolMessages` 返回 nil 的静默行为，hook.go:283-342），必须进 DLQ 人工裁决。

DLQ 出口：

- 指标：`projection_outbox_pending` / `projection_outbox_dead_letter` gauge（对齐 outbox dispatcher 的 SetPendingCount/SetDLQCount，internal/outbox/dispatcher.go:380-396）+ `lease_lost` / `retry` / `dead_letter` 计数器；
- 告警：dead_letter 非零即告警（这类事件每条都对应一条已计费/已响应的请求）；
- 管理：admin 端点 `ReplayDLQ(filter)`，filter 支持 request_id / tenant_id / target / 时间窗（对齐 inbox ReplayDLQ :335-354 与 replaySQL :437-446）。

### 4.4 重试、replay 与幂等

**自动重试**：pending + `next_attempt_at` 到期即可被 claim（§4.2 的 WHERE 覆盖），无状态机迁移。

**人工/自动 replay**：

```sql
-- 草案，Phase 6 才可评审执行
UPDATE request_projection_outbox
SET processing_status = 'pending', processed_at = NULL, processing_owner = NULL,
    lease_until = NULL, process_attempts = 0, next_attempt_at = now(),
    last_error = NULL, dead_lettered_at = NULL, dead_letter_reason = NULL,
    updated_at = now()
WHERE processing_status = 'dead_letter'
  AND ($1::text IS NULL OR request_id = $1)
  AND ($2::text IS NULL OR tenant_id = $2)
  AND ($3::text IS NULL OR projection_target = $3)
  AND ($4::timestamptz IS NULL OR completed_at >= $4)
  AND ($5::timestamptz IS NULL OR completed_at < $5);
```

自动 replay（定时 backfill 扫描器，§6.3）用同一 SQL 的无 filter 变体 + 上限批次。

**投影端幂等键（两级去重）**：

- 第一级（调度层）：outbox 行本身 `UNIQUE (request_id, projection_target)`，claim/ack 有 fencing，重复投递在 outbox 层被吸收。
- 第二级（投影层自然键）：at-least-once 语义下 worker 可能在 ack 前崩溃导致重复投递，投影端必须自幂等：
  - `session_v2` target：`session_turns` 的 `ON CONFLICT (tenant_id, request_id, partition_date) DO NOTHING`（turn_writer.go:275）+ `session_bodies` 的 `ON CONFLICT (tenant_id, session_id, turn_no, partition_date)`（bodies_writer.go:303）。**注意**：任务要求的 `(request_id, projection_target)` 幂等键在 outbox 行层面成立；V2 落库层面真正的自然键是 `(tenant_id, request_id, partition_date)`（含分区日期），两者是「调度幂等」与「存储幂等」的分层关系，设计上必须都成立且互不替代。
  - `stats` target：事件 ID 沿用现有 terminal EventID；`stats_event_inbox` 的 `ON CONFLICT (event_id, occurred_at) DO NOTHING`（event_writer.go:194）+ `usage_facts` 的 `ON CONFLICT (event_id, revision, occurred_at) DO NOTHING`（inbox_consumer.go:516）。

**lease 过期回收**：claim SQL 的 `processing AND lease_until < now()` 分支即回收路径；lease 默认 2 分钟（对齐 defaultInboxLease :20）。过期后被其他 worker 重领时 `fencing_token + 1`，旧 worker 的任何 fenced 写都变成 0 行（lease_lost），旧 worker 收到 RowsAffected!=1 后必须丢弃并退出该事件——fencing 顺序与 `durable` store 的 (lease_owner, fencing_token) 条件更新约定一致（store.go:5-10、store_claim.go:83-89）。

### 4.5 投影执行器（post-persist pipeline）

对每个 claim 到的事件：

```text
1. 按 request_id 回读 request_logs_hot（+ request_logs_bodies_hot 取 body）重建 RequestLogEntry
   （失败 ⇒ 不可重试场景判别：行不存在 = 事实被清理 ⇒ dead_letter+原因「fact_missing」；
     读超时 ⇒ 可重试）
2. 按 projection_target 分派：
   - session_v2：复用 sessionv2mirror.entryToProcessedRequest（hook.go:132-251）+
     SessionWriterV2.Write（session_writer_v2.go:233+，turn+bodies 同事务、advisory lock、
     delta 推导、aggregate 异步 best-effort）。迁移期沿用 sessions_v2.* 开关。
   - stats：复用 stats.EventFromTelemetry + EventWriter.persist 的 inbox 直插路径
     （asyncProjection=true 分支，event_writer.go:182-206），InboxConsumer 不变。
   - attachments：复用 attachmentmirror.PersistHook 的写逻辑（后置阶段）。
3. 成功 ⇒ fenced ack；失败 ⇒ §4.3 失败标记。
```

关键差异点（相对今天的 live 钩子路径，均为改善）：

- **超时预算**：钩子路径 500ms（`sessions_v2.write_timeout_ms`，hook.go:86）；dispatcher 路径放宽到 lease 的一部分（建议 30s 硬顶 + 单事件 ctx），因为已脱离请求关键路径。
- **body 读失败不再按空历史继续**：`GetLatestBodiesInTx` 失败今天只 Warn 后继续（session_writer_v2.go:263-267），Phase 3 要求改为失败进重试（final plan §6 Phase 3 第 5 条）。
- 保留 V2 turn+bodies 同事务、tenant/session advisory lock、aggregate claim 语义不变（final plan §6 Phase 3 第 3 条）。

## 5. onPersisted 瘦身与迁移开关

### 5.1 目标形态

`AddOnRequestLogPersisted` 注册的钩子只剩三类：实时通知（SSE hub、systemmonitor probe lane、routeincident）、缓存失效（boardcache、body-size、recent-success）、以及（过渡期）旧 V2/stats 路径。**任何写数据库的投影职责都退出钩子**：V2 写、stats 事件、attachments 镜像全部由 dispatcher 承担。钩子保持「cheap and non-blocking」的既有契约（client.go:513-516 注释）。

sessionv2mirror 的进程内 backlog（backlog.go）在 dispatcher 阶段 3 后废弃删除：重试职责由 outbox `process_attempts/next_attempt_at` 承担，可观测职责由 outbox gauge 承担。

### 5.2 共存期幂等性论证（双路径并行不产生重复数据）

迁移采用**先并行后摘除**，安全性依赖第二级幂等键：

- V2 双写窗口：钩子路径与 dispatcher 同时写同一 request → `session_turns` 的 `ON CONFLICT (tenant_id, request_id, partition_date) DO NOTHING` 吸收后到者；turn_no 由 advisory lock 下 `MAX+1` 分配（turn_writer.go:174-196），同一 request 只产生一个 turn。同 session 不同 request 的并发争用被 advisory lock 串行化。
- stats 双写窗口：同一 terminal EventID 双投 → `stats_event_dedup` UPSERT（幂等）+ `stats_event_inbox ON CONFLICT (event_id, occurred_at) DO NOTHING` + `usage_facts ON CONFLICT (event_id, revision, occurred_at) DO NOTHING` 三层吸收。
- 因此切换不需要精确的「同一时刻只走一条路」，只需要**最终**摘除旧路径。

### 5.3 开关与阶段（settings_kv 风格，对齐 sessions_v2.* 命名）

| 阶段 | `projection_outbox.emit_enabled` | `projection_outbox.dispatch_enabled` | `projection_outbox.stats_via_dispatcher` | `projection_outbox.session_v2_via_dispatcher` | 旧行为 |
| --- | --- | --- | --- | --- | --- |
| S0 现状 | false | false | false | false | 钩子全量负责 |
| S1 影子发射 | true | true | false | false | 双写并行，drift 对账开启 |
| S2 stats 切换 | true | true | true | false | EventWriter.Record 从钩子摘除 |
| S3 V2 切换 | true | true | true | true | sessionv2mirror V2 写从钩子摘除，backlog 下线 |
| 回滚 | 任意 | 任意 | 翻回 false | 翻回 false | 恢复钩子路径（前提：钩子代码未删，S3 后保留一个发布周期） |

- `emit_enabled=false` 时发射函数 no-op（不写 outbox 行），保证 Phase 6 schema 未铺的窗口零影响；
- 所有开关运行时可热载（settings.GetPlatformBool 语义，同 sessions_v2.shadow_write 的热载行为，backlog.go:73-78）；
- S1 上线前先跑 Phase 0 契约要求的 drift SQL 基线，S1→S3 每阶段对比 `outbox 处理条数 vs 钩子写条数 vs 投影表新增条数` 三个计数，确认偏差为 0 后推进；
- 摘除 `main.go:1983/3794/1966` 的注册是代码变更，放在 S3 稳定一个发布周期之后；dispatcher 关闭（dispatch_enabled=false）但 emit 开启是**合法但堆积**状态，只允许出现在回滚窗口，需配 pending 深度告警。

## 6. fallback / WAL replay 统一收敛

### 6.1 统一原则

「**谁让 request_logs 事实行成功落库，谁就自动携带投影事件**」。事件在事实事务内生成（§3.5），因此所有补写路径只需保证走同一事务函数，投影补偿就成为自动行为，无需各路径自埋 V2/stats 补写逻辑。

### 6.2 各路径收敛方式

| 路径 | 现状 | 收敛后 |
| --- | --- | --- |
| telemetry 主路径 | `persistRequestLog` 成功后触发钩子（client.go:829-859） | 事务内发射事件；钩子只剩通知/缓存 |
| telemetry 降级 fallback（JSONL spool） | 钩子不触发（client.go:596-606, 730-740） | 不变（PG 已不可用，outbox 同样不可写）；恢复后 `ReplayFallback`（:489-499）重入 `insertRequestLog/updateRequestLog`，**事务内自动补发射事件**，V2/stats 由 dispatcher 补齐 |
| request_wal replay | `request_logger.ReplayFallback`（:259-308）只补 `request_wal_hot`，从不补 V2/stats | `request_wal_hot` 是执行状态账本，不是请求事实；V2/stats 的补偿入口统一是「事实行 + outbox」。WAL replay 本身不改，但 final plan §4 的「replay 只补 V1」缺口由下述 backfill 兜住 |
| admin HTTP ingest | `admin/telemetry.go:341` 直写 `request_logs_hot`，无钩子 | 事务内补发射事件（复用同一 emit 函数），该第二写入口不再产生投影盲区 |
| sessionv2mirror backlog | 进程内 10000 条 FIFO，重启丢失（backlog.go） | 删除；由 outbox 重试 + DLQ + backfill 替代 |
| EventWriter 内存 deadLettered | 批量失败 5 次后内存计数并丢批（event_writer.go:125-132） | stats 经 dispatcher 入 inbox，EventWriter 的内存队列/deadLettered 退役（Phase 5 收敛完成态） |

### 6.3 幂等补写 backfill（消除历史漂移 + 持续对账）

dispatcher 提供一个低频扫描任务（建议 5-15 分钟一轮，带速率上限），用 drift SQL 找「事实存在但投影缺失」的 request_id，合成事件行：

```sql
-- 草案，Phase 6 才可评审执行（backfill 合成事件，UPSERT 语义与 §3.4 相同）
INSERT INTO request_projection_outbox (request_id, tenant_id, session_id, projection_target, ...)
SELECT r.request_id, r.tenant_id, r.gw_session_id, 'session_v2', ...
FROM request_logs_hot r
LEFT JOIN session_turns_hot t
  ON t.tenant_id = r.tenant_id AND t.request_id = r.request_id
WHERE r.success IS NOT NULL            -- 终态
  AND r.gw_session_id IS NOT NULL AND r.gw_session_id <> ''
  AND r.ts > now() - interval '2 days' -- 补写窗口
  AND t.request_id IS NULL
ON CONFLICT (request_id, projection_target) DO UPDATE
SET processing_status = 'pending', process_attempts = 0,
    next_attempt_at = now(), updated_at = now()
WHERE request_projection_outbox.processing_status <> 'processing';
```

（stats target 同理对 `stats_event_inbox`/`usage_facts` 反查。）该扫描器同时是 §5.3 各阶段的对账器：漂移从「事后人工 SQL」变成「自动合成事件 → 走同一条幂等投影管线」，这就是 fallback/WAL/live 三路径漂移的最终收敛点。补写条件里显式排除 `processing` 态避免与在途 worker 打架；`ON CONFLICT DO UPDATE ... WHERE` 保证 DLQ 行不被 backfill 无限复活（DLQ 只能人工/显式 replay）。

## 7. 不建表声明与 Phase 6 交接清单

再次声明：本文档不执行任何 DDL，不创建 `request_projection_outbox`，不修改 installer/migrations/sql 目录。Phase 6 需要交接的 schema 事项（全部走 `docs/standards/database-change-and-real-verification.md`）：

1. `request_projection_outbox` 建表 + 部分索引 + 唯一约束（§3.2 草案）；
2. 热表/父子分区策略决策：outbox 是短生命周期工作队列（processed 行很快可清理），建议**不分区、heap 存储**，与 request_logs 系列分区体系解耦——该决策在 Phase 6 preflight 中确认；
3. 清理策略（R1）：processed 行保留窗口 + 删除作业（复用 partition_manager 的调度骨架，或独立轻量 job），DLQ 行保留策略；
4. `emit_enabled` 默认值与发布顺序（schema 先行、代码后发、开关最后打开）；
5. 与迁移 603/604 的注册顺序关系（两者仍处 pending preflight，本设计不依赖它们）。

## 8. 风险与开放问题

### 风险

- **R1 outbox 表增长与清理**：每请求 1-2 行（stats + 可选 session_v2），高流量下日增百万级。processed 行必须有时限清理（建议保留 24-48h 供对账），否则 claim 部分索引膨胀、autovacuum 压力上升。DLQ 行保留策略需与告警 SLO 绑定。清理作业本身要幂等且限速（Phase 6 决策）。
- **R2 lease 时钟偏移**：claim/ack/失败标记全部使用数据库 `now()`（服务端单时钟），租约判定无跨节点偏移；残余偏移只在两处：(a) 退避抖动在应用侧生成（仅影响精度）；(b) 应用读到的 `lease_until/now()` 与下一语句的服务端 now() 之间有毫秒级漂移——fencing_token 使其无害。多 PG 副本（now() 走不同节点）场景需确认读写同一 primary（当前架构主写均在 primary，成立）。
- **R3 V2 幂等键冲突语义**：outbox 幂等键 `(request_id, projection_target)` 与 V2 存储自然键 `(tenant_id, request_id, partition_date)` 不重合。边界：同 request_id 跨日重放（partition_date 漂移）会生成第二个 turn 行；`tenant_id` 经 `nonEmpty(entry.TenantID, "default")` 兜底后与 V2 写入侧兜底不一致时会分裂。需在 Phase 3 实施时把两侧兜底规则收敛为同一函数（引用 final plan §4 对 EmitRequestLogUpdate 兜底漂移的同类批评）。
- **R4 双写窗口的 turn 语义**：S1 阶段钩子与 dispatcher 并发写同 session 时，advisory lock 串行化 turn_no 分配，但提交顺序不确定——turn_no 与 ts 可能倒挂（今天异步钩子路径同样存在）。需确认 V2 读路径是否依赖 turn_no 严格时序；若是，dispatcher 侧需按 completed_at 排序 claim（同 session 串行化），代价是吞吐。
- **R5 主事务放大**：主事务新增 1-2 行小 INSERT（无 body），估计 <1ms，但 request_logs_hot 写路径已极重（102 列 + bodies + usage_ledger + api_keys + outbox 事件）。Phase 3 上线前必须压测 p99 事务时长与连接池占用，必要时把 stats/session_v2 双行合并为单行多 target（JSONB 数组）再评估。

### 开放问题

- **O1 outbox payload 引用形态**：事件存纯 `request_id` 引用（本设计，D1）vs 存本地归档文件引用（Phase 2 LocalRequestArchive 就绪后）。若事实行在 7 天热窗口后被 promotion 且事件仍未处理（极端积压），回读要走分区父表 + body_resolver（admin/body_resolver.go），读放大需评估；兜底方案是 backfill 窗口限制在热窗口内（§6.3 已带 2 days 窗口）。
- **O2 stats 切换的 EventWriter 命运**：S2 后 EventWriter 的同步兜底/内存队列是否整体退役，还是保留为 dispatcher 故障时的应急直写通道？涉及 Phase 5 stats 收敛的边界，建议留给 Phase 5 设计文档。
- **O3 重发比较维度**：§3.4 用 `completed_at` 判「更终态」。同一 completed_at 的内容修正（如 CorrectEstimatedUsage 路径，client.go:1572）不会触发重投影——是否需要 `payload_sha256`（Phase 1 fact 就绪后）作为第二比较维度？
- **O4 attachments target 时机**：attachmentmirror 迁到 dispatcher 是 S3 之后独立小阶段，其幂等键（对象存储去重 + 表唯一约束）需单独梳理。
- **O5 body 摘要化差异**：live 钩子用内存全文，dispatcher 回读用 DB 存储（可能被 `requestBodiesSummaryEnabled` 摘要化，client.go:2167）。切换后 V2 内容可能比 live 路径「短」。需要决定：dispatcher 读取时是否绕过摘要（bodies 表若已只存摘要则无法绕过），或接受差异并在发布说明中声明。
- **O6 turn_logs 与 sessions aggregate 的 durable 化**：本设计只覆盖 turn+bodies 同事务主投影；turn logs（session_writer_v2.go:398-429 best-effort）与 sessions aggregate（:431+ 有限重试）失败仍不进 outbox。final plan §4 将其列为独立 P1，是否为它们增设子 target（`session_v2_logs`/`session_v2_aggregate`）留待实施时决策。

## 9. 附录：本文引用的关键代码位置

| 主题 | 位置 |
| --- | --- |
| 四版本常量（projection_event_version=1） | `internal/requestfact/types.go:9-19`；`RequestArchiveEnvelope.ProjectionEventVersion` :156 |
| 主事实事务（usage_ledger + request_logs_hot + bodies + commit） | `domains/hooks/observability/telemetry/client.go` `insertRequestLog` :861、:891、:938、:1070、`upsertRequestLogBodies` :2158（INSERT :2180）、commit :1466 |
| onPersisted 注册与触发 | client.go :503-527（Set/Add）、`persistRequestLog` :829-859（仅主路径成功触发） |
| 终态谓词 / 同事务 outbox 先例 | `requestLogEntryTerminal` client.go:1525；`insertSessionOpenedEvent` :1471 |
| fallback spool 写 / 回放 | client.go :596-606、:730-740；`ReplayFallback` :489-499 |
| request_wal 写入与回放 | `domains/hooks/observability/telemetry/request_logger.go` `ReplayFallback` :259-308、`upsertInitial` :220、`persistUpdate` :600、终态守卫 :644-695 |
| 第二写入口（无钩子） | `admin/telemetry.go:341`（request_logs_hot 直插）、:436（bodies） |
| V2 mirror 钩子与失败路径 | `internal/sessionv2mirror/hook.go` `PersistHook` :46-127、终态过滤 :52-63、失败+backlog :104-125、超时 :86-87、`entryToProcessedRequest` :132-251 |
| 进程内 backlog（将被替代） | `internal/sessionv2mirror/backlog.go` cap :25、`appendBacklog` :100-114、`DrainBacklog` :128-146 |
| V2 写入与幂等键 | `domains/session/v2/session_writer_v2.go` `Write` :233+（lock 预算 :236-238、previous-body 读 :263-267）、`turn_writer.go` advisory lock :38-39 / 流程 :174-196 / ON CONFLICT :275、`bodies_writer.go` ON CONFLICT :303 |
| stats 事件生产（进程内队列缺陷） | `domains/stats/event_writer.go` `Record` :73-101、run/deadLettered :103-158（:125-132 丢批）、persist/inbox 直插 :161-221 |
| claim/lease/fencing/DLQ/replay 生产先例 | `domains/stats/inbox_consumer.go` 常量 :17-22、claim :221-248、`claimSQL` :402-435、`markFailedSQL` :387-400、`markProcessedSQL` :378-385、`projectLeaseSQL` :370-376、`ReplayDLQ` :335-354、`replaySQL` :437-446、`insertUsageFactTx` :505-528 |
| durable lease/fencing 语义 | `durable/store.go` 语义注释 :1-13、哨兵 :38-46；`durable/store_claim.go` SKIP LOCKED claim :78-95 |
| durable 同事务 outbox 合并先例 | `durable/pending_outbox.go` `enqueuePendingOutbox` :18-35（ON CONFLICT GREATEST/LEAST）、`ProjectPendingOutbox` :37+ |
| 通用 outbox dispatcher 先例（per-event tx 教训） | `internal/outbox/dispatcher.go` 注释 :122-137、claim :203-212、退避 :317-334、DLQ :336-346、gauge :380-396 |
| 钩子注册矩阵 | `cmd/gateway/main.go` :1931、:1966、:1983、:2507、:2630、:2755、:3766、:3775、:3794 |
