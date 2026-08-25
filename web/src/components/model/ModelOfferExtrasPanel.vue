<script setup lang="ts">
/** IQ history + cross-credential check restored from ModelsTab drawer. */
import { ref, watch, computed, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useFormat } from '../../i18n/useFormat'
import { useChart, createTimeSeriesConfig } from '../../composables/useChart'
import type { ModelOffer } from '../../api/providers'
import { getProviderCredentials, checkCredential } from '../../api/providers'
import { getModelIQHistory, triggerModelIQTest, type IQHistoryPoint } from '../../api/model-iq'

const props = defineProps<{
  providerId: number
  offer: ModelOffer
  siblingOffers: ModelOffer[]
}>()

const emit = defineEmits<{ iqTested: [] }>()

const { t: td } = useI18n()
const pm = (k: string, params?: Record<string, unknown>): string =>
  td(`providerDetail.models.${k}` as never, params as never)
const { fmtDateTime } = useFormat()

const iqHistory = ref<IQHistoryPoint[]>([])
const iqHistoryLoading = ref(false)
const iqTestLoading = ref(false)
const iqTestError = ref('')
const checking = ref(false)
const checkResults = ref<Array<{
  credential_id: number
  credential_label: string
  phase1_status?: string
  phase1_message?: string
  phase2_status?: string | null
  phase2_message?: string | null
  status: string
  error: string | null
}> | null>(null)

watch(() => [props.offer.id, props.offer.credential_id, props.offer.raw_model_name], () => {
  checkResults.value = null
  void loadIQ()
}, { immediate: true })

const iqChartRef = ref<HTMLCanvasElement | null>(null)
const iqChartLabels = computed(() =>
  [...iqHistory.value].reverse().map(h => {
    const d = new Date(h.tested_at)
    return `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  }),
)
const iqChartConfig = computed(() =>
  createTimeSeriesConfig('line', iqChartLabels.value, [
    {
      label: pm('iqColScore'),
      data: [...iqHistory.value].reverse().map(h => h.overall_score),
      borderColor: '#58a6ff',
      backgroundColor: 'rgba(88,166,255,0.12)',
      tension: 0.3,
    },
    {
      label: pm('iqColAccuracy'),
      data: [...iqHistory.value].reverse().map(h => h.accuracy),
      borderColor: '#3fb950',
      backgroundColor: 'rgba(63,185,80,0.12)',
      tension: 0.3,
    },
  ], {
    scales: { y: { beginAtZero: true, max: 100 } },
    plugins: { legend: { display: true, labels: { boxWidth: 12, font: { size: 11 } } } },
  }),
)
const { initChart: iqInitChart, destroyChart: iqDestroyChart, isDisposed: iqChartDisposed } = useChart(iqChartRef, iqChartConfig)
let iqChartAlive = true
async function iqRefreshChart() {
  if (!iqChartAlive || iqChartDisposed()) return
  if (!iqHistory.value.length) {
    iqDestroyChart()
    return
  }
  await nextTick()
  if (!iqChartAlive || iqChartDisposed()) return
  iqInitChart()
}
watch(iqChartConfig, () => void iqRefreshChart(), { deep: true })
watch(() => iqHistory.value.length, () => void iqRefreshChart())
onMounted(() => void iqRefreshChart())
onBeforeUnmount(() => { iqChartAlive = false; iqDestroyChart() })

async function loadIQ() {
  iqHistoryLoading.value = true
  iqTestError.value = ''
  try {
    iqHistory.value = await getModelIQHistory(props.offer.credential_id, props.offer.raw_model_name, 50)
  } catch {
    iqHistory.value = []
  } finally {
    iqHistoryLoading.value = false
  }
}

async function runIQTest() {
  if (iqTestLoading.value) return
  iqTestLoading.value = true
  iqTestError.value = ''
  try {
    await triggerModelIQTest(props.offer.credential_id, props.offer.raw_model_name)
    await loadIQ()
    emit('iqTested')
  } catch (e: unknown) {
    iqTestError.value = e instanceof Error ? e.message : pm('iqTestFailed')
  } finally {
    iqTestLoading.value = false
  }
}

async function checkAcross() {
  checking.value = true
  checkResults.value = null
  const modelName = props.offer.raw_model_name
  try {
    const credentials = await getProviderCredentials(props.providerId)
    checkResults.value = await Promise.all(credentials.map(async (cred) => {
      const offerMatch = props.siblingOffers.find(
        o => o.credential_id === cred.id && o.raw_model_name.toLowerCase() === modelName.toLowerCase(),
      )
      const label = cred.label || cred.name || pm('creds.labelFallback', { id: cred.id })
      if (!offerMatch) {
        return {
          credential_id: cred.id,
          credential_label: label,
          phase1_status: 'unavailable',
          phase1_message: pm('phase1Missing'),
          phase2_status: null,
          phase2_message: null,
          status: 'unavailable',
          error: pm('offerNotInList'),
        }
      }
      try {
        const result = await checkCredential(props.providerId, cred.id, modelName)
        let phase1Status = 'error'
        let phase1Message = pm('phase1Failed', { reason: result.models_failure_reason || result.models_error || '' })
        if (result.models_ok) {
          phase1Status = 'ok'
          phase1Message = pm('phase1ModelsOk')
        } else if (result.effective_source === 'manifest' || result.effective_source === 'manifest_only') {
          phase1Status = 'warning'
          phase1Message = pm('phase1ManifestFallback')
        }
        let phase2Status: string | null = null
        let phase2Message: string | null = null
        let finalStatus = 'warning'
        let finalError: string | null = pm('phase2Skipped')
        if (result.probe_ok) {
          phase2Status = 'ok'
          phase2Message = pm('phase2ChatOk')
          finalStatus = 'ok'
          finalError = null
        } else if (result.probe_error) {
          phase2Status = 'error'
          phase2Message = pm('phase2Generic', { msg: result.probe_error })
          if (result.probe_http_status) phase2Message += ` (HTTP ${result.probe_http_status})`
          finalStatus = 'error'
          finalError = phase2Message
        }
        return {
          credential_id: cred.id,
          credential_label: label,
          phase1_status: phase1Status,
          phase1_message: phase1Message,
          phase2_status: phase2Status,
          phase2_message: phase2Message,
          status: finalStatus,
          error: finalError,
        }
      } catch (e: unknown) {
        return {
          credential_id: cred.id,
          credential_label: label,
          phase1_status: 'ok',
          phase1_message: pm('phase2InOfferOnly'),
          phase2_status: 'error',
          phase2_message: pm('phase2Generic', { msg: e instanceof Error ? e.message : String(e) }),
          status: 'error',
          error: pm('checkFailedPrefix', { msg: e instanceof Error ? e.message : String(e) }),
        }
      }
    }))
  } finally {
    checking.value = false
  }
}
</script>

<template>
  <section class="extras">
    <div class="extras-head">
      <h4>{{ pm('drawerSectionIq') }}</h4>
      <button type="button" class="btn btn-sm" :disabled="iqTestLoading" :title="pm('iqTestBtnTitle')" @click="runIQTest">
        {{ iqTestLoading ? pm('iqTestRunning') : pm('iqTestBtn') }}
      </button>
    </div>
    <p v-if="iqTestError" class="cell-sub cell-sub--danger">{{ iqTestError }}</p>
    <p v-else-if="iqHistoryLoading" class="cell-sub">{{ pm('iqHistoryLoading') }}</p>
    <canvas v-show="iqHistory.length" ref="iqChartRef" class="iq-canvas" />
    <p v-if="!iqHistoryLoading && !iqHistory.length" class="cell-sub">{{ pm('iqHistoryEmpty') }}</p>
    <ul v-if="iqHistory.length" class="iq-list">
      <li v-for="h in iqHistory.slice(0, 8)" :key="h.tested_at">
        {{ fmtDateTime(h.tested_at) }} · {{ h.overall_score.toFixed(1) }} · {{ h.grade }}
      </li>
    </ul>

    <div class="extras-head" style="margin-top:14px">
      <h4>{{ pm('drawerSectionCheck') }}</h4>
      <button type="button" class="btn btn-sm" :disabled="checking" @click="checkAcross">
        {{ checking ? pm('drawCheckAllLoading') : pm('drawCheckAllBtn') }}
      </button>
    </div>
    <ul v-if="checkResults" class="check-list">
      <li v-for="r in checkResults" :key="r.credential_id">
        <strong>{{ r.credential_label }}</strong>
        <span :class="'st-' + r.status">{{ r.status }}</span>
        <span class="cell-sub">{{ r.phase1_message }} {{ r.phase2_message || r.error || '' }}</span>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.extras { padding: 0 16px 16px; }
.extras-head { display: flex; justify-content: space-between; align-items: center; gap: 8px; }
.extras h4 { margin: 0; }
.iq-canvas { width: 100%; height: 140px; }
.iq-list, .check-list { margin: 8px 0 0; padding-left: 16px; font-size: 12px; }
.st-ok { color: var(--success); margin: 0 6px; }
.st-error, .st-unavailable { color: var(--danger); margin: 0 6px; }
.st-warning { color: var(--warning-dark); margin: 0 6px; }
</style>
