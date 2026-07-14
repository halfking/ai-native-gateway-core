<script setup lang="ts">
// SystemHealthBadge — 2026-07-14 (migration 341 + bg/system_health.go)
//
// Polls /api/health/system every 10s (cheap SQL: 30s windowed aggregate
// served by system_health_status() in migration 341) and renders a
// single-letter "H" indicator that mirrors the gateway-wide 30s success
// rate:
//
//   ok       → green dot
//   degraded → red dot
//   suspect  → grey dot (no traffic)
//
// The H is positioned next to the existing SystemStatusIndicator in
// App.vue so operators see the indicator on every page; a tooltip
// surfaces the precise success rate and sample count.
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { getSystemHealth, type SystemHealthResponse } from '../api/system'

const data = ref<SystemHealthResponse | null>(null)
const error = ref<string | null>(null)
let timer: number | null = null

const dotClass = computed(() => {
  if (error.value) return 'health-dot health-dot-err'
  const s = data.value?.status
  if (s === 'ok') return 'health-dot health-dot-ok'
  if (s === 'degraded') return 'health-dot health-dot-degraded'
  return 'health-dot health-dot-suspect'
})

const tooltip = computed(() => {
  if (error.value) return `系统健康自检：${error.value}`
  if (!data.value) return '系统健康自检：尚未取到数据'
  const pct = (data.value.success_rate * 100).toFixed(1)
  return `系统健康自检（30s）：${data.value.status} | 成功率 ${pct}% | 样本 ${data.value.sample_count} | 失败 ${data.value.failure_count}`
})

async function tick() {
  try {
    data.value = await getSystemHealth()
    error.value = null
  } catch (e) {
    error.value = (e as Error).message || 'request failed'
  }
}

onMounted(() => {
  tick()
  timer = window.setInterval(tick, 10_000)
})
onUnmounted(() => {
  if (timer !== null) window.clearInterval(timer)
})
</script>

<template>
  <span class="health-badge" :title="tooltip" aria-label="system-health">
    <span :class="dotClass" />
    <span class="health-letter">H</span>
  </span>
</template>

<style scoped>
.health-badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  cursor: default;
  font-size: 0.85em;
  line-height: 1;
  user-select: none;
}
.health-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  display: inline-block;
  transition: background-color 0.3s ease;
}
.health-dot-ok       { background-color: #2ecc71; }
.health-dot-degraded { background-color: #e74c3c; }
.health-dot-suspect  { background-color: #95a5a6; }
.health-dot-err      { background-color: #f39c12; }
.health-letter {
  font-weight: 600;
  color: var(--system-status-letter, inherit);
  opacity: 0.85;
}
</style>
