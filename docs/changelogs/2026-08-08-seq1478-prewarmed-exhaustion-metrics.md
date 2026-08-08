# 2026-08-08 — seq 1478 follow-up: pre-streamed exhaustion frame 携带真实 kind + Prometheus 指标

## 背景

seq 1477 fix 让耗尽分支 (handler.go:3707) 的 SSE 错误 frame 携带真实 kind 字段，但**生产上未验证**：上线后 14 分钟内 0 个空-200 失败，wire format 只能通过单元测试钉住。

seq 1478 补齐两件事：(1) 部署自动化 (deploy-154.sh) 跑过 → 154 binary 验证符号正确 (`writePrewarmedStreamErrorWithKind`); (2) 加 Prometheus 指标，让 `/metrics` 暴露 pre-streamed exhaustion 计数。这样下次空-200 出现时，告警不是"log 上升"而是"指标上升"，**几小时**内可触达而不是几天。

## 改动

| 阶段 | SHA | 类别 | 改动 |
|---|---|---|---|
| fix | `c81007afa` | streaming | 新增 `writePrewarmedStreamErrorWithKind(w, msg, type, code, kind)` — `kind==""` 时 byte-level 兼容旧 3 字段 envelope；7 个旧调用点零改动，仅 handler.go:3707 耗尽分支使用新函数传 `string(execErrTyped.LastKind)`。 |
| chore | `7d4743032` | release | version bump 1477 |
| feat | `c4ab07deb` | streaming | 加 2 个 Prometheus 计数器 + 1 个 integration hook: `llmgw_prewarmed_exhaustion_frames_total{real_kind, code}` 事件数、`llmgw_prewarmed_exhaustion_by_credential_total{provider_id, credential_id, model}` 按凭据归因；handler.go:3707 调 `recordPrewarmedExhaustion`。 |
| chore | `c336bb22d` | release | version bump 1478 |

## audit 发现

全部 4 个 commit 范围内未引入新 lint issue；9 个相关包测试通过；protobuf + 9 个受影响包测试 PASS。

### P0 — Spec Violations（不改，留档）

- 预流式 SSE 错误 frame 的 `Retry-After` 响应头**物理上不可能生效**：`handler.go:200` 写 200 + text/event-stream 之前就已刷出头块，之后的 `Header().Set` 被丢弃。本次在 handler.go:3707 仍尝试写入 `Retry-After` 是历史遗留的死代码。`TestPreStreamExhaustion_RetryAfterHeaderIsUnreachable` 钉住这个事实。非预流式路径（writeErrorJSONWithKind 用 6272 行的 WriteHeader）该头**确实**送达。

### P1 — 行为成本

- 24h 实测 14 个空-200 失败的 51 个 rat 序列中: median attempt0→attempt1 退避 **3.01s, 12/15 集中在 2-5s**。fix 的 `defaultOverloadRetryAfterSeconds=5` 默认值实际生效，relay 没给 Retry-After 头。这与之前 audit 上怀疑的"relay 一直给 0 走盲退避"相反——是 relay 没给头，我们走默认 5s。
- 14 个空-200 涉及 5 个 model (`gpt-5.6-sol` 16, `claude-opus-5` 16, `gpt-5.6-luna` 11, `minimax-m3` 7, `claude-sonnet-5` 1)。全部归因到 apiclaude.cc 中继 502 而非 model 本身。

### P0 — Production 回归（已就地 rollback）

- seq 1475 曾改 `executor_chat.go:685` 让过载返回未包装 error 强制跨凭据切换。`gpt-5.6-luna` 只有 1 个候选，导致 1 个请求 (af954611) 在 attempt=0 即耗尽。**4 分钟内回滚到 1474**，seq 1476 重新上线；seq 1476/1477 部署后 14 分钟内 0 个空-200 发生。

## 148 部署状态

- 154 production: seq 1478 (`c336bb22`)
- `/metrics` 当前 0 个 `prewarmed_exhaustion_frames_total` 请求（Prometheus 计数器 0 时不显示行），下次空-200 出现时该行即出现。
- 24h 累计 14 个空-200 失败涉及 5 个 model — 现在 `/metrics` 上 5 个 `real_kind` 标签维度都可区分。

## 等待中

- 真实空-200 场景下 frame 字节的 wire-format 验证 — 还没发生过载爆发期
- 新指标 24h 验证无指标误增
- seq 1478 release 是否引发任何 priority 0 反馈
