<script setup lang="ts">
// UsageEnhancedView — 批 158b（2026-10-17）
// P1 usageEnhanced 孤儿接前端：组合 5 个 usage API 的综合视图
// （总览 + 成本趋势 + 同期对比 + 缓存经济 + Top API keys）。
import { onMounted, ref } from 'vue'
import { getUsageEnhancedData, type UsageEnhancedData } from '../../api/usageEnhanced'

const loading = ref(false)
const error = ref('')
const data = ref<UsageEnhancedData | null>(null)

async function load() {
  loading.value = true
  error.value = ''
  try {
    data.value = await getUsageEnhancedData({ days: 7, topN: 10 })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function formatNumber(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return n.toLocaleString('en-US')
}

function formatCurrency(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return '$' + n.toFixed(2)
}

function formatPct(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return (n * 100).toFixed(1) + '%'
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="usage-enhanced-view">
    <header class="enhanced-header">
      <h1 class="enhanced-title">用量增强分析</h1>
      <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
        {{ loading ? '加载中…' : '刷新' }}
      </button>
    </header>

    <p v-if="error" class="enhanced-error">{{ error }}</p>

    <div v-if="loading && !data" class="enhanced-loading">加载中…</div>

    <template v-else-if="data">
      <!-- 4 卡片：总览 -->
      <section class="enhanced-cards">
        <div class="enhanced-card">
          <h2 class="enhanced-card-title">总成本</h2>
          <p class="enhanced-card-value">{{ formatCurrency(data.summary.total_cost) }}</p>
        </div>
        <div class="enhanced-card">
          <h2 class="enhanced-card-title">总请求数</h2>
          <p class="enhanced-card-value">{{ formatNumber(data.summary.total_requests) }}</p>
        </div>
        <div class="enhanced-card">
          <h2 class="enhanced-card-title">同期对比 Δ 成本</h2>
          <p class="enhanced-card-value" :class="{ 'is-up': data.period_compare.delta_cost > 0, 'is-down': data.period_compare.delta_cost < 0 }">
            {{ formatPct(data.period_compare.delta_cost_pct) }}
          </p>
        </div>
        <div class="enhanced-card">
          <h2 class="enhanced-card-title">缓存命中率</h2>
          <p class="enhanced-card-value">{{ formatPct(data.cache_economics.cache_hit_rate) }}</p>
        </div>
      </section>

      <!-- 1 大图：成本趋势（占位，后续可接 Chart.js） -->
      <section class="enhanced-section">
        <h2 class="enhanced-section-title">成本趋势（最近 {{ data.summary.days }} 天）</h2>
        <ul v-if="data.cost_trend.data.length" class="enhanced-list">
          <li v-for="p in data.cost_trend.data" :key="p.date" class="enhanced-trend-row">
            <span class="trend-date">{{ p.date }}</span>
            <span class="trend-cost">{{ formatCurrency(p.cost) }}</span>
            <span class="trend-requests">{{ formatNumber(p.requests) }} req</span>
          </li>
        </ul>
        <p v-else class="enhanced-empty">无数据</p>
      </section>

      <!-- 1 表格：Top API keys -->
      <section class="enhanced-section">
        <h2 class="enhanced-section-title">Top API keys（按总成本排序）</h2>
        <table v-if="data.hot_api_keys.length" class="enhanced-table">
          <thead>
            <tr><th>key_id</th><th>名称</th><th>总请求数</th><th>总成本</th></tr>
          </thead>
          <tbody>
            <tr v-for="k in data.hot_api_keys" :key="k.key_id">
              <td><code>{{ k.key_id }}</code></td>
              <td>{{ k.key_name }}</td>
              <td>{{ formatNumber(k.total_requests) }}</td>
              <td>{{ formatCurrency(k.total_cost) }}</td>
            </tr>
          </tbody>
        </table>
        <p v-else class="enhanced-empty">无数据</p>
      </section>

      <footer class="enhanced-footer">最近刷新: {{ data.refreshedAt }}</footer>
    </template>
  </div>
</template>

<style scoped>
.usage-enhanced-view {
  padding: var(--space-4);
}
.enhanced-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--space-3);
}
.enhanced-title {
  font-size: var(--text-xl);
  font-weight: var(--font-semibold);
}
.enhanced-error {
  color: var(--danger);
  padding: var(--space-2);
  background: var(--danger-bg);
  border-radius: var(--radius);
  margin-bottom: var(--space-3);
}
.enhanced-loading {
  color: var(--text-muted);
  padding: var(--space-3);
}
.enhanced-cards {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: var(--space-3);
  margin-bottom: var(--space-4);
}
.enhanced-card {
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: var(--space-3);
}
.enhanced-card-title {
  font-size: var(--text-sm);
  color: var(--text-muted);
  margin-bottom: var(--space-1);
}
.enhanced-card-value {
  font-size: var(--text-2xl);
  font-weight: var(--font-semibold);
}
.enhanced-card-value.is-up { color: var(--danger); }
.enhanced-card-value.is-down { color: var(--success); }
.enhanced-section {
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: var(--space-3);
  margin-bottom: var(--space-3);
}
.enhanced-section-title {
  font-size: var(--text-md);
  font-weight: var(--font-semibold);
  margin-bottom: var(--space-2);
}
.enhanced-list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.enhanced-trend-row {
  display: grid;
  grid-template-columns: 1fr 1fr 1fr;
  padding: var(--space-1) 0;
  border-bottom: 1px solid var(--border-muted);
}
.enhanced-trend-row:last-child { border-bottom: none; }
.enhanced-table {
  width: 100%;
  border-collapse: collapse;
}
.enhanced-table th,
.enhanced-table td {
  text-align: left;
  padding: var(--space-1) var(--space-2);
  border-bottom: 1px solid var(--border-muted);
}
.enhanced-table th {
  font-size: var(--text-sm);
  color: var(--text-muted);
  font-weight: var(--font-semibold);
}
.enhanced-empty {
  color: var(--text-muted);
  font-style: italic;
  margin: var(--space-2) 0;
}
.enhanced-footer {
  margin-top: var(--space-3);
  color: var(--text-muted);
  font-size: var(--text-sm);
}
@media (max-width: 768px) {
  .enhanced-cards { grid-template-columns: 1fr 1fr; }
}
</style>
