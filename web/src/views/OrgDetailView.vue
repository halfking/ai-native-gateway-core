<script setup lang="ts">
// 组织详情页（三级配额制，2026-11-27）：钱包信息卡 + 调整表单 + 成员名单
// （成员行带用户钱包与组织钱包两列余额）。与 TenantDetailView 同构。
import { useI18n } from 'vue-i18n'
import { surfaceError } from '../i18n/errorSurface'
import { computed, ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getOrg, getOrgMembers, updateOrgWallet, deleteOrg, updateOrg } from '../api'
import type { Org, OrgMember, WalletInfo } from '../api'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const org = ref<Org | null>(null)
const members = ref<OrgMember[]>([])
const loading = ref(false)
const error = ref('')

// 钱包调整表单
const editBalance = ref<number | null>(null)
const editLimit = ref<string>('') // '' = 不改；'none' = 设为不限；数字串 = 设值
const editPolicy = ref<string>('')
const savingWallet = ref(false)
const walletMsg = ref('')

const orgId = computed(() => Number(route.params.orgId))

async function load() {
  loading.value = true
  error.value = ''
  try {
    const data = await getOrg(orgId.value)
    org.value = data.org
    members.value = data.members || []
    if (org.value?.wallet) {
      editBalance.value = org.value.wallet.balance_credits
      editLimit.value = org.value.wallet.credit_limit_credits == null
        ? 'none'
        : String(org.value.wallet.credit_limit_credits)
      editPolicy.value = org.value.wallet.credit_policy
    }
  } catch (e: unknown) {
    error.value = surfaceError(e, { key: 'orgs.detail.loadFailed' })
  } finally {
    loading.value = false
  }
}

async function saveWallet() {
  if (!org.value) return
  savingWallet.value = true
  walletMsg.value = ''
  try {
    const body: {
      balance_credits?: number
      credit_limit_credits?: number | null
      credit_policy?: 'warn' | 'block'
    } = {}
    if (editBalance.value != null) body.balance_credits = editBalance.value
    if (editLimit.value === 'none') body.credit_limit_credits = null
    else if (editLimit.value !== '') body.credit_limit_credits = Number(editLimit.value)
    if (editPolicy.value === 'warn' || editPolicy.value === 'block') body.credit_policy = editPolicy.value
    const r = await updateOrgWallet(org.value.id, body)
    if (org.value) org.value.wallet = r.wallet
    walletMsg.value = t('orgs.detail.walletSaved')
  } catch (e: unknown) {
    walletMsg.value = surfaceError(e, { key: 'orgs.detail.walletSaveFailed' })
  } finally {
    savingWallet.value = false
  }
}

async function toggleEnabled() {
  if (!org.value) return
  try {
    const r = await updateOrg(org.value.id, { enabled: !org.value.enabled })
    org.value = r.org
  } catch (e: unknown) {
    error.value = surfaceError(e, { key: 'orgs.detail.updateFailed' })
  }
}

async function removeOrg() {
  if (!org.value) return
  if (!confirm(t('orgs.detail.deleteConfirm'))) return
  try {
    await deleteOrg(org.value.id)
    router.push('/orgs')
  } catch (e: unknown) {
    error.value = surfaceError(e, { key: 'orgs.detail.deleteFailed' })
  }
}

function fmtWallet(w?: WalletInfo | null) {
  if (!w) return '∞'
  const bal = w.balance_credits == null ? 0 : w.balance_credits
  const limit = w.credit_limit_credits == null ? '∞' : w.credit_limit_credits.toLocaleString()
  return `${bal.toLocaleString()} / ${limit}`
}

function walletClass(w?: WalletInfo | null) {
  if (!w || w.balance_credits == null) return ''
  return w.balance_credits < 0 ? 'wallet-negative' : ''
}

const sortedMembers = computed(() =>
  [...members.value].sort((a, b) => a.id - b.id),
)

onMounted(load)
</script>

<template>
  <div class="org-detail-page">
    <div class="page-header-row">
      <button class="btn btn-ghost btn-sm" @click="router.push('/orgs')">← {{ t('orgs.detail.back') }}</button>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="loading" class="loading">{{ t('tenants.list.loading') }}</div>

    <template v-if="org">
      <div class="detail-header">
        <h2>
          {{ org.name }}
          <code class="org-code">{{ org.code }}</code>
          <span class="badge" :class="org.enabled ? 'badge-green' : 'badge-red'">
            {{ org.enabled ? $t('common.status.enabled') : $t('common.status.disabled') }}
          </span>
        </h2>
        <div class="header-actions">
          <button class="btn btn-ghost btn-sm" @click="toggleEnabled">
            {{ org.enabled ? $t('common.status.disabled') : $t('common.status.enabled') }}
          </button>
          <button class="btn btn-ghost btn-sm btn-danger-ghost" @click="removeOrg">{{ t('orgs.detail.deleteBtn') }}</button>
        </div>
      </div>
      <p class="org-meta">{{ org.tenant_id }} <span v-if="org.description">· {{ org.description }}</span></p>

      <!-- 钱包卡 -->
      <div class="wallet-card">
        <h3>{{ t('orgs.detail.walletTitle') }}</h3>
        <div class="wallet-summary">
          <span class="mono" :class="walletClass(org.wallet)">{{ fmtWallet(org.wallet) }}</span>
          <span class="badge badge-blue">{{ org.wallet?.credit_policy || 'warn' }}</span>
        </div>
        <form class="wallet-form" @submit.prevent="saveWallet">
          <label>
            {{ t('orgs.detail.balanceLabel') }}
            <input v-model.number="editBalance" type="number" step="1" />
          </label>
          <label>
            {{ t('orgs.detail.limitLabel') }}
            <select v-model="editLimit">
              <option value="none">{{ t('orgs.detail.limitUnlimited') }}</option>
              <option value="" disabled hidden></option>
            </select>
            <input v-if="editLimit !== 'none'" v-model="editLimit" type="number" step="1" />
          </label>
          <label>
            {{ t('orgs.detail.policyLabel') }}
            <select v-model="editPolicy">
              <option value="warn">warn</option>
              <option value="block">block</option>
            </select>
          </label>
          <button type="submit" class="btn btn-primary btn-sm" :disabled="savingWallet">
            {{ savingWallet ? t('orgs.detail.saving') : t('orgs.detail.save') }}
          </button>
          <span v-if="walletMsg" class="wallet-msg">{{ walletMsg }}</span>
        </form>
        <p class="wallet-hint">{{ t('orgs.detail.walletHint') }}</p>
      </div>

      <!-- 成员表 -->
      <h3 class="members-title">{{ t('orgs.detail.membersTitle') }} ({{ members.length }})</h3>
      <table class="table" style="width:100%">
        <thead>
          <tr>
            <th>ID</th>
            <th>{{ $t('common.terms.username') }}</th>
            <th>{{ $t('common.terms.displayName') }}</th>
            <th>{{ $t('tenants.detail.userBalanceCol') }}</th>
            <th>{{ $t('tenants.detail.orgBalanceCol') }}</th>
            <th>{{ $t('common.terms.role') }}</th>
            <th>{{ $t('common.terms.status') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="m in sortedMembers" :key="m.id">
            <td>{{ m.id }}</td>
            <td><strong>{{ m.username }}</strong></td>
            <td>{{ m.display_name || '-' }}</td>
            <td><span class="mono" :class="{ 'wallet-negative': (m.user_wallet?.balance_credits ?? 0) < 0 }">{{ fmtWallet(m.user_wallet) }}</span></td>
            <td><span class="mono" :class="{ 'wallet-negative': (m.org_wallet?.balance_credits ?? 0) < 0 }">{{ fmtWallet(m.org_wallet) }}</span></td>
            <td><span class="badge badge-blue">{{ m.role }}</span></td>
            <td><span class="badge" :class="m.enabled ? 'badge-green' : 'badge-red'">{{ m.enabled ? $t('common.status.enabled') : $t('common.status.disabled') }}</span></td>
          </tr>
          <tr v-if="members.length === 0">
            <td colspan="7" style="text-align:center; padding:40px; color: var(--muted)">{{ t('orgs.detail.noMembers') }}</td>
          </tr>
        </tbody>
      </table>
    </template>
  </div>
</template>

<style scoped>
.org-detail-page { padding: 20px; }
.page-header-row { margin-bottom: 12px; }
.detail-header { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.detail-header h2 { display: flex; align-items: center; gap: 8px; margin: 0; }
.org-code { font-size: 13px; color: var(--muted); }
.header-actions { display: flex; gap: 8px; }
.btn-danger-ghost { color: var(--danger); }
.org-meta { color: var(--muted); font-size: 13px; margin: 6px 0 16px; }
.wallet-card {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;
  margin-bottom: 20px;
}
.wallet-summary { display: flex; align-items: center; gap: 10px; margin: 8px 0 12px; font-size: 15px; }
.wallet-form { display: flex; flex-wrap: wrap; gap: 12px; align-items: end; }
.wallet-form label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; color: var(--muted); }
.wallet-form input, .wallet-form select {
  padding: 6px 8px; border: 1px solid var(--border); border-radius: 6px;
  font-size: 13px; background: var(--bg); color: var(--text); min-width: 140px;
}
.wallet-msg { font-size: 12px; color: var(--success); }
.wallet-hint { color: var(--muted); font-size: 12px; margin-top: 10px; }
.members-title { margin: 8px 0 10px; }
.badge-purple { background: color-mix(in srgb, var(--accent) 15%, transparent); color: var(--accent-h); }
.badge-blue { background: var(--info-bg); color: var(--accent); }
.badge-red { background: var(--danger-bg); color: var(--danger); }
.badge-green { background: var(--success-bg); color: var(--success); }
.mono { font-family: 'SF Mono', 'Fira Code', monospace; font-size: 12px; }
.wallet-negative { color: var(--danger); font-weight: 600; }
.loading { text-align: center; padding: 40px; color: var(--muted); }
.alert-danger { background: var(--danger-bg); color: var(--danger); padding: 8px 12px; border-radius: 6px; margin-bottom: 12px; font-size: 13px; }
</style>
