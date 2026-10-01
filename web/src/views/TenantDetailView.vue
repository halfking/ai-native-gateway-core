<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, nextTick, watch } from 'vue'
import { formatDateTime } from '../utils/datetime'
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
import type { Tenant, TenantUser, TenantKey, TenantStats, TenantDailyStat, MaasWallet, MaasLedgerEntry, MaasBillingOrder, MaasConsumptionDetail } from '../api'
import TenantEditDialog from './TenantEditDialog.vue'
import FeeCostCell from '../components/FeeCostCell.vue'
import TenantModelPolicyPanel from '../components/TenantModelPolicyPanel.vue'
import StatCard from '../components/ui/StatCard.vue'
import BarCell from '../components/ui/BarCell.vue'
import SparkBars from '../components/ui/SparkBars.vue'
import { chartColors, createComboChartConfig, useChart } from '../composables/useChart'
import { useRaceGuard } from '../composables/useRaceGuard'
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
// 2026-09-30 统计 UI 优化轮：统计 tab 分布表指标切换（按 Token / 按积分），
// 概览 tab KPI sparkline 数据（近 7 天 daily 序列，独立于 stats tab 的窗口）。
const statsMetric = ref<'token' | 'credits'>('token')
const overviewDaily = ref<TenantDailyStat[]>([])
const keyAppFilter = ref('')
const keyOwnerFilter = ref('')

function fmtTokensCompact(n?: number | null): string {
  const v = n ?? 0
  if (v >= 1e9) return `${(v / 1e9).toFixed(2)}B`
  if (v >= 1e6) return `${(v / 1e6).toFixed(2)}M`
  if (v >= 1e3) return `${(v / 1e3).toFixed(1)}K`
  return String(v)
}
function fmtLatency(ms?: number | null): string {
  const v = ms ?? 0
  if (v <= 0) return '—'
  if (v >= 1000) return `${(v / 1000).toFixed(v >= 10000 ? 1 : 2)}s`
  return `${Math.round(v)}ms`
}
function fmtAxisCompact(v: number): string {
  if (Math.abs(v) >= 1e9) return `${(v / 1e9).toFixed(1)}B`
  if (Math.abs(v) >= 1e6) return `${(v / 1e6).toFixed(1)}M`
  if (Math.abs(v) >= 1e3) return `${(v / 1e3).toFixed(0)}K`
  return String(Math.round(v))
}

async function loadOverviewDaily() {
  try {
    const s = await getTenantStats(tenantCode.value, 7)
    overviewDaily.value = s.daily ?? []
  } catch {
    overviewDaily.value = [] // 概览 sparkline 失败静默降级，不阻塞页面
  }
}

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
    // 概览 KPI sparkline 用近 7 天 daily（轻查询，独立窗口，失败静默）。
    void loadOverviewDaily()
    if (activeTab.value === 'users') await loadUsers()
    if (activeTab.value === 'keys') await loadKeys()
    if (activeTab.value === 'stats') await loadStats()
    applyRouteFocus()
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

// 代际号防竞态（2026-10-01 第十八轮 ④ 收敛至 useRaceGuard，原样板
// useReconciliationPage.ts 的 fetchGen）：快速切换统计窗口（7/30/90 天）
// 时旧响应后到不得覆盖新结果；组件卸载后 in-flight 响应一律作废。
const statsRace = useRaceGuard()
async function loadStats() {
  const gen = statsRace.begin()
  try {
    const next = await getTenantStats(tenantCode.value, statsDays.value)
    if (statsRace.stale(gen)) return
    stats.value = next
  } catch (e: unknown) {
    if (statsRace.stale(gen)) return
    error.value = e instanceof Error ? e.message : '加载统计失败'
  }
}

// ── 统计 tab（2026-09-30 统计 UI 优化轮重做）──────────────────────
const statsDaily = computed<TenantDailyStat[]>(() => stats.value?.daily ?? [])
const statsDayLabels = computed(() => statsDaily.value.map((d) => d.date.slice(5)))
const statsHasDays = computed(() => statsDaily.value.length > 0)
const statsFailTotal = computed(() => statsDaily.value.reduce((a, d) => a + (d.errors ?? 0), 0))
const statsDailyAvg = computed(() => {
  const n = statsDaily.value.length
  return n > 0 ? Math.round((stats.value?.total_requests ?? 0) / n) : null
})
const statsDayDelta = computed(() => {
  const rows = statsDaily.value
  if (rows.length < 2) return null
  const prev = rows[rows.length - 2].requests ?? 0
  const last = rows[rows.length - 1].requests ?? 0
  if (prev <= 0) return null
  return ((last - prev) / prev) * 100
})
const visibleKeys = computed(() => keys.value.filter((k) => {
  if (keyAppFilter.value && k.application_code !== keyAppFilter.value) return false
  if (keyOwnerFilter.value && k.owner_user !== keyOwnerFilter.value) return false
  return true
}))
// 概览 sparkline：近 7 天 daily 序列。
const spark7d = (key: 'requests' | 'tokens' | 'credits') => overviewDaily.value.map((d) => d[key] ?? 0)
// 概览副指标同样取自近 7 天序列（与 stats tab 的窗口解耦）。
const overviewFail7d = computed(() => overviewDaily.value.reduce((a, d) => a + (d.errors ?? 0), 0))
const overviewDailyAvg = computed(() => {
  const n = overviewDaily.value.length
  return n > 0 ? Math.round(overviewDaily.value.reduce((a, d) => a + (d.requests ?? 0), 0) / n) : null
})

const statsReqChartConfig = computed(() =>
  createComboChartConfig('bar', statsDayLabels.value, [
    { label: '成功', data: statsDaily.value.map((d) => d.success), backgroundColor: chartColors.blue + 'cc', borderColor: chartColors.blue, stack: 'req' },
    { label: '失败', data: statsDaily.value.map((d) => d.errors), backgroundColor: chartColors.red + 'cc', borderColor: chartColors.red, stack: 'req' },
  ], { scales: { y: { stacked: true, beginAtZero: true } } }),
)

const statsTokChartConfig = computed(() =>
  createComboChartConfig('line', statsDayLabels.value, [
    { label: 'Token', data: statsDaily.value.map((d) => d.tokens), borderColor: chartColors.blue, backgroundColor: 'rgba(64, 158, 255, 0.16)', fill: true },
    { label: '积分', data: statsDaily.value.map((d) => d.credits), type: 'bar', backgroundColor: chartColors.orange + '99', yAxisID: 'y1' },
  ], {
    scales: {
      y: { beginAtZero: true, ticks: { callback: (v: string | number) => fmtAxisCompact(Number(v)) } },
      y1: { position: 'right', beginAtZero: true, grid: { drawOnChartArea: false }, ticks: { callback: (v: string | number) => fmtAxisCompact(Number(v)) } },
    },
  }),
)

const statsReqCanvas = ref<HTMLCanvasElement | null>(null)
const statsTokCanvas = ref<HTMLCanvasElement | null>(null)
const { initChart: initStatsReqChart, destroyChart: destroyStatsReqChart, isDisposed: statsReqDisposed } = useChart(statsReqCanvas, statsReqChartConfig)
const { initChart: initStatsTokChart, destroyChart: destroyStatsTokChart, isDisposed: statsTokDisposed } = useChart(statsTokCanvas, statsTokChartConfig)

let statsChartsAlive = true
async function refreshStatsCharts() {
  if (!statsChartsAlive) return
  if (!statsHasDays.value) {
    destroyStatsReqChart()
    destroyStatsTokChart()
    return
  }
  await nextTick()
  if (!statsChartsAlive) return
  if (!statsReqDisposed()) initStatsReqChart()
  if (!statsTokDisposed()) initStatsTokChart()
}
watch([statsReqChartConfig, statsTokChartConfig], () => void refreshStatsCharts(), { deep: true })
onBeforeUnmount(() => {
  statsChartsAlive = false
})

// 分布表指标切换：按 Token / 按积分（排序与占比条基准）。
function statsMetricValue(row: { tokens: number; credits: number }): number {
  return statsMetric.value === 'token' ? row.tokens : row.credits
}
const statsModelSorted = computed(() =>
  [...(stats.value?.by_model ?? [])].sort((a, b) => statsMetricValue(b) - statsMetricValue(a)),
)
const statsModelMax = computed(() => statsModelSorted.value.reduce((m, r) => Math.max(m, statsMetricValue(r)), 0))
const statsAppSorted = computed(() =>
  [...(stats.value?.by_application ?? [])].sort((a, b) => statsMetricValue(b) - statsMetricValue(a)),
)
const statsAppMax = computed(() => statsAppSorted.value.reduce((m, r) => Math.max(m, statsMetricValue(r)), 0))
const statsModelTotalReq = computed(() => (stats.value?.by_model ?? []).reduce((a, r) => a + r.requests, 0))

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

const tenantTabs = ['overview', 'users', 'keys', 'stats', 'billing', 'wallet', 'ledger', 'orders', 'model-policies'] as const
type TenantTab = typeof tenantTabs[number]

function applyRouteFocus() {
  const tab = route.query.tab
  if (typeof tab === 'string' && (tenantTabs as readonly string[]).includes(tab)) {
    void switchTab(tab as TenantTab)
  }
  if (typeof route.query.app === 'string') keyAppFilter.value = route.query.app
  if (typeof route.query.owner === 'string') keyOwnerFilter.value = route.query.owner
}

function formatDayDelta(v: number | null): string {
  if (v == null) return ''
  return `${v > 0 ? '+' : ''}${v.toFixed(1)}%`
}

function openAppKeys(code: string) {
  keyAppFilter.value = code
  keyOwnerFilter.value = ''
  void switchTab('keys')
}

async function switchTab(t: TenantTab) {
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
  return formatDateTime(s, { locale: localeRef.value })
}

function fmtNum(n?: number | null) {
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

      <!-- Overview Tab（2026-09-30 统计 UI 优化轮：KPI 升级为 主值+副指标+sparkline） -->
      <div v-if="activeTab === 'overview'" class="tab-content">
        <div class="stat-cards stat-cards--kpi">
          <StatCard label="用户数" icon="👥">
            <template #value>{{ tenant.user_count }}</template>
            <template #sub>密钥 {{ tenant.api_key_count }} 把</template>
          </StatCard>
          <StatCard label="密钥数" icon="🔑">
            <template #value>{{ tenant.api_key_count }}</template>
            <template #sub>独立模型 {{ stats?.unique_models ?? '—' }}</template>
          </StatCard>
          <StatCard label="7天请求" icon="📥">
            <template #value>{{ fmtNum(tenant.requests_7d) }}</template>
            <template #sub>失败 {{ fmtNum(overviewFail7d) }}（{{ (tenant.requests_7d ?? 0) > 0 ? ((overviewFail7d / (tenant.requests_7d ?? 0)) * 100).toFixed(1) : '0.0' }}%）</template>
            <template #spark><SparkBars :data="spark7d('requests')" color-token="--accent" /></template>
          </StatCard>
          <StatCard label="7天 Token" icon="🔢">
            <template #value>{{ fmtTokensCompact(tenant.tokens_7d) }}</template>
            <template #sub>日均请求 {{ fmtNum(overviewDailyAvg) }}</template>
            <template #spark><SparkBars :data="spark7d('tokens')" color-token="--probe-cyan" /></template>
          </StatCard>
          <StatCard label="7天费用" icon="🪙" tone="success">
            <template #value>
              <span class="stat-value stat-value--fee">
                <FeeCostCell
                  inline
                  :credits="tenant.credits_7d"
                  :cost-usd="tenant.cost_7d_usd"
                  :show-cost="showCost"
                />
              </span>
            </template>
            <template #sub>积分 {{ fmtNum(tenant.credits_7d) }}</template>
            <template #spark><SparkBars :data="spark7d('credits')" color-token="--success" /></template>
          </StatCard>
          <StatCard label="总请求数（历史）" icon="📚">
            <template #value>{{ fmtNum(tenant.total_requests) }}</template>
            <template #sub>自 {{ fmtTime(tenant.created_at) }} 起</template>
          </StatCard>
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
        <div v-if="keyAppFilter || keyOwnerFilter" class="stats-asof">
          <span v-if="keyAppFilter">应用 {{ keyAppFilter }}</span>
          <span v-if="keyOwnerFilter">账号 {{ keyOwnerFilter }}</span>
          <button type="button" class="btn btn-ghost btn-sm" @click="keyAppFilter = ''; keyOwnerFilter = ''">清除过滤</button>
        </div>
        <table class="table" style="width:100%">
          <thead>
            <tr><th>ID</th><th>密钥前缀</th><th>别名</th><th>应用</th><th>状态</th><th>请求数</th><th>费用</th><th>创建</th></tr>
          </thead>
          <tbody>
            <tr v-for="k in visibleKeys" :key="k.id">
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
            <tr v-if="visibleKeys.length === 0">
              <td colspan="8" style="text-align:center; padding:40px; color: var(--muted)">无密钥</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Model Policies Tab (Round 48, 2026-06-21) -->
      <div v-if="activeTab === 'model-policies'" class="tab-content">
        <TenantModelPolicyPanel :tenant-code="tenant.code" />
      </div>

      <!-- Stats Tab（2026-09-30 统计 UI 优化轮重做：KPI 8 卡 + 双趋势图 + 占比条分布） -->
      <div v-if="activeTab === 'stats'" class="tab-content">
        <div class="stats-toolbar">
          <label>时间窗口:</label>
          <div class="window-chips">
            <button
              v-for="d in [7, 30, 90, 365]"
              :key="d"
              class="chip"
              :class="{ active: statsDays === d }"
              type="button"
              @click="statsDays = d; loadStats()"
            >近 {{ d }} 天</button>
          </div>
          <span class="text-muted stats-asof">数据截至 {{ fmtTime(new Date().toISOString()) }}</span>
        </div>

        <div v-if="stats" class="stat-cards stat-cards--kpi">
          <StatCard label="总请求" icon="📥">
            <template #value>{{ fmtNum(stats.total_requests) }}</template>
            <template #sub>失败 <span style="color:var(--danger)">{{ fmtNum(statsFailTotal) }}</span>（{{ stats.total_requests > 0 ? ((statsFailTotal / stats.total_requests) * 100).toFixed(1) : '0.0' }}%）</template>
          </StatCard>
          <StatCard label="总 Token" icon="🔢">
            <template #value>{{ fmtTokensCompact(stats.total_tokens) }}</template>
            <template #sub>入 {{ fmtTokensCompact(stats.input_tokens) }} · 出 {{ fmtTokensCompact(stats.output_tokens) }} · 缓存读 {{ fmtTokensCompact(stats.cache_read_tokens) }} · 缓存写 {{ fmtTokensCompact(stats.cache_write_tokens) }}</template>
          </StatCard>
          <StatCard label="总费用" icon="🪙" tone="success">
            <template #value>
              <span class="stat-value stat-value--fee">
                <FeeCostCell inline :credits="stats.total_credits" :cost-usd="stats.total_cost_usd" :show-cost="showCost" />
              </span>
            </template>
            <template #sub>积分 {{ fmtNum(stats.total_credits) }}<template v-if="showCost"> · 成本 {{ fmtCost(stats.total_cost_usd) }}</template></template>
          </StatCard>
          <StatCard label="日均请求" icon="📈">
            <template #value>{{ fmtNum(statsDailyAvg) }}</template>
            <template #sub>
              <template v-if="statsDayDelta != null">较前一日 {{ formatDayDelta(statsDayDelta) }}</template>
              <template v-else>窗口 {{ stats.days }} 天</template>
            </template>
          </StatCard>
          <StatCard label="独立密钥" icon="🔑">
            <template #value>{{ stats.unique_keys }}</template>
            <template #sub>按密钥用量见密钥 tab</template>
          </StatCard>
          <StatCard label="独立模型" icon="🤖">
            <template #value>{{ stats.unique_models }}</template>
            <template #sub>Top 见下方模型分布</template>
          </StatCard>
          <StatCard label="独立应用" icon="🧩">
            <template #value>{{ stats.unique_apps }}</template>
            <template #sub>Top 见下方应用分布</template>
          </StatCard>
          <StatCard label="平均耗时" icon="⏱️">
            <template #value>{{ fmtLatency(stats.avg_latency_ms) }}</template>
            <template #sub>窗口内请求均值</template>
          </StatCard>
        </div>

        <div v-if="stats && statsHasDays" class="stats-trend-grid">
          <div class="stats-trend-card">
            <div class="stats-trend-title">请求与失败趋势</div>
            <div class="stats-trend-body"><canvas ref="statsReqCanvas" /></div>
          </div>
          <div class="stats-trend-card">
            <div class="stats-trend-title">Token 与积分趋势</div>
            <div class="stats-trend-body"><canvas ref="statsTokCanvas" /></div>
          </div>
        </div>

        <div v-if="stats" class="stats-tables">
          <div class="stats-tables-header">
            <h3>模型分布 · Top 20</h3>
            <span class="spacer"></span>
            <div class="window-chips">
              <button class="chip" :class="{ active: statsMetric === 'token' }" type="button" @click="statsMetric = 'token'">按 Token</button>
              <button class="chip" :class="{ active: statsMetric === 'credits' }" type="button" @click="statsMetric = 'credits'">按积分</button>
            </div>
          </div>
          <table class="table">
            <thead><tr><th>模型</th><th class="text-right">请求</th><th class="text-right">Token</th><th class="text-right">积分</th><th class="text-right">费用</th></tr></thead>
            <tbody>
              <tr v-for="m in statsModelSorted" :key="m.model">
                <td>
                  <BarCell :pct="statsModelMax > 0 ? (statsMetricValue(m) / statsModelMax) * 100 : 0">
                    <template #name>
                      <code>{{ m.model }}</code>
                      <span v-if="statsModelTotalReq > 0" class="cell-sub">{{ ((m.requests / statsModelTotalReq) * 100).toFixed(1) }}%</span>
                    </template>
                  </BarCell>
                </td>
                <td class="text-right">{{ fmtNum(m.requests) }}</td>
                <td class="text-right">{{ fmtTokensCompact(m.tokens) }}</td>
                <td class="text-right">{{ fmtNum(m.credits) }}</td>
                <td>
                  <FeeCostCell :credits="m.credits" :cost-usd="m.cost_usd" :show-cost="showCost" />
                </td>
              </tr>
              <tr v-if="statsModelSorted.length === 0"><td colspan="5" class="table-empty">窗口内无请求</td></tr>
            </tbody>
          </table>

          <div class="stats-tables-header">
            <h3>应用分布 · Top 20</h3>
            <span class="spacer"></span>
            <span class="text-muted" style="font-size:12px">点击行跳转密钥列表（带应用过滤）</span>
          </div>
          <table class="table">
            <thead><tr><th>应用</th><th class="text-right">请求</th><th class="text-right">Token</th><th class="text-right">积分</th><th class="text-right">费用</th></tr></thead>
            <tbody>
              <tr v-for="a in statsAppSorted" :key="a.application_code" class="stats-app-row" @click="openAppKeys(a.application_code)">
                <td>
                  <BarCell :pct="statsAppMax > 0 ? (statsMetricValue(a) / statsAppMax) * 100 : 0" tone="cyan">
                    <template #name><span class="badge badge-blue">{{ a.application_code || '—' }}</span></template>
                  </BarCell>
                </td>
                <td class="text-right">{{ fmtNum(a.requests) }}</td>
                <td class="text-right">{{ fmtTokensCompact(a.tokens) }}</td>
                <td class="text-right">{{ fmtNum(a.credits) }}</td>
                <td>
                  <FeeCostCell :credits="a.credits" :cost-usd="a.cost_usd" :show-cost="showCost" />
                </td>
              </tr>
              <tr v-if="statsAppSorted.length === 0"><td colspan="5" class="table-empty">窗口内无请求</td></tr>
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

@media (max-width: 768px) {
  .billing-toolbar { align-items: stretch; }
  .billing-user-input { min-width: 0; flex: 1 1 100%; }
  .billing-toolbar .btn { width: 100%; }
}

/* ── 2026-09-30 统计 UI 优化轮：KPI 网格 / 时间窗 chips / 趋势图卡 ── */
.stat-cards--kpi {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 12px;
}
.window-chips {
  display: inline-flex;
  gap: 6px;
}
.chip {
  border: 1px solid var(--border);
  background: transparent;
  color: var(--muted);
  border-radius: 999px;
  padding: 3px 12px;
  font-size: 12.5px;
  cursor: pointer;
}
.chip:hover {
  color: var(--accent);
  border-color: var(--accent);
}
.chip.active {
  background: var(--bg-subtle);
  border-color: var(--accent);
  color: var(--accent);
  font-weight: 600;
}
.stats-asof {
  font-size: 12px;
  margin-left: auto;
}
.stats-app-row { cursor: pointer; }
.stats-trend-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 14px;
  margin: 14px 0 4px;
}
@media (max-width: 1080px) {
  .stats-trend-grid { grid-template-columns: 1fr; }
}
.stats-trend-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
}
.stats-trend-title {
  font-weight: 600;
  font-size: 14px;
  margin-bottom: 8px;
}
.stats-trend-body {
  height: 240px;
  position: relative;
}
.stats-trend-body canvas {
  width: 100% !important;
  height: 100% !important;
}
.stats-tables-header {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 18px 0 8px;
  flex-wrap: wrap;
}
.stats-tables-header .spacer { flex: 1; }
.cell-sub {
  font-size: 11.5px;
  color: var(--muted);
}
</style>
