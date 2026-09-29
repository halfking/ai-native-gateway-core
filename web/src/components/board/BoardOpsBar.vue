<script setup lang="ts">
// BoardOpsBar.vue — 看板顶部运维状态 chips（2026-09-30 重构轮）。
// 取代原 BoardStatusCards 两张大卡：后台任务 + 系统自检压缩为横条 chip，
// 系统自检 chip 点击跳转 selfcheck tab（保留原交互）。
import { useI18n } from 'vue-i18n'
import type { BoardOperationalPayload } from '../../api/board'

defineProps<{
  operational: BoardOperationalPayload | null | undefined
}>()

const emit = defineEmits<{
  openSelfcheck: []
}>()

const { t } = useI18n()

function fmtPct(v: number | undefined) {
  if (v === undefined || v === null) return '—'
  return (Number(v) * 100).toFixed(1) + '%'
}
</script>

<template>
  <div v-if="operational" class="ops-bar">
    <div class="ops-chip" :class="{ 'ops-chip--warn': operational.background_tasks?.discovery?.running }">
      <span class="ops-dot" :class="operational.background_tasks?.discovery?.running ? 'ops-dot--warn' : 'ops-dot--ok'" aria-hidden="true"></span>
      <span class="ops-chip__title">{{ t('dashboard.board.bgTasks') }}</span>
      <span class="ops-chip__line">
        {{ t('dashboard.board.discoveryStatus') }}
        <b>{{ operational.background_tasks?.discovery?.running ? t('dashboard.board.opsRunning') : (operational.background_tasks?.discovery?.status ?? '—') }}</b>
      </span>
      <span class="ops-chip__sep" aria-hidden="true">·</span>
      <span class="ops-chip__line">
        {{ t('dashboard.board.opsChecks', { n: operational.background_tasks?.probe_loop?.checks_last_10m ?? 0 }) }}
      </span>
    </div>
    <button type="button" class="ops-chip ops-chip--btn" @click="emit('openSelfcheck')">
      <span class="ops-dot ops-dot--ok" aria-hidden="true"></span>
      <span class="ops-chip__title">{{ t('dashboard.board.selfcheckTitle') }}</span>
      <span class="ops-chip__line">{{ t('dashboard.board.opsRate24h', { n: fmtPct(operational.selfcheck?.success_rate) }) }}</span>
      <span class="ops-chip__go" aria-hidden="true">↗</span>
    </button>
  </div>
</template>

<style scoped>
.ops-bar {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  justify-content: flex-end;
}
.ops-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 11.5px;
  color: var(--text-muted);
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 999px;
  padding: 4px 11px;
  flex-wrap: wrap;
}
.ops-chip--btn {
  cursor: pointer;
  transition: border-color 0.15s, color 0.15s;
}
.ops-chip--btn:hover {
  border-color: var(--accent);
  color: var(--text);
}
.ops-chip--warn {
  border-color: color-mix(in srgb, var(--warning) 45%, var(--border));
}
.ops-chip__title {
  color: var(--text);
  font-weight: 600;
}
.ops-chip__line b {
  color: var(--text);
  font-weight: 600;
}
.ops-chip__sep {
  opacity: 0.6;
}
.ops-chip__go {
  color: var(--accent);
  font-weight: 700;
}
.ops-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
}
.ops-dot--ok {
  background: var(--success);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--success) 22%, transparent);
}
.ops-dot--warn {
  background: var(--warning);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--warning) 22%, transparent);
}
@media (max-width: 768px) {
  .ops-bar {
    justify-content: flex-start;
  }
  .ops-chip {
    border-radius: 8px;
  }
}
</style>
