<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { localeRef } from '../i18n'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import {
  getTenant, getTenantUsers, getTenantKeys, getTenantStats, updateUser,
  getAdminMaasWallet, getAdminMaasLedger, adjustAdminMaasCredits, grantAdminMaasCredits,
  getAdminMaasTenantOrders, confirmAdminMaasOrder,
  getAdminMaasConsumptionDetail,
  MAAS_LEDGER_TYPE_LABELS, MAAS_POOL_LABELS, MAAS_ORDER_STATUS_LABELS,
  TENANT_STATUS_LABELS, TENANT_STATUS_COLORS,
} from '../api'
import type { Tenant, TenantUser, TenantKey, TenantStats, MaasWallet, MaasLedgerEntry, MaasBillingOrder, MaasConsumptionDetail } from '../api'
import TenantEditDialog from './TenantEditDialog.vue'
import FeeCostCell from '../components/FeeCostCell.vue'
import TenantModelPolicyPanel from '../components/TenantModelPolicyPanel.vue'
import { isPlatformOpsView } from '../store'

const route = useRoute()
const router = useRouter()
const tenantCode = computed(() => String(route.params.tenantId))

const tenant = ref<Tenant | null>(null)
const users = ref<TenantUser[]>([])
const keys = ref<TenantKey[]>([])
const stats = ref<TenantStats | null>(null)
const loading = ref(false)
const error = ref('')
const activeTab = ref<'overview' | 'users' | 'keys' | 'stats' | 'billing' | 'wallet' | 'ledger' | 'orders' | 'model-policies'>('overview')
const statsDays = ref(7)
const billingDays = ref(7)
const billingOwnerUser = ref('')
const billing = ref<MaasConsumptionDetail | null>(null)
const billingLoading = ref(false)
const showEdit = ref(false)
const maasWallet = ref<MaasWallet | null>(null)
const maasLedger = ref<MaasLedgerEntry[]>([])
const maasOrders = ref<MaasBillingOrder[]>([])
const adjustAmount = ref('')
const adjustNote = ref('')
const grantAmount = ref('')
const grantNote = ref('')
const adjustSaving = ref(false)
const grantSaving = ref(false)
const confirmSaving = ref<number | null>(null)
const showCost = isPlatformOpsView()

function resetTenantScopedState() {
  users.value = []
  keys.value = []
  stats.value = null
  maasWallet.value = null
  maasLedger.value = []
  maasOrders.value = []
  billing.value = null
}

async function loadTenant() {
  loading.value = true
  error.value = ''
  resetTenantScopedState()
  try {
    tenant.value = await getTenant(tenantCode.value)
    if (activeTab.value === 'users') await loadUsers()
    if (activeTab.value === 'keys') await loadKeys()
    if (activeTab.value === 'stats') await loadStats()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function loadUsers() {
  try {
    users.value = await getTenantUsers(tenantCode.value)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载用户失败'
  }
}

async function loadKeys() {
  try {
    keys.value = await getTenantKeys(tenantCode.value)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载密钥失败'
  }
}

async function loadStats() {
  try {
    stats.value = await getTenantStats(tenantCode.value, statsDays.value)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载统计失败'
  }
}

async function loadWallet() {
  try {
    maasWallet.value = await getAdminMaasWallet(tenantCode.value)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载钱包失败'
  }
}

async function loadLedger() {
  try {
    const res = await getAdminMaasLedger(tenantCode.value, 100)
    maasLedger.value = res.items ?? []
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载账本失败'
  }
}

async function loadOrders() {
  try {
    const res = await getAdminMaasTenantOrders(tenantCode.value, 50)
    maasOrders.value = res.items ?? []
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载订单失败'
  }
}

async function loadBilling() {
  billingLoading.value = true
  try {
    billing.value = await getAdminMaasConsumptionDetail(tenantCode.value, billingDays.value, billingOwnerUser.value)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载计费明细失败'
  } finally {
    billingLoading.value = false
  }
}

async function submitAdjust() {
  const amount = parseInt(adjustAmount.value, 10)
  if (!amount || Number.isNaN(amount)) {
    error.value = '请输入有效的积分数量'
    return
  }
  adjustSaving.value = true
  error.value = ''
  try {
    await adjustAdminMaasCredits(tenantCode.value, amount, adjustNote.value.trim())
    adjustAmount.value = ''
    adjustNote.value = ''
    await Promise.all([loadWallet(), loadLedger()])
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '调整失败'
  } finally {
    adjustSaving.value = false
  }
}

async function submitGrant() {
  const amount = parseInt(grantAmount.value, 10)
  if (!amount || Number.isNaN(amount) || amount <= 0) {
    error.value = '请输入有效的信用积分数量'
    return
  }
  grantSaving.value = true
  error.value = ''
  try {
    await grantAdminMaasCredits(tenantCode.value, amount, grantNote.value.trim())
    grantAmount.value = ''
    grantNote.value = ''
    await Promise.all([loadWallet(), loadLedger()])
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '授予失败'
  } finally {
    grantSaving.value = false
  }
}

async function confirmOrder(orderId: number) {
  confirmSaving.value = orderId
  error.value = ''
  try {
    await confirmAdminMaasOrder(orderId, '管理员手动确认到账')
    await Promise.all([loadOrders(), loadWallet(), loadLedger()])
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '确认失败'
  } finally {
    confirmSaving.value = null
  }
}

function poolLabel(p: string | null | undefined) {
  if (!p) return '—'
  return MAAS_POOL_LABELS[p] || p
}

function orderStatusLabel(s: string) {
  return MAAS_ORDER_STATUS_LABELS[s] || s
}

function fmtPrice(cents: number) {
  return (cents / 100).toFixed(2)
}

function fmtCredits(n: number) {
  const sign = n > 0 ? '+' : ''
  return sign + n.toLocaleString(localeRef.value)
}

function ledgerTypeLabel(t: string) {
  return MAAS_LEDGER_TYPE_LABELS[t] || t
}

async function switchTab(t: 'overview' | 'users' | 'keys' | 'stats' | 'billing' | 'wallet' | 'ledger' | 'orders' | 'model-policies') {
  activeTab.value = t
  if (t === 'users' && users.value.length === 0) await loadUsers()
  if (t === 'keys' && keys.value.length === 0) await loadKeys()
  if (t === 'stats' && !stats.value) await loadStats()
  if (t === 'billing' && !billing.value) await loadBilling()
  if (t === 'wallet' && !maasWallet.value) await loadWallet()
  if (t === 'ledger' && maasLedger.value.length === 0) await loadLedger()
  if (t === 'orders' && maasOrders.value.length === 0) await loadOrders()
}

function onBillingFilterChange() {
  if (activeTab.value === 'billing') void loadBilling()
}

async function toggleUserEnabled(u: TenantUser) {
  try {
    await updateUser(u.id, { enabled: !u.enabled })
    await loadUsers()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '操作失败'
  }
}

function openEdit() {
  showEdit.value = true
}

function statusColor(s: string) {
  return TENANT_STATUS_COLORS[s] || 'badge-gray'
}

function statusLabel(s: string) {
  return TENANT_STATUS_LABELS[s] || s
}

function fmtTime(s: string) {
  if (!s) return '-'
  return new Date(s).toLocaleString(localeRef.value)
}

function fmtNum(n?: number) {
  if (n == null) return '-'
  return n.toLocaleString()
}

function fmtCost(n?: number) {
  if (n == null) return '-'
  return '$' + n.toFixed(2)
}

function fmtRate(value?: number) {
  if (value == null || !Number.isFinite(value)) return '—'
  return `${(value * 100).toFixed(1)}%`
}

function maasLink(path: string) {
  return { path, query: { tenant: tenantCode.value } }
}

onMounted(loadTenant)
watch(() => route.params.tenantId, loadTenant)
</script>

<template>
  <div class="tenant-detail">
    <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>

    <div v-if="loading && !tenant" class="loading">加载中…</div>

    <div v-else-if="tenant">
      <!-- Header -->
      <div class="tenant-header">
        <button class="btn-back" @click="router.push('/tenants')">← 返回租户列表</button>
        <div class="header-main">
          <h1>
            <strong>{{ tenant.name }}</strong>
            <span class="badge" :class="statusColor(tenant.status)">{{ statusLabel(tenant.status) }}</span>
          </h1>
          <div class="header-meta">
            <code class="code-badge">{{ tenant.code }}</code>
            <span v-if="tenant.contact_email">📧 {{ tenant.contact_email }}</span>
            <span>🕐 {{ fmtTime(tenant.created_at) }}</span>
          </div>
          <p v-if="tenant.description" class="description">{{ tenant.description }}</p>
        </div>
        <button class="btn btn-primary" @click="openEdit">编辑</button>
      </div>

      <!-- Tabs -->
      <div class="tabs">
        <button :class="{ active: activeTab === 'overview' }" @click="switchTab('overview')">概览</button>
        <button :class="{ active: activeTab === 'users' }" @click="switchTab('users')">用户 ({{ tenant.user_count }})</button>
        <button :class="{ active: activeTab === 'keys' }" @click="switchTab('keys')">密钥 ({{ tenant.api_key_count }})</button>
        <button :class="{ active: activeTab === 'model-policies' }" @click="switchTab('model-policies')">模型管控</button>
        <button :class="{ active: activeTab === 'stats' }" @click="switchTab('stats')">统计</button>
        <button :class="{ active: activeTab === 'billing' }" @click="switchTab('billing')">计费审计</button>
        <button :class="{ active: activeTab === 'wallet' }" @click="switchTab('wallet')">钱包</button>
        <button :class="{ active: activeTab === 'orders' }" @click="switchTab('orders')">订单</button>
        <button :class="{ active: activeTab === 'ledger' }" @click="switchTab('ledger')">账本</button>
      </div>

      <!-- Overview Tab -->
      <div v-if="activeTab === 'overview'" class="tab-content">
        <div class="stat-cards">
          <div class="stat-card">
            <div class="stat-label">用户数</div>
            <div class="stat-value">{{ tenant.user_count }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">密钥数</div>
            <div class="stat-value">{{ tenant.api_key_count }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">7天请求数</div>
            <div class="stat-value">{{ fmtNum(tenant.requests_7d) }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">7天 Token</div>
            <div class="stat-value">{{ fmtNum(tenant.tokens_7d) }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">7天费用</div>
            <div class="stat-value stat-value--fee">
              <FeeCostCell
                inline
                :credits="tenant.credits_7d"
                :cost-usd="tenant.cost_7d_usd"
                :show-cost="showCost"
              />
            </div>
          </div>
          <div class="stat-card">
            <div class="stat-label">总请求数</div>
            <div class="stat-value">{{ fmtNum(tenant.total_requests) }}</div>
          </div>
        </div>

        <div class="maas-shortcuts">
          <h3>MaaS 服务</h3>
          <p class="maas-shortcuts-desc">以该租户为上下文查看模型定价、账户与消耗（不在平台侧栏展示租户菜单）。</p>
          <div class="maas-shortcut-grid">
            <RouterLink :to="maasLink('/tenant/models')" class="maas-shortcut-card">
              <span class="maas-shortcut-icon">🤖</span>
              <span class="maas-shortcut-label">标准模型</span>
            </RouterLink>
            <RouterLink :to="maasLink('/tenant/pricing')" class="maas-shortcut-card">
              <span class="maas-shortcut-icon">💳</span>
              <span class="maas-shortcut-label">套餐与充值</span>
            </RouterLink>
            <RouterLink :to="maasLink('/tenant/usage')" class="maas-shortcut-card">
              <span class="maas-shortcut-icon">📉</span>
              <span class="maas-shortcut-label">消耗统计</span>
            </RouterLink>
            <RouterLink :to="maasLink('/tenant/account')" class="maas-shortcut-card">
              <span class="maas-shortcut-icon">💰</span>
              <span class="maas-shortcut-label">账户中心</span>
            </RouterLink>
            <button type="button" class="maas-shortcut-card maas-shortcut-card--tab" @click="switchTab('wallet')">
              <span class="maas-shortcut-icon">👛</span>
              <span class="maas-shortcut-label">钱包管理</span>
            </button>
            <button type="button" class="maas-shortcut-card maas-shortcut-card--tab" @click="switchTab('ledger')">
              <span class="maas-shortcut-icon">📒</span>
              <span class="maas-shortcut-label">账本流水</span>
            </button>
          </div>
        </div>
      </div>

      <!-- Users Tab -->
      <div v-if="activeTab === 'users'" class="tab-content">
        <table class="table" style="width:100%">
          <thead>
            <tr><th>ID</th><th>用户名</th><th>显示名</th><th>邮箱</th><th>角色</th><th>状态</th><th>最后登录</th><th>操作</th></tr>
          </thead>
          <tbody>
            <tr v-for="u in users" :key="u.id">
              <td>{{ u.id }}</td>
              <td><strong>{{ u.username }}</strong></td>
              <td>{{ u.display_name || '-' }}</td>
              <td>{{ u.email || '-' }}</td>
              <td><span class="badge" :class="u.role === 'super_admin' ? 'badge-purple' : 'badge-blue'">{{ u.role }}</span></td>
              <td><span class="badge" :class="u.enabled ? 'badge-green' : 'badge-red'">{{ u.enabled ? '启用' : '禁用' }}</span></td>
              <td class="mono">{{ fmtTime(u.last_login_at || '') }}</td>
              <td>
                <button class="btn btn-ghost btn-sm" @click="toggleUserEnabled(u)">
                  {{ u.enabled ? '禁用' : '启用' }}
                </button>
              </td>
            </tr>
            <tr v-if="users.length === 0">
              <td colspan="8" style="text-align:center; padding:40px; color: var(--muted)">无用户</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Keys Tab -->
      <div v-if="activeTab === 'keys'" class="tab-content">
        <table class="table" style="width:100%">
          <thead>
            <tr><th>ID</th><th>密钥前缀</th><th>别名</th><th>应用</th><th>状态</th><th>请求数</th><th>费用</th><th>创建</th></tr>
          </thead>
          <tbody>
            <tr v-for="k in keys" :key="k.id">
              <td>{{ k.id }}</td>
              <td><code>{{ k.key_prefix }}</code></td>
              <td>{{ k.key_alias || '-' }}</td>
              <td><span class="badge badge-blue">{{ k.application_code || '-' }}</span></td>
              <td><span class="badge" :class="k.enabled ? 'badge-green' : 'badge-red'">{{ k.enabled ? '启用' : '禁用' }}</span></td>
              <td>{{ fmtNum(k.total_requests) }}</td>
              <td>
                <span v-if="showCost">{{ fmtCost(k.total_cost_usd) }}</span>
                <span v-else>—</span>
              </td>
              <td class="mono">{{ fmtTime(k.created_at) }}</td>
            </tr>
            <tr v-if="keys.length === 0">
              <td colspan="8" style="text-align:center; padding:40px; color: var(--muted)">无密钥</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Model Policies Tab (Round 48, 2026-06-21) -->
      <div v-if="activeTab === 'model-policies'" class="tab-content">
        <TenantModelPolicyPanel :tenant-code="tenant.code" />
      </div>

      <!-- Stats Tab -->
      <div v-if="activeTab === 'stats'" class="tab-content">
        <div class="stats-toolbar">
          <label>时间窗口:</label>
          <select v-model.number="statsDays" @change="loadStats">
            <option :value="7">近 7 天</option>
            <option :value="30">近 30 天</option>
            <option :value="90">近 90 天</option>
            <option :value="365">近 365 天</option>
          </select>
        </div>

        <div v-if="stats" class="stat-cards">
          <div class="stat-card">
            <div class="stat-label">总请求</div>
            <div class="stat-value">{{ fmtNum(stats.total_requests) }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">总 Token</div>
            <div class="stat-value">{{ fmtNum(stats.total_tokens) }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">总费用</div>
            <div class="stat-value stat-value--fee">
              <FeeCostCell
                inline
                :credits="stats.total_credits"
                :cost-usd="stats.total_cost_usd"
                :show-cost="showCost"
              />
            </div>
          </div>
          <div class="stat-card">
            <div class="stat-label">独立密钥</div>
            <div class="stat-value">{{ stats.unique_keys }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">独立模型</div>
            <div class="stat-value">{{ stats.unique_models }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">独立应用</div>
            <div class="stat-value">{{ stats.unique_apps }}</div>
          </div>
        </div>

        <div v-if="stats" class="stats-tables">
          <h3>按模型分桶 (Top 20)</h3>
          <table class="table">
            <thead><tr><th>模型</th><th>请求</th><th>Token</th><th>费用</th></tr></thead>
            <tbody>
              <tr v-for="m in stats.by_model" :key="m.model">
                <td><code>{{ m.model }}</code></td>
                <td>{{ fmtNum(m.requests) }}</td>
                <td>{{ fmtNum(m.tokens) }}</td>
                <td>
                  <FeeCostCell
                    :credits="m.credits"
                    :cost-usd="m.cost_usd"
                    :show-cost="showCost"
                  />
                </td>
              </tr>
            </tbody>
          </table>

          <h3>按应用分桶 (Top 20)</h3>
          <table class="table">
            <thead><tr><th>应用</th><th>请求</th><th>Token</th><th>费用</th></tr></thead>
            <tbody>
              <tr v-for="a in stats.by_application" :key="a.application_code">
                <td><span class="badge badge-blue">{{ a.application_code }}</span></td>
                <td>{{ fmtNum(a.requests) }}</td>
                <td>{{ fmtNum(a.tokens) }}</td>
                <td>
                  <FeeCostCell
                    :credits="a.credits"
                    :cost-usd="a.cost_usd"
                    :show-cost="showCost"
                  />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Billing Audit Tab -->
      <div v-if="activeTab === 'billing'" class="tab-content">
        <div class="stats-toolbar billing-toolbar">
          <label>时间窗口:</label>
          <select v-model.number="billingDays" @change="onBillingFilterChange">
            <option :value="1">近 1 天</option>
            <option :value="7">近 7 天</option>
            <option :value="30">近 30 天</option>
            <option :value="90">近 90 天</option>
          </select>
          <label for="billing-owner-user">用户:</label>
          <input id="billing-owner-user" v-model="billingOwnerUser" class="billing-user-input" placeholder="按 API Key 所属用户过滤" @keyup.enter="loadBilling" />
          <button class="btn btn-primary btn-sm" :disabled="billingLoading" @click="loadBilling">{{ billingLoading ? '加载中…' : '查询' }}</button>
        </div>

        <div v-if="billing" class="stat-cards">
          <div class="stat-card"><div class="stat-label">计费组合</div><div class="stat-value">{{ billing.rows.length }}</div></div>
          <div class="stat-card"><div class="stat-label">积分消耗</div><div class="stat-value">{{ fmtNum(billing.rows.reduce((sum, row) => sum + row.credits_charged, 0)) }}</div></div>
          <div class="stat-card"><div class="stat-label">上游成本</div><div class="stat-value">{{ fmtCost(billing.rows.reduce((sum, row) => sum + row.upstream_cost_usd, 0)) }}</div></div>
          <div class="stat-card"><div class="stat-label">租户收入</div><div class="stat-value">{{ fmtCost(billing.rows.reduce((sum, row) => sum + row.tenant_revenue_usd, 0)) }}</div></div>
          <div class="stat-card"><div class="stat-label">毛利</div><div class="stat-value" :class="{ 'negative-value': billing.rows.reduce((sum, row) => sum + row.gross_margin_usd, 0) < 0 }">{{ fmtCost(billing.rows.reduce((sum, row) => sum + row.gross_margin_usd, 0)) }}</div></div>
          <div class="stat-card"><div class="stat-label">取消后计费</div><div class="stat-value">{{ fmtNum(billing.rows.reduce((sum, row) => sum + row.cancelled_billed_requests, 0)) }}</div></div>
        </div>

        <div class="billing-table-wrap">
          <table class="table billing-table">
            <thead><tr><th>用户</th><th>供应商</th><th>凭据</th><th>模型</th><th>请求</th><th>输入 / 输出</th><th>缓存读 / 写</th><th>积分</th><th>成本</th><th>收入</th><th>毛利率</th><th>取消计费</th></tr></thead>
            <tbody>
              <tr v-for="row in billing?.rows || []" :key="`${row.provider_id}-${row.credential_id}-${row.canonical_id}-${row.owner_user}`">
                <td>{{ row.owner_user || '未标记' }}</td><td>{{ row.provider_name || '未标记' }}</td><td><code>{{ row.credential_label || row.credential_id || '未标记' }}</code></td><td><code>{{ row.model }}</code></td>
                <td>{{ fmtNum(row.requests) }}</td><td class="mono">{{ fmtNum(row.prompt_tokens) }} / {{ fmtNum(row.completion_tokens) }}</td><td class="mono">{{ fmtNum(row.cache_read_tokens) }} / {{ fmtNum(row.cache_write_tokens) }}</td><td class="mono">{{ fmtNum(row.credits_charged) }}</td>
                <td>{{ fmtCost(row.upstream_cost_usd) }}</td><td>{{ fmtCost(row.tenant_revenue_usd) }}</td><td :class="{ 'negative-value': row.gross_margin_rate < 0 }">{{ fmtRate(row.gross_margin_rate) }}</td>
                <td><span v-if="row.cancelled_billed_requests" class="badge badge-yellow">{{ row.cancelled_billed_requests }}</span><span v-else>—</span></td>
              </tr>
              <tr v-if="!billingLoading && (!billing || billing.rows.length === 0)"><td colspan="12" class="table-empty">暂无已计费明细</td></tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Wallet Tab -->
      <div v-if="activeTab === 'wallet'" class="tab-content">
        <div v-if="maasWallet" class="stat-cards">
          <div class="stat-card">
            <div class="stat-label">订阅额度</div>
            <div class="stat-value">{{ fmtNum(maasWallet.quota_remaining) }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">信用积分</div>
            <div class="stat-value">{{ fmtNum(maasWallet.granted_balance) }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">充值积分</div>
            <div class="stat-value">{{ fmtNum(maasWallet.purchased_balance) }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">可用总额</div>
            <div class="stat-value">{{ fmtNum(maasWallet.total_available) }}</div>
          </div>
        </div>

        <div class="adjust-form">
          <h3>授予信用积分</h3>
          <div class="adjust-row">
            <label>积分数量</label>
            <input v-model="grantAmount" type="number" min="1" placeholder="正整数" />
          </div>
          <div class="adjust-row">
            <label>备注</label>
            <input v-model="grantNote" type="text" placeholder="授予原因（可选）" />
          </div>
          <button class="btn btn-primary btn-sm" :disabled="grantSaving" @click="submitGrant">
            {{ grantSaving ? '提交中…' : '授予信用积分' }}
          </button>
        </div>

        <div class="adjust-form">
          <h3>调整充值积分</h3>
          <div class="adjust-row">
            <label>变动数量</label>
            <input v-model="adjustAmount" type="number" placeholder="正数充值，负数扣减" />
          </div>
          <div class="adjust-row">
            <label>备注</label>
            <input v-model="adjustNote" type="text" placeholder="调整原因（可选）" />
          </div>
          <button class="btn btn-primary btn-sm" :disabled="adjustSaving" @click="submitAdjust">
            {{ adjustSaving ? '提交中…' : '提交调整' }}
          </button>
        </div>
      </div>

      <!-- Orders Tab -->
      <div v-if="activeTab === 'orders'" class="tab-content">
        <table class="table" style="width:100%">
          <thead>
            <tr>
              <th>订单号</th>
              <th>类型</th>
              <th>金额</th>
              <th>积分</th>
              <th>状态</th>
              <th>时间</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="o in maasOrders" :key="o.id">
              <td class="mono">{{ o.order_no }}</td>
              <td>{{ o.order_type === 'subscribe' ? '订阅' : '加油包' }}</td>
              <td>¥{{ fmtPrice(o.amount_cents) }}</td>
              <td>{{ fmtNum(o.credits) }}</td>
              <td>{{ orderStatusLabel(o.status) }}</td>
              <td class="mono">{{ fmtTime(o.created_at) }}</td>
              <td>
                <button
                  v-if="o.status === 'pending'"
                  class="btn btn-ghost btn-sm"
                  :disabled="confirmSaving === o.id"
                  @click="confirmOrder(o.id)"
                >
                  {{ confirmSaving === o.id ? '确认中…' : '确认到账' }}
                </button>
                <span v-else>—</span>
              </td>
            </tr>
            <tr v-if="maasOrders.length === 0">
              <td colspan="7" style="text-align:center; padding:40px; color: var(--muted)">暂无订单</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Ledger Tab -->
      <div v-if="activeTab === 'ledger'" class="tab-content">
        <table class="table" style="width:100%">
          <thead>
            <tr>
              <th>时间</th>
              <th>类型</th>
              <th>池</th>
              <th style="text-align:right">变动</th>
              <th style="text-align:right">余额</th>
              <th>关联</th>
              <th>备注</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="e in maasLedger" :key="e.id">
              <td class="mono">{{ fmtTime(e.created_at) }}</td>
              <td>{{ ledgerTypeLabel(e.entry_type) }}</td>
              <td>{{ poolLabel(e.pool) }}</td>
              <td class="mono" style="text-align:right">{{ fmtCredits(e.amount) }}</td>
              <td class="mono" style="text-align:right">{{ fmtNum(e.balance_after) }}</td>
              <td class="mono">{{ e.ref_type || '—' }} {{ e.ref_id || '' }}</td>
              <td>{{ e.note || '—' }}</td>
            </tr>
            <tr v-if="maasLedger.length === 0">
              <td colspan="7" style="text-align:center; padding:40px; color: var(--muted)">暂无账本记录</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <TenantEditDialog v-if="showEdit && tenant" :tenant="tenant" @close="showEdit = false; loadTenant()" @updated="loadTenant" />
  </div>
</template>

<style scoped>
.tenant-header {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 16px;
  padding: 16px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
}
.btn-back {
  padding: 6px 12px;
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  cursor: pointer;
  font-size: 13px;
}
.btn-back:hover { background: color-mix(in srgb, var(--on-primary) 5%, transparent); }
.header-main { flex: 1; }
.header-main h1 {
  font-size: 22px;
  margin: 0 0 8px;
  display: flex;
  align-items: center;
  gap: 12px;
}
.header-meta {
  display: flex;
  gap: 16px;
  font-size: 12px;
  color: var(--muted);
  align-items: center;
}
.code-badge {
  background: var(--bg);
  padding: 2px 8px;
  border-radius: 4px;
  font-family: 'SF Mono', 'Fira Code', monospace;
}
.description {
  font-size: 13px;
  color: var(--text);
  margin: 8px 0 0;
}

.tabs {
  display: flex;
  border-bottom: 1px solid var(--border);
  margin-bottom: 16px;
}
.tabs button {
  padding: 8px 16px;
  background: transparent;
  border: none;
  color: var(--muted);
  cursor: pointer;
  font-size: 13px;
  border-bottom: 2px solid transparent;
}
.tabs button.active {
  color: var(--accent-h);
  border-bottom-color: var(--accent-h);
}

.tab-content {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;
}

.stat-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
.stat-card {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;
  text-align: center;
}
.stat-label { font-size: 12px; color: var(--muted); margin-bottom: 6px; }
.stat-value { font-size: 22px; font-weight: 600; color: var(--text); }
.stat-value--fee :deep(.fee-main) {
  font-size: inherit;
  font-weight: inherit;
}
.stat-value--fee :deep(.fee-cost-sub) {
  font-size: 12px;
  font-weight: 400;
}

.badge-purple { background: color-mix(in srgb, var(--accent) 15%, transparent); color: var(--accent-h); padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-blue { background: var(--info-bg); color: var(--accent); padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-red { background: var(--danger-bg); color: var(--danger); padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-green { background: var(--success-bg); color: var(--success); padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-yellow { background: var(--warning-bg); color: var(--warning); padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-gray { background: var(--neutral-bg); color: var(--muted); padding: 2px 8px; border-radius: 8px; font-size: 11px; }

.mono { font-family: 'SF Mono', 'Fira Code', monospace; font-size: 12px; }

.stats-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}
.stats-toolbar select {
  padding: 4px 8px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  font-size: 13px;
}
.billing-toolbar { flex-wrap: wrap; }
.billing-user-input {
  min-width: 220px;
  padding: 5px 9px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  font-size: 13px;
}
.billing-table-wrap { overflow-x: auto; }
.billing-table { min-width: 1100px; }
.billing-table th, .billing-table td { white-space: nowrap; }
.table-empty { text-align: center; padding: 40px; color: var(--muted); }
.negative-value { color: var(--danger); }
.stats-tables h3 { font-size: 14px; margin: 16px 0 8px; color: var(--muted); }
.adjust-form {
  margin-top: 20px;
  padding-top: 16px;
  border-top: 1px solid var(--border);
}
.adjust-form h3 { font-size: 14px; margin: 0 0 12px; color: var(--muted); }
.adjust-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 10px;
}
.adjust-row label {
  width: 80px;
  font-size: 13px;
  color: var(--muted);
  flex-shrink: 0;
}
.adjust-row input {
  flex: 1;
  padding: 6px 10px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  font-size: 13px;
}
.loading { text-align: center; padding: 40px; color: var(--muted); }
.alert { padding: 8px 12px; border-radius: 4px; font-size: 13px; }
.alert-danger { background: var(--danger-bg); color: var(--danger); border: 1px solid var(--danger-bd); }

.maas-shortcuts {
  margin-top: 20px;
  padding-top: 16px;
  border-top: 1px solid var(--border);
}
.maas-shortcuts h3 {
  margin: 0 0 6px;
  font-size: 15px;
}
.maas-shortcuts-desc {
  margin: 0 0 12px;
  font-size: 12px;
  color: var(--muted);
}
.maas-shortcut-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 10px;
}
.maas-shortcut-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  padding: 14px 10px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  text-decoration: none;
  color: var(--text);
  cursor: pointer;
  font: inherit;
  transition: border-color .15s, background .15s;
}
.maas-shortcut-card:hover {
  border-color: var(--accent-h);
  background: color-mix(in srgb, var(--accent) 06%, transparent);
}
.maas-shortcut-card--tab {
  background: transparent;
}
.maas-shortcut-icon { font-size: 20px; }
.maas-shortcut-label { font-size: 12px; font-weight: 500; }

@media (max-width: 720px) {
  .billing-toolbar { align-items: stretch; }
  .billing-user-input { min-width: 0; flex: 1 1 100%; }
  .billing-toolbar .btn { width: 100%; }
}
</style>
