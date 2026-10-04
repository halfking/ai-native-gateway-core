<script setup lang="ts">
// BoardDistGrid.vue — 分布分析排行卡（2026-09-30 重构轮，取代原 7 饼图中的 5 个）。
// 客户端类型 / 来源 IP / 身份指纹（截断）/ 错误类型（可下钻，特有）/ 租户用量（特有）。
// 供应商→成本采购区、模型→模型分布表，各自独立成块。
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { fetchBoardErrorDrill, isBoardPieDegraded, type BoardPayload, type BoardPieItem } from '../../api/board'

const props = defineProps<{
  board: BoardPayload | null | undefined
  days: number
  loading?: boolean
}>()

const { t } = useI18n()

const metric = ref<'requests' | 'tokens'>('requests')
const TOP_N = 4

interface RankCard {
  id: string
  titleKey: string
  items: BoardPieItem[]
  truncate?: boolean
  danger?: boolean
  clickable?: boolean
  /**
   * 该维度是否处于降级（服务端没算出来）。
   * 与「空数组」严格区分：空数组是合法答案，降级不是。
   */
  degraded?: boolean
}

const cards = computed<RankCard[]>(() => [
  { id: 'clients', titleKey: 'dashboard.board.pieClients', items: props.board?.pies?.clients ?? [], degraded: isBoardPieDegraded(props.board, 'clients') },
  { id: 'errors', titleKey: 'dashboard.board.pieErrors', items: props.board?.pies?.errors ?? [], danger: true, clickable: true, degraded: isBoardPieDegraded(props.board, 'errors') },
  { id: 'identity', titleKey: 'dashboard.board.pieIdentity', items: props.board?.pies?.identity_hashes ?? [], truncate: true, degraded: isBoardPieDegraded(props.board, 'identity_hashes') },
  { id: 'tenants', titleKey: 'dashboard.board.pieTenants', items: props.board?.pies?.tenants ?? [], degraded: isBoardPieDegraded(props.board, 'tenants') },
  { id: 'ips', titleKey: 'dashboard.board.pieClientIp', items: props.board?.pies?.client_ips ?? [], degraded: isBoardPieDegraded(props.board, 'client_ips') },
])

function topItems(items: BoardPieItem[]) {
  return [...items].sort((a, b) => (b[metric.value] ?? 0) - (a[metric.value] ?? 0)).slice(0, TOP_N)
}

function restCount(items: BoardPieItem[]) {
  return Math.max(0, items.length - TOP_N)
}

function maxOf(items: BoardPieItem[]) {
  return Math.max(...topItems(items).map((p) => p[metric.value] ?? 0), 1)
}

function totalOf(items: BoardPieItem[]) {
  return items.reduce((acc, p) => acc + (p[metric.value] ?? 0), 0) || 1
}

function pct(items: BoardPieItem[], item: BoardPieItem) {
  return ((item[metric.value] ?? 0) / totalOf(items) * 100).toFixed(1) + '%'
}

function fmtValue(n: number | undefined) {
  if (n == null) return '—'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return Number(n).toLocaleString()
}

function shortKey(key: string) {
  return key.length > 14 ? `${key.slice(0, 6)}…${key.slice(-4)}` : key
}

function restSum(items: BoardPieItem[]) {
  const topKeys = new Set(topItems(items).map((item) => item.key))
  return items.filter((item) => !topKeys.has(item.key)).reduce((acc, item) => acc + (item[metric.value] ?? 0), 0)
}

function restLabel(items: BoardPieItem[], asCount: boolean) {
  const rest = restSum(items)
  if (asCount) return fmtValue(rest)
  return ((rest / totalOf(items)) * 100).toFixed(1) + '%'
}

function restWidth(items: BoardPieItem[]) {
  const pct = (restSum(items) / maxOf(items)) * 100
  return Math.min(100, Math.max(2, pct)).toFixed(1) + '%'
}

// ── 错误下钻（保留原有交互：三维度 tab + fetchBoardErrorDrill） ──
const errorDrillKind = ref<string | null>(null)
const errorDrillDim = ref<'model' | 'provider' | 'client'>('model')
const errorDrillItems = ref<BoardPieItem[]>([])
const errorDrillLoading = ref(false)
// 2026-10-03：原来这里是 `catch { errorDrillItems.value = [] }`。
// 后端失败时写 500 + error.detail，前端却把它丢成空数组 ⇒
// 下钻面板显示「暂无数据」——用户点开「错误下钻」看错误构成，
// 看到的是「没有错误」，而真相是「这次查询失败了」。
// 与本轮 pie 降级、credits 降级同族：把「不知道」渲染成「知道」。
const errorDrillError = ref<string | null>(null)

async function onErrorClick(kind: string) {
  errorDrillKind.value = kind
  errorDrillLoading.value = true
  errorDrillError.value = null
  // 这行清空在**当前模板下是冗余的**（2026-10-03 变异实测）：
  // 面板分支是 v-if=loading → v-else-if=error → v-else-if=!items，
  // 加载期间旧行被 loading 分支遮住，失败态也遮住，旧行泄漏**不可见**。
  //
  // 保留它是为了防一个具体的将来变化：若有人把 v-else-if 链改成
  // 并列渲染（例如给 loading 加骨架屏而不隐藏列表），这行就是唯一的防线。
  // 抽掉它的变异在**今天**不报红 —— 这不是判据失效，是判据选的维度
  // （渲染结果）看不见它，而那正是它当前不承担风险的原因。
  errorDrillItems.value = []
  try {
    const res = await fetchBoardErrorDrill({
      error_kind: kind,
      days: props.days,
      dimension: errorDrillDim.value,
    })
    errorDrillItems.value = res.items
  } catch (err) {
    // 保留服务端给出的原因；只有拿不到可读原因时才退回通用文案。
    const detail = (err as { detail?: string; message?: string } | null)
    errorDrillError.value = detail?.detail || detail?.message || String(err || '')
  } finally {
    errorDrillLoading.value = false
  }
}

async function onDrillDimChange(dim: 'model' | 'provider' | 'client') {
  errorDrillDim.value = dim
  if (errorDrillKind.value) await onErrorClick(errorDrillKind.value)
}
</script>

<template>
  <section class="dist">
    <div class="dist__bar">
      <h4 class="dist__title">{{ t('dashboard.board.distTitle') }}</h4>
      <div class="dist__metric" role="radiogroup" :aria-label="t('dashboard.board.metricToggle')">
        <button
          v-for="m in (['requests', 'tokens'] as const)"
          :key="m"
          type="button"
          class="dist__metric-btn"
          :class="{ active: metric === m }"
          :aria-pressed="metric === m"
          @click="metric = m"
        >
          {{ m === 'requests' ? t('dashboard.board.metricRequests') : t('dashboard.board.metricTokens') }}
        </button>
      </div>
    </div>

    <div class="dist__grid">
      <div v-for="card in cards" :key="card.id" class="dist-card" :class="{ 'dist-card--danger': card.danger }">
        <h6 class="dist-card__title">
          {{ t(card.titleKey) }}
          <span class="dist-card__cs">{{ t('dashboard.board.distTop', { n: TOP_N }) }}</span>
        </h6>
        <div v-if="loading && !card.items.length" class="dist-card__skeleton" />
        <div v-else-if="card.degraded" class="dist-card__degraded">
          {{ t('dashboard.board.pieDegraded', { reason: props.board?.degraded_pies?.reason || t('dashboard.board.pieDegradedGeneric') }) }}
        </div>
        <div v-else-if="!card.items.length" class="dist-card__empty">{{ t('dashboard.board.empty') }}</div>
        <div v-else class="dist-card__rank">
          <div
            v-for="item in topItems(card.items)"
            :key="item.key"
            class="rank-row"
            :class="{ 'rank-row--click': card.clickable }"
            :role="card.clickable ? 'button' : undefined"
            @click="card.clickable && onErrorClick(item.key)"
          >
            <span class="rank-row__name" :title="item.key">{{ card.truncate ? shortKey(item.key) : item.key }}</span>
            <span class="rank-row__bar" aria-hidden="true">
              <i :style="{ width: Math.max(2, ((item[metric] ?? 0) / maxOf(card.items)) * 100).toFixed(1) + '%' }"></i>
            </span>
            <span class="rank-row__val">{{ card.clickable ? fmtValue(item.requests) : pct(card.items, item) }}</span>
          </div>
          <div v-if="restCount(card.items) > 0" class="rank-row rank-row--rest">
            <span class="rank-row__name">{{ t('dashboard.board.distOthers', { n: restCount(card.items) }) }}</span>
            <span class="rank-row__bar" aria-hidden="true"><i :style="{ width: restWidth(card.items) }"></i></span>
            <span class="rank-row__val">{{ restLabel(card.items, !!card.clickable) }}</span>
          </div>
          <div v-if="card.clickable" class="dist-card__hint">{{ t('dashboard.board.distDrillHint') }}</div>
        </div>
      </div>
    </div>

    <div v-if="errorDrillKind" class="drill-panel">
      <div class="drill-panel__header">
        <span>{{ t('dashboard.board.errorDrill', { kind: errorDrillKind }) }}</span>
        <div class="drill-tabs">
          <button type="button" :class="{ active: errorDrillDim === 'model' }" @click="onDrillDimChange('model')">{{ t('dashboard.board.drillModel') }}</button>
          <button type="button" :class="{ active: errorDrillDim === 'provider' }" @click="onDrillDimChange('provider')">{{ t('dashboard.board.drillProvider') }}</button>
          <button type="button" :class="{ active: errorDrillDim === 'client' }" @click="onDrillDimChange('client')">{{ t('dashboard.board.drillClient') }}</button>
          <button type="button" class="drill-close" :aria-label="t('common.button.close')" @click="errorDrillKind = null">×</button>
        </div>
      </div>
      <div v-if="errorDrillLoading" class="drill-panel__loading">{{ t('dashboard.loading') }}</div>
      <div v-else-if="errorDrillError" class="drill-panel__error" role="alert">
        {{ t('dashboard.board.drillFailed', { reason: errorDrillError }) }}
      </div>
      <div v-else-if="!errorDrillItems.length" class="drill-panel__loading">{{ t('dashboard.board.empty') }}</div>
      <div v-else class="drill-panel__list">
        <div v-for="item in errorDrillItems.slice(0, 8)" :key="item.key" class="drill-row">
          <span class="drill-row__name" :title="item.key">{{ item.key }}</span>
          <span class="drill-row__bar" aria-hidden="true">
            <i :style="{ width: Math.max(2, (item.requests / Math.max(...errorDrillItems.map((x) => x.requests), 1)) * 100).toFixed(1) + '%' }"></i>
          </span>
          <span class="drill-row__val">{{ fmtValue(item.requests) }}</span>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.dist {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.dist__bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
}
.dist__title {
  font-size: 13.5px;
  font-weight: 700;
  margin: 0;
}
.dist__metric {
  display: inline-flex;
  gap: 2px;
  padding: 2px;
  border: 1px solid var(--border);
  border-radius: 7px;
  background: var(--bg-subtle);
}
.dist__metric-btn {
  border: 0;
  background: transparent;
  color: var(--text-muted);
  font-size: 11.5px;
  padding: 3px 9px;
  border-radius: 5px;
  cursor: pointer;
  font-weight: 600;
}
.dist__metric-btn.active {
  background: color-mix(in srgb, var(--accent) 14%, transparent);
  color: var(--accent);
}
.dist__grid {
  display: grid;
  grid-template-columns: repeat(5, 1fr);
  gap: 10px;
}
.dist-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 11px 13px;
  min-width: 0;
}
.dist-card--danger .rank-row__bar i {
  background: var(--danger);
}
.dist-card__title {
  font-size: 12.5px;
  font-weight: 700;
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  gap: 6px;
  margin: 0 0 8px;
  overflow: hidden;
  white-space: nowrap;
}
.dist-card__cs {
  color: var(--text-muted);
  font-weight: 500;
  font-size: 10.5px;
  flex-shrink: 0;
}
.rank-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 7px 0;
  font-size: 11.5px;
}
.rank-row--click {
  cursor: pointer;
}
.rank-row--click:hover .rank-row__name {
  color: var(--accent);
}
.rank-row__name {
  flex: 0 0 42%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text);
}
.rank-row__bar {
  flex: 1;
  height: 5px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--text-muted) 16%, transparent);
  overflow: hidden;
}
.rank-row__bar i {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: var(--accent);
}
.rank-row__val {
  flex: 0 0 auto;
  font-variant-numeric: tabular-nums;
  color: var(--text-muted);
  font-size: 11px;
}
.rank-row--rest .rank-row__name {
  color: var(--text-muted);
}
.dist-card__hint {
  margin-top: 6px;
  font-size: 10.5px;
  color: var(--accent);
}
.dist-card__empty {
  font-size: 12px;
  color: var(--text-muted);
  padding: 10px 0;
}
/* 降级态刻意用 warning 色而非 muted：空态是「确实没有」，降级是「不知道」。
   两者的视觉权重必须不同，否则用户读到的是同一个结论。 */
.dist-card__degraded {
  font-size: 12px;
  color: var(--warning, #e6a23c);
  padding: 10px 0;
  line-height: 1.5;
}
.dist-card__skeleton {
  min-height: 90px;
  background: linear-gradient(90deg, var(--border) 25%, transparent 37%, var(--border) 63%);
  background-size: 400% 100%;
  animation: dist-shimmer 1.2s ease infinite;
  border-radius: 8px;
}
@keyframes dist-shimmer {
  0% { background-position: 100% 0; }
  100% { background-position: 0 0; }
}
.drill-panel {
  border: 1px dashed var(--accent);
  border-radius: 10px;
  padding: 10px 12px;
}
.drill-panel__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 8px;
  font-size: 13px;
  font-weight: 600;
}
.drill-tabs button {
  font-size: 12px;
  margin-right: 6px;
  padding: 4px 8px;
  border-radius: 5px;
  border: 1px solid var(--border);
  background: transparent;
  color: var(--text);
  cursor: pointer;
}
.drill-tabs button.active {
  border-color: var(--accent);
  color: var(--accent);
}
.drill-close {
  margin-left: 8px;
}
.drill-panel__loading {
  font-size: 12px;
  color: var(--text-muted);
  padding: 8px 0;
}
/* 失败态用 danger 而非 muted：下钻面板的「暂无数据」意味着「这类错误真的没有」，
   与「查不出来」必须一眼可分。role="alert" 让读屏也会播报。 */
.drill-panel__error {
  font-size: 12px;
  color: var(--danger, #f56c6c);
  padding: 8px 0;
  line-height: 1.5;
}
.drill-panel__list {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 4px 24px;
}
.drill-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 4px 0;
  font-size: 11.5px;
}
.drill-row__name {
  flex: 0 0 44%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.drill-row__bar {
  flex: 1;
  height: 4px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--danger) 15%, transparent);
  overflow: hidden;
}
.drill-row__bar i {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: var(--danger);
}
.drill-row__val {
  flex: 0 0 auto;
  font-variant-numeric: tabular-nums;
  color: var(--text-muted);
}
@media (max-width: 1440px) {
  .dist__grid {
    grid-template-columns: repeat(3, 1fr);
  }
}
@media (max-width: 768px) {
  .dist__grid {
    grid-template-columns: repeat(2, 1fr);
  }
  .drill-panel__list {
    grid-template-columns: 1fr;
  }
}
@media (max-width: 480px) {
  .dist__grid {
    grid-template-columns: 1fr;
  }
}
</style>
