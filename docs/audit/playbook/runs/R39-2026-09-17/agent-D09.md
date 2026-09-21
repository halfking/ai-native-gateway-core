# D09 全局节点状态统一与自检 子代理报告（窗口：67f78247c..294e0f65d）

> 原文存档（主代理已逐条亲读复核，处置见轮文档）。子代理：Explore，2026-09-17。

前置核对更正：派发词称 67d8629d2 触及 credentialhealth/checker.go 与 bg/credential_recovery.go——经 `git log` 亲证不实，这两文件在窗口内的改动归属 **86e09daa7**（R36 遗留#3+#5：probeGuardStateTable 模式感知 + reviver no-op），已一并纳入审计。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P1** | **gateway-side 错误经队列路径仍污染共享 node_probe_state 并集群级排除路由**：20fb4c7a4 的"梯子不推进"守卫只加在 legacy runOne（CASE WHEN），队列路径的 `mirrorNodeProbeState` 失败分支无条件写 `consecutive_failures = attempt`（队列重排 1..7）+ 梯子 next_retry；且解密熔断只接在 legacy `drainDue`，`ProbeQueueWorker.processBatch` 认领无门。错 key 实例的持久队列 worker 3 次重排即把 cf 推到 ≥3 → `v_node_probe_state_compat` 投影 `broken_confirmed` → `brokenPairExcludeSQL` 在候选 SQL 中**只看 state、不联 cmb.available** → 全实例路由排除该 (cred,model)。这正是事故"中毒梯子"形状在修复后的残留通道；`deescalateGatewaySideProbeState` 不清 cf，且 sweep 后若队列继续写会回填 | bg/probe_service.go:829-845（UPDATE 无 CASE WHEN）；bg/node_probe.go:2167（legacy 有守卫，不对称）；bg/probe_queue_worker.go:144-155（Claim 无熔断）；bg/node_probe.go:1774（熔断仅 drainDue）；sql/objects/views/v_node_probe_state_compat.sql:17-19（cf≥3→broken_confirmed）；provider/client.go:2359-2371 + 1587/1653（路由排除） | 触发路径：共享库错 key 实例 → request_failure 任务 → probeDirect 解密失败 endpoint_build → Run 重排 → mirror 写 cf=attempt → 第 3 次 broken_confirmed。建议 mirrorNodeProbeState 复用 runOne 同型 CASE WHEN + processBatch 接 decryptCircuitTripped，配钉桩测试 |
| 2 | P3 | **404 升级条件是 attempt≥2 而非"连续两次 404"**：attempt 来源是 consecutive_failures+1（legacy）或队列任务重排序号（queue），计**任意错误类型**；先 timeout 后首个 404 即被 6h 停泊。事故文档宣称"首次 404 保持 5 分钟（防聚合器偶发 404 误终判）"未严格成立。影响有限（6h 复验 + 真实流量成功即时恢复） | bg/probe_recovery_policy.go:53-54；bg/node_probe.go:1945（attempt=cf+1）；bg/probe_service.go:295-297（task.Attempt）；文档 docs/audit/2026-09-17-credential-probe-false-unavailable-incident.md §2#1 | 如需严格"两次 404"，ladder 需记上一轮 errCode 再判；或修正文档表述。择一即可 |
| 3 | P3 | **endpoint_build 不区分"实例错 key"与"单凭据密文永久损坏"**：guard 把一切 endpoint_build/request_build 当实例级配置问题拒写绑定，而 resolveDirectTarget 的 "unsupported secret format"/keyring nil 也产出 endpoint_build → 真坏凭据在绑定面**永无不可用信号**，路由持续选中、请求期失败；兜底仅剩真实流量 breaker/credentialstate。判定目前**无法**区分"轮换中 vs 永久损坏"（两者同为连续解密失败），但熔断行为等价（暂停+周期半开），无锁定风险 | bg/node_probe.go:275-282（classifier 枚举）；bg/node_probe.go:2670-2676（unsupported secret format→endpoint_build）；bg/node_probe.go:2806-2816（拒写） | 可按错误明细细分（decrypt 类 vs 格式类）再决定是否豁免 guard；或接受请求面兜底并留档 |
| 4 | P3 | **expiredCmbRecoverySQL 注释漂移**：注释宣称 "Skip when node_probe_state.next_retry_at > now() (ladder mid-cycle)"，SQL 只有 paused NOT EXISTS、无此条件。对 404 6h 停泊对无实害（recover_at 与 next_retry 同为 6h、同到期），但注释误导后续维护 | bg/credential_recovery.go:1093（注释）vs 1108-1126（SQL 无对应谓词） | 改注释或补谓词，二选一 |
| 5 | P3 | **routing_health_checks probe_missing 直读冻结 model_probe_state，无模式门控**：新模式（默认）下 mps 停更，新增绑定永远在 mps 无行 → self-check 永久假阳性 warning。是 D09 域文档 R36 遗留"diagnostics 直读冻结表"未清完的残留面（另一面 backfill 已切 compat，见健康面#6） | bg/routing_health_checks.go:27（直读 mps）；cmd/gateway/main.go:3740（已注册生效） | 切 `v_node_probe_state_compat` 或按 probemode 选源（与 86e09daa7 同法） |

## 二、核实为健康的面

- **404 终态只作用于 (cred,model) 对，不误终态化凭据/节点**：仅写 cmb 可用面（`unavailableBindingHorizon`，bg/node_probe.go:2786-2792）与 observed-state 窗口（probe_service.go:722-723）；凭据级 `updateCredentialHealth` 仅成功路径调用（probe_service.go:687-689），节点 paused/manual 语义未触及；admin_protected 与 `manual%` 原因豁免（bg/node_probe.go:2836-2838）。
- **恢复闭环三通道齐全**：① reason 保留 `probe_` 前缀（"probe_"+reasonTag，bg/node_probe.go:2825-2841）被 `expiredCmbRecoverySQL` 的 `LIKE 'probe!_%' ESCAPE '!'` 命中（bg/credential_recovery.go:1116），6h 后复验权保留；② ladder 泵 `ProbeBackoffForErrCode` 404 attempt≥2 同停 6h（bg/probe_recovery_policy.go:53-54），双路径节奏一致；③ 真实流量成功经 write-through `healthyBindingSQL` 即时恢复（bg/node_probe_write_through.go:9-27）。模型恢复服务当天自愈，245/154 已端到端实证（事故文档 §5）。
- **applyOutcome 守卫链闭合**：`firstErrCode` direct 优先（bg/node_probe.go:2947-2957）→ errCode 为 endpoint_build 时 outer guard 同跳 binding+observed 双写（bg/probe_service.go:705-712）；即使 directErrCode 改写泄漏，`updateBindingAvailability` 内部 guard 二道防线（bg/node_probe.go:2806-2816）。隔离边界 = 分类器 → 函数内拒写 → 调用点守卫 → 实例熔断，四层（第 4 层缺口即发现#1）。
- **legacy runOne 失败分支正确**：CASE WHEN 不推进 cf + 15min gateway-side pacing（bg/node_probe.go:2151-2167），`TestRunOneGatewaySideSkipsSharedStateWrites` 钉桩；featured 回退倍率不再缩短 404 停泊（bg/probe_service.go:452-460）；featuredCycle 跳过 cmb.available=FALSE（bg/model_probe.go:776）。
- **decrypt 降级 sweep 语义安全**：只清 `endpoint_build/request_build` 梯子行与 `probe_endpoint_build/probe_request_build` 绑定（bg/node_probe.go:549-590），errCode 本身即写入者身份，任意实例可安全代清；触发于 Start + 熔断开→合（bg/node_probe.go:328-337, 531-534）。真坏密钥不会长期留在探测池：熔断后每 15min 仅一次半开探（bg/node_probe.go:289-331）。
- **单一事实源切换基本收口**（313d1ebc8 + 86e09daa7）：手动上下线/诊断修复双清两表（admin/credential_monitor.go:1128-1132、1183-1195；admin/diagnostics_credential.go:264-282、admin/diagnostics_routing.go:297-315 各有 R36 step3.5）；availability backfill 切 compat（admin/probe_dashboard.go:2249）；TriggerManual 走 SubmitManualProbe + node_probe_state healthy 标记（bg/model_probe.go:1366）；GetState 切 compat（bg/model_probe.go:1552-1561）；credential_recovery 守卫与 checker.RecoverExpired 均改 probeGuardStateTable 模式感知（bg/credential_recovery.go:313-317、613-620；credentialhealth/checker.go:644）；reviver 新模式 no-op（bg/broken_probe_reviver.go:89-94）；probe-health 视图族切 compat 有基线漂移拦截单测（db/probe_views_unified_test.go）。新模式残留直读写仅发现#5 一处假阳性面 + 惰性 legacy 写（model_probe.go consensus 分支仅 legacy 模式跑）。
- **smart-discovery.sh pg_container_usable fail-safe 达成**：`docker start` → env/port → **pg_isready（免认证）15s 就绪窗**（scripts/deploy-lib.legacy/smart-discovery.sh:90-99）→ 就绪后才做 `-d postgres` 精确 `pg_database` 探测（:103-117）；就绪后连接失败归为 auth 漂移诚实告警，库缺失走 will-create（DL_DISCOVERED_PG_HAS_DB=0，:126-129）。recovery 窗口先等后判，不再误判"not usable"；配套 tests/lib/test-smart-discovery-pg.sh。

## 三、未覆盖项与原因

- **发现#1 未做真库/集成实测**：只读静态审计无法起共享 PG + 错 key 双实例复现 cf→broken_confirmed→路由排除链；建议主代理修复时配 pgxmock/集成钉桩实证。
- **recoveringSweeper 在新模式下经 consensus machinery 对 model_probe_state 的写入是否完全惰性**：视图族已切 compat，理论上不影响展示，但 applyResult 状态写分支的逐行门控未核完（超出窗口预算；R36 遗留"compat 投影 total_attempts 语义漂移、node_probe_runs 无 skipped 映射"仍按域文档挂账）。
- **716 迁移 SQL 本体（385 行）与 down 未逐行审**：属迁移/视图域交叉，本轮聚焦 Go 读写路径；compat 视图 DROP+重建（db.ensureProbeHealthDashboardViews）与基线文件的 diff 级比对未做，依赖 db/probe_views_unified_test.go 拦截（未实际运行测试）。
- **admin/probe_dashboard.go:819/943 "legacy_source" 标签面**：仅确认为指标命名用途，未逐行核其数据源。
- 窗口内非本域提交（streaming 保活、deploy-local、RLS 719、717 迁移等）未审，归各属域。
