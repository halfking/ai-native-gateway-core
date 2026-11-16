<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { MarketPriceSyncRun } from '../../api/maas'

const props = defineProps<{
  runs: MarketPriceSyncRun[]
  loadFailed: boolean
}>()

const { t } = useI18n()
const detailRunId = ref<number | null>(null)

function toggleDetail(id: number) {
  detailRunId.value = detailRunId.value === id ? null : id
}
function conflictCount(d: { conflicts?: Record<string, unknown> | null }) {
  if (!d?.conflicts) return 0
  return Object.keys(d.conflicts).length
}
function detailMap(run: MarketPriceSyncRun, key: string): Record<string, number> {
  const v = (run.detail || {})[key]
  if (v && typeof v === 'object' && !Array.isArray(v)) return v as Record<string, number>
  return {}
}
function fmtWhen(s: string) {
  return s ? s.replace('T', ' ').replace('Z', ' UTC') : '—'
}
</script>

<template>
  <div class="card table-wrap">
    <div class="table-head-row">
      <h3 class="section-title">{{ t('marketPrices.syncTitle') }}</h3>
      <span class="badge badge-gray">{{ props.runs.length }}</span>
    </div>
    <div v-if="props.loadFailed" class="app-empty-state">
      <p>{{ t('marketPrices.syncLoadFailed') }}</p>
    </div>
    <div v-else-if="!props.runs.length" class="app-empty-state">
      <p>{{ t('marketPrices.syncEmptyWhy') }}</p>
      <p class="empty-next">{{ t('marketPrices.syncEmptyNext') }}</p>
    </div>
    <table v-else class="data-table">
      <thead>
        <tr>
          <th>{{ t('marketPrices.colRunDate') }}</th>
          <th>{{ t('marketPrices.colSourcePlatforms') }}</th>
          <th>{{ t('marketPrices.colSourceRows') }}</th>
          <th>{{ t('marketPrices.colLanded') }}</th>
          <th>{{ t('marketPrices.colRejected') }}</th>
          <th>{{ t('marketPrices.colConflicts') }}</th>
          <th>{{ t('marketPrices.colCreatedAt') }}</th>
          <th>{{ t('marketPrices.colActions') }}</th>
        </tr>
      </thead>
      <tbody>
        <template v-for="s in props.runs" :key="s.id">
          <tr>
            <td>{{ s.run_date }}</td>
            <td class="num">{{ s.source_platforms }}</td>
            <td class="num">{{ s.source_rows }}</td>
            <td class="num">{{ s.landed_rows }}</td>
            <td class="num" :class="{ 'num-warn': s.rejected_rows > 0 }">{{ s.rejected_rows }}</td>
            <td class="num" :class="{ 'num-warn': conflictCount(s) > 0 }">{{ conflictCount(s) }}</td>
            <td class="mono-sm">{{ fmtWhen(s.created_at) }}</td>
            <td>
              <button class="btn btn-ghost btn-sm" @click="toggleDetail(s.id)">
                {{ detailRunId === s.id ? t('marketPrices.detailCollapse') : t('marketPrices.detailExpand') }}
              </button>
            </td>
          </tr>
          <tr v-if="detailRunId === s.id">
            <td colspan="8" class="detail-cell">
              <p><b>{{ t('marketPrices.detailRejects') }}</b>: {{ JSON.stringify(detailMap(s, 'rejected_by_reason')) }}</p>
              <p><b>{{ t('marketPrices.detailConflicts') }}</b>: {{ JSON.stringify(s.conflicts || {}) }}</p>
              <p><b>{{ t('marketPrices.detailUnmapped') }}</b>: {{ JSON.stringify(detailMap(s, 'a17_unmapped_by_platform')) }}</p>
              <p><b>{{ t('marketPrices.detailExcluded') }}</b>: {{ JSON.stringify(detailMap(s, 'a17_excluded_by_platform')) }}</p>
              <p><b>{{ t('marketPrices.detailMismatch') }}</b>: {{ JSON.stringify(detailMap(s, 'a17_currency_mismatch_by_platform')) }}</p>
            </td>
          </tr>
        </template>
      </tbody>
    </table>
    <p v-if="props.runs.length" class="hint-line">{{ t('marketPrices.syncHint') }}</p>
  </div>
</template>

<style scoped>
.empty-next, .hint-line { font-size: 12px; color: var(--text-secondary, #666); }
.num-warn { color: #c0392b; font-weight: 600; }
.detail-cell { font-size: 12px; background: var(--surface-2, #f7f7f8); }
.detail-cell p { margin: 4px 0; word-break: break-all; }
</style>
