<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { RouterLink } from 'vue-router'
import {
  getMaasAccount,
  type MaasAccount,
  MAAS_LEDGER_TYPE_LABELS,
  MAAS_POOL_LABELS,
  MAAS_ORDER_STATUS_LABELS,
} from '../../api'
import { getCurrentTenantId, isPlatformOpsView } from '../../store'

const pageTitle = computed(() =>
  isPlatformOpsView() ? '账户中心' : '我的账户',
)

const account = ref<MaasAccount | null>(null)
const loading = ref(false)
const error = ref('')

const tenantLabel = `租户: ${getCurrentTenantId()}`

function fmtCredits(n: number) {
  return n.toLocaleString('zh-CN')
}

function fmtTime(s: string) {
  if (!s) return '—'
  return new Date(s).toLocaleString('zh-CN', { dateStyle: 'short', timeStyle: 'short' })
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
    account.value = await getMaasAccount()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <h2>{{ pageTitle }}</h2>
      <div class="page-header-actions">
        <span class="tenant-badge">{{ tenantLabel }}</span>
        <RouterLink to="/maas/pricing" class="btn btn-primary btn-sm">购买积分</RouterLink>
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
          {{ loading ? '加载中…' : '刷新' }}
        </button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <div v-if="account" class="wallet-grid">
      <div class="wallet-card card">
        <div class="pool-label">订阅额度</div>
        <div class="pool-value">{{ fmtCredits(account.wallet.quota_remaining) }}</div>
        <div class="pool-hint">月包周期内，到期清零</div>
        <div v-if="account.wallet.subscription" class="sub-info">
          {{ account.wallet.subscription.plan_name }}
          · 至 {{ fmtTime(account.wallet.subscription.period_end) }}
        </div>
      </div>
      <div class="wallet-card card">
        <div class="pool-label">信用积分</div>
        <div class="pool-value">{{ fmtCredits(account.wallet.granted_balance) }}</div>
        <div class="pool-hint">平台赠送 / 授信</div>
      </div>
      <div class="wallet-card card">
        <div class="pool-label">充值积分</div>
        <div class="pool-value">{{ fmtCredits(account.wallet.purchased_balance) }}</div>
        <div class="pool-hint">付费购买</div>
      </div>
      <div class="wallet-card card highlight">
        <div class="pool-label">可用总额</div>
        <div class="pool-value">{{ fmtCredits(account.wallet.total_available) }}</div>
        <div class="pool-hint">扣费顺序：订阅 → 信用 → 充值</div>
      </div>
    </div>

    <div v-if="account" class="section card">
      <div class="section-header">
        <h3>最近订单</h3>
        <RouterLink to="/maas/pricing" class="link-sm">去购买</RouterLink>
      </div>
      <table v-if="account.recent_orders.length" class="table">
        <thead>
          <tr>
            <th>订单号</th>
            <th>类型</th>
            <th>金额</th>
            <th>积分</th>
            <th>状态</th>
            <th>时间</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="o in account.recent_orders" :key="o.id">
            <td class="mono">{{ o.order_no }}</td>
            <td>{{ o.order_type === 'subscribe' ? '订阅' : '加油包' }}</td>
            <td>¥{{ fmtPrice(o.amount_cents) }}</td>
            <td>{{ fmtCredits(o.credits) }}</td>
            <td><span class="badge" :class="orderStatusClass(o.status)">{{ orderStatusLabel(o.status) }}</span></td>
            <td class="mono">{{ fmtTime(o.created_at) }}</td>
            <td>
              <RouterLink v-if="o.status === 'pending'" :to="`/maas/orders/${o.id}`" class="link-sm">去支付</RouterLink>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">暂无订单</div>
    </div>

    <div v-if="account" class="section card">
      <div class="section-header">
        <h3>最近流水</h3>
        <RouterLink to="/maas/usage" class="link-sm">消耗统计</RouterLink>
      </div>
      <table v-if="account.recent_ledger.length" class="table">
        <thead>
          <tr>
            <th>时间</th>
            <th>类型</th>
            <th>池</th>
            <th style="text-align:right">变动</th>
            <th style="text-align:right">余额</th>
            <th>备注</th>
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
      <div v-else class="empty">暂无流水</div>
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
.badge-yellow { background: rgba(234,179,8,.15); color: #fbbf24; padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-green { background: rgba(34,197,94,.15); color: #4ade80; padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-red { background: rgba(239,68,68,.15); color: #f87171; padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-gray { background: rgba(156,163,175,.15); color: #9ca3af; padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.tenant-badge {
  display: inline-flex;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  background: var(--surface-secondary, #f3f4f6);
  color: var(--text-secondary, #6b7280);
}
.alert-danger { padding: 8px 12px; border-radius: 4px; background: rgba(239,68,68,.1); color: #f87171; margin-bottom: 12px; }
</style>
