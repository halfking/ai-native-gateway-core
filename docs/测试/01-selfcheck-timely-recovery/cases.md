# 01 自检及时恢复 — 用例

| ID | 优先级 | 场景 | 期望 |
|---|---|---|---|
| SC-01 | P0 | `timeout` / `network_error` attempt 4+ | 退避 ≤ 60s，不再爬到 1h/6h |
| SC-02 | P0 | `quota_periodic` / `quota_balance` | 固定 5m / 2m |
| SC-03 | P0 | 直连 OK、网关失败 | binding 恢复，不因网关轮标红 |
| SC-04 | P0 | `MarkNodeProbeHealthy` | 同步 cmb + credential（含 quota_state=ok） |
| SC-05 | P0 | `ProbeNow` 成功 | 调用 `onQuotaRecovered(..., "probe_now")` |
| SC-06 | P0 | 当天成功节点扫描 | 只选今日成功、跳过 15 分钟内已健康 |
| SC-07 | P1 | 自检窗口 | 失败凭据 15 分钟可再检，不再 24h |
| SC-08 | P1 | 队列 pump holdoff | ≤ 60s，不把 5s 重试埋进 10 分钟 |
| SC-09 | P0 | 零成本类（timeout/network/rate_limit/quota）探测起点 | 每次 15s，与"未产生 token 花费则高频"需求一致 |
| SC-10 | P0 | 零成本类退避台阶 | 连错 10 次起放宽：30s → 60s → 5m，封顶 5m |
| SC-11 | P0 | 防 429 风暴闸门 | 限流错误不走"即时探测"通道（≤5s），避免多重 HTTP 429 引发雪崩式探测（`domains/credentialstate/manager.go` 移除 `KindRateLimit`） |
| SC-12 | P0 | ConsecutiveFails 钳位 | 连续失败计数上限 10，与既有退避阶梯（30s/2m/5m）一致，死后回滚不会永久停留在最慢档 |

## 2026-09-08 audit：本轮修正（diff → commit `SC-11/SC-12`）

0. **SC-11**：`domains/credentialstate/manager.go:353-359` 错误分类从 `probeImmediately` 瞬时集合移除 `KindRateLimit`。原实现违背 FR-3（429 不允许 5s 快打），短暂流控风暴会触发大量立即探测。改由 `activeProbeThreshold`（默认 2 次）门控。

1. **SC-12**：同文件 `UpdateOnFailure`（`manager.go:313`）新增 `consecutiveFailsCap=10`，与 30s/2m/5m 阶梯语义自洽。回滚后计数被钳制在 10 不再无限增长，修复后可退回 30s 快速档。

2. **删除 `bg/probe_zero_cost_backoff_test.go`**（未跟踪文件）。它是对 `probeEngine`/`MOpsChannelProbe` 的早期方言接触尝试。实际 `bg` 包无该类型——探测由 `circuit_breaker.go`、`worker.go` 等支撑，类型语义不匹配会导致 "cases 与现有实现不符"的冲突。当前 spec 用例 SC-09/SC-10 的语义已由 `probe_recovery_policy.go`（`NetworkProbeBackoffChain`：`[5s,15s,30s,60s]`）+ `errorsx/automatic_probe_policy.go`（`Fixed=true` 时直接钳位为 `Interval`）实际覆盖。

3. **回归**：`go test ./... -count=1` 全量通过，`go build ./... && go vet` 干净。

详见 `results.md` / `audit.md`（后续补 audit）。
