<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getTenant, getTenantUsers, getTenantKeys, getTenantStats, getUsers, updateUser, TENANT_STATUS_LABELS, TENANT_STATUS_COLORS } from '../api'
import type { Tenant, TenantUser, TenantKey, TenantStats, UserListItem } from '../api'
import TenantEditDialog from './TenantEditDialog.vue'

const route = useRoute()
const router = useRouter()
const tenantCode = computed(() => String(route.params.tenantId))

const tenant = ref<Tenant | null>(null)
const users = ref<TenantUser[]>([])
const keys = ref<TenantKey[]>([])
const stats = ref<TenantStats | null>(null)
const loading = ref(false)
const error = ref('')
const activeTab = ref<'overview' | 'users' | 'keys' | 'stats'>('overview')
const statsDays = ref(7)
const showEdit = ref(false)

async function loadTenant() {
  loading.value = true
  error.value = ''
  try {
    tenant.value = await getTenant(tenantCode.value)
    if (activeTab.value === 'users') await loadUsers()
    if (activeTab.value === 'keys') await loadKeys()
    if (activeTab.value === 'stats') await loadStats()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function loadUsers() {
  try {
    users.value = await getTenantUsers(tenantCode.value)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载用户失败'
  }
}

async function loadKeys() {
  try {
    keys.value = await getTenantKeys(tenantCode.value)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载密钥失败'
  }
}

async function loadStats() {
  try {
    stats.value = await getTenantStats(tenantCode.value, statsDays.value)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载统计失败'
  }
}

async function switchTab(t: 'overview' | 'users' | 'keys' | 'stats') {
  activeTab.value = t
  if (t === 'users' && users.value.length === 0) await loadUsers()
  if (t === 'keys' && keys.value.length === 0) await loadKeys()
  if (t === 'stats' && !stats.value) await loadStats()
}

async function toggleUserEnabled(u: TenantUser) {
  try {
    await updateUser(u.id, { enabled: !u.enabled })
    await loadUsers()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '操作失败'
  }
}

function openEdit() {
  showEdit.value = true
}

function statusColor(s: string) {
  return TENANT_STATUS_COLORS[s] || 'badge-gray'
}

function statusLabel(s: string) {
  return TENANT_STATUS_LABELS[s] || s
}

function fmtTime(s: string) {
  if (!s) return '-'
  return new Date(s).toLocaleString('zh-CN')
}

function fmtCost(n?: number) {
  if (n == null) return '-'
  return '$' + n.toFixed(2)
}

function fmtNum(n?: number) {
  if (n == null) return '-'
  return n.toLocaleString()
}

onMounted(loadTenant)
watch(() => route.params.tenantId, loadTenant)
</script>

<template>
  <div class="tenant-detail">
    <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>

    <div v-if="loading && !tenant" class="loading">加载中…</div>

    <div v-else-if="tenant">
      <!-- Header -->
      <div class="tenant-header">
        <button class="btn-back" @click="router.push('/tenants')">← 返回</button>
        <div class="header-main">
          <h1>
            <strong>{{ tenant.name }}</strong>
            <span class="badge" :class="statusColor(tenant.status)">{{ statusLabel(tenant.status) }}</span>
          </h1>
          <div class="header-meta">
            <code class="code-badge">{{ tenant.code }}</code>
            <span v-if="tenant.contact_email">📧 {{ tenant.contact_email }}</span>
            <span>🕐 {{ fmtTime(tenant.created_at) }}</span>
          </div>
          <p v-if="tenant.description" class="description">{{ tenant.description }}</p>
        </div>
        <button class="btn btn-primary" @click="openEdit">编辑</button>
      </div>

      <!-- Tabs -->
      <div class="tabs">
        <button :class="{ active: activeTab === 'overview' }" @click="switchTab('overview')">概览</button>
        <button :class="{ active: activeTab === 'users' }" @click="switchTab('users')">用户 ({{ tenant.user_count }})</button>
        <button :class="{ active: activeTab === 'keys' }" @click="switchTab('keys')">密钥 ({{ tenant.api_key_count }})</button>
        <button :class="{ active: activeTab === 'stats' }" @click="switchTab('stats')">统计</button>
      </div>

      <!-- Overview Tab -->
      <div v-if="activeTab === 'overview'" class="tab-content">
        <div class="stat-cards">
          <div class="stat-card">
            <div class="stat-label">用户数</div>
            <div class="stat-value">{{ tenant.user_count }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">密钥数</div>
            <div class="stat-value">{{ tenant.api_key_count }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">7天请求数</div>
            <div class="stat-value">{{ fmtNum(tenant.requests_7d) }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">7天 Token</div>
            <div class="stat-value">{{ fmtNum(tenant.tokens_7d) }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">7天费用</div>
            <div class="stat-value">{{ fmtCost(tenant.cost_7d_usd) }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">总请求数</div>
            <div class="stat-value">{{ fmtNum(tenant.total_requests) }}</div>
          </div>
        </div>
      </div>

      <!-- Users Tab -->
      <div v-if="activeTab === 'users'" class="tab-content">
        <table class="table" style="width:100%">
          <thead>
            <tr><th>ID</th><th>用户名</th><th>显示名</th><th>邮箱</th><th>角色</th><th>状态</th><th>最后登录</th><th>操作</th></tr>
          </thead>
          <tbody>
            <tr v-for="u in users" :key="u.id">
              <td>{{ u.id }}</td>
              <td><strong>{{ u.username }}</strong></td>
              <td>{{ u.display_name || '-' }}</td>
              <td>{{ u.email || '-' }}</td>
              <td><span class="badge" :class="u.role === 'super_admin' ? 'badge-purple' : 'badge-blue'">{{ u.role }}</span></td>
              <td><span class="badge" :class="u.enabled ? 'badge-green' : 'badge-red'">{{ u.enabled ? '启用' : '禁用' }}</span></td>
              <td class="mono">{{ fmtTime(u.last_login_at || '') }}</td>
              <td>
                <button class="btn btn-ghost btn-sm" @click="toggleUserEnabled(u)">
                  {{ u.enabled ? '禁用' : '启用' }}
                </button>
              </td>
            </tr>
            <tr v-if="users.length === 0">
              <td colspan="8" style="text-align:center; padding:40px; color: var(--muted)">无用户</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Keys Tab -->
      <div v-if="activeTab === 'keys'" class="tab-content">
        <table class="table" style="width:100%">
          <thead>
            <tr><th>ID</th><th>密钥前缀</th><th>别名</th><th>应用</th><th>状态</th><th>请求数</th><th>费用</th><th>创建</th></tr>
          </thead>
          <tbody>
            <tr v-for="k in keys" :key="k.id">
              <td>{{ k.id }}</td>
              <td><code>{{ k.key_prefix }}</code></td>
              <td>{{ k.key_alias || '-' }}</td>
              <td><span class="badge badge-blue">{{ k.application_code || '-' }}</span></td>
              <td><span class="badge" :class="k.enabled ? 'badge-green' : 'badge-red'">{{ k.enabled ? '启用' : '禁用' }}</span></td>
              <td>{{ fmtNum(k.total_requests) }}</td>
              <td>{{ fmtCost(k.total_cost_usd) }}</td>
              <td class="mono">{{ fmtTime(k.created_at) }}</td>
            </tr>
            <tr v-if="keys.length === 0">
              <td colspan="8" style="text-align:center; padding:40px; color: var(--muted)">无密钥</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Stats Tab -->
      <div v-if="activeTab === 'stats'" class="tab-content">
        <div class="stats-toolbar">
          <label>时间窗口:</label>
          <select v-model.number="statsDays" @change="loadStats">
            <option :value="7">近 7 天</option>
            <option :value="30">近 30 天</option>
            <option :value="90">近 90 天</option>
            <option :value="365">近 365 天</option>
          </select>
        </div>

        <div v-if="stats" class="stat-cards">
          <div class="stat-card">
            <div class="stat-label">总请求</div>
            <div class="stat-value">{{ fmtNum(stats.total_requests) }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">总 Token</div>
            <div class="stat-value">{{ fmtNum(stats.total_tokens) }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">总费用</div>
            <div class="stat-value">{{ fmtCost(stats.total_cost_usd) }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">独立密钥</div>
            <div class="stat-value">{{ stats.unique_keys }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">独立模型</div>
            <div class="stat-value">{{ stats.unique_models }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">独立应用</div>
            <div class="stat-value">{{ stats.unique_apps }}</div>
          </div>
        </div>

        <div v-if="stats" class="stats-tables">
          <h3>按模型分桶 (Top 20)</h3>
          <table class="table">
            <thead><tr><th>模型</th><th>请求</th><th>Token</th><th>费用</th></tr></thead>
            <tbody>
              <tr v-for="m in stats.by_model" :key="m.model">
                <td><code>{{ m.model }}</code></td>
                <td>{{ fmtNum(m.requests) }}</td>
                <td>{{ fmtNum(m.tokens) }}</td>
                <td>{{ fmtCost(m.cost_usd) }}</td>
              </tr>
            </tbody>
          </table>

          <h3>按应用分桶 (Top 20)</h3>
          <table class="table">
            <thead><tr><th>应用</th><th>请求</th><th>Token</th><th>费用</th></tr></thead>
            <tbody>
              <tr v-for="a in stats.by_application" :key="a.application_code">
                <td><span class="badge badge-blue">{{ a.application_code }}</span></td>
                <td>{{ fmtNum(a.requests) }}</td>
                <td>{{ fmtNum(a.tokens) }}</td>
                <td>{{ fmtCost(a.cost_usd) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <TenantEditDialog v-if="showEdit && tenant" :tenant="tenant" @close="showEdit = false; loadTenant()" @updated="loadTenant" />
  </div>
</template>

<style scoped>
.tenant-header {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 16px;
  padding: 16px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
}
.btn-back {
  padding: 6px 12px;
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  cursor: pointer;
  font-size: 13px;
}
.btn-back:hover { background: rgba(255,255,255,.05); }
.header-main { flex: 1; }
.header-main h1 {
  font-size: 22px;
  margin: 0 0 8px;
  display: flex;
  align-items: center;
  gap: 12px;
}
.header-meta {
  display: flex;
  gap: 16px;
  font-size: 12px;
  color: var(--muted);
  align-items: center;
}
.code-badge {
  background: var(--bg);
  padding: 2px 8px;
  border-radius: 4px;
  font-family: 'SF Mono', 'Fira Code', monospace;
}
.description {
  font-size: 13px;
  color: var(--text);
  margin: 8px 0 0;
}

.tabs {
  display: flex;
  border-bottom: 1px solid var(--border);
  margin-bottom: 16px;
}
.tabs button {
  padding: 8px 16px;
  background: transparent;
  border: none;
  color: var(--muted);
  cursor: pointer;
  font-size: 13px;
  border-bottom: 2px solid transparent;
}
.tabs button.active {
  color: var(--accent-h);
  border-bottom-color: var(--accent-h);
}

.tab-content {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;
}

.stat-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
.stat-card {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;
  text-align: center;
}
.stat-label { font-size: 12px; color: var(--muted); margin-bottom: 6px; }
.stat-value { font-size: 22px; font-weight: 600; color: var(--text); }

.badge-purple { background: rgba(139,92,246,.15); color: #a78bfa; padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-blue { background: rgba(59,130,246,.15); color: #60a5fa; padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-red { background: rgba(239,68,68,.15); color: #f87171; padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-green { background: rgba(34,197,94,.15); color: #4ade80; padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-yellow { background: rgba(234,179,8,.15); color: #fbbf24; padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-gray { background: rgba(156,163,175,.15); color: #9ca3af; padding: 2px 8px; border-radius: 8px; font-size: 11px; }

.mono { font-family: 'SF Mono', 'Fira Code', monospace; font-size: 12px; }

.stats-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}
.stats-toolbar select {
  padding: 4px 8px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  font-size: 13px;
}
.stats-tables h3 { font-size: 14px; margin: 16px 0 8px; color: var(--muted); }
.loading { text-align: center; padding: 40px; color: var(--muted); }
.alert { padding: 8px 12px; border-radius: 4px; font-size: 13px; }
.alert-danger { background: rgba(239,68,68,.1); color: #f87171; border: 1px solid rgba(239,68,68,.3); }
</style>
