<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import {
  getMaasOrder,
  MAAS_ORDER_STATUS_LABELS,
} from '../../api'
import type { MaasBillingOrder } from '../../api'
import { useMaasTenantContext } from '../../composables/useMaasTenantContext'
import PageBackLink from '../../components/PageBackLink.vue'

const route = useRoute()
const { maasBackLink } = useMaasTenantContext()
const orderId = computed(() => Number(route.params.id))
const backLink = computed(() => maasBackLink('order'))

const order = ref<MaasBillingOrder | null>(null)
const loading = ref(false)
const error = ref('')
let pollTimer: ReturnType<typeof setInterval> | null = null

function fmtPrice(cents: number) {
  return (cents / 100).toFixed(2)
}

function fmtCredits(n: number) {
  return n.toLocaleString('zh-CN')
}

function fmtTime(s: string) {
  if (!s) return '—'
  return new Date(s).toLocaleString('zh-CN')
}

function statusLabel(s: string) {
  return MAAS_ORDER_STATUS_LABELS[s] || s
}

function statusClass(s: string) {
  const map: Record<string, string> = {
    pending: 'badge-yellow',
    paid: 'badge-green',
    cancelled: 'badge-gray',
    expired: 'badge-red',
  }
  return map[s] || 'badge-gray'
}

function channelLabel(c: string) {
  return c === 'wechat' ? '微信支付' : c === 'alipay' ? '支付宝' : c
}

const productName = computed(() => {
  if (!order.value) return ''
  if (order.value.order_type === 'subscribe') return order.value.plan_name || '月包订阅'
  return order.value.package_name || '加油包'
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    order.value = await getMaasOrder(orderId.value)
    if (order.value.status === 'paid' && pollTimer) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  load()
  pollTimer = setInterval(load, 15000)
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<template>
  <div>
    <div class="page-header">
      <PageBackLink v-if="backLink" :to="backLink.to" :label="backLink.label" />
      <h2>订单支付</h2>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="loading && !order" class="empty">加载中…</div>

    <div v-if="order" class="order-card card">
      <div class="order-header">
        <div>
          <div class="order-no mono">{{ order.order_no }}</div>
          <div class="order-product">{{ productName }}</div>
        </div>
        <span class="badge" :class="statusClass(order.status)">{{ statusLabel(order.status) }}</span>
      </div>

      <div class="order-meta">
        <div class="meta-row"><span>类型</span><span>{{ order.order_type === 'subscribe' ? '月包订阅' : '加油包' }}</span></div>
        <div class="meta-row"><span>金额</span><span class="amount">¥{{ fmtPrice(order.amount_cents) }}</span></div>
        <div class="meta-row"><span>积分</span><span>{{ fmtCredits(order.credits) }}</span></div>
        <div class="meta-row"><span>支付方式</span><span>{{ channelLabel(order.payment_channel) }}</span></div>
        <div class="meta-row"><span>创建时间</span><span class="mono">{{ fmtTime(order.created_at) }}</span></div>
        <div class="meta-row"><span>过期时间</span><span class="mono">{{ fmtTime(order.expires_at) }}</span></div>
      </div>

      <div v-if="order.status === 'pending'" class="pay-section">
        <div class="qr-box">
          <div class="qr-placeholder">
            <div class="qr-icon">📱</div>
            <div class="qr-title">扫码支付（占位）</div>
            <div class="qr-order mono">{{ order.order_no }}</div>
          </div>
        </div>
        <div v-if="order.payment_hint" class="pay-hint">{{ order.payment_hint }}</div>
        <div v-if="order.stub_mode" class="stub-note">
          支付账号尚未接入，请备注订单号联系客服完成支付。
        </div>
      </div>

      <div v-else-if="order.status === 'paid'" class="paid-note">
        ✅ 支付已确认，积分已到账。
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 16px;
}
.page-header h2 { margin: 0; }
.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 20px;
  max-width: 480px;
}
.order-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 16px;
}
.order-no { font-size: 14px; color: var(--muted); }
.order-product { font-size: 18px; font-weight: 600; margin-top: 4px; }
.order-meta { border-top: 1px solid var(--border); padding-top: 12px; }
.meta-row {
  display: flex;
  justify-content: space-between;
  padding: 6px 0;
  font-size: 13px;
}
.meta-row span:first-child { color: var(--muted); }
.amount { font-size: 18px; font-weight: 700; color: var(--accent-h); }
.pay-section { margin-top: 20px; text-align: center; }
.qr-box {
  display: flex;
  justify-content: center;
  margin-bottom: 12px;
}
.qr-placeholder {
  width: 200px;
  height: 200px;
  border: 2px dashed var(--border);
  border-radius: 8px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  background: var(--bg);
}
.qr-icon { font-size: 48px; }
.qr-title { font-size: 13px; color: var(--muted); }
.qr-order { font-size: 11px; color: var(--text); word-break: break-all; padding: 0 8px; }
.pay-hint { font-size: 13px; color: var(--text); margin-bottom: 8px; }
.stub-note { font-size: 12px; color: var(--muted); }
.paid-note { margin-top: 16px; font-size: 14px; color: #4ade80; }
.mono { font-family: 'SF Mono', 'Fira Code', monospace; }
.badge-yellow { background: rgba(234,179,8,.15); color: #fbbf24; padding: 4px 10px; border-radius: 8px; font-size: 12px; }
.badge-green { background: rgba(34,197,94,.15); color: #4ade80; padding: 4px 10px; border-radius: 8px; font-size: 12px; }
.badge-red { background: rgba(239,68,68,.15); color: #f87171; padding: 4px 10px; border-radius: 8px; font-size: 12px; }
.badge-gray { background: rgba(156,163,175,.15); color: #9ca3af; padding: 4px 10px; border-radius: 8px; font-size: 12px; }
.empty { text-align: center; padding: 40px; color: var(--muted); }
.alert-danger { padding: 8px 12px; border-radius: 4px; background: rgba(239,68,68,.1); color: #f87171; margin-bottom: 12px; }
</style>
