<script setup lang="ts">
// LiveRequestStreamV2.vue — 实时请求流V2（泳道系统）
// 2026-07-05: 支持按原厂/供应商/模型分组的多泳道可视化
// 2026-07-05 v2: 添加管理员连接详情弹窗、空闲块机制

import { ref, computed } from 'vue'
import { useLiveStream } from '../composables/useLiveStream'
import { useSwimLane } from '../composables/useSwimLane'
import { isSuperAdmin } from '../store'
import SwimLane from './SwimLane.vue'
import LiveStreamLegend from './LiveStreamLegend.vue'
import type { GroupByDimension } from '../types/swimlane'

const emit = defineEmits<{
  openDetail: [requestId: string]
}>()

const { 
  requests: liveRequests, 
  snapshot: liveSnapshot,
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
  setGroupBy,
  toggleLegend,
  clearLegendSelection,
} = useSwimLane(liveSnapshot)

// 管理员连接详情弹窗
const showConnectionDetail = ref(false)
const isAdmin = computed(() => isSuperAdmin())

// SSE endpoint address
const streamUrl = computed(() => {
  return `${window.location.origin}/api/admin/live-stream`
})

// 切换连接详情弹窗
function toggleConnectionDetail() {
  if (isAdmin.value) {
    showConnectionDetail.value = !showConnectionDetail.value
  }
}

// 测试 SSE 连接
function testConnection() {
  if (connection.value === 'open') {
    alert('SSE连接正常！\n状态: 已连接\n地址: ' + streamUrl.value)
  } else {
    alert('SSE未连接\n状态: ' + connection.value + '\n地址: ' + streamUrl.value)
  }
}

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
            @click="toggleConnectionDetail"
            :title="isAdmin ? '点击查看连接详情' : connectionLabel"
            :disabled="!isAdmin"
          >
            <span class="status-dot" />
            {{ connectionLabel }}
          </button>
        </div>
        
        <!-- 连接详情弹窗（仅管理员） -->
        <div v-if="showConnectionDetail && isAdmin" class="connection-detail-popup">
          <div class="popup-header">
            <h4>SSE 连接详情</h4>
            <button type="button" class="popup-close" @click="showConnectionDetail = false">✕</button>
          </div>
          <div class="popup-body">
            <div class="detail-row">
              <span class="detail-label">连接状态:</span>
              <span class="detail-value" :class="connectionClass">{{ connectionLabel }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">SSE地址:</span>
              <code class="detail-value">{{ streamUrl }}</code>
            </div>
            <div class="popup-actions">
              <button type="button" class="test-btn" @click="testConnection">测试连接</button>
              <button type="button" class="close-btn" @click="showConnectionDetail = false">关闭</button>
            </div>
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
  position: relative;
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

.connection-status:hover:not(:disabled) {
  background: var(--bg-subtle, #161b22);
  border-color: var(--accent, #6366f1);
}

.connection-status:disabled {
  cursor: default;
  opacity: 0.8;
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

/* 连接详情弹窗 */
.connection-detail-popup {
  position: absolute;
  top: calc(100% + 8px);
  left: 0;
  min-width: 400px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
  z-index: 1000;
  overflow: hidden;
}

.popup-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 16px;
  background: var(--bg-subtle, #161b22);
  border-bottom: 1px solid var(--border, #30363d);
}

.popup-header h4 {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--text, #e6edf3);
}

.popup-close {
  background: none;
  border: none;
  color: var(--text-secondary, #8b949e);
  font-size: 18px;
  cursor: pointer;
  padding: 0;
  width: 24px;
  height: 24px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 4px;
  transition: all 0.15s ease;
}

.popup-close:hover {
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
}

.popup-body {
  padding: 16px;
}

.detail-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}

.detail-row:last-child {
  margin-bottom: 0;
}

.detail-label {
  font-size: 12px;
  color: var(--text-secondary, #8b949e);
  min-width: 90px;
}

.detail-value {
  font-size: 12px;
  color: var(--text, #e6edf3);
  flex: 1;
}

.detail-value code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  background: var(--bg-subtle, #161b22);
  padding: 4px 8px;
  border-radius: 4px;
  display: inline-block;
  border: 1px solid var(--border, #30363d);
}

.popup-actions {
  display: flex;
  gap: 8px;
  margin-top: 16px;
  padding-top: 16px;
  border-top: 1px solid var(--border, #30363d);
}

.test-btn,
.close-btn {
  flex: 1;
  padding: 6px 12px;
  border-radius: 4px;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.15s ease;
  border: 1px solid var(--border, #30363d);
}

.test-btn {
  background: var(--accent, #6366f1);
  color: white;
  border-color: var(--accent, #6366f1);
}

.test-btn:hover {
  background: #5558e3;
  border-color: #5558e3;
}

.close-btn {
  background: var(--bg-subtle, #161b22);
  color: var(--text, #e6edf3);
}

.close-btn:hover {
  background: var(--bg, #0f1117);
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
