<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getProviderLogs, getProviderCredentials, type ProviderLogEntry, type ProviderCredential } from '../../api'
import ModelPicker from '../../components/ModelPicker.vue'
import { useFormat } from '../../i18n/useFormat'

const props = defineProps<{ providerId: number }>()
const { t: td } = useI18n()
const pl = (k: string, params?: Record<string, unknown>): string => td(`providerDetail.logs.${k}` as never, params as never)
const { fmtDateTime, fmtNumber } = useFormat()

const logs = ref<ProviderLogEntry[]>([])
const credentials = ref<ProviderCredential[]>([])
const total = ref(0)
const page = ref(1)
const loading = ref(false)
const error = ref('')
const modelFilter = ref('')
const credentialId = ref<number | ''>('')
const successFilter = ref<'all' | 'true' | 'false'>('all')
const errorKindFilter = ref('')
const hours = ref(24)

function timeRange() {
  const end = new Date()
  const start = new Date(end.getTime() - hours.value * 3600 * 1000)
  return { from_ts: start.toISOString(), to_ts: end.toISOString() }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const range = timeRange()
    const resp = await getProviderLogs(props.providerId, {
      model: modelFilter.value.trim() || undefined,
      credential_id: credentialId.value === '' ? undefined : Number(credentialId.value),
      success: successFilter.value === 'all' ? undefined : successFilter.value === 'true',
      error_kind: errorKindFilter.value.trim() || undefined,
      from_ts: range.from_ts,
      to_ts: range.to_ts,
      page: page.value,
      page_size: 50,
    })
    logs.value = resp.items
    total.value = resp.total
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : pl('loadFailed')
  } finally {
    loading.value = false
  }
}

async function loadCredentials() {
  try {
    credentials.value = await getProviderCredentials(props.providerId)
  } catch {
    credentials.value = []
  }
}

function resetFilters() {
  modelFilter.value = ''
  credentialId.value = ''
  successFilter.value = 'all'
  errorKindFilter.value = ''
  hours.value = 24
  page.value = 1
  load()
}

function search() {
  page.value = 1
  load()
}

function credLabel(l: ProviderLogEntry) {
  if (l.credential_label) return l.credential_label
  return l.credential_id != null ? `#${l.credential_id}` : '—'
}

function fmtTs(ts: string | null) { return ts ? fmtDateTime(ts) : '—' }
function token(v: number | null | undefined) { return v == null ? '—' : fmtNumber(v) }

onMounted(() => { loadCredentials(); load() })
watch(() => props.providerId, () => { loadCredentials(); resetFilters() })
</script>

<template>
  <div>
    <div class="compact-filter-bar">
      <span class="cf-hint" :title="pl('filterHintTitle')">{{ pl('filterTitle') }}</span>
      <select v-model.number="hours" class="cf-select cf-hours" :title="pl('hoursLabel')" @change="search">
        <option :value="1">{{ pl('hours1') }}</option>
        <option :value="6">{{ pl('hours6') }}</option>
        <option :value="24">{{ pl('hours24') }}</option>
        <option :value="168">{{ pl('hours168') }}</option>
      </select>
      <div class="cf-grow" style="min-width:200px">
        <ModelPicker
          v-model="modelFilter"
          :placeholder="pl('modelPlaceholder')"
          :title="pl('modelPickerTitle')"
          @update:model-value="search"
        />
      </div>
      <select v-model="credentialId" class="cf-select cf-cred" :title="pl('credentialTitle')" @change="search">
        <option value="">{{ pl('credentialAll') }}</option>
        <option v-for="c in credentials" :key="c.id" :value="c.id">
          #{{ c.id }} {{ c.label || '—' }}
        </option>
      </select>
      <select v-model="successFilter" class="cf-select cf-status" :title="pl('credentialTitle')" @change="search">
        <option value="all">{{ pl('resultAll') }}</option>
        <option value="true">{{ pl('resultOk') }}</option>
        <option value="false">{{ pl('resultFail') }}</option>
      </select>
      <input
        v-model="errorKindFilter"
        class="cf-input cf-medium"
        :placeholder="pl('errorKindPlaceholder')"
        @keyup.enter="search"
      />
      <button class="btn btn-primary btn-sm" @click="search" :disabled="loading">{{ loading ? pl('searchLoading') : pl('search') }}</button>
      <button class="btn btn-ghost btn-sm" @click="resetFilters" :disabled="loading">{{ pl('reset') }}</button>
      <span class="cf-meta">{{ pl('total', { n: total }) }}</span>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <div v-if="total > 50" class="pager">
      <button class="btn btn-ghost btn-sm" :disabled="page <= 1" @click="page--; load()">{{ pl('pagerPrev') }}</button>
      <span class="cf-meta">{{ page }} / {{ Math.ceil(total / 50) }}</span>
      <button class="btn btn-ghost btn-sm" :disabled="page >= Math.ceil(total / 50)" @click="page++; load()">{{ pl('pagerNext') }}</button>
    </div>

    <div class="card" style="overflow-x:auto">
      <table v-if="logs.length" class="data-table logs-table">
        <thead>
          <tr>
            <th>{{ pl('table.time') }}</th>
            <th>{{ pl('table.credential') }}</th>
            <th>{{ pl('table.clientModel') }}</th>
            <th>{{ pl('table.outboundModel') }}</th>
            <th>{{ pl('table.result') }}</th>
            <th>{{ pl('table.errorKind') }}</th>
            <th>{{ pl('table.tokens') }}</th>
            <th>{{ pl('table.cost') }}</th>
            <th>{{ pl('table.latency') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(l, i) in logs" :key="l.request_id || i">
            <td>{{ fmtTs(l.ts) }}</td>
            <td class="cell-muted" :title="l.credential_id != null ? pl('credentialTitleAttr', { id: l.credential_id }) : ''">{{ credLabel(l) }}</td>
            <td><code>{{ l.client_model || '—' }}</code></td>
            <td><code>{{ l.outbound_model || '—' }}</code></td>
            <td>
              <span class="badge" :class="l.success ? 'badge-green' : 'badge-red'">{{ l.success ? pl('resultOk') : pl('resultFail') }}</span>
            </td>
            <td class="cell-muted">{{ l.error_kind || '—' }}</td>
            <td>{{ token(l.prompt_tokens) }} / {{ token(l.completion_tokens) }}</td>
            <td>{{ l.cost_usd != null ? '$' + Number(l.cost_usd).toFixed(6) : '—' }}</td>
            <td>{{ l.latency_ms != null ? l.latency_ms + 'ms' : '—' }}</td>
          </tr>
        </tbody>
      </table>
      <div v-if="!loading && logs.length === 0" class="empty-hint">{{ pl('empty') }}</div>
    </div>

    <div v-if="total > 50" class="pager">
      <button class="btn btn-ghost btn-sm" :disabled="page <= 1" @click="page--; load()">{{ pl('pagerPrev') }}</button>
      <span class="cf-meta">{{ page }} / {{ Math.ceil(total / 50) }}</span>
      <button class="btn btn-ghost btn-sm" :disabled="page >= Math.ceil(total / 50)" @click="page++; load()">{{ pl('pagerNext') }}</button>
    </div>
  </div>
</template>

<style scoped>
.logs-table {
  width: 100%;
  font-size: 12px;
}
.cell-muted {
  color: var(--muted);
}
.empty-hint {
  color: var(--muted);
  text-align: center;
  padding: 24px;
  font-size: 13px;
}
.pager {
  display: flex;
  gap: 12px;
  align-items: center;
  margin-top: 12px;
}
</style>
