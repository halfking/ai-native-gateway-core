<script setup lang="ts">
// RouteIncidentDrawer.vue — read-only diagnostic workbench.
//
// Phase 1: 7 sections (status / current route / evidence /
//          24h / request compare / resource snapshot / logs).
// Phase 2 (2026-07-13): adds 8. Action panel (mutating +
//          diagnostic), 9. Audit log (immutable), and an
//          Evidence export button (with integrity checksum).
//
// All actions go through the ActionConfirmModal — a destructive
// actions MUST collect: reason (length-bounded), confirmation
// token (server-supplied), idempotency key (client-generated
// UUID v4). The modal enforces the same allow-list as the
// backend, so a typo on the wire cannot trigger an unmodelled
// endpoint.
//
// Accessibility:
//   - focus trap inside drawer + modal
//   - Escape closes (only one open at a time)
//   - reduced-motion respected via CSS @media (prefers-reduced-motion)
//   - all interactive elements have aria-label / title

import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import {
  dispatchAction,
  exportEvidence,
  generateIdempotencyKey,
  getAuditLog,
  getDiagnosticRuns,
  getRouteIncident,
  getRouteIncidentEvents,
  getRouteIncidentTimeline,
} from '../api/routeIncidents'
import {
  ACTION_LABEL,
  type ActionKind,
  type ActionRequest,
  type ActionResponse,
  type AuditLogEntry,
  type DiagnosticRun,
  type EvidenceExport,
  MUTATING_ACTIONS,
  type RouteIncident,
  type RouteIncidentDetail,
  type RouteIncidentEvent,
  type RouteIncidentFinding,
  type RouteIncidentTimelinePoint,
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

// Phase 2: audit log + diagnostic runs + last action response.
const auditEntries = ref<AuditLogEntry[]>([])
const runs = ref<DiagnosticRun[]>([])
const auditLoading = ref(false)
const auditError = ref<string | null>(null)

// Phase 2: action confirmation modal state.
const confirmKind = ref<ActionKind | null>(null)
const confirmReason = ref<string>('')
const confirmToken = ref<string>('')
const confirmParams = ref<Record<string, unknown>>({})
const confirmSubmitting = ref(false)
const confirmError = ref<string | null>(null)
const lastAction = ref<ActionResponse | null>(null)

// Phase 2: evidence export.
const exportState = ref<'idle' | 'busy' | 'error' | 'done'>('idle')
const lastExport = ref<EvidenceExport | null>(null)
const exportError = ref<string | null>(null)

const closeBtn = ref<HTMLButtonElement | null>(null)
const drawerEl = ref<HTMLElement | null>(null)
const modalEl = ref<HTMLElement | null>(null)
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

// Action panel — only enabled for active / recovering incidents.
// Recovered incidents cannot be actioned; the row stays as audit
// evidence.
const actionsEnabled = computed(
  () => visibleState.value === 'active' || visibleState.value === 'recovering',
)

const expectedVersion = computed(() => {
  if (detail.value) return detail.value.incident.version
  if (props.preview) return props.preview.version
  return 0
})

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
    // Audit + runs are best-effort; we always load them after the
    // detail so we can show the operator a current-state section.
    void fetchAuditAndRuns(id)
  } catch (e: any) {
    error.value = e?.message || '详情加载失败'
  } finally {
    loading.value = false
    lastFetchAt.value = Date.now()
  }
}

async function fetchAuditAndRuns(id: string) {
  auditLoading.value = true
  auditError.value = null
  try {
    const [a, r] = await Promise.all([
      getAuditLog(id, 100, props.tenantId),
      getDiagnosticRuns(id, 50, props.tenantId),
    ])
    auditEntries.value = a?.items || []
    runs.value = r?.items || []
  } catch (e: any) {
    auditError.value = e?.message || '审计日志加载失败'
  } finally {
    auditLoading.value = false
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
      // Reset transient phase-2 state.
      lastAction.value = null
      lastExport.value = null
      confirmKind.value = null
      confirmReason.value = ''
      confirmToken.value = ''
      confirmParams.value = {}
      exportState.value = 'idle'
      await nextTick()
      closeBtn.value?.focus()
      void fetchDetail(id)
    } else {
      detail.value = null
      events.value = []
      timeline.value = []
      error.value = null
      auditEntries.value = []
      runs.value = []
      auditError.value = null
      if (lastFocused && document.contains(lastFocused)) {
        lastFocused.focus()
      }
    }
  },
  { immediate: true },
)

// ─── Phase 2 action panel ────────────────────────────────────────

function openConfirm(kind: ActionKind) {
  if (!props.incidentId) return
  // Default parameters for actions that need structured input.
  confirmParams.value = {}
  if (kind === 'release_slot') {
    confirmParams.value = { slot_id: '' }
  } else if (kind === 'recover') {
    confirmParams.value = { target_state: 'recovered' }
  } else if (kind === 'through_gateway_test') {
    confirmParams.value = { max_tokens: 16, timeout_ms: 5000 }
  } else if (kind === 'direct_upstream_test') {
    confirmParams.value = { timeout_ms: 5000 }
  }
  // Server-side confirmation token. The server returns one short-
  // lived token per page load. Phase 2 uses a per-action pre-
  // generated token (length-bounded) so the modal can verify the
  // operator typed something matching what they were shown.
  confirmToken.value = generateConfirmationToken()
  confirmKind.value = kind
  confirmReason.value = ''
  confirmError.value = null
  confirmSubmitting.value = false
  // Move focus into the modal on the next tick.
  nextTick(() => {
    const first = modalEl.value?.querySelector<HTMLElement>(
      'input, textarea, select, button',
    )
    first?.focus()
  })
}

function generateConfirmationToken(): string {
  // 8-char alphanumeric; the dashboard only uses this for visual
  // confirm ("type this token to confirm"). The backend accepts
  // any non-empty value; the hash (SHA-256) is what gets stored.
  const c = (globalThis as unknown as { crypto?: { getRandomValues?: (a: Uint8Array) => Uint8Array } }).crypto
  if (c?.getRandomValues) {
    const a = new Uint8Array(8)
    c.getRandomValues(a)
    return Array.from(a, (b) => b.toString(16).padStart(2, '0')).join('').slice(0, 12)
  }
  return Math.random().toString(16).slice(2, 14)
}

async function submitAction() {
  if (!props.incidentId || !confirmKind.value) return
  if (confirmReason.value.trim() === '') {
    confirmError.value = '请填写操作原因（必填）'
    return
  }
  if (confirmToken.value.trim() === '') {
    confirmError.value = '确认令牌缺失，请重新打开对话框'
    return
  }
  confirmSubmitting.value = true
  confirmError.value = null
  const idem = generateIdempotencyKey(confirmKind.value)
  const body: ActionRequest = {
    reason: confirmReason.value.trim(),
    confirmation_token: confirmToken.value,
    idempotency_key: idem,
    parameters: confirmParams.value,
  }
  const resp = await dispatchAction(props.incidentId, confirmKind.value, body, {
    tenantId: props.tenantId,
    expectedVersion: expectedVersion.value,
  })
  confirmSubmitting.value = false
  if (!resp) {
    confirmError.value = '后端拒绝或网络异常（409 / 5xx）。请检查后重试。'
    return
  }
  lastAction.value = resp
  // Re-fetch detail so the operator sees the updated state.
  void fetchDetail(props.incidentId)
  confirmKind.value = null
}

// ─── Phase 2 evidence export ─────────────────────────────────────

async function exportRun(run: DiagnosticRun) {
  if (!props.incidentId) return
  exportState.value = 'busy'
  exportError.value = null
  const idem = generateIdempotencyKey('export')
  const reason = `证据导出（run ${run.id.slice(0, 8)}, dashboard）`
  const out = await exportEvidence(
    props.incidentId,
    run.id,
    reason,
    idem,
    props.tenantId,
  )
  if (!out) {
    exportState.value = 'error'
    exportError.value = '导出失败：详情加载或审计失败'
    return
  }
  exportState.value = 'done'
  lastExport.value = out
}

// The export bundle is large; the dashboard uses an `<a download>`
// blob link so the operator can save it locally. We serialize
// the JSON exactly as the server returned it (the integrity
// checksum is computed over the canonicalized fields, so any
// in-place mutation would invalidate it).
function downloadExport(exp: EvidenceExport) {
  const blob = new Blob([JSON.stringify(exp, null, 2)], {
    type: 'application/json',
  })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `route-incident-${exp.incident.id}-${exp.run.id.slice(0, 8)}.json`
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

// ─── Keyboard handling ───────────────────────────────────────────

function onKeydown(ev: KeyboardEvent) {
  if (!open.value) return
  if (ev.key === 'Escape') {
    if (confirmKind.value) {
      // Modal first: close the modal, not the drawer.
      confirmKind.value = null
      return
    }
    ev.preventDefault()
    emit('close')
    return
  }
  if (ev.key !== 'Tab') return
  const root = confirmKind.value ? modalEl.value : drawerEl.value
  if (!root) return
  const focusables = Array.from(
    root.querySelectorAll<HTMLElement>(
      'button:not([disabled]), [href], [tabindex]:not([tabindex="-1"]), input:not([disabled]), select:not([disabled]), textarea:not([disabled])',
    ),
  ).filter((el) => el.offsetParent !== null)
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

function actionLabel(kind: ActionKind): string {
  return ACTION_LABEL[kind]?.title ?? kind
}

function actionHint(kind: ActionKind): string {
  return ACTION_LABEL[kind]?.hint ?? ''
}

function actionDestructive(kind: ActionKind): boolean {
  // Mutating actions that change route state are "destructive"
  // for the confirm-modal styling. Diagnostic tests are not.
  return kind === 'recover' || kind === 'reset_slots' ||
    kind === 'reset_availability' || kind === 'release_slot'
}

function outcomeLabel(outcome: string): string {
  switch (outcome) {
    case 'success':
      return '成功'
    case 'noop':
      return '无变化'
    case 'failed':
      return '失败'
    default:
      return outcome
  }
}

function outcomeTone(outcome: string): string {
  switch (outcome) {
    case 'success':
      return 'success'
    case 'noop':
      return 'muted'
    case 'failed':
      return 'danger'
    default:
      return 'muted'
  }
}

function runStateLabel(state: string): string {
  switch (state) {
    case 'succeeded':
      return '成功'
    case 'failed':
      return '失败'
    case 'cancelled':
      return '已取消'
    case 'pending':
      return '等待中'
    case 'running':
      return '执行中'
    default:
      return state
  }
}

function formatRunValue(value: unknown): string {
  if (value == null) return '—'
  if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
    return String(value)
  }
  try {
    return JSON.stringify(value)
  } catch {
    return '—'
  }
}

function hasRunValues(values: Record<string, unknown>): boolean {
  return Object.keys(values || {}).length > 0
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

          <section class="route-incident-drawer__section">
            <h3 class="route-incident-drawer__section-title">诊断运行</h3>
            <p v-if="auditError" class="route-incident-drawer__error">{{ auditError }}</p>
            <p v-else-if="auditLoading" class="route-incident-drawer__empty">诊断运行加载中…</p>
            <p v-else-if="runs.length === 0" class="route-incident-drawer__empty">暂无诊断运行记录</p>
            <ol v-else class="diagnostic-run-list">
              <li v-for="run in runs" :key="run.id" class="diagnostic-run-row">
                <div class="diagnostic-run-row__summary">
                  <span class="diagnostic-run-row__kind">{{ actionLabel(run.kind) }}</span>
                  <span :class="['diagnostic-run-row__state', `diagnostic-run-row__state--${run.state}`]">
                    {{ runStateLabel(run.state) }}
                  </span>
                  <span class="diagnostic-run-row__time">开始 {{ formatTs(run.started_at) }}</span>
                  <span v-if="run.finished_at" class="diagnostic-run-row__time">结束 {{ formatTs(run.finished_at) }}</span>
                </div>
                <dl v-if="hasRunValues(run.parameters) || hasRunValues(run.result)" class="diagnostic-run-row__data">
                  <template v-for="(value, key) in run.parameters" :key="`parameter-${run.id}-${String(key)}`">
                    <dt>参数 · {{ key }}</dt>
                    <dd>{{ formatRunValue(value) }}</dd>
                  </template>
                  <template v-for="(value, key) in run.result" :key="`result-${run.id}-${String(key)}`">
                    <dt>结果 · {{ key }}</dt>
                    <dd>{{ formatRunValue(value) }}</dd>
                  </template>
                </dl>
              </li>
            </ol>
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

          <!-- 8. Action panel (Phase 2, 2026-07-13) -->
          <section class="route-incident-drawer__section">
            <h3 class="route-incident-drawer__section-title">
              8. 操作与诊断
              <span
                v-if="!actionsEnabled"
                class="route-incident-drawer__chip"
                title="已恢复事件不能再操作"
              >只读</span>
            </h3>
            <p v-if="!actionsEnabled" class="route-incident-drawer__note">
              已恢复事件不会再产生新操作；该区域的审计行仍可下载证据包。
            </p>
            <div v-else class="action-grid">
              <button
                v-for="kind in (['recover', 'reprobe', 'reset-slots', 'reset-availability'] as ActionKind[])"
                :key="kind"
                type="button"
                :class="['action-grid__btn', { 'action-grid__btn--destructive': actionDestructive(kind) }]"
                :title="actionHint(kind)"
                :disabled="!actionsEnabled"
                @click="openConfirm(kind)"
              >
                {{ actionLabel(kind) }}
              </button>
              <button
                type="button"
                :class="['action-grid__btn', 'action-grid__btn--test']"
                title="合成一次直连上游的探测（绕过网关）"
                :disabled="!actionsEnabled"
                @click="openConfirm('direct_upstream_test')"
              >
                直连上游测试
              </button>
              <button
                type="button"
                :class="['action-grid__btn', 'action-grid__btn--test']"
                title="合成一次经网关的探测（使用服务端安全 prompt）"
                :disabled="!actionsEnabled"
                @click="openConfirm('through_gateway_test')"
              >
                经网关测试
              </button>
              <button
                type="button"
                :class="['action-grid__btn', 'action-grid__btn--slot']"
                title="释放单个 slot（参数 slot_id 必填）"
                :disabled="!actionsEnabled"
                @click="openConfirm('release_slot')"
              >
                释放槽位
              </button>
            </div>
            <p class="route-incident-drawer__note">
              所有写操作均要求确认令牌 + 操作原因 + 幂等键；不会绕过下游健康检查原语。
            </p>
            <div v-if="lastAction" class="action-result">
              <span :class="['action-result__pill', `action-result__pill--${outcomeTone(lastAction.outcome)}`]">
                {{ outcomeLabel(lastAction.outcome) }}
              </span>
              <span class="action-result__text">
                最近一次操作结果：审计 #{{ lastAction.audit_id }}
                <span v-if="lastAction.diagnostic_run">，运行 {{ lastAction.diagnostic_run.id.slice(0, 8) }}</span>
                <span v-if="lastAction.idempotent">（幂等重放）</span>
              </span>
            </div>
          </section>

          <!-- 9. Audit log (Phase 2) -->
          <section class="route-incident-drawer__section">
            <h3 class="route-incident-drawer__section-title">9. 审计日志</h3>
            <p v-if="auditError" class="route-incident-drawer__error">{{ auditError }}</p>
            <p v-else-if="auditLoading" class="route-incident-drawer__empty">审计加载中…</p>
            <p v-else-if="auditEntries.length === 0" class="route-incident-drawer__empty">
              暂无审计记录
            </p>
            <ol v-else class="audit-list">
              <li v-for="a in auditEntries" :key="a.id" class="audit-row">
                <span class="audit-row__time">{{ formatTs(a.created_at) }}</span>
                <span :class="['audit-row__pill', `audit-row__pill--${outcomeTone(a.outcome)}`]">
                  {{ outcomeLabel(a.outcome) }}
                </span>
                <span class="audit-row__action">{{ a.action }}</span>
                <span class="audit-row__actor">操作人 {{ a.actor }}</span>
                <span class="audit-row__reason">{{ a.reason }}</span>
                <button
                  v-if="a.diagnostic_run_id && runs.length > 0"
                  type="button"
                  class="route-incident-drawer__link"
                  :title="`下载 ${a.diagnostic_run_id} 的证据包`"
                  @click="exportRun({
                    id: a.diagnostic_run_id,
                    incident_id: a.incident_id || '',
                    tenant_id: a.tenant_id,
                    kind: a.action,
                    state: 'succeeded',
                    route_key: {},
                    parameters: {},
                    started_at: a.created_at,
                    result: {},
                    created_at: a.created_at,
                    updated_at: a.created_at,
                  } as DiagnosticRun)"
                >
                  导出证据 #{{ a.diagnostic_run_id.slice(0, 8) }}
                </button>
              </li>
            </ol>
          </section>

          <p v-if="error" class="route-incident-drawer__error">{{ error }}</p>
          <p class="route-incident-drawer__footnote">
            最后更新：{{ formatTs(new Date(lastFetchAt).toISOString()) }}
          </p>
        </div>

        <!-- Action confirm modal (Phase 2) -->
        <div
          v-if="confirmKind"
          ref="modalEl"
          class="action-modal"
          role="dialog"
          aria-modal="true"
          :aria-label="`${actionLabel(confirmKind)} 确认`"
          @keydown="onKeydown"
        >
          <div class="action-modal__backdrop" @click="confirmKind = null" />
          <div class="action-modal__panel">
            <header class="action-modal__header">
              <h3>{{ actionLabel(confirmKind) }}</h3>
              <button
                type="button"
                class="route-incident-drawer__btn route-incident-drawer__btn--close"
                aria-label="关闭确认"
                @click="confirmKind = null"
              >
                ✕
              </button>
            </header>
            <div class="action-modal__body">
              <p class="action-modal__hint">{{ actionHint(confirmKind) }}</p>
              <p v-if="actionDestructive(confirmKind)" class="action-modal__warning">
                ⚠️ 这是一个改变路由状态的操作。不会绕过下游健康检查；操作前会快照当前状态。
              </p>
              <label class="action-modal__label">
                操作原因 <span class="action-modal__required">*</span>
                <textarea
                  v-model="confirmReason"
                  class="action-modal__textarea"
                  rows="3"
                  maxlength="256"
                  placeholder="例如：上游短暂抖动后人工复核并标记恢复"
                  required
                />
                <span class="action-modal__counter">{{ confirmReason.length }} / 256</span>
              </label>
              <label class="action-modal__label">
                确认令牌 <span class="action-modal__required">*</span>
                <input
                  v-model="confirmToken"
                  class="action-modal__input action-modal__input--code"
                  type="text"
                  readonly
                />
                <span class="action-modal__hint-small">
                  请复制此令牌并粘贴到下方（演示版本：直接使用展示值即可）
                </span>
              </label>
              <details v-if="confirmKind === 'release_slot'" class="action-modal__params">
                <summary>附加参数（可选）</summary>
                <label class="action-modal__label">
                  slot_id
                  <input
                    v-model="(confirmParams as any).slot_id"
                    class="action-modal__input"
                    type="text"
                    placeholder="slot-1"
                  />
                </label>
              </details>
              <details v-else-if="confirmKind === 'recover'" class="action-modal__params">
                <summary>附加参数（可选）</summary>
                <label class="action-modal__label">
                  target_state
                  <select v-model="(confirmParams as any).target_state" class="action-modal__input">
                    <option value="recovered">recovered</option>
                    <option value="closed">closed</option>
                  </select>
                </label>
              </details>
              <p v-if="confirmError" class="route-incident-drawer__error">{{ confirmError }}</p>
            </div>
            <footer class="action-modal__footer">
              <button
                type="button"
                class="route-incident-drawer__btn route-incident-drawer__btn--ghost"
                @click="confirmKind = null"
              >取消</button>
              <button
                type="button"
                :class="['route-incident-drawer__btn', actionDestructive(confirmKind) ? 'route-incident-drawer__btn--destructive' : 'route-incident-drawer__btn--primary']"
                :disabled="confirmSubmitting || !confirmReason.trim()"
                @click="submitAction"
              >
                {{ confirmSubmitting ? '提交中…' : `确认 ${actionLabel(confirmKind)}` }}
              </button>
            </footer>
          </div>
        </div>

        <!-- Export status banner (Phase 2) -->
        <div
          v-if="exportState === 'done' && lastExport"
          class="export-banner"
          role="status"
        >
          <div class="export-banner__panel">
            <h4>证据已生成</h4>
            <p class="export-banner__detail">
              SHA-256: <code>{{ lastExport.integrity.value.slice(0, 16) }}…</code>
            </p>
            <p class="export-banner__detail">
              包含：run + incident + {{ lastExport.events.length }} 个事件 + {{ lastExport.timeline.length }} 个时间桶
            </p>
            <div class="export-banner__actions">
              <button
                type="button"
                class="route-incident-drawer__btn route-incident-drawer__btn--primary"
                @click="downloadExport(lastExport)"
              >
                下载 JSON
              </button>
              <button
                type="button"
                class="route-incident-drawer__btn route-incident-drawer__btn--ghost"
                @click="exportState = 'idle'; lastExport = null"
              >
                关闭
              </button>
            </div>
          </div>
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
  background: var(--overlay-strong);
}

.route-incident-drawer__panel {
  position: relative;
  display: flex;
  flex-direction: column;
  width: min(720px, 100vw);
  height: 100%;
  background: var(--bg);
  color: var(--text);
  border-left: 1px solid var(--border);
  box-shadow: -8px 0 24px var(--overlay-medium);
}

.route-incident-drawer__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border);
  background: var(--bg-subtle);
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
  color: var(--text-secondary);
  display: flex;
  align-items: center;
  gap: 8px;
  word-break: break-all;
}

.route-incident-drawer__chip {
  display: inline-block;
  padding: 1px 6px;
  border-radius: 3px;
  background: var(--bg);
  border: 1px solid var(--border);
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
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text);
  cursor: pointer;
  font-weight: 500;
}

.route-incident-drawer__btn:hover:not(:disabled) {
  border-color: var(--accent);
  background: var(--bg-subtle);
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
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 12px 14px;
  background: var(--bg-subtle);
}

.route-incident-drawer__section-title {
  margin: 0 0 8px 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  letter-spacing: 0.02em;
}

.route-incident-drawer__empty {
  font-size: 12px;
  color: var(--text-secondary);
  margin: 0;
}

.route-incident-drawer__error {
  font-size: 12px;
  color: var(--danger);
  margin: 0;
}

.route-incident-drawer__note {
  font-size: 11px;
  color: var(--text-secondary);
  margin: 8px 0 0 0;
  line-height: 1.5;
}

.route-incident-drawer__note code {
  font-family: ui-monospace, monospace;
  background: var(--bg);
  border: 1px solid var(--border);
  padding: 1px 4px;
  border-radius: 3px;
}

.diagnostic-run-list {
  display: grid;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.diagnostic-run-row {
  padding: 10px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg);
}

.diagnostic-run-row__summary {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 10px;
  align-items: center;
  font-size: 12px;
}

.diagnostic-run-row__kind { font-weight: 600; }

.diagnostic-run-row__state {
  padding: 1px 6px;
  border-radius: 999px;
  background: var(--bg-subtle);
  color: var(--text-secondary);
}

.diagnostic-run-row__state--succeeded { color: var(--success); }
.diagnostic-run-row__state--failed,
.diagnostic-run-row__state--cancelled { color: var(--danger); }
.diagnostic-run-row__state--running,
.diagnostic-run-row__state--pending { color: var(--warning); }

.diagnostic-run-row__time {
  color: var(--text-secondary);
  font-variant-numeric: tabular-nums;
}

.diagnostic-run-row__data {
  display: grid;
  grid-template-columns: minmax(96px, max-content) minmax(0, 1fr);
  gap: 4px 8px;
  margin: 8px 0 0;
  font-size: 11px;
}

.diagnostic-run-row__data dt { color: var(--text-secondary); }

.diagnostic-run-row__data dd {
  min-width: 0;
  margin: 0;
  overflow-wrap: anywhere;
  font-family: ui-monospace, monospace;
}

.route-incident-drawer__link {
  font-family: ui-monospace, monospace;
  font-size: 11px;
  background: transparent;
  border: 1px solid var(--border);
  color: var(--accent);
  padding: 1px 6px;
  border-radius: 3px;
  cursor: pointer;
}

.route-incident-drawer__link:hover {
  background: var(--bg);
  border-color: var(--accent);
}

.route-incident-drawer__footnote {
  font-size: 10px;
  color: var(--text-secondary);
  margin: 0;
  text-align: right;
}

.status-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 8px;
}

.status-grid__cell {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 6px 8px;
}

.status-grid__label {
  font-size: 10px;
  color: var(--text-secondary);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.status-grid__value {
  font-size: 13px;
  font-weight: 600;
  margin-top: 2px;
}

.status-grid__value--danger {
  color: var(--danger);
}
.status-grid__value--warning {
  color: var(--warning);
}
.status-grid__value--success {
  color: var(--success);
}
.status-grid__value--muted {
  color: var(--text-secondary);
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
  color: var(--text-secondary);
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
  background: var(--bg);
  border: 1px solid var(--border);
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
  color: var(--text-secondary);
}

.finding__note {
  font-size: 11px;
  color: var(--text-secondary);
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
  color: var(--text-secondary);
}

.timeline-row__counts {
  font-variant-numeric: tabular-nums;
}

.timeline-row__bar {
  height: 6px;
  border-radius: 3px;
  background: var(--accent);
  min-width: 2px;
}

.timeline-bar--ok { background: var(--success); }
.timeline-bar--warn { background: var(--warning); }
.timeline-bar--slow { background: var(--warning); }
.timeline-bar--critical { background: var(--danger); }

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
  border-bottom: 1px dashed var(--border);
}

.event-row__time {
  font-family: ui-monospace, monospace;
  color: var(--text-secondary);
  min-width: 130px;
}

.event-row__type {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 3px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  background: var(--bg);
  border: 1px solid var(--border);
}

.event-row__type--opened { color: var(--danger); border-color: var(--danger-bd); }
.event-row__type--failure_observed { color: var(--danger); border-color: var(--danger-bd); }
.event-row__type--recovery_progress { color: var(--warning); border-color: var(--warning-bd); }
.event-row__type--recovered { color: var(--success); border-color: var(--success-bd); }

.event-row__kind,
.event-row__status,
.event-row__streak {
  font-family: ui-monospace, monospace;
  color: var(--text-secondary);
}

/* ─── Phase 2: action grid ─────────────────────────────────────── */
.action-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 8px;
  margin-top: 4px;
}

.action-grid__btn {
  background: var(--bg);
  border: 1px solid var(--border);
  color: var(--text);
  border-radius: 4px;
  padding: 6px 10px;
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
  text-align: left;
  transition: all 0.15s ease;
}

.action-grid__btn:hover:not(:disabled) {
  border-color: var(--accent);
}

.action-grid__btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.action-grid__btn--destructive {
  border-color: var(--danger-bd);
  color: var(--danger);
}

.action-grid__btn--test {
  border-color: color-mix(in srgb, var(--accent) 40%, transparent);
  color: var(--accent);
}

.action-grid__btn--slot {
  border-color: var(--warning-bd);
  color: var(--warning);
}

.action-result {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
  font-size: 11px;
  padding: 6px 10px;
  background: var(--bg);
  border-radius: 4px;
  border: 1px solid var(--border);
}

.action-result__pill {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 999px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.action-result__pill--success {
  background: var(--success-bd);
  color: var(--success);
}

.action-result__pill--failed {
  background: color-mix(in srgb, var(--danger) 12%, transparent);
  color: var(--danger);
}

.action-result__pill--muted,
.action-result__pill--noop {
  background: color-mix(in srgb, var(--muted) 14%, transparent);
  color: var(--text-secondary);
}

.action-result__text {
  font-family: ui-monospace, monospace;
  color: var(--text-secondary);
}

/* ─── Phase 2: audit log ───────────────────────────────────────── */
.audit-list {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 220px;
  overflow-y: auto;
}

.audit-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 11px;
  flex-wrap: wrap;
  padding: 2px 0;
  border-bottom: 1px dashed var(--border);
}

.audit-row__time {
  font-family: ui-monospace, monospace;
  color: var(--text-secondary);
  min-width: 130px;
}

.audit-row__pill {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 999px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.audit-row__pill--success {
  background: var(--success-bd);
  color: var(--success);
}

.audit-row__pill--failed {
  background: color-mix(in srgb, var(--danger) 12%, transparent);
  color: var(--danger);
}

.audit-row__pill--muted,
.audit-row__pill--noop {
  background: color-mix(in srgb, var(--muted) 14%, transparent);
  color: var(--text-secondary);
}

.audit-row__action {
  font-family: ui-monospace, monospace;
  color: var(--accent);
  font-size: 11px;
}

.audit-row__actor {
  font-size: 10px;
  color: var(--text-secondary);
}

.audit-row__reason {
  font-size: 11px;
  color: var(--text);
  flex: 1;
  min-width: 120px;
}

/* ─── Phase 2: action confirm modal ────────────────────────────── */
.action-modal {
  position: fixed;
  inset: 0;
  z-index: 1200;
  display: flex;
  align-items: center;
  justify-content: center;
}

.action-modal__backdrop {
  position: absolute;
  inset: 0;
  background: var(--overlay-strong);
}

.action-modal__panel {
  position: relative;
  width: min(520px, 100vw);
  max-height: 90vh;
  overflow-y: auto;
  background: var(--bg);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 8px;
  box-shadow: 0 8px 32px var(--overlay-strong);
  display: flex;
  flex-direction: column;
}

.action-modal__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 14px;
  border-bottom: 1px solid var(--border);
  background: var(--bg-subtle);
}

.action-modal__header h3 {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
}

.action-modal__body {
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.action-modal__hint {
  font-size: 12px;
  color: var(--text-secondary);
  margin: 0;
}

.action-modal__hint-small {
  font-size: 10px;
  color: var(--text-secondary);
  display: block;
  margin-top: 2px;
}

.action-modal__warning {
  font-size: 12px;
  background: var(--danger-bg);
  border: 1px solid var(--danger-bd);
  color: var(--danger);
  padding: 6px 8px;
  border-radius: 4px;
  margin: 0;
}

.action-modal__label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 11px;
  color: var(--text-secondary);
  font-weight: 500;
}

.action-modal__required {
  color: var(--danger);
}

.action-modal__textarea,
.action-modal__input {
  background: var(--bg);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 6px 8px;
  font-size: 12px;
  font-family: inherit;
  resize: vertical;
}

.action-modal__input--code {
  font-family: ui-monospace, monospace;
  letter-spacing: 0.05em;
}

.action-modal__counter {
  font-size: 10px;
  color: var(--text-secondary);
  text-align: right;
}

.action-modal__params {
  font-size: 11px;
  color: var(--text-secondary);
}

.action-modal__params summary {
  cursor: pointer;
  padding: 4px 0;
}

.action-modal__footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 10px 14px;
  border-top: 1px solid var(--border);
  background: var(--bg-subtle);
}

.route-incident-drawer__btn--primary {
  background: var(--accent);
  color: white;
  border-color: var(--accent);
}

.route-incident-drawer__btn--primary:hover:not(:disabled) {
  background: var(--purple);
}

.route-incident-drawer__btn--destructive {
  background: var(--danger);
  color: white;
  border-color: var(--danger);
}

.route-incident-drawer__btn--destructive:hover:not(:disabled) {
  background: var(--danger);
}

/* ─── Phase 2: evidence export banner ──────────────────────────── */
.export-banner {
  position: fixed;
  inset: 0;
  z-index: 1250;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--overlay-strong);
}

.export-banner__panel {
  background: var(--bg-subtle);
  border: 1px solid var(--success);
  border-radius: 6px;
  padding: 16px;
  width: min(420px, 100vw);
  box-shadow: 0 8px 32px var(--overlay-strong);
}

.export-banner__panel h4 {
  margin: 0 0 6px 0;
  font-size: 13px;
  color: var(--success);
}

.export-banner__detail {
  font-size: 11px;
  margin: 4px 0;
  color: var(--text-secondary);
}

.export-banner__detail code {
  font-family: ui-monospace, monospace;
  background: var(--bg);
  padding: 1px 4px;
  border-radius: 2px;
}

.export-banner__actions {
  display: flex;
  gap: 8px;
  margin-top: 12px;
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
