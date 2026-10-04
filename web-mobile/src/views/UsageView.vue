<script setup lang="ts">
// UsageView — 用量：Tab 停靠（07 §5 简化：点按切换，不做横滑）+ 汇总卡 +
// 按模型宽表（专注模式）。Tab 独立数据、共享一次拉取。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { fetchUsageByModel, fetchUsageSummary, unwrapModelUsage, type ModelUsage, type UsageSummary } from '@/api/usage'
import { t } from '@/i18n'
import AppStateView from '@/components/common/AppStateView.vue'
import StatCard from '@/components/common/StatCard.vue'
import FocusLayer from '@/components/common/FocusLayer.vue'
import AppIcon from '@/components/common/AppIcon.vue'
import { fmtInt, fmtNum, fmtUsd } from '@/utils/format'

useHyperPage({ title: () => t('usage.title') })

const tab = ref<'summary' | 'byModel'>('summary')
const summary = ref<UsageSummary | null>(null)
const modelItems = ref<ModelUsage[]>([])
const degraded = ref(false)
const loading = ref(true)
const error = ref<string | null>(null)
const focusActive = ref(false)

let aborter: AbortController | null = null

async function load(): Promise<void> {
  aborter?.abort()
  aborter = new AbortController()
  const signal = aborter.signal
  error.value = null
  try {
    const [s, m] = await Promise.all([fetchUsageSummary(7, { signal }), fetchUsageByModel(7, { signal })])
    summary.value = s
    const unwrapped = unwrapModelUsage(m)
    modelItems.value = unwrapped.items
    degraded.value = unwrapped.degraded
  } catch (err) {
    if ((err as { name?: string })?.name === 'AbortError') return
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    if (!signal.aborted) loading.value = false
  }
}

onMounted(() => {
  void load()
})
onBeforeUnmount(() => {
  aborter?.abort()
})

const sortedModels = computed(() => [...modelItems.value].sort((a, b) => b.total_requests - a.total_requests))
</script>

<template>
  <div class="page usage">
    <!-- Tab 停靠：.page 即滚动宿主，sticky top:0 = 顶栏下沿（07 §4 effectiveTop） -->
    <div class="usage__tabs" role="tablist">
      <button
        type="button"
        role="tab"
        class="usage__tab"
        :class="{ 'usage__tab--active': tab === 'summary' }"
        :aria-selected="tab === 'summary'"
        @click="tab = 'summary'"
      >
        {{ t('usage.summary') }}
      </button>
      <button
        type="button"
        role="tab"
        class="usage__tab"
        :class="{ 'usage__tab--active': tab === 'byModel' }"
        :aria-selected="tab === 'byModel'"
        @click="tab = 'byModel'"
      >
        {{ t('usage.byModel') }}
      </button>
    </div>

    <AppStateView :loading="loading" :error="error" @retry="load">
      <template v-if="tab === 'summary' && summary">
        <div class="usage__grid">
          <StatCard :label="t('home.totalRequests')" :value="fmtInt(summary.total_requests)" :hint="summary.degraded ? t('home.summaryMissing') : undefined" />
          <StatCard :label="t('home.totalTokens')" :value="fmtInt(summary.total_prompt_tokens + summary.total_completion_tokens)" />
          <StatCard :label="t('home.totalCost')" :value="fmtUsd(summary.total_cost_usd)" />
          <StatCard :label="t('home.creditsCharged')" :value="fmtInt(summary.total_credits_charged)" />
          <StatCard :label="t('home.successRate')" :value="summary.success_rate != null ? fmtNum(summary.success_rate, 2) + '%' : '—'" />
          <StatCard :label="t('home.avgLatency')" :value="summary.avg_latency_ms != null ? fmtInt(Math.round(summary.avg_latency_ms)) + 'ms' : '—'" />
        </div>
        <p class="usage__period">{{ t('usage.period') }}</p>
      </template>

      <template v-else-if="tab === 'byModel'">
        <div class="usage__focus-head">
          <span v-if="degraded" class="badge badge--warning">{{ t('home.degraded') }}</span>
          <button type="button" class="btn btn--sm" @click="focusActive = true">
            <AppIcon name="expand" :size="16" />
            {{ t('common.focusView') }}
          </button>
        </div>

        <FocusLayer v-model:active="focusActive" :title="t('usage.byModel')">
          <div class="table-scroll">
            <table class="table">
              <thead>
                <tr>
                  <th>{{ t('usage.model') }}</th>
                  <th class="num">{{ t('usage.requests') }}</th>
                  <th class="num">{{ t('usage.tokens') }}</th>
                  <th class="num">{{ t('usage.cost') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="m in sortedModels" :key="m.model">
                  <td>{{ m.model }}</td>
                  <td class="num">{{ fmtInt(m.total_requests) }}</td>
                  <td class="num">{{ fmtInt(m.total_tokens) }}</td>
                  <td class="num">{{ fmtUsd(m.total_cost_usd) }}</td>
                </tr>
                <tr v-if="sortedModels.length === 0">
                  <td colspan="4">{{ t('common.empty') }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </FocusLayer>
      </template>
    </AppStateView>
  </div>
</template>

<style scoped>
.usage__tabs {
  position: sticky;
  top: 0;
  z-index: 2;
  display: flex;
  gap: var(--app-space-1);
  background: var(--app-bg);
  padding: var(--app-space-2) 0;
  margin-bottom: var(--app-space-2);
}

.usage__tab {
  flex: 1;
  min-height: 44px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 0.875rem;
  font-weight: 500;
  cursor: pointer;
}

.usage__tab--active {
  border-color: var(--kx-primary);
  color: var(--kx-primary);
  background: var(--app-primary-soft);
}

.usage__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--app-space-2);
}

@media (min-width: 600px) {
  .usage__grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

.usage__period {
  margin-top: var(--app-space-4);
  font-size: 0.75rem;
  color: var(--app-text-muted);
  text-align: center;
}

.usage__focus-head {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-2);
}
</style>
