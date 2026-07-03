<script setup lang="ts">
// LiveRequestStream — swim-lane container.
//
// 2026-07-03 v7: dynamic tile width — the swim lane now stretches
// each tile to fill the track, clamped to [64, 130] px. The
// previous hard-coded 60px wasted most of a 1920px dashboard and
// truncated vendor/provider labels to 2-3 characters.
//
// 2026-07-03 v7: failure detail — when a request is in failure
// state, the tile replaces its model line with the coarse
// error_kind label (e.g. "5xx" / "timeout" / "disc" / "rate") and
// demotes the model short label to line 3. The operator can now
// read the failure mode WITHOUT hovering. The translucent
// failure-family background (red / amber / yellow / purple) groups
// failures visually.

import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useLiveStream } from '../composables/useLiveStream'
import { useElementSize } from '../composables/useElementSize'
import { statusCategory, type StatusCategory } from '../composables/liveStreamDisplay'
import LiveRequestBlock from './LiveRequestBlock.vue'
import LiveStreamLegend from './LiveStreamLegend.vue'

const emit = defineEmits<{
  /** Bubble a tile click up so DashboardView can open the detail drawer. */
  openDetail: [requestId: string]
}>()

const { t } = useI18n()
const { requests, connection, paused, togglePause } = useLiveStream()

type StatusFilter = 'all' | 'success' | 'in_progress' | StatusCategory
const statusFilter = ref<StatusFilter>('all')

const filteredRequests = computed(() => {
  if (statusFilter.value === 'all') return requests.value
  return requests.value.filter((r) => {
    if (r.type === 'idle_marker') return true
    if (statusFilter.value === 'success' || statusFilter.value === 'in_progress') {
      return r.status === statusFilter.value
    }
    return statusCategory(r.status, r.error_kind ?? null) === statusFilter.value
  })
})

// --- Dynamic tile sizing (2026-07-03 v7) ----------------------------

const MIN_TILE_WIDTH = 64
const MAX_TILE_WIDTH = 130
const TILE_GAP = 4
const IDLE_WIDTH = 120
const ENTRANCE_RESERVE_PX = 24
const IDLE_RESERVE_PX = IDLE_WIDTH + TILE_GAP
const RESERVE_PX = ENTRANCE_RESERVE_PX + IDLE_RESERVE_PX

const trackRef = ref<HTMLElement | null>(null)
const { width: trackWidth } = useElementSize(trackRef)

const realTileCount = computed(() =>
  filteredRequests.value.filter((r) => r.type !== 'idle_marker').length,
)

const tileWidth = computed(() => {
  if (trackWidth.value <= 0 || realTileCount.value === 0) {
    return MIN_TILE_WIDTH
  }
  const usable = Math.max(0, trackWidth.value - RESERVE_PX)
  if (usable <= 0) return MIN_TILE_WIDTH
  const ideal = (usable - TILE_GAP) / realTileCount.value
  return Math.max(
    MIN_TILE_WIDTH,
    Math.min(MAX_TILE_WIDTH, Math.floor(ideal)),
  )
})

const visibleTileCount = computed(() => {
  if (trackWidth.value <= 0 || realTileCount.value === 0) return 0
  const usable = Math.max(0, trackWidth.value - RESERVE_PX)
  if (usable <= 0) return Math.min(realTileCount.value, 1)
  const capacity = Math.floor((usable + TILE_GAP) / (tileWidth.value + TILE_GAP))
  return Math.min(realTileCount.value, Math.max(1, capacity))
})

const visibleRequests = computed(() => {
  const all = filteredRequests.value
  const reals = all.filter((r) => r.type !== 'idle_marker')
  const idles = all.filter((r) => r.type === 'idle_marker')
  if (visibleTileCount.value >= reals.length) {
    return all
  }
  const take = reals.slice(reals.length - visibleTileCount.value)
  return [...idles, ...take]
})

const connectionLabel = computed(() => {
  if (connection.value === 'open') return t('dashboard.liveStream.connected')
  if (connection.value === 'connecting') return t('dashboard.liveStream.connecting')
  if (connection.value === 'reconnecting') return t('dashboard.liveStream.reconnecting')
  if (connection.value === 'unsupported') return t('dashboard.liveStream.unsupported')
  return t('dashboard.liveStream.disconnected')
})

function onSelect(requestId: string) {
  emit('openDetail', requestId)
}
</script>

<template>
  <div class="live-stream card">
    <div class="live-stream__header">
      <h3 class="live-stream__title">{{ t('dashboard.liveStream.title') }}</h3>
      <div class="live-stream__controls">
        <span
          class="live-stream__status"
          :class="{
            'live-stream__status--ok': connection === 'open',
            'live-stream__status--warn': connection !== 'open',
          }"
        >
          <span class="live-stream__dot" aria-hidden="true" />
          {{ connectionLabel }}
        </span>
        <button
          type="button"
          class="live-stream__btn"
          @click="togglePause"
        >
          {{ paused ? t('dashboard.liveStream.resume') : t('dashboard.liveStream.pause') }}
        </button>
        <select v-model="statusFilter" class="live-stream__select">
          <option value="all">{{ t('dashboard.liveStream.filterAll') }}</option>
          <option value="success">{{ t('dashboard.liveStream.filterSuccess') }}</option>
          <option value="in_progress">{{ t('dashboard.liveStream.filterInProgress') }}</option>
          <optgroup :label="t('dashboard.liveStream.filterGroupFailures')">
            <option value="failure_5xx">{{ t('dashboard.liveStream.filterFailure5xx') }}</option>
            <option value="failure_4xx">{{ t('dashboard.liveStream.filterFailure4xx') }}</option>
            <option value="failure_timeout">{{ t('dashboard.liveStream.filterFailureTimeout') }}</option>
            <option value="failure_not_found">{{ t('dashboard.liveStream.filterFailureNotFound') }}</option>
            <option value="failure_other">{{ t('dashboard.liveStream.filterFailureOther') }}</option>
          </optgroup>
        </select>
        <span
          class="live-stream__count"
          :title="t('dashboard.liveStream.countTooltip', { buffer: realTileCount, visible: visibleTileCount })"
          :aria-label="t('dashboard.liveStream.countAria', { buffer: realTileCount, visible: visibleTileCount })"
        >
          <span class="live-stream__count-num">{{ realTileCount }}</span>
          <span class="live-stream__count-sep">/</span>
          <span class="live-stream__count-num">{{ visibleTileCount }}</span>
        </span>
      </div>
    </div>

    <div ref="trackRef" class="live-stream__track">
      <TransitionGroup name="live-slide" tag="div" class="live-stream__track-inner">
        <LiveRequestBlock
          v-for="(req, idx) in visibleRequests"
          :key="req.request_id ?? `idle-${idx}-${req.ts}`"
          :request="req"
          :width="tileWidth"
          @select="onSelect"
        />
      </TransitionGroup>
      <div v-if="realTileCount === 0" class="live-stream__empty">
        {{ t('dashboard.liveStream.empty') }}
      </div>
    </div>

    <LiveStreamLegend />
  </div>
</template>

<style scoped>
.live-stream {
  border: 1px solid var(--border, #30363d);
  border-radius: var(--radius, 8px);
  background: var(--card, #1c2128);
  padding: 12px 16px 10px;
  margin-bottom: 20px;
}

.live-stream__header {
  display: flex;
  align-items: center;
  flex-wrap: nowrap;
  gap: 10px;
  margin-bottom: 10px;
  min-width: 0;
}

.live-stream__title {
  font-size: 14px;
  font-weight: 600;
  margin: 0;
  color: var(--text, #e6edf3);
  flex-shrink: 0;
  white-space: nowrap;
}

.live-stream__controls {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1 1 auto;
  min-width: 0;
  justify-content: flex-end;
  flex-wrap: nowrap;
  overflow: hidden;
}

.live-stream__status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--muted, #8b949e);
  flex-shrink: 0;
  white-space: nowrap;
}
.live-stream__dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--muted, #8b949e);
  flex-shrink: 0;
}
.live-stream__status--ok .live-stream__dot {
  background: var(--success, #3fb950);
  box-shadow: 0 0 0 3px rgba(63, 185, 80, 0.18);
}
.live-stream__status--warn .live-stream__dot {
  background: var(--warning, #d29922);
  animation: live-dot-pulse 1.4s ease-in-out infinite;
}
@keyframes live-dot-pulse {
  0%, 100% { opacity: 1; }
  50%      { opacity: 0.4; }
}

.live-stream__btn {
  font-size: 12px;
  padding: 4px 10px;
  border-radius: 4px;
  border: 1px solid var(--border, #30363d);
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  cursor: pointer;
  flex-shrink: 0;
  white-space: nowrap;
  min-width: 64px;
}
.live-stream__btn:hover {
  background: var(--bg-subtle, #161b22);
  border-color: var(--accent, #6366f1);
}

.live-stream__select {
  font-size: 12px;
  padding: 4px 10px;
  border-radius: 4px;
  border: 1px solid var(--border, #30363d);
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  cursor: pointer;
  flex: 0 1 auto;
  min-width: 120px;
  max-width: 220px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.live-stream__select option {
  background: var(--card, #1c2128);
  color: var(--text, #e6edf3);
}

.live-stream__count {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  white-space: nowrap;
  flex-shrink: 0;
  font-size: 12px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  color: var(--muted, #8b949e);
  padding: 4px 10px;
  border: 1px solid var(--border, #30363d);
  border-radius: 4px;
  background: var(--bg, #0f1117);
  font-variant-numeric: tabular-nums;
  min-width: 78px;
  justify-content: center;
}
.live-stream__count-num {
  color: var(--text, #e6edf3);
  font-weight: 600;
}
.live-stream__count-sep {
  color: var(--border, #30363d);
}

.live-stream__track {
  position: relative;
  height: 80px;
  overflow: hidden;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  padding: 6px 8px;
}

.live-stream__track-inner {
  display: flex;
  align-items: center;
  gap: 4px;
  height: 100%;
  min-height: 60px;
  justify-content: flex-end;
}

.live-stream__empty {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--muted, #8b949e);
  font-size: 12px;
  pointer-events: none;
}

.live-slide-enter-active {
  transition:
    transform 0.45s cubic-bezier(0.18, 1.25, 0.32, 1.0),
    opacity 0.35s ease;
}
.live-slide-enter-from {
  opacity: 0;
  transform: translateX(40px);
}
.live-slide-leave-active {
  transition: transform 0.3s ease, opacity 0.3s ease;
  position: absolute;
}
.live-slide-leave-to {
  opacity: 0;
  transform: translateX(-20px);
}

@media (prefers-reduced-motion: reduce) {
  .live-slide-enter-active,
  .live-slide-leave-active {
    transition: opacity 0.15s linear;
  }
  .live-slide-enter-from,
  .live-slide-leave-to {
    transform: none;
  }
  .live-stream__status--warn .live-stream__dot {
    animation: none;
  }
}
</style>
