# 28 · SR-W3 并行撞车决策与 safety-reaper 审计修正

- 日期：2026-08-15
- 决策对象：`feat/m3-sr-w3-durable`（worktree `llm-gateway-go-sr-w3`，`ffb4184d..aa00bad7` 共 7 commits）与 main 上已合入的 `durable/` 包（`2d003750` Wave A + `9ab7acdf` doc 27 审计修正）
- 上游：doc 25 §4（CLEAN 撞车先例）、doc 26（协作者 Wave A 执行记录）、doc 27（协作者审计报告）

## 1. main 上的审计修正（本 commit）

**ReapUnsafeCheckpointed 缺 lease 护栏（严重，潜伏）**：原谓词只看
`commit_state IN ('content','tool_call','terminal')` + 非终态，不看 lease——
正在流式输出、已过语义检查点的**活跃前台持有者**（lease 未过期、持续续租）
会被立即终态化为 resume_safety_blocked，前台随后的 CommitTerminal 被 fence
拒绝，客户端拿到成功流但任务被记失败并向 PendingStore 投影失败。当前
Wave A 未接前台路径故未爆发，属潜伏缺陷；Wave B 前台接线一上即触发。

修复：谓词追加 `AND (lease_until IS NULL OR lease_until < $2)`（$2 = now），
仅收割已弃置（崩溃/断连后 lease 过期或 NULL）任务；终态转换留给活跃持有者
自己的 fenced 写。回归测试 `TestStore_ReapUnsafeCheckpointed_LiveLeaseGuard`
钉住谓词参数与零行行为。

同一缺陷在并行分支的 `ReapUnsafeCheckpoints` 中独立发现并修复
（`aa00bad7`，集成测试 `TestPGSemanticCheckpointProtectedFromDeadlineReaper`
含「活跃 lease 双 reaper 均不碰」断言），两边审计结论一致，互相印证。

## 2. 撞车决策

两条并行轨同日实现了 SR-W3 存储层，且都以 `durable_llm_tasks` /
`durable_llm_task_events` / `durable_pending_outbox` 为表名，schema 不兼容
（状态枚举 `failed/canceled` vs `permanent_failed/cancelled`、
`checkpoint_payload` vs `parent_request_id`+RLS FORCE、迁移号 516 vs 515）。
两套 migration 同时在库会使 `IF NOT EXISTS` 静默跳过建表、另一套的 DML
因列缺失而全线报错——**合并即损坏，不可同时保留**。

**决策（遵循 doc 25 §4 CLEAN 先例）：main 的 `durable/` + 迁移 516 为权威
存储层；`feat/m3-sr-w3-durable` 分支不合入 main，整体保留为 Wave B 移植
源。** 理由：

1. main 版本已被 doc 27 审计并修正（瞬态错误重排、Stop 有界等待、退避
   下限、事件时间一致性），且有 OBS 等后续提交叠在其上；
2. 两套存储层等价（Create/Claim/Renew/Checkpoint/Terminal/双 Reaper/快照/
   outbox 全齐），合并只制造冲突与双份维护；
3. 分支的独有增量（worker/投递器/请求接线/指标/集成测试 harness）依赖其
   自有存储层类型，直接合入编译不过；移植是明确有界的 Wave B 工作。

## 3. 能力对照与 Wave B 移植清单

| 能力 | main（durable/） | 分支（domains/durabletask/） | Wave B 动作 |
|---|---|---|---|
| 存储/claim/terminal/双 reaper/快照 | ✅（doc 27 审计） | ✅（另一套 schema） | 保留 main 版本 |
| safety reaper lease 护栏 | ✅ 本 commit 修复 | ✅ aa00bad7 修复 | — |
| recovery worker 循环（Executor 接口+Noop+续租+结果映射） | ❌ | ✅ `worker.go` | 移植到 `durable/` |
| outbox 投递循环（解密→ProjectCAS→退避） | 部分（`ProjectPendingOutbox` 内联） | ✅ `outbox.go` | 对照合并：主循环取两者之长 |
| 请求入口接线（Foreground+context+gate 检查点+前台续租+main 开关） | ❌ | ✅ `foreground.go`+`streaming/durable_wiring.go` | 移植，注意两点已修缺陷（§4） |
| `durable_*` 指标（active gauge/recovery_runs/projections/errors/lease_lost） | ❌ | ✅ `metrics/durable_metrics.go` | 随接线移植 |
| testcontainers 真 PG 集成测试 | ❌（pgxmock + migration 字符串检查） | ✅ `pg_integration_test.go` | 移植 harness 与三不变量用例到 516 |
| RLS 访问 | ENABLE（owner 绕过） | FORCE + bypassTx | 以 main 形态为准，不移植 FORCE |
| pending PG 回源 | ✅（main 版 pg_source） | 分支另有版本 | 保留 main 版本 |

## 4. 移植时必须带上的分支审计修正（`aa00bad7`）

1. **终态化用 `context.WithoutCancel`**：客户端断连（survival 循环结束的常
   见原因）时请求 ctx 已取消，Complete/Fail 会报 context.Canceled，任务卡
   running 到 deadline；
2. **sessionless 请求跳过 durable**：ProjectCAS 要求 SessionID，无 session
   的 durable 任务投影必然失败且 outbox 无 deadline 兜底 → 无限退避；
3. （main 侧对应项即本 commit 的 lease 护栏。）

## 5. 分支保留物索引

- `feat/m3-sr-w3-durable`（已推送）：7 commits，`-race` + testcontainers
  integration 全绿；分支内 doc 26 为该轨执行记录（与 main 的 doc 26 编号
  撞车，内容互不引用，移植完成后随分支归档）；
- Wave B 移植完成后，`domains/durabletask/`、迁移 515、
  `db/durable_tasks_schema.go` 及分支 doc 26 一并废弃删除。
