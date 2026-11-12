<script setup lang="ts">
// PriceCompareView.vue — 价格对比分析面（定稿 M3-T2，2026-11-13 A-17 收口）。
// 读面：C1/C2/C3 评分行（supplier_market_position，评分 job 每日 upsert）
// + 双地板对拍（market_price_reference 959：overseas=USD 原生 / domestic=CNY
// 原生）+ 覆盖率摘要（price_confidence 四档计数）。
// 三级币种展示：原币（vendor_list_currency / 地板原生币）+ CNY + USD；
// USD 表达 = CNY / fx（快照 fx_rate 存在时才可导出，缺 ⇒ 显示「—」，不猜）。
// NULL≠0：无行情/无价格的格子一律显示「—」，绝不落 0。
// 红线锚点：MarketAdvantage 只展示（路由权重默认 0 未接线 —— M3-T3 另立裁决），
// 本页任何读数都不喂路由/计费。
import { useI18n } from 'vue-i18n'
import { ref, computed, onMounted } from 'vue'
import { getAdminMarketCompare, type MarketCompareRow } from '../../api/market'
import ResponsiveDataView from '../../components/ui/ResponsiveDataView.vue'
import type { CardField } from '../../components/ui/CardList.vue'
import { useActionMessage } from '../../composables/useActionMessage'

const { t } = useI18n()
const { notifyError } = useActionMessage()

const loading = ref(false)
const available = ref(true)
const unavailableReason = ref('')
const rows = ref<MarketCompareRow[]>([])
const summary = ref<Record<string, number>>({})
const qtext = ref('')
const confFilter = ref('')

const CONF_CLASSES: Record<string, string> = {
  trusted: 'badge-green',
  suspect_symmetric: 'badge-yellow',
  empty: 'badge-gray',
  unknown_currency: 'badge-red',
}

async function load() {
  loading.value = true
  try {
    const resp = await getAdminMarketCompare(qtext.value, confFilter.value, 200)
    available.value = resp.available
    unavailableReason.value = resp.reason || ''
    rows.value = resp.items || []
    summary.value = resp.summary || {}
  } catch {
    available.value = false
    notifyError(t('priceCompare.loadFailed'))
  } finally {
    loading.value = false
  }
}
onMounted(load)

function reload() { load() }

const bindingsTotal = computed(() =>
  Object.values(summary.value).reduce((a, b) => a + b, 0))

function confCount(k: string) { return summary.value[k] ?? 0 }

const confFilters = ['trusted', 'suspect_symmetric', 'empty', 'unknown_currency']

// 静态键映射（t() 的键必须可被静态抽取器找到 ⇒ 不做字符串拼接）。
const CONF_LABEL_KEYS: Record<string, string> = {
  trusted: 'priceCompare.confTrusted',
  suspect_symmetric: 'priceCompare.confSuspect',
  empty: 'priceCompare.confEmpty',
  unknown_currency: 'priceCompare.confUnknown',
}
function confLabel(k: string) { return t(CONF_LABEL_KEYS[k] || k) }

// ---- 三级币种展示 ----
function fmtNum(v: number | null | undefined, digits = 4): string {
  if (v === null || v === undefined) return '—'
  return Number(v).toFixed(digits)
}

// USD 表达：CNY / fx（快照汇率存在时才导出；fx 缺失 ⇒ null ⇒ '—'）。
function usdFromCNY(cny: number | null | undefined, fx: number | null | undefined): number | null {
  if (cny === null || cny === undefined) return null
  if (!fx || fx === 0) return null
  return cny / fx
}

function anchorTiers(row: MarketCompareRow): string {
  const native = fmtNum(row.vendor_list_native_in_per_1m)
  const cur = row.vendor_list_currency || '?'
  const cny = fmtNum(row.vendor_list_cny_in_per_1m)
  const usd = fmtNum(usdFromCNY(row.vendor_list_cny_in_per_1m, row.mpr_fx_rate))
  return `${native} ${cur} / ¥${cny} / $${usd}`
}

// 双地板三级币：原币（overseas=USD / domestic=CNY 原生）+ CNY + USD（USD/CNY
// 表达仅在快照 fx 存在时可导出，缺 ⇒ '—'，不猜第二把汇率）。
function floorTiers(row: MarketCompareRow, side: 'overseas' | 'domestic'): string {
  const isOvr = side === 'overseas'
  const inNative = isOvr ? row.overseas_min_in_per_1m : row.domestic_min_in_per_1m
  const outNative = isOvr ? row.overseas_min_out_per_1m : row.domestic_min_out_per_1m
  const fx = row.mpr_fx_rate
  const cny = isOvr ? usdToCNY(inNative, fx) : inNative
  const usd = isOvr ? inNative : usdFromCNY(inNative, fx)
  const pid = isOvr ? row.overseas_min_platform_id : row.domestic_min_platform_id
  const count = isOvr ? row.overseas_min_platform_count : row.domestic_min_platform_count
  const nativeCur = isOvr ? 'USD' : 'CNY'
  return `${fmtNum(inNative)}/${fmtNum(outNative)} ${nativeCur} / ¥${fmtNum(cny)} / $${fmtNum(usd)} · ${pid || '—'}(${count})`
}

// CNY 表达：USD × fx（fx 缺失 ⇒ null）。
function usdToCNY(usd: number | null | undefined, fx: number | null | undefined): number | null {
  if (usd === null || usd === undefined) return null
  if (!fx || fx === 0) return null
  return usd * fx
}

// 平台数 <3 ⇒ 该地板降级为参考值（告警 market_ref_thin_coverage 口径）。
function floorCount(row: MarketCompareRow, side: 'overseas' | 'domestic'): number {
  return side === 'overseas' ? row.overseas_min_platform_count : row.domestic_min_platform_count
}

const compareFields = computed<CardField[]>(() => [
  { key: 'canonical_name', label: t('priceCompare.colModel') },
  { key: 'currency', label: t('priceCompare.colCurrency') },
  { key: 'price_confidence', label: t('priceCompare.colConfidence') },
  { key: 'market_position', label: t('priceCompare.colMarketPosition') },
  { key: 'computed_at', label: t('priceCompare.colComputedAt') },
])
</script>

<template>
  <div class="page">
    <h2 class="page-title">{{ t('priceCompare.title') }}</h2>
    <p class="page-sub">{{ t('priceCompare.subtitle') }}</p>

    <!-- ① 覆盖率摘要 -->
    <div class="card summary-card">
      <div class="summary-row">
        <span class="badge badge-gray">{{ t('priceCompare.totalBindings') }}: {{ bindingsTotal }}</span>
        <span class="badge badge-green">{{ t('priceCompare.confTrusted') }}: {{ confCount('trusted') }}</span>
        <span class="badge badge-yellow">{{ t('priceCompare.confSuspect') }}: {{ confCount('suspect_symmetric') }}</span>
        <span class="badge badge-gray">{{ t('priceCompare.confEmpty') }}: {{ confCount('empty') }}</span>
        <span class="badge badge-red">{{ t('priceCompare.confUnknown') }}: {{ confCount('unknown_currency') }}</span>
      </div>
      <p class="hint-line">{{ t('priceCompare.summaryHint') }}</p>
    </div>

    <!-- 筛选 -->
    <div class="card filter-card">
      <input v-model="qtext" class="filter-input" type="text" :placeholder="t('priceCompare.searchPlaceholder')" @keyup.enter="reload" />
      <select v-model="confFilter" class="filter-select">
        <option value="">{{ t('priceCompare.confAll') }}</option>
        <option v-for="c in confFilters" :key="c" :value="c">{{ confLabel(c) }}</option>
      </select>
      <button class="btn btn-primary btn-sm" @click="reload">{{ t('priceCompare.apply') }}</button>
    </div>

    <!-- 空态：为什么空 + 下一步 -->
    <div v-if="!available" class="card table-wrap">
      <div class="app-empty-state">
        <p>{{ t('priceCompare.unavailableWhy') }}</p>
        <p class="empty-next mono-sm">{{ unavailableReason }}</p>
        <p class="empty-next">{{ t('priceCompare.unavailableNext') }}</p>
      </div>
    </div>
    <div v-else-if="!rows.length && !loading" class="card table-wrap">
      <div class="app-empty-state">
        <p>{{ t('priceCompare.emptyWhy') }}</p>
        <p class="empty-next">{{ t('priceCompare.emptyNext') }}</p>
      </div>
    </div>

    <!-- ② 评分行 + 双地板 -->
    <div v-else class="card table-wrap">
      <div class="table-head-row">
        <h3 class="section-title">{{ t('priceCompare.tableTitle') }}</h3>
        <span class="badge badge-gray">{{ rows.length }}</span>
      </div>
      <ResponsiveDataView :rows="rows" title-key="canonical_name" :title-format="row => String(row.canonical_name)" :fields="compareFields" :loading="loading" :empty="false">
        <template #table>
          <table class="data-table">
            <thead>
              <tr>
                <th>{{ t('priceCompare.colModel') }}</th>
                <th>{{ t('priceCompare.colCredential') }}</th>
                <th>{{ t('priceCompare.colCurrency') }}</th>
                <th>{{ t('priceCompare.colSupplier') }}</th>
                <th>{{ t('priceCompare.colConfidence') }}</th>
                <th>{{ t('priceCompare.colDiscount') }}</th>
                <th>{{ t('priceCompare.colMarketPosition') }}</th>
                <th>{{ t('priceCompare.colRefFloor') }}</th>
                <th>{{ t('priceCompare.colOverseasFloor') }}</th>
                <th>{{ t('priceCompare.colDomesticFloor') }}</th>
                <th>{{ t('priceCompare.colVsOverseas') }}</th>
                <th>{{ t('priceCompare.colVsDomestic') }}</th>
                <th>{{ t('priceCompare.colAdvantage') }}</th>
                <th>{{ t('priceCompare.colAnchor') }}</th>
                <th>{{ t('priceCompare.colComputedAt') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in rows" :key="`${r.credential_id}-${r.provider_model_id}`">
                <td><code class="mono-sm">{{ r.canonical_name }}</code></td>
                <td class="num">{{ r.credential_id }}</td>
                <td>{{ r.currency }}</td>
                <td class="num">{{ fmtNum(r.supplier_in_per_1m) }} / {{ fmtNum(r.supplier_out_per_1m) }}</td>
                <td><span class="badge" :class="CONF_CLASSES[r.price_confidence] || 'badge-gray'">{{ r.price_confidence }}</span></td>
                <td class="num">{{ fmtNum(r.discount_vs_vendor) }}</td>
                <td class="num">{{ fmtNum(r.market_position) }}</td>
                <td class="num">{{ fmtNum(r.market_ref_in_per_1m) }} / {{ fmtNum(r.market_ref_out_per_1m) }} <span class="badge badge-gray">{{ r.market_ref_used || '—' }}</span> {{ r.market_ref_platform_id }}({{ r.market_ref_platform_count }})</td>
                <td class="num tiers-cell" :class="{ 'thin-floor': floorCount(r, 'overseas') < 3 }">{{ floorTiers(r, 'overseas') }}</td>
                <td class="num tiers-cell" :class="{ 'thin-floor': floorCount(r, 'domestic') < 3 }">{{ floorTiers(r, 'domestic') }}</td>
                <td class="num">{{ fmtNum(r.market_position_vs_overseas) }}</td>
                <td class="num">{{ fmtNum(r.market_position_vs_domestic) }}</td>
                <td class="num">{{ fmtNum(r.market_advantage_score, 2) }}</td>
                <td class="num tiers-cell">{{ anchorTiers(r) }}</td>
                <td class="mono-sm">{{ (r.computed_at || '').replace('T', ' ').replace('Z', ' UTC') }}</td>
              </tr>
            </tbody>
          </table>
        </template>
        <template #cards="{ row }">
          <div class="compare-card">
            <p><b>{{ row.canonical_name }}</b> <span class="badge" :class="CONF_CLASSES[row.price_confidence] || 'badge-gray'">{{ row.price_confidence }}</span></p>
            <p class="hint-line">{{ t('priceCompare.colSupplier') }}: {{ fmtNum(row.supplier_in_per_1m) }} / {{ fmtNum(row.supplier_out_per_1m) }} {{ row.currency }}</p>
            <p class="hint-line">{{ t('priceCompare.colMarketPosition') }}: {{ fmtNum(row.market_position) }}</p>
            <p class="hint-line">{{ t('priceCompare.colAnchor') }}: {{ anchorTiers(row) }}</p>
            <p class="hint-line">{{ t('priceCompare.colOverseasFloor') }}: {{ floorTiers(row, 'overseas') }}</p>
            <p class="hint-line">{{ t('priceCompare.colDomesticFloor') }}: {{ floorTiers(row, 'domestic') }}</p>
          </div>
        </template>
      </ResponsiveDataView>
      <p class="hint-line">{{ t('priceCompare.tableHint') }}</p>
    </div>
  </div>
</template>

<style scoped>
.summary-card { display: flex; flex-direction: column; gap: 6px; }
.summary-row { display: flex; flex-wrap: wrap; gap: 8px; }
.filter-card { display: flex; gap: 8px; align-items: center; }
.filter-input { flex: 1; max-width: 280px; }
.filter-select { max-width: 200px; }
.tiers-cell { font-size: 11px; white-space: nowrap; }
.thin-floor { color: #b26a00; }
.empty-next { font-size: 12px; color: var(--text-secondary, #888); }
.hint-line { font-size: 12px; color: var(--text-secondary, #666); margin: 6px 0 0; }
.compare-card p { margin: 4px 0; }
</style>
