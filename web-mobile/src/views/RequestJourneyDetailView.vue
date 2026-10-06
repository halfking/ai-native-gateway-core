<script setup lang="ts">
// RequestJourneyDetailView — 单条请求的完整事件流（/journey/:id）。
//
// 数据源：GET /api/admin/request-journeys/{request_id}（AdminMiddleware）
//
// ★★ 本页的核心是**三态**，不是「有/无」：
//   ok                     有链路
//   observation_degraded   观测面降级，看不到 —— **不是查不到**
//   not_found              真的查不到
//   后端对第 3 种情况的契约（request_journey.go:265-268）是：
//     journey == nil && observation_status != observation_degraded ⇒ 才 404
//   ⇒ 「降级 + 无数据」返回的是 200 + 没有 journey 字段。
//   若把后两种合成一个「没找到」，我们就在对用户断言一个没有依据的结论。

import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchJourneyDetail,
  classifyJourneyDetail,
  type JourneyEvent,
  type JourneyDetailState,
  type JourneyDivergence,
} from '@/api/requestJourney'

const route = useRoute()

useHyperPage({ title: () => t('journey.detailTitle') })

const state = ref<JourneyDetailState | { kind: 'loading' }>({ kind: 'loading' })
const loadError = ref<string | null>(null)

const requestId = computed(() => {
  const raw = route.params.id
  const s = Array.isArray(raw) ? raw[0] : raw
  return typeof s === 'string' ? s : ''
})

async function load(): Promise<void> {
  const id = requestId.value
  if (!id) {
    state.value = { kind: 'not_found' }
    return
  }
  state.value = { kind: 'loading' }
  loadError.value = null
  try {
    const resp = await fetchJourneyDetail(id)
    state.value = classifyJourneyDetail(resp)
  } catch (err) {
    // 404 在本端点是**唯一**的「真查不到」信号，其余（网络/500）都归到这里，
    // 不能混成同一个提示 —— 否则用户会去查网络而问题在服务端。
    const status = (err as { status?: number })?.status
    if (status === 404) state.value = { kind: 'not_found' }
    else state.value = { kind: 'loading' }
    loadError.value = (err as Error)?.message ?? null
  }
}

watch(requestId, () => { void load() }, { immediate: true })

const journey = computed(() => (state.value.kind === 'ok' ? state.value.journey : null))
const degradedWithNoData = computed(() => state.value.kind === 'observation_degraded')
const events = computed<JourneyEvent[]>(() => journey.value?.events ?? [])

const DIVERGENCE_KEY: Record<JourneyDivergence, string> = {
  content_conflict: 'journey.divContentConflict',
  redis_divergence: 'journey.divRedisDivergence',
  postgres_gap: 'journey.divPostgresGap',
}

function eventTone(e: JourneyEvent): 'success' | 'warning' | 'danger' | 'muted' {
  if (e.error_kind || e.outcome === 'failure' || e.outcome === 'error') return 'danger'
  if (e.outcome === 'retry' || e.switch_reason) return 'warning'
  return 'muted'
}

function switchText(e: JourneyEvent): string | null {
  if (e.switch_reason) return e.switch_reason
  if (e.from_credential_id != null && e.to_credential_id != null && e.from_credential_id !== e.to_credential_id) {
    return `${e.from_credential_id} → ${e.to_credential_id}`
  }
  return null
}

/** 事件摘要行：一句话说清这一跳干了什么。 */
function eventSummary(e: JourneyEvent): string {
  const parts: string[] = [e.event_type]
  if (e.stage) parts.push(e.stage)
  const m = e.model || e.resolved_model || e.to_model
  if (m) parts.push(m)
  if (e.provider) parts.push(e.provider)
  if (typeof e.http_status === 'number') parts.push(`HTTP ${e.http_status}`)
  return parts.join(' · ')
}
</script>

<template>
  <div class="view-root jdetail">
    <p v-if="state.kind === 'loading'" class="jdetail__msg">{{ t('common.loading') }}</p>

    <!-- ★ 降级且无数据：文案必须与 notFound 不同 -->
    <div v-else-if="degradedWithNoData" class="jdetail__banner jdetail__banner--warn">
      <p class="jdetail__banner-title">{{ t('journey.degradedTitle') }}</p>
      <p class="jdetail__banner-body">{{ t('journey.notFoundDegraded') }}</p>
      <p v-if="loadError" class="jdetail__banner-err">{{ loadError }}</p>
    </div>

    <p v-else-if="state.kind === 'not_found'" class="jdetail__msg">{{ t('journey.notFound') }}</p>

    <template v-else-if="journey">
      <div class="data-card">
        <div class="card-field">
          <span>{{ t('journey.model') }}</span>
          <span class="card-field__value">{{ journey.tenant_id }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('journey.seq') }}</span>
          <span class="card-field__value num">{{ journey.request_id }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('journey.occurredAt') }}</span>
          <span class="card-field__value">{{ relativeTime(journey.started_at) }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('journey.events') }}</span>
          <span class="card-field__value num">{{ events.length }}</span>
        </div>
      </div>

      <!-- 数据分歧本身是排障线索，必须显式列出而不是折叠掉 -->
      <div v-if="state.kind === 'ok' && state.divergence.length > 0" class="jdetail__banner jdetail__banner--warn">
        <p class="jdetail__banner-title">{{ t('journey.divergences') }}</p>
        <ul class="jdetail__div-list">
          <li v-for="(d, i) in state.divergence" :key="i">
            {{ t(DIVERGENCE_KEY[d] ?? 'journey.divPostgresGap') }}
          </li>
        </ul>
      </div>

      <p v-if="state.kind === 'ok' && state.degraded" class="jdetail__banner jdetail__banner--warn">
        {{ t('journey.degraded') }}
      </p>

      <p v-if="events.length === 0" class="jdetail__msg">{{ t('journey.empty') }}</p>

      <ol v-else class="jdetail__timeline">
        <li v-for="(e, i) in events" :key="`${e.seq}-${i}`" class="jdetail__event">
          <div class="jdetail__event-head">
            <span class="jdetail__event-seq">{{ e.seq }}</span>
            <span class="badge" :class="`badge--${eventTone(e) === 'muted' ? 'muted' : eventTone(e)}`">
              {{ e.stage || e.event_type }}
            </span>
            <span class="jdetail__event-time">{{ relativeTime(e.occurred_at) }}</span>
          </div>
          <p class="jdetail__event-summary">{{ eventSummary(e) }}</p>
          <p v-if="switchText(e)" class="jdetail__event-note">
            {{ t('journey.switchReason') }}: {{ switchText(e) }}
          </p>
          <p v-if="e.retry_reason" class="jdetail__event-note">
            {{ t('journey.retryReason') }}: {{ e.retry_reason }}
          </p>
          <p v-if="e.error_kind" class="jdetail__event-err">{{ t('journey.errorKind') }}: {{ e.error_kind }}</p>
          <p v-if="e.attempt" class="jdetail__event-note">
            {{ t('journey.attempts') }} #{{ e.attempt.attempt_no }}
            <template v-if="e.attempt.provider"> · {{ e.attempt.provider }}</template>
          </p>
        </li>
      </ol>
    </template>
  </div>
</template>

<style scoped>
.jdetail {
  padding: var(--app-space-3);
}
.jdetail__msg {
  padding: var(--app-space-4) 0;
  color: var(--app-text-muted);
  font-size: 14px;
  text-align: center;
}
.jdetail__banner {
  margin: var(--app-space-2) 0;
  padding: var(--app-space-3);
  border-radius: var(--app-radius);
  background: var(--app-surface-muted);
  color: var(--app-text-secondary);
  font-size: 13px;
  line-height: 1.6;
}
.jdetail__banner--warn {
  background: var(--app-warning-soft);
  color: var(--app-warning);
}
.jdetail__banner-title {
  font-weight: 600;
  margin-bottom: 4px;
}
.jdetail__banner-err {
  margin-top: 6px;
  font-size: 12px;
  opacity: 0.85;
  word-break: break-all;
}
.jdetail__div-list {
  margin: 4px 0 0;
  padding-left: 18px;
}
.jdetail__timeline {
  list-style: none;
  margin: var(--app-space-3) 0 0;
  padding: 0;
}
.jdetail__event {
  position: relative;
  padding: var(--app-space-2) 0 var(--app-space-2) var(--app-space-4);
  border-left: 2px solid var(--app-border);
  margin-left: 6px;
}
.jdetail__event-head {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  flex-wrap: wrap;
}
.jdetail__event-seq {
  font-size: 12px;
  color: var(--app-text-muted);
  font-variant-numeric: tabular-nums;
  min-width: 22px;
}
.jdetail__event-time {
  font-size: 12px;
  color: var(--app-text-muted);
  margin-left: auto;
}
.jdetail__event-summary {
  margin: 4px 0 0;
  font-size: 14px;
  color: var(--app-text);
  word-break: break-word;
}
.jdetail__event-note {
  margin: 3px 0 0;
  font-size: 12px;
  color: var(--app-text-secondary);
}
.jdetail__event-err {
  margin: 3px 0 0;
  font-size: 12px;
  color: var(--app-danger);
}
</style>
