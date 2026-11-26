<script setup lang="ts">
// UsersView.vue — 用户管理（2026-09-30 统计 UI 优化轮增强）
// 新增：顶部统计条 / 搜索+角色+状态筛选 / 近 30 天用量列（请求·Token·积分，
// 占比条） / 点击行打开用户详情抽屉（UserDetailDrawer）。
// 用量数据 GET /api/admin/users/usage-summary（usage_facts × api_keys.owner_user，
// 与对账快照同源）；无用量账号显示 —，列表加载不被用量失败阻塞。
import { useI18n } from 'vue-i18n'
import { surfaceError } from '../i18n/errorSurface'
import { fmtDateTime24h } from '../i18n/useFormat'
import { sortByName } from '../utils/sortByName'
import { ref, computed, onMounted, watch } from 'vue'
import { getUsers, createUser, updateUser, deleteUser, resetUserPassword, getTenantsAdmin, getUserUsageSummary } from '../api'
import type { Tenant, UserUsageSummaryItem } from '../api'
import { store, isReadOnlyMode, isTenantAdmin } from '../store'
import { checkPasswordPolicy, passwordsMatch } from '../utils/passwordPolicy'
import { confirmDialog } from '../composables/useConfirmDialog'
import CollapsibleFilterSection from '../components/ui/CollapsibleFilterSection.vue'
// 2026-09-13 P3：页头/表格容器收敛到 ui 组件（方案 §4.5.2/§4.5.5）
import PageHeader from '../components/ui/PageHeader.vue'
import DataTable from '../components/ui/DataTable.vue'
import BarCell from '../components/ui/BarCell.vue'
import UserDetailDrawer from '../components/UserDetailDrawer.vue'
import type { DrawerUser } from '../components/UserDetailDrawer.vue'

const { t } = useI18n()


const readOnly = computed(() => isReadOnlyMode())
const tenantAdmin = computed(() => isTenantAdmin())
const canCreateUsers = computed(() => !readOnly.value)
const canResetPasswords = computed(() => !readOnly.value || tenantAdmin.value)
const canDeleteUsers = computed(() => !readOnly.value)
// 变更角色与建号/删号同权：只给平台管理员（租户管理员是只读）。
const canChangeRoles = computed(() => !readOnly.value)

// ── 角色口径（与后端 admin/users.go 同源，2026-11-26 角色治理轮）──
// ① 角色闭集：super_admin / tenant_admin / user（普通用户）。
// ② super_admin 是**平台级**身份，只允许落在 default 租户 ⇒ 非 default 租户
//    只能选 tenant_admin 或 user。
// ★ 前端只是提示层，真正的闸门在后端（POST /api/users、PUT /api/users/:id）。
const ROLE_SUPER_ADMIN = 'super_admin'
const ROLE_TENANT_ADMIN = 'tenant_admin'
const ROLE_USER = 'user'
const DEFAULT_TENANT_CODE = 'default'

function roleOptionsForTenant(tenantId: string): string[] {
  return tenantId === DEFAULT_TENANT_CODE
    ? [ROLE_SUPER_ADMIN, ROLE_TENANT_ADMIN, ROLE_USER]
    : [ROLE_TENANT_ADMIN, ROLE_USER]
}

interface User {
  id: number
  tenant_id: string
  username: string
  display_name: string
  email: string
  role: string
  enabled: boolean
  must_change_password?: boolean
  last_login_at: string | null
  created_at: string
}

const users = ref<User[]>([])
const loading = ref(false)
const error = ref('')
const showCreate = ref(false)
const editUser = ref<User | null>(null)
const resetPwdUser = ref<User | null>(null)
const filterTenant = ref<string>('')
const filterRole = ref<string>('')
const filterStatus = ref<string>('')
const search = ref<string>('')
const sortBy = ref<'default' | 'requests' | 'credits'>('default')

// 2026-11-18 筛选收纳轮：生效条件 chips（头部常驻，收拢时也可见）
const filterChips = computed(() => {
  const chips: Array<{ key: string; label: string; onRemove: () => void }> = []
  if (filterTenant.value) {
    chips.push({ key: 'tenant', label: `${t('users.filter.byTenant', )}: ${filterTenant.value}`, onRemove: () => { filterTenant.value = '' } })
  }
  if (filterRole.value) {
    chips.push({ key: 'role', label: `${t('users.table.role', )}: ${t(`users.role.${filterRole.value}`)}`, onRemove: () => { filterRole.value = '' } })
  }
  if (filterStatus.value) {
    chips.push({ key: 'status', label: `${t('users.table.status', )}: ${filterStatus.value === 'enabled' ? t('users.status.enabled') : t('users.status.disabled')}`, onRemove: () => { filterStatus.value = '' } })
  }
  if (sortBy.value !== 'default') {
    chips.push({ key: 'sort', label: `${t('users.filter.sortDefault', )}: ${sortBy.value === 'requests' ? t('users.filter.sortRequests', ) : t('users.filter.sortCredits', )}`, onRemove: () => { sortBy.value = 'default' } })
  }
  if (search.value.trim()) {
    chips.push({ key: 'search', label: `${t('common.button.search', )}: ${search.value.trim()}`, onRemove: () => { search.value = '' } })
  }
  return chips
})

function clearAllFilters() {
  filterTenant.value = ''
  filterRole.value = ''
  filterStatus.value = ''
  sortBy.value = 'default'
  search.value = ''
}
const allTenants = ref<Tenant[]>([])
const newPwd = ref('')
const createConfirmPwd = ref('')
const resetConfirmPwd = ref('')
const createPasswordPolicy = computed(() => checkPasswordPolicy(form.value.password))
const resetPasswordPolicy = computed(() => checkPasswordPolicy(newPwd.value))
const createPasswordsMatch = computed(() => !createConfirmPwd.value || passwordsMatch(form.value.password, createConfirmPwd.value))
const resetPasswordsMatch = computed(() => !resetConfirmPwd.value || passwordsMatch(newPwd.value, resetConfirmPwd.value))

// ── 用量统计（2026-09-30）：列表列 + 顶部统计条 ──────────────
const usageDays = 30
const usageItems = ref<UserUsageSummaryItem[]>([])
const usageReady = ref(false)
function accountKey(user: { tenant_id: string; username: string }): string {
  return JSON.stringify([user.tenant_id, user.username])
}
const usageByUser = computed<Map<string, UserUsageSummaryItem>>(() => {
  const m = new Map<string, UserUsageSummaryItem>()
  for (const it of usageItems.value) m.set(accountKey(it), it)
  return m
})

// 详情抽屉
const detailOpen = ref(false)
const detailUser = ref<DrawerUser | null>(null)
function openDetail(u: User) {
  detailUser.value = u
  detailOpen.value = true
}
function onDrawerToggleEnabled(u: DrawerUser) {
  const target = users.value.find((x) => x.id === u.id)
  if (!target) return
  // 乐观翻转：抽屉徽章立即切换；handleToggle 失败时回调回滚原状态，
  // 避免抽屉与列表状态分叉。
  detailUser.value = { ...target, enabled: !target.enabled }
  void handleToggle(target, () => {
    if (detailUser.value?.id === target.id) detailUser.value = { ...target }
  })
}
function onDrawerResetPassword(u: DrawerUser) {
  const target = users.value.find((x) => x.id === u.id)
  if (target) {
    detailOpen.value = false
    resetPwdUser.value = target
    newPwd.value = ''
    resetConfirmPwd.value = ''
  }
}

// 列表过滤（租户/角色/状态/搜索 + 排序，全本地）
const filteredUsers = computed(() => {
  let list = users.value
  if (filterTenant.value) list = list.filter((u) => u.tenant_id === filterTenant.value)
  if (filterRole.value) list = list.filter((u) => u.role === filterRole.value)
  if (filterStatus.value) list = list.filter((u) => (filterStatus.value === 'enabled' ? u.enabled : !u.enabled))
  const kw = search.value.trim().toLowerCase()
  if (kw) {
    list = list.filter(
      (u) =>
        u.username.toLowerCase().includes(kw) ||
        (u.display_name || '').toLowerCase().includes(kw) ||
        (u.email || '').toLowerCase().includes(kw),
    )
  }
  if (sortBy.value !== 'default') {
    list = [...list].sort((a, b) => {
      const ua = usageOf(a)
      const ub = usageOf(b)
      const va = sortBy.value === 'requests' ? ua?.requests ?? 0 : ua?.credits ?? 0
      const vb = sortBy.value === 'requests' ? ub?.requests ?? 0 : ub?.credits ?? 0
      return vb - va
    })
  }
  return list
})

const maxRequests = computed(() =>
  filteredUsers.value.reduce((m, u) => Math.max(m, usageOf(u)?.requests ?? 0), 0),
)

// 顶部统计条：基于当前筛选结果 + 用量表
const statBar = computed(() => {
  const list = filteredUsers.value
  let active = 0
  let req = 0
  let credits = 0
  for (const u of list) {
    const usage = usageOf(u)
    if (usage && usage.requests > 0) {
      active++
      req += usage.requests
      credits += usage.credits
    }
  }
  return {
    total: list.length,
    active,
    admins: list.filter((u) => u.role === 'super_admin' || u.role === 'tenant_admin').length,
    disabled: list.filter((u) => !u.enabled).length,
    req,
    credits,
  }
})

function usageOf(u: User): UserUsageSummaryItem | undefined {
  return usageByUser.value.get(accountKey(u))
}
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
function fmtLastActive(iso: string | null | undefined): string {
  if (!iso) return t('users.usage.never', )
  const diff = Date.now() - new Date(iso).getTime()
  const min = Math.floor(diff / 60000)
  if (min < 1) return t('users.usage.justNow', )
  if (min < 60) return `${min} ${t('users.usage.minAgo', )}`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr} ${t('users.usage.hourAgo', )}`
  const day = Math.floor(hr / 24)
  return `${day} ${t('users.usage.dayAgo', )}`
}

// Create form
const form = ref({
  username: '',
  password: '',
  tenant_id: 'default',
  display_name: '',
  email: '',
  role: 'tenant_admin',
})

// 建号时的角色下拉：按**所选租户**收敛（非 default 租户不给「超级管理员」）。
const createRoleOptions = computed(() => roleOptionsForTenant(form.value.tenant_id))
// 切租户后若当前角色在新租户不可用 ⇒ 立刻回落，
// 否则表单会带着一个后端必然拒绝的角色提交。
watch(() => form.value.tenant_id, () => {
  if (!createRoleOptions.value.includes(form.value.role)) {
    form.value.role = ROLE_TENANT_ADMIN
  }
})

// ── 变更角色（2026-11-26 角色治理轮）──────────────────────────────
// 产品诉求：「用户管理」里能改账号角色；非 default 租户不得出现超级管理员。
// 收掉 live 上租户 chenb 下那个 super_admin 账号也走这条路径。
const showRoleModal = ref(false)
const roleTarget = ref<User | null>(null)
const roleDraft = ref<string>(ROLE_TENANT_ADMIN)
const roleSaving = ref(false)
const roleOptions = computed(() => roleOptionsForTenant(roleTarget.value?.tenant_id ?? DEFAULT_TENANT_CODE))

function openRoleModal(u: User) {
  roleTarget.value = u
  roleDraft.value = u.role
  showRoleModal.value = true
}

async function submitRoleChange() {
  const target = roleTarget.value
  if (!target) return
  if (roleDraft.value === target.role) {
    showRoleModal.value = false
    return
  }
  roleSaving.value = true
  error.value = ''
  try {
    await updateUser(target.id, { role: roleDraft.value })
    showRoleModal.value = false
    await load()
  } catch (e: unknown) {
    error.value = surfaceError(e, { key: 'users.error.roleChangeFailed' })
  } finally {
    roleSaving.value = false
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    // 2026-10-03：按名称排序（老板要求的查找性排序）。
    // ★ 显式指定 ['username']：首列显示的是 username，display_name 是副标题。
    //   用默认候选顺序会先命中 display_name，于是按隐藏的中文名排 ——
    //   mavis_local 会落到「本」(b)，想找 mavis_local 反而找不到。
    //   与租户页那个「按 code 排、首列显示 name」是同一类毛病。
    users.value = sortByName(await getUsers(), undefined, ['username'])
    // 用量统计独立拉取：失败不阻塞列表（列显示 —）。
    try {
      const summary = await getUserUsageSummary(usageDays)
      usageReady.value = summary.degraded !== true
      usageItems.value = usageReady.value ? summary.items ?? [] : []
    } catch {
      usageReady.value = false
      usageItems.value = []
    }
  } catch (e: unknown) {
    error.value = surfaceError(e, { key: 'users.error.loadFailed' })
  } finally {
    loading.value = false
  }
}

async function handleCreate() {
  if (!form.value.username || !form.value.password) {
    error.value = t('users.error.usernamePasswordRequired')
    return
  }
  if (!passwordsMatch(form.value.password, createConfirmPwd.value)) {
    error.value = t('users.error.passwordMismatch')
    return
  }
  if (!createPasswordPolicy.value.valid) {
    error.value = t('users.error.passwordComplexity')
    return
  }
  try {
    await createUser(form.value)
    showCreate.value = false
    form.value = { username: '', password: '', tenant_id: 'default', display_name: '', email: '', role: 'tenant_admin' }
    await load()
  } catch (e: unknown) {
    error.value = surfaceError(e, { key: 'users.error.createFailed' })
  }
}

async function handleToggle(u: User, onRollback?: () => void) {
  try {
    await updateUser(u.id, { enabled: !u.enabled })
    await load()
  } catch (e: unknown) {
    error.value = surfaceError(e, { key: 'users.error.toggleFailed' })
    onRollback?.()
  }
}

async function handleDelete(u: User) {
  if (!(await confirmDialog(t('users.confirmDelete', { name: u.username })))) return
  try {
    await deleteUser(u.id)
    await load()
  } catch (e: unknown) {
    error.value = surfaceError(e, { key: 'users.error.deleteFailed' })
  }
}

async function handleResetPwd() {
  if (!resetPwdUser.value || !resetPasswordPolicy.value.valid) {
    error.value = t('users.error.resetPasswordComplexity')
    return
  }
  if (!passwordsMatch(newPwd.value, resetConfirmPwd.value)) {
    error.value = t('users.error.resetPasswordMismatch')
    return
  }
  try {
    await resetUserPassword(resetPwdUser.value.id, newPwd.value)
    resetPwdUser.value = null
    newPwd.value = ''
  } catch (e: unknown) {
    error.value = surfaceError(e, { key: 'users.error.resetFailed' })
  }
}

function roleLabel(r: string) {
  if (r === ROLE_SUPER_ADMIN) return t('users.role.super_admin')
  if (r === ROLE_TENANT_ADMIN) return t('users.role.tenant_admin')
  if (r === ROLE_USER) return t('users.role.user')
  // 未知角色（如 channel）显示原值 —— 别把它谎称成「租户管理员」。
  return r
}

function closeCreateModal() {
  showCreate.value = false
  form.value = { username: '', password: '', tenant_id: 'default', display_name: '', email: '', role: 'tenant_admin' }
  createConfirmPwd.value = ''
}

function closeResetModal() {
  resetPwdUser.value = null
  newPwd.value = ''
  resetConfirmPwd.value = ''
}

// 租户下拉的加载状态。
//
// 2026-10-03：原来 `catch { /* ignore */ }`，失败后 allTenants 保持 []，
// 页面把「租户下拉是空的」和「确实没有租户」渲染成同一个东西。
// 用户会以为自己在「全部租户」范围内筛，而真实范围是**未知**。
// 与 UserProfileListView 的「降级时不说『没有用户』」同一判据：
// 把「不知道」讲成「知道」比不显示更糟。
const tenantsError = ref('')

async function loadTenants() {
  tenantsError.value = ''
  try {
    allTenants.value = await getTenantsAdmin()
  } catch (e: unknown) {
    allTenants.value = []
    tenantsError.value = e instanceof Error && e.message ? e.message : t('users.error.tenantsLoadFailed')
  }
}
onMounted(() => { load(); loadTenants() })
</script>

<template>
  <div class="users-page">
    <PageHeader :title="t('users.title')">
      <template #actions>
        <button v-if="canCreateUsers" class="btn btn-primary" @click="showCreate = true">+ {{ t('users.create') }}</button>
      </template>
    </PageHeader>

    <div v-if="readOnly" class="alert alert-info" style="margin-bottom:12px">
      {{ t('users.readOnlyNotice') }}
    </div>

    <!-- ══ 顶部统计条（2026-09-30 统计 UI 优化轮） ══ -->
    <div class="mini-stats" data-testid="user-stats-bar">
      <div class="mini-stat"><div class="v">{{ statBar.total }}</div><div class="l">{{ t('users.stats.total', ) }}</div></div>
      <div class="mini-stat"><div class="v v-ok">{{ usageReady ? statBar.active : '—' }}</div><div class="l">{{ t('users.stats.active', ) }}</div></div>
      <div class="mini-stat"><div class="v">{{ statBar.admins }}</div><div class="l">{{ t('users.stats.admins', ) }}</div></div>
      <div class="mini-stat"><div class="v v-err">{{ statBar.disabled }}</div><div class="l">{{ t('users.stats.disabled', ) }}</div></div>
      <div class="mini-stat"><div class="v">{{ usageReady ? fmtNum(statBar.req) : '—' }}</div><div class="l">{{ t('users.stats.requests30d', ) }}</div></div>
      <div class="mini-stat"><div class="v">{{ usageReady ? fmtNum(statBar.credits) : '—' }}</div><div class="l">{{ t('users.stats.credits30d', ) }}</div></div>
    </div>

    <!-- 2026-11-18 筛选收纳轮：默认收拢，生效条件 chips 常驻头部 -->
    <CollapsibleFilterSection :chips="filterChips" @reset="clearAllFilters">
      <label>{{ t('users.filter.byTenant', $t('common.terms.tenant')) }}</label>
      <select v-model="filterTenant">
        <option value="">{{ t('users.filter.allTenants') }}</option>
        <option v-for="t in allTenants" :key="t.code" :value="t.code">
          {{ t.name }} ({{ t.code }})
        </option>
      </select>
      <!--
        加载失败时**不写**「没有租户」：筛选范围此时是未知的，
        而空下拉会让人以为自己正在看「全部租户」。
      -->
      <p v-if="tenantsError" class="filter-error" role="status">
        ⚠️ {{ t('users.error.tenantsLoadFailed') }}（{{ tenantsError }}）
      </p>
      <select v-model="filterRole">
        <option value="">{{ t('users.filter.allRoles', ) }}</option>
        <option value="tenant_admin">{{ t('users.role.tenant_admin') }}</option>
        <option value="super_admin">{{ t('users.role.super_admin') }}</option>
        <option value="user">{{ t('users.role.user') }}</option>
      </select>
      <select v-model="filterStatus">
        <option value="">{{ t('users.filter.allStatus', ) }}</option>
        <option value="enabled">{{ t('users.status.enabled') }}</option>
        <option value="disabled">{{ t('users.status.disabled') }}</option>
      </select>
      <select v-model="sortBy">
        <option value="default">{{ t('users.filter.sortDefault', ) }}</option>
        <option value="requests">{{ t('users.filter.sortRequests', ) }}</option>
        <option value="credits">{{ t('users.filter.sortCredits', ) }}</option>
      </select>
      <input v-model="search" class="search-input" :placeholder="t('users.filter.searchPlaceholder', )">
    </CollapsibleFilterSection>

    <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>

    <div v-if="loading" class="loading">{{ t('users.loading') }}</div>

    <DataTable v-else min-width="980px">
    <table class="table" style="width:100%">
      <thead>
        <tr>
          <th>{{ t('users.table.username', $t('common.terms.user')) }}</th>
          <th>{{ t('users.table.email', $t('common.terms.email')) }}</th>
          <th>{{ t('users.table.tenant', $t('common.terms.tenant')) }}</th>
          <th>{{ t('users.table.role', $t('common.terms.role')) }}</th>
          <th>{{ t('users.table.status', $t('common.terms.status')) }}</th>
          <th class="col-num">{{ t('users.usage.requests30d', ) }}</th>
          <th class="col-num">Token</th>
          <th class="col-num">{{ t('users.usage.credits30d', ) }}</th>
          <th>{{ t('users.table.lastLogin', ) }}</th>
          <th>{{ t('users.usage.lastActive', ) }}</th>
          <th>{{ t('users.table.actions', $t('common.actions')) }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="u in filteredUsers" :key="u.id" class="row-click" @click="openDetail(u)">
          <td>
            <BarCell :pct="maxRequests > 0 ? ((usageOf(u)?.requests ?? 0) / maxRequests) * 100 : 0">
              <template #name>
                <strong>{{ u.username }}</strong>
                <span class="cell-sub">{{ u.display_name || '-' }}</span>
                <span v-if="u.must_change_password" class="badge badge-yellow">{{ t('users.mustChangePassword.pending', ) }}</span>
              </template>
            </BarCell>
          </td>
          <td>{{ u.email || '-' }}</td>
          <td><code>{{ u.tenant_id }}</code></td>
          <td><span class="badge" :class="u.role === 'super_admin' ? 'badge-purple' : 'badge-blue'">{{ roleLabel(u.role) }}</span></td>
          <td>
            <span v-if="!readOnly" class="badge" :class="u.enabled ? 'badge-green' : 'badge-red'" style="cursor:pointer" @click.stop="handleToggle(u)">
              {{ u.enabled ? t('users.status.enabled') : t('users.status.disabled') }}
            </span>
            <span v-else class="badge" :class="u.enabled ? 'badge-green' : 'badge-red'">
              {{ u.enabled ? t('users.status.enabled') : t('users.status.disabled') }}
            </span>
          </td>
          <td class="col-num">{{ usageOf(u) ? fmtNum(usageOf(u)!.requests) : '—' }}</td>
          <td class="col-num">{{ usageOf(u) ? fmtTokensCompact(usageOf(u)!.tokens) : '—' }}</td>
          <td class="col-num">{{ usageOf(u) ? fmtNum(usageOf(u)!.credits) : '—' }}</td>
          <td class="mono-time">{{ fmtDateTime24h(u.last_login_at) }}</td>
          <td :class="usageOf(u) && usageOf(u)!.last_active_at ? 'txt-ok' : 'txt-muted'">
            {{ fmtLastActive(usageOf(u)?.last_active_at) }}
          </td>
          <td @click.stop>
            <button class="btn btn-ghost btn-sm" @click="openDetail(u)">{{ t('users.action.detail', $t('common.button.detail')) }}</button>
            <button v-if="canChangeRoles" class="btn btn-ghost btn-sm" data-testid="user-change-role-btn" @click="openRoleModal(u)">{{ t('users.action.changeRole') }}</button>
            <button v-if="canResetPasswords" class="btn btn-ghost btn-sm" @click="resetPwdUser = u; newPwd = ''; resetConfirmPwd = ''">{{ t('users.action.resetPassword') }}</button>
            <button v-if="canDeleteUsers && u.id !== store.userInfo?.id" class="btn btn-ghost btn-sm" style="color:var(--danger)" @click="handleDelete(u)">{{ t('users.action.delete') }}</button>
          </td>
        </tr>
        <tr v-if="filteredUsers.length === 0">
          <td colspan="11" style="text-align:center; padding:32px; color: var(--muted)">{{ t('users.empty', ) }}</td>
        </tr>
      </tbody>
    </table>
    </DataTable>

    <!-- 用户详情抽屉 -->
    <UserDetailDrawer
      v-model="detailOpen"
      :user="detailUser"
      @reset-password="onDrawerResetPassword"
      @toggle-enabled="onDrawerToggleEnabled"
    />

    <!-- Create Modal -->
    <div v-if="showCreate" class="modal-backdrop" @click.self="closeCreateModal">
      <div class="modal-card">
        <h3>{{ t('users.modal.create.title') }}</h3>
        <div class="form-group">
          <label>{{ t('users.modal.create.username') }} *</label>
          <input v-model="form.username" :placeholder="t('users.modal.create.usernamePlaceholder')" />
        </div>
        <div class="form-group">
          <label>{{ t('users.modal.create.password') }} *</label>
          <input v-model="form.password" type="password" :placeholder="t('users.modal.create.passwordPlaceholder')" />
          <div class="password-policy">
            <div
              v-for="item in createPasswordPolicy.requirements"
              :key="item.key"
              class="password-policy__item"
              :class="item.passed ? 'is-pass' : 'is-pending'"
            >
              {{ item.passed ? '✓' : '○' }} {{ item.label }}
            </div>
          </div>
        </div>
        <div class="form-group">
          <label>{{ t('users.modal.create.confirmPassword') }} *</label>
          <input v-model="createConfirmPwd" type="password" :placeholder="t('users.modal.create.confirmPasswordPlaceholder')" />
          <div v-if="createConfirmPwd" class="password-confirm" :class="createPasswordsMatch ? 'is-pass' : 'is-error'">
            {{ createPasswordsMatch ? t('users.passwordMatch.matched') : t('users.passwordMatch.mismatch') }}
          </div>
        </div>
        <div class="form-group">
          <label>{{ t('users.modal.create.displayName') }}</label>
          <input v-model="form.display_name" />
        </div>
        <div class="form-group">
          <label>{{ t('users.modal.create.email') }}</label>
          <input v-model="form.email" type="email" />
        </div>
        <div class="form-group">
          <label>{{ t('users.modal.create.tenant') }} *</label>
          <!-- 必填项：租户列表加载失败时下拉是空的，表单无法提交。
               不给提示的话，用户只会看到「required」校验失败，去检查自己填的字段。 -->
          <p v-if="tenantsError" class="filter-error" role="status">
            ⚠️ {{ t('users.error.tenantsLoadFailed') }}（{{ tenantsError }}）
          </p>
          <select v-model="form.tenant_id" required>
            <option v-for="t in allTenants" :key="t.code" :value="t.code">
              {{ t.name }} ({{ t.code }}) - {{ t.status }}
            </option>
          </select>
        </div>
        <div class="form-group">
          <label>{{ t('users.modal.create.role') }}</label>
          <!-- 角色下拉按**所选租户**收敛：非 default 租户不给「超级管理员」
               （平台级身份不属于业务租户），只给租户管理员 / 普通用户。 -->
          <select v-model="form.role">
            <option v-for="r in createRoleOptions" :key="r" :value="r">{{ roleLabel(r) }}</option>
          </select>
          <p v-if="form.tenant_id !== DEFAULT_TENANT_CODE" class="form-hint">{{ t('users.modal.create.roleHint') }}</p>
        </div>
        <div class="modal-actions">
          <button class="btn btn-primary" :disabled="!form.username || !createPasswordPolicy.valid || !passwordsMatch(form.password, createConfirmPwd)" @click="handleCreate">{{ t('users.modal.create.submit') }}</button>
          <button class="btn btn-ghost" @click="closeCreateModal">{{ t('users.modal.create.cancel') }}</button>
        </div>
      </div>
    </div>

    <!-- Role Change Modal（2026-11-26 角色治理轮） -->
    <div v-if="showRoleModal && roleTarget" class="modal-backdrop" @click.self="showRoleModal = false">
      <div class="modal-card">
        <h3>{{ t('users.modal.role.title', { name: roleTarget.username }) }}</h3>
        <div class="form-group">
          <label>{{ t('users.modal.role.current') }}</label>
          <div class="current-role">
            <span class="badge" :class="roleTarget.role === ROLE_SUPER_ADMIN ? 'badge-purple' : 'badge-blue'">{{ roleLabel(roleTarget.role) }}</span>
            <span class="cell-sub">{{ roleTarget.tenant_id }}</span>
          </div>
        </div>
        <div class="form-group">
          <label>{{ t('users.modal.role.label') }}</label>
          <select v-model="roleDraft" data-testid="user-role-select">
            <option v-for="r in roleOptions" :key="r" :value="r">{{ roleLabel(r) }}</option>
          </select>
          <p v-if="roleTarget.tenant_id !== DEFAULT_TENANT_CODE" class="form-hint">{{ t('users.modal.role.hint') }}</p>
        </div>
        <div class="modal-actions">
          <button class="btn btn-primary" data-testid="user-role-save" :disabled="roleSaving || roleDraft === roleTarget.role" @click="submitRoleChange">{{ t('common.save') }}</button>
          <button class="btn btn-ghost" @click="showRoleModal = false">{{ t('common.cancel') }}</button>
        </div>
      </div>
    </div>

    <!-- Reset Password Modal -->
    <div v-if="resetPwdUser" class="modal-backdrop" @click.self="closeResetModal">
      <div class="modal-card">
        <h3>{{ t('users.modal.reset.title', { name: resetPwdUser.username }) }}</h3>
        <div class="form-group">
          <label>{{ t('users.modal.reset.newPassword') }}</label>
          <input v-model="newPwd" type="password" :placeholder="t('users.modal.reset.passwordPlaceholder')" />
          <div class="password-policy">
            <div
              v-for="item in resetPasswordPolicy.requirements"
              :key="item.key"
              class="password-policy__item"
              :class="item.passed ? 'is-pass' : 'is-pending'"
            >
              {{ item.passed ? '✓' : '○' }} {{ item.label }}
            </div>
          </div>
        </div>
        <div class="form-group">
          <label>{{ t('users.modal.reset.confirmPassword') }}</label>
          <input v-model="resetConfirmPwd" type="password" :placeholder="t('users.modal.reset.confirmPasswordPlaceholder')" />
          <div v-if="resetConfirmPwd" class="password-confirm" :class="resetPasswordsMatch ? 'is-pass' : 'is-error'">
            {{ resetPasswordsMatch ? t('users.passwordMatch.matched') : t('users.passwordMatch.resetMismatch') }}
          </div>
        </div>
        <div class="modal-actions">
          <button class="btn btn-primary" :disabled="!resetPasswordPolicy.valid || !passwordsMatch(newPwd, resetConfirmPwd)" @click="handleResetPwd">{{ t('users.modal.reset.submit') }}</button>
          <button class="btn btn-ghost" @click="closeResetModal">{{ t('users.modal.reset.cancel') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>

.badge-purple { background: color-mix(in srgb, var(--accent) 15%, transparent); color: var(--accent-h); }
.badge-blue { background: var(--info-bg); color: var(--accent); }
.badge-green { background: var(--success-bg); color: var(--success); }
.badge-red { background: var(--danger-bg); color: var(--danger); }
.badge-yellow { background: var(--warning-bg); color: var(--warning); }

.modal-backdrop {
  position: fixed;
  inset: 0;
  background: var(--overlay-strong);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}
.modal-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 24px;
  width: 400px;
  max-height: 90vh;
  overflow-y: auto;
}
.modal-card h3 { margin: 0 0 16px; font-size: 16px; }
.modal-actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 16px; }

/* 角色下拉的约束说明：说清「为什么这里没有超级管理员」。 */
.form-hint {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--muted);
}

.current-role {
  display: flex;
  align-items: center;
  gap: 8px;
}

/* 2026-07-07: 过滤器行，select 限制最大宽度避免全屏拉伸；
 * 2026-09-30 增加角色/状态/排序/搜索（统计 UI 优化轮）。 */
.filters {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}
.filters select {
  width: auto;
  max-width: 200px;
}

/* 加载失败提示用 warning 而非 danger：不是故障，是「这个列表的范围未知」。 */
.filter-error {
  margin: 0;
  color: color-mix(in srgb, var(--warning) 80%, var(--text));
  font-size: 12px;
}
.search-input {
  width: 220px;
  max-width: 100%;
}

/* 顶部统计条（2026-09-30） */
.mini-stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 0;
  margin-bottom: 12px;
  border: 1px solid var(--border);
  border-radius: 12px;
  overflow: hidden;
  background: var(--card);
}
.mini-stat { padding: 8px 12px; border-right: 1px solid var(--border); min-width: 0; }
.mini-stat:last-child { border-right: none; }
.mini-stat .v { font-size: 16px; font-weight: 700; font-variant-numeric: tabular-nums; }
.mini-stat .v-ok { color: var(--success); }
.mini-stat .v-err { color: var(--danger); }
.mini-stat .l { font-size: 11px; color: var(--muted); }

.col-num { text-align: right; font-variant-numeric: tabular-nums; }
.cell-sub { font-size: 11px; color: var(--muted); }
.mono-time { font-variant-numeric: tabular-nums; }
.txt-ok { color: var(--success); font-size: 12px; }
.txt-muted { color: var(--muted); font-size: 12px; }
.row-click { cursor: pointer; }

.password-policy {
  display: grid;
  gap: 4px;
  margin-top: 8px;
}

.password-policy__item {
  font-size: 12px;
  line-height: 1.4;
}

.password-confirm {
  margin-top: 8px;
  font-size: 12px;
  line-height: 1.4;
}

.is-pass {
  color: var(--success);
}

.is-pending {
  color: var(--muted);
}

.is-error {
  color: var(--danger);
}
</style>
