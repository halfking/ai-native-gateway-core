<script setup lang="ts">
// LiveRequestStreamV2.vue — 实时请求流V2（泳道系统）
// 2026-07-05: 支持按原厂/供应商/模型分组的多泳道可视化

import { ref, watch, onMounted, computed } from 'vue'
import { useLiveStream } from '../composables/useLiveStream'
import { useSwimLane } from '../composables/useSwimLane'
import SwimLane from './SwimLane.vue'
import LiveStreamLegend from './LiveStreamLegend.vue'
import type { GroupByDimension } from '../types/swimlane'

const emit = defineEmits<{
  openDetail: [requestId: string]
}>()

const { 
  requests: liveRequests, 
  connection, 
  paused, 
  togglePause 
} = useLiveStream()

const {
  groupBy,
  lanes,
  selectedLegends,
  legendItems,
  statusLegendItems,
  initializeLanes,
  queueRequest,
  setGroupBy,
  toggleLegend,
  clearLegendSelection,
} = useSwimLane()

// WebSocket地址显示（仅管理员）
const showWsAddress = ref(false)
const wsAddress = computed(() => {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const host = window.location.host
  return `${protocol}//${host}/api/admin/live-stream`
})

// 缓存/窗口统计
const bufferCount = computed(() => {
  return liveRequests.value.filter(r => r.type !== 'idle_marker').length
})

const windowCount = computed(() => {
  return lanes.value.reduce((sum, lane) => sum + lane.requests.length, 0)
})

// 连接状态标签
const connectionLabel = computed(() => {
  if (connection.value === 'open') return '已连接'
  if (connection.value === 'connecting') return '连接中'
  if (connection.value === 'reconnecting') return '重连中'
  if (connection.value === 'unsupported') return '不支持'
  return '未连接'
})

const connectionClass = computed(() => {
  return connection.value === 'open' ? 'status--ok' : 'status--warn'
})

// 维度标签
const dimensionLabel = computed(() => {
  if (groupBy.value === 'vendor') return '原厂'
  if (groupBy.value === 'provider') return '供应商'
  return '模型'
})

// 初始化
onMounted(() => {
  // 用现有的liveRequests初始化泳道
  initializeLanes(liveRequests.value)
})

// 监听新请求
watch(liveRequests, (newRequests, oldRequests) => {
  // 增量处理（找出新增的请求）
  const oldIds = new Set(oldRequests.map(r => r.request_id).filter(Boolean))
  const newItems = newRequests.filter(r => r.request_id && !oldIds.has(r.request_id))
  
  for (const req of newItems) {
    queueRequest(req)
  }
}, { deep: true })

function handleGroupByChange(dimension: GroupByDimension) {
  setGroupBy(dimension)
  clearLegendSelection()
}

function handleTileClick(requestId: string) {
  emit('openDetail', requestId)
}

function handleToggleLegend(key: string) {
  toggleLegend(key)
}
</script>

<template>
  <div class="live-stream-v2">
    <!-- 标题栏 -->
    <div class="stream-header">
      <h3 class="stream-title">实时请求流</h3>
      
      <div class="stream-controls">
        <!-- 分组切换 -->
        <div class="control-group">
          <button
            type="button"
            class="control-btn"
            :class="{ 'control-btn--active': groupBy === 'vendor' }"
            @click="handleGroupByChange('vendor')"
          >
            按原厂
          </button>
          <button
            type="button"
            class="control-btn"
            :class="{ 'control-btn--active': groupBy === 'provider' }"
            @click="handleGroupByChange('provider')"
          >
            按供应商
          </button>
          <button
            type="button"
            class="control-btn"
            :class="{ 'control-btn--active': groupBy === 'model' }"
            @click="handleGroupByChange('model')"
          >
            按模型
          </button>
        </div>
        
        <!-- 连接状态 -->
        <div class="control-group">
          <button
            type="button"
            class="connection-status"
            :class="connectionClass"
            @click="showWsAddress = !showWsAddress"
            :title="showWsAddress ? '隐藏地址' : '显示地址（仅管理员）'"
          >
            <span class="status-dot" />
            {{ connectionLabel }}
          </button>
          <div v-if="showWsAddress" class="ws-address">
            <code>{{ wsAddress }}</code>
          </div>
        </div>
        
        <!-- 暂停/恢复 -->
        <button
          type="button"
          class="control-btn"
          @click="togglePause"
        >
          {{ paused ? '恢复' : '暂停' }}
        </button>
        
        <!-- 缓存统计 -->
        <div class="cache-stats">
          <span class="cache-stats__label">缓存/窗口</span>
          <span class="cache-stats__value">{{ bufferCount }}/{{ windowCount }}</span>
        </div>
      </div>
    </div>
    
    <!-- 图例行 -->
    <LiveStreamLegend
      :dimension-items="legendItems"
      :status-items="statusLegendItems"
      :selected-legends="selectedLegends"
      :dimension-label="dimensionLabel"
      @toggle-legend="handleToggleLegend"
    />
    
    <!-- 泳道区域 -->
    <div class="swim-lanes">
      <SwimLane
        v-for="lane in lanes"
        :key="lane.id"
        :lane="lane"
        :group-by="groupBy"
        :selected-legends="selectedLegends"
        @tile-click="handleTileClick"
      />
      <div v-if="lanes.length === 0" class="swim-lanes__empty">
        暂无请求数据
      </div>
    </div>
  </div>
</template>

<style scoped>
.live-stream-v2 {
  border: 1px solid var(--border, #30363d);
  border-radius: var(--radius, 8px);
  background: var(--card, #1c2128);
  padding: 12px 16px;
  margin-bottom: 20px;
}

.stream-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}

.stream-title {
  font-size: 14px;
  font-weight: 600;
  margin: 0;
  color: var(--text, #e6edf3);
  flex-shrink: 0;
}

.stream-controls {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.control-group {
  display: flex;
  align-items: center;
  gap: 4px;
  position: relative;
}

.control-btn {
  font-size: 12px;
  padding: 5px 12px;
  border-radius: 4px;
  border: 1px solid var(--border, #30363d);
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
  font-weight: 500;
}

.control-btn:hover {
  background: var(--bg-subtle, #161b22);
  border-color: var(--accent, #6366f1);
}

.control-btn--active {
  background: rgba(99, 102, 241, 0.15);
  border-color: var(--accent, #6366f1);
  color: var(--accent, #6366f1);
}

.connection-status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 12px;
  border-radius: 4px;
  border: 1px solid var(--border, #30363d);
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  font-size: 12px;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
}

.connection-status:hover {
  background: var(--bg-subtle, #161b22);
}

.status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--muted, #8b949e);
  flex-shrink: 0;
}

.status--ok .status-dot {
  background: var(--success, #3fb950);
  box-shadow: 0 0 0 3px rgba(63, 185, 80, 0.18);
}

.status--warn .status-dot {
  background: var(--warning, #d29922);
  animation: pulse-dot 1.4s ease-in-out infinite;
}

@keyframes pulse-dot {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
}

.ws-address {
  position: absolute;
  top: 100%;
  left: 0;
  margin-top: 4px;
  padding: 6px 10px;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 4px;
  z-index: 100;
  white-space: nowrap;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
}

.ws-address code {
  font-size: 11px;
  color: var(--text, #e6edf3);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

.cache-stats {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 12px;
  border: 1px solid var(--border, #30363d);
  border-radius: 4px;
  background: var(--bg, #0f1117);
  font-size: 12px;
  white-space: nowrap;
}

.cache-stats__label {
  color: var(--text-secondary, #8b949e);
}

.cache-stats__value {
  color: var(--text, #e6edf3);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.swim-lanes {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 12px;
}

.swim-lanes__empty {
  padding: 40px 20px;
  text-align: center;
  color: var(--muted, #8b949e);
  font-size: 13px;
}

@media (max-width: 768px) {
  .stream-header {
    flex-direction: column;
    align-items: stretch;
  }
  
  .stream-controls {
    flex-direction: column;
    align-items: stretch;
  }
  
  .control-group {
    justify-content: stretch;
  }
  
  .control-btn {
    flex: 1;
  }
}
</style>
