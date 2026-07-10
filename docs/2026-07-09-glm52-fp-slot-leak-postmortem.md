# Postmortem: GLM-5.2 "All candidates failed" — FP Slot 泄漏

- 日期：2026-07-09
- 严重度：P1（单个模型全部请求失败）
- 影响：llm.kxpms.cn 上 glm-5.2 请求 100% 返回 503，持续约 3 小时
- 状态：已修复并部署到 154

## 1. 症状

用户请求 `glm-5.2` 返回：

```
No available provider for model 'glm-5.2'. All 7 candidates failed.
```

网关日志反复出现：

```
INFO  cred_fp_slot saturated, credential_id=3, provider_id=24
INFO  cred_fp_slot saturated, credential_id=4, provider_id=24
INFO  cred_fp_slot saturated, credential_id=16, provider_id=581
ERROR executor failed: all N candidates failed: cred_fp_slot saturated for credential 4
```

## 2. 根因

`domains/streaming/executors/executor.go` 在释放指纹槽（fingerprint slot）时使用了**请求自身的 context**：

```go
defer func() {
    if fpLease != nil {
        e.FpSlots.Release(params.R.Context(), fpLease) // ← BUG
    }
}()
```

当客户端在上游响应前断开连接（超时 / Ctrl+C / 网络抖动），`params.R.Context()` 被取消，`Release` 内部的 Redis 调用立即返回 `context.Canceled`，槽位 key 未刷新、也未删除，保留完整的 30 分钟 TTL。

槽位泄漏累积过程：

1. 突发请求占用 slot → 上游慢 → 客户端断开 → Release 失败 → slot 泄漏
2. 泄漏槽位不会因为请求结束而释放，只能等 30 分钟 Redis TTL
3. `fp_slot_limit` 默认 20，泄漏满后该凭据的所有后续请求都拿到 `saturated`
4. 多个凭据同时泄漏 → 路由器遍历全部候选都被 `saturated` 挡掉 → 503

> 说明：本次不是模型名映射问题。智谱、NVIDIA NIM 都原生支持 `glm-5.2`。前期一度误判为上游不支持，是因为 saturated 把可用凭据挡在了路由之外，只剩余额不足的凭据被尝试。

## 3. 数据证据

- 数据库侧 `credentials.availability_state = ready`、`circuit_state = closed`、`model_offers` 正常，7 个候选中 5 个 `is_routable = true`。
- Redis 侧查 `llmgw:cred_fp_slot:*` 看到槽位被持有但 TTL 远未到期，说明是"活的泄漏"而非过期残留。
- 重启网关后立刻恢复，随后又快速复现，符合"内存/Redis 状态泄漏"特征。

## 4. 修复

### 4.1 统一释放入口（`executor.go`）

新增 `releaseFpLease` helper，所有 FP slot 释放都必须走它，使用独立的 `context.Background()` 加 3 秒超时，与请求生命周期解耦：

```go
func releaseFpLease(m *credentialfpslot.Manager, lease *credentialfpslot.Lease) {
    if lease == nil || m == nil || !m.Enabled() {
        return
    }
    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()
    m.Release(ctx, lease)
}
```

executor.go 中 3 处 `e.FpSlots.Release(params.R.Context(), fpLease)`（熔断跳过、并发限流跳过、defer 兜底）全部改为调用 `releaseFpLease`。

### 4.2 Release 增加有限重试（`credentialfpslot/slot.go`）

- 对**非 context 类**的瞬时 Redis 错误重试最多 3 次（50ms、100ms 退避）。
- context 已取消/超时时立即退出，不浪费重试。
- 失败时以 `slog.Error` 上报，便于监控捕获。
- slot 不再因单次 Redis 抖动永久泄漏；最坏情况下也保留 30 分钟 TTL 自我兜底。

### 4.3 部署

154 服务器以 systemd 方式部署（非 docker）。交叉编译后替换 `/opt/llm-gateway-go/llm-gateway-go`，旧版本备份为 `*.bak-<ts>-before-fpslot-fix`。

## 5. 验证

- 单元测试：`go test ./credentialfpslot/ ./domains/streaming/executors/` 全部通过。
- 生产验证：部署后 `curl https://llm.kxpms.cn/v1/chat/completions -d '{"model":"glm-5.2",...}'` 返回 200，延迟 2–4s，日志无 `saturated`。
- 持续观察未复现。

## 6. 后续改进（建议）

1. **监控指标**：暴露 `fp_slot_usage_ratio{credential_id}` 与 `fp_slot_release_failures_total`，配 Prometheus 告警（使用率 > 0.9 持续 5 分钟）。
2. **后台回收**：`credentialfpslot` 已有 reclaim 机制（基于 active gate + idle sweep），确认其已随网关启动，覆盖"Release 彻底失败"的兜底场景。
3. **URSM 路径对齐**：`domains/ursm/routing.go` 的 `fpSlotMgr.Release` 同样使用请求 ctx，建议后续一并改为独立 ctx（当前 URSM 未在 154 启用，暂不影响）。
4. **文档**：将"释放资源一律用独立 context"写入团队 Go 编码规范。
