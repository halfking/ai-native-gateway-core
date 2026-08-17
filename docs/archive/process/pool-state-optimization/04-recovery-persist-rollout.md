# 04 — 恢复、分钟持久化、灰度与迁移

## 1. RecoveryManager

### 1.1 触发

| 触发 | 行为 |
|---|---|
| 进程启动且 Redis 可连 | 检查 `ready` 与 `recovery_epoch` |
| `ready=0` 或 epoch 缺失 | 进入 recovery，阻塞权威/canary 路由 |
| Redis ping 失败 | 读路径保护性拒绝；不空库放行 |
| 运维 `EnterRecovery` | 强制 ready=0，重新预热 |
| 抽样校验失败（大量 key 缺失） | 自动 EnterRecovery |

### 1.2 预热数据源

1. DB 配置硬门：providers / credentials / credential_model_bindings（及 routable 视图字段）
2. 价格/计费：binding 价格、billing_mode、plan 派生
3. 最近分钟快照：available、cool_until、fail_streak、sr_*、score
4. 人工控制态：manual_disabled 与 admin 版本（必须从 DB/审计恢复）

### 1.3 预热步骤

```text
1. SET ready=0，记录/bump recovery_epoch
2. 批量加载 active provider/credential/binding
3. pipeline 写 Redis HASH（分批 500–2000）
4. 用最近快照覆盖 node 运行摘要（若有）
5. 重建 CandidateIndex
6. 抽样校验：随机 K 个 binding 与 DB 一致；人工禁用必须存在
7. SET ready=1，warmed_at=now，打点 metrics
```

### 1.4 路由语义

- `ready=0`：权威/canary 返回 `recovery_in_progress`（可映射 503 + 明确 reason）
- `shadow`：旧路径继续服务生产；URSM 侧 unready，不产生误导 diff
- recovery 期间禁止懒初始化“全新健康节点”绕过预热

## 2. 每分钟持久化

### 2.1 表设计

```sql
CREATE TABLE ursm_node_snapshot_min (
  snapshot_ts        timestamptz NOT NULL,
  recovery_epoch     bigint      NOT NULL,
  provider_id        int         NOT NULL,
  credential_id      int         NOT NULL,
  raw_model_name     text        NOT NULL,
  canonical_name     text,
  tenant_id          text,
  available          boolean     NOT NULL,
  health_status      text,
  fail_streak        int,
  cool_until         timestamptz,
  sr_1m              real,
  sr_5m              real,
  sr_30m             real,
  samples_1m         int,
  samples_5m         int,
  samples_30m        int,
  lat_p50_ms         int,
  lat_p95_ms         int,
  score              real,
  price_in_per_1m    numeric,
  price_out_per_1m   numeric,
  billing_mode       text,
  trust_level        real,
  baseurl_latency_ms int,
  conc_used          int,
  conc_limit         int,
  fp_used            int,
  fp_limit           int,
  source_priority    int,
  generation         bigint,
  payload            jsonb,
  PRIMARY KEY (snapshot_ts, credential_id, raw_model_name)
);
```

建议：按时间分区或 BRIN(`snapshot_ts`)。

### 2.2 写入节奏

- ticker：默认 60s（`ursm.v2.persist_interval_sec`）
- 以 CandidateIndex 反查 node，禁止 KEYS
- 批量 INSERT/COPY
- 失败重试 + 告警；不阻塞路由

### 2.3 保留

- 分钟明细：7–14 天（可配）
- 小时聚合：30–90 天（后续）
- 与 `state_change_log` 并存：快照=画像，change_log=迁移事件

### 2.4 复盘

- 某分钟谁被踢出号池、成功率/延迟
- 与 `request_logs.routing_attempts` 按时间 + credential + model 关联
- Redis 故障恢复时作运行摘要回填源

## 3. 灰度与回退

### 3.1 模式

| mode | 生产决策 | URSM 计算 | 写 Redis |
|---|---|---|---|
| off | 旧路径 | 否 | 否 |
| shadow | 旧路径 | 是，记 diff | 是（推荐，积累真实窗口） |
| canary | 命中比例走 URSM | 是 | 是 |
| authoritative | 全量 URSM | 是 | 是 |

影子写 Redis 时：**旧路径不得读取 URSM 裁决结果**。

### 3.2 配置键

```text
ursm.v2.mode = off|shadow|canary|authoritative
ursm.v2.canary_percent = 0..100
ursm.v2.canary_tenants = csv
ursm.v2.canary_models = csv
ursm.v2.shadow_sample_rate = 0..1
ursm.v2.record_timeout_ms = 20
ursm.v2.recovery_block = true
ursm.v2.persist_interval_sec = 60
```

一键回退：`ursm.v2.mode=off`。
与 `fp_slot` / `circuit_degradation` 等 kill switch 独立。

### 3.3 回退矩阵

| 症状 | 动作 |
|---|---|
| 无候选率升高 | canary%→0 或 mode=shadow/off |
| Redis 错误/延迟飙升 | 保护性拒绝 + 告警；需保可用性时人工 mode=off |
| 影子 diff 异常高 | 不进 canary，修 reducer |
| 快照写入失败 | 告警，不自动关路由 |
| 资源池异常 | 仅降级对应 resource 适配 |

说明：默认 **不** 因 Redis 故障自动 fail-open。若必须保通，只能人工切回旧路径。

### 3.4 影子 diff 协议

```text
ShadowDiff {
  request_id, tenant, model
  old_ordered: []{cid, raw, reason}
  new_ordered: []{cid, raw, reason, score}
  first_divergence_index
  availability_mismatches[]
  order_mismatches
  resource_pressure_delta
}
```

指标：

- `ursm_shadow_diff_total{type=availability|order|top1}`
- `ursm_shadow_agree_ratio`
- `ursm_state_record_applied/ignored/error`
- `ursm_recovery_ready` / `ursm_recovery_duration`
- `ursm_snapshot_write_failures`

### 3.5 进入 canary 门槛

1. shadow 运行达到约定窗口（建议 ≥24h 或等效流量）
2. top1 一致率达阈值（如 ≥95%，排除预期改进类）
3. 无候选率、错误率不劣于基线
4. 至少一次 Redis 重启恢复演练通过
5. 分钟快照连续成功写入

## 4. 迁移阶段

### Phase A — 骨架（不切流）

1. `domains/ursm` 拆六组件
2. StateStore key + Lua CAS + 窗口 ZSET
3. StateReducer 纯函数单测
4. RecoveryManager + ready 门闩
5. 分钟快照表 + writer
6. mode 固定 `off`

### Phase B — 并行写与影子

1. Executor 成功/失败旁路 `RecordRequest`
2. admin/config 同步写入 v2
3. Router shadow 双算 + diff
4. mode=`shadow` 上预发/245
5. 恢复演练：重启 Redis → 阻塞 → 预热 → ready

### Phase C — 灰度

1. 单 tenant/模型 1% → 10% → 50%
2. 观察无候选、failover、P95、Redis CPU
3. 异常则 percent=0 或 shadow

### Phase D — 权威与收敛

1. mode=`authoritative`
2. 旧 `credentialstate` 读路径下线
3. `routingstate` 收敛为 URSM shadow 或薄封装
4. runbook 完善

### 明确后置

- 跨实例 conc 全切 Redis
- WRR/水位大调度
- 删除 provider 30s 候选缓存
- 探测执行器并入 URSM

## 5. 验收标准

1. Redis 权威：热路径可用性不读 DB 状态表
2. Redis 读失败 → 候选保护性拒绝（reason 可观测）
3. Redis 重启 → ready=0 阻塞 → 预热后恢复
4. 人工 disable 不可被 request/probe 自动解开
5. 请求结果同步进窗口与 node 摘要；审计异步
6. 每分钟快照可查，可用于复盘与回填
7. shadow 指标齐全；可一键 `mode=off`
8. 路由附加开销 P95 ≤ 15ms
9. FP slot / RPM 现有测试不回归

## 6. 测试矩阵（摘要）

| 类别 | 覆盖 |
|---|---|
| Reducer | manual/probe/request 优先级；免费 transient；永久错误 |
| Store | CAS 忽略过期；NX 懒初始化；pipeline 批量读 |
| Recovery | 空库阻塞；快照回填；校验失败重进 recovery |
| Hotpath | plan 不 acquire；资源释放；Record 超时不拖垮请求 |
| Shadow | availability/order/top1 diff；采样 |
| Rollout | percent 稳定哈希；off 回退 |
| Resource | FP pin/active gate；RPM 窗口；conc 本地语义 |
| Persist | 分钟写入、失败重试、主键幂等 |

## 7. Runbook 要点（实施后补全命令）

- 查看 mode / ready / epoch
- 强制 EnterRecovery
- 调 canary percent
- 紧急 mode=off
- 对比某 request_id 的 routing_attempts 与 node 快照
- Redis 重启后观察 `ursm_recovery_*` 与无候选率
