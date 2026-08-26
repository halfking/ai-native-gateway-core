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
  const key = tile[props.groupBy as keyof RequestTile] as string
  return props.selectedLegends.has(key)
}

function isTileDimmed(tile: RequestTile): boolean {
  if (!props.selectedLegends || props.selectedLegends.size === 0) return false
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
    >
      <RequestTileComponent
        v-for="tile in visibleTiles"
        :key="`${tile.request_id}-${groupBy}`"
        :tile="tile"
        :group-by="groupBy"
        :mode="mode"
        :is-highlighted="isTileHighlighted(tile)"
        :is-dimmed="isTileDimmed(tile)"
        @click="handleTileClick"
      />
    </TransitionGroup>
  </div>
</template>

<style scoped>
.swim-lane-track {
  display: flex;
  overflow-x: hidden;
  min-width: 0;
}

.swim-lane-track__tiles {
  display: flex;
  gap: var(--tile-gap, 6px);
  flex-direction: row;
  min-width: 0;
}

.swim-lane-track__tiles--small {
  gap: var(--tile-gap, 4px);
}

/* 2026-07-26: Animation direction reversed. New tiles enter from RIGHT,
   old tiles exit to LEFT. Existing tiles shift left when new tile arrives. */
.swim-tile-enter-active {
  transition: all 0.3s ease;
}

.swim-tile-enter-from {
  opacity: 0;
  transform: translateX(20px); /* New tiles enter from RIGHT */
}

.swim-tile-leave-active {
  transition: all 0.3s ease;
  position: absolute;
}

.swim-tile-leave-to {
  opacity: 0;
  transform: translateX(-20px); /* Old tiles slide out LEFT */
}

/* Existing tiles shift LEFT when new tile arrives on RIGHT */
.swim-tile-move {
  transition: transform 0.3s ease;
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
