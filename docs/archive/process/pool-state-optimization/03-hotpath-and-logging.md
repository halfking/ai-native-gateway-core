# 03 — 请求热路径与 Debug 日志

## 1. 热路径总览

```text
HTTP 请求
  │
  ├─ 1. Auth / Key RPM / 模型解析（现有 handler）
  │
  ├─ 2. provider.Client.GetCandidates
  │        DB 配置候选 + 30s 缓存（硬门：租户/模态/计费/价格/limits/base_url）
  │
  ├─ 3. URSM.FilterAndScore / ListCandidates
  │        ready? → provider → credential → binding → node
  │        resource pressure（只读 stats）
  │        composite score + 排序
  │
  ├─ 4. Router 输出有序候选
  │        sticky / tier / billing-round / egress
  │
  ├─ 5. Executor 候选循环（骨架保留）
  │        ResourcePools.Acquire(FP → Conc → RPM)
  │        Upstream call
  │        ResourcePools.Release（defer，独立 ctx）
  │
  └─ 6. 结果回写
           同步：StateStore.RecordRequest (Lua)
           异步：审计 / 快照队列 / request_logs.routing_attempts
```

### 1.1 模式差异

| mode | 步骤 3 生产采用 | 额外动作 |
|---|---|---|
| off | 旧 Router | URSM 不参与 |
| shadow | 旧 Router | URSM 双算 + diff；可并行写 Redis |
| canary | 命中比例用 URSM | 未命中走旧路径 |
| authoritative | URSM | 旧状态读路径不再参与裁决 |

## 2. 过滤与评分顺序

| 阶段 | 规则 | 失败 reason |
|---|---|---|
| R0 Ready | `ready!=1` → 阻塞 | `recovery_in_progress` |
| R1 Provider | enabled 且非 manual_disabled | `provider_disabled` |
| R2 Credential | lifecycle active；非 auth_failed/suspended；配额非永久耗尽；非 manual_disabled | 对应 credential reason |
| R3 Binding | model available；非 broken_confirmed | `binding_unavailable` |
| R4 Node | cool_until 未到期；fail_streak/阈值门槛 | `node_cooling` / `node_unhealthy` |
| R5 Resource 只读 | FP/conc/rpm 压力进评分 | 不因饱和直接判 unhealthy |
| R6 Score | 价格 + 延迟 + 稳定性 + 压力 + 可信度 | 只排序 |

### 2.1 硬规则（对齐现网）

1. Plan 阶段不 acquire 资源
2. 免费凭据 transient 失败：不硬剔，软降权
3. 永久错误：binding 或 credential 硬剔
4. 人工禁用：request/probe 不可恢复
5. Redis 读错误：保护性拒绝该候选
6. key missing：DB 硬门通过则懒初始化；Redis 连接失败则拒绝

## 3. 评分输入（NodeView → score）

建议与现有 loadScore / composite 可配置对齐：

```text
score =
  w_price * price_norm +
  w_latency * latency_norm +
  w_stability * (1 - sr_5m_or_neutral) +
  w_pressure * resource_pressure +
  w_trust * (1 - trust_norm) +
  w_fail * fail_streak_norm
```

- 窗口不足样本：stability 用中性值，不重罚
- soft_demote：提高 score（更差）但不剔除
- hard_exclude：R4 直接失败（免费可关）

具体权重进入配置，不写死业务常量到多处。

## 4. 结果回写时序

```text
upstream 返回
  → errorsx 分类
  → StateStore.RecordRequest(Lua, timeout 默认 ≤20ms)
       append windows
       update node summary (CAS)
       maybe cool_until / binding 瞬态
  → ResourcePools.Release（defer，bg ctx，可重试）
  → async:
       state_change_log / 审计
       request_logs + routing_attempts
       shadow diff
       metrics
```

同步 Redis 失败：

- 不改变对客户端的上游结果
- `state_update_failed` metric + debug/warn
- 后续若 Redis 仍不可读，该节点保护性拒绝

## 5. 资源获取顺序（Executor）

```text
for cand in ordered:
  if mode in {canary,auth} && !available: continue
  fpLease = AcquireFP(...)
  if !ok: next / degraded without slot
  releaseConc, err = AcquireConc+RPM(...)
  if err: ReleaseFP; next
  call upstream
  always: ReleaseConc; ReleaseFP
  RecordRequest(...)
```

ResourcePools：

- FP → 现有 `credentialfpslot`（租户/holder/pin/active gate）
- RPM → 现有 Redis 60s ZSET
- Conc → 第一阶段进程内 semaphore；接口预留 Redis

“槽满” ≠ “凭据坏了”。

## 6. Debug 日志

### 6.1 统一字段

```json
{
  "event": "route.*",
  "trace_id": "...",
  "request_id": "...",
  "tenant_id": "...",
  "canonical_model": "...",
  "raw_model": "...",
  "credential_id": 12,
  "provider_id": 3,
  "ursm_mode": "shadow|canary|authoritative",
  "recovery_epoch": 42,
  "stage_ms": 1.2
}
```

### 6.2 事件清单

| event | 何时 | 关键字段 |
|---|---|---|
| `route.candidates_loaded` | DB 候选返回 | count, cache_hit, modality |
| `route.ursm_filter` | 候选过滤 | pass/fail, reason, layer |
| `route.ursm_score` | 评分 | score, sr_1m/5m, lat, pressure, price |
| `route.plan_ready` | 有序候选就绪 | ordered_ids, sticky, billing_round |
| `route.shadow_diff` | 影子差异 | old_top, new_top, mismatch_type |
| `route.resource_acquire` | FP/conc/rpm | ok/fail, slot, used/limit |
| `route.upstream_result` | 上游结束 | success, error_kind, latency_ms |
| `route.state_record` | Lua 结果 | applied/ignored, version, cool_until |
| `route.recovery_block` | 未 ready | progress, reason |

### 6.3 级别与采样

- 成功路径细节：Debug
- 过滤/降级/影子差异：Info（可按 `shadow_sample_rate` 采样）
- Redis 失败/恢复阻塞/CAS 冲突风暴：Warn
- 资源释放最终失败：Error

与 `RoutingAttemptsTracker` / `request_logs.routing_attempts` 对齐：每次尝试一行，结束汇总，按 `request_id` 复盘 failover 链。

## 7. 时延预算（中等规模）

| 步骤 | 预算 P95 |
|---|---|
| 候选缓存命中 | ≤ 1ms |
| URSM 批量可用性 | ≤ 3ms |
| 评分排序 | ≤ 1ms |
| RecordRequest Lua | ≤ 5ms（超时上限默认 20ms） |
| 路由附加总开销 | ≤ 15ms |

实现要点：单请求候选 **一次 pipeline 批量读**。

## 8. 与旧路径对照

```text
旧: DB候选 → StateManager(mem/redis/db fail-open) → P2C/Bandit
    → Executor(FP/Limiter) → credentialstate 更新（偏异步/多层）

新: DB候选 → URSM ready? → Redis 权威过滤评分
    → 同序 Executor → 同步 Redis 状态 → 异步审计
    → shadow 双算 diff
```

第一阶段不删除旧路径；由 RolloutController 选择 plan 结果来源。
