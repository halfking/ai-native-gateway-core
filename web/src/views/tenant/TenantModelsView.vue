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
import { useWindowClass } from '../../composables/useWindowClass'
import ResponsiveDataView from '../../components/ui/ResponsiveDataView.vue'
import type { CardField } from '../../components/ui/CardList.vue'
import { sortByName } from '../../utils/sortByName'
import PageBackLink from '../../components/PageBackLink.vue'

const { t } = useI18n()
const { isCompact } = useWindowClass()
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
  const hits = models.value.filter((m) => {
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
  // 2026-10-03：老板要「有名称的列表按名称排序」。本表首列就是模型标识，
  // 之前完全按后端返回顺序 —— 找特定模型只能翻页。
  // 排序键必须与首列一致：显式传 ['canonical_name']，不要用默认候选
  // （MaasModel 带 display_name，默认会先按中文显示名排，与首列不一致）。
  return sortByName(hits, undefined, ['canonical_name'])
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

/**
 * ── H6 第九条切片（2026-10-06）：模型目录表接 compact 卡片形态 ──────────────
 * 与切片七/八同源（无分页 API，只改呈现形态），但**三态归属是第三种形态**：
 *
 * 本页桌面空态是 `v-else-if="!filtered.length"` —— 整个 `.card.model-card`
 * （连标题带）都被撤掉，**不是「表在、空文案在」**。
 * ⇒ 容器挂在 `v-else` 分支里，**无数据时根本不挂载**，
 * 所以既不需要 `:empty` 也不需要 `:loading`：两档共用本页自己的 `.empty`。
 * （切片四~八是「桌面三态在页面里、容器只裁 compact」；这一页是「桌面三态把整块撤掉、
 * 容器压根不出现」。**两种都合法，判据是「空态时那块东西在不在」。**）
 *
 * `table-min-width` 这里传 **720px 而不是 0px**：本页 `.table` 自带 `min-width: 720px`，
 * 传 0px 会用容器的 CSS 变量把它**改小** —— 那是对桌面的真实变更。
 * 同理删掉 `.table-wrap`：容器自带 `overflow-x`，两个横滚容器嵌套会出双滚动条。
 */
/**
 * 副标题键：家族名。**只有在确实存在带家族的模型时才挂** ——
 * `CardList` 对缺值渲染 `—`，无家族时整页挂一个 `family_display_name` 就是一行破折号。
 */
const familyKeys = computed<string[]>(() =>
  filtered.value.some((m) => m.family_display_name) ? ['family_display_name'] : [],
)

const modelCardFields = computed<CardField[]>(() => [  { key: 'canonical_name', label: t('tenantModels.columns.model') },
  { key: 'context_window', label: t('tenantModels.columns.contextWindow'), format: (v) => fmtContext(v as number | null) },
  {
    // 桌面那一格是「是/否 徽章 + 模态标签」两段；卡片合进一个字段，两段信息都不丢
    key: 'modality',
    label: t('tenantModels.columns.multimodal'),
    type: 'badge',
    tone: (row) => (supportsMultimodal(String(row.modality ?? '')) ? 'good' : 'neutral'),
    format: (v) =>
      supportsMultimodal(String(v)) ? `${t('tenantModels.multimodal.yes')} · ${modalityLabel(String(v))}` : t('tenantModels.multimodal.no'),
  },
  { key: 'billing_mode', label: t('tenantModels.columns.billingMode'), format: (v) => billingLabel(String(v)) },
  { key: 'credits_per_1m_in', label: t('tenantModels.columns.inPrice'), format: (v) => fmtCredits(Number(v)) },
  { key: 'credits_per_1m_out', label: t('tenantModels.columns.outPrice'), format: (v) => fmtCredits(Number(v)) },
  {
    key: 'credits_per_1m_cache_in',
    label: t('tenantModels.columns.cacheIn'),
    format: (v, row) => fmtCredits(Number(v ?? row.credits_per_1m_in ?? 0)),
  },
  {
    key: 'credits_per_1m_cache_out',
    label: t('tenantModels.columns.cacheOut'),
    format: (v, row) => fmtCredits(Number(v ?? row.credits_per_1m_out ?? 0)),
  },
])

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
      <ResponsiveDataView
        :rows="filtered"
        title-key="display_name"
        :subtitle-keys="familyKeys"
        :fields="modelCardFields"
        table-min-width="720px"
      >
        <template #table>
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
        </template>
      </ResponsiveDataView>
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
  background: color-mix(in srgb, var(--accent) 4%, transparent);
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
  background: var(--success-bg);
  color: var(--success);
}
.badge-no {
  background: var(--neutral-bg);
  color: var(--muted);
}
.empty {
  text-align: center;
  padding: 40px;
  color: var(--muted);
}
.alert-danger {
  padding: 8px 12px;
  border-radius: 4px;
  background: color-mix(in srgb, var(--danger) 14%, transparent);
  color: var(--danger);
  margin-bottom: 12px;
}
.tenant-badge {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  background: var(--info-bg);
  color: var(--accent);
}
</style>