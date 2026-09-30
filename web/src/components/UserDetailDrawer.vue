<script setup lang="ts">
// UserDetailDrawer.vue — 用户详情抽屉（2026-09-30 统计 UI 优化轮）
//
// /users 列表点击行打开：KPI（请求/Token/积分/失败率·P95）+ 请求与 Token
// 趋势 + Top 模型/应用 + 最近请求（首字/总耗时）+ 基本信息。
// 数据面 GET /api/admin/users/{id}/stats（usage_facts × api_keys.owner_user，
// 与对账快照同源）；账号无用量时 KPI 全零 + 空态文案，基本信息仍完整展示。
// 启停/重置密码操作不在抽屉内自行执行，emit 回列表页复用既有 handler。
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { fmtDateTime24h } from '../i18n/useFormat'
import { getUserStats, type UserStats } from '../api'
import { isSuperAdmin } from '../store'
import StatCard from './ui/StatCard.vue'
import AppDrawer from './ui/AppDrawer.vue'
import { chartColors, createComboChartConfig, useChart } from '../composables/useChart'

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

const days = ref(30)
const stats = ref<UserStats | null>(null)
const loading = ref(false)
const errorText = ref('')

let fetchGen = 0
async function load() {
  if (!props.user) return
  const gen = ++fetchGen
  loading.value = true
  errorText.value = ''
  stats.value = null
  try {
    const next = await getUserStats(props.user.id, days.value)
    if (gen !== fetchGen) return
    stats.value = next
  } catch (e: unknown) {
    if (gen !== fetchGen) return
    errorText.value = e instanceof Error ? e.message : '加载用户统计失败'
  } finally {
    if (gen === fetchGen) loading.value = false
  }
}

watch(
  () => [props.modelValue, props.user?.id, days.value],
  ([open]) => {
    if (open) void load()
  },
)

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
          <span class="badge" :class="user.enabled ? 'badge-green' : 'badge-red'">{{ user.enabled ? t('users.status.enabled', '启用') : t('users.status.disabled', '禁用') }}</span>
          <span v-if="user.must_change_password" class="badge badge-yellow">{{ t('users.mustChangePassword.pending', '待改密') }}</span>
          <code class="mono udd-tenant">{{ user.tenant_id }}</code>
        </div>
        <div class="udd-actions">
          <button class="btn btn-sm" @click="onToggleEnabled">{{ user.enabled ? t('users.action.disable', '禁用') : t('users.action.enable', '启用') }}</button>
          <button class="btn btn-sm" @click="onResetPassword">{{ t('users.action.resetPassword', '重置密码') }}</button>
        </div>
      </div>

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
          >{{ t('users.detail.days', '近') }} {{ d }} {{ t('users.detail.daysUnit', '天') }}</button>
        </div>
      </div>

      <div v-if="loading" class="udd-empty">{{ t('users.detail.loading', '加载中…') }}</div>
      <div v-else-if="errorText" class="udd-empty udd-error">{{ errorText }}</div>

      <template v-else-if="stats">
        <!-- KPI -->
        <div class="udd-kpis">
          <StatCard :label="t('users.detail.kpiRequests', '请求数')" icon="📥" compact>
            <template #value>{{ fmtNum(stats.kpi.requests) }}</template>
            <template #sub>{{ t('users.detail.dailyAvg', '日均') }} {{ stats.days > 0 ? fmtNum(Math.round(stats.kpi.requests / stats.days)) : '-' }}</template>
          </StatCard>
          <StatCard label="Token" icon="🔢" compact>
            <template #value>{{ fmtTokensCompact(stats.kpi.tokens) }}</template>
            <template #sub>{{ t('users.detail.topModel', '主力') }} {{ stats.top_models[0]?.name ?? '—' }}</template>
          </StatCard>
          <StatCard :label="t('users.detail.kpiCredits', '积分消耗')" icon="🪙" compact tone="success">
            <template #value>{{ fmtNum(stats.kpi.credits) }}</template>
          </StatCard>
          <StatCard :label="t('users.detail.kpiErrP95', '失败率 / P95')" icon="⏱️" compact tone="danger">
            <template #value>{{ (stats.kpi.error_rate * 100).toFixed(1) }}%<span class="udd-unit">/ {{ fmtMs(stats.kpi.latency_p95_ms) }}</span></template>
            <template #sub>{{ t('users.detail.failed', '失败') }} {{ fmtNum(stats.kpi.errors) }}</template>
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
                <thead><tr><th>{{ t('users.detail.model', '模型') }}</th><th class="num">{{ t('users.detail.req', '请求') }}</th><th class="num">Token</th><th class="num">{{ t('users.detail.credits', '积分') }}</th></tr></thead>
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
                <thead><tr><th>{{ t('users.detail.app', '应用') }}</th><th class="num">{{ t('users.detail.req', '请求') }}</th><th class="num">Token</th><th class="num">{{ t('users.detail.credits', '积分') }}</th></tr></thead>
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
              <thead><tr><th>{{ t('users.detail.time', '时间') }}</th><th>{{ t('users.detail.model', '模型') }}</th><th class="num">{{ t('users.detail.firstTotal', '首字 / 总耗时') }}</th><th class="num">{{ t('users.detail.credits', '积分') }}</th><th>{{ t('users.detail.status', '状态') }}</th></tr></thead>
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
            <span class="k">{{ t('users.table.displayName', '显示名') }}</span><span>{{ user.display_name || '—' }}</span>
            <span class="k">{{ t('users.table.email', '邮箱') }}</span><span class="mono">{{ user.email || '—' }}</span>
            <span class="k">{{ t('users.table.tenant', '租户') }}</span><span class="mono">{{ user.tenant_id }}</span>
            <span class="k">{{ t('users.table.createdAt', '创建时间') }}</span><span class="mono">{{ fmtDateTime24h(user.created_at) }}</span>
            <span class="k">{{ t('users.table.lastLogin', '最后登录') }}</span><span class="mono">{{ user.last_login_at ? fmtDateTime24h(user.last_login_at) : '—' }}</span>
            <span class="k">{{ t('users.detail.hotKeys', '高频密钥') }}</span><span class="mono">{{ topKeysText }}</span>
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
  </AppDrawer>
</template>

<style scoped>
.udd-header {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 10px;
}
.udd-identity { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; flex: 1; }
.udd-tenant { font-size: 11.5px; color: var(--muted); }
.udd-actions { display: flex; gap: 6px; }
.udd-toolbar { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; }
.udd-chips { display: inline-flex; gap: 6px; }
.udd-chip {
  border: 1px solid var(--border);
  background: transparent;
  color: var(--muted);
  border-radius: 999px;
  padding: 2px 11px;
  font-size: 12.5px;
  cursor: pointer;
}
.udd-chip:hover { color: var(--accent); border-color: var(--accent); }
.udd-chip.active {
  background: var(--bg-subtle);
  border-color: var(--accent);
  color: var(--accent);
  font-weight: 600;
}
.udd-kpis {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 10px;
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
.udd-card-title { font-weight: 600; font-size: 13.5px; margin-bottom: 8px; }
.udd-chart { height: 170px; position: relative; }
.udd-chart canvas { width: 100% !important; height: 100% !important; }
.udd-grid2 {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
@media (max-width: 900px) { .udd-grid2 { grid-template-columns: 1fr; } }
.udd-table { width: 100%; border-collapse: collapse; font-size: 12.5px; }
.udd-table th {
  text-align: left;
  font-size: 10.5px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--muted);
  padding: 5px 6px;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
.udd-table td { padding: 6px; border-bottom: 1px solid var(--border); white-space: nowrap; }
.udd-table tbody tr:last-child td { border-bottom: none; }
.udd-table .num { text-align: right; font-variant-numeric: tabular-nums; }
.udd-table-empty { text-align: center; color: var(--muted); padding: 14px 0; }
.udd-time { font-size: 11.5px; }
.udd-kv {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 4px 14px;
  font-size: 12.5px;
}
.udd-kv .k { color: var(--muted); }
.udd-links { margin-top: 10px; }
.udd-link { color: var(--accent); font-size: 12.5px; }
.udd-empty {
  color: var(--muted);
  text-align: center;
  padding: 28px 8px;
  font-size: 13px;
}
.udd-error { color: var(--danger); }
.mono {
  font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
  font-variant-numeric: tabular-nums;
}
</style>
