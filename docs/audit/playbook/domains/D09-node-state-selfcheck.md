# D09 — 全局节点状态统一与自检流程

> 领域编号: D09 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：全局节点状态（凭据/供应商/通道/自身健康等系统内节点）的**统一状态源**——状态更新收敛在一个模块，由它响应多处反馈（请求正常/失败、探针结果）并统一推进状态机与统计；自检流程（self-check）调度、heartbeat、恢复及时性；NodeProbe 机制。
**不管**：熔断曲线参数（D08）；统计聚合表口径（D10）；队列调度（D04）。

## 2. 参考基线

设计文档：
- `docs/03-design/02-feature-design/会话优化v4/04-节点状态与路由处理设计.md` — 三个独立状态机/自检调度/heartbeat（现行主文档）
- `docs/03-design/01-architecture/architecture/node-probe-mechanism.md` — NodeProbe 触发/探测/状态同步
- `docs/架构优化v6/05-self-check-and-metrics.md` — 通用自检流程 + 12 SLO
- `docs/01-requirements/functional/FR-selfcheck-timely-recovery.md` + `docs/测试/01-selfcheck-timely-recovery/results.md`

代码入口：
- `domains/nodestatecache/`、`domains/nodehealth/`、`internal/probe/`、`internal/probeutil/`
- 状态钩子：`docs/03-design/02-feature-design/design/unified-credential-state-hook.md` 对应实现
- `domains/dbdegradation/`（DB 降级这类全局状态）

## 3. 检查清单

1. **单一写入面**：节点状态迁移只经统一状态模块（unified state hook / nodestatecache）导出的方法；grep 窗口内新增代码，任何直接 UPDATE 状态表/直接改内存状态标志的旁路 = P1（多处反馈会互相覆盖）。
2. **反馈全接入**：请求成败、探针结果、熔断事件、压缩重试成功（恢复信号）等反馈源都汇入统一模块；新反馈源接入时有去抖/去重，单次毛刺不抖动状态。
3. **状态一致**：同一次外部事件在多副本/多 worker 视角下收敛到同一状态（DB 幂等或 leader 语义），不存在"内存态与 DB 态长期分叉"。
4. **自检调度**：self-check 周期、heartbeat 超时、恢复探测（timely recovery FR）参数一致；节点恢复后自动回健康（不靠人工重启）。
5. **统计联动**：状态迁移同步更新统计相关计数（可用率/冷却数等），口径与 D10 对账。
6. **降级诚实**：dbdegradation 等全局降级状态对请求路径可见且行为一致。

## 4. 历史回归点（轮末回注区）

- [初版] 本域健康面基线待首个 playbook 轮（R34）建立；R30 中"breaker 单进程锁内迁移无竞态，跨实例靠 DB 幂等收敛"是相邻基准

- [R36] 探测系统统一（313d1ebc8）漏切面清单：手动 trigger 失败路径写旧表（成功路径 MarkNodeProbeHealthy 同步，失败零痕迹）→ probeSubmitter 接管；availability backfill/diagnostics 两端点直读/直写冻结 model_probe_state → 切 compat 视图/双清；遗留：credential_recovery 守卫+BrokenProbeReviver 无门控、compat 投影 total_attempts 语义漂移、node_probe_runs 无 skipped 映射

## 5. 子代理派发提示词

```text
你是 D09（全局节点状态统一与自检流程）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D09-node-state-selfcheck.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：窗口内新增的成败反馈路径是否绕过统一状态模块直写状态。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```

### R39 回注（2026-09-17，凭据误判事故修复批首审）
- **守卫对称性**：任何"错误不推进共享状态"的守卫必须同时覆盖 legacy tick 路径与持久队列路径（runOne CASE WHEN ↔ mirrorNodeProbeState CASE WHEN、drainDue 熔断 ↔ processBatch 熔断）——20fb4c7a4 只修一半，队列通道 3 次重排即可集群级排除路由（R39 P1-1 已修，钉桩 bg/probe_service_gateway_side_test.go ×3）。
- 探测"终态判定"语义审计：attempt 来源（cf+1 vs 队列重排序号）计任意错误类型，"连续 N 次同错误"需显式记录上轮 errCode（遗留：404 口径与事故文档表述偏差）。
- endpoint_build 类错误码混流"实例错 key"与"凭据密文永久损坏"两种语义，绑定面无信号即此因（遗留）。

### R40 回注（2026-09-18，R39 §三#2/#3 收口）
- **404 口径决议（闭合上文遗留）**：择"修文档"——`unavailableBindingHorizon` 的语义是"本轮失败 episode 内 attempt≥2 的任意 404 即升级 6h"（episode=成功/Submit 重置之间），非"连续两次 404"；episode 内被瞬时错误间隔的 404 仍升级属更保守方向（6h 自愈重查封顶），不值得为叙事精确性给 node_probe_state 加 prev_err_code 列。语义钉进 `modelNotServedRecheckInterval` 注释 + `TestUnavailableBindingHorizon` interleave 用例（bg/node_probe.go / bg/probe_model_not_served_test.go）。
- **endpoint_build 粒度细分（闭合上文遗留）**：新识别器 `isDecryptShapedProbeDetail`（resolveDirectTarget 的 `decrypt: %w` 包装前缀）+ `credentialSpecificDecryptFailure`（解密形且实例解密熔断计数未达 decryptTripThreshold=5）——熔断未跳闸说明本实例解其它凭据正常，失败跟随凭据 = 单凭据密文永久损坏，豁免 guard 写真实不可用（绑定/observed/ladder 全走 else 分支）；实例真错 key 时计数到 5 熔断跳闸，压制恢复 + deescalate sweep 修复 pre-trip 窗口的少量写入（自愈）。updateBindingAvailability 签名新增第 7 参 errDetail（源钉桩同步更新 ×2），sync/queue/probe_service 三路抑制条件全部携带豁免（bg/node_probe_gateway_side_test.go + bg/node_probe_credential_decrypt_test.go）。
- self-check/diagnostics 直读 model_probe_state 的残留面按 probeGuardStateTable/compat 视图逐个切换（R39 收口 probe_missing → v_node_probe_state_compat；legacy 模式下 nps 由 mirror 保持同构）。
- SQL 注释与谓词漂移：expiredCmbRecoverySQL 曾宣称不存在的 next_retry_at 跳过（R39 改注释）。
