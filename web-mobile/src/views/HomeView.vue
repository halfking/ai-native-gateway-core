<script setup lang="ts">
// HomeView — 总览：状态条（/healthz 公开端点）+ board 汇总卡 + 趋势
// sparkline + 模型分布 + 后台任务 chips。下拉刷新 = 整页重查（保旧刷新，
// 13 §3：后台刷新不整页换骨架）。
import { computed, onMounted, ref } from 'vue'
import type { ComponentPublicInstance } from 'vue'
import { useHyperPage } from '@/hyper'
import { fetchHealthz, type HealthzInfo } from '@/api/system'
import { fetchBoard, type BoardPayload } from '@/api/board'
import { t } from '@/i18n'
import PullRefreshContainer from '@/components/common/PullRefreshContainer.vue'
import AppStateView from '@/components/common/AppStateView.vue'
import StatCard from '@/components/common/StatCard.vue'
import Sparkline from '@/components/common/Sparkline.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { fmtInt, fmtNum, fmtUsd, relativeTime } from '@/utils/format'

const pullRef = ref<ComponentPublicInstance<{ rootRef: HTMLElement | null }> | null>(null)

useHyperPage({
  title: () => t('home.title'),
  scrollRoots: () => [pullRef.value?.rootRef ?? null],
})

const healthz = ref<HealthzInfo | null>(null)
const board = ref<BoardPayload | null>(null)
const initialLoading = ref(true)
const loadError = ref<string | null>(null)
const lastRefreshFailed = ref(false)
const healthzFailed = ref(false)

async function loadAll(): Promise<void> {
  const [hz, bd] = await Promise.allSettled([fetchHealthz(), fetchBoard(7)])
  healthzFailed.value = hz.status === 'rejected'
  if (hz.status === 'fulfilled') healthz.value = hz.value
  if (bd.status === 'fulfilled') {
    board.value = bd.value
  } else {
    loadError.value = String(bd.reason?.message ?? bd.reason ?? '')
  }
}

async function refresh(): Promise<void> {
  lastRefreshFailed.value = false
  try {
    await loadAll()
  } catch {
    lastRefreshFailed.value = true
  }
}

onMounted(async () => {
  await loadAll()
  initialLoading.value = false
})

const summary = computed(() => board.value?.summary)
const healthy = computed(() => healthz.value?.status === 'ok' && !healthzFailed.value)
const requestTrend = computed(() => (board.value?.trends ?? []).map((p) => p.requests))
const costTrend = computed(() => (board.value?.trends ?? []).map((p) => p.cost_usd))
const topModels = computed(() => [...(board.value?.pies?.models ?? [])].sort((a, b) => b.requests - a.requests).slice(0, 5))
const topModelsMax = computed(() => Math.max(...topModels.value.map((m) => m.requests), 1))
const discovery = computed(() => board.value?.background_tasks?.discovery)
const probeLoop = computed(() => board.value?.background_tasks?.probe_loop)
const degradedSummary = computed(() => summary.value?.degraded_summary === true)
const creditsMissing = computed(() => !!summary.value?.credits_missing_view)
</script>

<template>
  <PullRefreshContainer ref="pullRef" :on-refresh="refresh" class="home">
    <div class="page home__page">
      <p v-if="lastRefreshFailed" class="home__refresh-failed" role="status">{{ t('common.refreshFailed') }}</p>

      <AppStateView :loading="initialLoading && !board" :error="loadError" :skeleton-rows="5" @retry="refresh">
        <template v-if="board || healthz">
          <!-- 状态条 -->
          <div class="data-card home__status">
            <div class="card-row">
              <span class="home__status-label">
                <StatusDot :tone="healthy ? 'success' : 'danger'" :pulse="healthy" />
                {{ t('home.gatewayStatus') }}
              </span>
              <span class="badge" :class="healthy ? 'badge--success' : 'badge--danger'">
                {{ healthy ? t('home.healthy') : t('home.unhealthy') }}
              </span>
            </div>
            <div v-if="healthz" class="card-row home__version">
              <span class="home__meta">{{ t('home.version') }} {{ healthz.version || '—' }}</span>
              <span class="home__meta num">#{{ healthz.build_seq ?? '—' }}</span>
            </div>
            <div v-else-if="healthzFailed" class="home__meta">{{ t('common.error') }}</div>
          </div>

          <!-- 后台任务 chips -->
          <div v-if="discovery || probeLoop" class="home__chips">
            <span class="badge badge--info" v-if="discovery">
              {{ t('home.discovery') }}·{{ discovery.running ? t('home.running') : t('home.stopped') }}
            </span>
            <span class="badge badge--muted" v-if="probeLoop">
              {{ t('home.probeLoop') }}·{{ probeLoop.checks_last_10m ?? 0 }}/10m
            </span>
            <span v-if="board?.background_tasks?.degraded" class="badge badge--warning">{{ t('home.degraded') }}</span>
          </div>

          <!-- 汇总卡 -->
          <h2 class="page__section-title">{{ t('home.period') }}</h2>
          <div class="home__grid">
            <StatCard :label="t('home.totalRequests')" :value="fmtInt(summary?.total_requests)" :hint="degradedSummary ? t('home.summaryMissing') : undefined" />
            <StatCard :label="t('home.totalTokens')" :value="fmtInt(summary?.total_tokens)" :hint="degradedSummary ? t('home.summaryMissing') : undefined" />
            <StatCard :label="t('home.totalCost')" :value="fmtUsd(summary?.total_cost_usd)" />
            <StatCard :label="t('home.creditsCharged')" :value="fmtInt(summary?.total_credits_charged)" :hint="creditsMissing ? t('home.creditsMissing') : undefined" />
            <StatCard :label="t('home.successRate')" :value="summary?.success_rate != null ? fmtNum(summary.success_rate, 2) + '%' : '—'" />
            <StatCard :label="t('home.avgLatency')" :value="summary?.avg_latency_ms != null ? fmtInt(Math.round(summary.avg_latency_ms)) + 'ms' : '—'" />
            <StatCard :label="t('home.activeKeys')" :value="fmtInt(summary?.active_api_keys)" />
            <StatCard :label="t('home.activeModels')" :value="fmtInt(summary?.active_models)" />
          </div>

          <!-- 趋势 -->
          <h2 class="page__section-title">{{ t('home.trend') }}</h2>
          <div class="data-card home__trend">
            <div class="home__trend-row">
              <span class="home__trend-label">{{ t('home.requests') }}</span>
              <Sparkline :points="requestTrend" :width="220" :height="40" />
            </div>
            <div class="home__trend-row">
              <span class="home__trend-label">{{ t('home.cost') }}</span>
              <Sparkline :points="costTrend" :width="220" :height="40" tone="var(--app-success)" />
            </div>
          </div>

          <!-- 模型分布 -->
          <template v-if="topModels.length > 0">
            <h2 class="page__section-title">{{ t('home.topModels') }}</h2>
            <div class="data-card">
              <div v-for="m in topModels" :key="m.key" class="home__bar-row">
                <span class="home__bar-name">{{ m.key }}</span>
                <div class="home__bar-track">
                  <div class="home__bar-fill" :style="{ width: `${Math.max(3, (m.requests / topModelsMax) * 100)}%` }" />
                </div>
                <span class="home__bar-value num">{{ fmtInt(m.requests) }}</span>
              </div>
            </div>
          </template>

          <div v-if="discovery?.heartbeat_at" class="home__checked">
            {{ t('nodes.lastChecked') }} {{ relativeTime(discovery.heartbeat_at) }}
          </div>
        </template>
      </AppStateView>
    </div>
  </PullRefreshContainer>
</template>

<style scoped>
.home {
  display: flex;
  flex-direction: column;
}

.home__page {
  flex: 1;
}

.home__refresh-failed {
  font-size: 0.8125rem;
  color: var(--app-warning);
  margin-bottom: var(--app-space-2);
}

.home__status-label {
  display: inline-flex;
  align-items: center;
  gap: var(--app-space-2);
  font-weight: 600;
}

.home__version {
  margin-top: var(--app-space-2);
}

.home__meta {
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.home__chips {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  margin: var(--app-space-3) 0;
}

.home__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--app-space-2);
}

@media (min-width: 600px) {
  .home__grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }
}

.home__trend-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-3);
  padding: var(--app-space-2) 0;
}

.home__trend-label {
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
  flex-shrink: 0;
}

.home__bar-row {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  padding: 6px 0;
}

.home__bar-name {
  width: 38%;
  font-size: 0.75rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.home__bar-track {
  flex: 1;
  height: 8px;
  background: var(--app-surface-muted);
  border-radius: var(--app-radius-pill);
  overflow: hidden;
}

.home__bar-fill {
  height: 100%;
  background: var(--kx-primary);
  border-radius: var(--app-radius-pill);
}

.home__bar-value {
  min-width: 52px;
  text-align: right;
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.home__checked {
  margin-top: var(--app-space-4);
  font-size: 0.75rem;
  color: var(--app-text-muted);
  text-align: center;
}
</style>
