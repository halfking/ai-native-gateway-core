# D09 — 节点状态自检（凭据探测与恢复）

> 域知识库：[docs/audit/playbook/domains/D09-node-state-selfcheck.md](../../../docs/audit/playbook/domains/D09-node-state-selfcheck.md)
> **2026-11-07 填实**：凭据探测时间点规划与恢复及时性轮
> （方案档：`docs/audit/2026-11-07-凭据探测时间点规划与恢复及时性轮.md`）
> 状态：已填实（business/data/stress/safety 四类各有 ≥1 个可执行判据）

## 1. 审计要点（本域 = 「出错/用尽的凭据节点」的可用性检查与恢复闭环）

- 探测对象＝**24h 内使用的模型 ∪ 特色模型**（INV-3 范围门）；目标凭据＝出错（node_probe 失败）
  或用尽（periodic/balance/permanently exhausted）。
- 核心问题＝**凭据已恢复但状态未及时更正**：恢复检测必须有界 SLA；同时探测量要节约
  （健康停放 30 天、用尽期不得空打）。
- 机制分层（读 `bg/node_probe.go` / `bg/probe_queue.go` / `bg/probe_recovery_policy.go` /
  `bg/credential_probe_v2.go` / `bg/credential_recovery.go` / `bg/{periodic,balance}_quota_probe.go`）：
  节点级梯子（INV-1 停放 / INV-2 重武装 / 恢复 SLA 削顶 / 404·协议 6h 长停放 / 配额重置点对齐钉）
  ＋ 凭据级恢复族（30s tick / 2min BalanceQuotaProbe / 5min PeriodicQuotaProbe / 充值 webhook）
  ＋ 事件回拉（onQuotaRecovered → RearmCredentialFailures → 泵 30s 接管）。
- 2026-11-07 修复：F1 恢复验证探针空模型静默丢弃（选模 helper + credential_recovery 源）、
  F2 范围门豁免恢复源 {periodic, credential_recovery}（冷模型 Catch-22）、
  F3 scope skip 15min 重评（45s 空转）、F4 配额失败钉 recover_at+60s（用尽期打爆）、
  F5 quota 恢复事件 re-arm 失败行、F6 注释如实化。

## 2. 业务测试

- [x] B-01 `business/probe_recovery_contract_test.go` TestBusiness_ProbeRecoverySLA_NetworkClassCapped
      —— 可恢复类梯子全 attempt 域 ≤ 恢复 SLA（默认 5min）：「恢复 ≤5min 被复检」的机制保证。
- [x] B-02 同文件 TestBusiness_QuotaKindsFixedCadence —— 配额族固定节奏（periodic 5m / balance·permanent 2m）。
- [x] B-03 同文件 TestBusiness_Confirmed404ParksAt6h —— 确认 404 第 2 次起 6h 长停放、第 1 次普通梯。
- [x] B-04 同文件 TestBusiness_QuotaBodyClassifiesForPin —— 智谱 1310 周窗形态分类进配额族
      （F4 钉点前置）＋普通限流反例。

## 3. 数据测试

- [x] D-01 `data/probe_schedule_defaults_test.go` TestData_RecoveryBackoffSpecRegistered
      —— `probe.recovery_max_backoff_seconds` spec 登记（Default 300 / Min 60 / Max 21600）。
- [x] D-02 同文件 TestData_ResolveRecoveryProbeModel —— F1 选模链四腿（default 优先 /
      绑定回退 / 全无返回空 / DB 错误上浮）。

## 4. 压力测试

- [x] S-01 `stress/ladder_bound_property_test.go` TestStress_LadderBoundedAcrossAttempts
      —— 14 种失败类 × 200 attempt 全域梯子有界性（≤SLA ∪ 登记过的长停放/固定档），
      杜绝「用尽期探测打爆」与无界指数膨胀回归。

## 5. 安全测试

- [x] SF-01 `safety/recovery_event_guards_test.go` TestSafety_RearmCredentialFailuresNilGuards
      —— F5 恢复事件回调 nil/零值/负数安全（恢复 tick 的 dispatch goroutine 不被击穿）。
- [x] SF-02 同文件 TestSafety_ResolveRecoveryProbeModelNilGuards —— F1 选模 nil 守卫。
- [x] SF-03 同文件 TestSafety_UnknownKindKeepsBoundedGenericLadder —— 未知类走通用梯（6h 封顶）。

## 6. 验收门

```bash
go build ./...
go vet ./...
go test -race -timeout 60s ./tests/48h-audit/D09-node-state-selfcheck/...
```

单测层面的机器判据（与上面域判据互补，挂在 bg 包内）：
`bg/probe_schedule_planning_20261107_test.go`（9 测试/21 判据，含变异臂 3/3：
豁免集放大即红 / rearm 去 last_direct_ok 守卫即红 / 钉点宽限取负即红）。

## 7. 与方案文档的对齐

- 方案档：`docs/audit/2026-11-07-凭据探测时间点规划与恢复及时性轮.md`（§2 问题清单 / §3 修正设计 /
  §4 修后恢复检测 SLA 矩阵 / §5 判据与变异臂 / §6 明确没做）。
- 部署验证：本地 2.5.9.2946 → 245/154 → 211（蓝绿），每台读 `/api/system/version` +
  probe 观测面（`llmgw_node_probe_queue_submission_total{outcome=skipped_out_of_scope}` 增速应显著下降、
  配额用尽凭据的 node_probe_runs 在用尽期应停增、恢复事件后 30s 内出现复检 run）。
