<script setup lang="ts">
import type { LaidOutBar } from '../utils/waterfallTimeline'

defineProps<{
  bars: LaidOutBar[]
  tall?: boolean
}>()
</script>

<template>
  <div class="dwf-track" :class="{ tall }" data-testid="qwt-track">
    <div class="dwf-track-grid" aria-hidden="true">
      <span v-for="pct in [25, 50, 75]" :key="pct" :style="{ left: pct + '%' }" />
    </div>
    <div
      v-for="b in bars"
      :key="b.key + String(b.start)"
      class="seg"
      data-testid="qwt-seg"
      :data-stage="b.key"
      :title="`${b.label}: ${b.ms} ms${b.synthesized ? ' · 合成' : ''}`"
      :style="{
        left: b.leftPct + '%',
        width: b.widthPct + '%',
        background: b.color,
      }"
    />
  </div>
</template>

<style scoped>
.dwf-track {
  position: relative;
  height: 16px;
  background: var(--kx-bg);
  border-radius: 4px;
  overflow: hidden;
}
.dwf-track.tall {
  height: 24px;
}
.dwf-track-grid {
  position: absolute;
  inset: 0;
  pointer-events: none;
}
.dwf-track-grid span {
  position: absolute;
  top: 0;
  bottom: 0;
  width: 1px;
  background: color-mix(in srgb, var(--kx-border) 70%, transparent);
  transform: translateX(-50%);
}
.seg {
  position: absolute;
  top: 3px;
  height: calc(100% - 6px);
  border-radius: 2px;
  min-width: 2px;
}
.dwf-track.tall .seg {
  top: 4px;
  height: calc(100% - 8px);
}
</style>
