<script setup lang="ts">
// UserDetailDrawer.vue — 用户详情抽屉（2026-09-30 统计 UI 优化轮）
//
// /users 列表点击行打开：KPI（请求/Token/积分/失败率·P95）+ 请求与 Token
// 趋势 + Top 模型/应用 + 最近请求（首字/总耗时）+ 基本信息。
// 数据面 GET /api/admin/users/v2/{id}/stats（request_metrics 投影，
// 2026-11 统计 V2 轮起，v1 usage_facts 口径已退役）；账号无用量时 KPI 全零 +
// 空态文案，基本信息仍完整展示。
// 启停/重置密码操作不在抽屉内自行执行，emit 回列表页复用既有 handler。
//
// 2026-11-17 审计修复轮：新增「API 密钥」标签页（签发 / 启停 / 吊销 / 查完整密钥）。
// 本轮修掉的缺陷（详见 docs/audit/2026-11-17-用户详情API密钥管理-审计轮.md）：
//   F1 i18n 键缺失 22 个 + CJK 硬编码 +42（全部落到 8 语种 locale 文件）
//   F2 v-model.number 空输入得到 '' → budget_usd 传空串 → 后端 400 → 签发必失败
//   F3 ElMessageBox 的 'close'（ESC/X）拒绝被当成错误 → 取消却弹红色失败提示
//   F4 明文密钥残留在组件状态里 → 关抽屉不点「关闭」→ 下一个用户看到上一个的密钥
//   F5 状态徽章只反映 enabled，掩盖 status='revoked'（且对 revoked 点「启用」是静默失败）
//   F6 缺「查看存量密钥完整信息」（后端 /api/keys/{id}/reveal 早已存在却没接）
//   F7 getKeys() 无参 → super_admin 把全站密钥拉到浏览器再前端筛（越权下发 + 全表下载）
//   F8 密钥操作未做角色门禁
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { ElDialog, ElMessage, ElMessageBox } from 'element-plus'
import { surfaceError } from '../i18n/errorSurface'
import { useI18n } from 'vue-i18n'
import { fmtDateTime24h } from '../i18n/useFormat'
import {
  getUserStats, type UserStats,
  getKeys, createKey, revokeKey, disableKey, enableKey, revealKey,
  type ApiKey,
} from '../api'
import { isSuperAdmin, isTenantAdmin } from '../store'
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
const createKeyForm = ref({
  application_code: 'default',
  key_alias: '',
  budget_usd: '' as string,
  rate_limit_rpm: '' as string,
  remark: '',
})
const createKeySaving = ref(false)
// 存量密钥的完整明文（reveal 结果 / 签发结果）
const revealedSecret = ref('')
const revealedSecretPrefix = ref('')
const secretDialogOpen = ref(false)

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

// F7：owner + tenant 都交给服务端过滤。超管不再把全站密钥拉回浏览器。
async function loadKeys() {
  if (!props.user) return
  const target = props.user
  keysLoading.value = true
  keysError.value = ''
  try {
    const list = await getKeys({ tenant: target.tenant_id, owner: target.username })
    // 用户在请求期间被切换 ⇒ 丢弃这次响应（防止张冠李戴）
    if (props.user?.id !== target.id) return
    keys.value = list
  } catch (e: unknown) {
    if (props.user?.id !== target.id) return
    keysError.value = surfaceError(e, { key: 'users.detail.loadKeysFailed' })
  } finally {
    if (props.user?.id === target.id) keysLoading.value = false
  }
}

// F4：任何「可能含明文」的状态在关闭抽屉 / 切换用户时必须清零。
// 明文密钥跨用户残留 = 跨账号泄露，不是什么 UX 小瑕疵。
function resetSecretState() {
  revealedSecret.value = ''
  revealedSecretPrefix.value = ''
  secretDialogOpen.value = false
  showCreateKey.value = false
}

watch(
  () => [props.modelValue, props.user?.id, days.value, activeTab.value],
  ([open]) => {
    if (!open) {
      resetSecretState()
      return
    }
    if (activeTab.value === 'stats') void load()
    if (activeTab.value === 'keys') void loadKeys()
  },
)

// 切换用户 ⇒ 旧的密钥列表与明文都不能留在屏幕上
watch(
  () => props.user?.id,
  () => {
    keys.value = []
    resetSecretState()
    activeTab.value = 'stats'
  },
)

function switchTab(tab: 'stats' | 'keys') {
  activeTab.value = tab
  if (tab === 'stats' && !stats.value) void load()
  if (tab === 'keys' && keys.value.length === 0) void loadKeys()
}

// F2：v-model（不用 .number）⇒ 空输入是 ''；.number 在 Vue 3 里 looseToNumber
// 对 '' 返回原字符串 ⇒ 旧写法把 '' 当成有效值发出去，后端 *float64 解 JSON 失败
// ⇒ 400 "invalid body"。这里显式归一：空 ⇒ undefined。
function numOrUndef(v: string | number | null | undefined): number | undefined {
  if (v === null || v === undefined || v === '') return undefined
  const n = typeof v === 'number' ? v : Number(v)
  return Number.isFinite(n) ? n : undefined
}

async function handleCreateKey() {
  if (!props.user) return
  const target = props.user
  createKeySaving.value = true
  try {
    const response = await createKey({
      application_code: createKeyForm.value.application_code.trim() || 'default',
      tenant_id: target.tenant_id,
      key_alias: createKeyForm.value.key_alias.trim() || undefined,
      owner_user: target.username,
      budget_usd: numOrUndef(createKeyForm.value.budget_usd),
      rate_limit_rpm: numOrUndef(createKeyForm.value.rate_limit_rpm),
      remark: createKeyForm.value.remark.trim() || undefined,
    })
    revealSecretPayload(response.api_key, response.key_prefix)
    ElMessage.success(t('users.detail.keyCreated'))
    createKeyForm.value = {
      application_code: 'default',
      key_alias: '',
      budget_usd: '',
      rate_limit_rpm: '',
      remark: '',
    }
    await loadKeys()
  } catch (e: unknown) {
    ElMessage.error(surfaceError(e, { key: 'users.detail.createKeyFailed' }))
  } finally {
    createKeySaving.value = false
  }
}

// F6：存量密钥的完整信息 —— 后端 GET /api/keys/{id}/reveal（含审计日志）。
async function handleRevealKey(key: ApiKey) {
  try {
    const resp = await revealKey(key.id)
    revealSecretPayload(resp.api_key, key.key_prefix)
  } catch (e: unknown) {
    ElMessage.error(surfaceError(e, { key: 'users.detail.revealKeyFailed' }))
  }
}

function revealSecretPayload(secret: string, prefix: string) {
  revealedSecret.value = secret
  revealedSecretPrefix.value = prefix
  secretDialogOpen.value = true
}

function closeSecretDialog() {
  secretDialogOpen.value = false
  revealedSecret.value = ''
  revealedSecretPrefix.value = ''
}

async function copySecret() {
  try {
    await navigator.clipboard.writeText(revealedSecret.value)
    ElMessage.success(t('users.detail.copyKeySuccess'))
  } catch {
    ElMessage.error(t('users.detail.copyKeyFailed'))
  }
}

// F3：element-plus 的 confirm 在点「取消」时 reject 'cancel'，在点 X / ESC 时
// reject 'close' —— 旧写法只排除了 'cancel'，于是按 ESC 关掉确认框会弹一条
// 红色的「吊销失败」。
function isDialogDismiss(e: unknown): boolean {
  return e === 'cancel' || e === 'close'
}

async function handleRevokeKey(key: ApiKey) {
  try {
    await ElMessageBox.confirm(
      t('users.detail.revokeKeyConfirm', { prefix: key.key_prefix }),
      t('common.confirmTitle'),
      { type: 'warning' },
    )
    await revokeKey(key.id)
    ElMessage.success(t('users.detail.keyRevoked'))
    await loadKeys()
  } catch (e: unknown) {
    if (isDialogDismiss(e)) return
    ElMessage.error(surfaceError(e, { key: 'users.detail.revokeKeyFailed' }))
  }
}

// F5：只有「可启用」的密钥才给启用按钮。status='revoked' 是终态
// （后端对 revoked 密钥的 enable 明确 409，见 admin/keys.go setKeyEnabled），
// 给按钮等于骗管理员。
async function handleToggleKeyEnabled(key: ApiKey) {
  try {
    if (key.enabled) {
      await disableKey(key.id)
      ElMessage.success(t('users.detail.keyDisabled'))
    } else {
      await enableKey(key.id)
      ElMessage.success(t('users.detail.keyEnabled'))
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
      label: t('users.detail.requests'),
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
// ★ 抽屉内的「API 密钥」标签页不依赖该路由 ⇒ 租户管理员同样可用。
const canDrillKeys = computed(() => isSuperAdmin())
// F8：能管理密钥的角色 = 超管 + 租户管理员（legacy api_key 走 isSuperAdmin 分支）。
const canManageKeys = computed(() => isSuperAdmin() || isTenantAdmin())

// ── 密钥状态（真 status 优先，enabled 只作为辅证） ──────────────
// api_keys.status 的权威取值域来自表约束（sql/objects/tables/api_keys.sql）：
//   CHECK (status IN ('active','pending','disabled','throttled','revoked'))
// 鉴权谓词（domains/authentication/verifier.go callVerifyDB）是
//   enabled = TRUE AND COALESCE(status,'active') NOT IN ('revoked','disabled')
// ⇒ 「已吊销」必须与「已禁用」区分显示：前者不可恢复，后者可恢复；
//   'throttled' 是**仍可用但被限流**的态，不能显示成「禁用」（会误导管理员
//   去点「启用」，而启用并不会解除限流）。
type KeyState = 'active' | 'pending' | 'revoked' | 'disabled' | 'expired' | 'throttled'
function keyState(k: ApiKey): KeyState {
  const status = (k.status || 'active').toLowerCase()
  if (status === 'revoked') return 'revoked'
  if (status === 'pending') return 'pending'
  if (status === 'throttled') return 'throttled'
  if (k.expires_at && Date.parse(k.expires_at) <= Date.now()) return 'expired'
  if (status === 'disabled') return 'disabled'
  if (!k.enabled) return 'disabled'
  return 'active'
}
function keyStateLabel(state: KeyState): string {
  switch (state) {
    case 'revoked': return t('users.detail.keyStatusRevoked')
    case 'pending': return t('users.detail.keyStatusPending')
    case 'throttled': return t('users.detail.keyStatusThrottled')
    case 'expired': return t('users.detail.keyStatusExpired')
    case 'disabled': return t('users.detail.keyStatusDisabled')
    default: return t('users.detail.keyStatusActive')
  }
}
function keyStateBadge(state: KeyState): string {
  switch (state) {
    case 'active': return 'badge-green'
    case 'pending': return 'badge-yellow'
    case 'throttled': return 'badge-yellow'
    case 'expired': return 'badge-gray'
    case 'disabled': return 'badge-red'
    default: return 'badge-gray'
  }
}
// 只有非终态的密钥才允许「启用」
function canEnable(k: ApiKey): boolean {
  return keyState(k) !== 'revoked'
}
function canRevoke(k: ApiKey): boolean {
  return keyState(k) !== 'revoked'
}
// 系统密钥不可吊销/禁用（后端同样拒绝，见 KeysView 的 systemKeyCannotDisable）
function isSystemKey(k: ApiKey): boolean {
  return k.is_system === true
}
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
          <span class="badge" :class="user.enabled ? 'badge-green' : 'badge-red'">{{ user.enabled ? t('users.status.enabled') : t('users.status.disabled') }}</span>
          <span v-if="user.must_change_password" class="badge badge-yellow">{{ t('users.mustChangePassword.pending') }}</span>
          <code class="mono udd-tenant">{{ user.tenant_id }}</code>
        </div>
        <div class="udd-actions">
          <button class="btn btn-sm" @click="onToggleEnabled">{{ user.enabled ? t('users.status.disabled') : t('users.status.enabled') }}</button>
          <button class="btn btn-sm" @click="onResetPassword">{{ t('users.action.resetPassword') }}</button>
        </div>
      </div>

      <!-- 标签页导航 -->
      <div class="udd-tabs">
        <button
          class="udd-tab"
          :class="{ active: activeTab === 'stats' }"
          type="button"
          @click="switchTab('stats')"
        >{{ t('users.detail.statsTab') }}</button>
        <button
          v-if="canManageKeys"
          class="udd-tab"
          :class="{ active: activeTab === 'keys' }"
          type="button"
          @click="switchTab('keys')"
        >
          {{ t('users.detail.keysTab') }}
          <span v-if="keys.length > 0" class="udd-tab-badge">{{ keys.length }}</span>
        </button>
      </div>

      <!-- ══ 统计标签页 ══ -->
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
            >{{ t('users.detail.days') }} {{ d }} {{ t('users.detail.daysUnit') }}</button>
          </div>
        </div>

        <div v-if="loading" class="udd-empty">{{ t('users.detail.loading') }}</div>
        <div v-else-if="errorText" class="udd-empty udd-error">{{ errorText }}</div>
        <div v-else-if="stats?.degraded" class="udd-empty udd-error">{{ t('users.detail.loadStatsFailed') }}</div>

        <template v-else-if="stats?.kpi">
          <!-- KPI -->
          <div class="udd-kpis">
            <StatCard :label="t('users.detail.kpiRequests')" compact>
              <template #value>{{ fmtNum(stats.kpi.requests) }}</template>
              <template #sub>{{ t('users.detail.dailyAvg') }} {{ stats.days > 0 ? fmtNum(Math.round(stats.kpi.requests / stats.days)) : '-' }}</template>
            </StatCard>
            <StatCard label="Token" compact>
              <template #value>{{ fmtTokensCompact(stats.kpi.tokens) }}</template>
              <template #sub>{{ t('users.detail.topModel') }} {{ stats.top_models[0]?.name ?? '—' }}</template>
            </StatCard>
            <StatCard :label="t('users.detail.kpiCredits')" compact tone="success">
              <template #value>{{ fmtNum(stats.kpi.credits) }}</template>
            </StatCard>
            <StatCard :label="t('users.detail.kpiErrP95')" compact tone="danger">
              <template #value>{{ (stats.kpi.error_rate * 100).toFixed(1) }}%<span class="udd-unit">/ {{ fmtMs(stats.kpi.latency_p95_ms) }}</span></template>
              <template #sub>{{ t('users.detail.failed') }} {{ fmtNum(stats.kpi.errors) }}</template>
            </StatCard>
          </div>

          <div v-if="stats.kpi.requests === 0" class="udd-empty">
            {{ t('users.detail.noUsage') }}
          </div>

          <template v-else>
            <!-- 趋势 -->
            <div class="udd-card">
              <div class="udd-card-title">{{ t('users.detail.trend') }}</div>
              <div class="udd-chart"><canvas ref="canvasRef" /></div>
            </div>

            <!-- Top 模型 / 应用 -->
            <div class="udd-grid2">
              <div class="udd-card">
                <div class="udd-card-title">{{ t('users.detail.topModels') }}</div>
                <table class="udd-table">
                  <thead><tr><th>{{ t('users.detail.model') }}</th><th class="num">{{ t('users.detail.req') }}</th><th class="num">Token</th><th class="num">{{ t('users.detail.credits') }}</th></tr></thead>
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
                <div class="udd-card-title">{{ t('users.detail.topApps') }}</div>
                <table class="udd-table">
                  <thead><tr><th>{{ t('users.detail.app') }}</th><th class="num">{{ t('users.detail.req') }}</th><th class="num">Token</th><th class="num">{{ t('users.detail.credits') }}</th></tr></thead>
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
              <div class="udd-card-title">{{ t('users.detail.recent') }}</div>
              <table class="udd-table">
                <thead><tr><th>{{ t('users.detail.time') }}</th><th>{{ t('users.detail.model') }}</th><th class="num">{{ t('users.detail.firstTotal') }}</th><th class="num">{{ t('users.detail.credits') }}</th><th>{{ t('users.detail.status') }}</th></tr></thead>
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
            <div class="udd-card-title">{{ t('users.detail.basic') }}</div>
            <div class="udd-kv">
              <span class="k">{{ t('users.table.displayName') }}</span><span>{{ user.display_name || '—' }}</span>
              <span class="k">{{ t('users.table.email') }}</span><span class="mono">{{ user.email || '—' }}</span>
              <span class="k">{{ t('users.table.tenant') }}</span><span class="mono">{{ user.tenant_id }}</span>
              <span class="k">{{ t('users.table.createdAt') }}</span><span class="mono">{{ fmtDateTime24h(user.created_at) }}</span>
              <span class="k">{{ t('users.table.lastLogin') }}</span><span class="mono">{{ user.last_login_at ? fmtDateTime24h(user.last_login_at) : '—' }}</span>
              <span class="k">{{ t('users.detail.hotKeys') }}</span>
              <span v-if="!stats.top_keys.length">—</span>
              <span v-else-if="canDrillKeys" class="udd-key-links">
                <template v-for="(k, i) in stats.top_keys" :key="k.id ?? i">
                  <span v-if="i > 0" class="udd-key-sep"> · </span>
                  <a v-if="k.id" class="udd-link" :href="keyHref(k.id)">{{ k.name }}×{{ k.requests }}</a>
                  <span v-else class="mono">{{ k.name }}×{{ k.requests }}</span>
                </template>
              </span>
              <span v-else class="mono">{{ topKeysText }}</span>
              <span class="k">{{ t('users.detail.keyCount') }}</span>
              <span v-if="canDrillKeys"><a class="udd-link" :href="keysHref">{{ stats.key_count ?? 0 }}</a></span>
              <span v-else>{{ stats.key_count ?? 0 }}</span>
            </div>
            <div class="udd-links">
              <a class="udd-link" :href="logsHref">{{ t('users.detail.viewAllLogs') }}</a>
            </div>
          </div>
        </template>
      </div>

      <!-- ══ API 密钥标签页 ══ -->
      <div v-if="activeTab === 'keys' && canManageKeys" class="udd-tab-content">
        <div class="udd-keys-toolbar">
          <span class="udd-keys-count">{{ t('users.detail.keysCount', { count: keys.length }) }}</span>
          <button class="btn btn-primary btn-sm" type="button" @click="showCreateKey = true">
            + {{ t('users.detail.createKey') }}
          </button>
        </div>

        <div v-if="keysLoading" class="udd-empty">{{ t('common.loading') }}</div>
        <div v-else-if="keysError" class="udd-empty udd-error">{{ keysError }}</div>
        <div v-else-if="keys.length === 0" class="udd-empty">{{ t('users.detail.noKeys') }}</div>

        <div v-else class="udd-keys-list">
          <div v-for="key in keys" :key="key.id" class="udd-key-card">
            <div class="udd-key-header">
              <div class="udd-key-prefix">
                <code>{{ key.key_prefix }}****</code>
                <span v-if="key.key_alias" class="udd-key-alias">{{ key.key_alias }}</span>
                <span v-if="isSystemKey(key)" class="badge badge-gray">{{ t('users.detail.keyIsSystem') }}</span>
              </div>
              <div class="udd-key-actions">
                <button
                  class="btn btn-ghost btn-xs"
                  type="button"
                  :disabled="isSystemKey(key)"
                  @click="handleRevealKey(key)"
                >{{ t('users.detail.revealKey') }}</button>
                <button
                  v-if="key.enabled || canEnable(key)"
                  class="btn btn-ghost btn-xs"
                  type="button"
                  :disabled="isSystemKey(key) || !canEnable(key)"
                  @click="handleToggleKeyEnabled(key)"
                >{{ key.enabled ? t('users.detail.disableKey') : t('users.detail.enableKey') }}</button>
                <button
                  v-if="canRevoke(key)"
                  class="btn btn-ghost btn-xs udd-btn-danger"
                  type="button"
                  :disabled="isSystemKey(key)"
                  @click="handleRevokeKey(key)"
                >{{ t('users.detail.revokeKey') }}</button>
              </div>
            </div>

            <div class="udd-key-info">
              <div class="udd-key-row">
                <span class="udd-key-label">{{ t('users.detail.keyStatus') }}</span>
                <span class="badge" :class="keyStateBadge(keyState(key))">{{ keyStateLabel(keyState(key)) }}</span>
              </div>
              <div class="udd-key-row">
                <span class="udd-key-label">{{ t('users.detail.keyApp') }}</span>
                <span class="badge badge-blue">{{ key.application_code || '—' }}</span>
              </div>
              <div class="udd-key-row">
                <span class="udd-key-label">{{ t('users.detail.keyRequestCount') }}</span>
                <span>{{ fmtNum(key.total_requests) }}</span>
              </div>
              <div class="udd-key-row">
                <span class="udd-key-label">Token</span>
                <span>{{ fmtTokensCompact(key.total_prompt_tokens + key.total_completion_tokens) }}</span>
              </div>
              <div class="udd-key-row">
                <span class="udd-key-label">{{ t('users.detail.keyRateLimit') }}</span>
                <span>{{ key.rate_limit_rpm ? `${key.rate_limit_rpm} RPM` : t('keys.common.unlimited') }}</span>
              </div>
              <div class="udd-key-row">
                <span class="udd-key-label">{{ t('users.detail.keyBudget') }}</span>
                <span>{{ key.budget_usd != null ? `$${key.budget_usd.toFixed(2)}` : t('keys.common.unlimited') }}</span>
              </div>
              <div class="udd-key-row">
                <span class="udd-key-label">{{ t('users.detail.keyLastUsed') }}</span>
                <span class="mono">{{ key.last_used_at ? fmtDateTime24h(key.last_used_at) : t('users.detail.keyNeverUsed') }}</span>
              </div>
              <div class="udd-key-row">
                <span class="udd-key-label">{{ t('users.detail.keyCreatedAt') }}</span>
                <span class="mono">{{ key.created_at ? fmtDateTime24h(key.created_at) : '—' }}</span>
              </div>
            </div>

            <p v-if="key.remark" class="udd-key-remark-line">
              <span class="udd-key-label">{{ t('users.detail.keyRemark') }}</span> {{ key.remark }}
            </p>
            <p v-if="keyState(key) === 'revoked'" class="udd-key-note">
              {{ t('users.detail.keyRevokedNote') }}
            </p>
          </div>
        </div>

        <!-- 签发密钥对话框 -->
        <el-dialog
          v-model="showCreateKey"
          :title="t('users.detail.createKeyTitle')"
          width="500px"
          style="max-height: 80vh; overflow-y: auto;"
        >
          <div class="udd-form">
            <label class="udd-form-label" for="udd-key-app">{{ t('users.detail.keyAppCode') }} *</label>
            <input id="udd-key-app" v-model="createKeyForm.application_code" class="udd-input" type="text" placeholder="default" />
          </div>
          <div class="udd-form">
            <label class="udd-form-label" for="udd-key-alias">{{ t('users.detail.keyAlias') }}</label>
            <input id="udd-key-alias" v-model="createKeyForm.key_alias" class="udd-input" type="text" :placeholder="t('users.detail.keyAliasPlaceholder')" />
          </div>
          <div class="udd-form">
            <label class="udd-form-label" for="udd-key-budget">{{ t('users.detail.keyBudget') }} (USD)</label>
            <input id="udd-key-budget" v-model="createKeyForm.budget_usd" class="udd-input" type="number" min="0" step="0.01" :placeholder="t('users.detail.keyBudgetPlaceholder')" />
          </div>
          <div class="udd-form">
            <label class="udd-form-label" for="udd-key-rpm">{{ t('users.detail.keyRateLimit') }} (RPM)</label>
            <input id="udd-key-rpm" v-model="createKeyForm.rate_limit_rpm" class="udd-input" type="number" min="0" :placeholder="t('users.detail.keyRateLimitPlaceholder')" />
          </div>
          <div class="udd-form">
            <label class="udd-form-label" for="udd-key-remark">{{ t('users.detail.keyRemark') }}</label>
            <textarea id="udd-key-remark" v-model="createKeyForm.remark" class="udd-input udd-textarea" rows="3" :placeholder="t('users.detail.keyRemarkPlaceholder')"></textarea>
          </div>
          <template #footer>
            <button class="btn btn-ghost" type="button" @click="showCreateKey = false">{{ t('common.cancel') }}</button>
            <button class="btn btn-primary" type="button" :disabled="createKeySaving" @click="handleCreateKey">
              {{ createKeySaving ? t('common.loading') : t('users.detail.createKey') }}
            </button>
          </template>
        </el-dialog>

        <!-- 完整密钥对话框（签发结果 + 存量密钥 reveal 共用） -->
        <el-dialog
          v-model="secretDialogOpen"
          :title="t('users.detail.revealKeyTitle')"
          width="520px"
          style="max-height: 80vh; overflow-y: auto;"
          @closed="closeSecretDialog"
        >
          <p class="udd-secret-warn">{{ t('users.detail.revealKeyWarn') }}</p>
          <div class="udd-secret-box">
            <code>{{ revealedSecret }}</code>
          </div>
          <p class="udd-secret-prefix">
            {{ t('users.detail.keyPrefix') }}: <code>{{ revealedSecretPrefix }}****</code>
          </p>
          <template #footer>
            <button class="btn btn-ghost" type="button" @click="closeSecretDialog">{{ t('common.close') }}</button>
            <button class="btn btn-primary" type="button" @click="copySecret">{{ t('common.copy') }}</button>
          </template>
        </el-dialog>
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
}
.udd-tab:hover { color: var(--accent); }
.udd-tab.active {
  color: var(--accent);
  border-bottom-color: var(--accent);
  font-weight: 600;
}
.udd-tab-badge {
  display: inline-block;
  background: var(--accent);
  color: #fff;
  font-size: 10px;
  padding: 1px 5px;
  border-radius: 10px;
  margin-left: 4px;
  font-weight: 600;
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
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.udd-keys-count { color: var(--muted); font-size: 12px; }
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
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 8px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--border);
}
.udd-key-prefix {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.udd-key-prefix code {
  font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
  font-size: 13px;
  font-weight: 600;
}
.udd-key-alias { color: var(--muted); font-size: 12px; }
.udd-key-actions { display: flex; gap: 4px; flex-wrap: wrap; }
.udd-btn-danger { color: var(--danger); }
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
  min-width: 0;
}
.udd-key-label {
  color: var(--muted);
  font-size: 11px;
}
.udd-key-remark-line {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--text);
  word-break: break-word;
}
.udd-key-note {
  margin: 8px 0 0;
  font-size: 11px;
  color: var(--danger);
}

/* 签发表单 */
.udd-form { margin-bottom: 12px; }
.udd-form-label {
  display: block;
  margin-bottom: 4px;
  font-size: 12px;
  font-weight: 500;
}
.udd-input {
  width: 100%;
  padding: 6px 10px;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg);
  color: var(--text);
  font-size: 13px;
  box-sizing: border-box;
}
.udd-input:focus { outline: none; border-color: var(--accent); }
.udd-textarea { resize: vertical; }

/* 完整密钥对话框 */
.udd-secret-warn {
  color: var(--warning);
  font-size: 12px;
  margin: 0 0 12px;
}
.udd-secret-box {
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 12px;
  margin-bottom: 8px;
  word-break: break-all;
}
.udd-secret-box code {
  font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
  font-size: 13px;
  color: var(--accent);
}
.udd-secret-prefix {
  margin: 0;
  font-size: 11px;
  color: var(--muted);
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
