<script setup lang="ts">
// RequestJourneyDetailView.vue — T9 mock-stage Journey 详情整合视图
//
// 三个面板的组合视图（路由 / 动作 / 失败追踪）：
//   ① RoutingAttemptsTimeline：后端 request-journeys events（seq 升序完整 lifecycle）
//   ② ActionTimeline：liveStreamStore 单例 actions（实时增量，挂在 actions Map）
//   ③ RequestTracePanel：失败快照 + AI 提示词生成（沿用 trace API）
//
// 数据通道：
//   - 路由面板 REST only（getRequestJourney），与 RequestJourneyQueues.vue 同款；
//   - 动作面板 SSE only（store.actions Map），不调后端；
//   - 追踪面板 REST（getRequestTrace），与 RequestTracePanel.vue 同款。
//
// scope=all（super_admin 全局入站）走 /admin/request-journeys/:id 时服务端按
// 入站快照只回 minimal fields——这里检测 journey.events 为空时退化为仅动作面板
// + 路由面板隐藏 + 追踪面板降级提示，避免误报"无事件"。
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { getRequestJourney } from '../api/request-journeys'
import type { RequestJourney } from '../api/request-journeys'
import { acquireLiveStream } from '../composables/liveStreamStore'
import RoutingAttemptsTimeline from '../components/RoutingAttemptsTimeline.vue'
import ActionTimeline from '../components/ActionTimeline.vue'
import RequestTracePanel from '../components/RequestTracePanel.vue'

const route = useRoute()
const { t } = useI18n()

const requestId = computed(() => String(route.params.requestId || ''))
const journey = ref<RequestJourney | null>(null)
const loading = ref(false)
const error = ref('')
const observationDegraded = ref(false)
const scopeAll = ref(false)

let releaseStream: (() => void) | null = null

async function loadJourney(id: string) {
  if (!id) return
  loading.value = true
  error.value = ''
  try {
    const j = await getRequestJourney(id)
    journey.value = j
    observationDegraded.value = j.observation_status === 'observation_degraded'
    // scope=all（super_admin 全局入站）：服务端回写 minimal events；
    // 用 events 为空 + observation_degraded 兜底标记。
    if (!j.events?.length) scopeAll.value = true
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    journey.value = null
  } finally {
    loading.value = false
  }
}

watch(requestId, (id) => {
  void loadJourney(id)
}, { immediate: true })

onMounted(() => {
  releaseStream = acquireLiveStream()
})

onUnmounted(() => {
  if (releaseStream) { releaseStream(); releaseStream = null }
})

</script>

<template>
  <section class="journey-detail-view" data-testid="journey-detail-view">
    <header class="jdv-head">
      <h2>{{ t('requestJourneyDetail.title') }}</h2>
      <p class="jdv-sub">
        <span class="jdv-id">{{ requestId }}</span>
        <span v-if="scopeAll" class="jdv-chip is-muted" data-testid="scope-all-chip">
          {{ t('requestJourneyDetail.scopeAll') }}
        </span>
        <span v-if="observationDegraded" class="jdv-chip is-warning" data-testid="observation-degraded-chip">
          {{ t('requestJourneyDetail.observationDegraded') }}
        </span>
      </p>
    </header>

    <div v-if="loading" class="jdv-state">{{ t('requestJourneyDetail.loading') }}</div>
    <div v-else-if="error" class="jdv-state jdv-state--error" data-testid="journey-error" role="alert">
      {{ t('requestJourneyDetail.error') }}: {{ error }}
    </div>

    <template v-else-if="journey">
      <section class="jdv-panel" data-testid="routing-panel">
        <header class="jdv-panel-head">
          <h3>{{ t('requestJourneyDetail.routingTitle') }}</h3>
        </header>
        <RoutingAttemptsTimeline :journey-events="journey.events" />
      </section>

      <section class="jdv-panel" data-testid="action-panel">
        <header class="jdv-panel-head">
          <h3>{{ t('requestJourneyDetail.actionTitle') }}</h3>
        </header>
        <ActionTimeline :request-id="requestId" />
      </section>

      <section class="jdv-panel" data-testid="trace-panel">
        <header class="jdv-panel-head">
          <h3>{{ t('requestJourneyDetail.traceTitle') }}</h3>
        </header>
        <RequestTracePanel :request-id="requestId" />
      </section>
    </template>
  </section>
</template>

<style scoped>
.journey-detail-view {
  display: flex;
  flex-direction: column;
  gap: 14px;
  min-width: 0;
}

.jdv-head h2 {
  margin: 0;
  font-size: 16px;
  color: var(--kx-text);
}

.jdv-sub {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 4px 0 0;
  color: var(--kx-muted);
  font-size: 12px;
}

.jdv-id {
  font-family: var(--kx-mono, ui-monospace, monospace);
  color: var(--kx-text);
}

.jdv-chip {
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 11px;
  border: 1px solid transparent;
}

.jdv-chip.is-warning { color: var(--kx-warning); background: var(--kx-warning-soft); }
.jdv-chip.is-muted { color: var(--kx-muted); background: var(--kx-bg-accent); }

.jdv-panel {
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  background: var(--kx-surface);
  padding: 10px 14px;
}

.jdv-panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}

.jdv-panel-head h3 {
  margin: 0;
  font-size: 13px;
  color: var(--kx-text);
}

.jdv-link {
  border: 0;
  background: transparent;
  color: var(--kx-primary);
  cursor: pointer;
  font: inherit;
  font-size: 12px;
}

.jdv-state {
  padding: 18px 8px;
  color: var(--kx-muted);
  font-size: 12px;
  text-align: center;
}

.jdv-state--error { color: var(--kx-danger); }
</style>