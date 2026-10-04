<script setup lang="ts">
// MaaSAccountView.vue — /tenant/account 页面（我的账户 / 管理员只读账户视图）。
// 2026-07-12: 文案全面接入 i18n。
import { ref, computed, onMounted } from 'vue'
import { formatDateTime } from '../../utils/datetime'
import { useI18n } from 'vue-i18n'
import { localeRef } from '../../i18n'
import { RouterLink } from 'vue-router'
import {
  getMaasAccount,
  getAdminMaasAccount,
  type MaasAccount,
  MAAS_LEDGER_TYPE_LABELS,
  MAAS_POOL_LABELS,
  MAAS_ORDER_STATUS_LABELS,
} from '../../api'
import { useMaasTenantContext } from '../../composables/useMaasTenantContext'
import { useWindowClass } from '../../composables/useWindowClass'
import ResponsiveDataView from '../../components/ui/ResponsiveDataView.vue'
import type { CardField } from '../../components/ui/CardList.vue'
import PageBackLink from '../../components/PageBackLink.vue'

const { t } = useI18n()
const { isCompact } = useWindowClass()

const { tenantLabel, tenantCode, isAdminTenantView, pageTitle: ctxPageTitle, maasBackLink } = useMaasTenantContext()
const pageTitle = computed(() =>
  ctxPageTitle(isAdminTenantView.value ? t('tenants.account.adminTitle') : t('tenants.account.title')),
)
const backLink = computed(() => maasBackLink('account'))

const account = ref<MaasAccount | null>(null)
const loading = ref(false)
const error = ref('')

const pricingLink = computed(() =>
  isAdminTenantView.value
    ? { path: '/tenant/pricing', query: { tenant: tenantCode.value } }
    : { path: '/tenant/pricing' },
)
const usageLink = computed(() =>
  isAdminTenantView.value
    ? { path: '/tenant/usage', query: { tenant: tenantCode.value } }
    : { path: '/tenant/usage' },
)

function fmtCredits(n: number) {
  return n.toLocaleString(localeRef.value)
}

function fmtTime(s: string) {
  if (!s) return '—'
  return formatDateTime(s, { locale: localeRef.value, options: { dateStyle: 'short', timeStyle: 'short' } })
}

function fmtPrice(cents: number) {
  return (cents / 100).toFixed(2)
}

function ledgerTypeLabel(t: string) {
  return MAAS_LEDGER_TYPE_LABELS[t] || t
}

function poolLabel(p: string | null | undefined) {
  if (!p) return '—'
  return MAAS_POOL_LABELS[p] || p
}

function orderStatusLabel(s: string) {
  return MAAS_ORDER_STATUS_LABELS[s] || s
}

function orderTypeLabel(orderType: string) {
  return orderType === 'subscribe' ? t('tenants.account.orderTypeSubscribe') : t('tenants.account.orderTypeTopup')
}

function orderStatusClass(s: string) {
  const map: Record<string, string> = {
    pending: 'badge-yellow',
    paid: 'badge-green',
    cancelled: 'badge-gray',
    expired: 'badge-red',
  }
  return map[s] || 'badge-gray'
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const raw = isAdminTenantView.value
      ? await getAdminMaasAccount(tenantCode.value)
      : await getMaasAccount()
    account.value = {
      ...raw,
      recent_ledger: raw.recent_ledger ?? [],
      recent_orders: raw.recent_orders ?? [],
    }
  } catch (e: unknown) {
    account.value = null
    error.value = e instanceof Error ? e.message : t('tenants.account.loadFailed')
  } finally {
    loading.value = false
  }
}

/**
 * ── H6 第七条切片（2026-10-06）：两张表接 compact 卡片形态 ──────────────────
 * 本页**没有分页 API**：两张表都随 `getMaasAccount()` 一次取回
 * （`recent_orders` / `recent_ledger`），所以这里只改**呈现形态**，
 * 不引入连续加载 —— 呈现形态与加载方式本来就是两个独立维度（规范 03 §1）。
 *
 * 两处与既有切片不同的取舍：
 * 1. **桌面空态仍在本页**（两个 `.empty` div），所以容器的 `:empty` 带
 *    `isCompact` 前置（03 §4 例外条款：桌面刷新时表格在不在）。
 * 2. **`#table` 槽内保留 `v-if`**。空态时桌面原本**整张表都不渲染**；
 *    若把 `v-if` 提到容器外，空态下容器仍会渲染一个带边框的空表壳
 *    —— 那是桌面观感的静默变化。
 * 3. **不传 `:loading`**：两张表没有独立加载态（整页一次取），传了就是死代码，
 *    而且刷新时会让 compact 闪一个转圈、桌面却仍显示旧行。
 */

/** 订单金额：分 → ¥ 两位小数。与表格那一格共用，不写第二份。 */
function orderAmountText(cents: number): string {
  return `¥${fmtPrice(cents)}`
}

/** 台账变动额：正数带 `+`（表格与卡片同一套规则）。 */
function ledgerAmountText(amount: number): string {
  return `${amount > 0 ? '+' : ''}${fmtCredits(amount)}`
}

/** 订单状态 → 卡片 tone。桌面是四支 class，卡片是四支强调色，同一份映射。 */
function orderStatusTone(s: string): 'neutral' | 'good' | 'warn' | 'danger' {
  if (s === 'paid') return 'good'
  if (s === 'pending') return 'warn'
  if (s === 'expired') return 'danger'
  return 'neutral'
}

const orderCardFields = computed<CardField[]>(() => [
  { key: 'order_type', label: t('tenants.account.orderType'), format: (v) => orderTypeLabel(String(v)) },
  { key: 'amount_cents', label: t('tenants.account.orderAmount'), format: (v) => orderAmountText(Number(v)) },
  { key: 'credits', label: t('tenants.account.orderCredits'), format: (v) => fmtCredits(Number(v)) },
  {
    key: 'status',
    label: t('tenants.account.orderStatus'),
    type: 'badge',
    // ★ 逐行求值：状态色是**每行**的（待支付/已支付/已取消可同页共存），
    //   字段级常量会把整列表按第一行的状态上色。
    tone: (row) => orderStatusTone(String(row.status ?? '')),
    format: (v) => orderStatusLabel(String(v)),
  },
  { key: 'created_at', label: t('tenants.account.orderTime'), format: (v) => fmtTime(String(v)) },
])

const ledgerCardFields = computed<CardField[]>(() => [
  { key: 'created_at', label: t('tenants.account.ledgerTime'), format: (v) => fmtTime(String(v)) },
  { key: 'pool', label: t('tenants.account.ledgerPool'), format: (v) => poolLabel(v as string | null | undefined) },
  { key: 'amount', label: t('tenants.account.ledgerDelta'), align: 'end', format: (v) => ledgerAmountText(Number(v)) },
  { key: 'balance_after', label: t('tenants.account.ledgerBalance'), align: 'end', format: (v) => fmtCredits(Number(v)) },
  { key: 'note', label: t('tenants.account.ledgerNote'), format: (v) => (v ? String(v) : '—') },
])

/** 台账没有天然标题，用类型作卡头（原始 `entry_type` 是机器码，出卡头不合适）。 */
function ledgerCardTitle(row: Record<string, unknown>): string {
  return ledgerTypeLabel(String(row.entry_type ?? ''))
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <PageBackLink v-if="backLink" :to="backLink.to" :label="backLink.label" />
      <h2>{{ pageTitle }}</h2>
      <div class="page-header-actions">
        <span class="tenant-badge">{{ tenantLabel }}</span>
        <RouterLink v-if="!isAdminTenantView" to="/tenant/pricing" class="btn btn-primary btn-sm">
          {{ t('tenants.account.buyCredits') }}
        </RouterLink>
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
          {{ loading ? t('tenants.account.loading') : t('tenants.account.refresh') }}
        </button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-else-if="loading && !account" class="empty">{{ t('tenants.account.loading') }}</div>

    <div v-if="account" class="wallet-grid">
      <div class="wallet-card card">
        <div class="pool-label">{{ t('tenants.account.walletQuotaRemaining') }}</div>
        <div class="pool-value">{{ fmtCredits(account.wallet.quota_remaining) }}</div>
        <div class="pool-hint">{{ t('tenants.account.walletQuotaHint') }}</div>
        <div v-if="account.wallet.subscription" class="sub-info">
          {{ t('tenants.account.subscriptionInfo', {
            plan: account.wallet.subscription.plan_name,
            date: fmtTime(account.wallet.subscription.period_end),
          }) }}
        </div>
      </div>
      <div class="wallet-card card">
        <div class="pool-label">{{ t('tenants.account.walletGrantedBalance') }}</div>
        <div class="pool-value">{{ fmtCredits(account.wallet.granted_balance) }}</div>
        <div class="pool-hint">{{ t('tenants.account.walletGrantedHint') }}</div>
      </div>
      <div class="wallet-card card">
        <div class="pool-label">{{ t('tenants.account.walletPurchasedBalance') }}</div>
        <div class="pool-value">{{ fmtCredits(account.wallet.purchased_balance) }}</div>
        <div class="pool-hint">{{ t('tenants.account.walletPurchasedHint') }}</div>
      </div>
      <div class="wallet-card card highlight">
        <div class="pool-label">{{ t('tenants.account.walletTotalAvailable') }}</div>
        <div class="pool-value">{{ fmtCredits(account.wallet.total_available) }}</div>
        <div class="pool-hint">{{ t('tenants.account.walletTotalHint') }}</div>
      </div>
    </div>

    <div v-if="account" class="section card">
      <div class="section-header">
        <h3>{{ t('tenants.account.recentOrders') }}</h3>
        <RouterLink v-if="!isAdminTenantView" :to="pricingLink" class="link-sm">{{ t('tenants.account.goBuy') }}</RouterLink>
      </div>
      <ResponsiveDataView
        :rows="account.recent_orders"
        title-key="order_no"
        :fields="orderCardFields"
        table-min-width="0px"
        :empty="isCompact && account.recent_orders.length === 0"
        :empty-text="t('tenants.account.emptyOrders')"
      >
        <template #table>
          <table v-if="account.recent_orders.length" class="table">
            <thead>
              <tr>
                <th>{{ t('tenants.account.orderNo') }}</th>
                <th>{{ t('tenants.account.orderType') }}</th>
                <th>{{ t('tenants.account.orderAmount') }}</th>
                <th>{{ t('tenants.account.orderCredits') }}</th>
                <th>{{ t('tenants.account.orderStatus') }}</th>
                <th>{{ t('tenants.account.orderTime') }}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="o in account.recent_orders" :key="o.id">
                <td class="mono">{{ o.order_no }}</td>
                <td>{{ orderTypeLabel(o.order_type) }}</td>
                <td>¥{{ fmtPrice(o.amount_cents) }}</td>
                <td>{{ fmtCredits(o.credits) }}</td>
                <td><span class="badge" :class="orderStatusClass(o.status)">{{ orderStatusLabel(o.status) }}</span></td>
                <td class="mono">{{ fmtTime(o.created_at) }}</td>
                <td>
                  <RouterLink v-if="o.status === 'pending'" :to="`/tenant/orders/${o.id}`" class="link-sm">
                    {{ t('tenants.account.orderPayLink') }}
                  </RouterLink>
                </td>
              </tr>
            </tbody>
          </table>
        </template>
        <template #actions="{ row }">
          <RouterLink
            v-if="row.status === 'pending'"
            :to="`/tenant/orders/${row.id}`"
            class="link-sm"
          >
            {{ t('tenants.account.orderPayLink') }}
          </RouterLink>
        </template>
      </ResponsiveDataView>
      <div v-if="!isCompact && !account.recent_orders.length" class="empty">{{ t('tenants.account.emptyOrders') }}</div>
    </div>

    <div v-if="account" class="section card">
      <div class="section-header">
        <h3>{{ t('tenants.account.recentLedger') }}</h3>
        <RouterLink :to="usageLink" class="link-sm">{{ t('tenants.account.consumptionStats') }}</RouterLink>
      </div>
      <ResponsiveDataView
        :rows="account.recent_ledger"
        title-key="entry_type"
        :title-format="ledgerCardTitle"
        :fields="ledgerCardFields"
        table-min-width="0px"
        :empty="isCompact && account.recent_ledger.length === 0"
        :empty-text="t('tenants.account.emptyLedger')"
      >
        <template #table>
          <table v-if="account.recent_ledger.length" class="table">
            <thead>
              <tr>
                <th>{{ t('tenants.account.ledgerTime') }}</th>
                <th>{{ t('tenants.account.ledgerType') }}</th>
                <th>{{ t('tenants.account.ledgerPool') }}</th>
                <th style="text-align:right">{{ t('tenants.account.ledgerDelta') }}</th>
                <th style="text-align:right">{{ t('tenants.account.ledgerBalance') }}</th>
                <th>{{ t('tenants.account.ledgerNote') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="e in account.recent_ledger" :key="e.id">
                <td class="mono">{{ fmtTime(e.created_at) }}</td>
                <td>{{ ledgerTypeLabel(e.entry_type) }}</td>
                <td>{{ poolLabel(e.pool) }}</td>
                <td class="mono" style="text-align:right">{{ e.amount > 0 ? '+' : '' }}{{ fmtCredits(e.amount) }}</td>
                <td class="mono" style="text-align:right">{{ fmtCredits(e.balance_after) }}</td>
                <td>{{ e.note || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </template>
      </ResponsiveDataView>
      <div v-if="!isCompact && !account.recent_ledger.length" class="empty">{{ t('tenants.account.emptyLedger') }}</div>
    </div>

    <div v-else-if="!loading && !error" class="section card empty">
      {{ t('tenants.account.emptyAccount') }}
    </div>
  </div>
</template>

<style scoped>
.page-header-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}
.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
}
.wallet-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px;
  margin-bottom: 20px;
}
.wallet-card {
  padding: 16px;
}
.wallet-card.highlight {
  border-color: var(--accent-h);
}
.pool-label {
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 4px;
}
.pool-value {
  font-size: 24px;
  font-weight: 700;
}
.pool-hint {
  font-size: 11px;
  color: var(--muted);
  margin-top: 4px;
}
.sub-info {
  font-size: 11px;
  color: var(--accent-h);
  margin-top: 6px;
}
.section {
  padding: 16px;
  margin-bottom: 16px;
}
.section-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.section-header h3 {
  margin: 0;
  font-size: 15px;
}
.empty {
  text-align: center;
  padding: 24px;
  color: var(--muted);
}
.mono { font-family: 'SF Mono', 'Fira Code', monospace; font-size: 12px; }
.link-sm { font-size: 12px; color: var(--accent-h); text-decoration: none; }
.badge-yellow { background: var(--warning-bg); color: var(--warning); padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-green { background: var(--success-bg); color: var(--success); padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-red { background: var(--danger-bg); color: var(--danger); padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-gray { background: var(--neutral-bg); color: var(--muted); padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.tenant-badge {
  display: inline-flex;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  background: var(--surface-secondary);
  color: var(--text-secondary);
}
.alert-danger { padding: 8px 12px; border-radius: 4px; background: var(--danger-bg); color: var(--danger); margin-bottom: 12px; }
</style>