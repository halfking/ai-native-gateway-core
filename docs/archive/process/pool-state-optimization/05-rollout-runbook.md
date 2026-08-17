# 05 - URSM v2 渐进切换说明

> 权威操作手册：[`docs/runbooks/ursm-v2-cutover.md`](../runbooks/ursm-v2-cutover.md)
> 路径固定为 `off → shadow（双轨核对）→ canary（1% → 5% → 10%）→ authoritative`。252 保持 deferred/fail-closed；只沿 `local → dev → 245 → 154` 推进。

## 模式

| mode | 权威状态源 | 说明 |
|---|---|---|
| `off` | legacy credentialstate | 默认与紧急回退 |
| `shadow` | legacy credentialstate | 开启 double-write 后全量 outcome 旁写，采样双算；不改变生产路由 |
| `canary` | 命中请求使用 URSM v2 | 稳定百分比选中；持续采样 legacy/v2 diff |
| `authoritative` | URSM v2 | 全量 v2；未 ready/Redis 读取失败时保护性拒绝 |

## Shadow 双轨门禁

```text
URSM_V2_MODE=shadow
URSM_V2_SHADOW_DOUBLE_WRITE=true
URSM_V2_SHADOW_SAMPLE_RATE=0.01
# 删除 URSM_V2_CANARY_PERCENT
```

245 连续 7 天必须同时证明：

1. outcome 旁写：`llm_gateway_ursm_v2_shadow_records_total{result="failed"}` 增量为 0，`recorded` 增量大于 0。
2. 候选 diff：`ursm_shadow_diff_total` 的 `availability`、`order`、`top1`、`error`、`not_ready` 增量均为 0，`identical` 至少 10,000。

代码真实 label 是 `availability`、`order`，不能使用历史错误名称 `availability_mismatch`、`order_mismatch`。同时相对 24 小时基线，错误率/无候选率不得增加，P99 增幅不得超过 5%。

可用以下只读工具确认 scrape 的双轨标签契约；跨实例趋势仍由权威 runbook 的 PromQL `increase()` 查询判断：

```bash
bash scripts/verify-ursm-v2-rollout.sh --stage shadow \
  --url http://127.0.0.1:8781 --token "$LLM_GATEWAY_ADMIN_API_KEY"
```

## Canary 阶梯

仅在 shadow 全部达标后执行：

| 百分比 | 最短观察期 | 升档条件 |
|---|---:|---|
| 1% | 24 小时 | sidecar failed=0、候选 diff=0、canary 增长、fallback 不增长、性能门禁通过 |
| 5% | 48 小时 | 同上 |
| 10% | 7 天 | 同上；完成后再单独评审 authoritative |

配置保持 `URSM_V2_MODE=canary` 和 `URSM_V2_SHADOW_SAMPLE_RATE=0.01`，仅将 `URSM_V2_CANARY_PERCENT` 依次改为 `1`、`5`、`10`。出现 canary fallback、候选 diff、outcome 写失败或性能越线，立即回 `shadow` 并删除 canary percent。

## Authoritative 与回退

authoritative 需另行审批，并要求全量 tenant-aware coverage、recovery 演练、`LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true`。启动前关 `ready`，coverage 完整才开 gate；Redis 读失败不回退 legacy。

回退只能持久修改真实 systemd EnvironmentFile 或 Kubernetes Deployment：设 `URSM_V2_MODE=off`，删除 double-write、sample rate、canary percent 后重启/滚动发布。验证 legacy manager 与 `routing_state_source_total{source="off"}`，并保留 `ursm:v2:*` 现场数据供复盘。
