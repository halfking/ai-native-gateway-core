<script setup lang="ts">
// OverviewView.vue — 总览：状态条 + 汇总卡 + 趋势 sparkline + 后台任务 chips，
// 下拉刷新（PtrState 状态机驱动，07 §2）。
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { fetchBoard, type BoardPayload } from '../api/board'
import { fetchHealth, fetchSystemVersion, type SystemVersion } from '../api/system'
import { PullToRefresh } from '../runtime/pullToRefresh'
import { createQueryEpoch, currentSessionEpoch, isStale } from '../runtime/epochs'
import { useHyperPage } from '../composables/useHyperPage'
import { useTitleStore } from '../stores/titleStore'
import SkeletonList from '../components/ui/SkeletonList.vue'
import ErrorRetry from '../components/ui/ErrorRetry.vue'
import Sparkline from '../components/ui/Sparkline.vue'
import { t } from '../i18n'

const titleStore = useTitleStore()
useHyperPage({ routeTitle: t('overview.metrics') })

const board = ref<BoardPayload | null>(null)
const version = ref<SystemVersion | null>(null)
const healthOk = ref<boolean | null>(null)
const loading = ref(true)
const loadError = ref('')
const staleNotice = ref(false)

const qe = createQueryEpoch()

const ptr = reactive({ pull: 0, armed: false, refreshing: false })
const reduced = () => window.matchMedia('(prefers-reduced-motion: reduce)').matches

const ptrEngine = new PullToRefresh({
  onRefresh: async () => {
    staleNotice.value = false
    const claim = { session: currentSessionEpoch(), query: qe.next() }
    try {
      const [b, v, h] = await Promise.all([
        fetchBoard(7),
        fetchSystemVersion().catch(() => null),
        fetchHealth().then(() => true).catch(() => false),
      ])
      if (isStale(claim.session, claim.query, { session: currentSessionEpoch(), query: qe.current })) {
        staleNotice.value = true
        return
      }
      board.value = b
      version.value = v
      healthOk.value = h
    } catch {
      // 刷新失败保留旧数字 +「内容为上次更新」提示（17 §8-3）。
      staleNotice.value = true
    }
  },
  reducedMotion: reduced,
})

let startY = 0

function onTouchStart(e: TouchEvent): void {
  const host = e.currentTarget as HTMLElement
  startY = e.touches[0]?.clientY ?? 0
  ptrEngine.touchStart(host.scrollTop)
}

function onTouchMove(e: TouchEvent): void {
  if (e.touches.length !== 1) return
  const host = e.currentTarget as HTMLElement
  const cur = e.touches[0]?.clientY ?? startY
  ptrEngine.touchMove(cur - startY, host.scrollTop)
  syncPtr()
}

function onTouchEnd(): void {
  void ptrEngine.touchEnd().then(syncPtr)
}

function syncPtr(): void {
  const cur = ptrEngine.current()
  ptr.pull = cur.pull
  ptr.armed = cur.armed
  ptr.refreshing = cur.state === 'refreshing'
}

async function initialLoad(): Promise<void> {
  loading.value = true
  loadError.value = ''
  const claim = { session: currentSessionEpoch(), query: qe.next() }
  try {
    const [b, v, h] = await Promise.all([
      fetchBoard(7),
      fetchSystemVersion().catch(() => null),
      fetchHealth().then(() => true).catch(() => false),
    ])
    if (isStale(claim.session, claim.query, { session: currentSessionEpoch(), query: qe.current })) return
    board.value = b
    version.value = v
    healthOk.value = h
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

const summary = computed(() => board.value?.summary)
const trendPoints = computed(() => (board.value?.trends ?? []).map((p) => p.requests))

const versionText = computed(() => {
  const v = version.value
  if (!v) return '—'
  return String(v.version ?? v.build ?? v.commit ?? '—')
})

function fmtNum(n: number | undefined): string {
  if (n === undefined || n === null) return '—'
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

function fmtPct(n: number | undefined): string {
  return n === undefined || n === null ? '—' : `${(n * 100).toFixed(1)}%`
}

onMounted(() => {
  titleStore.setRegistered(t('overview.metrics'))
  void initialLoad()
})

onBeforeUnmount(() => {
  ptrEngine.cancel()
})
</script>

<template>
  <section
    class="m-page ptr-host"
    @touchstart.passive="onTouchStart"
    @touchmove.passive="onTouchMove"
    @touchend.passive="onTouchEnd"
  >
    <div class="ptr-indicator" :style="{ height: `${ptr.pull}px` }" :data-armed="ptr.armed">
      <span v-if="ptr.pull > 8">{{ ptr.refreshing ? t('common.loading') : ptr.armed ? t('common.confirm') : '' }}</span>
    </div>

    <SkeletonList v-if="loading" :lines="5" />

    <ErrorRetry v-else-if="loadError" :message="loadError" @retry="initialLoad" />

    <template v-else>
      <p v-if="staleNotice" class="stale-note">{{ t('overview.refreshAt') }}</p>

      <!-- 状态条：健康 + 版本，一行扫读 -->
      <div class="status-bar">
        <span class="m-chip" :class="healthOk ? 'm-chip--success' : 'm-chip--danger'">
          <span class="dot"></span>
          {{ healthOk === null ? '—' : healthOk ? t('overview.statusOk') : t('overview.statusDown') }}
        </span>
        <span class="ver">{{ t('overview.version') }} {{ versionText }}</span>
      </div>

      <p class="m-group-label">{{ t('overview.metrics') }}</p>
      <div class="m-metrics">
        <div class="m-metric">
          <p class="m-metric__k">{{ t('overview.requests') }}</p>
          <p class="m-metric__v">{{ fmtNum(summary?.total_requests) }}</p>
        </div>
        <div class="m-metric">
          <p class="m-metric__k">{{ t('overview.tokens') }}</p>
          <p class="m-metric__v">{{ fmtNum(summary?.total_tokens) }}</p>
        </div>
        <div class="m-metric">
          <p class="m-metric__k">{{ t('overview.successRate') }}</p>
          <p class="m-metric__v">{{ fmtPct(summary?.success_rate) }}</p>
        </div>
        <div class="m-metric">
          <p class="m-metric__k">{{ t('overview.cost') }}</p>
          <p class="m-metric__v">${{ (summary?.total_cost_usd ?? 0).toFixed(2) }}</p>
        </div>
        <div class="m-metric">
          <p class="m-metric__k">{{ t('overview.avgLatency') }}</p>
          <p class="m-metric__v">{{ summary?.avg_latency_ms ? `${Math.round(summary.avg_latency_ms)}ms` : '—' }}</p>
        </div>
        <div class="m-metric">
          <p class="m-metric__k">{{ t('overview.activeKeys') }}</p>
          <p class="m-metric__v">{{ fmtNum(summary?.active_api_keys) }}</p>
        </div>
      </div>

      <p class="m-group-label">{{ t('overview.trend') }}</p>
      <div class="m-card">
        <Sparkline :points="trendPoints" :width="320" :height="56" />
      </div>

      <template v-if="board?.background_tasks || board?.selfcheck">
        <p class="m-group-label">{{ t('overview.tasks') }}</p>
        <div class="m-card chips">
          <span
            v-if="board?.background_tasks?.discovery"
            class="m-chip"
            :class="board.background_tasks.discovery.running ? 'm-chip--success' : 'm-chip--warning'"
          >
            {{ t('overview.discovery') }}: {{ board.background_tasks.discovery.status ?? '—' }}
          </span>
          <span v-if="board?.background_tasks?.probe_loop" class="m-chip">
            {{ t('overview.probe') }}: {{ board.background_tasks.probe_loop.checks_last_10m ?? 0 }}
          </span>
          <span v-if="board?.selfcheck" class="m-chip" :class="board.selfcheck.last_status === 'ok' ? 'm-chip--success' : 'm-chip--warning'">
            {{ t('overview.selfcheck') }}: {{ board.selfcheck.last_status ?? '—' }}
          </span>
        </div>
      </template>
    </template>
  </section>
</template>

<style scoped>
.ptr-host {
  position: relative;
}

.ptr-indicator {
  overflow: hidden;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 0.75rem;
  color: var(--app-text-secondary);
  transition: none;
}

.ptr-indicator[data-armed='true'] {
  color: var(--app-primary);
  font-weight: 600;
}

.stale-note {
  font-size: 0.75rem;
  color: var(--app-warning);
  margin-bottom: var(--app-space-2);
}

.status-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--app-space-2);
}

.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: currentColor;
}

.ver {
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}

.chips {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
}
</style>
