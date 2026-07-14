<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { BoardPayload } from '../../api/board'

defineProps<{
  board: BoardPayload | null | undefined
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
  <div v-if="board" class="status-grid">
    <div class="status-card">
      <div class="status-card__title">{{ t('dashboard.board.bgTasks') }}</div>
      <div v-if="board.background_tasks?.discovery?.running" class="status-card__badge status-card__badge--active">
        {{ t('dashboard.backgroundTasks.title') }}
      </div>
      <div v-else class="status-card__line">
        {{ t('dashboard.board.discoveryStatus') }}: {{ board.background_tasks?.discovery?.status ?? '—' }}
      </div>
      <div class="status-card__line">
        {{ t('dashboard.board.probeChecks') }}: {{ board.background_tasks?.probe_loop?.checks_last_10m ?? 0 }}
      </div>
    </div>
    <div class="status-card status-card--clickable" @click="emit('openSelfcheck')">
      <div class="status-card__title">{{ t('dashboard.board.selfcheckTitle') }}</div>
      <div class="status-card__line">
        {{ t('dashboard.board.selfcheckLast') }}: {{ board.selfcheck?.last_status ?? '—' }}
      </div>
      <div class="status-card__line">
        {{ t('dashboard.board.selfcheckRate') }}: {{ fmtPct(board.selfcheck?.success_rate) }}
      </div>
      <div class="status-card__hint">{{ t('dashboard.board.selfcheckHint') }}</div>
    </div>
  </div>
</template>

<style scoped>
.status-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
.status-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
}
.status-card--clickable {
  cursor: pointer;
}
.status-card--clickable:hover {
  border-color: var(--accent);
}
.status-card__title {
  font-weight: 600;
  margin-bottom: 8px;
  font-size: 13px;
}
.status-card__line {
  font-size: 12px;
  color: var(--text-muted);
  margin-bottom: 4px;
}
.status-card__badge {
  display: inline-block;
  font-size: 12px;
  padding: 4px 8px;
  border-radius: 4px;
  margin-bottom: 6px;
}
.status-card__badge--active {
  background: rgba(210, 153, 34, 0.15);
  color: var(--warning);
}
.status-card__hint {
  font-size: 11px;
  color: var(--accent);
  margin-top: 6px;
}
</style>
