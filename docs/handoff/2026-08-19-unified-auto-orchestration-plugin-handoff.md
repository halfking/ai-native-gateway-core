# Unified Auto-Orchestration Plugin Handoff

## 1. Mission Summary

Design and implement a unified **Auto-Orchestration Plugin** that controls a Goal-enabled conversation across request, session, tool, response/stream, audit, durable recovery and handoff boundaries. It must observe lifecycle facts and create bounded, durable actions without bypassing dispatch, durable task ownership, approval, explicit handoff confirmation, or session persistence.

The intended end state is: a client explicitly enables continuous Goal mode for a new session; the gateway creates a durable `GoalRun`; the orchestrator safely continues only eligible steps, waits for tool/user/handoff input when required, audits verified completion, and exposes durable status/result APIs. It must stop at `manual_required` rather than fabricate success or perform privileged external actions.

## 2. Current Approach / Plan

The authoritative design is:

- [Unified design contract](../03-design/02-feature-design/会话优化v4/13-统一自动编排插件与Goal会话控制设计.md)
- [Execution plan](../04-implementation/plan/2026-08-19-unified-auto-orchestration-plugin-execution-plan.md)
- [Read-only audit](../audit/2026-08-19-unified-auto-orchestration-plugin-audit.md)

Architecture decisions:

1. The orchestrator is a **decision/action layer**, not a second provider executor.
2. Existing owners remain authoritative: Goal session, durable task, handoff confirmation, approval, dispatch and Session V2 each retain their own state and terminal semantics.
3. `GoalRun` is a new persistent coordination ledger with steps/actions, policy snapshot, lease/CAS/fencing and a durable parent/child request sequence.
4. A `PluginBindingRegistry` bridges external/in-process plugins to existing request pipeline, response interceptor, tool interceptor, analysis event worker and session-close seam.
5. Goal mode must be explicitly requested at session creation or first request; response keyword activation is not sufficient for continuous Goal.
6. Content/tool/side-effect semantic checkpoints are non-replayable. Automatic recovery is only valid before those boundaries.
7. Handoff remains explicit by default. Approval, tool execution, code changes, commits, merge/push, deployment, migration and privileged external side effects are not automatically authorized.

## 3. Progress

- ✅ Completed: architecture, requirements, safety boundary, state machine and protocol design.
- ✅ Completed: current-code audit of Goal, pipeline, response/stream, tool, plugin-runtime, durable, pending and handoff paths.
- ✅ Completed: phased execution plan with dependencies, tests, release gates and agent isolation rules.
- 🚧 In progress: implementation has **not started**. No GoalRun schema, binding registry, scheduler or new API has been written.
- ⏳ Pending: Wave 0 through Wave 6 from the execution plan.

## 4. Current State

- Repository: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go`
- Branch at document-baseline capture: `main`
- Baseline commit captured while drafting: `8992aa0cf`
- Important: **always rerun `git status --short`, `git rev-parse HEAD`, `git branch --show-current`, and `git reflog -5 --oneline` before work.** The repository is active and history changed during prior analysis.
- Documentation added by this task:
  - `docs/03-design/02-feature-design/会话优化v4/13-统一自动编排插件与Goal会话控制设计.md`
  - `docs/04-implementation/plan/2026-08-19-unified-auto-orchestration-plugin-execution-plan.md`
  - `docs/audit/2026-08-19-unified-auto-orchestration-plugin-audit.md`
  - `docs/handoff/2026-08-19-unified-auto-orchestration-plugin-handoff.md`
- Existing worktree changes belong to other work. At baseline they included version metadata and Dashboard files. Do not alter, stage, stash, reset, restore or clean them.
- Code snapshot: **uncommitted documentation task**. Do not assume this handoff document or other new docs are committed/pushed.

## 5. Next Steps

1. Run Wave 0 as read-only work in isolated worktrees and freeze the data/state/protocol/security contracts before code changes.
2. Implement Wave 1 and Wave 2 independently after their contracts are accepted: plugin binding security and GoalRun durable ledger/request activation.
3. Implement Wave 3 continuation/recovery only after GoalRun CAS/outbox and protocol preservation are in place.
4. Implement handoff persistence, audit/model policy and stream/tool controls in Wave 4/5, always keeping explicit approval and no-replay boundaries.
5. Use isolated real PG/Redis/plugin/provider tests for Wave 6. Do not use 245/154/252 as an isolation environment and do not deploy without separate approval.

## 6. Key Facts

### 6.1 Current integration seams

| Concern | Existing seam | Planned use |
|---|---|---|
| Request/governance | `domains/pipeline` and v1 `ChatHandler` | request observation, block/suspend, bounded rewrite |
| Response/stream | `domains/hooks/response`, `interceptingStreamWriter` | response observation, frame-time deterministic control, continuation decisions |
| Tool | tool registry/interceptor and tool execution tracker | default wait/observe; explicit allowlist only for execution |
| Durable | `durable` Store, recovery worker, commit gate | recovery/claim/lease/fencing and no-replay boundary |
| Async events | `analysis_events` publisher/worker | durable orchestration event/action processing |
| Handoff | request proposal/confirmation/restore | explicit cross-session transition |
| Audit | Goal audit hook/history store | verified completion and `audit_degraded` handling |
| External plugin | `plugin-runtime` | supervisor/manifest only until binding/capability/sandbox are implemented |

### 6.2 Non-negotiable safety rules

- No automatic provider call outside dispatch/Executor/survival/durable owners.
- No automatic handoff confirmation or silent session rotation.
- No automatic replay after content, tool call or side-effect checkpoint.
- No assumption that `stop` or no `tools` means task completion.
- No automatic client tool execution; `tool_calls` normally means `waiting_tool`.
- No use of post-response interception as a claim that client-visible output was redacted or blocked.
- No auto-approval after timeout for high-impact actions.
- No automatic audit repair, commit, merge, push, deployment, migration, credential change or production cleanup.
- No full gateway environment, DSN, credentials, raw authorization or cross-tenant data passed to plugins.

### 6.3 Evidence and release rule

Use these exact status levels in reports:

```text
IMPLEMENTED != LOCAL_VERIFIED != REAL_DEPENDENCY_VERIFIED != RELEASE_READY
```

Mock tests do not prove real PostgreSQL/Redis/provider/plugin behavior. 245 canary still lacks full audit/URSM/routing-resolve evidence and cannot be used as a release proof for this initiative or for 154.

## 7. Blockers / Risks

1. `plugin-runtime` currently lacks binding registration, capability enforcement, ready-after-handshake semantics and meaningful sandboxing.
2. Existing Goal follow-up is process-local and Chat-only; it is not a durable multi-protocol scheduler.
3. GoalState is redacted/size-limited but not encrypted at rest; ADR-0001 remains binding until KMS or approved field narrowing exists.
4. Real PG/RLS, Redis recovery, provider behavior, plugin isolation and long-running worker failure modes require dedicated isolated dependencies.
5. Production operation requires separate authorization. Do not blur code implementation with 245 restart-loop remediation or canary operational work.

## 8. Durable / Goal Checkpoint

| Item | Required next implementation state |
|---|---|
| Goal activation | Explicit versioned request `goal` object, server-side policy snapshot |
| Coordination SSoT | `goal_runs`, steps and action/outbox ledger; Goal session remains contextual state |
| Successor | DB CAS/lease + monotonic sequence; never process-local map only |
| Recovery | only `none`/`metadata` checkpoint; revalidate policy/key/session/candidate |
| Settlement | persist terminal intent with shared bounded, lease-aware retry; intent repair must finalize only, never replay upstream |
| Content/tool checkpoint | `resume_safety_blocked` or `manual_required`; never transparent replay |
| Completion | evidence + confidence + audit; not keyword/no-tools alone |
| Tool state | waiting/approval/result protocol; not synthetic continue |
| Handoff | explicit proposal/confirmation across sessions; preserve 12-contract semantics |
| Delivery | durable 202 + GoalRun status API + pending result projection; keep 202 contracts separate |

## 9. References

- `docs/03-design/02-feature-design/会话优化v4/12-GoalHandoff契约.md`
- `docs/03-design/02-feature-design/会话优化v4/13-统一自动编排插件与Goal会话控制设计.md`
- `docs/04-implementation/plan/2026-08-19-unified-auto-orchestration-plugin-execution-plan.md`
- `docs/audit/2026-08-19-unified-auto-orchestration-plugin-audit.md`
- `docs/adr/ADR-0001-handoff-goal-state-at-rest-encryption.md`
- `docs/handoff/2026-08-19-canary-runbook.md`
- `docs/handoff/2026-08-19-canary-evidence-and-status.md`
- `docs/session-logs/2026/08/2026-08-19-245-restart-loop-followup.md`

---

## 10. Batch Execution Prompts

### 10.1 New-session Goal-mode master prompt

Copy the entire block into a new session after the documents are committed or otherwise made available in that session.

```markdown
## 🔄 上下文交接完成

**继续会话：统一自动编排插件与 Goal 会话控制实施**

项目：`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go`

先读取：
1. `docs/handoff/2026-08-19-unified-auto-orchestration-plugin-handoff.md`
2. `docs/03-design/02-feature-design/会话优化v4/13-统一自动编排插件与Goal会话控制设计.md`
3. `docs/04-implementation/plan/2026-08-19-unified-auto-orchestration-plugin-execution-plan.md`
4. `docs/audit/2026-08-19-unified-auto-orchestration-plugin-audit.md`

以 Goal continuous 模式编排本项目：持续推进 Wave 0 到 Wave 6；每一波完成后根据验收证据决定下一波。只有在任务的文件范围、测试和依赖都满足时才继续；遇到真实 PG/Redis/provider/plugin 依赖缺失、生产授权不足、跨工作包冲突或任何不可逆操作时，写入 `manual_required` 和新的 handoff，不得猜测成功或绕过限制。

强制规则：
- 首先执行 `git status --short`、`git rev-parse HEAD`、`git branch --show-current`、`git reflog -5 --oneline`，不要依赖旧 SHA。
- 主工作树可能有协作者未提交修改。绝不 reset/restore/checkout/stash/clean，绝不 `git add -A`。
- 每个子代理必须独立 worktree/branch，明确允许文件；不得自行 commit/push/merge。
- 不执行生产 DDL、cleanup、部署、SSH、systemd、重启、凭据或模型 policy 修改。245/154/252 不可作为隔离 canary。
- 不自动执行 tool side effect、approval、handoff confirmation、commit、merge、push、deployment 或 audit repair。
- 使用状态：IMPLEMENTED、LOCAL_VERIFIED、REAL_DEPENDENCY_VERIFIED、RELEASE_READY；不能跨级宣称。
- 每个工作包输出：文件 diff/stat、测试命令和结果、证据等级、未验证项、风险/阻塞、下一步建议。

执行顺序：Wave 0 先冻结契约；Wave 1 和 Wave 2 可在契约批准后并行；Wave 3 依赖 Wave 2；Wave 4/5 依赖相关 ledger/action 接口；Wave 6 仅在隔离真实依赖和授权完备时启动。
```

### 10.2 Wave A — contract and safety review (parallel, read-only)

```markdown
任务：Wave A / W0-A 至 W0-D。仅只读审查统一自动编排插件的领域状态机、事件 envelope、插件 capability/DTO/failure policy、Goal/durable/pending/handoff/approval 协议隔离和安全边界。

工作目录：为此任务建立独立 worktree 和分支；不要改主工作树。
允许修改：无。允许新增：仅在被主协调者明确要求时新增设计补充文档。
禁止：任何生产操作、任何代码/迁移改动、commit/push/merge、reset/checkout/stash/clean、读取或输出 secret/DSN/token。

必读：
- `docs/03-design/02-feature-design/会话优化v4/13-统一自动编排插件与Goal会话控制设计.md`
- `docs/04-implementation/plan/2026-08-19-unified-auto-orchestration-plugin-execution-plan.md`
- `docs/audit/2026-08-19-unified-auto-orchestration-plugin-audit.md`

交付：状态迁移矩阵、owner 边界、DTO 白名单、capability 与 failure policy 表、202 协议矩阵、file:line 证据、发现的矛盾/未决问题。标明 S1/S2/S3/S4 证据等级。不要把建议表述为已实现。
```

### 10.3 Wave B — plugin binding runtime (after Wave A)

```markdown
任务：实现 W1-A/W1-B：PluginBindingRegistry、manifest capability enforcement、handshake/health ready gate、受限 DTO 和 pipeline/response/tool/analysis/session-close adapter 桥。

前置：Wave A 的 capability、DTO、failure policy 和 binding phase 已冻结；先读取最新 handoff 和所有设计文档。
隔离：独立 worktree/branch；只修改分配范围。
允许文件：`plugin-runtime/**`、`cmd/gateway/plugin_*`、新增受限 adapter 包、对应单元/集成测试；如需调整 `domain/request_envelope.go`，必须先在设计中登记 metadata ownership。
禁止文件：GoalRun schema/store、durable owner、handoff confirmation、session-v2 persistence owner、部署/迁移/前端/版本文件。
不可变约束：不传完整环境/DSN/credential；data-plane write capability 在 sandbox 前拒绝；未签名/未握手/未健康/无 capability 的 plugin 不得 ready；并行 Metadata 不可无锁写。
测试：manifest/binding validation、capability deny、handshake/health failure、scope、DTO redaction、timeout/circuit breaker、legacy path no-op。
交付：diff/stat、测试命令/结果、证据等级、未完成 sandbox/真实 plugin 测试、阻塞。不得 commit/push/merge。
```

### 10.4 Wave C — GoalRun ledger and request activation (after Wave A; parallel with Wave B)

```markdown
任务：实现 W2-A/W2-B/W2-C：GoalRun schema/store/action outbox、显式版本化 Goal request、session/GoalRun 幂等初始化、GoalRun status API 与 pending projection。

前置：Wave A 已冻结 GoalRun state、202 protocol、policy snapshot 和 tenant/security rules。
隔离：独立 worktree/branch。
允许文件：新的 `domains/goalrun/**` 或等价包、对应 SQL migration/index/test、目标 session/streaming handler 的最小 request parsing/API wiring、pending projection 代码与测试。
禁止文件：plugin-runtime、dispatch algorithm、provider credential persistence、handoff confirmation owner、生产部署脚本、前端、版本元数据。不要修改无关未提交文件。
不可变约束：PG 是权威；Redis 仅 projection；tenant/API-key/session ownership 每次校验；action 有 idempotency/CAS/lease/sequence；不保存 credential/token/raw confirmation；Goal 限制只能被服务端收紧；旧请求兼容。
测试：schema up/reapply/down/ledger、Goal request schema/fuzz、idempotency、tenant isolation、GoalRun/step chain、pending/status headers/body/protocol separation、PG fallback 设计测试。
交付：diff/stat、测试命令/结果、迁移风险、真实 PG/RLS 未验证项。不得 apply migration、commit/push/merge。
```

### 10.5 Wave D — continuation and durable recovery (after Wave C)

```markdown
任务：实现 W3-A/W3-B/W3-C：持久 continuation scheduler、GoalRun action claim、durable/survival adapter、checkpoint/lease/fencing/restart fault injection。

前置：GoalRun ledger/outbox 和 request/status contract 已合入或提供稳定接口。
隔离：独立 worktree/branch。
允许文件：`domains/goalrun/**` scheduler/worker、`durable/**` 的最小 adapter、`domains/streaming/durable_*`/survival integration、仅相关测试。
禁止：直接 provider 调用旁路、修改 credentials、跨协议固定降级为 Chat、handoff confirmation 自动化、工具自动执行、部署/生产操作。
不可变约束：只有 none/metadata commit 可恢复；content/tool/side-effect 后必须 `resume_safety_blocked` 或 `manual_required`；每 run 单 successor；cancel/terminal sticky；worker 重新验证 key/tenant/session/policy/candidate；不得持久 credential；101 次 attempt 上限保持。
测试：crash at create/claim/reschedule/checkpoint/settlement，lease expiry/fencing，Redis flush/PG fallback，gateway/worker restart，cancel race，deadline/budget，goroutine leak，多 worker no duplicate。
交付：diff/stat、测试命令/结果、不可验证的真实依赖、风险。不得 commit/push/merge。
```

### 10.6 Wave E — handoff, audit, fallback and tools (after Wave C/D interfaces)

```markdown
任务：实现 W4-A 至 W5-B：持久 handoff reservation/restore、GoalState v2 决策支撑、audit/repair/verify orchestration、model fallback policy 协同、stream termination 和 waiting_tool/approval 边界。

前置：GoalRun event/action interfaces 已冻结；先区分同 session continuation 和跨 session explicit handoff。
隔离：独立 worktree/branch。
允许文件：`domains/hooks/handoff/**`、`domains/hooks/goal/**`、`domains/hooks/response/**`、相关 streaming/tool/audit/model-policy adapter 与测试；GoalState schema 只能在明确 ADR 决策后改动。
禁止：自动 confirmation、timeout auto-approve、tool execute 默认开启、audit repair 直接写代码/部署、KMS/secret 伪实现、生产 migration/cleanup。
不可变约束：handoff accounting-first/restore-retry/manual-required；audit parse failure/低置信为 audit_degraded；Goal loop switch 与 dispatch switch 分离计数；stream block 必须终止上游和写协议终态；已提交语义不 replay。
测试：confirmation replay/expiry/restore conflict、audit single-winner/degraded、fallback compatibility/attempt cap、tool call waiting、stream terminal protocol、protocol bridge matrix。
交付：diff/stat、测试命令/结果、ADR/产品决策阻塞、未验证项。不得 commit/push/merge。
```

### 10.7 Wave F — real dependency verification and release gates (last)

```markdown
任务：执行 W6 的隔离真实依赖验证和 release-gate 文档更新。此任务默认只读/隔离验证；任何生产操作必须停止并请求书面授权。

前置：所有对应实现已通过本地测试并有清晰变更集；ops 已提供独立 PG、Redis、tenant、credential、model allowlist、key prefix 和隔离 token。
隔离：不得使用 245/154/252 作为隔离 canary；不得修改主工作树协作文件。
允许：隔离环境 setup/验证脚本、runbook evidence、只读指标/日志；生产目标仅可做已授权只读检查。
禁止：生产 DDL、cleanup、deploy、SSH、systemd、restart、credential 操作、push/merge；没有 rollback/owner/window 时不得继续。
验证：Goal root -> durable 202 -> worker -> successor -> completion -> audit -> status/pending；restarts/lease/fencing/Redis fallback；tool/handoff/approval boundary；24h observation；kill switch/rollback rehearsal。
NO-GO：scope leak、duplicate execution、semantic replay、unexpected model switch、terminal overwrite、unbounded backlog、panic/OOM、缺少真实依赖或授权。
交付：证据等级、命令/结果摘要、指标时间窗、GO/NO-GO、风险、回滚状态、下一 handoff。不得宣称 RELEASE_READY，除非所有门禁证据和审批完整。
```
