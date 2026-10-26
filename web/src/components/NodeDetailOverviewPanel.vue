<script setup lang="ts">
import { watch } from 'vue'
import type { RoutingCandidate } from '../api/routing'
import type {
  CredentialModelStatus,
  CredentialRoutingDecision,
  ModelHistoryEvent,
  WindowStats,
} from '../api/credential-monitor'
import type { LiveNodeStatus } from '../composables/liveStreamStore'
import { fmtTime, pct, statusClass } from '../utils/nodeDetailFormat'
import { useCredentialLabels } from '../composables/useCredentialLabels'
import { useI18n } from 'vue-i18n'
import NodeDetailAccessErrorsPanel from './NodeDetailAccessErrorsPanel.vue'

const { t } = useI18n()

const props = defineProps<{
  node: LiveNodeStatus
  detailLoading: boolean
  detailLoaded: boolean
  candidate: RoutingCandidate | null
  candidateLoading: boolean
  models: CredentialModelStatus[]
  selectedModel: string
  windowEntries: Array<{ rid: string; ts: number; ok: boolean; lat: number; err?: string }>
  windowStats: WindowStats | null
  windowSource: string
  errorKinds: Array<[string, number]>
  history: ModelHistoryEvent[]
  failedWindowEntries: Array<{ rid: string; ts: number; ok: boolean; lat: number; err?: string }>
  failedDecisions: CredentialRoutingDecision[]
}>()

const emit = defineEmits<{
  chooseModel: [model: string]
  openRequest: [requestId: string]
}>()

// credentialDisplayName resolves credential id → human label; the composable
// keeps a Map<id,label> refreshed via loadCredentialLabels(), and falls back
// to "凭据 #ID" if the label is missing.
const { credentialDisplayName, loadCredentialLabels } = useCredentialLabels()

// Refresh the label cache whenever the candidate changes so the overview
// shows the current label (best-effort, no-op when fresh within TTL).
watch(() => props.candidate?.credential_id, (id) => {
  if (id) void loadCredentialLabels()
})
</script>

<template>
  <section class="nd-section">
    <h3>实时状态</h3>
    <dl class="nd-grid">
      <div><dt>{{ $t('common.terms.nodeState.circuitOpen') }}</dt><dd :class="statusClass(node.circuit_state)">{{ node.circuit_state || $t('common.terms.unknown') }}</dd></div>
      <div><dt>{{ $t('common.terms.availability') }}</dt><dd :class="statusClass(node.availability_state)">{{ node.availability_state || $t('common.terms.unknown') }}</dd></div>
      <div><dt>{{ $t('common.terms.quota') }}</dt><dd :class="statusClass(node.quota_state)">{{ node.quota_state || $t('common.terms.unknown') }}</dd></div>
      <div><dt>{{ $t('common.terms.health') }}</dt><dd :class="statusClass(node.health_status)">{{ node.health_status || $t('common.terms.unknown') }}</dd></div>
      <div><dt>{{ $t('common.terms.nodeState.manualDisabled') }}</dt><dd>{{ node.manual_disabled ? $t('common.button.yes') : $t('common.button.no') }}</dd></div>
      <div><dt>最近错误</dt><dd class="nd-wrap">{{ node.last_error || '—' }}<small v-if="node.last_error_at"> · {{ fmtTime(node.last_error_at) }}</small></dd></div>
    </dl>
  </section>

  <div v-if="detailLoading && !detailLoaded" class="nd-seg-loading" role="status">明细与近期情况加载中…</div>

  <template v-else-if="detailLoaded">
    <section v-if="candidate" class="nd-section">
      <h3>{{ $t('common.terms.routingCandidates') }} <small v-if="candidateLoading">{{ $t('common.feedback.loading') }}</small></h3>
      <dl class="nd-grid">
        <div><dt>{{ $t('common.credentialFallback') }}</dt><dd>{{ credentialDisplayName(candidate.credential_id) }} · {{ candidate.credential_label }}</dd></div>
        <div><dt>Provider</dt><dd>{{ candidate.provider_name }}</dd></div>
        <div><dt>{{ $t('common.terms.routableOrNot') }}</dt><dd :class="candidate.routable ? 'is-ok' : 'is-bad'">{{ candidate.routable ? $t('common.button.yes') : $t('common.button.no') }}</dd></div>
        <div><dt>阻断原因</dt><dd class="nd-wrap">{{ candidate.runtime_block_reason || candidate.block_reason || '—' }}</dd></div>
        <div><dt>生命周期</dt><dd>{{ candidate.lifecycle_status || '—' }}</dd></div>
        <div><dt>配额 / 余额</dt><dd>{{ candidate.quota_state || '—' }} / {{ candidate.balance_usd ?? '—' }} USD</dd></div>
        <div><dt>熔断 / 冷却</dt><dd>{{ candidate.circuit_state || 'closed' }} / {{ fmtTime(candidate.cooling_until) }}</dd></div>
        <div><dt>并发 / 会话</dt><dd>{{ candidate.effective_concurrency ?? '—' }} / {{ candidate.active_sessions ?? 0 }}</dd></div>
        <div><dt>Tier / 权重 / 优先级</dt><dd>T{{ candidate.tier }} / {{ candidate.weight }} / {{ candidate.manual_priority ?? 99 }}</dd></div>
        <div><dt>{{ $t('common.terms.successRate') }} / P95</dt><dd>{{ pct(candidate.success_rate) }} / {{ candidate.p95_latency_ms ? `${candidate.p95_latency_ms}ms` : '—' }}</dd></div>
        <div><dt>{{ $t('common.terms.consecutiveFailures') }}</dt><dd :class="(candidate.credential_consecutive_failures ?? candidate.consecutive_failures ?? 0) ? 'is-warn' : 'is-ok'">{{ candidate.credential_consecutive_failures ?? candidate.consecutive_failures ?? 0 }}</dd></div>
        <div><dt>生效窗口</dt><dd>{{ fmtTime(candidate.effective_at) }} ～ {{ fmtTime(candidate.expires_at) }}</dd></div>
      </dl>
    </section>

    <section v-if="models.length" class="nd-section">
      <h3>模型状态</h3>
      <div class="nd-models">
        <button
          v-for="model in models"
          :key="model.raw_model_name"
          class="nd-model"
          :class="{ active: selectedModel === model.raw_model_name }"
          @click="emit('chooseModel', model.raw_model_name)"
        >
          <strong>{{ model.raw_model_name }}</strong>
          <span :class="statusClass(model.effective_state === 'available' ? 'ready' : model.effective_state)">{{ model.effective_state }}</span>
          <span>{{ pct(model.recent_success_rate) }} · {{ model.p95_latency_ms ?? '—' }}ms</span>
        </button>
      </div>
    </section>

    <section v-if="selectedModel" class="nd-section">
      <h3>{{ $t('common.terms.slidingWindow') }} <small>{{ $t('ops.overview.recentRequestsHint', { source: windowSource ? ` · ${windowSource}` : '' }) }}</small></h3>
      <div v-if="windowEntries.length" class="nd-window" :aria-label="`${selectedModel} 最近调用结果`">
        <button
          v-for="entry in windowEntries.slice(0, 100)"
          :key="`${entry.rid}-${entry.ts}`"
          type="button"
          class="nd-window-cell"
          :class="[entry.ok ? 'ok' : 'bad', entry.rid ? 'is-clickable' : '']"
          :disabled="!entry.rid"
          :title="`${entry.ok ? '成功' : '失败'} · ${entry.lat}ms ${entry.err || ''}${entry.rid ? ' · 点击查看详情' : ''}`"
          @click="emit('openRequest', entry.rid)"
        />
      </div>
      <p v-else class="nd-muted">该模型在窗口内没有调用样本。</p>
      <div v-if="windowStats" class="nd-stat-line">总计 {{ windowStats.total }} · 成功 {{ windowStats.success }} · 失败 {{ windowStats.failed }} · 失败率 {{ (windowStats.failure_rate * 100).toFixed(1) }}%</div>
      <div v-if="errorKinds.length" class="nd-error-kinds"><span v-for="[kind, count] in errorKinds" :key="kind">{{ kind }} × {{ count }}</span></div>
    </section>

    <NodeDetailAccessErrorsPanel
      :failed-window-entries="failedWindowEntries"
      :failed-decisions="failedDecisions"
      :fmt-time="fmtTime"
      @open="emit('openRequest', $event)"
    />

    <section class="nd-section">
      <h3>模型状态变化</h3>
      <p v-if="!history.length" class="nd-muted">暂无状态变化记录。</p>
      <ol v-else class="nd-history">
        <li v-for="(event, index) in history" :key="index">
          <time>{{ fmtTime(event.ts) }}</time>
          <strong>{{ event.source === 'manual' ? '手动' : '自动' }} · {{ event.event }}</strong>
          <span>{{ event.reason || event.error_code || event.error_message || '—' }}</span>
        </li>
      </ol>
    </section>
  </template>
</template>
