# 02 — Redis 协议、滑动窗口与原子更新

## 1. 部署假设

- 单实例 Redis 7 + AOF/持久卷
- Go 使用 `redis.UniversalClient` 抽象，第一阶段不启用 Cluster 语义
- key 命名预留 `{cid}` 形态，便于未来 hash-tag 扩展
- 跨 key 原子操作允许同实例 Lua；禁止依赖 Cluster 跨槽事务

## 2. Key 协议

```text
# 元信息 / 恢复
ursm:v2:meta:epoch                         HASH   recovery_epoch, ready, warmed_at, reason
ursm:v2:meta:ready                         STRING 0|1

# 配置/控制态（低频）
ursm:v2:provider:{pid}                     HASH
ursm:v2:credential:{cid}                   HASH
ursm:v2:binding:{cid}:{raw_model}          HASH

# 运行时节点态（高频）
ursm:v2:node:{cid}:{raw_model}             HASH

# 滑动窗口（多池）
ursm:v2:win:1m:{cid}:{raw_model}           ZSET
ursm:v2:win:5m:{cid}:{raw_model}           ZSET
ursm:v2:win:30m:{cid}:{raw_model}          ZSET

# 候选索引
ursm:v2:idx:model:{tenant}:{canonical}:{profile}:{modality}  ZSET
# member = {cid}:{raw_model}
# score  = composite_score 或 last_activity_ms

# 资源池（对齐现网语义，统一观测前缀；实现可复用旧 key 过渡）
ursm:v2:res:fp:tenant:{t}:cred:{cid}:{slot}
ursm:v2:res:fp:pin:tenant:{t}:{holder}:{cid}
ursm:v2:res:rpm:{pid}:{cid}                ZSET   # 60s 滑动
ursm:v2:res:conc:{pid}:{cid}               HASH/STRING  # 第二阶段跨实例

# 影子差异
ursm:v2:diff:shadow:{yyyyMMddHHmm}         LIST/STREAM
```

### 2.1 设计原则

1. 健康态与资源租约分 key
2. 滑动窗口用 ZSET，不塞进大 JSON
3. Hash 只存即时摘要，热路径 `HMGET` 必要字段
4. 每个裁决对象带 `generation` + `source_priority` + `updated_at_ms`
5. 禁止生产路径 `KEYS`；扫描走 CandidateIndex 或已知集合

## 3. Hash 字段约定

### 3.1 node

```text
available            0|1
health               unknown|healthy|warning|cooling|unhealthy
fail_streak          int
cool_until_ms        int
last_err             string
sr_1m, sr_5m, sr_30m float as string
samples_1m/5m/30m    int
lat_ewma_ms          int
lat_p50_ms           int
lat_p95_ms           int
score                float
generation           int
source_priority      int
updated_at_ms        int
seed_source          lazy_request|warmup|sync
manual_hold          0|1   # 若节点级运维冻结
```

### 3.2 binding

```text
available, reason, probe_state
price_in_per_1m, price_out_per_1m
billing_mode, currency
canonical_name, outbound_model
generation, source_priority, updated_at_ms
```

### 3.3 credential

```text
status, lifecycle_status
availability_state, health_status, quota_state
manual_disabled
plan_type, trust
fp_slot_limit, concurrency_limit, rpm_limit
generation, source_priority, updated_at_ms
```

### 3.4 provider

```text
enabled, manual_disabled
base_url_latency_ms, trust
origin_type
generation, source_priority, updated_at_ms
```

## 4. 滑动窗口

### 4.1 结构

```text
ZSET score  = event_ts_ms
     member = {request_id}:{0|1}:{latency_ms}
TTL:
  1m  → 90s
  5m  → 6m
  30m → 35m
```

### 4.2 同步更新（RecordRequest Lua 内）

1. `ZADD` 1m/5m（30m 可同写或由分钟任务汇总）
2. `ZREMRANGEBYSCORE` 裁剪
3. `ZCARD` + 遍历/计数成功样本（样本大时可用成对统计字段辅助）
4. 更新 node 摘要：`sr_* / samples_* / lat_* / fail_streak / cool_until / score`
5. 对裁决字段做 CAS

### 4.3 双通道

| 通道 | 行为 |
|---|---|
| 窗口样本 | 始终 append（观测） |
| 裁决字段 | 来源优先 + generation CAS |

### 4.4 阈值（可配置，默认建议）

```text
soft_demote: samples_5m >= 10 && sr_5m < 0.80
hard_exclude: samples_5m >= 20 && sr_5m < 0.50   # 免费凭据可关闭 hard
fail_streak_limit: 3
cool_backoff: 30s → 2m → 5m（封顶可配）
empty window: 中性分（不因无样本惩罚）
```

免费凭据 + transient：不设 `available=0`，只靠 `sr_*` 软降权。

## 5. 懒初始化

前置：候选已通过 DB 配置硬门，且 `ready=1`。

```text
HSET NX node key 初始字段
EXPIRE node
HSET NX binding/credential/provider 摘要（来自 CandidateSeed）
```

并发：多实例 NX，一写多读。
Redis 连接失败：不初始化，不当作 available。

## 6. Lua 脚本清单

| 脚本 | 作用 |
|---|---|
| `record_request.lua` | 追加窗口 + 更新 node 摘要 + CAS 裁决字段 |
| `apply_admin.lua` | 最高优先级写 provider/credential/binding |
| `apply_probe.lua` | probe 优先级写 binding/node/credential |
| `lazy_init_node.lua` | NX 初始化 |
| `set_ready.lua` | recovery epoch + ready 切换 |
| `index_upsert.lua` | 候选索引增删改 score |

资源相关继续复用：

- `credentialfpslot` acquire/release/reclaim scripts
- `credential` RPM sliding window script

第一阶段不重写其语义，ResourcePools 适配调用。

## 7. record_request 结果码

```text
applied
ignored_stale
ignored_priority
ignored_manual_hold
error_redis
```

调用方：

- `applied` / `ignored_*`：请求主流程不失败
- `error_redis`：记 metric + warn 日志；不回滚上游结果

## 8. 批量读（热路径）

对同一请求候选列表：

```text
PIPELINE:
  HMGET provider...
  HMGET credential...
  HMGET binding...
  HMGET node...
  (可选) resource stats
```

禁止对 N 个候选串行单 key RTT。
目标：状态读取 P95 ≤ 5ms。

## 9. 内存与 TTL 预算（中等规模）

假设 10k node：

- node hash ~ 0.5–1KB → ~10MB
- 三窗口，每节点短期样本有界 → 数十 MB 量级
- 索引 ZSET 按模型分片 → 较小

控制手段：

- 窗口 TTL
- member 不存大 payload
- 冷节点依赖配置同步，不长期空转堆积

## 10. 与旧 key 过渡

| 旧 | 过渡策略 |
|---|---|
| `llmgw:credstate:*` | shadow 期双写或旁路写 v2；权威后停读旧 key |
| `llmgw:cred_fp_node:*` | 健康收敛到 `ursm:v2:node`；fp 包停写健康 JSON |
| `llmgw:tenant:*:cred_fp_slot:*` | 继续使用，ResourcePools 包装 |
| `rpm:{pid:cid}` | 可保留或迁移别名；逻辑不变 |
| `ursm:node:*`（旧半成品） | 不混用；v2 新前缀，避免半兼容 |
