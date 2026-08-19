# 统一自动编排插件执行计划

**状态**：Draft；实施前必须完成 G0，任何波次均不得把本文的设计当作已上线事实。  
**日期**：2026-08-19  
**设计依据**：[统一自动编排插件与 Goal 会话控制设计](../../03-design/02-feature-design/会话优化v4/13-统一自动编排插件与Goal会话控制设计.md)  
**交接执行包**：[handoff](../../handoff/2026-08-19-unified-auto-orchestration-plugin-handoff.md)

---

## 0. 强制执行原则

```text
IMPLEMENTED != LOCAL_VERIFIED != REAL_DEPENDENCY_VERIFIED != RELEASE_READY
```

- `IMPLEMENTED`：代码和迁移已写入，不表示编译、测试或行为正确。
- `LOCAL_VERIFIED`：指定本地测试、静态检查和 mock/in-memory 场景通过。
- `REAL_DEPENDENCY_VERIFIED`：隔离 PostgreSQL、Redis、插件进程和模拟 provider 的真实依赖场景通过。
- `RELEASE_READY`：完成隔离 canary、观察窗口、审批、回滚演练和发布门禁后才能标记。

任何状态声明必须附带命令、日志/指标或可重现证据。不得因历史 handoff、旧 commit SHA、文档 checkbox 或某一个单元测试通过而推断更高状态。

### 0.1 不可越过的生产边界

下列操作不在自动 Goal 或子代理授权范围内：生产 DDL/迁移、数据 cleanup、部署、SSH、systemd、重启、凭据/DSN/KMS 变更、模型 allowlist/cost policy 变更、Git 推送/合并。每项都需要独立书面授权、目标环境、审批人、维护窗口和回滚计划。

245、154、252 不是隔离 canary。隔离 canary 必须有独立 PG、Redis、tenant、credential、模型 allowlist、key prefix 和 token；缺任意一项即 NO-GO。

---

## G0：实施前基线与工作树保护

### 目标

在任何改动前建立可复现基线，并避免覆盖同时进行的工作。

### 必做

1. 记录 `git status --short`、`git rev-parse HEAD`、当前 branch、`git reflog -5`。
2. 记录当前未提交文件；未经原作者授权不得修改、暂存、恢复、stash 或纳入提交。
3. 每个执行 agent 使用独立 worktree 和 branch；主工作树不作为 agent 修改目标。
4. 阅读设计、审计和 handoff；验证相关实现/测试仍与文档引用一致。
5. 确认是否存在真实 PG/Redis、插件 sandbox、provider fixture 和 canary 授权；不存在则只做设计/本地测试并记录阻塞。

### 禁止

- `git reset --hard`、`git checkout --`、`git restore`、`git clean`、`git stash`。
- 对未声明范围的文件 `git add -A`。
- 根据文档推断生产服务、canary、migration 或 worker 已启用。

### 退出条件

基线、允许文件清单、禁止文件清单、agent 分支和真实依赖状态均进入本轮 handoff。

---

## Wave 0：契约冻结与设计验证（可并行，只读）

| 工作包 | 目标 | 主要范围 | 交付物 |
|---|---|---|---|
| W0-A 领域状态机 | 冻结 GoalRun/step/action、终态、lease/CAS 和 reason code | `domain/`、`durable/`、Goal/Handoff 设计 | 状态迁移矩阵与非法迁移测试清单 |
| W0-B 插件能力 | 冻结 binding、capability、DTO、scope、timeout、failure policy | `plugin-runtime/`、`domains/pipeline/` | manifest/binding 契约与 DTO 白名单 |
| W0-C 协议矩阵 | 冻结 Goal/durable/pending/handoff/approval 的 202、poll、stream 语义 | `domains/streaming/`、`domains/session/` | 协议兼容矩阵和错误信封 |
| W0-D 安全审计 | 审查外部插件环境/DSN、tool 自动化、stream 不可逆、GoalState 明文风险 | plugin runtime、安全/audit、ADR | 风险清单与 fail-closed 条件 |

### 不变量

- GoalRun 不能成为第二个 durable task owner。
- Goal session 不能成为跨会话完整执行账本。
- response end 不能被描述为 client-visible redaction 点。
- tool call、content 和 side effect 后不得透明 replay。

### 验收

设计评审确认单一 owner、协议隔离、自动化边界和 `manual_required` 路径。Wave 0 不修改生产代码。

---

## Wave 1：插件运行时与 hook binding 桥

### W1-A：PluginBindingRegistry 与 manifest gate

**目标**：建立插件生命周期 binding 的强校验和 ready 门禁。

**允许文件（预期）**：

```text
plugin-runtime/types.go
plugin-runtime/manifest*.go
plugin-runtime/registry*.go
cmd/gateway/plugin_runtime_init.go
cmd/gateway/plugin_supervisor_init.go
新增 plugin-runtime/binding*.go 与对应测试
```

**实现要点**：

- binding 指定 phase、priority、mode、capabilities、tenant/model scope、timeout、concurrency、failure policy 和 DTO profile。
- capability 必须真正授权；未知 capability、非法 phase、超范围 binding 或无签名生产 manifest 拒绝注册。
- 启动后仅在 signature、handshake、health、binding registration 都成功时标记 ready。
- 不传递完整 gateway environment 或 DSN；先建立受控 env allowlist，未完成 sandbox 时禁止 data-plane write capability。

**测试**：manifest/binding validation、handshake failure、health failure、scope、capability deny、timeout/circuit breaker、ready state transition。

### W1-B：生命周期 adapter

**目标**：把 remote/in-process binding 分别接到现有 adapter seam。

| Binding | 接入点 | 限制 |
|---|---|---|
| request/governance | request 前 pipeline / v1 hard gate | rewrite 后必须重验 schema/policy；身份字段不可改 |
| response/stream | `ResponseInterceptor` | stream block 必须协议终止；不可撤回历史 frame |
| tool | tool interceptor + execution observer | 默认 observe；execute 默认拒绝 |
| audit/durable | `analysis_events` worker | outbox/worker，不能只用 MemoryBus |
| session close | `SessionCloseHook` | 异步、可重试、不阻断主请求 |

**退出条件**：未启用 binding 时旧路径行为不变；插件失败行为符合每个 binding 的显式 policy。

---

## Wave 2：GoalRun 持久账本与请求级激活

### W2-A：Schema、Store 与 Outbox

**目标**：增加 `goal_runs`、`goal_run_steps`、`goal_run_actions` 或等价表/迁移/Repository。

**约束**：

- tenant 过滤和 RLS 与 durable/confirmation 同级；所有 action 有 idempotency key、causation ID、expected version。
- 创建 successor 使用 CAS/lease；序号单调。
- action/outbox 和 projection 可重放；不得保存 credential、raw confirmation token 或超出 ADR-0001 约束的明文内容。
- migration 必须有 up/reapply/down、checksum/ledger、真实 PG 验证计划。

### W2-B：Goal request 与 session 初始化

**目标**：解析 versioned `goal` 对象和兼容 header，原子/幂等创建 GoalRun、Goal session、根 step；将服务端收紧后的 policy snapshot 回给客户端。

**接入边界**：body 规范化、认证、tenant/session ownership 完成后；durable snapshot cut point 前。未知 version、租户/API-key 不匹配、非法 limits 或未声明 capability 要 fail-closed。

### W2-C：Status API 与 pending projection

**目标**：新增稳定 `GET /v1/goal-runs/{id}` 或等价 session 子资源，并扩展 pending projection 的 run/task/sequence 关联。

**约束**：PG 为权威，Redis 为 projection；状态 API 要 tenant/session ownership；durable 202、handoff 202、approval 202 语义严格隔离。

### 验收

- 新会话 Goal 请求幂等、授权隔离、旧请求兼容。
- root request/run/step/task 的关联可查询。
- PG/Redis 不可用分别符合已定义 fail-open/fail-closed。

---

## Wave 3：Continuation、durable/survival 与重启恢复

### W3-A：Continuation scheduler

**目标**：替代进程内 `go injectFollowUpRequest` 作为连续 Goal 的权威排程路径。

**规则**：

- action 在 DB 落库后由 worker claim；一个 run 同时只有一个有效 successor。
- parent/child request、action、policy、budget 和 sequence 持久化。
- 原协议 shape 通过 IR/协议 adapter 保留；不得固定降级为 Chat Completions。
- cancel、terminal、lease lost、budget exhausted 均阻止新 successor。

### W3-B：Durable/survival adapter

**目标**：只在 `none/metadata` commit state 使用自动 recovery；重用 durable Store、lease、fencing、deadline、attempt budget 与 pending outbox。

**规则**：

- content/tool/side-effect checkpoint 后写 `resume_safety_blocked` 或 `manual_required`，绝不重放。
- worker 每次执行前重新验证 API key、tenant、session、policy、candidate；不持久化 credential。
- recovery policy 只能输出 `retry_now`、`wait_recovery`、`durable_async`、`resume_blocked`、`terminal`、`manual_required`。

### W3-B1：Settlement intent 可靠性补强

**目标**：统一前台和 RecoveryWorker 对 terminal settlement intent 的有界持久化重试，减少“结果已经生成但 intent 未落库”导致的轮询结果丢失或不必要重执行。

**规则**：

- 抽取共享的 `PersistSettlementIntent` 重试策略：在 detached settlement deadline 内指数退避；`ErrLeaseLost` 立即停止，不能盲目重试。
- 前台 `ReleaseToWorker` 的 `Reschedule` 使用同样有界重试，临时失败后尽力立即移交；耗尽后保留 lease-expiry claim 兜底，不得直接丢弃 task。
- `FinalizeSettlement` 已有 intent-outbox repair 时，只能 drain/finalize intent，**不得重放 upstream**。
- `CheckpointCommitState` 仍是 write-ahead fail-closed gate：checkpoint 失败不发送语义 frame，不能用普通重试或继续发流来“修复”。
- 如果数据库持续不可用导致 intent 在完整 settlement deadline 内仍无法写入，记录明确的 durable-result-loss 风险并交给 safety reaper/人工处置；若产品要求此场景仍可恢复 result body，必须另立 WAL/queue 设计，不能伪称当前 task 表可保证。

**测试**：前台 `ReleaseToWorker` transient retry 成功与 `ErrLeaseLost` 单次退出；worker intent 前两次失败、后续成功；intent 已落库但 finalize 失败后 worker repair 且不重放；intent 重试耗尽后已 checkpoint task 只能进入 safety block，未 checkpoint task 只能走既有 lease/deadline 恢复语义。

### W3-C：故障注入

覆盖 Create/Claim/Reschedule/Checkpoint/Settlement、gateway/worker crash、lease expiry/fencing、Redis flush/PG fallback、cancel race、deadline、101-attempt 上限、goroutine leak。

### 验收

真实 PG/Redis 隔离测试中，重启/多 worker 下不重复执行、终态不可覆盖、不可重放边界可靠。

---

## Wave 4：Handoff、审计、修复与模型策略

### W4-A：持久 handoff reservation/restore

替换或旁路 `MemoryHandoffTrigger` 的 proposal binding、reservation、debounce/ack 临时状态；保留显式 confirmation 的 one-time capability、accounting-first、restore retry、manual-required 语义。

### W4-B：GoalState v2

决定并实现 KMS envelope encryption 或经产品批准的字段收窄；在此之前不得扩大 GoalState 明文字段。目标 session 冲突、未知 version、损坏 snapshot、tenant mismatch 必须人工升级。

### W4-C：Audit/repair/verify 编排

- Goal completion candidate -> audit action -> `audit_degraded` / verified completion。
- audit 只写一次；非法 JSON、低置信或 history 不可用不能默认通过。
- repair 只创建建议或 approval-required action；不自动提交、合并、推送、部署、迁移或写外部系统。

### W4-D：Model fallback policy

Goal loop model switch 与 dispatch fallback 分别计数。复用 dispatch/model policy 的 capability、task、工具、format、cost、IQ 与 tried-model filter；只在 semantic output 前透明切换。

---

## Wave 5：流式、工具与协议验证

### W5-A：stream termination

将 chunk block 扩展为取消上游、正确协议终态、frame 计数审计和不可重放声明；覆盖 OpenAI Chat、Responses、Anthropic、Gemini bridge。

### W5-B：tool/approval resume

实现或明确拒绝 `waiting_tool` 的可信执行器协议。客户端 tool call 默认等待客户端 result；gateway execute 仅限 allowlist、幂等、审计和 approval 完整闭环。

### W5-C：端到端矩阵

覆盖 continuous Goal：root request -> durable 202 -> worker -> successor -> complete -> audit -> status/pending；包括 stream、tool、handoff、approval、model switch、cancel 和 terminal races。

---

## Wave 6：灰度、canary 与发布门禁

### 灰度阶梯

```text
observe -> recommend -> act-recovery -> act-handoff -> act-fallback -> audit-orchestrated
```

- 默认关闭；tenant allowlist；每一层有独立 kill switch 和审计。
- 首先在隔离 PG/Redis + 单 tenant/model/credential 环境运行。
- 连续 24 小时观察 action duplicate、lease lost、safety block、restore pending、fallback rejection、token/cost 与 worker backlog。
- 245 canary 当前 audit/URSM/routing resolve 尚未完整验证，不能作为本项目全量或 154 发布证据。

### 立即 NO-GO

- scope leakage、重复 provider/tool 执行、content/tool checkpoint 后 replay；
- 无法解释的 model switch、terminal 覆盖、outbox 无界积压、panic/OOM；
- 未授权生产 action、缺少 rollback 或缺少隔离依赖。

---

## 依赖图与并行方式

```text
W0-A/B/C/D
   |      \
   v       -> W1-A -> W1-B
W2-A -> W2-B -> W2-C
   |                 \
   v                  -> W3-A -> W3-B -> W3-C
W4-A/B/C/D ------------------------------/
                           |
                           v
                       W5-A/B/C
                           |
                           v
                         W6
```

| 组别 | 可并行起点 | 合流条件 |
|---|---|---|
| 设计/安全 | W0-A/B/C/D | 契约和 DTO/能力结论评审完毕 |
| 插件运行时 | W1-A | W0-B/D 完成 |
| GoalRun 数据 | W2-A | W0-A/C 完成 |
| 恢复 | W3-C 测试框架可先行 | W2-A/B、durable adapter 完成 |
| Handoff/audit/model | W4 设计可先行 | W2/W3 事件/ledger 接口冻结 |
| 验证/运维 | W5 设计可先行 | 对应实现完成；真实依赖和授权到位 |

---

## 通用 agent 交付契约

每个 agent：

1. 使用独立 worktree/branch；只修改任务允许文件。
2. 不修改主工作树协作者的未提交文件；不 reset/restore/stash/clean；不执行生产操作。
3. 不自行 commit/push/merge；输出 diff stat、完整 `git status --short`、测试命令与结果、未验证项和阻塞。
4. 遇到跨工作包依赖、真实依赖缺失、权限不明、风险超过范围时停在 `manual_required`，生成 handoff，而不是猜测、绕过或伪造成功。
5. 输出中不包含密钥、DSN、token、cookie、用户原文或生产敏感数据。
