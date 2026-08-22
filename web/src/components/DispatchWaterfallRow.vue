<script setup lang="ts">
import type { LaidOutRow } from '../utils/waterfallTimeline'

const props = defineProps<{
  row: LaidOutRow
  selected?: boolean
}>()

const emit = defineEmits<{
  select: []
}>()

function shortId(id: string): string {
  return id.length > 12 ? `${id.slice(0, 10)}…` : id
}

function resultClass(result: string): string {
  if (result === 'success') return 'ok'
  if (result.includes('fail') || result === 'error') return 'bad'
  return 'muted'
}
</script>

<template>
  <button
    class="qwt-row"
    type="button"
    :class="{ selected: props.selected }"
    data-testid="qwt-row"
    @click="emit('select')"
  >
    <div class="id">
      <span class="model">{{ row.request.model || '—' }}</span>
      <code>{{ shortId(row.request.request_id) }}</code>
    </div>
    <span class="result" :class="resultClass(row.request.result)">{{ row.request.result }}</span>
    <span class="num">{{ row.request.total_ms }}ms</span>
    <div class="track" data-testid="qwt-track">
      <div
        v-for="b in row.bars"
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
    <span class="num muted">{{ row.request.queue_wait_ms }}ms</span>
    <span class="num muted">{{ row.request.upstream_latency_ms }}ms</span>
  </button>
</template>

<style scoped>
.qwt-row {
  display: grid;
  grid-template-columns: minmax(140px, 180px) 78px 72px minmax(220px, 1fr) 72px 72px;
  gap: 8px;
  align-items: center;
  width: 100%;
  border: 0;
  border-bottom: 1px solid var(--kx-border);
  background: transparent;
  color: var(--kx-text);
  text-align: left;
  padding: 6px 8px;
  cursor: pointer;
  font: inherit;
}
.qwt-row:hover { background: var(--kx-bg-accent); }
.qwt-row.selected { background: var(--kx-primary-soft); }
.id {
  display: flex;
  flex-direction: column;
  min-width: 0;
  gap: 1px;
}
.model {
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 11px;
  color: var(--kx-muted);
}
.result {
  font-size: 11px;
  text-transform: lowercase;
  padding: 1px 6px;
  border-radius: 999px;
  border: 1px solid var(--kx-border);
  justify-self: start;
}
.result.ok {
  color: var(--kx-success);
  border-color: color-mix(in srgb, var(--kx-success) 40%, var(--kx-border));
  background: var(--kx-success-soft);
}
.result.bad {
  color: var(--kx-danger);
  border-color: color-mix(in srgb, var(--kx-danger) 40%, var(--kx-border));
  background: var(--kx-danger-soft);
}
.result.muted { color: var(--kx-muted); }
.num {
  font-variant-numeric: tabular-nums;
  font-size: 12px;
  text-align: right;
}
.num.muted { color: var(--kx-muted); }
.track {
  position: relative;
  height: 16px;
  background: var(--kx-bg);
  border-radius: 4px;
  overflow: hidden;
}
.seg {
  position: absolute;
  top: 3px;
  height: 10px;
  border-radius: 2px;
  min-width: 2px;
}
</style>
