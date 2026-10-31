<script setup lang="ts">
// MaaSBillsView.vue — /tenant/bills 月账单 + 优惠券激活 + CSV
import { ref, computed, onMounted } from 'vue'
import { surfaceError } from '../../i18n/errorSurface'
import { useI18n } from 'vue-i18n'
import {
  getMaasBills,
  activateMaasCoupon,
  getMaasCouponRedemptions,
  type MaasMonthlyBill,
  type MaasCouponRedemption,
} from '../../api'
import { useMaasTenantContext } from '../../composables/useMaasTenantContext'
import { exportFile } from '../../utils/exportFile'
import { readExportBlob, ExportTooLargeError } from '../../utils/exportResponse'
import { formatBytes } from '../../utils/format'
import PageBackLink from '../../components/PageBackLink.vue'
import ResponsiveDataView from '../../components/ui/ResponsiveDataView.vue'
import type { CardField } from '../../components/ui/CardList.vue'
import { authBearer } from '../../store'

const { t } = useI18n()
const { pageTitle: ctxPageTitle, maasBackLink } = useMaasTenantContext()
const pageTitle = computed(() => ctxPageTitle(t('tenants.bills.title')))
const backLink = computed(() => maasBackLink('bills'))

const bills = ref<MaasMonthlyBill[]>([])
const redemptions = ref<MaasCouponRedemption[]>([])
const loading = ref(false)
const error = ref('')
const couponCode = ref('')
const couponMsg = ref('')
const activating = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [b, r] = await Promise.all([
      getMaasBills(24),
      getMaasCouponRedemptions(20),
    ])
    bills.value = b.items || []
    redemptions.value = r.items || []
  } catch (e) {
    error.value = surfaceError(e, { key: 'tenants.bills.loadFailed' })
  } finally {
    loading.value = false
  }
}

async function activate() {
  couponMsg.value = ''
  activating.value = true
  try {
    const red = await activateMaasCoupon(couponCode.value.trim())
    couponMsg.value = t('tenants.bills.couponOk', { n: red.credits_granted, code: red.code || couponCode.value })
    couponCode.value = ''
    await load()
  } catch (e) {
    couponMsg.value = surfaceError(e, { key: 'tenants.bills.couponFailed' })
  } finally {
    activating.value = false
  }
}

async function downloadCSV() {
  try {
    const res = await fetch('/api/maas/bills/export', {
      headers: {
        Authorization: authBearer() ? `Bearer ${authBearer()}` : '',
        Accept: 'text/csv',
      },
      credentials: 'include',
    })
    if (!res.ok) throw new Error(`export ${res.status}`)
    // UI规范 19 §3.2 D-5：下载走 exportFile 降级链（分享面→壳桥→blob）；
    // 取流要受控（readExportBlob）—— 超限要给用户看得见的那个数。
    const blob = await readExportBlob(res)
    await exportFile({ filename: 'bills.csv', blob })
  } catch (e) {
    error.value =
      e instanceof ExportTooLargeError
        ? t('common.exportTooLarge', { max: formatBytes(e.limit) })
        : surfaceError(e, { key: 'tenants.bills.exportFailed' })
  }
}

function fmtCents(c: number, currency: string) {
  return `${(c / 100).toFixed(2)} ${currency || 'CNY'}`
}

// compact 档的账单卡字段（与下方桌面表格同一份列政策；2026-10-28 分支审计轮：
// wip/07 合入时只带了桌面 <table> ⇒ compact-card 覆盖率基线红 ⇒ 按 MaaSUsageView
// 的同族范式接 ResponsiveDataView，compact 出卡片、桌面表格逐字保留）。
const billCardFields = computed<CardField[]>(() => [
  { key: 'total_credits', label: t('tenants.bills.colCredits'), type: 'metric', align: 'end', format: (v) => Number(v).toLocaleString() },
  { key: 'total_requests', label: t('tenants.bills.colRequests'), type: 'metric', align: 'end', format: (v) => Number(v).toLocaleString() },
  { key: 'amount_cents', label: t('tenants.bills.colAmount'), type: 'metric', align: 'end', format: (v, row) => fmtCents(Number(v), String(row.currency || 'CNY')) },
  { key: 'status', label: t('tenants.bills.colStatus'), type: 'badge' },
])

onMounted(load)
</script>

<template>
  <div class="page tenant-bills">
    <PageBackLink v-if="backLink" v-bind="backLink" />
    <header class="page-header">
      <h1>{{ pageTitle }}</h1>
      <div class="page-actions">
        <button type="button" class="btn btn-secondary" :disabled="loading" @click="load">
          {{ t('tenants.bills.refresh') }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="loading" @click="downloadCSV">
          {{ t('tenants.bills.exportCsv') }}
        </button>
      </div>
    </header>

    <p v-if="error" class="error-banner">{{ error }}</p>

    <section class="card-block">
      <h2>{{ t('tenants.bills.listTitle') }}</h2>
      <ResponsiveDataView
        :rows="bills"
        title-key="period_ym"
        :fields="billCardFields"
        :loading="loading"
        :empty="!loading && bills.length === 0"
        :empty-text="t('tenants.bills.emptyNoResult')"
        table-min-width="0px"
      >
        <template #table>
          <table class="data-table">
            <thead>
              <tr>
                <th>{{ t('tenants.bills.colPeriod') }}</th>
                <th>{{ t('tenants.bills.colCredits') }}</th>
                <th>{{ t('tenants.bills.colRequests') }}</th>
                <th>{{ t('tenants.bills.colAmount') }}</th>
                <th>{{ t('tenants.bills.colStatus') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="b in bills" :key="b.id">
                <td>{{ b.period_ym }}</td>
                <td>{{ b.total_credits.toLocaleString() }}</td>
                <td>{{ b.total_requests.toLocaleString() }}</td>
                <td>{{ fmtCents(b.amount_cents, b.currency) }}</td>
                <td>{{ b.status }}</td>
              </tr>
            </tbody>
          </table>
        </template>
      </ResponsiveDataView>
    </section>

    <section class="card-block">
      <h2>{{ t('tenants.bills.couponTitle') }}</h2>
      <div class="coupon-row">
        <input
          v-model="couponCode"
          type="text"
          class="input"
          :placeholder="t('tenants.bills.couponPlaceholder')"
          @keyup.enter="activate"
        />
        <button type="button" class="btn btn-primary" :disabled="activating || !couponCode.trim()" @click="activate">
          {{ t('tenants.bills.couponActivate') }}
        </button>
      </div>
      <p v-if="couponMsg" class="hint">{{ couponMsg }}</p>
      <ul v-if="redemptions.length" class="redeem-list">
        <li v-for="r in redemptions" :key="r.id">
          {{ r.code }} · +{{ r.credits_granted.toLocaleString() }} · {{ r.created_at }}
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.page-header { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.page-actions { display: flex; gap: 8px; }
.card-block { margin-top: 24px; }
.coupon-row { display: flex; gap: 8px; max-width: 480px; }
.coupon-row .input { flex: 1; }
.data-table { width: 100%; border-collapse: collapse; }
.data-table th, .data-table td { text-align: left; padding: var(--space-2) var(--space-3); border-bottom: 1px solid var(--border); }
.empty, .hint { color: var(--text-secondary); }
.error-banner { color: var(--danger); }
.redeem-list { margin-top: var(--space-3); padding-left: var(--space-4); color: var(--text-secondary); }
</style>
