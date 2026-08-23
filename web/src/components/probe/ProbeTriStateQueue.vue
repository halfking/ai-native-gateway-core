<script setup lang="ts">
// ProbeTriStateQueue.vue — 自检 tab 三段式队列（OBS-FE4, 25 号 §6.2 / 26 号 §4）
//
// 三段：待请求（pending）/ 正在请求（in_flight）/ 已完成（completed，保留
// 最近 200 条，与后端 probeCompletedWindow 对齐）。新数据从右侧进入
// （TransitionGroup，只用 transform/opacity）。
//
// 数据通道：
//   - SSE /api/admin/probe/stream（probeStreamStore 单例）为主——实时增量。
//   - GET /api/admin/probe/tasks?status=…（tri-state API）挂载时拉三段做
//     补全/兜底，并周期性对账；API 失败 → SSE-only 降级并显示降级提示。
//   - SSE 断线时展示重连中状态（store 的 connection）。
//
// 可见性门控（沿用 probeStreamStore 模式）：document.hidden 时不渲染
// （v-if 卸下列表）、不轮询、不推进 in_flight 计时；恢复可见时立即刷新。
//
// 颜色只用 var(--kx-*)；未实现字段（next_retry_at_ms 等）不渲染、不零值冒充。
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import {
  fetchProbeTriStateTasks,
  type ProbeTriStateTask,
  type ProbeTriStateStatus,
  type ProbeOutcome,
} from '../../api-selfcheck'
import { acquireProbeStream, useProbeStream, type ProbeOrigin } from '../../composables/probeStreamStore'
import { credentialDisplayName, loadCredentialLabels } from '../../composables/useCredentialLabels'

// 前端 cap 与后端 probeCompletedWindow (=200) 同步。
const MAX_COMPLETED = 200
const POLL_MS = 15_000

type CardStatus = 'pending' | 'in_flight' | 'completed'

interface ProbeCard {
  /** 渲染 key：API 行用行 id（同 dedup_key 可同时存在 completed 行 + pending 重臂行） */
  key: string
  /** dedup_key：SSE 增量匹配 + 失败卡退避下一跳匹配 */
  dedup: string
  source: 'api' | 'sse'
  credential_id: number
  provider_id?: number
  /** 供应商显示名（2026-08-20）— 自检 tab 卡片展示 供应商+凭据 用 */
  provider_name?: string
  provider_code?: string
  raw_model: string
  command?: string
  origin: ProbeOrigin
  status: CardStatus
  outcome?: ProbeOutcome
  attempt: number
  max_attempts: number
  priority: number
  /** 仅 pending 重臂行携带（后端 omitempty），absent = 无 */
  next_retry_at_ms?: number
  reason_code?: string
  http_status?: number
  latency_ms?: number
  created_at?: string
  updated_at?: string
  finished_at?: string
  /** in_flight 实时计时基点（unix ms） */
  started_ms?: number
}

// ── SSE 单例订阅 ─────────────────────────────────────────
const { tiles, connection } = useProbeStream()
let releaseStream: (() => void) | null = null

// ── 本地状态 ─────────────────────────────────────────────
const cards = ref(new Map<string, ProbeCard>())
const firstLoading = ref(true)
/** tri-state API 拉取失败 → SSE-only 降级 */
const apiDegraded = ref(false)
const pageHidden = ref(typeof document !== 'undefined' ? document.hidden : false)
const expandedKeys = ref(new Set<string>())
/** in_flight 实时耗时的“当前时刻”，仅页面可见时推进 */
const nowMs = ref(Date.now())

let pollTimer: number | undefined
let tickTimer: number | undefined

// ── 工具 ─────────────────────────────────────────────────

function originFromSource(source?: string, reason?: string): ProbeOrigin {
  if (source === 'request_failure' || source === 'no_candidates' || reason === 'request_failure' || reason === 'no_candidates') return 'error'
  if (source === 'admin' || source === 'external_async') return 'manual'
  return 'scheduled'
}

const ORIGIN_LABEL: Record<ProbeOrigin, string> = {
  scheduled: '定期',
  error: '错误触发',
  manual: '手动',
}

const OUTCOME_LABEL: Record<ProbeOutcome, string> = {
  success: '成功',
  failed: '失败',
  expired: '过期',
  cancelled: '取消',
}

function fmtClock(ms?: number): string {
  if (!ms) return '—'
  return new Date(ms).toLocaleTimeString('zh-CN', { hour12: false })
}

function fmtMs(v?: number): string {
  if (v === undefined || v === null) return '—'
  if (v >= 1000) return (v / 1000).toFixed(2) + 's'
  return v + 'ms'
}

function fmtDateTime(iso?: string): string {
  if (!iso) return '—'
  const t = new Date(iso)
  if (Number.isNaN(t.getTime())) return '—'
  return t.toLocaleString('zh-CN', { hour12: false })
}

/** 距目标时刻的相对文案（“45s 后”/“已到”） */
function fmtCountdown(targetMs?: number): string {
  if (!targetMs) return ''
  const delta = targetMs - nowMs.value
  if (delta <= 0) return '已到'
  if (delta < 60_000) return Math.ceil(delta / 1000) + 's 后'
  if (delta < 3600_000) return Math.ceil(delta / 60_000) + 'm 后'
  return Math.ceil(delta / 3600_000) + 'h 后'
}

function sortTs(c: ProbeCard): number {
  if (c.finished_at) return new Date(c.finished_at).getTime() || 0
  if (c.updated_at) return new Date(c.updated_at).getTime() || 0
  return c.started_ms ?? 0
}

// ── 卡片写入 ─────────────────────────────────────────────

function capCompleted(map: Map<string, ProbeCard>) {
  const done = [...map.values()].filter((c) => c.status === 'completed').sort((a, b) => sortTs(b) - sortTs(a))
  if (done.length > MAX_COMPLETED) {
    for (const c of done.slice(MAX_COMPLETED)) map.delete(c.key)
  }
}

function upsertCard(next: ProbeCard) {
  // SSE 增量按 dedup 命中已有卡（含 API 行）就地折叠；否则新卡从右侧进入。
  let targetKey = next.key
  if (next.source === 'sse') {
    for (const [k, c] of cards.value) {
      if (c.dedup === next.dedup) { targetKey = k; break }
    }
  }
  const prev = cards.value.get(targetKey)
  if (prev) {
    // 生命周期更新：状态类字段以增量为准（undefined = 本跳无值，不保留旧值
    // 冒充，如 next_retry_at_ms 仅 pending 重臂行携带）；身份字段仅在增量
    // 缺失时沿用旧值。
    cards.value.set(targetKey, {
      ...next,
      key: targetKey,
      credential_id: next.credential_id || prev.credential_id,
      provider_id: next.provider_id ?? prev.provider_id,
      // SSE deltas carry provider_name only when the publisher looked it up.
      // Preserve the prior card's value when the delta omits it so a later
      // completed/failed transition doesn't blank the cached supplier name.
      provider_name: next.provider_name ?? prev?.provider_name,
      provider_code: next.provider_code ?? prev?.provider_code,
      raw_model: next.raw_model || prev.raw_model,
      command: next.command ?? prev.command,
      origin: next.origin ?? prev.origin,
      attempt: next.attempt || prev.attempt,
      max_attempts: next.max_attempts || prev.max_attempts,
      priority: next.priority || prev.priority,
      created_at: next.created_at ?? prev.created_at,
    })
  } else {
    cards.value.set(targetKey, next)
  }
  capCompleted(cards.value)
}

function taskToCard(t: ProbeTriStateTask): ProbeCard {
  return {
    key: `api-${t.id}`,
    dedup: t.dedup_key || `api-${t.id}`,
    source: 'api',
    credential_id: t.credential_id,
    provider_id: t.provider_id,
    provider_name: t.provider_name,
    provider_code: t.provider_code,
    raw_model: t.raw_model,
    command: t.command,
    origin: t.origin ?? originFromSource(t.source),
    status: t.status,
    outcome: t.outcome,
    attempt: t.attempt,
    max_attempts: t.max_attempts,
    priority: t.priority,
    next_retry_at_ms: t.next_retry_at_ms,
    reason_code: t.reason_code,
    http_status: t.http_status,
    latency_ms: t.latency_ms,
    created_at: t.created_at,
    updated_at: t.updated_at,
    finished_at: t.finished_at,
  }
}

function tileToCard(tile: (typeof tiles.value)[number]): ProbeCard | null {
  if (!tile?.id) return null
  let status: CardStatus
  let outcome: ProbeOutcome | undefined
  switch (tile.status) {
    case 'pending':
      status = 'pending'
      break
    case 'in-flight':
      status = 'in_flight'
      break
    case 'ok':
      status = 'completed'
      outcome = 'success'
      break
    case 'fail':
      status = 'completed'
      outcome = 'failed'
      break
    default:
      return null
  }
  return {
    key: tile.id,
    dedup: tile.id,
    source: 'sse',
    credential_id: tile.credential_id,
    provider_id: tile.provider_id,
    provider_name: tile.provider_name,
    provider_code: tile.provider_code,
    raw_model: tile.raw_model ?? '',
    origin: tile.origin ?? originFromSource(tile.source, tile.reason),
    status,
    outcome,
    attempt: tile.attempt ?? 1,
    max_attempts: 7,
    priority: 0,
    // next_retry_at_ms 仅 pending 重臂行携带；undefined 原样透传，不冒充。
    next_retry_at_ms: status === 'pending' ? tile.next_retry_at_ms : undefined,
    reason_code: tile.err_code,
    http_status: tile.http_status,
    latency_ms: tile.latency_ms,
    started_ms: status === 'in_flight' ? (tile.ts || Date.now()) : undefined,
    finished_at: status === 'completed' && tile.ts ? new Date(tile.ts).toISOString() : undefined,
    updated_at: tile.ts ? new Date(tile.ts).toISOString() : undefined,
  }
}

// ── 数据加载 ─────────────────────────────────────────────

async function refreshFromApi() {
  try {
    const [pending, inFlight, completed] = await Promise.all([
      fetchProbeTriStateTasks('pending', 50),
      fetchProbeTriStateTasks('in_flight', 50),
      fetchProbeTriStateTasks('completed', MAX_COMPLETED),
    ])
    apiDegraded.value = false
    // 三段整体替换为权威快照，同时保留 SSE 先行看到、API 尚未落库的增量
    // （按 dedup 判断；同 dedup 的 API 行已覆盖对应卡片）。
    const next = new Map<string, ProbeCard>()
    const nextDedups = new Set<string>()
    for (const t of [...pending.tasks, ...inFlight.tasks, ...completed.tasks]) {
      const card = taskToCard(t)
      next.set(card.key, card)
      nextDedups.add(card.dedup)
    }
    for (const c of cards.value.values()) {
      if (c.source === 'sse' && !nextDedups.has(c.dedup) && c.status !== 'completed') {
        next.set(c.key, c)
      }
    }
    cards.value = next
    capCompleted(cards.value)
  } catch {
    // API 失败 → SSE-only 降级（已有 tiles 继续渲染），显示降级提示。
    apiDegraded.value = true
  } finally {
    firstLoading.value = false
  }
}

function startPoll() {
  stopPoll()
  pollTimer = window.setInterval(() => {
    // 页面不可见：不轮询（沿用 probeStreamStore 的可见性门控）。
    if (typeof document !== 'undefined' && document.hidden) return
    void refreshFromApi()
  }, POLL_MS)
}

function stopPoll() {
  if (pollTimer) clearInterval(pollTimer)
  pollTimer = undefined
}

function startTick() {
  stopTick()
  tickTimer = window.setInterval(() => {
    // 页面不可见：不推进 in_flight 计时（避免隐藏页定时器风暴）。
    if (typeof document !== 'undefined' && document.hidden) return
    nowMs.value = Date.now()
  }, 1000)
}

function stopTick() {
  if (tickTimer) clearInterval(tickTimer)
  tickTimer = undefined
}

function onVisibilityChange() {
  const hidden = typeof document !== 'undefined' ? document.hidden : false
  pageHidden.value = hidden
  if (!hidden) {
    // 恢复可见：立即刷新三段（store 侧 SSE 已自行重连重放）。
    nowMs.value = Date.now()
    void refreshFromApi()
  }
}

onMounted(() => {
  void loadCredentialLabels()
  void refreshFromApi()
  startPoll()
  startTick()
  document.addEventListener('visibilitychange', onVisibilityChange)
  releaseStream = acquireProbeStream()
})

onUnmounted(() => {
  stopPoll()
  stopTick()
  document.removeEventListener('visibilitychange', onVisibilityChange)
  if (releaseStream) { releaseStream(); releaseStream = null }
  expandedKeys.value.clear()
})

// SSE 增量：单例 store 已做可见性门控（隐藏页不写入 tiles），这里直接消费。
// deep: store 的 collapseTile 通过索引赋值/push 原地更新数组，浅 watch 会漏。
watch(tiles, (list) => {
  for (const tile of list) {
    const card = tileToCard(tile)
    if (card) upsertCard(card)
  }
}, { deep: true })

// ── 视图投影 ─────────────────────────────────────────────

const pendingCards = computed(() =>
  [...cards.value.values()]
    .filter((c) => c.status === 'pending')
    .sort((a, b) => (b.priority - a.priority) || ((a.next_retry_at_ms ?? Number.MAX_SAFE_INTEGER) - (b.next_retry_at_ms ?? Number.MAX_SAFE_INTEGER)))
)

const inFlightCards = computed(() =>
  [...cards.value.values()]
    .filter((c) => c.status === 'in_flight')
    .sort((a, b) => (a.started_ms ?? sortTs(a)) - (b.started_ms ?? sortTs(b)))
)

const completedCards = computed(() =>
  [...cards.value.values()]
    .filter((c) => c.status === 'completed')
    .sort((a, b) => sortTs(b) - sortTs(a))
)

/** 失败卡的退避下一跳：同 dedup_key 的 pending 重臂行 next_retry_at_ms */
const backoffByDedup = computed(() => {
  const m = new Map<string, number>()
  for (const c of cards.value.values()) {
    if (c.status === 'pending' && c.next_retry_at_ms) m.set(c.dedup, c.next_retry_at_ms)
  }
  return m
})

function elapsedOf(c: ProbeCard): string {
  const base = c.started_ms ?? (c.updated_at ? new Date(c.updated_at).getTime() : NaN)
  if (!base || Number.isNaN(base)) return '—'
  const ms = Math.max(0, nowMs.value - base)
  if (ms < 60_000) return (ms / 1000).toFixed(1) + 's'
  return Math.floor(ms / 60_000) + 'm' + Math.floor((ms % 60_000) / 1000) + 's'
}

function toggleExpand(key: string) {
  const s = expandedKeys.value
  if (s.has(key)) s.delete(key)
  else s.add(key)
}

const sseReconnecting = computed(() => connection.value === 'connecting' || connection.value === 'reconnecting')

function outcomeClass(o?: ProbeOutcome): string {
  switch (o) {
    case 'success': return 'is-success'
    case 'failed': return 'is-danger'
    case 'expired': return 'is-warning'
    default: return 'is-muted'
  }
}

function originClass(o: ProbeOrigin): string {
  switch (o) {
    case 'error': return 'is-danger'
    case 'manual': return 'is-warning'
    default: return 'is-primary'
  }
}

// credentialLabel 渲染供应商 + 凭据名称；名称未加载或凭据已删除时，
// credentialDisplayName 保留“凭据 #ID”作为可追溯 fallback。
function credentialLabel(c: ProbeCard): string {
  const cred = credentialDisplayName(c.credential_id)
  if (c.provider_name) return `${c.provider_name} · ${cred}`
  if (c.provider_code) return `${c.provider_code} · ${cred}`
  return cred
}

// credentialTitle 在悬停 tooltip 里给出更完整的供应商 + 凭据 + 模型信息。
function credentialTitle(c: ProbeCard): string {
  const parts: string[] = []
  if (c.provider_name) parts.push(c.provider_name)
  if (c.provider_code && c.provider_code !== c.provider_name) parts.push(`(${c.provider_code})`)
  parts.push(credentialDisplayName(c.credential_id))
  if (c.raw_model) parts.push(c.raw_model)
  return parts.join(' · ')
}
</script>

<template>
  <div class="probe-tri-queue" data-testid="probe-tri-queue">
    <!-- 状态条：SSE 连接态 + API 降级提示 -->
    <div class="tri-status-row">
      <span
        class="tri-chip"
        :class="sseReconnecting ? 'is-warning' : 'is-success'"
        data-testid="sse-state"
      >
        {{ sseReconnecting ? 'SSE 重连中…' : '实时连接正常' }}
      </span>
      <span v-if="apiDegraded" class="tri-chip is-danger" data-testid="api-degraded">
        队列 API 拉取失败，正在以 SSE 实时数据降级展示
      </span>
    </div>

    <!-- 页面不可见：不渲染（沿用 probeStreamStore 可见性门控模式） -->
    <template v-if="!pageHidden">
      <!-- 首次加载 Skeleton -->
      <div v-if="firstLoading && cards.size === 0" class="tri-skeleton" data-testid="tri-skeleton">
        <div v-for="i in 3" :key="i" class="tri-skeleton-card"></div>
      </div>

      <template v-else>
        <!-- ① 待请求 pending -->
        <section class="tri-section" data-testid="tri-pending">
          <header class="tri-section-head">
            <h4 class="tri-section-title">待请求</h4>
            <span class="tri-count">{{ pendingCards.length }}</span>
          </header>
          <div v-if="pendingCards.length" class="tri-cards tri-cards--small">
            <TransitionGroup name="probe-in">
              <div
                v-for="c in pendingCards"
                :key="c.key"
                class="probe-card probe-card--pending"
                data-testid="probe-pending-card"
              >
                <div class="probe-card__target" :title="credentialTitle(c)">
                  <span class="probe-card__cred">{{ credentialLabel(c) }}</span>
                  <span class="probe-card__model">{{ c.raw_model || '—' }}</span>
                </div>
                <div class="probe-card__meta">
                  <span class="tri-badge" :class="originClass(c.origin)" data-testid="origin-badge">
                    {{ ORIGIN_LABEL[c.origin] }}
                  </span>
                  <span class="probe-card__attempt">{{ c.attempt }}/{{ c.max_attempts }}</span>
                </div>
                <div v-if="c.next_retry_at_ms" class="probe-card__meta probe-card__eta">
                  预计 {{ fmtClock(c.next_retry_at_ms) }}（{{ fmtCountdown(c.next_retry_at_ms) }}）
                </div>
              </div>
            </TransitionGroup>
          </div>
          <div v-else class="tri-empty" data-testid="tri-pending-empty">暂无待请求任务</div>
        </section>

        <!-- ② 正在请求 in_flight -->
        <section class="tri-section" data-testid="tri-inflight">
          <header class="tri-section-head">
            <h4 class="tri-section-title">正在请求</h4>
            <span class="tri-count">{{ inFlightCards.length }}</span>
          </header>
          <div v-if="inFlightCards.length" class="tri-cards tri-cards--small">
            <TransitionGroup name="probe-in">
              <div
                v-for="c in inFlightCards"
                :key="c.key"
                class="probe-card probe-card--inflight"
                data-testid="probe-inflight-card"
              >
                <div class="probe-card__target" :title="credentialTitle(c)">
                  <span class="probe-card__cred">{{ credentialLabel(c) }}</span>
                  <span class="probe-card__model">{{ c.raw_model || '—' }}</span>
                </div>
                <div class="probe-card__meta">
                  <span class="tri-badge" :class="originClass(c.origin)" data-testid="origin-badge">
                    {{ ORIGIN_LABEL[c.origin] }}
                  </span>
                  <span class="probe-card__elapsed" data-testid="inflight-elapsed">{{ elapsedOf(c) }}</span>
                </div>
              </div>
            </TransitionGroup>
          </div>
          <div v-else class="tri-empty" data-testid="tri-inflight-empty">暂无正在请求任务</div>
        </section>

        <!-- ③ 已完成 completed（大形态可展开，保留最近 200 条） -->
        <section class="tri-section" data-testid="tri-completed">
          <header class="tri-section-head">
            <h4 class="tri-section-title">已完成</h4>
            <span class="tri-count">{{ completedCards.length }}<span class="tri-count-cap">/200</span></span>
          </header>
          <div v-if="completedCards.length" class="tri-cards tri-cards--large">
            <TransitionGroup name="probe-in">
              <div
                v-for="c in completedCards"
                :key="c.key"
                class="probe-card probe-card--completed"
                :class="outcomeClass(c.outcome)"
                :data-expanded="expandedKeys.has(c.key) ? 'true' : 'false'"
                data-testid="probe-completed-card"
              >
                <div class="probe-card__summary" @click="toggleExpand(c.key)">
                  <span class="tri-badge" :class="outcomeClass(c.outcome)" data-testid="outcome-badge">
                    {{ c.outcome ? OUTCOME_LABEL[c.outcome] : '完成' }}
                  </span>
                  <span class="tri-badge" :class="originClass(c.origin)" data-testid="origin-badge">
                    {{ ORIGIN_LABEL[c.origin] }}
                  </span>
                  <span class="probe-card__cred" :title="credentialTitle(c)">{{ credentialLabel(c) }}</span>
                  <span class="probe-card__model">{{ c.raw_model || '—' }}</span>
                  <span class="probe-card__latency">{{ fmtMs(c.latency_ms) }}</span>
                  <span class="probe-card__expand">{{ expandedKeys.has(c.key) ? '▾' : '▸' }}</span>
                </div>
                <div v-if="expandedKeys.has(c.key)" class="probe-card__detail" data-testid="completed-detail">
                  <div class="probe-detail-row"><span class="probe-detail-label">结果</span><span>{{ c.outcome ? OUTCOME_LABEL[c.outcome] : '—' }}</span></div>
                  <div class="probe-detail-row"><span class="probe-detail-label">延迟</span><span>{{ fmtMs(c.latency_ms) }}</span></div>
                  <div class="probe-detail-row"><span class="probe-detail-label">HTTP</span><span>{{ c.http_status ?? '—' }}</span></div>
                  <div class="probe-detail-row"><span class="probe-detail-label">错误码</span><span>{{ c.reason_code || '—' }}</span></div>
                  <div class="probe-detail-row"><span class="probe-detail-label">观察时间</span><span>{{ fmtDateTime(c.finished_at) }}</span></div>
                  <div class="probe-detail-row"><span class="probe-detail-label">尝试</span><span>{{ c.attempt }}/{{ c.max_attempts }}</span></div>
                  <div class="probe-detail-row"><span class="probe-detail-label">命令</span><span>{{ c.command || '—' }}</span></div>
                </div>
                <!-- 失败卡：退避下一跳（同 dedup_key 的 pending 重臂行） -->
                <div
                  v-if="c.outcome === 'failed' && backoffByDedup.has(c.dedup)"
                  class="probe-card__backoff"
                  data-testid="backoff-next-hop"
                >
                  退避下一跳 {{ fmtClock(backoffByDedup.get(c.dedup)) }}（{{ fmtCountdown(backoffByDedup.get(c.dedup)) }}）
                </div>
              </div>
            </TransitionGroup>
          </div>
          <div v-else class="tri-empty" data-testid="tri-completed-empty">暂无已完成任务</div>
        </section>
      </template>
    </template>
  </div>
</template>

<style scoped>
/* 颜色只用 var(--kx-*) 色板 token；动画只用 transform/opacity。 */
.probe-tri-queue {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.tri-status-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.tri-chip {
  display: inline-flex;
  align-items: center;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 12px;
  border: 1px solid transparent;
}

.tri-chip.is-success {
  color: var(--kx-success);
  background: var(--kx-success-soft);
}

.tri-chip.is-warning {
  color: var(--kx-warning);
  background: var(--kx-warning-soft);
}

.tri-chip.is-danger {
  color: var(--kx-danger);
  background: var(--kx-danger-soft);
}

.tri-section {
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  background: var(--kx-surface);
  padding: 10px 12px;
}

.tri-section-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 8px;
}

.tri-section-title {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--kx-text);
}

.tri-count {
  font-size: 12px;
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
}

.tri-count-cap {
  color: var(--kx-muted);
  opacity: 0.8;
}

.tri-cards {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  position: relative;
}

.tri-cards--small .probe-card {
  width: 260px;
}

.tri-cards--large .probe-card {
  width: 100%;
}

.probe-card {
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: var(--kx-surface);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--kx-text);
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.probe-card--inflight {
  border-color: var(--kx-primary);
}

.probe-card--completed.is-success {
  border-color: var(--kx-success);
}

.probe-card--completed.is-danger {
  border-color: var(--kx-danger);
}

.probe-card--completed.is-warning {
  border-color: var(--kx-warning);
}

.probe-card__target {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.probe-card__cred {
  color: var(--kx-muted);
  white-space: nowrap;
}

.probe-card__model {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.probe-card__meta {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.probe-card__eta,
.probe-card__elapsed {
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
}

.probe-card__elapsed {
  color: var(--kx-primary);
  font-weight: 600;
}

.probe-card__summary {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  min-width: 0;
  flex-wrap: wrap;
}

.probe-card__latency {
  margin-left: auto;
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
}

.probe-card__expand {
  color: var(--kx-muted);
}

.probe-card__detail {
  border-top: 1px solid var(--kx-border);
  padding-top: 6px;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 4px 16px;
}

.probe-detail-row {
  display: flex;
  gap: 8px;
  font-size: 12px;
}

.probe-detail-label {
  color: var(--kx-muted);
  min-width: 3.5em;
}

.probe-card__backoff {
  border-top: 1px dashed var(--kx-danger);
  color: var(--kx-danger);
  padding-top: 6px;
  font-variant-numeric: tabular-nums;
}

.tri-badge {
  display: inline-flex;
  align-items: center;
  padding: 1px 8px;
  border-radius: 4px;
  font-size: 11px;
  white-space: nowrap;
}

.tri-badge.is-primary {
  color: var(--kx-primary);
  background: var(--kx-primary-soft);
}

.tri-badge.is-success {
  color: var(--kx-success);
  background: var(--kx-success-soft);
}

.tri-badge.is-warning {
  color: var(--kx-warning);
  background: var(--kx-warning-soft);
}

.tri-badge.is-danger {
  color: var(--kx-danger);
  background: var(--kx-danger-soft);
}

.tri-badge.is-muted {
  color: var(--kx-muted);
  background: var(--kx-bg-accent);
}

.tri-empty {
  padding: 12px;
  text-align: center;
  color: var(--kx-muted);
  font-size: 12px;
}

.tri-skeleton {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.tri-skeleton-card {
  height: 64px;
  border-radius: 8px;
  background: var(--kx-bg-accent);
  animation: tri-skeleton-pulse 1.2s ease-in-out infinite;
}

@keyframes tri-skeleton-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.55; }
}

/* 新数据从右侧进入（只用 transform/opacity）。 */
.probe-in-enter-active {
  transition: transform 0.3s ease, opacity 0.3s ease;
}

.probe-in-enter-from {
  transform: translateX(32px);
  opacity: 0;
}

.probe-in-leave-active {
  transition: transform 0.2s ease, opacity 0.2s ease;
  position: absolute;
}

.probe-in-leave-to {
  transform: translateX(-16px);
  opacity: 0;
}

.probe-in-move {
  transition: transform 0.3s ease;
}
</style>
