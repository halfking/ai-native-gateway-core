<script setup lang="ts">
import { computed } from 'vue'
import type { RequestTile, SwimLaneMode, GroupByDimension } from '../types/swimlane'
import RequestTileComponent from './RequestTile.vue'

const props = defineProps<{
  tiles: RequestTile[]
  mode: SwimLaneMode
  groupBy: GroupByDimension
  maxVisible: number
  selectedLegends?: Set<string>
}>()

const emit = defineEmits<{
  tileClick: [requestId: string]
}>()

// 2026-07-26: Direction reversal. Tiles are now sorted ASC (oldest first) by
// liveStreamStore. Display newest on RIGHT: take the TAIL (last N tiles),
// render in order. When overflow, the oldest (leftmost) gets pushed out.
const visibleTiles = computed(() => {
  const max = props.maxVisible
  if (props.tiles.length <= max) return props.tiles
  // Take last N (newest)
  return props.tiles.slice(-max)
})

function isTileHighlighted(tile: RequestTile): boolean {
  if (!props.selectedLegends || props.selectedLegends.size === 0) return false
  // 2026-08-27: credential维度使用credential_id作为稳定lane key
  if (props.groupBy === 'credential') {
    const credentialKey = tile.credential_id ? String(tile.credential_id) : ''
    return credentialKey ? props.selectedLegends.has(credentialKey) : false
  }
  const key = tile[props.groupBy as keyof RequestTile] as string
  return props.selectedLegends.has(key)
}

function isTileDimmed(tile: RequestTile): boolean {
  if (!props.selectedLegends || props.selectedLegends.size === 0) return false
  // 2026-08-27: credential维度使用credential_id作为稳定lane key
  if (props.groupBy === 'credential') {
    const credentialKey = tile.credential_id ? String(tile.credential_id) : ''
    return credentialKey ? !props.selectedLegends.has(credentialKey) : false
  }
  const key = tile[props.groupBy as keyof RequestTile] as string
  return !props.selectedLegends.has(key)
}

function handleTileClick(requestId: string) {
  emit('tileClick', requestId)
}
</script>

<template>
  <div class="swim-lane-track">
    <TransitionGroup
      name="swim-tile"
      tag="div"
      class="swim-lane-track__tiles"
      :class="{ 'swim-lane-track__tiles--small': mode === 'small' }"
      move-class="swim-tile-move"
    >
      <RequestTileComponent
        v-for="tile in visibleTiles"
        :key="`${tile.request_id}-${groupBy}`"
        :tile="tile"
        :group-by="groupBy"
        :mode="mode"
        :is-highlighted="isTileHighlighted(tile)"
        :is-dimmed="isTileDimmed(tile)"
        :show-timeline-badge="false"
        @click="handleTileClick"
      />
    </TransitionGroup>
  </div>
</template>

<style scoped>
.swim-lane-track {
  display: flex;
  justify-content: flex-start; /* left-anchored: oldest on the left, new tiles grow to the right */
  overflow-x: hidden;
  min-width: 0;
  position: relative;
  width: 100%;
}

.swim-lane-track__tiles {
  display: flex;
  justify-content: flex-start;
  gap: var(--tile-gap, 6px);
  flex-direction: row;
  min-width: 0;
}

.swim-lane-track__tiles--small {
  gap: var(--tile-gap, 4px);
}

/* Right-anchored + no move/absolute-leave: prevents whole-lane shake on insert */
.swim-tile-enter-active,
.swim-tile-leave-active {
  transition: opacity 0.2s ease;
}

.swim-tile-enter-from,
.swim-tile-leave-to {
  opacity: 0;
}

/* Disable FLIP move; absolute leave was collapsing then restoring layout */
.swim-tile-move {
  transition: none;
}

@media (prefers-reduced-motion: reduce) {
  .swim-tile-enter-active,
  .swim-tile-leave-active {
    transition: opacity 0.1s linear;
  }
}
</style>
