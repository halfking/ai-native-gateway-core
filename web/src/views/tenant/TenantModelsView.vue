<script setup lang="ts">
// TenantModelsView.vue — 租户视角的标准模型目录。
// 2026-07-12 v2:
//   - 不再按供应商分组，避免暴露供应商信息给租户管理员
//   - 列保持「标准模型 / 上下文 / 多模态 / 计费模式 / 4 个积分单价」
//   - 顶部提供模型搜索 + 多模态 / 计费模式筛选
//   - 所有文案走 i18n
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { localeRef } from '../../i18n'
import { getMaasModels } from '../../api'
import type { MaasModel } from '../../api'
import { useMaasTenantContext } from '../../composables/useMaasTenantContext'
import PageBackLink from '../../components/PageBackLink.vue'

const { t } = useI18n()
const { tenantLabel, pageTitle: ctxPageTitle, maasBackLink } = useMaasTenantContext()
const pageTitle = computed(() => ctxPageTitle(t('tenantModels.page.title')))
const backLink = computed(() => maasBackLink('models'))

const models = ref<MaasModel[]>([])
const loading = ref(false)
const error = ref('')
const search = ref('')
const filterMultimodal = ref<'all' | 'yes' | 'no'>('all')

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  return models.value.filter((m) => {
    if (filterMultimodal.value !== 'all') {
      const isMulti = supportsMultimodal(m.modality)
      if (filterMultimodal.value === 'yes' && !isMulti) return false
      if (filterMultimodal.value === 'no' && isMulti) return false
    }
    if (!q) return true
    const hay = [
      m.canonical_name,
      m.display_name,
      m.family_display_name ?? '',
      m.family ?? '',
      m.modality,
    ]
      .join(' ')
      .toLowerCase()
    return hay.includes(q)
  })
})

function modalityLabel(modality: string) {
  return t(`tenantModels.modalities.${modality}`, modality)
}

function billingLabel(mode: string) {
  return t(`tenantModels.billing.${mode}`, mode)
}

function supportsMultimodal(modality: string) {
  return modality === 'multimodal' || modality === 'vision' || modality === 'audio'
}

function fmtCredits(n: number) {
  return n.toLocaleString(localeRef.value)
}

function fmtContext(ctx: number | null | undefined) {
  if (ctx == null || ctx <= 0) return t('tenantModels.context.notSet')
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
    error.value = e instanceof Error ? e.message : t('tenantModels.page.loadFailed')
  } finally {
    loading.value = false
  }
}

function clearFilters() {
  search.value = ''
  filterMultimodal.value = 'all'
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
          {{ loading ? t('tenantModels.page.loading') : t('tenantModels.page.refresh') }}
        </button>
      </div>
    </div>

    <div class="filter-bar">
      <div class="filter-bar__group">
        <label class="filter-bar__label">{{ t('models.filter.textSearchPlaceholder') }}</label>
        <input
          v-model="search"
          class="filter-bar__input"
          type="search"
          :placeholder="t('tenantModels.page.filterPlaceholder')"
        />
      </div>
      <div class="filter-bar__group">
        <label class="filter-bar__label">{{ t('tenantModels.columns.multimodal') }}</label>
        <select v-model="filterMultimodal" class="filter-bar__select">
          <option value="all">{{ t('common.all') }}</option>
          <option value="yes">{{ t('tenantModels.multimodal.yes') }}</option>
          <option value="no">{{ t('tenantModels.multimodal.no') }}</option>
        </select>
      </div>
      <div class="filter-bar__count">
        {{ t('tenantModels.filterBar.count', { n: filtered.length, m: models.length }) }}
        <button
          v-if="search || filterMultimodal !== 'all'"
          type="button"
          class="link-btn"
          @click="clearFilters"
        >
          {{ t('common.button.clear') }}
        </button>
      </div>
    </div>

    <p class="page-desc">{{ t('tenantModels.page.desc') }}</p>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-else-if="loading && !models.length" class="empty">{{ t('tenantModels.page.loading') }}</div>
    <div v-else-if="!filtered.length" class="empty card">{{ t('tenantModels.page.empty') }}</div>

    <div v-else class="card model-card">
      <div class="card-title">
        {{ t('tenantModels.page.vendorSectionTitle') }}
        <span class="hint">{{ t('tenantModels.page.modelCount', { n: filtered.length }) }}</span>
      </div>
      <div class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t('tenantModels.columns.model') }}</th>
              <th>{{ t('tenantModels.columns.contextWindow') }}</th>
              <th>{{ t('tenantModels.columns.multimodal') }}</th>
              <th>{{ t('tenantModels.columns.billingMode') }}</th>
              <th class="num-col">{{ t('tenantModels.columns.inPrice') }}</th>
              <th class="num-col">{{ t('tenantModels.columns.outPrice') }}</th>
              <th class="num-col">{{ t('tenantModels.columns.cacheIn') }}</th>
              <th class="num-col">{{ t('tenantModels.columns.cacheOut') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in filtered" :key="m.canonical_name">
              <td>
                <div class="model-name">{{ m.display_name }}</div>
                <code class="model-code">{{ m.canonical_name }}</code>
                <div v-if="m.family_display_name" class="model-family">
                  {{ m.family_display_name }}
                </div>
              </td>
              <td>{{ fmtContext(m.context_window) }}</td>
              <td>
                <span
                  class="badge"
                  :class="supportsMultimodal(m.modality) ? 'badge-yes' : 'badge-no'"
                >
                  {{ supportsMultimodal(m.modality) ? t('tenantModels.multimodal.yes') : t('tenantModels.multimodal.no') }}
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
.filter-bar {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: flex-end;
  margin-bottom: 12px;
  padding: 12px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
}
.filter-bar__group {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 180px;
}
.filter-bar__label {
  font-size: 11px;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.filter-bar__input,
.filter-bar__select {
  padding: 6px 10px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-size: 13px;
}
.filter-bar__count {
  margin-inline-start: auto;
  font-size: 12px;
  color: var(--muted);
  display: flex;
  align-items: center;
  gap: 8px;
}
.link-btn {
  background: none;
  border: none;
  color: var(--accent-h);
  font-size: 12px;
  cursor: pointer;
  padding: 0;
}
.link-btn:hover {
  text-decoration: underline;
}
.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  margin-bottom: 16px;
  overflow: hidden;
}
.model-card {
  padding: 0;
}
.card-title {
  padding: 14px 16px 10px;
  border-bottom: 1px solid var(--border);
  background: rgba(99, 102, 241, 0.04);
  font-size: 15px;
  font-weight: 600;
}
.card-title .hint {
  font-weight: 400;
  font-size: 12px;
  color: var(--muted);
  margin-inline-start: 8px;
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