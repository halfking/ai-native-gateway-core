---
title: 后端并发架构交接
date: 2026-08-25
---

# Handoff — 后端并发架构问题梳理（2026-08-25）

**分支**：`main` @ 合并 `54315836b`（fix/sessionmeta-json-audit）
**范围**：并发限流（手动/自动）、三层 FIFO 队列、指纹槽位、五层信号量——现状、已知问题与遗留风险

---

## 1. 三层队列架构（dispatch v2, `domains/dispatch/`）

队列**不按节点组织**，只有全局/按模型/按凭据三层，均为有界 channel FIFO：

| 层 | 实现 | 默认容量 | 满时行为 |
|---|---|---|---|
| Tier-0 全局 | `totalExecutionQueue` (`total_queue.go`) | 1000（热配置 `llmgw_dispatch_total_queue_capacity`） | 立即 `OverflowError{total_queue_full}` |
| Tier-1 按模型 | `modelQueue` (`snapshot.go`) | 300（`llmgw_dispatch_max_queue_depth`） | 同上 |
| Tier-2 按凭据 | `credForwarder.queue` (`forwarder.go:19-36`) | 300 或凭据级 `max_queue_depth` | 同上 |

v4 R1.3 起为**零等待准入**（`MaxQueueWaitMS=0`）：队满即拒，不排队等待。Tier-1 由 `runModelDrainer` 排干到共享 `dispatchIn`（容量 = DispatcherWorkers×4，workers=8）。

**要点**：`DispatcherWorkers`/`FailoverWorkers` 固定 8/8（`config.go`），非自适应；Tier-0 语义是"全系统背压总闸"，不区分模型冷热。

---

## 2. 手动 / 自动并发

- 存储：`credentials.concurrency_limit`（手动硬上限）与 `concurrency_limit_auto`（自动调优值），schema 见 `db/db.go:2261-2317`（auto 回填 `COALESCE(concurrency_limit,5)`）。
- **自动降**：`credentialhealth/tuner.go:78` `OnError` 仅对 503（`KindConcurrent`）降并发（`decreaseConcurrency`:101）；429 故意不在此处理（由内存态 `Limiter.Shrink` 处理，tuner.go:63-77 P2-7 注释）。
- **自动升**：`bg/concurrency_auto_scaleup.go:90`，小时级 worker；准入条件：≥60 调用/时、成功率 ≥95%、零 429/503、上限 <50，每次 +1。
- **手动覆盖**：`admin/credential_monitor.go:892` `handleSetConcurrencyAuto` 直接写 auto 列。
- 关系：**auto 优先但永不超手动硬上限**（见 §4 effective_concurrency）。

前端统一编辑器（手动/自动/指纹槽位三字段一次保存）见 `docs/changelogs/2026-08-24-credential-concurrency-unified-editor.md`；本次已随 `54315836b` 合入 main（`NodeDetailConcurrencyPanel.vue`：三字段组合变更会同时调 PATCH + setConcurrencyAuto，不再互相覆盖）。

---

## 3. 指纹槽位（fp_slot_limit）——与并发是两个正交概念

- 列：`credentials.fp_slot_limit`（INT NOT NULL DEFAULT 20，CHECK 0..10000，`db/db.go:2321-2410`）。
- 语义（`credentialfpslot/slot.go:218-225` 注释明确）：`concurrency_limit` = 最大**在途请求数**；`fp_slot_limit` = 最大** distinct 用户身份数**。DB 约束 `credentials_fp_slot_vs_concurrency` 强制 fp_slot ≤ concurrency。
- 执行：`Manager.Acquire`（`slot.go:337`）Redis 虚拟槽池（槽 TTL 30min、会话钉住 24h、active-gate 抢占 5min、`reclaim.go` 后台回收），返回三态 OK/saturated/redis_error。
- 调用点：`domains/streaming/executors/executor_dispatch.go:474-488`——饱和是**降级**而非失败（`running without slot`，`fpSlotDegraded=true`）；租约在 deferred cleanup 释放（:538）。
- ⚠️ 遗留：旧 `EffectiveLimit`（slot.go ~250）仍把两者混为一谈，正确映射是 `EffectiveFpSlotLimit`（:233）——迁移调用方时注意别用错。

---

## 4. effective_concurrency——**SQL 与 Go 优先级不一致（现存风险）**

| 位置 | 逻辑 | 优先级 |
|---|---|---|
| SQL（`admin/credential_monitor.go:287,350`） | `COALESCE(manual, auto, 5)` | **手动优先** |
| Go（`domains/providerprofile/adapters.go:339-350` `GetModelScale`） | eff=auto；auto≤0 用 manual；manual>0 则 clamp ≤ manual | **自动优先、手动封顶** |
| Go（`provider/client.go:1587-1622` `applyCapacityWeightedLB`） | 同上，再 clamp 到 LB 权重区间 | 同上 |

两者在"manual 与 auto 同时设置且不等"时会给出**不同答案**：例如 manual=10、auto=8 时 SQL 显示 10，Go 实际执行 8。管理界面（走 SQL）展示的"生效并发"可能与运行时真实限流不符。代码中没有注释解释或调和该差异——**建议下一迭代统一为"auto 优先、manual 封顶"并让 SQL 对齐**。

---

## 5. 五层信号量 Limiter 与 Governor 的双层限流

- `domains/credential/limiter.go`：全局 1000 / pool 100 / 凭据 50 / identity 10(soft) / per-key(soft) 五层信号量，获取限时 5s（OPT-2，曾是无界等待钉死 executor 120s）。
- dispatch 路径走 `AcquireAllNoCredLayer`（`limiter.go:563`）**跳过凭据层**，因为凭据层限流由 per-credential Governor 承担，避免双重计数。
- Governor（`governor.go`）按 `credentials.concurrency_mode` 选择：`concurrencyGovernor`（在途信号量）/ `rpmGovernor`（令牌桶）/ `tpmGovernor`（按预估 token 计费，默认 800）/ `noopGovernor`。
- ⚠️ `governor_snapshot.go:78`：快照校验失败直接 **panic**（"no graceful path"）——坏数据会把整个 gateway 打挂，考虑改为 reject+log。

---

## 6. 历史修复记录（防复发）

无未关闭的 TODO/FIXME；已修复缺陷记录在 `CHANGELOG.md` 与 `.audit/`，关键的几个：

1. **CredEnqueuedAt 数据竞争**（2026-08-13）：`tryEnqueueCred` 在 channel send **之后**赋值，与 forwarder loop 竞争——已改为 send 前赋值。教训：**入队元数据必须在 send 前写完**。
2. **Tier-2 取消不排干**：forwarder `loop()` 在 ctx cancel 时不排干 `cf.queue`，已排队请求永不 complete，Submit 调用方永久阻塞——已加 `drainAndComplete()`。
3. **shutdown 丢在途请求**：`routeFailover` stopCh 分支不调 `complete(qr)`——已用 `ErrShutdown` 补全。
4. **fpslot Redis 错误误报为饱和**：曾导致误降级——已改三态 `acquireOutcome`（`slot.go:370-380`）。
5. **Release 泄漏槽位**：Release 失败的槽要等 30min TTL，曾造成 `cred_fp_slot saturated` 事故——已加有界重试（`slot.go:396-403`，3 次、detached context）。
6. **scaleup 静默无效**：`avg_concurrent` 恒 NULL 使自动升并发匹配零凭据（P1-5）——已修；`Stop()` double-close panic 已用 `sync.Once` 修（`concurrency_auto_scaleup.go:21-24`）。
7. **TPM Lua ZSET 成员冲突**：导致超收——已修。
8. **压力缓存陷阱**：`limiter.go:749-756` 曾有 5s TTL 压力缓存，因 Release 不失效被移除（2026-07-27）——**注释明确警告不要重新引入**。
9. **shutdown 顺序**：model-queue channel **不可 close**（sender 竞争，`pipeline.go` ~711）；snapshot observer 必须先于清空 forwarders 停止（`Stop`，~706）。
10. **ADR-Disp-003**：首字节已发出后**禁止跨凭据切换**。

---

## 7. 下一迭代建议（按优先级）

| # | 项 | 位置 | 说明 |
|---|---|---|---|
| P1 | 统一 effective_concurrency 语义 | `credential_monitor.go` SQL vs `adapters.go`/`client.go` | SQL 改为 auto 优先、manual 封顶，与运行时一致 |
| P2 | governor 快照 panic → 优雅拒绝 | `governor_snapshot.go:78` | 坏数据不应打挂 gateway |
| P3 | 清理 `EffectiveLimit` 遗留调用 | `credentialfpslot/slot.go` ~250 | 统一到 `EffectiveFpSlotLimit`，消除并发/槽位混淆 |
| P4 | DispatcherWorkers 自适应 | `config.go` | 固定 8 在大实例下可能成为吞吐瓶颈（需压测佐证） |
| P5 | Tier-0 不分模型的公平性 | `total_queue.go` | 单一全局队列下一个热模型可占满 1000 深度挤掉其他模型（设计取舍，待观察） |

---

## 关联文档

- `docs/changelogs/2026-08-24-credential-concurrency-unified-editor.md` — 前端统一编辑器
- `.audit/concurrency-audit-report.md`、`CHANGELOG.md` — 历史并发缺陷全记录
- `domains/dispatch/README.md` — ADR-Disp-003 等架构决策
