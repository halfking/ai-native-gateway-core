<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import {
  getProviderProbeHistory,
  triggerProviderProbe,
  type ProbeRun,
} from '../../api'

const props = defineProps<{ providerId: number }>()

const runs = ref<ProbeRun[]>([])
const loading = ref(false)
const error = ref('')
const statusFilter = ref<string>('')
const triggering = ref<Set<string>>(new Set())

const statusOptions = [
  { value: '', label: '全部' },
  { value: 'ok', label: '成功' },
  { value: 'http_4xx', label: '客户端错误 (4xx)' },
  { value: 'http_5xx', label: '服务端错误 (5xx)' },
  { value: 'network', label: '网络错误' },
  { value: 'auth', label: '鉴权错误' },
  { value: 'skipped', label: '已跳过' },
]

async function load() {
  loading.value = true
  error.value = ''
  try {
    const r = await getProviderProbeHistory(props.providerId, {
      limit: 100,
      status: statusFilter.value || undefined,
    })
    runs.value = r.runs
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function trigger(credentialId: number, rawModel: string) {
  const key = `${credentialId}:${rawModel}`
  triggering.value.add(key)
  try {
    await triggerProviderProbe(props.providerId, credentialId, rawModel)
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '触发失败'
  } finally {
    triggering.value.delete(key)
  }
}

function statusClass(s: string) {
  return `status-pill status-${s}`
}

function stateClass(s: string) {
  return `state-pill state-${s}`
}

const grouped = computed(() => {
  // Group by (credential, model) for the trigger column.
  const map = new Map<string, ProbeRun[]>()
  for (const r of runs.value) {
    const k = `${r.credential_id}:${r.raw_model_name}`
    if (!map.has(k)) map.set(k, [])
    map.get(k)!.push(r)
  }
  return map
})

watch(() => props.providerId, load, { immediate: true })
watch(statusFilter, load)
</script>

<template>
  <div class="probe-history">
    <div class="probe-toolbar">
      <label>
        状态过滤：
        <select v-model="statusFilter">
          <option v-for="o in statusOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>
      </label>
      <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">刷新</button>
      <span class="muted">最近 100 条 · 触发方式：scheduler 自动 / manual 手动</span>
    </div>

    <div v-if="error" class="error-bar">{{ error }}</div>

    <table v-if="runs.length > 0" class="probe-table">
      <thead>
        <tr>
          <th>时间</th>
          <th>凭据</th>
          <th>模型</th>
          <th>状态</th>
          <th>HTTP</th>
          <th>错误</th>
          <th>延迟</th>
          <th>状态变化</th>
          <th>触发</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in runs" :key="r.id">
          <td class="ts">{{ new Date(r.created_at).toLocaleString() }}</td>
          <td>{{ r.credential_id }}</td>
          <td class="mono">{{ r.raw_model_name }}</td>
          <td><span :class="statusClass(r.status)">{{ r.status }}</span></td>
          <td>{{ r.http_status ?? '—' }}</td>
          <td class="err-cell">
            <code v-if="r.error_code">{{ r.error_code }}</code>
            <div v-if="r.error_message" class="muted small">{{ r.error_message }}</div>
          </td>
          <td>{{ r.latency_ms }}ms</td>
          <td><span :class="stateClass(r.state_change)">{{ r.state_change }}</span></td>
          <td>{{ r.triggered_by }}</td>
          <td>
            <button
              v-if="r.status !== 'skipped' && r.status !== 'ok'"
              class="btn btn-xs"
              :disabled="triggering.has(`${r.credential_id}:${r.raw_model_name}`)"
              @click="trigger(r.credential_id, r.raw_model_name)"
            >
              {{ triggering.has(`${r.credential_id}:${r.raw_model_name}`) ? '…' : '重新测试' }}
            </button>
          </td>
        </tr>
      </tbody>
    </table>
    <div v-else-if="!loading" class="empty">暂无测试记录</div>
  </div>
</template>

<style scoped>
.probe-history { display: flex; flex-direction: column; gap: 12px; }
.probe-toolbar { display: flex; align-items: center; gap: 12px; }
.probe-toolbar select { padding: 4px 8px; border-radius: 4px; border: 1px solid #ccc; }
.probe-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.probe-table th, .probe-table td { padding: 6px 8px; border-bottom: 1px solid #eaecef; text-align: left; vertical-align: top; }
.probe-table th { background: #f6f8fa; font-weight: 600; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
.ts { white-space: nowrap; font-size: 12px; }
.err-cell { max-width: 360px; }
.muted { color: #6a737d; }
.small { font-size: 11px; }
.error-bar { background: #ffeef0; border: 1px solid #f97583; padding: 8px 12px; border-radius: 4px; color: #b31d28; }
.empty { padding: 24px; text-align: center; color: #6a737d; }
.status-pill { padding: 1px 6px; border-radius: 3px; font-size: 11px; font-weight: 600; color: #fff; }
.status-ok { background: #28a745; }
.status-http_4xx { background: #d29922; }
.status-http_5xx { background: #cb2431; }
.status-network { background: #6f42c1; }
.status-auth { background: #cb2431; }
.status-skipped { background: #6a737d; }
.status-unknown { background: #6a737d; }
.state-pill { padding: 1px 6px; border-radius: 3px; font-size: 11px; }
.state-recovered { background: #d4edda; color: #155724; }
.state-broke { background: #f8d7da; color: #721c24; }
.state-unchanged { background: #e2e3e5; color: #383d41; }
</style>
