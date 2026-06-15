<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { getMaasLedger, MAAS_LEDGER_TYPE_LABELS } from '../../api'
import type { MaasLedgerEntry } from '../../api'
import { isSuperAdmin, isDefaultTenant, isPlatformOpsView, getCurrentTenantId } from '../../store'

const pageTitle = computed(() =>
  isPlatformOpsView() ? 'MaaS 积分消耗' : '我的消耗',
)

const ledger = ref<MaasLedgerEntry[]>([])
const loading = ref(false)
const error = ref('')
const limit = ref(50)

const tenantLabel = computed(() => {
  const tenantId = getCurrentTenantId()
  if (isSuperAdmin() && isDefaultTenant()) return '整站数据'
  if (isDefaultTenant()) return '默认租户'
  return `租户: ${tenantId}`
})

const consumeTotal = computed(() => {
  return ledger.value
    .filter((e) => e.entry_type === 'consume')
    .reduce((sum, e) => sum + Math.abs(e.amount), 0)
})

const recentConsumeCount = computed(() => {
  return ledger.value.filter((e) => e.entry_type === 'consume').length
})

function fmtCredits(n: number) {
  const sign = n > 0 ? '+' : ''
  return sign + n.toLocaleString('zh-CN')
}

function fmtTime(s: string) {
  if (!s) return '-'
  return new Date(s).toLocaleString('zh-CN')
}

function typeLabel(t: string) {
  return MAAS_LEDGER_TYPE_LABELS[t] || t
}

function typeBadgeClass(t: string) {
  if (t === 'consume') return 'badge-red'
  if (t === 'topup') return 'badge-green'
  return 'badge-blue'
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const res = await getMaasLedger(limit.value)
    ledger.value = res.items ?? []
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <h2>{{ pageTitle }}</h2>
      <div class="page-header-actions">
        <span
          class="tenant-badge"
          :class="{ 'tenant-badge--admin': isSuperAdmin(), 'tenant-badge--default': isDefaultTenant() }"
        >
          {{ tenantLabel }}
        </span>
        <select v-model.number="limit" class="limit-select" @change="load">
          <option :value="50">最近 50 条</option>
          <option :value="100">最近 100 条</option>
          <option :value="200">最近 200 条</option>
        </select>
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
          {{ loading ? '加载中…' : '刷新' }}
        </button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <div class="stat-cards">
      <div class="stat-card card">
        <div class="stat-label">近期消耗汇总</div>
        <div class="stat-value">{{ consumeTotal.toLocaleString('zh-CN') }} <span class="unit">积分</span></div>
        <div class="stat-hint">基于当前 {{ limit }} 条流水中的 consume 记录</div>
      </div>
      <div class="stat-card card">
        <div class="stat-label">消耗笔数</div>
        <div class="stat-value">{{ recentConsumeCount }}</div>
        <div class="stat-hint">当前窗口内</div>
      </div>
    </div>

    <div class="card table-card">
      <h3 class="table-title">积分流水</h3>
      <table class="table" style="width:100%">
        <thead>
          <tr>
            <th>时间</th>
            <th>类型</th>
            <th style="text-align:right">变动</th>
            <th style="text-align:right">余额</th>
            <th>关联</th>
            <th>备注</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in ledger" :key="e.id">
            <td class="mono">{{ fmtTime(e.created_at) }}</td>
            <td>
              <span class="badge" :class="typeBadgeClass(e.entry_type)">{{ typeLabel(e.entry_type) }}</span>
            </td>
            <td class="num" :class="{ 'amount-neg': e.amount < 0, 'amount-pos': e.amount > 0 }">
              {{ fmtCredits(e.amount) }}
            </td>
            <td class="num">{{ e.balance_after.toLocaleString('zh-CN') }}</td>
            <td class="mono ref-cell">
              <span v-if="e.ref_type">{{ e.ref_type }}</span>
              <span v-if="e.ref_id" class="ref-id">{{ e.ref_id }}</span>
              <span v-if="!e.ref_type && !e.ref_id">—</span>
            </td>
            <td>{{ e.note || '—' }}</td>
          </tr>
          <tr v-if="!loading && ledger.length === 0">
            <td colspan="6" class="empty">暂无流水记录</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.page-header-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}
.limit-select {
  padding: 4px 8px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  font-size: 13px;
}
.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
}
.stat-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 12px;
  margin-bottom: 20px;
}
.stat-card {
  padding: 16px;
  text-align: center;
}
.stat-label {
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 6px;
}
.stat-value {
  font-size: 26px;
  font-weight: 700;
  color: var(--text);
}
.stat-value .unit {
  font-size: 13px;
  font-weight: 500;
  color: var(--muted);
}
.stat-hint {
  font-size: 11px;
  color: var(--muted);
  margin-top: 6px;
}
.table-card {
  padding: 16px;
}
.table-title {
  font-size: 14px;
  margin: 0 0 12px;
  color: var(--muted);
}
.mono {
  font-family: 'SF Mono', 'Fira Code', monospace;
  font-size: 12px;
}
.num {
  text-align: right;
  font-family: 'SF Mono', 'Fira Code', monospace;
  font-size: 13px;
}
.amount-neg { color: #f87171; }
.amount-pos { color: #4ade80; }
.ref-cell {
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ref-id {
  display: block;
  font-size: 11px;
  color: var(--muted);
}
.badge {
  padding: 2px 8px;
  border-radius: 8px;
  font-size: 11px;
}
.badge-red { background: rgba(239,68,68,.15); color: #f87171; }
.badge-green { background: rgba(34,197,94,.15); color: #4ade80; }
.badge-blue { background: rgba(59,130,246,.15); color: #60a5fa; }
.empty {
  text-align: center;
  padding: 40px;
  color: var(--muted);
}
.tenant-badge {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  background: var(--surface-secondary, #f3f4f6);
  color: var(--text-secondary, #6b7280);
}
.tenant-badge--admin {
  background: rgba(59, 130, 246, 0.1);
  color: #3b82f6;
}
.tenant-badge--default {
  background: rgba(34, 197, 94, 0.1);
  color: #22c55e;
}
</style>
