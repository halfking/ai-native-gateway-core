<script setup lang="ts">
// TodaySummaryRail.vue — 左栏「当天汇总」（2026-10-28 当天汇总轮）
//
// 由 ProviderTodayRail（仅供应商组）升级而来：**供应商 + 模型两组**当天
// 聚合，各自可折叠；行内显示 请求 / 成功率 / 占比条 / token / 缓存。
// 点行联动图过滤（供应商 ↔ providers 过滤、模型 ↔ models 过滤），
// 与「图、左右面板收敛到同一过滤子集」的既有关键约束一致。
//
// 口径说明必须显示在栏内而不是藏在 tooltip 里：
//  · 当天 = **UTC 日桶**（stats_usage_daily.day_utc，两组同表同口径：
//    providers 组 dimension_type='provider'，模型组 'provider_model'
//    在 canonical_id 上再聚合 —— 同一标准模型跨供应商/别名合并一行）；
//  · 该聚合表每小时重建，水位可能滞后 ≤1h。把它画成「看起来实时的数字」
//    是本仓反复吃过的亏（假实时），水位与 degraded 都是一级信息。
//  · 两组独立失败：一组 degraded 不拖垮另一组（allSettled 语义）。

import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { CaretRight } from '@element-plus/icons-vue'
import {
  getProvidersToday,
  getTopologyModelsToday,
  type TopologyModelsToday,
  type TopologyProvidersToday,
} from '../../api/topology'

const props = defineProps<{
  /** 当前过滤的供应商 code（空 = 不过滤）。左栏与图收敛到同一子集。 */
  activeProvider?: string
  /** 当前过滤的模型 canonical 名（空 = 不过滤）。 */
  activeModel?: string
}>()

const emit = defineEmits<{
  (e: 'select-provider', code: string): void
  (e: 'select-model', model: string): void
}>()

const { t } = useI18n()

const providersData = ref<TopologyProvidersToday | null>(null)
const modelsData = ref<TopologyModelsToday | null>(null)
const providersFailed = ref(false)
const modelsFailed = ref(false)
const loading = ref(false)
let timer: number | null = null
let controller: AbortController | null = null

async function load() {
  controller?.abort()
  controller = new AbortController()
  loading.value = true
  // 两组独立加载：一组失败/降级不拖垮另一组（沿用 KPI 行的 allSettled 语义）。
  const [providersRes, modelsRes] = await Promise.allSettled([
    getProvidersToday({ signal: controller.signal }),
    getTopologyModelsToday({ signal: controller.signal }),
  ])
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
  loading.value = false
}

/** 两组各自的可用性（degraded ≠ 0 请求，必须分开说）。 */
const providersUnavailable = computed(() => providersFailed.value || providersData.value?.degraded === true)
const modelsUnavailable = computed(() => modelsFailed.value || modelsData.value?.degraded === true)

// ── 折叠态（owner 2026-10-28：可以折叠，展开看更多动态信息）──────────
// 默认：供应商组展开（既有行为），模型组收起（新增面，先给组头合计）。
const groupOpen = ref({ providers: true, models: false })

function toggleGroup(key: 'providers' | 'models') {
  groupOpen.value = { ...groupOpen.value, [key]: !groupOpen.value[key] }
}

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

const watermark = computed(() => {
  const raw = providersData.value?.source_high_watermark ?? modelsData.value?.source_high_watermark
  if (!raw) return ''
  const at = Date.parse(raw)
  return Number.isFinite(at) ? new Date(at).toLocaleTimeString() : ''
})

const dayUtc = computed(() => providersData.value?.day_utc ?? modelsData.value?.day_utc ?? '')

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

/** 缓存量 = cache_read + cache_write（命中 + 写入都是缓存层的量）。
 *  与大方块「缓存」格同源（SSE cache_read/write_tokens 的日桶累计）。 */
function cacheSum(item: { cache_read_tokens?: number; cache_write_tokens?: number }): number {
  return (item.cache_read_tokens ?? 0) + (item.cache_write_tokens ?? 0)
}

function cacheText(item: { cache_read_tokens?: number; cache_write_tokens?: number }): string {
  const n = compactNumber(cacheSum(item))
  return n === '—' ? n : `${n}`
}

function successRate(item: { success?: number; requests: number }): string {
  if (!item.requests) return '—'
  return `${(((item.success ?? 0) / item.requests) * 100).toFixed(1)}%`
}

onMounted(() => {
  void load()
  timer = window.setInterval(load, 60_000)
})

onUnmounted(() => {
  if (timer) window.clearInterval(timer)
  controller?.abort()
})
</script>

<template>
  <aside class="rail" aria-label="today summary">
    <header class="rail__head">
      <h3 class="rail__title">{{ t('liveTopology.rail.title') }}</h3>
      <p v-if="dayUtc" class="rail__meta">
        {{ t('liveTopology.rail.dayUtc', { day: dayUtc }) }}
        <span v-if="watermark">{{ t('liveTopology.rail.watermark', { time: watermark }) }}</span>
      </p>
    </header>

    <!-- ── 供应商组 ─────────────────────────────────────────── -->
    <section class="rail__group" data-testid="rail-group-providers">
      <button
        type="button"
        class="rail__group-head"
        :aria-expanded="groupOpen.providers"
        :aria-label="t('liveTopology.rail.toggle', { group: t('liveTopology.rail.providerGroup') })"
        @click="toggleGroup('providers')"
      >
        <CaretRight class="rail__caret" :class="{ 'is-open': groupOpen.providers }" aria-hidden="true" />
        <span class="rail__group-title">{{ t('liveTopology.rail.providerGroup') }}</span>
        <span v-if="!providersUnavailable && providerTotals" class="rail__group-sum">
          {{ compactNumber(providerTotals.requests) }} · {{ tokens(providerTotals.total_tokens) }} ·
          {{ t('liveTopology.rail.cache') }} {{ compactNumber(cacheSum(providerTotals)) }}
        </span>
      </button>

      <p v-if="providersUnavailable" class="rail__degraded">
        {{ providersFailed ? t('liveTopology.rail.unavailable') : t('liveTopology.rail.degraded') }}
      </p>
      <template v-else-if="groupOpen.providers">
        <ul v-if="providerItems.length" class="rail__list">
          <li
            v-for="item in providerItems"
            :key="item.provider_id || item.catalog_code"
            class="rail__row"
            :class="{ 'is-active': activeProvider === item.catalog_code, 'is-unknown': !item.known }"
          >
            <button type="button" class="rail__row-btn" @click="emit('select-provider', item.catalog_code)">
              <span class="rail__name">
                {{ item.display_name || item.catalog_code || t('liveTopology.rail.unknownProvider') }}
              </span>
              <span class="rail__figures">
                <b>{{ compactNumber(item.requests) }}</b>
                <i>{{ successRate(item) }}</i>
              </span>
              <span class="rail__bar" :style="{ width: `${Math.round((item.share || 0) * 100)}%` }" />
              <span class="rail__tokens">
                {{ tokens(item.total_tokens) }}<template v-if="cacheSum(item)"> · {{ t('liveTopology.rail.cache') }} {{ cacheText(item) }}</template>
              </span>
            </button>
          </li>
        </ul>
        <p v-else class="rail__empty">{{ t('liveTopology.rail.empty') }}</p>
      </template>
    </section>

    <!-- ── 模型组 ───────────────────────────────────────────── -->
    <section class="rail__group" data-testid="rail-group-models">
      <button
        type="button"
        class="rail__group-head"
        :aria-expanded="groupOpen.models"
        :aria-label="t('liveTopology.rail.toggle', { group: t('liveTopology.rail.modelGroup') })"
        @click="toggleGroup('models')"
      >
        <CaretRight class="rail__caret" :class="{ 'is-open': groupOpen.models }" aria-hidden="true" />
        <span class="rail__group-title">{{ t('liveTopology.rail.modelGroup') }}</span>
        <span v-if="!modelsUnavailable" class="rail__group-sum">
          {{ compactNumber(modelTotals.requests) }} · {{ tokens(modelTotals.total_tokens) }} ·
          {{ t('liveTopology.rail.cache') }} {{ compactNumber(modelTotals.cache_read_tokens + modelTotals.cache_write_tokens) }}
        </span>
      </button>

      <p v-if="modelsUnavailable" class="rail__degraded">
        {{ modelsFailed ? t('liveTopology.rail.unavailable') : t('liveTopology.rail.degraded') }}
      </p>
      <template v-else-if="groupOpen.models">
        <ul v-if="modelItems.length" class="rail__list">
          <li
            v-for="item in modelItems"
            :key="item.canonical_id || item.raw_model_name"
            class="rail__row"
            :class="{ 'is-active': activeModel === modelName(item) }"
          >
            <button type="button" class="rail__row-btn" @click="emit('select-model', modelName(item))">
              <span class="rail__name">{{ modelName(item) }}</span>
              <span class="rail__figures">
                <b>{{ compactNumber(item.requests) }}</b>
                <i>{{ successRate(item) }}</i>
              </span>
              <span class="rail__bar" :style="{ width: `${Math.round((item.share || 0) * 100)}%` }" />
              <span class="rail__tokens">
                {{ tokens(item.total_tokens) }}<template v-if="cacheSum(item)"> · {{ t('liveTopology.rail.cache') }} {{ cacheText(item) }}</template>
              </span>
            </button>
          </li>
        </ul>
        <p v-else class="rail__empty">{{ t('liveTopology.rail.empty') }}</p>
      </template>
    </section>
  </aside>
</template>

<style scoped>
.rail {
  display: flex;
  flex-direction: column;
  gap: 6px;
  height: 100%;
  min-height: 0;
  padding: 8px;
  border: 1px solid var(--kx-border);
  border-radius: 12px;
  background: var(--kx-surface);
  overflow: hidden;
}

.rail__head {
  display: flex;
  flex-direction: column;
  gap: 2px;
  flex: none;
}

.rail__title {
  margin: 0;
  font-size: var(--kx-font-md, 14px);
  color: var(--kx-text);
}

.rail__meta {
  margin: 0;
  font-size: 11px;
  color: var(--kx-muted);
}

/* ── 分组（2026-10-28 当天汇总轮）：组头可折叠 ─────────────────── */
.rail__group {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-height: 0;
}

.rail__group-head {
  display: flex;
  align-items: center;
  gap: 4px;
  width: 100%;
  padding: 3px 4px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  cursor: pointer;
  text-align: left;
  color: inherit;
  flex: none;
}

.rail__group-head:hover {
  background: var(--kx-primary-soft);
}

/* 折叠箭头：SVG 图标（按钮位禁字形，V3），旋转表达开合 —— 90° 旋转是
   状态编码，展开态箭头朝下。尺寸显式（裸 svg 会撑爆布局）。 */
.rail__caret {
  flex: none;
  color: var(--kx-muted);
  transform: rotate(0deg);
  transition: transform 0.15s var(--kx-motion-ease-out);
}

.rail__caret.is-open {
  transform: rotate(90deg);
}

.rail__caret > svg {
  width: 12px;
  height: 12px;
}

.rail__group-title {
  flex: none;
  font-size: var(--kx-font-sm, 12px);
  font-weight: 600;
  color: var(--kx-text);
}

/* 组头合计（折叠态也可见的「即时汇总」）：请求 · token · 缓存 */
.rail__group-sum {
  flex: 1;
  min-width: 0;
  font-size: 11px;
  color: var(--kx-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-variant-numeric: tabular-nums;
}

.rail__degraded {
  margin: 0;
  padding: 8px;
  border-radius: 8px;
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-warning);
  background: var(--kx-warning-soft);
}

.rail__list {
  flex: 1;
  min-height: 0;
  margin: 0;
  padding: 0;
  list-style: none;
  overflow-y: auto;
}

.rail__row-btn {
  display: grid;
  grid-template-columns: 1fr auto;
  gap: 2px 4px;
  width: 100%;
  padding: 4px 4px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  cursor: pointer;
  text-align: left;
  color: inherit;
}

.rail__row-btn:hover,
.rail__row.is-active .rail__row-btn {
  background: var(--kx-primary-soft);
}

.rail__name {
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.rail__row.is-unknown .rail__name {
  color: var(--kx-muted);
}

.rail__figures {
  display: flex;
  gap: 4px;
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-text);
}

.rail__figures i {
  color: var(--kx-muted);
  font-style: normal;
}

.rail__bar {
  grid-column: 1 / -1;
  height: 3px;
  border-radius: 2px;
  background: var(--kx-primary);
  opacity: 0.65;
  /* 60s 轮询刷新时占比条平滑移动（width 是数据编码 ⇒ 已在
     design-motion-decisions.json 的 layoutDriven 登记）。 */
  transition: width 0.4s var(--kx-motion-ease-out);
}

.rail__tokens {
  grid-column: 1 / -1;
  font-size: 11px;
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
}

.rail__empty {
  margin: 0;
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-muted);
}

@media (prefers-reduced-motion: reduce) {
  .rail__bar,
  .rail__caret {
    transition: none;
  }
}
</style>
