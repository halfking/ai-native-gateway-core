<script setup lang="ts">
// StatsPanel.vue — 头部区间统计面板（2026-10-28 头部统计轮；2026-10-29 单一统计轮扩三组）
//
// owner 口径：所有统计放页面上部、可折叠展开；左下统计栏（TodaySummaryRail）
// 与下方 SSE 三块汇总（SummaryHeader）先后退役 —— **全页只有这一份统计**：
// 客户端 + 供应商 + 模型三组 + 总量行，区间 5分钟/1小时/当天。
// 客户端组的数据源与供应商/模型组不同（见 load 注释），口径行对用户说清。
//
// 区间口径（必须对用户说清，不许混）：
//   · 当天 = stats_usage_daily 日桶（providers-today / models-today 既有口径，
//     rollup 每小时重建 ⇒ 水位滞后 ≤1h，口径行显式展示）；
//   · 5 分钟 / 1 小时 = 同一端点 ?window= 直查 request_logs（实时、无 rollup
//     滞后）。★ 绝不从实时流（SSE）聚合短窗口：Redis 快照每泳道只留
//     LiveStreamLaneVisibleLimit=100 条，高峰期聚合出的「1 小时」系统性偏低
//     —— 把低估说成统计是本仓反复吃过的亏。
//
// 面板结构：
//   · 总量行（请求数/成功率/输入/输出/缓存/失败）**永远可见** —— 折叠只收
//     明细两组；把总账藏起来，面板就失去了「有地方显示所有请求数」的意义。
//   · 口径行以**响应自己的 window 字段**为准（数据说自己是什么口径），
//     不以当前选中的区间为准 —— 切换瞬间数据未到时不会把旧数据标成新口径。
//   · 明细两组与图、左右面板收敛到同一过滤子集（点行联动过滤）。
//
// 刷新节奏（2026-10-29 统计同步轮）：轮询是兜底（短窗口 30s / 日桶 60s，
// 且后台标签页跳过 tick），「新请求落地 ⇒ 统计同步」由 dataRevision
// （liveStreamStore.liveDataRevision）驱动：尾沿防抖 4s 合流突发，后台
// 标签页只记账、回前台补拉 —— 秒级同步且不给端点加突发压力。

import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { CaretRight } from '@element-plus/icons-vue'
import {
  getClientsToday,
  getProvidersToday,
  getTopologyModelsToday,
  type TopologyClientsToday,
  type TopologyModelsToday,
  type TopologyProvidersToday,
} from '../../api/topology'
import { clientDisplayName } from '../../lib/liveTopologyGraph'

const props = defineProps<{
  /** 当前过滤的客户端身份键（空 = 不过滤）。面板与图收敛到同一子集。 */
  activeClient?: string
  /** 当前过滤的供应商 code（空 = 不过滤）。 */
  activeProvider?: string
  /** 当前过滤的模型 canonical 名（空 = 不过滤）。 */
  activeModel?: string
  /** 实时在途数（拓扑窗口口径，来自网关核心节点）——null/undefined = 未测量不显示。 */
  inFlight?: number | null
  /** 转发队列深度（未接线时 null —— 隐藏，不冒充 0）。 */
  queueDepth?: number | null
  /** 实时流数据修订号（liveStreamStore.liveDataRevision）：请求集合每变动
   *  一次 +1。2026-10-29 统计同步轮：统计是 DB 直查口径，此前只靠轮询 ⇒
   *  新请求进来图和面板立刻动、统计最多滞后一个轮询周期。现在 watch 本号
   *  触发**尾沿防抖**刷新 —— 突发合流成一次拉取，新数据落地后秒级同步。 */
  dataRevision?: number
}>()

const emit = defineEmits<{
  (e: 'select-client', key: string): void
  (e: 'select-provider', code: string): void
  (e: 'select-model', model: string): void
}>()

const { t } = useI18n()

type StatsInterval = '5m' | '1h' | 'day'

const interval = ref<StatsInterval>('day')
/** 折叠只收明细两组；总量行常驻（见文件头）。 */
const collapsed = ref(false)

const clientsData = ref<TopologyClientsToday | null>(null)
const providersData = ref<TopologyProvidersToday | null>(null)
const modelsData = ref<TopologyModelsToday | null>(null)
const clientsFailed = ref(false)
const providersFailed = ref(false)
const modelsFailed = ref(false)
let timer: number | null = null
let controller: AbortController | null = null
/** 上一次 load 的发起时刻（回前台补拉用「数据多旧」判断，不用时间戳对表）。 */
let lastLoadAt = 0

async function load() {
  controller?.abort()
  controller = new AbortController()
  lastLoadAt = Date.now()
  const win = interval.value === 'day' ? undefined : interval.value
  // 三组独立加载：一组失败/降级不拖垮另一组（allSettled 语义）。
  // ★ 客户端组恒走 request_logs 直查（clients-today）：stats 事件链不带客户端
  // 身份列 ⇒ 没有日桶；day 模式后端自行以 rollup 水位封顶，与日桶对账。
  // day 区间才带 ?by=provider 交叉展开（窗口口径后端不提供）。
  const [clientsRes, providersRes, modelsRes] = await Promise.allSettled([
    getClientsToday({ signal: controller.signal, window: win }),
    getProvidersToday({ signal: controller.signal, window: win }),
    getTopologyModelsToday({ signal: controller.signal, byProvider: !win, window: win }),
  ])
  if (clientsRes.status === 'fulfilled') {
    clientsData.value = clientsRes.value
    clientsFailed.value = false
  } else if ((clientsRes.reason as Error)?.name !== 'AbortError') {
    clientsFailed.value = true
  }
  if (providersRes.status === 'fulfilled') {
    providersData.value = providersRes.value
    providersFailed.value = false
  } else if ((providersRes.reason as Error)?.name !== 'AbortError') {
    providersFailed.value = true
  }
  if (modelsRes.status === 'fulfilled') {
    modelsData.value = modelsRes.value
    modelsFailed.value = false
  } else if ((modelsRes.reason as Error)?.name !== 'AbortError') {
    modelsFailed.value = true
  }
}

/** 短窗口 30s、日桶 60s（日桶本身每小时才重建，更快轮询是假动作）。 */
const pollMs = computed(() => (interval.value === 'day' ? 60_000 : 30_000))

// ── 新数据到达 → 统计同步刷新（2026-10-29 统计同步轮）────────────────
// 轮询只是兜底（水位/日桶推进不依赖新流量）；「新请求落地」这条路径由
// dataRevision 驱动：尾沿防抖把一次突发（几十条生命周期帧）合流成一次
// 拉取；后台标签页只记账不拉取，回前台再补 —— 看不见的页面不打端点。
const SYNC_DEBOUNCE_MS = 4_000
let syncTimer: number | null = null
let syncPendingWhileHidden = false

function scheduleSyncLoad() {
  if (document.hidden) {
    syncPendingWhileHidden = true
    return
  }
  if (syncTimer) window.clearTimeout(syncTimer)
  syncTimer = window.setTimeout(() => {
    syncTimer = null
    // 排定时查过 hidden 还不够：武装在前、转隐藏在后这条时序下，到期
    // 这刻必须再查一次 —— 隐藏页不打端点，转成「挂起」等回前台补
    // （审计轮判据「防抖已武装后再转隐藏：到期不得拉取」钉住）。
    if (document.hidden) {
      syncPendingWhileHidden = true
      return
    }
    void load()
  }, SYNC_DEBOUNCE_MS)
}

function onVisibilityChange() {
  if (document.hidden) return
  // 数据已旧过本区间的轮询周期 ⇒ 立即补一轮，并把挂起的防抖/记账一并
  // 消化（同一轮已覆盖，4 秒后不得再补打一轮）。
  if (Date.now() - lastLoadAt >= pollMs.value) {
    if (syncTimer) {
      window.clearTimeout(syncTimer)
      syncTimer = null
    }
    syncPendingWhileHidden = false
    void load()
    return
  }
  if (syncPendingWhileHidden) {
    syncPendingWhileHidden = false
    scheduleSyncLoad()
  }
}

watch(
  () => props.dataRevision,
  (after, before) => {
    // 挂载时的初值落地不算「新数据」——那是初始加载自己要干的事。
    if (before === undefined) return
    scheduleSyncLoad()
  },
)

function startTimer() {
  if (timer) window.clearInterval(timer)
  // 后台标签页跳过轮询 tick：看不见的页面不消耗端点与电量；
  // 回前台由 onVisibilityChange 按数据新旧补拉。
  timer = window.setInterval(() => {
    if (!document.hidden) void load()
  }, pollMs.value)
}

watch(interval, () => {
  void load()
  startTimer()
})

onMounted(() => {
  void load()
  startTimer()
  document.addEventListener('visibilitychange', onVisibilityChange)
})

onUnmounted(() => {
  if (timer) window.clearInterval(timer)
  if (syncTimer) window.clearTimeout(syncTimer)
  syncTimer = null
  controller?.abort()
  document.removeEventListener('visibilitychange', onVisibilityChange)
})

// ── 口径行：以响应自己的 window 字段为准（数据说自己是什么口径）────────
const dataWindow = computed(() => providersData.value?.window ?? modelsData.value?.window ?? '')
const isDayData = computed(() => !dataWindow.value)

const watermark = computed(() => {
  if (!isDayData.value) return ''
  const raw = providersData.value?.source_high_watermark ?? modelsData.value?.source_high_watermark
  if (!raw) return ''
  const at = Date.parse(raw)
  return Number.isFinite(at) ? new Date(at).toLocaleTimeString() : ''
})

const dayUtc = computed(() =>
  isDayData.value ? (providersData.value?.day_utc ?? modelsData.value?.day_utc ?? '') : '',
)

const intervalText = computed(() =>
  dataWindow.value === '1h' ? t('liveTopology.stats.interval1h') : t('liveTopology.stats.interval5m'),
)

const noteText = computed(() => {
  if (isDayData.value) {
    if (!dayUtc.value) return ''
    const base = t('liveTopology.rail.dayUtc', { day: dayUtc.value })
    return watermark.value ? `${base} · ${t('liveTopology.rail.watermark', { time: watermark.value })}` : base
  }
  return t('liveTopology.stats.realtimeNote', { window: intervalText.value })
})

/** 空态文案：day 与短窗口分开说（点名缺的是什么、哪个区间）。 */
const emptyText = computed(() =>
  isDayData.value ? t('liveTopology.rail.empty') : t('liveTopology.stats.emptyNoResultWindow', { window: intervalText.value }),
)

// ── 总量行（常驻）───────────────────────────────────────────────────
// 总账取 provider 维度聚合的 totals（含 provider_id=0 未知桶 —— 客户端侧
// 被拒/未归因的流量不丢，只标未知；两口径的 handler 都算这份 totals）。
const totals = computed(() => providersData.value?.totals ?? null)

/** 失败 = failure ∪ rate_limited —— 与页面其它失败口径一致
 *  （SummaryHeader 的 failure 同样把 rate_limited 并进来）。 */
const failureCount = computed(() => (totals.value ? totals.value.failure + totals.value.rate_limited : 0))

const successRateText = computed(() => {
  const v = totals.value
  if (!v || !v.requests) return '—'
  return `${((v.success / v.requests) * 100).toFixed(1)}%`
})

const clientsUnavailable = computed(() => clientsFailed.value || clientsData.value?.degraded === true)
const providersUnavailable = computed(() => providersFailed.value || providersData.value?.degraded === true)
const modelsUnavailable = computed(() => modelsFailed.value || modelsData.value?.degraded === true)

/** 总量行的降级文案：day 日桶未初始化与「服务不可用」是两句话。 */
const totalsDownText = computed(() =>
  providersFailed.value || !isDayData.value
    ? t('liveTopology.rail.unavailable')
    : t('liveTopology.rail.degraded'),
)

// ── 明细三组（折叠目标；2026-10-29 单一统计轮：clients 并入）────────
// 默认全展开：桌面三列并排、高度可控；窄屏单列，需要时可各自收起。
const groupOpen = ref({ clients: true, providers: true, models: true })

function toggleGroup(key: 'clients' | 'providers' | 'models') {
  groupOpen.value = { ...groupOpen.value, [key]: !groupOpen.value[key] }
}

/** 客户端行显示名：品牌映射（clientDisplayName）+ unknown 走 i18n。 */
function clientName(key: string): string {
  return key === 'unknown' ? t('liveTopology.client.unknown') : clientDisplayName(key)
}

const clientItems = computed(() => {
  const list = clientsData.value?.items ?? []
  const filtered = props.activeClient ? list.filter((i) => i.client_key === props.activeClient) : list
  return filtered.slice(0, 14)
})

const providerItems = computed(() => {
  const list = providersData.value?.items ?? []
  const filtered = props.activeProvider
    ? list.filter((i) => i.catalog_code === props.activeProvider || `cred:${i.provider_id}` === props.activeProvider)
    : list
  return filtered.slice(0, 14)
})

const modelItems = computed(() => {
  const list = modelsData.value?.items ?? []
  const filtered = props.activeModel ? list.filter((i) => modelName(i) === props.activeModel) : list
  return filtered.slice(0, 14)
})

/** 模型行显示名：canonical 优先、raw 兜底（与拓扑图 modelKeyOf 同一口径）。 */
function modelName(item: { canonical_name?: string; raw_model_name?: string }): string {
  return (item.canonical_name ?? '').trim() || (item.raw_model_name ?? '').trim() || '—'
}

// ── 模型行内供应商展开（day 区间 ?by=provider 交叉展开）──────────────
// 展开态按模型行的 key 记（轮询重载后保持展开，不闪回）。
const expandedModels = ref(new Set<string>())

function modelRowKey(item: { canonical_id?: number; raw_model_name?: string }): string {
  return String(item.canonical_id || item.raw_model_name || '')
}

function toggleModelProviders(item: { canonical_id?: number; raw_model_name?: string }) {
  const key = modelRowKey(item)
  const next = new Set(expandedModels.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  expandedModels.value = next
}

function modelProvidersExpanded(item: { canonical_id?: number; raw_model_name?: string }): boolean {
  return expandedModels.value.has(modelRowKey(item))
}

/** 子行供应商显示名：display_name → catalog_code → 未知桶。 */
function providerName(item: { display_name?: string; catalog_code?: string }): string {
  return (
    (item.display_name ?? '').trim() ||
    (item.catalog_code ?? '').trim() ||
    t('liveTopology.rail.unknownProvider')
  )
}

const clientTotals = computed(() =>
  clientItems.value.reduce(
    (acc, i) => ({
      requests: acc.requests + i.requests,
      total_tokens: acc.total_tokens + i.total_tokens,
      cache_read_tokens: acc.cache_read_tokens + i.cache_read_tokens,
      cache_write_tokens: acc.cache_write_tokens + i.cache_write_tokens,
    }),
    { requests: 0, total_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
  ),
)

const providerTotals = computed(() => {
  const raw = providersData.value?.totals
  if (!raw) return null
  if (!props.activeProvider) return raw
  const sum = providerItems.value.reduce(
    (acc, i) => ({
      requests: acc.requests + i.requests,
      success: acc.success + i.success,
      failure: acc.failure + i.failure,
      total_tokens: acc.total_tokens + i.total_tokens,
      cache_read_tokens: (acc.cache_read_tokens ?? 0) + i.cache_read_tokens,
      cache_write_tokens: (acc.cache_write_tokens ?? 0) + i.cache_write_tokens,
      cost_usd: acc.cost_usd + i.cost_usd,
    }),
    { requests: 0, success: 0, failure: 0, total_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0 },
  )
  return { ...sum } as typeof raw
})

const modelTotals = computed(() => {
  const list = modelsData.value?.items ?? []
  const filtered = props.activeModel ? list.filter((i) => modelName(i) === props.activeModel) : list
  return filtered.reduce(
    (acc, i) => ({
      requests: acc.requests + i.requests,
      total_tokens: acc.total_tokens + i.total_tokens,
      cache_read_tokens: acc.cache_read_tokens + i.cache_read_tokens,
      cache_write_tokens: acc.cache_write_tokens + i.cache_write_tokens,
    }),
    { requests: 0, total_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
  )
})

function compactNumber(value: number): string {
  if (!Number.isFinite(value)) return '—'
  if (value >= 1e9) return `${(value / 1e9).toFixed(2)}B`
  if (value >= 1e6) return `${(value / 1e6).toFixed(2)}M`
  if (value >= 1e3) return `${(value / 1e3).toFixed(1)}K`
  return String(value)
}

/** token 量。缺省返回 `—`，不带单位：拼成「— tok」等于把「没有数据」
 *  说成一个零值，违反本仓的假零禁令。 */
function tokens(value: number): string {
  const n = compactNumber(value)
  return n === '—' ? n : `${n} tok`
}

/** 缓存量 = cache_read + cache_write（命中 + 写入都是缓存层的量）。 */
function cacheSum(item: { cache_read_tokens?: number; cache_write_tokens?: number }): number {
  return (item.cache_read_tokens ?? 0) + (item.cache_write_tokens ?? 0)
}

function successRate(item: { success?: number; requests: number }): string {
  if (!item.requests) return '—'
  return `${(((item.success ?? 0) / item.requests) * 100).toFixed(1)}%`
}
</script>

<template>
  <section class="stats" data-testid="stats-panel">
    <header class="stats__bar">
      <button
        type="button"
        class="stats__toggle"
        :aria-expanded="!collapsed"
        :aria-label="t(collapsed ? 'liveTopology.stats.expand' : 'liveTopology.stats.collapse')"
        data-testid="stats-toggle"
        @click="collapsed = !collapsed"
      >
        <CaretRight class="stats__caret" :class="{ 'is-open': !collapsed }" aria-hidden="true" />
        <span class="stats__title">{{ t('liveTopology.stats.title') }}</span>
      </button>

      <select
        v-model="interval"
        class="stats__interval"
        :aria-label="t('liveTopology.stats.intervalLabel')"
        data-testid="stats-interval"
      >
        <option value="5m">{{ t('liveTopology.stats.interval5m') }}</option>
        <option value="1h">{{ t('liveTopology.stats.interval1h') }}</option>
        <option value="day">{{ t('liveTopology.stats.intervalDay') }}</option>
      </select>

      <!-- 总量行常驻（折叠也可见）：请求数 / 成功率 / 输入 / 输出 / 缓存 / 失败 -->
      <div v-if="providersUnavailable" class="stats__totals stats__totals--down" data-testid="stats-totals-down">
        {{ totalsDownText }}
      </div>
      <div v-else-if="totals" class="stats__totals" data-testid="stats-totals">
        <span class="stats__metric">
          <i>{{ t('liveTopology.stats.requests') }}</i>
          <b>{{ compactNumber(totals.requests) }}</b>
        </span>
        <span class="stats__metric">
          <i>{{ t('liveTopology.stats.successRate') }}</i>
          <b>{{ successRateText }}</b>
        </span>
        <span class="stats__metric">
          <i>{{ t('liveTopology.stats.inputTokens') }}</i>
          <b>{{ tokens(totals.prompt_tokens) }}</b>
        </span>
        <span class="stats__metric">
          <i>{{ t('liveTopology.stats.outputTokens') }}</i>
          <b>{{ tokens(totals.completion_tokens) }}</b>
        </span>
        <span class="stats__metric">
          <i>{{ t('liveTopology.rail.cache') }}</i>
          <b>{{ tokens(cacheSum(totals)) }}</b>
        </span>
        <span class="stats__metric">
          <i>{{ t('liveTopology.stats.failures') }}</i>
          <b>{{ compactNumber(failureCount) }}</b>
        </span>
        <!-- 实时流程状态（网关核心窗口口径）：左栏明细/图看得到「正在发生」，
             DB 统计永远只看得到「已记账」——两个观测层在这里并排。 -->
        <template v-if="typeof inFlight === 'number'">
          <span class="stats__live-divider" aria-hidden="true" />
          <span class="stats__metric stats__metric--live" data-testid="stats-inflight">
            <i>{{ t('liveTopology.stats.inFlight') }}</i>
            <b>{{ compactNumber(inFlight) }}</b>
          </span>
          <span v-if="typeof queueDepth === 'number'" class="stats__metric stats__metric--live" data-testid="stats-queue">
            <i>{{ t('liveTopology.stats.queue') }}</i>
            <b>{{ compactNumber(queueDepth) }}</b>
          </span>
        </template>
      </div>
    </header>

    <template v-if="!collapsed">
      <p v-if="noteText" class="stats__note" data-testid="stats-note">{{ noteText }}</p>

      <div class="stats__groups">
        <!-- ── 客户端组（2026-10-29 单一统计轮：原 SSE 三块汇总的 clients 维度并入）── -->
        <section class="stats__group" data-testid="stats-group-clients">
          <button
            type="button"
            class="stats__group-head"
            :aria-expanded="groupOpen.clients"
            :aria-label="t('liveTopology.rail.toggle', { group: t('liveTopology.summary.clients') })"
            @click="toggleGroup('clients')"
          >
            <CaretRight class="stats__caret stats__caret--sub" :class="{ 'is-open': groupOpen.clients }" aria-hidden="true" />
            <span class="stats__group-title">{{ t('liveTopology.summary.clients') }}</span>
            <span v-if="!clientsUnavailable" class="stats__group-sum">
              {{ compactNumber(clientTotals.requests) }} · {{ tokens(clientTotals.total_tokens) }} ·
              {{ t('liveTopology.rail.cache') }} {{ compactNumber(clientTotals.cache_read_tokens + clientTotals.cache_write_tokens) }}
            </span>
          </button>

          <p v-if="clientsUnavailable" class="stats__group-down">
            {{ clientsFailed ? t('liveTopology.rail.unavailable') : t('liveTopology.rail.degraded') }}
          </p>
          <template v-else-if="groupOpen.clients">
            <ul v-if="clientItems.length" class="stats__list">
              <li
                v-for="item in clientItems"
                :key="item.client_key"
                class="stats__row"
                :class="{ 'is-active': activeClient === item.client_key, 'is-unknown': item.client_key === 'unknown' }"
              >
                <button type="button" class="stats__row-btn" @click="emit('select-client', item.client_key)">
                  <span class="stats__name">{{ clientName(item.client_key) }}</span>
                  <span class="stats__figures">
                    <b>{{ compactNumber(item.requests) }}</b>
                    <i>{{ successRate(item) }}</i>
                  </span>
                  <span class="stats__share" :style="{ width: `${Math.round((item.share || 0) * 100)}%` }" />
                  <span class="stats__tokens">
                    {{ tokens(item.total_tokens) }}<template v-if="cacheSum(item)"> · {{ t('liveTopology.rail.cache') }} {{ compactNumber(cacheSum(item)) }}</template>
                  </span>
                </button>
              </li>
            </ul>
            <p v-else class="stats__empty">{{ emptyText }}</p>
          </template>
        </section>

        <!-- ── 供应商组 ─────────────────────────────────────────── -->
        <section class="stats__group" data-testid="stats-group-providers">
          <button
            type="button"
            class="stats__group-head"
            :aria-expanded="groupOpen.providers"
            :aria-label="t('liveTopology.rail.toggle', { group: t('liveTopology.rail.providerGroup') })"
            @click="toggleGroup('providers')"
          >
            <CaretRight class="stats__caret stats__caret--sub" :class="{ 'is-open': groupOpen.providers }" aria-hidden="true" />
            <span class="stats__group-title">{{ t('liveTopology.rail.providerGroup') }}</span>
            <span v-if="!providersUnavailable && providerTotals" class="stats__group-sum">
              {{ compactNumber(providerTotals.requests) }} · {{ tokens(providerTotals.total_tokens) }} ·
              {{ t('liveTopology.rail.cache') }} {{ compactNumber(cacheSum(providerTotals)) }}
            </span>
          </button>

          <p v-if="providersUnavailable" class="stats__group-down">
            {{ providersFailed ? t('liveTopology.rail.unavailable') : t('liveTopology.rail.degraded') }}
          </p>
          <template v-else-if="groupOpen.providers">
            <ul v-if="providerItems.length" class="stats__list">
              <li
                v-for="item in providerItems"
                :key="item.provider_id || item.catalog_code"
                class="stats__row"
                :class="{ 'is-active': activeProvider === item.catalog_code, 'is-unknown': !item.known }"
              >
                <button type="button" class="stats__row-btn" @click="emit('select-provider', item.catalog_code)">
                  <span class="stats__name">
                    {{ item.display_name || item.catalog_code || t('liveTopology.rail.unknownProvider') }}
                  </span>
                  <span class="stats__figures">
                    <b>{{ compactNumber(item.requests) }}</b>
                    <i>{{ successRate(item) }}</i>
                  </span>
                  <span class="stats__share" :style="{ width: `${Math.round((item.share || 0) * 100)}%` }" />
                  <span class="stats__tokens">
                    {{ tokens(item.total_tokens) }}<template v-if="cacheSum(item)"> · {{ t('liveTopology.rail.cache') }} {{ compactNumber(cacheSum(item)) }}</template>
                  </span>
                </button>
              </li>
            </ul>
            <p v-else class="stats__empty">{{ emptyText }}</p>
          </template>
        </section>

        <!-- ── 模型组 ───────────────────────────────────────────── -->
        <section class="stats__group" data-testid="stats-group-models">
          <button
            type="button"
            class="stats__group-head"
            :aria-expanded="groupOpen.models"
            :aria-label="t('liveTopology.rail.toggle', { group: t('liveTopology.rail.modelGroup') })"
            @click="toggleGroup('models')"
          >
            <CaretRight class="stats__caret stats__caret--sub" :class="{ 'is-open': groupOpen.models }" aria-hidden="true" />
            <span class="stats__group-title">{{ t('liveTopology.rail.modelGroup') }}</span>
            <span v-if="!modelsUnavailable" class="stats__group-sum">
              {{ compactNumber(modelTotals.requests) }} · {{ tokens(modelTotals.total_tokens) }} ·
              {{ t('liveTopology.rail.cache') }} {{ compactNumber(modelTotals.cache_read_tokens + modelTotals.cache_write_tokens) }}
            </span>
          </button>

          <p v-if="modelsUnavailable" class="stats__group-down">
            {{ modelsFailed ? t('liveTopology.rail.unavailable') : t('liveTopology.rail.degraded') }}
          </p>
          <template v-else-if="groupOpen.models">
            <ul v-if="modelItems.length" class="stats__list">
              <li
                v-for="item in modelItems"
                :key="item.canonical_id || item.raw_model_name"
                class="stats__row"
                :class="{ 'is-active': activeModel === modelName(item) }"
              >
                <button type="button" class="stats__row-btn" @click="emit('select-model', modelName(item))">
                  <span class="stats__name">{{ modelName(item) }}</span>
                  <span class="stats__figures">
                    <b>{{ compactNumber(item.requests) }}</b>
                    <i>{{ successRate(item) }}</i>
                  </span>
                  <span class="stats__share" :style="{ width: `${Math.round((item.share || 0) * 100)}%` }" />
                  <span class="stats__tokens">
                    {{ tokens(item.total_tokens) }}<template v-if="cacheSum(item)"> · {{ t('liveTopology.rail.cache') }} {{ compactNumber(cacheSum(item)) }}</template>
                  </span>
                </button>
                <!-- 行内供应商展开（仅 day 区间有子行）：该模型在哪些供应商上
                     各跑了多少。展开钮与选行钮分开：展开不触发图过滤。 -->
                <button
                  v-if="item.providers?.length"
                  type="button"
                  class="stats__expand"
                  :aria-expanded="modelProvidersExpanded(item)"
                  :aria-label="t('liveTopology.rail.toggleProviders', { model: modelName(item) })"
                  data-testid="stats-model-expand"
                  @click.stop="toggleModelProviders(item)"
                >
                  <CaretRight class="stats__caret stats__caret--leaf" :class="{ 'is-open': modelProvidersExpanded(item) }" aria-hidden="true" />
                  {{ t('liveTopology.rail.providersCount', { n: item.providers.length }) }}
                </button>
                <ul v-if="item.providers?.length && modelProvidersExpanded(item)" class="stats__sublist" data-testid="stats-model-providers">
                  <li
                    v-for="sub in item.providers"
                    :key="sub.provider_id || sub.catalog_code"
                    class="stats__subrow"
                    :class="{ 'is-unknown': !sub.known }"
                  >
                    <span class="stats__name">{{ providerName(sub) }}</span>
                    <span class="stats__figures">
                      <b>{{ compactNumber(sub.requests) }}</b>
                      <i>{{ successRate(sub) }}</i>
                    </span>
                    <span class="stats__share" :style="{ width: `${Math.round((sub.share || 0) * 100)}%` }" />
                    <span class="stats__tokens">{{ Math.round((sub.share || 0) * 100) }}%</span>
                  </li>
                </ul>
              </li>
            </ul>
            <p v-else class="stats__empty">{{ emptyText }}</p>
          </template>
        </section>
      </div>
    </template>
  </section>
</template>

<style scoped>
.stats {
  padding: 4px 8px 8px;
  border: 1px solid var(--kx-border);
  border-radius: 10px;
  background: var(--kx-surface);
}

.stats__bar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

/* 折叠开关（caret + 标题一个按钮）；窗口区间选择独占其余空间。 */
.stats__toggle {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 2px 4px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  cursor: pointer;
  color: inherit;
  flex: none;
}

.stats__toggle:hover {
  background: var(--kx-primary-soft);
}

.stats__title {
  font-size: var(--kx-font-md, 14px);
  font-weight: 600;
  color: var(--kx-text);
}

.stats__interval {
  /* 全局 select { width: 100% }（style.css 浏览器表面轮）会把表单控件撑满
     父容器 —— 头部这里必须收回内容宽（与过滤栏 select 的 max-width 同款修法）。 */
  width: auto;
  min-width: 72px;
  padding: 0 4px;
  height: 24px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: var(--kx-surface);
  color: var(--kx-text);
  font-size: 11px;
  flex: none;
}

/* SVG 图标必须显式尺寸（裸 svg 按包含块宽度渲染会撑爆布局）。 */
.stats__caret {
  flex: none;
  color: var(--kx-muted);
  transform: rotate(0deg);
  transition: transform 0.15s var(--kx-motion-ease-out);
}

.stats__caret.is-open {
  transform: rotate(90deg);
}

.stats__caret > svg {
  width: 12px;
  height: 12px;
}

.stats__caret--leaf > svg {
  width: 10px;
  height: 10px;
}

/* 总量行：一个指标 = 标签 + 数值；窄屏换行不截断（总账不容许被裁）。 */
.stats__totals {
  display: flex;
  align-items: baseline;
  gap: 4px 12px;
  flex: 1;
  min-width: 0;
  flex-wrap: wrap;
  font-variant-numeric: tabular-nums;
}

.stats__totals--down {
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-warning);
}

.stats__metric {
  display: inline-flex;
  align-items: baseline;
  gap: 3px;
  white-space: nowrap;
}

.stats__metric i {
  font-size: 11px;
  font-style: normal;
  color: var(--kx-muted);
}

.stats__metric b {
  font-size: var(--kx-font-sm, 12px);
  font-weight: 600;
  color: var(--kx-text);
}

/* 实时流程状态（在途/排队）与 DB 记账指标的分隔：细竖线。 */
.stats__live-divider {
  align-self: stretch;
  width: 1px;
  background: var(--kx-border);
  flex: none;
}

/* 在途/排队的数值用主色：它们是「正在发生的流程」——与「已记账」的
   DB 指标在视觉上分层（文本色不算装饰性着色，第 21 轮判据不管文本）。 */
.stats__metric--live b {
  color: var(--kx-primary);
}

.stats__note {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--kx-muted);
}

/* 明细两组：桌面并排、窄屏单列（断点白名单 768）。 */
.stats__groups {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 4px 16px;
  margin-top: 4px;
}

@media (min-width: 768px) {
  .stats__groups {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

.stats__group {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.stats__group-head {
  display: flex;
  align-items: center;
  gap: 4px;
  width: 100%;
  padding: 2px 4px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  cursor: pointer;
  text-align: left;
  color: inherit;
  flex: none;
}

.stats__group-head:hover {
  background: var(--kx-primary-soft);
}

.stats__group-title {
  flex: none;
  font-size: var(--kx-font-sm, 12px);
  font-weight: 600;
  color: var(--kx-text);
}

/* 组头合计（折叠态也可见的「即时汇总」）：请求 · token · 缓存 */
.stats__group-sum {
  flex: 1;
  min-width: 0;
  font-size: 11px;
  color: var(--kx-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-variant-numeric: tabular-nums;
}

.stats__group-down {
  margin: 0;
  padding: 4px 8px;
  border-radius: 8px;
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-warning);
  background: var(--kx-warning-soft);
}

.stats__list {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  /* 三组默认全展开时必须限高：14 行 × 两行文本会把面板撑到两千多像素
     （2026-10-29 生产实测 2660px，拓扑被推出视口）——「所有统计可见」
     与「节约头部空间」的平衡点 = 组内滚动（旧左下栏同款手法）。 */
  max-height: 240px;
  overflow-y: auto;
}

.stats__row-btn {
  display: grid;
  grid-template-columns: 1fr auto;
  gap: 2px 4px;
  width: 100%;
  padding: 3px 4px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  cursor: pointer;
  text-align: left;
  color: inherit;
}

.stats__row-btn:hover,
.stats__row.is-active .stats__row-btn {
  background: var(--kx-primary-soft);
}

.stats__name {
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.stats__row.is-unknown .stats__name {
  color: var(--kx-muted);
}

.stats__figures {
  display: flex;
  gap: 4px;
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-text);
}

.stats__figures i {
  color: var(--kx-muted);
  font-style: normal;
}

.stats__share {
  grid-column: 1 / -1;
  height: 3px;
  border-radius: 2px;
  background: var(--kx-primary);
  opacity: 0.65;
  /* 轮询刷新时占比条平滑移动（width 是数据编码 ⇒ 已在
     design-motion-decisions.json 的 layoutDriven 登记，key =
     components/live-topology/StatsPanel.vue|.stats__share|width）。
     ★ 2026-10-30 r1050 轮订正：★ 这条注释此前**声称已登记但裁决簿里没有** ⇒
     ★ ★ 注释里的「已登记」也是会过期的断言 ⇒ 本轮补登记并把 key 写进注释，
     ★ ★ 让「注释声称」与「裁决簿实有」逐字对应（★ 便于下一个人核对）。 */
  transition: width 0.4s var(--kx-motion-ease-out);
}

.stats__tokens {
  grid-column: 1 / -1;
  font-size: 11px;
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
}

/* ── 模型行内供应商展开（day 区间）───────────────────────────────── */
.stats__expand {
  display: flex;
  align-items: center;
  gap: 3px;
  width: 100%;
  padding: 1px 4px 1px 12px;
  border: 0;
  background: transparent;
  cursor: pointer;
  text-align: left;
  font-size: 11px;
  color: var(--kx-muted);
}

.stats__expand:hover {
  color: var(--kx-text);
  background: var(--kx-primary-soft);
}

.stats__sublist {
  margin: 0;
  padding: 0 0 2px 12px;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.stats__subrow {
  display: grid;
  grid-template-columns: 1fr auto;
  gap: 1px 4px;
  padding: 2px 4px;
  border-left: 2px solid var(--kx-border);
  border-radius: 0 6px 6px 0;
}

.stats__subrow .stats__name {
  font-size: 11px;
  color: var(--kx-muted);
}

.stats__subrow .stats__figures {
  font-size: 11px;
}

.stats__subrow .stats__figures b {
  font-weight: 500;
}

.stats__subrow .stats__share {
  height: 2px;
  opacity: 0.45;
}

.stats__empty {
  margin: 0;
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-muted);
}

@media (prefers-reduced-motion: reduce) {
  .stats__share,
  .stats__caret {
    transition: none;
  }
}
</style>
