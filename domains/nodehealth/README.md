# NodeHealth 节点健康决策域

> 本包把 request / probe 的终态结果收敛为一条归一化的节点健康决策流。
> 它不直接写任何 legacy 健康存储（circuit、credentialstate、URSM、队列），
> 全部副作用以显式 `Effect` 交由调用方的 `Adapter` 落地。

## 核心概念

| 概念 | 含义 |
|-----|-----|
| `NodeKey` | 一个可路由节点 = (tenant, provider, credential, **model**)。节点状态按完整 key 隔离，同凭据不同模型互不可见 |
| `Phase` | 一次 attempt 内独立可观测的阶段：`request` / `direct_probe` / `gateway_probe` |
| `ErrorKind` | 归一化的健康相关失败分类（network/timeout/upstream/rate_limit/auth/quota/model_binding/request） |
| `Observation` | 一条终态结果（含 attempt 去重键） |
| `Decision` | 一次 reduce 的完整可观测结果：状态迁移、连败计数、effects |
| `Effect` | 显式集成指令，由 Adapter 翻译到真实存储 |

## 状态机

```
healthy ──失败──→ suspect（连败 1-2）
   │                │ 连败 ≥3
   │                ↓
   │            degraded ──成功──→ healthy（连败清零）
   └──────────→ quarantined（permanent 错误：auth / quota / model_binding）

degraded/suspect ──direct probe 成功──→ recovering
recovering       ──gateway probe 成功──→ healthy（双轮确认，防单轮误判）
```

- `request` 级错误（客户端 bug、内容策略、上下文超限）与 `canceled` 不构成健康证据：
  状态与连败均不变，不产生任何 effect。

## 错误作用域与隔离（本域的核心契约）

同凭据下**模型 A 的失败不得影响模型 B 的可用性**。错误按作用域分三档：

| 错误 | 节点状态 | binding 级效果 | 凭据级效果 |
|-----|---------|---------------|-----------|
| `model_binding`（model_not_found / deprecated） | 立即 quarantined | `SetBindingUnavailable` + `InvalidateCandidateCache` + `ScheduleProbe` | **无**：不记 `RecordCircuitFailure`，不 `SetCredentialUnavailable` |
| `auth` / `quota` | 立即 quarantined | `SetBindingUnavailable` | `RecordCircuitFailure` + `SetCredentialUnavailable`（凭据级问题是全模型的证据） |
| 瞬态（network/timeout/rate_limit/upstream） | 连败 1-2 → suspect；≥3 → degraded | 连败 ≥3 时 `SetBindingUnavailable` | 连败 ≥3 时 `RecordCircuitFailure` + `SetCredentialUnavailable`（凭据网络路径是共享的） |

凭据级熔断器（`domains/credential/breaker.go`）按 (provider, credential) 键控，
不含 model 维度——因此 binding 级错误必须在 reducer 处就与 circuit 隔离；
下游 `executor_nodehealth.go` 依赖 `RecordCircuitFailure`/`RecoverCircuit` 判定
probe 槽位是否被消费，binding 级失败不产出该 effect，即释放槽位（正确：
模型 A 的绑定错误不是凭据电路证据）。持久层 `WriteOnError` 对
`KindModelNotFound` 已按 per-model binding 落库（7 天冷却），兄弟模型不受影响。

## 去重与回收

`Reduce` 按 (node, attempt, phase) 幂等去重：重复事件返回原 Decision 并标记
`Duplicate=true`、`Accepted=false`，不重复触发状态迁移与 Adapter 调用。

去重历史是有界的插入序环形结构，双重回收：

- **容量**：默认 `DefaultSeenCapacity` = 100000 条，满时逐出最旧；
- **TTL**：默认 `DefaultSeenTTL` = 15 分钟，每次 `Reduce` 前从队头惰性回收过期条目
  （合法重放来自 outbox 重试与 journey 重读，均在秒级；双轮 probe 窗口为分钟级，
  15 分钟足够覆盖且不误伤）。

不变量：**回收只作用于去重历史，永不重置节点状态**——被回收条目的重放会被
重新接受并按当前节点状态结算。回收量通过
`nodehealth_reducer_seen_evicted_total{reason=ttl|capacity}` 暴露。

`ReducerConfig{SeenCapacity, SeenTTL, Now}` 支持容量/TTL/时钟注入，
测试可确定性推进时间。

## 文件清单

| 文件 | 职责 |
|-----|-----|
| `reducer.go` | 状态机、去重与回收、effects 决策（本域全部逻辑） |
| `metrics.go` | `nodehealth_reducer_seen_evicted_total` 回收指标 |
| `reducer_test.go` | 行为契约测试（状态迁移、隔离、去重、回收） |

## Adapter 接线

生产 Adapter 是 `domains/streaming/executors/executor_nodehealth.go`：
每个 Effect 翻译为一次真实调用（Circuit.RecordFailure/RecordSuccess、
credential Writer.WriteOnError/RestoreOnSuccess、URSM RecordRequest、
candidate cache 失效、probe scheduler）。dispatch 转发结果
（`reduceDispatchForwardOutcome`）是 Observation 的主要来源。
