# 泳道跳变真正根因分析

**部署版本**: `82b5edcf22e9a141ed88731f30fa7ebfccf9a6f6`  
**问题场景**: 没有刷新页面，正常观察时突然大幅跳变  
**时间特征**: 两张截图间隔15秒，数据从20:46跳到21:08显示的状态

---

## 🎯 真正的根本原因

### 问题：每5分钟的idle marker触发完整的delta重算

**代码路径**:
```
定时器触发 (每5分钟)
  ↓
idleTicker.C → maybeEmitIdleMarker()
  ↓
computeScopeDelta() → 从Redis读取完整快照
  ↓
SnapshotFromDimensionQueues() → 扫描所有维度队列
  ↓
与cached snapshot对比生成delta
  ↓
推送巨大的delta到前端 → 泳道跳变
```

**关键代码**: `admin/live_stream_sse.go:941-943`
```go
ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
deltaByCacheKey[sk] = h.computeScopeDelta(ctx, cs.tenantID, cs.isSuper)
cancel()
```

---

## 📊 数据流分解

### 正常情况下的更新流程

```
新请求到达
  ↓
写入Redis维度队列 (ZADD)
  ↓
发布Redis通知 (PUBLISH llmgw:live:notify)
  ↓
SSE订阅者收到通知
  ↓
从Redis加载单个请求详情
  ↓
调用 computeScopeDelta() 生成小delta
  ↓
推送到前端 (单个请求的变化)
```

### Idle Marker触发时的流程（问题所在）

```
定时器触发 (每5分钟)
  ↓
maybeEmitIdleMarker()
  ↓
为每个scope调用 computeScopeDelta()
  ↓
SnapshotFromDimensionQueues():
  - SCAN Redis查找所有维度队列键
  - 每个维度队列读取最后20条记录
  - 去重并合并 (可能几百个请求)
  - ASC排序
  ↓
与cached snapshot对比
  ↓
如果cached落后，生成包含**所有遗漏请求**的delta
  ↓
推送到前端 → 一次性合并大量数据 → 视觉跳变
```

---

## 🔬 为什么cached snapshot会落后？

### 原因1: 正常request delta的更新不完整

```go
// admin/live_stream_sse.go:408-415
tenantDelta := h.computeScopeDelta(ctx, tenantID, false)
```

每个新请求触发的 `computeScopeDelta()` 只包含：
- 该请求所属的几个泳道的更新
- **不是全量扫描所有维度队列**

而 `maybeEmitIdleMarker()` 中的 `computeScopeDelta()` 会：
- **全量扫描所有维度队列**
- 读取每个队列的最后20条
- 如果前5分钟有100个请求分布在50个泳道，cached只更新了其中活跃的20个泳道
- idle marker扫描时会发现另外30个泳道的数据没有同步

### 原因2: Redis队列与cached snapshot的不一致

观察代码：
```go
// admin/live_stream_redis_store_snapshot_fix.go:49
requestIDs, err := s.rdb.ZRevRange(ctx, key, 0, int64(LiveStreamLaneVisibleLimit-1)).Result()
```

这里从Redis读取的是**每个维度队列的最新20条**。

但是：
```go
// admin/live_stream_sse.go:509-513
delta := ComputeDelta(cached, snapshot)
h.cachedSnapshot[scope.cacheKey] = &cachedSnapshotEntry{
    snapshot:     snapshot,
    lastAccessed: time.Now(),
}
```

每次request的delta更新后，cached会被**部分更新**（只更新变化的泳道）。

但Redis队列是**完整的状态**（所有泳道都有完整的20条记录）。

---

## 🐛 问题的时间维度

### 第1张截图 (20:46)
- 前端显示: MiniMax (7), NVIDIA NIM (37), 普联 (4)
- cached snapshot: 反映了过去5分钟内的部分更新
- 最后一次idle marker: 可能在20:41或更早

### 15秒后 (用户观察期间)
- **21:05左右**: idle marker定时器触发（每5分钟一次）
- 触发 `maybeEmitIdleMarker()` → 全量扫描Redis
- 发现Redis中有大量请求未反映在cached snapshot中
- 生成巨大的delta
- 推送到前端

### 第2张截图 (21:08)
- 前端显示: MiniMax (44), NVIDIA NIM (85), 普联 (28)
- 这是**idle marker推送后的状态**
- 数据来自Redis的完整状态，而非增量积累

---

## 🔍 核心矛盾

### 设计意图
`maybeEmitIdleMarker()` 原本的目的是：
- 为长时间无请求的泳道添加"空闲"标记
- 保持前端界面的活跃感

### 实际效果
由于它会调用 `computeScopeDelta()` 进行全量扫描：
- 成为了一个**隐式的全量同步机制**
- 每5分钟强制将前端状态对齐到Redis
- 如果cached与Redis有显著差异 → 产生大delta → 视觉跳变

---

## 💡 验证方法

### 验证1: 检查idle marker日志

```bash
# SSH到245服务器
ssh -p 25022 root@115.29.212.252

# 查看最近的idle marker日志
docker logs -f llm-gateway-container 2>&1 | grep "idle marker"
```

**期望结果**: 每5分钟左右出现一次 "idle marker injected" 日志

### 验证2: 前端监控idle_marker消息

```javascript
// 浏览器Console执行
const es = new EventSource('/api/admin/live-stream?token=...');
let lastIdleMarkerTime = null;

es.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    if (msg.type === 'idle_marker') {
        const now = new Date();
        console.log('🔔 收到idle_marker', {
            时间: now.toLocaleTimeString(),
            距上次: lastIdleMarkerTime 
                ? ((now - lastIdleMarkerTime) / 1000 / 60).toFixed(1) + '分钟'
                : '首次',
            delta中的泳道数: Object.values(msg.delta?.changed_lanes || {})
                .flat().length,
            泳道详情: msg.delta?.changed_lanes
        });
        lastIdleMarkerTime = now;
    }
};
```

**期望结果**: 
- 每5分钟收到一次 `idle_marker` 消息
- `delta.changed_lanes` 包含大量泳道（几十个）
- 如果泳道数 > 10，说明触发了大规模同步

### 验证3: 对比idle marker前后的数据

**操作步骤**:
1. 打开仪表盘，记录当前各泳道数量
2. 等待约5分钟（不刷新页面）
3. 观察是否突然跳变
4. 跳变时检查浏览器Network标签，查看是否有 `idle_marker` 消息

**期望结果**: 跳变时刻与 `idle_marker` 消息时刻一致

---

## 🛠️ 解决方案

### 方案A: 分离idle marker和数据同步（推荐）

**问题根源**: `maybeEmitIdleMarker()` 承担了两个职责：
1. 发送空闲标记
2. 顺带做了全量数据同步

**解决方案**: 让idle marker只做它本职工作

```go
// admin/live_stream_sse.go:909-944
func (h *LiveStreamSSEHub) maybeEmitIdleMarker() {
    now := time.Now().UTC()

    // 1) Write idle markers to Redis (保持不变)
    if h.store != nil {
        ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
        if err := h.store.ScanAndRecordIdleMarkers(ctx, now, 0); err != nil {
            slog.Warn("live stream idle marker scan failed", "err", err.Error())
        }
        cancel()
    }

    // 2) 收集lane IDs (保持不变)
    h.cachedSnapshotMu.RLock()
    // ... 代码省略 ...
    h.cachedSnapshotMu.RUnlock()

    if len(laneIDs) == 0 {
        return
    }

    // 3) ⚠️ 关键修改：不要全量重算delta
    // 旧代码会调用 computeScopeDelta()，触发全量扫描
    // 新代码只发送 lane IDs，不包含delta
    
    base := LiveStreamEnvelope{
        Type:      "idle_marker",
        Timestamp: now,
        LaneIDs:   laneIDs,
        // Delta: nil,  // ← 不再计算delta
    }

    h.mu.RLock()
    clients := make([]*liveStreamClient, 0, len(h.clients))
    for c := range h.clients {
        clients = append(clients, c)
    }
    h.mu.RUnlock()

    // 4) 直接推送，不包含数据更新
    data, err := json.Marshal(base)
    if err != nil {
        slog.Warn("live stream idle marker marshal failed", "err", err.Error())
        return
    }

    for _, c := range clients {
        if !h.writeEvent(c, data) {
            h.evict(c)
        }
    }

    slog.Debug("live stream idle marker injected (no delta)",
        "lanes", len(laneIDs))
}
```

**前端修改**:
```typescript
// web/src/composables/liveStreamStore.ts:346-353
if (env.type === 'idle_marker') {
    // 旧注释说：delta已经在上面合并了，这里不再重复
    // 但实际上后端会发送delta，导致重复处理
    
    // 新逻辑：如果有delta，才合并（向后兼容）
    // 如果没有delta，只处理lane IDs（新行为）
    if (env.delta) {
        mergeDelta(env.delta)
    }
    
    // 处理 lane IDs，添加/更新空闲标记
    if (env.lane_ids && env.lane_ids.length > 0) {
        handleLaneIdleCheck(env.lane_ids, env.ts)
    }
    return
}
```

**效果**:
- ✅ idle marker恢复本职工作（只发送空闲标记）
- ✅ 不再每5分钟触发全量同步
- ✅ 消除定期跳变
- ⚠️ 需要前后端配合修改

---

### 方案B: 保留同步，但做差异检测

如果需要保留每5分钟的数据校正机制（防止长期偏差），可以加入智能判断：

```go
// admin/live_stream_sse.go:931-943
deltaByCacheKey := make(map[string]*LiveStreamDelta, len(all))
for _, cs := range all {
    sk := newLiveStreamScope(cs.tenantID, cs.isSuper).cacheKey
    if _, ok := deltaByCacheKey[sk]; ok {
        continue
    }
    if h.store == nil {
        deltaByCacheKey[sk] = nil
        continue
    }
    
    ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
    
    // ⚠️ 新增：只在cached明显落后时才计算delta
    cached := h.getCachedSnapshot(sk)
    fresh, err := h.store.SnapshotFromDimensionQueues(ctx, cs.tenantID, cs.isSuper)
    cancel()
    
    if err != nil || fresh == nil {
        deltaByCacheKey[sk] = nil
        continue
    }
    
    // 差异检测：只有总数差异 > 10% 才推送
    if cached != nil {
        oldTotal := cached.Summary.Total
        newTotal := fresh.Summary.Total
        diff := absInt(newTotal - oldTotal)
        threshold := oldTotal / 10 // 10%
        
        if diff <= threshold {
            // 差异很小，跳过推送
            slog.Debug("idle marker: skipping delta (small change)",
                "old_total", oldTotal, "new_total", newTotal, "diff", diff)
            deltaByCacheKey[sk] = nil
            continue
        }
        
        slog.Info("idle marker: significant data drift detected",
            "old_total", oldTotal, "new_total", newTotal, 
            "diff", diff, "threshold", threshold)
    }
    
    // 差异显著，计算并推送delta
    delta := ComputeDelta(cached, fresh)
    h.updateCachedSnapshot(sk, fresh)
    deltaByCacheKey[sk] = delta
}
```

**效果**:
- ✅ 保留数据校正机制
- ✅ 只在真正需要时同步（差异 > 10%）
- ✅ 大部分时候不会触发跳变
- ⚠️ 在数据持续丢失的情况下，仍会5分钟跳变一次

---

### 方案C: 增加增量request delta的可靠性（治本）

**问题根源**: 为什么cached会落后？因为request delta有丢失。

**解决思路**: 让每次request的delta更可靠

```go
// admin/live_stream_sse.go:1249-1254
func (h *LiveStreamSSEHub) enqueueBroadcast(req LiveRequest) {
    select {
    case h.broadcast <- req:
    default:
        // ⚠️ 问题：队列满时直接丢弃
        slog.Debug("live stream broadcast queue full, dropping request", "request_id", req.RequestID)
        
        // ✅ 新方案：标记为"需要同步"
        atomic.AddInt64(&h.droppedRequestCount, 1)
    }
}
```

新增周期性检查：
```go
// 在 Run() 中添加一个新的ticker
droppedCheckTicker := time.NewTicker(30 * time.Second)
defer droppedCheckTicker.Stop()

// ...

case <-droppedCheckTicker.C:
    dropped := atomic.SwapInt64(&h.droppedRequestCount, 0)
    if dropped > 0 {
        slog.Warn("live stream detected dropped requests, triggering sync",
            "dropped_count", dropped)
        // 触发一次温和的同步（只同步有问题的scope）
        h.syncDroppedRequests()
    }
```

**效果**:
- ✅ 在检测到丢失时立即补救
- ✅ 不等5分钟才发现问题
- ✅ 减少用户感知的跳变
- ⚠️ 增加系统复杂度

---

## 📋 推荐实施路径

### 短期修复（1天）- 方案A
1. 修改 `maybeEmitIdleMarker()`，移除delta计算
2. 修改前端 `liveStreamStore.ts`，处理无delta的idle_marker
3. 部署观察

**预期效果**: 消除每5分钟的定期跳变

### 中期优化（3天）- 方案A + 方案C
1. 实施方案A
2. 添加dropped request监控
3. 在检测到丢失时触发targeted sync（只同步有问题的数据）

**预期效果**: 数据始终准确，无跳变

### 长期优化（1周）- 完整重构
1. 引入snapshot版本号机制
2. 前端只接受更新的snapshot
3. 后端推送时附带版本信息
4. 解决race condition的根本问题

---

## 🔎 为什么之前没发现这个问题？

### 推测的演进历史

**阶段1**: 最初的实现
- `pushFullSnapshots()` 每30分钟推送全量
- `maybeEmitIdleMarker()` 也推送delta
- 两者都会导致跳变，但30分钟的更明显

**阶段2**: 2026-07-19 禁用 `pushFullSnapshots()`
- 目的：消除30分钟的跳变
- 副作用：5分钟的idle marker跳变变得更明显了
- 之前被30分钟的大跳变掩盖，现在暴露出来

**阶段3**: 当前（问题凸显）
- `pushFullSnapshots()` 被禁用
- 只剩 `maybeEmitIdleMarker()` 在做同步
- 成为唯一的跳变源，用户察觉到了

---

## 📝 总结

### 真正的根本原因
**每5分钟的idle marker机制，在发送空闲标记时，顺带做了全量数据扫描和同步**。由于日常的request delta存在丢失（队列满、网络问题），cached snapshot逐渐与Redis偏离。当idle marker触发时，一次性推送所有遗漏的数据，导致前端在15秒内（用户观察期间）接收到大量更新，产生视觉跳变。

### 表象
用户看到的"15秒跳变"，实际是：
- idle marker每5分钟触发一次
- 恰好在用户观察期间触发（21:05左右）
- 前端接收到包含几十个泳道、上百个请求的delta
- 通过 `mergeDelta()` 一次性合并
- 泳道数量瞬间从 (7, 37, 4) 跳到 (44, 85, 28)

### 不是刷新
- 用户没有按F5
- 没有触发 `initial_data`
- 是后端主动推送的 `idle_marker` 消息（带有巨大的delta）

### 解决方案优先级
1. ⭐⭐⭐ **方案A** - 分离idle marker和数据同步（最直接）
2. ⭐⭐ **方案B** - 保留同步但加差异检测（折中）
3. ⭐ **方案C** - 提高request delta可靠性（治本但复杂）

---

**分析完成时间**: 2026-07-20  
**分析工程师**: AI Agent (Kiro)  
**部署版本**: 82b5edcf22e9a141ed88731f30fa7ebfccf9a6f6
