<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import {
  getMaasPlans,
  getMaasTopupPackages,
  getMaasWallet,
  getAdminMaasWallet,
  getMaasSettings,
  createMaasOrder,
} from '../../api'
import type { MaasPlan, MaasTopupPackage, MaasWallet } from '../../api'
import { useMaasTenantContext } from '../../composables/useMaasTenantContext'
import PageBackLink from '../../components/PageBackLink.vue'

const { tenantLabel, tenantCode, isAdminTenantView, pageTitle: ctxPageTitle, maasBackLink, tenantQuerySuffix } = useMaasTenantContext()
const pageTitle = computed(() => ctxPageTitle('套餐与充值'))
const backLink = computed(() => maasBackLink('pricing'))

const router = useRouter()
const plans = ref<MaasPlan[]>([])
const topups = ref<MaasTopupPackage[]>([])
const wallet = ref<MaasWallet | null>(null)
const currencyDisplay = ref('CNY')
const loading = ref(false)
const buying = ref<number | null>(null)
const error = ref('')
const payChannel = ref<'alipay' | 'wechat'>('alipay')

function fmtPrice(cents: number) {
  return (cents / 100).toFixed(2)
}

function fmtCredits(n: number) {
  return n.toLocaleString('zh-CN')
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [planRes, topupRes, walletRes, settingsRes] = await Promise.all([
      getMaasPlans(),
      getMaasTopupPackages(),
      isAdminTenantView.value
        ? getAdminMaasWallet(tenantCode.value)
        : getMaasWallet(),
      getMaasSettings(),
    ])
    plans.value = planRes.items ?? []
    topups.value = topupRes.items ?? []
    wallet.value = walletRes
    currencyDisplay.value = settingsRes.currency_display || 'CNY'
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function buyPlan(plan: MaasPlan) {
  buying.value = plan.id
  error.value = ''
  try {
    const order = await createMaasOrder({
      type: 'subscribe',
      plan_id: plan.id,
      payment_channel: payChannel.value,
    })
    router.push({
      path: `/tenant/orders/${order.id}`,
      query: tenantQuerySuffix(),
    })
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '创建订单失败'
  } finally {
    buying.value = null
  }
}

async function buyTopup(pkg: MaasTopupPackage) {
  buying.value = pkg.id + 10000
  error.value = ''
  try {
    const order = await createMaasOrder({
      type: 'topup',
      package_id: pkg.id,
      payment_channel: payChannel.value,
    })
    router.push({
      path: `/tenant/orders/${order.id}`,
      query: tenantQuerySuffix(),
    })
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '创建订单失败'
  } finally {
    buying.value = null
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <PageBackLink v-if="backLink" :to="backLink.to" :label="backLink.label" />
      <h2>{{ pageTitle }}</h2>
      <div class="page-header-actions">
        <span class="tenant-badge tenant-badge--admin">
          {{ tenantLabel }}
        </span>
        <select v-if="!isAdminTenantView" v-model="payChannel" class="channel-select">
          <option value="alipay">支付宝</option>
          <option value="wechat">微信支付</option>
        </select>
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
          {{ loading ? '加载中…' : '刷新' }}
        </button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <div v-if="wallet" class="wallet-card card">
      <div class="wallet-stat">
        <div class="wallet-label">订阅额度</div>
        <div class="wallet-value">{{ fmtCredits(wallet.quota_remaining) }}</div>
      </div>
      <div class="wallet-stat">
        <div class="wallet-label">信用积分</div>
        <div class="wallet-value">{{ fmtCredits(wallet.granted_balance) }}</div>
      </div>
      <div class="wallet-stat">
        <div class="wallet-label">充值积分</div>
        <div class="wallet-value">{{ fmtCredits(wallet.purchased_balance) }}</div>
      </div>
      <div class="wallet-stat wallet-stat--highlight">
        <div class="wallet-label">可用总额</div>
        <div class="wallet-value">{{ fmtCredits(wallet.total_available) }}</div>
      </div>
    </div>

    <div v-if="isAdminTenantView" class="alert alert-info">
      管理员只读视图：代客下单请在租户详情页「钱包 / 订单」Tab 操作。
    </div>

    <h3 class="section-title">月包套餐</h3>
    <div class="pricing-grid">
      <div v-for="p in plans" :key="p.id" class="pricing-card card">
        <div class="pricing-tier">{{ p.tier }}</div>
        <h4>{{ p.name }}</h4>
        <div class="pricing-price">
          <span class="price-num">¥{{ fmtPrice(p.price_cents) }}</span>
          <span class="price-period">/ 月</span>
        </div>
        <div class="pricing-credits">{{ fmtCredits(p.monthly_credits) }} 积分 / 月</div>
        <button
          v-if="!isAdminTenantView"
          class="btn btn-primary btn-block"
          :disabled="!!buying"
          @click="buyPlan(p)"
        >
          {{ buying === p.id ? '创建订单…' : '立即购买' }}
        </button>
      </div>
      <div v-if="!loading && plans.length === 0" class="empty-card">暂无可用月包</div>
    </div>

    <h3 class="section-title">加油包</h3>
    <div class="pricing-grid">
      <div v-for="t in topups" :key="t.id" class="pricing-card card">
        <div class="pricing-tier">{{ t.tier }}</div>
        <h4>{{ t.name }}</h4>
        <div class="pricing-price">
          <span class="price-num">¥{{ fmtPrice(t.price_cents) }}</span>
        </div>
        <div class="pricing-credits">{{ fmtCredits(t.credits_amount) }} 积分</div>
        <button
          v-if="!isAdminTenantView"
          class="btn btn-primary btn-block"
          :disabled="!!buying"
          @click="buyTopup(t)"
        >
          {{ buying === t.id + 10000 ? '创建订单…' : '立即购买' }}
        </button>
      </div>
      <div v-if="!loading && topups.length === 0" class="empty-card">暂无可用加油包</div>
    </div>
  </div>
</template>

<style scoped>
.page-header-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}
.channel-select {
  padding: 4px 8px;
  font-size: 12px;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg);
  color: var(--text);
}
.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
}
.wallet-card {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 16px;
  padding: 20px;
  margin-bottom: 24px;
}
.wallet-label {
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 6px;
}
.wallet-value {
  font-size: 22px;
  font-weight: 700;
  color: var(--text);
}
.wallet-stat--highlight .wallet-value {
  color: var(--accent-h);
}
.section-title {
  font-size: 15px;
  font-weight: 600;
  margin: 0 0 12px;
  color: var(--text);
}
.pricing-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 16px;
  margin-bottom: 28px;
}
.pricing-card {
  padding: 20px;
  text-align: center;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.pricing-tier {
  font-size: 11px;
  text-transform: uppercase;
  color: var(--muted);
  letter-spacing: 0.05em;
}
.pricing-card h4 {
  margin: 0;
  font-size: 16px;
}
.pricing-price { margin: 4px 0; }
.price-num {
  font-size: 28px;
  font-weight: 700;
  color: var(--accent-h);
}
.price-period { font-size: 13px; color: var(--muted); }
.pricing-credits {
  font-size: 13px;
  color: var(--muted);
  margin-bottom: 8px;
}
.btn-block { width: 100%; }
.empty-card {
  grid-column: 1 / -1;
  text-align: center;
  padding: 32px;
  color: var(--muted);
  background: var(--card);
  border: 1px dashed var(--border);
  border-radius: 8px;
}
.tenant-badge {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  background: var(--surface-secondary, #f3f4f6);
  color: var(--text-secondary, #6b7280);
}
.tenant-badge--admin {
  background: rgba(59, 130, 246, 0.1);
  color: #3b82f6;
}
.tenant-badge--default {
  background: rgba(34, 197, 94, 0.1);
  color: #22c55e;
}
.alert-danger {
  padding: 8px 12px;
  border-radius: 4px;
  background: rgba(239,68,68,.1);
  color: #f87171;
  margin-bottom: 12px;
}
.alert-info {
  padding: 8px 12px;
  border-radius: 4px;
  background: rgba(59,130,246,.1);
  color: #60a5fa;
  margin-bottom: 12px;
  font-size: 13px;
}
</style>
