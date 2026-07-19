# 泳道跳变问题深度分析

**日期**: 2026-07-19  
**问题**: 前端多维度泳道图中数据大幅跳变，不像增量更新  
**数据源**: Redis（不是数据库查询）

---

## 关键发现

### 1. 数据流架构

```
请求进入网关
  ↓
telemetry.Client.EmitRequestLog()  
  ↓ (触发 onEmitted hook)
LiveStreamSSEHub 收到通知
  ↓
LiveStreamRedisStore.Track()      ← 写入 Redis ZSET
  ↓
计算 delta 并推送 SSE
  ↓
前端收到增量更新
```

### 2. Redis 数据结构

**文件**: `admin/live_stream_redis_store.go`

```
主队列: ZSET llmgw:live:main
  - score = unix_ms (时间戳毫秒)
  - member = JSON 序列化的请求记录
  - TTL = 2 小时

维度队列: 
  - ZSET llmgw:live:dim:vendor:{vendor}
  - ZSET llmgw:live:dim:provider:{provider_id}
  - ZSET llmgw:live:dim:model:{model}
  
状态队列:
  - ZSET llmgw:live:status:success
  - ZSET llmgw:live:status:failure
  - ZSET llmgw:live:status:rate_limited
  - ZSET llmgw:live:status:in_progress
```

### 3. 推送机制（两种模式）

**文件**: `admin/live_stream_sse.go`

#### 3.1 增量推送（Delta）

- **触发**: 每次新请求到达时
- **逻辑**: `computeScopeDelta()` 计算与缓存快照的差异
- **推送**: 只发送变化的部分（delta）
- **SSE 消息类型**: `"delta"`

```go
// line 464-511
func (h *LiveStreamSSEHub) computeScopeDelta(ctx context.Context, tenantID string, isSuper bool) *LiveStreamDelta {
    // 从 Redis 读取最新快照
    fresh, err := h.store.Snapshot(ctx, tenantID, isSuper, h.cfg.InitialReplayLimit)
    
    // 与缓存快照对比
    old := cached.snapshot
    
    // 计算差异
    delta := computeDelta(old, fresh)
    
    // 更新缓存
    cached.snapshot = fresh
    
    return delta
}
```

#### 3.2 全量推送（Full Snapshot Refresh）

- **触发**: 定时器，默认 **30 分钟**一次
- **逻辑**: `pushFullSnapshots()` 从 Redis 读取完整快照
- **推送**: 发送所有数据（全量替换）
- **SSE 消息类型**: `"snapshot_refresh"`

```go
// line 548-603
func (h *LiveStreamSSEHub) pushFullSnapshots() {
    // 每 30 分钟执行一次
    snapshot, err := h.store.Snapshot(ctx, entry.tenantID, entry.isSuper, h.cfg.InitialReplayLimit)
    
    // 更新缓存
    h.cachedSnapshot[scope.cacheKey] = &cachedSnapshotEntry{
        snapshot: snapshot,
    }
    
    // 推送全量快照
    env := LiveStreamEnvelope{
        Type:      "snapshot_refresh",  // ← 关键：告诉前端"全量替换"
        Snapshot:  snapshot,
    }
    h.fanOut(env)
}
```

---

## 跳变的根本原因

### 原因 1: 全量快照推送（每 30 分钟）

**现象**: 前端每 30 分钟收到 `"snapshot_refresh"` 消息

**问题**: 如果前端收到这个消息后**全量替换**本地数据，会导致：
1. 所有泳道瞬间重建
2. 视觉上出现"跳变"
3. 即使数据实际上没变，UI 也会重新渲染

**证据**:
```go
// admin/live_stream_sse.go:238-239
if c.SnapshotRefreshInterval <= 0 {
    c.SnapshotRefreshInterval = 30 * time.Minute  // ← 默认 30 分钟
}
```

### 原因 2: Redis 数据被 TRIM 了

**现象**: Redis ZSET 的 TTL 是 2 小时，但前端可能缓存了更久的数据

**问题**: 
1. Redis 定期 TRIM 掉 2 小时前的数据
2. 后端从 Redis 读到的快照比前端缓存的数据**少**
3. 前端收到 delta 后，某些泳道的请求数突然减少
4. 视觉上表现为"数据跳变"

**证据**:
```go
// admin/live_stream_redis_store.go:28
// TTL: LiveStreamRecordRetention (default 2 hours, product minimum)
```

### 原因 3: 前端处理 `snapshot_refresh` 的方式不当

**如果前端逻辑是**:
```typescript
// ❌ 错误：全量替换
if (msg.type === 'snapshot_refresh') {
    setLanes(msg.snapshot.dimensions);  // 完全替换，导致跳变
}
```

**应该是**:
```typescript
// ✅ 正确：平滑合并
if (msg.type === 'snapshot_refresh') {
    // 逐个泳道比对，只更新变化的部分
    mergeLanesSmooth(currentLanes, msg.snapshot.dimensions);
}
```

---

## 验证方法

### 1. 检查前端是否收到 `snapshot_refresh`

打开浏览器开发者工具，查看 EventSource 消息：

```javascript
// 在前端 Console 中
const es = new EventSource('/api/admin/live-stream');
es.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    if (msg.type === 'snapshot_refresh') {
        console.log('🔄 收到全量快照推送', msg);
    }
};
```

**如果每 30 分钟收到一次 `snapshot_refresh`，且随后出现跳变 → 确认是原因 1**

### 2. 检查 Redis 中的数据量

```bash
# 连接到 Redis
redis-cli -h <redis_host> -p <redis_port>

# 查看主队列长度
ZCARD llmgw:live:main

# 查看最早和最晚的记录时间戳
ZRANGE llmgw:live:main 0 0 WITHSCORES  # 最早
ZRANGE llmgw:live:main -1 -1 WITHSCORES  # 最晚

# 检查是否有数据被 TRIM（时间跨度 < 2 小时但前端显示更久）
```

**如果 Redis 数据时间跨度 < 前端显示的时间跨度 → 确认是原因 2**

### 3. 检查前端处理逻辑

查看前端代码中 SSE 消息处理：

```typescript
// 搜索 'snapshot_refresh' 的处理逻辑
// 检查是否做了全量替换
```

---

## 修复方案

### 方案 A: 禁用或延长全量快照推送间隔（后端）

**修改**: `admin/live_stream_sse.go`

```go
// 当前默认 30 分钟，改为 2 小时或更长
if c.SnapshotRefreshInterval <= 0 {
    c.SnapshotRefreshInterval = 2 * time.Hour  // ← 改为 2 小时
}
```

**优点**: 减少全量推送频率  
**缺点**: 如果 Redis 和前端数据偏离太多，修正会延迟

### 方案 B: 优化 `snapshot_refresh` 推送策略（后端）

**修改**: 只在**真正需要**时推送全量快照，而非定时推送

```go
func (h *LiveStreamSSEHub) pushFullSnapshots() {
    // 只在检测到 Redis 数据与缓存差异较大时推送
    if needsFullRefresh(cached, fresh) {
        h.fanOut(LiveStreamEnvelope{Type: "snapshot_refresh", ...})
    }
}

func needsFullRefresh(cached, fresh *LiveStreamSnapshot) bool {
    // 例如：总请求数差异 > 20% 才推送
    if cached == nil {
        return true
    }
    diff := abs(cached.Summary.Total - fresh.Summary.Total)
    return diff > cached.Summary.Total / 5
}
```

### 方案 C: 前端平滑处理 `snapshot_refresh`（推荐）

**修改**: 前端代码

```typescript
// ❌ 当前（猜测）
function handleSnapshotRefresh(msg) {
    setLanes(msg.snapshot.dimensions);  // 全量替换
}

// ✅ 优化后
function handleSnapshotRefresh(msg) {
    setLanes(prevLanes => {
        // 逐个维度、逐个泳道平滑合并
        const merged = {};
        for (const dim of ['vendor', 'provider', 'model']) {
            merged[dim] = mergeLanesSmooth(
                prevLanes[dim] || [],
                msg.snapshot.dimensions[dim] || []
            );
        }
        return merged;
    });
}

function mergeLanesSmooth(oldLanes, newLanes) {
    // 按 lane.id 匹配，保留旧的请求记录，只添加新的
    const laneMap = new Map(oldLanes.map(l => [l.id, l]));
    
    return newLanes.map(newLane => {
        const oldLane = laneMap.get(newLane.id);
        if (!oldLane) {
            return newLane;  // 新泳道，直接使用
        }
        
        // 合并请求记录（按 request_id 去重）
        const oldReqIds = new Set(oldLane.requests.map(r => r.id));
        const newReqs = newLane.requests.filter(r => !oldReqIds.has(r.id));
        
        return {
            ...newLane,
            requests: [...newReqs, ...oldLane.requests].slice(0, 20)  // 保留最新 20 条
        };
    });
}
```

### 方案 D: 增加前端本地缓存时长对齐（前端）

确保前端缓存时长 ≤ Redis TTL（2 小时）

```typescript
// 定期清理超过 2 小时的本地缓存数据
setInterval(() => {
    const twoHoursAgo = Date.now() - 2 * 60 * 60 * 1000;
    lanes.forEach(lane => {
        lane.requests = lane.requests.filter(r => r.ts > twoHoursAgo);
    });
}, 10 * 60 * 1000);  // 每 10 分钟清理一次
```

---

## 推荐实施顺序

1. **立即**: 方案 C（前端平滑处理） - 成本最低，效果最好
2. **短期**: 方案 D（前端缓存对齐） - 防止数据不一致
3. **中期**: 方案 B（优化推送策略） - 减少不必要的全量推送
4. **可选**: 方案 A（延长间隔） - 如果方案 C 仍有跳变

---

## 验证计划

1. **部署前**: 在浏览器 Console 监控 30 分钟，记录 `snapshot_refresh` 出现次数
2. **部署后**: 同样监控 30 分钟，确认跳变消失
3. **长期**: 监控前端错误日志，检查是否有数据不一致的报错

---

## 相关文件

- `admin/live_stream_redis_store.go` - Redis 存储实现
- `admin/live_stream_sse.go` - SSE 推送逻辑
- `admin/live_stream_stats_handler.go` - 统计 API
- 前端代码：需要定位具体的 SSE 消息处理文件

---

**结论**: 泳道跳变的根本原因很可能是**每 30 分钟的全量快照推送 + 前端全量替换处理**。
建议优先修复前端的 `snapshot_refresh` 处理逻辑，改为平滑合并而非全量替换。
