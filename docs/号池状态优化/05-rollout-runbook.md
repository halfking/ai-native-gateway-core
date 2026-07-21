# 05 — 灰度、回退与运维 Runbook

> 范围：URSM v2 上线期的模式切换、强制恢复、紧急回退、复盘。
> 设计上下文见 `00-overview.md`、`04-recovery-persist-rollout.md`；本文不重复架构。

## 1. 模式与开关

### 1.1 四个模式

| mode | 生产决策 | v2 计算 | 写 Redis | 何时用 |
|---|---|---|---|---|
| `off` | 旧路径 | 否 | 否 | 默认；紧急回退 |
| `shadow` | 旧路径 | 是，记 diff | 是（推荐） | 灰度前攒窗口 |
| `canary` | 命中比例走 v2 | 是 | 是 | 按比例放量 |
| `authoritative` | 全量 v2 | 是 | 是 | 收敛终态 |

### 1.2 真正生效的环境变量

`domains/ursm/v2/config.go:LoadFromEnv()` **只读两个变量**——其他字段必须由调用方显式构造 `Config{}` 注入，或通过未来的 settings 注册表下发。

| 变量 | 字段 | 取值 | 默认 |
|---|---|---|---|
| `URSM_V2_MODE` | `Config.Mode` | `off`/`shadow`/`canary`/`authoritative` | `off` |
| `URSM_V2_CANARY_PERCENT` | `Config.CanaryPercent` | 0–100 | 0 |

`DefaultConfig()` 还给出的非环境变量字段（运维改值只能走代码/配置中心）：

- `ShadowSampleRate=0.01` — shadow 模式采样率
- `RecordTimeoutMs=20` — sidecar `RecordRequest` 写超时
- `RecoveryBlockOnMiss=true` — 未 ready 时路由保护性拒绝
- `PersistIntervalSec=60` — 分钟快照写入间隔
- `RedisKeyPrefix="ursm:v2:"` — 所有 key 前缀
- 窗口 TTL：1m=90s / 5m=6m / 30m=35m；node TTL=60m

> 注：计划文档（`04-recovery-persist-rollout.md` 3.2 节）里写到的 `URSM_V2_CANARY_TENANTS` / `URSM_V2_CANARY_MODELS` / `URSM_V2_SHADOW_SAMPLE_RATE` / `URSM_V2_FORCE_RECOVERY` 当前**没有**在 `LoadFromEnv()` 内读取；若需要白名单/采样率/强制恢复，按下面 1.4 / 2.2 的方式操作 Redis 键或代码侧注入。

### 1.3 接入判定（main.go 守卫）

`cmd/gateway/main.go` 仅当以下**全部**成立才构造 `URSMv2`：

1. `redisClientForCache != nil`（即 `LLM_GATEWAY_REDIS_ADDR` 已配且 ping 通）
2. `URSM_V2_MODE ∈ {shadow, canary, authoritative}`（否则连 `SetReady(true)` 都不会调）

只要 `URSM_V2_MODE=off`，Router 与 Executor 上的 `URSMv2 == nil`，侧车调用全部走 `if e.URSMv2 != nil` 守卫，旧 `credentialstate` 路径完整接管。

### 1.4 Canary 命中规则

`domains/ursm/v2/rollout/controller.go` 判定顺序：

1. `CanaryTenants` 命中 → v2
2. `CanaryModels` 命中 → v2
3. 否则 `sha256(tenant|model|requestID)` 头 4 字节大端取模 100，与 `CanaryPercent` 比较
4. `CanaryPercent ≥ 100` → 全部 v2；`≤ 0` → 全部不走 v2

切 percent：热改需进程重启（`LoadFromEnv` 在启动期执行）；冷改直接发版。

## 2. 状态检视与强制恢复

### 2.1 Ready gate 检视

```bash
redis-cli GET ursm:v2:meta:ready        # 期望 "1"；"0"/missing → 阻塞
redis-cli HGETALL ursm:v2:meta:epoch    # counter / reason / started_at
redis-cli HGET ursm:v2:meta:epoch counter
```

`Manager.Ready(ctx)`（`recovery/manager.go`）每请求读 `meta:ready`；为 0 时 `FilterAndScore` 返回 `"ursm.v2: not ready"` 错误，Router / PlanCandidates 走旧路径。

### 2.2 强制进入 Recovery

两种方式，按场景选：

**(a) 运维命令式（推荐，热生效）**

```bash
redis-cli SET ursm:v2:meta:ready 0
redis-cli HINCRBY ursm:v2:meta:epoch counter 1
redis-cli HSET ursm:v2:meta:epoch reason "ops: <ticket>" started_at "$(date -u +%FT%TZ)"
```

这等价于 `recovery.Manager.EnterRecovery(ctx, reason)`（`recovery/manager.go`）——下一请求 `Manager.Ready()` 立即返回 false，并触发进程内已注册的 warmup 重跑（若 boot 期已 wire）。

**(b) 进程内触发（代码路径）**

任意持有 `*ursmv2.Manager` 的 goroutine 调：

```go
_ = mgr.SetReady(ctx, false)
// 或完整版：bump epoch + 记录 reason
_ = mgr.EnterRecovery(ctx, "ops: <ticket>")
```

> 计划文档提到的 `URSM_V2_FORCE_RECOVERY=1` 当前**未在 `LoadFromEnv` 接线**——下一请求不会自动 `EnterRecovery`。若需要"env 触发"，请通过发版或在 wrapper 内显式读 env 后调 `EnterRecovery`。

### 2.3 预热（WarmupFromSeed）

`recovery.Manager.WarmupFromSeed(ctx, []Seed{...})`：

1. `SetReady(false)`
2. pipeline `HSET ursm:v2:node:<cid>:<raw>` 含 `available=1`、`source_priority=30`、`generation=1`、`seed_source=warmup`
3. `SetReady(true)`

`recovery_in_progress` 期间，权威 / canary 路径不返回候选——这是 **D6 的核心不变式**。

## 3. 紧急回退

### 3.1 标准回退（流程）

```bash
# 1. 把所有网关实例的 URSM_V2_MODE 设为 off
#    - k8s: 修改 ConfigMap / env，触发滚动重启
#    - bare metal: 改 systemd EnvironmentFile，systemctl restart gateway
# 2. 验证
redis-cli GET ursm:v2:meta:ready   # 应保持 1；新进程不会再 SET
curl -fsS http://<pod>:9090/metrics | grep ursm_v2_mode  # 已无 v2 流量
```

为什么这能保证回退：`main.go:493` 的 `if env := os.Getenv("URSM_V2_MODE"); env == "shadow"|"canary"|"authoritative"` 守卫，让 `URSMv2` 仅在非 off 时被构造；off 启动下 `router.URSMv2` 和 `routingExec.URSMv2` 都为 nil，旧 `credentialstate` 路径完整接管，无 v2 副作用。

### 3.2 回退矩阵

| 症状 | 首选动作 | 兜底 |
|---|---|---|
| 无候选率（`no_candidate_total`）上升 | canary%→0 或 `mode=shadow` | `mode=off` + 重启 |
| Redis 延迟 / 错误飙升 | 告警观察 `ursm_recovery_ready=0` | 人工 `mode=off`（**不**自动 fail-open） |
| shadow diff 异常（`ursm_shadow_diff_total{type="top1"}` 飙升） | 维持 `shadow`，不进入 canary | 修 reducer 后再放量 |
| 分钟快照写入失败（`ursm_snapshot_write_failures`） | 告警，**不**关路由 | 排查 DB；写失败不影响热路径 |
| `meta:ready` 莫名回 0 | 查 epoch `reason` 字段 | 调 `EnterRecovery` 重跑 warmup |
| 路由 P95 上涨 ≥ 15ms | canary%→0 | `mode=off` |
| 资源池（fp / conc / rpm）异常 | 仅降级对应 resource 适配器 | 与 v2 无关，不需关 v2 |

> **D2 不变式**：Redis 读失败默认**保护性拒绝**候选，不会自动 fail-open。如果业务上必须保通，唯一的出口是人工 `mode=off`。

### 3.3 与既有 kill switch 的关系

`mode=off` 与 `fp_slot` / `circuit_degradation` 等 kill switch **相互独立**——回退 v2 不会动 FP slot 状态，也不会自动恢复已被熔断的 upstream。

## 4. 复盘与可观测

### 4.1 数据源

| 来源 | 看什么 |
|---|---|
| `request_logs.routing_attempts`（JSONB） | 同一 request_id 下所有路由轮次的 cid / raw / reason；与 v2 plan 对比即得"v2 排序与真实落点差异" |
| `ursm_node_snapshot_min`（分钟表） | 某分钟谁被踢出号池、成功率 / 延迟 / score / generation；recovery 时回填源 |
| `ursm:v2:node:<cid>:<raw>`（Redis HASH） | 实时画像：`available` / `fail_streak` / `cool_until` / `sr_1m|5m|30m` / `lat_ewma` / `score` / `source_priority` / `generation` |
| `ursm:v2:win:<bucket>:<cid>:<raw>`（ZSET） | 滑动窗口原始样本，TTL > 窗口长度 |
| `ursm:v2:meta:ready` / `ursm:v2:meta:epoch` | recovery 闸门与 epoch |

### 4.2 指标

- `ursm_shadow_diff_total{type=availability|order|top1}`
- `ursm_shadow_agree_ratio`
- `ursm_state_record_applied/ignored/error`
- `ursm_recovery_ready`（0/1）/ `ursm_recovery_duration`（warmup 秒数）
- `ursm_snapshot_write_failures`

### 4.3 复盘脚本示例

```sql
-- 1. 找一次失败请求的完整路由链
SELECT request_id, routing_attempts
FROM request_logs
WHERE request_id = '<id>'
  AND created_at > now() - interval '1 hour';

-- 2. 该请求关联的 credential 在 v2 里的快照
SELECT snapshot_ts, credential_id, raw_model_name, available, sr_5m, lat_p95_ms, score, generation
FROM ursm_node_snapshot_min
WHERE credential_id IN (...) AND snapshot_ts BETWEEN ... AND ...;
```

## 5. 决策记录（DR 摘录）

| ID | 锁定决策 | 含义 | 代码锚点 |
|---|---|---|---|
| D1 | Redis 运行时权威 | 热路径不读 DB 状态 | `domains/ursm/v2/store` |
| D2 | Redis 读失败 → 保护性拒绝 | 不自动 fail-open | `recovery.Manager.Ready` / `store.PipelineNodeViews` |
| D3 | 首次请求懒初始化 | 必须过 DB 硬门 + Redis NX | `sync.NewSyncer` |
| D4 | 五层粒度（provider/credential/binding/node/resource） | 跨层观测与租约 | `store/keys.go` |
| D5 | Redis 7 + AOF 单实例 | Lua 跨 key 安全 | 部署侧 |
| D6 | 丢失恢复阻塞至预热完成 | 空库不伪装健康 | `recovery.WarmupFromSeed` |
| D7 | 中等规模（1k credential / 10k node） | pipeline + ZSET 足够 | `store.PipelineNodeViews` |
| D8 | 来源优先 + CAS（admin>probe>request） | 人工意图不被覆盖 | `api.SourcePriority*` + reducer |
| D9 | 人工禁用仅显式 enable 可恢复 | probe/request 不能自动解开 | `admin.ApplyAdmin` |
| D10 | 结果同步 Redis / 审计异步 | 下一请求立即可见 | `Manager.RecordRequest` |
| D11 | 影子后灰度（off→shadow→canary→authoritative） | 可一键回退 | `rollout.Controller` |
| D12 | URSM 内演进六组件 | 复用注入点 | `domains/ursm/v2` 子包 |

## 6. 验收对照

| 项 | 检查方式 |
|---|---|
| Redis 权威 | `routing_attempts` 中无 DB 来源的可用性字段 |
| Redis 故障不 fail-open | `meta:ready=0` 后观测 `no_candidate_total` 与日志 |
| 重启恢复 | 演练：kill -9 redis → 观察 `ursm_recovery_duration` 与 ready=1 时点 |
| 人工 disable 不被覆盖 | admin disable 后观察 v2 plan 不再使用该 cid |
| 快照可查 | `SELECT count(*) FROM ursm_node_snapshot_min WHERE snapshot_ts > now()-interval '1h'` |
| 一键回退 | `URSM_V2_MODE=off` 重启，5 分钟内 `request_logs` 中 v2 字段消失 |

## 7. 相关入口

- 设计：`docs/号池状态优化/00-overview.md`
- 组件：`01-architecture.md`
- Redis key / Lua：`02-redis-protocol.md`
- 热路径 / debug：`03-hotpath-and-logging.md`
- Recovery / 快照 / 迁移阶段：`04-recovery-persist-rollout.md`
- 索引：`docs/号池状态优化/README.md`