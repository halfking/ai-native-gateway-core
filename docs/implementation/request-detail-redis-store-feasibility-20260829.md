# Request-Detail 方案 C (Redis Store) 可行性研究

**日期**: 2026-08-29  
**作者**: Request-Detail 审计闭环第六轮（方案 C 预研）  
**状态**: 🟡 **冻结** — 等待方案 D soak 1 周结果 + 触发条件  
**关联文档**:
- `request-detail-cross-replica-visibility-20260828.md`（方案 C 推荐理由）
- `request-detail-sticky-routing-deferred-20260829.md`（方案 B 已关闭）
- `request-detail-performance-audit-20260828.md`
- `KNOWN_LIMITATIONS_MULTI_INSTANCE.md`（Circuit/Limiter 跨进程架构约束）

## 1. 摘要（TL;DR）

方案 C（抽象 `Store` 为接口，新增 `RedisStore` 实现）**技术可行**：
- Redis 客户端已存在（`github.com/redis/go-redis/v9`）
- Redis 共享工具包已存在（`internal/redis/safe_operations.go` + `script.go`，305 行）
- 245 已有 Redis 客户端连接（`LLM_GATEWAY_REDIS_ADDR=<env:HOST_252_INTERNAL_IP>:6389`，DB 2）
- 性能基线：PING 0-2ms，1MB 读 48ms / 写 54ms

但**当前不应实施**：
- 没有真实的多副本业务需求（方案 B 已记录：所有 LB 都是单实例）
- 方案 D 100ms retry 在单副本下已能救回绝大多数 read-your-writes miss
- 1MB body 写 Redis 50ms 的延迟会拖慢 admin UI（特别是大请求详情的 fast-path）
- 引入 Redis SPOF，需要新增运维依赖

**结论**：技术预研完整封档，等触发条件再实施。

## 2. 现状调研（2026-08-29）

### 2.1 Redis 基础设施

| 组件 | 状态 | 位置 |
|---|---|---|
| Redis 客户端 | ✅ 已存在 | `github.com/redis/go-redis/v9`（vendor） |
| Redis 共享工具包 | ✅ 已存在 | `internal/redis/safe_operations.go` + `script.go`（305 行） |
| 现有 Redis Store 参考 | ✅ 已存在 | `domains/session/preprocess/redis_store.go`（827 行） |
| 252 Redis 实例 | ✅ 运行中 | `<env:HOST_252_INTERNAL_IP>:6389`，密码 `Veritrans9900`，DB 0/1/2/9 |
| 245 → 252 Redis | ✅ 可达 | PONG 成功，282µs 延迟（基线） |
| 154 → 252 Redis | ✅ 可达 | （与 245 同源，未单独测） |

### 2.2 245 Redis 实际性能基线（2026-08-29 测得）

| 操作 | 延迟 | 备注 |
|---|---|---|
| PING | 0-2 ms | 健康 |
| SET 1MB random data | 54 ms | 包含 RTT + 序列化 |
| GET 1MB random data | 48 ms | 包含 RTT + 反序列化 |
| DBSIZE DB 2 | 1,345,756 keys | 99.98% 有 expires，avg_ttl ≈ 17 天 |
| Connected clients | 83 / 10000 | 余量充足 |

### 2.3 `domains/requestdetail/` 当前代码规模

```
2947 行（含测试）
├── store.go (455 行)        — LocalStore 实现
├── capture_forwarder.go (344 行) — 异步写入管道
├── hooks.go (169 行)        — CaptureFromEntry / ClearAfterPersist 公开 API
├── locator.go (290 行)      — L1→L2→L3 回退链（已含方案 D retry）
├── types.go (59 行)         — Source / Persistence / Bodies / Meta / Detail
├── metrics.go (87 行)       — Prom 计数器
├── *_test.go (1543 行)      — 测试
└── 其他 (60 行)
```

**改造点**：
- `Store` 是具体类型 → 抽接口
- `NewStoreWithOptions` → 增加 `NewRedisStore`
- `SetGlobal` / `Global()` / `CaptureForwarder` / `StartGlobalCaptureForwarder` 全部签名不变
- 7 个公开方法需要在新接口中定义：`PutMeta / Put / PutBodies / GetMeta / GetFile / Clear / HasLocal`

### 2.4 Redis 现有用法（避免命名冲突）

- DB 2 已有 134 万 expires 键，命名空间已被 `ursm.v2:node:*`、`llm_gateway:*`、`rlmgw:*` 等业务占用
- 需要为请求详情选择独立的 key 前缀，建议：`rdetail:{tenant_id}:{request_id}`（包含租户隔离）
- TTL 与 L2 文件 TTL 对齐（默认 30min，可配置）

## 3. 方案 C 设计草案（实施前需细化）

### 3.1 接口设计

```go
// Store 是跨副本请求详情存储的统一接口。
// 当前唯一实现是 LocalStore（节点本地）；未来可扩展 RedisStore。
type Store interface {
    // PutMeta 写入/更新请求的 metadata（轻量，内存语义）。
    PutMeta(meta Meta) error
    
    // Put 一次性写入 meta + bodies（fast-path；失败回退到 PutBodies）。
    Put(meta Meta, bodies *Bodies) error
    
    // PutBodies 分开写 bodies（用于 file/redis backend）。
    PutBodies(meta Meta, bodies Bodies) error
    
    // GetMeta 仅查 meta（不加载 body）。
    GetMeta(requestID string) (Meta, bool)
    
    // GetFile 查 meta + bodies；第三个返回值标识是否找到。
    GetFile(requestID string) (filePayload, bool, error)
    
    // HasLocal 检查本地是否有该 request_id（admin UI 状态显示用）。
    HasLocal(requestID string) bool
    
    // Clear 删除存储（meta + bodies）。
    Clear(requestID string) error
}

// 后端选择（feature flag）。
const (
    BackendLocal Store = "local"  // 默认：节点本地文件
    BackendRedis Store = "redis"  // 可选：跨副本共享
)

// 通过 env 切换：LLM_GATEWAY_REQUEST_DETAIL_BACKEND=local|redis（默认 local）
```

### 3.2 Redis key 设计

```
rdetail:{tenant_id}:{request_id}:meta       # Hash: request_status, success, latency_ms, ...
rdetail:{tenant_id}:{request_id}:bodies     # String (JSON): {"request":..., "response":..., "outbound":...}
rdetail:{tenant_id}:{request_id}:ts         # Sorted Set score: 写入时间戳（用于 TTL / 统计）
rdetail:global:recent                       # List: 最近 N 个 request_id（用于 LRULRU 驱逐）
```

**注意**：方案 C 与方案 D 协同 — Redis Store 的 body 写入也是异步的，
因此方案 D 的 retry 仍然必要（Redis 写入与 admin 读取的窗口）。

### 3.3 L1/L2/L3 回退链改造

当前 Locator 顺序：
1. L1 = LocalStore 内存（节点本地）
2. L2 = LocalStore 文件（节点本地）
3. L3 = BodyReader（PG DB，全局源）

方案 C 后的新顺序：
1. L1 = LocalStore 内存（节点本地 hot-path，保留 fast-path）
2. L2 = RedisStore meta+bodies（跨副本共享热路径）— **新增**
3. L3 = LocalStore 文件（节点本地 fallback，兼容旧 L2 部署）
4. L4 = PG DB（全局源，作为兜底）

**关键设计决策**：
- L1 保留 LocalStore 内存（避免每次 admin 请求都打 Redis）
- L2 = RedisStore 是新主路径；TTL 30min
- L3 = LocalStore 文件作为 cold-path（hot-cache miss 时 fallback 到本地 FS）
- L4 = PG DB 兜底

### 3.4 迁移路径（如果决定实施）

**Phase 1（2 周）**：
- 抽象 `Store` 接口，新增 `RedisStore` 实现
- 通过 `LLM_GATEWAY_REQUEST_DETAIL_BACKEND=local|redis` 切换；默认 `local`
- 单实例部署运行 1 周 soak，验证 RedisStore 正确性

**Phase 2（1 周）**：
- k3s `pms-test` 部署 `replicas: 2`，启用 RedisStore
- 验证跨副本 admin 读命中率 ≥ 99%
- 验证 Redis 故障降级（Redis 不可达时回退到 local + retry）

**Phase 3（2 周）**：
- 154 生产灰度：1% → 25% → 100%（按 `request_id % 100` 切流）
- 监控 `rdetail_redis_miss_total / rdetail_redis_hit_total` 比例
- 异常指标（Redis 写入失败、Redis 慢查询 > 100ms）触发自动回滚

### 3.5 风险评估

| 风险 | 影响 | 缓解 |
|---|---|---|
| Redis SPOF | 高 | 252 Redis 已经是主备（推测），但需确认；failover 期间降级到 LocalStore + 方案 D retry |
| Redis 写入延迟 ~50ms | 中 | 异步写入（已有 capture_forwarder）+ admin 读路径走 L1/L2 缓存 |
| 1MB body Redis 占用 | 中 | DB 2 容量评估（每条 1MB × 30min TTL × QPS 100 = 300MB 临时峰值，可控） |
| 跨租户泄漏 | 高 | key 必须包含 `tenant_id`；L1/L2/L3 都需 tenant 校验；admin UI 已有 `LookupScope` 机制 |
| Redis 客户端连接池 | 低 | `go-redis/v9` 默认 10 连接池；高 QPS 下需提到 100+ |

## 4. 当前不实施的原因

| # | 原因 |
|---|---|
| 1 | 没有真实的多副本业务需求（方案 B 已记录） |
| 2 | 方案 D 100ms retry 在单副本下已能救回绝大多数 read-your-writes miss |
| 3 | 1MB body 写 Redis 50ms 的延迟会拖慢 admin UI（特别是大请求详情的 fast-path） |
| 4 | 引入 Redis SPOF（虽然 245 已配置 LLM_GATEWAY_REDIS_ADDR） |
| 5 | 业务层已经有 `domains/session/preprocess/redis_store.go` 的经验，但请求详情的访问模式（admin UI 偶发读）与 session 的访问模式（chat 频繁读写）不同，需要重新调优 |

## 5. 重新评估的触发条件（与方案 B 文档同步）

**任一**满足时重启方案 C 评估：

| 条件 | 评估响应 |
|---|---|
| 生产 154 部署从单实例改为多副本 | 同时评估 B + C；B 优先（实施快），C 作为长期演进 |
| k3s `pms-test` 改 `replicas: 3+` 用于压测 | 优先评估 C（多副本测试环境是验证 C 的天然场景） |
| 方案 D soak 1 周 `hit/(hit+miss) < 0.2` 持续 1h | 说明 retry 不够；优先加大 Delay 到 500ms × 3 次；仍不达标 → 评估 C |
| 出现业务工单"跨副本 admin 读 404" | 立刻评估 C（业务驱动） |

## 6. 监控假设（如果实施）

新增 Prom 计数器（基于方案 D 的 `requestdetail_locator_db_retry_total` 模式）：

```
# Redis 命中/未命中
requestdetail_redis_hit_total{backend="redis"}              # 累计 L1/L2/L3 Redis 命中
requestdetail_redis_miss_total{backend="redis"}             # 累计 Redis miss

# Redis 延迟
requestdetail_redis_op_duration_seconds_bucket{op="get|set",le=...}

# Redis 错误
requestdetail_redis_error_total{op="get|set",kind="timeout|conn|proto"}

# Redis fallback 触发（Redis 不可达时降级到 local）
requestdetail_redis_fallback_total{kind="conn|timeout|proto"}
```

## 7. 决策者签字栏（占位）

| 角色 | 决策 | 备注 |
|---|---|---|
| 架构 | （待评审） | |
| 运维 | （待评审） | |
| 业务 | （待评审） | |

## 8. 引用

- 跨副本 recommendation：`docs/implementation/request-detail-cross-replica-visibility-20260828.md` §3
- 方案 B 关闭决策：`docs/implementation/request-detail-sticky-routing-deferred-20260829.md`
- 性能审计：`docs/implementation/request-detail-performance-audit-20260828.md`
- Redis 共享包：`internal/redis/safe_operations.go`、`internal/redis/script.go`
- Redis Store 参考实现：`domains/session/preprocess/redis_store.go`
- 多实例架构限制：`docs/archive/process/process/2026-07/KNOWN_LIMITATIONS_MULTI_INSTANCE.md`
- 方案 D commit：`2a29eebf`
- 方案 B deferred commit：`e8bf2e962`
- env 配置：`envs/common/database.yaml`、`envs/servers/<env:HOST_245_IP>/.env.secrets.plain.yaml`
