<script setup lang="ts">
// NodesView — 节点（凭据）健康：连续加载卡片列表 + 搜索（250ms debounce，
// 13 §5）+ 详情 Sheet（每模型探测宽表走专注模式，07 §8）。
import { computed, onBeforeUnmount, ref } from 'vue'
import { ContinuousListController, useHyperPage } from '@/hyper'
import { fetchMonitorSummary, type CredentialMonitorSummary } from '@/api/nodes'
import { t } from '@/i18n'
import HyperList from '@/components/common/HyperList.vue'
import AppSheet from '@/components/common/AppSheet.vue'
import FocusLayer from '@/components/common/FocusLayer.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import AppIcon from '@/components/common/AppIcon.vue'
import { relativeTime } from '@/utils/format'

useHyperPage({ title: () => t('nodes.title') })

// 全量缓存 + 前端过滤：monitor-summary 单端点返回全网凭据（量级 ~百），
// 分页在客户端切片；queryRevision 机制照常隔离 requery 前后响应。
let cache: CredentialMonitorSummary[] = []
const query = ref('')

const controller = new ContinuousListController<CredentialMonitorSummary>({
  fetchPage: async (page) => {
    if (page > 1) return { items: [], total: filtered().length }
    if (cache.length === 0) {
      cache = await fetchMonitorSummary()
    }
    const items = filtered()
    return { items, total: items.length }
  },
  stableKey: (c) => `cred-${c.id}`,
  scopeKey: 'nodes',
})

function filtered(): CredentialMonitorSummary[] {
  const q = query.value.trim().toLowerCase()
  if (!q) return cache
  return cache.filter(
    (c) => c.provider_name.toLowerCase().includes(q) || c.label.toLowerCase().includes(q),
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

// 详情 Sheet + 专注宽表
const detail = ref<CredentialMonitorSummary | null>(null)
const focusActive = ref(false)

const detailOpenProxy = computed({
  get: () => detail.value != null,
  set: (v: boolean) => {
    if (!v) {
      detail.value = null
      focusActive.value = false
    }
  },
})

function healthTone(c: CredentialMonitorSummary): 'success' | 'warning' | 'danger' | 'muted' {
  if (c.manual_disabled) return 'muted'
  if (c.availability_state === 'down' || c.health_status === 'down') return 'danger'
  if (c.availability_state === 'degraded' || c.consecutive_failures > 0 || c.broken_model_count > 0) return 'warning'
  return 'success'
}

function stateBadge(c: CredentialMonitorSummary): { cls: string; label: string } {
  if (c.manual_disabled) return { cls: 'badge--muted', label: t('nodes.down') }
  const s = c.availability_state || c.health_status
  if (s === 'down' || s === 'failed') return { cls: 'badge--danger', label: t('nodes.down') }
  if (s === 'degraded' || s === 'recovering') return { cls: 'badge--warning', label: t('nodes.degraded') }
  if (s === 'up' || s === 'healthy' || s === 'ok') return { cls: 'badge--success', label: t('nodes.healthy') }
  return { cls: 'badge--muted', label: s || t('common.unknown') }
}

function probeBadge(state: string): { cls: string; label: string } {
  switch (state) {
    case 'healthy_confirmed':
      return { cls: 'badge--success', label: t('nodes.healthy') }
    case 'broken_confirmed':
      return { cls: 'badge--danger', label: t('nodes.broken') }
    case 'recovering':
      return { cls: 'badge--warning', label: t('nodes.recovering') }
    default:
      return { cls: 'badge--muted', label: t('nodes.unknownState') }
  }
}

const detailTitle = computed(() => detail.value ? `${detail.value.provider_name} · ${detail.value.label}` : '')
</script>

<template>
  <div class="view-root">
  <HyperList
    :controller="controller"
    :item-key="(c: CredentialMonitorSummary) => `cred-${c.id}`"
    :on-refresh="async () => { cache = await fetchMonitorSummary() }"
    :empty-hint="t('common.empty')"
  >
    <template #header>
      <div class="nodes__search">
        <AppIcon name="search" :size="18" />
        <input
          type="search"
          :placeholder="t('nodes.searchPlaceholder')"
          :aria-label="t('common.search')"
          @input="onSearchInput"
        />
      </div>
    </template>

    <template #item="{ item: c }">
      <button type="button" class="data-card node-card" @click="detail = c">
        <div class="card-row">
          <span class="node-card__name">
            <StatusDot :tone="healthTone(c)" />
            {{ c.provider_name }} · {{ c.label }}
          </span>
          <span class="badge" :class="stateBadge(c).cls">{{ stateBadge(c).label }}</span>
        </div>
        <div class="node-card__fields">
          <span class="node-card__field">
            {{ t('nodes.modelsAvailable', { available: c.model_available, total: c.model_total }) }}
          </span>
          <span class="node-card__field">{{ t('nodes.concurrency') }} {{ c.effective_concurrency }}</span>
          <span class="node-card__field">{{ t('nodes.lastChecked') }} {{ relativeTime(c.health_checked_at) }}</span>
        </div>
        <div v-if="c.consecutive_failures > 0" class="node-card__warn">
          {{ t('nodes.consecutiveFailures', { n: c.consecutive_failures }) }}
        </div>
      </button>
    </template>
  </HyperList>

  <!-- 节点详情：字段 + 每模型宽表（专注入口） -->
  <AppSheet v-model="detailOpenProxy" presentation="sheet" :title="detailTitle">
    <template v-if="detail">
      <div class="data-card">
        <div class="card-field">
          <span>{{ t('nodes.effectiveState') }}</span>
          <span class="card-field__value">{{ detail.effective_state || detail.status }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('nodes.availability') }}</span>
          <span class="card-field__value">{{ detail.availability_state }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('nodes.quota') }}</span>
          <span class="card-field__value">{{ detail.quota_state }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('nodes.concurrency') }}</span>
          <span class="card-field__value num">{{ detail.effective_concurrency }} / {{ detail.concurrency_limit ?? '—' }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('keys.requests') }}</span>
          <span class="card-field__value num">{{ detail.total_requests }}</span>
        </div>
        <div v-if="detail.effective_reason || detail.state_reason_code" class="card-field">
          <span>{{ t('nodes.reason') }}</span>
          <span class="card-field__value">{{ detail.effective_reason || detail.state_reason_code }}</span>
        </div>
      </div>

      <div class="nodes__per-model-head">
        <h3 class="page__section-title" style="margin-inline: 0">{{ t('nodes.perModel') }}</h3>
        <button type="button" class="btn btn--sm" @click="focusActive = true">
          <AppIcon name="expand" :size="16" />
          {{ t('common.focusView') }}
        </button>
      </div>

      <!-- 专注层：单实例 Teleport，inactive 原位渲染 -->
      <FocusLayer v-model:active="focusActive" :title="detailTitle">
        <div class="table-scroll">
          <table class="table">
            <thead>
              <tr>
                <th>{{ t('usage.model') }}</th>
                <th>{{ t('nodes.probeState') }}</th>
                <th class="num">{{ t('nodes.latency') }}</th>
                <th class="num">{{ t('nodes.successRate') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="m in detail.models ?? []" :key="m.raw_model_name">
                <td>{{ m.canonical_name || m.raw_model_name }}</td>
                <td>
                  <span class="badge" :class="probeBadge(m.probe_state).cls">{{ probeBadge(m.probe_state).label }}</span>
                </td>
                <td class="num">{{ m.p95_latency_ms != null ? Math.round(m.p95_latency_ms) + 'ms' : '—' }}</td>
                <td class="num">
                  {{ m.recent_success_rate != null ? (m.recent_success_rate * 100).toFixed(1) + '%' : '—' }}
                </td>
              </tr>
              <tr v-if="(detail.models ?? []).length === 0">
                <td colspan="4">{{ t('common.empty') }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </FocusLayer>
    </template>
  </AppSheet>
  </div>
</template>

<style scoped>
.nodes__search {
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

.nodes__search input {
  flex: 1;
  border: none;
  background: transparent;
  color: var(--app-text);
  font-size: var(--app-font-input);
  min-width: 0;
  /* 2026-10-04：与 ModelsView 同一处缺陷。容器 48px 但 input 自身只有 20px 高，
     而容器是 div 不是 label —— 点容器的 padding 不会聚焦，真实热区就是那 20px。 */
  height: 100%;
  align-self: stretch;
}

.nodes__search input:focus {
  outline: none;
}

.node-card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  width: 100%;
  text-align: left;
  border: 1px solid var(--app-border-subtle);
  cursor: pointer;
  font: inherit;
  color: inherit;
}

.node-card:active {
  background: var(--app-primary-softer);
}

.node-card__name {
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

.node-card__fields {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1) var(--app-space-3);
}

.node-card__field {
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.node-card__warn {
  font-size: 0.75rem;
  color: var(--app-warning);
}

.nodes__per-model-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin: var(--app-space-4) 0 var(--app-space-2);
}
</style>
