<script setup lang="ts">
// ProviderTodayRail.vue — 左栏「供应商当天请求与 token 统计」（2026-10-16）
//
// 口径说明必须显示在栏内而不是藏在 tooltip 里：
//  · 当天 = **UTC 日桶**（后端 stats_usage_daily 的 day_utc）；
//  · 该聚合表每小时重建，水位可能滞后 ≤1h。
// 把它画成「看起来实时的数字」是本仓反复吃过的亏（假实时），
// 所以水位与 degraded 状态都是一级信息，不是脚注。

import { computed, onBeforeUnmount, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getProvidersToday, type TopologyProvidersToday } from '../../api/topology'

const props = defineProps<{
  /** 当前过滤的供应商 code（空 = 不过滤）。左栏与图收敛到同一子集。 */
  activeProvider?: string
}>()

const emit = defineEmits<{
  (e: 'select-provider', code: string): void
}>()

const { t } = useI18n()

const data = ref<TopologyProvidersToday | null>(null)
const loading = ref(false)
const failed = ref(false)
let timer: number | null = null
let controller: AbortController | null = null

async function load() {
  controller?.abort()
  controller = new AbortController()
  loading.value = true
  try {
    data.value = await getProvidersToday({ signal: controller.signal })
    failed.value = false
  } catch (err) {
    if ((err as Error)?.name === 'AbortError') return
    failed.value = true
  } finally {
    loading.value = false
  }
}

/** 统计不可用（端点降级 / 请求失败）—— 与「当天 0 请求」严格区分。 */
const unavailable = computed(() => failed.value || data.value?.degraded === true)

const items = computed(() => {
  const list = data.value?.items ?? []
  const filtered = props.activeProvider
    ? list.filter((i) => i.catalog_code === props.activeProvider || `cred:${i.provider_id}` === props.activeProvider)
    : list
  return filtered.slice(0, 14)
})

const totals = computed(() => {
  const t = data.value?.totals
  if (!t) return null
  if (!props.activeProvider) return t
  const sum = items.value.reduce(
    (acc, i) => ({
      requests: acc.requests + i.requests,
      success: acc.success + i.success,
      failure: acc.failure + i.failure,
      total_tokens: acc.total_tokens + i.total_tokens,
      cost_usd: acc.cost_usd + i.cost_usd,
    }),
    { requests: 0, success: 0, failure: 0, total_tokens: 0, cost_usd: 0 },
  )
  return { ...sum } as typeof t
})

const watermark = computed(() => {
  const raw = data.value?.source_high_watermark
  if (!raw) return ''
  const at = Date.parse(raw)
  return Number.isFinite(at) ? new Date(at).toLocaleTimeString() : ''
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

function successRate(item: { success: number; requests: number }): string {
  if (!item.requests) return '—'
  return `${((item.success / item.requests) * 100).toFixed(1)}%`
}

// ── 大数字脉冲（2026-10-28 live 页动效轮）──────────────────────
// 60s 轮询刷新时两个汇总大数字**变化**的那格闪一次 —— 数字硬跳变没有
// 「刚刚更新过」的痕迹，运维分不清这格是活的还是忘了刷新。
// ★ 首见不闪（数据到达 ≠ 新活动）；**任何变化都闪**（该表是日桶累计，
// 变化只会是增长或过滤切换，两个都值得一次反馈）。
const pulsing = ref(new Set<'requests' | 'tokens'>())
const lastTotals = new Map<'requests' | 'tokens', number>()
const pulseTimers: number[] = []
/** 脉冲键 → totals 实际字段（'tokens' 是 UI 语义，后端字段叫 total_tokens）。 */
const PULSE_FIELDS: Array<{ key: 'requests' | 'tokens'; pick: (t: NonNullable<typeof totals.value>) => number }> = [
  { key: 'requests', pick: (t) => t.requests },
  { key: 'tokens', pick: (t) => t.total_tokens },
]

watch(
  totals,
  (t) => {
    if (!t) return
    for (const field of PULSE_FIELDS) {
      const value = field.pick(t)
      const prev = lastTotals.get(field.key)
      if (prev !== undefined && value !== prev) {
        pulsing.value = new Set(pulsing.value).add(field.key)
        const added = field.key
        pulseTimers.push(
          window.setTimeout(() => {
            const next = new Set(pulsing.value)
            next.delete(added)
            pulsing.value = next
          }, 340),
        )
      }
      lastTotals.set(field.key, value)
    }
  },
  { immediate: true },
)
onBeforeUnmount(() => {
  for (const handle of pulseTimers) window.clearTimeout(handle)
})

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
  <aside class="rail" aria-label="provider daily totals">
    <header class="rail__head">
      <h3 class="rail__title">{{ t('liveTopology.rail.title') }}</h3>
      <p v-if="data?.day_utc" class="rail__meta">
        {{ t('liveTopology.rail.dayUtc', { day: data.day_utc }) }}
        <span v-if="watermark">{{ t('liveTopology.rail.watermark', { time: watermark }) }}</span>
      </p>
    </header>

    <p v-if="unavailable" class="rail__degraded">
      {{ failed ? t('liveTopology.rail.unavailable') : t('liveTopology.rail.degraded') }}
    </p>

    <template v-else>
      <div v-if="totals" class="rail__totals">
        <div class="rail__total">
          <span class="rail__total-value" :class="{ 'is-pulse': pulsing.has('requests') }">{{ compactNumber(totals.requests) }}</span>
          <span class="rail__total-label">{{ t('liveTopology.rail.requests') }}</span>
        </div>
        <div class="rail__total">
          <span class="rail__total-value" :class="{ 'is-pulse': pulsing.has('tokens') }">{{ compactNumber(totals.total_tokens) }}</span>
          <span class="rail__total-label">{{ t('liveTopology.rail.tokens') }}</span>
        </div>
      </div>

      <ul v-if="items.length" class="rail__list">
        <li
          v-for="item in items"
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
            <span class="rail__tokens">{{ tokens(item.total_tokens) }}</span>
          </button>
        </li>
      </ul>
      <p v-else class="rail__empty">{{ t('liveTopology.rail.empty') }}</p>
    </template>
  </aside>
</template>

<style scoped>
.rail {
  display: flex;
  flex-direction: column;
  gap: 8px;
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

.rail__degraded {
  margin: 0;
  padding: 8px;
  border-radius: 8px;
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-warning);
  background: var(--kx-warning-soft);
}

.rail__totals {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 4px;
}

.rail__total {
  display: flex;
  flex-direction: column;
  padding: 4px 8px;
  border-radius: 8px;
  background: var(--kx-primary-soft);
}

.rail__total-value {
  font-size: 16px;
  font-weight: 700;
  color: var(--kx-primary);
}

.rail__total-label {
  font-size: 11px;
  color: var(--kx-muted);
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

/* 大数字更新脉冲：scale 一次（一次性 ≤800ms、指数减速、非入场类）。
   60s 轮询里只有真正变化的那格闪 —— 数字硬跳变没有「刚更新过」的痕迹。 */
.rail__total-value.is-pulse {
  animation: rail-num-pulse 0.32s var(--kx-motion-ease-out);
}

@keyframes rail-num-pulse {
  0% { transform: scale(1); }
  40% { transform: scale(1.07); }
  100% { transform: scale(1); }
}

@media (prefers-reduced-motion: reduce) {
  .rail__bar,
  .rail__total-value.is-pulse {
    transition: none;
    animation: none;
  }
}

.rail__tokens {
  grid-column: 1 / -1;
  font-size: 11px;
  color: var(--kx-muted);
}

.rail__empty {
  margin: 0;
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-muted);
}
</style>
