# Session V2 后续闭环设计（2026-08-29）

## 状态与安全边界

本文件是设计冻结草案，不是 apply 脚本。当前结论保持 **No-Go**：在 staging 只读盘点、迁移演练、回滚演练和至少 100 个 settled 会话的 V1/V2 parity 完成前，不执行 staging/生产数据 `UPDATE`、`DELETE`、`DROP`、detach，也不切换 V2 读路径。

现状依据：`sql/migrations/startup/430_sessions_v2_schema.sql`、`526_session_turns_hot.sql`、`domains/session/v2/{session_writer_v2.go,turn_writer.go,bodies_writer.go}`、`bg/partition_manager.go` 和 `docs/audit/2026-08-29-24h-correction-audit.md`。

## 1. `session_bodies_hot` 迁移设计

### 1.1 目标契约

`public.session_bodies_hot` 是独立 heap 表，只承接最近 8 小时的 V2 正文；`public.session_bodies` 保持 RANGE 月分区，冷分区在迁移后才允许评估 columnar。hot 与 parent 的业务列合同必须完全一致，避免 602 事故中的 `SELECT *`/位置漂移。

字段：

- `id BIGINT NOT NULL DEFAULT nextval('public.session_bodies_id_seq')`
- `session_id TEXT NOT NULL`, `turn_no INTEGER NOT NULL`, `tenant_id VARCHAR(255) NOT NULL`, `request_id TEXT NOT NULL`
- `request_delta JSONB`, `response_delta JSONB`, `outbound_body JSONB`
- `request_attachments JSONB NOT NULL DEFAULT '[]'::jsonb`, `response_attachments JSONB NOT NULL DEFAULT '[]'::jsonb`
- `ts TIMESTAMPTZ NOT NULL DEFAULT now()`, `partition_date DATE NOT NULL DEFAULT current_date`
- `PRIMARY KEY (id, partition_date)`
- `UNIQUE (tenant_id, session_id, turn_no, partition_date)`
- `UNIQUE (tenant_id, request_id, partition_date)`

不在本迁移中加入晚到元数据列。晚到信息通过第 2 节 append-only side table 承接，从而不把可变字段带入冷正文分区。

索引最小集合：`(ts,id,partition_date)`、`(tenant_id,session_id,turn_no DESC)`、`(tenant_id,request_id)`；附件存在时再按 JSONB 查询需求添加受控索引，不默认建立高基数 GIN。

### 1.2 RLS 与 view

hot 表启用 RLS，沿用 526 的三层策略：

1. tenant isolation：`tenant_id = current_setting('app.current_tenant', true)`，同时用于 `WITH CHECK`；
2. `super_admin`/`app.bypass_rls=true` 只用于受控运维连接；
3. restrictive owner filter：从 `request_logs_hot UNION ALL request_logs` 按 `(gw_session_id, tenant_id, ts ASC)` 取首条 `owner_user`，要求 session 与 tenant 同时匹配。

创建 `public.session_bodies_with_current_month`，`WITH (security_invoker=true)`，显式列 `UNION ALL`：

- hot 行只有在 parent 不存在同一 `(tenant_id,request_id)` 时返回；
- parent 行来自 `public.session_bodies` 全部已附着月分区；
- view 不允许 `SELECT *`，不隐藏 JSON decode/缺失 body。

admin/reader/validator 统一改读此 view，并以 `(tenant_id,session_id,turn_no)` JOIN `session_turns_with_current_month`；禁止只用 session/turn 连接。

### 1.3 原子 promote

新增 `promote_session_bodies_hot_to_partition(interval, integer)`，默认 retention 为 **8 hours**、batch 默认 500（上限 100000）。语义与 526/602 一致：

1. 参数校验；取得全局 promote advisory lock；
2. 以 `ts < statement_timestamp() - p_retention`、`ORDER BY ts,id,partition_date`、`FOR UPDATE SKIP LOCKED` 建批；
3. 按 tenant/session、tenant/request 的稳定顺序取得 `session_turns_advisory_lock_key` 派生锁，和正文 writer 共享锁域；
4. 对每个 `partition_date` 调用 `ensure_sessions_v2_partitions(date)`，并验证目标月分区存在、已 attach、仍为 heap；
5. 单数据修改语句：`WITH moved AS (DELETE ... RETURNING explicit columns) INSERT INTO public.session_bodies(explicit columns) SELECT ... FROM moved`；任何 insert 错误都让整个事务回滚，hot 行不能丢；
6. 不使用 `ON CONFLICT`、不吞异常、不使用 `SELECT *`。目标 parent 已有同 tenant/request 时，先记录冲突指标并跳过/保留 hot，不能静默删除可能不同版本的正文。

`bg/partition_manager.go`：将 ensure 与 promote 分别登记；正文使用独立 `session_bodies_hot` retention/batch setting，默认仍为 8h/500。promote 成功后只 `ANALYZE` 目标月分区，不在请求路径执行。

### 1.4 上线顺序、兼容与回滚窗口

**Phase 0（只读）**：检查 parent/hot 列合同、sequence、RLS、月分区边界、当前 parent 正文计数和 tenant/null/ts drift；不写数据。

**Phase 1（additive schema）**：只创建 hot、view、函数和索引；保留旧 parent writer/read。旧版本服务忽略新增对象，故可先部署 schema。

**Phase 2（双版本 writer）**：新版本以 feature flag `session_bodies_hot_write` 写 hot；在同一 caller-managed transaction 中完成 turn + body。为避免 mirror 行让 promote 永远跳过，不对新写请求继续复制到 parent；parent 只作为历史 fallback。旧版本仍只写 parent，因此 view 可同时读两者。

**Phase 3（staging canary）**：先开启一个 tenant/application，验证读一致、body presence、冲突数、promote 原子失败回滚和 8h retention；连续观察至少 24h。

**回滚窗口**：Phase 2/3 保留至少 7 天。回滚只切回旧 read/write flag，不删除 hot。若必须清空 hot，先暂停/切换新 writer 到 parent，再在低峰以受控 drain 将 hot 原子 promote 到 parent，确认 hot=0 后才允许旧版本完全接管；禁止直接 `DELETE hot`。迁移 down 只撤销 view/函数/索引/空 hot 表，且前置检查 hot 行数为 0、没有依赖和未完成 drain。任何不满足条件的 down 都是 NO-GO。

**兼容矩阵**：

| DB schema | 旧服务 | 新服务 | 读路径 |
|---|---|---|---|
| 无 hot | 读写 parent | 禁止开启 flag | parent |
| 有 hot + view | parent | flag off | parent/view 可回退 |
| 有 hot + view | parent | flag on | view（hot 优先） |
| hot promote 完成 | parent | flag on | view（冷 parent + hot） |

## 2. 已 promote turn metadata 的 enrichment / aggregate claim

禁止对已 promote 的 columnar `session_turns` 做晚到可变 `UPDATE`。新增 append-only `session_turn_enrichment_events`（tenant/session/turn/request、`event_id UUID`、`event_kind` 白名单、`patch JSONB`、`source`、`observed_at`、`schema_version`、`created_at`），唯一键为 `event_id`，并按 `(tenant_id,request_id,observed_at)` 建受控索引。patch 必须有大小上限、允许字段白名单和单调 `source_seq`。

新增 `session_turn_aggregate_claims` 作为幂等 claim ledger：唯一键 `(tenant_id,request_id,aggregate_kind,source_event_id)`，worker 通过 `INSERT ... ON CONFLICT DO NOTHING RETURNING` 抢 claim；同一 tenant/session 仍先拿 526 advisory lock。claim 成功后：

- hot turn 可在事务内做白名单字段 enrichment；
- 已 promote turn 只写 enrichment event，不更新冷表；
- reader view 用 lateral `latest patch` 投影可读字段，或由异步 materializer 写 heap aggregate projection；
- session snapshot 只通过 append-only aggregate event + 幂等 upsert 更新，失败可重试；
- 不以 `aggregate_applied_at` 作为冷表可变状态。现有 parent-first claim/update 需要在迁移后保留兼容读，但新 writer 不再对冷 turn 直接更新。

每个事件带 `base_revision`/`schema_version`，patch 不适用时进入 dead-letter 指标而不覆盖原值。保留原始 event 作为审计证据，禁止物理删除。

## 3. `provider_error_details` 有界异步聚合

权威明细仍是 `candidate_failure_logs_hot → current-month view → credential detail API`。`provider_error_details` 只做趋势聚合，不取代明细。

新增有界 `provider_error_detail_events_hot`（heap，8h retention）：`event_id`、tenant/provider/model/endpoint、`error_type`、`error_code`、脱敏 message preview（上限 2KB）、`context`（上限 16KB）、`request_id`、`failure_stage`、`observed_at`、`created_at`；唯一 `event_id`。入口使用固定容量 channel + non-blocking enqueue，满时丢弃并增加 `provider_error_detail_events_dropped_total`，不能阻塞主请求。

聚合 worker 每批最多 N=500、每次运行 deadline 小于 lease，按稳定 error fingerprint（tenant/provider/model/endpoint/type/code/脱敏 message hash）取 claim；用 advisory lock + `INSERT ... ON CONFLICT`/受控 upsert 写 `provider_error_details`，单个 poison event 不阻塞整批，失败计数递增并留在 hot。聚合字段只允许 occurrences/first_seen/last_seen/preview/context_digest 等白名单；原文永不落聚合表。worker backlog、oldest age、success/failure/drop 都暴露 metrics。

## 4. preflight failure 与 `failure_stage`

在 circuit-open、limiter/concurrency、key-rotation exhausted、no-candidate、deadline 等统一 preflight 返回点建立闭合枚举：`preflight.circuit_open`、`preflight.limiter`、`preflight.concurrency`、`preflight.key_rotation`、`preflight.no_candidate`、`preflight.deadline`。每个 failure outcome 在进入候选执行前填充 `failure_stage='preflight'`、`failure_detail_code` 为枚举值，并写入同一 request capture；若 candidate failure logger 可用则同步写 hot，否则进入有界事件队列。API 只返回稳定 code/preview，不返回上游原文或 credential secret。

## 5. SurvivalCoordinator `: thinking:` retry notice

在 `SurvivalCoordinator` 增加 nil-safe `RetryNotice func(ctx context.Context, attempt int, reason string, retryAt time.Time)` seam，并由 streaming wiring 桥接到已有安全 `DispatchNotice`/`: thinking:` comment channel。只接受固定脱敏 reason vocabulary；attempt、retry-after 为数值字段，不携带 provider response、request body 或 credential 信息。

在 retry decision 已确定、gate 已 discard、等待开始前发送一次 notice；同一 attempt/reason 只发送一次。notice 发送失败不改变 retry 决策，但记录 metric/log；连接已 semantic commit 或 resume-blocked 时不发送透明 retry notice。keepalive 仍由现有 transport heartbeat owner 负责，避免把 thinking comment 与语义对话混淆。补充 RetryNow/WaitRecovery、deadline、client cancel、notice failure 的单测。

## 6. staging 只读盘点与 100 会话 parity 门禁

只读盘点必须先通过 `env-injector inject aliyun-gateway-154`，命令使用 SSOT 导出的 SSH/PG 环境，不复制历史文档中的 DSN。查询清单：

1. migration/schema version、parent/hot/view/function 是否存在；
2. parent/hot 列、类型、not-null、唯一键、RLS policy、partition bounds/access method；
3. `session_bodies` 中 `tenant_id IS NULL`、metadata/body tenant mismatch、`ts` drift、目标月份缺分区；
4. 最近 8h/7d 的 V1 session 数、body presence 覆盖率、V2 turn/body 数和 hot/cold 分布；
5. 只读 `EXPLAIN (FORMAT JSON)` 验证按 tenant/session/request 的索引路径，不执行 `CREATE INDEX`；
6. provider error/detail/preflight failure 的计数和最新时间（只读）。

Parity 只对 settled sessions：V1 `request_logs`/`request_logs_bodies` 有完整可读数据，且 `MAX(ts) < now()-settle_window`。批量工具使用 `-max-sessions 100`（建议取 120，剔除缺 body/未 settled 后必须仍有 ≥100），`-settle-window 30m`，`-format json`，输出报告保存到脱敏证据目录。validator 必须读取 `*_with_current_month` view；任何 skipped/loader error 都不算通过。

通过条件：至少 100 个 session 全部 loader 成功；request-id parity 100%；tenant/session/turn join 无跨租户行；body reconstruction 通过（压缩模式仅允许既定 warning）；snapshot totals 与 turns 一致；p99 单会话校验延迟在 staging 基线的 1.2 倍以内；V2 read fallback 与前端抽样无 404/空正文。否则保持 No-Go。

## 7. 交付顺序与证据

1. 先合入 additive migration + migration tests（本地 testcontainers/SQL fixture）；
2. 修 validator view/JOIN 和批量分页，再 staging 只读验证；
3. staging apply 需另开受审会话，先备份、演练 promote/down、保留 gate.json；本会话不 apply；
4. provider aggregation、preflight stage、retry notice 分别独立 commit、单测和 staging smoke；
5. 100-session parity、24h canary、rollback rehearsal 全部通过后，才由人工 gate 决定读切换。

任何 migration drift、hot 行无法 drain、parent access method 仍不明确、parity 样本不足或出现跨租户结果，都自动判定 No-Go。
