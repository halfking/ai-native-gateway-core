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
    <div class="chips">
      <span :class="['pill', snap?.wired ? 'ok' : 'muted']">{{ snap?.wired ? 'wired' : 'not wired' }}</span>
      <span :class="['pill', snap?.enabled ? 'ok' : 'muted']">{{ snap?.enabled ? 'enabled' : 'disabled' }}</span>
      <span class="chip">来源 {{ sourceLabel }}</span>
      <span class="chip">样本 {{ snap?.requests?.length ?? 0 }}</span>
      <span class="chip">模型队列 {{ modelDepth }}</span>
      <span class="chip">凭据队列 {{ credDepth }}</span>
      <span :class="['chip', 'tone-' + diagnosisTone]">
        瓶颈 {{ diagnosis?.bottleneck || 'none' }}
      </span>
    </div>
    <p v-if="diagnosis?.message && diagnosis.message !== '—'" class="msg">
      {{ diagnosis.message }}
      <em v-if="diagnosis.suggestion">{{ diagnosis.suggestion }}</em>
    </p>
  </section>
</template>

<style scoped>
.dw-status { margin-bottom: 14px; }
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}
/* .chip / .pill / .chip--ok / .chip--warn / .chip--danger 从全局 styles/pill-chip.css 继承（P1-8）。
 * 注意：本组件模板使用 .chip.tone-* 类名（而非 .chip--*）。保留 .tone-* 兼容修饰符。 */
.chip.tone-ok { border-color: color-mix(in srgb, var(--kx-success) 40%, var(--kx-border)); }
.chip.tone-warn { border-color: color-mix(in srgb, var(--kx-warning) 50%, var(--kx-border)); }
.chip.tone-danger {
  border-color: color-mix(in srgb, var(--kx-danger) 50%, var(--kx-border));
  color: var(--kx-danger);
}
.msg {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--kx-muted);
}
.msg em { font-style: normal; margin-left: 8px; }
</style>
