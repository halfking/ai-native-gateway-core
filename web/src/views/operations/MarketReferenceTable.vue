<script setup lang="ts">
// MarketReferenceTable.vue — 五价定价参考清单（P1/P2/P3/P5）。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { getAdminMaasMarketPriceOverview, type MarketPriceOverviewRow } from '../../api/maas'
import {
  fmtDualFloors, fmtMarketNum, fmtMarketRatio, fmtPair, fmtVendorAnchor, marketDriftClass,
} from '../../utils/marketPriceFormat'

const { t } = useI18n()
const loading = ref(false)
const items = ref<MarketPriceOverviewRow[]>([])
const q = ref('')
const onlyResolved = ref(true)
const syncDate = ref('')

const resolvedCount = computed(() => items.value.filter(v => v.resolved).length)

async function load() {
  loading.value = true
  try {
    const res = await getAdminMaasMarketPriceOverview(q.value, onlyResolved.value, 300)
    items.value = res.items || []
    syncDate.value = res.sync?.[0]?.run_date || ''
  } catch {
    items.value = []
  } finally {
    loading.value = false
  }
}
onMounted(load)
defineExpose({ reload: load })
</script>

<template>
  <div class="ref-panel">
    <div class="filter-card card">
      <input v-model="q" class="filter-input" type="text" :placeholder="t('priceCompare.refSearch')" @keyup.enter="load" />
      <label class="chk">
        <input v-model="onlyResolved" type="checkbox" @change="load" />
        {{ t('priceCompare.refOnlyResolved') }}
      </label>
      <button class="btn btn-primary btn-sm" @click="load">{{ t('priceCompare.apply') }}</button>
      <span v-if="syncDate" class="badge badge-gray">{{ t('priceCompare.refSyncAsOf', { date: syncDate, n: resolvedCount }) }}</span>
    </div>
    <div v-if="loading" class="card hint-line">…</div>
    <div v-else-if="!items.length" class="card app-empty-state">
      <p>{{ t('priceCompare.refEmptyWhy') }}</p>
      <p class="empty-next">{{ t('priceCompare.refEmptyNext') }}</p>
    </div>
    <div v-else class="card table-wrap">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('priceCompare.colModel') }}</th>
            <th>{{ t('priceCompare.colMarketMin') }}</th>
            <th>{{ t('priceCompare.colFloors') }}</th>
            <th>{{ t('priceCompare.colVendor') }}</th>
            <th>{{ t('priceCompare.colSupplier') }}</th>
            <th>{{ t('priceCompare.colRetail') }}</th>
            <th>{{ t('priceCompare.colDrift') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="v in items" :key="v.canonical_name">
            <td>
              <div>{{ v.display_name || v.canonical_name }}</div>
              <code class="mono-sm">{{ v.canonical_name }}</code>
              <span v-if="!v.resolved" class="badge badge-gray">{{ t('priceCompare.refUnresolved') }}</span>
            </td>
            <td class="num">{{ fmtPair(v.market_min_in_cny_per_1m, v.market_min_out_cny_per_1m, '¥') }}</td>
            <td class="num tiers">{{ fmtDualFloors(v) }}</td>
            <td class="num">{{ fmtVendorAnchor(v) }}</td>
            <td class="num">{{ fmtPair(v.supplier_in_per_1m, v.supplier_out_per_1m, '') }} {{ v.supplier_currency || '' }}</td>
            <td class="num">{{ fmtPair(v.retail_cny_in_per_1m, v.retail_cny_out_per_1m, '¥') }}</td>
            <td><span class="badge" :class="marketDriftClass(v.retail_vs_market)">{{ fmtMarketRatio(v.retail_vs_market) }}</span></td>
          </tr>
        </tbody>
      </table>
      <p class="hint-line">{{ t('priceCompare.refHint') }}</p>
    </div>
  </div>
</template>

<style scoped>
.filter-card { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.filter-input { flex: 1; max-width: 280px; }
.chk { font-size: 13px; display: flex; gap: 4px; align-items: center; }
.tiers { font-size: 11px; white-space: nowrap; }
.hint-line, .empty-next { font-size: 12px; color: var(--text-secondary, #666); }
</style>
