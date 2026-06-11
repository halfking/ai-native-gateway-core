<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { getTenants, type TenantSummary } from '../api'

const router = useRouter()
const tenants = ref<TenantSummary[]>([])
const loading = ref(false)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    tenants.value = await getTenants()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

function viewDetail(tenantId: string) {
  router.push(`/tenants/${encodeURIComponent(tenantId)}`)
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k'
  return String(n)
}

function formatCost(n: number): string {
  if (n === 0) return '—'
  return '$' + n.toFixed(2)
}

function formatRequests(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k'
  return String(n)
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <h2>租户管理</h2>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="loading" class="empty">加载中…</div>

    <div class="card" v-if="!loading">
      <table>
        <thead>
          <tr>
            <th>租户 ID</th>
            <th>密钥数</th>
            <th>总请求</th>
            <th>总 Token</th>
            <th>总费用</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="t in tenants" :key="t.tenant_id">
            <td><code>{{ t.tenant_id }}</code></td>
            <td style="text-align:right">{{ t.key_count }}</td>
            <td style="text-align:right;font-size:12px;color:var(--muted)">{{ formatRequests(t.total_requests) }}</td>
            <td style="text-align:right;font-size:12px;color:var(--muted)">{{ formatTokens(t.total_tokens) }}</td>
            <td style="text-align:right" :class="{ 'has-cost': t.total_cost_usd > 0 }">{{ formatCost(t.total_cost_usd) }}</td>
            <td>
              <button class="btn btn-secondary btn-sm" @click="viewDetail(t.tenant_id)">查看详情</button>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-if="tenants.length === 0" class="empty">暂无租户数据</div>
    </div>
  </div>
</template>

<style scoped>
.has-cost {
  color: var(--accent, #d4a017);
  font-weight: 600;
}
</style>
