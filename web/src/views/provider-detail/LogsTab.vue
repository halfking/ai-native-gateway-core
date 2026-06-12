<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { getProviderLogs, getProviderCredentials, type ProviderLogEntry, type ProviderCredential } from '../../api'
import ModelPicker from '../../components/ModelPicker.vue'

const props = defineProps<{ providerId: number }>()
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
    error.value = e instanceof Error ? e.message : '加载失败'
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

function fmtTs(ts: string | null) { return ts ? new Date(ts).toLocaleString('zh-CN', { hour12: false }) : '—' }
function token(v: number | null | undefined) { return v == null ? '—' : v.toLocaleString() }

onMounted(() => { loadCredentials(); load() })
watch(() => props.providerId, () => { loadCredentials(); resetFilters() })
</script>

<template>
  <div>
    <div class="compact-filter-bar">
      <span class="cf-hint" title="仅显示经本供应商凭据路由的请求">本供应商</span>
      <select v-model.number="hours" class="cf-select cf-hours" title="时间范围" @change="search">
        <option :value="1">1小时</option>
        <option :value="6">6小时</option>
        <option :value="24">24小时</option>
        <option :value="168">7天</option>
      </select>
      <div class="cf-grow" style="min-width:200px">
        <ModelPicker
          v-model="modelFilter"
          placeholder="选择模型…"
          title="筛选供应商日志模型"
          @update:model-value="search"
        />
      </div>
      <select v-model="credentialId" class="cf-select cf-cred" title="凭据" @change="search">
        <option value="">全部凭据</option>
        <option v-for="c in credentials" :key="c.id" :value="c.id">
          #{{ c.id }} {{ c.label || '—' }}
        </option>
      </select>
      <select v-model="successFilter" class="cf-select cf-status" title="结果" @change="search">
        <option value="all">全部</option>
        <option value="true">成功</option>
        <option value="false">失败</option>
      </select>
      <input
        v-model="errorKindFilter"
        class="cf-input cf-medium"
        placeholder="错误类型"
        @keyup.enter="search"
      />
      <button class="btn btn-primary btn-sm" @click="search" :disabled="loading">{{ loading ? '…' : '查询' }}</button>
      <button class="btn btn-ghost btn-sm" @click="resetFilters" :disabled="loading">重置</button>
      <span class="cf-meta">共 {{ total }} 条</span>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div class="card" style="overflow-x:auto">
      <table v-if="logs.length" class="data-table logs-table">
        <thead>
          <tr>
            <th>时间</th>
            <th>凭据</th>
            <th>客户端模型</th>
            <th>出站模型</th>
            <th>结果</th>
            <th>错误类型</th>
            <th>Token (入/出)</th>
            <th>费用</th>
            <th>延迟</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(l, i) in logs" :key="l.request_id || i">
            <td>{{ fmtTs(l.ts) }}</td>
            <td class="cell-muted" :title="l.credential_id != null ? `凭据 #${l.credential_id}` : ''">{{ credLabel(l) }}</td>
            <td><code>{{ l.client_model || '—' }}</code></td>
            <td><code>{{ l.outbound_model || '—' }}</code></td>
            <td>
              <span class="badge" :class="l.success ? 'badge-green' : 'badge-red'">{{ l.success ? 'OK' : 'FAIL' }}</span>
            </td>
            <td class="cell-muted">{{ l.error_kind || '—' }}</td>
            <td>{{ token(l.prompt_tokens) }} / {{ token(l.completion_tokens) }}</td>
            <td>{{ l.cost_usd != null ? '$' + Number(l.cost_usd).toFixed(6) : '—' }}</td>
            <td>{{ l.latency_ms != null ? l.latency_ms + 'ms' : '—' }}</td>
          </tr>
        </tbody>
      </table>
      <div v-if="!loading && logs.length === 0" class="empty-hint">该时间范围内暂无本供应商请求日志</div>
    </div>

    <div v-if="total > 50" class="pager">
      <button class="btn btn-ghost btn-sm" :disabled="page <= 1" @click="page--; load()">上一页</button>
      <span class="cf-meta">{{ page }} / {{ Math.ceil(total / 50) }}</span>
      <button class="btn btn-ghost btn-sm" :disabled="page >= Math.ceil(total / 50)" @click="page++; load()">下一页</button>
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
