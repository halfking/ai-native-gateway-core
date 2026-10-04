<script setup lang="ts">
// ModelsView — 模型目录：家族分组连续加载 + 搜索；点开 Sheet 看版本明细。
import { computed, onBeforeUnmount, ref } from 'vue'
import { ContinuousListController, useHyperPage } from '@/hyper'
import { fetchAvailableModels, type ModelFamily, type AvailableVersion } from '@/api/models'
import { t } from '@/i18n'
import HyperList from '@/components/common/HyperList.vue'
import AppSheet from '@/components/common/AppSheet.vue'
import AppIcon from '@/components/common/AppIcon.vue'
import { fmtCompact } from '@/utils/format'

useHyperPage({ title: () => t('models.title') })

let cache: ModelFamily[] = []
const query = ref('')
let unmapped = 0

const controller = new ContinuousListController<ModelFamily>({
  fetchPage: async (page) => {
    if (page > 1) return { items: [], total: filtered().length }
    if (cache.length === 0) {
      const resp = await fetchAvailableModels()
      cache = resp.families ?? []
      unmapped = resp.unmapped ?? 0
    }
    const items = filtered()
    return { items, total: items.length }
  },
  stableKey: (f) => `family-${f.id}`,
  scopeKey: 'models',
})

function filtered(): ModelFamily[] {
  const q = query.value.trim().toLowerCase()
  if (!q) return cache
  return cache.filter(
    (f) =>
      f.display_name.toLowerCase().includes(q) ||
      f.vendor.toLowerCase().includes(q) ||
      f.versions.some((v) => v.canonical_name.toLowerCase().includes(q)),
  )
}

let debounceTimer: ReturnType<typeof setTimeout> | null = null
function onSearchInput(ev: Event): void {
  const value = (ev.target as HTMLInputElement).value
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => {
    query.value = value
    controller.loadFirst('requery')
  }, 250)
}
onBeforeUnmount(() => {
  if (debounceTimer) clearTimeout(debounceTimer)
})

const detail = ref<ModelFamily | null>(null)
const detailOpen = computed({
  get: () => detail.value != null,
  set: (v: boolean) => {
    if (!v) detail.value = null
  },
})

function maxContext(f: ModelFamily): number | null {
  const vals = f.versions.map((v) => v.context_window).filter((v): v is number => v != null)
  return vals.length > 0 ? Math.max(...vals) : null
}

function maxProviders(f: ModelFamily): number {
  return Math.max(...f.versions.map((v) => v.provider_count), 0)
}

const footerHint = computed(() => (unmapped > 0 ? t('models.unmappedHint', { n: unmapped }) : ''))

function versionMeta(v: AvailableVersion): string[] {
  const parts: string[] = []
  if (v.modality) parts.push(v.modality)
  if (v.context_window != null) parts.push(t('models.contextWindow', { n: Math.round(v.context_window / 1024) }))
  if (v.parameters_b != null) parts.push(t('models.parameters', { n: v.parameters_b }))
  return parts
}
</script>

<template>
  <div class="view-root">
  <HyperList
    :controller="controller"
    :item-key="(f: ModelFamily) => `family-${f.id}`"
    :on-refresh="async () => { const resp = await fetchAvailableModels(); cache = resp.families ?? []; unmapped = resp.unmapped ?? 0 }"
    :empty-hint="footerHint || t('common.empty')"
  >
    <template #header>
      <div class="models__search">
        <AppIcon name="search" :size="18" />
        <input type="search" :placeholder="t('models.searchPlaceholder')" :aria-label="t('common.search')" @input="onSearchInput" />
      </div>
      <p v-if="footerHint" class="models__unmapped">{{ footerHint }}</p>
    </template>

    <template #item="{ item: f }">
      <button type="button" class="data-card family-card" @click="detail = f">
        <div class="card-row">
          <span class="family-card__name">{{ f.display_name }}</span>
          <span v-if="f.versions.some((v) => v.featured)" class="badge badge--info">{{ t('models.featured') }}</span>
        </div>
        <div class="family-card__meta">
          <span class="badge badge--muted">{{ f.vendor }}</span>
          <span class="family-card__stat">{{ t('models.versions', { n: f.versions.length }) }}</span>
          <span class="family-card__stat">{{ t('models.providerCount', { n: maxProviders(f) }) }}</span>
          <span v-if="maxContext(f) != null" class="family-card__stat">
            {{ t('models.contextWindow', { n: Math.round((maxContext(f) ?? 0) / 1024) }) }}
          </span>
        </div>
      </button>
    </template>
  </HyperList>

  <AppSheet v-model="detailOpen" presentation="sheet" :title="detail?.display_name ?? ''">
    <template v-if="detail">
      <div class="models__version-list">
        <div v-for="v in detail.versions" :key="v.canonical_name" class="data-card models__version">
          <div class="card-row">
            <span class="models__version-name">{{ v.display_name || v.canonical_name }}</span>
            <span class="badge badge--muted num">{{ fmtCompact(v.provider_count) }} P</span>
          </div>
          <div class="models__version-meta">
            <span v-for="meta in versionMeta(v)" :key="meta" class="family-card__stat">{{ meta }}</span>
          </div>
          <code v-if="v.canonical_name" class="models__canonical">{{ v.canonical_name }}</code>
        </div>
      </div>
    </template>
  </AppSheet>
  </div>
</template>

<style scoped>
.models__search {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  height: 48px;
  padding: 0 var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  color: var(--app-text-muted);
  margin-bottom: var(--app-space-3);
}

.models__search input {
  flex: 1;
  border: none;
  background: transparent;
  color: var(--app-text);
  font-size: var(--app-font-input);
  min-width: 0;
}

.models__search input:focus {
  outline: none;
}

.models__unmapped {
  font-size: 0.75rem;
  color: var(--app-text-muted);
  margin-bottom: var(--app-space-2);
}

.family-card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  width: 100%;
  text-align: left;
  cursor: pointer;
  font: inherit;
  color: inherit;
}

.family-card:active {
  background: var(--app-primary-softer);
}

.family-card__name {
  font-weight: 600;
  font-size: 0.9375rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.family-card__meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--app-space-1) var(--app-space-2);
}

.family-card__stat {
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.models__version-list {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-3);
}

.models__version-name {
  font-weight: 600;
  font-size: 0.875rem;
}

.models__version-meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1) var(--app-space-2);
  margin-top: var(--app-space-1);
}

.models__canonical {
  display: block;
  margin-top: var(--app-space-2);
  font-size: 0.75rem;
  color: var(--app-text-muted);
  background: var(--app-surface-muted);
  border-radius: var(--app-radius-sm);
  padding: 4px 8px;
  word-break: break-all;
}
</style>
