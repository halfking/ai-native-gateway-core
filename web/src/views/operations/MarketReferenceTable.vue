<script setup lang="ts">
// MarketReferenceTable.vue — 五价定价参考清单（P1/P2/P3/P5）。
// ★ 2026-11-18（价格对比优化轮）：使用 ModelPicker 组件，选择后自动刷新。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { getAdminMaasMarketPriceOverview, type MarketPriceOverviewRow } from '../../api/maas'
import {
  fmtDualFloors, fmtMarketNum, fmtMarketRatio, fmtPair, fmtVendorAnchor, marketDriftClass,
} from '../../utils/marketPriceFormat'
import ModelPicker from '../../components/ModelPicker.vue'

const { t } = useI18n()
const loading = ref(false)
const items = ref<MarketPriceOverviewRow[]>([])
const q = ref('')
const onlyResolved = ref(true)
const syncDate = ref('')
// ★ 2026-11-17（12h 审计）：区分「读失败」与「没有数据」。
const loadFailed = ref(false)

const resolvedCount = computed(() => items.value.filter(v => v.resolved).length)

async function load() {
  loading.value = true
  try {
    const res = await getAdminMaasMarketPriceOverview(q.value, onlyResolved.value, 300)
    items.value = res.items || []
    syncDate.value = res.sync?.[0]?.run_date || ''
    loadFailed.value = false
  } catch {
    // ★ 2026-11-17（12h 审计）：读失败**不等于**空集。
    //   此前 catch 吞成 items=[] ⇒ 接口 500 与「确实没有参考数据」渲染成同一个空态
    //   ⇒ 运维看不到「这页挂了」。单列 loadFailed，让失败与空集可区分。
    items.value = []
    loadFailed.value = true
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
      <div class="model-picker-wrapper">
        <ModelPicker
          v-model="q"
          :placeholder="t('priceCompare.refSearch')"
          :title="t('priceCompare.colModel')"
          @update:model-value="load"
        />
      </div>
      <label class="chk">
        <input v-model="onlyResolved" type="checkbox" @change="load" />
        {{ t('priceCompare.refOnlyResolved') }}
      </label>
      <button class="btn btn-primary btn-sm" @click="load">{{ t('priceCompare.apply') }}</button>
      <span v-if="syncDate" class="badge badge-gray">{{ t('priceCompare.refSyncAsOf', { date: syncDate, n: resolvedCount }) }}</span>
    </div>
    <div v-if="loading" class="card hint-line">…</div>
    <!-- ★ 2026-11-17（12h 审计）：读失败与「确实没有数据」必须可区分。
         此前两者共用同一个空态 ⇒ 接口 500 时用户以为是自己没数据。 -->
    <div v-else-if="loadFailed" class="card app-empty-state">
      <p>{{ t('marketPrices.loadFailed') }}</p>
      <p class="empty-next">{{ t('priceCompare.refEmptyNext') }}</p>
    </div>
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
.model-picker-wrapper { flex: 1; min-width: 280px; max-width: 400px; }
.chk { font-size: 13px; display: flex; gap: 4px; align-items: center; }
.tiers { font-size: 11px; white-space: nowrap; }
.hint-line, .empty-next { font-size: 12px; color: var(--text-secondary, #666); }
</style>
