# 01 自检及时恢复 — 审计记录

日期：2026-09-08 · 基线：`04c7b250a`（首轮合入 main）

## 发现与修正

| # | 级别 | 发现 | 修正 |
|---|---|---|---|
| A1 | P0 | `MarkNodeProbeHealthy` 由 executor 在**每次**业务成功时调用（`EffectCancelProbeBackoff`），首轮写回让 `credentials` 行每次成功都被无条件 UPDATE → 热行写放大 | `healthyCredentialSQL` 增加 `IS DISTINCT FROM` / `quota_recover_at IS NOT NULL` 守卫；`healthyBindingSQL` 只碰 `available=FALSE` 且非 `manual%` 的 binding。健康态 0 行 |
| A2 | P1 | 写回把 `quota_state='ok'` 但留下 `quota_recover_at`，与 `writeHealth` 2026-07-06 修正不一致 | 同步 `quota_recover_at = NULL` |
| A3 | P1 | 凭据自检窗口收到 15m 后仍按 `last_error_at DESC` 挑选，LIMIT 1/5m 会让 3 个高噪凭据饿死其余 | `ORDER BY COALESCE(l.last_at,'1970-01-01') ASC` 最久未检优先 |
| A4 | P1 | 当天成功扫描对健康节点 15m 复扫，队列模式下每 15m 最多 40 对 × 2 轮 | 健康节点 1h 复扫；新增 `cmb.available=FALSE` 必扫条件 |
| A5 | P2 | `ProbeBackoffForKind(KindRateLimit)` 落到通用链，429 后 5s 就重试 | 策略间隔（3m/5m/15m）作为通用链下限 |
| A6 | P2 | `classifyProbeErrCode` 未知码默认返回 `KindAuth`，语义误导 | 返回空 kind → 通用链 |
| A7 | P2 | `ProbeNow` 无 `default_probe_model` 时取 `loadBoundRawModelsAll()[0]`：无序且可能是不可用 binding | `fallbackProbeModel` 先 available 后 all，排序取首 |
| A8 | P2 | `node_probe.go` 头注释“双轮必须成功”、pump 注释“10 分钟”与代码不一致 | 更新注释 |

## 复核未改项

- `applyOutcome` 直连成功恢复 binding：网关轮失败仍返回 `ProbeQueueFailed` 继续走退避重探，`mirrorNodeProbeState` 记 `last_direct_ok=TRUE/last_gateway_ok=FALSE`；已在头注释写明语义。
- 余额额度 tick 改 `ProbeNowAsync`：按凭据去重，余额耗尽凭据数量小，接受 2m 节拍。
- 硬配额（`balance_exhausted`/`permanently_exhausted`）被节点级成功清零：与 `writeHealth` 2026-08-07 P0 守卫同一契约——实测成功比历史 quota_state 更新。

## 验证

- `go build ./...` / `go vet ./bg/` 通过
- `go test ./bg ./errorsx ./domains/nodehealth ./domains/credential ./domains/streaming/...` 全绿
- `golangci-lint` 本机版本（go1.26 构建）低于项目 go1.27.1，无法运行；以 `go vet` 代替

## 未做

- 未连生产库做 `EXPLAIN` / 实际写入验证；未做 UI 实测（本任务无前端改动）
