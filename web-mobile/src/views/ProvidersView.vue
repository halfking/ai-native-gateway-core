<script setup lang="ts">
// ProvidersView — 供应商列表：卡片 + 搜索 + 可用性筛选（抽屉席位）。
//
// 2026-10-06 新增。选它的理由：菜单里 `/providers`（UI规范 17 §2 席位映射外的
// 桌面页）是运维高频入口，而移动端此前只能从节点卡反推供应商 —— 供应商维度的
// 「谁挂了 / 谁没绑模型 / 谁被手动停用」在移动端完全不可见。
//
// 纯只读（h.providerConsole，admin/handler.go:1224）：改供应商是重操作，
// 留在桌面（§11.4 同款分工）。
import { computed, onBeforeUnmount, ref } from 'vue'
import { ContinuousListController, useHyperPage } from '@/hyper'
import { getProviders, providerName, type Provider, type RoutabilityFilter } from '@/api/providers'
import { t } from '@/i18n'
import HyperList from '@/components/common/HyperList.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import AppIcon from '@/components/common/AppIcon.vue'
import NodeAuditSheet from '@/components/shell/NodeAuditSheet.vue'

useHyperPage({ title: () => t('providers.title') })

let cache: Provider[] = []
const query = ref('')
const routability = ref<RoutabilityFilter>('all')

/** 筛选条：与后端 routability 取值 1:1（桌面 providers.ts:107-113）。 */
const FILTERS: Array<{ key: RoutabilityFilter; labelKey: string }> = [
  { key: 'all', labelKey: 'providers.filterAll' },
  { key: 'available', labelKey: 'providers.filterAvailable' },
  { key: 'unavailable', labelKey: 'providers.filterUnavailable' },
  { key: 'no_models', labelKey: 'providers.filterNoModels' },
  { key: 'manual_disabled', labelKey: 'providers.filterManualDisabled' },
]

const controller = new ContinuousListController<Provider>({
  fetchPage: async (page) => {
    if (page > 1) return { items: [], total: filtered().length }
    // 筛选走服务端（后端有 routability 过滤），搜索走客户端 —— 与桌面同款分工。
    if (cache.length === 0) {
      cache = await getProviders({ routability: routability.value })
    }
    const items = filtered()
    return { items, total: items.length }
  },
  stableKey: (p) => `provider-${p.id}`,
  scopeKey: 'providers',
})

function filtered(): Provider[] {
  const q = query.value.trim().toLowerCase()
  if (!q) return cache
  return cache.filter(
    (p) =>
      providerName(p).toLowerCase().includes(q) ||
      p.code.toLowerCase().includes(q) ||
      p.catalog_code.toLowerCase().includes(q),
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

/** 换筛选条件要**重取**（参数进了 query），不是只重查本地。 */
async function onFilterChange(key: RoutabilityFilter): Promise<void> {
  if (routability.value === key) return
  routability.value = key
  cache = []
  controller.loadFirst('requery')
}

function tone(p: Provider): 'success' | 'warning' | 'danger' | 'muted' {
  if (p.manual_disabled) return 'muted'
  if (p.routability === 'unavailable') return 'danger'
  if (p.health_status === 'warning' || p.routability === 'no_models') return 'warning'
  if (!p.enabled) return 'muted'
  return 'success'
}

function badge(p: Provider): { cls: string; label: string } {
  if (p.manual_disabled) return { cls: 'badge--muted', label: t('providers.manualDisabled') }
  if (!p.enabled) return { cls: 'badge--muted', label: t('providers.disabled') }
  if (p.routability === 'unavailable') return { cls: 'badge--danger', label: t('providers.unavailable') }
  if (p.routability === 'no_models') return { cls: 'badge--warning', label: t('providers.noModels') }
  if (p.health_status === 'warning') return { cls: 'badge--warning', label: t('providers.degraded') }
  if (p.routability === 'available') return { cls: 'badge--success', label: t('providers.available') }
  return { cls: 'badge--muted', label: p.health_status || t('common.unknown') }
}

// 节点操作审计：按 provider 维度（覆盖面依据见 api/nodeAudit.ts 头注）。
const auditProvider = ref<Provider | null>(null)
const auditOpen = computed({
  get: () => auditProvider.value != null,
  set: (v: boolean) => {
    if (!v) auditProvider.value = null
  },
})

const summary = computed(() => {
  const total = cache.length
  const down = cache.filter((p) => p.routability === 'unavailable').length
  const off = cache.filter((p) => p.manual_disabled || !p.enabled).length
  return { total, down, off }
})
</script>

<template>
  <div class="view-root providers">
  <HyperList
    :controller="controller"
    :item-key="(p: Provider) => `provider-${p.id}`"
    :on-refresh="async () => { cache = await getProviders({ routability: routability }) }"
    :empty-hint="t('common.empty')"
  >
    <template #header>
      <div class="providers__search">
        <AppIcon name="search" :size="18" />
        <input
          type="search"
          :placeholder="t('providers.searchPlaceholder')"
          :aria-label="t('common.search')"
          @input="onSearchInput"
        />
      </div>

      <div class="providers__filters" role="tablist">
        <button
          v-for="flt in FILTERS"
          :key="flt.key"
          type="button"
          class="providers__chip"
          :class="{ 'providers__chip--on': routability === flt.key }"
          :aria-selected="routability === flt.key"
          @click="onFilterChange(flt.key)"
        >
          {{ t(flt.labelKey) }}
        </button>
      </div>

      <p v-if="cache.length > 0" class="providers__summary">
        {{ t('providers.summary', { total: summary.total, down: summary.down, off: summary.off }) }}
      </p>
    </template>

    <template #item="{ item: p }">
      <button
        type="button"
        class="data-card provider-card"
        :aria-label="t('audit.openFor', { name: providerName(p) })"
        @click="auditProvider = p"
      >
        <div class="card-row">
          <span class="provider-card__name">
            <StatusDot :tone="tone(p)" />
            {{ providerName(p) }}
          </span>
          <span class="badge" :class="badge(p).cls">{{ badge(p).label }}</span>
        </div>
        <div class="provider-card__fields">
          <span class="provider-card__field">{{ t('providers.catalog') }} {{ p.catalog_code || '—' }}</span>
          <span class="provider-card__field">{{ t('providers.protocol') }} {{ p.protocol }}</span>
        </div>
        <div v-if="typeof p.credential_count === 'number'" class="provider-card__fields">
          <span class="provider-card__field">
            {{ t('providers.credentials', { n: p.credential_count }) }}
          </span>
          <span v-if="typeof p.model_count === 'number'" class="provider-card__field">
            {{ t('providers.models', { n: p.model_count }) }}
          </span>
          <span class="provider-card__field provider-card__audit">{{ t('audit.open') }}</span>
        </div>
      </button>
    </template>
  </HyperList>

  <NodeAuditSheet
    v-if="auditProvider"
    :model-value="auditOpen"
    :provider-id="auditProvider.id"
    :provider-name="providerName(auditProvider)"
    @close="auditProvider = null"
  />
  </div>
</template>

<style scoped>
.providers__search {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  height: 48px;
  padding: 0 var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  color: var(--app-text-muted);
  margin-bottom: var(--app-space-2);
}

.providers__search input {
  flex: 1;
  border: none;
  background: transparent;
  color: var(--app-text);
  font-size: var(--app-font-input);
  min-width: 0;
  /* 容器 48px 但 input 自身要撑满，否则真实热区只有一行文字高 */
  height: 100%;
  align-self: stretch;
}

.providers__search input:focus {
  outline: none;
}

.providers__filters {
  display: flex;
  gap: var(--app-space-2);
  overflow-x: auto;
  padding-bottom: var(--app-space-2);
  -webkit-overflow-scrolling: touch;
}

.providers__chip {
  flex: 0 0 auto;
  min-height: 48px;
  padding: 0 var(--app-space-3);
  /* R1（17 §4-R1 / 06 §7）：新增触控控件一律 ≥48 CSS px。
     44px 是存量 .btn 的下限、不是新标准；这些 chip 是 2026-10-06 新增的，
     走的是自定义选择器而非 .btn，故此前落在 32-40px —— 违反 R1。 */
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font: inherit;
  font-size: 0.8125rem;
  cursor: pointer;
}

.providers__chip--on {
  background: var(--app-primary);
  border-color: var(--app-primary);
  color: var(--app-on-primary);
}

.providers__summary {
  margin: 0 0 var(--app-space-2);
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.provider-card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  width: 100%;
  text-align: left;
  font: inherit;
  color: inherit;
  cursor: pointer;
}

.provider-card:active {
  background: var(--app-primary-softer);
}

.provider-card__audit {
  margin-inline-start: auto;
  color: var(--app-primary);
}

.provider-card__name {
  display: inline-flex;
  align-items: center;
  gap: var(--app-space-2);
  font-weight: 600;
  font-size: 0.9375rem;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.provider-card__fields {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1) var(--app-space-3);
}

.provider-card__field {
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}
</style>
