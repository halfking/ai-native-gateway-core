# 设计：网关任务托管（Hosted Task Delegation）

> 状态：设计提案（未实施） 日期：2026-09-14
> 目标：前端智能体可将一个任务**移交给网关全权处理**（网关侧运行 pi 智能体），完成后网关**反馈结果**给前端智能体；任务可随时**拉回**前端智能体。全程由 **acc + agent-companion** 做任务管理调配、多智能体协同与 handoff，由 **memora** 做会话与上下文管理。

---

## 1. 需求重述

| # | 需求 | 对应能力缺口 |
|---|------|------------|
| R1 | 前端智能体 → 网关：任务移交（含上下文） | 需新增网关托管 API 与任务台账 |
| R2 | 网关侧全权执行：安装 pi 智能体，长任务自主运行 | companion 已支持 pi driver；需部署 + 驱动链路 |
| R3 | 任务管理调配、多智能体调度协同（长任务分阶段） | ACC coordinator/mission/decomposer 已有，需接线 |
| R4 | 自动上下文管理及 handoff（阶段间移交） | pi compaction + memora compress + ACC StructuredHandoffPacket，需编排串联 |
| R5 | 完成后网关 → 前端智能体反馈 | 网关 outbox 签名 webhook + 结果拉取已具备，需接事件 |
| R6 | 任务拉回（网关 → 前端智能体） | 需新增 recall API + handoff 包组装 |

## 2. 各模块能力盘点（2026-09-14 调研结论）

### 2.1 llm-gateway-go-5（网关，本仓库）
- **已有持久任务台账**：`goal_runs` / `goal_run_steps` / `goal_run_actions`（迁移 549/554/555，含 lease/fencing），状态机含 `waiting_tool` / `waiting_input` / `waiting_handoff` / `auditing`（`domains/goalrun/types.go:17-92`）。
- **可恢复任务队列**：`durable_llm_tasks`（迁移 516/520），claim/lease/fencing token/write-ahead commit/reaper（`durable/task.go:14`），env 门控 `LLM_GATEWAY_REQUEST_SURVIVAL_DURABLE_ENABLED`。
- **完成回调**：`internal/outbox`（PG outbox → Dispatcher 重试/DLQ → 签名 HTTP POST，`X-Gateway-Event-Signature` 等头，`internal/outbox/dispatcher.go:98`）。
- **结果拉取**：`pending/`（Redis CAS + PG 兜底，供轮询）。
- **会话线层**：`session_summaries`（事实上的会话主表）+ `request_logs` 分区；`X-Gw-Session-Id` 归组。
- **handoff 相关**：`/v1/handoffs/confirm` 已挂载（`domains/streaming/handoff_confirmation.go:23`）；`/v1/goal-runs/{id}` handler 已实现但**路由未挂载**（`cmd/gateway/main.go:753` `_ = goalRunHandler`）；自动 handoff 触发器已停用（`domains/hooks/handoff/doc.go`，因引用了不存在的 sessions 表，复活需映射 `session_summaries`）。
- **网关自调 LLM 先例**：`internal/loopback` 回环调自身 `/v1/chat/completions`（自动标题 `admin/auto_title_generator.go`）。
- **网关上跑外部进程先例**：`plugin-runtime/`（manifest/handshake/安装器/生命周期）。
- **后台 worker 框架**：`bg/base_worker.go:33` BaseWorker（ticker 循环，main.go 约 79 个 worker）。
- 缺口：**无通用 agent 推理循环/tool 执行器**（`domains/toolexecution` 只做统计）。

### 2.2 acc（Agent Control Center）
- 双进程：Node `:4100`（Express+WS）+ acc-go `:4101`（Echo+Asynq+Temporal），`/api/v2/*` 走 Go。
- **任务管理**：12 态统一状态机（`lib/task-states.js`），canonical `acc_tasks`；coordinator API：`POST /api/v2/coordinator/tasks`、`/tasks/:id/decompose`、**`/tasks/:id/dispatch`**、**`/tasks/:id/handoff`**、`/tasks/:id/complete`、`/webhook/llm-gateway`（`acc-go/internal/httpapi/handlers/coordinator_native.go:62-74`）。
- **运行时注册**：`/api/v2/runtime` register/heartbeat，实例可声明 `api_endpoint`（`internal/runtimecontrol/service.go:89-113`）——**外部运行时（网关上的 companion+pi）接入 ACC 的标准挂点**。
- **移交状态机（最完整）**：编排 v3 `/api/v2/orchestration/tasks/:id/transfers`，16 态（requested→source_fenced→snapshot_captured→…→committed），含 fencing token/CAS revision/快照/幂等键/SSE（`internal/httpapi/handlers/transfer_native.go:53-72`）。
- **语义交接包**：`StructuredHandoffPacket{goal, currentResult, doneWhen, blockers, nextOwner, requiresInputFrom, artifactRefs}`（`lib/collaboration-engine/handoff-manager.js:1-70`）。
- **多智能体协同**：Mission 7-Phase（intake→deliberating→planning→executing→auditing→reporting→completed）、任务拆解 L1→L4（`lib/task-decomposer.js`）、协作引擎 12 工作流、A2A JSON-RPC 对等面（`internal/a2a/`）。
- **与 memora**：`KXMEMORY_BASE_URL` + `internal/kxmem/` 客户端；MCP 工具 `acc_memora_search/store`。

### 2.3 agent-companion
- Go headless 守护进程，桥接本机编码智能体与 ACC；**pi 是一等驱动**（`pi --mode json`，`internal/agentfacade/cli_driver.go:37-39`）。
- **续跑**：`POST /api/v1/native/sessions/:id/operate` 对观测到的原生会话续跑（`internal/api/operate.go:61-233`）；`ResumePrefix` 生成 `--resume`/`--session`（pi）。
- **网关会话归组**：每次 dispatch 注入 `AGENT_GATEWAY_SESSION=gw_<dispatch_id>` → 一个 dispatch = 一个网关会话（`internal/accbridge/executor.go:119-124`）。
- **记忆注入**：run 组装接入 `MemoraSearch`（注入 prompt）与 `MemoraSink`（产物/会话沉淀）（`cmd/agent-companion/main.go:688-696`）。
- **LLM 配置下发**：`internal/llmconf` 把网关 baseUrl+key 渲染进各 CLI 原生配置（`/llm/deploy`，diff/dry-run/备份）。
- **边界契约（红线）**：本地 API 只读，**写路径全走 ACC**（`docs/swarm/SWARM_BOUNDARY_CONTRACT.md:2.1-2.3`）。
- 无任务台账（权威在 ACC）；沙箱：cube sandbox（bash/http 步骤计划执行，`internal/cube/plan.go:15-45`）。
- **三层权威 + 索引**（`docs/SESSION_STORAGE_DESIGN.md`）：线层=网关（会话/正文/成本）、续层=agent 原生会话文件、知层=memora（只存提炼物）、索引层=ACC 四元关联（task↔dispatch↔agent_session↔gw_session，`internal/accbridge/session_report.go:31-41`）。

### 2.4 memora（kxmemory-go）
- Go 记忆/知识服务：L1–L6 分层记忆、混合检索（BM25+向量+RRF+MMR，`internal/memory/hybrid.go:33`）、会话压缩 `POST /api/v1/sessions/:session_id/compress`（summary/entity/hybrid，`internal/sessionmemory/handler.go:44-50`）、会话摘要摄取 `/api/session/ingest-summary`。
- **隔离**：`scope_chain = {tenant_id, project_id, run_id, task_id, agent_session_id}`，PG RLS 强制（`internal/v2/retrieval.go:37-43`）；v2 检索强制要求 `project_id`。
- **跨 agent 延续原语**：`memory.session_id` 挂会话、`openclaw_handoffs`（agent→target_agent 带 summary JSONB，迁移 000017）、`openclaw_shared_thread`（项目级共享线程）、上下文 manifest（迁移 000043，append-only+哈希快照）。
- `source_system` 白名单**已含 `gateway` / `pi-swarm`**（`internal/v2/ingest.go:44-50`）——网关作为记忆来源是官方支持枚举。
- **无出站 webhook**（`docs/integration/redclaw-acc.md` §2.5）：跨服务全入站，异步靠轮询 + receipts。

### 2.5 pi（@earendil-works/pi-coding-agent v0.85.0）
- 4 种运行形态：TUI / `-p` print / `--mode json`（JSONL 事件流）/ `--mode rpc`（stdin/stdout JSONL 协议）+ SDK。
- **会话**：JSONL 树（v3 格式），`~/.pi/agent/sessions/...`；`-c`/`--session`/`--fork` 恢复分叉；可 `PI_CODING_AGENT_SESSION_DIR` 重定向。
- **上下文管理**：自动 compaction（`contextWindow - reserveTokens` 触发，结构化摘要 + retainedTail checkpoint）、`/compact [instructions]`、分支摘要（`docs/compaction.md`）。
- **LLM 后端**：`~/.pi/agent/models.json` 自定义 provider（baseUrl + `openai-completions`）——**本机已实证指向 `https://llm.kxpms.cn/v1`（即网关），带 `X-Gw-Session-Id` 头注入**。
- 工具：read/write/edit/bash/grep 等；**不内置 MCP**（扩展机制补齐；pi-swarm 的 `extension/pi-swarm-tools.js`、`src/accmcp.js` 是现成的 ACC MCP 客户端先例）。
- Linux 常驻：官方 containerization 文档（node:24-slim Dockerfile）+ systemd；pi-swarm `deploy/README.md:71-75` 已给 launchd→systemd 映射。

## 3. 总体架构

### 3.1 角色分工（职责单一，权威分明）

```
前端智能体(用户机器)          网关宿主机(245/154)                     平台服务
┌─────────────────┐   delegate   ┌──────────────────────────┐        ┌──────────┐
│ zcode/claude/…  │ ───────────▶ │  llm-gateway-go           │───────▶│ ACC      │
│  · 发起托管      │ ◀─────────── │  · hosted-task 台账+API   │ dispatch│ :4100/01 │
│  · 接收回调      │  webhook/轮询 │  · outbox 回调/结果拉取    │ ◀────── │ 拆解/派发 │
│  · recall 拉回   │              │  · 会话线层权威/计费       │        │ transfer │
└─────────────────┘              │  · loopback LLM 供给      │        └────┬─────┘
                                 └──────────────────────────┘             │ dispatch
                                        ▲ LLM (loopback)                  ▼
                                        │                          ┌──────────────┐
                                 ┌──────┴───────────┐              │ agent-companion│
                                 │ pi (gateway 宿主机)│◀────────────│ (网关宿主机)    │
                                 │  · tool loop      │  spawn/驱动  │ · pi driver    │
                                 │  · compaction     │              │ · resume/operate│
                                 └──────┬───────────┘              │ · 记忆注入      │
                                        │ L1/L2 沉淀/compress       └──────┬───────┘
                                        ▼                                 │ session_report
                                 ┌──────────────┐◀────────────────────────┘
                                 │ memora       │  scope_chain 隔离 · handoff 快照
                                 └──────────────┘
```

- **网关 = 托管面权威**：任务台账、API、回调、结果、会话线层、计费（pi 的 LLM 调用全过网关，计费自动进 `usage_ledger`）。
- **ACC = 编排权威**：多阶段拆解、dispatch、transfer/handoff 状态机、四元索引。
- **companion = 执行面**：pi 进程托管/续跑/记忆注入；遵守"本地只读、写走 ACC"契约。
- **memora = 上下文权威**：L1 切片/L2 摘要/压缩/handoff 快照/跨 agent 检索。
- **pi = 执行体**：tool 循环 + 会话内 compaction。

### 3.2 关键决策

| 决策 | 选择 | 理由 | 备选 |
|------|------|------|------|
| D1 网关如何驱动 pi | 网关→ACC coordinator dispatch→companion→pi | 尊重 companion"写走 ACC"红线；复用 dispatch 幂等键/租约/reaper | 网关直接调 companion 本地 API（需改契约，否决） |
| D2 任务台账放哪 | 网关新建 `hosted_tasks`，并保留 goal_run 关联 | 面向前端智能体的数据面契约归网关；goal_runs 状态机可内嵌复用 | 直接复用 goal_runs（耦合 chat 请求语义，否决） |
| D3 完成通知 | 网关 outbox 签名 webhook（主）+ `GET result` 轮询（兜底） | outbox 重试/DLQ/HMAC 已生产级；memora 式纯轮询不满足"反馈"需求 | memora 通知（无出站 webhook，否决） |
| D4 拉回语义 | recall = ACC transfer(halt) + 网关组装 handoff 包 | 复用 16 态 transfer 的 fence/snapshot 语义；包组装是网关强项（线层在手） | 仅 cancel（丢上下文，否决） |
| D5 上下文延续 | 线层（网关）+ 知层（memora）+ 续层指针（pi session JSONL） | 即平台既有三层权威设计，不新造 | 全量正文塞 memora（违反知层契约，否决） |
| D6 ACC 状态回投 | 网关 BaseWorker 轮询 ACC 任务状态 | 简单可靠；ACC 有 SSE 但跨网部署轮询更稳 | ACC→网关 webhook（ACC 侧新增出站，可后补） |

## 4. 核心流程

### 4.1 委托（前端 → 网关）
1. 前端智能体调 `POST /v1/hosted-tasks`：任务目标/完成判据/上下文（内联摘要 + memora scope 引用 + artifact 引用）/回调 URL/预算与限额。
2. 网关：鉴权（既有 KeyVerifier）→ 建 `hosted_tasks` 行（状态 `delegated`）→ 建 `gw_<id>` 会话组 → 写 outbox 事件 `hosted_task.accepted`。
3. 网关以 service token 调 ACC `POST /api/v2/coordinator/tasks`（payload 带 correlation_id=hosted_task_id）+ `/tasks/:id/dispatch`；ACC 按 runtime registry 派给网关宿主机上的 companion。
4. companion 驱动 pi：注入 `AGENT_GATEWAY_SESSION=gw_<id>`、网关 baseUrl/key（llmconf 或 env）、memora 记忆检索结果（MemoraSearch）；pi 以 `--mode json` 无头运行。

### 4.2 执行与上下文管理（长任务分阶段）
- **会话内**：pi 自动 compaction 兜住上下文窗口；工具调用过网关计费与 `toolexecution` 统计。
- **阶段间**：ACC decomposer/Mission 拆解为多阶段（多 dispatch，同 correlation_id）；阶段边界由 companion 触发 memora `ingest-summary`（L2）+ `POST /api/v1/sessions/:id/compress`（hybrid）+ typed artifact ingest（L5）；下一阶段 dispatch 时 MemoraSearch 注入上阶段提炼物。阶段移交用 `StructuredHandoffPacket`。
- **线层**：全程 `X-Gw-Session-Id=gw_<id>`，网关 `session_summaries`/`request_logs` 留正文与成本权威。

### 4.3 完成 → 反馈（网关 → 前端）
1. companion 上报 ACC（完成回写 + `acc_report_session` 四元索引）；pi 产物按需 ingest memora。
2. 网关 worker 轮询发现 ACC 任务终态 → 校验预算/产物 → 置 `hosted_tasks` 终态（`completed`/`failed`）→ 写 outbox。
3. outbox Dispatcher 签名 POST 前端回调 URL（`hosted_task.completed`，带 result 摘要 + 签名头）；前端也可随时 `GET /v1/hosted-tasks/:id` 与 `.../result`（pending/ 支撑，含产物引用、成本、memora artifact id、pi session 指针）。

### 4.4 拉回（网关 → 前端智能体）
1. 前端调 `POST /v1/hosted-tasks/:id/recall`。
2. 网关经 ACC 发起 transfer（进入 source_fenced/snapshot_captured，或轻量路径：dispatch cancel + companion run cancel）。
3. 网关组装 **handoff 包**：① 线层导出（该 gw 会话 turns 摘要/全文引用）；② 续层指针（pi session JSONL 路径/id，companion nativestore 可查）；③ 知层（memora scope_chain 检索引用 + 最新 compress 结果）；④ `StructuredHandoffPacket`（goal/currentResult/doneWhen/blockers/artifactRefs）。
4. 置状态 `recalled`，outbox 事件 `hosted_task.recalled`；前端智能体以自身上下文 + handoff 包续跑。同时写 memora `openclaw_handoffs`（from=pi@gateway, to=前端 agent）留档。

## 5. API 与数据模型（网关新增）

### 5.1 数据面 API（`/v1/hosted-tasks`，鉴权走既有 KeyVerifier）
| 端点 | 说明 |
|------|------|
| `POST /v1/hosted-tasks` | 委托：goal、done_when、context{summary, memora_scope{tenant,project,task}, artifacts[]}、callback{url}、limits{max_duration, budget_credits}、model_pref → `{task_id, status_url}` |
| `GET /v1/hosted-tasks/:id` | 状态 + 事件时间线（归属校验：tenant/key） |
| `GET /v1/hosted-tasks/:id/result` | 终态结果：产物引用、成本、session 指针、memora artifact ids |
| `POST /v1/hosted-tasks/:id/recall` | 拉回：触发 transfer + 组装 handoff 包 |
| `POST /v1/hosted-tasks/:id/cancel` | 取消（不组包） |
| webhook 事件 | `hosted_task.accepted / running / waiting_input / completed / failed / recalled / budget_exceeded`（outbox HMAC 签名） |

挂载遵循 `main.go:5889` sessions 的写法；`/v1/goal-runs/:id`（已实现未挂载）顺手并入本次挂载。

### 5.2 数据模型（新迁移 `sql/migrations/startup/`）
- `hosted_tasks`：`id, tenant_id, api_key_id, frontend_agent(kind, instance_id), goal, done_when, status, acc_task_id, acc_dispatch_id, gw_session_id, goal_run_id(可空), callback_url, budget_credits, spent_credits, model_pref, created_at, updated_at, finished_at`。状态机：`delegated→running→(waiting_input|completing)→completed|failed|recalled|cancelled`。
- `hosted_task_events`：状态流转/阶段进度台账（或映射写入 goal_run_steps）。
- 复用：`internal/outbox`（回调）、`pending/`（结果缓存）、`durable/`（网关自身崩溃恢复时 claim/lease 语义）、`usage_ledger`（pi 调用计费，零改造）。

## 6. 部署拓扑

- 网关宿主机新增两个常驻进程：**agent-companion**（systemd，注册 ACC runtime control，`api_endpoint` 指向本地只读面）与 **pi**（由 companion 按需 spawn，不必常驻；Node 22+ 运行时进宿主机或复用 pi 官方 Docker 模板 + 沙箱）。
- pi 的 LLM 后端：`models.json` provider 指向 `http://127.0.0.1:8781/v1`（loopback，走 `internal/loopback` 同款端口感知）+ 网关签发的内部 API key（独立 key、独立预算、可观测隔离）。
- ACC 侧：网关宿主机 companion 完成一次 `runtime register`（capabilities 声明 pi/claude/codex 驱动），dispatch 幂等键沿用 `pi-swarm-engine-<task_id>` 风格：`gw-hosted-<hosted_task_id>`。
- memora/ACC/网关已共享 PG 实例，无新增存储依赖。

## 7. 实施路线

| 阶段 | 内容 | 验收 |
|------|------|------|
| **P0 MVP（≈1–2 周）** | 网关宿主机部署 companion+pi（llmconf 指向网关）；网关 `hosted_tasks` 表 + `/v1/hosted-tasks` 五端点 + 状态机；ACC dispatch 接线（单阶段，手动派发）；完成回调 outbox 事件 + result 拉取；挂载 `/v1/goal-runs/:id` | 前端 curl 委托一个单阶段任务 → 网关上 pi 自主完成 → 回调+拉取结果；计费入账 |
| **P1（≈2–3 周）** | recall/handoff 包组装（线层导出+续层指针+memora 快照+Packet）；ACC decomposer 多阶段拆解 + 阶段边界 memora compress/ingest 自动化；预算/时长限额执行与 `budget_exceeded` 事件；网关 worker 轮询 ACC 状态 | 两阶段任务跨阶段上下文延续验证；中途 recall 后前端智能体凭 handoff 包续跑成功 |
| **P2（后续）** | 多 pi 并发/多宿主机（runtime registry 多实例+负载策略）；沙箱加固（companion cube sandbox 或 pi 容器化）；前端智能体侧封装 skill（delegate/recall 一键化，参照 `~/.agents/skills/handoff`）；网关 web 管理面 hosted-task 看板；pi 接 ACC MCP 工具（复用 pi-swarm accmcp 扩展）实现 pi 会话内自查任务/自报进度 | 多任务并发、多宿主调度、看板可见 |

## 8. 风险与对策

| 风险 | 对策 |
|------|------|
| companion"本地只读"契约被绕过 temptations | 严格执行 D1（写路径全走 ACC）；网关不直写 companion 状态 |
| pi bash 工具在网关宿主机上的安全面 | P0 限内网+独立低权用户；P2 cube sandbox/容器隔离；对 hosted 任务的 bash 调用加 `toolexecution` 审计与告警 |
| ACC dispatch 对 pi kind 的支持确认 | companion 侧 pi driver 已实证；ACC 侧 engine kind 需在 P0 首日验证（pi-swarm M2-A 已走通同链路，风险低） |
| memora 无出站 webhook | 全部事件通知由网关 outbox 承担；memora 侧维持入站轮询模式 |
| `session_summaries` 替代 sessions 表的历史坑 | 新代码一律以 `session_summaries` 为会话主表（`domains/hooks/handoff/doc.go` 教训） |
| 长任务网关重启 | hosted_tasks 状态落 PG；恢复逻辑复用 durable claim/lease 语义；companion operations ledger 对 `unknown_outcome` 的保守语义兜底 |
| 预算失控 | 委托时强制 budget_credits + max_duration；pi 全部 LLM 走网关 → 天然可计量可熔断 |

## 9. 结论

**可行，且大部分原语已存在并有实证**：pi 可编程驱动/可恢复/自带 compaction 且已实证走网关 LLM；companion 原生支持 pi 并已打通 ACC dispatch、网关会话归组、memora 注入三条链路；ACC 有完整的 dispatch/handoff/transfer 状态机；memora 有 scope_chain 隔离、压缩与 handoff 表；网关有 outbox 回调、pending 结果、goal_runs/durable 台账。**真正的新建工作集中在网关的 hosted-task 子系统（API+台账+回调/召回闭环）与各链路的编排接线**，按 P0→P2 路线可在不破坏既有边界契约的前提下落地。
