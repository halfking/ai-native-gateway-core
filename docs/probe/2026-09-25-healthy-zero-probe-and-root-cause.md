# 对健康节点零探测 + 探测失败根因分类（2026-09-25）

**状态**: ✅ 已落地并推送（commit `59c7f4233` + 本轮审计修复）
**需求来源**: "对于没有错误的节点就不要探测了，只针对有异常的节点探测。错误的探测要搞清楚是协议问题还是节点的问题。"
**关联文档**: [2026-09-20-probe-volume-optimization.md](2026-09-20-probe-volume-optimization.md)（探测量优化政策 INV-1..5）

---

## 一、问题定性（批判式审计后的最终根因）

### 1.1 健康节点被重探 —— 两处根因，不是一处

**根因①（主犯）：统一队列执行路径的复合 success 判定回归了 2026-09-10 hzx-2 教义。**

`bg/probe_service.go` 的 `ProbeService.Run` 原为：

```go
success := direct.ok && gw.ok && gateway.pinned
```

direct 轮拿凭据自己的密钥直连上游——它 2xx 即可证明节点健康；gateway 轮是网关绕路发出的复合请求，其失败（网关 5xx、pin 不支持、`no_candidates`）不是节点的错。复合判定把"direct 成功 + gateway 失败"整单判 Failed，随后：

1. `mirrorNodeProbeState` 失败分支以 gateway errCode 武装 `node_probe_state`（`last_err_code` 非空 + `consecutive_failures=attempt` + 重试梯）；
2. `pumpDueStatesToQueue`（INV-1 错误证据门看到"错误证据"）按梯重排队；
3. 可证健康的节点被无限重探。

讽刺的是：同一次运行里 `applyOutcome` 已经按 direct 成功把绑定面翻回 available——结算语义自相矛盾。legacy `runOne` 路径（`success := direct.ok`，2026-09-10 hzx-2 事故修复 + `TestNodeProbeLadderDrivenByDirectRoundOnly` 守卫）是对的，统一队列路径漏改。

**根因②：legacy `runOne` 成功分支把 gateway 码写进成功行，破坏 healthy-parked 形状。**

成功分支 UPDATE 原写 `last_err_code = firstErrCode(direct, gw)`（direct-only 恢复时即 gateway 码）。而 `nodeProbeHealthyParkedSQL`（INV-1）要求 `last_err_code` 为空——成功行因此不满足停放形状，落入 `reconcileStaleNodeProbeStates`（`cmb.available=TRUE AND NOT healthy-parked`）的候选集，每 30s tick 被 `Submit` 重新提交（入队 `NextRunAt=now()+5s`）。

### 1.2 错误探测无法区分协议/节点 —— 分类体系缺口

已有体系只回答"哪一类 HTTP/传输错误"（`classifyProbeNetworkError`、`nodeProbeResultToStatus`、`classifyProbeErrorKind`），没有回答"这个失败该由谁负责、重试能不能治好"。三类失败被同等压进 5s→6h 梯：

- **节点问题**（超时/认证/欠费/5xx）——重试梯有意义，节点可能自愈；
- **协议问题**（404 model not found / 405 / 415 / 契约形 400/422）——重探**永远**不可能成功，只有修正 provider 协议/端点/目录配置才能治愈；按梯重探纯属烧钱（apigpt 14 模型 × 7 尝试/24h 的永恒churn 形状，`ProbeBackoffForErrCode` 只对 404 做了 6h 停放，400/405/415 没有）;
- **网关问题**（endpoint/request build、解密、pin 不支持）——本实例/网关能力的错，节点可能是好的。

---

## 二、修复内容（代码位置）

### 2.1 对健康节点零探测

| 位置 | 改动 |
|------|------|
| `bg/probe_service.go` `Run` | `success := direct.ok`（与 legacy runOne 对齐）；direct 成功即终态结算 `successResult()`——**无 NextRunAt**，队列 worker 无从重臂健康节点；gateway 异常降级为观测性元数据：`ReasonCode=gateway_round_degraded`（pinned 失败）/ `gateway_pin_unsupported`（legacy unpinned），Status 仍为 Success |
| `bg/probe_service.go` `Run` 退避块 | 失败退避只由 direct 轮决定：`errCode := direct.errCode`、`probeBackoffForDirectOutcome(direct.errCode, direct.rootCause, attempt)`；featured 加速乘数豁免 protocol 形失败（防止 6h 停放被砍半重新变 churn） |
| `bg/probe_service.go` `mirrorNodeProbeState` | 成功分支 `last_err_code/last_err_detail = NULL`、`last_gateway_ok = $3(gw.ok)`（原硬编码 TRUE 且无 gateway 信息） |
| `bg/node_probe.go` `runOne` 成功分支 | `last_err_code = NULL` / `last_err_detail = NULL` **SQL 字面量**（见审计 A）、`last_gateway_ok = $3(gw.ok)`；gateway 异常照记 node_probe_runs 审计 + WARN 日志 |
| `bg/node_probe.go` `runOne` 失败分支 | 退避经 `probeBackoffForDirectOutcome`（根因感知） |

### 2.2 根因分类（node / protocol / gateway）

**新增 `bg/probe_root_cause.go`**（纯函数，无 IO）：

- `classifyProbeRootCause(errCode, httpStatus, responseBody) → ProbeRootCause`：
  - `gateway`：endpoint_build / request_build / gateway_not_configured / gateway_pin_unsupported / gateway_probe_failed（含 `probe_` 前缀防御形）；
  - `node`：timeout/dns/connection/network、401/402/403/408/429、5xx、**计费形 400/422**（响应体含 `insufficient_quota`/`余额不足`/`欠费`/`额度不足` 等双语关键词——OpenAI 系上游对欠费返回 400，按状态码一刀切会误停放可充值恢复的凭据）；
  - `protocol`：404/405/410/415 + 契约形 400/422；
  - 兜底：未知 → node（保守，保梯）。
- 每轮失败统一在 `probeDirect`/`probeGateway` 的 **defer 收口**分类（两条执行路径自动继承），`err_detail` 追加 `(root_cause=...)` 机器可查后缀（幂等，不破坏 `decrypt: `、`no rows in result set` 前缀哨兵）。
- 指标 `llmgw_node_probe_root_cause_total{round, cause, err_code}`（`emitProbe` 为唯一计数点，每失败轮恰一次）。
- `error_kind` 新增 `probe_direct_protocol_mismatch`（`classifyProbeErrorKind`，仅 HTTP4xx 且 RootCause=protocol）——legacy executor 轮未带 RootCause，保持原映射。
- 失败日志按根因给出处置指引（protocol → 修配置别重探；gateway → 修本实例；node → 梯适用）。
- 退避策略 `probeBackoffForDirectOutcome`（`bg/probe_recovery_policy.go`）：protocol 形 attempt≥2 → `modelNotServedRecheckInterval`（6h，与 404 同款）；attempt=1 保留短梯（聚合商一次性抖动仍能快速复验）。

### 2.3 R65 批判式审计追加修复

| ID | 发现 | 处置 |
|----|------|------|
| A | `db/db.go:72` 池为 `QueryExecModeSimpleProtocol`；runOne 成功分支传裸 untyped `nil` 参数存在编码歧义（且恒 NULL 参数化本身多余） | 改 SQL 字面量 `last_err_code = NULL`；守卫同步（`gw.ok, nil, nil` 断言 → NULL 字面量断言） |
| B | `ProbeQueueTTL=5min` vs protocol 6h 重臂——expires_at 会否掐死 6h 计划？ | 核查通过：`reviveExpiredReadySQL` 对 attempt<max 的 ready 行续命 `GREATEST(next_run_at,now())+TTL`，6h 节奏保住（2026-09-20 的 404-6h-park 已依赖同机制） |
| C | 新 ReasonCode 会否被队列 CHECK 拒绝？ | 核查通过：`credential_probe_queue.reason_code` 为无约束 TEXT（migration 489） |
| D | 统一路 directRound 是否真走分类器？ | 核查通过：生产路径 `s.worker.probeDirect`（defer 分类生效）；`probeResultToRound` 对未分类轮兜底 classify |
| E | 改动区残留/重复块？失败分支 `firstErrCode(direct,gw)`？ | 核查通过：失败语义下取 direct 码正确；Run/mirror 无残留复合判定 |
| F | 新 error_kind 兼容性？ | 核查通过：不在 `transientErrorKinds` 白名单（不触发 reviewing→unreachable，protocol mismatch 本就不该）；router `state:` 瞬态集不涉及 |
| **G** | **`reconcileStaleNodeProbeStateSQL` 缺 `next_retry_at` 调度门**：gateway-side 失败行（绑定面拒写保持 available=TRUE + 15m 固定退避）恰落其候选集，`Submit` 每 30s 以 +5s 入队，**完全绕过退避梯**——与 `pumpDueStatesSQL`（有 `nps.next_retry_at <= now()`）口径不一致 | **已修**：SQL 增加 `AND nps.next_retry_at <= now()`；`TestReconcileStaleNodeProbeStateSQLPreservesPausedNodes` 补断言 |

---

## 三、行为变化（部署后可感知）

1. **探测量**：direct-verified 健康对不再被泵/调解器/成功行回声重复探测；protocol 形失败对第二次起 6h 节奏（不再走 7 步梯）。
2. **队列结算**：direct 成功 + gateway 降级的任务以 Success 结算（ReasonCode 携带降级原因），`onQuotaRecovered` 通知照发（旧复合判定下反而不发——缓存延迟恢复，属顺带修正）。
3. **`node_probe_runs.success` 口径**：统一队列路径由复合改为 direct-only——与 legacy 路径、`probeRecovered`、INV-4 两连成功门对齐。看板 success 率轻微上移属修正而非漂移。
4. **`last_gateway_ok` 语义**：停放行如实记录 gateway 轮结果（不再是硬编码 TRUE/NULL 二选一）。

## 四、测试

```text
go build ./...                                  0 错误
go test ./bg/ -count=1                          ok 25.1s（含本轮新增/更新钉桩）
go test ./... -count=1                          297 包全绿，0 FAIL
```

关键钉桩：
- `bg/probe_root_cause_test.go`（新增）：分类器 24 案例表驱动（含计费形 400/422 vs 契约形、双语余额关键词）、退避策略（protocol attempt1 短梯 / attempt2 起 6h / node 原样）、标注幂等与哨兵不破坏；
- `bg/probe_service_test.go`：`TestProbeServiceGatewayFailureDoesNotLadderHealthyNode`（direct OK + gateway 503 → 终态 Success + 无 NextRunAt + 观测元数据）、`TestProbeServiceLegacyUnpinnedGatewayCannotRecover`（pin 缺口不重臂节点）；
- `bg/probe_recovery_authority_test.go`：URSM direct-only + 终态结算 + 调解器 `next_retry_at` 门；
- `bg/node_probe_ladder_regression_test.go`：锁 runOne 成功行 NULL 字面量停放形状 + 禁复合 success 复发。

## 五、遗留风险（诚实清单）

1. **队列任务代际的 attempt 重置**：queue 路径 attempt 取自 `task.Attempt`（每任务代从 1 起）。protocol/404 对在每个任务代（≤7 尝试或 expires）里前两次是快探（5s/30s 短梯），attempt≥2 才进 6h 停放；任务代终止后由泵再生成。即"每代 2 个快探 + 6h 节奏"，非严格每 6h 恰一次。属 pre-existing 口径（404-park 同款），本轮未动。
2. **存量污染行**：上一版本已写入的成功行（`last_direct_ok=TRUE` + 非空 `last_err_code`）无数据迁移，靠下次相遇收敛（必要性门 skip 删镜像行，或下一次探测成功重写干净停放形状）。收敛前每对最多多探一轮。
3. **未做生产实测**：`llmgw_node_probe_root_cause_total` 的真实分布、协议形占比、探测量降幅均需部署后观察；本文档的量级判断全部来自代码路径推演。
4. **36h lookback claim 不查 `next_retry_at`**：但它只选 `cmb.available=FALSE`（真异常节点）+ 5min claim 租约，频率可接受，登记不动。
5. **featured 加速乘数**对 node 形失败仍会把梯砍半（设计如此，未被本轮波及）。

## 六、下一轮候选

1. 部署后核对指标：`root_cause_total` 分布、必要性门 skip 计数、`node_probe_runs` 日增量对比（预期显著下降）。
2. 评估队列任务代际 attempt 重置（遗留 1）：让失败退避读 `node_probe_state.consecutive_failures` 而非 `task.Attempt`，或泵入队时透传连续失败计数。
3. `reason_detail` 已携带根因后缀，可考虑 admin 自检看板按 root_cause 分组展示（协议 vs 节点 vs 网关饼图）。
