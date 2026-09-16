# 设计：网关任务托管（Hosted Task Delegation）v2

> 状态：**v2 已核实，P0 已实施**（2026-09-15，本仓 §6.1 清单落地：迁移 711 三表、
> domains/hostedtask 门面/投影/回调、bg reconciler、mux 装配（默认关，
> `LLM_GATEWAY_HOSTED_TASKS_ENABLED=true` 开启）、installer 三处同步；
> §6.0 跨仓库门禁（companion 部署/JWT/245 端到端）仍为部署前置）
> v1：2026-09-14 设计提案（ace419dcc）
> v2：2026-09-14 依据跨仓库源码核实修订，修正 v1 中 6 处与实现不符的断言，明确"网关转交 ACC、进展经网关通知前端、上下文环境经 Memora 交换"的执行契约与 P0 逐文件清单。
> 本文档所有 file:line 引用均经源码核实（2026-09-14 工作树）；标注【跨仓库】的条目属其他仓库职责，本仓仅定义契约。

---

## 0. 修订记录（v1 → v2 核实修正）

| # | v1 断言 | 核实结论 | 证据 |
|---|--------|---------|------|
| F1 | 复用 `internal/outbox` 向前端 callback URL 发签名 webhook | **不成立**。outbox 是网关→ASM 单一固定端点（`ASM_INTERNAL_ENDPOINT`），表无 destination 列，deliverer 只持一个 endpoint | `cmd/gateway/main.go:546-576`、`internal/outbox/delivery.go:65-104`、`deploy/sql/migrations/V357__create_outbox_events_table.sql:18-51` |
| F2 | 挂载 `/v1/goal-runs/:id` 即可 | handler 已实现但**从未挂载**（`_ = goalRunHandler`），且直接信任 `X-Tenant-ID`、store 为 nil 会 panic，**不能只加一行 mux** | `cmd/gateway/main.go:744-757`、`internal/handlers/goalrun_handler.go:75-99` |
| F3 | pi 经 ACP/RPC 驱动 | **实为 CLI headless**：`pi --mode json "<prompt>"`，prompt 走位置参数，模型经 `PI_MODEL` env，resume 用 `--session <id>` | 【跨仓库】`agent-companion/internal/agentfacade/known.go:72-90,255-259`、`cli_driver.go:227-275` |
| F4 | 取消经 ACC 可靠生效 | **仅 delivered**。companion `FacadeExecutor` 未实现 `CommandCanceler`，ACC 回执链停在 delivered；且 pi `exit 0 + stopReason=error` 会被 facade 标记成功 | 【跨仓库】`agent-companion/internal/acc/command_loop.go:19-30,530-660`、`internal/agentfacade/cli_driver.go:175-217`、`parsers.go:335-352` |
| F5 | Memora compress 可直接用于阶段压缩 | compress 走 V1Auth 且 **body/header tenant 可覆盖认证 tenant**，不得暴露给不可信调用方；只能由网关受控调用（tenant 写死） | 【跨仓库】`memora/internal/sessionmemory/handler.go:250-273`、`internal/middleware/auth.go:111-178` |
| F6 | 复用 durable 队列/pending 承载托管任务 | durable 是 LLM 请求生存队列（加密快照绑定 HTTP 请求语义）；pending 是 Redis 短期缓存（TTL 1h、body 1MiB）。**只复用模式（lease/fencing/CAS），不复用表与 runner** | `durable/task.go:1-10`、`pending/pending.go:35-47`、`sql/migrations/startup/516_durable_llm_tasks.sql:7-17` |

另两条部署纪律性发现：

- GoalRun 迁移存在 **549/554 双 schema 漂移**（549=UUID+`goal_run_id`、554=TEXT+`root_request_id`；现行代码/installer 对齐 554/555）。新建迁移不得引用 549 血统。
  `sql/migrations/startup/549_goal_run_ledger.sql:5-50` vs `554_goal_runs.sql:28-98`、`installer/internal/dbinit/runner.go:48-49`
- 迁移须**三处同步**：`sql/migrations/startup/` + `installer/cmd/llm-gw-installer/embeddata/startup/` + `installer/internal/dbinit/runner.go` StartupFiles 清单，并记入 `docs/db-changelog.md`。反例：R28 的 704 已入 migrations 与 changelog，但 embeddata/runner **尚未同步**。
- 自动 handoff 触发器**已复活**（v1 说停用已过期）：`domains/hooks/handoff/trigger_hook.go:1-36` supersedes 停用版，默认 `LLM_GATEWAY_HANDOFF_ENABLED=false`。
- 会话事实边界：`session_summaries` 是分析聚合表；V2 `public.sessions`（迁移 430）仍是 shadow；request_logs 才是正文/请求核心。托管会话导出应读 request_logs + session V2 视图，勿再宣称"session_summaries 是主表"。
  `sql/migrations/startup/310_session_summaries.sql:7-75`、`430_sessions_v2_schema.sql:34-98`、`docs/03-design/01-architecture/architecture/ARCHITECTURE.md:171-193`

---

## 1. 架构总则

### 1.1 单写者原则（源自 RedClaw《27-ACC-Memory-Gateway三系统整合方案》）

每种状态只允许一个系统拥有写权限，其余系统只保存引用、投影或诊断：

| 领域 | 唯一权威 | 其他系统 |
|------|---------|---------|
| 托管任务接入/投影/通知/结果拉取 | **网关** | 前端只对接网关 |
| 任务账本、dispatch、handoff/transfer、执行进度事件 | **ACC** | 网关投影、companion 上报 |
| agent 进程、原生会话（resume/fork） | **agent-companion + pi 原生会话文件** | 只读观测 |
| 会话正文、成本、计费（线层） | **网关**（request_logs + usage_ledger） | — |
| 上下文/环境/知识（知层）、handoff 快照 | **Memora** | 网关/companion 经 API 存取 |

RedClaw 蜂群契约佐证：companion 本地 API 只读、写路径必须经 ACC（【跨仓库】`RedClaw/docs/swarm/SWARM_BOUNDARY_CONTRACT.md`、`agent-companion/docs/swarm/SWARM_SYSTEM_PLAN.md §1`）。

### 1.2 角色与链路

```
前端智能体(用户机器)            网关宿主机(245/154)                       平台服务
┌────────────────┐  ①委托     ┌────────────────────────────┐  ②转交    ┌─────────────┐
│ zcode/claude/… │ ─────────▶ │ llm-gateway-go              │ ────────▶ │ ACC acc-go   │
│ 只对接网关      │ ◀───────── │  · /v1/hosted-tasks 门面     │  runtime  │  :4101       │
│                │  ⑤通知/拉取 │  · hosted_tasks 投影+台账    │  dispatch │ 任务账本/租约 │
└────────────────┘            │  · reconciler 订阅 ACC 事件  │ ◀─SSE/轮询─│ fencing/SSE  │
                              │  · 签名回调(SSRF防护)        │           └──────┬──────┘
                              │  · 线层:会话正文/成本/计费    │                  │③派发
                              └────────────────────────────┘                  ▼
                                     ▲ LLM(loopback,计费)            ┌──────────────────┐
                                     │                               │ agent-companion   │
                              ┌──────┴────────┐      ④spawn/驱动      │ (网关宿主机,systemd)│
                              │ pi (--mode json)│◀────────────────────│ 租约/围栏/去重/恢复 │
                              │  tool loop     │                      └────────┬─────────┘
                              │  compaction    │                                │⑥上下文交换
                              └───────┬────────┘                                ▼
                                      │产物/摘要                        ┌──────────────┐
                                      └───────────────────────────────▶│ Memora        │
                                        (typed ingest/scope_chain)     │ 上下文/环境/快照│
                                                                       └──────────────┘
```

### 1.3 三条不变量

1. **前端永不直连 ACC/companion/Memora**：委托、查询、拉取、召回、通知全部经网关。
2. **网关不建第二套执行 owner**：`hosted_tasks` 只做关联投影（hosted_id ↔ acc_command_id/gw_session/tenant），lease/fencing/重试由 ACC Runtime Control 与 companion 持有；网关不重试 dispatch 语义（重试=同 Idempotency-Key 重放）。
3. **执行真相在 ACC，存储真相在 Memora，通知真相在网关 outbox**：跨服务一律以 Idempotency-Key/Correlation-ID 对账，不凭网络异常推断状态。

---

## 2. 各模块已核实能力与边界

### 2.1 ACC（编排权威）【跨仓库：agent-control-center】

| 能力 | 契约 | 关键证据 |
|------|------|---------|
| Runtime 注册/心跳 | `POST /api/v2/runtime/register`（runtime_id+instance_id 必填，返回 run_id；幂等 replay=200）；`POST /api/v2/runtime/heartbeat`（仅 runtime_id/instance_id/api_endpoint 三字段生效） | `acc-go/internal/httpapi/handlers/runtime_control_native.go:140-185`、`internal/runtimecontrol/service.go:195-224` |
| **执行派发（P0 主链）** | `POST /api/v2/runtime/dispatch` + `Idempotency-Key`（header/body 一致性校验）+ `{runtime_id, task_id, operation, payload{kind:pi, prompt, cwd, timeout_ms, session_id}, source_ref, correlation_id}` → 202 `{command_id}`；按 runtime_id+task_id 自动 provision command task | `runtime_control_native.go:113-123,330-367`、`internal/runtimecontrol/service.go:560-615` |
| 进展事件 | `GET /api/v2/orchestration/runs/:run_id/events/stream`（`?after=` / `Last-Event-ID` 恢复；durable 回放+live）；轮询兜底 `GET /api/v2/runtime/commands/:command_id` | `internal/httpapi/handlers/orchestration_events_stream.go:45-74`、`runtime_control_native.go:369-379` |
| 取消 | `POST /api/v2/runtime/commands/:command_id/cancel`（2xx=requested）；回执链 requested→delivered→effective 单调推进 | `runtime_control_native.go:453`、migration 214 |
| 任务账本（可选） | canonical `/api/v2/canonical/tasks`（Idempotency-Key 强制、12 态、乐观并发 resource_version）；注意 **coordinator 的 dispatch/handoff/decompose 是 Node 转发，不是 Go native**，P0 不依赖 | `internal/httpapi/handlers/acc.go:510-714`、`coordinator_native.go:1-25` |
| 召回/移交（P1） | v3 transfer 16 态：requested→source_fenced→snapshot_captured→…→committed；要求 snapshot_ref+`sha256:` hash+manifest_version+expected_task_revision+Idempotency-Key | `internal/orchestration/transfer_model.go:40,173-215`、`internal/httpapi/handlers/transfer_native.go:50-71` |
| 鉴权 | Runtime Control 全部挂 RequireAuth：Bearer 须含 sub+**tenant_id** claim（`sk-svc.*` 不通用） | `internal/auth/auth.go:266-290`、`orchestration_native.go:369-381` |

### 2.2 agent-companion（执行面）【跨仓库】

- pi 驱动：`pi --mode json`（known.go:72-90），resume `--session`（known.go:255-259），`PI_MODEL` env 注入（cli_driver.go:250-275），进程组 SIGTERM→5s→SIGKILL（cli_process_unix.go:11-39）。
- 命令消费：ACC run SSE（`internal/acc/sse.go:48-167`）→ lease acquire/start/execute/complete（`command_loop.go:350-529`）；SettledCursor+durable inbox 重启恢复（`recovery.go:279-360`）。
- 网关联动：每次 dispatch 注入 `AGENT_GATEWAY_SESSION=gw_<dispatch_id>`（accbridge/executor.go:116-125）+ 网关 URL/key/correlation env（gateway/gateway.go:108-136）。
- 记忆：MemoraSearch 前置注入（executor.go:153-193，**tenant 硬编码 default**，跨租户任务需 P1 修复）；tool 产物/终局会话 best-effort ingest（executor.go:238-309,394-439）。
- **边界（写入 P0 契约）**：①取消仅到 delivered（§0-F4）；②`CheckPath/CheckTool` 未接入执行链，`payload.cwd` 可覆盖默认值 → **cwd 必须由网关白名单映射，禁止透传任意路径**；③facade 运行表纯内存，重启后 run 查询消失 → 网关不得依赖 companion 查询做结果权威。

### 2.3 Memora（上下文/环境交换层）【跨仓库】

| 接口 | 契约要点 | 证据 |
|------|---------|------|
| v2 typed ingest（**主写路径**） | `POST /api/v2/memories/ingest`：service JWT（tenant 从 JWT 派生）+ `Idempotency-Key` + `X-Correlation-ID` 必填；items ≤5000，每 item 须 `source_type/source_id/scope_chain.project_id/content_preview`；批内同 project；响应须解析 `failed[]/degraded`（200≠全部成功）；`source_system` 白名单已含 `gateway` | `memora/internal/v2/ingest.go:44-50,142-273,376-413` |
| v2 search | `POST /api/v2/memories/search`：scope_chain.tenant 必须==JWT tenant，project_id 必填，tags 在 `filters.tags`（顶层 tags 不生效），`policy.on_degraded` 显式 | `internal/v2/retrieval.go:36-75,153-238` |
| context manifest（**环境快照**） | `POST /api/v2/context-manifests`：append-only+版本+每 entry content_hash；verify 报 drifted/missing | `internal/v2/context_manifests.go:25-142`、迁移 000043 |
| 会话压缩（受控） | `POST /api/v1/sessions/:id/compress`：**仅网关服务端调用**（V1Auth，tenant 写死，见 §0-F5） | `internal/sessionmemory/handler.go:40-63,250-273` |
| PG-first 可靠性范式 | ingest receipt：PG 事务先落主记录→向量异步补偿→replay 补做→`vector_ready` | `internal/memory/ingest_receipt.go` |
| 无出站 webhook | 完成通知不能依赖 Memora | `docs/integration/redclaw-acc.md §2.5` |

### 2.4 网关（本仓，接入面/通知通道）

| 原语 | 现状与复用方式 |
|------|---------------|
| API key/tenant | 复用 `domains/authentication.KeyVerifier` + `domains/session/handler.go:14-138` 的 authenticate 模式（context 注入 api_key_id/tenant_id；**必须断言跨包 `authentication.InvalidKeyError`**，勿照抄 session 本地副本陷阱 handler.go:122-133） |
| HMAC 签名 | 复用 `internal/outbox/signature.go:10-37`（`timestamp.nonce.body`，hmac-sha256=）与 wire envelope 渲染（`wire.go:55-82`） |
| SSRF 防护 | 复用 `internal/safehttpclient`（私网/回环/metadata/IPv6/DNS rebinding/redirect 复验，allowlist 显式开启内网）`safe_http_client.go:57-109` |
| 后台 worker | 复用 `bg/base_worker.go:45-149`（幂等 Start/Stop、panic 隔离） |
| 迁移纪律 | 下一编号 **711**（704=R28 探测退避；705–710 已被远程会话 V2 等批次占用：`705…710_*.sql`）；唯一编号测试 `migration_version_unique_test.go` |
| 会话线层 | dispatch 会话组 `gw_<hosted_task_id>`；正文/成本权威在 request_logs/usage_ledger，零改造入账 |
| 现有 GoalRun | 仅作参考模式；P0 不写入 goal_runs（避免与 chat-goal 语义混淆） |

---

## 3. 端到端数据流

### 3.1 委托与执行（P0 主链）

```
①前端 → POST /v1/hosted-tasks (Bearer sk-*, Idempotency-Key)
   网关: KeyVerifier→tenant/api_key → 校验(goal/limits/callback SSRF) →
   Tx{ INSERT hosted_tasks(delegated) + hosted_task_events(accepted)
       + hosted_task_callbacks(URL+secret 持久化) }
   → 202 {hosted_task_id, status_url}

②网关 → ACC Runtime Control dispatch（同 Idempotency-Key 派生键 gw-hosted-<id>-a1）
   payload: {kind:"pi", prompt(goal+done_when+context 摘要), cwd(白名单映射路径),
             timeout_ms, mcp_servers?}; source_ref:"llm-gateway/hosted-task";
   correlation_id: hosted_task_id
   成功 → Tx{ status=dispatching→running 事件, 记 acc_command_id/acc_run_id }
   失败(网络/5xx) → 同键重放（ACC 幂等保证不双发）; 持续失败 → 事件 dispatch_degraded

③ACC SSE → companion（租约 acquire/start）→ spawn pi
   pi 环境注入: AGENT_GATEWAY_SESSION=gw_<hosted_task_id>（companion 按 dispatch_id 生成）
   pi 全部 LLM → 网关 loopback /v1/chat/completions → usage_ledger 入账（零改造）

④网关 reconciler（BaseWorker）:
   订阅 GET /api/v2/orchestration/runs/{run_id}/events/stream（after/Last-Event-ID 持久游标）
   + 定时轮询 GET /api/v2/runtime/commands/{command_id} 兜底
   → 投影 hosted_tasks.status/progress（CAS revision）+ hosted_task_events
   → 终态判定: pi 结果须校验 raw.stop_reason≠error（§0-F4）；unknown_outcome 不猜测，
     进入 needs_review（人工对账状态，P0 暴露为 failed(unknown) + 事件注明）

⑤通知:
   终态 → Tx{ 终态落 hosted_tasks(PG 权威) + result 持久化 + 事件 } →
   callback deliverer（safehttpclient + HMAC）POST callback.url → 2xx 完成;
   失败按退避重试→DLQ; 前端随时 GET /v1/hosted-tasks/{id}(/result) 拉取兜底
```

### 3.2 取消（P0）

前端 `POST .../cancel` → 网关 CAS（终态抢占）→ ACC `commands/:id/cancel`（requested）→ 事件 `cancel_requested`。
回执 delivered/effective 由 reconciler 跟踪；**effective 依赖 companion 侧 CommandCanceler 修复（P1）**，P0 文档明确"取消=请求受理"。

### 3.3 召回/拉回（P1）

`POST .../recall` → 网关组装 handoff 包（不改 ACC 状态时用轻量路径：cancel + 快照）：
① 线层导出（request_logs 该 gw 会话 turns 摘要/引用）；② 续层指针（pi native session id，来自 ACC session report 四元索引）；③ 知层（Memora scope_chain + context-manifest 引用 + 最新 compress 结果）；④ StructuredHandoffPacket{goal, currentResult, doneWhen, blockers, nextOwner, requiresInputFrom, artifactRefs}。
需执行权转移时走 ACC v3 transfer（source_fence→snapshot_capture→source_stop→source_release→commit）。事件 `hosted_task.recalled`，前端凭包续跑。

---

## 4. API / 状态机 / 事件契约（P0）

### 4.1 端点（鉴权=KeyVerifier；tenant 一律取自验证后 context）

| 端点 | 说明 |
|------|------|
| `POST /v1/hosted-tasks` | 必带 `Idempotency-Key`（16-256，对齐 handoff 约定）。body：`{goal(必填), done_when, context{summary, memora{project_id, tags[]}, artifacts[]}, environment{workspace_id(白名单映射,不接受裸路径), model_pref, timeout_seconds}, callback{url}, limits{deadline_seconds, budget_credits(P0 校验记录,P1 熔断)}}` → 202 `{hosted_task_id, status_url}`；同键同体重放 200 返回原任务 |
| `GET /v1/hosted-tasks/{id}` | 状态 + 事件时间线；跨租户/不存在统一 404 |
| `GET /v1/hosted-tasks/{id}/result` | running→202+Retry-After；终态→200 `{summary, output, artifact_refs[], memora{scope_chain, manifest_key}, cost, gw_session_id, pi_session_ref, result_version, content_hash}`（PG 权威，Redis 仅投影） |
| `POST /v1/hosted-tasks/{id}/cancel` | 202 `{cancel_status:"requested"}`；终态后 409 |
| `POST /v1/hosted-tasks/{id}/recall` | **P0 返回 501**（显式不支持），P1 实现 |

### 4.2 hosted_tasks 状态机（独立于 GoalRun 字符串）

```
delegated → dispatching → running → completed | failed
                                   ↘ needs_review (unknown_outcome)
任意非终态 → cancelled | expired(deadline reaper)
终态 sticky（CAS 抢占，cancel 与 complete 竞争只活一个）
```

### 4.3 事件（hosted_task_events，append-only，唯一 (task_id, seq)）

`accepted / dispatch_degraded / running / progress / cancel_requested / completed / failed / expired / cancelled / callback_delivered / callback_dlq`
（P1 增：budget_exceeded / recalled / phase_changed）

---

## 5. Memora 上下文与环境交换设计

| 时机 | 写入方 | 内容与接口 |
|------|--------|-----------|
| 委托时 | 网关（service JWT，tenant=KeyVerifier 派生值） | 任务上下文（goal/done_when/约束）typed ingest（source_type=context, scope_chain={project_id, task_id=hosted_id}）；**环境信息**（workspace 映射、模型偏好、工具约束）写 context-manifest（manifest_key=hosted_id, hash 快照） |
| 执行中 | companion（已有链路，P0 局限见下） | tool 产物 artifact ingest（幂等键=dispatch_id-tool_call_id）；**P0 限制**：companion MemoraSearch tenant 硬编码 default → P0 网关在 prompt 内联上下文摘要+scope 引用，跨租户注入修复列 P1【跨仓库】 |
| 阶段边界(P1) | 网关受控调用 | `/api/v1/sessions/:id/compress`（tenant 写死）+ L2 ingest-summary（X-API-Key 面，`X-Memora-Stub:true` 视为未持久化） |
| 终局 | companion + 网关 | 会话摘要/decision_outcome ingest（幂等键=hosted_id）；网关校验 `degraded/failed[]` 后才在事件中声明"知识已沉淀" |
| 召回(P1) | 网关 | handoff 包携带 scope_chain + manifest_key + compress 引用 |

**红线**：Memora 只存提炼物与引用，不存会话正文（线层在网关）；Memora 无出站通知，一切事件经网关 outbox 语义。

---

## 6. P0 可执行清单

### 6.0 跨仓库前置门禁（先于网关编码验收，均【跨仓库】）

1. 网关宿主机部署 agent-companion（systemd 常驻）+ Node/pi 运行时；companion 向 ACC Runtime Control `register`（agent_inventory 声明 kind=pi），heartbeat 正常。
2. companion 侧 LLM 配置指向网关 loopback（独立内部 API key，独立预算/观测）。
3. 网关持有：ACC service JWT（含 tenant_id claim）、Memora service JWT（v2 契约）。
4. 245 staging 演练：创建单阶段任务→pi 自主完成→网关入账→回调+拉取（§8 矩阵 A 组）。

### 6.1 网关侧新文件（逐文件）

| 文件 | 内容 |
|------|------|
| `sql/migrations/startup/711_hosted_tasks.sql` (+.down) | 三表：`hosted_tasks`（id, tenant_id, api_key_id, goal, done_when, status CHECK, acc_command_id, acc_run_id, gw_session_id, workspace_id, model_pref, deadline_at, callback_url_hash, result JSONB, result_version, revision, idempotency 唯一(tenant_id,idempotency_key), 终态 sticky CHECK）；`hosted_task_events`（唯一(task_id,seq)）；`hosted_task_callbacks`（url, secret 加密, attempt/next_at/status, DLQ 字段）。全表 RLS（app.current_tenant），bypass 仅 worker 角色 |
| `sql/migrations/startup/migration_711_test.go` | 唯一编号+fresh up/down/up+RLS 负向矩阵 |
| `domains/hostedtask/types.go` | 状态/事件枚举 + 纯函数迁移矩阵（表驱动测试） |
| `domains/hostedtask/store.go` | Tx 内幂等创建/CAS 投影/终态抢占/事件追加（参照 routeincident store.go:77-164 的 FOR UPDATE+version CAS） |
| `domains/hostedtask/handler.go` | 五端点；authenticate 复用 session 模式（跨包 InvalidKeyError 断言） |
| `domains/hostedtask/acc_client.go` | Runtime Control 客户端：dispatch/getCommand/cancel/run SSE（token env `LLM_GATEWAY_ACC_SERVICE_TOKEN`、base env `LLM_GATEWAY_ACC_BASE_URL`；注入 http.Client 便于测试；SSE 游标持久化在 hosted_tasks 行） |
| `domains/hostedtask/callbacks.go` + `internal/hostedcallback/` | callback deliverer：safehttpclient（redirect=0 或逐跳复验；allowlist 可配）+ 复用 `outbox.SignPayload` 头；退避重试→DLQ；event_id=`hosted_<id>_ev<seq>` 固定 |
| `bg/hosted_task_reconciler.go` | BaseWorker：SSE 订阅（断线 after 恢复）+ 轮询兜底 + deadline reaper + 终态触发回调入队 |
| `cmd/gateway/main.go` | 装配 + `mux.Handle("/v1/hosted-tasks", …)`、`/v1/hosted-tasks/`；**顺带修复**：`/v1/goal-runs/{id}` 挂载前先给 goalrun_handler 补 KeyVerifier 归属校验（独立小 PR） |
| `installer/…/embeddata/startup/711_*` + `runner.go` 清单 + `docs/db-changelog.md` | 三处同步（§0 反例教训） |
| `config/config.go` + `.env.example` | hostedtask 配置块（ACC URL/token、callback allowlist、deadline 默认值、worker 开关） |

### 6.2 P0 明确不做

recall（501）、多阶段拆解、budget 熔断执行、多 runtime 调度、pi 沙箱（P0 以 workspace 白名单+低权用户+内网过渡，P2 容器化）、修改现有 ASM outbox 语义。

---

## 7. P1 / P2 路线

- **P1（2–3 周）**：recall/handoff 包（§3.3，含 ACC transfer 接线或轻量快照路径）；ACC canonical task 双写（账本对齐，资源版本乐观并发）；多阶段（ACC 拆解/阶段边界 Memora compress+ingest 自动化）；budget 熔断（usage_ledger 按 gw_session 归集 + 原子扣减 + `budget_exceeded` 单次事件）；companion 侧修复【跨仓库】：FacadeExecutor 实现 CommandCanceler、pi stopReason=error→失败、MemoraSearch tenant 参数化。
- **P2**：多 pi 并发/多宿主（runtime registry 负载策略）、pi 容器化/cube 隔离、web 管理看板、前端 delegate/recall skill、pi 接 ACC MCP 工具（复用 pi-swarm accmcp 扩展）。

---

## 8. 验收测试矩阵

| 组 | 用例（全部门禁） |
|----|----------------|
| A 端到端(245) | 委托→pi 完成→usage_ledger 入账→回调 2xx→result 可读；记录 commit/迁移/flag/外部版本 |
| B API | 401/403(跨租户 404)/400/405/幂等重放(同键同体 200、同键异体 409)/并发 cancel-vs-complete 单终态 |
| C 路由装配 | httptest 打最终 mux（含 h2c），/v1/hosted-tasks 不被 static fallback 吞 |
| D 迁移/RLS | 711 fresh/upgrade/down-up；NOSUPERUSER+NOBYPASSRLS 租户 A/B 负向；无 TEST_DATABASE_URL 不得记 PASS |
| E 回调 | HMAC 正确/过期/重放；2xx；4xx 不重试；5xx 退避→DLQ；loopback/RFC1918/metadata/redirect 复验全拒；POST 成功 commit 前崩溃→重投+event_id 幂等 |
| F 结果 | Redis 清空后 PG 回源；result_version 单调；hash/tenant 不匹配拒绝 |
| G 恢复 | 网关重启后 SSE 游标续传；同 Idempotency-Key 重放不双发；unknown_outcome→needs_review 不猜测 |
| H 取消 | 2xx=requested；旧执行迟到回写被终态 CAS 拒（0 行） |

---

## 9. 风险与对策

| 风险 | 对策 |
|------|------|
| callback SSRF/数据外泄 | P0 即做：safehttpclient+allowlist+redirect 复验+secret 加密存储 |
| 取消不可证（companion delivered 止步） | 语义降级"requested"；effective 事件化；P1 companion 修复 |
| pi 假成功（stopReason=error） | 网关终态判定强制校验 raw.stop_reason |
| ACC/companion 状态漂移 | 执行真相=ACC；网关只投影+needs_review 人工对账态 |
| cwd 任意路径（CheckPath 未接线） | workspace_id 白名单映射，禁裸路径 |
| 迁移漂移重演 | 711 三处同步纳入 PR checklist + CI 唯一编号测试 |
| Memora stub/降级伪成功 | 解析 failed[]/degraded/X-Memora-Stub，未确认不声明沉淀完成 |
| 跨仓库依赖不可复现 | §6.0 四项门禁前置于编码验收；缺依赖记 SKIPPED-CONFIG |

---

## 10. 决策记录

| 决策 | 选择 | 依据 |
|------|------|------|
| D1 谁执行 | 网关转交 ACC（Runtime Control dispatch），不自建 agent runtime | 用户原则+RedClaw 单写者+companion 写路径契约（ACC 是唯一能驱动 companion 的控制面） |
| D2 进展如何回前端 | ACC SSE→网关 reconciler 投影→网关签名回调+拉取 | 用户原则；复用网关 HMAC/重试模式；前端零新增依赖 |
| D3 上下文放哪 | Memora（v2 typed ingest + context-manifest + scope_chain），正文留网关线层 | 用户原则+三层权威设计（SESSION_STORAGE_DESIGN） |
| D4 hosted_tasks 定位 | 关联投影表，非执行 owner | 防 durable/goalrun/ACC 三方状态竞争（R28 审计结论） |
| D5 P0 回调实现 | 新建 hosted_task_callbacks+deliverer，不动 ASM outbox | F1 核实：现 outbox 单端点语义不匹配 |
| D6 P0 驱动协议 | pi `--mode json` headless | F3 核实：RPC/ACP 驱动尚未实现，headless 已实证可跑 |
