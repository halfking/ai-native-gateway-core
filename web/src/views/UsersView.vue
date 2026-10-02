<script setup lang="ts">
// UsersView.vue — 用户管理（2026-09-30 统计 UI 优化轮增强）
// 新增：顶部统计条 / 搜索+角色+状态筛选 / 近 30 天用量列（请求·Token·积分，
// 占比条） / 点击行打开用户详情抽屉（UserDetailDrawer）。
// 用量数据 GET /api/admin/users/usage-summary（usage_facts × api_keys.owner_user，
// 与对账快照同源）；无用量账号显示 —，列表加载不被用量失败阻塞。
import { useI18n } from 'vue-i18n'
import { fmtDateTime24h } from '../i18n/useFormat'
import { sortByName } from '../utils/sortByName'
import { ref, computed, onMounted } from 'vue'
import { getUsers, createUser, updateUser, deleteUser, resetUserPassword, getTenantsAdmin, getUserUsageSummary } from '../api'
import type { Tenant, UserUsageSummaryItem } from '../api'
import { store, isReadOnlyMode, isTenantAdmin } from '../store'
import { checkPasswordPolicy, passwordsMatch } from '../utils/passwordPolicy'
import { confirmDialog } from '../composables/useConfirmDialog'
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
const usageByUser = computed<Map<string, UserUsageSummaryItem>>(() => {
  const m = new Map<string, UserUsageSummaryItem>()
  for (const it of usageItems.value) m.set(it.username, it)
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
      const ua = usageByUser.value.get(a.username)
      const ub = usageByUser.value.get(b.username)
      const va = sortBy.value === 'requests' ? ua?.requests ?? 0 : ua?.credits ?? 0
      const vb = sortBy.value === 'requests' ? ub?.requests ?? 0 : ub?.credits ?? 0
      return vb - va
    })
  }
  return list
})

const maxRequests = computed(() =>
  filteredUsers.value.reduce((m, u) => Math.max(m, usageByUser.value.get(u.username)?.requests ?? 0), 0),
)

// 顶部统计条：基于当前筛选结果 + 用量表
const statBar = computed(() => {
  const list = filteredUsers.value
  let active = 0
  let req = 0
  let credits = 0
  for (const u of list) {
    const usage = usageByUser.value.get(u.username)
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
  return usageByUser.value.get(u.username)
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
  if (!iso) return t('users.usage.never', '从未活跃')
  const diff = Date.now() - new Date(iso).getTime()
  const min = Math.floor(diff / 60000)
  if (min < 1) return t('users.usage.justNow', '刚刚')
  if (min < 60) return `${min} ${t('users.usage.minAgo', '分钟前')}`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr} ${t('users.usage.hourAgo', '小时前')}`
  const day = Math.floor(hr / 24)
  return `${day} ${t('users.usage.dayAgo', '天前')}`
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
      usageItems.value = summary.items ?? []
    } catch {
      usageItems.value = []
    }
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('users.error.loadFailed')
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
    error.value = e instanceof Error ? e.message : t('users.error.createFailed')
  }
}

async function handleToggle(u: User, onRollback?: () => void) {
  try {
    await updateUser(u.id, { enabled: !u.enabled })
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('users.error.toggleFailed')
    onRollback?.()
  }
}

async function handleDelete(u: User) {
  if (!(await confirmDialog(t('users.confirmDelete', { name: u.username })))) return
  try {
    await deleteUser(u.id)
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('users.error.deleteFailed')
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
    error.value = e instanceof Error ? e.message : t('users.error.resetFailed')
  }
}

function roleLabel(r: string) {
  return r === 'super_admin' ? t('users.role.super_admin') : t('users.role.tenant_admin')
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

async function loadTenants() {
  try {
    allTenants.value = await getTenantsAdmin()
  } catch { /* ignore */ }
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
      <div class="mini-stat"><div class="v">{{ statBar.total }}</div><div class="l">{{ t('users.stats.total', '总用户') }}</div></div>
      <div class="mini-stat"><div class="v v-ok">{{ statBar.active }}</div><div class="l">{{ t('users.stats.active', '活跃（30 天内有请求）') }}</div></div>
      <div class="mini-stat"><div class="v">{{ statBar.admins }}</div><div class="l">{{ t('users.stats.admins', '管理员') }}</div></div>
      <div class="mini-stat"><div class="v v-err">{{ statBar.disabled }}</div><div class="l">{{ t('users.stats.disabled', '已禁用') }}</div></div>
      <div class="mini-stat"><div class="v">{{ fmtNum(statBar.req) }}</div><div class="l">{{ t('users.stats.requests30d', '近 30 天请求') }}</div></div>
      <div class="mini-stat"><div class="v">{{ fmtNum(statBar.credits) }}</div><div class="l">{{ t('users.stats.credits30d', '近 30 天积分消耗') }}</div></div>
    </div>

    <div class="filters">
      <label>{{ t('users.filter.byTenant', '租户') }}</label>
      <select v-model="filterTenant">
        <option value="">{{ t('users.filter.allTenants') }}</option>
        <option v-for="t in allTenants" :key="t.code" :value="t.code">
          {{ t.name }} ({{ t.code }})
        </option>
      </select>
      <select v-model="filterRole">
        <option value="">{{ t('users.filter.allRoles', '全部角色') }}</option>
        <option value="tenant_admin">{{ t('users.role.tenant_admin') }}</option>
        <option value="super_admin">{{ t('users.role.super_admin') }}</option>
      </select>
      <select v-model="filterStatus">
        <option value="">{{ t('users.filter.allStatus', '全部状态') }}</option>
        <option value="enabled">{{ t('users.status.enabled') }}</option>
        <option value="disabled">{{ t('users.status.disabled') }}</option>
      </select>
      <select v-model="sortBy">
        <option value="default">{{ t('users.filter.sortDefault', '默认排序') }}</option>
        <option value="requests">{{ t('users.filter.sortRequests', '按近 30 天请求') }}</option>
        <option value="credits">{{ t('users.filter.sortCredits', '按近 30 天积分') }}</option>
      </select>
      <input v-model="search" class="search-input" :placeholder="t('users.filter.searchPlaceholder', '搜索用户名 / 显示名 / 邮箱…')">
    </div>

    <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>

    <div v-if="loading" class="loading">{{ t('users.loading') }}</div>

    <DataTable v-else min-width="980px">
    <table class="table" style="width:100%">
      <thead>
        <tr>
          <th>{{ t('users.table.username', '用户') }}</th>
          <th>{{ t('users.table.email', '邮箱') }}</th>
          <th>{{ t('users.table.tenant', '租户') }}</th>
          <th>{{ t('users.table.role', '角色') }}</th>
          <th>{{ t('users.table.status', '状态') }}</th>
          <th class="col-num">{{ t('users.usage.requests30d', '近 30 天请求') }}</th>
          <th class="col-num">Token</th>
          <th class="col-num">{{ t('users.usage.credits30d', '近 30 天积分') }}</th>
          <th>{{ t('users.table.lastLogin', '最后登录') }}</th>
          <th>{{ t('users.usage.lastActive', '最后活跃') }}</th>
          <th>{{ t('users.table.actions', '操作') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="u in filteredUsers" :key="u.id" class="row-click" @click="openDetail(u)">
          <td>
            <BarCell :pct="maxRequests > 0 ? ((usageOf(u)?.requests ?? 0) / maxRequests) * 100 : 0">
              <template #name>
                <strong>{{ u.username }}</strong>
                <span class="cell-sub">{{ u.display_name || '-' }}</span>
                <span v-if="u.must_change_password" class="badge badge-yellow">{{ t('users.mustChangePassword.pending', '待改密') }}</span>
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
            <button class="btn btn-ghost btn-sm" @click="openDetail(u)">{{ t('users.action.detail', '详情') }}</button>
            <button v-if="canResetPasswords" class="btn btn-ghost btn-sm" @click="resetPwdUser = u; newPwd = ''; resetConfirmPwd = ''">{{ t('users.action.resetPassword') }}</button>
            <button v-if="canDeleteUsers && u.id !== store.userInfo?.id" class="btn btn-ghost btn-sm" style="color:var(--danger)" @click="handleDelete(u)">{{ t('users.action.delete') }}</button>
          </td>
        </tr>
        <tr v-if="filteredUsers.length === 0">
          <td colspan="11" style="text-align:center; padding:32px; color: var(--muted)">{{ t('users.empty', '无匹配用户') }}</td>
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
          <select v-model="form.tenant_id" required>
            <option v-for="t in allTenants" :key="t.code" :value="t.code">
              {{ t.name }} ({{ t.code }}) - {{ t.status }}
            </option>
          </select>
        </div>
        <div class="form-group">
          <label>{{ t('users.modal.create.role') }}</label>
          <select v-model="form.role">
            <option value="tenant_admin">{{ t('users.role.tenant_admin') }}</option>
            <option value="super_admin">{{ t('users.role.super_admin') }}</option>
          </select>
        </div>
        <div class="modal-actions">
          <button class="btn btn-primary" :disabled="!form.username || !createPasswordPolicy.valid || !passwordsMatch(form.password, createConfirmPwd)" @click="handleCreate">{{ t('users.modal.create.submit') }}</button>
          <button class="btn btn-ghost" @click="closeCreateModal">{{ t('users.modal.create.cancel') }}</button>
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
.search-input {
  width: 220px;
  max-width: 100%;
}

/* 顶部统计条（2026-09-30） */
.mini-stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 0;
  margin-bottom: 14px;
  border: 1px solid var(--border);
  border-radius: 12px;
  overflow: hidden;
  background: var(--card);
}
.mini-stat { padding: 10px 14px; border-right: 1px solid var(--border); min-width: 0; }
.mini-stat:last-child { border-right: none; }
.mini-stat .v { font-size: 18px; font-weight: 700; font-variant-numeric: tabular-nums; }
.mini-stat .v-ok { color: var(--success); }
.mini-stat .v-err { color: var(--danger); }
.mini-stat .l { font-size: 11px; color: var(--muted); }

.col-num { text-align: right; font-variant-numeric: tabular-nums; }
.cell-sub { font-size: 11.5px; color: var(--muted); }
.mono-time { font-variant-numeric: tabular-nums; }
.txt-ok { color: var(--success); font-size: 12.5px; }
.txt-muted { color: var(--muted); font-size: 12.5px; }
.row-click { cursor: pointer; }

.password-policy {
  display: grid;
  gap: 6px;
  margin-top: 10px;
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
