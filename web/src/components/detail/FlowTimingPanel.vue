<script setup lang="ts">
// FlowTimingPanel — shows each trace stage with duration_ms (— when unknown).
import { computed, ref, watch } from 'vue'
import { getRequestTrace, type RequestTrace, type TraceEvent } from '../../api/trace'

const props = defineProps<{ requestId: string | null }>()
const emit = defineEmits<{
  (e: 'goto', section: 'waterfall' | 'attempts'): void
}>()

const loading = ref(false)
const error = ref('')
const trace = ref<RequestTrace | null>(null)

watch(
  () => props.requestId,
  async (id) => {
    trace.value = null
    error.value = ''
    if (!id) return
    loading.value = true
    try {
      trace.value = await getRequestTrace(id)
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)

const events = computed(() => trace.value?.events ?? [])

function durationLabel(e: TraceEvent): string {
  if (typeof e.duration_ms !== 'number' || !Number.isFinite(e.duration_ms)) return '—'
  return `${e.duration_ms}ms`
}

function statusClass(status: string): string {
  if (status === 'failed' || status === 'timeout') return 'bad'
  if (status === 'skipped') return 'muted'
  return 'ok'
}
</script>

<template>
  <div class="flow-panel">
    <div v-if="loading" class="text-muted">加载流程…</div>
    <div v-else-if="error" class="err" role="alert">{{ error }}</div>
    <template v-else-if="trace">
      <div class="flow-summary">
        <span>总计 <strong>{{
          typeof trace.total_duration_ms === 'number' ? `${trace.total_duration_ms}ms` : '—'
        }}</strong></span>
        <span>状态 {{ trace.final_status || 'in_progress' }}</span>
        <span v-if="trace.failed_at_stage" class="bad">失败于 {{ trace.failed_at_stage }}</span>
        <span class="muted">来源 {{ trace.source }}</span>
        <button type="button" class="btn btn-sm link" @click="emit('goto', 'waterfall')">查看调度瀑布</button>
        <button type="button" class="btn btn-sm link" @click="emit('goto', 'attempts')">查看路由重试</button>
      </div>
      <ul class="flow-list">
        <li
          v-for="e in events"
          :key="e.seq"
          class="flow-row"
          :class="statusClass(e.status)"
        >
          <span class="stage">{{ e.stage_name || e.stage }}</span>
          <span class="status">{{ e.status }}</span>
          <span class="dur">{{ durationLabel(e) }}</span>
          <span v-if="e.error" class="err-snip">{{ e.error }}</span>
        </li>
      </ul>
      <p v-if="!events.length" class="text-muted">暂无链路事件</p>
    </template>
    <div v-else class="text-muted">无流程数据</div>
  </div>
</template>

<style scoped>
.flow-summary {
  display: flex; flex-wrap: wrap; gap: 12px;
  font-size: 12px; margin-bottom: 10px; color: var(--text-secondary);
}
.flow-list { list-style: none; margin: 0; padding: 0; }
.flow-row {
  display: grid;
  grid-template-columns: minmax(120px, 1.4fr) 72px 72px 1fr;
  gap: 8px; align-items: baseline;
  padding: 6px 8px; border-bottom: 1px solid var(--border);
  font-size: 12px;
}
.flow-row.bad .stage, .flow-row.bad .dur { color: var(--danger); }
.flow-row.muted { opacity: 0.65; }
.stage { font-weight: 600; }
.dur { font-variant-numeric: tabular-nums; text-align: right; font-weight: 600; }
.status { color: var(--muted); }
.err-snip { color: var(--danger); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.err { color: var(--danger); }
.muted, .text-muted { color: var(--muted); font-size: 12px; }
.link { margin-left: 4px; }
</style>
