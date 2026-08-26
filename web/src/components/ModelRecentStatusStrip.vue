<script setup lang="ts">
/**
 * ModelRecentStatusStrip — 模型标题行右侧缩小状态色块条。
 * 颜色与按模型泳道小模式竖条一致（statusBarColor）。
 */
import { computed } from 'vue'
import type { LiveStreamTile } from '../composables/liveStreamStore'
import { statusBarColor, statusSemanticLabel } from '../composables/liveStreamDisplay'

const props = defineProps<{
  tiles: LiveStreamTile[]
}>()

const cells = computed(() =>
  (props.tiles || []).map(tile => ({
    key: tile.request_id || `${tile.timestamp}|${tile.status}`,
    color: statusBarColor(tile.status, tile.error_kind),
    title: [
      statusSemanticLabel(tile.status, tile.error_kind),
      tile.request_id ? tile.request_id.slice(0, 12) : '',
    ].filter(Boolean).join(' · '),
  })),
)
</script>

<template>
  <div
    v-if="cells.length"
    class="model-recent-status-strip"
    role="img"
    :aria-label="`最近 ${cells.length} 个请求状态`"
  >
    <span
      v-for="cell in cells"
      :key="cell.key"
      class="model-recent-status-strip__cell"
      :style="{ background: cell.color }"
      :title="cell.title"
    />
  </div>
</template>

<style scoped>
.model-recent-status-strip {
  display: flex;
  align-items: stretch;
  gap: 1px;
  margin-left: auto;
  flex: 1 1 auto;
  min-width: 0;
  max-width: 220px;
  height: 12px;
  overflow: hidden;
  justify-content: flex-end;
}
.model-recent-status-strip__cell {
  flex: 0 0 3px;
  width: 3px;
  min-width: 2px;
  border-radius: 1px;
  background: var(--muted);
}
</style>
