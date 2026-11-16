<script setup lang="ts">
// UserDetailDrawer.vue — 用户详情抽屉（2026-09-30 统计 UI 优化轮）
//
// /users 列表点击行打开：KPI（请求/Token/积分/失败率·P95）+ 请求与 Token
// 趋势 + Top 模型/应用 + 最近请求（首字/总耗时）+ 基本信息。
// 数据面 GET /api/admin/users/{id}/stats（usage_facts × api_keys.owner_user，
// 与对账快照同源）；账号无用量时 KPI 全零 + 空态文案，基本信息仍完整展示。
// 启停/重置密码操作不在抽屉内自行执行，emit 回列表页复用既有 handler。
//
// 2026-11-17：新增 API Keys 标签页，展示该用户名下密钥 + 创建密钥入口。
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { ElMessageBox, ElMessage } from 'element-plus'
import { surfaceError } from '../i18n/errorSurface'
import { useI18n } from 'vue-i18n'
import { fmtDateTime24h } from '../i18n/useFormat'
import { getUserStats, type UserStats, getKeys, createKey, revokeKey, disableKey, enableKey, type ApiKey } from '../api'
import { isSuperAdmin } from '../store'
import StatCard from './ui/StatCard.vue'
import AppDrawer from './ui/AppDrawer.vue'
import { chartColors, createComboChartConfig, useChart } from '../composables/useChart'
import { useRaceGuard } from '../composables/useRaceGuard'

export interface DrawerUser {
  id: number
  username: string
  display_name: string
  email: string
  tenant_id: string
  role: string
  enabled: boolean
  must_change_password?: boolean
  last_login_at: string | null
  created_at: string
}

const props = defineProps<{
  modelValue: boolean
  user: DrawerUser | null
}>()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  'reset-password': [user: DrawerUser]
  'toggle-enabled': [user: DrawerUser]
}>()

const { t } = useI18n()

const activeTab = ref<'stats' | 'keys'>('stats')
const days = ref(30)
const stats = ref<UserStats | null>(null)
const loading = ref(false)
const errorText = ref('')

// ── API Keys 标签页 ─────────────────────────────────────────
const keys = ref<ApiKey[]>([])
const keysLoading = ref(false)
const keysError = ref('')
const showCreateKey = ref(false)
const newKeyForm = ref({
  application_code: 'default',
  key_alias: '',
  budget_usd: null as number | null,
  rate_limit_rpm: null as number | null,
  remark: '',
})
const createKeySaving = ref(false)
const createdKeyResponse = ref<{ api_key: string; key_prefix: string } | null>(null)

// 代际号防竞态（2026-10-01 第十八轮 ④ 收敛至 useRaceGuard，原内联实现
// 见 useReconciliationPage.ts 样板）：快速切换 user/days 时旧响应后到不得
// 覆盖新结果；组件卸载后 in-flight 响应一律作废。
const race = useRaceGuard()
async function load() {
  if (!props.user) return
  const gen = race.begin()
  loading.value = true
  errorText.value = ''
  stats.value = null
  try {
    const next = await getUserStats(props.user.id, days.value)
    if (race.stale(gen)) return
    stats.value = next
  } catch (e: unknown) {
    if (race.stale(gen)) return
    errorText.value = surfaceError(e, { key: 'users.detail.loadStatsFailed' })
  } finally {
    if (race.current(gen)) loading.value = false
  }
}

async function loadKeys() {
  if (!props.user) return
  keysLoading.value = true
  keysError.value = ''
  try {
    const allKeys = await getKeys()
    // 只保留该用户名下的密钥
    keys.value = allKeys.filter(k => k.owner_user === props.user!.username && k.tenant_id === props.user!.tenant_id)
  } catch (e: unknown) {
    keysError.value = surfaceError(e, { key: 'users.detail.loadKeysFailed' })
  } finally {
    keysLoading.value = false
  }
}

watch(
  () => [props.modelValue, props.user?.id, days.value, activeTab.value],
  ([open]) => {
    if (open) {
      if (activeTab.value === 'stats') void load()
      if (activeTab.value === 'keys') void loadKeys()
    }
  },
)

function switchTab(tab: 'stats' | 'keys') {
  activeTab.value = tab
  if (tab === 'stats' && !stats.value) void load()
  if (tab === 'keys' && keys.value.length === 0) void loadKeys()
}

async function handleCreateKey() {
  if (!props.user) return
  createKeySaving.value = true
  try {
    const response = await createKey({
      application_code: newKeyForm.value.application_code,
      tenant_id: props.user.tenant_id,
      key_alias: newKeyForm.value.key_alias || undefined,
      owner_user: props.user.username,
      budget_usd: newKeyForm.value.budget_usd ?? undefined,
      rate_limit_rpm: newKeyForm.value.rate_limit_rpm ?? undefined,
      remark: newKeyForm.value.remark || undefined,
    })
    createdKeyResponse.value = { api_key: response.api_key, key_prefix: response.key_prefix }
    ElMessage.success(t('users.detail.keyCreated', '密钥创建成功'))
    // 刷新密钥列表
    await loadKeys()
  } catch (e: unknown) {
    ElMessage.error(surfaceError(e, { key: 'users.detail.createKeyFailed' }))
  } finally {
    createKeySaving.value = false
  }
}

function resetCreateKeyForm() {
  showCreateKey.value = false
  createdKeyResponse.value = null
  newKeyForm.value = {
    application_code: 'default',
    key_alias: '',
    budget_usd: null,
    rate_limit_rpm: null,
    remark: '',
  }
}

async function handleRevokeKey(key: ApiKey) {
  try {
    await ElMessageBox.confirm(
      t('users.detail.revokeKeyConfirm', `确定要吊销密钥 ${key.key_prefix}**** 吗？此操作不可恢复。`),
      t('common.terms.confirm', '确认'),
      { type: 'warning' },
    )
    await revokeKey(key.id)
    ElMessage.success(t('users.detail.keyRevoked', '密钥已吊销'))
    await loadKeys()
  } catch (e: unknown) {
    if (e !== 'cancel') {
      ElMessage.error(surfaceError(e, { key: 'users.detail.revokeKeyFailed' }))
    }
  }
}

async function handleToggleKeyEnabled(key: ApiKey) {
  try {
    if (key.enabled) {
      await disableKey(key.id)
      ElMessage.success(t('users.detail.keyDisabled', '密钥已禁用'))
    } else {
      await enableKey(key.id)
      ElMessage.success(t('users.detail.keyEnabled', '密钥已启用'))
    }
    await loadKeys()
  } catch (e: unknown) {
    ElMessage.error(surfaceError(e, { key: 'users.detail.toggleKeyFailed' }))
  }
}

function onToggleEnabled() {
  if (props.user) emit('toggle-enabled', props.user)
}
function onResetPassword() {
  if (props.user) emit('reset-password', props.user)
}

// ── 趋势图：请求（面积，左轴）+ Token（线，右轴） ──────────────
const dayLabels = computed(() => (stats.value?.daily ?? []).map((d) => d.date.slice(5)))
const hasDaily = computed(() => (stats.value?.daily ?? []).length > 0)
const trendConfig = computed(() =>
  createComboChartConfig('line', dayLabels.value, [
    {
      label: t('users.detail.requests', '请求'),
      data: (stats.value?.daily ?? []).map((d) => d.requests),
      borderColor: chartColors.blue,
      backgroundColor: 'rgba(64, 158, 255, 0.16)',
      fill: true,
    },
    {
      label: 'Token',
      data: (stats.value?.daily ?? []).map((d) => d.tokens),
      borderColor: chartColors.cyan,
      backgroundColor: 'transparent',
      yAxisID: 'y1',
    },
  ], {
    scales: {
      y: { beginAtZero: true },
      y1: { position: 'right', beginAtZero: true, grid: { drawOnChartArea: false } },
    },
  }),
)
const canvasRef = ref<HTMLCanvasElement | null>(null)
const { initChart, destroyChart, isDisposed } = useChart(canvasRef, trendConfig)
let alive = true
async function refreshChart() {
  if (!alive) return
  if (!hasDaily.value) {
    destroyChart()
    return
  }
  await nextTick()
  if (!alive || isDisposed()) return
  initChart()
}
watch(trendConfig, () => void refreshChart(), { deep: true })
onBeforeUnmount(() => {
  alive = false
})

// ── 格式化 ──────────────────────────────────────────────
function fmtNum(n: number | null | undefined): string {
  return (n ?? 0).toLocaleString('en-US')
}
function fmtTokensCompact(n: number | null | undefined): string {
  const v = n ?? 0
  if (v >= 1e9) return `${(v / 1e9).toFixed(2)}B`
  if (v >= 1e6) return `${(v / 1e6).toFixed(2)}M`
  if (v >= 1e3) return `${(v / 1e3).toFixed(1)}K`
  return String(v)
}
function fmtMs(ms: number | null | undefined): string {
  if (ms == null) return '—'
  return ms >= 10000 ? `${(ms / 1000).toFixed(0)}s` : `${(ms / 1000).toFixed(1)}s`
}
function statusBadge(status: string): string {
  return status === 'success' ? 'badge-green' : status === 'rate_limited' ? 'badge-yellow' : 'badge-red'
}
const topKeysText = computed(() =>
  (stats.value?.top_keys ?? []).map((k) => `${k.name}×${k.requests}`).join(' · ') || '—',
)
function keyHref(keyId: number): string {
  if (!props.user) return '/tenants'
  const q = new URLSearchParams({ tab: 'keys', key_id: String(keyId) })
  return `/tenants/${encodeURIComponent(props.user.tenant_id)}?${q.toString()}`
}
const logsHref = computed(() => {
  if (!props.user) return '/request-logs'
  const q = new URLSearchParams({ owner_user: props.user.username, preset: 'd7' })
  return `/request-logs?${q.toString()}`
})
const keysHref = computed(() => {
  if (!props.user) return '/tenants'
  const q = new URLSearchParams({ tab: 'keys', owner: props.user.username })
  return `/tenants/${encodeURIComponent(props.user.tenant_id)}?${q.toString()}`
})
// /tenants/:tenantId 挂 requiresSuper 路由门（router.ts），非 super（如
// tenant_admin）点「API 密钥」下钻会被守卫拦到 /forbidden，故仅 super
// 展示链接，其余只保留数字展示；logsHref（/request-logs）无门不受影响。
const canDrillKeys = computed(() => isSuperAdmin())
</script>

<template>
  <AppDrawer
    :model-value="modelValue"
    :title="user ? user.username : ''"
    width="min(46vw, 620px)"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div v-if="user" class="udd">
      <!-- 头部：身份徽章 + 操作 -->
      <div class="udd-header">
        <div class="udd-identity">
          <span class="badge" :class="user.role === 'super_admin' ? 'badge-purple' : 'badge-blue'">{{ user.role }}</span>
          <span class="badge" :class="user.enabled ? 'badge-green' : 'badge-red'">{{ user.enabled ? t('users.status.enabled', $t('common.status.enabled')) : t('users.status.disabled', $t('common.status.disabled')) }}</span>
          <span v-if="user.must_change_password" class="badge badge-yellow">{{ t('users.mustChangePassword.pending', '待改密') }}</span>
          <code class="mono udd-tenant">{{ user.tenant_id }}</code>
        </div>
        <div class="udd-actions">
          <button class="btn btn-sm" @click="onToggleEnabled">{{ user.enabled ? t('users.action.disable', $t('common.status.disabled')) : t('users.action.enable', $t('common.status.enabled')) }}</button>
          <button class="btn btn-sm" @click="onResetPassword">{{ t('users.action.resetPassword', '重置密码') }}</button>
        </div>
      </div>

      <!-- 标签页导航 -->
      <div class="udd-tabs">
        <button
          class="udd-tab"
          :class="{ active: activeTab === 'stats' }"
          @click="switchTab('stats')"
        >{{ t('users.detail.statsTab', '统计') }}</button>
        <button
          class="udd-tab"
          :class="{ active: activeTab === 'keys' }"
          @click="switchTab('keys')"
        >{{ t('users.detail.keysTab', 'API 密钥') }} <span v-if="keys.length > 0" class="udd-tab-badge">{{ keys.length }}</span></button>
      </div>

      <!-- 统计标签页 -->
      <div v-if="activeTab === 'stats'" class="udd-tab-content">
        <!-- 时间窗 -->
        <div class="udd-toolbar">
          <div class="udd-chips">
            <button
              v-for="d in [7, 30, 90]"
              :key="d"
              class="udd-chip"
              :class="{ active: days === d }"
              type="button"
              @click="days = d"
            >{{ t('users.detail.days', '近') }} {{ d }} {{ t('users.detail.daysUnit', $t('common.days')) }}</button>
          </div>
        </div>

        <div v-if="loading" class="udd-empty">{{ t('users.detail.loading', $t('common.feedback.loading')) }}</div>
        <div v-else-if="errorText" class="udd-empty udd-error">{{ errorText }}</div>

        <template v-else-if="stats">
        <!-- KPI -->
        <div class="udd-kpis">
          <StatCard :label="t('users.detail.kpiRequests', $t('common.terms.requestCount'))" compact>
            <template #value>{{ fmtNum(stats.kpi.requests) }}</template>
            <template #sub>{{ t('users.detail.dailyAvg', '日均') }} {{ stats.days > 0 ? fmtNum(Math.round(stats.kpi.requests / stats.days)) : '-' }}</template>
          </StatCard>
          <StatCard label="Token" compact>
            <template #value>{{ fmtTokensCompact(stats.kpi.tokens) }}</template>
            <template #sub>{{ t('users.detail.topModel', '主力') }} {{ stats.top_models[0]?.name ?? '—' }}</template>
          </StatCard>
          <StatCard :label="t('users.detail.kpiCredits', '积分消耗')" compact tone="success">
            <template #value>{{ fmtNum(stats.kpi.credits) }}</template>
          </StatCard>
          <StatCard :label="t('users.detail.kpiErrP95', '失败率 / P95')" compact tone="danger">
            <template #value>{{ (stats.kpi.error_rate * 100).toFixed(1) }}%<span class="udd-unit">/ {{ fmtMs(stats.kpi.latency_p95_ms) }}</span></template>
            <template #sub>{{ t('users.detail.failed', $t('common.status.failed')) }} {{ fmtNum(stats.kpi.errors) }}</template>
          </StatCard>
        </div>

        <div v-if="stats.kpi.requests === 0" class="udd-empty">
          {{ t('users.detail.noUsage', '该账号名下密钥在本窗口内无请求') }}
        </div>

        <template v-else>
          <!-- 趋势 -->
          <div class="udd-card">
            <div class="udd-card-title">{{ t('users.detail.trend', '请求与 Token 趋势') }}</div>
            <div class="udd-chart"><canvas ref="canvasRef" /></div>
          </div>

          <!-- Top 模型 / 应用 -->
          <div class="udd-grid2">
            <div class="udd-card">
              <div class="udd-card-title">{{ t('users.detail.topModels', 'Top 模型') }}</div>
              <table class="udd-table">
                <thead><tr><th>{{ t('users.detail.model', $t('common.terms.model')) }}</th><th class="num">{{ t('users.detail.req', $t('common.terms.request')) }}</th><th class="num">Token</th><th class="num">{{ t('users.detail.credits', $t('common.unit.credits')) }}</th></tr></thead>
                <tbody>
                  <tr v-for="m in stats.top_models" :key="m.name">
                    <td class="mono">{{ m.name }}</td>
                    <td class="num">{{ fmtNum(m.requests) }}</td>
                    <td class="num">{{ fmtTokensCompact(m.tokens) }}</td>
                    <td class="num">{{ fmtNum(m.credits) }}</td>
                  </tr>
                  <tr v-if="!stats.top_models.length"><td colspan="4" class="udd-table-empty">—</td></tr>
                </tbody>
              </table>
            </div>
            <div class="udd-card">
              <div class="udd-card-title">{{ t('users.detail.topApps', 'Top 应用') }}</div>
              <table class="udd-table">
                <thead><tr><th>{{ t('users.detail.app', $t('common.terms.application')) }}</th><th class="num">{{ t('users.detail.req', $t('common.terms.request')) }}</th><th class="num">Token</th><th class="num">{{ t('users.detail.credits', $t('common.unit.credits')) }}</th></tr></thead>
                <tbody>
                  <tr v-for="a in stats.top_apps" :key="a.name">
                    <td><span class="badge badge-blue">{{ a.name }}</span></td>
                    <td class="num">{{ fmtNum(a.requests) }}</td>
                    <td class="num">{{ fmtTokensCompact(a.tokens) }}</td>
                    <td class="num">{{ fmtNum(a.credits) }}</td>
                  </tr>
                  <tr v-if="!stats.top_apps.length"><td colspan="4" class="udd-table-empty">—</td></tr>
                </tbody>
              </table>
            </div>
          </div>

          <!-- 最近请求 -->
          <div class="udd-card">
            <div class="udd-card-title">{{ t('users.detail.recent', '最近请求') }}</div>
            <table class="udd-table">
              <thead><tr><th>{{ t('users.detail.time', $t('common.time')) }}</th><th>{{ t('users.detail.model', $t('common.terms.model')) }}</th><th class="num">{{ t('users.detail.firstTotal', '首字 / 总耗时') }}</th><th class="num">{{ t('users.detail.credits', $t('common.unit.credits')) }}</th><th>{{ t('users.detail.status', $t('common.terms.status')) }}</th></tr></thead>
              <tbody>
                <tr v-for="(rq, i) in stats.recent_requests" :key="i">
                  <td class="mono udd-time">{{ fmtDateTime24h(rq.ts) }}</td>
                  <td class="mono">{{ rq.model }}</td>
                  <td class="num">{{ fmtMs(rq.first_chunk_ms) }} / {{ fmtMs(rq.total_ms) }}</td>
                  <td class="num">{{ fmtNum(rq.credits) }}</td>
                  <td><span class="badge" :class="statusBadge(rq.status)">{{ rq.status }}</span></td>
                </tr>
                <tr v-if="!stats.recent_requests.length"><td colspan="5" class="udd-table-empty">—</td></tr>
              </tbody>
            </table>
          </div>
        </template>

        <!-- 基本信息 -->
        <div class="udd-card">
          <div class="udd-card-title">{{ t('users.detail.basic', '基本信息') }}</div>
          <div class="udd-kv">
            <span class="k">{{ t('users.table.displayName', $t('common.terms.displayName')) }}</span><span>{{ user.display_name || '—' }}</span>
            <span class="k">{{ t('users.table.email', $t('common.terms.email')) }}</span><span class="mono">{{ user.email || '—' }}</span>
            <span class="k">{{ t('users.table.tenant', $t('common.terms.tenant')) }}</span><span class="mono">{{ user.tenant_id }}</span>
            <span class="k">{{ t('users.table.createdAt', $t('common.createdAt')) }}</span><span class="mono">{{ fmtDateTime24h(user.created_at) }}</span>
            <span class="k">{{ t('users.table.lastLogin', '最后登录') }}</span><span class="mono">{{ user.last_login_at ? fmtDateTime24h(user.last_login_at) : '—' }}</span>
            <span class="k">{{ t('users.detail.hotKeys', '高频密钥') }}</span>
            <span v-if="!stats.top_keys.length">—</span>
            <span v-else-if="canDrillKeys" class="udd-key-links">
              <template v-for="(k, i) in stats.top_keys" :key="k.id ?? i">
                <span v-if="i > 0" class="udd-key-sep"> · </span>
                <a v-if="k.id" class="udd-link" :href="keyHref(k.id)">{{ k.name }}×{{ k.requests }}</a>
                <span v-else class="mono">{{ k.name }}×{{ k.requests }}</span>
              </template>
            </span>
            <span v-else class="mono">{{ topKeysText }}</span>
            <span class="k">{{ t('users.detail.keyCount', 'API 密钥') }}</span>
            <span v-if="canDrillKeys"><a class="udd-link" :href="keysHref">{{ stats.key_count ?? 0 }}</a></span>
            <span v-else>{{ stats.key_count ?? 0 }}</span>
          </div>
          <div class="udd-links">
            <a class="udd-link" :href="logsHref">{{ t('users.detail.viewAllLogs', '在日志中查看全部') }}</a>
          </div>
        </div>
      </template>
      </div>

      <!-- API Keys 标签页 -->
      <div v-if="activeTab === 'keys'" class="udd-tab-content">
        <div class="udd-keys-toolbar">
          <button class="btn btn-primary btn-sm" @click="showCreateKey = true">
            <span style="margin-right: 4px;">+</span> {{ t('users.detail.createKey', '签发密钥') }}
          </button>
        </div>

        <div v-if="keysLoading" class="udd-empty">{{ t('common.feedback.loading', '加载中...') }}</div>
        <div v-else-if="keysError" class="udd-empty udd-error">{{ keysError }}</div>
        <div v-else-if="keys.length === 0" class="udd-empty">{{ t('users.detail.noKeys', '该用户暂无 API 密钥') }}</div>

        <div v-else class="udd-keys-list">
          <div v-for="key in keys" :key="key.id" class="udd-key-card">
            <div class="udd-key-header">
              <div class="udd-key-prefix">
                <code>{{ key.key_prefix }}****</code>
                <span v-if="key.key_alias" class="udd-key-alias">{{ key.key_alias }}</span>
              </div>
              <div class="udd-key-actions">
                <button
                  class="btn btn-ghost btn-xs"
                  @click="handleToggleKeyEnabled(key)"
                >{{ key.enabled ? t('common.status.disabled', '禁用') : t('common.status.enabled', '启用') }}</button>
                <button
                  class="btn btn-ghost btn-xs btn-danger"
                  @click="handleRevokeKey(key)"
                >{{ t('users.detail.revokeKey', '吊销') }}</button>
              </div>
            </div>
            <div class="udd-key-info">
              <div class="udd-key-row">
                <span class="udd-key-label">{{ t('common.terms.application', '应用') }}</span>
                <span class="badge badge-blue">{{ key.application_code }}</span>
              </div>
              <div class="udd-key-row">
                <span class="udd-key-label">{{ t('common.terms.status', '状态') }}</span>
                <span class="badge" :class="key.enabled ? 'badge-green' : 'badge-red'">
                  {{ key.enabled ? t('common.status.enabled', '启用') : t('common.status.disabled', '禁用') }}
                </span>
              </div>
              <div class="udd-key-row">
                <span class="udd-key-label">{{ t('users.detail.requests', '请求数') }}</span>
                <span>{{ fmtNum(key.total_requests) }}</span>
              </div>
              <div class="udd-key-row">
                <span class="udd-key-label">Token</span>
                <span>{{ fmtTokensCompact(key.total_prompt_tokens + key.total_completion_tokens) }}</span>
              </div>
              <div v-if="key.rate_limit_rpm" class="udd-key-row">
                <span class="udd-key-label">{{ t('users.detail.rateLimit', '速率限制') }}</span>
                <span>{{ key.rate_limit_rpm }} RPM</span>
              </div>
              <div v-if="key.remark" class="udd-key-row">
                <span class="udd-key-label">{{ t('common.terms.remark', '备注') }}</span>
                <span class="udd-key-remark">{{ key.remark }}</span>
              </div>
            </div>
          </div>
        </div>

        <!-- 创建密钥对话框 -->
        <teleport to="body">
          <div v-if="showCreateKey" class="modal-overlay" @click.self="resetCreateKeyForm">
            <div class="modal-dialog">
              <div class="modal-header">
                <h3>{{ t('users.detail.createKeyTitle', '为用户签发密钥') }}</h3>
                <button class="modal-close" @click="resetCreateKeyForm">×</button>
              </div>
              <div class="modal-body">
                <div v-if="createdKeyResponse" class="udd-key-created">
                  <div class="udd-key-created-icon">✓</div>
                  <p class="udd-key-created-title">{{ t('users.detail.keyCreatedSuccess', '密钥创建成功') }}</p>
                  <div class="udd-key-created-secret">
                    <code>{{ createdKeyResponse.api_key }}</code>
                  </div>
                  <p class="udd-key-created-warn">{{ t('users.detail.keyCreatedWarn', '请妥善保管，关闭后将无法再次查看完整密钥') }}</p>
                  <button class="btn btn-primary" @click="resetCreateKeyForm">{{ t('common.terms.close', '关闭') }}</button>
                </div>

                <form v-else @submit.prevent="handleCreateKey">
                  <div class="form-group">
                    <label>{{ t('common.terms.application', '应用') }} *</label>
                    <input
                      v-model="newKeyForm.application_code"
                      type="text"
                      class="form-input"
                      required
                      placeholder="default"
                    />
                  </div>
                  <div class="form-group">
                    <label>{{ t('users.detail.keyAlias', '密钥别名') }}</label>
                    <input
                      v-model="newKeyForm.key_alias"
                      type="text"
                      class="form-input"
                      placeholder="可选，方便识别"
                    />
                  </div>
                  <div class="form-group">
                    <label>{{ t('users.detail.budgetUsd', '预算 (USD)') }}</label>
                    <input
                      v-model.number="newKeyForm.budget_usd"
                      type="number"
                      class="form-input"
                      min="0"
                      step="0.01"
                      placeholder="留空表示无限制"
                    />
                  </div>
                  <div class="form-group">
                    <label>{{ t('users.detail.rateLimitRpm', '速率限制 (RPM)') }}</label>
                    <input
                      v-model.number="newKeyForm.rate_limit_rpm"
                      type="number"
                      class="form-input"
                      min="0"
                      placeholder="留空表示无限制"
                    />
                  </div>
                  <div class="form-group">
                    <label>{{ t('common.terms.remark', '备注') }}</label>
                    <textarea
                      v-model="newKeyForm.remark"
                      class="form-input"
                      rows="3"
                      placeholder="可选"
                    ></textarea>
                  </div>
                  <div class="modal-footer">
                    <button type="button" class="btn btn-ghost" @click="resetCreateKeyForm">{{ t('common.terms.cancel', '取消') }}</button>
                    <button type="submit" class="btn btn-primary" :disabled="createKeySaving">
                      {{ createKeySaving ? t('common.feedback.submitting', '提交中...') : t('common.terms.create', '创建') }}
                    </button>
                  </div>
                </form>
              </div>
            </div>
          </div>
        </teleport>
      </div>
    </div>
  </AppDrawer>
</template>

<style scoped>
.udd-header {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.udd-identity { display: flex; align-items: center; gap: 4px; flex-wrap: wrap; flex: 1; }
.udd-tenant { font-size: 11px; color: var(--muted); }
.udd-actions { display: flex; gap: 4px; }

/* 标签页导航 */
.udd-tabs {
  display: flex;
  gap: 4px;
  margin-bottom: 12px;
  border-bottom: 1px solid var(--border);
}
.udd-tab {
  padding: 8px 16px;
  background: transparent;
  border: none;
  border-bottom: 2px solid transparent;
  color: var(--muted);
  font-size: 13px;
  cursor: pointer;
  transition: all 0.2s;
  position: relative;
}
.udd-tab:hover {
  color: var(--accent);
}
.udd-tab.active {
  color: var(--accent);
  border-bottom-color: var(--accent);
  font-weight: 600;
}
.udd-tab-badge {
  display: inline-block;
  background: var(--accent);
  color: white;
  font-size: 10px;
  padding: 1px 5px;
  border-radius: 10px;
  margin-left: 4px;
  font-weight: 600;
}
.udd-tab-content {
  animation: fadeIn 0.2s;
}
@keyframes fadeIn {
  from { opacity: 0; }
  to { opacity: 1; }
}

.udd-toolbar { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; }
.udd-chips { display: inline-flex; gap: 4px; }
.udd-chip {
  border: 1px solid var(--border);
  background: transparent;
  color: var(--muted);
  border-radius: 999px;
  padding: 2px 12px;
  font-size: 12px;
  cursor: pointer;
}
.udd-chip:hover { color: var(--accent); border-color: var(--accent); }
.udd-chip.active {
  background: var(--bg-subtle);
  border-color: var(--accent);
  color: var(--accent);
  font-weight: 600;
}

/* API Keys 标签页 */
.udd-keys-toolbar {
  display: flex;
  justify-content: flex-end;
  margin-bottom: 12px;
}
.udd-keys-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.udd-key-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
}
.udd-key-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--border);
}
.udd-key-prefix {
  display: flex;
  align-items: center;
  gap: 8px;
}
.udd-key-prefix code {
  font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
  font-size: 13px;
  font-weight: 600;
}
.udd-key-alias {
  color: var(--muted);
  font-size: 12px;
}
.udd-key-actions {
  display: flex;
  gap: 4px;
}
.udd-key-info {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 8px;
  font-size: 12px;
}
.udd-key-row {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.udd-key-label {
  color: var(--muted);
  font-size: 11px;
}
.udd-key-remark {
  color: var(--muted);
  font-style: italic;
}

/* 创建密钥成功提示 */
.udd-key-created {
  text-align: center;
  padding: 20px;
}
.udd-key-created-icon {
  font-size: 48px;
  color: var(--success);
  margin-bottom: 16px;
}
.udd-key-created-title {
  font-size: 16px;
  font-weight: 600;
  margin-bottom: 16px;
}
.udd-key-created-secret {
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 12px;
  margin-bottom: 16px;
  word-break: break-all;
}
.udd-key-created-secret code {
  font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
  font-size: 13px;
  color: var(--accent);
}
.udd-key-created-warn {
  color: var(--warning);
  font-size: 12px;
  margin-bottom: 16px;
}

/* 模态框 */
.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 2000;
}
.modal-dialog {
  background: var(--card);
  border-radius: 8px;
  max-width: 500px;
  width: 90%;
  max-height: 90vh;
  overflow: auto;
}
.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border);
}
.modal-header h3 {
  margin: 0;
  font-size: 16px;
}
.modal-close {
  background: transparent;
  border: none;
  font-size: 24px;
  color: var(--muted);
  cursor: pointer;
  padding: 0;
  width: 24px;
  height: 24px;
  display: flex;
  align-items: center;
  justify-content: center;
}
.modal-close:hover {
  color: var(--text);
}
.modal-body {
  padding: 20px;
}
.form-group {
  margin-bottom: 16px;
}
.form-group label {
  display: block;
  margin-bottom: 4px;
  font-size: 13px;
  font-weight: 500;
}
.form-input {
  width: 100%;
  padding: 8px 12px;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg);
  color: var(--text);
  font-size: 13px;
}
.form-input:focus {
  outline: none;
  border-color: var(--accent);
}
.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}

.udd-kpis {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 8px;
  margin-bottom: 12px;
}
.udd-unit { font-size: 12px; font-weight: 400; color: var(--muted); margin-left: 4px; }
.udd-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
  margin-bottom: 12px;
}
.udd-card-title { font-weight: 600; font-size: 13px; margin-bottom: 8px; }
.udd-chart { height: 170px; position: relative; }
.udd-chart canvas { width: 100% !important; height: 100% !important; }
.udd-grid2 {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
@media (max-width: 768px) { .udd-grid2 { grid-template-columns: 1fr; } }
.udd-table { width: 100%; border-collapse: collapse; font-size: 12px; }
.udd-table th {
  text-align: left;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.01em;
  color: var(--muted);
  padding: 4px 4px;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
.udd-table td { padding: 4px; border-bottom: 1px solid var(--border); white-space: nowrap; }
.udd-table tbody tr:last-child td { border-bottom: none; }
.udd-table .num { text-align: right; font-variant-numeric: tabular-nums; }
.udd-table-empty { text-align: center; color: var(--muted); padding: 12px 0; }
.udd-time { font-size: 11px; }
.udd-kv {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 4px 12px;
  font-size: 12px;
}
.udd-kv .k { color: var(--muted); }
.udd-key-links { display: inline; }
.udd-key-sep { color: var(--muted); }
.udd-links { margin-top: 8px; }
.udd-link { color: var(--accent); font-size: 12px; }
.udd-empty {
  color: var(--muted);
  text-align: center;
  padding: 24px 8px;
  font-size: 13px;
}
.udd-error { color: var(--danger); }
.mono {
  font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
  font-variant-numeric: tabular-nums;
}
</style>
