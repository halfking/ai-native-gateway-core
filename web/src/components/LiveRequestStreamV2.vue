<script setup lang="ts">
// LiveRequestStreamV2.vue — 实时请求流V2（泳道系统）
// 2026-07-05: 支持按原厂/供应商/模型分组的多泳道可视化
// 2026-07-05 v2: 添加管理员连接详情弹窗、空闲块机制
// 2026-07-07: 管理员可编辑远端SSE地址

import { ref, computed, onMounted, watch } from 'vue'
import { useLiveStream } from '../composables/useLiveStream'
import { useSwimLane } from '../composables/useSwimLane'
import { isSuperAdmin, authBearer } from '../store'
import { redisHealthyRef, redisErrorRef } from '../composables/liveStreamStore'
import SwimLane from './SwimLane.vue'
import LiveStreamLegend from './LiveStreamLegend.vue'
import type { GroupByDimension } from '../types/swimlane'

const emit = defineEmits<{
  openDetail: [requestId: string]
}>()

// 解构出 reconnect —— 保存新 URL 后立即用新地址重连，不再只是 localStorage 默默记住
const { 
  snapshot: liveSnapshot,
  connection, 
  paused, 
  togglePause,
  reconnect: reconnectStream,
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

// SSE endpoint address - 可编辑
// 行为：
//  1) 默认值走 window.location.origin + ENDPOINT
//  2) localStorage 里允许管理员保存一个自定义 URL（比如反向代理 / 内网穿透）
//  3) 保存后立即关闭旧连接、打开新地址，刷新整个流
const STORAGE_KEY = 'llmgw_sse_endpoint'
const ENDPOINT_PATH = '/api/admin/live-stream'
const defaultStreamUrl = computed(() => `${window.location.origin}${ENDPOINT_PATH}`)
const streamUrl = ref('')
const isEditingUrl = ref(false)
const editUrlValue = ref('')

// 把 string → URL 转换成一个 EventSource 可用的最终地址
// 优先使用 withCredentials 发送 HttpOnly cookie，仅在 cookie 不可用时降级为 ?token=
function buildFinalUrl(url: string): string {
  const trimmed = url.trim()
  if (!trimmed) return defaultStreamUrl.value
  // 仅当 localStorage 的 api_key 明确标记为非 cookie 模式时才用 ?token= 降级
  let apiKeySuffix = ''
  try {
    const apiKey = localStorage.getItem('llmgw_api_key')
    if (apiKey && apiKey.startsWith('token:')) {
      apiKeySuffix = (trimmed.includes('?') ? '&' : '?') + 'token=' + encodeURIComponent(apiKey.slice(6))
    }
  } catch {
    /* SSR / storage disabled */
  }
  return trimmed + apiKeySuffix
}

onMounted(() => {
  const saved = (() => {
    try {
      return localStorage.getItem(STORAGE_KEY) || ''
    } catch {
      return ''
    }
  })()
  streamUrl.value = saved || defaultStreamUrl.value
})

// 如果用户修改了 window.location（多 tab 测试），默认地址也跟着变
watch(defaultStreamUrl, (cur) => {
  const saved = (() => {
    try {
      return localStorage.getItem(STORAGE_KEY) || ''
    } catch {
      return ''
    }
  })()
  if (!saved) streamUrl.value = cur
})

// 切换连接详情弹窗
function toggleConnectionDetail() {
  if (isAdmin.value) {
    showConnectionDetail.value = !showConnectionDetail.value
    if (!showConnectionDetail.value) {
      isEditingUrl.value = false
    }
  }
}

// 开始编辑URL
function startEditUrl() {
  editUrlValue.value = streamUrl.value
  isEditingUrl.value = true
}

// 保存URL —— 立刻用新地址重连 SSE
function saveUrl() {
  const url = editUrlValue.value.trim()
  if (url) {
    streamUrl.value = url
    try {
      localStorage.setItem(STORAGE_KEY, url)
    } catch {
      /* ignore */
    }
    reconnectStream()
  }
  isEditingUrl.value = false
}

// 重置为默认URL
function resetUrl() {
  streamUrl.value = defaultStreamUrl.value
  try {
    localStorage.removeItem(STORAGE_KEY)
  } catch {
    /* ignore */
  }
  isEditingUrl.value = false
  reconnectStream()
}

// 取消编辑
function cancelEditUrl() {
  isEditingUrl.value = false
}

// 测试 SSE 连接
function testConnection() {
  if (connection.value === 'open') {
    window.alert('SSE连接正常！\n状态: 已连接\n地址: ' + streamUrl.value)
  } else {
    window.alert('SSE未连接\n状态: ' + connection.value + '\n地址: ' + streamUrl.value)
  }
}

// 连接成功后立即把当前 URL 喂给 store（让 store 切换到新 ENDPOINT）
// 这里通过调用 reconnectStream 已经触发了 store 内部的 close + openConnection()
// 但 openConnection() 写死了 ENDPOINT；要想自定义 URL，需要 store 暴露 setter。
// 为最小改动，我们让 store 优先读 localStorage 里的自定义地址（见 liveStreamStore.ts）。
// 暴露给 store 的"当前目标 URL"：
const liveUrl = computed(() => buildFinalUrl(streamUrl.value))

// 缓存/窗口统计 — 驱动自服务端 snapshot
const bufferCount = computed(() => {
  return liveSnapshot.value?.summary?.total ?? 0
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
            title="点击查看连接详情"
          >
            <span class="status-dot" />
            {{ connectionLabel }}
          </button>
        </div>
        
        <!-- 连接详情弹窗（所有用户可查看） -->
        <div v-if="showConnectionDetail" class="connection-detail-popup">
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
              <div class="detail-value url-edit-group">
                <template v-if="!isEditingUrl">
                  <code class="url-display">{{ streamUrl }}</code>
                  <button v-if="isAdmin" type="button" class="edit-btn" @click="startEditUrl" title="编辑地址">编辑</button>
                </template>
                <template v-else>
                  <input 
                    v-model="editUrlValue" 
                    class="url-input" 
                    placeholder="输入SSE地址"
                    @keyup.enter="saveUrl"
                    @keyup.escape="cancelEditUrl"
                  />
                  <div class="url-edit-actions">
                    <button type="button" class="save-btn" @click="saveUrl">保存</button>
                    <button type="button" class="reset-btn" @click="resetUrl" title="恢复默认">默认</button>
                    <button type="button" class="cancel-btn" @click="cancelEditUrl">取消</button>
                  </div>
                </template>
              </div>
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
    
    <!-- Redis 健康警告 -->
    <div v-if="!redisHealthyRef" class="redis-health-warning">
      <span class="redis-warning-icon">⚠</span>
      <span>Redis 不可用：{{ redisErrorRef || '缓存服务连接失败' }}。实时数据降级为数据库查询，可能存在延迟。</span>
    </div>
    
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
  /* min-width:0 让组件在 flex/grid 父容器中能正确收缩 */
  min-width: 0;
  max-width: 100%;
  box-sizing: border-box;
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

.connection-status:hover {
  background: var(--bg-subtle, #161b22);
  border-color: var(--accent, #6366f1);
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

.url-edit-group {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
}

.url-display {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  background: var(--bg-subtle, #161b22);
  padding: 4px 8px;
  border-radius: 4px;
  border: 1px solid var(--border, #30363d);
  word-break: break-all;
  flex: 1;
  font-size: 12px;
}

.url-input {
  flex: 1;
  padding: 4px 8px;
  border-radius: 4px;
  border: 1px solid var(--accent, #6366f1);
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  font-size: 12px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  outline: none;
}

.url-input:focus {
  border-color: var(--accent, #6366f1);
  box-shadow: 0 0 0 2px rgba(99, 102, 241, 0.2);
}

.url-edit-actions {
  display: flex;
  gap: 4px;
}

.edit-btn {
  padding: 3px 8px;
  border-radius: 4px;
  border: 1px solid var(--border, #30363d);
  background: var(--bg-subtle, #161b22);
  color: var(--text-secondary, #8b949e);
  font-size: 11px;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
}

.edit-btn:hover {
  background: var(--bg, #0f1117);
  border-color: var(--accent, #6366f1);
  color: var(--text, #e6edf3);
}

.save-btn,
.reset-btn,
.cancel-btn {
  padding: 3px 8px;
  border-radius: 4px;
  border: 1px solid var(--border, #30363d);
  font-size: 11px;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
}

.save-btn {
  background: var(--accent, #6366f1);
  color: white;
  border-color: var(--accent, #6366f1);
}

.save-btn:hover {
  background: #5558e3;
}

.reset-btn {
  background: var(--bg-subtle, #161b22);
  color: var(--text-secondary, #8b949e);
}

.reset-btn:hover {
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
}

.cancel-btn {
  background: var(--bg-subtle, #161b22);
  color: var(--text-secondary, #8b949e);
}

.cancel-btn:hover {
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
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

/* Redis 健康警告条 */
.redis-health-warning {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  margin-top: 8px;
  background: rgba(245, 158, 11, 0.15);
  border: 1px solid rgba(245, 158, 11, 0.4);
  border-radius: 6px;
  color: #fbbf24;
  font-size: 12px;
  line-height: 1.4;
}

.redis-warning-icon {
  font-size: 16px;
  flex-shrink: 0;
}

.swim-lanes {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 12px;
  /* 关键：swim-lanes 是 swim-lane 的 flex 父，必须 min-width:0 否则
   *   flex item 拒绝收缩、宽屏溢出时仍会出现横向滚动。 */
  min-width: 0;
  width: 100%;
  max-width: 100%;
  box-sizing: border-box;
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
