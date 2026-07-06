<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ref, computed, onMounted } from 'vue'
import { getUsers, createUser, updateUser, deleteUser, resetUserPassword, getTenantsAdmin } from '../api'
import type { Tenant } from '../api'
import { store, isReadOnlyMode, isTenantAdmin } from '../store'
import { checkPasswordPolicy, passwordsMatch } from '../utils/passwordPolicy'

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
const allTenants = ref<Tenant[]>([])
const newPwd = ref('')
const createConfirmPwd = ref('')
const resetConfirmPwd = ref('')
const createPasswordPolicy = computed(() => checkPasswordPolicy(form.value.password))
const resetPasswordPolicy = computed(() => checkPasswordPolicy(newPwd.value))
const createPasswordsMatch = computed(() => !createConfirmPwd.value || passwordsMatch(form.value.password, createConfirmPwd.value))
const resetPasswordsMatch = computed(() => !resetConfirmPwd.value || passwordsMatch(newPwd.value, resetConfirmPwd.value))

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
    users.value = await getUsers()
    // Filter by tenant if set
    if (filterTenant.value) {
      users.value = users.value.filter(u => u.tenant_id === filterTenant.value)
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

async function handleToggle(u: User) {
  try {
    await updateUser(u.id, { enabled: !u.enabled })
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('users.error.toggleFailed')
  }
}

async function handleDelete(u: User) {
  if (!confirm(t('users.confirmDelete', { name: u.username }))) return
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

function fmtDate(s: string | null) {
  if (!s) return '-'
  return new Date(s).toLocaleString('zh-CN')
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
    <div class="page-header">
      <h1>👤 {{ t('users.title') }}</h1>
      <button v-if="canCreateUsers" class="btn btn-primary" @click="showCreate = true">+ {{ t('users.create') }}</button>
    </div>

    <div v-if="readOnly" class="alert alert-info" style="margin-bottom:12px">
      {{ t('users.readOnlyNotice') }}
    </div>

    <div class="filters">
      <label>{{ t('users.filter.byTenant') }}</label>
      <select v-model="filterTenant" @change="load">
        <option value="">{{ t('users.filter.allTenants') }}</option>
        <option v-for="t in allTenants" :key="t.code" :value="t.code">
          {{ t.name }} ({{ t.code }})
        </option>
      </select>
    </div>

    <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>

    <div v-if="loading" class="loading">{{ t('users.loading') }}</div>

    <table v-else class="table" style="width:100%">
      <thead>
        <tr>
          <th>{{ t('users.table.id') }}</th>
          <th>{{ t('users.table.username') }}</th>
          <th>{{ t('users.table.displayName') }}</th>
          <th>{{ t('users.table.email') }}</th>
          <th>{{ t('users.table.tenant') }}</th>
          <th>{{ t('users.table.role') }}</th>
          <th>{{ t('users.table.mustChangePassword') }}</th>
          <th>{{ t('users.table.status') }}</th>
          <th>{{ t('users.table.lastLogin') }}</th>
          <th>{{ t('users.table.actions') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="u in users" :key="u.id">
          <td>{{ u.id }}</td>
          <td><strong>{{ u.username }}</strong></td>
          <td>{{ u.display_name || '-' }}</td>
          <td>{{ u.email || '-' }}</td>
          <td><code>{{ u.tenant_id }}</code></td>
          <td><span class="badge" :class="u.role === 'super_admin' ? 'badge-purple' : 'badge-blue'">{{ roleLabel(u.role) }}</span></td>
          <td>
            <span class="badge" :class="u.must_change_password ? 'badge-yellow' : 'badge-green'">
              {{ u.must_change_password ? t('users.mustChangePassword.pending') : t('users.mustChangePassword.completed') }}
            </span>
          </td>
          <td>
            <span v-if="!readOnly" class="badge" :class="u.enabled ? 'badge-green' : 'badge-red'" style="cursor:pointer" @click="handleToggle(u)">
              {{ u.enabled ? t('users.status.enabled') : t('users.status.disabled') }}
            </span>
            <span v-else class="badge" :class="u.enabled ? 'badge-green' : 'badge-red'">
              {{ u.enabled ? t('users.status.enabled') : t('users.status.disabled') }}
            </span>
          </td>
          <td>{{ fmtDate(u.last_login_at) }}</td>
          <td>
            <button v-if="canResetPasswords" class="btn btn-ghost btn-sm" @click="resetPwdUser = u; newPwd = ''; resetConfirmPwd = ''">{{ t('users.action.resetPassword') }}</button>
            <button v-if="canDeleteUsers && u.id !== store.userInfo?.id" class="btn btn-ghost btn-sm" style="color:var(--danger)" @click="handleDelete(u)">{{ t('users.action.delete') }}</button>
            <span v-else-if="readOnly" class="text-muted" style="font-size:12px;color:var(--muted)">—</span>
          </td>
        </tr>
      </tbody>
    </table>

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
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
}
.page-header h1 { font-size: 20px; margin: 0; }

.badge-purple { background: rgba(139,92,246,.15); color: #a78bfa; }
.badge-blue { background: rgba(59,130,246,.15); color: #60a5fa; }
.badge-green { background: rgba(34,197,94,.15); color: #4ade80; }
.badge-red { background: rgba(239,68,68,.15); color: #f87171; }
.badge-yellow { background: rgba(234,179,8,.15); color: #facc15; }

.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0,0,0,.5);
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
  color: #4ade80;
}

.is-pending {
  color: var(--muted);
}

.is-error {
  color: #f87171;
}
</style>
