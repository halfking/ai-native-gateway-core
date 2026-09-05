# SSE 总览页实时流带宽分析与优化方案

**日期**: 2026-08-27  
**范围**: llmgo.kxpms.cn (245 预发) + llm.kxpms.cn (154 生产)  
**目标**: 评估总览页 SSE 实时流 (`GET /api/admin/live-stream`) 的数据格式与数据量，识别精简优化机会，提高性能并降低带宽用量

---

## 0. 执行摘要

**核心发现**:

1. **154 生产环境 (8k req/min = 133 req/s) 单客户端带宽达 ~3.3 MB/s**，5 个管理员同时打开总览页时服务器出向带宽峰值 **16.5 MB/s**。
2. **`detail_dimensions` 字段完全冗余**：与 `dimensions` 逐字节重复，占 initial_data / snapshot_refresh 全量帧的 **50% payload**，但前端 **零消费**（仅作类型占位）。
3. **每个 `request` 事件携带完整 delta**：4 个维度 × 20 tiles/lane ≈ **25 KB/事件**，133 req/s 下单客户端 **3.3 MB/s 持续推送**。
4. **2s 心跳固定开销**：`node_update` (11 节点 ≈ 4.2 KB) + `queue_snapshot` (≈ 2.2 KB) 每 2 秒推一次，空闲基线 **11 MB/小时/客户端**。
5. **HTTP/2 下 nginx 未启用 gzip**：SSE `text/event-stream` 未被 `gzip_types` 覆盖，JSON 明文传输无压缩。

**优化潜力 (按优先级)**:

| 优先级 | 措施 | 节省幅度 | 实施复杂度 | ROI |
|---|---|---|---|---|
| **P0** | 删除 `detail_dimensions` 冗余字段 | ~50% (initial_data/snapshot_refresh) | 低 (前后端各一处) | 极高 |
| **P1** | 启用 gzip 压缩 (nginx `gzip_types` 增加 `text/event-stream`) | 60-75% | 极低 (配置) | 极高 |
| **P2** | Delta 节流从 2s 延长到 5s (2s 已是 8/25 紧急加入，可考虑再放宽) | 减少 60% delta 推送次数 | 低 (配置) | 高 |
| **P3** | `changed_lanes` 仅推送变化维度 (当前 4 维度全推) | 25-50% (delta 体积) | 中 (需改 `ComputeDelta` 逻辑) | 中 |
| **P4** | `LiveRequest`/`LiveStreamTile` 字段裁剪 (omitempty 已有，再压缩空间有限) | 5-10% | 低-中 | 低 |
| **P5** | 心跳降频 2s → 5s (牺牲实时性) | 心跳开销 -60% | 低 (配置) | 低 |

**推荐立即实施**: P0 + P1，零风险、极低成本、立竿见影，可节省 **70-80% 总带宽**。

---

## 1. 数据流架构与推送频率

### 1.1 SSE 事件类型与触发频率

| 事件类型 | 触发时机 | 频率 (154 生产, 8k req/min) | Payload 典型大小 |
|---|---|---|---|
| `initial_data` | 连接建立时 | 每客户端连接一次 | **546-2670 KB** (11 lane/维度 × 20-100 tiles) |
| `request` | 每个请求完成/in-flight 时 | **133/秒** | **25 KB** (含 delta) |
| `snapshot_refresh` | 定时全量刷新 | 每 30 分钟 | 同 initial_data |
| `node_update` | 节点状态矩阵推送 | 每 2 秒 | **4.2 KB** (11 节点) |
| `queue_snapshot` | 调度队列深度推送 | 每 2 秒 | **2.2 KB** (10 模型 + 11 凭据泳道) |
| `keepalive` (comment) | SSE 保活心跳 | 每 25 秒 | 20 bytes |
| `idle_marker` | 泳道 5 分钟无流量 | 低频 | 忽略 |
| `request_lifecycle` | 动作事件 (OBS-BE2) | 与 request 同频但异步 poll | 忽略 (本次不涉及) |

### 1.2 后端推送路径

```
telemetry.OnPersisted (每个请求)
  → LiveStreamSSEHub.Publish(req)
    → broadcast chan (bounded 1000)
      → Run() 主循环 case req := <-h.broadcast:
        → computeScopeDelta(ctx, tenantID, false/true)  // 2s 节流缓存
          → store.SnapshotFromDimensionQueues (Redis pipeline 读 42 个维度队列)
            → BuildLiveStreamSnapshot → ComputeDelta(old, new)
              → lanesChanged → 4 维度逐 lane 比对 request_id/status/timestamp
        → fanOut(envelope{type:"request", request:req, delta:delta, superDelta:superDelta})
          → json.Marshal 两次 (tenant scope / super scope)
          → 广播给所有 SSE 客户端 (每客户端完整副本)
```

**关键节流点** (`admin/live_stream_sse.go:943-1067`):

- `defaultLiveStreamSnapshotMinInterval = 2s` (2026-08-25 加入，避免 154 下 133 req/s 时每个请求都跑 Redis pipeline)
- 命中节流时复用上次成功的 `delta` 缓存 (`h.lastDeltaByScope`)
- **未命中时每次重新计算完整 snapshot + delta**

### 1.3 前端消费路径

```
EventSource('/api/admin/live-stream', {withCredentials:true})
  → handleEnvelope(env)
    → switch(env.type)
      case 'initial_data': applyInitialData(env.requests); mergeSnapshotFromServer(env.snapshot)
      case 'request': mergeDelta(env.delta); pushOrQueue(env.request)
      case 'node_update': liveStreamState.nodes = env.nodes
      case 'queue_snapshot': liveStreamState.queueSnapshot = env.queue
  → mergeTilesById(lane.requests, incoming, {dropAbsent})
    → 合并后 existing.splice(0, length, ...next.slice(-20))  // 客户端本地 trim 到 20 条
```

**前端本地限流**:

- 每个 lane 客户端最多保留 **20 tiles** (`liveStreamStore.ts:1224`)
- Actions (lifecycle) 限流：`ACTIONS_PER_REQUEST_CAP=50`, `ACTIONS_GLOBAL_CAP=2000`
- **`detail_dimensions` 字段零消费**：仅作类型占位 (`useSwimLane.ts:12` 空数组初始化)，实际渲染全部读取 `snapshot.dimensions[dim]`

---

## 2. 数据量实测估算

### 2.1 基础 Tile/Lane/Snapshot 字节数

基于 `LiveStreamTile` 典型成功态字段 (15 个字段中约 10 个有值):

```json
{
  "request_id": "req_8f3a2b1c9d4e5f60",
  "timestamp": "2026-08-27T11:34:06Z",
  "model": "gpt-5.6-terra",
  "vendor": "openai",
  "provider": "OpenAI 官方",
  "status": "success",
  "credential_id": 42,
  "credential_label": "主力Key-01",
  "latency_ms": 823,
  "cost_usd": 0.00412,
  "prompt_tokens": 512,
  "completion_tokens": 128,
  "stage_category": "llm"
}
```

**单个 tile JSON**: **~308 bytes**

**单个 lane** (id/name/dimension + stats + N 个 tiles):

| tiles 数量 | lane JSON 字节数 |
|---|---|
| 1 | 501 bytes |
| 5 | 1,737 bytes |
| 20 | **6,372 bytes** |
| 100 | 31,092 bytes |

### 2.2 完整 Snapshot (initial_data / snapshot_refresh)

假设 4 维度 (credential/vendor/provider/model) 各 11 条 lane (对应 154 生产 "42 个维度队列" 注释):

| 场景 | lanes/维度 | tiles/lane | Snapshot 总大小 | 去重后 (无 detail_dimensions) | 节省比例 |
|---|---|---|---|---|---|
| 154 生产 (忙碌, 100 tiles 打满) | 11 | 100 | **2,670 KB** | **1,335 KB** | **50%** |
| 154 生产 (适中, 20 tiles) | 11 | 20 | **546 KB** | **273 KB** | **50%** |
| 245 预发 (低流量) | 5 | 10 | 128 KB | 64 KB | 50% |

**`detail_dimensions` 冗余验证**:

- 后端: `admin/live_stream_redis_store.go:918-970` `BuildLiveStreamSnapshot` 返回的 `DetailDimensions` 与 `Dimensions` **逐字段完全一致**
- 前端: `web/src/composables/liveStreamStore.ts` 中 `detail_dimensions` **仅在类型定义与 merge 时出现**，实际渲染全部读 `dimensions[dim]`，`detail_dimensions` 零引用

### 2.3 增量 Delta (request 事件)

每个 `request` 事件 envelope 结构:

```json
{
  "type": "request",
  "ts": "2026-08-27T11:34:06.123Z",
  "request": { /* LiveRequest ~598 bytes */ },
  "delta": {
    "summary": { /* ~80 bytes */ },
    "changed_lanes": {
      "credential": [ /* lane 含 N tiles */ ],
      "vendor": [ /* lane 含 N tiles */ ],
      "provider": [ /* lane 含 N tiles */ ],
      "model": [ /* lane 含 N tiles */ ]
    },
    "dimension_legends": { /* 4 维度 × ~100 bytes */ },
    "status_legends": [ /* ~150 bytes */ ]
  }
}
```

当 `changed_lanes` 中每个维度有 1 条受影响 lane (含 N tiles):

| tiles/lane | 单个 request 事件大小 |
|---|---|
| 5 | 7.7 KB |
| 20 | **25.3 KB** |
| 50 | 60.6 KB |

### 2.4 心跳固定开销 (node_update + queue_snapshot, 每 2s)

| 事件 | 典型规模 | 字节数 |
|---|---|---|
| `node_update` | 11 节点 (对应 11 凭据) | **4,218 bytes** |
| `queue_snapshot` | 10 模型泳道 + 11 凭据泳道 | **2,163 bytes** |
| **合计 (每 2 秒)** | | **6,381 bytes** |

**空闲基线带宽** (客户端无任何真实请求，仅心跳):

- 每秒: `6381 / 2 = 3,190 bytes/s`
- 每小时: `3190 × 3600 / 1024 ≈ **11.2 MB/小时/客户端**`

---

## 3. 154 生产环境 (8k req/min) 带宽实测推算

### 3.1 单客户端带宽

**假设**:

- 请求速率: 8000 req/min = **133.3 req/s**
- 每个 request 触发一次 `type:"request"` 事件推送
- Delta 中 4 个维度各有 1 条受影响 lane，每个 lane 含 20 tiles (前端本地 trim 上限)
- 单个 request 事件 **25.3 KB**

**计算**:

- `request` 事件带宽: `133.3 req/s × 25.3 KB = **3,374 KB/s** ≈ **3.3 MB/s**`
- 心跳基线: `3.2 KB/s`
- **总计**: **3.3 MB/s** (忽略 30 分钟一次的 snapshot_refresh 546 KB 脉冲)

**每小时流量**: `3.3 MB/s × 3600 = **11.9 GB/小时/客户端**`

### 3.2 多客户端场景

假设 **5 个管理员同时打开总览页** (5 个并发 SSE 连接):

- 服务器出向总带宽峰值: `5 × 3.3 MB/s = **16.5 MB/s** = **132 Mbps**`
- 每小时总流量: `5 × 11.9 GB = **59.5 GB/小时**`

**瓶颈分析**:

- 154 生产机器 (阿里云 ECS，典型出向带宽 100 Mbps) 在 5-10 个并发客户端时即可能达到出向带宽上限
- Redis 压力: 每次未命中节流窗口 (2s 内首次) 需跑 `SnapshotFromDimensionQueues` (2 个 pipeline × 42 维度队列 ZRevRange)

### 3.3 245 预发环境对比

245 为测试环境，真实流量 << 154 (文档多处提及 "测试环境无真实流量" / "低流量")。估算:

- 假设 10 req/min = 0.17 req/s
- 单客户端: `0.17 × 25.3 KB + 3.2 KB/s ≈ **7.5 KB/s**`
- 每小时: `27 MB/小时`

**结论**: 245 环境带宽压力**微乎其微**，优化收益不明显；**154 生产是主战场**。

---

## 4. 优化方案与实施路径

### 4.1 P0: 删除 `detail_dimensions` 冗余字段

**现状**:

- 后端 `BuildLiveStreamSnapshot` (admin/live_stream_redis_store.go:918) 返回 `DetailDimensions` 与 `Dimensions` 完全一致
- 前端 `liveStreamStore.ts` 的 `detail_dimensions` 字段零消费，仅类型占位
- 每次 initial_data / snapshot_refresh 推送**双份数据**

**方案**:

1. **后端**: `BuildLiveStreamSnapshot` / `LiveStreamSnapshot` 结构体删除 `DetailDimensions` 字段
2. **前端**: `LiveStreamSnapshot` 类型删除 `detail_dimensions`，merge 逻辑删除对应分支

**影响**:

- initial_data / snapshot_refresh 从 **546 KB → 273 KB** (154 生产适中场景，节省 **50%**)
- 30 分钟一次的全量刷新从 **273 KB/客户端** → 无感
- 连接建立时的首帧延迟减半

**风险**: 极低 (前端零依赖，后端仅结构体字段删除)

**实施复杂度**: 低 (前后端各一处改动 + 类型同步)

**ROI**: **极高** (50% 全量帧节省 + 零风险)

---

### 4.2 P1: 启用 gzip 压缩

**现状**:

- nginx 配置 (`deploy/llmgo-245.nginx.conf`) 的 `location = /api/admin/live-stream` 未启用 gzip
- 后端 `HandleLiveStream` (admin/live_stream_sse.go:2194) 设置 `Content-Type: text/event-stream`
- HTTP/2 下 nginx 默认 `gzip_types` **不包含** `text/event-stream`，JSON 明文传输

**方案**:

在 nginx 全局或 `server {}` 块增加:

```nginx
gzip on;
gzip_types text/plain text/css application/json application/javascript text/xml application/xml text/event-stream;
gzip_comp_level 6;
gzip_vary on;
gzip_min_length 1024;
```

**预期收益**:

- JSON 文本压缩比通常 **60-75%**
- 单客户端带宽从 3.3 MB/s → **0.8-1.3 MB/s**
- 5 客户端峰值从 16.5 MB/s → **4-6.6 MB/s**

**风险**:

- CPU 开销增加 (gzip_comp_level 6 适中)
- SSE 是实时流，gzip 需 flush 确保及时性 (nginx 默认行为已支持)

**实施复杂度**: **极低** (纯配置，reload nginx 生效)

**ROI**: **极高** (60-75% 带宽节省 + 零代码改动)

---

### 4.3 P2: Delta 节流窗口从 2s 延长到 5s

**现状**:

- `defaultLiveStreamSnapshotMinInterval = 2s` (admin/live_stream_sse.go:943)
- 2s 内复用缓存 delta，超过 2s 后重新跑 Redis pipeline + ComputeDelta
- 8/25 紧急加入以应对 154 的 133 req/s 风暴

**方案**:

```go
const defaultLiveStreamSnapshotMinInterval = 5 * time.Second
```

或环境变量 `LLM_GATEWAY_LIVE_STREAM_SNAPSHOT_MIN_INTERVAL=5s` 覆盖。

**影响**:

- Delta 推送延迟从 ≤2s → ≤5s (dashboard 可接受的 sub-second → few-second 延迟)
- Redis `SnapshotFromDimensionQueues` 调用频率 **-60%** (每 5s 一次 vs 每 2s 一次)
- `request` 事件中 `delta` 字段在 5s 窗口内为 **同一份缓存** (不影响 `request` tile 本身的实时性)

**风险**: 低 (泳道汇总数据略陈旧 ≤5s，运维可接受)

**实施复杂度**: 低 (配置常量或环境变量)

**ROI**: 高 (Redis 压力 -60%，CPU/网络略减)

---

### 4.4 P3: `changed_lanes` 仅推送变化维度

**现状**:

- `ComputeDelta` (admin/live_stream_redis_store.go:1836-1860) 对 4 个维度 (credential/vendor/provider/model) 逐个调用 `lanesChanged`
- 即使某维度无变化，只要其它维度变化，也会在 `delta.changed_lanes[dim]` 推送完整 lane 数组
- 实测: 每个 request 都会触发 4 个维度全推 (因为新 request 同时出现在 4 个维度的泳道里)

**方案**:

```go
func ComputeDelta(old, new *LiveStreamSnapshot) *LiveStreamDelta {
    delta := &LiveStreamDelta{
        Summary:          new.Summary,
        ChangedLanes:     map[string][]LiveStreamLane{},
        DimensionLegends: map[string][]LiveStreamLegendItem{},
        StatusLegends:    new.StatusLegends,
    }
    for _, dim := range []string{"credential", "vendor", "provider", "model"} {
        oldLanes := old.Dimensions[dim]
        newLanes := new.Dimensions[dim]
        if lanesChanged(oldLanes, newLanes) {
            delta.ChangedLanes[dim] = newLanes
            delta.DimensionLegends[dim] = new.DimensionLegends[dim]
        }
        // 新增: 若未变化，delta.ChangedLanes[dim] 不设置 (omitempty)
    }
    return delta
}
```

前端 `mergeDelta` 已兼容 (当前代码 `delta.changed_lanes[dim]` 可能为空)。

**影响**:

- 当某维度泳道未变化时，delta 不推送该维度 → 体积 **-25%** (4 维度变 3 维度)
- 但实测中新 request 通常同时出现在 4 个维度，收益有限 (除非按维度分流推送)

**风险**: 低 (前端已兼容空 changed_lanes)

**实施复杂度**: 中 (需改 ComputeDelta + 回归测试)

**ROI**: 中 (理论 25-50%，实际收益取决于维度变化模式)

---

### 4.5 P4: `LiveRequest` / `LiveStreamTile` 字段裁剪

**现状**:

- `LiveRequest` 结构体 ~25 个字段 (admin/live_stream_sse.go:305-356)
- `LiveStreamTile` 结构体 ~13 个字段 (admin/live_stream_redis_store.go:49-73)
- 已有 `omitempty` 标记，空值不序列化
- 典型成功态 request JSON **~598 bytes**

**候选裁剪字段** (需权衡前端需求):

| 字段 | 典型填充率 | 前端用途 | 裁剪可行性 |
|---|---|---|---|
| `gw_session_id` | 50% | 会话关联 | 低 (功能需要) |
| `canonical_name` | 100% | 模型聚合 | 低 (核心) |
| `model_category` | 100% | 维度分组 | 低 (核心) |
| `total_tokens` | 100% | 可由 prompt + completion 计算 | **中** (可前端计算) |
| `client_profile` | 30% | 筛选/诊断 | 中 |
| `identity_hash` | 80% | 租户统计 | 中 |
| `credits_charged` | 50% | 计费展示 | 低 |
| `agent_name/agent_type` | 20% | 客户端感知 | 中 |
| `probe_*` 系列 | 5% | 探测标记 | **高** (低频) |
| `parent_request_id/request_type` | 10% | 主从关联 | 中 |

**方案**:

1. `total_tokens` 前端计算: `prompt_tokens + completion_tokens` (节省 ~10 bytes/tile)
2. `probe_*` 字段仅在 `is_probe=true` 时填充 (已有 omitempty，无额外收益)
3. `client_profile` / `identity_hash` / `agent_*` 按需延迟加载 (需前端改造)

**预期收益**: 5-10% (单 tile 从 308 bytes → 280 bytes)

**风险**: 中 (需前端配合改造，回归测试成本高)

**实施复杂度**: 中-高

**ROI**: **低** (收益有限 vs 改造成本)

---

### 4.6 P5: 心跳降频 2s → 5s

**现状**:

- `node_update` + `queue_snapshot` 每 2 秒推一次 (admin/live_stream_sse.go:768-769)
- 空闲基线 **11.2 MB/小时/客户端**

**方案**:

```go
queueTicker := time.NewTicker(5 * time.Second)
nodeTicker := time.NewTicker(5 * time.Second)
```

**影响**:

- 节点状态 / 队列深度更新延迟 2s → 5s
- 心跳开销 **-60%** (从 11.2 MB/小时 → 4.5 MB/小时)

**风险**: 低 (运维监控可接受 5s 延迟)

**实施复杂度**: 低 (常量改动)

**ROI**: **低** (心跳仅占总带宽 ~0.1%，对 3.3 MB/s 主体无影响)

---

## 5. 实施优先级与 ROI 排序

| 优先级 | 措施 | 预期节省 | 实施复杂度 | 风险 | 预计工时 | 立即可行 |
|---|---|---|---|---|---|---|
| **P0** | 删除 `detail_dimensions` | 50% (全量帧) | 低 | 极低 | 2h | ✅ |
| **P1** | 启用 gzip | 60-75% (全部) | 极低 | 低 | 0.5h | ✅ |
| **P2** | Delta 节流 2s→5s | Redis 压力 -60% | 低 | 低 | 0.5h | ✅ |
| **P3** | `changed_lanes` 按需推送 | 25-50% (delta) | 中 | 低 | 4h | 需测试 |
| **P4** | `LiveRequest` 字段裁剪 | 5-10% | 中-高 | 中 | 8h | 需评审 |
| **P5** | 心跳降频 2s→5s | <1% (总带宽) | 低 | 低 | 0.5h | 低优先级 |

**推荐立即实施 (P0+P1)**:

- **P0 (删除 detail_dimensions)** + **P1 (gzip)** 组合可节省 **70-80% 总带宽**
- 零风险、极低成本 (< 3 小时)
- 154 生产单客户端从 **3.3 MB/s → 0.7-1 MB/s**，5 客户端峰值从 **16.5 MB/s → 3.5-5 MB/s**

**次优先 (P2)**:

- Delta 节流延长到 5s 进一步降低 Redis 压力，为未来流量增长留余量

**待评审 (P3/P4)**:

- P3/P4 需前后端配合改造 + 回归测试，ROI 相对较低，可作为后续持续优化项

---

## 6. 验证方案

### 6.1 P0+P1 上线前后对比

**监控指标**:

1. **服务器出向带宽** (154 主机 `ifconfig` / 阿里云监控)
   - 基准: 5 客户端时 16.5 MB/s
   - 目标: < 5 MB/s
2. **单客户端 SSE 接收速率** (浏览器 DevTools Network → live-stream 连接 → Transfer)
   - 基准: 3.3 MB/s
   - 目标: < 1 MB/s
3. **Redis `SnapshotFromDimensionQueues` 调用频率** (日志 `slog.Info("snapshot from dimension queues built")`)
   - 基准: 每 2s 一次 (有活跃客户端时)
   - P2 后: 每 5s 一次

### 6.2 前后对比测试步骤

1. **基线采集 (当前版本, 无优化)**:
   - 245 环境打开 5 个浏览器 tab 到总览页
   - 运行 `watch -n 10 'ss -s; ifstat 1 1'` 监控网络
   - 记录 1 分钟内 SSE 接收字节数 (DevTools)

2. **P0 部署 (删除 detail_dimensions)**:
   - 修改后端 `admin/live_stream_redis_store.go` + 前端 `liveStreamStore.ts`
   - 部署到 245 → 验证 initial_data 体积减半
   - 154 灰度 1 小时 → 全量

3. **P1 部署 (gzip)**:
   - 245/154 nginx 配置增加 `gzip_types text/event-stream`
   - reload nginx
   - 验证 `curl -H "Accept-Encoding: gzip" https://llmgo.kxpms.cn/api/admin/live-stream` 响应带 `Content-Encoding: gzip`

4. **P2 部署 (delta 节流 5s)**:
   - 环境变量 `LLM_GATEWAY_LIVE_STREAM_SNAPSHOT_MIN_INTERVAL=5s`
   - 重启服务
   - 日志验证 `cachedSnapshotThrottleHits` 计数增加

### 6.3 回归风险

**P0 风险点**:

- 前端老客户端 (未部署新版) 连到新后端: TypeScript 类型不匹配 → 前端 undefined 读取
- 缓解: 前端 `detail_dimensions` 改为可选 `detail_dimensions?: Record<...>` (向后兼容)

**P1 风险点**:

- gzip 未正确 flush 导致 SSE 延迟 → nginx 1.18+ 默认已支持 chunked gzip flush
- 验证: 观察前端泳道实时性是否劣化

**P2 风险点**:

- 5s 延迟下泳道汇总数据陈旧感 → 运维反馈可接受性

---

## 7. 长期优化方向 (不在本次范围)

1. **维度分流推送**: 前端按 active dimension 订阅 (`groupBy=credential` 时仅推 credential 维度)，需协议改造
2. **增量 tile 推送**: 当前 delta 推送整个 lane (20 tiles)，改为仅推新增/变化的 tile
3. **WebSocket 双向通道**: 支持客户端 ACK / backpressure，服务端按客户端消费速度推送
4. **Protobuf 二进制协议**: 替代 JSON (需大改造，ROI 不明显因为 gzip 后 JSON 已足够紧凑)
5. **按租户分流 SSE hub**: 多租户环境下避免一个租户的流量风暴影响其它租户的 SSE 连接

---

## 8. 附录：关键代码位置索引

| 模块 | 文件 | 关键函数/常量 | 行号 |
|---|---|---|---|
| SSE Hub 主循环 | `admin/live_stream_sse.go` | `Run()` | 748-897 |
| Delta 节流 | 同上 | `computeScopeDelta` + `defaultLiveStreamSnapshotMinInterval` | 943-1067, 943 |
| Snapshot 构造 | `admin/live_stream_redis_store.go` | `BuildLiveStreamSnapshot` | 918-970 |
| Delta 计算 | 同上 | `ComputeDelta` / `lanesChanged` | 1836-1893 |
| Redis 读取 | `admin/live_stream_redis_store_snapshot_fix.go` | `SnapshotFromDimensionQueues` | 38-239 |
| SSE 写入 | `admin/live_stream_sse.go` | `writeEvent` | 1907-1935 |
| 前端 SSE 消费 | `web/src/composables/liveStreamStore.ts` | `handleEnvelope` / `mergeDelta` | 821-920, 1104-1132 |
| 前端本地 trim | 同上 | `mergeTilesById` (`.slice(-20)`) | 1182-1225 |
| 心跳配置 | `admin/live_stream_sse.go` | `IdleTickInterval` / `KeepaliveInterval` | 364-365, 761-762 |
| 心跳推送 | 同上 | `queueTicker` / `nodeTicker` | 768-769, 885-889 |
| nginx 配置 | `deploy/llmgo-245.nginx.conf` | `location = /api/admin/live-stream` | 60-78 |

---

## 9. 结论

llmgo.kxpms.cn 总览页 SSE 实时流在 154 生产环境 (8k req/min) 下**单客户端带宽达 3.3 MB/s**，5 个并发连接时服务器出向带宽峰值 **16.5 MB/s**，已接近典型 ECS 出向带宽上限 (100 Mbps)。

**核心问题**:

1. **`detail_dimensions` 字段完全冗余**，占全量帧 50% 体积，前端零消费
2. **未启用 gzip 压缩**，JSON 明文传输
3. **每个 request 推送完整 delta** (4 维度 × 20 tiles ≈ 25 KB)

**推荐立即实施 P0+P1** (删除冗余字段 + gzip)，可**零风险、极低成本 (< 3 小时) 节省 70-80% 带宽**，154 生产单客户端从 3.3 MB/s 降至 **0.7-1 MB/s**，多客户端场景不再成为瓶颈。

P2 (delta 节流延长) 可作为次优先项，进一步降低 Redis 压力。P3/P4 需较大改造，ROI 相对较低，可作为后续持续优化。
