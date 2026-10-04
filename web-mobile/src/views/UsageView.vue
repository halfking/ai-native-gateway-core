<script setup lang="ts">
// UsageView.vue — 用量：汇总卡 + 模型分布；Tab 停靠（CSS sticky 简化实现，
// 07 §4/R7）；模型分布 → 专注宽表全页查看。
import { computed, onMounted, ref } from 'vue'
import {
  getUsageSummary,
  getUsageByModel,
  normalizeUsageByModel,
  type UsageSummary,
  type ModelUsage,
} from '../api/usage'
import { useHyperPage } from '../composables/useHyperPage'
import { focusWorkspace, type FocusHandle } from '../runtime/focusWorkspace'
import { useTitleStore } from '../stores/titleStore'
import SkeletonList from '../components/ui/SkeletonList.vue'
import ErrorRetry from '../components/ui/ErrorRetry.vue'
import Icon from '../components/Icon.vue'
import FocusWorkspace from '../components/focus/FocusWorkspace.vue'
import { t } from '../i18n'

const titleStore = useTitleStore()
useHyperPage({
  routeTitle: t('usage.title'),
  activeTab: () => tab.value,
})

const tab = ref<'summary' | 'byModel'>('summary')
const summary = ref<UsageSummary | null>(null)
const byModel = ref<ModelUsage[]>([])
const loading = ref(true)
const loadError = ref('')
const focusHandle = ref<FocusHandle | null>(null)

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const [s, m] = await Promise.all([getUsageSummary(7), getUsageByModel(7)])
    summary.value = s
    byModel.value = normalizeUsageByModel(m)
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

const maxModelReq = computed(() =>
  Math.max(1, ...byModel.value.map((m) => m.total_requests)),
)

function fmtNum(n: number | undefined): string {
  if (n === undefined || n === null) return '—'
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

function openFocus(e: Event): void {
  focusHandle.value = focusWorkspace().open({
    title: t('usage.modelTable'),
    triggerEl: (e.currentTarget as HTMLElement) ?? null,
  })
}

onMounted(() => {
  titleStore.setRegistered(t('usage.title'))
  void load()
})
</script>

<template>
  <section class="m-page usage-page">
    <!-- Tab 停靠（sticky）：滚动中保持可见 -->
    <div class="tabs" role="tablist">
      <button
        type="button"
        role="tab"
        class="tabs__item"
        :class="{ 'is-active': tab === 'summary' }"
        @click="tab = 'summary'"
      >
        {{ t('usage.tab.summary') }}
      </button>
      <button
        type="button"
        role="tab"
        class="tabs__item"
        :class="{ 'is-active': tab === 'byModel' }"
        @click="tab = 'byModel'"
      >
        {{ t('usage.tab.byModel') }}
      </button>
    </div>

    <SkeletonList v-if="loading" :lines="5" />
    <ErrorRetry v-else-if="loadError" :message="loadError" @retry="load" />

    <template v-else-if="tab === 'summary' && summary">
      <p v-if="summary.degraded" class="degraded-note">{{ t('usage.degraded') }}</p>
      <div class="m-metrics">
        <div class="m-metric">
          <p class="m-metric__k">{{ t('usage.totalRequests') }}</p>
          <p class="m-metric__v">{{ fmtNum(summary.total_requests) }}</p>
        </div>
        <div class="m-metric">
          <p class="m-metric__k">{{ t('usage.tokens') }}</p>
          <p class="m-metric__v">{{ fmtNum(summary.total_prompt_tokens + summary.total_completion_tokens) }}</p>
        </div>
        <div class="m-metric">
          <p class="m-metric__k">{{ t('usage.cost') }}</p>
          <p class="m-metric__v">${{ (summary.total_cost_usd ?? 0).toFixed(2) }}</p>
        </div>
        <div class="m-metric">
          <p class="m-metric__k">{{ t('usage.avgLatency') }}</p>
          <p class="m-metric__v">{{ Math.round(summary.avg_latency_ms) }}ms</p>
        </div>
      </div>
    </template>

    <template v-else-if="tab === 'byModel'">
      <button type="button" class="m-btn m-btn--ghost focus-entry" @click="openFocus">
        <Icon name="focus" />
        {{ t('usage.viewAll') }}
      </button>
      <div
        v-for="m in byModel.slice(0, 20)"
        :key="m.model"
        class="m-card"
        :data-row-id="`usage-${m.model}`"
      >
        <div class="m-card__row">
          <span class="model-name">{{ m.model }}</span>
          <span class="num model-req">{{ fmtNum(m.total_requests) }}</span>
        </div>
        <div class="bar" aria-hidden="true">
          <div class="bar__fill" :style="{ width: `${(m.total_requests / maxModelReq) * 100}%` }"></div>
        </div>
        <div class="m-kv">
          <span class="m-kv__k">{{ m.provider_code }}</span>
          <span class="m-kv__v num">${{ m.total_cost_usd.toFixed(2) }}</span>
        </div>
      </div>
    </template>

    <FocusWorkspace v-if="focusHandle" :handle="focusHandle" @exit="focusHandle = null">
      <div class="m-table-wrap">
        <table class="m-table">
          <thead>
            <tr>
              <th>Model</th>
              <th>Provider</th>
              <th class="num">Requests</th>
              <th class="num">Tokens</th>
              <th class="num">Cost</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in byModel" :key="`focus-${m.model}`">
              <td>{{ m.model }}</td>
              <td>{{ m.provider_code }}</td>
              <td class="num">{{ m.total_requests }}</td>
              <td class="num">{{ fmtNum(m.total_tokens) }}</td>
              <td class="num">${{ m.total_cost_usd.toFixed(2) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </FocusWorkspace>
  </section>
</template>

<style scoped>
.usage-page {
  position: relative;
}

.tabs {
  position: sticky;
  top: 0;
  z-index: 2;
  display: flex;
  gap: var(--app-space-2);
  background: var(--app-bg);
  padding: var(--app-space-2) 0;
  margin-bottom: var(--app-space-2);
}

.tabs__item {
  flex: 1;
  min-height: 48px;
  border-radius: var(--app-radius);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-weight: 600;
}

.tabs__item.is-active {
  border-color: var(--app-primary);
  color: var(--app-primary);
  background: var(--app-info-soft);
}

.degraded-note {
  font-size: 0.75rem;
  color: var(--app-warning);
  margin-bottom: var(--app-space-2);
}

.focus-entry {
  width: 100%;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-3);
}

.model-name {
  font-size: 0.875rem;
  font-weight: 550;
  word-break: break-all;
}

.model-req {
  font-weight: 650;
}

.bar {
  height: 4px;
  border-radius: 2px;
  background: var(--app-surface-muted);
  margin: var(--app-space-2) 0;
}

.bar__fill {
  height: 100%;
  border-radius: 2px;
  background: var(--app-primary);
}
</style>
