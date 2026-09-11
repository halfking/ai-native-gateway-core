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

## 部署与修复收口（2026-09-11 凌晨）

### 部署时间线

| 时间(CST) | 事件 | 结果 |
|---|---|---|
| 04:1x | 首次 deploy-245（候选 8782） | EnsureSchema statement timeout（57014）自动回滚，2078 保持 active |
| 05:07 | 二次 deploy-245 | 迁移 693 ALTER TABLE 在 252 PG 锁等待超时，未切流 |
| ~05:20 | **252 PG 磁盘写满事故** | `/`（197G）100%，新建连接 FATAL `could not write init file`，pg-252-pg17 容器随之离线（"Initialized"），154 生产 DB 层短暂中断 |
| ~05:26 | 磁盘处置 | 元凶：并行会话的 `llm_gateway-pre-sync-20260911.dump`（7GB，残缺）+ 昨夜 `252_pg_dumpall.sql`（49GB）+ podman 悬空镜像。`docker image prune`（实为 podman）回收 ~27GB；两个 dump 由其属主自行清理。PG 自愈重启恢复，最终 42GB 可用 |
| 05:32 | deploy-245 成功 | 2079-fe640764（693 已由守护脚本预应用，切换前迁移瞬时通过），总 86s |
| 05:40 | deploy-154 成功 | 2079-fe640764，总 151s |
| 05:48 | deploy-245（修复） | 2080-69420603，75s |
| 05:51 | deploy-154（修复） | 2080-69420603，76s |

### F2 之后的两个 probeSubmitter 新根因（部署后仍报错，均已修复）

1. **245：env 门控（非代码）**。`/opt/llm-gateway-go/.env:242` 设 `LLM_GATEWAY_USE_NEW_PROBE_MODE=false`，`shouldStartNewProbeWorkers()` 整体为假，main.go credRecovery 接线块被静默跳过（每 30s 三连 ERROR，60 条/10min）。已改为 `true` 对齐 154；顺带删除同文件一行字面 `\n` 损坏的 `LLM_GATEWAY_RECOVERY_L2_MODE` 残行（有效值仍为后面的 `=off`，行为不变）。备份：`.env.bak-20260911-probefix`。
2. **154：URSM v2 authoritative 分支缺接线（真实代码缺口）**。154 以 authoritative 模式运行（无 credentialstate.Manager），nodeProbeWorker 走 main.go 独立 fallback 分支，该分支从未给 credRecovery `SetProbeSubmitter`。旧构建 2073 因 traffic-only 门整个 bg 块不跑而无声；f56598b59 放行后 worker 起来才暴露（同 pid 既打 "node_probe_worker started" 又打 "probeSubmitter not wired"）。修复 `69420603b`：authoritative 分支镜像标准分支的接线（含 SetProbeSubmitterImmediate 与 OnQuotaRecovered hook），journald 出现 `credRecovery: expired-binding probe submitter wired (authoritative)`。

### 部署后验证证据（两节点 2080-69420603）

- 245（port 8781）/154（port 8781）healthz 200 ready:true；`postgres disabled` 0 条；`probeSubmitter not wired` 0 条。
- 245: `credRecovery: expired-binding probe submitter wired`、`node_probe_worker started (api_key_resolved:true)`、`credential_selfcheck_worker started`；`balance_quota_probe` 每 2min 提交（count 7→10）；`credential_recovery: stale node_probe_state rows handed to probe queue` 每 30s（31–48 pairs）。
- 154: `...submitter wired (authoritative)`、`balance_quota_probe count=10`、stale-state 投递 50 pairs/12 creds。
- 共享库层面：quota-probe 家族现覆盖 13 个 permanently_exhausted 凭据（12 active 全部入轨；47 sp1-2 仍 `status=disabled` 被 status 资格门挡住，与 cred 41 原状态同类，留待运维决策，本轮未动）。

### cred 41 / cred 42 终态

- **cred 42 (hzx-2)**：`active/ready/ok`，22 条 binding `available=false` 为 0，`v_routable_credential_models` 11/11 可路由。**恢复闭环完成**。
- **cred 41 (hzx)**：新增发现——其 `status='disabled'`（08-21 即已置，`manual_disabled=f`）挡在 BalanceQuotaProbe 资格 SQL（`status='active'`）之外，F1 审计未覆盖此层；探针族全部代码（writeHealth 等）均不翻 `status`，故单纯部署 F1 无法自动恢复。本轮经 admin API `PATCH /api/providers/14/credentials/41 {"status":"active"}` 解锁资格（**非** force_enable，availability/quota 未动）。之后 BalanceQuotaProbe 每 2min 真实探测（05:40/05:50/05:52/05:54…），上游持续返回 **402 payment required**，探针诚实拒绝翻牌（`writeHealth skipped stale result`）。
  - **上游定性（2026-09-11 06:1x，服务端 reveal 后直连复测，key 未落任何日志）**：cred 41 key → HTTP 402 `insufficient_balance_error "insufficient balance (1008)"`；同一时刻 cred 42 key → HTTP 200 正常 completion。**key 有效、鉴权通过，所属账户余额为零——充值未进入该 key 对应的 MiniMax 账户/分组**（对比 cred 42 正常，疑似充到了别的账户）。上游充值到位后，网关下一轮 2min 探针自动翻 ready/ok，无需任何人工干预。

### 本轮新增变更

- `cmd/gateway/main.go`（69420603b）：authoritative 分支 credRecovery 接线补齐（+21 行）。
- 245 `.env`：USE_NEW_PROBE_MODE 纠偏 + 损坏行清理（节点侧变更，不入库）。
- DB：迁移 693（`provider_models_canonical_cleared_at`）已应用并登记 schema_migrations（05:32:46，列由锁窗口守护预加、记账由部署 runner 完成）；cred 41 status 经 admin API 置 active（审计走 admin 链路）。

## closeout 后复核（2026-09-11 07:20–08:35）

### 状态延续确认（journald 实测；unit 名：245=llmgo-245-canary@8781，154=llm-gateway-go-canary@8781）

- 245（PID 910109）/154（PID 14264）healthz 200 ready:true，5:48/5:51 启动后持续运行。
- `probeSubmitter not wired` 两节点 **0 条**；`postgres disabled` **0 条**。
- `credential_recovery: stale node_probe_state rows handed to probe queue` 30s 周期持续（pairs 40–50 / unique_credentials 9–11）；`balance_quota_probe` 每 2min count=10；`periodic_quota_probe` 5min 周期；`credential_selfcheck_worker: model ok` 正常。
- cred 41 `balance_last_checked_at=07:41:09` 持续被 BalanceQuotaProbe 按设计探测，MiniMax 仍 402 —— 与上文上游定性一致，无新变化。

### 本轮新发现（转 R12 候选）

| 级别 | 发现 | 证据 | 处置建议 |
|---|---|---|---|
| P2 | `node_probe_runs` audit insert 违反 `attempt_check`（attempt BETWEEN 1 AND 7，migration 341/415），legacy runOne 整体失败、该次探测丢失（154 cred 59 claude-sonnet-4-6，trigger_kind=request_failure，SQLSTATE 23514） | 154 journald 07:28:40 | 审计插入失败不应中止 runOne：降级 WARN 丢行，或插入前 clamp attempt；另需定位 attempt 越界来源（0 或 >7） |
| P3 | 154 collector → `POST /api/v1/collect/runtime` 404：该路由注册在 license-authority（`cmd/license-authority/collect_handler.go:90`），gateway 未注册；collector 的 authorityURL 指向 `https://llm.kxpms.cn`，nginx 未把 `/api/v1/collect/*` 分流给 license-authority → 154 运行时指标上报全丢（分钟级 404 WARN） | 154 journald http_request | 拓扑修正候选：nginx 加 `location /api/v1/collect/` → license-authority，或 collector authorityURL 改指 authority 专口 |
| P3 | residual failed unit 污染 systemd 列表（245 `llmgo-245-canary@8782` 上一轮回滚残留 / 154 `llm-gateway-go.service` legacy 弃用单元） | `systemctl list-units --state=failed` | ✅ 本轮已 `systemctl reset-failed` 清零，运行中服务未受影响（154 canary healthz 200 复验） |

### 巡检方法修正（自我纠错，供后续会话）

本复核会话初期曾三次误判（均未落入已提交文档，此处记录避免复发）：①误判两节点服务下线——查询了不存在的 unit 名（245 正确名 `llmgo-245-canary@<port>`，154 为 `llm-gateway-go-canary@<port>`）；②误判 journald 自 09-10 空窗——同因；③误判 slot 缺 state/version 文件是部署边缘态——蓝绿契约下 `slots/<port>` 本就是指向 release 的裸 symlink（unit 遵循 active symlink），无 per-slot 元数据文件。教训：巡检一律先 `systemctl list-units --type=service | grep -iE 'llm|canary'` 定位真实 unit 名，再查日志与状态。

### 版本漂移记录（部署收口后 main 继续前进，未部署）

部署基线 2080=`69420603b`。其后 main 新增（建议下一窗口以 2081 批量部署）：

- `93c2025ac` docs：cred 41 上游定性（本文档上文已含）。
- `42e507ce7` merge：自检必要性门控 end-to-end（`11af45216`，`bg/probe_necessity.go` +373 行、双 ProbeService 接线点、URSM probe evidence 层；fail-open 设计，16 用例全绿）。
- `dc8463e28` perf：ursm `ProbeHealthEvidence` redis pipeline 批量化（消除 ~1000 RTT/次 preflight）。
- `5367d6f84` test：removeSkippedProbe 分支序 pin + typed-nil trap 修复（`bg/probe_necessity.go`）。

部署决策留待下一窗口：necessity gate 为 fail-open、测试全绿，但 245/154 刚稳定约 2h 且 cred 恢复链路正在产出证据，不建议立即追部署；等 cred 41 上游充值归属确认后一并上 2081。
