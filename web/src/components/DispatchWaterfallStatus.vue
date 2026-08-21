<script setup lang="ts">
import { computed } from 'vue'
import type { DispatchQueuesSnapshot, WaterfallSnapshot } from '../api/dispatch'

const props = defineProps<{
  snap: WaterfallSnapshot | null
  queues: DispatchQueuesSnapshot | null
  sourceLabel: string
}>()

const diagnosis = computed(() => props.snap?.bottleneck_diagnosis)
const diagnosisTone = computed(() => {
  const b = diagnosis.value?.bottleneck
  if (!b || b === 'none') return 'ok'
  if (b === 'routing') return 'warn'
  return 'danger'
})

const modelDepth = computed(() =>
  props.queues?.models?.reduce((s, m) => s + (m.depth || 0), 0) ?? '—',
)
const credDepth = computed(() =>
  props.queues?.credentials?.reduce((s, c) => s + (c.depth || 0), 0) ?? '—',
)
</script>

<template>
  <section class="dw-status">
    <div class="card">
      <div class="k">Pipeline</div>
      <div class="v">
        <span :class="['pill', snap?.wired ? 'ok' : 'muted']">{{ snap?.wired ? 'wired' : 'not wired' }}</span>
        <span :class="['pill', snap?.enabled ? 'ok' : 'muted']">{{ snap?.enabled ? 'enabled' : 'disabled' }}</span>
      </div>
    </div>
    <div class="card">
      <div class="k">样本来源</div>
      <div class="v num" style="font-size:14px">{{ sourceLabel }}</div>
    </div>
    <div class="card">
      <div class="k">样本数</div>
      <div class="v num">{{ snap?.requests?.length ?? 0 }}</div>
    </div>
    <div class="card">
      <div class="k">模型队列</div>
      <div class="v num">{{ modelDepth }}</div>
    </div>
    <div class="card grow" :class="'tone-' + diagnosisTone">
      <div class="k">瓶颈诊断 · 凭据队列深度 {{ credDepth }}</div>
      <div class="v diag">
        <strong>{{ diagnosis?.bottleneck || 'none' }}</strong>
        <span>{{ diagnosis?.message || '—' }}</span>
        <em v-if="diagnosis?.suggestion">{{ diagnosis?.suggestion }}</em>
      </div>
    </div>
  </section>
</template>

<style scoped>
.dw-status {
  display: grid;
  grid-template-columns: repeat(4, minmax(100px, 1fr)) 2fr;
  gap: 10px;
  margin-bottom: 14px;
}
@media (max-width: 1100px) {
  .dw-status { grid-template-columns: repeat(2, 1fr); }
}
.card {
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  padding: 10px 12px;
}
.card .k {
  font-size: 11px;
  color: var(--kx-muted);
  margin-bottom: 6px;
}
.card .v { font-size: 14px; }
.card .num { font-size: 20px; font-weight: 600; }
.card .diag {
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 13px;
}
.card .diag em {
  font-style: normal;
  color: var(--kx-muted);
  font-size: 12px;
}
.card.tone-ok { border-color: color-mix(in srgb, var(--kx-success) 40%, var(--kx-border)); }
.card.tone-warn { border-color: color-mix(in srgb, var(--kx-warning) 50%, var(--kx-border)); }
.card.tone-danger { border-color: color-mix(in srgb, var(--kx-danger) 50%, var(--kx-border)); }
.pill {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 11px;
  margin-right: 6px;
  background: var(--kx-bg);
  color: var(--kx-muted);
  border: 1px solid var(--kx-border);
}
.pill.ok {
  color: var(--kx-success);
  border-color: color-mix(in srgb, var(--kx-success) 40%, var(--kx-border));
  background: var(--kx-success-soft);
}
</style>
