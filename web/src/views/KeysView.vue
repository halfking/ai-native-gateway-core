<script setup lang="ts">
import { computed, ref, onBeforeUnmount, onMounted, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { getKeys, createKey, revokeKey, revealKey, approveKey, disableKey, enableKey, patchKeyProfile, getDefaultLimits, setDefaultLimits, getKeyConflict, type ApiKey, type KeyCreatedResponse, type DefaultLimits, type KeyConflict } from '../api'
import { store, clearApiKey, setApiKey, setPreferredChatKeyId, isSuperAdmin, isDefaultTenant, getCurrentTenantId } from '../store'
import FilterInput from '../components/FilterInput.vue'

const router = useRouter()
const route = useRoute()

const redirectAfter = computed(() => {
  const r = route.query.redirect
  return typeof r === 'string' && r.startsWith('/') ? r : ''
})

const keys = ref<ApiKey[]>([])
const selectedKey = ref<ApiKey | null>(null)
const editForm = ref({ profile: '', ownerUser: '', keyAlias: '' })
const profileSaving = ref(false)
const loading = ref(false)
const error = ref('')
const activeTab = ref<'all' | 'active' | 'pending' | 'closed'>('active')
const filterApp = ref('')
const filterProfile = ref('')
const filterOwner = ref('')
const filterTenant = ref('')

function isExpired(k: ApiKey): boolean {
  if (!k.expires_at) return false
  return new Date(k.expires_at).getTime() <= Date.now()
}

function keyState(k: ApiKey): 'active' | 'pending' | 'closed' {
  if (k.status === 'pending') return 'pending'
  if (k.status === 'active' && !isExpired(k) && k.enabled) return 'active'
  return 'closed'
}

function keyStateLabel(k: ApiKey): string {
  if (k.status === 'pending') return '待审批'
  if (k.status === 'active' && !isExpired(k) && k.enabled) return '正常'
  return isExpired(k) ? '已过期' : '已作废'
}

function keyStateBadgeClass(k: ApiKey): string {
  const state = keyState(k)
  if (state === 'active') return 'badge-green'
  if (state === 'pending') return 'badge-yellow'
  return 'badge-red'
}

const statusTabs = computed(() => {
  const summary = { all: 0, active: 0, pending: 0, closed: 0 }
  for (const k of keys.value) {
    summary.all += 1
    summary[keyState(k)] += 1
  }
  return [
    { value: 'all' as const, label: '全部', count: summary.all },
    { value: 'active' as const, label: '可用', count: summary.active },
    { value: 'pending' as const, label: '待审', count: summary.pending },
    { value: 'closed' as const, label: '已作废/过期', count: summary.closed },
  ]
})

const filteredKeys = computed(() => {
  let list = keys.value
  if (activeTab.value !== 'all') {
    list = list.filter((k) => keyState(k) === activeTab.value)
  }
  if (filterApp.value) {
    list = list.filter((k) => k.application_code === filterApp.value)
  }
  if (filterProfile.value) {
    list = list.filter((k) => (k.default_client_profile || '') === filterProfile.value)
  }
  if (filterOwner.value) {
    const q = filterOwner.value.trim().toLowerCase()
    list = list.filter((k) => (k.owner_user || '').toLowerCase().includes(q))
  }
  if (filterTenant.value) {
    list = list.filter((k) => k.tenant_id === filterTenant.value)
  }
  return list
})

const uniqueApps = computed(() => {
  const s = new Set(keys.value.map((k) => k.application_code))
  return [...s].sort()
})

const uniqueProfiles = computed(() => {
  const s = new Set(keys.value.map((k) => k.default_client_profile || ''))
  return [...s].filter(Boolean).sort()
})

const uniqueOwners = computed(() => {
  const s = new Set(keys.value.map((k) => k.owner_user || ''))
  return [...s].filter(Boolean).sort()
})

const uniqueTenants = computed(() => {
  const s = new Set(keys.value.map((k) => k.tenant_id))
  return [...s].sort()
})

// New key modal
const showNew = ref(false)
const newApp = ref('')
const newTenant = ref('')
const newKeyAlias = ref('')
const newOwner = ref('')
const newBudget = ref('')
const newRpm = ref('')
const newRemark = ref('')
const newSaving = ref(false)

// Live conflict detection: mirrors the server-side guard so users see
// "this (tenant, application, alias) group already has a valid key" before
// submitting.  Two layers, front-end first for instant feedback, then
// confirmed by GET /api/keys/lookup (adminMiddleware-protected) on debounced
// input.  A conflict is defined as: an existing api_keys row with the same
// (tenant_id, application_code, key_alias) tuple that is either
// status=active+enabled+non-expired OR is_system=true.  This matches
// admin.findActiveKeyConflict on the Go side.
const newConflictLocal = computed<{ id: number; prefix: string; isSystem: boolean } | null>(() => {
  const app = newApp.value.trim()
  const tenant = newTenant.value.trim() || 'default'
  const alias = newKeyAlias.value.trim()
  if (!app || !alias) return null
  const hit = keys.value.find((k) =>
    k.application_code === app &&
    k.tenant_id === tenant &&
    (k.key_alias || '').trim() === alias &&
    ((k as any).is_system === true ||
      (k.status === 'active' && !isExpired(k) && k.enabled))
  )
  if (!hit) return null
  return { id: hit.id, prefix: hit.key_prefix, isSystem: (hit as any).is_system === true }
})

// Server-confirmed conflict: hit the live /api/keys/lookup endpoint so the
// UI is honest even if the cached getKeys() list is stale (e.g. another
// admin just created a key in the same group).  Debounced 350ms.
const serverConflict = ref<KeyConflict | null>(null)
const serverConflictLoading = ref(false)
let serverConflictReq: { cancelled: boolean } = { cancelled: true }
let serverConflictTimer: number | undefined
const newConflict = computed<{ id: number; prefix: string; isSystem: boolean; status?: string; expiresAt?: string | null; ownerUser?: string } | null>(() => {
  // Server wins: it has the freshest view of the DB.
  if (serverConflict.value) {
    return {
      id: serverConflict.value.id,
      prefix: serverConflict.value.key_prefix,
      isSystem: serverConflict.value.is_system,
      status: serverConflict.value.status,
      expiresAt: serverConflict.value.expires_at,
      ownerUser: serverConflict.value.owner_user,
    }
  }
  return newConflictLocal.value
})

function scheduleServerLookup() {
  if (serverConflictTimer) window.clearTimeout(serverConflictTimer)
  serverConflictTimer = window.setTimeout(runServerLookup, 350)
}

async function runServerLookup() {
  // Cancel any in-flight request from a previous keystroke.
  serverConflictReq.cancelled = true
  const myReq = { cancelled: false }
  serverConflictReq = myReq

  const app = newApp.value.trim()
  const tenant = newTenant.value.trim() || 'default'
  const alias = newKeyAlias.value.trim()
  if (!app || !alias) {
    serverConflict.value = null
    return
  }
  serverConflictLoading.value = true
  try {
    const resp = await getKeyConflict({ application_code: app, tenant_id: tenant, key_alias: alias })
    if (myReq.cancelled) return
    serverConflict.value = resp.conflict
  } catch {
    // Network/permission error: silently fall back to local heuristic.
    // The submit-time 409 from the backend is the real safety net.
    if (myReq.cancelled) return
    serverConflict.value = null
  } finally {
    if (!myReq.cancelled) serverConflictLoading.value = false
  }
}

watch(
  () => [newApp.value, newTenant.value, newKeyAlias.value],
  () => {
    // Don't eagerly null-out the last server result.  Keep it visible
    // while the next request is in-flight so the user never sees a
    // false-negative "no conflict" flash between keystrokes.
    scheduleServerLookup()
  },
)
const newErr = ref('')
const createdKey = ref<KeyCreatedResponse | null>(null)

// Default limits config
const defaultLimits = ref<DefaultLimits>({ rate_limit_rpm: 12, rate_limit_concurrent: 6, rate_limit_tpm: null })
const showDefaultLimits = ref(false)
const limitsSaving = ref(false)
const limitsErr = ref('')
const limitsSuccess = ref('')

// Copy feedback
const copiedId = ref<string | null>(null)
const copyNotice = ref('')
let copyNoticeTimer: number | undefined

function openKeyDrawer(k: ApiKey) {
  selectedKey.value = k
  editForm.value = {
    profile: k.default_client_profile || '',
    ownerUser: k.owner_user || '',
    keyAlias: k.key_alias || '',
  }
}

function closeKeyDrawer() {
  selectedKey.value = null
}

async function saveKeyProfile() {
  if (!selectedKey.value) return
  profileSaving.value = true
  try {
    const updates: Record<string, string> = {}
    updates.default_client_profile = editForm.value.profile.trim()
    updates.owner_user = editForm.value.ownerUser.trim()
    updates.key_alias = editForm.value.keyAlias.trim()
    await patchKeyProfile(selectedKey.value.id, updates)
    closeKeyDrawer()
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '保存失败'
  } finally {
    profileSaving.value = false
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    keys.value = await getKeys()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

function openNew() {
  newApp.value = 'default'
  // For non-default tenants, default to current tenant and disable selection
  const tenantId = getCurrentTenantId()
  if (isDefaultTenant()) {
    newTenant.value = 'default'
  } else {
    newTenant.value = tenantId
  }
  newKeyAlias.value = ''
  newOwner.value = ''
  newBudget.value = ''
  newRpm.value = ''
  newRemark.value = ''
  newErr.value = ''
  createdKey.value = null
  serverConflict.value = null
  showNew.value = true
  // First server lookup happens after the user types an alias; until then
  // the local heuristic covers the "default/default" placeholder.
}

async function submitNew() {
  if (!newApp.value) { newErr.value = '请填写应用代码'; return }
  if (!newKeyAlias.value.trim()) { newErr.value = '请填写密钥别名'; return }
  if (newConflict.value) {
    newErr.value = newConflict.value.isSystem
      ? `系统密钥 #${newConflict.value.id} (${newConflict.value.prefix}****) 已占用该租户+应用+别名，请先禁用或吊销后再签发。`
      : `密钥 #${newConflict.value.id} (${newConflict.value.prefix}****) 已占用该租户+应用+别名，请先禁用或吊销后再签发。`
    return
  }
  newSaving.value = true
  newErr.value = ''
  try {
    const resp = await createKey({
      application_code: newApp.value,
      tenant_id: newTenant.value || undefined,
      key_alias: newKeyAlias.value.trim(),
      owner_user: newOwner.value || undefined,
      budget_usd: newBudget.value ? Number(newBudget.value) : undefined,
      rate_limit_rpm: newRpm.value ? Number(newRpm.value) : undefined,
      remark: newRemark.value || undefined,
    })
    createdKey.value = resp
    await load()
  } catch (e: unknown) {
    newErr.value = e instanceof Error ? e.message : '创建失败'
  } finally {
    newSaving.value = false
  }
}

async function revoke(k: ApiKey) {
  if (!confirm(`确认吊销密钥 ${k.key_prefix}***？此操作不可撤销。`)) return
  try {
    await revokeKey(k.id)
    keys.value = keys.value.filter(x => x.id !== k.id)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '吊销失败'
  }
}

function fmtDate(s: string | null | undefined) {
  if (!s) return '—'
  return new Date(s).toLocaleString('zh-CN', { dateStyle: 'short', timeStyle: 'short' })
}

function fmtCost(n: number | string | null | undefined): string {
  if (n == null) return '—'
  return '$' + Number(n).toFixed(2)
}

function formatTokens(n: number | string | null | undefined): string {
  if (n == null) return '0'
  const v = Number(n)
  if (v >= 1_000_000) return (v / 1_000_000).toFixed(1) + 'M'
  if (v >= 1_000) return (v / 1_000).toFixed(1) + 'k'
  return String(v)
}

function formatRequests(n: number | string | null | undefined): string {
  if (n == null) return '0'
  const v = Number(n)
  if (v >= 1_000_000) return (v / 1_000_000).toFixed(1) + 'M'
  if (v >= 1_000) return (v / 1_000).toFixed(1) + 'k'
  return String(v)
}

async function copyText(val: string): Promise<void> {
  if (navigator.clipboard?.writeText && window.isSecureContext) {
    await navigator.clipboard.writeText(val)
    return
  }

  const textarea = document.createElement('textarea')
  textarea.value = val
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.left = '-9999px'
  textarea.style.top = '0'
  document.body.appendChild(textarea)
  textarea.focus()
  textarea.select()
  const ok = document.execCommand('copy')
  document.body.removeChild(textarea)
  if (!ok) throw new Error('copy failed')
}

// Copy a row key — fetches full decrypted key from backend first
async function copyKey(k: ApiKey, id: string) {
  try {
    let val: string
    if (k.key_prefix) {
      const result = await revealKey(k.id)
      val = result.api_key
    } else {
      val = k.key_prefix
    }
    await copyText(val)
    copiedId.value = id
    copyNotice.value = '已复制完整密钥'
  } catch (e) {
    // 409 means key was created before encrypted storage feature - can't reveal retroactively
    const msg = e instanceof Error ? e.message : String(e)
    if (msg.includes('No encrypted key stored') || msg.includes('409')) {
      copyNotice.value = '此密钥不支持复制完整内容，请重新签发密钥'
    } else {
      copyNotice.value = msg || '复制失败'
    }
  }
  if (copyNoticeTimer) window.clearTimeout(copyNoticeTimer)
  copyNoticeTimer = window.setTimeout(() => {
    copiedId.value = null
    copyNotice.value = ''
  }, 4000)
}

async function copyCreatedKey(id: string) {
  const val = createdKey.value?.api_key
  try {
    if (!val) throw new Error('后端未返回有效密钥')
    await copyText(val)
    copiedId.value = id
    copyNotice.value = '已复制完整密钥'
  } catch (e) {
    copyNotice.value = e instanceof Error ? e.message : '复制失败，请手动复制'
  }
  if (copyNoticeTimer) window.clearTimeout(copyNoticeTimer)
  copyNoticeTimer = window.setTimeout(() => {
    copiedId.value = null
    copyNotice.value = ''
  }, 2500)
}

async function approveSelected() {
  const k = selectedKey.value
  if (!k) return
  try {
    await approveKey(k.id)
    closeKeyDrawer()
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '审批失败'
  }
}

async function disableSelected() {
  const k = selectedKey.value
  if (!k) return
  if ((k as any).is_system) {
    error.value = '系统密钥无法禁用'
    return
  }
  if (!confirm(`确认禁用密钥 ${k.key_prefix}？可通过"启用"恢复。`)) return
  try {
    await disableKey(k.id)
    const currentKeyPrefix = store.apiKey ? store.apiKey.substring(0, 12) : ''
    if (k.key_prefix && currentKeyPrefix.startsWith(k.key_prefix.substring(0, 8))) {
      clearApiKey()
      window.location.href = '/login'
      return
    }
    closeKeyDrawer()
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '禁用失败'
  }
}

async function enableSelected() {
  const k = selectedKey.value
  if (!k) return
  try {
    await enableKey(k.id)
    closeKeyDrawer()
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '启用失败'
  }
}

async function copySelectedKey() {
  const k = selectedKey.value
  if (!k) return
  await copyKey(k, `drawer-${k.id}`)
}

function viewStats(k: ApiKey) {
  router.push(`/keys/${k.id}`)
}

function setTab(next: 'all' | 'active' | 'pending' | 'closed') {
  activeTab.value = next
}

function rateLimitLabel(k: ApiKey): string {
  const rpm = k.rate_limit_rpm
  const conc = k.rate_limit_concurrent
  if (rpm == null && conc == null) {
    const d = defaultLimits.value
    const parts: string[] = ['默认配置']
    if (d.rate_limit_rpm) parts.push(`${d.rate_limit_rpm} RPM`)
    if (d.rate_limit_concurrent) parts.push(`${d.rate_limit_concurrent} 并发`)
    return parts.join(' (') + (parts.length > 1 ? ')' : '')
  }
  const parts: string[] = []
  if (rpm != null) parts.push(`${rpm} RPM`)
  if (conc != null) parts.push(`${conc} 并发`)
  return parts.join(' / ')
}

async function loadDefaultLimits() {
  try {
    defaultLimits.value = await getDefaultLimits()
  } catch { /* use hardcoded fallback */ }
}

async function saveDefaultLimits() {
  limitsErr.value = ''
  limitsSuccess.value = ''
  limitsSaving.value = true
  try {
    const data = { ...defaultLimits.value }
    if (data.rate_limit_tpm === 0 || isNaN(data.rate_limit_tpm as number)) {
      data.rate_limit_tpm = null
    }
    await setDefaultLimits(data as DefaultLimits)
    limitsSuccess.value = '默认限制已保存，将在 15 秒内生效'
    showDefaultLimits.value = false
  } catch (e: unknown) {
    limitsErr.value = e instanceof Error ? e.message : '保存失败'
  } finally {
    limitsSaving.value = false
  }
}

function openDefaultLimits() {
  limitsErr.value = ''
  limitsSuccess.value = ''
  showDefaultLimits.value = true
  loadDefaultLimits()
}

async function useCreatedKeyAndReturn() {
  const created = createdKey.value
  if (!created?.api_key) return
  setApiKey(created.api_key)
  setPreferredChatKeyId(created.id)
  showNew.value = false
  createdKey.value = null
  const dest = redirectAfter.value
  if (dest) {
    await router.push(dest)
  }
}

async function useExistingKeyAndReturn(k: ApiKey) {
  try {
    const result = await revealKey(k.id)
    setApiKey(result.api_key)
    setPreferredChatKeyId(k.id)
    const dest = redirectAfter.value
    if (dest) await router.push(dest)
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : '获取密钥失败'
    error.value = msg.includes('no ciphertext')
      ? '此密钥无法还原完整内容，请重新签发'
      : msg
  }
}

onMounted(() => {
  void load()
  void loadDefaultLimits()
  if (route.query.action === 'create') {
    openNew()
  }
})
onBeforeUnmount(() => {
  if (copyNoticeTimer) window.clearTimeout(copyNoticeTimer)
  if (serverConflictTimer) window.clearTimeout(serverConflictTimer)
  serverConflictReq.cancelled = true
})
</script>

<template>
  <div>
    <div class="page-header">
      <h2>API 密钥管理</h2>
      <div style="display:flex;gap:8px">
        <button class="btn btn-ghost" @click="openDefaultLimits">⚙ 默认限制</button>
        <button class="btn btn-primary" @click="openNew">+ 签发密钥</button>
      </div>
    </div>

    <div v-if="redirectAfter" class="alert alert-info">
      选择或签发密钥后将自动返回
      <RouterLink :to="redirectAfter" class="link-inline">{{ redirectAfter }}</RouterLink>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="copyNotice" class="copy-toast" :class="{ error: copyNotice.startsWith('复制失败') }">{{ copyNotice }}</div>
    <div v-if="loading" class="empty">加载中…</div>

    <div class="card" v-if="!loading">
      <div class="status-tabs">
        <button
          v-for="tab in statusTabs"
          :key="tab.value"
          class="status-tab"
          :class="{ active: activeTab === tab.value }"
          @click="setTab(tab.value)"
        >
          <span>{{ tab.label }}</span>
          <span class="count">{{ tab.count }}</span>
        </button>
      </div>

      <div class="filter-bar">
        <FilterInput
          v-model="filterTenant"
          :options="uniqueTenants"
          placeholder="按租户过滤"
        />
        <FilterInput
          v-model="filterApp"
          :options="uniqueApps"
          placeholder="按应用过滤"
        />
        <FilterInput
          v-model="filterProfile"
          :options="uniqueProfiles"
          placeholder="按 Client Profile 过滤"
        />
        <FilterInput
          v-model="filterOwner"
          :options="uniqueOwners"
          placeholder="按归属用户过滤"
        />
        <button
          v-if="filterTenant || filterApp || filterProfile || filterOwner"
          class="btn btn-ghost btn-xs"
          @click="filterTenant = ''; filterApp = ''; filterProfile = ''; filterOwner = ''"
        >清除全部</button>
      </div>

      <table>
        <thead>
          <tr>
            <th style="width:40px">ID</th>
            <th>前缀</th>
            <th>租户</th>
            <th>应用</th>
            <th>别名</th>
            <th>Client Profile</th>
            <th>归属用户</th>
            <th>状态</th>
            <th>预算</th>
            <th>速率限制</th>
            <th>总请求</th>
            <th>总 Token</th>
            <th>累计费用</th>
            <th>到期</th>
            <th>最后使用</th>
            <th>备注</th>
            <th v-if="redirectAfter">返回</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="k in filteredKeys"
            :key="k.id"
            class="key-row"
            :class="{ selected: selectedKey?.id === k.id }"
            @click="openKeyDrawer(k)"
          >
            <td style="font-size:11px;color:var(--muted);font-family:monospace">{{ k.id }}</td>
            <td>
              <div class="key-cell">
                <code style="font-size:12px">{{ k.key_prefix }}***</code>
                <button
                  class="btn btn-ghost btn-xs"
                  :class="{ 'btn-success': copiedId === `view-${k.id}` }"
                  @click.stop="copyKey(k, `view-${k.id}`)"
                  title="复制完整密钥"
                >
                  {{ copiedId === `view-${k.id}` ? '✓' : '📋' }}
                </button>
              </div>
            </td>
            <td><code style="font-size:11px">{{ k.tenant_id }}</code></td>
            <td>{{ k.application_code }}</td>
            <td><code style="font-size:11px">{{ k.key_alias || '—' }}</code></td>
            <td>
              <code style="font-size:11px">{{ k.default_client_profile || '—' }}</code>
            </td>
            <td>
              <span style="font-size:12px">{{ k.owner_user ?? '—' }}</span>
            </td>
            <td>
              <span class="badge"
                :class="keyStateBadgeClass(k)"
              >
                {{ keyStateLabel(k) }}
              </span>
              <span v-if="(k as any).is_system" class="badge badge-system">系统</span>
            </td>
            <td>{{ k.budget_usd != null ? fmtCost(k.budget_usd) : '无限制' }}</td>
            <td>{{ rateLimitLabel(k) }}</td>
            <td style="font-size:12px;color:var(--muted);text-align:right">{{ formatRequests(k.total_requests) }}</td>
            <td style="font-size:12px;color:var(--muted);text-align:right" :title="`入 ${formatTokens(k.total_prompt_tokens)} / 出 ${formatTokens(k.total_completion_tokens)}`">
              {{ formatTokens(k.total_prompt_tokens + k.total_completion_tokens) }}
            </td>
            <td style="font-size:12px;text-align:right" :class="{ 'has-cost': k.total_cost_usd > 0 }">
              {{ k.total_cost_usd > 0 ? fmtCost(k.total_cost_usd) : '—' }}
            </td>
            <td style="font-size:12px;color:var(--muted)">{{ fmtDate(k.expires_at) }}</td>
            <td style="font-size:12px;color:var(--muted)">{{ fmtDate(k.last_used_at) }}</td>
            <td style="font-size:11px;color:var(--muted);max-width:160px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" :title="k.remark || ''">
              {{ k.remark || '—' }}
            </td>
            <td v-if="redirectAfter">
              <button
                v-if="keyState(k) === 'active'"
                type="button"
                class="btn btn-primary btn-xs"
                @click.stop="useExistingKeyAndReturn(k)"
              >
                使用并返回
              </button>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-if="!loading && filteredKeys.length === 0" class="empty">当前状态下没有密钥</div>
    </div>

    <!-- Key edit drawer -->
    <Teleport to="body">
      <div v-if="selectedKey" class="drawer-backdrop" @click="closeKeyDrawer">
        <div class="drawer-panel card" @click.stop>
          <div class="drawer-header">
            <div>
              <div style="font-size:15px;font-weight:600">密钥 #{{ selectedKey.id }}</div>
              <div class="drawer-sub"><code>{{ selectedKey.key_prefix }}***</code> · {{ selectedKey.application_code }}</div>
            </div>
            <button class="btn btn-ghost btn-sm" @click="closeKeyDrawer">✕ 关闭</button>
          </div>

          <div class="drawer-body">
            <div class="drawer-section">
              <div class="drawer-section-title">基本信息</div>
              <div class="detail-grid">
                <span class="dk">租户</span><span class="dv mono">{{ selectedKey.tenant_id }}</span>
                <span class="dk">应用</span><span class="dv">{{ selectedKey.application_code }}</span>
                <span class="dk">状态</span>
                <span class="dv">
                  <span class="badge" :class="keyStateBadgeClass(selectedKey)">{{ keyStateLabel(selectedKey) }}</span>
                  <span v-if="(selectedKey as any).is_system" class="badge badge-system">系统</span>
                </span>
                <span class="dk">预算</span><span class="dv">{{ selectedKey.budget_usd != null ? fmtCost(selectedKey.budget_usd) : '无限制' }}</span>
                <span class="dk">速率限制</span><span class="dv">{{ rateLimitLabel(selectedKey) }}</span>
                <span class="dk">到期</span><span class="dv">{{ fmtDate(selectedKey.expires_at) }}</span>
                <span class="dk">备注</span><span class="dv">{{ selectedKey.remark || '—' }}</span>
              </div>
            </div>

            <div class="drawer-section">
              <div class="drawer-section-title">编辑</div>
              <div class="form-group">
                <label>Client Profile</label>
                <input
                  v-model="editForm.profile"
                  class="input"
                  placeholder="cursor / roocode / cline"
                />
              </div>
              <div class="form-group">
                <label>归属用户</label>
                <input
                  v-model="editForm.ownerUser"
                  class="input"
                  placeholder="用户名"
                />
              </div>
              <div class="form-group">
                <label>密钥别名</label>
                <input
                  v-model="editForm.keyAlias"
                  class="input"
                  placeholder="如: prod, dev, zhangsan-cli"
                />
              </div>
            </div>
          </div>

          <div class="drawer-footer">
            <div class="drawer-actions">
              <button
                class="btn btn-ghost btn-sm"
                :class="{ 'btn-success': copiedId === `drawer-${selectedKey.id}` }"
                @click="copySelectedKey"
              >
                {{ copiedId === `drawer-${selectedKey.id}` ? '✓ 已复制' : '📋 复制密钥' }}
              </button>
              <button class="btn btn-secondary btn-sm" @click="viewStats(selectedKey)">📊 使用统计</button>
              <button
                v-if="selectedKey.status === 'pending'"
                class="btn btn-success btn-sm"
                @click="approveSelected"
              >审批</button>
              <button
                v-else-if="selectedKey.status === 'active'"
                class="btn btn-sm"
                @click="disableSelected"
              >禁用</button>
              <button
                v-else-if="selectedKey.status === 'disabled'"
                class="btn btn-secondary btn-sm"
                @click="enableSelected"
              >启用</button>
            </div>
            <div class="drawer-save-row">
              <button class="btn btn-ghost" @click="closeKeyDrawer">取消</button>
              <button class="btn btn-primary" @click="saveKeyProfile" :disabled="profileSaving">
                {{ profileSaving ? '保存中…' : '保存' }}
              </button>
            </div>
          </div>
        </div>
      </div>
    </Teleport>

    <div class="modal-overlay" v-if="showNew" @click.self="() => { if (!createdKey) showNew = false }">
      <div class="modal" @click.stop>
        <h3>签发新密钥</h3>

        <!-- Show created key with copy button -->
        <template v-if="createdKey">
          <div class="alert alert-success">密钥已创建，请立即保存！关闭后无法再次查看完整密钥。</div>
          <div class="key-display">
            <code>{{ createdKey.api_key || '（密钥返回异常）' }}</code>
          </div>
          <div class="key-copy-row">
            <button
              class="btn btn-primary"
              :class="{ 'btn-success': copiedId === 'new-key' }"
              @click="copyCreatedKey('new-key')"
              :disabled="!createdKey.api_key"
            >
              {{ copiedId === 'new-key' ? '✓ 已复制' : '📋 复制密钥' }}
            </button>
            <span class="hint">{{ createdKey.api_key ? '可多次点击复制' : '后端未返回有效密钥，请检查接口返回' }}</span>
          </div>
          <div style="text-align:right;margin-top:16px;display:flex;gap:8px;justify-content:flex-end;flex-wrap:wrap">
            <button
              v-if="redirectAfter && createdKey.api_key"
              class="btn btn-primary"
              @click="useCreatedKeyAndReturn"
            >
              使用此密钥并返回
            </button>
            <button class="btn btn-ghost" @click="showNew = false">我已保存，关闭</button>
          </div>
        </template>

        <!-- Create form -->
        <template v-else>
          <div v-if="newErr" class="alert alert-danger">{{ newErr }}</div>
          <div
            v-if="newConflict"
            class="alert alert-warning"
            data-testid="key-conflict-warning"
          >
            <template v-if="newConflict.isSystem">
              ⚠ 系统密钥 #{{ newConflict.id }}
              (<code>{{ newConflict.prefix }}****</code>) 已占用该 (租户 + 应用 + 别名) 组合。
              <strong>请先在列表中禁用或吊销该系统密钥</strong>，再签发新密钥。
            </template>
            <template v-else>
              ⚠ 已有可用密钥 #{{ newConflict.id }}
              (<code>{{ newConflict.prefix }}****</code>) 使用同一 (租户 + 应用 + 别名) 组合。
              <strong>请先禁用或吊销该密钥</strong>，再签发新密钥。
            </template>
            <div
              v-if="newConflict.status || newConflict.expiresAt || newConflict.ownerUser"
              class="conflict-meta"
            >
              <span v-if="newConflict.status">状态: <code>{{ newConflict.status }}</code></span>
              <span v-if="newConflict.expiresAt">到期: <code>{{ fmtDate(newConflict.expiresAt) }}</code></span>
              <span v-if="newConflict.ownerUser">归属: <code>{{ newConflict.ownerUser }}</code></span>
            </div>
            <div v-if="serverConflictLoading" class="conflict-loading">正在向服务器确认…</div>
          </div>
          <div class="form-group">
            <label>租户（默认 default）</label>
            <input 
              v-model="newTenant" 
              placeholder="default"
              :disabled="!isDefaultTenant()"
              :title="isDefaultTenant() ? '可修改租户' : '非 default 租户只能签发当前租户的密钥'"
            />
            <span v-if="!isDefaultTenant()" class="hint">
              非 default 租户只能签发当前租户（{{ getCurrentTenantId() }}）的密钥
            </span>
          </div>
          <div class="form-group">
            <label>应用代码 *</label>
            <input v-model="newApp" placeholder="如: default, portal, agent" />
          </div>
          <div class="form-group">
            <label>密钥别名 *（同一租户+应用下的唯一标识）</label>
            <input v-model="newKeyAlias" placeholder="如: prod, dev, zhangsan-cli" />
          </div>
          <div class="form-group">
            <label>归属用户（可选）</label>
            <input v-model="newOwner" placeholder="如: admin" />
          </div>
          <div class="form-group">
            <label>预算上限 USD（可选）</label>
            <input v-model="newBudget" type="number" step="0.01" placeholder="留空不限制" />
          </div>
          <div class="form-group">
            <label>每分钟请求数限制（可选）</label>
            <input v-model="newRpm" type="number" placeholder="留空不限制" />
          </div>
          <div class="form-group">
            <label>备注（说明创建原因）</label>
            <input v-model="newRemark" placeholder="如: 测试使用、正式环境密钥" maxlength="512" />
          </div>
          <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
            <button class="btn btn-ghost" @click="showNew = false">取消</button>
            <button
              class="btn btn-primary"
              @click="submitNew"
              :disabled="newSaving || newConflict !== null"
            >
              {{ newSaving ? '签发中…' : (newConflict ? '存在重复，无法签发' : '签发') }}
            </button>
          </div>
        </template>
      </div>
    </div>

    <!-- Default Limits Config Modal -->
    <div class="modal-overlay" v-if="showDefaultLimits" @click.self="showDefaultLimits = false">
      <div class="modal" style="max-width:440px" @click.stop>
        <h3>默认速率限制配置</h3>
        <p style="font-size:13px;color:var(--muted);margin-bottom:12px">
          当密钥未设置自定义限制时，将使用以下默认值。
          修改后保存到 Redis，所有实例在 15 秒内生效。
        </p>
        <div v-if="limitsErr" class="alert alert-danger">{{ limitsErr }}</div>
        <div v-if="limitsSuccess" class="alert alert-success">{{ limitsSuccess }}</div>
        <div class="form-group">
          <label>默认 RPM（每分钟请求数）</label>
          <input v-model.number="defaultLimits.rate_limit_rpm" type="number" min="0" placeholder="0=不限制" />
        </div>
        <div class="form-group">
          <label>默认并发数</label>
          <input v-model.number="defaultLimits.rate_limit_concurrent" type="number" min="0" placeholder="0=不限制" />
        </div>
        <div class="form-group">
          <label>默认 TPM（每分钟 token 数）</label>
          <input v-model.number="defaultLimits.rate_limit_tpm" type="number" min="0" placeholder="0=不限制" />
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" @click="showDefaultLimits = false">取消</button>
          <button class="btn btn-primary" @click="saveDefaultLimits" :disabled="limitsSaving">
            {{ limitsSaving ? '保存中…' : '保存' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.status-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 14px;
}

.filter-bar {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 12px;
  flex-wrap: wrap;
}

.filter-bar :deep(.input) {
  min-width: 180px;
  padding: 6px 10px;
  font-size: 13px;
}

.status-tab {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--bg);
  color: var(--text);
  cursor: pointer;
  transition: all 0.16s ease;
}

.status-tab:hover {
  border-color: var(--accent);
  transform: translateY(-1px);
}

.status-tab.active {
  background: rgba(99, 102, 241, 0.14);
  border-color: var(--accent);
  color: var(--text);
}

.status-tab .count {
  min-width: 20px;
  padding: 0 6px;
  border-radius: 999px;
  background: var(--bg-subtle, var(--bg));
  color: var(--muted);
  text-align: center;
  font-size: 12px;
  line-height: 18px;
}

.key-row {
  cursor: pointer;
  transition: background 0.12s ease;
}

.key-row:hover {
  background: rgba(99, 102, 241, 0.06);
}

.key-row.selected {
  background: rgba(99, 102, 241, 0.1);
}

.key-cell {
  display: flex;
  align-items: center;
  gap: 4px;
}

.drawer-sub {
  margin-top: 4px;
  font-size: 12px;
  color: var(--muted);
}

.drawer-body {
  flex: 1;
  overflow-y: auto;
}

.drawer-footer {
  margin-top: auto;
  padding-top: 16px;
  border-top: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.drawer-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.drawer-save-row {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
}

.detail-grid {
  display: grid;
  grid-template-columns: 100px 1fr;
  gap: 8px 12px;
  font-size: 13px;
}

.detail-grid .dk {
  color: var(--muted);
}

.detail-grid .dv.mono {
  font-family: monospace;
  font-size: 12px;
}

.has-cost {
  color: var(--accent, #d4a017);
  font-weight: 600;
}

.key-display {
  display: flex;
  align-items: center;
  gap: 8px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 10px 12px;
  margin-bottom: 8px;
  word-break: break-all;
}

.key-display code {
  flex: 1;
  font-size: 12px;
  color: var(--success);
}

.key-copy-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 8px;
}

.hint {
  font-size: 12px;
  color: var(--muted);
}

.copy-toast {
  position: fixed;
  top: 16px;
  right: 16px;
  z-index: 1000;
  padding: 8px 12px;
  border-radius: var(--radius);
  background: var(--success);
  color: white;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.18);
  font-size: 13px;
}

.copy-toast.error {
  background: var(--danger);
}

.conflict-meta {
  display: flex;
  gap: 16px;
  margin-top: 6px;
  font-size: 12px;
  color: var(--muted);
  flex-wrap: wrap;
}

.conflict-loading {
  margin-top: 4px;
  font-size: 11px;
  color: var(--muted);
  font-style: italic;
}
</style>
