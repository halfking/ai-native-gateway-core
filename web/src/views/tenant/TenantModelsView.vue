<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { getMaasModels } from '../../api'
import type { MaasModel } from '../../api'
import { useMaasTenantContext } from '../../composables/useMaasTenantContext'
import PageBackLink from '../../components/PageBackLink.vue'
import ModelCatalogFilterBar from '../../components/ModelCatalogFilterBar.vue'
import { useModelCatalogFilters } from '../../composables/useModelCatalogFilters'
import { vendorLabelZh } from '../../utils/modelCatalog'

const { tenantLabel, pageTitle: ctxPageTitle, maasBackLink } = useMaasTenantContext()
const pageTitle = computed(() => ctxPageTitle('标准模型'))
const backLink = computed(() => maasBackLink('models'))

const models = ref<MaasModel[]>([])
const loading = ref(false)
const error = ref('')

const {
  pickedModel,
  filterVendor,
  filtered,
  vendorOptions,
  clearFilters,
} = useModelCatalogFilters<MaasModel>({
  items: models,
  getVendor: (m) => m.vendor?.trim() || '其他',
  getCanonicalName: (m) => m.canonical_name,
  getDisplayName: (m) => m.display_name,
  getSearchExtras: (m) => [
    m.family_display_name ?? '',
    m.family ?? '',
    m.modality,
    vendorLabelZh(m.vendor?.trim() || '其他'),
  ],
})

const vendorGroups = computed(() => {
  const map = new Map<string, MaasModel[]>()
  for (const m of filtered.value) {
    const vendor = m.vendor?.trim() || '其他'
    const list = map.get(vendor) ?? []
    list.push(m)
    map.set(vendor, list)
  }
  return [...map.entries()]
    .sort(([a], [b]) => vendorLabelZh(a).localeCompare(vendorLabelZh(b), 'zh-CN'))
    .map(([vendor, items]) => ({ vendor, vendorLabel: vendorLabelZh(vendor), items }))
})

const MODALITY_LABELS: Record<string, string> = {
  text: '文本',
  vision: '视觉',
  audio: '音频',
  multimodal: '多模态',
  embedding: '向量',
}

const BILLING_LABELS: Record<string, string> = {
  token: '按 Token 积分',
}

const rateCols = [
  { key: 'in', label: '输入/1M' },
  { key: 'out', label: '输出/1M' },
  { key: 'cache_in', label: '缓存读/1M' },
  { key: 'cache_out', label: '缓存写/1M' },
] as const

function fmtCredits(n: number) {
  return n.toLocaleString('zh-CN')
}

function modalityLabel(modality: string) {
  return MODALITY_LABELS[modality] ?? modality
}

function billingLabel(mode: string) {
  return BILLING_LABELS[mode] ?? mode
}

function supportsMultimodal(modality: string) {
  return modality === 'multimodal' || modality === 'vision' || modality === 'audio'
}

function fmtContext(ctx: number | null | undefined) {
  if (ctx == null || ctx <= 0) return '—'
  if (ctx >= 1_000_000) return `${(ctx / 1_000_000).toFixed(ctx % 1_000_000 === 0 ? 0 : 1)}M`
  if (ctx >= 1000) return `${Math.round(ctx / 1000)}K`
  return String(ctx)
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const res = await getMaasModels()
    models.value = res.items ?? []
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="tenant-models-page">
    <div class="page-header">
      <PageBackLink v-if="backLink" :to="backLink.to" :label="backLink.label" />
      <h2>{{ pageTitle }}</h2>
      <div class="page-header-actions">
        <span class="tenant-badge">{{ tenantLabel }}</span>
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
          {{ loading ? '加载中…' : '刷新' }}
        </button>
      </div>
    </div>

    <ModelCatalogFilterBar
      v-model:picked-model="pickedModel"
      v-model:filter-vendor="filterVendor"
      :vendor-options="vendorOptions"
      :count="filtered.length"
      picker-title="标准模型 · 筛选"
      picker-placeholder="选择标准模型…"
      @clear="clearFilters"
    />

    <p class="page-desc">
      平台标准模型目录，按模型原厂分组。计费含输入 / 输出 / 缓存读 / 缓存写，均按每百万 Token 扣积分。
    </p>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-else-if="loading && !models.length" class="empty">加载中…</div>
    <div v-else-if="!vendorGroups.length" class="empty card">暂无可用模型</div>

    <div v-for="group in vendorGroups" :key="group.vendor" class="vendor-section card">
      <div class="vendor-header">
        <h3>{{ group.vendorLabel }}</h3>
        <span class="vendor-count">{{ group.items.length }} 个模型</span>
      </div>
      <div class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th>标准模型</th>
              <th>上下文</th>
              <th>多模态</th>
              <th>计费模式</th>
            <th v-for="col in rateCols" :key="col.key" class="num-col">{{ col.label }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in group.items" :key="m.canonical_name">
              <td>
                <div class="model-name">{{ m.display_name }}</div>
                <code class="model-code">{{ m.canonical_name }}</code>
                <div v-if="m.family_display_name" class="model-family">{{ m.family_display_name }}</div>
              </td>
              <td>{{ fmtContext(m.context_window) }}</td>
              <td>
                <span
                  class="badge"
                  :class="supportsMultimodal(m.modality) ? 'badge-yes' : 'badge-no'"
                >
                  {{ supportsMultimodal(m.modality) ? '支持' : '仅文本' }}
                </span>
                <span class="modality-tag">{{ modalityLabel(m.modality) }}</span>
              </td>
              <td>{{ billingLabel(m.billing_mode) }}</td>
              <td class="num">{{ fmtCredits(m.credits_per_1m_in) }}</td>
              <td class="num">{{ fmtCredits(m.credits_per_1m_out) }}</td>
              <td class="num">{{ fmtCredits(m.credits_per_1m_cache_in ?? m.credits_per_1m_in) }}</td>
              <td class="num">{{ fmtCredits(m.credits_per_1m_cache_out ?? m.credits_per_1m_out) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-header-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.page-desc {
  font-size: 13px;
  color: var(--muted);
  margin: -8px 0 16px;
}
.search-input {
  padding: 6px 10px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-size: 13px;
  width: 200px;
}
.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  margin-bottom: 16px;
  overflow: hidden;
}
.vendor-section {
  padding: 0;
}
.vendor-header {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  padding: 14px 16px 10px;
  border-bottom: 1px solid var(--border);
  background: rgba(99, 102, 241, 0.04);
}
.vendor-header h3 {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
}
.vendor-count {
  font-size: 12px;
  color: var(--muted);
}
.table-wrap {
  overflow-x: auto;
}
.table {
  width: 100%;
  min-width: 720px;
}
.model-name {
  font-weight: 600;
  font-size: 13px;
}
.model-code {
  display: block;
  font-size: 11px;
  color: var(--muted);
  margin-top: 2px;
}
.model-family {
  font-size: 11px;
  color: var(--muted);
  margin-top: 2px;
}
.num-col,
.num {
  text-align: right;
  font-family: 'SF Mono', 'Fira Code', monospace;
  font-size: 13px;
  white-space: nowrap;
}
.modality-tag {
  display: block;
  font-size: 11px;
  color: var(--muted);
  margin-top: 4px;
}
.badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 8px;
  font-size: 11px;
}
.badge-yes {
  background: rgba(34, 197, 94, 0.12);
  color: #4ade80;
}
.badge-no {
  background: rgba(156, 163, 175, 0.12);
  color: #9ca3af;
}
.empty {
  text-align: center;
  padding: 40px;
  color: var(--muted);
}
.alert-danger {
  padding: 8px 12px;
  border-radius: 4px;
  background: rgba(239, 68, 68, 0.1);
  color: #f87171;
  margin-bottom: 12px;
}
.tenant-badge {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  background: rgba(59, 130, 246, 0.1);
  color: #3b82f6;
}
</style>
