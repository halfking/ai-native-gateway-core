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
// ★ 2026-11-18（价格对比优化轮）：改用 ModelPicker 组件替代手动输入，
//      复用 routing-v2 的选择器，热门模型按名称字母顺序排列。
// ★ 2026-11-19（比较与定价优化轮）：接收全局模型过滤参数，移除本组件的模型选择器。
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getAdminMarketModelPlatforms,
  type MarketPlatformQuoteRow,
  type MarketPlatformQuoteDirectoryRow,
} from '../../api/market'
import { fmtMarketNum, fmtPair } from '../../utils/marketPriceFormat'

const props = defineProps<{
  globalModels?: string[] // 全局模型过滤
}>()

const { t } = useI18n()
const loading = ref(false)
const available = ref(true)
const reason = ref('')
const rows = ref<MarketPlatformQuoteRow[]>([])
const directory = ref<MarketPlatformQuoteDirectoryRow[]>([])
const dirLoading = ref(false)
const dirFailed = ref(false)
// ★ 匹配范围：'' = 只查本模型（精确匹配）；'family' = 额外含同族变体（显式开启）。
const scope = ref<'' | 'family'>('')

// 2026-11-19：使用全局模型过滤的第一个模型作为当前查询模型。
//
// ★ D23（2026-11-19 审计轮）：上游 797b42067 把本组件改成「接收全局模型过滤、
//   移除本组件的模型选择器」，但**只改了一半**——脚本里删掉了 `model` ref 与
//   ModelPicker 导入，模板里的 `<ModelPicker v-model="model">` 与其余 `model`
//   引用却留在原地 ⇒ vue-tsc 报 `Cannot find name 'model'`（上游自己的提交
//   说明里写「vue-tsc 通过」，与事实不符），且**运行时模板渲染也会炸**。
//
// ★ 本地覆盖（pickedModel）的存在理由：目录表的「选择」按钮要能选中一个模型，
//   而 currentModel 是**只读 computed**（值来自父组件的全局过滤）⇒ 无法赋值。
//   故用一个本地覆盖 ref 承接该按钮，且**全局过滤一变即失效**（见下方 watch），
//   避免「本地覆盖」长期遮住父组件的新全局口径。
const pickedModel = ref('')
const currentModel = computed(() =>
  pickedModel.value.trim() ||
  (props.globalModels && props.globalModels.length > 0 ? props.globalModels[0] : '')
)
const selectedModel = computed(() => currentModel.value.trim().toLowerCase())
// 同族视图下必须显示每行的 canonical_name，否则用户分不清哪行是哪个模型。
const showModelColumn = computed(() => scope.value === 'family')

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
  const m = currentModel.value.trim()
  if (!m) return
  loading.value = true
  try {
    const res = await getAdminMarketModelPlatforms(m, 'standard', 300, scope.value)
    available.value = res.available
    reason.value = res.reason || ''
    // 契约 C：名称列表按平台名排序（首列展示 platform_name）。
    // ★ 同族模式下按「是否本模型」优先排序（同族行排在后面），
    //   否则本模型的行情会被同族的行淹没。
    rows.value = [...(res.items || [])].sort((a, b) => {
      const aOwn = (a.canonical_name || '').trim().toLowerCase() === selectedModel.value
      const bOwn = (b.canonical_name || '').trim().toLowerCase() === selectedModel.value
      if (aOwn !== bOwn) return aOwn ? -1 : 1
      const an = (a.platform_name || a.platform_id || '').trim()
      const bn = (b.platform_name || b.platform_id || '').trim()
      if (!an && bn) return 1
      if (an && !bn) return -1
      return an.localeCompare(bn, undefined, { numeric: true, sensitivity: 'base' })
    })
  } catch (err) {
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

// ★ 2026-11-19：监听全局模型过滤变化，自动刷新
watch(() => props.globalModels, () => {
  // ★ D23：全局过滤变了 ⇒ 放弃本地覆盖，回到「以全局为准」的口径。
  pickedModel.value = ''
  if (currentModel.value.trim()) {
    load()
  }
}, { deep: true, immediate: true })

// ★ 最便宜标记：只看**非原厂**行（is_vendor 是基准锚不是竞品）。
// ★ 且**只在本模型内比较**（F11）：同族变体（glm-5.3-flash 等）的价
//   不是这个模型的价 ⇒ 绝不能让它抢走「最便宜」标记。
const cheapestOut = computed(() => {
  let best: number | null = null
  for (const r of rows.value) {
    if (r.is_vendor) continue
    if (!isSelectedModel(r)) continue
    const v = r.cny_out_per_1m
    if (v === null || v === undefined) continue
    if (best === null || v < best) best = v
  }
  return best
})

function isSelectedModel(r: MarketPlatformQuoteRow): boolean {
  return (r.canonical_name || '').trim().toLowerCase() === selectedModel.value
}

function isCheapest(r: MarketPlatformQuoteRow): boolean {
  if (r.is_vendor) return false
  if (!isSelectedModel(r)) return false
  const v = r.cny_out_per_1m
  if (v === null || v === undefined || cheapestOut.value === null) return false
  return v === cheapestOut.value
}

function pick(d: MarketPlatformQuoteDirectoryRow) {
  // ★ D23：currentModel 是只读 computed ⇒ 目录「选择」写入本地覆盖 ref。
  pickedModel.value = d.canonical_name
  load()
}

function setScope(next: '' | 'family') {
  if (scope.value === next) return
  scope.value = next
  if (currentModel.value.trim()) load()
}
</script>

<template>
  <div class="plat-panel">
    <!-- ★ D23：模型选择器已移除（上游 797b42067 的声明意图：改用父组件的全局
         模型过滤）。目录表的「选择」按钮改为写入本地覆盖 ref（pickedModel）。 -->
    <div class="filter-card card">
      <button class="btn btn-ghost btn-sm" @click="loadDirectory">{{ t('priceCompare.platReloadDir') }}</button>
      <!-- ★ 匹配范围（F11）：默认只查本模型；同族变体必须**显式**开启 -->
      <span class="scope-label">{{ t('priceCompare.platScopeLabel') }}</span>
      <button
        class="btn btn-sm"
        :class="scope === '' ? 'btn-primary' : 'btn-ghost'"
        @click="setScope('')"
      >{{ t('priceCompare.platScopeExact') }}</button>
      <button
        class="btn btn-sm"
        :class="scope === 'family' ? 'btn-primary' : 'btn-ghost'"
        @click="setScope('family')"
      >{{ t('priceCompare.platScopeFamily') }}</button>
    </div>

    <div v-if="dirLoading" class="card hint-line">…</div>
    <div v-else-if="dirFailed" class="card app-empty-state">
      <p>{{ t('priceCompare.platDirFailed') }}</p>
      <p class="empty-next">{{ t('priceCompare.platDirFailedNext') }}</p>
    </div>
    <div v-else-if="directory.length && !currentModel.trim()" class="card table-wrap">
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
    <div v-else-if="!currentModel.trim()" class="card app-empty-state">
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
            <th v-if="showModelColumn">{{ t('priceCompare.colModel') }}</th>
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
            <td v-if="showModelColumn">
              <div>{{ r.canonical_name }}</div>
              <code v-if="!isSelectedModel(r)" class="mono-sm family-tag">{{ t('priceCompare.platScopeFamilyTag') }}</code>
            </td>
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
      <p class="hint-line">{{ t('priceCompare.platHint', { n: rows.length, model: rows[0]?.canonical_name || currentModel }) }}</p>
    </div>
  </div>
</template>

<style scoped>
.filter-card { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.cheapest-row { background: rgba(30, 142, 62, 0.08); }
.scope-label { font-size: 12px; color: var(--text-secondary, #666); }
.family-tag { color: var(--text-secondary, #666); }
.hint-line, .empty-next { font-size: 12px; color: var(--text-secondary, #666); }
</style>
