<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getTenantUsage, getKeys, type TenantUsage, type ApiKey } from '../api'

const route = useRoute()
const router = useRouter()
const tenantId = computed(() => decodeURIComponent(route.params.tenantId as string))

const usage = ref<TenantUsage | null>(null)
const keys = ref<ApiKey[]>([])
const loading = ref(false)
const error = ref('')
const days = ref(30)
const activeTab = ref<'keys' | 'usage'>('keys')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [u, k] = await Promise.all([
      getTenantUsage(tenantId.value, days.value),
      getKeys(),
    ])
    usage.value = u
    keys.value = (k as ApiKey[]).filter((key: ApiKey) => key.tenant_id === tenantId.value)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k'
  return String(n)
}

function formatCost(n: number): string {
  if (n === 0) return '—'
  return '$' + n.toFixed(4)
}

function formatRequests(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k'
  return String(n)
}

function keyStateLabel(k: ApiKey): string {
  if (k.status === 'pending') return '待审批'
  if (k.status === 'active' && k.enabled) return '正常'
  if (k.status === 'disabled') return '已禁用'
  return '已作废'
}

function keyStateBadgeClass(k: ApiKey): string {
  if (k.status === 'pending') return 'badge-yellow'
  if (k.status === 'active' && k.enabled) return 'badge-green'
  return 'badge-red'
}

function viewKeyStats(k: ApiKey) {
  router.push(`/keys/${k.id}`)
}

watch(tenantId, () => { load() })
onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <div style="display:flex;align-items:center;gap:12px">
        <button class="btn btn-ghost btn-sm" @click="router.push('/tenants')">← 返回</button>
        <h2>租户: {{ tenantId }}</h2>
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        <label style="font-size:12px;color:var(--muted)">统计周期</label>
        <select v-model.number="days" @change="load()" class="input" style="width:auto;padding:4px 8px;font-size:12px">
          <option :value="7">近 7 天</option>
          <option :value="30">近 30 天</option>
          <option :value="90">近 90 天</option>
        </select>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="loading" class="empty">加载中…</div>

    <template v-if="!loading && usage">
      <div class="stats-grid">
        <div class="stat-card">
          <div class="stat-label">密钥数</div>
          <div class="stat-value">{{ keys.length }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">总请求</div>
          <div class="stat-value">{{ formatRequests(usage.total_requests) }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">总 Token</div>
          <div class="stat-value">{{ formatTokens(usage.total_prompt_tokens + usage.total_completion_tokens) }}</div>
          <div class="stat-sub">入 {{ formatTokens(usage.total_prompt_tokens) }} · 出 {{ formatTokens(usage.total_completion_tokens) }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">总费用</div>
          <div class="stat-value has-cost">{{ formatCost(usage.total_cost_usd) }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">独立模型</div>
          <div class="stat-value">{{ usage.unique_models }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">独立应用</div>
          <div class="stat-value">{{ usage.unique_applications }}</div>
        </div>
      </div>

      <div class="card" style="margin-top:16px">
        <div class="tab-bar">
          <button class="tab-btn" :class="{ active: activeTab === 'keys' }" @click="activeTab = 'keys'">密钥列表 ({{ keys.length }})</button>
          <button class="tab-btn" :class="{ active: activeTab === 'usage' }" @click="activeTab = 'usage'">用量概览</button>
        </div>

        <template v-if="activeTab === 'keys'">
          <table>
            <thead>
              <tr>
                <th style="width:40px">ID</th>
                <th>前缀</th>
                <th>应用</th>
                <th>别名</th>
                <th>状态</th>
                <th>总请求</th>
                <th>总 Token</th>
                <th>累计费用</th>
                <th>最后使用</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="k in keys" :key="k.id">
                <td style="font-size:11px;color:var(--muted);font-family:monospace">{{ k.id }}</td>
                <td><code style="font-size:12px">{{ k.key_prefix }}***</code></td>
                <td>{{ k.application_code }}</td>
                <td><code style="font-size:11px">{{ k.key_alias || '—' }}</code></td>
                <td>
                  <span class="badge" :class="keyStateBadgeClass(k)">{{ keyStateLabel(k) }}</span>
                  <span v-if="k.is_system" class="badge badge-system">系统</span>
                </td>
                <td style="font-size:12px;color:var(--muted);text-align:right">{{ formatRequests(k.total_requests) }}</td>
                <td style="font-size:12px;color:var(--muted);text-align:right">{{ formatTokens(k.total_prompt_tokens + k.total_completion_tokens) }}</td>
                <td style="font-size:12px;text-align:right" :class="{ 'has-cost': k.total_cost_usd > 0 }">{{ k.total_cost_usd > 0 ? formatCost(k.total_cost_usd) : '—' }}</td>
                <td style="font-size:12px;color:var(--muted)">{{ k.last_used_at ? new Date(k.last_used_at).toLocaleDateString('zh-CN') : '—' }}</td>
                <td>
                  <button class="btn btn-secondary btn-sm" @click="viewKeyStats(k)">📊 统计</button>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-if="keys.length === 0" class="empty">该租户没有密钥</div>
        </template>

        <template v-if="activeTab === 'usage'">
          <div class="usage-summary">
            <div class="usage-row">
              <span class="usage-label">统计周期</span>
              <span class="usage-value">近 {{ days }} 天</span>
            </div>
            <div class="usage-row">
              <span class="usage-label">总请求数</span>
              <span class="usage-value">{{ usage.total_requests.toLocaleString() }}</span>
            </div>
            <div class="usage-row">
              <span class="usage-label">提示 Token</span>
              <span class="usage-value">{{ usage.total_prompt_tokens.toLocaleString() }}</span>
            </div>
            <div class="usage-row">
              <span class="usage-label">补全 Token</span>
              <span class="usage-value">{{ usage.total_completion_tokens.toLocaleString() }}</span>
            </div>
            <div class="usage-row">
              <span class="usage-label">总费用 (USD)</span>
              <span class="usage-value has-cost">${{ usage.total_cost_usd.toFixed(4) }}</span>
            </div>
            <div class="usage-row">
              <span class="usage-label">独立密钥数</span>
              <span class="usage-value">{{ usage.unique_keys }}</span>
            </div>
            <div class="usage-row">
              <span class="usage-label">独立模型数</span>
              <span class="usage-value">{{ usage.unique_models }}</span>
            </div>
            <div class="usage-row">
              <span class="usage-label">独立应用数</span>
              <span class="usage-value">{{ usage.unique_applications }}</span>
            </div>
          </div>
        </template>
      </div>
    </template>
  </div>
</template>

<style scoped>
.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 12px;
}

.stat-card {
  background: var(--card-bg, #fff);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 16px;
}

.stat-label {
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 4px;
}

.stat-value {
  font-size: 24px;
  font-weight: 700;
  color: var(--text);
}

.stat-sub {
  font-size: 11px;
  color: var(--muted);
  margin-top: 4px;
}

.has-cost {
  color: var(--accent, #d4a017);
  font-weight: 600;
}

.tab-bar {
  display: flex;
  gap: 4px;
  margin-bottom: 16px;
  border-bottom: 1px solid var(--border);
  padding-bottom: 8px;
}

.tab-btn {
  padding: 6px 14px;
  border: none;
  background: transparent;
  color: var(--muted);
  cursor: pointer;
  font-size: 13px;
  border-radius: var(--radius) var(--radius) 0 0;
  border-bottom: 2px solid transparent;
  transition: all 0.15s;
}

.tab-btn:hover {
  color: var(--text);
  background: var(--bg-subtle, rgba(0,0,0,0.04));
}

.tab-btn.active {
  color: var(--accent);
  border-bottom-color: var(--accent);
  font-weight: 600;
}

.usage-summary {
  max-width: 400px;
}

.usage-row {
  display: flex;
  justify-content: space-between;
  padding: 8px 0;
  border-bottom: 1px solid var(--border);
  font-size: 14px;
}

.usage-label {
  color: var(--muted);
}

.usage-value {
  font-weight: 500;
}
</style>
