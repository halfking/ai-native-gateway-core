<script setup lang="ts">
// RequestTile.vue — 请求色块组件（80x60）
// 2026-07-13 v5: 现代观测面板风格 — 玻璃质感卡片 + 左侧色带 + 状态圆点
// 2026-07-14: 探测/idle tile 显示明确的错误原因（不再静默 "[空闲]"）

import { computed, nextTick, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RequestTile as RequestTileType, GroupByDimension, SwimLaneMode } from '../types/swimlane'
import {
  VENDOR_COLORS,
  calculateFontSize,
  truncateText,
} from '../types/swimlane'
import { errorKindLabel, statusBarColor, statusSemanticLabel } from '../composables/liveStreamDisplay'
import {
  getRequestActions,
  getRequestChildren,
  getRequestCredentialId,
  requestCredentialRevision,
  type LiveRequest,
} from '../composables/liveStreamStore'
import { credentialDisplayName } from '../composables/useCredentialLabels'
import ActionTimeline from './ActionTimeline.vue'

const { t, locale } = useI18n()

const props = withDefaults(defineProps<{
  tile: RequestTileType
  groupBy: GroupByDimension
  isHighlighted: boolean
  isDimmed: boolean
  mode?: SwimLaneMode
  showTimelineBadge?: boolean
}>(), {
  // Live stream cards never show ≈N; detail drawer / ActionTimeline keep it.
  showTimelineBadge: false,
})

const mode = computed<SwimLaneMode>(() => props.mode || 'small')
const isSmall = computed(() => mode.value === 'small')

const emit = defineEmits<{
  click: [requestId: string]
}>()

const accentColor = computed(() => {
  return VENDOR_COLORS[props.tile.vendor] || VENDOR_COLORS['__unknown__']
})

// 2026-07-23: 小模式竖条颜色（集中映射，亮暗皮肤通用）
const barColor = computed(() =>
  statusBarColor(props.tile.status, props.tile.error_kind),
)
const statusLabel = computed(() =>
  statusSemanticLabel(props.tile.status, props.tile.error_kind),
)

const statusColor = computed(() => {
  const s = props.tile.status
  if (s === 'failure') return '#ef4444'
  if (s === 'success') return '#22c55e'
  if (s === 'in_progress') return '#3b82f6'
  if (s === 'cancelled' || s === 'canceled') return '#9ca3af'
  if (s === 'idle') return '#9ca3af'
  return '#a1a1aa'
})

const showStatusDot = computed(() => props.tile.status !== 'idle')

const isTestRequest = computed(() => props.tile.is_probe === true)
const probeOrigin = computed(() => props.tile.probe_origin || 'direct')
const probeBadgeClass = computed(() => `request-tile__probe-badge--${probeOrigin.value}`)
const probeBadgeTooltip = computed(() => {
  const labels: Record<string, string> = {
    direct: t('dashboard.liveStream.probeDirect'),
    gateway: t('dashboard.liveStream.probeGateway'),
    scheduled: t('dashboard.liveStream.probeScheduled'),
  }
  return labels[probeOrigin.value] || t('dashboard.liveStream.probeGeneric')
})
const isIdle = computed(() => props.tile.status === 'idle')
const isInProgress = computed(() => props.tile.status === 'in_progress')
const isFailure = computed(() => props.tile.status === 'failure')

const credentialId = computed(() => {
  void requestCredentialRevision.value
  return getRequestCredentialId(props.tile.request_id)
})
const credentialName = computed(() => credentialDisplayName(credentialId.value))

// 2026-07-14: idle markers now arrive with an explicit error_kind
// ('no_traffic_5min') so we can show "无流量 X 分钟" instead of a
// generic "[空闲]" tile. Compute the elapsed minutes from the
// tile's timestamp; this matches the back-end's "5 minute silence"
// threshold so the label tells the operator exactly how long the
// lane has been silent.
const idleElapsedMinutes = computed<number | null>(() => {
  if (!isIdle.value || !props.tile.timestamp) return null
  const t = new Date(props.tile.timestamp).getTime()
  if (Number.isNaN(t)) return null
  const diffMs = Date.now() - t
  if (diffMs <= 0) return null
  return Math.floor(diffMs / 60000)
})

const idleLabel = computed<string>(() => {
  const m = idleElapsedMinutes.value
  if (m == null) return t('dashboard.liveStream.tileIdle')
  if (m < 1) return t('dashboard.liveStream.idleUnderOneMin')
  if (m < 60) return t('dashboard.liveStream.idleMinutes', { n: m })
  const hours = Math.floor(m / 60)
  const remMin = m % 60
  if (remMin === 0) return t('dashboard.liveStream.idleHours', { h: hours })
  return t('dashboard.liveStream.idleHoursMinutes', { h: hours, m: remMin })
})

// 2026-07-14: tile error reason line. Prefer a human-readable label
// of the typed error_kind (e.g. "5xx 服务端错误") when the tile is a
// probe failure — that is the "明确的错误原因" the operator asked
// for. For idle markers we use the idle_label so the operator can
// see the elapsed time at a glance.
const errorReasonLabel = computed<string | null>(() => {
  if (isIdle.value) return idleLabel.value
  if (props.tile.error_kind) return errorKindLabel(props.tile.error_kind)
  return null
})

const timeLabel = computed(() => {
  const date = new Date(props.tile.timestamp)
  const hh = String(date.getHours()).padStart(2, '0')
  const mm = String(date.getMinutes()).padStart(2, '0')
  return `${hh}:${mm}`
})

const latencyLabel = computed(() => {
  if (!props.tile.latency_ms) return ''
  const ms = props.tile.latency_ms
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.round(ms)}ms`
})

const modelFontSize = computed(() => calculateFontSize(props.tile.model, 80))

const line2Content = computed(() => {
  if (isIdle.value) {
    return props.tile.model || t('dashboard.liveStream.tileIdle')
  }
  if (props.tile.is_probe) {
    const origin = props.tile.probe_origin === 'gateway' ? 'GW' :
                   props.tile.probe_origin === 'scheduled' ? 'SCHED' : 'DIRECT'
    const attempt = props.tile.probe_attempt ? `#${props.tile.probe_attempt}` : ''
    return `${origin}${attempt}`
  }

  if (props.groupBy === 'vendor' || props.groupBy === 'provider') {
    return truncateText(props.tile.model, 12)
  }
  if (props.tile.status === 'success') return `✓ ${t('dashboard.liveStream.legend.success')}`
  if (props.tile.status === 'in_progress') return t('dashboard.liveStream.legend.inProgress')
  if (props.tile.status === 'cancelled' || props.tile.status === 'canceled') return t('dashboard.liveStream.legend.cancelled')
  if (props.tile.error_kind) {
    return truncateText(errorKindLabel(props.tile.error_kind), 12)
  }
  return '—'
})

const line3Content = computed(() => {
  if (isIdle.value) return t('dashboard.liveStream.idleHeartbeat')
  if (props.groupBy === 'vendor') return truncateText(props.tile.provider, 10)
  if (props.groupBy === 'provider') return truncateText(props.tile.vendor, 10)
  return truncateText(props.tile.provider, 10)
})

function tooltipLine(labelKey: string, value: string) {
  return `${t(labelKey)}: ${value}`
}

const tooltipText = computed(() => {
  const lines: string[] = []
  const tip = 'dashboard.liveStream.tooltip'
  const statusVal = 'dashboard.liveStream.tooltip.statusValue'
  if (props.tile.is_probe) {
    lines.push(t('dashboard.liveStream.probeGeneric'))
    const originLabel = props.tile.probe_origin === 'gateway'
      ? t('dashboard.liveStream.originGateway')
      : props.tile.probe_origin === 'scheduled'
        ? t('dashboard.liveStream.originScheduled')
        : t('dashboard.liveStream.originDirect')
    lines.push(t('dashboard.liveStream.probeOriginLabel', { origin: originLabel }))
    if (props.tile.probe_attempt) {
      lines.push(t('dashboard.liveStream.probeAttempt', { n: props.tile.probe_attempt }))
    }
  }
  if (props.tile.status === 'failure') {
    if (props.tile.error_kind) {
      lines.push(tooltipLine(`${tip}.errorKind`, errorKindLabel(props.tile.error_kind)))
      lines.push(tooltipLine(`${tip}.errorRawCode`, props.tile.error_kind))
    } else {
      lines.push(tooltipLine(`${tip}.status`, t(`${statusVal}.failure`)))
    }
  } else if (props.tile.status === 'success') {
    lines.push(tooltipLine(`${tip}.status`, t(`${statusVal}.success`)))
  } else if (props.tile.status === 'in_progress') {
    lines.push(tooltipLine(`${tip}.status`, t(`${statusVal}.inProgress`)))
  } else if (props.tile.status === 'cancelled' || props.tile.status === 'canceled') {
    lines.push(tooltipLine(`${tip}.status`, t(`${statusVal}.cancelled`)))
  } else if (props.tile.status === 'idle') {
    // 2026-07-14: idle tile tooltip carries the explicit
    // "no_traffic_5min" error_kind + the elapsed minutes so an
    // operator hovering the tile learns (a) why it's idle and
    // (b) how long the silence has lasted. The locale variable is
    // referenced to keep vue-i18n's reactive locale watcher live —
    // otherwise re-rendering on language switch drops the line.
    void locale.value
    if (props.tile.error_kind) {
      lines.push(tooltipLine(`${tip}.errorKind`, errorKindLabel(props.tile.error_kind)))
    }
    lines.push(tooltipLine(`${tip}.status`, idleLabel.value))
  }
  if (props.tile.model) lines.push(tooltipLine(`${tip}.model`, props.tile.model))
  if (props.tile.vendor) lines.push(tooltipLine(`${tip}.vendor`, props.tile.vendor))
  if (props.tile.provider) lines.push(tooltipLine(`${tip}.provider`, props.tile.provider))
  if (credentialId.value != null) lines.push(tooltipLine('凭据', credentialName.value))
  if (props.tile.latency_ms != null) {
    const ms = props.tile.latency_ms
    lines.push(tooltipLine(`${tip}.latency`, ms >= 1000 ? (ms / 1000).toFixed(1) + 's' : Math.round(ms) + 'ms'))
  }
  if (props.tile.prompt_tokens != null || props.tile.completion_tokens != null) {
    const p = props.tile.prompt_tokens ?? 0
    const c = props.tile.completion_tokens ?? 0
    lines.push(tooltipLine(`${tip}.tokens`, t(`${tip}.tokenFormat`, { p, c })))
  }
  if (props.tile.cost_usd != null) lines.push(tooltipLine(`${tip}.cost`, `$${props.tile.cost_usd.toFixed(4)}`))
  if (props.tile.request_id) lines.push(tooltipLine(`${tip}.requestId`, props.tile.request_id.slice(0, 12)))
  if (props.tile.timestamp) {
    try {
      lines.push(tooltipLine(`${tip}.time`, new Date(props.tile.timestamp).toLocaleString()))
    } catch { /* ignore */ }
  }
  return lines.join('\n')
})

function handleClick() {
  if (isIdle.value) return
  emit('click', props.tile.request_id)
}

// ─── 2026-08-15 OBS-FE3（26号 §3 / 24号 §1）：卡片生命周期徽标 ───────────
//
// 1) stage 徽标：仅在后端上报了 `stage`（24号 §1 状态机枚举）时渲染；
//    字段缺失 = 未上报，禁止用猜测值冒充（13号门禁）。
// 2) retrySeq 角标：retrySeq >= 1（非首次尝试）时显示 ⭐r{n} 特别标。
// 3) 子请求徽标与列表：右上角 children 计数徽标，点击弹出子请求列表面板。
//    数据只来自 liveStreamStore 的 SSE child_request 索引
//    （getRequestChildren），绝不调后端查询兜底（26号 §3：查询兜底
//    属于会话详情侧，实时流只用推送数据）；事件未到时列表显示空态文案。
const stageLabel = computed<string | null>(() => {
  const s = props.tile.stage
  return typeof s === 'string' && s.length > 0 ? s : null
})

const retrySeqLabel = computed<string | null>(() => {
  const n = props.tile.retrySeq
  return typeof n === 'number' && Number.isFinite(n) && n >= 1 ? `r${n}` : null
})

// 子请求索引只读消费（store 导出）；Map 变化驱动徽标/面板响应式更新。
const childRequests = computed<LiveRequest[]>(() => getRequestChildren(props.tile.request_id))
const childCount = computed(() => childRequests.value.length)

const childBadgeTitle = computed(() =>
  childCount.value > 0
    ? `子请求 ${childCount.value} 个（点击查看）`
    : '子请求（暂无推送，点击查看）',
)

const stageCategory = computed(() => props.tile.stage_category || null)
const isRouting = computed(() => stageCategory.value === 'routing' && isInProgress.value)
const isWaitingLLM = computed(() => stageCategory.value === 'llm' && isInProgress.value)
const isRetrying = computed(() => stageCategory.value === 'retrying')

// 子请求类型缩写（26号 §3：title/summary/sensitive_word）。
const CHILD_TYPE_SHORT: Record<string, string> = {
  title: 'T',
  summary: 'S',
  sensitive_word: 'SW',
  probe: 'P',
}
function childTypeShort(child: LiveRequest): string {
  const t = child.requestType
  return t ? (CHILD_TYPE_SHORT[t] ?? '·') : '·'
}

const childrenBadgeRef = ref<HTMLButtonElement | null>(null)
const childPanelOpen = ref(false)
const childPanelStyle = ref<Record<string, string>>({})

// 面板 Teleport 到 body：泳道轨道 overflow:hidden 会裁剪任何内嵌弹出层。
// 打开时按徽标位置计算 fixed 坐标；jsdom 下 rect 全 0，测试只断言内容。
function toggleChildPanel(evt: Event) {
  evt.stopPropagation()
  if (childPanelOpen.value) {
    childPanelOpen.value = false
    return
  }
  childPanelOpen.value = true
  void nextTick(() => {
    const anchor = childrenBadgeRef.value
    if (!anchor) return
    const rect = anchor.getBoundingClientRect()
    const PANEL_WIDTH = 240
    const vw = typeof window !== 'undefined' && window.innerWidth ? window.innerWidth : PANEL_WIDTH + 16
    const left = Math.max(8, Math.min(rect.right - PANEL_WIDTH, vw - PANEL_WIDTH - 8))
    childPanelStyle.value = { top: `${Math.round(rect.bottom + 6)}px`, left: `${Math.round(left)}px` }
  })
}

function closeChildPanel() {
  childPanelOpen.value = false
}

// ─── 2026-08-15 OBS-FE2（26号 §2/§6）：大形态动作时间线 ─────────────────────
//
// 大形态卡片的"轨迹"按钮弹出 ActionTimeline 面板（Teleport 到 body，防泳道
// 裁剪）。数据只来自 liveStreamStore 的 request_lifecycle 推送索引
// （getRequestActions），不调后端查询；事件未到时面板内显示空态文案。
const timelineActions = computed(() => getRequestActions(props.tile.request_id))
const timelineCount = computed(() => timelineActions.value.length)

const timelineBadgeRef = ref<HTMLButtonElement | null>(null)
const timelinePanelOpen = ref(false)
const timelinePanelStyle = ref<Record<string, string>>({})

const timelineBadgeTitle = computed(() =>
  timelineCount.value > 0
    ? `动作轨迹 ${timelineCount.value} 条（点击查看）`
    : '动作轨迹（暂无推送，点击查看）',
)

function toggleTimelinePanel(evt: Event) {
  evt.stopPropagation()
  if (timelinePanelOpen.value) {
    timelinePanelOpen.value = false
    return
  }
  timelinePanelOpen.value = true
  void nextTick(() => {
    const anchor = timelineBadgeRef.value
    if (!anchor) return
    const rect = anchor.getBoundingClientRect()
    const PANEL_WIDTH = 320
    const vw = typeof window !== 'undefined' && window.innerWidth ? window.innerWidth : PANEL_WIDTH + 16
    const left = Math.max(8, Math.min(rect.right - PANEL_WIDTH, vw - PANEL_WIDTH - 8))
    timelinePanelStyle.value = { top: `${Math.round(rect.bottom + 6)}px`, left: `${Math.round(left)}px` }
  })
}

function closeTimelinePanel() {
  timelinePanelOpen.value = false
}
</script>

<template>
  <div
    v-if="isSmall"
    class="request-bar"
    :class="{
      'request-bar--highlighted': isHighlighted,
      'request-bar--dimmed': isDimmed,
      'request-bar--idle': isIdle,
      'request-bar--in-progress': isInProgress,
      'request-bar--failure': isFailure,
      'request-bar--routing': isRouting,
      'request-bar--llm': isWaitingLLM,
    }"
    :style="{ '--bar-color': barColor }"
    :title="tooltipText"
    role="button"
    tabindex="0"
    :aria-label="statusLabel + ' ' + (tile.model || '')"
    @click="handleClick"
    @keydown.enter="handleClick"
  >
    <span v-if="tile.is_probe" class="request-bar__probe-mark" aria-hidden="true" />
    <span v-if="isRouting" class="request-bar__stage-mark request-bar__stage-mark--routing" aria-hidden="true" />
    <span v-if="isWaitingLLM" class="request-bar__stage-mark request-bar__stage-mark--llm" aria-hidden="true" />
  </div>

  <div
    v-else
    class="request-tile"
    :class="{
      'request-tile--highlighted': isHighlighted,
      'request-tile--dimmed': isDimmed,
      'request-tile--probe': tile.is_probe,
      'request-tile--idle': isIdle,
      'request-tile--in-progress': isInProgress,
      'request-tile--failure': isFailure,
      'request-tile--routing': isRouting,
      'request-tile--llm': isWaitingLLM,
    }"
    :style="{
      '--accent-color': accentColor,
      '--status-color': statusColor,
      '--model-font-size': modelFontSize + 'px',
    }"
    :title="tooltipText"
    @click="handleClick"
  >
    <div class="request-tile__accent" aria-hidden="true" />
    <span
      v-if="showStatusDot"
      class="request-tile__status-dot"
      :class="{ 'request-tile__status-dot--pulse': isInProgress }"
      aria-hidden="true"
    />
    <span
      v-if="isTestRequest"
      class="request-tile__probe-badge"
      :class="probeBadgeClass"
      :title="probeBadgeTooltip"
      aria-label="probe"
    >
      <svg class="request-tile__probe-icon" viewBox="0 0 16 16" aria-hidden="true">
        <circle cx="8" cy="8" r="5.5" fill="none" stroke="currentColor" stroke-width="1.4" />
        <circle cx="8" cy="8" r="1.6" fill="currentColor" />
        <path d="M8 2.2v2.2M8 11.6v2.2M2.2 8h2.2M11.6 8h2.2" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
      </svg>
    </span>

    <!-- OBS-FE3 (26号 §3): stage 徽标 — 仅当后端上报 stage 时渲染，缺省不显示。 -->
    <span
      v-if="stageLabel"
      class="request-tile__stage-badge"
      :title="`stage: ${stageLabel}`"
    >{{ stageLabel }}</span>

    <!-- OBS-FE3 (24号 §1): 重试角标 — retrySeq >= 1 时显示 ⭐r{n} 特别标。 -->
    <span
      v-if="retrySeqLabel"
      class="request-tile__retry-badge"
      :title="`重试第 ${retrySeqLabel.slice(1)} 次`"
    >⭐{{ retrySeqLabel }}</span>

    <!-- OBS-FE3 (26号 §3): 子请求计数徽标（右上角，位于状态点下方）。
         点击弹出子请求列表面板；计数 0（事件未推送）时置灰仍可点击，面板显示空态。 -->
    <button
      v-if="!isIdle"
      ref="childrenBadgeRef"
      type="button"
      class="request-tile__children-badge"
      :class="{ 'request-tile__children-badge--empty': childCount === 0 }"
      :title="childBadgeTitle"
      :aria-label="childBadgeTitle"
      :aria-expanded="childPanelOpen"
      aria-haspopup="dialog"
      @click.stop="toggleChildPanel"
      @keydown.enter.stop.prevent="toggleChildPanel"
    >{{ childCount }}</button>

    <!-- OBS-FE2 (26号 §6): 动作轨迹按钮（仅大形态，位于子请求徽标下方）。
         点击弹出 ActionTimeline 面板；计数 0（事件未推送）时置灰仍可点击。 -->
    <button
      v-if="!isIdle && showTimelineBadge"
      ref="timelineBadgeRef"
      type="button"
      class="request-tile__timeline-badge"
      :class="{ 'request-tile__timeline-badge--empty': timelineCount === 0 }"
      :title="timelineBadgeTitle"
      :aria-label="timelineBadgeTitle"
      :aria-expanded="timelinePanelOpen"
      aria-haspopup="dialog"
      @click.stop="toggleTimelinePanel"
      @keydown.enter.stop.prevent="toggleTimelinePanel"
    >≈{{ timelineCount }}</button>

    <div class="request-tile__body">
      <div class="request-tile__time">{{ timeLabel }}</div>
      <div class="request-tile__model">{{ line2Content }}</div>
      <!--
        2026-07-14: explicit error_reason strip below the model line.
        Idle tiles surface "无流量 X 分钟", probe failures surface the
        classified error_kind ("5xx", "rate_limit", etc.). Plain
        failure tiles reuse the same field so the operator always sees
        WHY the tile is red, not just THAT it is red.
      -->
      <div
        v-if="errorReasonLabel"
        class="request-tile__reason"
        :class="{ 'request-tile__reason--idle': isIdle, 'request-tile__reason--probe': isTestRequest }"
      >{{ errorReasonLabel }}</div>
      <div class="request-tile__footer">
        <span class="request-tile__provider">{{ line3Content }}</span>
        <span v-if="latencyLabel && !isIdle" class="request-tile__latency">{{ latencyLabel }}</span>
      </div>
      <span v-if="isRouting" class="request-tile__stage-indicator request-tile__stage-indicator--routing" title="路由排队中" aria-label="路由排队中">⏳</span>
      <span v-if="isWaitingLLM" class="request-tile__stage-indicator request-tile__stage-indicator--llm" title="等待大模型响应" aria-label="等待大模型响应">🔄</span>
      <span v-if="isRetrying" class="request-tile__stage-indicator request-tile__stage-indicator--retrying" title="重试调度中" aria-label="重试调度中">🔁</span>
    </div>

    <!-- OBS-FE3 (26号 §3): 子请求列表面板 — Teleport 到 body，
         避免被泳道轨道 overflow:hidden 裁剪。数据只来自 SSE
         child_request 推送（liveStreamStore.getRequestChildren），
         不调后端查询；事件未到时显示空态文案。 -->
    <Teleport to="body">
      <div
        v-if="childPanelOpen"
        class="request-tile__children-panel"
        :style="childPanelStyle"
        role="dialog"
        aria-label="子请求列表"
        tabindex="-1"
        @click.stop
        @keydown.esc.stop="closeChildPanel"
      >
        <div class="request-tile__children-panel-header">
          <span class="request-tile__children-panel-title">子请求 {{ childCount }}</span>
          <button
            type="button"
            class="request-tile__children-panel-close"
            aria-label="关闭子请求列表"
            @click.stop="closeChildPanel"
          >✕</button>
        </div>
        <ul v-if="childCount > 0" class="request-tile__children-list">
          <li
            v-for="child in childRequests"
            :key="child.request_id"
            class="request-tile__child-row"
          >
            <span
              class="request-tile__child-type"
              :title="child.requestType || ''"
            >{{ childTypeShort(child) }}</span>
            <span class="request-tile__child-model">{{ child.model || (child.request_id || '').slice(0, 8) || '—' }}</span>
            <span class="request-tile__child-status" :class="`request-tile__child-status--${child.status || 'unknown'}`">
              {{ child.status || '—' }}
            </span>
          </li>
        </ul>
        <p v-else class="request-tile__children-empty">
          暂无子请求事件推送（等待 child_request）
        </p>
      </div>
    </Teleport>

    <!-- OBS-FE2 (26号 §6): 动作时间线面板 — Teleport 到 body，数据只来自
         request_lifecycle 推送（liveStreamStore.getRequestActions）。 -->
    <Teleport to="body">
      <div
        v-if="timelinePanelOpen"
        class="request-tile__timeline-panel"
        :style="timelinePanelStyle"
        role="dialog"
        aria-label="动作时间线"
        tabindex="-1"
        @click.stop
        @keydown.esc.stop="closeTimelinePanel"
      >
        <div class="request-tile__timeline-panel-header">
          <span class="request-tile__timeline-panel-title">动作轨迹 {{ timelineCount }}</span>
          <button
            type="button"
            class="request-tile__children-panel-close"
            aria-label="关闭动作时间线"
            @click.stop="closeTimelinePanel"
          >✕</button>
        </div>
        <ActionTimeline :request-id="tile.request_id" />
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
/* ════════════════════════════════════════════════════════════════
 * 2026-07-23: 小模式竖条（request-bar）— 默认展示模式。
 * 颜色全部走 CSS 变量（--success/--danger/--warning/--kx-surface），
 * 因此亮色/暗色皮肤下都清晰。in_progress 用 --kx-surface 作白底，
 * 配脉冲动画。探测请求左侧青色标记条区分类型。
 * ════════════════════════════════════════════════════════════════ */
.request-bar {
  width: 9px;
  height: 52px;
  flex-shrink: 0;
  border-radius: 3px;
  background: var(--bar-color, var(--muted));
  border: 1px solid color-mix(in srgb, var(--text) 18%, transparent);
  /* 包含 border，避免 9px 实际渲染成 11px 破坏网格对齐 */
  box-sizing: border-box;
  cursor: pointer;
  position: relative;
  overflow: hidden;
  transition: transform 0.15s cubic-bezier(0.22, 1, 0.36, 1), box-shadow 0.15s ease;
}

.request-bar:hover {
  transform: scaleY(1.08);
  box-shadow: 0 0 0 1px color-mix(in srgb, var(--text) 35%, transparent),
              0 4px 10px color-mix(in srgb, var(--text) 18%, transparent);
  z-index: 10;
}

.request-bar--highlighted {
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 60%, transparent);
  z-index: 5;
}

.request-bar--dimmed {
  opacity: 0.35;
  filter: grayscale(0.4);
}

.request-bar--idle {
  opacity: 0.55;
  cursor: default;
  border-style: dashed;
}
.request-bar--idle:hover {
  transform: none;
  box-shadow: none;
}

/* in_progress: 白底 + 顶部脉冲色块，表示正在进行 */
.request-bar--in-progress {
  border-color: color-mix(in srgb, var(--accent) 45%, transparent);
  box-shadow: inset 0 -2px 0 color-mix(in srgb, var(--accent) 35%, transparent);
}
.request-bar--in-progress::after {
  content: '';
  position: absolute;
  inset: 0;
  background: linear-gradient(180deg, color-mix(in srgb, var(--accent) 22%, transparent) 0%, transparent 60%);
  animation: bar-pulse 1.6s ease-in-out infinite;
}
@keyframes bar-pulse {
  0%, 100% { opacity: 0.45; }
  50% { opacity: 1; }
}

/* Dual-stage: routing = cold dashed stripe; llm = solid pulse top mark */
.request-bar--routing {
  border-style: dashed;
  border-color: color-mix(in srgb, var(--accent) 55%, transparent);
  background:
    repeating-linear-gradient(
      -45deg,
      color-mix(in srgb, var(--accent) 14%, transparent) 0 3px,
      transparent 3px 7px
    ),
    var(--kx-surface, var(--on-primary));
}
.request-bar--routing::after {
  animation: none;
  opacity: 0.35;
  background: linear-gradient(180deg, color-mix(in srgb, var(--accent) 18%, transparent) 0%, transparent 70%);
}
.request-bar--llm {
  border-style: solid;
  border-color: color-mix(in srgb, var(--accent) 60%, transparent);
}
.request-bar__stage-mark {
  position: absolute;
  left: 1px;
  right: 1px;
  top: 0;
  height: 3px;
  border-radius: 2px 2px 0 0;
  pointer-events: none;
}
.request-bar__stage-mark--routing {
  background: color-mix(in srgb, var(--accent) 75%, transparent);
}
.request-bar__stage-mark--llm {
  background: color-mix(in srgb, var(--accent) 85%, transparent);
  animation: bar-pulse 1.2s ease-in-out infinite;
}

/* 探测请求左侧青色标记条 */
.request-bar__probe-mark {
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 2px;
  background: var(--probe-cyan);
  border-radius: 3px 0 0 3px;
  box-shadow: 0 0 4px color-mix(in srgb, var(--probe-cyan) 70%, transparent);
}

.request-bar--failure {
  border-color: color-mix(in srgb, var(--danger) 50%, transparent);
}

@media (prefers-reduced-motion: reduce) {
  .request-bar { transition: none; }
  .request-bar:hover { transform: none; }
  .request-bar--in-progress::after { animation: none; opacity: 0.6; }
}

.request-tile {
  --tile-radius: 8px;
  width: 80px;
  height: 60px;
  border-radius: var(--tile-radius);
  border: 1px solid color-mix(in srgb, var(--accent-color) 38%, transparent);
  background:
    linear-gradient(
      145deg,
      color-mix(in srgb, var(--accent-color) 16%, var(--kx-surface)) 0%,
      color-mix(in srgb, var(--accent-color) 6%, var(--kx-bg)) 100%
    );
  box-shadow: var(--kx-shadow-sm);
  padding: 0;
  cursor: pointer;
  transition:
    transform 0.18s cubic-bezier(0.22, 1, 0.36, 1),
    box-shadow 0.18s ease,
    border-color 0.18s ease;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI',
               'PingFang SC', 'Microsoft YaHei', sans-serif;
  display: flex;
  flex-shrink: 0;
  position: relative;
  overflow: hidden;
}

.request-tile:hover {
  transform: translateY(-2px) scale(1.04);
  border-color: color-mix(in srgb, var(--accent-color) 65%, transparent);
  box-shadow:
    0 6px 16px color-mix(in srgb, var(--text) 15%, transparent),
    0 0 0 1px color-mix(in srgb, var(--accent-color) 25%, transparent);
  z-index: 10;
}

.request-tile--highlighted {
  box-shadow:
    0 0 0 2px color-mix(in srgb, var(--accent) 55%, transparent),
    0 4px 14px color-mix(in srgb, var(--accent) 20%, transparent);
  z-index: 5;
}

.request-tile--dimmed {
  opacity: 0.38;
  filter: grayscale(0.45);
}

.request-tile--idle {
  border-style: dashed;
  border-color: color-mix(in srgb, var(--accent-color) 30%, transparent);
  background: transparent;
  box-shadow: none;
  cursor: default;
  opacity: 0.6;
}

.request-tile--idle:hover {
  transform: none;
  box-shadow: none;
}

.request-tile--failure {
  border-color: color-mix(in srgb, var(--danger) 45%, var(--accent-color));
}

/* Dual-stage large cards: routing = cold dashed; llm = solid pulse border */
.request-tile--routing {
  border-style: dashed;
  border-color: color-mix(in srgb, var(--accent) 55%, transparent);
  background:
    linear-gradient(
      145deg,
      color-mix(in srgb, var(--accent) 14%, var(--kx-surface)) 0%,
      color-mix(in srgb, var(--accent) 5%, var(--kx-bg)) 100%
    );
  box-shadow: none;
}
.request-tile--llm {
  border-style: solid;
  border-color: color-mix(in srgb, var(--accent) 55%, var(--accent-color));
  box-shadow:
    0 0 0 1px color-mix(in srgb, var(--accent) 22%, transparent),
    0 0 10px color-mix(in srgb, var(--accent) 12%, transparent);
  animation: request-tile-llm-glow 1.8s ease-in-out infinite;
}
@keyframes request-tile-llm-glow {
  0%, 100% { box-shadow: 0 0 0 1px color-mix(in srgb, var(--accent) 18%, transparent); }
  50% { box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 35%, transparent), 0 0 12px color-mix(in srgb, var(--accent) 18%, transparent); }
}

.request-tile--probe {
  border-color: color-mix(in srgb, var(--probe-cyan) 50%, var(--accent-color));
  /* 2026-07-14: 探测请求特殊背景 — 青色玻璃质感，与正常业务请求一眼区分 */
  background:
    linear-gradient(
      145deg,
      color-mix(in srgb, var(--probe-cyan) 22%, var(--kx-surface)) 0%,
      color-mix(in srgb, var(--probe-cyan-deep) 10%, var(--kx-bg)) 100%
    );
  box-shadow:
    inset 0 1px 0 color-mix(in srgb, var(--probe-cyan) 12%, transparent),
    0 0 0 1px color-mix(in srgb, var(--probe-cyan) 20%, transparent),
    0 1px 3px color-mix(in srgb, var(--text) 12%, transparent);
}

.request-tile--probe.request-tile--failure {
  border-color: color-mix(in srgb, var(--probe-cyan) 35%, var(--danger) 45%);
  background:
    linear-gradient(
      145deg,
      color-mix(in srgb, var(--probe-cyan) 14%, color-mix(in srgb, var(--danger) 18%, var(--kx-surface))) 0%,
      color-mix(in srgb, var(--probe-cyan-deep) 7%, var(--kx-bg)) 100%
    );
}

.request-tile__accent {
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 3px;
  background: var(--accent-color);
  border-radius: var(--tile-radius) 0 0 var(--tile-radius);
}

.request-tile--failure .request-tile__accent {
  background: linear-gradient(180deg, var(--danger), var(--accent-color));
}

.request-tile__status-dot {
  position: absolute;
  top: 5px;
  right: 5px;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--status-color);
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--text) 15%, transparent);
  z-index: 2;
}

.request-tile__status-dot--pulse {
  animation: status-pulse 1.6s ease-in-out infinite;
}

@keyframes status-pulse {
  0%, 100% {
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--text) 15%, transparent), 0 0 0 0 color-mix(in srgb, var(--accent) 16%, transparent);
  }
  50% {
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--text) 15%, transparent), 0 0 0 4px color-mix(in srgb, var(--accent) 16%, transparent);
  }
}

.request-tile__probe-badge {
  position: absolute;
  top: 3px;
  left: 5px;
  width: 14px;
  height: 14px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 3px;
  color: var(--probe-dark-bg);
  background: linear-gradient(180deg, var(--probe-cyan-light) 0%, var(--probe-cyan) 100%);
  border: 1.5px solid var(--probe-cyan-darker);
  box-shadow: 0 0 5px color-mix(in srgb, var(--probe-cyan) 30%, transparent);
  z-index: 3;
}

.request-tile__probe-icon {
  width: 10px;
  height: 10px;
  display: block;
}

/* 按探测来源区分颜色：scheduled=橙黄/周期，direct=红色/主动 */
.request-tile__probe-badge--direct {
  background: linear-gradient(180deg, var(--danger-bd) 0%, var(--danger) 100%);
  border-color: var(--danger);
  color: var(--on-primary);
}

.request-tile__probe-badge--scheduled {
  background: linear-gradient(180deg, var(--warning-bg) 0%, var(--warning) 100%);
  border-color: var(--warning);
  color: var(--warning-dark);
}

.request-tile__probe-badge--gateway {
  background: linear-gradient(180deg, var(--accent-h) 0%, var(--accent) 100%);
  border-color: var(--accent);
  color: var(--on-primary);
}

.request-tile__body {
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  flex: 1;
  min-width: 0;
  padding: 5px 6px 4px 9px;
}

.request-tile__time {
  font-size: 9px;
  text-align: center;
  line-height: 1.1;
  color: var(--kx-muted);
  font-weight: 500;
  font-variant-numeric: tabular-nums;
  letter-spacing: 0.02em;
  width: 100%;
  flex-shrink: 0;
}

.request-tile__model {
  font-size: var(--model-font-size, 10px);
  text-align: center;
  line-height: 1.25;
  font-weight: 600;
  color: var(--kx-text);
  overflow: hidden;
  text-overflow: ellipsis;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  word-break: break-word;
  margin: 1px 0;
}

.request-tile__footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 2px;
  min-width: 0;
}

.request-tile__provider {
  font-size: 8px;
  line-height: 1.2;
  color: var(--kx-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  flex: 1;
  min-width: 0;
}

.request-tile__latency {
  font-size: 8px;
  line-height: 1.2;
  font-variant-numeric: tabular-nums;
  color: var(--kx-text);
  font-weight: 600;
  flex-shrink: 0;
}

/* 2026-07-14: explicit error_reason strip rendered below the model
   line so operators see WHY the tile is red/idle/probe-failed, not
   just THAT it is. Plain text, dimmed, single line — must not
   steal vertical space from the model label above.
   - .request-tile__reason:        base (failure path)
   - .request-tile__reason--idle:  idle lane ("空闲 X 分钟")
   - .request-tile__reason--probe: probe row, mirrors the cyan accent */
.request-tile__reason {
  font-size: 8px;
  line-height: 1.1;
  text-align: center;
  font-weight: 600;
  color: color-mix(in srgb, var(--danger) 12%, transparent); /* default = failure red */
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  letter-spacing: 0.01em;
  margin-top: 1px;
}
.request-tile__reason--idle {
  color: color-mix(in srgb, var(--muted) 14%, transparent);
  font-weight: 500;
}
.request-tile__reason--probe {
  color: color-mix(in srgb, var(--probe-cyan) 22%, transparent);
}

@media (prefers-reduced-motion: reduce) {
  .request-tile {
    transition: opacity 0.15s linear;
  }
  .request-tile:hover {
    transform: none;
  }
  .request-tile__status-dot--pulse {
    animation: none;
  }
}

/* ════════════════════════════════════════════════════════════════
 * 2026-08-15 OBS-FE3（26号 §3 / 24号 §1）：请求卡生命周期徽标。
 * 颜色全部走 var(--kx-*)；动画只用 transform/opacity。
 *  - stage 徽标：左上角（探测徽标占位时下移），仅后端上报时渲染。
 *  - retry 角标：左上角第二行，⭐r{n} 描边样式（--kx-warning）。
 *  - 子请求徽标：右上角（状态点下方）计数角标。
 *  - 子请求面板：Teleport 到 body 的 fixed 弹层，绕开泳道轨道
 *    overflow:hidden 裁剪。
 * ════════════════════════════════════════════════════════════════ */
.request-tile__stage-badge {
  position: absolute;
  top: 2px;
  left: 3px;
  z-index: 3;
  max-width: 46px;
  padding: 1px 3px;
  border-radius: 3px;
  font-size: 7px;
  font-weight: 700;
  line-height: 1.2;
  letter-spacing: 0.02em;
  color: var(--kx-primary);
  background: var(--kx-primary-soft);
  border: 1px solid var(--kx-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  pointer-events: none;
}

/* 探测徽标（左上角 14x14）占位时，stage 徽标下移避免重叠 */
.request-tile--probe .request-tile__stage-badge {
  top: 19px;
}

/* 重试角标：⭐r{n}，warning 描边样式（描边 + 透明底），仅 retrySeq>=1 */
.request-tile__retry-badge {
  position: absolute;
  top: 14px;
  left: 3px;
  z-index: 3;
  max-width: 46px;
  padding: 1px 3px;
  border-radius: 3px;
  font-size: 7px;
  font-weight: 700;
  line-height: 1.2;
  font-variant-numeric: tabular-nums;
  color: var(--kx-warning);
  background: var(--kx-warning-soft);
  border: 1px solid var(--kx-warning);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  pointer-events: none;
}

.request-tile--probe .request-tile__retry-badge {
  top: 31px;
}

/* 子请求计数徽标：右上角（状态点下方），计数 0 时置灰但仍可点击（弹空态） */
.request-tile__children-badge {
  position: absolute;
  top: 13px;
  right: 3px;
  z-index: 4;
  min-width: 13px;
  height: 13px;
  padding: 0 3px;
  border-radius: 7px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 8px;
  font-weight: 700;
  line-height: 1;
  font-variant-numeric: tabular-nums;
  color: var(--kx-text-on-primary);
  background: var(--kx-primary);
  border: 1px solid var(--kx-primary);
  cursor: pointer;
  transition: transform 0.12s ease, opacity 0.12s ease;
}

.request-tile__children-badge:hover {
  transform: scale(1.12);
}

.request-tile__children-badge--empty {
  color: var(--kx-muted);
  background: var(--kx-surface);
  border-color: var(--kx-border);
  opacity: 0.75;
}

/* 子请求列表面板（Teleport 到 body，fixed 定位由打开时计算覆盖） */
.request-tile__children-panel {
  position: fixed;
  top: 0;
  left: 0;
  width: 240px;
  max-width: calc(100vw - 16px);
  max-height: 220px;
  display: flex;
  flex-direction: column;
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  box-shadow: var(--kx-shadow-md);
  z-index: 2000;
  animation: request-tile-panel-in 0.15s ease-out;
}

@keyframes request-tile-panel-in {
  from { opacity: 0; transform: translateY(-4px); }
  to   { opacity: 1; transform: translateY(0); }
}

.request-tile__children-panel-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 6px 8px;
  border-bottom: 1px solid var(--kx-border);
  background: var(--kx-bg-accent);
}

.request-tile__children-panel-title {
  font-size: 11px;
  font-weight: 600;
  color: var(--kx-text);
}

.request-tile__children-panel-close {
  border: none;
  background: transparent;
  color: var(--kx-muted);
  font-size: 12px;
  line-height: 1;
  cursor: pointer;
  padding: 2px 4px;
  border-radius: 3px;
  transition: opacity 0.12s ease, transform 0.12s ease;
}

.request-tile__children-panel-close:hover {
  color: var(--kx-text);
  transform: scale(1.1);
}

.request-tile__children-list {
  list-style: none;
  margin: 0;
  padding: 4px;
  overflow-y: auto;
  flex: 1;
  min-height: 0;
}

.request-tile__child-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 5px;
  border-radius: 4px;
  font-size: 11px;
}

.request-tile__child-row:hover {
  background: var(--kx-bg-accent);
}

/* 类型缩写 chip：T=title / S=summary / SW=sensitive_word / P=probe */
.request-tile__child-type {
  flex: 0 0 auto;
  min-width: 18px;
  text-align: center;
  padding: 1px 3px;
  border-radius: 3px;
  font-size: 9px;
  font-weight: 700;
  letter-spacing: 0.02em;
  color: var(--kx-primary);
  background: var(--kx-primary-soft);
  border: 1px solid var(--kx-border);
}

.request-tile__child-model {
  flex: 1 1 auto;
  min-width: 0;
  color: var(--kx-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.request-tile__child-status {
  flex: 0 0 auto;
  font-size: 9px;
  font-weight: 600;
  color: var(--kx-muted);
}

.request-tile__child-status--success { color: var(--kx-success); }
.request-tile__child-status--failure,
.request-tile__child-status--rate_limited { color: var(--kx-danger); }
.request-tile__child-status--in_progress { color: var(--kx-primary); }

.request-tile__children-empty {
  margin: 0;
  padding: 10px 12px;
  font-size: 11px;
  line-height: 1.5;
  color: var(--kx-muted);
}

/* OBS-FE2 动作轨迹按钮：位于子请求徽标下方；计数 0 时置灰仍可点击（弹空态） */
.request-tile__timeline-badge {
  position: absolute;
  top: 29px;
  right: 3px;
  z-index: 4;
  min-width: 13px;
  height: 13px;
  padding: 0 3px;
  border-radius: 7px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 8px;
  font-weight: 700;
  line-height: 1;
  font-variant-numeric: tabular-nums;
  color: var(--kx-text-on-primary);
  background: var(--kx-success);
  border: 1px solid var(--kx-success);
  cursor: pointer;
  transition: transform 0.12s ease, opacity 0.12s ease;
}

.request-tile__timeline-badge:hover {
  transform: scale(1.12);
}

.request-tile__timeline-badge--empty {
  color: var(--kx-muted);
  background: var(--kx-surface);
  border-color: var(--kx-border);
  opacity: 0.75;
}

/* 动作时间线面板（Teleport 到 body，fixed 定位由打开时计算覆盖） */
.request-tile__timeline-panel {
  position: fixed;
  top: 0;
  left: 0;
  width: 320px;
  max-width: calc(100vw - 16px);
  max-height: 300px;
  display: flex;
  flex-direction: column;
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  box-shadow: var(--kx-shadow-md);
  z-index: 2000;
  animation: request-tile-panel-in 0.15s ease-out;
}

.request-tile__timeline-panel-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 6px 8px;
  border-bottom: 1px solid var(--kx-border);
  background: var(--kx-bg-accent);
}

.request-tile__timeline-panel-title {
  font-size: 11px;
  font-weight: 600;
  color: var(--kx-text);
}

.request-tile__timeline-panel > :deep(.action-timeline) {
  padding: 4px 8px;
  overflow-y: auto;
  flex: 1;
  min-height: 0;
}

@media (prefers-reduced-motion: reduce) {
  .request-tile__children-panel,
  .request-tile__children-badge,
  .request-tile__children-panel-close,
  .request-tile__timeline-badge,
  .request-tile__timeline-panel {
    animation: none;
    transition: none;
  }
}

.request-tile__stage-indicator {
  position: absolute;
  bottom: 4px;
  left: 6px;
  z-index: 2;
  font-size: 10px;
  line-height: 1;
  pointer-events: none;
}

.request-tile__stage-indicator--routing {
  animation: request-tile-queue-pulse 2s ease-in-out infinite;
}

.request-tile__stage-indicator--llm {
  animation: request-tile-llm-spin 1.5s linear infinite;
}

.request-tile__stage-indicator--retrying {
  animation: request-tile-retry-pulse 1s ease-in-out infinite;
}

@keyframes request-tile-queue-pulse {
  0%, 100% { opacity: 0.5; transform: scale(1); }
  50% { opacity: 1; transform: scale(1.1); }
}

@keyframes request-tile-llm-spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}

@keyframes request-tile-retry-pulse {
  0%, 100% { opacity: 0.6; }
  50% { opacity: 1; }
}

@media (prefers-reduced-motion: reduce) {
  .request-tile__stage-indicator {
    animation: none;
  }
}
</style>
