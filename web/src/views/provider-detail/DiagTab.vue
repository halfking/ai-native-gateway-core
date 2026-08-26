<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { startDiagnose, getDiagnoseResult, getTask, type BackgroundTask } from '../../api'

const props = defineProps<{ providerId: number }>()
    const { t: td } = useI18n()
    const pdg = (k: string, params?: Record<string, unknown>): string => td(`providerDetail.diag.${k}` as never, params as never)

const taskStatus = ref<BackgroundTask | null>(null)
const cachedResult = ref<any>(null)
const loading = ref(false)
const error = ref('')
const polling = ref(false)

function assertDiagnoseTaskMatches(task: BackgroundTask, expectedProviderId: number) {
  const tp = task.provider_id
  if (tp == null) return
  if (tp === expectedProviderId) return
  const msg =
    `[diag] task ${task.id} refers to provider=${tp} ` +
    `but caller requested provider=${expectedProviderId}; ` +
    `treating as a stale/foreign task`
  console.error(msg, task)
  throw new Error(msg)
}

async function runDiagnose() {
  loading.value = true
  error.value = ''
  try {
    const { task_id } = await startDiagnose(props.providerId)
    polling.value = true
    const deadline = Date.now() + 120000
    while (Date.now() < deadline) {
      const task = await getTask(task_id)
      taskStatus.value = task
      assertDiagnoseTaskMatches(task, props.providerId)
      if (task.status !== 'running') {
        polling.value = false
        if (task.status === 'succeeded') {
          cachedResult.value = task.result
        } else {
          error.value = task.error || pdg('errorDiag')
        }
        return
      }
      await new Promise(r => setTimeout(r, 2000))
    }
    polling.value = false
    error.value = pdg('errorTimeout')
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : pdg('errorDiag')
    polling.value = false
  } finally {
    loading.value = false
  }
}

async function loadCached() {
  try {
    const data = await getDiagnoseResult(props.providerId)
    if (data?.result) cachedResult.value = data.result
  } catch { /* no cached result */ }
}

onMounted(loadCached)

function scoreColor(score: number): string {
  if (score >= 80) return '#4caf50'
  if (score >= 50) return '#f0b429'
  return '#f44336'
}
</script>

<template>
  <div>
    <div style="display:flex;gap:12px;align-items:center;margin-bottom:16px">
      <button class="btn btn-primary" @click="runDiagnose" :disabled="loading">
        {{ loading ? (polling ? pdg('runningDiag') : pdg('starting')) : pdg('runFull') }}
      </button>
      <span v-if="polling" style="color:var(--muted);font-size:12px">{{ pdg('pollingHint') }}</span>
    </div>
    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <div v-if="taskStatus?.status === 'running'" style="text-align:center;padding:40px;color:var(--muted)">
      {{ pdg('taskRunning', { id: taskStatus.id }) }}
    </div>

    <template v-if="cachedResult">
      <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:12px;margin-bottom:16px">
        <div style="background:var(--bg-subtle);border:1px solid var(--border);border-radius:8px;padding:14px">
          <div style="font-size:12px;color:var(--muted)">{{ pdg('summaryTotalCreds') }}</div>
          <div style="font-size:20px;font-weight:600">{{ cachedResult.summary?.total_credentials ?? 0 }}</div>
          <div style="font-size:11px;color:var(--muted)">
            <span style="color:#4caf50">{{ pdg('summaryHealthySuffix', { n: cachedResult.summary?.healthy ?? 0 }) }}</span> ·
            <span style="color:#f0b429">{{ pdg('summaryDegradedSuffix', { n: cachedResult.summary?.degraded ?? 0 }) }}</span> ·
            <span style="color:#f44336">{{ pdg('summaryUnreachableSuffix', { n: cachedResult.summary?.unreachable ?? 0 }) }}</span>
          </div>
        </div>
        <div style="background:var(--bg-subtle);border:1px solid var(--border);border-radius:8px;padding:14px">
          <div style="font-size:12px;color:var(--muted)">{{ pdg('summaryModelCoverage') }}</div>
          <div style="font-size:20px;font-weight:600">{{ (cachedResult.summary?.models_coverage_pct ?? 0).toFixed(1) }}%</div>
        </div>
        <div style="background:var(--bg-subtle);border:1px solid var(--border);border-radius:8px;padding:14px">
          <div style="font-size:12px;color:var(--muted)">{{ pdg('summaryAvgLatency') }}</div>
          <div style="font-size:20px;font-weight:600">{{ (cachedResult.summary?.avg_latency_ms ?? 0).toFixed(0) }} ms</div>
        </div>
      </div>

      <div v-if="cachedResult.error_classification" style="margin-bottom:16px">
        <h4 style="margin:0 0 8px;font-size:14px">{{ pdg('errorClassification') }}</h4>
        <div style="display:flex;gap:16px;flex-wrap:wrap">
          <span v-if="cachedResult.error_classification.auth_errors">{{ pdg('errorAuth', { n: cachedResult.error_classification.auth_errors }) }}</span>
          <span v-if="cachedResult.error_classification.rate_limit_errors">{{ pdg('errorRateLimit', { n: cachedResult.error_classification.rate_limit_errors }) }}</span>
          <span v-if="cachedResult.error_classification.timeout_errors">{{ pdg('errorTimeout_', { n: cachedResult.error_classification.timeout_errors }) }}</span>
          <span v-if="cachedResult.error_classification.model_not_found_errors">{{ pdg('errorModelNotFound', { n: cachedResult.error_classification.model_not_found_errors }) }}</span>
          <span v-if="cachedResult.error_classification.other_errors">{{ pdg('errorOther', { n: cachedResult.error_classification.other_errors }) }}</span>
          <span v-if="!cachedResult.error_classification.auth_errors && !cachedResult.error_classification.rate_limit_errors && !cachedResult.error_classification.timeout_errors && !cachedResult.error_classification.model_not_found_errors && !cachedResult.error_classification.other_errors" style="color:var(--muted)">{{ pdg('errorNone') }}</span>
        </div>
      </div>

      <div v-if="cachedResult.health_scores?.length" style="margin-bottom:16px">
        <h4 style="margin:0 0 8px;font-size:14px">{{ pdg('healthScoresTitle') }}</h4>
        <div style="display:flex;gap:12px;flex-wrap:wrap">
          <div v-for="s in cachedResult.health_scores" :key="s.credential_id" style="display:flex;align-items:center;gap:6px;min-width:120px">
            <span style="color:var(--muted);font-size:11px">#{{ s.credential_id }}</span>
            <div style="flex:1;height:6px;background:var(--bg-subtle);border-radius:3px;overflow:hidden">
              <div :style="{ width: s.score + '%', background: scoreColor(s.score), height: '100%', borderRadius: '3px' }"></div>
            </div>
            <span :style="{ color: scoreColor(s.score), fontWeight: 600, fontSize: '13px', minWidth: '24px' }">{{ s.score.toFixed(0) }}</span>
          </div>
        </div>
      </div>

      <div v-if="cachedResult.credentials?.length">
        <h4 style="margin:0 0 8px;font-size:14px">{{ pdg('credDetailTitle') }}</h4>
        <table style="width:100%;border-collapse:collapse;font-size:12px">
          <thead>
            <tr class="diag-cred-header">
              <th style="padding:6px">{{ pdg('credCol') }}</th>
              <th style="padding:6px">{{ pdg('credColStatus') }}</th>
              <th style="padding:6px">{{ pdg('credColCircuit') }}</th>
              <th style="padding:6px">{{ pdg('credColModels') }}</th>
              <th style="padding:6px">{{ pdg('credColChat') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="cd in cachedResult.credentials" :key="cd.credential_id" style="border-bottom:1px solid var(--border)">
              <td style="padding:6px">#{{ cd.credential_id }} {{ cd.label }}</td>
              <td style="padding:6px"><span class="badge" :class="cd.status === 'active' ? 'badge-green' : 'badge-red'">{{ cd.status }}</span></td>
              <td style="padding:6px"><span class="badge" :class="cd.circuit_state === 'closed' ? 'badge-green' : 'badge-amber'">{{ cd.circuit_state }}</span></td>
              <td style="padding:6px">
                <span v-if="cd.models_probe?.error" class="badge badge-red">{{ pdg('probeFailed') }}</span>
                <span v-else class="badge" :class="cd.models_probe?.status_code === 200 ? 'badge-green' : 'badge-red'">
                  {{ pdg('probeMetaModels', { status: cd.models_probe?.status_code || '—', count: cd.models_probe?.models_count ?? 0, latency: cd.models_probe?.latency_ms ?? 0 }) }}
                </span>
              </td>
              <td style="padding:6px">
                <span v-if="cd.chat_probe?.error" class="badge badge-red">{{ pdg('probeFailed') }}</span>
                <span v-else class="badge" :class="cd.chat_probe?.status_code === 200 ? 'badge-green' : 'badge-red'">
                  {{ pdg('probeMetaChat', { status: cd.chat_probe?.status_code || '—', latency: cd.chat_probe?.latency_ms ?? 0 }) }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>

<style scoped>
.diag-cred-header {
  text-align: start;
  border-bottom: 1px solid var(--border);
}
</style>
