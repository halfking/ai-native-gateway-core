// useSwimLane.ts — 泳道数据管理核心逻辑
// 2026-07-05: 实时请求流泳道系统

import { ref, computed, watch, onUnmounted } from 'vue'
import type { 
  GroupByDimension, 
  SwimLane, 
  RequestTile, 
  DimensionStats, 
  DimensionStat 
} from '../types/swimlane'
import { inferVendor, getStatusBorderKey, VENDOR_COLORS, STATUS_BORDER_COLORS } from '../types/swimlane'
import type { LiveRequest } from './liveStreamStore'

const MAX_REQUESTS_PER_LANE = 30
const TOP_N = 5

export function useSwimLane() {
  // 当前分组维度
  const groupBy = ref<GroupByDimension>('vendor')
  
  // 维度统计数据（累计）
  const dimensionStats = ref<DimensionStats>({
    vendor: [],
    provider: [],
    model: [],
  })
  
  // 泳道列表
  const lanes = ref<SwimLane[]>([])
  
  // 图例选择状态
  const selectedLegends = ref<Set<string>>(new Set())
  
  // 重绘标志
  const needsRedraw = ref(false)
  const rafId = ref<number | null>(null)
  
  // 消息队列（批量处理）
  const messageQueue = ref<LiveRequest[]>([])
  const flushTimer = ref<number | null>(null)
  
  // 获取当前维度的Top5
  function getTop5Keys(dimension: GroupByDimension): string[] {
    const stats = dimensionStats.value[dimension]
    return stats
      .sort((a, b) => b.requestCount - a.requestCount)
      .slice(0, TOP_N)
      .map(s => s.key)
  }
  
  // 构建泳道数组
  function buildLanes(dimension: GroupByDimension): SwimLane[] {
    const stats = dimensionStats.value[dimension]
    const top5Keys = getTop5Keys(dimension)
    
    const newLanes: SwimLane[] = []
    
    // 前5个泳道
    for (const key of top5Keys) {
      const stat = stats.find(s => s.key === key)
      if (!stat) continue
      
      newLanes.push({
        id: key,
        name: key,
        dimension,
        requests: [],
        stats: {
          total: stat.requestCount,
          success: stat.successCount,
          failure: stat.failureCount,
        },
        isOthers: false,
      })
    }
    
    // "其它"泳道
    const otherStats = stats.filter(s => !top5Keys.includes(s.key))
    if (otherStats.length > 0) {
      newLanes.push({
        id: '__others__',
        name: '其它',
        dimension,
        requests: [],
        stats: {
          total: otherStats.reduce((sum, s) => sum + s.requestCount, 0),
          success: otherStats.reduce((sum, s) => sum + s.successCount, 0),
          failure: otherStats.reduce((sum, s) => sum + s.failureCount, 0),
        },
        isOthers: true,
      })
    }
    
    return newLanes
  }
  
  // 更新维度统计
  function updateDimensionStats(tile: RequestTile) {
    // 更新vendor统计
    updateStat(dimensionStats.value.vendor, tile.vendor, tile.status)
    
    // 更新provider统计
    updateStat(dimensionStats.value.provider, tile.provider, tile.status)
    
    // 更新model统计
    updateStat(dimensionStats.value.model, tile.model, tile.status)
  }
  
  function updateStat(stats: DimensionStat[], key: string, status: string) {
    let stat = stats.find(s => s.key === key)
    if (!stat) {
      stat = {
        key,
        requestCount: 0,
        successCount: 0,
        failureCount: 0,
        lastSeen: new Date().toISOString(),
      }
      stats.push(stat)
    }
    
    stat.requestCount++
    stat.lastSeen = new Date().toISOString()
    
    if (status === 'success') {
      stat.successCount++
    } else if (status === 'failure') {
      stat.failureCount++
    }
  }
  
  // 将LiveRequest转换为RequestTile
  function liveRequestToTile(req: LiveRequest): RequestTile {
    const vendor = inferVendor(req.model || '')
    
    return {
      request_id: req.request_id || '',
      timestamp: req.ts,
      model: req.model || 'unknown',
      vendor,
      provider: req.provider_code || 'unknown',
      status: req.status || 'in_progress',
      error_kind: req.error_kind || undefined,
      latency_ms: req.latency_ms || undefined,
      cost_usd: req.cost_usd || undefined,
      prompt_tokens: req.prompt_tokens || undefined,
      completion_tokens: req.completion_tokens || undefined,
    }
  }
  
  // 添加请求到对应泳道
  function addRequestToLane(tile: RequestTile) {
    const dimension = groupBy.value
    const key = tile[dimension] as string
    
    // 找到对应泳道
    const top5Keys = getTop5Keys(dimension)
    let targetLane: SwimLane | undefined
    
    if (top5Keys.includes(key)) {
      targetLane = lanes.value.find(l => l.id === key)
    } else {
      targetLane = lanes.value.find(l => l.id === '__others__')
    }
    
    if (!targetLane) return
    
    // 检查是否已存在（更新状态）
    const existingIndex = targetLane.requests.findIndex(r => r.request_id === tile.request_id)
    if (existingIndex >= 0) {
      // 移除旧的，添加新的到末尾（表示状态更新）
      targetLane.requests.splice(existingIndex, 1)
    }
    
    // 添加到末尾
    targetLane.requests.push(tile)
    
    // 限制数量
    while (targetLane.requests.length > MAX_REQUESTS_PER_LANE) {
      targetLane.requests.shift()
    }
    
    // 更新泳道统计
    targetLane.stats.total++
    if (tile.status === 'success') {
      targetLane.stats.success++
    } else if (tile.status === 'failure') {
      targetLane.stats.failure++
    }
  }
  
  // 检查是否需要重排泳道（Top5变化）
  function checkNeedsReorder(): boolean {
    const currentTop5 = getTop5Keys(groupBy.value)
    const laneKeys = lanes.value
      .filter(l => !l.isOthers)
      .map(l => l.id)
    
    if (currentTop5.length !== laneKeys.length) return true
    
    for (let i = 0; i < currentTop5.length; i++) {
      if (currentTop5[i] !== laneKeys[i]) return true
    }
    
    return false
  }
  
  // 重建泳道（保留现有请求数据）
  function rebuildLanes() {
    const oldLanes = lanes.value
    const newLanes = buildLanes(groupBy.value)
    
    // 将旧泳道的请求数据迁移到新泳道
    for (const newLane of newLanes) {
      const oldLane = oldLanes.find(l => l.id === newLane.id)
      if (oldLane) {
        newLane.requests = oldLane.requests
        newLane.stats = oldLane.stats
      }
    }
    
    // 处理旧泳道中不再Top5的数据，迁移到"其它"
    const newTop5Ids = newLanes.filter(l => !l.isOthers).map(l => l.id)
    const othersLane = newLanes.find(l => l.isOthers)
    
    for (const oldLane of oldLanes) {
      if (!oldLane.isOthers && !newTop5Ids.includes(oldLane.id)) {
        // 这个泳道不再是Top5，迁移到"其它"
        if (othersLane) {
          othersLane.requests.push(...oldLane.requests)
          // 限制数量
          while (othersLane.requests.length > MAX_REQUESTS_PER_LANE) {
            othersLane.requests.shift()
          }
        }
      }
    }
    
    lanes.value = newLanes
    needsRedraw.value = false
  }
  
  // 调度重绘（RAF + 防抖）
  function scheduleRedraw() {
    if (!needsRedraw.value) return
    
    if (rafId.value) {
      cancelAnimationFrame(rafId.value)
    }
    
    rafId.value = requestAnimationFrame(() => {
      rebuildLanes()
      rafId.value = null
    })
  }
  
  // 处理单个新请求
  function processRequest(req: LiveRequest) {
    if (req.type === 'idle_marker') return
    if (!req.request_id) return
    
    const tile = liveRequestToTile(req)
    
    // 更新维度统计
    updateDimensionStats(tile)
    
    // 添加到泳道
    addRequestToLane(tile)
    
    // 检查是否需要重排
    if (checkNeedsReorder()) {
      needsRedraw.value = true
      scheduleRedraw()
    }
  }
  
  // 批量处理消息队列
  function flushMessageQueue() {
    if (messageQueue.value.length === 0) return
    
    const batch = messageQueue.value.splice(0)
    for (const req of batch) {
      processRequest(req)
    }
  }
  
  // 添加新请求到队列
  function queueRequest(req: LiveRequest) {
    messageQueue.value.push(req)
    
    if (!flushTimer.value) {
      flushTimer.value = window.setTimeout(() => {
        flushMessageQueue()
        flushTimer.value = null
      }, 100) // 100ms批量处理
    }
  }
  
  // 初始化泳道（从历史数据）
  function initializeLanes(initialRequests: LiveRequest[]) {
    // 清空现有数据
    dimensionStats.value = {
      vendor: [],
      provider: [],
      model: [],
    }
    
    // 处理所有历史请求
    for (const req of initialRequests) {
      if (req.type === 'idle_marker') continue
      if (!req.request_id) continue
      
      const tile = liveRequestToTile(req)
      updateDimensionStats(tile)
    }
    
    // 构建初始泳道
    lanes.value = buildLanes(groupBy.value)
    
    // 填充请求数据
    for (const req of initialRequests) {
      if (req.type === 'idle_marker') continue
      if (!req.request_id) continue
      
      const tile = liveRequestToTile(req)
      addRequestToLane(tile)
    }
  }
  
  // 切换分组维度
  function setGroupBy(dimension: GroupByDimension) {
    groupBy.value = dimension
    needsRedraw.value = true
    scheduleRedraw()
  }
  
  // 切换图例选择
  function toggleLegend(key: string) {
    if (selectedLegends.value.has(key)) {
      selectedLegends.value.delete(key)
    } else {
      selectedLegends.value.add(key)
    }
    // 强制响应式更新
    selectedLegends.value = new Set(selectedLegends.value)
  }
  
  // 清空图例选择
  function clearLegendSelection() {
    selectedLegends.value.clear()
    selectedLegends.value = new Set()
  }
  
  // 获取图例列表（Top5 + 其它）
  const legendItems = computed(() => {
    const dimension = groupBy.value
    const top5Keys = getTop5Keys(dimension)
    const stats = dimensionStats.value[dimension]
    
    const items = top5Keys.map(key => {
      const stat = stats.find(s => s.key === key)
      return {
        key,
        name: key,
        count: stat?.requestCount || 0,
        color: dimension === 'vendor' ? (VENDOR_COLORS[key] || VENDOR_COLORS['__unknown__']) : (VENDOR_COLORS['__unknown__']),
      }
    })
    
    // 添加"其它"
    const otherStats = stats.filter(s => !top5Keys.includes(s.key))
    if (otherStats.length > 0) {
      items.push({
        key: '__others__',
        name: '其它',
        count: otherStats.reduce((sum, s) => sum + s.requestCount, 0),
        color: VENDOR_COLORS['__others__'],
      })
    }
    
    return items
  })
  
  // 获取状态图例
  const statusLegendItems = computed(() => {
    return [
      { key: 'success', name: '成功', color: STATUS_BORDER_COLORS['success'] },
      { key: 'in_progress', name: '进行中', color: STATUS_BORDER_COLORS['in_progress'] },
      { key: 'failure_5xx', name: '5xx错误', color: STATUS_BORDER_COLORS['failure_5xx'] },
      { key: 'failure_4xx', name: '4xx错误', color: STATUS_BORDER_COLORS['failure_4xx'] },
      { key: 'failure_timeout', name: '超时', color: STATUS_BORDER_COLORS['failure_timeout'] },
      { key: 'failure_not_found', name: '未找到', color: STATUS_BORDER_COLORS['failure_not_found'] },
      { key: 'failure_other', name: '其它错误', color: STATUS_BORDER_COLORS['failure_other'] },
    ]
  })
  
  // 清理
  onUnmounted(() => {
    if (rafId.value) {
      cancelAnimationFrame(rafId.value)
    }
    if (flushTimer.value) {
      clearTimeout(flushTimer.value)
    }
  })
  
  return {
    // 状态
    groupBy,
    lanes,
    dimensionStats,
    selectedLegends,
    legendItems,
    statusLegendItems,
    
    // 方法
    initializeLanes,
    queueRequest,
    setGroupBy,
    toggleLegend,
    clearLegendSelection,
  }
}
