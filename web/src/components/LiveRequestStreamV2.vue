<script setup lang="ts">
// LiveRequestStreamV2.vue — 实时请求流V2（泳道系统）
// 2026-07-05: 支持按原厂/供应商/模型分组的多泳道可视化
// 2026-07-05 v2: 添加管理员连接详情弹窗、空闲块机制
// 2026-07-07: 管理员可编辑远端SSE地址
// 2026-07-13: 转发泳道诊断事件，承载 RouteIncidentDrawer

import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useLiveStream, type LiveStatus, type LiveModelCategory } from '../composables/useLiveStream'
import { useSwimLane } from '../composables/useSwimLane'
import { isSuperAdmin, authBearer, getCurrentTenantId } from '../store'
import { redisHealthyRef, redisErrorRef } from '../composables/liveStreamStore'
import { fetchProviderLatency } from '../api/provider-probe'
import SwimLane from './SwimLane.vue'
import LiveStreamLegend from './LiveStreamLegend.vue'
import EmergencyDiagnosticModal from './EmergencyDiagnosticModal.vue'
import RouteIncidentDrawer from './RouteIncidentDrawer.vue'
import type { GroupByDimension } from '../types/swimlane'
import type { RouteIncident } from '../types/routeIncident'

const { t } = useI18n()

const emit = defineEmits<{
  openDetail: [requestId: string]
}>()

// 2026-07-13: 诊断工作台 state
const activeIncidentId = ref<string | null>(null)
const activeIncidentPreview = ref<RouteIncident | null>(null)

function handleDiagnose(incidentId: string, preview: RouteIncident) {
  activeIncidentId.value = incidentId
  activeIncidentPreview.value = preview
}

function closeDiagnose() {
  activeIncidentId.value = null
  activeIncidentPreview.value = null
}

function handleRequestFromDrawer(requestId: string) {
  closeDiagnose()
  emit('openDetail', requestId)
}

// 2026-07-24: 请求类型过滤。两项都选中表示显示全部请求。
const requestTypeFilter = ref<Set<'business' | 'probe'>>(new Set(['business', 'probe']))
const activeFilterMenu = ref<'status' | 'model' | 'provider' | 'vendor' | null>(null)
const filterGroupRef = ref<HTMLElement | null>(null)

// 2026-07-24: 多维过滤状态
const statusFilter = ref<Set<LiveStatus>>(new Set())
const modelFilter = ref<Set<string>>(new Set())
const providerFilter = ref<Set<string>>(new Set())
const vendorFilter = ref<Set<LiveModelCategory>>(new Set())

function toggleRequestType(type: 'business' | 'probe') {
  const next = new Set(requestTypeFilter.value)
  if (next.has(type)) {
    if (next.size > 1) next.delete(type)
  } else {
    next.add(type)
  }
  requestTypeFilter.value = next
}

function toggleFilterMenu(menu: 'status' | 'model' | 'provider' | 'vendor') {
  activeFilterMenu.value = activeFilterMenu.value === menu ? null : menu
}

function closeFilterMenu(event: MouseEvent) {
  const target = event.target as Node
  if (!filterGroupRef.value?.contains(target)) activeFilterMenu.value = null
}

onMounted(() => document.addEventListener('click', closeFilterMenu))
onUnmounted(() => document.removeEventListener('click', closeFilterMenu))

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
  mode: laneMode,
  setMode: setLaneMode,
  lanes,
  selectedLegends,
  legendItems,
  statusLegendItems,
  setGroupBy,
  toggleLegend,
  clearLegendSelection,
} = useSwimLane(liveSnapshot)

// 2026-07-23: 供应商 HTTP 延时（子项②）。仅在 provider 维度展示。
// providerLatencyMap: { [providerCode(字符串)]: latency_ms }
// key 用 provider_code（不是 provider_id），因为 lane.id = req.ProviderCode
const providerLatencyMap = ref<Record<string, number>>({})
let latencyTimer: ReturnType<typeof setInterval> | null = null

async function refreshProviderLatency() {
  // 仅 provider 维度拉取，减少不必要的请求
  if (groupBy.value !== 'provider') return
  try {
    const res = await fetchProviderLatency()
    const map: Record<string, number> = {}
    for (const e of res.entries || []) {
      if (e.latency_ms <= 0) continue
      // 优先用 provider_code（与 lane.id 完全一致），回退到 name 作为容错
      if (e.provider_code) map[e.provider_code] = e.latency_ms
      if (e.provider_name) map[e.provider_name] = e.latency_ms
    }
    providerLatencyMap.value = map
  } catch {
    // 接口可能在新探测模式未启用时不存在，静默失败
  }
}

// 维度切换到 provider 时立即拉取一次
watch(groupBy, (g) => {
  if (g === 'provider') void refreshProviderLatency()
})
const showConnectionDetail = ref(false)
const isAdmin = computed(() => isSuperAdmin())

// 应急诊断弹窗
const showEmergencyDiagnostic = ref(false)
const emergencyCredentialId = ref(0)
const emergencyModel = ref('')
const emergencyLaneName = ref('')

function handleEmergencyDiagnose(data: { credentialId: number; model: string; laneName: string }) {
  emergencyCredentialId.value = data.credentialId
  emergencyModel.value = data.model
  emergencyLaneName.value = data.laneName
  showEmergencyDiagnostic.value = true
}

function handleEmergencyClose() {
  showEmergencyDiagnostic.value = false
}

function handleEmergencyRecovered() {
  // 恢复成功后，可以选择刷新泳道或显示通知
  console.log('Credential recovered successfully')
}

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
  // 2026-07-23: 供应商延时轮询（5 分钟一次，与探测节奏对齐）
  void refreshProviderLatency()
  latencyTimer = setInterval(() => void refreshProviderLatency(), 5 * 60 * 1000)
})

onUnmounted(() => {
  if (latencyTimer) {
    clearInterval(latencyTimer)
    latencyTimer = null
  }
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
    window.alert(t('dashboard.liveStream.sseTestOk', { url: streamUrl.value }))
  } else {
    window.alert(t('dashboard.liveStream.sseTestFail', { status: connection.value, url: streamUrl.value }))
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
  if (connection.value === 'open') return t('dashboard.liveStream.statusOpen')
  if (connection.value === 'connecting') return t('dashboard.liveStream.statusConnecting')
  if (connection.value === 'reconnecting') return t('dashboard.liveStream.statusReconnecting')
  if (connection.value === 'unsupported') return t('dashboard.liveStream.statusUnsupported')
  return t('dashboard.liveStream.statusClosed')
})

const connectionClass = computed(() => {
  return connection.value === 'open' ? 'status--ok' : 'status--warn'
})

// 维度标签
const dimensionLabel = computed(() => {
  if (groupBy.value === 'vendor') return t('dashboard.liveStream.dimensionVendor')
  if (groupBy.value === 'provider') return t('dashboard.liveStream.dimensionProvider')
  return t('dashboard.liveStream.dimensionModel')
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

// 2026-07-13: 过滤泳道（"仅探测"过滤器）
// 2026-07-24: 扩展为多维过滤（请求类型、状态、模型、供应商、原厂）
const filteredLanes = computed(() => {
  return lanes.value
    .map(lane => ({
      ...lane,
      requests: lane.requests.filter(r => {
        // 请求类型过滤
        const requestType = r.is_probe === true ? 'probe' : 'business'
        if (!requestTypeFilter.value.has(requestType)) {
          return false
        }
        
        // 状态过滤
        if (statusFilter.value.size > 0 && (!r.status || !statusFilter.value.has(r.status as LiveStatus))) {
          return false
        }
        
        // 模型过滤
        if (modelFilter.value.size > 0 && (!r.model || !modelFilter.value.has(r.model))) {
          return false
        }
        
        // 供应商过滤（使用 provider 字段）
        if (providerFilter.value.size > 0 && (!r.provider || !providerFilter.value.has(r.provider))) {
          return false
        }
        
        // 原厂过滤（使用 vendor 字段）
        if (vendorFilter.value.size > 0 && (!r.vendor || !vendorFilter.value.has(r.vendor as LiveModelCategory))) {
          return false
        }
        
        return true
      }),
    }))
    .filter(lane => lane.requests.length > 0)
})

// 2026-07-13: 冷启动检测 — 后端尚未推送任何泳道
// "仅探测" 过滤后为空不算冷启动（已有泳道只是被过滤）
// 2026-07-13 修正：只有 filteredLanes 也为空时才显示空态（避免有泳道显示时仍显示"暂无请求数据"）
const isColdStart = computed(() => {
  return lanes.value.length === 0 && filteredLanes.value.length === 0
})

// 2026-07-24: 从当前所有请求中提取可选项（用于下拉框）
const availableStatuses = computed(() => {
  const statuses = new Set<LiveStatus>()
  for (const lane of lanes.value) {
    for (const req of lane.requests) {
      if (req.status) statuses.add(req.status as LiveStatus)
    }
  }
  return Array.from(statuses).sort()
})

const availableModels = computed(() => {
  const models = new Set<string>()
  for (const lane of lanes.value) {
    for (const req of lane.requests) {
      if (req.model) models.add(req.model)
    }
  }
  return Array.from(models).sort()
})

const availableProviders = computed(() => {
  const providers = new Set<string>()
  for (const lane of lanes.value) {
    for (const req of lane.requests) {
      if (req.provider) providers.add(req.provider)
    }
  }
  return Array.from(providers).sort()
})

const availableVendors = computed(() => {
  const vendors = new Set<LiveModelCategory>()
  for (const lane of lanes.value) {
    for (const req of lane.requests) {
      if (req.vendor) vendors.add(req.vendor as LiveModelCategory)
    }
  }
  return Array.from(vendors).sort()
})

// 2026-07-24: 切换过滤器的辅助函数
function toggleStatusFilter(status: LiveStatus) {
  const next = new Set(statusFilter.value)
  if (next.has(status)) {
    next.delete(status)
  } else {
    next.add(status)
  }
  statusFilter.value = next
}

function toggleModelFilter(model: string) {
  const next = new Set(modelFilter.value)
  if (next.has(model)) {
    next.delete(model)
  } else {
    next.add(model)
  }
  modelFilter.value = next
}

function toggleProviderFilter(provider: string) {
  const next = new Set(providerFilter.value)
  if (next.has(provider)) {
    next.delete(provider)
  } else {
    next.add(provider)
  }
  providerFilter.value = next
}

function toggleVendorFilter(vendor: LiveModelCategory) {
  const next = new Set(vendorFilter.value)
  if (next.has(vendor)) {
    next.delete(vendor)
  } else {
    next.add(vendor)
  }
  vendorFilter.value = next
}

function clearStatusFilter() {
  statusFilter.value = new Set()
}

function clearModelFilter() {
  modelFilter.value = new Set()
}

function clearProviderFilter() {
  providerFilter.value = new Set()
}

function clearVendorFilter() {
  vendorFilter.value = new Set()
}

function clearAllFilters() {
  requestTypeFilter.value = new Set(['business', 'probe'])
  statusFilter.value = new Set()
  modelFilter.value = new Set()
  providerFilter.value = new Set()
  vendorFilter.value = new Set()
}

// 计算激活的过滤器数量（用于显示徽章）
const activeFilterCount = computed(() => {
  let count = 0
  if (requestTypeFilter.value.size < 2) count++
  count += statusFilter.value.size
  count += modelFilter.value.size
  count += providerFilter.value.size
  count += vendorFilter.value.size
  return count
})
</script>

<template>
  <div class="live-stream-v2">
    <!-- 标题栏 -->
    <div class="stream-header">
      <h3 class="stream-title">{{ t('dashboard.liveStream.title') }}</h3>

      <div class="stream-controls">
        <!-- 分组切换 -->
        <div class="control-group control-group--dimension">
          <button
            type="button"
            class="control-btn"
            :class="{ 'control-btn--active': groupBy === 'vendor' }"
            @click="handleGroupByChange('vendor')"
          >
            {{ t('dashboard.liveStream.groupByVendor') }}
          </button>
          <button
            type="button"
            class="control-btn"
            :class="{ 'control-btn--active': groupBy === 'provider' }"
            @click="handleGroupByChange('provider')"
          >
            {{ t('dashboard.liveStream.groupByProvider') }}
          </button>
          <button
            type="button"
            class="control-btn"
            :class="{ 'control-btn--active': groupBy === 'model' }"
            @click="handleGroupByChange('model')"
          >
            {{ t('dashboard.liveStream.groupByModel') }}
          </button>
        </div>

        <!-- 2026-07-23: 大/小 模式切换（小=竖条默认，大=卡片） -->
        <div class="control-group control-group--mode">
          <button
            type="button"
            class="control-btn"
            :class="{ 'control-btn--active': laneMode === 'small' }"
            :title="t('dashboard.liveStream.modeSmallTitle')"
            @click="setLaneMode('small')"
          >
            {{ t('dashboard.liveStream.modeSmall') }}
          </button>
          <button
            type="button"
            class="control-btn"
            :class="{ 'control-btn--active': laneMode === 'large' }"
            :title="t('dashboard.liveStream.modeLargeTitle')"
            @click="setLaneMode('large')"
          >
            {{ t('dashboard.liveStream.modeLarge') }}
          </button>
        </div>

        <div class="control-group control-group--request-type request-type-filter">
          <button
            type="button"
            class="control-btn"
            :class="{ 'control-btn--active': requestTypeFilter.has('business') }"
            @click="toggleRequestType('business')"
            :title="t('dashboard.liveStream.businessTitle')"
          >
            {{ t('dashboard.liveStream.business') }}
          </button>
          <button
            type="button"
            class="control-btn control-btn--probe"
            :class="{ 'control-btn--active': requestTypeFilter.has('probe') }"
            @click="toggleRequestType('probe')"
            :title="t('dashboard.liveStream.probeTitle')"
          >
            {{ t('dashboard.liveStream.probe') }}
          </button>
        </div>

        <!-- 2026-07-24: 多维过滤器 -->
        <div ref="filterGroupRef" class="filter-group">
          <!-- 状态过滤 -->
          <div class="filter-dropdown">
            <button type="button" class="filter-btn" :class="{ 'filter-btn--active': statusFilter.size > 0 }" @click.stop="toggleFilterMenu('status')">
              {{ t('dashboard.liveStream.filterStatus') }}
              <span v-if="statusFilter.size > 0" class="filter-badge">{{ statusFilter.size }}</span>
            </button>
            <div v-if="activeFilterMenu === 'status'" class="filter-menu">
              <button type="button" class="filter-option filter-option--all" :class="{ 'filter-option--selected': statusFilter.size === 0 }" @click="clearStatusFilter">
                {{ t('dashboard.liveStream.filterAllOptions') }}
              </button>
              <label v-for="status in availableStatuses" :key="status" class="filter-option">
                <input
                  type="checkbox"
                  :checked="statusFilter.has(status)"
                  @change="toggleStatusFilter(status)"
                />
                <span>{{ t(`dashboard.liveStream.status.${status}`) }}</span>
              </label>
            </div>
          </div>

          <!-- 模型过滤 -->
          <div class="filter-dropdown">
            <button type="button" class="filter-btn" :class="{ 'filter-btn--active': modelFilter.size > 0 }" @click.stop="toggleFilterMenu('model')">
              {{ t('dashboard.liveStream.filterModel') }}
              <span v-if="modelFilter.size > 0" class="filter-badge">{{ modelFilter.size }}</span>
            </button>
            <div v-if="activeFilterMenu === 'model'" class="filter-menu">
              <button type="button" class="filter-option filter-option--all" :class="{ 'filter-option--selected': modelFilter.size === 0 }" @click="clearModelFilter">
                {{ t('dashboard.liveStream.filterAllOptions') }}
              </button>
              <label v-for="model in availableModels" :key="model" class="filter-option">
                <input
                  type="checkbox"
                  :checked="modelFilter.has(model)"
                  @change="toggleModelFilter(model)"
                />
                <span>{{ model }}</span>
              </label>
              <span v-if="availableModels.length === 0" class="filter-empty">{{ t('dashboard.liveStream.filterEmpty') }}</span>
            </div>
          </div>

          <!-- 供应商过滤 -->
          <div class="filter-dropdown">
            <button type="button" class="filter-btn" :class="{ 'filter-btn--active': providerFilter.size > 0 }" @click.stop="toggleFilterMenu('provider')">
              {{ t('dashboard.liveStream.filterProvider') }}
              <span v-if="providerFilter.size > 0" class="filter-badge">{{ providerFilter.size }}</span>
            </button>
            <div v-if="activeFilterMenu === 'provider'" class="filter-menu">
              <button type="button" class="filter-option filter-option--all" :class="{ 'filter-option--selected': providerFilter.size === 0 }" @click="clearProviderFilter">
                {{ t('dashboard.liveStream.filterAllOptions') }}
              </button>
              <label v-for="provider in availableProviders" :key="provider" class="filter-option">
                <input
                  type="checkbox"
                  :checked="providerFilter.has(provider)"
                  @change="toggleProviderFilter(provider)"
                />
                <span>{{ provider }}</span>
              </label>
              <span v-if="availableProviders.length === 0" class="filter-empty">{{ t('dashboard.liveStream.filterEmpty') }}</span>
            </div>
          </div>

          <!-- 原厂过滤 -->
          <div class="filter-dropdown">
            <button type="button" class="filter-btn" :class="{ 'filter-btn--active': vendorFilter.size > 0 }" @click.stop="toggleFilterMenu('vendor')">
              {{ t('dashboard.liveStream.filterVendor') }}
              <span v-if="vendorFilter.size > 0" class="filter-badge">{{ vendorFilter.size }}</span>
            </button>
            <div v-if="activeFilterMenu === 'vendor'" class="filter-menu">
              <button type="button" class="filter-option filter-option--all" :class="{ 'filter-option--selected': vendorFilter.size === 0 }" @click="clearVendorFilter">
                {{ t('dashboard.liveStream.filterAllOptions') }}
              </button>
              <label v-for="vendor in availableVendors" :key="vendor" class="filter-option">
                <input
                  type="checkbox"
                  :checked="vendorFilter.has(vendor)"
                  @change="toggleVendorFilter(vendor)"
                />
                <span>{{ t(`dashboard.liveStream.vendor.${vendor}`) }}</span>
              </label>
              <span v-if="availableVendors.length === 0" class="filter-empty">{{ t('dashboard.liveStream.filterEmpty') }}</span>
            </div>
          </div>

          <!-- 清除所有过滤器 -->
          <button
            v-if="activeFilterCount > 0"
            type="button"
            class="control-btn clear-filters-btn"
            @click="clearAllFilters"
            :title="t('dashboard.liveStream.clearFilters')"
          >
            {{ t('dashboard.liveStream.clearFilters') }}
          </button>
        </div>

        <div class="control-group">
          <button
            type="button"
            class="connection-status"
            :class="connectionClass"
            @click="toggleConnectionDetail"
            :title="t('dashboard.liveStream.connectionDetailTitle')"
          >
            <span class="status-dot" />
            {{ connectionLabel }}
          </button>
        </div>

        <div v-if="showConnectionDetail" class="connection-detail-popup">
          <div class="popup-header">
            <h4>{{ t('dashboard.liveStream.sseDetailTitle') }}</h4>
            <button type="button" class="popup-close" @click="showConnectionDetail = false">✕</button>
          </div>
          <div class="popup-body">
            <div class="detail-row">
              <span class="detail-label">{{ t('dashboard.liveStream.sseStatusLabel') }}:</span>
              <span class="detail-value" :class="connectionClass">{{ connectionLabel }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">{{ t('dashboard.liveStream.sseUrlLabel') }}:</span>
              <div class="detail-value url-edit-group">
                <template v-if="!isEditingUrl">
                  <code class="url-display">{{ streamUrl }}</code>
                  <button v-if="isAdmin" type="button" class="edit-btn" @click="startEditUrl" :title="t('dashboard.liveStream.editUrl')">{{ t('dashboard.liveStream.editUrl') }}</button>
                </template>
                <template v-else>
                  <input
                    v-model="editUrlValue"
                    class="url-input"
                    :placeholder="t('dashboard.liveStream.editUrlPlaceholder')"
                    @keyup.enter="saveUrl"
                    @keyup.escape="cancelEditUrl"
                  />
                  <div class="url-edit-actions">
                    <button type="button" class="save-btn" @click="saveUrl">{{ t('dashboard.liveStream.save') }}</button>
                    <button type="button" class="reset-btn" @click="resetUrl" :title="t('dashboard.liveStream.resetDefault')">{{ t('dashboard.liveStream.resetDefault') }}</button>
                    <button type="button" class="cancel-btn" @click="cancelEditUrl">{{ t('dashboard.liveStream.cancel') }}</button>
                  </div>
                </template>
              </div>
            </div>
            <div class="popup-actions">
              <button type="button" class="test-btn" @click="testConnection">{{ t('dashboard.liveStream.testConnection') }}</button>
              <button type="button" class="close-btn" @click="showConnectionDetail = false">{{ t('dashboard.liveStream.close') }}</button>
            </div>
          </div>
        </div>

        <!-- 暂停/恢复 -->
        <button
          type="button"
          class="control-btn"
          @click="togglePause"
        >
          {{ paused ? t('dashboard.liveStream.resume') : t('dashboard.liveStream.pause') }}
        </button>

        <div class="cache-stats">
          <span class="cache-stats__label">{{ t('dashboard.liveStream.cacheWindow') }}</span>
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
      <span>{{ t('dashboard.liveStream.redisWarning', { error: redisErrorRef || t('dashboard.liveStream.redisFallbackError') }) }}</span>
    </div>

    <!-- 泳道区域 -->
    <div class="swim-lanes">
      <SwimLane
        v-for="lane in filteredLanes"
        :key="lane.id"
        :lane="lane"
        :group-by="groupBy"
        :mode="laneMode"
        :selected-legends="selectedLegends"
        :avg-latency-ms="providerLatencyMap[lane.id]"
        @tile-click="handleTileClick"
        @emergency-diagnose="handleEmergencyDiagnose"
        @diagnose="(id: string, preview: RouteIncident) => handleDiagnose(id, preview)"
      />
      <!-- 2026-07-13: 冷启动提示，仅整局无任何泳道数据时显示
           "仅探测"过滤为空时不显示（已有泳道只是被过滤，按钮文字已说明） -->
      <div v-if="isColdStart" class="swim-lanes__empty">
        <span class="swim-lanes__empty-icon">⏳</span>
        <span class="swim-lanes__empty-text">{{ t('dashboard.liveStream.emptyWaiting') }}</span>
      </div>
    </div>

    <!-- 诊断工作台（2026-07-13，Phase 1 只读） -->
    <RouteIncidentDrawer
      :incident-id="activeIncidentId"
      :tenant-id="isSuperAdmin() ? undefined : getCurrentTenantId()"
      :preview="activeIncidentPreview"
      @close="closeDiagnose"
      @open-request="handleRequestFromDrawer"
    />

    <!-- 应急诊断弹窗 -->
    <EmergencyDiagnosticModal
      :visible="showEmergencyDiagnostic"
      :credential-id="emergencyCredentialId"
      :model="emergencyModel"
      :lane-name="emergencyLaneName"
      @close="handleEmergencyClose"
      @recovered="handleEmergencyRecovered"
    />
  </div>
</template>

<style scoped>
.live-stream-v2 {
  border: 1px solid var(--border);
  border-radius: var(--radius, 8px);
  background: var(--card);
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
  color: var(--text);
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
  padding: 4px 8px;
  border-radius: 6px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  /* 2026-07-24: 视觉分组 - 让相邻的功能簇有清晰的区隔 */
}

/* 2026-07-24: 不同功能簇使用不同色调做软区分 */
.control-group.control-group--dimension {
  background: color-mix(in srgb, var(--accent) 6%, var(--bg-subtle));
}

.control-group.control-group--mode {
  background: color-mix(in srgb, var(--success) 6%, var(--bg-subtle));
}

.control-group.control-group--request-type {
  background: color-mix(in srgb, #409eff 8%, var(--bg-subtle));
}

.control-btn {
  font-size: 12px;
  padding: 5px 12px;
  border-radius: 4px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text);
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
  font-weight: 500;
}

.control-btn:hover {
  background: var(--bg-subtle);
  border-color: var(--accent);
}

.control-btn--active {
  background: color-mix(in srgb, var(--accent) 15%, transparent);
  border-color: var(--accent);
  color: var(--accent);
}

/* 2026-07-13: 探测过滤器按钮专用样式 */
.control-btn--probe {
  border-color: rgba(64, 158, 255, 0.4);
}
.control-btn--probe.control-btn--active {
  background: rgba(64, 158, 255, 0.18);
  border-color: #1890ff;
  color: #1890ff;
}

/* 2026-07-24: 多维过滤器样式 */
.filter-group {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding: 4px 8px;
  border-radius: 6px;
  background: color-mix(in srgb, #722ed1 8%, var(--bg-subtle));
  border: 1px solid color-mix(in srgb, #722ed1 25%, var(--border));
}

.filter-dropdown {
  position: relative;
}

.filter-btn {
  font-size: 12px;
  padding: 5px 12px;
  border-radius: 4px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text);
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.filter-btn:hover {
  background: var(--bg-subtle);
  border-color: var(--accent);
}

.filter-btn--active {
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  border-color: var(--accent);
  color: var(--accent);
}

.filter-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 18px;
  height: 18px;
  padding: 0 5px;
  border-radius: 9px;
  background: var(--accent);
  color: var(--bg);
  font-size: 11px;
  font-weight: 600;
}

.filter-menu {
  position: absolute;
  top: 100%;
  left: 0;
  margin-top: 4px;
  min-width: 160px;
  max-height: 300px;
  overflow-y: auto;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 6px;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
  z-index: 100;
  padding: 4px;
}

.filter-option {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  cursor: pointer;
  border-radius: 4px;
  transition: background 0.15s ease;
  font-size: 13px;
  user-select: none;
}

.filter-option--all {
  width: 100%;
  border: 0;
  background: transparent;
  color: var(--text);
  text-align: left;
}

.filter-option--selected {
  color: var(--accent);
  font-weight: 600;
}

.filter-empty {
  display: block;
  padding: 8px 10px;
  color: var(--text-tertiary);
  font-size: 12px;
}

.filter-option:hover {
  background: var(--bg-subtle);
}

.filter-option input[type="checkbox"] {
  cursor: pointer;
}

.clear-filters-btn {
  font-size: 12px;
  padding: 5px 12px;
  border-radius: 4px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--warning);
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
}

.clear-filters-btn:hover {
  background: color-mix(in srgb, var(--warning) 12%, transparent);
  border-color: var(--warning);
}

.connection-status {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 12px;
  border-radius: 4px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text);
  font-size: 12px;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
}

.connection-status:hover {
  background: var(--bg-subtle);
  border-color: var(--accent);
}

.status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--muted);
  flex-shrink: 0;
}

.status--ok .status-dot {
  background: var(--success);
  box-shadow: 0 0 0 3px rgba(63, 185, 80, 0.18);
}

.status--warn .status-dot {
  background: var(--warning);
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
  background: var(--bg);
  border: 1px solid var(--border);
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
  background: var(--bg-subtle);
  border-bottom: 1px solid var(--border);
}

.popup-header h4 {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
}

.popup-close {
  background: none;
  border: none;
  color: var(--text-secondary);
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
  background: var(--bg);
  color: var(--text);
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
  color: var(--text-secondary);
  min-width: 90px;
}

.detail-value {
  font-size: 12px;
  color: var(--text);
  flex: 1;
}

.detail-value code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  background: var(--bg-subtle);
  padding: 4px 8px;
  border-radius: 4px;
  display: inline-block;
  border: 1px solid var(--border);
}

.url-edit-group {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
}

.url-display {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  background: var(--bg-subtle);
  padding: 4px 8px;
  border-radius: 4px;
  border: 1px solid var(--border);
  word-break: break-all;
  flex: 1;
  font-size: 12px;
}

.url-input {
  flex: 1;
  padding: 4px 8px;
  border-radius: 4px;
  border: 1px solid var(--accent);
  background: var(--bg);
  color: var(--text);
  font-size: 12px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  outline: none;
}

.url-input:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 20%, transparent);
}

.url-edit-actions {
  display: flex;
  gap: 4px;
}

.edit-btn {
  padding: 3px 8px;
  border-radius: 4px;
  border: 1px solid var(--border);
  background: var(--bg-subtle);
  color: var(--text-secondary);
  font-size: 11px;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
}

.edit-btn:hover {
  background: var(--bg);
  border-color: var(--accent);
  color: var(--text);
}

.save-btn,
.reset-btn,
.cancel-btn {
  padding: 3px 8px;
  border-radius: 4px;
  border: 1px solid var(--border);
  font-size: 11px;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
}

.save-btn {
  background: var(--accent);
  color: white;
  border-color: var(--accent);
}

.save-btn:hover {
  background: #5558e3;
}

.reset-btn {
  background: var(--bg-subtle);
  color: var(--text-secondary);
}

.reset-btn:hover {
  background: var(--bg);
  color: var(--text);
}

.cancel-btn {
  background: var(--bg-subtle);
  color: var(--text-secondary);
}

.cancel-btn:hover {
  background: var(--bg);
  color: var(--text);
}

.popup-actions {
  display: flex;
  gap: 8px;
  margin-top: 16px;
  padding-top: 16px;
  border-top: 1px solid var(--border);
}

.test-btn,
.close-btn {
  flex: 1;
  padding: 6px 12px;
  border-radius: 4px;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.15s ease;
  border: 1px solid var(--border);
}

.test-btn {
  background: var(--accent);
  color: white;
  border-color: var(--accent);
}

.test-btn:hover {
  background: #5558e3;
  border-color: #5558e3;
}

.close-btn {
  background: var(--bg-subtle);
  color: var(--text);
}

.close-btn:hover {
  background: var(--bg);
}

.cache-stats {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 12px;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg);
  font-size: 12px;
  white-space: nowrap;
}

.cache-stats__label {
  color: var(--text-secondary);
}

.cache-stats__value {
  color: var(--text);
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
  gap: 10px;
  margin-top: 12px;
  min-width: 0;
  width: 100%;
  max-width: 100%;
  box-sizing: border-box;
  padding: 4px 0 2px;
}

.swim-lanes__empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 48px 20px;
  text-align: center;
  color: var(--muted);
  font-size: 13px;
  border: 1px dashed color-mix(in srgb, var(--border) 80%, transparent);
  border-radius: 8px;
  background: linear-gradient(180deg, rgba(22, 27, 34, 0.6) 0%, rgba(15, 17, 23, 0.4) 100%);
}

.swim-lanes__empty-icon {
  font-size: 22px;
  opacity: 0.85;
  animation: empty-pulse 2.4s ease-in-out infinite;
}

.swim-lanes__empty-text {
  letter-spacing: 0.2px;
  color: var(--text-secondary);
}

@keyframes empty-pulse {
  0%, 100% { opacity: 0.85; transform: scale(1); }
  50% { opacity: 0.5; transform: scale(0.96); }
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
