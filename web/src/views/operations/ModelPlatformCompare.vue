<script setup lang="ts">
// ModelPlatformCompare.vue —— 按模型跨平台现价（970，对齐 salesdecorate Compare）。
//
// ★ 2026-11-17（定价对比完善轮）三处可用性修正：
//   ① **模型选择器**：此前必须手打 canonical 名才能查（实测最刺眼的缺口）
//      ⇒ 现在先拉**模型目录**（按可比平台数降序）再选，不必记名字。
//   ② **最便宜标记**：非原厂行按输出价排序，最低价染绿（原厂行染橙 —— 原厂是
//      基准锚不是竞品，同 salesdecorate Compare 的染色口径）。
//   ③ **缓存命中价 + 阶梯**：972 起平台现价带 cache_hit / tier ⇒ 一并展示
//      （缓存命中价是真实成本的大头，此前网关侧根本没有这个数）。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getAdminMarketModelPlatforms,
  type MarketPlatformQuoteRow,
  type MarketPlatformQuoteDirectoryRow,
} from '../../api/market'
import { fmtMarketNum, fmtPair } from '../../utils/marketPriceFormat'

const { t } = useI18n()
const model = ref('')
const loading = ref(false)
const available = ref(true)
const reason = ref('')
const rows = ref<MarketPlatformQuoteRow[]>([])
const directory = ref<MarketPlatformQuoteDirectoryRow[]>([])
const dirLoading = ref(false)
const dirFailed = ref(false)

async function loadDirectory() {
  dirLoading.value = true
  dirFailed.value = false
  try {
    const res = await getAdminMarketModelPlatforms('', 'standard', 200)
    directory.value = res.directory || []
  } catch {
    dirFailed.value = true
    directory.value = []
  } finally {
    dirLoading.value = false
  }
}

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
    // ★ 2026-11-17（12h 审计）：reason 会被**原样渲染**（<p class="empty-next mono-sm">
    //   {{ reason }}</p>）⇒ 此处塞内部 token 'load_failed' ⇒ 用户在页面上看到
    //   一个英文机器词。改为已本地化的失败文案。
    available.value = false
    reason.value = t('marketPrices.loadFailed')
    rows.value = []
  } finally {
    loading.value = false
  }
}

onMounted(loadDirectory)

// ★ 最便宜标记：只看**非原厂**行（is_vendor 是基准锚不是竞品）。
const cheapestOut = computed(() => {
  let best: number | null = null
  for (const r of rows.value) {
    if (r.is_vendor) continue
    const v = r.cny_out_per_1m
    if (v === null || v === undefined) continue
    if (best === null || v < best) best = v
  }
  return best
})

function isCheapest(r: MarketPlatformQuoteRow): boolean {
  if (r.is_vendor) return false
  const v = r.cny_out_per_1m
  if (v === null || v === undefined || cheapestOut.value === null) return false
  return v === cheapestOut.value
}

function pick(d: MarketPlatformQuoteDirectoryRow) {
  model.value = d.canonical_name
  load()
}
</script>

<template>
  <div class="plat-panel">
    <!-- ① 模型选择器（不再要求手打 canonical 名） -->
    <div class="filter-card card">
      <input v-model="model" class="filter-input" type="text" :placeholder="t('priceCompare.platSearch')" @keyup.enter="load" />
      <button class="btn btn-primary btn-sm" @click="load">{{ t('priceCompare.apply') }}</button>
      <button class="btn btn-ghost btn-sm" @click="loadDirectory">{{ t('priceCompare.platReloadDir') }}</button>
    </div>

    <div v-if="dirLoading" class="card hint-line">…</div>
    <div v-else-if="dirFailed" class="card app-empty-state">
      <p>{{ t('priceCompare.platDirFailed') }}</p>
      <p class="empty-next">{{ t('priceCompare.platDirFailedNext') }}</p>
    </div>
    <div v-else-if="directory.length" class="card table-wrap">
      <div class="table-head-row">
        <h3 class="section-title">{{ t('priceCompare.platDirTitle') }}</h3>
        <span class="badge badge-gray">{{ directory.length }}</span>
      </div>
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('priceCompare.colModel') }}</th>
            <th>{{ t('priceCompare.platDirPlatforms') }}</th>
            <th>{{ t('priceCompare.colMarketMin') }}</th>
            <th>{{ t('priceCompare.colRunDate') }}</th>
            <th>{{ t('priceCompare.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="d in directory" :key="d.canonical_name">
            <td>
              <div>{{ d.display_name || d.canonical_name }}</div>
              <code class="mono-sm">{{ d.canonical_name }}</code>
            </td>
            <td class="num">{{ d.platforms }}</td>
            <td class="num">{{ fmtPair(d.min_cny_in_per_1m, d.min_cny_out_per_1m, '¥') }}</td>
            <td class="mono-sm">{{ d.run_date || '—' }}</td>
            <td class="actions"><button class="btn btn-ghost btn-sm" @click="pick(d)">{{ t('priceCompare.platDirPick') }}</button></td>
          </tr>
        </tbody>
      </table>
      <p class="hint-line">{{ t('priceCompare.platDirHint') }}</p>
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
            <th>{{ t('priceCompare.colCacheHit') }}</th>
            <th>{{ t('priceCompare.colTier') }}</th>
            <th>{{ t('priceCompare.colVendorFlag') }}</th>
            <th>{{ t('priceCompare.colRunDate') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="r in rows"
            :key="`${r.canonical_name}-${r.platform_id}-${r.tier}`"
            :class="{ 'cheapest-row': isCheapest(r) }"
          >
            <td>
              <div>{{ r.platform_name || r.platform_id }}</div>
              <code class="mono-sm">{{ r.platform_id }}</code>
              <span v-if="isCheapest(r)" class="badge badge-green">{{ t('priceCompare.platCheapest') }}</span>
            </td>
            <td>{{ r.region || '—' }}</td>
            <td>{{ r.currency || '—' }}</td>
            <td class="num">{{ fmtPair(r.input_per_1m, r.output_per_1m, '') }}</td>
            <td class="num">{{ fmtPair(r.cny_in_per_1m, r.cny_out_per_1m, '¥') }}</td>
            <td class="num">{{ fmtMarketNum(r.cny_cache_hit_per_1m) }}</td>
            <td class="mono-sm">{{ r.tier || '—' }}</td>
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
.cheapest-row { background: rgba(30, 142, 62, 0.08); }
.hint-line, .empty-next { font-size: 12px; color: var(--text-secondary, #666); }
</style>
