<script setup lang="ts">
// ModelPlatformCompare.vue — 按模型跨平台现价（970，对齐 salesdecorate Compare）。
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getAdminMarketModelPlatforms, type MarketPlatformQuoteRow } from '../../api/market'
import { fmtMarketNum, fmtPair } from '../../utils/marketPriceFormat'

const { t } = useI18n()
const model = ref('')
const loading = ref(false)
const available = ref(true)
const reason = ref('')
const rows = ref<MarketPlatformQuoteRow[]>([])

async function load() {
  const m = model.value.trim()
  if (!m) return
  loading.value = true
  try {
    const res = await getAdminMarketModelPlatforms(m, 'standard', 300)
    available.value = res.available
    reason.value = res.reason || ''
    // 契约 C：名称列表按平台名排序（首列展示 platform_name）。
    rows.value = [...(res.items || [])].sort((a, b) => {
      const an = (a.platform_name || a.platform_id || '').trim()
      const bn = (b.platform_name || b.platform_id || '').trim()
      if (!an && bn) return 1
      if (an && !bn) return -1
      return an.localeCompare(bn, undefined, { numeric: true, sensitivity: 'base' })
    })
  } catch {
    available.value = false
    reason.value = 'load_failed'
    rows.value = []
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="plat-panel">
    <div class="filter-card card">
      <input v-model="model" class="filter-input" type="text" :placeholder="t('priceCompare.platSearch')" @keyup.enter="load" />
      <button class="btn btn-primary btn-sm" @click="load">{{ t('priceCompare.apply') }}</button>
    </div>
    <div v-if="!available" class="card app-empty-state">
      <p>{{ t('priceCompare.platUnavailableWhy') }}</p>
      <p class="empty-next mono-sm">{{ reason }}</p>
      <p class="empty-next">{{ t('priceCompare.platUnavailableNext') }}</p>
    </div>
    <div v-else-if="loading" class="card hint-line">…</div>
    <div v-else-if="!model.trim()" class="card app-empty-state">
      <p>{{ t('priceCompare.platNeedModel') }}</p>
    </div>
    <div v-else-if="!rows.length" class="card app-empty-state">
      <p>{{ t('priceCompare.platEmptyWhy') }}</p>
      <p class="empty-next">{{ t('priceCompare.platEmptyNext') }}</p>
    </div>
    <div v-else class="card table-wrap">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('priceCompare.colPlatform') }}</th>
            <th>{{ t('priceCompare.colRegion') }}</th>
            <th>{{ t('priceCompare.colCurrency') }}</th>
            <th>{{ t('priceCompare.colNativeIO') }}</th>
            <th>{{ t('priceCompare.colCNYIO') }}</th>
            <th>{{ t('priceCompare.colVendorFlag') }}</th>
            <th>{{ t('priceCompare.colRunDate') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in rows" :key="`${r.canonical_name}-${r.platform_id}`">
            <td>
              <div>{{ r.platform_name || r.platform_id }}</div>
              <code class="mono-sm">{{ r.platform_id }}</code>
            </td>
            <td>{{ r.region || '—' }}</td>
            <td>{{ r.currency || '—' }}</td>
            <td class="num">{{ fmtPair(r.input_per_1m, r.output_per_1m, '') }}</td>
            <td class="num">{{ fmtPair(r.cny_in_per_1m, r.cny_out_per_1m, '¥') }}</td>
            <td>
              <span v-if="r.is_vendor" class="badge badge-blue">{{ t('priceCompare.vendorYes') }}</span>
              <span v-else class="badge badge-gray">{{ t('priceCompare.vendorNo') }}</span>
            </td>
            <td class="mono-sm">{{ r.run_date || '—' }}</td>
          </tr>
        </tbody>
      </table>
      <p class="hint-line">{{ t('priceCompare.platHint', { n: rows.length, model: rows[0]?.canonical_name || model }) }}</p>
    </div>
  </div>
</template>

<style scoped>
.filter-card { display: flex; gap: 8px; align-items: center; }
.filter-input { flex: 1; max-width: 320px; }
.hint-line, .empty-next { font-size: 12px; color: var(--text-secondary, #666); }
</style>
