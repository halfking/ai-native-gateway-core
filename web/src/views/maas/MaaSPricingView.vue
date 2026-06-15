<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  getMaasPlans,
  getMaasTopupPackages,
  getMaasWallet,
  getMaasSettings,
} from '../../api'
import type { MaasPlan, MaasTopupPackage, MaasWallet } from '../../api'
import { isSuperAdmin, isDefaultTenant, getCurrentTenantId } from '../../store'

const plans = ref<MaasPlan[]>([])
const topups = ref<MaasTopupPackage[]>([])
const wallet = ref<MaasWallet | null>(null)
const currencyDisplay = ref('CNY')
const loading = ref(false)
const error = ref('')

const tenantLabel = computed(() => {
  const tenantId = getCurrentTenantId()
  if (isSuperAdmin() && isDefaultTenant()) return '整站数据'
  if (isDefaultTenant()) return '默认租户'
  return `租户: ${tenantId}`
})

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
      getMaasWallet(),
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

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <h2>MaaS 套餐与加油包</h2>
      <div class="page-header-actions">
        <span
          class="tenant-badge"
          :class="{ 'tenant-badge--admin': isSuperAdmin(), 'tenant-badge--default': isDefaultTenant() }"
        >
          {{ tenantLabel }}
        </span>
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
          {{ loading ? '加载中…' : '刷新' }}
        </button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <div v-if="wallet" class="wallet-card card">
      <div class="wallet-stat">
        <div class="wallet-label">钱包余额</div>
        <div class="wallet-value">{{ fmtCredits(wallet.balance_credits) }} <span class="unit">积分</span></div>
      </div>
      <div class="wallet-stat">
        <div class="wallet-label">月包剩余额度</div>
        <div class="wallet-value">{{ fmtCredits(wallet.quota_remaining) }} <span class="unit">积分</span></div>
      </div>
      <div class="wallet-stat wallet-stat--highlight">
        <div class="wallet-label">可用总额</div>
        <div class="wallet-value">{{ fmtCredits(wallet.total_available) }} <span class="unit">积分</span></div>
      </div>
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
          class="btn btn-primary btn-block"
          disabled
          title="联系管理员开通"
        >
          购买
        </button>
        <div class="buy-hint">联系管理员开通</div>
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
          class="btn btn-primary btn-block"
          disabled
          title="联系管理员开通"
        >
          购买
        </button>
        <div class="buy-hint">联系管理员开通</div>
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
.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
}
.wallet-card {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
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
  font-size: 24px;
  font-weight: 700;
  color: var(--text);
}
.wallet-value .unit {
  font-size: 13px;
  font-weight: 500;
  color: var(--muted);
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
.pricing-price {
  margin: 4px 0;
}
.price-num {
  font-size: 28px;
  font-weight: 700;
  color: var(--accent-h);
}
.price-period {
  font-size: 13px;
  color: var(--muted);
}
.pricing-credits {
  font-size: 13px;
  color: var(--muted);
  margin-bottom: 8px;
}
.btn-block {
  width: 100%;
}
.buy-hint {
  font-size: 11px;
  color: var(--muted);
}
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
</style>
