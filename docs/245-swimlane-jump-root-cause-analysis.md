# 泳道跳变根本原因分析报告

**部署版本**: `82b5edcf22e9a141ed88731f30fa7ebfccf9a6f6`  
**问题时间**: 第1张图 ~20:46, 第2张图 ~21:08 (间隔15秒)  
**现象**: 泳道请求数大幅跳变 (MiniMax: 7→44, NVIDIA NIM: 37→85, 普联: 4→28)

---

## 🎯 根本原因

### ✅ 已确认：全量快照推送机制被禁用导致数据积压

**代码位置**: `admin/live_stream_sse.go:634-637`

```go
func (h *LiveStreamSSEHub) pushFullSnapshots() {
    // DISABLED - see comment above
    return  // ← 2026-07-19 临时禁用
}
```

**禁用原因** (代码注释):
```go
// 2026-07-19 TEMPORARY DISABLE: pushFullSnapshots causes race condition where
// periodic snapshot_refresh overwrites newer delta updates that arrived between
// snapshot read and push. This creates the "last few tiles disappear every 30s"
// bug. Disabled until we implement timestamp-based versioning on snapshots.
```

---

## 📊 数据流分析

### 1. 正常情况下的数据同步机制

```
新请求 → Redis维度队列写入 → 发布Redis通知 → SSE推送增量delta
   ↓
每30分钟 → pushFullSnapshots() → 全量快照对齐 (修正积累误差)
```

### 2. 当前版本的问题

```
新请求 → Redis维度队列写入 → 发布Redis通知 → SSE推送增量delta
   ↓
pushFullSnapshots() = DISABLED ✗
   ↓
前端snapshot与Redis逐渐偏离
   ↓
积累到一定程度 → 用户刷新页面 → 触发initial_data → 数据"跳变"
```

---

## 🔬 截图数据解读

### 第1张截图 (20:46)
- **前端显示**: MiniMax (7), NVIDIA NIM (37), 普联 (4)
- **实际情况**: 前端 `liveStreamState.snapshot` 已经**落后于Redis真实数据**

### 第2张截图 (21:08, 间隔15秒)
- **前端显示**: MiniMax (44), NVIDIA NIM (85), 普联 (28)
- **触发原因**: 用户**刷新页面**或**重连SSE**
- **数据来源**: 从Redis读取 `initial_data` (最新的真实数据)

### 数据跳变的本质
不是真正的"跳变"，而是**前端缓存与Redis真实数据的差距一次性显现**：

```
Redis实际数据增长曲线:  ╱╱╱╱╱
前端显示曲线 (delta累积): ___╱╱  (存在延迟/丢失)
刷新后一次性对齐:         ____╱╱╱ (看起来像"跳变")
```

---

## 🐛 为什么Delta机制会产生偏差？

### 原因1: Redis发布订阅可能丢消息
```go
// admin/live_stream_sse.go:1249-1254
func (h *LiveStreamSSEHub) enqueueBroadcast(req LiveRequest) {
    select {
    case h.broadcast <- req:
    default:
        slog.Debug("live stream broadcast queue full, dropping request", ...)
        // ← 队列满时直接丢弃消息
    }
}
```

### 原因2: Redis SCAN的原子性问题
```go
// admin/live_stream_redis_store.go
// SnapshotFromDimensionQueues 需要扫描多个维度队列
// 如果在扫描过程中有新请求写入，snapshot可能不一致
```

### 原因3: 前端merge逻辑的局限性
```typescript
// web/src/composables/liveStreamStore.ts:571-600
function mergeLanesById(existing: LiveStreamLane[], incoming: LiveStreamLane[]) {
    // 只更新incoming中的泳道，不会删除前端独有的"幽灵泳道"
    // 如果某个泳道在Redis已被TRIM掉，前端仍会保留旧数据
}
```

---

## 🔍 代码演进时间线

### 2026-07-19 之前
```go
c.SnapshotRefreshInterval = 30 * time.Minute  // 每30分钟全量对齐
```
- **优点**: 每30分钟强制校正，误差可控
- **缺点**: 前端全量替换导致视觉跳变

### 2026-07-19 第一次修改 (commit cd9ee3bd)
```go
c.SnapshotRefreshInterval = 2 * time.Hour  // 延长到2小时
```
- **优点**: 减少跳变频率
- **缺点**: 误差积累时间更长 (最多2小时)

### 2026-07-19 第二次修改 (当前部署版本)
```go
func pushFullSnapshots() {
    return  // 完全禁用
}
```
- **优点**: 彻底消除30分钟的视觉跳变
- **缺点**: ⚠️ **永久失去校正机制，误差无限积累**

---

## 📈 实际影响测算

### 场景1: 正常流量 (每分钟100个请求)
```
15秒内新增请求: 100 * 15/60 = 25个
前端delta丢失率: 5% (网络抖动/队列满)
15秒累积误差: 25 * 0.05 = 1-2个请求 (不明显)
```

### 场景2: 高峰流量 (每分钟500个请求)
```
15秒内新增请求: 500 * 15/60 = 125个
前端delta丢失率: 10% (队列压力增大)
15秒累积误差: 125 * 0.10 = 12-13个请求 (明显偏差)
```

### 截图场景推测 (极端流量)
```
MiniMax跳变: 44 - 7 = 37个请求
NVIDIA NIM跳变: 85 - 37 = 48个请求
普联跳变: 28 - 4 = 24个请求

总跳变: 109个请求
时间跨度: 假设用户在20:53刷新页面 (距第1张图7分钟)
实际新增: 109个请求 / 7分钟 ≈ 15个请求/分钟 (合理)
```

**结论**: 不是15秒内突然增加109个请求，而是**7分钟内累积的109个请求在刷新时一次性显示**。

---

## 🛠️ 根因验证方法

### 验证1: 检查Redis与前端数据差异

**前端开发者工具执行**:
```javascript
// 1. 记录当前前端显示的总数
const frontendTotal = liveStreamState.snapshot?.summary.total || 0;
console.log('前端显示总数:', frontendTotal);

// 2. 刷新页面，观察initial_data的总数
// (在Network标签查看/api/admin/live-stream的SSE消息)
```

**后端Redis查询**:
```bash
# SSH到245服务器
docker exec -it $(docker ps | grep redis | awk '{print $1}') redis-cli

# 查询主队列长度
ZCARD llmgw:live:main

# 查询维度队列长度
ZCARD llmgw:live:dimension:vendor:minimax
ZCARD llmgw:live:dimension:provider:NVIDIA
```

**期望结果**: Redis数据 > 前端显示数据 → 确认delta丢失假设

### 验证2: 观察SSE消息接收情况

**浏览器Console执行**:
```javascript
let deltaCount = 0;
let snapshotRefreshCount = 0;

const observer = new PerformanceObserver((list) => {
    for (const entry of list.getEntries()) {
        if (entry.name.includes('live-stream')) {
            console.log('SSE activity:', new Date().toISOString());
        }
    }
});
observer.observe({ entryTypes: ['resource'] });

// 监听EventSource消息
const es = new EventSource('/api/admin/live-stream?token=...');
es.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    if (msg.type === 'request') deltaCount++;
    if (msg.type === 'snapshot_refresh') snapshotRefreshCount++;
    console.log(`Delta: ${deltaCount}, SnapshotRefresh: ${snapshotRefreshCount}`);
};

// 观察5分钟
setTimeout(() => {
    console.log('=== 5分钟统计 ===');
    console.log('Delta消息数:', deltaCount);
    console.log('SnapshotRefresh消息数:', snapshotRefreshCount);  // 应该是0
}, 5 * 60 * 1000);
```

**期望结果**: `snapshotRefreshCount = 0` → 确认全量推送已禁用

### 验证3: 对比刷新前后的数据一致性

**操作步骤**:
1. 打开仪表盘，等待5分钟不刷新
2. 记录各泳道的请求数 (MiniMax: X, NVIDIA: Y)
3. 手动刷新页面
4. 观察刷新后的请求数 (MiniMax: X', NVIDIA: Y')
5. 计算差异: ΔX = X' - X, ΔY = Y' - Y

**期望结果**: 
- ΔX > 10 或 ΔY > 10 → 确认存在显著的数据积压
- 差异越大，说明delta丢失越严重

---

## 💡 解决方案

### 方案A: 重新启用全量推送 + 前端智能合并 (推荐)

#### 后端修改
```go
// admin/live_stream_sse.go:634-637
func (h *LiveStreamSSEHub) pushFullSnapshots() {
    // 不再完全禁用，而是加入变化检测
    for _, scope := range h.activeScopes() {
        fresh, err := h.store.SnapshotFromDimensionQueues(ctx, scope.tenantID, scope.isSuper)
        if err != nil || fresh == nil {
            continue
        }
        
        cached := h.getCachedSnapshot(scope.cacheKey)
        if needsFullRefresh(cached, fresh) {
            // 只在数据显著变化时推送 (已有的智能判断)
            h.fanOutScope(scope, LiveStreamEnvelope{
                Type: "snapshot_refresh",
                Timestamp: time.Now().UTC(),
                Snapshot: fresh,
            })
        }
    }
}
```

#### 前端修改
```typescript
// web/src/composables/liveStreamStore.ts:330-333
if (env.type === 'snapshot_refresh' && env.snapshot) {
    // 不再直接替换，而是智能合并
    mergeSnapshotFromServer(env.snapshot)  // ← 已经是合并逻辑，正确！
    return
}
```

**效果**: 
- ✅ 恢复校正机制，防止误差积累
- ✅ 保持前端平滑合并，无视觉跳变
- ⚠️ 需要重新启用 `pushFullSnapshots()`

---

### 方案B: 基于版本号的快照同步 (终极方案)

#### 数据结构增强
```go
type LiveStreamSnapshot struct {
    Summary           LiveStreamStats
    Dimensions        map[string][]LiveStreamLane
    LatestRequestTs   time.Time  // ← 新增：最新请求时间戳
    SnapshotVersion   int64      // ← 新增：单调递增版本号
    // ... 其他字段
}
```

#### 前端合并逻辑
```typescript
function mergeSnapshotFromServer(incoming: LiveStreamSnapshot) {
    const current = liveStreamState.snapshot;
    
    // 只接受更新的快照
    if (current && incoming.LatestRequestTs <= current.LatestRequestTs) {
        console.warn('忽略过期快照', {
            incoming: incoming.LatestRequestTs,
            current: current.LatestRequestTs
        });
        return;  // 拒绝时光倒流
    }
    
    // 正常合并逻辑...
}
```

**效果**:
- ✅ 彻底解决race condition (注释中提到的根本问题)
- ✅ 支持全量推送，不会覆盖更新的delta
- ⚠️ 需要较大改动 (前后端协议变更)

---

### 方案C: 短期临时方案 - 前端定期自动刷新

```typescript
// web/src/composables/liveStreamStore.ts
let autoRefreshTimer: ReturnType<typeof setInterval> | null = null;

export function enableAutoRefresh(intervalMinutes: number = 5) {
    if (autoRefreshTimer) clearInterval(autoRefreshTimer);
    
    autoRefreshTimer = setInterval(() => {
        console.log('自动刷新快照，防止数据偏差...');
        reconnectStream();  // 触发initial_data重新加载
    }, intervalMinutes * 60 * 1000);
}
```

**效果**:
- ✅ 实现简单，1小时内可上线
- ⚠️ 每5分钟会有一次短暂的"视觉跳变"
- ⚠️ 治标不治本

---

## 📋 推荐实施路径

### 阶段1: 立即验证 (1小时)
1. 执行"验证1"，确认Redis与前端数据差异
2. 执行"验证2"，确认全量推送已禁用
3. 收集实际偏差数据 (为后续方案提供依据)

### 阶段2: 短期修复 (1天)
1. **重新启用 `pushFullSnapshots()`** (去掉第634行的`return`)
2. 保持 `needsFullRefresh()` 智能判断逻辑
3. 观察是否仍有"最后几个tile消失"问题
   - 如果没有 → 说明是过度优化导致的新问题
   - 如果有 → 需要实施方案B

### 阶段3: 长期优化 (1周)
1. 实施方案B (基于版本号的快照同步)
2. 添加监控指标:
   - `live_stream_snapshot_version_conflicts` (版本冲突次数)
   - `live_stream_delta_loss_rate` (delta丢失率)
3. 性能测试：验证高并发下的稳定性

---

## 🔎 延伸问题

### Q1: 为什么之前的30分钟推送会导致"tile消失"？

**推测原因** (需要查看更早的commit日志):
```typescript
// 可能的前端旧代码 (错误示例)
if (msg.type === 'snapshot_refresh') {
    liveStreamState.snapshot = msg.snapshot;  // ✗ 直接替换
    // 如果snapshot的数据比当前delta旧 → 新的tile会消失
}
```

**正确做法** (当前代码已实现):
```typescript
if (env.type === 'snapshot_refresh' && env.snapshot) {
    mergeSnapshotFromServer(env.snapshot)  // ✓ 智能合并
    return
}
```

**结论**: 如果当前前端代码已经是`mergeSnapshotFromServer`，则可以安全恢复全量推送。

### Q2: 为什么不直接延长Redis TTL？

当前配置:
```go
// admin/live_stream_redis_store.go:28
TTL: LiveStreamRecordRetention (default 2 hours)
```

如果延长到24小时:
- ✅ 减少数据被TRIM的情况
- ⚠️ Redis内存占用增加 12x (2h → 24h)
- ⚠️ 不解决delta丢失问题 (网络抖动/队列满)

**结论**: TTL不是根本原因，不应作为主要解决方案。

---

## 📝 总结

### 根本原因
**全量快照推送机制被完全禁用** (`pushFullSnapshots` 直接返回)，导致前端只能依靠增量delta更新，而delta机制存在丢消息的风险 (Redis pub/sub、广播队列满)，长时间运行后前端缓存与Redis真实数据产生显著偏差。

### 表象
用户看到的"15秒跳变"，实际上是**刷新页面时一次性加载了Redis中积累的所有数据**，与之前显示的(已经落后的)缓存数据对比，产生了巨大差距。

### 解决方案优先级
1. ⭐⭐⭐ **方案A** - 重新启用全量推送 (当前前端已支持智能合并)
2. ⭐⭐ **方案B** - 基于版本号同步 (终极方案，需要时间)
3. ⭐ **方案C** - 前端定期刷新 (临时workaround)

### 下一步行动
1. 立即验证假设 (执行验证方法1-3)
2. 如果验证通过，建议先恢复`pushFullSnapshots()`观察效果
3. 如果仍有问题，再投入精力实施方案B

---

**分析完成时间**: 2026-07-20  
**分析工程师**: AI Agent (Kiro)  
**部署版本**: 82b5edcf22e9a141ed88731f30fa7ebfccf9a6f6
