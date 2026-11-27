<script setup lang="ts">
// 组织管理列表页（三级配额制：租户→组织→用户，2026-11-27）。
// 与 TenantsView 同构：桌面表格 + compact 卡片双形态；余额语义
// balance/limit null = 不限制（∞），负余额红显。
import { useI18n } from 'vue-i18n'
import { surfaceError } from '../i18n/errorSurface'
import { sortByName } from '../utils/sortByName'
import { computed, ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { getOrgs, getTenantsAdmin } from '../api'
import type { Org, Tenant, WalletInfo } from '../api'
import OrgCreateDialog from './OrgCreateDialog.vue'
import PageHeader from '../components/ui/PageHeader.vue'
import ResponsiveDataView from '../components/ui/ResponsiveDataView.vue'
import type { CardField } from '../components/ui/CardList.vue'

const { t } = useI18n()
const router = useRouter()

const orgs = ref<Org[]>([])
const tenants = ref<Tenant[]>([])
const loading = ref(false)
const error = ref('')
const filterTenant = ref<string>('')
const showCreate = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    orgs.value = sortByName(await getOrgs(filterTenant.value || undefined))
  } catch (e: unknown) {
    error.value = surfaceError(e, { key: 'orgs.list.loadFailed' })
  } finally {
    loading.value = false
  }
}

async function loadTenants() {
  try {
    tenants.value = await getTenantsAdmin()
  } catch {
    tenants.value = []
  }
}

function fmtNum(n?: number) {
  if (n == null) return '-'
  return n.toLocaleString()
}

function fmtWallet(w?: WalletInfo) {
  if (!w) return '-'
  const bal = w.balance_credits == null ? 0 : w.balance_credits
  const limit = w.credit_limit_credits == null ? '∞' : w.credit_limit_credits.toLocaleString()
  return `${bal.toLocaleString()} / ${limit}`
}

function walletCellClass(w?: WalletInfo) {
  if (!w || w.balance_credits == null) return ''
  return w.balance_credits < 0 ? 'wallet-negative' : ''
}

const TABLE_MIN_WIDTH = '820px'

function goDetail(o: Org) {
  router.push(`/orgs/${o.id}`)
}

const cardFields = computed<CardField[]>(() => [
  { key: 'tenant_id', label: t('orgs.list.colTenant'), format: (v) => (v == null ? null : String(v)) },
  {
    key: 'wallet',
    label: t('tenants.list.colBalance'),
    type: 'metric',
    align: 'end',
    format: (v) => fmtWallet(v as WalletInfo | undefined),
  },
  {
    key: 'member_count',
    label: t('orgs.list.colMembers'),
    type: 'metric',
    align: 'end',
    format: (v) => fmtNum(v as number | undefined),
  },
  {
    key: 'enabled',
    label: t('common.terms.status'),
    type: 'badge',
    format: (v) => (v ? t('common.status.enabled') : t('common.status.disabled')),
  },
])

const cardSubtitleKeys = ['code']

function onCardClick(o: Org) {
  goDetail(o)
}

onMounted(() => { load(); loadTenants() })
</script>

<template>
  <div class="orgs-page">
    <PageHeader :title="t('orgs.list.title')">
      <template #actions>
        <button class="btn btn-primary" @click="showCreate = true">{{ t('orgs.list.createBtn') }}</button>
      </template>
    </PageHeader>

    <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>

    <div class="filters">
      <label>{{ t('orgs.list.tenantFilter') }}:</label>
      <select v-model="filterTenant" @change="load">
        <option value="">{{ t('tenants.list.allStatuses') }}</option>
        <option v-for="tt in tenants" :key="tt.code" :value="tt.code">{{ tt.name }}</option>
      </select>
    </div>

    <div v-if="loading" class="loading">{{ t('tenants.list.loading') }}</div>

    <ResponsiveDataView
      v-else
      :rows="orgs"
      title-key="name"
      :subtitle-keys="cardSubtitleKeys"
      :fields="cardFields"
      :table-min-width="TABLE_MIN_WIDTH"
      :clickable="true"
      :clickable-label="t('orgs.list.colName')"
      :empty="orgs.length === 0"
      :empty-text="t('orgs.list.empty')"
      @row-click="onCardClick"
    >
      <template #table>
        <table class="table orgs-table" style="width:100%">
          <thead>
            <tr>
              <th>{{ t('orgs.list.colName') }}</th>
              <th>{{ t('orgs.list.colCode') }}</th>
              <th>{{ t('orgs.list.colTenant') }}</th>
              <th>{{ t('tenants.list.colBalance') }}</th>
              <th>{{ t('orgs.list.colMembers') }}</th>
              <th>{{ t('common.terms.status') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="o in orgs"
              :key="o.id"
              class="org-row"
              tabindex="0"
              @click="goDetail(o)"
              @keydown.enter="goDetail(o)"
            >
              <td><strong>{{ o.name }}</strong></td>
              <td><code>{{ o.code }}</code></td>
              <td><span class="badge badge-blue">{{ o.tenant_id }}</span></td>
              <td><span class="mono" :class="walletCellClass(o.wallet)">{{ fmtWallet(o.wallet) }}</span></td>
              <td>{{ fmtNum(o.member_count) }}</td>
              <td><span class="badge" :class="o.enabled ? 'badge-green' : 'badge-red'">{{ o.enabled ? $t('common.status.enabled') : $t('common.status.disabled') }}</span></td>
            </tr>
            <tr v-if="orgs.length === 0">
              <td colspan="6" style="text-align:center; padding:40px; color: var(--muted)">{{ t('orgs.list.empty') }}</td>
            </tr>
          </tbody>
        </table>
      </template>
    </ResponsiveDataView>

    <OrgCreateDialog v-if="showCreate" @close="showCreate = false" @created="load" />
  </div>
</template>

<style scoped>
.orgs-page { padding: 20px; }
.filters { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; }
.orgs-table .org-row { cursor: pointer; }
.orgs-table .org-row:hover { background: color-mix(in srgb, var(--accent) 6%, transparent); }
.orgs-table .org-row:focus-visible { outline: 2px solid var(--accent-h); outline-offset: -2px; }
.badge-purple { background: color-mix(in srgb, var(--accent) 15%, transparent); color: var(--accent-h); }
.badge-blue { background: var(--info-bg); color: var(--accent); }
.badge-red { background: var(--danger-bg); color: var(--danger); }
.badge-green { background: var(--success-bg); color: var(--success); }
.mono { font-family: 'SF Mono', 'Fira Code', monospace; font-size: 12px; }
.wallet-negative { color: var(--danger); font-weight: 600; }
.loading { text-align: center; padding: 40px; color: var(--muted); }
</style>
