# 01 · 统一自检队列与 ProbeService 实施（2026-08-13）

本文档记录需求 6「审计整个自检模块」的完全统一实施，对应 `00-实时号池状态与统一自检探测方案.md` 的阶段 2。所有路径收敛到单一持久队列 `credential_probe_queue` + 单一执行拥有者 `ProbeService.Run`。

## 目标映射（需求 6 bullets）

| 需求 bullet | 实现 | 关键文件 |
|---|---|---|
| 1 对外增删自检 API | `POST/DELETE /api/admin/probe/tasks` | `admin/probe_dashboard.go`, `bg/probe_queue.go` (Cancel/BuildProbeDedupKey) |
| 2 自检事件规范 | `probe_command` 枚举 node_probe/integrity_verify/selfcheck/models_list；ProbeQueueTask 标准字段 | `bg/probe_queue.go`, `bg/probe_service.go` |
| 3 状态修改 + 延时回退 | `probe_revert_at` 列 + `bg/probe_rollback.go` ticker + `MarkTentativeRestore` | 迁移 `351_probe_revert_at.sql`, `bg/probe_rollback.go`, `bg/node_probe.go` (updateBindingAvailability 清 probe_revert_at) |
| 4 三队列管理 | ready/running/(success\|failed\|expired\|cancelled) 状态机；`FOR UPDATE SKIP LOCKED` + lease_token 原子 claim | `bg/probe_queue.go` |
| 5 不切模型/不切节点 | 网关轮带 `X-LLM-Pin-Credential` 硬过滤到目标凭据；自检只针对 (cred,model) | `bg/active_probe_executor.go` (RunGateway), `middleware/origin_mw.go`, `domains/streaming/executors/executor.go` (PinCredentialID) |
| 6 计划/定时入队 | `next_run_at` 未来 T → Claim 到点才执行；定时源不进待执行展示（生产者迁移后生效） | `bg/probe_queue.go` (Claim) |
| 7 完成入已完成队列 | Complete 写 terminal 状态 + node_probe_runs 审计 | `bg/probe_service.go`, `bg/probe_queue_worker.go` |
| 8 首页 SSE+Redis 大小形态 | `probeStreamStore.ts` 单例 EventSource，右追加+id 收敛 | `web/src/composables/probeStreamStore.ts`, `web/src/views/SelfCheckPanel.vue` |

## 架构

```
Submit(credID,model) ──► ProbeQueue.Enqueue(node_probe task, dedup_key=node_probe:<cred>:<model>)
                                    │
        credential_probe_queue (ready/running/.../cancelled, lease, dedup)
                                    │ Claim (SKIP LOCKED, next_run_at<=now())
                                    ▼
        ProbeQueueWorker.processTask ──► [node_probe] ProbeService.Run
                                    │       ├─ Round1 direct (probeDirect, proxy-aware)
                                    │       ├─ 状态恢复 (binding/health/observed, 清 probe_revert_at)
                                    │       ├─ Round2 gateway (executor.RunGateway, X-LLM-Pin-Credential)
                                    │       ├─ side-effects (circuit/cache/URSM/modelIQ/pg_notify)
                                    │       ├─ mirror node_probe_state (兼容桥)
                                    │       └─ insert node_probe_runs (审计)
                                    ▼ Complete(NextRunAt = 7步链)
        success → terminal success；failed & attempt<max → ready 就地 re-arm（7步链）
```

## 关键不变量与资源竞争处理

- **队列原子性**：统一仍用 `FOR UPDATE SKIP LOCKED` + `lease_token`，单一并发模型。dedup 部分唯一索引覆盖 ready/running，活跃窗口内重 Submit 保留既有回退（不塌陷）。
- **load-bearing 顺序**：direct 成功后、gateway 轮之前先恢复路由可见状态（`probe_service.go` Run，镜像自 `node_probe.go runOne`），避免「能用一下又 5xx」振荡。
- **延时回退确认竞态**：确认（probe 成功）与回退（ticker）写同一行 `probe_revert_at`；confirm 提交后回退 UPDATE 匹配 0 行（one-shot guard）。
- **回退链选择**：node_probe 任务用 7 步 `NodeProbeBackoffChain`（ProbeService 计算 NextRunAt，completeNodeProbe 保留）；integrity/selfcheck 用 5 步 `ActiveProbeBackoffChain`。
- **ProbeSync 同步快路径**：保留在 NodeProbeWorker（延迟敏感，不经队列），仅写 node_probe_runs 审计。
- **定针安全**：`X-LLM-Pin-Credential` 仅 system 调用方可用（OriginMiddleware 对非 system 剥离），dispatchRoute 硬过滤。

## kill-switch / 回退

- `LLM_GATEWAY_PROBE_QUEUE_ENABLED=false`：回退到 legacy `node_probe_state` 直跑路径（NodeProbeWorker.loop + runOne）。`SetProbeQueue(nil)` 即关闭队列模式。
- 前端 SSE 失败回落 REST 轮询（SelfCheckPanel 保留 15s poll）。

## 过渡说明（C11）

`node_probe_state` 由 `ProbeService.mirrorNodeProbeState` 镜像写入，作为兼容桥使既有 `/api/admin/probe/node-tasks` 面板与 `credential_recovery` 在统一期间继续可用。队列行是 dedup/lease/backoff 的真值源。完全切换仪表板读取到 `credential_probe_queue WHERE probe_command='node_probe'` 为后续清理项（不阻塞本次功能）。

## 迁移

- `sql/migrations/domain/351_probe_revert_at.sql`：`credential_model_bindings` + `credentials` 增加 `probe_revert_at` + 索引（幂等 `ADD COLUMN IF NOT EXISTS`）。
