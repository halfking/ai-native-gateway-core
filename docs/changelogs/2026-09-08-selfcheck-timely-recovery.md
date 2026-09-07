# 2026-09-08 — 自检及时恢复

当天成功过的模型节点要能及时、不过量地探测；错误状态、额度恢复、偶发网络问题必须尽快写回路由可见面。

## 根因

- 节点探测失败一律走 5s→6h 长链，网络抖动在第 5 次后要等 1 小时
- 队列 pump holdoff 10 分钟，把 5s 重试埋掉
- ProbeService 要求直连+网关双轮成功才恢复 binding；网关 pin 失败会把实际上游可用的节点标红
- `MarkNodeProbeHealthy` / 业务成功只改 `node_probe_state`，不写 cmb / credential
- `updateCredentialHealth` 拒绝硬配额行，充值后状态不翻
- `ProbeNow` 成功不调 `onQuotaRecovered`，候选缓存继续排除该凭据
- 每日审计 72h/24h，没有“今天成功过的模型”扫描
- 凭据自检 24 小时才再检一次

## 修复

| 项 | 行为 |
|---|---|
| 网络/超时退避 | 5s / 15s / 30s / 60s，封顶 60s |
| 额度退避 | 固定 2m / 5m |
| pump holdoff | 10m → 45s |
| applyOutcome | 直连成功即恢复 |
| 业务成功 | 同步 cmb + credential + `quota_state=ok` |
| ProbeNow | 无 default_probe_model 时用 binding 回退；成功通知 `probe_now` |
| 余额额度 tick | 优先 `ProbeNowAsync` |
| 当天成功扫描 | 15 分钟、最多 40 对、跳过 15 分钟内已健康 |
| 凭据自检窗口 | 24h → 15m |

## 验证

`go test ./bg ./errorsx -count=1`
