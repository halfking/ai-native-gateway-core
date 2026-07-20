# 泳道"滚动"现象根因分析

**部署版本**: `82b5edcf22e9a141ed88731f30fa7ebfccf9a6f6`  
**问题描述**: 泳道数据像在"滚动"，没有大量请求进来，但数字跳变明显  
**时间特征**: 不是每5分钟发生，间隔不规律

---

## 🎯 核心发现

代码注释中已经明确指出了问题（commit `9095d788`）：

```go
// 2026-07-20: Sort ASC by timestamp so grouped[key] inside
// buildLiveStreamLanes is also ASC (oldest first, newest at the
// tail). Without this, the order is determined by redis SCAN +
// cross-dimKey deduplication order, which is non-deterministic
// across calls. lastTiles() then picks an unstable subset and
// the frontend sees different request_id sets in consecutive
// snapshots — the root cause of the swim-lane flicker reported
// on 245 (cache/window count dropping by a handful on every new
// request).
```

**问题本质**: Redis SCAN的顺序是**非确定性的**，导致每次读取的20个tile窗口不一致。

---

## 📊 问题机制详解

### 1. Redis维度队列的结构

```
llmgw:live:dimension:vendor:minimax
  - ZSET，score = timestamp
  - 可能包含100+条记录（在2小时TTL内）
  - 每次查询只读取最新20条: ZRevRange(key, 0, 19)
```

### 2. SnapshotFromDimensionQueues的读取流程

```go
// Step 1: 扫描所有维度队列键
dimKeys, err := s.discoverDimensionQueues(ctx, tenantID, isSuper)
// dimKeys = ["...:vendor:minimax", "...:vendor:openai", "...:provider:NVIDIA", ...]
// 顺序由 Redis SCAN 决定，是随机的！

// Step 2: 从每个队列读取20条
for _, key := range dimKeys {
    requestIDs, _ := s.rdb.ZRevRange(ctx, key, 0, 19).Result()
    // 每个队列各读20条
    // 如果一个请求同时在多个维度队列中，会去重
}

// Step 3: 去重后的allRequests
// 问题：dimKeys的顺序影响去重结果！
```

### 3. "滚动"的真相

**场景**：MiniMax泳道有150个请求在Redis中

**第1次查询** (20:46):
```
SCAN找到的顺序: [vendor:minimax, provider:NVIDIA, model:gpt-4, ...]
  ↓
从vendor:minimax读取20条: [req1, req2, ..., req20]
从provider:NVIDIA读取20条: [req5, req6, ..., req24]  // 部分重复
  ↓
去重后: 可能只有30条unique请求
  ↓
grouped['minimax'] = [req1, req2, ..., req15]  // 去重+分组后只剩15条
  ↓
lastTiles(grouped['minimax'], 20) = 全部15条
  ↓
stats.total = 15  // 但实际显示可能更少（某种过滤？）
```

**第2次查询** (21:08):
```
SCAN找到的顺序: [model:gpt-4, vendor:minimax, provider:NVIDIA, ...]  // 顺序变了！
  ↓
从model:gpt-4读取20条: [req10, req11, ..., req29]  // 先读了，标记为seen
从vendor:minimax读取20条: [req1, req2, ..., req20]
  ↓
去重: req10-req20已经seen，跳过
  ↓
grouped['minimax'] = [req1-req9, req21-...]  // 窗口"滑动"了
  ↓
stats.total = 40+  // 因为窗口不同，统计的请求集合不同
```

**结果**: 
- 没有新请求进来
- Redis数据没变
- 但因为SCAN顺序随机，读取的窗口每次不同
- 前端看到的就是数据在"滚动"

---

## 🐛 为什么修复还不够？

### Commit 9095d788 的修复

```go
// 修复后：在去重合并后，对allRequests按时间戳排序
sort.SliceStable(allRequests, func(i, j int) bool {
    return allRequests[i].Ts < allRequests[j].Ts
})
```

**修复的问题**: grouped[key]内部的tile顺序稳定了

**没有修复的问题**: 
1. **SCAN顺序仍然随机** → 不同次查询读到的request_id集合不同
2. **去重逻辑仍然依赖SCAN顺序** → 同一个请求可能在这次被读到，下次被跳过
3. **窗口仍然不稳定** → 每个泳道统计的20条tile是浮动的

---

## 🔬 问题复现推理

### 为什么你看到的跳变不规律？

因为Redis SCAN的顺序是**伪随机**的，取决于：
1. Redis的rehash状态
2. 并发写入导致的slot变化
3. 网络延迟（如果是Redis Cluster）

### 为什么是"滚动"而不是"闪烁"？

因为窗口是**渐进式变化**，不是完全替换：
```
第1次: [req1-req20]
第2次: [req5-req25]   // 部分重叠
第3次: [req10-req30]  // 继续滑动
```

看起来像是数据在向右滚动。

### 为什么数字会跳变？

stats.total是基于**当前窗口内的请求**统计的：
```go
for _, req := range items {
    key := liveStreamDimensionKey(dimension, req)
    st := stats[key]
    countStatus(&st, req.Status)
    stats[key] = st
}
```

如果窗口变了，统计基数就变了：
- 窗口1: 20个请求 → total=20
- 窗口2: 35个请求（因为SCAN顺序变化，去重后更多） → total=35

---

## 💡 真正的解决方案

### 方案A: 固定维度队列扫描顺序（推荐）

**核心思路**: 让SCAN结果排序，消除非确定性

```go
// admin/live_stream_redis_store_snapshot_fix.go:156-162
func (s *LiveStreamRedisStore) discoverDimensionQueues(ctx context.Context, tenantID string, isSuper bool) ([]string, error) {
    // ... 现有的SCAN逻辑 ...
    
    // ⚠️ 新增：排序，消除SCAN的随机性
    sort.Strings(allKeys)
    return allKeys, nil
}
```

**效果**:
- ✅ SCAN顺序固定 → 每次读到的request_id集合一致
- ✅ 去重逻辑稳定 → 窗口不再滑动
- ✅ stats.total稳定 → 数字不再跳变
- ⏱️ 排序开销极小（通常<100个key，ns级别）

---

### 方案B: 改用固定key列表，不依赖SCAN（终极方案）

**核心思路**: 维护一个已知维度值的缓存，避免SCAN

```go
// 在写入时记录维度值
func (s *LiveStreamRedisStore) Record(ctx context.Context, req LiveRequest) error {
    // ... 现有写入逻辑 ...
    
    // 记录已知的维度值到一个SET
    vendor := req.ModelCategory
    provider := req.ProviderCode
    model := req.CanonicalName
    
    s.rdb.SAdd(ctx, "llmgw:live:known_vendors", vendor)
    s.rdb.SAdd(ctx, "llmgw:live:known_providers", provider)
    s.rdb.SAdd(ctx, "llmgw:live:known_models", model)
}

// 在读取时使用固定列表
func (s *LiveStreamRedisStore) discoverDimensionQueues(ctx context.Context, tenantID string, isSuper bool) ([]string, error) {
    // 不再SCAN，而是从SET读取
    vendors, _ := s.rdb.SMembers(ctx, "llmgw:live:known_vendors").Result()
    providers, _ := s.rdb.SMembers(ctx, "llmgw:live:known_providers").Result()
    models, _ := s.rdb.SMembers(ctx, "llmgw:live:known_models").Result()
    
    var keys []string
    for _, v := range vendors {
        keys = append(keys, liveStreamDimPrefix+"vendor:"+v)
    }
    // ... provider, model同理 ...
    
    sort.Strings(keys)  // 确保顺序
    return keys, nil
}
```

**效果**:
- ✅ 完全消除SCAN的非确定性
- ✅ 性能更好（SMEMBERS比SCAN快）
- ⚠️ 需要维护额外的SET
- ⚠️ 需要清理过期的维度值

---

### 方案C: 前端缓存request_id集合，只更新差异

**核心思路**: 前端记住上次的request_id列表，只接受真正的变化

```typescript
// web/src/composables/liveStreamStore.ts
const lastSnapshotRequestIds = new Map<string, Set<string>>() // lane_id → request_ids

function mergeLanesById(existing: LiveStreamLane[], incoming: LiveStreamLane[]) {
    for (const lane of incoming) {
        const laneKey = lane.dimension + ':' + lane.id
        const lastIds = lastSnapshotRequestIds.get(laneKey) || new Set()
        const incomingIds = new Set(lane.requests.map(r => r.request_id))
        
        // 检测"伪变化"
        if (setsEqual(lastIds, incomingIds)) {
            // request_id集合没变，跳过更新（避免滚动效应）
            continue
        }
        
        // 真正的变化，更新
        lastSnapshotRequestIds.set(laneKey, incomingIds)
        // ... 正常合并逻辑 ...
    }
}

function setsEqual(a: Set<string>, b: Set<string>): boolean {
    if (a.size !== b.size) return false
    for (const item of a) {
        if (!b.has(item)) return false
    }
    return true
}
```

**效果**:
- ✅ 前端过滤掉"伪变化"
- ✅ 不需要后端改动
- ⚠️ 治标不治本（后端仍在发送不稳定数据）
- ⚠️ 可能错过真正的状态变化（如果request_id集合碰巧相同）

---

## 🧪 验证方法

### 验证1: 检查SCAN顺序是否随机

**在Redis中执行**:
```bash
# 多次执行，观察顺序是否变化
redis-cli --scan --pattern "llmgw:live:dimension:*" | head -20
redis-cli --scan --pattern "llmgw:live:dimension:*" | head -20
redis-cli --scan --pattern "llmgw:live:dimension:*" | head -20
```

**期望结果**: 三次的顺序不同 → 证实SCAN随机性

### 验证2: 前端日志对比snapshot差异

**浏览器Console执行**:
```javascript
const es = new EventSource('/api/admin/live-stream?token=...');
let lastSnapshot = null;

es.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    if (msg.type === 'request' && msg.delta) {
        const currentSnapshot = msg.delta.changed_lanes;
        
        if (lastSnapshot) {
            // 对比MiniMax泳道的request_id集合
            const lastMinimax = lastSnapshot.vendor?.find(l => l.id === 'minimax');
            const currMinimax = currentSnapshot.vendor?.find(l => l.id === 'minimax');
            
            if (lastMinimax && currMinimax) {
                const lastIds = new Set(lastMinimax.requests.map(r => r.request_id));
                const currIds = new Set(currMinimax.requests.map(r => r.request_id));
                
                // 计算差异
                const added = [...currIds].filter(id => !lastIds.has(id));
                const removed = [...lastIds].filter(id => !currIds.has(id));
                
                if (removed.length > 0 && added.length > 0) {
                    console.warn('🔄 检测到窗口滚动！', {
                        时间: new Date().toLocaleTimeString(),
                        旧total: lastMinimax.stats.total,
                        新total: currMinimax.stats.total,
                        移除的请求: removed.length,
                        新增的请求: added.length,
                        移除示例: removed.slice(0, 3),
                        新增示例: added.slice(0, 3)
                    });
                }
            }
        }
        
        lastSnapshot = currentSnapshot;
    }
};
```

**期望结果**: 
- 如果频繁出现"检测到窗口滚动"
- 且 `移除的请求` 和 `新增的请求` 数量相近
- 说明是窗口滑动，不是真实流量变化

### 验证3: 后端日志分析

**在服务器上执行**:
```bash
# 查看computeScopeDelta的调用频率
docker logs -f llm-gateway-container 2>&1 | grep "scope snapshot"

# 统计每次返回的request数量
docker logs llm-gateway-container 2>&1 | \
  grep "live stream scope snapshot" | \
  tail -20
```

**期望结果**: 
- 如果没有新请求，但snapshot的total数字仍在波动
- 说明是窗口不稳定导致的

---

## 📋 推荐实施方案

### 立即修复（1小时）- 方案A

```go
// admin/live_stream_redis_store_snapshot_fix.go:156行后添加
func (s *LiveStreamRedisStore) discoverDimensionQueues(...) ([]string, error) {
    // ... 现有代码 ...
    
    // 新增：排序，消除SCAN的非确定性
    sort.Strings(allKeys)
    return allKeys, nil
}
```

**验证**:
1. 修改代码
2. 重启服务
3. 运行验证2，观察"窗口滚动"警告是否消失

### 完整修复（1天）- 方案B

1. 在Record()中维护已知维度值的SET
2. 在discoverDimensionQueues()中使用SET代替SCAN
3. 添加维度值的TTL清理逻辑
4. 性能测试

---

## 📝 总结

### 问题根源
**Redis SCAN的非确定性** → 每次读取的维度队列顺序不同 → 去重后的request_id集合不同 → 每个泳道的20个tile窗口浮动 → 统计数字跳变 → 看起来像"滚动"。

### 为什么commit 9095d788没有完全修复？
该commit只修复了**grouped[key]内部的顺序**，但没有修复**SCAN顺序的随机性**，所以窗口仍然不稳定。

### 解决方案
**最简单**: 在discoverDimensionQueues()的返回前加一行 `sort.Strings(allKeys)`  
**最彻底**: 用SET维护已知维度值，避免SCAN

### 预期效果
修复后，泳道的stats.total只会在真实有新请求时变化，不会无故"滚动"。

---

**分析完成时间**: 2026-07-20  
**分析工程师**: AI Agent (Kiro)  
**部署版本**: 82b5edcf (包含部分修复9095d788，但仍存在SCAN顺序问题)
