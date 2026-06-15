<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { getTenantsAdmin, TENANT_STATUSES, TENANT_STATUS_LABELS, TENANT_STATUS_COLORS } from '../api'
import type { Tenant } from '../api'

const router = useRouter()
const tenants = ref<Tenant[]>([])
const loading = ref(false)
const error = ref('')
const filterStatus = ref<string>('')
const showCreate = ref(false)
const showEdit = ref(false)
const editTarget = ref<Tenant | null>(null)

async function load() {
  loading.value = true
  error.value = ''
  try {
    tenants.value = await getTenantsAdmin(filterStatus.value || undefined)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
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

function fmtNum(n?: number) {
  if (n == null) return '-'
  return n.toLocaleString()
}

function fmtCost(n?: number) {
  if (n == null) return '-'
  return '$' + n.toFixed(2)
}

function openEdit(t: Tenant) {
  editTarget.value = { ...t }
  showEdit.value = true
}

function goDetail(t: Tenant) {
  router.push(`/tenants/${t.code}`)
}

onMounted(load)
</script>

<template>
  <div class="tenants-page">
    <div class="page-header">
      <h1>🏢 租户管理</h1>
      <button class="btn btn-primary" @click="showCreate = true">+ 新建租户</button>
    </div>

    <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>

    <!-- Status filter -->
    <div class="filters">
      <label>状态:</label>
      <select v-model="filterStatus" @change="load">
        <option value="">全部</option>
        <option v-for="s in TENANT_STATUSES" :key="s" :value="s">{{ statusLabel(s) }}</option>
      </select>
    </div>

    <div v-if="loading" class="loading">加载中…</div>

    <table v-else class="table" style="width:100%">
      <thead>
        <tr>
          <th>租户名</th>
          <th>租户 code</th>
          <th>状态</th>
          <th>用户数</th>
          <th>密钥数</th>
          <th>7天费用</th>
          <th>总请求</th>
          <th>联系邮箱</th>
          <th>创建时间</th>
          <th>操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="t in tenants" :key="t.code">
          <td><strong>{{ t.name }}</strong></td>
          <td><code>{{ t.code }}</code></td>
          <td><span class="badge" :class="statusColor(t.status)">{{ statusLabel(t.status) }}</span></td>
          <td>{{ fmtNum(t.user_count) }}</td>
          <td>{{ fmtNum(t.api_key_count) }}</td>
          <td>{{ fmtCost(t.cost_7d_usd) }}</td>
          <td>{{ fmtNum(t.total_requests) }}</td>
          <td>{{ t.contact_email || '-' }}</td>
          <td class="mono">{{ fmtTime(t.created_at) }}</td>
          <td>
            <button class="btn btn-ghost btn-sm" @click="goDetail(t)">详情</button>
            <button class="btn btn-ghost btn-sm" @click="openEdit(t)">编辑</button>
          </td>
        </tr>
        <tr v-if="tenants.length === 0">
          <td colspan="10" style="text-align:center; color: var(--muted); padding: 40px">无数据</td>
        </tr>
      </tbody>
    </table>

    <!-- Create Dialog -->
    <TenantCreateDialog v-if="showCreate" @close="showCreate = false" @created="load" />

    <!-- Edit Dialog -->
    <TenantEditDialog v-if="showEdit && editTarget" :tenant="editTarget" @close="showEdit = false; editTarget = null" @updated="load" />
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
}
.badge-purple { background: rgba(139,92,246,.15); color: #a78bfa; }
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
