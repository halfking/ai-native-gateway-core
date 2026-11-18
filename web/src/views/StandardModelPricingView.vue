<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { surfaceError } from '../i18n/errorSurface'
import { localeRef } from '../i18n'
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  getAdminMaasModelRates,
  updateAdminMaasSettings,
  upsertAdminMaasModelRate,
  deleteAdminMaasModelRate,
  resetAdminMaasModelRateFields,
  batchUpsertAdminMaasModelRates,
  batchResetAdminMaasModelRates,
  batchFillGlobalAdminMaasModelRates,
  getAdminMaasModelPriceHistory,
  getAdminMaasModelPriceChanges,
  type AdminMaasModelRate,
  type ModelPriceVersion,
  type MaasAdminSettings,
  type MaasModelRateUpsert,
} from '../api'
import { getAdminMarketCompare, type MarketCompareRow } from '../api/market'
import ModelCatalogFilterBar from '../components/ModelCatalogFilterBar.vue'
import { useModelCatalogFilters } from '../composables/useModelCatalogFilters'
import { sortByName } from '../utils/sortByName'
import { confirmDialog } from '../composables/useConfirmDialog'
import { useActionMessage } from '../composables/useActionMessage'
import ActionOverflowMenu from '../components/ui/ActionOverflowMenu.vue'
import ResponsiveDataView from '../components/ui/ResponsiveDataView.vue'
import type { CardField } from '../components/ui/CardList.vue'
import ModelPicker from '../components/ModelPicker.vue'
// 2026-11-19（合并轮）：价格对比面并入本页 —— 四个子区按 tab 懒挂载（v-if），
// 原价对比页路由 /operations/price-compare 保留为重定向（收藏链接不断）。
import MarketReferenceTable from './operations/MarketReferenceTable.vue'
import ModelPlatformCompare from './operations/ModelPlatformCompare.vue'
import PricingSuggestionPanel from './operations/PricingSuggestionPanel.vue'

const { t } = useI18n()

type RateField = 'in' | 'out' | 'cache_in' | 'cache_out' | 'image' | 'audio' | 'video'

const loading = ref(false)
const savingSettings = ref(false)
// 审计 R3#10：操作反馈条统一走 useActionMessage。
const { message: settingsMsg, error, notifySuccess, notifyError, clear: clearActionMsg } = useActionMessage()
const models = ref<AdminMaasModelRate[]>([])
const settings = ref<MaasAdminSettings | null>(null)

function isFullyManual(m: AdminMaasModelRate) {
  return m.manual_in && m.manual_out && m.manual_cache_in && m.manual_cache_out &&
    (m.manual_image || m.manual_audio || m.manual_video ||
      (m.credits_per_1m_image_tokens === 0 && m.credits_per_1m_audio_tokens === 0 && m.credits_per_1m_video_tokens === 0))
}

function isMultimodal(m: AdminMaasModelRate | null | undefined) {
  if (!m) return false
  const mod = (m.modality || '').toLowerCase()
  return mod === 'multimodal' || mod === 'vision' || mod === 'audio' || mod === 'video' ||
    m.manual_image || m.manual_audio || m.manual_video
}

function modalityLabel(m: AdminMaasModelRate): string {
  const mod = (m.modality || '').toLowerCase()
  if (mod === 'multimodal') return t('standardModelPricing.table.modalityMultimodal')
  if (mod === 'vision') return t('standardModelPricing.table.modalityVision')
  if (mod === 'audio') return t('standardModelPricing.table.modalityAudio')
  if (mod === 'video') return t('standardModelPricing.table.modalityVideo')
  if (mod === 'text') return t('standardModelPricing.table.modalityText')
  if (mod === 'embedding') return t('standardModelPricing.table.modalityEmbedding')
  return t('standardModelPricing.table.modalityOther')
}

const pricingStatusOptions = [
  { value: 'default', label: t('standardModelPricing.defaultOnly') },
  { value: 'custom', label: t('standardModelPricing.customOnly') },
  { value: 'partial', label: t('standardModelPricing.partialOnly') },
]

const {
  pickedModel,
  filterVendor,
  extraFilter: filterMode,
  vendorOptions,
  filtered,
  clearFilters: clearCatalogFilters,
} = useModelCatalogFilters<AdminMaasModelRate>({
  items: models,
  getVendor: (m) => m.vendor?.trim() || t('standardModelPricing.otherVendor'),
  getCanonicalName: (m) => m.canonical_name,
  getDisplayName: (m) => m.display_name,
  matchExtra: (m, mode) => {
    if (mode === 'custom') return m.is_custom
    if (mode === 'partial') return m.is_custom && !isFullyManual(m)
    return true
  },
})
// tab 定义（labelKey必须是静态键 —— t() 的键要可被静态抽取器找到）。
const PAGE_TAB_DEFS: { key: PricingPageTab; labelKey: string }[] = [
  { key: 'rates', labelKey: 'standardModelPricing.page.title' },
  { key: 'reference', labelKey: 'priceCompare.tabReference' },
  { key: 'scoring', labelKey: 'priceCompare.tabScoring' },
  { key: 'platforms', labelKey: 'priceCompare.tabPlatforms' },
  { key: 'suggestion', labelKey: 'priceCompare.tabSuggestion' },
]

// 2026-10-03：标准模型定价表首列是「标准模型」（display_name 为主、canonical_name 为副），
// 之前完全按后端返回顺序，找某个模型只能翻。
//
// 复核更正：上一轮我把这个页面记成「排序会连带改筛选/分组语义，待决策」——
// **那个理由不成立**。useModelCatalogFilters 内部只做 filter（厂商 / 模式 / 搜索），
// 既不排序也不分组（src/composables/useModelCatalogFilters.ts:36-60），
// 所以在视图里排不碰任何筛选语义，无需决策。
const sortedFiltered = computed(() => sortByName(filtered.value, (a, b) => a.canonical_id - b.canonical_id, ['display_name']))

const selectedRows = ref<Set<number>>(new Set())
const clipboard = ref<{ in: number; out: number; cache_in: number; cache_out: number; image: number; audio: number; video: number } | null>(null)
const showBatchModal = ref(false)
const batchForm = ref({
  credits_per_1m_in: 0,
  credits_per_1m_out: 0,
  credits_per_1m_cache_in: 0,
  credits_per_1m_cache_out: 0,
  credits_per_1m_image_tokens: 0,
  credits_per_1m_audio_tokens: 0,
  credits_per_1m_video_tokens: 0,
})
const savingBatch = ref(false)
const batchMsg = ref('')

const editRow = ref<AdminMaasModelRate | null>(null)
const editForm = ref<MaasModelRateUpsert>(emptyEditForm())
const savingRow = ref(false)

// 2026-11-09（运营审计轮项2）：价格期版本账视图（迁移 952）。
const showHistoryModal = ref(false)
const historyRow = ref<AdminMaasModelRate | null>(null)
const historyItems = ref<ModelPriceVersion[]>([])
const historyLoading = ref(false)
const showChangesModal = ref(false)
const changeItems = ref<ModelPriceVersion[]>([])
const changesLoading = ref(false)

// ── 2026-11-19（合并轮）：页面级 tab。rates=定价管理主体（默认）；
// reference=定价参考 / scoring=供应评分 / platforms=跨平台现价 /
// suggestion=定价建议（原 PriceCompareView 四 tab 平移）。
// ?tab= 深链支持：MarketPricesView「打开对比」按钮带 tab 跳转。
const route = useRoute()
const router = useRouter()
export type PricingPageTab = 'rates' | 'reference' | 'scoring' | 'platforms' | 'suggestion'
const VALID_PAGE_TABS: PricingPageTab[] = ['rates', 'reference', 'scoring', 'platforms', 'suggestion']
const pageTab = ref<PricingPageTab>(normalizeTab(route.query.tab))
watch(() => route.query.tab, (v) => { pageTab.value = normalizeTab(v) })

function normalizeTab(v: unknown): PricingPageTab {
  const s = String(v ?? '')
  return (VALID_PAGE_TABS as string[]).includes(s) ? (s as PricingPageTab) : 'rates'
}

function switchPageTab(next: PricingPageTab) {
  pageTab.value = next
  // 深链同步：刷新/分享 URL 保持在同一 tab。
  if (route.query.tab !== next) {
    router.replace({ query: { ...route.query, tab: next === 'rates' ? undefined : next } }) //深链同步（replace 不入历史栈；rates 是默认 tab ⇒ 不带 query）
  }
}

// ── 2026-11-19（比较与定价优化轮）：全局模型过滤 ──────────────
// 页面级全局模型多选过滤器（影响 reference/scoring/platforms/suggestion 四个 tab）。
// 选中模型后，四个区域只显示这些标准模型的数据；rates tab 保持独立的 ModelCatalogFilterBar。
const globalSelectedModels = ref<string[]>([])
const globalBaseFolded = ref(false) // 全局基准折叠状态

// 全局模型变化处理：触发子区域数据刷新
function onGlobalModelsChange(models: string | string[]) {
  globalSelectedModels.value = Array.isArray(models) ? models : (models ? [models] : [])
  // 根据当前 tab 触发对应的数据刷新
  if (pageTab.value === 'scoring') loadScoring()
  // reference/platforms/suggestion 通过 props 传递，组件内部 watch 自动刷新
}

function clearGlobalModels() {
  globalSelectedModels.value = []
  onGlobalModelsChange([])
}

// rates tab 厂家过滤（根据全局模型过滤）
const filteredVendorOptions = computed(() => {
  if (pageTab.value !== 'rates' || globalSelectedModels.value.length === 0) {
    return vendorOptions.value
  }
  
  const modelsSet = new Set(globalSelectedModels.value)
  const vendorsWithData = new Set<string>()
  
  models.value.forEach(m => {
    if (modelsSet.has(m.canonical_name)) {
      const vendor = m.vendor?.trim() || t('standardModelPricing.otherVendor')
      vendorsWithData.add(vendor)
    }
  })
  
  return vendorOptions.value.filter(opt => vendorsWithData.has(opt))
})

// ── scoring 壳（自 PriceCompareView 平移，逐字保留口径）──────────────
// C1/C2/C3 评分行（supplier_market_position，评分 job 每日 upsert）
// + 双地板对拍（market_price_reference 959：overseas=USD 原生 /
// domestic=CNY 原生）+ 覆盖率摘要（price_confidence 四档计数）。
// 红线：scoring/地板读数只读对账，不喂路由/计费。
// NULL≠0：无行情/无价格的格子一律显示「—」，绝不落 0。
// 2026-11-19：scoring 的模型选择器已移除，改用全局模型过滤（globalSelectedModels）。
const scoringLoaded = ref(false)
const scoringLoading = ref(false)
const scoringAvailable = ref(true)
const scoringUnavailableReason = ref('')
const scoreRows = ref<MarketCompareRow[]>([])
const scoringSummary = ref<Record<string, number>>({})
const scoreConfFilter = ref('')

const SCORING_CONF_CLASSES: Record<string, string> = {
  trusted: 'badge-green',
  suspect_symmetric: 'badge-yellow',
  empty: 'badge-gray',
  unknown_currency: 'badge-red',
}

async function loadScoring() {
  scoringLoading.value = true
  try {
    // 2026-11-19：使用全局模型过滤（多选时取第一个作为查询参数）
    const qtext = globalSelectedModels.value.length > 0 ? globalSelectedModels.value[0] : ''
    const resp = await getAdminMarketCompare(qtext, scoreConfFilter.value, 200)
    scoringAvailable.value = resp.available
    scoringUnavailableReason.value = resp.reason || ''
    // 如果有全局模型过滤，前端再过滤结果只保留选中的模型
    let items = resp.items || []
    if (globalSelectedModels.value.length > 0) {
      const modelsSet = new Set(globalSelectedModels.value)
      items = items.filter(item => modelsSet.has(item.canonical_name))
    }
    scoreRows.value = items
    scoringSummary.value = resp.summary || {}
    scoringLoaded.value = true
  } catch {
    scoringAvailable.value = false
    notifyError(t('priceCompare.loadFailed'))
  } finally {
    scoringLoading.value = false
  }
}

function reloadScoring() { loadScoring() }

const scoringBindingsTotal = computed(() =>
  Object.values(scoringSummary.value).reduce((a, b) => a + b, 0))

function scoringConfCount(k: string) { return scoringSummary.value[k] ?? 0 }

const scoringConfFilters = ['trusted', 'suspect_symmetric', 'empty', 'unknown_currency']

// 静态键映射（t() 的键必须可被静态抽取器找到 ⇒ 不做字符串拼接）。
const SCORING_CONF_LABEL_KEYS: Record<string, string> = {
  trusted: 'priceCompare.confTrusted',
  suspect_symmetric: 'priceCompare.confSuspect',
  empty: 'priceCompare.confEmpty',
  unknown_currency: 'priceCompare.confUnknown',
}
function scoringConfLabel(k: string) { return t(SCORING_CONF_LABEL_KEYS[k] || k) }

// ---- 三级币种展示（自 PriceCompareView 平移）----
function fmtScoringNum(v: number | null | undefined, digits = 4): string {
  if (v === null || v === undefined) return '—'
  return Number(v).toFixed(digits)
}

// USD 表达：CNY / fx（快照汇率存在时才导出；fx 缺失 ⇒ null ⇒ '—'）。
function usdFromCNY(cny: number | null | undefined, fx: number | null | undefined): number | null {
  if (cny === null || cny === undefined) return null
  if (!fx || fx === 0) return null
  return cny / fx
}

// CNY 表达：USD × fx（fx 缺失 ⇒ null）。
function usdToCNY(usd: number | null | undefined, fx: number | null | undefined): number | null {
  if (usd === null || usd === undefined) return null
  if (!fx || fx === 0) return null
  return usd * fx
}

function anchorTiers(row: MarketCompareRow): string {
  const native = fmtScoringNum(row.vendor_list_native_in_per_1m)
  const cur = row.vendor_list_currency || '?'
  const cny = fmtScoringNum(row.vendor_list_cny_in_per_1m)
  const usd = fmtScoringNum(usdFromCNY(row.vendor_list_cny_in_per_1m, row.mpr_fx_rate))
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
  return `${fmtScoringNum(inNative)}/${fmtScoringNum(outNative)} ${nativeCur} / ¥${fmtScoringNum(cny)} / $${fmtScoringNum(usd)} · ${pid || '—'}(${count})`
}

// 平台数 <3 ⇒ 该地板降级为参考值（告警 market_ref_thin_coverage 口径）。
function floorCount(row: MarketCompareRow, side: 'overseas' | 'domestic'): number {
  return side === 'overseas' ? row.overseas_min_platform_count : row.domestic_min_platform_count
}

const scoringFields = computed<CardField[]>(() => [
  { key: 'canonical_name', label: t('priceCompare.colModel') },
  { key: 'currency', label: t('priceCompare.colCurrency') },
  { key: 'price_confidence', label: t('priceCompare.colConfidence') },
  { key: 'market_position', label: t('priceCompare.colMarketPosition') },
  { key: 'computed_at', label: t('priceCompare.colComputedAt') },
])

// 懒加载：scoring 首次切入才拉数据（避免进页面即打重端点）。
watch(pageTab, (next) => {
  if (next === 'scoring' && !scoringLoaded.value && !scoringLoading.value) loadScoring()
})

const modeLabelKey: Record<string, string> = {
  pay_as_you_go: 'standardModelPricing.history.modePayAsYouGo',
  token_plan: 'standardModelPricing.history.modeTokenPlan',
  booster_pack: 'standardModelPricing.history.modeBooster',
}

async function openHistory(row: AdminMaasModelRate) {
  historyRow.value = row
  showHistoryModal.value = true
  historyLoading.value = true
  try {
    const res = await getAdminMaasModelPriceHistory(row.canonical_id)
    historyItems.value = res.items || []
  } catch {
    historyItems.value = []
  } finally {
    historyLoading.value = false
  }
}

async function openChanges() {
  showChangesModal.value = true
  changesLoading.value = true
  try {
    const res = await getAdminMaasModelPriceChanges(30, 200)
    changeItems.value = res.items || []
  } catch {
    changeItems.value = []
  } finally {
    changesLoading.value = false
  }
}

function fmtPeriod(v: ModelPriceVersion): string {
  const to = v.effective_to
  if (!to) return t('standardModelPricing.history.current')
  return t('standardModelPricing.history.until', { time: new Date(to).toLocaleString(localeRef.value) })
}

function fmtPrice(v: ModelPriceVersion): string {
  const parts: string[] = []
  if (v.credits_per_1m_in != null) parts.push(`${t('standardModelPricing.input')} ${v.credits_per_1m_in}`)
  if (v.credits_per_1m_out != null) parts.push(`${t('standardModelPricing.output')} ${v.credits_per_1m_out}`)
  if (v.credits_per_1m_cache_in != null) parts.push(`${t('standardModelPricing.cacheRead')} ${v.credits_per_1m_cache_in}`)
  if (v.credits_per_1m_cache_out != null) parts.push(`${t('standardModelPricing.cacheWrite')} ${v.credits_per_1m_cache_out}`)
  return parts.length ? parts.join(' · ') : '—'
}

function emptyEditForm(): MaasModelRateUpsert {
  return {
    credits_per_1m_in: 0,
    credits_per_1m_out: 0,
    credits_per_1m_cache_in: 0,
    credits_per_1m_cache_out: 0,
    manual_in: false,
    manual_out: false,
    manual_cache_in: false,
    manual_cache_out: false,
  }
}

const customCount = computed(() => models.value.filter((m) => m.is_custom).length)
const defaultCount = computed(() => models.value.length - customCount.value)
const multimodalCount = computed(() => models.value.filter((m) => m.manual_image || m.manual_audio || m.manual_video).length)

const discountPercent = computed({
  get: () => Math.round((settings.value?.global_discount ?? 1) * 100),
  set: (v: number) => {
    if (settings.value) settings.value.global_discount = Math.min(100, Math.max(1, v)) / 100
  },
})

function manualCount(m: AdminMaasModelRate) {
  return [m.manual_in, m.manual_out, m.manual_cache_in, m.manual_cache_out, m.manual_image, m.manual_audio, m.manual_video].filter(Boolean).length
}

function fmtCredits(n: number) {
  return n.toLocaleString(localeRef.value)
}

function pricePer1M(credits: number) {
  const st = settings.value
  if (!st || credits <= 0) return '—'
  const yuan = (credits * st.cents_per_credit) / 100
  return `${st.currency_display === 'CNY' ? '¥' : st.currency_display}${yuan.toFixed(4)}`
}

function effectiveGlobal(field: RateField) {
  const st = settings.value
  if (!st) return 0
  const disc = st.global_discount ?? 1
  const pick = (base: number | undefined, fallback: number) =>
    Math.ceil((base && base > 0 ? base : fallback) * disc)
  const inBase = st.base_credits_per_1m_in ?? st.base_credits_per_1m
  switch (field) {
    case 'in':
      return pick(st.base_credits_per_1m_in, inBase)
    case 'out':
      return pick(st.base_credits_per_1m_out, inBase)
    case 'cache_in':
      return pick(st.base_credits_per_1m_cache_in, inBase)
    case 'cache_out':
      return pick(st.base_credits_per_1m_cache_out, inBase)
    case 'image':
      return pick(undefined, inBase)
    case 'audio':
      return pick(undefined, inBase)
    case 'video':
      return pick(undefined, inBase)
  }
}

async function load() {
  loading.value = true
  try {
    const rates = await getAdminMaasModelRates()
    models.value = rates.items ?? []
    settings.value = {
      ...rates.settings,
      base_credits_per_1m_in: rates.settings.base_credits_per_1m_in ?? rates.settings.base_credits_per_1m,
      base_credits_per_1m_out: rates.settings.base_credits_per_1m_out ?? rates.settings.base_credits_per_1m,
      base_credits_per_1m_cache_in: rates.settings.base_credits_per_1m_cache_in ?? rates.settings.base_credits_per_1m,
      base_credits_per_1m_cache_out: rates.settings.base_credits_per_1m_cache_out ?? rates.settings.base_credits_per_1m,
      global_discount: rates.settings.global_discount ?? 1,
    }
  } catch (e: unknown) {
    notifyError(surfaceError(e, { key: 'standardModelPricing.loadFailed' }))
  } finally {
    loading.value = false
  }
}

async function saveSettings() {
  if (!settings.value) return
  savingSettings.value = true
  clearActionMsg()
  try {
    await updateAdminMaasSettings({
      cents_per_credit: settings.value.cents_per_credit,
      base_credits_per_1m_in: settings.value.base_credits_per_1m_in ?? settings.value.base_credits_per_1m,
      base_credits_per_1m_out: settings.value.base_credits_per_1m_out,
      base_credits_per_1m_cache_in: settings.value.base_credits_per_1m_cache_in,
      base_credits_per_1m_cache_out: settings.value.base_credits_per_1m_cache_out,
      global_discount: settings.value.global_discount ?? 1,
      currency_display: settings.value.currency_display,
    })
    notifySuccess(t('standardModelPricing.saved'))
    await load()
  } catch (e: unknown) {
    notifyError(surfaceError(e, { key: 'standardModelPricing.saveFailed' }))
  } finally {
    savingSettings.value = false
  }
}

function openEdit(row: AdminMaasModelRate) {
  editRow.value = row
  editForm.value = {
    credits_per_1m_in: row.manual_in ? (row.custom_credits_per_1m_in ?? row.credits_per_1m_in) : effectiveGlobal('in'),
    credits_per_1m_out: row.manual_out ? (row.custom_credits_per_1m_out ?? row.credits_per_1m_out) : effectiveGlobal('out'),
    credits_per_1m_cache_in: row.manual_cache_in ? (row.custom_credits_per_1m_cache_in ?? row.credits_per_1m_cache_in) : effectiveGlobal('cache_in'),
    credits_per_1m_cache_out: row.manual_cache_out ? (row.custom_credits_per_1m_cache_out ?? row.credits_per_1m_cache_out) : effectiveGlobal('cache_out'),
    credits_per_1m_image_tokens: row.manual_image ? (row.custom_credits_per_1m_image_tokens ?? row.credits_per_1m_image_tokens) : effectiveGlobal('image'),
    credits_per_1m_audio_tokens: row.manual_audio ? (row.custom_credits_per_1m_audio_tokens ?? row.credits_per_1m_audio_tokens) : effectiveGlobal('audio'),
    credits_per_1m_video_tokens: row.manual_video ? (row.custom_credits_per_1m_video_tokens ?? row.credits_per_1m_video_tokens) : effectiveGlobal('video'),
    manual_in: row.manual_in,
    manual_out: row.manual_out,
    manual_cache_in: row.manual_cache_in,
    manual_cache_out: row.manual_cache_out,
    manual_image: row.manual_image,
    manual_audio: row.manual_audio,
    manual_video: row.manual_video,
  }
}

function closeEdit() {
  editRow.value = null
}

// ★ 2026-10-27 第 25 增量（动作区必须可扫读）：编辑弹窗底栏从 5 个并列按钮
//   收到 3 个可见动作 —— ① 明面保留「保存 / 取消」（弹窗的收尾动作）
//   ② 3 个**整表范围**的工具动作（复制价格 / 粘贴 / 填充全局）收进「更多」。
//   ★ 这三个动作的共同点是「一次改动全部 7 个费率字段」⇒ 它们是**表单级**工具，
//   不是某个字段的动作 ⇒ 归到一处比平铺在底栏更贴切。
const editModalTools = computed(() => [
  { key: 'copy', label: '复制价格' },
  { key: 'paste', label: '粘贴', disabled: !clipboard.value },
  { key: 'fillGlobal', label: t('standardModelPricing.editModal.fillGlobal') },
])

function onEditModalTool(key: string) {
  if (key === 'copy') copyEditForm()
  else if (key === 'paste') pasteEditForm()
  else if (key === 'fillGlobal') fillGlobalToEdit()
}

async function saveEdit() {
  if (!editRow.value) return
  if (!editForm.value.manual_in && !editForm.value.manual_out && !editForm.value.manual_cache_in && !editForm.value.manual_cache_out &&
      !editForm.value.manual_image && !editForm.value.manual_audio && !editForm.value.manual_video) {
    notifyError(t('standardModelPricing.needOneManual'))
    return
  }
  savingRow.value = true
  clearActionMsg()
  try {
    await upsertAdminMaasModelRate(editRow.value.canonical_id, { ...editForm.value })
    closeEdit()
    await load()
  } catch (e: unknown) {
    notifyError(surfaceError(e, { key: 'standardModelPricing.saveFailed' }))
  } finally {
    savingRow.value = false
  }
}

async function resetAll(row: AdminMaasModelRate) {
  if (!row.is_custom) return
  if (!(await confirmDialog(`${t('standardModelPricing.editModal.resetConfirm').replace('{name}', row.display_name)}`))) return
  clearActionMsg()
  try {
    await deleteAdminMaasModelRate(row.canonical_id)
    await load()
  } catch (e: unknown) {
    notifyError(surfaceError(e, { key: 'standardModelPricing.resetFailed' }))
  }
}

async function resetField(row: AdminMaasModelRate, field: RateField) {
  clearActionMsg()
  try {
    await resetAdminMaasModelRateFields(row.canonical_id, [field])
    if (editRow.value?.canonical_id === row.canonical_id) closeEdit()
    await load()
  } catch (e: unknown) {
    notifyError(surfaceError(e, { key: 'standardModelPricing.resetFailed' }))
  }
}

function fillGlobalToEdit() {
  editForm.value.credits_per_1m_in = effectiveGlobal('in')
  editForm.value.credits_per_1m_out = effectiveGlobal('out')
  editForm.value.credits_per_1m_cache_in = effectiveGlobal('cache_in')
  editForm.value.credits_per_1m_cache_out = effectiveGlobal('cache_out')
  editForm.value.credits_per_1m_image_tokens = effectiveGlobal('image')
  editForm.value.credits_per_1m_audio_tokens = effectiveGlobal('audio')
  editForm.value.credits_per_1m_video_tokens = effectiveGlobal('video')
}

const textRateFields: { key: 'in' | 'out' | 'cache_in' | 'cache_out'; label: string; formKey: 'credits_per_1m_in' | 'credits_per_1m_out' | 'credits_per_1m_cache_in' | 'credits_per_1m_cache_out'; manualKey: 'manual_in' | 'manual_out' | 'manual_cache_in' | 'manual_cache_out'; valueKey: 'credits_per_1m_in' | 'credits_per_1m_out' | 'credits_per_1m_cache_in' | 'credits_per_1m_cache_out'; manualFlag: 'manual_in' | 'manual_out' | 'manual_cache_in' | 'manual_cache_out' }[] = [
  { key: 'in', label: t('standardModelPricing.input'), formKey: 'credits_per_1m_in', manualKey: 'manual_in', valueKey: 'credits_per_1m_in', manualFlag: 'manual_in' },
  { key: 'out', label: t('standardModelPricing.output'), formKey: 'credits_per_1m_out', manualKey: 'manual_out', valueKey: 'credits_per_1m_out', manualFlag: 'manual_out' },
  { key: 'cache_in', label: t('standardModelPricing.cacheRead'), formKey: 'credits_per_1m_cache_in', manualKey: 'manual_cache_in', valueKey: 'credits_per_1m_cache_in', manualFlag: 'manual_cache_in' },
  { key: 'cache_out', label: t('standardModelPricing.cacheWrite'), formKey: 'credits_per_1m_cache_out', manualKey: 'manual_cache_out', valueKey: 'credits_per_1m_cache_out', manualFlag: 'manual_cache_out' },
]

const multimodalRateFields: { key: 'image' | 'audio' | 'video'; label: string; formKey: 'credits_per_1m_image_tokens' | 'credits_per_1m_audio_tokens' | 'credits_per_1m_video_tokens'; manualKey: 'manual_image' | 'manual_audio' | 'manual_video'; valueKey: 'credits_per_1m_image_tokens' | 'credits_per_1m_audio_tokens' | 'credits_per_1m_video_tokens'; manualFlag: 'manual_image' | 'manual_audio' | 'manual_video' }[] = [
  { key: 'image', label: t('standardModelPricing.field.image'), formKey: 'credits_per_1m_image_tokens', manualKey: 'manual_image', valueKey: 'credits_per_1m_image_tokens', manualFlag: 'manual_image' },
  { key: 'audio', label: t('standardModelPricing.field.audio'), formKey: 'credits_per_1m_audio_tokens', manualKey: 'manual_audio', valueKey: 'credits_per_1m_audio_tokens', manualFlag: 'manual_audio' },
  { key: 'video', label: t('standardModelPricing.field.video'), formKey: 'credits_per_1m_video_tokens', manualKey: 'manual_video', valueKey: 'credits_per_1m_video_tokens', manualFlag: 'manual_video' },
]

const rateFields = computed(() => [...textRateFields, ...multimodalRateFields])

const allSelected = computed(() => {
  return filtered.value.length > 0 && filtered.value.every(row => selectedRows.value.has(row.canonical_id))
})

function toggleRow(canonicalId: number) {
  if (selectedRows.value.has(canonicalId)) {
    selectedRows.value.delete(canonicalId)
  } else {
    selectedRows.value.add(canonicalId)
  }
}

function toggleSelectAll(e: Event) {
  const checked = (e.target as HTMLInputElement).checked
  if (checked) {
    filtered.value.forEach(row => selectedRows.value.add(row.canonical_id))
  } else {
    selectedRows.value.clear()
  }
}

function copyRow(row: AdminMaasModelRate) {
  clipboard.value = {
    in: row.credits_per_1m_in,
    out: row.credits_per_1m_out,
    cache_in: row.credits_per_1m_cache_in,
    cache_out: row.credits_per_1m_cache_out,
    image: row.credits_per_1m_image_tokens,
    audio: row.credits_per_1m_audio_tokens,
    video: row.credits_per_1m_video_tokens,
  }
}

function copyEditForm() {
  clipboard.value = {
    in: editForm.value.credits_per_1m_in,
    out: editForm.value.credits_per_1m_out,
    cache_in: editForm.value.credits_per_1m_cache_in,
    cache_out: editForm.value.credits_per_1m_cache_out,
    image: editForm.value.credits_per_1m_image_tokens ?? 0,
    audio: editForm.value.credits_per_1m_audio_tokens ?? 0,
    video: editForm.value.credits_per_1m_video_tokens ?? 0,
  }
}

function pasteEditForm() {
  if (!clipboard.value) return
  editForm.value.credits_per_1m_in = clipboard.value.in
  editForm.value.credits_per_1m_out = clipboard.value.out
  editForm.value.credits_per_1m_cache_in = clipboard.value.cache_in
  editForm.value.credits_per_1m_cache_out = clipboard.value.cache_out
  editForm.value.credits_per_1m_image_tokens = clipboard.value.image
  editForm.value.credits_per_1m_audio_tokens = clipboard.value.audio
  editForm.value.credits_per_1m_video_tokens = clipboard.value.video
  editForm.value.manual_in = true
  editForm.value.manual_out = true
  editForm.value.manual_cache_in = true
  editForm.value.manual_cache_out = true
  editForm.value.manual_image = true
  editForm.value.manual_audio = true
  editForm.value.manual_video = true
}

async function pasteToSelected() {
  if (!clipboard.value || selectedRows.value.size === 0) return
  savingBatch.value = true
  batchMsg.value = ''
  try {
    const updates = Array.from(selectedRows.value).map(canonical_id => ({
      canonical_id,
      credits_per_1m_in: clipboard.value!.in,
      credits_per_1m_out: clipboard.value!.out,
      credits_per_1m_cache_in: clipboard.value!.cache_in,
      credits_per_1m_cache_out: clipboard.value!.cache_out,
      credits_per_1m_image_tokens: clipboard.value!.image,
      credits_per_1m_audio_tokens: clipboard.value!.audio,
      credits_per_1m_video_tokens: clipboard.value!.video,
      manual_in: true,
      manual_out: true,
      manual_cache_in: true,
      manual_cache_out: true,
      manual_image: true,
      manual_audio: true,
      manual_video: true,
    }))
    const res = await batchUpsertAdminMaasModelRates(updates)
    batchMsg.value = t('standardModelPricing.batch.msgPasted').replace('{n}', String(res.updated))
    selectedRows.value.clear()
    await load()
  } catch (e: unknown) {
    batchMsg.value = t('standardModelPricing.batch.msgFailed')
  } finally {
    savingBatch.value = false
  }
}

function openBatchModal() {
  batchForm.value = {
    credits_per_1m_in: 0,
    credits_per_1m_out: 0,
    credits_per_1m_cache_in: 0,
    credits_per_1m_cache_out: 0,
    credits_per_1m_image_tokens: 0,
    credits_per_1m_audio_tokens: 0,
    credits_per_1m_video_tokens: 0,
  }
  showBatchModal.value = true
}

async function applyBatch() {
  if (selectedRows.value.size === 0) return
  savingBatch.value = true
  batchMsg.value = ''
  try {
    const updates = Array.from(selectedRows.value).map(canonical_id => ({
      canonical_id,
      credits_per_1m_in: batchForm.value.credits_per_1m_in,
      credits_per_1m_out: batchForm.value.credits_per_1m_out,
      credits_per_1m_cache_in: batchForm.value.credits_per_1m_cache_in,
      credits_per_1m_cache_out: batchForm.value.credits_per_1m_cache_out,
      credits_per_1m_image_tokens: batchForm.value.credits_per_1m_image_tokens,
      credits_per_1m_audio_tokens: batchForm.value.credits_per_1m_audio_tokens,
      credits_per_1m_video_tokens: batchForm.value.credits_per_1m_video_tokens,
      manual_in: true,
      manual_out: true,
      manual_cache_in: true,
      manual_cache_out: true,
      manual_image: true,
      manual_audio: true,
      manual_video: true,
    }))
    const res = await batchUpsertAdminMaasModelRates(updates)
    batchMsg.value = t('standardModelPricing.batch.msgPasted').replace('{n}', String(res.updated))
    showBatchModal.value = false
    selectedRows.value.clear()
    await load()
  } catch (e: unknown) {
    batchMsg.value = t('standardModelPricing.batch.msgFailed')
  } finally {
    savingBatch.value = false
  }
}

async function batchResetAll() {
  const ids = Array.from(selectedRows.value)
  if (ids.length === 0) return
  if (!(await confirmDialog(t('standardModelPricing.batch.resetAllConfirm').replace('{n}', String(ids.length))))) return
  savingBatch.value = true
  batchMsg.value = ''
  try {
    const res = await batchResetAdminMaasModelRates(ids.map(canonical_id => ({ canonical_id, fields: ['all'] })))
    batchMsg.value = t('standardModelPricing.batch.msgReset').replace('{n}', String(res.updated))
    selectedRows.value.clear()
    await load()
  } catch (e: unknown) {
    batchMsg.value = t('standardModelPricing.batch.msgFailed')
  } finally {
    savingBatch.value = false
  }
}

async function batchFillGlobal() {
  const ids = Array.from(selectedRows.value)
  if (ids.length === 0) return
  if (!(await confirmDialog(t('standardModelPricing.batch.fillGlobalConfirm').replace('{n}', String(ids.length))))) return
  savingBatch.value = true
  batchMsg.value = ''
  try {
    const res = await batchFillGlobalAdminMaasModelRates(ids.map(canonical_id => ({ canonical_id, all_dimensions: true })))
    batchMsg.value = t('standardModelPricing.batch.msgFilled').replace('{n}', String(res.updated))
    selectedRows.value.clear()
    await load()
  } catch (e: unknown) {
    batchMsg.value = t('standardModelPricing.batch.msgFailed')
  } finally {
    savingBatch.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="pricing-page">
    <div class="page-header">
      <h2>{{ t('standardModelPricing.page.title') }}</h2>
      <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
        {{ loading ? t('standardModelPricing.page.refreshLoading') : t('standardModelPricing.page.refresh') }}
      </button>
    </div>

    <p class="page-desc" v-html="t('standardModelPricing.desc')"></p>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="settingsMsg" class="alert alert-success">{{ settingsMsg }}</div>

    <!-- 2026-11-19（比较与定价优化轮）：全局基准折叠 -->
    <details class="collapsible-section" open>
      <summary class="section-summary">
        <h3 class="section-title">{{ t('standardModelPricing.settings.title') }}</h3>
        <span class="toggle-hint">{{ globalBaseFolded ? '展开' : '收起' }}</span>
      </summary>
      <div v-if="settings" class="card settings-card">
        <div class="settings-grid">
          <label class="field">
            <span class="field-label">{{ t('standardModelPricing.settings.inputToken') }}</span>
            <input v-model.number="settings.base_credits_per_1m_in" type="number" min="1" class="input compact" />
            <span class="field-hint">{{ t('standardModelPricing.settings.effective', { value: fmtCredits(effectiveGlobal('in')) }) }}</span>
          </label>
          <label class="field">
            <span class="field-label">{{ t('standardModelPricing.settings.outputToken') }}</span>
            <input v-model.number="settings.base_credits_per_1m_out" type="number" min="1" class="input compact" />
            <span class="field-hint">{{ t('standardModelPricing.settings.effective', { value: fmtCredits(effectiveGlobal('out')) }) }}</span>
          </label>
          <label class="field">
            <span class="field-label">{{ t('standardModelPricing.settings.cacheReadToken') }}</span>
            <input v-model.number="settings.base_credits_per_1m_cache_in" type="number" min="1" class="input compact" />
            <span class="field-hint">{{ t('standardModelPricing.settings.effective', { value: fmtCredits(effectiveGlobal('cache_in')) }) }}</span>
          </label>
          <label class="field">
            <span class="field-label">{{ t('standardModelPricing.settings.cacheWriteToken') }}</span>
            <input v-model.number="settings.base_credits_per_1m_cache_out" type="number" min="1" class="input compact" />
            <span class="field-hint">{{ t('standardModelPricing.settings.effective', { value: fmtCredits(effectiveGlobal('cache_out')) }) }}</span>
          </label>
          <label class="field">
            <span class="field-label">{{ t('standardModelPricing.settings.discount') }}</span>
            <div class="discount-row">
              <input v-model.number="discountPercent" type="range" min="10" max="100" step="1" class="discount-slider" />
              <span class="discount-val">{{ discountPercent }}%</span>
            </div>
            <span class="field-hint">{{ t('standardModelPricing.settings.discountHint') }}</span>
          </label>
          <label class="field">
            <span class="field-label">{{ t('standardModelPricing.settings.centsPerCredit') }}</span>
            <input v-model.number="settings.cents_per_credit" type="number" min="0.0001" step="0.0001" class="input compact" />
          </label>
          <label class="field">
            <span class="field-label">{{ t('standardModelPricing.settings.currencyDisplay') }}</span>
            <select v-model="settings.currency_display" class="input compact">
              <option value="CNY">CNY</option>
              <option value="USD">USD</option>
            </select>
          </label>
        </div>
        <div class="settings-actions">
          <button class="btn btn-primary btn-sm" :disabled="savingSettings" @click="saveSettings">
            {{ savingSettings ? t('standardModelPricing.settings.saving') : t('standardModelPricing.settings.save') }}
          </button>
          <span class="cf-meta">
            {{ t('standardModelPricing.filter.summary', { custom: customCount, defaults: defaultCount, total: models.length }) }}
            <span v-if="multimodalCount > 0" class="multimodal-tag"> · {{ multimodalCount }} 含多模态手工</span>
          </span>
        </div>
      </div>
    </details>

    <!-- rates tab 使用原有的 ModelCatalogFilterBar -->
    <ModelCatalogFilterBar
      v-if="pageTab === 'rates'"
      v-model:picked-model="pickedModel"
      v-model:filter-vendor="filterVendor"
      v-model:extra-filter="filterMode"
      :vendor-options="filteredVendorOptions"
      :count="filtered.length"
      picker-title="定价管理 · 模型筛选"
      picker-placeholder="搜索标准模型…"
      status-label="全部定价"
      :status-options="pricingStatusOptions"
      @clear="clearCatalogFilters"
    />

    <!-- 2026-11-19（比较与定价优化轮）：全局模型多选过滤器（影响 reference/scoring/platforms/suggestion 四个 tab） -->
    <div v-if="pageTab !== 'rates'" class="card global-model-filter">
      <div class="filter-label">{{ t('standardModelPricing.globalModelFilter.label') }}</div>
      <div class="model-picker-wrapper">
        <ModelPicker
          v-model="globalSelectedModels"
          mode="multi"
          :placeholder="t('standardModelPricing.globalModelFilter.placeholder')"
          :title="t('standardModelPricing.globalModelFilter.title')"
          compact
          @update:model-value="onGlobalModelsChange"
        />
      </div>
      <button 
        v-if="globalSelectedModels.length > 0" 
        class="btn btn-ghost btn-sm" 
        @click="clearGlobalModels"
      >
        {{ t('common.clear') }}
      </button>
    </div>

    <div v-if="pageTab === 'rates'" class="bulk-bar">
      <button class="btn btn-ghost btn-sm" @click="openChanges">{{ t('standardModelPricing.history.openChanges') }}</button>
    </div>
    <!-- 2026-11-19（合并轮）：页面级 tab 行 —— 原「价格对比分析」页四 tab 并入；rates=本页定价管理主体。 -->
    <div class="card tab-row" data-testid="page-tabs">
      <button v-for="pt in PAGE_TAB_DEFS" :key="pt.key" class="btn btn-sm"
        :class="pageTab === pt.key ? 'btn-primary' : 'btn-ghost'"
        :data-testid="`page-tab-${pt.key}`"
        @click="switchPageTab(pt.key)">{{ t(pt.labelKey) }}</button>
    </div>

    <template v-if="pageTab !== 'rates'">
      <MarketReferenceTable v-if="pageTab === 'reference'" :global-models="globalSelectedModels" />
      <ModelPlatformCompare v-else-if="pageTab === 'platforms'" :global-models="globalSelectedModels" />
      <PricingSuggestionPanel v-else-if="pageTab === 'suggestion'" :global-models="globalSelectedModels" />
      <template v-else>
        <!-- scoring（供应评分）：覆盖率摘要 + 筛选 + 评分行 × 双地板（自 PriceCompareView 平移）
             2026-11-19：移除本区域的模型选择器，改用全局模型过滤 -->
        <div class="card summary-card">
          <div class="summary-row">
            <span class="badge badge-gray">{{ t('priceCompare.totalBindings') }}: {{ scoringBindingsTotal }}</span>
            <span class="badge badge-green">{{ t('priceCompare.confTrusted') }}: {{ scoringConfCount('trusted') }}</span>
            <span class="badge badge-yellow">{{ t('priceCompare.confSuspect') }}: {{ scoringConfCount('suspect_symmetric') }}</span>
            <span class="badge badge-gray">{{ t('priceCompare.confEmpty') }}: {{ scoringConfCount('empty') }}</span>
            <span class="badge badge-red">{{ t('priceCompare.confUnknown') }}: {{ scoringConfCount('unknown_currency') }}</span>
          </div>
          <p class="hint-line">{{ t('priceCompare.summaryHint') }}</p>
        </div>
        <div class="card filter-card">
          <select v-model="scoreConfFilter" class="filter-select">
            <option value="">{{ t('priceCompare.confAll') }}</option>
            <option v-for="c in scoringConfFilters" :key="c" :value="c">{{ scoringConfLabel(c) }}</option>
          </select>
          <button class="btn btn-primary btn-sm" @click="reloadScoring">{{ t('priceCompare.apply') }}</button>
        </div>
        <div v-if="!scoringAvailable" class="card table-wrap">
          <div class="app-empty-state">
            <p>{{ t('priceCompare.unavailableWhy') }}</p>
            <p class="empty-next mono-sm">{{ scoringUnavailableReason }}</p>
            <p class="empty-next">{{ t('priceCompare.unavailableNext') }}</p>
          </div>
        </div>
        <div v-else-if="!scoreRows.length && !scoringLoading" class="card table-wrap">
          <div class="app-empty-state">
            <p>{{ t('priceCompare.emptyWhy') }}</p>
            <p class="empty-next">{{ t('priceCompare.emptyNext') }}</p>
          </div>
        </div>
        <div v-else class="card table-wrap">
          <div class="table-head-row">
            <h3 class="section-title">{{ t('priceCompare.tableTitle') }}</h3>
            <span class="badge badge-gray">{{ scoreRows.length }}</span>
          </div>
          <ResponsiveDataView :rows="scoreRows" title-key="canonical_name" :title-format="row => String(row.canonical_name)" :fields="scoringFields" :loading="scoringLoading" :empty="false">
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
            <tr v-for="r in scoreRows" :key="`${r.credential_id}-${r.provider_model_id}`">
              <td><code class="mono-sm">{{ r.canonical_name }}</code></td>
              <td class="num">{{ r.credential_id }}</td>
              <td>{{ r.currency }}</td>
              <td class="num">{{ fmtScoringNum(r.supplier_in_per_1m) }} / {{ fmtScoringNum(r.supplier_out_per_1m) }}</td>
              <td><span class="badge" :class="SCORING_CONF_CLASSES[r.price_confidence] || 'badge-gray'">{{ r.price_confidence }}</span></td>
              <td class="num">{{ fmtScoringNum(r.discount_vs_vendor) }}</td>
              <td class="num">{{ fmtScoringNum(r.market_position) }}</td>
              <td class="num">{{ fmtScoringNum(r.market_ref_in_per_1m) }} / {{ fmtScoringNum(r.market_ref_out_per_1m) }} <span class="badge badge-gray">{{ r.market_ref_used || '—' }}</span> {{ r.market_ref_platform_id }}({{ r.market_ref_platform_count }})</td>
              <td class="num tiers-cell" :class="{ 'thin-floor': floorCount(r, 'overseas') < 3 }">{{ floorTiers(r, 'overseas') }}</td>
              <td class="num tiers-cell" :class="{ 'thin-floor': floorCount(r, 'domestic') < 3 }">{{ floorTiers(r, 'domestic') }}</td>
              <td class="num">{{ fmtScoringNum(r.market_position_vs_overseas) }}</td>
              <td class="num">{{ fmtScoringNum(r.market_position_vs_domestic) }}</td>
              <td class="num">{{ fmtScoringNum(r.market_advantage_score, 2) }}</td>
              <td class="num tiers-cell">{{ anchorTiers(r) }}</td>
              <td class="mono-sm">{{ (r.computed_at || '').replace('T', ' ').replace('Z', ' UTC') }}</td>
            </tr>
            </tbody>
            </table>
            </template>
            <template #cards="{ row }">
            <div class="compare-card">
              <p><b>{{ row.canonical_name }}</b> <span class="badge" :class="SCORING_CONF_CLASSES[row.price_confidence] || 'badge-gray'">{{ row.price_confidence }}</span></p>
              <p class="hint-line">{{ t('priceCompare.colSupplier') }}: {{ fmtScoringNum(row.supplier_in_per_1m) }} / {{ fmtScoringNum(row.supplier_out_per_1m) }} {{ row.currency }}</p>
              <p class="hint-line">{{ t('priceCompare.colMarketPosition') }}: {{ fmtScoringNum(row.market_position) }}</p>
              <p class="hint-line">{{ t('priceCompare.colAnchor') }}: {{ anchorTiers(row) }}</p>
              <p class="hint-line">{{ t('priceCompare.colOverseasFloor') }}: {{ floorTiers(row, 'overseas') }}</p>
              <p class="hint-line">{{ t('priceCompare.colDomesticFloor') }}: {{ floorTiers(row, 'domestic') }}</p>
            </div>
            </template>
          </ResponsiveDataView>
          <p class="hint-line">{{ t('priceCompare.tableHint') }}</p>
        </div>
      </template>
    </template>
    <template v-else>

    <div class="card table-wrap">
      <table class="data-table pricing-table">
        <thead>
          <tr>
            <th><input type="checkbox" :checked="allSelected" @change="toggleSelectAll" /></th>
            <th>{{ t('standardModelPricing.table.colModel') }}</th>
            <th>{{ t('standardModelPricing.table.colModality') }}</th>
            <th v-for="f in textRateFields" :key="f.key" class="num">{{ f.label }}</th>
            <th>{{ t('standardModelPricing.table.colStatus') }}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!loading && filtered.length === 0">
            <td :colspan="5 + textRateFields.length" class="empty-cell">{{ t('standardModelPricing.table.empty') }}</td>
          </tr>
          <tr v-for="row in sortedFiltered" :key="row.canonical_id" :class="{ selected: selectedRows.has(row.canonical_id) }">
            <td><input type="checkbox" :checked="selectedRows.has(row.canonical_id)" @change="toggleRow(row.canonical_id)" /></td>
            <td>
              <div class="model-name">{{ row.display_name }}</div>
              <code class="mono-sm">{{ row.canonical_name }}</code>
            </td>
            <td>
              <span class="modality-badge" :class="'modality-' + (row.modality || 'other')">{{ modalityLabel(row) }}</span>
              <span v-if="row.manual_image || row.manual_audio || row.manual_video" class="multimodal-tag" :title="t('standardModelPricing.table.hasMultimodal')">MM</span>
            </td>
            <td v-for="f in textRateFields" :key="f.key" class="num rate-cell">
              <span>{{ fmtCredits(row[f.valueKey]) }}</span>
              <span v-if="row[f.manualFlag]" class="manual-tag" :title="t('standardModelPricing.table.manualTagTitle')">{{ t('standardModelPricing.table.manualTag') }}</span>
            </td>
            <td>
              <span v-if="!row.is_custom" class="badge badge-gray">{{ t('standardModelPricing.table.statusDefault') }}</span>
              <span v-else-if="isFullyManual(row)" class="badge badge-blue">{{ t('standardModelPricing.table.statusFullCustom') }}</span>
              <span v-else class="badge badge-yellow">{{ t('standardModelPricing.table.statusPartialCustom', { n: manualCount(row) }) }}</span>
            </td>
            <td class="actions">
              <button class="btn btn-ghost btn-sm" @click="openEdit(row)">{{ t('standardModelPricing.table.edit') }}</button>
              <button class="btn btn-ghost btn-sm" @click="copyRow(row)">复制</button>
              <button class="btn btn-ghost btn-sm" @click="openHistory(row)">{{ t('standardModelPricing.history.open') }}</button>
              <button v-if="row.is_custom" class="btn btn-ghost btn-sm" @click="resetAll(row)">{{ t('standardModelPricing.table.reset') }}</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="selectedRows.size > 0" class="bulk-bar">
      <span>{{ t('standardModelPricing.batch.selected', { n: selectedRows.size }) }}</span>
      <button class="btn btn-ghost btn-sm" :disabled="!clipboard" @click="pasteToSelected">{{ t('standardModelPricing.batch.paste') }}</button>
      <button class="btn btn-primary btn-sm" @click="openBatchModal">{{ t('standardModelPricing.batch.price') }}</button>
      <button class="btn btn-ghost btn-sm" @click="batchFillGlobal">{{ t('standardModelPricing.batch.fillGlobal') }}</button>
      <button class="btn btn-ghost btn-sm" @click="batchResetAll">{{ t('standardModelPricing.batch.resetAll') }}</button>
      <span v-if="batchMsg" class="batch-msg">{{ batchMsg }}</span>
    </div>

    <div v-if="showBatchModal" class="modal-backdrop" @click.self="showBatchModal = false">
      <div class="modal card">
        <h3 class="section-title">{{ t('standardModelPricing.batch.price') }} · {{ selectedRows.size }} 个模型</h3>
        <p class="modal-hint">统一设置以下积分值，所有勾选模型的手工标记将全部开启。</p>
        <div class="edit-grid">
          <div v-for="f in textRateFields" :key="f.key" class="edit-field">
            <label class="edit-head">
              <span>{{ f.label }}</span>
            </label>
            <input
              v-model.number="batchForm[f.formKey]"
              type="number"
              min="1"
              class="input compact"
            />
          </div>
          <div v-for="f in multimodalRateFields" :key="f.key" class="edit-field">
            <label class="edit-head">
              <span>{{ f.label }}</span>
            </label>
            <input
              v-model.number="batchForm[f.formKey]"
              type="number"
              min="1"
              class="input compact"
            />
          </div>
        </div>
        <div class="modal-actions">
          <button class="btn btn-primary btn-sm" :disabled="savingBatch" @click="applyBatch">
            {{ savingBatch ? '保存中…' : '应用到所选' }}
          </button>
          <button class="btn btn-ghost btn-sm" @click="showBatchModal = false">取消</button>
        </div>
      </div>
    </div>

    <div v-if="showHistoryModal && historyRow" class="modal-backdrop" @click.self="showHistoryModal = false">
      <div class="modal card">
        <h3 class="section-title">{{ t('standardModelPricing.history.title') }} · {{ historyRow.display_name }}</h3>
        <code class="mono-sm modal-code">{{ historyRow.canonical_name }}</code>
        <div v-if="historyLoading" class="modal-hint">…</div>
        <p v-else-if="historyItems.length === 0" class="modal-hint">{{ t('standardModelPricing.history.empty') }}</p>
        <ul v-else class="price-history-list">
          <li v-for="v in historyItems" :key="v.billing_mode + v.effective_from" class="price-history-item">
            <div class="ph-head">
              <span class="ph-mode-label">{{ t('standardModelPricing.history.mode') }}:</span>
              <span class="badge badge-blue">{{ t(modeLabelKey[v.billing_mode] || 'standardModelPricing.history.modePayAsYouGo') }}</span>
              <span class="ph-period">{{ fmtPeriod(v) }}</span>
              <span class="ph-source">{{ t('standardModelPricing.history.source') }}: {{ v.source }}</span>
            </div>
            <div class="ph-prices">{{ fmtPrice(v) }}</div>
          </li>
        </ul>
        <div class="modal-actions">
          <button class="btn btn-ghost btn-sm" @click="showHistoryModal = false">OK</button>
        </div>
      </div>
    </div>

    <div v-if="showChangesModal" class="modal-backdrop" @click.self="showChangesModal = false">
      <div class="modal card">
        <h3 class="section-title">{{ t('standardModelPricing.history.changesTitle') }}</h3>
        <div v-if="changesLoading" class="modal-hint">…</div>
        <p v-else-if="changeItems.length === 0" class="modal-hint">{{ t('standardModelPricing.history.empty') }}</p>
        <ul v-else class="price-history-list">
          <li v-for="v in changeItems" :key="v.canonical_id + v.billing_mode + v.effective_from" class="price-history-item">
            <div class="ph-head">
              <span class="ph-model">{{ v.display_name || v.canonical_name }}</span>
              <span class="ph-mode-label">{{ t('standardModelPricing.history.mode') }}:</span>
              <span class="badge badge-blue">{{ t(modeLabelKey[v.billing_mode] || 'standardModelPricing.history.modePayAsYouGo') }}</span>
              <span class="ph-period">{{ new Date(v.effective_from).toLocaleString(localeRef) }}</span>
            </div>
            <div class="ph-prices">{{ fmtPrice(v) }}</div>
          </li>
        </ul>
        <div class="modal-actions">
          <button class="btn btn-ghost btn-sm" @click="showChangesModal = false">OK</button>
        </div>
      </div>
    </div>


    <div v-if="editRow" class="modal-backdrop" @click.self="closeEdit">
      <div class="modal card">
        <h3 class="section-title">{{ t('standardModelPricing.editModal.title', { name: editRow.display_name }) }}</h3>
        <code class="mono-sm modal-code">{{ editRow.canonical_name }} · {{ modalityLabel(editRow) }}</code>
        <p class="modal-hint">{{ t('standardModelPricing.editModal.hint') }}</p>

        <h4 class="section-sub">{{ t('standardModelPricing.editModal.sectionText') }}</h4>
        <div class="edit-grid">
          <div v-for="f in textRateFields" :key="f.key" class="edit-field">
            <label class="edit-head">
              <input v-model="editForm[f.manualKey]" type="checkbox" />
              <span>{{ f.label }}{{ t('standardModelPricing.editModal.fieldSuffix') }}</span>
              <button
                v-if="editRow[f.manualFlag]"
                type="button"
                class="link-sm"
                @click="resetField(editRow, f.key)"
              >{{ t('standardModelPricing.editModal.resetBase') }}</button>
            </label>
            <input
              v-model.number="editForm[f.formKey]"
              type="number"
              min="1"
              class="input compact"
              :disabled="!editForm[f.manualKey]"
            />
            <span class="field-hint">{{ t('standardModelPricing.editModal.globalApprox', { value: fmtCredits(effectiveGlobal(f.key)) }) }}</span>
          </div>
        </div>

        <h4 class="section-sub">
          {{ t('standardModelPricing.editModal.sectionMultimodal') }}
          <span v-if="!isMultimodal(editRow)" class="section-hint">(本模型按多模态记，仍可手工设置未来值)</span>
        </h4>
        <div class="edit-grid">
          <div v-for="f in multimodalRateFields" :key="f.key" class="edit-field">
            <label class="edit-head">
              <input v-model="editForm[f.manualKey]" type="checkbox" />
              <span>{{ f.label }}{{ t('standardModelPricing.editModal.fieldSuffix') }}</span>
              <button
                v-if="editRow[f.manualFlag]"
                type="button"
                class="link-sm"
                @click="resetField(editRow, f.key)"
              >{{ t('standardModelPricing.editModal.resetBase') }}</button>
            </label>
            <input
              v-model.number="editForm[f.formKey]"
              type="number"
              min="1"
              class="input compact"
              :disabled="!editForm[f.manualKey]"
            />
            <span class="field-hint">{{ t('standardModelPricing.editModal.globalApprox', { value: fmtCredits(effectiveGlobal(f.key)) }) }}</span>
          </div>
        </div>

        <div class="modal-actions">
          <button class="btn btn-primary btn-sm" :disabled="savingRow" @click="saveEdit">
            {{ savingRow ? t('standardModelPricing.editModal.saving') : t('standardModelPricing.editModal.save') }}
          </button>
          <button class="btn btn-ghost btn-sm" type="button" @click="closeEdit">{{ t('standardModelPricing.editModal.cancel') }}</button>
          <!-- ★ 第 25 增量：3 个表单级工具收进「更多」（复制价格 / 粘贴 / 填充全局） -->
          <ActionOverflowMenu :items="editModalTools" @select="onEditModalTool" />
 </div>
 </div>
 </div>
 </template>
 </div>
</template>

<style scoped>
.page-desc { color: var(--muted); font-size: 13px; margin: -8px 0 16px; line-height: 1.6; }
.section-title { font-size: 14px; font-weight: 600; margin: 0 0 12px; }

/* 2026-11-19：全局基准折叠样式 */
.collapsible-section {
  margin-bottom: 1rem;
}
.section-summary {
  cursor: pointer;
  list-style: none;
  padding: 0.75rem 1rem;
  background: var(--card-bg);
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  display: flex;
  justify-content: space-between;
  align-items: center;
  transition: background 0.2s;
}
.section-summary:hover {
  background: var(--hover-bg);
}
.section-summary::-webkit-details-marker {
  display: none;
}
.toggle-hint {
  font-size: 0.875rem;
  color: var(--text-secondary);
}

/* 2026-11-19：全局模型过滤器样式 */
.global-model-filter {
  display: flex;
  align-items: center;
  gap: 1rem;
  padding: 1rem;
  margin-bottom: 1rem;
}
.filter-label {
  font-weight: 500;
  white-space: nowrap;
  font-size: 14px;
}
.model-picker-wrapper {
  flex: 1;
  min-width: 300px;
}

.settings-card { margin-bottom: 16px; }
.settings-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
}
@media (max-width: 1024px) { .settings-grid { grid-template-columns: repeat(2, 1fr); } }
@media (max-width: 640px) { .settings-grid { grid-template-columns: 1fr; } }
.field { display: flex; flex-direction: column; gap: 4px; }
.field-label { font-size: 12px; color: var(--muted); }
.field-hint { font-size: 11px; color: var(--muted); }
.input.compact { width: 100%; max-width: 100%; }
.discount-row { display: flex; align-items: center; gap: 8px; }
.discount-slider { flex: 1; }
.discount-val { font-size: 13px; font-weight: 600; min-width: 42px; }
.settings-actions {
  display: flex; align-items: center; gap: 12px;
  margin-top: 16px; padding-top: 12px; border-top: 1px solid var(--border);
}
.table-wrap { overflow-x: auto; }
.pricing-table { width: 100%; font-size: 13px; min-width: 960px; }
.model-name { font-weight: 600; }
.mono-sm { font-family: ui-monospace, Menlo, monospace; font-size: 11px; color: var(--muted); }
.cell-muted { color: var(--muted); }
.num { text-align: right; font-variant-numeric: tabular-nums; }
.rate-cell { white-space: nowrap; }
.manual-tag {
  display: inline-block; margin-left: 4px; padding: 0 4px;
  font-size: 11px; border-radius: 4px;
  background: var(--info-bg); color: var(--accent);
}
.modality-badge {
  display: inline-block; padding: 2px 8px; border-radius: 8px;
  font-size: 11px; font-weight: 500; margin-right: 4px;
  background: rgba(148, 163, 184, 0.18); color: var(--muted);
}
.modality-badge.modality-multimodal { background: color-mix(in srgb, var(--magenta) 18%, transparent); color: var(--purple); }
.modality-badge.modality-vision { background: var(--success-bd); color: var(--success); }
.modality-badge.modality-audio { background: var(--warning-bd); color: var(--warning); }
.modality-badge.modality-video { background: rgba(244, 63, 94, 0.18); color: var(--danger); }
.modality-badge.modality-embedding { background: color-mix(in srgb, var(--accent) 18%, transparent); color: var(--accent-h); }
.multimodal-tag {
  display: inline-block; padding: 1px 4px; border-radius: 6px;
  font-size: 11px; font-weight: 600;
  background: color-mix(in srgb, var(--magenta) 18%, transparent); color: var(--purple);
  margin-left: 2px;
}
.section-sub { font-size: 12px; font-weight: 600; margin: 12px 0 8px; color: var(--muted); }
.section-hint { font-weight: 400; color: var(--muted); margin-left: 4px; }
.actions { white-space: nowrap; display: flex; gap: 4px; justify-content: flex-end; }
.empty-cell { text-align: center; color: var(--muted); padding: 24px; }
.badge { padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-gray { background: var(--neutral-bg); color: var(--muted); }
.badge-blue { background: var(--info-bg); color: var(--accent); }
.badge-yellow { background: var(--warning-bg); color: var(--warning); }
.modal-backdrop {
  position: fixed; inset: 0; z-index: 100;
  background: var(--overlay-medium); display: flex; align-items: center; justify-content: center; padding: 16px;
}
.modal { width: min(560px, 100%); padding: 16px; max-height: 90vh; overflow-y: auto; }
.modal-code { display: block; margin-bottom: 8px; }
.modal-hint { font-size: 12px; color: var(--muted); margin: 0 0 16px; }
.edit-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
@media (max-width: 480px) { .edit-grid { grid-template-columns: 1fr; } }
.edit-field { display: flex; flex-direction: column; gap: 4px; }
.edit-head { display: flex; align-items: center; gap: 8px; font-size: 13px; }
.link-sm { font-size: 11px; margin-left: auto; background: none; border: none; color: var(--accent-h); cursor: pointer; }
.modal-actions { display: flex; gap: 8px; margin-top: 16px; padding-top: 12px; border-top: 1px solid var(--border); }
.modal-wide { width: min(1100px, 92vw); }
.badge-red { background: #d9302514; color: #d93025; }
.price-history-list { list-style: none; margin: 0; padding: 0; max-height: 420px; overflow-y: auto; }
.price-history-item { padding: 8px 0; border-bottom: 1px solid var(--kx-border); }
.price-history-item:last-child { border-bottom: none; }
.ph-head { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.ph-model { font-weight: 600; }
.ph-period { color: var(--kx-text-secondary); font-size: 12px; }
.ph-source { color: var(--kx-text-secondary); font-size: 12px; }
.ph-prices { margin-top: 4px; font-size: 13px; }
.bulk-bar {
  display: flex; align-items: center; gap: 8px;
  padding: 8px 12px; background: var(--bg-subtle); border-radius: 8px;
  margin-top: 12px; font-size: 13px;
  color: var(--text);
}
.batch-msg { font-size: 12px; color: var(--success); }
tr.selected { background: var(--bg-secondary); }
.modal { background: var(--card); color: var(--text); border: 1px solid var(--border); border-radius: var(--radius); }
</style>
