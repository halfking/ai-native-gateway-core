<script setup lang="ts">
/**
 * 其它模型：当前凭据下非当前模型的可用性与探活/上下线（顶级 tab）。
 */
import { computed, ref, watch } from 'vue'
import {
  getCredentialMonitorSummary,
  sessionPingCredential,
  testCredentialModel,
  toggleModelAvailability,
  type CredentialModelStatus,
  type ModelToggleAction,
} from '../api/credential-monitor'

const props = defineProps<{
  credentialId: number
  currentModel: string
  models: CredentialModelStatus[]
  canEdit: boolean
  /** 父级已有完整 models 时可跳过补拉 */
  needsRefresh: boolean
}>()

const emit = defineEmits<{
  refreshed: [models: CredentialModelStatus[]]
  message: [text: string]
  error: [text: string]
}>()

const loading = ref(false)
const probing = ref(false)
const rowBusy = ref<Record<string, boolean>>({})
const reason = ref('')
const probeResults = ref<Record<string, string>>({})

const otherModels = computed(() => {
  const current = props.currentModel.trim().toLowerCase()
  return (props.models ?? []).filter(m => m.raw_model_name.toLowerCase() !== current)
})

watch(
  () => [props.credentialId, props.needsRefresh] as const,
  ([, needs]) => {
    if (needs) void refreshModels()
  },
  { immediate: true },
)

async function refreshModels() {
  if (!props.credentialId || loading.value) return
  loading.value = true
  try {
    const result = await getCredentialMonitorSummary({
      credential_id: props.credentialId,
      mode: 'detail',
    })
    const models = result.credentials?.[0]?.models ?? []
    emit('refreshed', models)
  } catch (error) {
    emit('error', error instanceof Error ? error.message : '其它模型状态加载失败')
  } finally {
    loading.value = false
  }
}

function statusClass(value: string | null | undefined): string {
  if (['ready', 'healthy', 'active', 'closed', 'ok', 'available', 'healthy_confirmed'].includes(value || '')) return 'is-ok'
  if (['cooling', 'half_open', 'low', 'warning', 'degraded', 'recovering', 'unknown'].includes(value || '')) return 'is-warn'
  return 'is-bad'
}

function pct(value: number | null | undefined): string {
  return value == null ? '—' : `${(value * 100).toFixed(1)}%`
}

function fmtTime(value: string | null | undefined): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

async function withRowBusy(model: string, fn: () => Promise<void>) {
  rowBusy.value = { ...rowBusy.value, [model]: true }
  try {
    await fn()
  } finally {
    const next = { ...rowBusy.value }
    delete next[model]
    rowBusy.value = next
  }
}

async function pingModel(model: string) {
  if (!props.canEdit) return
  await withRowBusy(model, async () => {
    try {
      const result = await sessionPingCredential(props.credentialId, model)
      probeResults.value = {
        ...probeResults.value,
        [model]: result.status === 'healthy'
          ? `Ping OK ${result.latency_ms}ms`
          : (result.error || result.status),
      }
      emit('message', `${model}: ${probeResults.value[model]}`)
    } catch (error) {
      const msg = error instanceof Error ? error.message : 'Ping 失败'
      probeResults.value = { ...probeResults.value, [model]: msg }
      emit('error', `${model}: ${msg}`)
    }
  })
}

async function queueProbe(model: string) {
  if (!props.canEdit) return
  await withRowBusy(model, async () => {
    try {
      await testCredentialModel(props.credentialId, model)
      probeResults.value = { ...probeResults.value, [model]: '探活已入队' }
      emit('message', `${model}: 探活已入队`)
    } catch (error) {
      const msg = error instanceof Error ? error.message : '探活提交失败'
      probeResults.value = { ...probeResults.value, [model]: msg }
      emit('error', `${model}: ${msg}`)
    }
  })
}

async function toggleModel(model: CredentialModelStatus) {
  if (!props.canEdit) return
  const trimmed = reason.value.trim()
  if (!trimmed) {
    emit('error', '请输入维护原因后再上下线模型。')
    return
  }
  const action: ModelToggleAction = model.binding_unavailable_reason === 'manual_offline' ? 'online' : 'offline'
  await withRowBusy(model.raw_model_name, async () => {
    try {
      await toggleModelAvailability(props.credentialId, model.raw_model_name, action, trimmed)
      emit('message', action === 'online' ? `${model.raw_model_name} 已恢复上线` : `${model.raw_model_name} 已下线`)
      reason.value = ''
      await refreshModels()
    } catch (error) {
      emit('error', error instanceof Error ? error.message : '模型上下线失败')
    }
  })
}

async function probeAll() {
  if (!props.canEdit || probing.value || !otherModels.value.length) return
  probing.value = true
  const concurrency = 3
  const queue = [...otherModels.value.map(m => m.raw_model_name)]
  const runOne = async (model: string) => {
    try {
      await testCredentialModel(props.credentialId, model)
      probeResults.value = { ...probeResults.value, [model]: '探活已入队' }
    } catch (error) {
      probeResults.value = {
        ...probeResults.value,
        [model]: error instanceof Error ? error.message : '失败',
      }
    }
  }
  try {
    const workers = Array.from({ length: Math.min(concurrency, queue.length) }, async () => {
      while (queue.length) {
        const model = queue.shift()
        if (model) await runOne(model)
      }
    })
    await Promise.all(workers)
    emit('message', `已提交 ${otherModels.value.length} 个模型的探活任务。`)
  } finally {
    probing.value = false
  }
}
</script>

<template>
  <section class="nd-section om-panel">
    <div class="om-toolbar">
      <h3>其它模型 <small>当前凭据下非 {{ currentModel || '当前' }} 的绑定</small></h3>
      <div class="om-actions">
        <button class="btn btn-sm btn-ghost" type="button" :disabled="loading" @click="refreshModels">
          {{ loading ? '刷新中…' : '刷新状态' }}
        </button>
        <button
          class="btn btn-sm btn-primary"
          type="button"
          :disabled="!canEdit || probing || !otherModels.length"
          @click="probeAll"
        >
          {{ probing ? '全面探测中…' : '一键全面探测' }}
        </button>
      </div>
    </div>

    <p v-if="!canEdit" class="om-hint">仅 default 租户可探活或上下线；当前只读。</p>
    <label v-else class="om-reason">
      维护原因（上下线必填）
      <input v-model="reason" placeholder="说明本次状态修改原因" />
    </label>

    <div v-if="loading && !otherModels.length" class="nd-seg-loading" role="status">其它模型加载中…</div>
    <p v-else-if="!otherModels.length" class="nd-muted">该凭据下没有其它模型绑定。</p>
    <div v-else class="om-table-wrap">
      <table class="om-table">
        <thead>
          <tr>
            <th>模型</th>
            <th>生效状态</th>
            <th>探活</th>
            <th>成功率</th>
            <th>P95</th>
            <th>最近探活</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="model in otherModels" :key="model.raw_model_name">
            <td>
              <strong>{{ model.raw_model_name }}</strong>
              <div v-if="model.model_disabled_reason" class="om-sub">{{ model.model_disabled_reason }}</div>
              <div v-if="probeResults[model.raw_model_name]" class="om-sub">{{ probeResults[model.raw_model_name] }}</div>
            </td>
            <td :class="statusClass(model.effective_state)">{{ model.effective_state }}</td>
            <td :class="statusClass(model.probe_state)">{{ model.probe_state }}</td>
            <td>{{ pct(model.recent_success_rate) }}</td>
            <td>{{ model.p95_latency_ms != null ? `${model.p95_latency_ms}ms` : '—' }}</td>
            <td>{{ fmtTime(model.probe_last_attempt_at) }}</td>
            <td class="om-row-actions">
              <button
                class="btn btn-sm"
                type="button"
                :disabled="!canEdit || rowBusy[model.raw_model_name]"
                @click="pingModel(model.raw_model_name)"
              >Ping</button>
              <button
                class="btn btn-sm"
                type="button"
                :disabled="!canEdit || rowBusy[model.raw_model_name]"
                @click="queueProbe(model.raw_model_name)"
              >探活</button>
              <button
                class="btn btn-sm"
                type="button"
                :disabled="!canEdit || rowBusy[model.raw_model_name]"
                @click="toggleModel(model)"
              >{{ model.binding_unavailable_reason === 'manual_offline' ? '上线' : '下线' }}</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.om-toolbar { display: flex; justify-content: space-between; gap: 12px; align-items: flex-start; flex-wrap: wrap; margin-bottom: 10px; }
.om-toolbar h3 { margin: 0; font-size: 14px; }
.om-actions { display: flex; gap: 8px; flex-wrap: wrap; }
.om-hint, .nd-muted { font-size: 12px; color: var(--kx-muted); }
.om-reason { display: grid; gap: 5px; font-size: 12px; max-width: 560px; margin-bottom: 10px; }
.om-reason input { box-sizing: border-box; width: 100%; padding: 7px; border: 1px solid var(--kx-border); border-radius: 5px; background: var(--kx-bg); color: inherit; }
.om-table-wrap { overflow: auto; }
.om-table { width: 100%; border-collapse: collapse; font-size: 11px; }
.om-table th, .om-table td { text-align: left; padding: 7px 6px; border-bottom: 1px solid var(--kx-border); vertical-align: top; }
.om-sub { color: var(--kx-muted); margin-top: 2px; font-size: 10px; overflow-wrap: anywhere; }
.om-row-actions { display: flex; gap: 4px; flex-wrap: wrap; }
.is-ok { color: var(--kx-success); }
.is-warn { color: var(--kx-warning); }
.is-bad { color: var(--kx-danger); }
.nd-seg-loading { padding: 24px; text-align: center; color: var(--kx-muted); font-size: 12px; }
</style>
