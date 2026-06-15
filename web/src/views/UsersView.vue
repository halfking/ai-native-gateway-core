<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { getUsers, createUser, updateUser, deleteUser, resetUserPassword } from '../api'
import { store } from '../store'

interface User {
  id: number
  tenant_id: string
  username: string
  display_name: string
  email: string
  role: string
  enabled: boolean
  last_login_at: string | null
  created_at: string
}

const users = ref<User[]>([])
const loading = ref(false)
const error = ref('')
const showCreate = ref(false)
const editUser = ref<User | null>(null)
const resetPwdUser = ref<User | null>(null)
const newPwd = ref('')

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
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function handleCreate() {
  if (!form.value.username || !form.value.password) {
    error.value = '用户名和密码不能为空'
    return
  }
  try {
    await createUser(form.value)
    showCreate.value = false
    form.value = { username: '', password: '', tenant_id: 'default', display_name: '', email: '', role: 'tenant_admin' }
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '创建失败'
  }
}

async function handleToggle(u: User) {
  try {
    await updateUser(u.id, { enabled: !u.enabled })
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '操作失败'
  }
}

async function handleDelete(u: User) {
  if (!confirm(`确认删除用户 ${u.username}？`)) return
  try {
    await deleteUser(u.id)
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '删除失败'
  }
}

async function handleResetPwd() {
  if (!resetPwdUser.value || newPwd.value.length < 8) {
    error.value = '密码至少8个字符'
    return
  }
  try {
    await resetUserPassword(resetPwdUser.value.id, newPwd.value)
    resetPwdUser.value = null
    newPwd.value = ''
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '重置失败'
  }
}

function roleLabel(r: string) {
  return r === 'super_admin' ? '超级管理员' : '租户管理员'
}

function fmtDate(s: string | null) {
  if (!s) return '-'
  return new Date(s).toLocaleString('zh-CN')
}

onMounted(load)
</script>

<template>
  <div class="users-page">
    <div class="page-header">
      <h1>👤 用户管理</h1>
      <button class="btn btn-primary" @click="showCreate = true">+ 新建用户</button>
    </div>

    <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>

    <div v-if="loading" class="loading">加载中…</div>

    <table v-else class="table" style="width:100%">
      <thead>
        <tr>
          <th>ID</th>
          <th>用户名</th>
          <th>显示名</th>
          <th>邮箱</th>
          <th>租户</th>
          <th>角色</th>
          <th>状态</th>
          <th>最后登录</th>
          <th>操作</th>
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
            <span class="badge" :class="u.enabled ? 'badge-green' : 'badge-red'" style="cursor:pointer" @click="handleToggle(u)">
              {{ u.enabled ? '启用' : '禁用' }}
            </span>
          </td>
          <td>{{ fmtDate(u.last_login_at) }}</td>
          <td>
            <button class="btn btn-ghost btn-sm" @click="resetPwdUser = u; newPwd = ''">重置密码</button>
            <button v-if="u.id !== store.userInfo?.id" class="btn btn-ghost btn-sm" style="color:var(--danger)" @click="handleDelete(u)">删除</button>
          </td>
        </tr>
      </tbody>
    </table>

    <!-- Create Modal -->
    <div v-if="showCreate" class="modal-backdrop" @click.self="showCreate = false">
      <div class="modal-card">
        <h3>新建用户</h3>
        <div class="form-group">
          <label>用户名 *</label>
          <input v-model="form.username" placeholder="username" />
        </div>
        <div class="form-group">
          <label>密码 *</label>
          <input v-model="form.password" type="password" placeholder="至少8位" />
        </div>
        <div class="form-group">
          <label>显示名</label>
          <input v-model="form.display_name" />
        </div>
        <div class="form-group">
          <label>邮箱</label>
          <input v-model="form.email" type="email" />
        </div>
        <div class="form-group">
          <label>租户 ID</label>
          <input v-model="form.tenant_id" />
        </div>
        <div class="form-group">
          <label>角色</label>
          <select v-model="form.role">
            <option value="tenant_admin">租户管理员</option>
            <option value="super_admin">超级管理员</option>
          </select>
        </div>
        <div class="modal-actions">
          <button class="btn btn-primary" @click="handleCreate">创建</button>
          <button class="btn btn-ghost" @click="showCreate = false">取消</button>
        </div>
      </div>
    </div>

    <!-- Reset Password Modal -->
    <div v-if="resetPwdUser" class="modal-backdrop" @click.self="resetPwdUser = null">
      <div class="modal-card">
        <h3>重置密码 — {{ resetPwdUser.username }}</h3>
        <div class="form-group">
          <label>新密码</label>
          <input v-model="newPwd" type="password" placeholder="至少8位" />
        </div>
        <div class="modal-actions">
          <button class="btn btn-primary" @click="handleResetPwd">确认</button>
          <button class="btn btn-ghost" @click="resetPwdUser = null">取消</button>
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
</style>
