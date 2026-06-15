<script setup lang="ts">
import { ref, watch } from 'vue'
import DecisionTimeline from './DecisionTimeline.vue'
import RadarCompare from './RadarCompare.vue'
import type { DecisionReplayL1, DecisionReplayL2 } from '../../api-autoroute'

const props = defineProps<{
  requestId: string
  l1?: DecisionReplayL1 | Record<string, unknown>
  l2?: DecisionReplayL2
  loading?: boolean
  compact?: boolean
}>()

const emit = defineEmits<{ close: [] }>()
</script>

<template>
  <div class="decision-detail" :class="{ compact }">
    <div v-if="!compact" class="detail-head">
      <span class="detail-title">决策回放</span>
      <code class="req-id">{{ requestId }}</code>
      <button class="btn btn-ghost btn-sm" @click="emit('close')">关闭</button>
    </div>
    <div v-if="loading" class="text-muted">加载 L2 回放…</div>
    <template v-else>
      <DecisionTimeline compact :l1="l1" :l2="l2" />
      <RadarCompare
        v-if="(l1 as DecisionReplayL1)?.candidates_top3?.length"
        :candidates="(l1 as DecisionReplayL1).candidates_top3 ?? []"
        :size="compact ? 160 : 180"
      />
    </template>
  </div>
</template>

<style scoped>
.decision-detail {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.detail-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
}
.detail-title { font-size: 12px; font-weight: 600; }
.req-id { font-size: 9px; color: var(--muted); flex: 1; overflow: hidden; text-overflow: ellipsis; }
.text-muted { font-size: 11px; color: var(--muted); }
</style>
