<script setup lang="ts">
import { ref, onMounted } from 'vue'

const loading = ref(false)
const error = ref('')

// Pricing data
const pricingTree = ref<any[]>([])
const summary = ref<any>(null)

// Filters
const filters = ref({
  search: '',
  provider_id: '',
  billing_mode: '',
  currency: '',
  pricing_status: '',
  availability: '',
})

// Provider list for filter
const providers = ref<any[]>([])

async function fetchData() {
  loading.value = true
  error.value = ''
  try {
    const params = new URLSearchParams()
    if (filters.value.search) params.set('search', filters.value.search)
    if (filters.value.provider_id) params.set('provider_id', filters.value.provider_id)
    if (filters.value.billing_mode) params.set('billing_mode', filters.value.billing_mode)
    if (filters.value.currency) params.set('currency', filters.value.currency)
    if (filters.value.pricing_status) params.set('pricing_status', filters.value.pricing_status)
    if (filters.value.availability) params.set('availability', filters.value.availability)
    
    const s = params.toString()
    const [tree, sum] = await Promise.all([
      fetch(`/api/pricing/tree${s ? '?' + s : ''}`).then(r => r.json()),
      fetch('/api/pricing/summary').then(r => r.json()),
    ])
    pricingTree.value = tree.offers || tree
    summary.value = sum
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function fetchProviders() {
  try {
    const res = await fetch('/api/providers').then(r => r.json())
    providers.value = res
  } catch { /* ignore */ }
}

function money(v: number | string | null | undefined) {
  if (v == null) return '—'
  const n = typeof v === 'string' ? Number(v) : v
  return Number.isNaN(n) ? '—' : `$${n.toFixed(4)}`
}

function onFilterChange() {
  fetchData()
}

function formatTime(ts: string | null) {
  if (!ts) return '—'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}

onMounted(() => {
  fetchData()
  fetchProviders()
})
</script>

<template>
  <div>
    <div class="page-header" style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px">
      <h2>定价管理</h2>
      <div style="display:flex;gap:8px">
        <button class="btn btn-ghost btn-sm" @click="fetchData" :disabled="loading">
          {{ loading ? '加载中...' : '刷新' }}
        </button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>

    <!-- Summary Cards -->
    <div v-if="summary" class="stat-grid" style="margin-bottom:20px">
      <div class="stat-card">
        <div class="stat-label">总模型数</div>
        <div class="stat-value">{{ summary.total_offers ?? 0 }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">已定价 (输入)</div>
        <div class="stat-value">{{ summary.priced_in ?? 0 }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">已定价 (输出)</div>
        <div class="stat-value">{{ summary.priced_out ?? 0 }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">CNY</div>
        <div class="stat-value">{{ summary.cny_offers ?? 0 }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">USD</div>
        <div class="stat-value">{{ summary.usd_offers ?? 0 }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">免费</div>
        <div class="stat-value">{{ summary.free_offers ?? 0 }}</div>
      </div>
    </div>

    <!-- Filter Bar -->
    <div class="card" style="margin-bottom:16px">
      <div style="display:flex;gap:12px;flex-wrap:wrap;align-items:center">
        <input class="input" v-model="filters.search" placeholder="搜索模型..." style="width:200px" @input="onFilterChange" />
        <select class="input" v-model="filters.provider_id" @change="onFilterChange" style="width:180px">
          <option value="">全部提供商</option>
          <option v-for="p in providers" :key="p.id" :value="p.id">{{ p.display_name }}</option>
        </select>
        <select class="input" v-model="filters.billing_mode" @change="onFilterChange" style="width:150px">
          <option value="">全部计费</option>
          <option value="per_token">按 Token</option>
          <option value="per_request">按请求</option>
          <option value="monthly">包月</option>
          <option value="free">免费</option>
        </select>
        <select class="input" v-model="filters.currency" @change="onFilterChange" style="width:120px">
          <option value="">全部货币</option>
          <option value="CNY">CNY</option>
          <option value="USD">USD</option>
        </select>
        <select class="input" v-model="filters.pricing_status" @change="onFilterChange" style="width:120px">
          <option value="">全部状态</option>
          <option value="priced">已定价</option>
          <option value="unpriced">未定价</option>
          <option value="free">免费</option>
        </select>
        <select class="input" v-model="filters.availability" @change="onFilterChange" style="width:120px">
          <option value="">全部可用</option>
          <option value="true">可用</option>
          <option value="false">不可用</option>
        </select>
      </div>
    </div>

    <!-- Pricing Table -->
    <div class="card">
      <div v-if="loading" style="text-align:center;padding:24px;color:var(--muted)">加载中...</div>
      <div v-else-if="pricingTree.length === 0" style="text-align:center;padding:24px;color:var(--muted)">
        暂无定价数据
      </div>
      <div v-else style="overflow-x:auto">
        <table class="data-table" style="width:100%;font-size:12px">
          <thead>
            <tr>
              <th>模型</th>
              <th>提供商</th>
              <th>输入价格</th>
              <th>输出价格</th>
              <th>货币</th>
              <th>计费方式</th>
              <th>来源</th>
              <th>最后更新</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="o in pricingTree" :key="o.id">
              <td><code style="font-size:11px">{{ o.raw_model_name }}</code></td>
              <td>{{ o.provider_name || '—' }}</td>
              <td>{{ o.unit_price_in_per_1m != null ? money(o.unit_price_in_per_1m) : '—' }}</td>
              <td>{{ o.unit_price_out_per_1m != null ? money(o.unit_price_out_per_1m) : '—' }}</td>
              <td>{{ o.currency || '—' }}</td>
              <td>{{ o.billing_mode || '—' }}</td>
              <td>{{ o.pricing_source || '—' }}</td>
              <td style="font-size:11px">{{ formatTime(o.pricing_updated_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
