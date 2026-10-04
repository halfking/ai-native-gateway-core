<script setup lang="ts">
// AlertsView — 告警时间线（candidate-failures 告警环）。
import { ref } from 'vue'
import { ContinuousListController, useHyperPage } from '@/hyper'
import { fetchAlerts, type CandidateFailureAlert } from '@/api/alerts'
import { t } from '@/i18n'
import HyperList from '@/components/common/HyperList.vue'
import { fmtTime } from '@/utils/format'

useHyperPage({ title: () => t('alerts.title') })

let cache: CandidateFailureAlert[] = []
const controller = new ContinuousListController<CandidateFailureAlert>({
  fetchPage: async (page) => {
    if (page > 1) return { items: [], total: cache.length }
    const resp = await fetchAlerts()
    cache = resp.data ?? []
    return { items: cache, total: cache.length }
  },
  stableKey: (a) => `${a.credential_id}-${a.raw_model_name}-${a.ts}`,
  scopeKey: 'alerts',
})

const expanded = ref<string | null>(null)

function kindTone(kind: string): string {
  if (kind.includes('auth')) return 'badge--danger'
  if (kind.includes('rate') || kind.includes('429')) return 'badge--warning'
  return 'badge--info'
}

function alertKey(a: CandidateFailureAlert): string {
  return `${a.credential_id}-${a.raw_model_name}-${a.ts}`
}
</script>

<template>
  <HyperList
    :controller="controller"
    :item-key="alertKey"
    :empty-hint="t('alerts.emptyHint')"
  >
    <template #item="{ item: a }">
      <button type="button" class="data-card alert-card" @click="expanded = expanded === alertKey(a) ? null : alertKey(a)">
        <div class="card-row">
          <span class="badge" :class="kindTone(a.error_kind)">{{ a.error_kind }}</span>
          <span class="alert-card__count num">×{{ a.count }}</span>
        </div>
        <div class="alert-card__model">{{ a.raw_model_name }}</div>
        <div class="alert-card__meta">
          <span>{{ t('alerts.credential') }} #{{ a.credential_id }}</span>
          <span>{{ fmtTime(a.ts) }}</span>
        </div>
        <pre v-if="expanded === alertKey(a) && a.last_response_preview" class="alert-card__body">{{ a.last_response_preview }}</pre>
      </button>
    </template>
  </HyperList>
</template>

<style scoped>
.alert-card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  width: 100%;
  text-align: left;
  cursor: pointer;
  font: inherit;
  color: inherit;
}

.alert-card:active {
  background: var(--app-primary-softer);
}

.alert-card__count {
  font-size: 0.8125rem;
  color: var(--app-danger);
  font-weight: 600;
}

.alert-card__model {
  font-size: 0.875rem;
  font-weight: 500;
  word-break: break-all;
}

.alert-card__meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1) var(--app-space-3);
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.alert-card__body {
  margin: 0;
  padding: var(--app-space-2);
  background: var(--app-surface-muted);
  border-radius: var(--app-radius-sm);
  font-size: 0.6875rem;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 160px;
  overflow-y: auto;
}
</style>
