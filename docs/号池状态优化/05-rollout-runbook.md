# 05 - 一次性全量切换说明

> 权威操作手册：[`docs/runbooks/ursm-v2-cutover.md`](../runbooks/ursm-v2-cutover.md)
> 路径固定为 `off → shadow（核对）→ authoritative（全量）`，不使用 canary。

## 模式

| mode | 权威状态源 | 说明 |
|---|---|---|
| `off` | legacy credentialstate | 默认与紧急回退 |
| `shadow` | legacy credentialstate | v2 outcome 双写、采样双算，不改变路由 |
| `authoritative` | URSM v2 | 全量 v2；未 ready/Redis 读取失败时保护性拒绝 |

`ModeCanary` 代码仍保留兼容，但生产环境和本文不使用 `URSM_V2_CANARY_PERCENT`。

## 全量切换的硬门禁

1. 全量 migration CLI 已从 `credentials.tenant_id` 生成 tenant-aware node key，并写入全局 `ursm:v2:meta:coverage`；带 `--tenant-id` 的局部 migration 只写独立 tenant manifest，不能作为 authoritative gate 输入。
2. coverage manifest 每个 key 都存在且含 `generation`、`available`；legacy-only key 不计入 authoritative 覆盖。
3. Shadow 连续 7 天 availability/order mismatch、error/not-ready、sidecar failed 均为 0，且 identical 样本量达到 runbook 要求。
4. `LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true`，Redis restart/recovery 演练通过。
5. strict authoritative read-error / not-ready 拒绝测试通过。
6. snapshot writer 正常；snapshot 仅作审计，尚不是 Redis restore 源。

## Shadow

```text
URSM_V2_MODE=shadow
URSM_V2_SHADOW_DOUBLE_WRITE=1
URSM_V2_SHADOW_SAMPLE_RATE=0.01
```

Shadow 中必须保留 legacy manager；`ursm_shadow_diff_total` 是 legacy/v2 比较指标，`llm_gateway_ursm_v2_shadow_records_total` 是 outcome 双写指标。

## Authoritative

```text
URSM_V2_MODE=authoritative
LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true
```

启动时先将 `meta:ready` 置为 0，再验证 coverage manifest；只有完整覆盖才开门。authoritative 不装配 legacy manager，不使用 DB-only 或 degraded 旁路替代 v2 状态。

## 回退

持久修改真实 systemd EnvironmentFile 或 Kubernetes Deployment，设置 `URSM_V2_MODE=off` 并重启/滚动发布。回退后必须验证 legacy manager、off state-source 和 legacy outcome 写入恢复。保留 `ursm:v2:*` 现场数据，不删除。
