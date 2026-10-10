<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useHyperPage } from '@/hyper'
import { fetchUserStats, fetchUserUsageSummary, type UserStatsResponse, type UserUsageRow } from '@/api/userUsageStats'
import { fetchManagedUsers, setManagedUserEnabled, type ManagedUser } from '@/api/users'
import { t } from '@/i18n'
import AppStateView from '@/components/common/AppStateView.vue'
import DataTableFocus from '@/components/common/DataTableFocus.vue'
import StatCard from '@/components/common/StatCard.vue'
import { fmtDate, fmtInt, fmtNum, fmtTime, fmtUsd } from '@/utils/format'
import { formatApiError } from '@/utils/errors'

const route = useRoute()
const router = useRouter()
const selectedId = computed(() => Number(route.params.id) || 0)
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
const totalCost = computed(() => detail.value?.daily.reduce((sum, row) => sum + row.cost, 0) ?? 0)

async function loadList(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    const [managed, summary] = await Promise.all([
      fetchManagedUsers(),
      fetchUserUsageSummary({ days: days.value }),
    ])
    users.value = managed
    usage.value = summary.items
  } catch (cause) {
    error.value = formatApiError(cause)
  } finally {
    loading.value = false
  }
}

async function loadDetail(): Promise<void> {
  detailError.value = null
  detail.value = null
  if (!selectedId.value) return
  try {
    detail.value = await fetchUserStats({ userId: selectedId.value, days: days.value })
  } catch (cause) {
    detailError.value = formatApiError(cause)
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

onMounted(async () => {
  await loadList()
  await loadDetail()
})
watch(days, async () => {
  await loadList()
  await loadDetail()
})
watch(selectedId, () => void loadDetail())
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
            <button type="button" class="btn" :disabled="updatingId === user.id" @click="toggleEnabled(user)">
              {{ user.enabled ? t('users.disable') : t('users.enable') }}
            </button>
          </div>
        </li>
      </ul>
    </template>

    <template v-else>
      <p v-if="detailError" class="user-usage__error" role="alert">{{ detailError }}</p>
      <p v-if="selectedUser" class="user-usage__identity">{{ selectedUser.display_name || selectedUser.username }} · {{ selectedUser.tenant_id }}</p>
      <template v-if="detail">
        <div class="user-usage__kpis">
          <StatCard :label="t('users.requests')" :value="fmtInt(detail.kpi.requests)" />
          <StatCard :label="t('users.tokens')" :value="fmtInt(detail.kpi.tokens)" />
          <StatCard :label="t('users.credits')" :value="fmtInt(detail.kpi.credits)" />
          <StatCard :label="t('users.cost')" :value="fmtUsd(totalCost)" />
          <StatCard :label="t('users.errors')" :value="fmtInt(detail.kpi.errors)" :hint="fmtNum(detail.kpi.error_rate, 2) + '%'" />
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
        <section class="user-usage__section"><h2>{{ t('users.recent') }}</h2><div v-for="(row, index) in detail.recent_requests" :key="`${row.ts}-${index}`" class="user-usage__bucket"><span>{{ row.model }} · {{ fmtTime(row.ts) }}</span><b>{{ row.status }}</b></div></section>
      </template>
    </template>
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
</style>
