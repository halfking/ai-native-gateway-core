<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import {
  getVendorCredentialErrorDetail,
  type VendorCredentialErrorDetail,
  type VendorErrorHours,
  type VendorRecentFailure,
} from '../../api/vendor-credential-error'
import {
  errorKindBadgeClass,
  errorKindI18nKey,
  errorStageI18nKey,
  rawVocabFallback,
  retryableBadgeClass,
  retryableI18nKey,
} from '../../utils/errorVocab'
import { openRequestDetailPage } from '../../utils/openRequestDetailPage'

const props = defineProps<{
  credentialId?: number
}>()

const { t } = useI18n()
const router = useRouter()
const pd = (key: string, params?: Record<string, unknown>): string =>
  t(`providerDetail.errorDetail.${key}`, params ?? {})

// 2026-09-05 审计 F2-#2：error_kind / stage / retryable 徽标与
// RoutingAttemptsTimeline 共享 utils/errorVocab.ts 单一词表，
// 同一 error_kind 在两处渲染一致的本地化标签 + 徽标 tone。
function formatErrorKind(kind?: string | null): string {
  const key = errorKindI18nKey(kind)
  if (key) return t(key)
  return kind ? rawVocabFallback(kind) : '—'
}

function formatStage(stage?: string | null): string {
  const key = errorStageI18nKey(stage)
  if (key) return t(key)
  return stage ?? '—'
}

function formatRetryable(retryable?: boolean | null): string | null {
  const key = retryableI18nKey(retryable)
  return key ? t(key) : null
}

// 审计 F2-#4：request_id 可点击跳转 request-detail（复用全局工具）。
function openFailureRequest(item: VendorRecentFailure): void {
  openRequestDetailPage(item.request_id, undefined, router)
}

const hours = ref<VendorErrorHours>('24')
const loading = ref(false)
const error = ref('')
const data = ref<VendorCredentialErrorDetail | null>(null)
let requestSequence = 0
let requestController: AbortController | null = null

const hasCredential = computed(() => Number.isInteger(props.credentialId) && (props.credentialId ?? 0) > 0)

function formatTime(value: string | null | undefined): string {
  if (!value) return '—'
  return new Date(value).toLocaleString()
}

function formatScore(value: number | null | undefined): string {
  return value == null ? '—' : value.toFixed(1)
}

async function loadData() {
  const sequence = ++requestSequence
  requestController?.abort()
  requestController = null
  if (!hasCredential.value) {
    data.value = null
    error.value = ''
    loading.value = false
    return
  }
  const controller = new AbortController()
  requestController = controller
  loading.value = true
  error.value = ''
  try {
    const result = await getVendorCredentialErrorDetail(props.credentialId as number, hours.value, { signal: controller.signal })
    if (sequence === requestSequence) data.value = result
  } catch (err: unknown) {
    if (sequence !== requestSequence || controller.signal.aborted) return
    data.value = null
    error.value = err instanceof Error ? err.message : pd('loadFailed')
  } finally {
    if (sequence === requestSequence) {
      loading.value = false
      requestController = null
    }
  }
}

watch(() => props.credentialId, loadData, { immediate: true })
watch(hours, loadData)
onBeforeUnmount(() => {
  requestSequence++
  requestController?.abort()
  requestController = null
})
</script>

<template>
  <div class="error-detail-tab">
    <div v-if="!hasCredential" class="empty-hint">{{ pd('selectCredential') }}</div>
    <div v-else>
      <div class="toolbar">
        <span class="section-title">{{ pd('title') }}</span>
        <select v-model="hours" class="cf-select" :disabled="loading" :title="pd('windowTitle')">
          <option value="1">{{ pd('lastHour') }}</option>
          <option value="24">{{ pd('lastDay') }}</option>
          <option value="168">{{ pd('lastWeek') }}</option>
        </select>
      </div>

      <div v-if="error" class="alert alert-danger">{{ error }}</div>
      <div v-if="loading" class="empty-hint">{{ pd('loading') }}</div>

      <template v-if="data && !loading">
        <div class="status-grid">
          <div class="status-item"><span>{{ pd('credential') }}</span><strong>{{ data.credential.label }}</strong></div>
          <div class="status-item"><span>{{ pd('health') }}</span><strong>{{ data.credential.health_status }}</strong></div>
          <div class="status-item"><span>{{ pd('availability') }}</span><strong>{{ data.credential.availability_state }}</strong></div>
          <div class="status-item"><span>{{ pd('circuit') }}</span><strong>{{ data.credential.circuit_state }}</strong></div>
          <div class="status-item"><span>{{ pd('consecutiveFailures') }}</span><strong>{{ data.credential.consecutive_failures }}</strong></div>
          <div class="status-item"><span>{{ pd('balance') }}</span><strong>{{ data.credential.balance_usd == null ? '—' : `${data.credential.balance_usd} ${data.credential.balance_currency ?? ''}` }}</strong></div>
        </div>

        <div v-if="data.credential.state_reason_detail" class="reason-line">
          {{ data.credential.state_reason_code }}: {{ data.credential.state_reason_detail }}
        </div>

        <section class="section-block">
          <h3>{{ pd('summary') }}</h3>
          <table v-if="data.error_summary.length" class="data-table">
            <thead><tr><th>{{ pd('errorKind') }}</th><th>{{ pd('count') }}</th><th>{{ pd('statusCodes') }}</th><th>{{ pd('lastSeen') }}</th></tr></thead>
            <tbody><tr v-for="item in data.error_summary" :key="item.error_kind">
              <td><span class="badge" :class="errorKindBadgeClass(item.error_kind)">{{ formatErrorKind(item.error_kind) }}</span></td>
              <td>{{ item.count }}</td><td>{{ item.distinct_status_codes }}</td><td>{{ formatTime(item.last_seen) }}</td>
            </tr></tbody>
          </table>
          <div v-else class="empty-hint">{{ pd('noErrors') }}</div>
        </section>

        <section class="section-block">
          <h3>{{ pd('recentFailures') }}</h3>
          <table v-if="data.recent_failures.length" class="data-table failures-table">
            <thead><tr><th>{{ pd('time') }}</th><th>{{ pd('requestId') }}</th><th>{{ pd('model') }}</th><th>{{ pd('supplier') }}</th><th>{{ pd('kind') }}</th><th>{{ pd('errorCode') }}</th><th>{{ pd('httpStatus') }}</th><th>{{ pd('stage') }}</th><th>{{ pd('retry') }}</th><th>{{ pd('latency') }}</th><th>{{ pd('message') }}</th><th>{{ pd('upstreamPreview') }}</th></tr></thead>
            <tbody><tr v-for="item in data.recent_failures" :key="`${item.request_id}-${item.attempt_index}-${item.ts}`">
              <td>{{ formatTime(item.ts) }}</td>
              <td>
                <button
                  v-if="item.request_id"
                  type="button"
                  class="request-link"
                  :title="pd('openRequestTitle')"
                  @click="openFailureRequest(item)"
                >{{ item.request_id }}</button>
                <span v-else>—</span>
              </td>
              <td>{{ item.raw_model_name }}</td>
              <td>{{ item.supplier || '—' }}</td>
              <td>
                <span class="badge" :class="errorKindBadgeClass(item.error_kind)">{{ formatErrorKind(item.error_kind) }}</span>
              </td>
              <td>{{ item.error_code || '—' }}</td>
              <td>{{ item.upstream_status_code ?? '—' }}</td>
              <td>{{ formatStage(item.stage) }}</td>
              <td>
                <span v-if="formatRetryable(item.retryable)" class="badge" :class="retryableBadgeClass(item.retryable)">{{ formatRetryable(item.retryable) }}</span>
                <span v-else>—</span>
              </td>
              <td>{{ item.latency_ms != null ? `${item.latency_ms}ms` : '—' }}</td>
              <td class="message-cell">{{ item.error_message ?? '—' }}</td><td class="preview-cell">{{ item.upstream_response_preview ?? '—' }}</td>
            </tr></tbody>
          </table>
          <div v-else class="empty-hint">{{ pd('noRecentFailures') }}</div>
        </section>

        <section class="section-block">
          <h3>{{ pd('qualityScores') }}</h3>
          <table v-if="data.quality_scores_7d.length" class="data-table">
            <thead><tr><th>{{ pd('date') }}</th><th>{{ pd('totalScore') }}</th><th>{{ pd('availabilityScore') }}</th><th>{{ pd('stabilityScore') }}</th></tr></thead>
            <tbody><tr v-for="item in data.quality_scores_7d" :key="item.profile_date"><td>{{ item.profile_date }}</td><td>{{ formatScore(item.total_score) }}</td><td>{{ formatScore(item.availability_score) }}</td><td>{{ formatScore(item.stability_score) }}</td></tr></tbody>
          </table>
          <div v-else class="empty-hint">{{ pd('noQualityScores') }}</div>
        </section>
      </template>
    </div>
  </div>
</template>

<style scoped>
.badge-orange { background: var(--warning-bg); color: var(--warning); }
/* 审计 F2-#2：与 RoutingAttemptsTimeline 共享词表徽标 tone（badge class 同名）。 */
.badge-success { background: var(--success-bg); color: var(--success); }
.badge-muted { background: var(--neutral-bg); color: var(--muted); }
.request-link {
  max-width: 180px; padding: 0; border: 0; background: none; color: var(--accent);
  font: inherit; font-family: monospace; cursor: pointer; text-align: left;
  display: inline-block; vertical-align: bottom;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.request-link:hover { text-decoration: underline; }

.error-detail-tab { font-size: 12px; }
.toolbar, .status-grid { display: flex; gap: 12px; align-items: center; }
.toolbar { justify-content: space-between; margin-bottom: 12px; }
.section-title, h3 { font-size: 14px; font-weight: 600; }
.status-grid { align-items: stretch; flex-wrap: wrap; margin-bottom: 12px; }
.status-item { min-width: 130px; padding: 10px 12px; border: 1px solid var(--border-color); background: var(--card-bg); }
.status-item span { display: block; color: var(--muted); margin-bottom: 4px; }
.reason-line, .section-block { margin-top: 12px; }
.reason-line { color: var(--muted); }
.data-table { width: 100%; font-size: 12px; }
.message-cell, .preview-cell { max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.empty-hint { color: var(--muted); text-align: center; padding: 24px; }
@media (max-width: 720px) { .status-item { flex: 1 1 42%; } .failures-table { min-width: 1180px; } }
</style>
