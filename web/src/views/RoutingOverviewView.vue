<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  getRoutingModelTree,
  type RoutingModelTreeResponse,
  type RoutingTreeCredential,
} from '../api'
import { isReadOnlyMode } from '../store'

interface FlatVariant {
  id: string
  series: string
  generation: string
  variant: string
  canonical_name: string
  tags: string[]
  credentials: RoutingTreeCredential[]
  routableCount: number
  featured: boolean
}

const tree = ref<RoutingModelTreeResponse>({ featured: [], series: [], unmapped: [] })
const loading = ref(false)
const error = ref('')
const search = ref('')
const featuredOnly = ref(false)
const selectedSeries = ref('')
const selectedVariantId = ref('')
const readOnly = computed(() => isReadOnlyMode())

const featuredSet = computed(() => new Set(tree.value.featured.map(s => s.toLowerCase())))

const flatVariants = computed((): FlatVariant[] => {
  const out: FlatVariant[] = []
  for (const series of tree.value.series) {
    for (const gen of series.generations) {
      for (const v of gen.variants) {
        const id = `${series.series}::${gen.generation}::${v.canonical_name}`
        const routableCount = v.credentials.filter(c => c.runtime_routable).length
        out.push({
          id,
          series: series.series,
          generation: gen.generation,
          variant: v.variant,
          canonical_name: v.canonical_name,
          tags: v.tags,
          credentials: v.credentials,
          routableCount,
          featured: featuredSet.value.has(v.canonical_name.toLowerCase()),
        })
      }
    }
  }
  return out
})

const seriesList = computed(() => {
  const q = search.value.trim().toLowerCase()
  const base = featuredOnly.value
    ? flatVariants.value.filter(v => v.featured)
    : flatVariants.value
  const filtered = q
    ? base.filter(v => variantHaystack(v).includes(q))
    : base
  const counts = new Map<string, number>()
  for (const v of filtered) {
    counts.set(v.series, (counts.get(v.series) ?? 0) + 1)
  }
  return [...counts.entries()]
    .sort((a, b) => a[0].localeCompare(b[0]))
    .map(([name, count]) => ({ name, count }))
})

const variantPills = computed(() => {
  const q = search.value.trim().toLowerCase()
  let list = featuredOnly.value
    ? flatVariants.value.filter(v => v.featured)
    : flatVariants.value
  if (selectedSeries.value) {
    list = list.filter(v => v.series === selectedSeries.value)
  }
  if (q) {
    list = list.filter(v => variantHaystack(v).includes(q))
  }
  return list.sort((a, b) => {
    if (a.featured !== b.featured) return a.featured ? -1 : 1
    return a.canonical_name.localeCompare(b.canonical_name)
  })
})

const selectedVariant = computed(() =>
  flatVariants.value.find(v => v.id === selectedVariantId.value) ?? null,
)

const heroChips = computed(() => {
  const total = flatVariants.value.length
  const routable = flatVariants.value.filter(v => v.routableCount > 0).length
  const creds = flatVariants.value.reduce((s, v) => s + v.credentials.length, 0)
  return [
    { label: '系列', value: String(seriesList.value.length) },
    { label: '变体', value: String(total) },
    { label: '可路由', value: String(routable) },
    { label: '凭据', value: readOnly.value ? '—' : String(creds) },
  ]
})

function variantHaystack(v: FlatVariant): string {
  return [
    v.series, v.generation, v.variant, v.canonical_name,
    ...v.tags,
    ...v.credentials.flatMap(c => [c.provider_name, c.credential_label, c.raw_model_name]),
  ].join(' ').toLowerCase()
}

function isCredentialRoutable(c: RoutingTreeCredential): boolean {
  return c.runtime_routable
}

function reasonLabel(reason: string | null | undefined): string {
  switch (reason) {
    case 'circuit_open': return '熔断中'
    case 'balance_exhausted': return '余额耗尽'
    case 'quota_periodic_exhausted': return '周期额度耗尽'
    case 'quota_balance_exhausted': return '额度耗尽'
    case 'quota_permanently_exhausted': return '永久额度耗尽'
    case 'availability_cooling': return '冷却中'
    case 'availability_rate_limited': return '限流中'
    case 'availability_unreachable': return '暂不可达'
    case 'availability_auth_failed': return '鉴权失败'
    case 'availability_suspended': return '已暂停'
    case 'lifecycle_disabled': return '生命周期禁用'
    case 'lifecycle_suspended': return '生命周期暂停'
    case 'lifecycle_retired': return '生命周期退役'
    default: return reason || '可路由'
  }
}

function latencyLabel(ms: number): string {
  if (!ms || ms <= 0) return '-'
  return ms < 1000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(1)}s`
}

function rateLabel(r: number): string {
  return !r || r <= 0 ? '-' : `${(r * 100).toFixed(0)}%`
}

function money(value: number | string | null | undefined, currency = 'USD'): string {
  if (value === null || value === undefined) return '-'
  const n = typeof value === 'string' ? Number(value) : value
  return Number.isNaN(n) ? '-' : n.toFixed(2)
}

function priceLabel(c: RoutingTreeCredential): string {
  return `${money(c.unit_price_in_per_1m)}/${money(c.unit_price_out_per_1m)}`
}

function statusClass(c: RoutingTreeCredential): string {
  if (!isCredentialRoutable(c)) return 'badge-red'
  if (c.tier === 1) return 'badge-green'
  if (c.tier === 3) return 'badge-purple'
  return 'badge-amber'
}

function selectSeries(name: string) {
  selectedSeries.value = selectedSeries.value === name ? '' : name
  if (selectedVariantId.value) {
    const v = flatVariants.value.find(x => x.id === selectedVariantId.value)
    if (v && selectedSeries.value && v.series !== selectedSeries.value) {
      selectedVariantId.value = ''
    }
  }
}

function selectVariant(v: FlatVariant) {
  selectedVariantId.value = selectedVariantId.value === v.id ? '' : v.id
  if (!selectedSeries.value) selectedSeries.value = v.series
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    tree.value = await getRoutingModelTree(featuredOnly.value)
    if (selectedVariantId.value && !flatVariants.value.some(v => v.id === selectedVariantId.value)) {
      selectedVariantId.value = ''
    }
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
  <div class="routing-overview-view">
    <div class="top-bar">
      <div class="top-bar-head">
        <router-link to="/routing-v2" class="back-link">← 路由全景</router-link>
        <h2>模型路由全景</h2>
        <div class="toolbar-filters">
          <button
            class="profile-pill"
            :class="{ active: featuredOnly }"
            @click="toggleFeatured"
          >{{ featuredOnly ? '★ 仅特色' : '☆ 仅特色' }}</button>
        </div>
        <button class="btn btn-sm btn-ghost refresh-btn" :disabled="loading" @click="load" title="刷新">↻</button>
      </div>
      <div class="hero-stats">
        <span v-for="c in heroChips" :key="c.label" class="chip">{{ c.label }} <strong>{{ c.value }}</strong></span>
      </div>
    </div>

    <div v-if="readOnly" class="card compact-card alert-info-card">
      📖 租户管理员视图：凭据详情（供应商、价格、状态）已隐藏。
    </div>

    <div v-if="error" class="alert alert-danger compact-alert">{{ error }}</div>

    <!-- Model selection -->
    <div class="card compact-card">
      <div class="card-toolbar">
        <div class="toolbar-left">
          <span class="layer-tag l2">L2</span>
          <span class="toolbar-title">模型选择</span>
        </div>
        <input v-model="search" class="search-input" placeholder="搜索系列、变体、供应商…" />
      </div>

      <div v-if="loading" class="loading-hint">加载中…</div>
      <template v-else>
        <div v-if="seriesList.length" class="pill-section">
          <span class="pill-label">系列</span>
          <div class="pill-row">
            <button
              v-for="s in seriesList"
              :key="s.name"
              class="task-pill"
              :class="{ active: selectedSeries === s.name }"
              @click="selectSeries(s.name)"
            >{{ s.name }} <span class="pill-count">{{ s.count }}</span></button>
          </div>
        </div>

        <div v-if="variantPills.length" class="pill-section">
          <span class="pill-label">变体</span>
          <div class="pill-row">
            <button
              v-for="v in variantPills"
              :key="v.id"
              class="task-pill variant-pill"
              :class="{ active: selectedVariantId === v.id, featured: v.featured }"
              :title="v.canonical_name"
              @click="selectVariant(v)"
            >
              <span v-if="v.featured" class="star">★</span>
              {{ v.variant }}
              <span class="pill-count" :class="v.routableCount > 0 ? 'ok' : 'bad'">{{ v.routableCount }}/{{ v.credentials.length }}</span>
            </button>
          </div>
        </div>
        <div v-else class="text-muted empty-inline">暂无匹配模型</div>
      </template>
    </div>

    <!-- Selected variant detail -->
    <div v-if="selectedVariant" class="tab-content">
      <div class="card compact-card">
        <div class="section-head tight">
          <h3>{{ selectedVariant.variant }}</h3>
          <code class="key-code">{{ selectedVariant.canonical_name }}</code>
          <span class="text-muted">{{ selectedVariant.series }} · {{ selectedVariant.generation }}</span>
          <div class="tag-row">
            <span v-for="tag in selectedVariant.tags" :key="tag" class="badge badge-gray">{{ tag }}</span>
          </div>
        </div>

        <div v-if="readOnly" class="readonly-summary">
          <span :class="selectedVariant.routableCount > 0 ? 'status-ok' : 'status-bad'">
            {{ selectedVariant.routableCount > 0 ? '✓ 可路由' : '✗ 暂不可路由' }}
          </span>
          <span class="text-muted">凭据 {{ selectedVariant.credentials.length }} 条（详情已隐藏）</span>
        </div>

        <div v-else-if="selectedVariant.credentials.length" class="table-wrap">
          <table class="dense-table">
            <thead>
              <tr>
                <th>供应商</th><th>凭据</th><th>Tier</th><th>状态</th><th>成功率</th><th>P95</th><th>入/出 $/1M</th><th>上游模型</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="cred in selectedVariant.credentials"
                :key="`${cred.provider_id}-${cred.credential_id}-${cred.raw_model_name}`"
                :style="cred.runtime_routable ? '' : 'opacity:0.55'"
              >
                <td>{{ cred.provider_name }}</td>
                <td>
                  <div>#{{ cred.credential_id }}</div>
                  <div class="text-muted">{{ cred.credential_label }}</div>
                </td>
                <td><span class="badge" :class="statusClass(cred)">T{{ cred.tier }} · w{{ cred.weight }}</span></td>
                <td>
                  <span :class="cred.runtime_routable ? 'badge badge-green' : 'badge badge-red'">
                    {{ cred.runtime_routable ? '可路由' : reasonLabel(cred.runtime_block_reason) }}
                  </span>
                </td>
                <td>{{ rateLabel(cred.success_rate) }}</td>
                <td>{{ latencyLabel(cred.p95_latency_ms) }}</td>
                <td class="price-cell">{{ priceLabel(cred) }}</td>
                <td><code class="mono-sm">{{ cred.raw_model_name }}</code></td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="text-muted">该变体暂无凭据配置</div>
      </div>
    </div>

    <div v-else-if="!loading && variantPills.length" class="card compact-card hint-card">
      <p class="text-muted">点击上方变体按钮查看凭据路由详情</p>
    </div>

  </div>
</template>

<style scoped>
.routing-overview-view { max-width: 1200px; }

.top-bar {
  margin-bottom: 8px;
  padding: 8px 10px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.top-bar-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 6px;
}
.top-bar-head h2 { font-size: 15px; margin: 0; }
.back-link { font-size: 11px; color: var(--muted); text-decoration: none; }
.back-link:hover { color: var(--accent-h); }
.refresh-btn { margin-left: auto; }

.hero-stats { display: flex; flex-wrap: wrap; gap: 4px; }
.chip {
  display: inline-flex; align-items: center; gap: 3px;
  padding: 2px 8px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 99px;
  font-size: 10px;
  color: var(--muted);
}
.chip strong { color: var(--text); font-weight: 600; }

.tab-content { display: flex; flex-direction: column; gap: 8px; }
.compact-card { padding: 8px 10px; margin-bottom: 8px; }
.compact-alert { padding: 4px 8px; font-size: 11px; margin-bottom: 8px; }
.alert-info-card { font-size: 11px; color: var(--muted); }

.card-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  flex-wrap: wrap;
  margin-bottom: 6px;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--border);
}
.toolbar-left { display: flex; align-items: center; gap: 6px; }
.toolbar-title { font-size: 12px; font-weight: 600; }
.toolbar-filters { display: flex; align-items: center; gap: 3px; }

.layer-tag {
  display: inline-flex; align-items: center; justify-content: center;
  width: 22px; height: 14px;
  border-radius: 3px;
  font-size: 8px; font-weight: 700;
}
.layer-tag.l2 { background: rgba(63,185,80,.22); color: var(--success); }

.search-input {
  flex: 1;
  min-width: 140px;
  max-width: 280px;
  font-size: 11px;
  padding: 3px 8px;
}

.profile-pill {
  padding: 1px 7px;
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 99px;
  font-size: 10px;
  cursor: pointer;
  color: var(--muted);
}
.profile-pill.active {
  border-color: var(--accent);
  color: var(--accent-h);
  background: color-mix(in srgb, var(--accent) 10%, transparent);
}

.pill-section { margin-bottom: 8px; }
.pill-section:last-child { margin-bottom: 0; }
.pill-label {
  display: block;
  font-size: 9px;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: .04em;
  margin-bottom: 4px;
  font-weight: 600;
}
.pill-row {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.task-pill {
  padding: 2px 8px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 99px;
  font-size: 11px;
  cursor: pointer;
  transition: all .12s;
  color: var(--text);
  white-space: nowrap;
}
.task-pill:hover { border-color: var(--accent); }
.task-pill.active {
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 12%, var(--bg-subtle));
}
.task-pill.featured .star { color: var(--accent-h); font-size: 9px; }
.pill-count {
  font-size: 9px;
  color: var(--muted);
  margin-left: 2px;
}
.pill-count.ok { color: var(--success); }
.pill-count.bad { color: var(--danger); }

.section-head {
  display: flex; align-items: center; gap: 6px; flex-wrap: wrap;
  margin-bottom: 6px;
}
.section-head.tight { margin-bottom: 4px; }
.section-head h3 { margin: 0; font-size: 12px; font-weight: 600; }

.key-code { font-size: 10px; font-family: ui-monospace, monospace; color: var(--accent-h); }
.tag-row { display: flex; flex-wrap: wrap; gap: 4px; margin-left: auto; }
.mono-sm { font-family: ui-monospace, monospace; font-size: 9px; }
.price-cell { font-variant-numeric: tabular-nums; font-size: 10px; }

.dense-table { font-size: 11px; width: 100%; }
.dense-table thead th { padding: 3px 6px; font-size: 9px; white-space: nowrap; }
.dense-table tbody td { padding: 4px 6px; }
.table-wrap { overflow-x: auto; -webkit-overflow-scrolling: touch; }

.loading-hint { padding: 12px; text-align: center; color: var(--muted); font-size: 11px; }
.empty-inline { padding: 8px 0; font-size: 11px; }
.hint-card { text-align: center; padding: 16px; }
.hint-card p { margin: 0; }
.text-muted { color: var(--muted); font-size: 10px; }

.readonly-summary {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px;
  background: var(--bg-subtle);
  border-radius: 4px;
  font-size: 11px;
}
.status-ok { color: var(--success); font-weight: 600; }
.status-bad { color: var(--danger); font-weight: 600; }

.badge-purple { background: #ede9fe; color: #5b21b6; }

@media (max-width: 720px) {
  .search-input { max-width: 100%; width: 100%; }
  .tag-row { margin-left: 0; width: 100%; }
}
</style>
