<script setup lang="ts">
// SwimLane.vue — 单条泳道组件
// 2026-07-05: 显示单条泳道及其请求色块

import { computed } from 'vue'
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

// 判断色块是否高亮
function isTileHighlighted(tileKey: string): boolean {
  if (props.selectedLegends.size === 0) return false
  return props.selectedLegends.has(tileKey)
}

// 判断色块是否暗化
function isTileDimmed(tileKey: string): boolean {
  if (props.selectedLegends.size === 0) return false
  return !props.selectedLegends.has(tileKey)
}

function handleTileClick(requestId: string) {
  emit('tileClick', requestId)
}
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
    <div class="swim-lane__track">
      <TransitionGroup name="swim-tile" tag="div" class="swim-lane__tiles">
        <RequestTile
          v-for="tile in lane.requests"
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
}

.swim-lane__label {
  flex: 0 0 120px;
  display: flex;
  flex-direction: column;
  justify-content: center;
  padding: 6px 10px;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  gap: 4px;
}

.swim-lane__name {
  font-size: 11px;
  font-weight: 600;
  color: var(--text, #e6edf3);
  line-height: 1.3;
  word-break: break-word;
}

.swim-lane__stats {
  display: flex;
  gap: 6px;
  font-size: 10px;
  font-variant-numeric: tabular-nums;
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
  flex: 1 1 auto;
  display: flex;
  align-items: center;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  padding: 4px 8px;
  overflow-x: auto;
  overflow-y: hidden;
}

.swim-lane__tiles {
  display: flex;
  gap: 6px;
  align-items: center;
  min-height: 60px;
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
