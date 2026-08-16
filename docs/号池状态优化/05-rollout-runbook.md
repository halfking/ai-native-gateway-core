# 05 - 灰度、回退与运维说明

> 本文保留 URSM v2 的设计语义。实际切换命令、验收门槛和回滚步骤以
> [`docs/runbooks/ursm-v2-cutover.md`](../runbooks/ursm-v2-cutover.md) 为唯一权威来源。
> 最后核对：2026-08-16。

## 1. 当前模式语义

| mode | 生产路由权威 | v2 outcome | v2 路由计算 |
|---|---|---|---|
| `off` | legacy | 不写 | 不计算 |
| `shadow` | legacy | double-write 开启时全量旁写 | 按 sample rate 双算，只观测 |
| `canary` | legacy/v2 按 percent 灰度 | 命中 v2 的请求写入 | 命中请求采用 v2 |
| `authoritative` | v2 | 全量 | 全量 |

Redis 可用时，`off` 也会构造 no-op v2 Manager；不能以 Manager 是否为 nil 判断模式。只有 `authoritative` 禁止装配 legacy `credentialstate.Manager`。`shadow` 必须保留 legacy 读写权威。

## 2. 已接线环境变量

```text
URSM_V2_MODE=off|shadow|canary|authoritative
URSM_V2_CANARY_PERCENT=0..100
URSM_V2_SHADOW_DOUBLE_WRITE=0|1
URSM_V2_SHADOW_SAMPLE_RATE=0..1
URSM_V2_COOL_SECONDS=<positive integer>
URSM_V2_LRU_SIZE=<non-negative integer>
URSM_V2_LRU_SOFT_TTL_MS=<positive integer>
```

`URSM_V2_SHADOW_SAMPLE_RATE` 默认 `0.01`，只控制 observe-only diff 的采样，不降低 shadow outcome 的 100% 双写。`URSM_V2_CANARY_TENANTS`、`URSM_V2_CANARY_MODELS` 和 `URSM_V2_FORCE_RECOVERY` 仍未接入环境变量。

## 3. Shadow 数据与指标

Shadow 同时产生两类可观测数据：

1. outcome 双写：legacy 写旧状态，v2 写 tenant-aware Redis node/window key。
2. 路由双算：Router 返回 legacy 顺序，采样计算 v2 顺序并计数比较结果。

生产指标：

```text
llm_gateway_ursm_v2_shadow_records_total{result="recorded|skipped|failed"}
ursm_shadow_diff_total{type="identical|availability_mismatch|order_mismatch|not_ready|error|sampled_out"}
routing_state_source_total{source="..."}
```

`ursm_shadow_diff_total` 不使用 tenant、request、model 或 credential 标签，避免高基数。mismatch 的具体 request id 与两侧顺序只写结构化日志。

Prometheus 必须至少保留 8 天。进程 counter 会在重启后归零，因此 7 天切换门槛必须通过 Prometheus `increase(...[7d])` 聚合，不得读取当前进程值代替。

## 4. Ready 与恢复

```bash
redis-cli GET ursm:v2:meta:ready
redis-cli HGETALL ursm:v2:meta:epoch
```

进入 recovery：

```bash
redis-cli SET ursm:v2:meta:ready 0
redis-cli HINCRBY ursm:v2:meta:epoch counter 1
redis-cli HSET ursm:v2:meta:epoch reason "ops: <ticket>" started_at "$(date -u +%FT%TZ)"
```

默认不因 Redis 故障自动 fail-open。必须保通时，持久设置 `URSM_V2_MODE=off` 并滚动重启。不要删除 `ursm:v2:*`，现场数据用于复盘。

## 5. 切换约束

本 P0-Z1 只批准 `off -> shadow`：

```text
URSM_V2_MODE=shadow
URSM_V2_SHADOW_DOUBLE_WRITE=1
URSM_V2_SHADOW_SAMPLE_RATE=0.01
```

进入 canary 前要求 shadow 连续 7 天：

- availability mismatch = 0
- order mismatch = 0
- error/not-ready = 0
- shadow write failed = 0
- identical 有足够样本量
- 请求错误率、无候选率、P99 不劣于基线

Canary 只能用 `URSM_V2_CANARY_PERCENT`，按 `1 -> 5 -> 10 -> 25 -> 50 -> 100` 逐级推进。

`authoritative` 本次禁止切换。tenant-aware 数据覆盖、Redis 恢复演练、snapshot 连续写入、fallback 语义和独立变更审批全部完成后，才能另行评审。

## 6. 设计入口

- Redis 协议：`02-redis-protocol.md`
- 热路径：`03-hotpath-and-logging.md`
- 恢复与持久化：`04-recovery-persist-rollout.md`
- 可执行 runbook：`../runbooks/ursm-v2-cutover.md`
