<script setup lang="ts">
/**
 * 2026-07-28: One-line integrity status chip for the model-routing
 * dashboard. Hits /api/admin/model-integrity/summary on mount and
 * shows a single dot with the unresolved count. Clicking navigates
 * to the dedicated dashboard (the parent decides the route — we
 * emit 'open').
 *
 * Design constraints (per plan):
 *  - Stay a single component (1-line UI) — no new page, no new
 *    charts, no SCSS.
 *  - No external state (Vuex/Pinia). Self-contained fetch + emit.
 *  - Graceful failure: any error is silently swallowed (chip goes
 *    gray) so a missing admin permission never breaks the page.
 */
import { onMounted, ref } from 'vue'
import { getModelIntegritySummary, type ModelIntegritySummary } from '../api/integrity'

const props = defineProps<{
  hours?: number
}>()
const emit = defineEmits<{
  (e: 'open'): void
}>()

const unresolved = ref(0)
const total = ref(0)
const critical = ref(0)
const loading = ref(false)
const errored = ref(false)

async function refresh() {
  loading.value = true
  errored.value = false
  try {
    const r = await getModelIntegritySummary(props.hours ?? 24)
    let u = 0
    let t = 0
    let c = 0
    for (const s of (r.summaries ?? []) as ModelIntegritySummary[]) {
      t += s.anomaly_count
      u += Math.max(s.anomaly_count - s.resolved_count, 0)
      if (s.severity === 'critical' || s.severity === 'high') {
        c += Math.max(s.anomaly_count - s.resolved_count, 0)
      }
    }
    unresolved.value = u
    total.value = t
    critical.value = c
  } catch {
    errored.value = true
  } finally {
    loading.value = false
  }
}

onMounted(refresh)

function onClick() {
  emit('open')
}

function color() {
  if (errored.value) return 'grey'
  if (critical.value > 0) return 'red'
  if (unresolved.value > 0) return 'orange'
  return 'green'
}
</script>

<template>
  <span
    class="integrity-chip"
    :class="`integrity-chip--${color()}`"
    :title="
      errored
        ? 'integrity: cannot load'
        : `${unresolved} unresolved / ${total} in last ${props.hours ?? 24}h (${critical} high+critical)`
    "
    @click="onClick"
  >
    <span class="dot" />
    <span class="label">Integrity</span>
    <span class="count">{{ unresolved }}</span>
  </span>
</template>

<style scoped>
.integrity-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 2px 10px;
  border-radius: 12px;
  font-size: 12px;
  cursor: pointer;
  user-select: none;
  border: 1px solid currentColor;
  background: transparent;
}
.integrity-chip--green { color: var(--success); }
.integrity-chip--orange { color: var(--warning); }
.integrity-chip--red { color: var(--danger); }
.integrity-chip--grey { color: var(--muted); }
.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: currentColor;
}
.count {
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
</style>
