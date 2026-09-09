# 审计 — 自检恢复门控（2026-09-09 P0 修复的复核与收口）

日期：2026-09-10 · 复核基线：`f56598b59`（2026-09-09 credential_recovery 修复）· 现场证据：245 实机（SSH + psql 只读）

## 背景与事故回顾

用户报告：MiniMax 两个官方凭据（`hzx` id=41 / `hzx-2` id=42）上游余额已恢复约 2 小时，路由面仍将其排除。
根因（2026-09-09 会话定位）：蓝绿改造（2026-08-31）后 154/245 唯一运行实例均被
`deploy/llm-gateway-go-canary@.service:27` 钉死为 `LLM_GATEWAY_RUNTIME_ROLE=traffic-only`，
而 `cmd/gateway/main.go` 的 bg-services 块以 `!cfg.IsTrafficOnly()` 门控，导致
`credential_recovery` 的 30s 恢复 tick 在全网零实例运行。

`f56598b59` 将该门替换为 `!config.IsCredRecoveryDisabled()`（env 退出开关）。

## 本轮复核发现

| # | 级别 | 发现 | 结论 / 修正 |
|---|---|---|---|
| F1 | **P0** | 修复不完整：`BalanceQuotaProbe` / `PeriodicQuotaProbe` / `credProbeV2` 仍被嵌套 `if !bgDataPlaneOnly`（main.go credProbeV2 块）挡住。`credential_recovery` 的 SQL 刻意不自动翻转硬配额（`permanently_exhausted` / `balance_exhausted`，见 `bg/credential_recovery.go` suspended 守卫注释"只能由 balance_quota_probe 探活成功后经 writeHealth 翻回"）| **cred 41（hzx，quota_permanent）至今没有自动恢复路径**。本轮修正：该块门改为 `!bgDataPlaneOnly \|\| !config.IsQuotaProbeDisabled()`，默认全网运行，`LLM_GATEWAY_QUOTA_PROBE_DISABLED=true` 供真分离拓扑退出 |
| F2 | P0（运维） | 修复未部署：245 运行 `2072-c2b62335`（19:32 启动，早于 21:20 的修复提交）；journald 自 09-07 起 0 条 `credRecovery` / `credential_recovery` / `expired-binding` 日志；启动日志无 `CHECKPOINT: credRecovery started` | 代码已含修复（strings 验证）但角色门在旧构建中关闭。**需经 `scripts/deploy-245.sh`（及 154）部署后重启生效**——本轮仅提交代码，未部署 |
| F3 | P2（披露） | `f56598b59` 的门控替换实际放行了整个 bg-services 块（main.go 3573–5336，约 40 个 worker：probeRollback / modelTier / brokenProbeReviver / routingHealthChecker / pendingSweeper / 清扫器族 / 统计族等），不止 commit message 所称的 credential_recovery | 评估：等于恢复 2026-08-31 之前的"全网常开"已知良好行为；重复运行风险集中在部署重叠窗口（分钟级），各 worker 依赖幂等 UPDATE / 队列 dedup（`ON CONFLICT DO NOTHING`）/ 租约。**不再收窄**（收窄会重新打断已依赖这些 worker 的单实例集群）；已在 main.go 注释与本审计中如实披露 |
| F4 | P3 | main.go 注释引用漂移：materializedViewRefresher 先例 "line 3461" 实为 3465 | 已修正为 3465 并补充本审计链接 |

## 线上证据快照（2026-09-10 01:17+08，只读查询）

- cred 41 `hzx`：`suspended` / `permanently_exhausted` / `quota_permanent`，两个 recover_at 均 NULL（F1 所述无路径的实例）。
- cred 42 `hzx-2`：凭据级 18:37 已被 admin force-enable 为 `ready/ok`；但 **10/11 binding 仍 `available=false`**，`unavailable_recover_at=2026-09-09 08:03`（滞后 17h）。其恢复依赖 `recoverExpiredBindings`（要求 `availability_state='ready'`，已满足）→ 部署 F1+F56598b59 后即可入队 NodeProbeWorker。
- NodeProbeWorker / CredentialSelfcheck / today_success_probe **不受** `bgDataPlaneOnly` 门控，traffic-only 下可运行——binding 级恢复链路在部署后是通的；缺的只是配额族（F1）。

## 本轮代码变更

1. `config/runtime_role.go`：新增 `IsQuotaProbeDisabled()`（`LLM_GATEWAY_QUOTA_PROBE_DISABLED`，仅字面 `true` 生效，大小写无关、容忍空白）。
2. `cmd/gateway/main.go`：credProbeV2 块门 `!bgDataPlaneOnly` → `!bgDataPlaneOnly || !config.IsQuotaProbeDisabled()`；修正行号漂移注释；补充范围披露。
3. `config/runtime_role_test.go`：新增 `TestQuotaProbeEnvContract`（6 子用例，与 `TestCredRecoveryEnvContract` 对称）。

## 明确不做（本轮边界）

- 不解锁 3894（activeProbe/probeQueueWorker）、4520（taxonomySync）、4661–5009（统计/调优/auto-route 族）的 `!bgDataPlaneOnly` 门——与本事故恢复链路无关，风险面大，留给拓扑专项。
- 不执行部署（纪律：仅脚本部署；部署属运维动作，见 handoff 下一步）。

## 验证

- `go build ./...`、`go vet ./config ./cmd/gateway` 通过（见提交前运行记录）
- `go test ./config ./domains/credentialstate` 全绿（含新增 TestQuotaProbeEnvContract）
