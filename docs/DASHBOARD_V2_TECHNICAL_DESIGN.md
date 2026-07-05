# 仪表盘V2技术设计文档

## 1. 系统架构

### 1.1 整体架构图

```
┌─────────────────────────────────────────────────────────────┐
│                     DashboardView.vue                        │
│                   (版本切换入口)                              │
│  ┌──────────────┐              ┌──────────────┐            │
│  │ V1 旧版按钮  │              │ V2 新版按钮  │            │
│  └──────────────┘              └──────────────┘            │
└─────────────────────────────────────────────────────────────┘
              │                            │
              ├────────────┐              │
              ▼            ▼              ▼
    ┌─────────────┐  ┌─────────────┐  ┌─────────────────┐
    │TenantDashboard│  │DashboardView│  │DashboardViewV2│
    │    View       │  │   Legacy    │  │  (新版泳道)   │
    └─────────────┘  └─────────────┘  └─────────────────┘
                                                │
                    ┌───────────────────────────┼───────────────────────────┐
                    ▼                           ▼                           ▼
          ┌────────────────┐        ┌────────────────┐        ┌────────────────┐
          │  紧凑统计卡片  │        │LiveRequestStreamV2│      │  StatsDrawer   │
          │  (单行9指标)   │        │   (泳道容器)    │        │  (统计抽屉)    │
          └────────────────┘        └────────────────┘        └────────────────┘
                                             │
                    ┌────────────────────────┼────────────────────────┐
                    ▼                        ▼                        ▼
          ┌──────────────┐        ┌──────────────┐        ┌──────────────┐
          │LiveStreamLegend│      │  SwimLane×N  │        │useSwimLane   │
          │  (图例系统)   │        │  (多条泳道)  │        │ (逻辑层)     │
          └──────────────┘        └──────────────┘        └──────────────┘
                                           │
                                           ▼
                                  ┌──────────────┐
                                  │ RequestTile×M│
                                  │ (请求色块)   │
                                  └──────────────┘
```

### 1.2 数据流架构

```
后端 WebSocket (SSE)
    │
    ├─→ /api/admin/live-stream
    │
    ▼
┌────────────────────────────────┐
│   liveStreamStore.ts           │
│   (单例EventSource管理)        │
│   • refCount管理连接生命周期   │
│   • 最多缓存60条请求           │
│   • paused时消息入队列         │
└────────────────────────────────┘
    │
    ├─→ useLiveStream() composable
    │   (多个组件共享同一连接)
    │
    ▼
┌────────────────────────────────┐
│   DashboardViewV2.vue          │
│   • watch(liveRequests)        │
│   • applyIncrementalStats()    │
│   • 实时更新统计卡片           │
└────────────────────────────────┘
    │
    └─→ LiveRequestStreamV2.vue
        │
        ▼
    ┌────────────────────────────┐
    │   useSwimLane()            │
    │   • queueRequest()         │
    │   • messageQueue (批量)    │
    │   • flushTimer (100ms)     │
    └────────────────────────────┘
        │
        ▼
    ┌────────────────────────────┐
    │   processRequest()         │
    │   1. updateDimensionStats  │
    │   2. addRequestToLane      │
    │   3. checkNeedsReorder     │
    └────────────────────────────┘
        │
        ├─→ Top5不变 → 直接追加到泳道
        │
        └─→ Top5变化 → scheduleRedraw()
                │
                ▼
            ┌────────────────────┐
            │ RAF优化重绘        │
            │ • buildLanes()     │
            │ • 保留现有数据     │
            │ • Vue transition   │
            └────────────────────┘
```

## 2. 核心算法

### 2.1 Top5动态排名算法

```typescript
function getTop5Keys(dimension: GroupByDimension): string[] {
  const stats = dimensionStats.value[dimension]
  return stats
    .sort((a, b) => b.requestCount - a.requestCount)
    .slice(0, 5)
    .map(s => s.key)
}
```

**时间复杂度**: O(n log n)  
**空间复杂度**: O(n)  
**优化点**: 可用快速选择算法优化到O(n)

### 2.2 泳道分配算法

```typescript
function addRequestToLane(tile: RequestTile) {
  const dimension = groupBy.value
  const key = tile[dimension] as string
  const top5Keys = getTop5Keys(dimension)
  
  // 判断目标泳道
  let targetLane: SwimLane | undefined
  if (top5Keys.includes(key)) {
    targetLane = lanes.value.find(l => l.id === key)
  } else {
    targetLane = lanes.value.find(l => l.id === '__others__')
  }
  
  // 去重检查（更新状态）
  const existingIndex = targetLane.requests.findIndex(
    r => r.request_id === tile.request_id
  )
  if (existingIndex >= 0) {
    targetLane.requests.splice(existingIndex, 1)
  }
  
  // 追加到队尾
  targetLane.requests.push(tile)
  
  // 限流（FIFO）
  while (targetLane.requests.length > 30) {
    targetLane.requests.shift()
  }
}
```

**时间复杂度**: O(m)，m为单泳道请求数（≤30）  
**空间复杂度**: O(1)

### 2.3 智能重绘算法

```typescript
function checkNeedsReorder(): boolean {
  const currentTop5 = getTop5Keys(groupBy.value)
  const laneKeys = lanes.value
    .filter(l => !l.isOthers)
    .map(l => l.id)
  
  // 长度检查
  if (currentTop5.length !== laneKeys.length) return true
  
  // 顺序检查
  for (let i = 0; i < currentTop5.length; i++) {
    if (currentTop5[i] !== laneKeys[i]) return true
  }
  
  return false
}
```

**触发条件**:
1. Top5成员变化
2. Top5顺序变化

**防抖策略**: RAF确保每帧最多重绘一次

### 2.4 批量消息处理

```typescript
const messageQueue = ref<LiveRequest[]>([])
const flushTimer = ref<number | null>(null)

function queueRequest(req: LiveRequest) {
  messageQueue.value.push(req)
  
  if (!flushTimer.value) {
    flushTimer.value = window.setTimeout(() => {
      flushMessageQueue()
      flushTimer.value = null
    }, 100) // 100ms批量处理
  }
}
```

**优势**:
- 减少DOM操作频率
- 提高渲染性能
- 避免阻塞主线程

## 3. 性能优化策略

### 3.1 渲染优化

#### 3.1.1 虚拟滚动（未实现，可扩展）
当泳道请求数超过100时，考虑虚拟滚动：

```typescript
const visibleRange = computed(() => {
  const scrollLeft = scrollContainerRef.value?.scrollLeft || 0
  const containerWidth = scrollContainerRef.value?.clientWidth || 0
  const tileWidth = 80 + 6 // tile + gap
  
  const startIndex = Math.floor(scrollLeft / tileWidth)
  const visibleCount = Math.ceil(containerWidth / tileWidth) + 2
  
  return {
    start: Math.max(0, startIndex - 5), // 预加载5个
    end: Math.min(requests.length, startIndex + visibleCount + 5)
  }
})
```

#### 3.1.2 RAF节流
```typescript
let rafId: number | null = null

function scheduleRedraw() {
  if (rafId) cancelAnimationFrame(rafId)
  
  rafId = requestAnimationFrame(() => {
    rebuildLanes()
    rafId = null
  })
}
```

#### 3.1.3 CSS优化
- 使用`transform`和`opacity`触发GPU加速
- `will-change: transform`提前告知浏览器
- `contain: layout style paint`隔离布局影响

### 3.2 内存优化

#### 3.2.1 数据上限
- 每条泳道最多30个请求
- EventSource缓冲区最多60条
- 消息队列最多240条（60×4）

#### 3.2.2 及时清理
```typescript
onUnmounted(() => {
  if (rafId.value) cancelAnimationFrame(rafId.value)
  if (flushTimer.value) clearTimeout(flushTimer.value)
  // useLiveStream会自动release连接
})
```

### 3.3 网络优化

#### 3.3.1 EventSource连接复用
所有组件共享单个WebSocket连接，通过refCount管理：

```typescript
let refCount = 0
let es: EventSource | null = null

export function acquireLiveStream() {
  refCount++
  if (refCount === 1 && !es) {
    connect()
  }
  
  return () => {
    refCount--
    if (refCount === 0 && es) {
      disconnect()
    }
  }
}
```

#### 3.3.2 断线重连
```typescript
function reconnect() {
  if (es) {
    es.close()
    es = null
  }
  connectionRef.value = 'reconnecting'
  setTimeout(() => connect(), 3000)
}
```

## 4. 数据结构设计

### 4.1 核心类型

```typescript
// 分组维度
type GroupByDimension = 'vendor' | 'provider' | 'model'

// 请求色块
interface RequestTile {
  request_id: string
  timestamp: string
  model: string
  vendor: string
  provider: string
  status: string
  error_kind?: string
  latency_ms?: number
  cost_usd?: number
  prompt_tokens?: number
  completion_tokens?: number
}

// 维度统计
interface DimensionStat {
  key: string
  requestCount: number
  successCount: number
  failureCount: number
  lastSeen: string
}

// 泳道
interface SwimLane {
  id: string
  name: string
  dimension: GroupByDimension
  requests: RequestTile[]
  stats: {
    total: number
    success: number
    failure: number
  }
  isOthers: boolean
}
```

### 4.2 状态管理

```typescript
// useSwimLane内部状态
const groupBy = ref<GroupByDimension>('vendor')
const dimensionStats = ref<DimensionStats>({
  vendor: [],
  provider: [],
  model: []
})
const lanes = ref<SwimLane[]>([])
const selectedLegends = ref<Set<string>>(new Set())
```

**响应式依赖图**:
```
groupBy ─┬─→ legendItems (computed)
         ├─→ rebuildLanes (watch)
         └─→ addRequestToLane (function)

dimensionStats ─→ legendItems (computed)

lanes ──────────→ SwimLane (template)

selectedLegends ─→ isTileHighlighted (computed)
                 └─→ isTileDimmed (computed)
```

## 5. 组件通信

### 5.1 父子通信

```
DashboardViewV2
    │
    ├─→ LiveRequestStreamV2
    │       ├── props: 无
    │       └── emits: openDetail(requestId)
    │
    └─→ StatsDrawer
            ├── props: hotKeys, models, days, loading
            └── expose: open(tab), close()
```

### 5.2 兄弟通信

通过父组件中转：

```typescript
// DashboardViewV2.vue
const activeRequestId = ref<string | null>(null)

function openRequestDetail(id: string) {
  activeRequestId.value = id
}

<LiveRequestStreamV2 @open-detail="openRequestDetail" />
<RequestLogDrawer :request-id="activeRequestId" @close="..." />
```

### 5.3 跨组件状态共享

通过composable共享状态：

```typescript
// useLiveStream()是单例
const { requests } = useLiveStream()

// 多个组件可以同时访问
const streamV1 = useLiveStream() // DashboardViewLegacy
const streamV2 = useLiveStream() // LiveRequestStreamV2
// streamV1.requests === streamV2.requests (同一引用)
```

## 6. 测试策略

### 6.1 单元测试

```typescript
// useSwimLane.test.ts
describe('useSwimLane', () => {
  it('should build lanes correctly', () => {
    const { lanes, initializeLanes } = useSwimLane()
    initializeLanes(mockRequests)
    expect(lanes.value).toHaveLength(6) // Top5 + Others
  })
  
  it('should rebuild lanes when top5 changes', () => {
    const { lanes, queueRequest } = useSwimLane()
    // ... 模拟Top5变化
    expect(lanes.value[0].id).toBe('new-top-vendor')
  })
})
```

### 6.2 集成测试

```typescript
// LiveRequestStreamV2.test.ts
it('should update lanes when new request arrives', async () => {
  const wrapper = mount(LiveRequestStreamV2)
  
  // 模拟WebSocket推送
  await pushMockRequest({ model: 'gpt-4', vendor: 'openai' })
  
  await nextTick()
  
  expect(wrapper.findAll('.request-tile')).toHaveLength(1)
})
```

### 6.3 性能测试

```typescript
// performance.test.ts
it('should handle 1000 requests in 1 second', async () => {
  const start = performance.now()
  
  for (let i = 0; i < 1000; i++) {
    queueRequest(mockRequest())
  }
  
  await flushPromises()
  
  const duration = performance.now() - start
  expect(duration).toBeLessThan(1000)
})
```

## 7. 浏览器兼容性

### 7.1 目标浏览器
- Chrome/Edge 90+
- Firefox 88+
- Safari 14+

### 7.2 Polyfill需求
无需额外polyfill，Vue3和Vite已处理。

### 7.3 降级策略

```css
@media (prefers-reduced-motion: reduce) {
  .swim-tile-enter-active,
  .swim-tile-leave-active {
    transition: opacity 0.15s linear;
  }
  .swim-tile-enter-from,
  .swim-tile-leave-to {
    transform: none;
  }
}
```

## 8. 安全考虑

### 8.1 XSS防护
- Vue自动转义所有插值 `{{ }}`
- 不使用`v-html`
- API返回数据由后端验证

### 8.2 WebSocket安全
- Cookie HttpOnly认证
- Same-origin策略
- 后端验证JWT

### 8.3 敏感信息
- API Key只显示前缀
- 错误信息脱敏
- WebSocket地址仅管理员可见

## 9. 可维护性

### 9.1 代码组织

```
web/src/
├── types/
│   └── swimlane.ts          # 类型定义
├── composables/
│   ├── useLiveStream.ts     # WebSocket管理
│   ├── liveStreamStore.ts   # 单例store
│   └── useSwimLane.ts       # 泳道逻辑
├── components/
│   ├── RequestTile.vue      # 色块
│   ├── SwimLane.vue         # 泳道
│   ├── LiveStreamLegend.vue # 图例
│   ├── LiveRequestStreamV2.vue # 容器
│   └── StatsDrawer.vue      # 抽屉
└── views/
    ├── DashboardView.vue    # 入口
    ├── DashboardViewV2.vue  # 新版
    └── DashboardViewLegacy.vue # 旧版
```

### 9.2 注释规范

每个文件顶部包含：
```typescript
// FileName.vue — 简短描述
// 2026-07-05: 详细说明、设计决策、注意事项
```

关键算法添加注释：
```typescript
// 智能重绘：只在Top5排名变化时触发
function checkNeedsReorder(): boolean {
  // ...
}
```

### 9.3 Git提交规范

```
feat(dashboard): 实现泳道可视化系统

- 新增RequestTile组件（80x60色块）
- 实现useSwimLane逻辑（Top5算法+批量处理）
- 添加LiveRequestStreamV2容器组件
- 支持按原厂/供应商/模型三维度分组

Closes #123
```

## 10. 未来扩展方向

### 10.1 数据导出
```typescript
function exportSwimLaneData() {
  const csv = lanes.value.flatMap(lane => 
    lane.requests.map(req => ({
      lane: lane.name,
      time: req.timestamp,
      model: req.model,
      status: req.status,
      latency: req.latency_ms
    }))
  )
  downloadCSV(csv, 'swimlane-data.csv')
}
```

### 10.2 快捷键
```typescript
onMounted(() => {
  document.addEventListener('keydown', (e) => {
    if (e.key === '1') setGroupBy('vendor')
    if (e.key === '2') setGroupBy('provider')
    if (e.key === '3') setGroupBy('model')
    if (e.key === ' ') togglePause()
    if (e.key === 'Escape') clearLegendSelection()
  })
})
```

### 10.3 Web Worker
将统计计算移到Worker：

```typescript
// swimlane.worker.ts
self.onmessage = (e) => {
  const { requests, dimension } = e.data
  const stats = computeStats(requests, dimension)
  self.postMessage(stats)
}
```

### 10.4 IndexedDB持久化
```typescript
async function saveToIndexedDB() {
  const db = await openDB('dashboard', 1)
  await db.put('swimlane', {
    groupBy: groupBy.value,
    lanes: lanes.value,
    timestamp: Date.now()
  })
}
```

---

**文档版本**: 1.0  
**最后更新**: 2026-07-05  
**维护者**: 开发团队
