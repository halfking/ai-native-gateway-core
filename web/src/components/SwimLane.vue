<script setup lang="ts">
// SwimLane.vue — 单条泳道组件
// 2026-07-05: 显示单条泳道及其请求色块
// 2026-07-07: 动态计算可显示的请求数，适配窗口宽度；标题折行
// 2026-07-07 v2: ResizeObserver 防抖 + 父容器 min-width:0 + 宽屏/窄屏分级

import { computed, ref, onMounted, onUnmounted, watch, nextTick } from 'vue'
import type { SwimLane as SwimLaneType, GroupByDimension } from '../types/swimlane'
import RequestTile from './RequestTile.vue'

const props = defineProps<{
  lane: SwimLaneType
  groupBy: GroupByDimension
  selectedLegends: Set<string>
}>()

const emit = defineEmits<{
  tileClick: [requestId: string]
}>()

// 泳道名称显示（不截断，允许折行）
const displayName = computed(() => {
  const name = props.lane.name
  const count = props.lane.stats.total
  return `${name} (${count})`
})

// 动态计算可显示的请求数
const trackRef = ref<HTMLElement | null>(null)
const trackWidth = ref(0)
const TILE_WIDTH = 80
const TILE_GAP = 6
const TRACK_PADDING = 16

// 落到 1 时也至少允许显示一个，避免空泳道
const MIN_VISIBLE_TILES = 1

const maxVisibleTiles = computed(() => {
  const total = props.lane.requests.length
  if (trackWidth.value <= 0) return total
  const availableWidth = Math.max(0, trackWidth.value - TRACK_PADDING)
  if (availableWidth <= 0) return MIN_VISIBLE_TILES
  // 第一个 tile 没有前 gap，最后一个有 padding；保守按 (n+gap) total 算
  const count = Math.floor((availableWidth + TILE_GAP) / (TILE_WIDTH + TILE_GAP))
  return Math.max(MIN_VISIBLE_TILES, Math.min(count, total))
})

// 只显示可容纳的请求（保留最新的若干个）
const visibleRequests = computed(() => {
  const requests = props.lane.requests
  const max = maxVisibleTiles.value
  if (requests.length <= max) return requests
  return requests.slice(requests.length - max)
})

function isTileHighlighted(tileKey: string): boolean {
  if (props.selectedLegends.size === 0) return false
  return props.selectedLegends.has(tileKey)
}

function isTileDimmed(tileKey: string): boolean {
  if (props.selectedLegends.size === 0) return false
  return !props.selectedLegends.has(tileKey)
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

// lane 数据变化（如新增请求）也再测一次，防止容器宽度的视口调整与请求密度共同变化
watch(
  () => props.lane.requests.length,
  () => measureTrack()
)
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
      </div>
    </div>
    <div class="swim-lane__track" ref="trackRef">
      <TransitionGroup name="swim-tile" tag="div" class="swim-lane__tiles">
        <RequestTile
          v-for="tile in visibleRequests"
          :key="tile.request_id"
          :tile="tile"
          :group-by="groupBy"
          :is-highlighted="isTileHighlighted(tile[groupBy] as string)"
          :is-dimmed="isTileDimmed(tile[groupBy] as string)"
          @click="handleTileClick"
        />
      </TransitionGroup>
    </div>
  </div>
</template>

<style scoped>
.swim-lane {
  display: flex;
  align-items: stretch;
  gap: 8px;
  min-height: 68px;
  padding: 4px 0;
  /* flex item 默认 min-width:auto 会阻止父容器内收缩，导致宽屏以下
   * 视口下 swim-lane 把父容器顶超出滚动；显式 0 让 ResizeObserver 拿到
   * 真实的可用宽度。 */
  min-width: 0;
  width: 100%;
  max-width: 100%;
  box-sizing: border-box;
}

.swim-lane__label {
  /* 固定宽度的左侧标题 — 已显式足够放常见原厂/供应商/模型名，长名折行。 */
  flex: 0 0 160px;
  min-width: 0;
  width: 160px;
  display: flex;
  flex-direction: column;
  justify-content: center;
  padding: 6px 10px;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  gap: 4px;
  overflow-wrap: break-word;
}

.swim-lane__name {
  font-size: 11px;
  font-weight: 600;
  color: var(--text, #e6edf3);
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
  color: var(--muted, #8b949e);
  font-weight: 500;
}

.swim-lane__stat--success {
  color: var(--success, #3fb950);
}

.swim-lane__stat--failure {
  color: var(--danger, #f85149);
}

.swim-lane__track {
  /* flex: 1 1 auto + min-width: 0 才是"占满剩余空间 + 可被压缩"的正确写法；
   * 缺 min-width:0 时，ResizeObserver 拿到的 clientWidth 会比预期大，
   * maxVisibleTiles 计算错误，色块超出滚动条 / 进入下一行。 */
  flex: 1 1 0;
  min-width: 0;
  width: auto;
  display: flex;
  align-items: center;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  padding: 4px 8px;
  overflow: hidden;
  /* 不要横向滚动 — 超出显示宽度的请求由 JS 截断显示最新的 N 个 */
  overflow-x: hidden;
  overflow-y: hidden;
}

.swim-lane__tiles {
  display: flex;
  gap: 6px;
  align-items: center;
  min-height: 60px;
  width: 100%;
  min-width: 0;
  /* 最新的请求贴右侧（视觉上像时间轴向右流动） */
  justify-content: flex-end;
  flex-wrap: nowrap;
}

/* 动画 */
.swim-tile-enter-active {
  transition:
    transform 0.5s cubic-bezier(0.18, 1.25, 0.32, 1.0),
    opacity 0.4s ease;
}

.swim-tile-enter-from {
  opacity: 0;
  transform: translateX(30px) scale(0.8);
}

.swim-tile-leave-active {
  transition: 
    transform 0.3s ease,
    opacity 0.3s ease;
  position: absolute;
}

.swim-tile-leave-to {
  opacity: 0;
  transform: translateX(-20px) scale(0.9);
}

.swim-tile-move {
  transition: transform 0.4s ease;
}

@media (prefers-reduced-motion: reduce) {
  .swim-tile-enter-active,
  .swim-tile-leave-active {
    transition: opacity 0.15s linear;
  }
  .swim-tile-enter-from,
  .swim-tile-leave-to {
    transform: none;
  }
  .swim-tile-move {
    transition: none;
  }
}
</style>
