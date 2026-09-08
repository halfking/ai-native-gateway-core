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
