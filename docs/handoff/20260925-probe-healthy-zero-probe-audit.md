# r0925 probe healthy-zero-probe 批判式审计 Handoff

**项目**: 对健康节点零探测 + 探测失败根因分类（协议/节点/网关）
**最后更新**: 2026-09-25
**状态**: ✅ 两轮交付均已合入 main 并推送（`59c7f4233` 首轮 + 本审计轮）；**未部署、未做生产实测**
**负责人**: handoff 接收方
**关联 commit**: `59c7f4233`（fix(probe) 主轮）、本轮审计修复随 handoff commit
**需求来源**: 用户 2026-09-25 指令——"对于没有错误的节点就不要探测了，只针对有异常的节点探测。错误的探测要搞清楚是协议问题还是节点的问题。请及时提交代码并推送。"

---

## 一、交付物清单（以代码为准，非声明）

| 文件 | 内容 | 核验方式 |
|------|------|---------|
| `bg/probe_root_cause.go`（新增 ~200 行） | node/protocol/gateway 纯函数分类器 + `llmgw_node_probe_root_cause_total` 指标 + 日志/标注 helper | 表驱动单测 24 案例 |
| `bg/probe_root_cause_test.go`（新增） | 分类器/计费形体判定/根因退避/标注幂等 | `go test ./bg/` 绿 |
| `bg/node_probe.go` | round 携带 `rootCause`；probeDirect/probeGateway defer 收口分类；runOne 成功行 NULL 字面量停放；失败退避根因感知；emitProbe 计数点 | 回归守卫 + 全量测试 |
| `bg/probe_service.go` | `success := direct.ok`；终态 `successResult()` 无 NextRunAt；`gateway_round_degraded`/`gateway_pin_unsupported` 观测性 ReasonCode；mirror 成功分支 `last_gateway_ok=$3`；退避只由 direct 决定；featured 乘数豁免 protocol | `TestProbeServiceGatewayFailureDoesNotLadderHealthyNode` 等 |
| `bg/probe_recovery_policy.go` | `probeBackoffForDirectOutcome`（protocol attempt≥2 → 6h park） | 单测 |
| `bg/active_probe_executor.go` | `ProbeResult.RootCause` 字段 + `probe_direct_protocol_mismatch` error_kind | 单测覆盖 |
| `bg/credential_recovery.go` | **审计 G 修复**：reconcileStaleNodeProbeStateSQL 补 `nps.next_retry_at <= now()` 调度门 | 守卫断言 |
| `bg/node_probe_ladder_regression_test.go` / `bg/probe_recovery_authority_test.go` / `bg/probe_service_test.go` | 守卫更新 + 新契约钉桩 | 全量绿 |
| `docs/probe/2026-09-25-healthy-zero-probe-and-root-cause.md`（新增） | 问题定性/修复清单/审计 A-G/行为变化/遗留风险 | — |
| `docs/probe/2026-09-20-probe-volume-optimization.md` | 追加 §9 交叉引用 | — |

## 二、批判式审计核心发现（本轮自查）

### 2.1 已修复

| ID | 发现 | 严重度 | 处置 |
|----|------|--------|------|
| A | runOne 成功分支传裸 untyped `nil` 参数，而池是 `QueryExecModeSimpleProtocol`（db/db.go:72）；且恒 NULL 参数化多余 | P2（歧义风险+卫生） | 改 SQL 字面量 `last_err_code = NULL`，守卫同步 |
| G | **stale-state 调解器绕过调度面**：gateway-side 失败行（绑定面拒写保持 available=TRUE + 15m 退避）恰在候选集内，每 30s tick 以 +5s 重新入队，退避梯完全失效——与 pumpDueStatesSQL 口径不一致 | **P1** | SQL 补 `nps.next_retry_at <= now()` + 守卫 |

### 2.2 核查通过（声明过、本次实证）

- B：`ProbeQueueTTL=5min` 不会掐死 6h 重臂——`reviveExpiredReadySQL` 续命 `GREATEST(next_run_at,now())+TTL`；
- C：`credential_probe_queue.reason_code` 无 CHECK（migration 489 纯 TEXT），新 ReasonCode 不会让 complete 失败；
- D：统一路 `directRound` 生产走 `worker.probeDirect`，defer 分类真实生效；
- E：mirror 失败分支 `firstErrCode(direct,gw)` 在 direct 失败语义下取 direct 码正确；无残留复合判定；
- F：`probe_direct_protocol_mismatch` 不在 `transientErrorKinds`、router `state:` 瞬态集，不会误触发 reviewing→unreachable。

### 2.3 首轮交付中的表述修正

- 首轮提交信息声称"protocol 停放 6h"——实际是**每任务代前两次快探（5s/30s），attempt≥2 才 6h**；且 queue 路径 attempt 随任务代重置，任务代终止后泵再生成时会再快探两次。与 404-park 同款 pre-existing 口径，登记为遗留 1，未在本轮扩scope修复。

## 三、测试命令与结果

```text
go build ./...                      → 0 错误
go test ./bg/ -count=1              → ok 25.1s
go test ./... -count=1              → 297 包 ok，0 FAIL（rebase 后复跑一次确认）
```

## 四、遗留风险

1. **队列任务代际 attempt 重置**（见 2.3）——每代 2 个快探 + 6h 节奏，非严格 6h 一次。
2. **网络类短梯长尾**——`NetworkProbeBackoffChain` 封顶 60s 后无沉底，失败行以 ~60s 节奏被泵无限重探（45 对实测，见并行轮 `docs/probe` P0-2 方案修订 13de0f7df）。节点确实异常故符合"只探异常节点"，但无上限；该方案已立项"短梯长尾化 + pair 级频控"，**本 handoff 不重复设计**，下一轮实现时与遗留 3 一并处理。
3. **存量污染行无迁移**——旧版写入的成功行（last_direct_ok=TRUE + 非空 last_err_code）靠下次相遇收敛（gate skip 删行或下次成功重写），收敛前每对至多多探一轮。
4. **零生产实测**——指标分布、探测量降幅全是代码推演；部署后必须核对 `llmgw_node_probe_root_cause_total` 与 `node_probe_runs` 日增量。
5. **36h lookback claim 不查 next_retry_at**——仅选 cmb.available=FALSE（真异常）+ 5min 租约，登记不动。
6. **行为变化面**——队列结算口径、`node_probe_runs.success` 口径（composite→direct-only）、`onQuotaRecovered` 在 gateway 降级时也会触发，看板数字会动，需在发布说明里提示。

## 五、下一轮提示词

> 延续 r0925 探测轮（handoff：docs/handoff/20260925-probe-healthy-zero-probe-audit.md）。请：① 部署后核对探测收敛：`llmgw_node_probe_root_cause_total` 分布（node/protocol/gateway 占比）、`node_probe_runs` 日增量对比上一版本、`llmgw_node_probe_necessity_skip_total` 是否回落；② 按已立项的 P0-2 方案修订（探测成本方案批判式复审修订，13de0f7df）实现"短梯长尾化 + request_failure pair 级频控"，消解网络类 60s 档滞留的无限重探，并顺带处理队列任务代际 attempt 重置（失败退避改读 `node_probe_state.consecutive_failures` 或泵入队透传连续失败计数），两者共用同一批契约测试；③ 对历史 `node_probe_state` 做一次只读盘点，确认无 `last_direct_ok=TRUE AND last_err_code<>''` 的存量污染行残留（有则评估一次性清洗 SQL）；④ 全程实事求是：每项声称给证据，修不完的列遗留，不凑数。
