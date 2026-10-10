<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useHyperPage } from '@/hyper'
import { fetchUserStats, fetchUserUsageSummary, type UserStatsResponse, type UserUsageRow } from '@/api/userUsageStats'
import { fetchManagedUsers, setManagedUserEnabled, type ManagedUser } from '@/api/users'
import { createKey } from '@/api/keys'
import { t } from '@/i18n'
import AppStateView from '@/components/common/AppStateView.vue'
import AppSheet from '@/components/common/AppSheet.vue'
import DataTableFocus from '@/components/common/DataTableFocus.vue'
import StatCard from '@/components/common/StatCard.vue'
import { fmtDate, fmtInt, fmtNum, fmtTime, fmtUsd } from '@/utils/format'
import { formatApiError } from '@/utils/errors'

const route = useRoute()
const router = useRouter()
const selectedId = computed(() => { const id = Number(route.params.id); return Number.isSafeInteger(id) && id > 0 ? id : 0 })
useHyperPage({ title: () => selectedId.value ? t('users.detailTitle') : t('users.title') })

const days = ref(30)
const users = ref<ManagedUser[]>([])
const usage = ref<UserUsageRow[]>([])
const detail = ref<UserStatsResponse | null>(null)
const loading = ref(true)
const updatingId = ref<number | null>(null)
const query = ref('')
const error = ref<string | null>(null)
const detailError = ref<string | null>(null)

const usageByUsername = computed(() => new Map(usage.value.map((row) => [row.username, row])))
const visibleUsers = computed(() => users.value.filter((user) => {
  const value = `${user.username} ${user.display_name} ${user.email} ${user.tenant_id}`.toLowerCase()
  return value.includes(query.value.trim().toLowerCase())
}))
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
  users.value = []
  usage.value = []
  try {
    const [managed, summary] = await Promise.all([
      fetchManagedUsers({ signal }),
      fetchUserUsageSummary({ days: days.value }, { signal }),
    ])
    if (signal.aborted || disposed) return
    users.value = managed
    usage.value = summary.items
  } catch (cause) {
    if (!signal.aborted && !disposed) error.value = formatApiError(cause)
  } finally {
    if (!signal.aborted && !disposed) loading.value = false
  }
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
  try {
    const response = await fetchUserStats({ userId: selectedId.value, days: days.value }, { signal })
    if (!signal.aborted && !disposed) detail.value = response
  } catch (cause) {
    if (!signal.aborted && !disposed) detailError.value = formatApiError(cause)
  } finally {
    if (!signal.aborted && !disposed) detailLoading.value = false
  }
}

async function toggleEnabled(user: ManagedUser): Promise<void> {
  const next = !user.enabled
  const confirmed = window.confirm(t(next ? 'users.enableConfirm' : 'users.disableConfirm', { name: user.display_name || user.username }))
  if (!confirmed || updatingId.value != null) return
  updatingId.value = user.id
  error.value = null
  try {
    await setManagedUserEnabled(user.id, next)
    await loadList()
    if (selectedId.value) await loadDetail()
  } catch (cause) {
    error.value = formatApiError(cause)
  } finally {
    updatingId.value = null
  }
}

function showDetails(user: ManagedUser): void {
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
  await loadList()
  await loadDetail()
})
watch(days, async () => {
  await loadList()
  await loadDetail()
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
        <input v-model="query" type="search" :placeholder="t('users.searchPlaceholder')" />
      </label>
      <p v-if="!visibleUsers.length" class="user-usage__empty">{{ t('common.empty') }}</p>
      <ul v-else class="user-usage__list">
        <li v-for="user in visibleUsers" :key="user.id" class="user-usage__card">
          <div class="user-usage__head">
            <div><strong>{{ user.display_name || user.username }}</strong><small>{{ user.username }} · {{ user.tenant_id }}</small></div>
            <span class="badge" :class="user.enabled ? 'badge--success' : 'badge--muted'">{{ user.enabled ? t('users.enabled') : t('users.disabled') }}</span>
          </div>
          <div class="user-usage__metrics">
            <span><small>{{ t('users.requests') }}</small><b>{{ fmtInt(usageByUsername.get(user.username)?.requests ?? 0) }}</b></span>
            <span><small>{{ t('users.tokens') }}</small><b>{{ fmtInt(usageByUsername.get(user.username)?.tokens ?? 0) }}</b></span>
            <span><small>{{ t('users.credits') }}</small><b>{{ fmtInt(usageByUsername.get(user.username)?.credits ?? 0) }}</b></span>
          </div>
          <div class="user-usage__actions">
            <button type="button" class="btn" @click="showDetails(user)">{{ t('users.details') }}</button>
            <button type="button" class="btn" :disabled="updatingId != null" @click="toggleEnabled(user)">
              {{ user.enabled ? t('users.disable') : t('users.enable') }}
            </button>
          </div>
        </li>
      </ul>
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
            <tbody><tr v-for="row in detail.daily" :key="row.date"><td>{{ fmtDate(`${row.date}T00:00:00`) }}</td><td>{{ fmtInt(row.requests) }}</td><td>{{ fmtInt(row.tokens) }}</td><td>{{ fmtInt(row.credits) }}</td><td>{{ fmtUsd(row.cost) }}</td></tr></tbody>
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
.user-usage { display: grid; gap: 12px; padding-bottom: var(--app-safe-bottom); }
.user-usage__toolbar { display: flex; gap: 10px; align-items: end; justify-content: space-between; }
.user-usage__toolbar label, .user-usage__search { display: grid; gap: 5px; color: var(--app-text-muted); font-size: 12px; }
.user-usage select, .user-usage input { min-height: 48px; padding: 0 12px; border: 1px solid var(--app-border); border-radius: 10px; background: var(--app-surface); color: var(--app-text); font: inherit; }
.user-usage__list { display: grid; gap: 10px; margin: 0; padding: 0; list-style: none; }
.user-usage__card, .user-usage__section { min-width: 0; padding: 14px; background: var(--app-surface); border: 1px solid var(--app-border); border-radius: 12px; }
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
@media (min-width: 700px) { .user-usage__list { grid-template-columns: repeat(2, minmax(0, 1fr)); } .user-usage__kpis { grid-template-columns: repeat(3, minmax(0, 1fr)); } }

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
