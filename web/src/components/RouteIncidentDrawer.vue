<script setup lang="ts">
// RouteIncidentDrawer.vue — read-only diagnostic workbench.
//
// Phase 1 (this file) renders the seven sections from the spec:
//   1. Status header (affected route, state, failure streak,
//      recovery progress, first/last failure, refresh /
//      "diagnostic only" copy).
//   2. Current route (protocol, model mapping, provider,
//      credential id, routing decision).
//   3. Evidence summary (error kinds, failure stage, latency
//      change, correlated dimensions, sample request links).
//   4. Last 24 hours (5-min request / error / latency / recovery
//      timeline). Insufficient data is explicit.
//   5. Request and transformation comparison — phase 1 surface:
//      a) the latest sample request id (read from the detail's
//         sample_request_ids) and b) a one-line note that phase 2
//         will hydrate field-path diffs.
//   6. Resource snapshot (slot and concurrency state,
//      circuit/quota/availability, action history). Phase 1
//      reports zeros / "not yet wired" placeholders.
//   7. Logs and requests — time-correlated events and a deep
//      link to the existing request-detail drawer.
//
// The drawer has dialog semantics (focus trap, Escape close,
// focus restoration, reduced-motion support) per spec §"Dashboard
// Experience". Mobile uses full viewport; desktop uses the right
// drawer treatment.

import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import {
  getRouteIncident,
  getRouteIncidentEvents,
  getRouteIncidentTimeline,
} from '../api/routeIncidents'
import type {
  RouteIncident,
  RouteIncidentDetail,
  RouteIncidentEvent,
  RouteIncidentFinding,
  RouteIncidentTimelinePoint,
} from '../types/routeIncident'

const props = defineProps<{
  incidentId: string | null
  tenantId?: string
  // Pre-fetched incident snapshot from the SSE-driven state map.
  // The drawer uses this to render the header immediately and then
  // upgrades to the full detail response (which has findings,
  // timeline, sample request ids).
  preview?: RouteIncident | null
}>()

const emit = defineEmits<{
  close: []
  openRequest: [requestId: string]
}>()

const detail = ref<RouteIncidentDetail | null>(null)
const events = ref<RouteIncidentEvent[]>([])
const timeline = ref<RouteIncidentTimelinePoint[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const timelineError = ref<string | null>(null)
const lastFetchAt = ref<number>(0)

const closeBtn = ref<HTMLButtonElement | null>(null)
const drawerEl = ref<HTMLElement | null>(null)
let lastFocused: HTMLElement | null = null

// open state — animates in only when incidentId is non-null.
const open = computed(() => !!props.incidentId)

// recoveryProgress is shown as "n/5" inline. The drawer shows it
// in the status header and the Evidence summary's first finding.
const recoveryProgress = computed(() => {
  if (detail.value) {
    return detail.value.recovery_progress
  }
  if (props.preview) {
    return {
      current: props.preview.recovery_streak,
      target: 5,
    }
  }
  return { current: 0, target: 5 }
})

const visibleState = computed(() => {
  if (detail.value) return detail.value.incident.state
  if (props.preview) return props.preview.state
  return 'active'
})

const visibleStateLabel = computed(() => {
  switch (visibleState.value) {
    case 'active':
      return '活跃'
    case 'recovering':
      return '恢复中'
    case 'recovered':
      return '已恢复'
    default:
      return visibleState.value
  }
})

const stateTone = computed(() => {
  switch (visibleState.value) {
    case 'active':
      return 'danger'
    case 'recovering':
      return 'warning'
    case 'recovered':
      return 'success'
    default:
      return 'muted'
  }
})

const isInsufficientTimeline = computed(
  () => timeline.value.length === 0 && !loading.value && !timelineError.value,
)

const isInsufficientFindings = computed(
  () =>
    !!detail.value &&
    (detail.value.findings?.length ?? 0) === 0 &&
    (detail.value.insufficient_data?.includes('findings') ?? false),
)

const findingsForView = computed<RouteIncidentFinding[]>(() => {
  if (!detail.value) return []
  return detail.value.findings || []
})

const sampleRequests = computed(() => detail.value?.sample_request_ids || [])

const routeKey = computed(() => {
  if (detail.value) return detail.value.incident.route_key
  if (props.preview) return props.preview.route_key
  return null
})

const currentRoute = computed(() => detail.value?.current_route || null)

const resourceSnapshot = computed(
  () =>
    detail.value?.resource_snapshot || {
      action_history_count: 0,
    },
)

// fetchDetail loads the rich detail payload. Failures are surfaced
// as `error` so the drawer can render an "unable to load" state.
async function fetchDetail(id: string) {
  loading.value = true
  error.value = null
  timelineError.value = null
  try {
    const [d, e, t] = await Promise.all([
      getRouteIncident(id, props.tenantId),
      getRouteIncidentEvents(id, 200, props.tenantId),
      getRouteIncidentTimeline(id, props.tenantId),
    ])
    detail.value = d
    events.value = e?.items || []
    timeline.value = t?.items || []
    if (!d) {
      error.value = '找不到该路由事件'
    } else if (!t) {
      timelineError.value = '近 24 小时数据加载失败'
    }
  } catch (e: any) {
    error.value = e?.message || '详情加载失败'
  } finally {
    loading.value = false
    lastFetchAt.value = Date.now()
  }
}

async function refresh() {
  if (!props.incidentId) return
  await fetchDetail(props.incidentId)
}

// watch(incidentId) drives the open/close lifecycle and the
// initial fetch.
watch(
  () => props.incidentId,
  async (id) => {
    if (id) {
      lastFocused = (document.activeElement as HTMLElement) || null
      await nextTick()
      closeBtn.value?.focus()
      void fetchDetail(id)
    } else {
      detail.value = null
      events.value = []
      timeline.value = []
      error.value = null
      if (lastFocused && document.contains(lastFocused)) {
        lastFocused.focus()
      }
    }
  },
  { immediate: true },
)

// Keyboard handling: Escape closes, Tab is trapped inside the
// drawer. Phase 1 doesn't allow any in-drawer mutations, so the
// only tabbable element is the close button + a few read-only
// links; the trap is a defensive net.
function onKeydown(ev: KeyboardEvent) {
  if (!open.value) return
  if (ev.key === 'Escape') {
    ev.preventDefault()
    emit('close')
    return
  }
  if (ev.key !== 'Tab') return
  const root = drawerEl.value
  if (!root) return
  const focusables = Array.from(
    root.querySelectorAll<HTMLElement>(
      'button, [href], [tabindex]:not([tabindex="-1"]), input, select, textarea',
    ),
  ).filter((el) => !el.hasAttribute('disabled') && el.offsetParent !== null)
  if (focusables.length === 0) return
  const first = focusables[0]
  const last = focusables[focusables.length - 1]
  if (ev.shiftKey && document.activeElement === first) {
    ev.preventDefault()
    last.focus()
  } else if (!ev.shiftKey && document.activeElement === last) {
    ev.preventDefault()
    first.focus()
  }
}

onBeforeUnmount(() => {
  if (lastFocused && document.contains(lastFocused)) {
    lastFocused.focus()
  }
})

function openSample(requestId: string) {
  if (!requestId) return
  emit('openRequest', requestId)
}

function close() {
  emit('close')
}

// Helper to format a ts string for display.
function formatTs(ts?: string | null): string {
  if (!ts) return '—'
  try {
    const d = new Date(ts)
    if (Number.isNaN(d.getTime())) return ts
    return d.toLocaleString()
  } catch {
    return ts
  }
}

function latencyClass(p: RouteIncidentTimelinePoint): string {
  if (p.p99_latency_ms == null) return ''
  if (p.p99_latency_ms >= 5000) return 'timeline-bar--critical'
  if (p.p99_latency_ms >= 2000) return 'timeline-bar--slow'
  if (p.p99_latency_ms >= 1000) return 'timeline-bar--warn'
  return 'timeline-bar--ok'
}

function timelineBarWidth(p: RouteIncidentTimelinePoint): string {
  // Scale against the largest p99 in the visible window.
  const max = timeline.value.reduce(
    (m, x) => Math.max(m, x.p99_latency_ms ?? 0),
    0,
  )
  if (max <= 0) return '0%'
  return `${Math.max(2, ((p.p99_latency_ms ?? 0) / max) * 100)}%`
}
</script>

<template>
  <Transition name="route-incident-drawer">
    <div
      v-if="open"
      ref="drawerEl"
      class="route-incident-drawer"
      role="dialog"
      aria-modal="true"
      :aria-label="`诊断工作台: ${preview?.route_key?.model || ''}`"
      @keydown="onKeydown"
    >
      <div class="route-incident-drawer__backdrop" @click="close" />
      <div class="route-incident-drawer__panel">
        <header class="route-incident-drawer__header">
          <div class="route-incident-drawer__title">
            <span class="route-incident-drawer__icon" aria-hidden="true">🔍</span>
            <div>
              <div class="route-incident-drawer__heading">
                诊断工作台
              </div>
              <div class="route-incident-drawer__sub">
                {{ routeKey?.model || '—' }}
                <span v-if="routeKey?.endpoint_protocol" class="route-incident-drawer__chip">
                  {{ routeKey.endpoint_protocol }}
                </span>
              </div>
            </div>
          </div>
          <div class="route-incident-drawer__actions">
            <button
              type="button"
              class="route-incident-drawer__btn route-incident-drawer__btn--ghost"
              :disabled="loading"
              @click="refresh"
            >
              {{ loading ? '刷新中…' : '刷新' }}
            </button>
            <button
              ref="closeBtn"
              type="button"
              class="route-incident-drawer__btn route-incident-drawer__btn--close"
              aria-label="关闭诊断"
              @click="close"
            >
              ✕
            </button>
          </div>
        </header>

        <div class="route-incident-drawer__body">
          <!-- 1. Status header -->
          <section class="route-incident-drawer__section">
            <h3 class="route-incident-drawer__section-title">1. 状态</h3>
            <div class="status-grid">
              <div class="status-grid__cell">
                <div class="status-grid__label">状态</div>
                <div :class="['status-grid__value', `status-grid__value--${stateTone}`]">
                  {{ visibleStateLabel }}
                </div>
              </div>
              <div class="status-grid__cell">
                <div class="status-grid__label">失败连续</div>
                <div class="status-grid__value">
                  {{ detail?.incident?.failure_streak ?? preview?.failure_streak ?? 0 }}
                </div>
              </div>
              <div class="status-grid__cell">
                <div class="status-grid__label">恢复进度</div>
                <div class="status-grid__value">
                  <template v-if="visibleState === 'recovering'">
                    Recovery {{ recoveryProgress.current }} / {{ recoveryProgress.target }}
                  </template>
                  <template v-else>—</template>
                </div>
              </div>
              <div class="status-grid__cell">
                <div class="status-grid__label">首次失败</div>
                <div class="status-grid__value">
                  {{ formatTs(detail?.incident?.first_failure_at ?? preview?.first_failure_at) }}
                </div>
              </div>
              <div class="status-grid__cell">
                <div class="status-grid__label">最近失败</div>
                <div class="status-grid__value">
                  {{ formatTs(detail?.incident?.last_failure_at ?? preview?.last_failure_at) }}
                </div>
              </div>
              <div class="status-grid__cell">
                <div class="status-grid__label">最近成功</div>
                <div class="status-grid__value">
                  {{ formatTs(detail?.incident?.last_success_at) }}
                </div>
              </div>
            </div>
          </section>

          <!-- 2. Current route -->
          <section class="route-incident-drawer__section">
            <h3 class="route-incident-drawer__section-title">2. 当前路由</h3>
            <div v-if="currentRoute" class="kv">
              <div class="kv__row">
                <span class="kv__key">协议</span>
                <span class="kv__val">{{ currentRoute.protocol || '—' }}</span>
              </div>
              <div class="kv__row">
                <span class="kv__key">标准模型</span>
                <span class="kv__val">{{ currentRoute.canonical_model || '—' }}</span>
              </div>
              <div class="kv__row">
                <span class="kv__key">出站模型</span>
                <span class="kv__val">{{ currentRoute.outbound_model || '—' }}</span>
              </div>
              <div class="kv__row">
                <span class="kv__key">供应商 ID</span>
                <span class="kv__val">{{ currentRoute.provider_id ?? '—' }}</span>
              </div>
              <div class="kv__row">
                <span class="kv__key">凭据 ID</span>
                <span class="kv__val">{{ currentRoute.credential_id ?? '—' }}</span>
              </div>
              <div class="kv__row">
                <span class="kv__key">路由决策</span>
                <span class="kv__val">{{ currentRoute.routing_decision || '—' }}</span>
              </div>
            </div>
            <p v-else class="route-incident-drawer__empty">路由信息加载中…</p>
          </section>

          <!-- 3. Evidence summary -->
          <section class="route-incident-drawer__section">
            <h3 class="route-incident-drawer__section-title">3. 证据概览</h3>
            <div v-if="findingsForView.length > 0" class="findings">
              <article
                v-for="(f, idx) in findingsForView"
                :key="idx"
                class="finding"
              >
                <header class="finding__head">
                  <span class="finding__label">{{ f.label }}</span>
                  <span class="finding__samples">样本 {{ f.sample_count }}</span>
                </header>
                <p v-if="f.note" class="finding__note">{{ f.note }}</p>
                <ul v-if="f.request_ids && f.request_ids.length > 0" class="finding__ids">
                  <li v-for="id in f.request_ids" :key="id">
                    <button
                      type="button"
                      class="route-incident-drawer__link"
                      @click="openSample(id)"
                    >
                      {{ id }}
                    </button>
                  </li>
                </ul>
              </article>
            </div>
            <p
              v-else-if="isInsufficientFindings"
              class="route-incident-drawer__empty"
            >
              证据不足：暂无可用样本
            </p>
            <p v-else class="route-incident-drawer__empty">证据加载中…</p>
          </section>

          <!-- 4. Last 24 hours -->
          <section class="route-incident-drawer__section">
            <h3 class="route-incident-drawer__section-title">4. 近 24 小时</h3>
            <p v-if="timelineError" class="route-incident-drawer__error">
              {{ timelineError }}
            </p>
            <div v-else-if="isInsufficientTimeline" class="route-incident-drawer__empty">
              证据不足：近 24 小时该路由无数据
            </div>
            <div v-else-if="timeline.length > 0" class="timeline">
              <div
                v-for="p in timeline"
                :key="p.bucket_start"
                class="timeline-row"
              >
                <span class="timeline-row__time">{{ formatTs(p.bucket_start) }}</span>
                <span class="timeline-row__counts">
                  {{ p.requests }} req · {{ p.errors }} err
                </span>
                <span
                  :class="['timeline-row__bar', latencyClass(p)]"
                  :style="{ width: timelineBarWidth(p) }"
                />
                <span class="timeline-row__p99">
                  {{ p.p99_latency_ms != null ? Math.round(p.p99_latency_ms) + 'ms' : '—' }}
                </span>
              </div>
            </div>
            <p v-else class="route-incident-drawer__empty">时间线加载中…</p>
          </section>

          <!-- 5. Request and transformation comparison -->
          <section class="route-incident-drawer__section">
            <h3 class="route-incident-drawer__section-title">
              5. 请求与转换对比
            </h3>
            <p v-if="sampleRequests.length === 0" class="route-incident-drawer__empty">
              暂无样本请求
            </p>
            <ul v-else class="sample-requests">
              <li v-for="id in sampleRequests" :key="id">
                <button
                  type="button"
                  class="route-incident-drawer__link"
                  @click="openSample(id)"
                >
                  {{ id }}
                </button>
              </li>
            </ul>
            <p class="route-incident-drawer__note">
              字段级差异和脱敏预览将在第二期提供（spec §"Drawer sections", item 5）。
            </p>
          </section>

          <!-- 6. Resource snapshot -->
          <section class="route-incident-drawer__section">
            <h3 class="route-incident-drawer__section-title">6. 资源快照</h3>
            <div class="kv">
              <div class="kv__row">
                <span class="kv__key">槽位使用</span>
                <span class="kv__val">
                  {{ resourceSnapshot.slots_in_use ?? '—' }}
                  <template v-if="resourceSnapshot.slots_total != null">
                    / {{ resourceSnapshot.slots_total }}
                  </template>
                </span>
              </div>
              <div class="kv__row">
                <span class="kv__key">活跃并发</span>
                <span class="kv__val">
                  {{ resourceSnapshot.live_concurrent ?? '—' }}
                </span>
              </div>
              <div class="kv__row">
                <span class="kv__key">熔断状态</span>
                <span class="kv__val">
                  {{ resourceSnapshot.circuit_state || '—' }}
                </span>
              </div>
              <div class="kv__row">
                <span class="kv__key">剩余配额</span>
                <span class="kv__val">
                  {{ resourceSnapshot.quota_remaining ?? '—' }}
                </span>
              </div>
              <div class="kv__row">
                <span class="kv__key">可用性</span>
                <span class="kv__val">
                  {{ resourceSnapshot.availability_state || '—' }}
                </span>
              </div>
              <div class="kv__row">
                <span class="kv__key">操作历史</span>
                <span class="kv__val">
                  {{ resourceSnapshot.action_history_count }}
                </span>
              </div>
            </div>
            <p class="route-incident-drawer__note">
              第一期只读展示，<code>release_slot / reset_slots / reset_availability / reprobe / recover</code>
              将在第二期提供（spec §"Phase Two Diagnostic Runs And Actions"）。
            </p>
          </section>

          <!-- 7. Logs and requests -->
          <section class="route-incident-drawer__section">
            <h3 class="route-incident-drawer__section-title">7. 日志与请求</h3>
            <ol v-if="events.length > 0" class="event-list">
              <li v-for="e in events" :key="e.id" class="event-row">
                <span class="event-row__time">{{ formatTs(e.created_at) }}</span>
                <span :class="['event-row__type', `event-row__type--${e.event_type}`]">
                  {{ e.event_type }}
                </span>
                <span v-if="e.failure_kind" class="event-row__kind">{{ e.failure_kind }}</span>
                <span v-if="e.terminal_status" class="event-row__status">
                  {{ e.terminal_status }}
                </span>
                <span v-if="e.failure_streak" class="event-row__streak">
                  fs={{ e.failure_streak }}
                </span>
                <span v-if="e.recovery_streak" class="event-row__streak">
                  rs={{ e.recovery_streak }}
                </span>
                <button
                  v-if="e.request_id"
                  type="button"
                  class="route-incident-drawer__link"
                  @click="openSample(e.request_id)"
                >
                  {{ e.request_id }}
                </button>
              </li>
            </ol>
            <p v-else class="route-incident-drawer__empty">暂无事件记录</p>
          </section>

          <p v-if="error" class="route-incident-drawer__error">{{ error }}</p>
          <p class="route-incident-drawer__footnote">
            最后更新：{{ formatTs(new Date(lastFetchAt).toISOString()) }}
          </p>
        </div>
      </div>
    </div>
  </Transition>
</template>

<style scoped>
.route-incident-drawer {
  position: fixed;
  inset: 0;
  z-index: 1100;
  display: flex;
  justify-content: flex-end;
  font-family: var(--font-base, system-ui, -apple-system, sans-serif);
}

.route-incident-drawer__backdrop {
  position: absolute;
  inset: 0;
  background: rgba(0, 0, 0, 0.45);
}

.route-incident-drawer__panel {
  position: relative;
  display: flex;
  flex-direction: column;
  width: min(720px, 100vw);
  height: 100%;
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  border-left: 1px solid var(--border, #30363d);
  box-shadow: -8px 0 24px rgba(0, 0, 0, 0.4);
}

.route-incident-drawer__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border, #30363d);
  background: var(--bg-subtle, #161b22);
  position: sticky;
  top: 0;
  z-index: 2;
}

.route-incident-drawer__title {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
}

.route-incident-drawer__icon {
  font-size: 18px;
}

.route-incident-drawer__heading {
  font-size: 14px;
  font-weight: 600;
}

.route-incident-drawer__sub {
  font-size: 12px;
  color: var(--text-secondary, #8b949e);
  display: flex;
  align-items: center;
  gap: 8px;
  word-break: break-all;
}

.route-incident-drawer__chip {
  display: inline-block;
  padding: 1px 6px;
  border-radius: 3px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  font-size: 10px;
  font-family: ui-monospace, monospace;
}

.route-incident-drawer__actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

.route-incident-drawer__btn {
  font-size: 12px;
  padding: 5px 10px;
  border-radius: 4px;
  border: 1px solid var(--border, #30363d);
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  cursor: pointer;
  font-weight: 500;
}

.route-incident-drawer__btn:hover:not(:disabled) {
  border-color: var(--accent, #6366f1);
  background: var(--bg-subtle, #161b22);
}

.route-incident-drawer__btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.route-incident-drawer__btn--close {
  width: 28px;
  height: 28px;
  padding: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}

.route-incident-drawer__body {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 18px;
  scrollbar-gutter: stable;
}

.route-incident-drawer__section {
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  padding: 12px 14px;
  background: var(--bg-subtle, #161b22);
}

.route-incident-drawer__section-title {
  margin: 0 0 8px 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--text, #e6edf3);
  letter-spacing: 0.02em;
}

.route-incident-drawer__empty {
  font-size: 12px;
  color: var(--text-secondary, #8b949e);
  margin: 0;
}

.route-incident-drawer__error {
  font-size: 12px;
  color: var(--danger, #f85149);
  margin: 0;
}

.route-incident-drawer__note {
  font-size: 11px;
  color: var(--text-secondary, #8b949e);
  margin: 8px 0 0 0;
  line-height: 1.5;
}

.route-incident-drawer__note code {
  font-family: ui-monospace, monospace;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  padding: 1px 4px;
  border-radius: 3px;
}

.route-incident-drawer__link {
  font-family: ui-monospace, monospace;
  font-size: 11px;
  background: transparent;
  border: 1px solid var(--border, #30363d);
  color: var(--accent, #6366f1);
  padding: 1px 6px;
  border-radius: 3px;
  cursor: pointer;
}

.route-incident-drawer__link:hover {
  background: var(--bg, #0f1117);
  border-color: var(--accent, #6366f1);
}

.route-incident-drawer__footnote {
  font-size: 10px;
  color: var(--text-secondary, #8b949e);
  margin: 0;
  text-align: right;
}

.status-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 8px;
}

.status-grid__cell {
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 4px;
  padding: 6px 8px;
}

.status-grid__label {
  font-size: 10px;
  color: var(--text-secondary, #8b949e);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.status-grid__value {
  font-size: 13px;
  font-weight: 600;
  margin-top: 2px;
}

.status-grid__value--danger {
  color: var(--danger, #f85149);
}
.status-grid__value--warning {
  color: var(--warning, #d29922);
}
.status-grid__value--success {
  color: var(--success, #3fb950);
}
.status-grid__value--muted {
  color: var(--text-secondary, #8b949e);
}

.kv {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.kv__row {
  display: flex;
  gap: 8px;
  font-size: 12px;
  align-items: center;
}

.kv__key {
  flex: 0 0 96px;
  color: var(--text-secondary, #8b949e);
}

.kv__val {
  flex: 1;
  font-family: ui-monospace, monospace;
  word-break: break-all;
}

.findings {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.finding {
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 4px;
  padding: 8px;
}

.finding__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.finding__label {
  font-size: 12px;
  font-weight: 600;
}

.finding__samples {
  font-size: 10px;
  color: var(--text-secondary, #8b949e);
}

.finding__note {
  font-size: 11px;
  color: var(--text-secondary, #8b949e);
  margin: 4px 0;
}

.finding__ids {
  list-style: none;
  padding: 0;
  margin: 4px 0 0 0;
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.timeline {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 240px;
  overflow-y: auto;
}

.timeline-row {
  display: grid;
  grid-template-columns: 96px 1fr 90px 60px;
  gap: 8px;
  font-size: 11px;
  align-items: center;
  padding: 2px 0;
}

.timeline-row__time {
  font-family: ui-monospace, monospace;
  color: var(--text-secondary, #8b949e);
}

.timeline-row__counts {
  font-variant-numeric: tabular-nums;
}

.timeline-row__bar {
  height: 6px;
  border-radius: 3px;
  background: var(--accent, #6366f1);
  min-width: 2px;
}

.timeline-bar--ok { background: var(--success, #3fb950); }
.timeline-bar--warn { background: var(--warning, #d29922); }
.timeline-bar--slow { background: #f97316; }
.timeline-bar--critical { background: var(--danger, #f85149); }

.timeline-row__p99 {
  text-align: right;
  font-variant-numeric: tabular-nums;
  font-family: ui-monospace, monospace;
}

.sample-requests {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.event-list {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 240px;
  overflow-y: auto;
}

.event-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 11px;
  flex-wrap: wrap;
  padding: 2px 0;
  border-bottom: 1px dashed var(--border, #30363d);
}

.event-row__time {
  font-family: ui-monospace, monospace;
  color: var(--text-secondary, #8b949e);
  min-width: 130px;
}

.event-row__type {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 3px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
}

.event-row__type--opened { color: var(--danger, #f85149); border-color: rgba(248, 81, 73, 0.4); }
.event-row__type--failure_observed { color: var(--danger, #f85149); border-color: rgba(248, 81, 73, 0.4); }
.event-row__type--recovery_progress { color: var(--warning, #d29922); border-color: rgba(210, 153, 34, 0.4); }
.event-row__type--recovered { color: var(--success, #3fb950); border-color: rgba(63, 185, 80, 0.4); }

.event-row__kind,
.event-row__status,
.event-row__streak {
  font-family: ui-monospace, monospace;
  color: var(--text-secondary, #8b949e);
}

/* Reduced motion: drop the slide-in animation. */
@media (prefers-reduced-motion: reduce) {
  .route-incident-drawer-enter-active,
  .route-incident-drawer-leave-active {
    transition: opacity 0.12s linear;
  }
  .route-incident-drawer-enter-from,
  .route-incident-drawer-leave-to {
    opacity: 0;
    transform: none;
  }
}

.route-incident-drawer-enter-active .route-incident-drawer__panel,
.route-incident-drawer-leave-active .route-incident-drawer__panel {
  transition: transform 0.28s cubic-bezier(0.16, 1, 0.3, 1);
}

.route-incident-drawer-enter-from .route-incident-drawer__panel,
.route-incident-drawer-leave-to .route-incident-drawer__panel {
  transform: translateX(100%);
}

.route-incident-drawer-enter-active .route-incident-drawer__backdrop,
.route-incident-drawer-leave-active .route-incident-drawer__backdrop {
  transition: opacity 0.28s ease;
}

.route-incident-drawer-enter-from .route-incident-drawer__backdrop,
.route-incident-drawer-leave-to .route-incident-drawer__backdrop {
  opacity: 0;
}

/* Mobile: full viewport. */
@media (max-width: 768px) {
  .route-incident-drawer__panel {
    width: 100vw;
    border-left: none;
  }
}
</style>
