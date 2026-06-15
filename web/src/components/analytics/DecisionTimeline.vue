<script setup lang="ts">
import { computed } from 'vue'
import type { DecisionReplayL1, DecisionReplayL2 } from '../../api-autoroute'

const props = defineProps<{
  l1?: DecisionReplayL1 | null
  l2?: DecisionReplayL2 | null
  compact?: boolean
}>()

const l1Steps = computed(() => {
  const d = props.l1
  if (!d) return []
  return [
    { key: 'classify', label: '任务分类', value: d.task_type || '-', detail: d.classifier },
    { key: 'score', label: '6维评分', value: d.chosen_model || '-', detail: d.profile },
    { key: 'pick', label: '选模型', value: d.chosen_model || '-', detail: d.confidence != null ? `${(d.confidence * 100).toFixed(0)}%` : '' },
  ]
})

const l2Steps = computed(() => {
  const d = props.l2
  if (!d) return []
  const trace = d.decision_trace as Record<string, unknown> | undefined
  const planned = Array.isArray(trace?.planned_candidates) ? trace!.planned_candidates.length : 0
  const blocked = Array.isArray(trace?.blocked_candidates) ? trace!.blocked_candidates.length : 0
  return [
    { key: 'resolve', label: '模型解析', value: d.resolution_path || d.canonical_model || '-', detail: '' },
    { key: 'plan', label: '候选计划', value: String(planned || d.candidates_tried || '-'), detail: blocked ? `${blocked} 阻断` : '' },
    { key: 'exec', label: '执行凭据', value: d.chosen_credential_id != null ? `#${d.chosen_credential_id}` : '-', detail: d.success ? '成功' : '失败' },
  ]
})

const hasL2 = computed(() => props.l2 && Object.keys(props.l2).length > 0)
</script>

<template>
  <div class="timeline" :class="{ compact }">
    <div class="tl-layer l1">
      <span class="layer-tag l1">L1</span>
      <div class="tl-steps">
        <template v-for="(s, i) in l1Steps" :key="s.key">
          <div class="tl-step" :class="{ active: i === l1Steps.length - 1 }">
            <span class="tl-label">{{ s.label }}</span>
            <span class="tl-value">{{ s.value }}</span>
            <span v-if="s.detail" class="tl-detail">{{ s.detail }}</span>
          </div>
          <span v-if="i < l1Steps.length - 1" class="tl-arrow">→</span>
        </template>
      </div>
    </div>
    <div class="tl-bridge">↓</div>
    <div class="tl-layer l2">
      <span class="layer-tag l2">L2</span>
      <div v-if="hasL2" class="tl-steps">
        <template v-for="(s, i) in l2Steps" :key="s.key">
          <div class="tl-step" :class="{ active: i === l2Steps.length - 1 }">
            <span class="tl-label">{{ s.label }}</span>
            <span class="tl-value">{{ s.value }}</span>
            <span v-if="s.detail" class="tl-detail">{{ s.detail }}</span>
          </div>
          <span v-if="i < l2Steps.length - 1" class="tl-arrow">→</span>
        </template>
      </div>
      <div v-else class="tl-empty text-muted">暂无 L2 决策日志（routing_decision_log）</div>
    </div>
  </div>
</template>

<style scoped>
.timeline {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 6px 0;
}
.timeline.compact { gap: 2px; padding: 4px 0; }
.tl-layer {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  flex-wrap: wrap;
}
.tl-steps {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px;
  flex: 1;
}
.tl-step {
  display: flex;
  flex-direction: column;
  gap: 1px;
  padding: 4px 8px;
  border-radius: 4px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  min-width: 72px;
}
.tl-step.active {
  border-color: color-mix(in srgb, var(--accent) 40%, var(--border));
  background: color-mix(in srgb, var(--accent) 6%, var(--bg-subtle));
}
.tl-label { font-size: 9px; color: var(--muted); }
.tl-value { font-size: 11px; font-weight: 600; }
.tl-detail { font-size: 9px; color: var(--muted); }
.tl-arrow { font-size: 10px; color: var(--muted); }
.tl-bridge { text-align: center; font-size: 10px; color: var(--muted); line-height: 1; }
.tl-empty { font-size: 10px; padding: 4px 0; }
.layer-tag {
  font-size: 9px;
  font-weight: 700;
  padding: 2px 5px;
  border-radius: 3px;
  flex-shrink: 0;
}
.layer-tag.l1 { color: var(--accent-h); background: color-mix(in srgb, var(--accent) 12%, transparent); }
.layer-tag.l2 { color: var(--success); background: color-mix(in srgb, var(--success) 12%, transparent); }
</style>
