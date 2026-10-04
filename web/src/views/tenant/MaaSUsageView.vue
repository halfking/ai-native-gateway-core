<script setup lang="ts">
// MaaSUsageView.vue — /tenant/usage 页面（我的消耗 / 管理员只读消耗视图）。
// 2026-07-12: 文案全面接入 i18n。
import { ref, computed, onMounted } from 'vue'
import { formatDateTime } from '../../utils/datetime'
import { useI18n } from 'vue-i18n'
import { localeRef } from '../../i18n'
import { RouterLink } from 'vue-router'
import {
  getMaasLedger,
  getAdminMaasLedger,
  getMaasUsageSummary,
  getAdminMaasUsageSummary,
  MAAS_LEDGER_TYPE_LABELS,
} from '../../api'
import type { MaasLedgerEntry, MaasUsageSummary } from '../../api'
import { useMaasTenantContext } from '../../composables/useMaasTenantContext'
import PageBackLink from '../../components/PageBackLink.vue'
import KxDateRangePicker from '../../components/ui/KxDateRangePicker.vue'
import { useSpanDaysRange } from '../../composables/useSpanDaysRange'
import type { KxDateRange } from '../../components/ui/kx-date-types'
import { useWindowClass } from '../../composables/useWindowClass'
import ResponsiveDataView from '../../components/ui/ResponsiveDataView.vue'
import type { CardField } from '../../components/ui/CardList.vue'
import FeeCostCell from '../../components/FeeCostCell.vue'

const { t } = useI18n()
const { isCompact } = useWindowClass()

const { tenantLabel, tenantCode, isAdminTenantView, pageTitle: ctxPageTitle, maasBackLink } = useMaasTenantContext()
const pageTitle = computed(() =>
  ctxPageTitle(isAdminTenantView.value ? t('tenants.usage.adminTitle') : t('tenants.usage.title')),
)
const backLink = computed(() => maasBackLink('usage'))

const days = ref(7)
const { presets: dayPresets, rangeValue, applyRange } = useSpanDaysRange(days, [
  { days: 7, labelKey: 'tenants.usage.days7' },
  { days: 30, labelKey: 'tenants.usage.days30' },
])

function onRangeApply(range: KxDateRange) {
  applyRange(range)
  void load()
}
const limit = ref(50)
const summary = ref<MaasUsageSummary | null>(null)
const ledger = ref<MaasLedgerEntry[]>([])
const loading = ref(false)
const error = ref('')

const consumeTotal = computed(() =>
  ledger.value
    .filter((e) => e.entry_type === 'consume')
    .reduce((sum, e) => sum + Math.abs(e.amount), 0),
)

const recentConsumeCount = computed(() =>
  ledger.value.filter((e) => e.entry_type === 'consume').length,
)

const maxModelCredits = computed(() => {
  const rows = summary.value?.by_model ?? []
  return Math.max(1, ...rows.map((r) => r.credits))
})

const maxTrendCredits = computed(() => {
  const rows = summary.value?.trend ?? []
  return Math.max(1, ...rows.map((r) => r.credits))
})

const maxTrendRequests = computed(() => {
  const rows = summary.value?.trend ?? []
  return Math.max(1, ...rows.map((r) => r.requests))
})

const pricingLink = computed(() =>
  isAdminTenantView.value
    ? { path: '/tenant/pricing', query: { tenant: tenantCode.value } }
    : { path: '/tenant/pricing' },
)

function fmtCredits(n: number) {
  const sign = n > 0 ? '+' : ''
  return sign + n.toLocaleString(localeRef.value)
}

function fmtNum(n: number | undefined) {
  if (n === undefined || n === null) return '—'
  return n.toLocaleString(localeRef.value)
}

function fmtTime(s: string) {
  if (!s) return '—'
  return formatDateTime(s, { locale: localeRef.value })
}

function typeLabel(entryType: string) {
  return MAAS_LEDGER_TYPE_LABELS[entryType] || entryType
}

function typeBadgeClass(entryType: string) {
  if (entryType === 'consume') return 'badge-red'
  if (entryType === 'topup') return 'badge-green'
  return 'badge-blue'
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [sum, led] = await Promise.all([
      isAdminTenantView.value
        ? getAdminMaasUsageSummary(tenantCode.value, days.value, 10)
        : getMaasUsageSummary(days.value, 10),
      isAdminTenantView.value
        ? getAdminMaasLedger(tenantCode.value, limit.value)
        : getMaasLedger(limit.value),
    ])
    summary.value = {
      ...sum,
      by_model: sum.by_model ?? [],
      trend: sum.trend ?? [],
    }
    ledger.value = led.items ?? []
  } catch (e: unknown) {
    summary.value = null
    ledger.value = []
    error.value = e instanceof Error ? e.message : t('tenants.usage.loadFailed')
  } finally {
    loading.value = false
  }
}

onMounted(load)

/**
 * ── H6 第十四条切片（2026-10-06）：账本科表接 compact 卡片形态 ───────────────────
 *
 * ## 无分页 API ⇒ 只改呈现形态
 *
 * `getMaasLedger(limit)` 一次取回整段（`limit` 是「取多少条」的下拉，不是分页器，
 * 没有 offset）⇒ 与第七/八/十/十二/十三同源，**不引入连续加载**。
 *
 * ## 三态归属：**表壳不能空**（切片七同源），但本页是它的**反身**
 *
 * 桌面是 `<table>` **恒渲染** + 空态做成 `<tbody>` 里的一行
 * （`<tr v-if="!loading && ledger.length === 0"><td colspan="6">`）。
 * 切片七/十三的 `#table` 槽内保留 `v-if` 是为了**别多出空表壳**；
 * 本页反过来 —— 空态**就在壳里面**，所以：
 * - `#table` 槽里**不加** `v-if`（加了就把桌面空态行一起删掉）；
 * - compact 的空态只能来自容器的 `:empty`，且必须带 `!loading` 前置，
 *   否则**加载中**（桌面此刻不出空态行）会凭空多出一个空态 —— 13 §7「失败态不显示空态」
 *   的同族病灶。
 *
 * ## 卡头用 `titleFormat` 出 typeLabel，键仍是 `id`
 *
 * `entry_type` 只有 3 个取值，**拿它当 `titleKey` 必然撞键**（同一类型多行）⇒
 * `title-key="id"`（后端主键，逐行唯一），卡头文本走 `titleFormat`。
 * 前几条切片不需要 `titleFormat` 是因为卡头与键同源；这里是**键与脸必须不同**的那种。
 *
 * ## 卡片的**唯一取舍**：桌面 type 徽章的颜色不出现在卡片上
 *
 * 桌面有两处独立的颜色：type 徽章（`badge-red/green/blue`，按 `entry_type`）
 * 与金额符号（`amount-neg` → `--danger` / `amount-pos` → `--success`，按 `amount` 符号）。
 * 卡片保留**后者**（`tone` 按金额符号逐行求值），前者只留文本。
 * 理由：消费（红）与充值（绿）在账本里金额符号与 type 同向，颜色信息没丢；
 * 代价是第三种 type 的蓝色不出来了。**这是有界的取舍，不是漏**，
 * 门禁断桌面两处颜色都还在（零回归），并断卡片 tone 逐行。
 *
 * ## 空态的 CTA 不降级
 *
 * 桌面空态那一格里还有「去购买额度」的 `RouterLink`。`emptyText` 只能装纯文本
 * ⇒ 把它降成一句话就是**删掉该屏唯一的 CTA**。因此用 `ResponsiveDataView` 的
 * `#empty` 透传口（本次为该组件新加的插槽，见组件注释）。
 *
 * ## 表格与卡片共用同一份格式化函数
 *
 * `fmtTime` / `fmtCredits` / `fmtNum` / `typeLabel` 桌面原本就在用，抽成卡片字段的
 * `format` 复用即可，**不写第二份**。
 */

/**
 * 卡头文本 = 类型标签。`:key` 仍是 `id`（`entry_type` 只有 5 个取值，
 * 拿它当 `titleKey` 必然撞键 —— fixture 里 consume 有两行）。
 * 未知类型回落 `typeLabel` 自己的 `|| entryType`（桌面表格同一个口径）。
 */
function ledgerTitle(row: Record<string, unknown>): string {
  return typeLabel(String(row.entry_type ?? ''))
}

/** 账本卡片字段。5 个：时间 / 变动 / 余额 / 关联 / 备注（type 走卡头，不重复）。 */
const ledgerCardFields = computed<CardField[]>(() => [
  { key: 'created_at', label: t('tenants.usage.colTime'), format: (v) => fmtTime(v == null ? '' : String(v)) },
  {
    key: 'amount',
    label: t('tenants.usage.colDelta'),
    type: 'metric',
    align: 'end',
    // 与桌面的 `amount-neg` / `amount-pos` 同一判据：按**金额符号**，不是按 type
    tone: (row) => {
      const n = Number(row.amount)
      if (n < 0) return 'danger'
      if (n > 0) return 'good'
      return undefined
    },
    format: (v) => fmtCredits(Number(v)),
  },
  { key: 'balance_after', label: t('tenants.usage.colBalance'), align: 'end', format: (v) => fmtNum(v == null ? undefined : Number(v)) },
  {
    key: 'ref_type',
    label: t('tenants.usage.colRef'),
    // 桌面是一格里两个 span（`ref_type` + `ref_id`），卡片 `dd` 只能给一个字符串
    format: (v, row) => {
      const type = v == null ? '' : String(v)
      const id = row.ref_id == null ? '' : String(row.ref_id)
      if (!type && !id) return '—'
      return [type, id].filter(Boolean).join(' ')
    },
  },
  { key: 'note', label: t('tenants.usage.colNote'), format: (v) => (v ? String(v) : '—') },
])
</script>

<template>
  <div>
    <div class="page-header">
      <PageBackLink v-if="backLink" :to="backLink.to" :label="backLink.label" />
      <h2>{{ pageTitle }}</h2>
      <div class="page-header-actions">
        <span class="tenant-badge tenant-badge--admin">{{ tenantLabel }}</span>
        <KxDateRangePicker
          :model-value="rangeValue"
          :presets="dayPresets"
          :max-span-days="30"
          @apply="onRangeApply"
        />
        <select v-model.number="limit" class="limit-select" @change="load">
          <option :value="50">{{ t('tenants.usage.ledgerLimit50') }}</option>
          <option :value="100">{{ t('tenants.usage.ledgerLimit100') }}</option>
          <option :value="200">{{ t('tenants.usage.ledgerLimit200') }}</option>
        </select>
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
          {{ loading ? t('tenants.usage.loading') : t('tenants.usage.refresh') }}
        </button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-else-if="loading && !summary" class="empty">{{ t('tenants.usage.loading') }}</div>

    <div v-if="summary" class="stat-cards">
      <div class="stat-card card">
        <div class="stat-label">{{ t('tenants.usage.statCreditsConsumed') }}</div>
        <div class="stat-value stat-value--fee">
          <FeeCostCell
            inline
            :credits="summary.total_credits"
            :cost-usd="summary.total_cost_usd"
            :show-cost="isAdminTenantView"
          />
        </div>
        <div class="stat-hint">
          {{ t('tenants.usage.statCreditsConsumedHint', { days, n: fmtNum(summary.total_requests) }) }}
        </div>
      </div>
      <div class="stat-card card">
        <div class="stat-label">{{ t('tenants.maasUsageView.ledgerTotalLabel') }}</div>
        <div class="stat-value">{{ fmtNum(consumeTotal) }} <span class="unit">{{ t('common.unit.credits') }}</span></div>
        <div class="stat-hint">
          {{ t('tenants.maasUsageView.ledgerTotalHint', { limit, count: recentConsumeCount }) }}
        </div>
      </div>
    </div>

    <div v-if="summary" class="card chart-card">
      <div class="card-title">
        {{ t('tenants.usage.trendTitle') }}
        <span class="hint">{{ t('tenants.usage.trendCredits') }} · {{ t('tenants.usage.trendRequests') }}</span>
      </div>
      <div v-if="!summary.trend.length" class="empty">
        {{ t('tenants.usage.emptyTrend', { days }) }}
        <RouterLink v-if="!isAdminTenantView" :to="pricingLink">{{ t('tenants.usage.emptyTrendBuyCredits') }}</RouterLink>
        <span v-else>{{ t('tenants.usage.emptyTrendAdminHint') }}</span>
      </div>
      <div v-else class="trend-grid">
        <div class="trend-section">
          <div class="trend-label">{{ t('tenants.usage.trendCredits') }}</div>
          <div class="trend-bars">
            <div
              v-for="row in summary.trend"
              :key="'c-' + row.date"
              class="trend-col"
              :title="`${row.date}: ${row.credits} ${t('common.unit.credits')}`"
            >
              <span
                class="trend-bar credits"
                :style="{ height: (row.credits / maxTrendCredits * 100) + '%' }"
              />
              <span class="trend-date">{{ row.date.slice(5) }}</span>
            </div>
          </div>
        </div>
        <div class="trend-section">
          <div class="trend-label">{{ t('tenants.usage.trendRequests') }}</div>
          <div class="trend-bars">
            <div
              v-for="row in summary.trend"
              :key="'r-' + row.date"
              class="trend-col"
              :title="`${row.date}: ${row.requests} ${t('common.unit.requests')}`"
            >
              <span
                class="trend-bar requests"
                :style="{ height: (row.requests / maxTrendRequests * 100) + '%' }"
              />
              <span class="trend-date">{{ row.date.slice(5) }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <div v-if="summary" class="card chart-card">
      <div class="card-title">
        {{ t('tenants.usage.byModel') }}
        <span class="hint">{{ t('tenants.usage.byModelHint') }}</span>
      </div>
      <div v-if="!summary.by_model.length" class="empty">{{ t('tenants.usage.emptyByModel') }}</div>
      <div v-else class="bar-chart">
        <div v-for="row in summary.by_model" :key="row.model" class="bar-row">
          <span class="bar-label" :title="row.model">{{ row.model }}</span>
          <span class="bar-track">
            <span
              class="bar-fill credits"
              :style="{ width: (row.credits / maxModelCredits * 100) + '%' }"
            />
          </span>
          <span class="bar-meta">
            <FeeCostCell
              inline
              :credits="row.credits"
              :cost-usd="row.cost_usd"
              :show-cost="isAdminTenantView"
            />
            · {{ fmtNum(row.requests) }} {{ t('common.unit.requests') }}
          </span>
        </div>
      </div>
    </div>

    <div class="card table-card">
      <h3 class="table-title">{{ t('tenants.usage.ledgerTitle') }}</h3>
      <ResponsiveDataView
        :rows="ledger"
        title-key="id"
        :title-format="ledgerTitle"
        :fields="ledgerCardFields"
        table-min-width="0px"
        :empty="isCompact && !loading && ledger.length === 0"
        :empty-text="t('tenants.usage.emptyLedger')"
      >
        <template #empty>
          {{ t('tenants.usage.emptyLedger') }}
          <RouterLink v-if="!isAdminTenantView" :to="pricingLink">{{ t('tenants.usage.goBuyCredits') }}</RouterLink>
        </template>
        <template #table>
      <table class="table" style="width:100%">
        <thead>
          <tr>
            <th>{{ t('tenants.usage.colTime') }}</th>
            <th>{{ t('tenants.usage.colType') }}</th>
            <th style="text-align:right">{{ t('tenants.usage.colDelta') }}</th>
            <th style="text-align:right">{{ t('tenants.usage.colBalance') }}</th>
            <th>{{ t('tenants.usage.colRef') }}</th>
            <th>{{ t('tenants.usage.colNote') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in ledger" :key="e.id">
            <td class="mono">{{ fmtTime(e.created_at) }}</td>
            <td>
              <span class="badge" :class="typeBadgeClass(e.entry_type)">{{ typeLabel(e.entry_type) }}</span>
            </td>
            <td class="num" :class="{ 'amount-neg': e.amount < 0, 'amount-pos': e.amount > 0 }">
              {{ fmtCredits(e.amount) }}
            </td>
            <td class="num">{{ fmtNum(e.balance_after) }}</td>
            <td class="mono ref-cell">
              <span v-if="e.ref_type">{{ e.ref_type }}</span>
              <span v-if="e.ref_id" class="ref-id">{{ e.ref_id }}</span>
              <span v-if="!e.ref_type && !e.ref_id">—</span>
            </td>
            <td>{{ e.note || '—' }}</td>
          </tr>
          <tr v-if="!loading && ledger.length === 0">
            <td colspan="6" class="empty">
              {{ t('tenants.usage.emptyLedger') }}
              <RouterLink v-if="!isAdminTenantView" :to="pricingLink">{{ t('tenants.usage.goBuyCredits') }}</RouterLink>
            </td>
          </tr>
        </tbody>
      </table>
        </template>
      </ResponsiveDataView>
    </div>
  </div>
</template>

<style scoped>
.page-header-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.limit-select {
  padding: 4px 8px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  font-size: 13px;
}
.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
}
.stat-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 12px;
  margin-bottom: 20px;
}
.stat-card {
  padding: 16px;
  text-align: center;
}
.stat-label {
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 6px;
}
.stat-value {
  font-size: 26px;
  font-weight: 700;
  color: var(--text);
}
.stat-value--fee :deep(.fee-main) {
  font-size: inherit;
  font-weight: inherit;
}
.stat-value--fee :deep(.fee-cost-sub) {
  font-size: 11px;
  font-weight: 400;
}
.stat-value .unit {
  font-size: 13px;
  font-weight: 500;
  color: var(--muted);
}
.stat-hint {
  font-size: 11px;
  color: var(--muted);
  margin-top: 6px;
}
.chart-card {
  padding: 16px;
  margin-bottom: 16px;
}
.card-title {
  font-size: 14px;
  margin: 0 0 12px;
  color: var(--muted);
}
.card-title .hint {
  font-size: 11px;
  font-weight: 400;
  margin-left: 6px;
}
.trend-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}
.trend-label {
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 8px;
}
.trend-bars {
  display: flex;
  align-items: flex-end;
  gap: 4px;
  min-height: 120px;
}
.trend-col {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  min-width: 0;
}
.trend-bar {
  display: block;
  width: 100%;
  max-width: 28px;
  min-height: 2px;
  border-radius: 3px 3px 0 0;
}
.trend-bar.credits { background: var(--accent); }
.trend-bar.requests { background: var(--success); }
.trend-date {
  font-size: 10px;
  color: var(--muted);
  margin-top: 4px;
}
.bar-chart {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.bar-row {
  display: grid;
  grid-template-columns: minmax(80px, 1fr) auto;
  gap: 8px;
  align-items: center;
}
.bar-label {
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.bar-track {
  grid-column: 1 / -1;
  height: 8px;
  background: var(--border);
  border-radius: 4px;
  overflow: hidden;
}
.bar-fill {
  display: block;
  height: 100%;
  border-radius: 4px;
}
.bar-fill.credits { background: var(--accent); }
.bar-meta {
  font-size: 11px;
  color: var(--muted);
  text-align: right;
}
.table-card {
  padding: 16px;
}
.table-title {
  font-size: 14px;
  margin: 0 0 12px;
  color: var(--muted);
}
.mono {
  font-family: 'SF Mono', 'Fira Code', monospace;
  font-size: 12px;
}
.num {
  text-align: right;
  font-family: 'SF Mono', 'Fira Code', monospace;
  font-size: 13px;
}
.amount-neg { color: var(--danger); }
.amount-pos { color: var(--success); }
.ref-cell {
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ref-id {
  display: block;
  font-size: 11px;
  color: var(--muted);
}
.badge {
  padding: 2px 8px;
  border-radius: 8px;
  font-size: 11px;
}
.badge-red { background: var(--danger-bg); color: var(--danger); }
.badge-green { background: var(--success-bg); color: var(--success); }
.badge-blue { background: var(--info-bg); color: var(--accent); }
.empty {
  text-align: center;
  padding: 40px;
  color: var(--muted);
}
.alert-danger {
  padding: 8px 12px;
  border-radius: 4px;
  background: var(--danger-bg);
  color: var(--danger);
  margin-bottom: 12px;
}
.tenant-badge {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  background: var(--surface-secondary);
  color: var(--text-secondary);
}
.tenant-badge--admin {
  background: var(--info-bg);
  color: var(--accent);
}
@media (max-width: 768px) {
  .trend-grid {
    grid-template-columns: 1fr;
  }
}
</style>