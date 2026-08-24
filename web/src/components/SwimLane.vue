<script setup lang="ts">
// SwimLane.vue — 单条泳道组件
// 2026-07-05: 显示单条泳道及其请求色块
// 2026-07-07: 动态计算可显示的请求数，适配窗口宽度；标题折行
// 2026-07-07 v2: ResizeObserver 防抖 + 父容器 min-width:0 + 宽屏/窄屏分级
// 2026-07-13: 增加诊断入口（活跃/恢复中显示 1/5..4/5；其它泳道禁用并附 tooltip）

import { computed, ref, onMounted, onUnmounted, watch, nextTick } from 'vue'
import type {
  SwimLane as SwimLaneType,
  RequestTile as RequestTileType,
  GroupByDimension,
  SwimLaneMode,
} from '../types/swimlane'
import { useRouteIncidents } from '../composables/useRouteIncidents'
import type { RouteIncident } from '../types/routeIncident'
import RequestTile from './RequestTile.vue'
import SwimLaneTrack from './SwimLaneTrack.vue'

const props = defineProps<{
  lane: SwimLaneType
  groupBy: GroupByDimension
  selectedLegends: Set<string>
  mode?: SwimLaneMode
  avgLatencyMs?: number
}>()

// 2026-07-23: 小模式尺寸（竖条更窄，可放更多请求）
const laneMode = computed<SwimLaneMode>(() => props.mode || 'small')
const isSmall = computed(() => laneMode.value === 'small')
const TILE_WIDTH = computed(() => (isSmall.value ? 9 : 80))
const TILE_GAP = computed(() => (isSmall.value ? 4 : 6))
const TRACK_PADDING = 16

// 延时显示（子项②）：仅 provider 维度且有数据时展示
const showLatency = computed(
  () => props.groupBy === 'provider' && props.avgLatencyMs != null && props.avgLatencyMs > 0,
)
const latencyText = computed(() => {
  const ms = props.avgLatencyMs
  if (ms == null || ms <= 0) return ''
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.round(ms)}ms`
})

const emit = defineEmits<{
  tileClick: [requestId: string]
  emergencyDiagnose: [data: { credentialId: number; model: string; laneName: string }]
  diagnose: [incidentId: string, preview: RouteIncident]
}>()

// ─── 诊断入口（2026-07-13，spec §"Dashboard Experience"）─────────────
// 触发条件（state-machine 视角）：后端检测到连续 3 次终态失败，
// 通过 SSE incident_update 把泳道变成 active；恢复中显示 n/5；
// 恢复 5 次后自动隐藏。其它泳道（无可识别路由身份）禁用。
// 同时保留用户此前基于"可见错误率 >= 1/3"的应急诊断按钮作为
// 状态机未启用时的兜底（dashboard 可能在没有 route_incidents
// 表的旧环境运行）。
const { incidentsForLane, canDiagnose } = useRouteIncidents()

const activeIncidents = computed<RouteIncident[]>(() => {
  if (!canDiagnose(props.lane)) return []
  return incidentsForLane(props.groupBy, props.lane.id)
})

const primaryIncident = computed<RouteIncident | null>(
  () => activeIncidents.value[0] ?? null,
)

const diagnoseLabel = computed<string>(() => {
  const inc = primaryIncident.value
  if (!inc) return ''
  if (inc.state === 'recovering') {
    return `Recovery ${inc.recovery_streak}/5`
  }
  return `诊断 · ${inc.failure_streak}`
})

const diagnoseDisabled = computed<boolean>(() => {
  if (!canDiagnose(props.lane)) return true
  return activeIncidents.value.length === 0
})

const diagnoseTooltip = computed<string>(() => {
  if (!canDiagnose(props.lane)) {
    return '"其它" 聚合泳道无可识别的路由身份，暂不支持诊断'
  }
  if (activeIncidents.value.length === 0) {
    return '当前泳道无活跃诊断事件（连续失败未达到 3 次）'
  }
  return primaryIncident.value?.state === 'recovering'
    ? '连续成功进度：点击查看恢复详情'
    : '点击查看诊断工作台'
})

function handleDiagnoseClick() {
  const inc = primaryIncident.value
  if (!inc || diagnoseDisabled.value) return
  emit('diagnose', inc.id, inc)
}

// 泳道名称显示（不截断，允许折行）
const displayName = computed(() => {
  const name = props.lane.name
  const count = props.lane.stats.total
  return `${name} (${count})`
})

// 动态计算可显示的请求数
const trackRef = ref<HTMLElement | null>(null)
const trackWidth = ref(0)

// 落到 1 时也至少允许显示一个，避免空泳道
const MIN_VISIBLE_TILES = 1

const maxVisibleTiles = computed(() => {
  // Width-only capacity: do NOT clamp to tiles.length — coupling window size
  // to count re-layouts the visible window as tiles arrive (shake secondary).
  if (trackWidth.value <= 0) return 10
  const availableWidth = Math.max(0, trackWidth.value - TRACK_PADDING)
  if (availableWidth <= 0) return MIN_VISIBLE_TILES
  const count = Math.floor((availableWidth + TILE_GAP.value) / (TILE_WIDTH.value + TILE_GAP.value))
  return Math.max(MIN_VISIBLE_TILES, count)
})

// 2026-07-13: 应急诊断按钮 — 旧版启发式（错误率 >= 1/3）。
// 当且仅当 state-machine 还没有 active incident 时才显示，避免重复。
// 兜底逻辑：dashboard 第一次加载、observer 未启动、或
// route_incidents 表尚未迁移。
const visibleErrorRate = computed(() => {
  const visible = props.lane.requests
  if (visible.length === 0) return 0
  const errorCount = visible.filter((r) => r.status !== 'success').length
  return errorCount / visible.length
})

const showEmergencyButton = computed(() => {
  if (primaryIncident.value) return false
  return visibleErrorRate.value >= 1 / 3
})

function handleEmergencyDiagnose() {
  // 2026-07-26: Backend下发按 (ts ASC, request_id ASC) 排序（oldest first）。
  // 最近的失败在数组尾部，因此反向遍历找第一个非 success。
  const recentFailure = [...props.lane.requests].reverse().find((r) => r.status !== 'success')
  if (!recentFailure) return
  emit('emergencyDiagnose', {
    credentialId: (recentFailure as any).credential_id || 0,
    model: (recentFailure as any).client_model || props.lane.name,
    laneName: props.lane.name,
  })
}

function handleTileClick(requestId: string) {
  emit('tileClick', requestId)
}

// —— ResizeObserver 监听轨道宽度变化 ——
let resizeObserver: ResizeObserver | null = null
let rafPending = false

function measureTrack() {
  if (rafPending) return
  rafPending = true
  requestAnimationFrame(() => {
    rafPending = false
    if (trackRef.value) {
      const w = trackRef.value.getBoundingClientRect().width
      if (Math.abs(w - trackWidth.value) > 0.5) trackWidth.value = w
    }
  })
}

onMounted(async () => {
  await nextTick()
  measureTrack()
  if (trackRef.value && typeof ResizeObserver !== 'undefined') {
    resizeObserver = new ResizeObserver(() => measureTrack())
    resizeObserver.observe(trackRef.value)
  }
  // 字体加载、布局微调等都会影响宽度，再补一帧
  await nextTick()
  measureTrack()
})

onUnmounted(() => {
  if (resizeObserver) {
    resizeObserver.disconnect()
    resizeObserver = null
  }
})

// 父组件传入 lane 变化或 ref 切换时重新测量
watch(trackRef, async (newEl, oldEl) => {
  if (resizeObserver && oldEl) resizeObserver.unobserve(oldEl)
  if (resizeObserver && newEl) {
    await nextTick()
    measureTrack()
    resizeObserver.observe(newEl)
  }
})

// Do not re-measure on requests.length — width is independent of tile count;
// re-measure here caused maxVisible flicker as tiles arrived.

// 2026-07-23: 模式切换后 tile 尺寸变化，需重新计算容量
watch(laneMode, async () => {
  await nextTick()
  measureTrack()
})
</script>

<template>
  <div class="swim-lane">
    <div class="swim-lane__label">
      <div class="swim-lane__name">{{ displayName }}</div>
      <div class="swim-lane__stats">
        <span class="swim-lane__stat swim-lane__stat--success" :title="`成功: ${lane.stats.success}`">
          ✓{{ lane.stats.success }}
        </span>
        <span class="swim-lane__stat swim-lane__stat--failure" :title="`失败: ${lane.stats.failure}`">
          ✗{{ lane.stats.failure }}
        </span>
        <span
          v-if="showLatency"
          class="swim-lane__stat swim-lane__latency"
          :title="`供应商 HTTP 延时: ${latencyText}`"
        >⏱{{ latencyText }}</span>
        <button
          v-if="!lane.isOthers || primaryIncident || showEmergencyButton"
          type="button"
          :class="[
            'swim-lane__diagnose',
            primaryIncident
              ? `swim-lane__diagnose--${primaryIncident.state}`
              : (lane.isOthers ? 'swim-lane__diagnose--disabled' : 'swim-lane__diagnose--idle'),
            { 'swim-lane__diagnose--disabled': diagnoseDisabled },
          ]"
          :disabled="diagnoseDisabled"
          :title="diagnoseTooltip"
          :aria-label="diagnoseTooltip"
          @click.stop="primaryIncident ? handleDiagnoseClick() : handleEmergencyDiagnose()"
        >
          <span class="swim-lane__diagnose-dot" aria-hidden="true" />
          <span class="swim-lane__diagnose-label">
            <template v-if="primaryIncident">{{ diagnoseLabel }}</template>
            <template v-else-if="lane.isOthers">诊断（不可用）</template>
            <template v-else>诊断</template>
          </span>
        </button>
      </div>
    </div>
    <div
      class="swim-lane__track"
      :class="{ 'swim-lane__track--small': isSmall }"
      :style="{ '--tile-w': TILE_WIDTH + 'px', '--tile-gap': TILE_GAP + 'px' }"
      ref="trackRef"
    >
      <SwimLaneTrack
        :tiles="lane.requests"
        :mode="laneMode"
        :group-by="groupBy"
        :max-visible="maxVisibleTiles"
        :selected-legends="selectedLegends"
        @tile-click="handleTileClick"
      />
    </div>
  </div>
</template>

<style scoped>
.swim-lane {
  display: flex;
  align-items: stretch;
  gap: 10px;
  min-height: 72px;
  padding: 4px 0;
  min-width: 0;
  width: 100%;
  max-width: 100%;
  box-sizing: border-box;
}

.swim-lane__label {
  flex: 0 0 160px;
  min-width: 0;
  width: 160px;
  display: flex;
  flex-direction: column;
  justify-content: center;
  padding: 8px 10px;
  background: var(--surface-secondary);
  border: 1px solid var(--border);
  border-radius: 8px;
  gap: 5px;
  overflow-wrap: break-word;
  box-shadow: var(--kx-shadow-sm);
}

.swim-lane__name {
  font-size: 11px;
  font-weight: 600;
  color: var(--text);
  line-height: 1.4;
  /* 强制断词 + 折行；禁用单行省略，避免长名称被截断显示成 "..." */
  word-break: break-word;
  overflow-wrap: anywhere;
  white-space: normal;
  max-width: 100%;
  min-width: 0;
}

.swim-lane__stats {
  display: flex;
  gap: 6px;
  font-size: 10px;
  font-variant-numeric: tabular-nums;
  flex-wrap: wrap;
}

.swim-lane__stat {
  color: var(--muted);
  font-weight: 500;
}

.swim-lane__stat--success {
  color: var(--success);
}

.swim-lane__stat--failure {
  color: var(--danger);
}

.swim-lane__diagnose {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  border-radius: 999px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text);
  font-size: 10px;
  font-weight: 600;
  cursor: pointer;
  font-variant-numeric: tabular-nums;
  transition: all 0.15s ease;
  white-space: nowrap;
}

.swim-lane__diagnose:hover:not(:disabled) {
  border-color: var(--accent);
  background: var(--bg-subtle);
}

.swim-lane__diagnose:disabled,
.swim-lane__diagnose--disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.swim-lane__diagnose-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--text-secondary);
}

.swim-lane__diagnose--active {
  border-color: var(--danger-bd);
  color: var(--danger);
}
.swim-lane__diagnose--active .swim-lane__diagnose-dot {
  background: var(--danger);
  box-shadow: 0 0 0 3px rgba(248, 81, 73, 0.18);
  animation: pulse-dot 1.4s ease-in-out infinite;
}

.swim-lane__diagnose--recovering {
  border-color: var(--warning-bd);
  color: var(--warning);
}
.swim-lane__diagnose--recovering .swim-lane__diagnose-dot {
  background: var(--warning);
}

.swim-lane__diagnose-label {
  line-height: 1;
}

@keyframes pulse-dot {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.5; }
}

@media (prefers-reduced-motion: reduce) {
  .swim-lane__diagnose--active .swim-lane__diagnose-dot {
    animation: none;
  }
}

.swim-lane__emergency-btn {
  margin-top: 6px;
  padding: 2px 6px;
  font-size: 10px;
  font-weight: 600;
  color: var(--danger);
  background: transparent;
  border: 1px solid var(--danger);
  border-radius: 4px;
  cursor: pointer;
  transition: all 0.2s ease;
  align-self: flex-start;
}

.swim-lane__emergency-btn:hover {
  background: color-mix(in srgb, var(--danger) 15%, transparent);
}

.swim-lane__emergency-btn:active {
  transform: scale(0.97);
}


.swim-lane__track {
  --tile-w: 80px;
  --tile-gap: 6px;
  flex: 1 1 0;
  min-width: 0;
  width: auto;
  display: flex;
  align-items: center;
  background:
    repeating-linear-gradient(
      90deg,
      transparent 0,
      transparent calc(var(--tile-w) + var(--tile-gap) - 1px),
      color-mix(in srgb, var(--text) 3%, transparent) calc(var(--tile-w) + var(--tile-gap) - 1px),
      color-mix(in srgb, var(--text) 3%, transparent) calc(var(--tile-w) + var(--tile-gap))
    ),
    var(--bg-tertiary);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 6px 10px;
  overflow: hidden;
  overflow-x: hidden;
  overflow-y: hidden;
  box-shadow: var(--kx-shadow-sm);
}

.swim-lane__track--small {
  /* 小模式轨道更紧凑 */
  min-height: 64px;
}

.swim-lane__latency {
  color: var(--accent-h);
  font-weight: 600;
}

/* 2026-07-26: Animation removed from SwimLane.vue — now handled by SwimLaneTrack.vue.
   Keeping reduced-motion media query for accessibility. */
@media (prefers-reduced-motion: reduce) {
  * {
    animation-duration: 0.01ms !important;
    transition-duration: 0.01ms !important;
  }
}
</style>
