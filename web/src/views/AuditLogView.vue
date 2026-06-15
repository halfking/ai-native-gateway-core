<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { getAuditLogs } from '../api'

interface Entry {
  id: number
  ts: string
  actor: string
  action: string
  target_type?: string
  target_id?: number
  before_json?: any
  after_json?: any
}

const entries = ref<Entry[]>([])
const total = ref(0)
const page = ref(1)
const size = ref(50)
const loading = ref(false)
const error = ref('')

// Filters
const filterActor = ref('')
const filterAction = ref('')
const filterFrom = ref('')
const filterTo = ref('')

const totalPages = computed(() => Math.ceil(total.value / size.value))

async function load() {
  loading.value = true
  error.value = ''
  try {
    const r = await getAuditLogs({
      page: page.value,
      size: size.value,
      actor: filterActor.value || undefined,
      action: filterAction.value || undefined,
      from: filterFrom.value ? new Date(filterFrom.value).toISOString() : undefined,
      to: filterTo.value ? new Date(filterTo.value).toISOString() : undefined,
    })
    entries.value = r.entries || []
    total.value = r.total || 0
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

function nextPage() {
  if (page.value < totalPages.value) {
    page.value++
    load()
  }
}

function prevPage() {
  if (page.value > 1) {
    page.value--
    load()
  }
}

function actionColor(action: string): string {
  if (action.startsWith('user.create')) return 'badge-purple'
  if (action.startsWith('user.delete')) return 'badge-red'
  if (action.startsWith('user.update')) return 'badge-blue'
  if (action.startsWith('user.')) return 'badge-blue'
  if (action.startsWith('auth.login_failed')) return 'badge-red'
  if (action.startsWith('auth.rate_limited')) return 'badge-red'
  if (action.startsWith('auth.')) return 'badge-green'
  return 'badge-blue'
}

function fmtTime(s: string) {
  if (!s) return '-'
  return new Date(s).toLocaleString('zh-CN')
}

function fmtJson(v: any): string {
  if (v == null) return ''
  if (typeof v === 'string') return v
  return JSON.stringify(v)
}

onMounted(load)
</script>

<template>
  <div class="audit-page">
    <div class="page-header">
      <h1>📋 审计日志</h1>
      <span class="total-badge">总计: {{ total }} 条</span>
    </div>

    <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>

    <!-- Filters -->
    <div class="filters">
      <div class="filter-group">
        <label>操作员</label>
        <input v-model="filterActor" placeholder="LIKE 匹配" />
      </div>
      <div class="filter-group">
        <label>动作</label>
        <input v-model="filterAction" placeholder="如 user.* 或 auth.*" />
      </div>
      <div class="filter-group">
        <label>起始时间</label>
        <input v-model="filterFrom" type="datetime-local" />
      </div>
      <div class="filter-group">
        <label>截止时间</label>
        <input v-model="filterTo" type="datetime-local" />
      </div>
      <button class="btn btn-primary" @click="page = 1; load()">查询</button>
      <button class="btn btn-ghost" @click="filterActor = ''; filterAction = ''; filterFrom = ''; filterTo = ''; page = 1; load()">重置</button>
    </div>

    <div v-if="loading" class="loading">加载中…</div>

    <table v-else class="table" style="width:100%">
      <thead>
        <tr>
          <th style="width: 160px">时间</th>
          <th style="width: 120px">操作员</th>
          <th style="width: 180px">动作</th>
          <th style="width: 100px">目标</th>
          <th>详情</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="e in entries" :key="e.id">
          <td class="mono">{{ fmtTime(e.ts) }}</td>
          <td><strong>{{ e.actor || '-' }}</strong></td>
          <td><span class="badge" :class="actionColor(e.action)">{{ e.action }}</span></td>
          <td>
            <span v-if="e.target_type">{{ e.target_type }} #{{ e.target_id || '?' }}</span>
            <span v-else>-</span>
          </td>
          <td class="details-cell">
            <code v-if="e.after_json">{{ fmtJson(e.after_json) }}</code>
            <code v-else-if="e.before_json">{{ fmtJson(e.before_json) }}</code>
            <span v-else>-</span>
          </td>
        </tr>
        <tr v-if="entries.length === 0">
          <td colspan="5" style="text-align:center; color: var(--muted); padding: 40px">无数据</td>
        </tr>
      </tbody>
    </table>

    <!-- Pagination -->
    <div class="pagination" v-if="total > 0">
      <button class="btn btn-ghost btn-sm" :disabled="page === 1" @click="prevPage">上一页</button>
      <span class="page-info">第 {{ page }} / {{ totalPages }} 页</span>
      <button class="btn btn-ghost btn-sm" :disabled="page >= totalPages" @click="nextPage">下一页</button>
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
.total-badge {
  padding: 4px 12px;
  background: rgba(99, 102, 241, 0.15);
  color: var(--accent-h);
  border-radius: 12px;
  font-size: 12px;
  font-weight: 600;
}
.filters {
  display: flex;
  gap: 12px;
  align-items: flex-end;
  margin-bottom: 16px;
  padding: 12px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
}
.filter-group {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.filter-group label {
  font-size: 12px;
  color: var(--muted);
}
.filter-group input {
  padding: 6px 10px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  font-size: 13px;
  min-width: 140px;
}
.badge-purple { background: rgba(139,92,246,.15); color: #a78bfa; }
.badge-red { background: rgba(239,68,68,.15); color: #f87171; }
.badge-blue { background: rgba(59,130,246,.15); color: #60a5fa; }
.badge-green { background: rgba(34,197,94,.15); color: #4ade80; }
.mono { font-family: 'SF Mono', 'Fira Code', monospace; font-size: 12px; }
.details-cell { font-size: 12px; max-width: 400px; }
.details-cell code {
  font-family: 'SF Mono', 'Fira Code', monospace;
  font-size: 11px;
  color: var(--muted);
  word-break: break-all;
  background: var(--bg);
  padding: 2px 6px;
  border-radius: 3px;
}
.pagination {
  display: flex;
  gap: 16px;
  align-items: center;
  justify-content: center;
  margin-top: 16px;
}
.page-info {
  font-size: 13px;
  color: var(--muted);
}
.loading {
  text-align: center;
  padding: 40px;
  color: var(--muted);
}
</style>
