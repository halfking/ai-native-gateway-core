---
archived_from: docs/2026-07-27-routing-state-m4-ops-checklist.md
archived_at: 2026-08-17
archived_by: docs-archive v1.0
backup_ts: 20260817-190606
status: archived
---

> 本文档已归档，原文保持不变。

# Routing-State M4 — Ops Canary Rollout Checklist

> 关联 spec: `docs/superpowers/specs/2026-07-27-routing-state-architecture.md` (M4)
> 关联审计: `docs/superpowers/specs/2026-07-27-request-flow-audit.md` (S-1/S-2)
>
> 这是**运维操作清单**，不是代码。M2/M3 代码已落地（NodeMirror LRU 接入热路径 +
> Ready 缓存 + routing_state_source 可观测）。M4 是把 `URSM_V2_MODE` 从 `off`
> 灰度切到 `authoritative`，根治多状态后端分裂脑（S-1/S-2）。需要部署窗口 +
> Grafana 监控，不能在代码分支里做。

## 前置条件（代码已就绪）

- [x] M1: `ur:*` 命名空间 + LRU/Sticky/Intent cache 层（已合并 origin/main）
- [x] M2: NodeMirror 接入 FilterAndScore 热路径 + fail-open（本分支）
- [x] M3a: Ready() 1s 缓存（S-4 解决）
- [x] M3b: routing_state_source=fallback 可观测（本分支，部分）
- [x] M3c: 8 个 feature flag 标记 Deprecated + 委托 legacyflags（已合并）

## Canary 阶段（245 先行，7 天）

部署 245: `URSM_V2_MODE=canary`（可加 `URSM_V2_CANARY_PERCENT=10` 起步）。

每日监控（Grafana 面板）：

- [ ] **P95 路由耗时** ≤ 1ms 增量（FilterAndScore 路径，含 LRU 命中）。超 1ms 自动回退 `off`。
- [ ] **fallback 占比** < 1%（routing_state_source=fallback 的日志占比）。**1min > 5% 触发 P3 告警**。
- [ ] **LRU hit rate** ≥ 80%（NodeMirror Get 命中 / FilterAndScore 调用）。
- [ ] **状态分裂告警**：抽查 `ur:cred:` 与 `llmgw:credstate:` / `llmgw:cred_fp_node:` 的 `disabled`/`cool_until_ms` 是否一致。canary 期双写并存，分歧应 < 1%。
- [ ] **503 / model_not_found 错误率** 无上升。
- [ ] **request_wal_hot 与 request_logs_hot 终态一致性**（L-2 守卫生效后，success 行不应被 disconnect probe 回退）。

7 天稳定 + 上述指标全绿 → 进入 authoritative。

## Authoritative 切换

部署 245: `URSM_V2_MODE=authoritative`。

- [ ] 切换后 **legacy writers 零调用**：grep 日志确认 `legacyWritersEnabled()` 返回 false（FpSlots Recorder / credentialstate Observer / routingstate Shadow 不再写）。此时 Ready() 走 1s 缓存（M3a），热路径 Redis 往返 ~1次/秒。
- [ ] **routing_state_source 分布**：应主要落在 `ursm_v2_redis` / `ursm_v2_lru`，`fallback` < 1%。
- [ ] 24h 观察 sticky 跨实例一致性（Redis 强一致，进程 LRU 仅读加速）。

## 154 同样流程

245 稳定 7 天后，154 重复 canary → authoritative。

## 回滚（任何阶段一键）

`URSM_V2_MODE=off`：旧路径（StateManager + DB-only + FpSlots）仍在 `_to-be-deprecated/` 编译通过，立即生效。sticky key 格式不变，跨 245/154 兼容。

## 物理清理（M4 收尾，7d 稳定后）

- [ ] `_to-be-deprecated/routingstate/`、`_to-be-deprecated/credentialstate/` 物理删除。
- [ ] `go build ./...` 仍通过（验收 #8）。
- [ ] `ur:` 与 `llmgw:credstate:` 旧前缀物理隔离，旧 key 7d TTL 自然过期。

## 未做完的代码 follow-up（非阻塞 M4）

- **routing_state_source 全链路传播**：当前只在 fail-open 日志暴露。完整方案是在 routing decision log 加字段，让每条决策行可查 source。需要改 router→logCtx 管道。
- **scoring 字段填充**（spec Task 14）：PipelineNodeViews 当前不读 `lat_ewma_ms`/`sr_5m`（record_request.lua 也不写）。LRU 已能缓存它们，等 store/lua 补齐后自动生效。M4 canary 期间评分用 BaseURLMs 兜底，不影响可用性。
