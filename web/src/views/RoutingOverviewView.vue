<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  getRoutingModelTree,
  type RoutingModelTreeResponse,
  type RoutingTreeCredential,
  type RoutingTreeSeries,
} from '../api'
import { isReadOnlyMode } from '../store'

const tree = ref<RoutingModelTreeResponse>({ featured: [], series: [], unmapped: [] })
const loading = ref(false)
const error = ref('')
const search = ref('')
const featuredOnly = ref(false)
const readOnly = computed(() => isReadOnlyMode())

function isCredentialRoutable(c: RoutingTreeCredential): boolean {
  return c.runtime_routable
}

function reasonLabel(reason: string | null | undefined): string {
  switch (reason) {
    case 'circuit_open':
      return '熔断中'
    case 'balance_exhausted':
      return '余额耗尽'
    case 'quota_periodic_exhausted':
      return '周期额度耗尽'
    case 'quota_balance_exhausted':
      return '额度耗尽'
    case 'quota_permanently_exhausted':
      return '永久额度耗尽'
    case 'availability_cooling':
      return '冷却中'
    case 'availability_rate_limited':
      return '限流中'
    case 'availability_unreachable':
      return '暂不可达'
    case 'availability_auth_failed':
      return '鉴权失败'
    case 'availability_suspended':
      return '已暂停'
    case 'lifecycle_disabled':
      return '生命周期禁用'
    case 'lifecycle_suspended':
      return '生命周期暂停'
    case 'lifecycle_retired':
      return '生命周期退役'
    default:
      return reason || '可路由'
  }
}

function latencyLabel(ms: number): string {
  if (!ms || ms <= 0) return '-'
  return ms < 1000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(1)}s`
}

function rateLabel(r: number): string {
  return !r || r <= 0 ? '-' : `${(r * 100).toFixed(1)}%`
}

function money(value: number | string | null | undefined, currency = 'USD'): string {
  if (value === null || value === undefined) return '-'
  const n = typeof value === 'string' ? Number(value) : value
  return Number.isNaN(n) ? '-' : `${n.toFixed(4)} ${currency}`
}

function priceLabel(c: RoutingTreeCredential): string {
  const input = money(c.unit_price_in_per_1m, c.currency || 'USD')
  const output = money(c.unit_price_out_per_1m, c.currency || 'USD')
  return `${input}/${output} /1M`
}

function quotaRemaining(c: RoutingTreeCredential): string {
  const cap = Number(c.quota_cap_usd || 0)
  const used = Number(c.quota_used_usd || 0)
  if (!cap) return money(c.balance_usd, 'USD')
  return money(Math.max(0, cap - used), 'USD')
}

function statusClass(c: RoutingTreeCredential): string {
  if (!isCredentialRoutable(c)) return 'badge-red'
  if (c.tier === 1) return 'badge-green'
  if (c.tier === 3) return 'badge-purple'
  return 'badge-amber'
}

function credentialCount(series: RoutingTreeSeries): number {
  return series.generations.reduce(
    (sum, generation) => sum + generation.variants.reduce((inner, variant) => inner + variant.credentials.length, 0),
    0,
  )
}

const filteredSeries = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return tree.value.series
  return tree.value.series
    .map((series) => ({
      ...series,
      generations: series.generations
        .map((generation) => ({
          ...generation,
          variants: generation.variants.filter((variant) => {
            const haystack = [
              series.series,
              generation.generation,
              variant.variant,
              variant.canonical_name,
              ...variant.tags,
              ...variant.credentials.flatMap((c) => [c.provider_name, c.credential_label, c.raw_model_name]),
            ].join(' ').toLowerCase()
            return haystack.includes(q)
          }),
        }))
        .filter((generation) => generation.variants.length > 0),
    }))
    .filter((series) => series.generations.length > 0)
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    tree.value = await getRoutingModelTree(featuredOnly.value)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

function toggleFeatured() {
  featuredOnly.value = !featuredOnly.value
  load()
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <h2>路由总览</h2>
      <div style="display:flex;gap:8px">
        <button class="btn" :class="featuredOnly ? 'btn-primary' : 'btn-ghost'" @click="toggleFeatured">
          {{ featuredOnly ? '★ 仅特色' : '☆ 仅特色' }}
        </button>
        <button class="btn btn-ghost" @click="load" :disabled="loading">刷新</button>
      </div>
    </div>

    <div v-if="readOnly" class="alert alert-info" style="margin-bottom:12px">
      📖 您是租户管理员，只能查看模型路由总览，不能查看凭据路由详情（供应商、凭据、价格、状态等已隐藏）。
    </div>

    <div class="card toolbar">
      <input v-model="search" placeholder="搜索模型、版本、供应商或凭据…" />
      <span class="badge badge-gray">{{ filteredSeries.length }} 个系列</span>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="loading" class="empty">加载中…</div>

    <template v-if="!loading">
      <div v-if="filteredSeries.length === 0" class="empty">暂无路由数据</div>

      <section v-for="series in filteredSeries" :key="series.series" class="series-block">
        <div class="series-header">
          <h3>{{ series.series }}</h3>
          <span v-if="!readOnly" class="badge badge-blue">{{ credentialCount(series) }} 个供应商凭据</span>
          <span v-else class="badge badge-gray">{{ (series as any).generations?.reduce((s: number, g: any) => s + g.variants.reduce((vs: number, v: any) => vs + (v.credential_count || 0), 0), 0) || 0 }} 个模型变体</span>
        </div>

        <div v-for="generation in series.generations" :key="generation.generation" class="generation-block">
          <div class="generation-title">{{ generation.generation }}</div>

          <div v-for="variant in generation.variants" :key="variant.canonical_name" class="variant-card">
            <div class="variant-head">
              <div>
                <strong>{{ variant.variant }}</strong>
                <code>{{ variant.canonical_name }}</code>
              </div>
              <div class="tag-row">
                <span v-for="tag in variant.tags" :key="tag" class="badge badge-gray">{{ tag }}</span>
              </div>
            </div>

            <!-- Read-only mode (tenant_admin): show only model availability, not credential details -->
            <div v-if="readOnly" class="readonly-summary">
              <div class="readonly-info">
                <span :class="(variant as any).available ? 'status-ok' : 'status-bad'">
                  {{ (variant as any).available ? '✓ 可路由' : '✗ 暂不可路由' }}
                </span>
                <span class="readonly-meta">
                  凭据数: {{ (variant as any).credential_count || 0 }}
                  （详情已隐藏）
                </span>
              </div>
            </div>

            <!-- Full mode (super_admin): show all credential details -->
            <div v-else class="credential-grid">
              <article v-for="cred in variant.credentials" :key="`${cred.provider_id}-${cred.credential_id}-${cred.raw_model_name}`" class="credential-card">
                <div class="credential-top">
                  <strong>{{ cred.provider_name }}</strong>
                  <span class="badge" :class="statusClass(cred)">T{{ cred.tier }} · w{{ cred.weight }}</span>
                </div>
                <div class="credential-line">{{ cred.credential_label }} · #{{ cred.credential_id }} · {{ cred.catalog_code }}</div>
                <div class="metric-row">
                  <span>{{ cred.credential_status }}</span>
                  <span>{{ cred.lifecycle_status || 'active' }}</span>
                  <span>{{ cred.availability_state || 'ready' }}</span>
                  <span>{{ cred.quota_state || 'ok' }}</span>
                </div>
                <div class="metric-row">
                  <span :style="cred.runtime_routable ? 'color:var(--success,#15803d)' : 'color:var(--danger,#b91c1c)'">
                    {{ cred.runtime_routable ? '可路由' : reasonLabel(cred.runtime_block_reason) }}
                  </span>
                  <span>{{ rateLabel(cred.success_rate) }}</span>
                  <span>{{ latencyLabel(cred.p95_latency_ms) }}</span>
                  <span>并发 {{ cred.effective_concurrency ?? cred.concurrency_limit ?? '-' }}</span>
                </div>
                <div class="metric-row">
                  <span>{{ priceLabel(cred) }}</span>
                  <span>余额 {{ quotaRemaining(cred) }}</span>
                </div>
                <div class="raw-name">{{ cred.raw_model_name }}</div>
                <div v-if="cred.standardized_name" class="raw-name" style="color:var(--accent)">{{ cred.standardized_name }}</div>
              </article>
            </div>
          </div>
        </div>
      </section>

      <section v-if="tree.unmapped.length" class="series-block">
        <div class="series-header">
          <h3>未归类</h3>
          <span class="badge badge-amber">{{ tree.unmapped.length }} 条</span>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
.toolbar { display: flex; gap: 10px; align-items: center; margin-bottom: 16px; }
.toolbar input { max-width: 420px; }
.series-block { margin-bottom: 18px; }
.series-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.series-header h3 { margin: 0; font-size: 18px; }
.generation-block { border-left: 3px solid var(--border, #e5e7eb); padding-left: 12px; margin-bottom: 12px; }
.generation-title { font-weight: 700; color: var(--muted); margin-bottom: 8px; }
.variant-card { border: 1px solid var(--border, #e5e7eb); border-radius: 8px; padding: 12px; margin-bottom: 10px; background: var(--card); }
.variant-head { display: flex; justify-content: space-between; gap: 12px; align-items: flex-start; margin-bottom: 10px; }
.variant-head code { display: block; margin-top: 3px; font-size: 12px; color: var(--muted); }
.tag-row { display: flex; flex-wrap: wrap; gap: 4px; justify-content: flex-end; }
.credential-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 8px; }
.credential-card { border: 1px solid var(--border, #e5e7eb); border-radius: 6px; padding: 10px; background: var(--bg-subtle, #fafafa); }
.credential-top { display: flex; justify-content: space-between; gap: 8px; align-items: center; }
.credential-line, .raw-name { color: var(--muted); font-size: 11px; margin-top: 4px; }
.metric-row { display: flex; flex-wrap: wrap; gap: 6px; color: var(--muted); font-size: 11px; margin-top: 6px; }
.badge-blue { background: #dbeafe; color: #1e40af; }
.badge-amber { background: #fef3c7; color: #92400e; }
.badge-purple { background: #ede9fe; color: #5b21b6; }

.readonly-summary {
  padding: 10px 12px;
  background: var(--bg-subtle, #fafafa);
  border-radius: 6px;
  border: 1px solid var(--border, #e5e7eb);
}
.readonly-info {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 12px;
}
.status-ok {
  color: var(--success, #15803d);
  font-weight: 600;
}
.status-bad {
  color: var(--danger, #b91c1c);
  font-weight: 600;
}
.readonly-meta {
  color: var(--muted, #6b7280);
  font-size: 11px;
}

@media (max-width: 720px) {
  .toolbar, .variant-head { flex-direction: column; align-items: stretch; }
  .tag-row { justify-content: flex-start; }
}
</style>
