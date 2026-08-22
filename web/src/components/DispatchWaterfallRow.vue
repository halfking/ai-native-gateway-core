<script setup lang="ts">
import type { LaidOutRow } from '../utils/waterfallTimeline'
import DispatchWaterfallTrack from './DispatchWaterfallTrack.vue'

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
      <span class="metrics">
        queue {{ row.request.queue_wait_ms }}ms · ttfb {{ row.request.upstream_latency_ms }}ms
      </span>
    </div>
    <span class="result" :class="resultClass(row.request.result)">{{ row.request.result }}</span>
    <DispatchWaterfallTrack :bars="row.bars" />
    <span class="num">{{ row.request.total_ms }}ms</span>
  </button>
</template>

<style scoped>
.qwt-row {
  display: grid;
  grid-template-columns: var(--qwt-grid-cols, minmax(140px, 200px) 78px minmax(240px, 1fr) 72px);
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
.metrics {
  font-size: 10px;
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
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
</style>
