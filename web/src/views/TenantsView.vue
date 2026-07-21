<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { localeRef } from '../i18n'
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { getTenantsAdmin, TENANT_STATUSES, TENANT_STATUS_COLORS } from '../api'
import type { Tenant } from '../api'
import TenantCreateDialog from './TenantCreateDialog.vue'
import FeeCostCell from '../components/FeeCostCell.vue'
import { isPlatformOpsView } from '../store'
import { useTenantStatusLabel } from '../composables/useTenantStatusLabel'

const { t } = useI18n()
const { tenantStatusLabel } = useTenantStatusLabel()

const router = useRouter()
const tenants = ref<Tenant[]>([])
const loading = ref(false)
const error = ref('')
const filterStatus = ref<string>('')
const showCreate = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    tenants.value = await getTenantsAdmin(filterStatus.value || undefined)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('tenants.list.loadFailed')
  } finally {
    loading.value = false
  }
}

function statusColor(s: string) {
  return TENANT_STATUS_COLORS[s] || 'badge-gray'
}

function statusLabel(s: string) {
  return tenantStatusLabel(s)
}

function fmtTime(s: string) {
  if (!s) return '-'
  return new Date(s).toLocaleString(localeRef.value)
}

function fmtNum(n?: number) {
  if (n == null) return '-'
  return n.toLocaleString()
}

const showCost = isPlatformOpsView()

function goDetail(t: Tenant) {
  router.push(`/tenants/${t.code}`)
}

onMounted(load)
</script>

<template>
  <div class="tenants-page">
    <div class="page-header">
      <h1>{{ t('tenants.list.title') }}</h1>
      <button class="btn btn-primary" @click="showCreate = true">{{ t('tenants.list.createBtn') }}</button>
    </div>

    <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>

    <div class="filters">
      <label>{{ t('tenants.list.statusLabel') }}:</label>
      <select v-model="filterStatus" @change="load">
        <option value="">{{ t('tenants.list.allStatuses') }}</option>
        <option v-for="s in TENANT_STATUSES" :key="s" :value="s">{{ statusLabel(s) }}</option>
      </select>
    </div>

    <div v-if="loading" class="loading">{{ t('tenants.list.loading') }}</div>

    <table v-else class="table tenants-table" style="width:100%">
      <thead>
        <tr>
          <th>{{ t('tenants.list.colName') }}</th>
          <th>{{ t('tenants.list.colCode') }}</th>
          <th>{{ t('tenants.list.colStatus') }}</th>
          <th>{{ t('tenants.list.colUsers') }}</th>
          <th>{{ t('tenants.list.colKeys') }}</th>
          <th>{{ t('tenants.list.colCost7d') }}</th>
          <th>{{ t('tenants.list.colRequests') }}</th>
          <th>{{ t('tenants.list.colContact') }}</th>
          <th>{{ t('tenants.list.colCreated') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="tenant in tenants"
          :key="tenant.code"
          class="tenant-row"
          tabindex="0"
          @click="goDetail(tenant)"
          @keydown.enter="goDetail(tenant)"
        >
          <td><strong>{{ tenant.name }}</strong></td>
          <td><code>{{ tenant.code }}</code></td>
          <td><span class="badge" :class="statusColor(tenant.status)">{{ statusLabel(tenant.status) }}</span></td>
          <td>{{ fmtNum(tenant.user_count) }}</td>
          <td>{{ fmtNum(tenant.api_key_count) }}</td>
          <td>
            <FeeCostCell
              :credits="tenant.credits_7d"
              :cost-usd="tenant.cost_7d_usd"
              :show-cost="showCost"
            />
          </td>
          <td>{{ fmtNum(tenant.total_requests) }}</td>
          <td>{{ tenant.contact_email || '-' }}</td>
          <td class="mono">{{ fmtTime(tenant.created_at) }}</td>
        </tr>
        <tr v-if="tenants.length === 0">
          <td colspan="9" style="text-align:center; color: var(--muted); padding: 40px">{{ t('tenants.list.empty') }}</td>
        </tr>
      </tbody>
    </table>

    <TenantCreateDialog v-if="showCreate" @close="showCreate = false" @created="load" />
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
}
.page-header h1 { font-size: 20px; margin: 0; }
.filters {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}
.filters label { font-size: 13px; color: var(--muted); }
.filters select {
  padding: 4px 8px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  font-size: 13px;
  width: auto;
  max-width: 200px;
}
.tenants-table .tenant-row {
  cursor: pointer;
}
.tenants-table .tenant-row:hover {
  background: color-mix(in srgb, var(--accent) 6%, transparent);
}
.tenants-table .tenant-row:focus-visible {
  outline: 2px solid var(--accent-h);
  outline-offset: -2px;
}
.badge-purple { background: color-mix(in srgb, var(--accent) 15%, transparent); color: var(--accent-h); }
.badge-blue { background: rgba(59,130,246,.15); color: #60a5fa; }
.badge-red { background: rgba(239,68,68,.15); color: #f87171; }
.badge-green { background: rgba(34,197,94,.15); color: #4ade80; }
.badge-yellow { background: rgba(234,179,8,.15); color: #fbbf24; }
.badge-gray { background: rgba(156,163,175,.15); color: #9ca3af; }
.mono { font-family: 'SF Mono', 'Fira Code', monospace; font-size: 12px; }
.loading {
  text-align: center;
  padding: 40px;
  color: var(--muted);
}
</style>
