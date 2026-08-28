<script setup lang="ts">
// LiveRequestStreamV2.vue — 实时请求流V2（泳道系统）
// 2026-07-05: 支持按原厂/供应商/模型分组的多泳道可视化
// 2026-07-05 v2: 添加管理员连接详情弹窗、空闲块机制
// 2026-07-07: 管理员可编辑远端SSE地址
// 2026-07-13: 转发泳道诊断事件，承载 RouteIncidentDrawer
// 2026-08-14 V3.2: 添加"按处理队列"维度，显示队列透视 + 节点矩阵面板

import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useLiveStream, type LiveStatus, type LiveModelCategory } from '../composables/useLiveStream'
import { useSwimLane } from '../composables/useSwimLane'
import { useLiveStreamFilters } from '../composables/useLiveStreamFilters'
import { useLiveStreamUrl } from '../composables/useLiveStreamUrl'
import { useProviderLatency } from '../composables/useProviderLatency'
import { useEmergencyDiagnostic } from '../composables/useEmergencyDiagnostic'
import { useIncidentDiagnosis } from '../composables/useIncidentDiagnosis'
import { useConnectionDetail } from '../composables/useConnectionDetail'
import { isSuperAdmin, authBearer, getCurrentTenantId } from '../store'
import { redisHealthyRef, redisErrorRef } from '../composables/liveStreamStore'
import SwimLane from './SwimLane.vue'
import LiveStreamLegend from './LiveStreamLegend.vue'
import EmergencyDiagnosticModal from './EmergencyDiagnosticModal.vue'
import RouteIncidentDrawer from './RouteIncidentDrawer.vue'
import LiveStreamFilterDialog from './LiveStreamFilterDialog.vue'
import QueuePerspectivePanel from './QueuePerspectivePanel.vue'
import RequestJourneyQueues from './RequestJourneyQueues.vue'
import NodeStatusMatrix from './NodeStatusMatrix.vue'
import type { GroupByDimension } from '../types/swimlane'
import type { RouteIncident } from '../types/routeIncident'

const { t } = useI18n()

const emit = defineEmits<{
  openDetail: [requestId: string]
}>()

// 2026-08-06: 诊断工作台（RouteIncidentDrawer）状态抽到 useIncidentDiagnosis composable
const {
  activeIncidentId,
  activeIncidentPreview,
  handleDiagnose,
  closeDiagnose,
  handleRequestFromDrawer,
} = useIncidentDiagnosis({
  onOpenRequest: (requestId: string) => emit('openDetail', requestId),
})

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

// 2026-08-06: 过滤器状态管理抽到 useLiveStreamFilters composable
const {
  requestTypeFilter,
  statusFilter,
  modelFilter,
  providerFilter,
  vendorFilter,
  agentFilter,
  toggleRequestType,
  applyStatusFilter,
  applyModelFilter,
  applyProviderFilter,
  applyVendorFilter,
  applyAgentFilter,
  clearAllFilters,
  availableStatuses,
  availableModels,
  availableProviders,
  availableVendors,
  availableAgents,
  statusFilterSelected,
  modelFilterSelected,
  providerFilterSelected,
  vendorFilterSelected,
  agentFilterSelected,
  activeFilterCount,
  filteredLanes,
} = useLiveStreamFilters({ lanes })

// 2026-07-24: 筛选弹窗状态（保留在组件内，仅 UI 控制）
const filterDialog = ref<'status' | 'model' | 'provider' | 'vendor' | 'agent' | null>(null)

function openFilterDialog(kind: 'status' | 'model' | 'provider' | 'vendor' | 'agent') {
  filterDialog.value = kind
}

// 2026-08-06: 供应商 HTTP 延时轮询抽到 useProviderLatency composable
// （providerLatencyMap / 5 分钟轮询 / groupBy 联动，由 composable 管理生命周期）
const { providerLatencyMap } = useProviderLatency({ groupBy })
const isAdmin = computed(() => isSuperAdmin())

// 2026-08-06: 应急诊断弹窗状态抽到 useEmergencyDiagnostic composable
const {
  showEmergencyDiagnostic,
  emergencyCredentialId,
  emergencyModel,
  emergencyLaneName,
  handleEmergencyDiagnose,
  handleEmergencyClose,
  handleEmergencyRecovered,
} = useEmergencyDiagnostic()

// 2026-08-06: SSE endpoint URL 管理抽到 useLiveStreamUrl composable
// （localStorage 持久化 / 编辑状态机 / 保存重连 / 连接测试）
const {
  streamUrl,
  isEditingUrl,
  editUrlValue,
  startEditUrl,
  saveUrl,
  resetUrl,
  cancelEditUrl,
  testConnection,
} = useLiveStreamUrl({ connection, reconnect: reconnectStream, t })

// 2026-08-06: 连接详情弹窗状态抽到 useConnectionDetail composable
// （isAdmin 仍在本组件模板使用，isEditingUrl 由 useLiveStreamUrl 提供）
const {
  showConnectionDetail,
  toggleConnectionDetail,
} = useConnectionDetail({ isAdmin, isEditingUrl })

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
  if (groupBy.value === 'credential') return t('dashboard.liveStream.dimensionCredential')
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
// 2026-07-13: 冷启动检测 — 后端尚未推送任何泳道
// "仅探测" 过滤后为空不算冷启动（已有泳道只是被过滤）
// 2026-07-13 修正：只有 filteredLanes 也为空时才显示空态（避免有泳道显示时仍显示"暂无请求数据"）
const isColdStart = computed(() => {
  return lanes.value.length === 0 && filteredLanes.value.length === 0
})

// 2026-08-06: 保留 UI label 辅助函数（i18n 翻译）
function statusOptionLabel(v: string) {
  return t(`dashboard.liveStream.status.${v}`)
}
function vendorOptionLabel(v: string) {
  const key = `dashboard.liveStream.vendor.${v}`
  const labeled = t(key)
  return labeled === key ? v : labeled
}

</script>

<template>
  <div class="live-stream-v2">
    <!-- 标题栏 -->
    <div class="stream-header">
      <h3 class="stream-title">{{ t('dashboard.liveStream.title') }}</h3>

      <div
        class="stream-controls"
        role="toolbar"
        :aria-label="t('dashboard.liveStream.controlsAria', '实时请求流控制栏')"
        tabindex="0"
      >
        <!-- 分组切换 -->
        <div class="control-group control-group--dimension">
          <button
            type="button"
            class="control-btn"
            :class="{ 'control-btn--active': groupBy === 'queue' }"
            @click="handleGroupByChange('queue')"
          >
            {{ t('dashboard.liveStream.groupByQueue') }}
          </button>
          <button
            type="button"
            class="control-btn"
            :class="{ 'control-btn--active': groupBy === 'credential' }"
            @click="handleGroupByChange('credential')"
          >
            {{ t('dashboard.liveStream.groupByCredential') }}
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

        <!-- 2026-07-24: 多维筛选（弹窗选择，选项完整显示） -->
        <div class="filter-group">
          <span class="filter-group__label">筛选</span>
          <button
            type="button"
            class="filter-btn"
            :class="{ 'filter-btn--active': statusFilter.size > 0 }"
            @click="openFilterDialog('status')"
          >
            {{ t('dashboard.liveStream.filterStatus') }}
            <span v-if="statusFilter.size > 0" class="filter-badge">{{ statusFilter.size }}</span>
          </button>
          <button
            type="button"
            class="filter-btn"
            :class="{ 'filter-btn--active': modelFilter.size > 0 }"
            @click="openFilterDialog('model')"
          >
            {{ t('dashboard.liveStream.filterModel') }}
            <span v-if="modelFilter.size > 0" class="filter-badge">{{ modelFilter.size }}</span>
          </button>
          <button
            type="button"
            class="filter-btn"
            :class="{ 'filter-btn--active': providerFilter.size > 0 }"
            @click="openFilterDialog('provider')"
          >
            {{ t('dashboard.liveStream.filterProvider') }}
            <span v-if="providerFilter.size > 0" class="filter-badge">{{ providerFilter.size }}</span>
          </button>
          <button
            type="button"
            class="filter-btn"
            :class="{ 'filter-btn--active': vendorFilter.size > 0 }"
            @click="openFilterDialog('vendor')"
          >
            {{ t('dashboard.liveStream.filterVendor') }}
            <span v-if="vendorFilter.size > 0" class="filter-badge">{{ vendorFilter.size }}</span>
          </button>
          <!-- 2026-07-27: 客户端维度筛选 -->
          <button
            type="button"
            class="filter-btn"
            :class="{ 'filter-btn--active': agentFilter.size > 0 }"
            @click="openFilterDialog('agent')"
          >
            {{ t('dashboard.liveStream.filterAgent') }}
            <span v-if="agentFilter.size > 0" class="filter-badge">{{ agentFilter.size }}</span>
          </button>
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

        <LiveStreamFilterDialog
          :open="filterDialog === 'status'"
          :title="t('dashboard.liveStream.filterStatus')"
          :options="availableStatuses"
          :selected="statusFilterSelected"
          :label-of="statusOptionLabel"
          :searchable="false"
          @update:open="(v) => { if (!v) filterDialog = null }"
          @apply="applyStatusFilter"
        />
        <LiveStreamFilterDialog
          :open="filterDialog === 'model'"
          :title="t('dashboard.liveStream.filterModel')"
          :options="availableModels"
          :selected="modelFilterSelected"
          @update:open="(v) => { if (!v) filterDialog = null }"
          @apply="applyModelFilter"
        />
        <LiveStreamFilterDialog
          :open="filterDialog === 'provider'"
          :title="t('dashboard.liveStream.filterProvider')"
          :options="availableProviders"
          :selected="providerFilterSelected"
          @update:open="(v) => { if (!v) filterDialog = null }"
          @apply="applyProviderFilter"
        />
        <LiveStreamFilterDialog
          :open="filterDialog === 'vendor'"
          :title="t('dashboard.liveStream.filterVendor')"
          :options="availableVendors"
          :selected="vendorFilterSelected"
          :label-of="vendorOptionLabel"
          :searchable="false"
          @update:open="(v) => { if (!v) filterDialog = null }"
          @apply="applyVendorFilter"
        />
        <!-- 2026-07-27: 客户端筛选弹窗 (agent_name,全小写) -->
        <LiveStreamFilterDialog
          :open="filterDialog === 'agent'"
          :title="t('dashboard.liveStream.filterAgent')"
          :options="availableAgents"
          :selected="agentFilterSelected"
          :searchable="true"
          @update:open="(v) => { if (!v) filterDialog = null }"
          @apply="applyAgentFilter"
        />

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

    <!-- 2026-08-14 V3.2: 按处理队列维度时显示队列透视 + 节点矩阵面板 -->
    <!-- 2026-08-28: 传递上层筛选条件到 QueuePerspectivePanel -->
    <div v-if="groupBy === 'queue'" class="v32-queue-panels">
      <QueuePerspectivePanel
        :model-filter="modelFilter"
        :provider-filter="providerFilter"
        :vendor-filter="vendorFilter"
        :agent-filter="agentFilter"
        :status-filter="statusFilter"
      />
      <RequestJourneyQueues />
      <NodeStatusMatrix />
    </div>

    <!-- 泳道区域（其他维度） -->
    <div v-else class="swim-lanes">
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
  flex-direction: column;
  align-items: stretch;
  gap: 12px;
  margin-bottom: 12px;
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
  width: 100%;
  min-width: 0;
  overflow-x: auto;
  overflow-y: hidden;
  flex-wrap: nowrap;
  padding: 2px 2px 6px;
  overscroll-behavior-inline: contain;
  scrollbar-width: thin;
  scrollbar-color: var(--border) transparent;
  -webkit-overflow-scrolling: touch;
}

.stream-controls:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
  border-radius: 6px;
}

.stream-controls::-webkit-scrollbar {
  height: 6px;
}

.stream-controls::-webkit-scrollbar-thumb {
  background: var(--border);
  border-radius: 3px;
}

.stream-controls::-webkit-scrollbar-track {
  background: transparent;
}

.control-group {
  display: flex;
  align-items: center;
  flex: 0 0 auto;
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
  background: color-mix(in srgb, var(--accent) 14%, var(--bg-subtle));
  border-color: color-mix(in srgb, var(--accent) 35%, var(--border));
}

.control-group.control-group--mode {
  background: color-mix(in srgb, var(--success) 14%, var(--bg-subtle));
  border-color: color-mix(in srgb, var(--success) 35%, var(--border));
}

.control-group.control-group--request-type {
  background: color-mix(in srgb, var(--accent) 16%, var(--bg-subtle));
  border-color: color-mix(in srgb, var(--accent) 38%, var(--border));
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
  border-color: color-mix(in srgb, var(--accent) 40%, transparent);
}
.control-btn--probe.control-btn--active {
  background: color-mix(in srgb, var(--accent) 18%, transparent);
  border-color: var(--accent);
  color: var(--accent);
}

/* 2026-07-24: 多维过滤器使用警告色，与维度/模式/请求类型组区分。 */
.filter-group {
  display: flex;
  align-items: center;
  flex: 0 0 auto;
  gap: 8px;
  flex-wrap: nowrap;
  padding: 5px 10px;
  border-radius: 6px;
  background: color-mix(in srgb, var(--warning) 18%, var(--bg-subtle));
  border: 1px solid color-mix(in srgb, var(--warning) 45%, var(--border));
}

/* 2026-07-24: "筛选"前缀标签，让用户一眼看到这是筛选项区 */
.filter-group__label {
  font-size: 11px;
  font-weight: 600;
  color: var(--warning);
  letter-spacing: 0.5px;
  user-select: none;
  padding-right: 2px;
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
  box-shadow: var(--kx-shadow-sm);
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
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--success) 18%, transparent);
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
  box-shadow: var(--kx-shadow-md);
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
  background: var(--accent-h);
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
  background: var(--accent-h);
  border-color: var(--accent-h);
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
  background: var(--warning-soft);
  border: 1px solid color-mix(in srgb, var(--warning) 40%, transparent);
  border-radius: 6px;
  color: var(--warning);
  font-size: 12px;
  line-height: 1.4;
}

.redis-warning-icon {
  font-size: 16px;
  flex-shrink: 0;
}

/* 2026-08-14 V3.2: 按处理队列维度的面板容器 */
.v32-queue-panels {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-top: 12px;
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
  background: var(--bg-secondary);
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
  .stream-controls {
    flex-direction: row;
    flex-wrap: nowrap;
    overflow-x: auto;
  }

  .control-group,
  .filter-group {
    flex: 0 0 auto;
  }

  .control-btn {
    flex: 0 0 auto;
  }
}
</style>
