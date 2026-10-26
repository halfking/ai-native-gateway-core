<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useHyperPage } from '@/hyper'
import { ApiError } from '@/api/client'
import { fetchUserStats, fetchUserUsageSummary, type UserStatsResponse, type UserUsageRow } from '@/api/userUsageStats'
import { fetchManagedUsers, setManagedUserEnabled, type ManagedUser } from '@/api/users'
import { fetchTenants, fetchTenantUsers, fetchTenantStats, type TenantInfo, type TenantStats, type TenantUser } from '@/api/tenants'
import { createKey } from '@/api/keys'
import { useAuthStore } from '@/stores/auth'
import { t } from '@/i18n'
import AppStateView from '@/components/common/AppStateView.vue'
import AppSheet from '@/components/common/AppSheet.vue'
import DataTableFocus from '@/components/common/DataTableFocus.vue'
import StatCard from '@/components/common/StatCard.vue'
import { fmtDate, fmtInt, fmtNum, fmtTime, fmtUsd } from '@/utils/format'
import { formatApiError } from '@/utils/errors'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const isSuperAdmin = computed(() => auth.role === 'super_admin')
const selectedId = computed(() => { const id = Number(route.params.id); return Number.isSafeInteger(id) && id > 0 ? id : 0 })
useHyperPage({ title: () => selectedId.value ? t('users.detailTitle') : t('users.title') })

// ★ 默认窗口必须是 **7**，不是 30（2026-10-13 实测修正）。
//
// 三处权威都指向 7：
//   · 后端 `getTenantStats` 自身缺省就是 7（`days < 1 ⇒ 7`，admin/tenants.go）
//   · Web 端 `TenantDetailView.statsDays = ref(7)`（选项 [7,30,90,365]，默认 7）
//   · 本页此前是唯一的 30 —— 且 **30 天在真库上确定性失败**。
//
// 实测（245 / 2026-10-13，端到端 curl 计时，非推断）：
//   GET /api/admin/tenants/default/stats?days=30 → **504 / 10.09s / 10.10s**
//       （两次都精确顶穿 `context.WithTimeout(10s)`，不是抖动）
//   GET …?days=7  → **200 / 2.74s / 4.27s**
//
// 根因（EXPLAIN ANALYZE 实测，非推测）：30 天窗在该租户命中 1,516,587 行，
// 而 `totals` 段要的 `total_tokens / api_key_id / application_id /
// raw_model_name / canonical_id` **没有一列**在既有覆盖索引
// `usage_ledger_ts_tenant_cover_agg_idx (ts, tenant_id) INCLUDE
// (prompt_tokens, completion_tokens, cost_usd, latency_ms, success)` 里
// ⇒ 不可能走 index-only scan ⇒ 120 万次堆取。
// 同谓词只查「被覆盖的那几列」= 0.9s，带上 DISTINCT 全部列 = 10.3s（热态）。
//
// ⚠️ 选项表（7/30/90）**保持原样**：30/90 仍可手动选，选中后的降级提示
// 「该时间范围的租户用量统计超时，请缩短统计天数后重试」是端点既有的
// 504 契约（`writeTenantStatsError`），不是新加的行为。
// 这里改的是**默认落在哪个档位**，不是端点的能力边界。
const days = ref(7)
const users = ref<ManagedUser[]>([])
const usage = ref<UserUsageRow[]>([])
const tenants = ref<TenantInfo[]>([])
interface TenantSection { expanded: boolean; users: TenantUser[] | null; stats: TenantStats | null; loading: boolean; error: string | null; usersError: string | null; statsError: string | null }
const tenantSections = ref<Record<string, TenantSection>>({})
const detail = ref<UserStatsResponse | null>(null)
const loading = ref(true)
const updatingId = ref<number | null>(null)
const query = ref('')
const error = ref<string | null>(null)
const detailError = ref<string | null>(null)

const visibleTenants = computed(() => tenants.value.filter((tenant) => `${tenant.name} ${tenant.code} ${tenant.status}`.toLowerCase().includes(query.value.trim().toLowerCase())))
const visibleUsers = computed(() => users.value.filter((user) => `${user.username} ${user.display_name} ${user.email} ${user.tenant_id}`.toLowerCase().includes(query.value.trim().toLowerCase())))
const usageByUsername = computed(() => new Map(usage.value.map((row) => [row.username, row])))
const selectedUser = computed(() => users.value.find((user) => user.id === selectedId.value) ?? null)
const detailLoading = ref(false)
let disposed = false
let listAborter: AbortController | null = null
let detailAborter: AbortController | null = null
let createGeneration = 0

// 「在用户名下添加 apikey」（2026-10-10，第二百零一批）—— 仅在用户详情
// 页打开抽屉；创建时把 owner_user 绑定为当前用户 username（后端字段语义见
// api/keys.ts 的 CreateKeyPayload.owner_user 注释）。
const createOpen = ref(false)
const formAppCode = ref('')
const formName = ref('')
const formBudget = ref('')
const creating = ref(false)
const createdSecret = ref<string | null>(null)
const createError = ref<string | null>(null)
const copied = ref(false)

async function loadList(): Promise<void> {
  if (disposed) return
  listAborter?.abort()
  listAborter = new AbortController()
  const signal = listAborter.signal
  loading.value = true
  error.value = null
  tenants.value = []
  users.value = []
  usage.value = []
  tenantSections.value = {}
  try {
    const listData = isSuperAdmin.value
      ? await fetchTenants({}, { signal })
      : await Promise.all([fetchManagedUsers({ signal }), fetchUserUsageSummary({ days: days.value }, { signal })])
    if (signal.aborted || disposed) return
    if (isSuperAdmin.value) tenants.value = listData as TenantInfo[]
    else { users.value = listData[0] as ManagedUser[]; usage.value = (listData[1] as Awaited<ReturnType<typeof fetchUserUsageSummary>>).items }
  } catch (cause) {
    if (!signal.aborted && !disposed) error.value = formatApiError(cause)
  } finally {
    if (!signal.aborted && !disposed) loading.value = false
  }
}

function tenantSection(code: string): TenantSection {
  return tenantSections.value[code] ?? { expanded: false, users: null, stats: null, loading: false, error: null, usersError: null, statsError: null }
}

async function toggleTenant(tenant: TenantInfo): Promise<void> {
  const state = tenantSection(tenant.code)
  state.expanded = !state.expanded
  tenantSections.value = { ...tenantSections.value, [tenant.code]: state }
  // stats 504 时 state.stats 仍为 null，但已有 statsError —— 勿反复重入把用户列表冲掉。
  if (!state.expanded || state.loading || (state.users && (state.stats || state.statsError))) return
  await loadTenantSection(tenant, state)
}

async function loadTenantSection(tenant: TenantInfo, state: TenantSection): Promise<void> {
  if (state.loading) return
  state.loading = true
  state.error = null
  state.usersError = null
  state.statsError = null
  tenantSections.value = { ...tenantSections.value, [tenant.code]: state }
  // 2026-10-18：default 租户 stats 即便 days=1 也会在 ~10s 打 504（llmgo 实测）。
  // 旧实现 Promise.allSettled 等两边都结束才关 loading ⇒ 展开后整段「正在加载…」
  // 挡住用户列表与「创建密钥」。用户腿先落盘、先解 loading；stats 随后补齐或出超时提示。
  const usersPromise = fetchTenantUsers(tenant.code)
  const statsPromise = fetchTenantStats(tenant.code, { days: days.value })
  try {
    state.users = await usersPromise
  } catch (reason) {
    state.usersError = formatApiError(reason)
  }
  state.loading = false
  tenantSections.value = { ...tenantSections.value, [tenant.code]: state }
  try {
    state.stats = await statsPromise
  } catch (reason) {
    state.statsError = reason instanceof ApiError && reason.status === 504
      ? t('users.tenantStatsTimeout')
      : formatApiError(reason)
  }
  if (!state.users && !state.stats) state.error = [state.usersError, state.statsError].filter(Boolean).join(' · ')
  tenantSections.value = { ...tenantSections.value, [tenant.code]: state }
}

function retryTenant(tenant: TenantInfo): void {
  const state = tenantSection(tenant.code)
  state.users = null
  state.stats = null
  state.error = null
  state.usersError = null
  state.statsError = null
  state.expanded = true
  tenantSections.value = { ...tenantSections.value, [tenant.code]: state }
  void loadTenantSection(tenant, state)
}

async function loadDetail(): Promise<void> {
  if (disposed) return
  detailAborter?.abort()
  detailAborter = new AbortController()
  const signal = detailAborter.signal
  detailError.value = null
  detail.value = null
  detailLoading.value = false
  if (!selectedId.value) return
  detailLoading.value = true
  // 用户列表与画像拆开：画像解包失败时仍要能拿到 selectedUser，
  // 否则「创建密钥」按钮可见但 openCreate 因 selectedUser==null 静默 no-op。
  try {
    const managedUsers = await fetchManagedUsers({ signal })
    if (!signal.aborted && !disposed) users.value = managedUsers
  } catch (cause) {
    if (!signal.aborted && !disposed && !detailError.value) detailError.value = formatApiError(cause)
  }
  try {
    const response = await fetchUserStats({ userId: selectedId.value, days: days.value }, { signal })
    if (!signal.aborted && !disposed) detail.value = response
  } catch (cause) {
    if (!signal.aborted && !disposed) detailError.value = formatApiError(cause)
  } finally {
    if (!signal.aborted && !disposed) detailLoading.value = false
  }
}

async function toggleEnabled(user: ManagedUser | TenantUser): Promise<void> {
  const next = !user.enabled
  const confirmed = window.confirm(t(next ? 'users.enableConfirm' : 'users.disableConfirm', { name: user.display_name || user.username }))
  if (!confirmed || updatingId.value != null) return
  updatingId.value = user.id
  error.value = null
  try {
    await setManagedUserEnabled(user.id, next)
    if (selectedId.value) await loadDetail()
    else {
      const state = tenantSection(user.tenant_id)
      state.users = null
      tenantSections.value = { ...tenantSections.value, [user.tenant_id]: state }
      const tenant = tenants.value.find((item) => item.code === user.tenant_id)
      if (tenant && state.expanded) await loadTenantSection(tenant, state)
    }
  } catch (cause) {
    error.value = formatApiError(cause)
  } finally {
    updatingId.value = null
  }
}

function showDetails(user: ManagedUser | TenantUser): void {
  void router.push(`/users/${user.id}`)
}

function backToList(): void {
  void router.push('/users')
}

// ---- 用户下创建 API Key ----
function openCreate(): void {
  if (!selectedUser.value || creating.value) return
  // 默认 appCode = 用户名，便于一眼看出归属；用户可改。
  formAppCode.value = selectedUser.value.username
  formName.value = ''
  formBudget.value = ''
  createdSecret.value = null
  createError.value = null
  copied.value = false
  createOpen.value = true
}

function closeCreate(): void {
  ++createGeneration
  createOpen.value = false
  formAppCode.value = ''
  formName.value = ''
  formBudget.value = ''
  createdSecret.value = null
  createError.value = null
  copied.value = false
}

async function submitCreate(): Promise<void> {
  if (creating.value || !selectedUser.value) return
  if (!formAppCode.value.trim()) {
    createError.value = t('keys.form.appNameRequired')
    return
  }
  const user = selectedUser.value
  const generation = ++createGeneration
  const budget = formBudget.value.trim() === '' ? undefined : Number(formBudget.value)
  if (budget != null && (!Number.isFinite(budget) || budget < 0)) {
    createError.value = t('keys.form.budgetInvalid')
    return
  }
  creating.value = true
  createError.value = null
  try {
    const resp = await createKey({
      application_code: formAppCode.value.trim(),
      key_alias: formName.value.trim() || undefined,
      budget_usd: budget,
      owner_user: user.username,
      tenant_id: user.tenant_id,
    })
    if (disposed || generation !== createGeneration || selectedId.value !== user.id) return
    createdSecret.value = resp.api_key
    await loadDetail()
  } catch (cause) {
    if (!disposed && generation === createGeneration) createError.value = formatApiError(cause)
  } finally {
    creating.value = false
  }
}

async function copySecret(): Promise<void> {
  if (!createdSecret.value) return
  try {
    await navigator.clipboard.writeText(createdSecret.value)
    copied.value = true
  } catch {
    /* 剪贴板不可用 — 用户手动长按复制 */
  }
}

onMounted(async () => {
  if (!selectedId.value) await loadList()
  else loading.value = false
  await loadDetail()
})
watch(days, async () => {
  if (selectedId.value) { await loadDetail(); return }
  const expanded = Object.entries(tenantSections.value).filter(([, state]) => state.expanded)
  await Promise.all(expanded.map(async ([code, state]) => {
    state.stats = null
    tenantSections.value = { ...tenantSections.value, [code]: state }
    const tenant = tenants.value.find((item) => item.code === code)
    if (tenant) await loadTenantSection(tenant, state)
  }))
})
watch(selectedId, () => { closeCreate(); void loadDetail() })
onBeforeUnmount(() => { disposed = true; listAborter?.abort(); detailAborter?.abort(); closeCreate() })
</script>

<template>
  <div class="user-usage">
    <div class="user-usage__toolbar">
      <label>
        <span>{{ t('users.days') }}</span>
        <select v-model.number="days">
          <option :value="7">7</option><option :value="30">30</option><option :value="90">90</option>
        </select>
      </label>
      <button v-if="selectedId" type="button" class="btn" @click="backToList">{{ t('users.back') }}</button>
      <button v-if="selectedId" type="button" class="btn btn--primary" @click="openCreate">{{ t('keys.create') }}</button>
    </div>

    <p v-if="error" class="user-usage__error" role="alert">{{ error }}</p>
    <AppStateView v-if="loading" :loading="true" />

    <template v-else-if="!selectedId">
      <label class="user-usage__search">
        <span>{{ t('common.search') }}</span>
        <input v-model="query" type="search" :placeholder="isSuperAdmin ? t('users.searchTenantPlaceholder') : t('users.searchPlaceholder')" />
      </label>
      <template v-if="isSuperAdmin">
      <p v-if="!visibleTenants.length" class="user-usage__empty">{{ t('common.empty') }}</p>
      <ul v-else class="user-usage__list">
        <li v-for="tenant in visibleTenants" :key="tenant.code" class="user-usage__tenant">
          <button type="button" class="user-usage__tenant-head" :aria-expanded="tenantSections[tenant.code]?.expanded ?? false" @click="toggleTenant(tenant)">
            <span><strong>{{ tenant.name }}</strong><small>{{ tenant.code }} · {{ tenant.status }}</small></span>
            <span class="user-usage__tenant-count">{{ tenant.user_count == null ? '—' : fmtInt(tenant.user_count) }} {{ t('users.tenantUsers') }} <b>{{ tenantSections[tenant.code]?.expanded ? '−' : '+' }}</b></span>
          </button>
          <section v-if="tenantSections[tenant.code]?.expanded" class="user-usage__tenant-body">
            <p v-if="tenantSections[tenant.code]?.loading" class="user-usage__empty">{{ t('common.loading') }}</p>
            <div v-else-if="tenantSections[tenant.code]?.error" class="user-usage__error" role="alert">
              <p>{{ tenantSections[tenant.code]?.error }}</p>
              <button type="button" class="btn" @click="retryTenant(tenant)">{{ t('common.retry') }}</button>
            </div>
            <template v-else>
              <p v-if="tenantSections[tenant.code]?.statsError" class="user-usage__error" role="status">{{ tenantSections[tenant.code]?.statsError }}</p>
              <p v-if="tenantSections[tenant.code]?.usersError" class="user-usage__error" role="status">{{ tenantSections[tenant.code]?.usersError }}</p>
              <template v-if="tenantSections[tenant.code]?.stats">
              <div class="user-usage__tenant-kpis">
                <StatCard :label="t('users.requests')" :value="fmtInt(tenantSections[tenant.code]!.stats!.total_requests)" />
                <StatCard :label="t('users.tokens')" :value="fmtInt(tenantSections[tenant.code]!.stats!.total_tokens)" />
                <StatCard :label="t('users.cost')" :value="fmtUsd(tenantSections[tenant.code]!.stats!.total_cost_usd)" />
                <StatCard :label="t('users.credits')" :value="fmtInt(tenantSections[tenant.code]!.stats!.total_credits)" />
              </div>
              <section class="user-usage__tenant-breakdown">
                <h3>{{ t('users.topModels') }}</h3>
                <p v-if="!tenantSections[tenant.code]!.stats!.by_model.length" class="user-usage__empty">{{ t('common.empty') }}</p>
                <div v-for="row in tenantSections[tenant.code]!.stats!.by_model" :key="row.model" class="user-usage__bucket"><span>{{ row.model }}</span><b>{{ fmtInt(row.requests) }} {{ t('users.requests') }} · {{ fmtUsd(row.cost_usd) }}</b></div>
              </section>
              <section class="user-usage__tenant-breakdown">
                <h3>{{ t('users.topApps') }}</h3>
                <div v-for="row in tenantSections[tenant.code]!.stats!.by_application" :key="row.application_code" class="user-usage__bucket"><span>{{ row.application_code }}</span><b>{{ fmtInt(row.requests) }} {{ t('users.requests') }} · {{ fmtUsd(row.cost_usd) }}</b></div>
              </section>
              <section class="user-usage__tenant-breakdown">
                <h3>{{ t('users.daily') }} · {{ tenantSections[tenant.code]!.stats!.days }} {{ t('users.days') }}</h3>
                <div v-for="row in tenantSections[tenant.code]!.stats!.daily" :key="row.date" class="user-usage__bucket"><span>{{ fmtDate(`${row.date}T00:00:00`) }}</span><b>{{ fmtInt(row.requests) }} {{ t('users.requests') }} · {{ fmtInt(row.tokens) }} {{ t('users.tokens') }} · {{ fmtUsd(row.cost_usd) }}</b></div>
              </section>
              </template>
              <template v-if="tenantSections[tenant.code]?.users">
              <div class="user-usage__users-head"><h3>{{ t('users.tenantUsers') }}</h3><span>{{ fmtInt(tenantSections[tenant.code]!.users!.length) }}</span></div>
              <p v-if="!tenantSections[tenant.code]!.users!.length" class="user-usage__empty">{{ t('users.noTenantUsers') }}</p>
              <article v-for="user in tenantSections[tenant.code]!.users" :key="user.id" class="user-usage__card">
                <div class="user-usage__head"><div><strong>{{ user.display_name || user.username }}</strong><small>{{ user.username }} · {{ user.email || '—' }}</small></div><span class="badge" :class="user.enabled ? 'badge--success' : 'badge--muted'">{{ user.enabled ? t('users.enabled') : t('users.disabled') }}</span></div>
                <div class="user-usage__actions">
                  <button type="button" class="btn" @click="showDetails(user)">{{ t('users.details') }}</button>
                  <button type="button" class="btn" :disabled="updatingId != null" @click="toggleEnabled(user)">{{ user.enabled ? t('users.disable') : t('users.enable') }}</button>
                </div>
              </article>
              </template>
            </template>
          </section>
        </li>
      </ul>
      </template>
      <template v-else>
        <p v-if="!visibleUsers.length" class="user-usage__empty">{{ t('common.empty') }}</p>
        <ul v-else class="user-usage__list">
          <li v-for="user in visibleUsers" :key="user.id" class="user-usage__card">
            <div class="user-usage__head">
              <div><strong>{{ user.display_name || user.username }}</strong><small>{{ user.username }} · {{ user.tenant_id }} · {{ user.email || '—' }}</small></div>
              <span class="badge" :class="user.enabled ? 'badge--success' : 'badge--muted'">{{ user.enabled ? t('users.enabled') : t('users.disabled') }}</span>
            </div>
            <div class="user-usage__metrics">
              <span><small>{{ t('users.requests') }}</small><b>{{ fmtInt(usageByUsername.get(user.username)?.requests ?? 0) }}</b></span>
              <span><small>{{ t('users.tokens') }}</small><b>{{ fmtInt(usageByUsername.get(user.username)?.tokens ?? 0) }}</b></span>
              <span><small>{{ t('users.credits') }}</small><b>{{ fmtInt(usageByUsername.get(user.username)?.credits ?? 0) }}</b></span>
            </div>
            <div class="user-usage__actions">
              <button type="button" class="btn" @click="showDetails(user)">{{ t('users.details') }}</button>
              <button type="button" class="btn" :disabled="updatingId != null" @click="toggleEnabled(user)">{{ user.enabled ? t('users.disable') : t('users.enable') }}</button>
            </div>
          </li>
        </ul>
      </template>
    </template>

    <template v-else>
      <AppStateView v-if="detailLoading" :loading="true" />
      <p v-if="detailError" class="user-usage__error" role="alert">{{ detailError }}</p>
      <p v-if="selectedUser" class="user-usage__identity">{{ selectedUser.display_name || selectedUser.username }} · {{ selectedUser.tenant_id }}</p>
      <template v-if="detail">
        <div class="user-usage__kpis">
          <StatCard :label="t('users.requests')" :value="fmtInt(detail.kpi.requests)" />
          <StatCard :label="t('users.tokens')" :value="fmtInt(detail.kpi.tokens)" />
          <StatCard :label="t('users.credits')" :value="fmtInt(detail.kpi.credits)" />
          <StatCard :label="t('users.cost')" :value="detail.kpi.cost_usd == null ? '—' : fmtUsd(detail.kpi.cost_usd)" />
          <StatCard :label="t('users.errors')" :value="fmtInt(detail.kpi.errors)" :hint="fmtNum(detail.kpi.error_rate * 100, 2) + '%'" />
          <StatCard :label="t('users.p95')" :value="detail.kpi.latency_p95_ms == null ? '—' : `${fmtInt(detail.kpi.latency_p95_ms)}ms`" />
          <StatCard :label="t('users.keys')" :value="fmtInt(detail.key_count)" />
        </div>
        <DataTableFocus :title="t('users.daily')">
          <div class="user-usage__table-scroll"><table class="table">
            <thead><tr><th>{{ t('users.date') }}</th><th>{{ t('users.requests') }}</th><th>{{ t('users.tokens') }}</th><th>{{ t('users.credits') }}</th><th>{{ t('users.cost') }}</th></tr></thead>
            <tbody><tr v-for="row in detail.daily" :key="row.date"><td>{{ fmtDate(`${row.date}T00:00:00`) }}</td><td>{{ fmtInt(row.requests) }}</td><td>{{ fmtInt(row.tokens) }}</td><td>{{ fmtInt(row.credits) }}</td><td>{{ fmtUsd(row.cost_usd) }}</td></tr></tbody>
          </table></div>
        </DataTableFocus>
        <section class="user-usage__section"><h2>{{ t('users.topModels') }}</h2><div v-for="row in detail.top_models" :key="row.name" class="user-usage__bucket"><span>{{ row.name }}</span><b>{{ fmtInt(row.requests) }} {{ t('users.requests') }} · {{ fmtUsd(row.cost_usd) }}</b></div></section>
        <section class="user-usage__section"><h2>{{ t('users.topApps') }}</h2><div v-for="row in detail.top_apps" :key="row.name" class="user-usage__bucket"><span>{{ row.name }}</span><b>{{ fmtInt(row.requests) }} {{ t('users.requests') }} · {{ fmtUsd(row.cost_usd) }}</b></div></section>
        <section class="user-usage__section"><h2>{{ t('users.topKeys') }}</h2><div v-for="row in detail.top_keys" :key="row.name" class="user-usage__bucket"><span>{{ row.name }}</span><b>{{ fmtInt(row.requests) }} {{ t('users.requests') }} · {{ fmtUsd(row.cost_usd) }}</b></div></section>
        <section class="user-usage__section"><h2>{{ t('users.recent') }}</h2><div v-for="(row, index) in detail.recent_requests" :key="`${row.ts}-${index}`" class="user-usage__bucket"><span>{{ row.model }} · {{ fmtTime(row.ts) }}</span><b>{{ row.status }}</b><RouterLink v-if="row.request_id" :to="{ name: 'request-detail', params: { id: row.request_id } }">{{ t('users.details') }}</RouterLink></div></section>
      </template>
    </template>

    <!-- 用户下创建 API Key 的抽屉（2026-10-10，第二百零一批） -->
    <AppSheet
      :model-value="createOpen"
      :title="selectedUser ? `${t('keys.create')} · ${selectedUser.username}` : t('keys.create')"
      @update:model-value="(value: boolean) => { if (!value) closeCreate() }"
    >
      <div class="user-usage__create">
        <p v-if="!createdSecret" class="user-usage__hint">{{ t('keys.createHint') }}</p>
        <p v-if="!createdSecret" class="user-usage__identity">
          {{ t('users.title') }}: <strong>{{ selectedUser?.username }}</strong> · {{ selectedUser?.tenant_id }}
        </p>
        <label v-if="!createdSecret" class="user-usage__field">
          <span>{{ t('keys.form.appName') }}</span>
          <input v-model="formAppCode" type="text" :placeholder="t('keys.form.appNamePlaceholder')" />
        </label>
        <label v-if="!createdSecret" class="user-usage__field">
          <span>{{ t('keys.form.name') }}</span>
          <input v-model="formName" type="text" :placeholder="t('keys.form.namePlaceholder')" />
        </label>
        <label v-if="!createdSecret" class="user-usage__field">
          <span>{{ t('keys.form.budget') }}</span>
          <input v-model="formBudget" type="number" min="0" step="0.01" :placeholder="t('keys.form.budgetPlaceholder')" />
        </label>
        <p v-if="createError" class="user-usage__error" role="alert">{{ createError }}</p>

        <template v-if="createdSecret">
          <p class="user-usage__hint">{{ t('keys.revealed') }}</p>
          <div class="user-usage__secret-row">
            <code class="user-usage__secret">{{ createdSecret }}</code>
            <button type="button" class="btn" @click="copySecret">{{ copied ? t('common.copied') : t('common.copy') }}</button>
          </div>
        </template>

        <div v-if="!createdSecret" class="user-usage__create-actions">
          <button type="button" class="btn" @click="closeCreate">{{ t('common.cancel') }}</button>
          <button type="button" class="btn btn--primary" :disabled="creating" @click="submitCreate">
            {{ creating ? t('common.loading') : t('keys.create') }}
          </button>
        </div>
        <div v-else class="user-usage__create-actions">
          <button type="button" class="btn btn--primary" @click="closeCreate">{{ t('common.done') }}</button>
        </div>
      </div>
    </AppSheet>
  </div>
</template>

<style scoped>
/* ⚠️ 2026-10-13 真机实测修的缺陷：横向内边距此前**完全缺失**。
   留白在本仓来自 `HyperList`（`padding: … var(--app-space-content-x) …`）
   与 `.page`，而本视图的根容器是裸 `<div class="user-usage">`，两者都没用。
   ⇒ 工具栏的「统计天数」「搜索」两个**裸标签**直接贴到屏幕边缘。
   实测（iPhone 6s 真机截图 750px@2x 逐行扫描最左非背景像素）：
     本页左边距众数 x=0 ；走 HyperList 的用量页 x=32（=1rem，全站一致）。
   ⇒ 顶部导航在 x=39、页面内容在 x=0，同一屏内两套左边距。
   ⚠️ 只补**横向**：`padding-bottom` 原来就有，别覆盖。
   ⚠️ 别改成 `.page` 类 —— 本视图不是滚动宿主，换类会引入第二个滚动根。 */
.user-usage {
  display: grid;
  gap: 12px;
  padding-inline: var(--app-space-content-x);
  padding-bottom: var(--app-safe-bottom);
}
.user-usage__toolbar { display: flex; gap: 10px; align-items: end; justify-content: space-between; }
.user-usage__toolbar label, .user-usage__search { display: grid; gap: 5px; color: var(--app-text-muted); font-size: 12px; }
.user-usage select, .user-usage input { min-height: 48px; padding: 0 12px; border: 1px solid var(--app-border); border-radius: 10px; background: var(--app-surface); color: var(--app-text); font: inherit; }
.user-usage__list { display: grid; gap: 10px; margin: 0; padding: 0; list-style: none; }
.user-usage__card, .user-usage__section, .user-usage__tenant { min-width: 0; padding: 14px; background: var(--app-surface); border: 1px solid var(--app-border); border-radius: 12px; }
.user-usage__tenant { padding: 0; overflow: hidden; }
.user-usage__tenant-head { display: flex; width: 100%; min-height: 64px; align-items: center; justify-content: space-between; gap: 8px; padding: 12px; border: 0; background: transparent; color: var(--app-text); text-align: left; }
.user-usage__tenant-head > span:first-child { display: grid; gap: 4px; }
.user-usage__tenant-head small, .user-usage__tenant-count { color: var(--app-text-muted); font-size: 12px; }
.user-usage__tenant-count b { margin-left: 8px; color: var(--app-primary); font-size: 18px; }
.user-usage__tenant-body { display: grid; gap: 12px; padding: 12px; border-top: 1px solid var(--app-border); }
.user-usage__tenant-kpis { display: grid; grid-template-columns: repeat(2,minmax(0,1fr)); gap: 8px; }
.user-usage__tenant-breakdown { display: grid; gap: 6px; }
.user-usage__tenant-breakdown h3, .user-usage__users-head h3 { margin: 0; font-size: 14px; }
.user-usage__users-head { display: flex; align-items: center; justify-content: space-between; }
.user-usage__head, .user-usage__actions, .user-usage__bucket { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.user-usage__head small, .user-usage__metrics small { display: block; color: var(--app-text-muted); }
.user-usage__metrics { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; margin: 14px 0; }
.user-usage__metrics span { min-width: 0; overflow-wrap: anywhere; }
.user-usage__metrics b { display: block; margin-top: 4px; font-variant-numeric: tabular-nums; }
.user-usage__actions { justify-content: flex-start; flex-wrap: wrap; }
.user-usage__actions .btn { min-height: 48px; }
.user-usage__error { color: var(--app-danger, #e35353); overflow-wrap: anywhere; }
.user-usage__empty, .user-usage__identity { color: var(--app-text-muted); }
.user-usage__kpis { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
.user-usage__table-scroll { overflow-x: auto; }
.user-usage__section h2 { margin: 0 0 10px; font-size: 14px; }
.user-usage__bucket { min-height: 48px; border-top: 1px solid var(--app-border); overflow-wrap: anywhere; }
.user-usage__bucket b { font-size: 0.75rem; text-align: end; }
@media (min-width: 700px) { .user-usage__list { grid-template-columns: 1fr; } .user-usage__kpis { grid-template-columns: repeat(3, minmax(0, 1fr)); } .user-usage__tenant-kpis { grid-template-columns: repeat(4,minmax(0,1fr)); } }

/* 「在用户名下添加 apikey」抽屉内排版（2026-10-10） */
.user-usage__create { display: grid; gap: 12px; }
.user-usage__hint { margin: 0; color: var(--app-text-muted); }
.user-usage__field { display: grid; gap: 6px; color: var(--app-text-muted); font-size: 12px; }
.user-usage__field input { min-height: 48px; padding: 0 12px; border: 1px solid var(--app-border); border-radius: 10px; background: var(--app-surface); color: var(--app-text); font: inherit; }
.user-usage__secret-row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.user-usage__secret { flex: 1 1 200px; min-width: 0; padding: 12px; background: var(--app-surface); border: 1px solid var(--app-border); border-radius: 10px; overflow-wrap: anywhere; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 13px; }
.user-usage__create-actions { display: flex; gap: 8px; justify-content: flex-end; flex-wrap: wrap; }
.user-usage__create-actions .btn { min-height: 48px; }
</style>
