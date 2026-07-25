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

// Display first N tiles (backend sends newest first)
const visibleTiles = computed(() => {
  const max = props.maxVisible
  if (props.tiles.length <= max) return props.tiles
  return props.tiles.slice(0, max)
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

/* Animation: new tiles slide in from LEFT */
.swim-tile-enter-active {
  transition: all 0.3s ease;
}

.swim-tile-enter-from {
  opacity: 0;
  transform: translateX(-20px);
}

.swim-tile-leave-active {
  transition: all 0.3s ease;
  position: absolute;
}

.swim-tile-leave-to {
  opacity: 0;
  transform: translateX(20px);
}

/* Existing tiles shift RIGHT when new tile arrives on LEFT */
.swim-tile-move {
  transition: transform 0.3s ease;
}
</style>
