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
import ModelPicker from './ModelPicker.vue'
import QueuePerspectivePanel from './QueuePerspectivePanel.vue'
import RequestJourneyQueues from './RequestJourneyQueues.vue'
import NodeStatusMatrix from './NodeStatusMatrix.vue'
import type { GroupByDimension } from '../types/swimlane'
import type { RouteIncident } from '../types/routeIncident'
import { Clock, Close, Warning } from '@element-plus/icons-vue'

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
// initialDataReceived（2026-10-16 audit）：「连接已建立」≠「本代际权威初始帧已到」。
// 只有 initial_data（含显式空回放）能证明当前身份的 requests/snapshot 已交付；
// health/keepalive 类旁路帧不得撤掉原显示区域的加载标记。
const {
  snapshot: liveSnapshot,
  connection,
  initialDataReceived,
  paused,
  togglePause,
  reconnect: reconnectStream,
} = useLiveStream()

const {
  groupBy,
  mode: laneMode,
  setMode: setLaneMode,
  lanes,
  filterSourceLanes,
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
  } = useLiveStreamFilters({ lanes: filterSourceLanes })

// 2026-07-24: 筛选弹窗状态（保留在组件内，仅 UI 控制）
// 2026-09-19: 模型维度改用标准 ModelPicker（与 routing-v2 resolve 一致），不再走通用弹窗
const filterDialog = ref<'status' | 'provider' | 'vendor' | 'agent' | null>(null)

function openFilterDialog(kind: 'status' | 'provider' | 'vendor' | 'agent') {
  filterDialog.value = kind
}

function onModelFilterPicked(value: string | string[]) {
  // R46 F8⑩: 非数组（single 模式）按单值包裹，不得吞成空数组（清空筛选）；
  // 空值清空视为未选。
  applyModelFilter(Array.isArray(value) ? value : [value].filter((v) => !!v))
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

// ── 2026-10-15：按处理队列首开反馈（2026-10-16 audit 收口） ─────────────────
//
// 用户报告：第一次打开总览页的「按处理队列」视图时看不到任何连接动作。
// groupBy 的默认值就是 'queue'（liveStreamPreferences defaults），所以每个
// 新会话首开都落在这个视图。
//
// 本组件只负责「原显示区域的加载/连接反馈」；fatal-closed 的自动重连已下沉到
// liveStreamStore（audit 修复：RequestRegistry / RequestJourneyDetail 等直接
// acquire 的 consumer 此前没有任何恢复路径）。
//
// awaitingInitialData：连接未建立，或已建立但本代际的权威 initial_data 还没到
// （含显式空回放）。health/keepalive 类旁路帧不能代表 requests/snapshot 已交付，
// 不得撤掉原显示区域的加载标记。
const awaitingInitialData = computed(
  () => connection.value !== 'open' || !initialDataReceived.value,
)

const queueStreamNotice = computed<{ tone: 'connecting' | 'closed' | 'unsupported'; text: string } | null>(() => {
  switch (connection.value) {
    case 'closed':
      return { tone: 'closed', text: t('dashboard.liveStream.queueStreamClosed') }
    case 'unsupported':
      return { tone: 'unsupported', text: t('dashboard.liveStream.queueStreamUnsupported') }
  }
  if (awaitingInitialData.value) {
    // 已连接但权威初始帧未到：文案如实区分「连接中」与「接收首批数据中」。
    return connection.value === 'open'
      ? { tone: 'connecting', text: t('dashboard.liveStream.queueStreamAwaiting') }
      : { tone: 'connecting', text: t('dashboard.liveStream.queueStreamConnecting') }
  }
  return null
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

// 冷启动空态按交付状态区分文案：本代际权威初始帧未到说「正在连接」，
// 已交付才说「等待请求」。混用一句会把建连窗口读成「没有数据」。
const coldStartText = computed(() => {
  if (connection.value === 'open' && initialDataReceived.value) {
    return t('dashboard.liveStream.emptyWaiting')
  }
  return t('dashboard.liveStream.emptyConnecting')
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
        <div class="control-group">
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
        <div class="control-group">
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

        <div class="control-group">
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
          <span class="filter-group__label">{{ t('dashboard.liveStream.filterGroup') }}</span>
          <button
            type="button"
            class="filter-btn"
            :class="{ 'filter-btn--active': statusFilter.size > 0 }"
            @click="openFilterDialog('status')"
          >
            {{ t('dashboard.liveStream.filterStatus') }}
            <span v-if="statusFilter.size > 0" class="filter-badge">{{ statusFilter.size }}</span>
          </button>
          <!-- 2026-09-19: 模型筛选改用标准 ModelPicker 多选组件（与 routing-v2 resolve 同源） -->
          <div
            class="filter-model-picker"
            :class="{ 'filter-model-picker--active': modelFilter.size > 0 }"
          >
            <ModelPicker
              mode="multi"
              compact
              :model-value="modelFilterSelected"
              :placeholder="t('dashboard.liveStream.filterModel')"
              :title="t('dashboard.liveStream.filterModel')"
              @update:model-value="onModelFilterPicked"
            />
          </div>
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
            <button type="button" class="popup-close" @click="showConnectionDetail = false"><Close /></button>
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
      <span class="redis-warning-icon"><Warning /></span>
      <span>{{ t('dashboard.liveStream.redisWarning', { error: redisErrorRef || t('dashboard.liveStream.redisFallbackError') }) }}</span>
    </div>

    <!-- 2026-08-14 V3.2: 按处理队列维度时显示队列透视 + 节点矩阵面板 -->
    <!-- 2026-08-28: 传递上层筛选条件到 QueuePerspectivePanel -->
    <div v-if="groupBy === 'queue'" class="v32-queue-panels">
      <!-- 2026-10-15: 队列视图自身的连接反馈——首开/断连时在数据区直接看见
           「正在连接实时流」，不再需要从工具栏药丸或「切维度重挂载」来确认。 -->
      <div
        v-if="queueStreamNotice"
        class="live-queue-connect"
        :class="`live-queue-connect--${queueStreamNotice.tone}`"
        role="status"
        aria-live="polite"
      >
        <span class="live-queue-connect__dot" aria-hidden="true" />
        <span class="live-queue-connect__text">{{ queueStreamNotice.text }}</span>
      </div>
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
        <span class="swim-lanes__empty-icon"><Clock /></span>
        <span class="swim-lanes__empty-text">{{ coldStartText }}</span>
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
  margin-bottom: 16px;
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
  /* ★ 2026-10-27 第 21 增量：分组容器不再各涂一种色调 ⇒ 组与组之间改由**间距**区分。
     组内按钮间距 4px，组间 14px ⇒ 比例 3.5:1，肉眼一眼分得开「组内」与「组间」。 */
  gap: 12px;
  width: 100%;
  min-width: 0;
  overflow-x: auto;
  overflow-y: hidden;
  flex-wrap: nowrap;
  padding: 2px 2px 4px;
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

/* ★ 2026-10-27 第 21 增量（颜色只承载语义，不承载分组）：
   分组容器一律中性底色 —— 旧实现给「维度 / 模式 / 请求类型」各涂一种色调
   （accent / success / accent），选色理由是「跟旁边不一样」，
   那是**装饰**不是状态；其中「小 / 大」被涂成 success（「它不是成功」）、
   「筛选」被涂成 warning（「它不是警告」）⇒ 颜色在撒谎。
   仓内明文：DESIGN.md §视觉世界「状态用最小的颜色量表达、每个元素都承担信息职责」；
   impeccable colorize.md「Decoration without a relationship to hierarchy,
   state, content… is not a color strategy」。
   现在：颜色预算只留给组内「被选中的那一个」（.control-btn--active）；
   组与组之间靠 .stream-controls 的间距区分。 */
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
}

.control-btn {
  font-size: 12px;
  padding: 4px 12px;
  border-radius: 4px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text);
  cursor: pointer;
  transition: background 0.15s var(--kx-motion-ease-out), border-color 0.15s var(--kx-motion-ease-out);
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

/* ★ 2026-10-27 第 21 增量：筛选组不再用告警色。
   旧注释写得很直白：「多维过滤器使用警告色，与维度/模式/请求类型组区分」
   —— 选色理由是「跟旁边不一样」，不是「这里有告警」。
   筛选区没有告警状态 ⇒ 涂橙是撒谎；组内的「筛选」标签已经承担了区隔职责。 */
.filter-group {
  display: flex;
  align-items: center;
  flex: 0 0 auto;
  gap: 8px;
  flex-wrap: nowrap;
  padding: 4px 8px;
  border-radius: 6px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
}

/* 2026-07-24: "筛选"前缀标签，让用户一眼看到这是筛选项区 */
.filter-group__label {
  font-size: 11px;
  font-weight: 600;
  color: var(--text-secondary);
  letter-spacing: 0.5px;
  user-select: none;
  padding-right: 2px;
}

.filter-dropdown {
  position: relative;
}

.filter-btn {
  font-size: 12px;
  padding: 4px 12px;
  border-radius: 4px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text);
  cursor: pointer;
  transition: background 0.15s var(--kx-motion-ease-out), border-color 0.15s var(--kx-motion-ease-out);
  white-space: nowrap;
  display: inline-flex;
  align-items: center;
  gap: 4px;
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

/* 2026-09-19: 模型筛选标准选择器（ModelPicker multi）——限宽融入筛选行 */
.filter-model-picker {
  flex: 0 0 auto;
  width: 150px;
  min-width: 110px;
  max-width: 240px;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg);
  transition: border-color 0.15s var(--kx-motion-ease-out);
}

.filter-model-picker:hover {
  border-color: var(--accent);
}

.filter-model-picker--active {
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 12%, transparent);
}

/* 内层触发器去边框，与相邻 filter-btn 高度对齐 */
.filter-model-picker :deep(.mp-trigger) {
  border: 0;
  background: transparent;
  min-height: 26px;
  padding: 2px 4px;
}

.filter-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 18px;
  height: 18px;
  padding: 0 4px;
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
  border-radius: 6px;
  box-shadow: var(--kx-shadow-sm);
  z-index: 100;
  padding: 4px;
}

.filter-option {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  cursor: pointer;
  border-radius: 4px;
  transition: background 0.15s var(--kx-motion-ease-out);
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
  padding: 8px 8px;
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
  padding: 4px 12px;
  border-radius: 4px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--warning);
  cursor: pointer;
  transition: background 0.15s var(--kx-motion-ease-out), border-color 0.15s var(--kx-motion-ease-out);
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
  gap: 4px;
  padding: 4px 12px;
  border-radius: 4px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text);
  font-size: 12px;
  cursor: pointer;
  transition: background 0.15s var(--kx-motion-ease-out), border-color 0.15s var(--kx-motion-ease-out);
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
  transition: background 0.15s var(--kx-motion-ease-out), color 0.15s var(--kx-motion-ease-out);
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
  transition: background 0.15s var(--kx-motion-ease-out), border-color 0.15s var(--kx-motion-ease-out), color 0.15s var(--kx-motion-ease-out);
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
  transition: background 0.15s var(--kx-motion-ease-out);
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
  padding: 4px 12px;
  border-radius: 4px;
  font-size: 12px;
  cursor: pointer;
  transition: background 0.15s var(--kx-motion-ease-out), border-color 0.15s var(--kx-motion-ease-out);
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
  gap: 4px;
  padding: 4px 12px;
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

/* 2026-10-15: 队列视图连接状态条。色调用既有语义令牌（--warning/--danger），
   与 Redis 健康警告条同一视觉家族；动画只用 opacity（rule：禁 layout 动画）。 */
.live-queue-connect {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 12px;
  border-radius: 6px;
  font-size: 12px;
  line-height: 1.5;
  border: 1px solid var(--border);
  background: var(--bg-subtle);
  color: var(--text-secondary);
}

.live-queue-connect--closed {
  background: color-mix(in srgb, var(--warning) 10%, var(--bg-subtle));
  border-color: color-mix(in srgb, var(--warning) 40%, var(--border));
  color: var(--text);
}

.live-queue-connect--unsupported {
  background: color-mix(in srgb, var(--danger) 8%, var(--bg-subtle));
  border-color: color-mix(in srgb, var(--danger) 35%, var(--border));
  color: var(--text);
}

.live-queue-connect__dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
  background: var(--muted);
  animation: live-queue-connect-pulse 1.4s ease-in-out infinite;
}

.live-queue-connect--closed .live-queue-connect__dot {
  background: var(--warning);
}

.live-queue-connect--unsupported .live-queue-connect__dot {
  background: var(--danger);
  animation: none;
}

@keyframes live-queue-connect-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.35; }
}

@media (prefers-reduced-motion: reduce) {
  .live-queue-connect__dot {
    animation: none;
  }
}

.swim-lanes {
  display: flex;
  flex-direction: column;
  gap: 8px;
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
  gap: 8px;
  padding: 48px 16px;
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

/* ── 2026-10-27 第 17 增量（AI 痕迹门 V3）：图标尺寸 ──
   element-plus 图标是裸 <svg viewBox="0 0 1024 1024">，**不带 width/height**
   ⇒ 浏览器按包含块宽度渲染（会撑爆布局）⇒ 必须显式定尺寸。
   尺寸沿用原字形的 font-size 观感（字形墨迹 ≈ 0.7em）。 */
.redis-warning-icon > svg { width: 14px; height: 14px; }
.swim-lanes__empty-icon > svg { width: 18px; height: 18px; }
</style>
