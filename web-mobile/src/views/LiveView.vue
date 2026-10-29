<script setup lang="ts">
// LiveView — 实时页（live）的移动端形态（2026-10-29）。
//
// 数据源（三条腿，全部 admin 档、tenant_admin 可用）：
//   GET /api/admin/topology/{providers,models,clients}-today  区间统计（5m/1h/当天）
//   GET /api/admin/request-journeys/queues                    实时请求（在途 + 已完成快照）
//   GET /api/admin/live-stream/stats                          SSE Hub 健康脚注
//
// ── 为什么移动端不做桌面那套 SSE 拓扑 ──────────────────────────────────────
// 桌面 /live 是 Canvas 拓扑 + Element Plus + 粒子池（9600+ 行），其移动形态
// （2026-10-28 手机适配轮）走的是**桌面站点窄屏响应式**，/m/ 不重定向深链。
// 本页是 Hyper 形态的原生观察页：轮询 + 下拉刷新，零常驻连接 ——
// 壳内 native transport 不共享 cookie jar ⇒ EventSource 的同源 cookie 前提
// 不成立（详见 api/liveStream.ts 文件头），且观察页 10s 节奏足够。
//
// ── 权限 ──────────────────────────────────────────────────────────────────
// 三条腿都是 `admin(...)` 注册 ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** requiresRole。
// （数据本身在 hub / SQL 内按租户作用域过滤，越权面在后端已收口。）
//
// ⚠️ 契约要点（判据钉在 spec）：
//   · degraded: true = 聚合表未就绪 ⇒ 显示「统计不可用」，不是零行。
//   · window= 乱写被后端静默落回 day ⇒ 区间标题以响应回显的 window 为准。
//   · day 口径有 rollup 水位（≤1h 滞后）⇒ 水位时间必须显示。
//   · 成功率分母 0 ⇒ 「—」，不许 0% 冒充。
//   · 实时请求与统计是**两个来源**：统计是 DB 口径、实时是进程内快照，
//     两者数字短暂不一致是正常的（页面文案要说明分工，不是合并成一套）。

import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import PullRefreshContainer from '@/components/common/PullRefreshContainer.vue'
import AppStateView from '@/components/common/AppStateView.vue'
import StatCard from '@/components/common/StatCard.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { fmtInt, fmtNum, fmtTime, fmtUsd, relativeTime } from '@/utils/format'
import { useAuthStore } from '@/stores/auth'
import { useRouter } from 'vue-router'
import {
  fetchClientsToday,
  fetchModelsToday,
  fetchProvidersToday,
  liveStatRowName,
  liveStatRowSuccessRate,
  type LiveStatsEnvelope,
  type LiveStatsWindow,
} from '@/api/liveTopology'
import { fetchLiveStreamStats, liveStreamStatsNotWired, type LiveStreamStats } from '@/api/liveStream'
import {
  fetchJourneyQueues,
  isDegraded,
  isIngress,
  type JourneyQueuesResponse,
  type RequestSnapshot,
  type IngressSnapshot,
} from '@/api/requestJourney'

const router = useRouter()
const auth = useAuthStore()

useHyperPage({ title: () => t('live.title') })

const homeRef = ref<HTMLElement | null>(null)
const homeScroller = (): HTMLElement | null => homeRef.value

type WindowKey = LiveStatsWindow | 'day'
const WINDOW_KEYS: readonly WindowKey[] = ['5m', '1h', 'day']

const windowKey = ref<WindowKey>('5m')

function windowParam(w: WindowKey): LiveStatsWindow | undefined {
  return w === 'day' ? undefined : w
}

const initialLoading = ref(true)
const loadError = ref<string | null>(null)
const lastRefreshFailed = ref(false)

const providers = ref<LiveStatsEnvelope | null>(null)
const models = ref<LiveStatsEnvelope | null>(null)
const clients = ref<LiveStatsEnvelope | null>(null)

/** 实时请求（journey queues 的 total 视图快照）。 */
const liveQueue = ref<JourneyQueuesResponse | null>(null)
const liveQueueFailed = ref(false)

/** SSE Hub 脚注（503 = 未装配，显示为「未启用」而非报错）。 */
const hubStats = ref<LiveStreamStats | null>(null)
const hubNotWired = ref(false)

let aborter: AbortController | null = null
let pollTimer: number | null = null
let disposed = false

/** 区间口径以**响应回显**为准（?window= 乱写被后端静默落回 day）。 */
const echoedWindow = computed<WindowKey>(() => {
  const w = providers.value?.window
  if (w === '5m' || w === '1h') return w
  return 'day'
})

const degraded = computed(
  () => !!(providers.value?.degraded || models.value?.degraded || clients.value?.degraded),
)

/** day 口径的 rollup 水位（≤1h 滞后）；短窗口口径恒空。 */
const watermark = computed(() => {
  if (echoedWindow.value !== 'day') return ''
  return providers.value?.source_high_watermark || models.value?.source_high_watermark || ''
})

const totals = computed(() => providers.value?.totals)
const rows = computed<LiveStatsEnvelope | null>(() => {
  if (tab.value === 'providers') return providers.value
  if (tab.value === 'models') return models.value
  return clients.value
})

const tab = ref<'providers' | 'models' | 'clients'>('providers')

async function loadStats(signal: AbortSignal): Promise<void> {
  const w = windowParam(windowKey.value)
  const [pv, mv, cv] = await Promise.all([
    fetchProvidersToday(w, { signal }),
    fetchModelsToday(w, { signal }),
    fetchClientsToday(w, { signal }),
  ])
  providers.value = pv
  models.value = mv
  clients.value = cv
}

async function loadLiveQueue(signal: AbortSignal): Promise<void> {
  try {
    liveQueue.value = await fetchJourneyQueues({ view: 'total' }, { signal })
    liveQueueFailed.value = false
  } catch (err) {
    if (signal.aborted) return
    liveQueueFailed.value = true
  }
}

async function loadHub(signal: AbortSignal): Promise<void> {
  try {
    hubStats.value = await fetchLiveStreamStats({ signal })
    hubNotWired.value = false
  } catch (err) {
    if (signal.aborted) return
    if (liveStreamStatsNotWired(err)) hubNotWired.value = true
    // 其它失败（网络抖动）保持上一次读数 —— 脚注不冒充硬错误。
  }
}

async function loadAll(): Promise<void> {
  aborter?.abort()
  aborter = new AbortController()
  const signal = aborter.signal
  try {
    await Promise.all([loadStats(signal), loadLiveQueue(signal), loadHub(signal)])
    if (signal.aborted || disposed) return
    loadError.value = null
    lastRefreshFailed.value = false
  } catch (err) {
    if (signal.aborted || disposed) return
    loadError.value = (err as { message?: string })?.message || t('common.error')
    lastRefreshFailed.value = true
  }
}

async function refresh(): Promise<void> {
  await loadAll()
}

onMounted(async () => {
  await loadAll()
  if (disposed) return
  initialLoading.value = false
  pollTimer = window.setInterval(() => {
    if (document.hidden) return
    void loadAll()
  }, 10_000)
})

onBeforeUnmount(() => {
  disposed = true
  aborter?.abort()
  if (pollTimer !== null) window.clearInterval(pollTimer)
})

// ── 展开收起（供应商/模型两组默认收起，只显 top5 摘要） ────────────────────
const expanded = ref(false)
const visibleRows = computed(() => {
  const items = rows.value?.items ?? []
  return expanded.value ? items : items.slice(0, 5)
})

function toneOf(r: LiveRow): 'success' | 'warning' | 'danger' | 'muted' {
  // ★ RequestSnapshot 没有 status 字段（只有 outcome/lifecycle_state），
  //   联合类型上 r.status 是索引签名的 unknown —— 与 RequestJourneyView.tone
  //   同款的窄化读法，不允许 as 强转。
  if (isIngress(r)) {
    if (r.status === 'failed') return 'danger'
    if (r.status === 'in_flight') return 'warning'
    return 'muted'
  }
  if (r.error_kind || r.outcome === 'failure') return 'danger'
  if (r.lifecycle_state === 'in_flight') return 'warning'
  return 'muted'
}

type LiveRow = RequestSnapshot | IngressSnapshot

const liveRows = computed<LiveRow[]>(() => (liveQueue.value?.total_snapshot?.requests ?? []).slice(0, 12))

function liveModelText(r: LiveRow): string {
  if (isIngress(r)) return '—'
  return r.resolved_model || r.requested_model || '—'
}

function liveStageText(r: LiveRow): string {
  return isIngress(r) ? r.status : r.current_stage
}

function openJourney(r: LiveRow): void {
  void router.push({ path: `/journey/${encodeURIComponent(r.request_id)}` })
}

function openFullJourney(): void {
  void router.push({ path: '/journey' })
}

/** 超管入口：桌面 /live 全景页（同源 SPA，直接整页跳）。 */
const desktopLive = computed(() => auth.role === 'super_admin')
function openDesktopLive(): void {
  window.location.href = '/live'
}
</script>

<template>
  <PullRefreshContainer :on-refresh="refresh" :scroller="homeScroller" class="live">
    <div ref="homeRef" class="page live__page">
      <p v-if="lastRefreshFailed" class="live__refresh-failed" role="status">{{ t('common.refreshFailed') }}</p>

      <AppStateView :loading="initialLoading && !providers" :error="loadError" :skeleton-rows="5" @retry="refresh">
        <template v-if="providers">
          <!-- 区间选择：chip 三态。⚠️ 标题以回显为准（echoedWindow）。 -->
          <div class="live__row" role="tablist">
            <button
              v-for="w in WINDOW_KEYS"
              :key="w"
              type="button"
              class="live__chip"
              :class="{ 'live__chip--on': windowKey === w }"
              :aria-selected="windowKey === w"
              :data-testid="`live-window-${w}`"
              @click="windowKey !== w && ((windowKey = w), void loadAll())"
            >
              {{ t(`live.window.${w}`) }}
            </button>
            <span v-if="echoedWindow !== windowKey" class="live__echo" data-testid="live-window-echo">
              {{ t('live.windowEcho', { w: t(`live.window.${echoedWindow}`) }) }}
            </span>
          </div>

          <!-- 降级：统计不可用 ≠ 零行。总量卡也一并隐藏 —— degraded 下就算
               信封带了 totals，那些 0 也「不可作为结论」（同 HomeView 汇总卡
               的处置：降级是整块声明，不是逐卡标 0）。 -->
          <template v-if="degraded">
            <p class="live__banner live__banner--warn" role="status">
              <StatusDot tone="warning" />
              <span>{{ t('live.degraded') }}</span>
            </p>
          </template>
          <template v-else>
            <!-- day 口径水位（≤1h 滞后）必须显式展示 -->
            <p v-if="watermark" class="live__banner" data-testid="live-watermark">
              {{ t('live.watermark') }} · {{ fmtTime(watermark) }}
            </p>

            <!-- 总量行 -->
            <div class="live__grid">
              <StatCard :label="t('live.totalRequests')" :value="fmtInt(totals?.requests)" />
              <StatCard :label="t('live.successRate')" :value="totals && totals.requests ? fmtNum((totals.success / totals.requests) * 100, 1) + '%' : '—'" />
              <StatCard :label="t('live.totalTokens')" :value="fmtInt(totals?.total_tokens)" />
              <StatCard :label="t('live.totalCost')" :value="fmtUsd(totals?.cost_usd)" />
            </div>
          </template>

          <!-- 分组 tab + 行清单 -->
          <div class="live__row" role="tablist">
            <button
              v-for="tb in ['providers', 'models', 'clients'] as const"
              :key="tb"
              type="button"
              class="live__chip live__chip--sm"
              :class="{ 'live__chip--on': tab === tb }"
              :aria-selected="tab === tb"
              :data-testid="`live-tab-${tb}`"
              @click="tab = tb"
            >
              {{ t(`live.tab.${tb}`) }}
            </button>
          </div>

          <div class="data-card">
            <p v-if="(rows?.items ?? []).length === 0" class="live__empty" data-testid="live-rows-empty">
              {{ t('live.empty') }}
            </p>
            <div v-for="row in visibleRows" :key="liveStatRowName(row) + String(row.requests)" class="live__bar-row">
              <span class="live__bar-name" :title="liveStatRowName(row)">{{ liveStatRowName(row) || t('live.unknownName') }}</span>
              <div class="live__bar-track">
                <div class="live__bar-fill" :style="{ width: `${Math.max(3, row.share * 100)}%` }" />
              </div>
              <span class="live__bar-meta num">{{ fmtInt(row.requests) }}</span>
              <span class="live__bar-meta num live__bar-rate" :data-testid="'live-rate-' + liveStatRowName(row)">
                {{ liveStatRowSuccessRate(row) === null ? '—' : fmtNum(liveStatRowSuccessRate(row) as number, 1) + '%' }}
              </span>
            </div>
            <button
              v-if="(rows?.items ?? []).length > 5"
              type="button"
              class="live__more"
              :data-testid="'live-expand-' + (expanded ? 'less' : 'more')"
              @click="expanded = !expanded"
            >
              {{ expanded ? t('live.collapse') : t('live.expand', { n: (rows?.items ?? []).length }) }}
            </button>
          </div>

          <!-- 实时请求（进程内快照，与上面的 DB 统计口径不同源） -->
          <div class="live__row live__row--section">
            <h2 class="page__section-title live__section-title">{{ t('live.requestsTitle') }}</h2>
            <button type="button" class="live__link" data-testid="live-open-journey" @click="openFullJourney">
              {{ t('live.openJourney') }}
            </button>
          </div>
          <p v-if="liveQueueFailed" class="live__banner live__banner--warn">
            <StatusDot tone="warning" />
            <span>{{ t('live.queueUnavailable') }}</span>
          </p>
          <p v-else-if="liveQueue && isDegraded(liveQueue)" class="live__banner live__banner--warn">
            <StatusDot tone="warning" />
            <span>{{ t('live.queueDegraded') }}</span>
          </p>
          <div class="data-card" data-testid="live-requests">
            <p v-if="liveRows.length === 0 && !liveQueueFailed" class="live__empty">
              {{ t('live.requestsEmpty') }}
            </p>
            <button v-for="r in liveRows" :key="r.request_id" type="button" class="live__req" @click="openJourney(r)">
              <span class="live__req-model">
                <StatusDot :tone="toneOf(r)" />
                <span class="live__req-name">{{ liveModelText(r) }}</span>
              </span>
              <span class="badge" :class="`badge--${toneOf(r) === 'muted' ? 'muted' : toneOf(r)}`">{{ liveStageText(r) }}</span>
              <span class="live__req-time num">{{ relativeTime(r.updated_at) }}</span>
            </button>
          </div>

          <!-- SSE Hub 脚注：观察健康，不是硬错误 -->
          <p class="live__hub" data-testid="live-hub">
            <template v-if="hubNotWired">{{ t('live.hub.notWired') }}</template>
            <template v-else-if="hubStats">
              {{ t('live.hub.clients', { n: hubStats.active_clients }) }}
              · {{ t('live.hub.broadcasts', { n: hubStats.broadcast_count }) }}
              <template v-if="hubStats.broadcast_drops > 0">
                · <span class="live__hub-warn">{{ t('live.hub.drops', { n: hubStats.broadcast_drops }) }}</span>
              </template>
            </template>
            <template v-else>{{ t('common.loading') }}</template>
          </p>

          <!-- 桌面全景入口（super_admin 的 /live 有完整拓扑；admin 档不显示，
               避免一个点了 403 的入口 —— 与抽屉层「按 role 分档渲染」同规矩）。 -->
          <button v-if="desktopLive" type="button" class="live__more live__more--desktop" data-testid="live-open-desktop" @click="openDesktopLive">
            {{ t('live.openDesktop') }}
          </button>
        </template>
      </AppStateView>
    </div>
  </PullRefreshContainer>
</template>

<style scoped>
.live {
  display: flex;
  flex-direction: column;
}

.live__page {
  flex: 1;
}

.live__refresh-failed {
  font-size: 0.8125rem;
  color: var(--app-warning);
  margin-bottom: var(--app-space-2);
}

.live__row {
  display: flex;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  align-items: center;
  margin-bottom: var(--app-space-2);
}

.live__row--section {
  justify-content: space-between;
  margin-top: var(--app-space-4);
}

.live__section-title {
  margin: 0;
}

.live__chip {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
}

.live__chip--sm {
  min-height: 36px; /* R1-legacy: 存量 chip 档（RequestJourneyView 同款） */
  padding: 0 var(--app-space-2);
  font-size: 0.75rem;
}

.live__chip--on {
  color: var(--app-primary);
  border-color: var(--app-primary-border);
  background: var(--app-primary-soft);
  font-weight: 600;
}

.live__echo {
  font-size: 0.6875rem;
  color: var(--app-text-muted);
}

.live__banner {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  margin: 0 0 var(--app-space-2);
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.live__banner--warn {
  color: var(--app-warning);
}

.live__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-3);
}

.live__bar-row {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  padding: var(--app-space-1) 0;
  font-size: 0.8125rem;
}

.live__bar-name {
  flex: 0 0 38%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--app-text);
}

.live__bar-track {
  flex: 1 1 auto;
  height: 8px;
  border-radius: var(--app-radius-pill);
  background: var(--app-surface-muted);
  overflow: hidden;
}

.live__bar-fill {
  height: 100%;
  border-radius: var(--app-radius-pill);
  background: var(--app-primary);
}

.live__bar-meta {
  flex: 0 0 auto;
  min-width: 3.5em;
  text-align: right;
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.live__bar-rate {
  min-width: 4em;
}

.live__more {
  min-height: 44px; /* R1-legacy: 文本链式按钮，非表单控件 */
  margin-top: var(--app-space-1);
  padding: 0;
  border: none;
  background: none;
  font-size: 0.8125rem;
  color: var(--app-primary);
}

.live__more--desktop {
  display: block;
  width: 100%;
  text-align: center;
}

.live__link {
  min-height: 44px; /* R1-legacy: 同上 */
  padding: 0;
  border: none;
  background: none;
  font-size: 0.8125rem;
  color: var(--app-primary);
}

.live__empty {
  margin: 0;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}

.live__req {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  width: 100%;
  min-height: 44px; /* R1-legacy: 列表行（触控目标由整行面积保证） */
  padding: var(--app-space-1) 0;
  border: none;
  border-bottom: 1px solid var(--app-border-subtle);
  background: none;
  text-align: left;
  font-size: 0.8125rem;
}

.live__req:last-child {
  border-bottom: none;
}

.live__req-model {
  display: flex;
  flex: 1 1 auto;
  align-items: center;
  gap: var(--app-space-2);
  min-width: 0;
}

.live__req-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--app-text);
}

.live__req-time {
  flex: 0 0 auto;
  font-size: 0.6875rem;
  color: var(--app-text-muted);
}

.live__hub {
  margin: var(--app-space-3) 0 0;
  font-size: 0.6875rem;
  color: var(--app-text-muted);
}

.live__hub-warn {
  color: var(--app-warning);
}
</style>
