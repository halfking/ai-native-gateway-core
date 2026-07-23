# 2026-07-24 — 路由解析与 chat-completion 节点可见性差异修复

## 现象（2026-07-24 报告）

老板在生产环境复现：

1. 请求 `glm-5.2`，`/api/routing/resolve`（路由解析 tab）显示 **2 个可路由节点**。
2. 实际 `/v1/chat/completions` 请求得到 **503 no available nodes**（错误码 `no_candidate`）。
3. **普联**供应商下明明有可用的模型，但在路由解析结果中**看不到**。
4. 把普联下所有模型清空再"重新获取"后，普联模型**重新出现在**路由解析里。
5. 老板要求：**自检应该对过去不可用的节点及时做探测并修正状态**。

## 根因

视图 `v_routable_credential_models`（迁移 417，文件
`sql/migrations/startup/417_route_excludes_failed_node_probes.sql`）对
`(credential_id, raw_model_name)` 是否可路由要同时满足：

```sql
cmb.available = TRUE
AND cmb.unavailable_reason IS DISTINCT FROM 'manual'
AND NOT EXISTS (
    SELECT 1 FROM node_probe_state nps
    WHERE nps.credential_id = cmb.credential_id
      AND nps.raw_model_name = pm.raw_model_name
      AND nps.last_direct_ok = FALSE
      AND nps.next_retry_at > now()
)
```

`bg/credential_recovery.go` 的 60s tick 此前只恢复 5 类：

| 类别 | 恢复位置 | 备注 |
|---|---|---|
| `availability_state` ('cooling'/'rate_limited'/'auth_failed'/'unreachable') | `bg/credential_recovery.go:56-92` | 通过 `availability_recover_at` |
| `quota_state` (periodic_exhausted) | `bg/credential_recovery.go:99-113` + `:229-241` | 通过 `quota_recover_at` |
| `circuit_state` (open) | `bg/credential_recovery.go:123-137` | 通过 `cooling_until` |
| `health_status` ('unreachable'/'auth_failed'/'error') | `bg/credential_recovery.go:169-188` | 通过 `health_checked_at` |
| `cmb` 的 `unavailable_reason='mnf_cooling'` | `bg/credential_recovery.go:190-205` + `:252-307` | 通过 `unavailable_at + N min` |

**而 `cmb.unavailable_reason IN ('continuous_failure', 'probe_*')` 的绑定永远不会自动恢复** —— 这两类恰好是 `credentialhealth/checker.go:markDegraded` 和 `bg/node_probe.go:updateBindingAvailability` 在真实失败后写下的最常见原因。

`NodeProbeWorker.Submit` 是**错误触发**的入口（state manager 失败 ≥ 2 次 / `OnNoCandidates`），如果业务流量成功绕开某个曾失败的节点、路由从不再选中它，则 `Submit` 永远不会被调用，于是它会一直在视图外、直到运维手动 clear+re-fetch。

## 修复

`bg/credential_recovery.go` 新增分支 `recoverExpiredBindings`：

1. **SELECT-only**：仅查询**满足所有安全条件**的 `(credential_id, raw_model_name)` 对：
   - `cmb.available = FALSE`
   - `cmb.unavailable_recover_at IS NOT NULL AND <= now()`
   - `cmb.unavailable_reason IN ('continuous_failure') OR cmb.unavailable_reason LIKE 'probe_%'`
   - 不触碰 `manual*` / `admin_protected`（运维人员的选择必须尊重）
   - 凭据必须 `status='active'` / `lifecycle_status='active'` / `availability_state='ready'`（上游本身冷却的不动）
   - provider 必须 `enabled=TRUE` 且未手动禁用
   - **不打扰** `node_probe_state.paused=TRUE` 或 `next_retry_at > now()` 的行（mid-cycle ladder 必须按既定节奏走完）
2. **不直接翻转 `cmb.available=TRUE`**：而是把每个 `(cred, model)` 对通过 `NodeProbeWorker.Submit` 投递，**让探测工作器的成功路径作为权威写者**（`runOne` success branch 同时 `updateBindingAvailability=TRUE` + `updateObservedState` + `invalidateCandidateCache` + 写 `node_probe_state.last_direct_ok=TRUE`，与手动 `TriggerManual` / `TriggerAllSync` 的回路完全一致）。
3. **候选缓存立即失效**：按唯一 `credential_id` 调 `InvalidateCandidateCacheForCredential`，下一个 chat 请求无需等 30s `candCache` TTL 才能看到恢复的绑定。
4. **向后兼容**：未注入 `probeSubmitter` 时（如旧的 `LLM_GATEWAY_USE_NEW_PROBE_MODE=false` 模式），函数 no-op 返回 nil，不破坏既有的 legacy 探测栈。
5. **错误向上传播**：`db.Query` 错误用 `fmt.Errorf("%w")` 包装后返回，让 `recover()` 的 warn 日志能诊断 DB 抖动而不是静默吞掉。

## 接线（cmd/gateway/main.go:2236-2255）

```go
// 2026-07-24 P0 fix: bg/credential_recovery's 60s tick scans
// cmb.available=FALSE rows whose unavailable_recover_at has elapsed
// and hands them to NodeProbeWorker.
if credRecovery != nil {
    credRecovery.SetProbeSubmitter(func(credID int, model string) {
        nodeProbe.Submit(credID, model, "default", "expired-binding-recovery")
    })
    credRecovery.SetInvalidateCandidateCache(provider.InvalidateCandidateCacheForCredential)
    slog.Info("credRecovery: expired-binding probe submitter wired")
}
```

注：`credRecovery.Start` 在 line 1902，但 `nodeProbe` 构造在 line 2211 之后——所以必须用 setter 在 nodeProbe 构造后再注入。60s tick 第一次跑时 setter 已就位（启动 > 60s），即使启动后立刻 tick 也是安全的（`recoverExpiredBindings` 检测到 `r.probeSubmitter == nil` 时直接返回 nil）。

## 测试

`bg/credential_recovery_test.go` 新增 4 个回归测试，全部覆盖核心 seam：

| 测试 | 验证什么 |
|---|---|
| `TestExpiredCmbRecoverySQLGuards` | SQL 包含全部 17 项安全守卫（cmb/reason/cred/provider/node_probe_state 正反两面） |
| `TestRecoverExpiredBindingsEnqueuesProbes` | pgxmock 注入 2 行 `(cred, model)`，验证 `probeSubmitter` 按行调用 + `invalidateCandidateCache` 按唯一 cred 调用 |
| `TestRecoverExpiredBindingsSkipsWhenNoRows` | 空结果时无回调、无错误 |
| `TestRecoverExpiredBindingsReturnsErrorOnQueryFailure` | DB 出错时回调不执行、错误向上传播 |

## 验证证据

```text
$ go test ./bg/ -run 'TestExpiredCmbRecoverySQLGuards|TestRecoverExpiredBindingsEnqueuesProbes|TestRecoverExpiredBindingsSkipsWhenNoRows|TestRecoverExpiredBindingsReturnsErrorOnQueryFailure' -count=1 -v
=== RUN   TestExpiredCmbRecoverySQLGuards
--- PASS: TestExpiredCmbRecoverySQLGuards (0.00s)
=== RUN   TestRecoverExpiredBindingsEnqueuesProbes
2026/07/24 00:10:14 INFO expired-binding probe recovery queued pairs=2 unique_credentials=1
--- PASS: TestRecoverExpiredBindingsEnqueuesProbes (0.00s)
=== RUN   TestRecoverExpiredBindingsSkipsWhenNoRows
--- PASS: TestRecoverExpiredBindingsSkipsWhenNoRows (0.00s)
=== RUN   TestRecoverExpiredBindingsReturnsErrorOnQueryFailure
--- PASS: TestRecoverExpiredBindingsReturnsErrorOnQueryFailure (0.00s)
PASS
ok      github.com/kaixuan/llm-gateway-go/bg  0.545s
```

```text
$ go test ./bg/ ./provider/... ./domains/credentialstate/... -count=1
ok  github.com/kaixuan/llm-gateway-go/bg                  0.658s
ok  github.com/kaixuan/llm-gateway-go/provider            0.824s
ok  github.com/kaixuan/llm-gateway-go/domains/credentialstate 0.820s
```

```text
$ golangci-lint run --no-config ./bg/credential_recovery.go ./bg/credential_recovery_test.go
0 issues.
```

## 改动清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `bg/credential_recovery.go` | 修改 | 新增 `credentialRecoveryDB` 接口、`SetProbeSubmitter` / `SetInvalidateCandidateCache` setter、`expiredCmbRecoverySQL()` SELECT、`recoverExpiredBindings()` 方法；`recover()` 末尾挂新分支 |
| `bg/credential_recovery_test.go` | 修改 | 新增 4 个回归测试 |
| `cmd/gateway/main.go` | 修改 | 在 nodeProbe 构造之后向 credRecovery 注入 submitter 与 invalidator |
| `CHANGELOG.md` | 修改 | Unreleased → Fixed 新增本修复条目 |
| `docs/changelogs/2026-07-24-expired-binding-probe-recovery.md` | 新增 | 本文档 |

## 风险与遗留

- **风险**：在大型部署上 `expiredCmbRecoverySQL` 单次扫描可能返回大量历史冷却行；已加 `LIMIT 50` 防止单 tick 风暴。下一 tick 继续扫描剩下的行，最终全部探测一次（探测工作器本身的 30s tick + 5s/30s/60s/5m/1h/2h/24h ladder 自带节流）。
- **遗留**：现有未恢复的 `continuous_failure` / `probe_*` 绑定需要等 60s tick 第一次跑过才会被探测一次；如需立即全量恢复，可在 admin 上调用 `POST /api/providers/{id}/probe-history/trigger-all`（已存在）做全量预热。
- **预存在的 2 个 build/vet 失败** 与本修复无关：`cmd/gateway/system_monitor_adapter.go` 的 `GetMetricsCollector` 接口缺失（SystemMonitor Phase 3 阶段的中间态）、`domains/streaming/executors/router_scoring_test.go:TestLatencyScore_SaturationCurve/latency_unicode` 数值断言差异（与本修复无关的既有 flaky）。