<script setup lang="ts">
// RequestTile.vue — 请求色块组件（80x60）
// 2026-07-13 v5: 现代观测面板风格 — 玻璃质感卡片 + 左侧色带 + 状态圆点
// 2026-07-14: 探测/idle tile 显示明确的错误原因（不再静默 "[空闲]"）

import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RequestTile as RequestTileType, GroupByDimension } from '../types/swimlane'
import {
  VENDOR_COLORS,
  calculateFontSize,
  truncateText,
} from '../types/swimlane'
import { errorKindLabel } from '../composables/liveStreamDisplay'

const { t, locale } = useI18n()

const props = defineProps<{
  tile: RequestTileType
  groupBy: GroupByDimension
  isHighlighted: boolean
  isDimmed: boolean
}>()

const emit = defineEmits<{
  click: [requestId: string]
}>()

const accentColor = computed(() => {
  return VENDOR_COLORS[props.tile.vendor] || VENDOR_COLORS['__unknown__']
})

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
</script>

<template>
  <div
    class="request-tile"
    :class="{
      'request-tile--highlighted': isHighlighted,
      'request-tile--dimmed': isDimmed,
      'request-tile--probe': tile.is_probe,
      'request-tile--idle': isIdle,
      'request-tile--in-progress': isInProgress,
      'request-tile--failure': isFailure,
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
    </div>
  </div>
</template>

<style scoped>
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
  border-color: color-mix(in srgb, #ef4444 45%, var(--accent-color));
}

.request-tile--probe {
  border-color: color-mix(in srgb, #38bdf8 50%, var(--accent-color));
  /* 2026-07-14: 探测请求特殊背景 — 青色玻璃质感，与正常业务请求一眼区分 */
  background:
    linear-gradient(
      145deg,
      color-mix(in srgb, #38bdf8 22%, var(--kx-surface)) 0%,
      color-mix(in srgb, #0ea5e9 10%, var(--kx-bg)) 100%
    );
  box-shadow:
    inset 0 1px 0 color-mix(in srgb, #38bdf8 12%, transparent),
    0 0 0 1px color-mix(in srgb, #38bdf8 20%, transparent),
    0 1px 3px color-mix(in srgb, var(--text) 12%, transparent);
}

.request-tile--probe.request-tile--failure {
  border-color: color-mix(in srgb, #38bdf8 35%, #ef4444 45%);
  background:
    linear-gradient(
      145deg,
      color-mix(in srgb, #38bdf8 14%, color-mix(in srgb, #ef4444 18%, var(--kx-surface))) 0%,
      color-mix(in srgb, #0ea5e9 7%, var(--kx-bg)) 100%
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
  background: linear-gradient(180deg, #ef4444, var(--accent-color));
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
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--text) 15%, transparent), 0 0 0 0 rgba(59, 130, 246, 0.5);
  }
  50% {
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--text) 15%, transparent), 0 0 0 4px rgba(59, 130, 246, 0.25);
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
  color: #0c1a26;
  background: linear-gradient(180deg, #7dd3fc 0%, #38bdf8 100%);
  border: 1.5px solid #0284c7;
  box-shadow: 0 0 5px rgba(56, 189, 248, 0.8);
  z-index: 3;
}

.request-tile__probe-icon {
  width: 10px;
  height: 10px;
  display: block;
}

/* 按探测来源区分颜色：scheduled=橙黄/周期，direct=红色/主动 */
.request-tile__probe-badge--direct {
  background: linear-gradient(180deg, #fca5a5 0%, #ef4444 100%);
  border-color: #b91c1c;
  color: #fff;
}

.request-tile__probe-badge--scheduled {
  background: linear-gradient(180deg, #fde68a 0%, #fbbf24 100%);
  border-color: #f59e0b;
  color: #422006;
}

.request-tile__probe-badge--gateway {
  background: linear-gradient(180deg, #93c5fd 0%, #3b82f6 100%);
  border-color: #1d4ed8;
  color: #fff;
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
  color: rgba(248, 113, 113, 0.95); /* default = failure red */
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  letter-spacing: 0.01em;
  margin-top: 1px;
}
.request-tile__reason--idle {
  color: rgba(156, 163, 175, 0.95);
  font-weight: 500;
}
.request-tile__reason--probe {
  color: rgba(56, 189, 248, 0.95);
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
</style>
