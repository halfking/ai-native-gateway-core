<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { getProviderLogs, getProviderCredentials, type ProviderLogEntry, type ProviderCredential } from '../../api'

const props = defineProps<{ providerId: number }>()
const logs = ref<ProviderLogEntry[]>([])
const credentials = ref<ProviderCredential[]>([])
const total = ref(0)
const page = ref(1)
const loading = ref(false)
const error = ref('')
const keyword = ref('')
const credentialId = ref<number | ''>('')
const successFilter = ref<'all' | 'true' | 'false'>('all')
const errorKindFilter = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const resp = await getProviderLogs(props.providerId, {
      model: keyword.value.trim() || undefined,
      credential_id: credentialId.value === '' ? undefined : Number(credentialId.value),
      success: successFilter.value === 'all' ? undefined : successFilter.value === 'true',
      error_kind: errorKindFilter.value.trim() || undefined,
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
    credentials.value = await listProviderCredentials(props.providerId)
  } catch (e) {
    console.warn('Failed to load credentials for filter:', e)
    credentials.value = []
  }
}

function resetFilters() {
  keyword.value = ''
  credentialId.value = ''
  successFilter.value = 'all'
  errorKindFilter.value = ''
  page.value = 1
  load()
}

function fmtTs(ts: string | null) { return ts ? new Date(ts).toLocaleString('zh-CN', { hour12: false }) : '—' }
function token(v: number | null | undefined) { return v == null ? '—' : v.toLocaleString() }

onMounted(() => { loadCredentials(); load() })
watch(() => props.providerId, () => { loadCredentials(); resetFilters() })
</script>

<template>
  <div>
    <div style="display:flex;gap:8px;flex-wrap:wrap;align-items:center;margin-bottom:12px">
      <input v-model="keyword" placeholder="搜索模型名..." style="padding:4px 8px;width:180px" @keyup.enter="page=1;load()" />
      <select v-model="credentialId" style="padding:4px 8px;max-width:200px" @change="page=1;load()">
        <option value="">所有凭据</option>
        <option v-for="c in credentials" :key="c.id" :value="c.id">
          #{{ c.id }} {{ c.label || '—' }}{{ c.status !== 'active' ? ' (' + c.status + ')' : '' }}
        </option>
      </select>
      <select v-model="successFilter" style="padding:4px 8px" @change="page=1;load()">
        <option value="all">全部状态</option>
        <option value="true">成功</option>
        <option value="false">失败</option>
      </select>
      <input v-model="errorKindFilter" placeholder="错误类型..." style="padding:4px 8px;width:140px" @keyup.enter="page=1;load()" />
      <button class="btn btn-primary btn-sm" @click="page=1;load()" :disabled="loading">{{ loading ? '加载中...' : '查询' }}</button>
      <button class="btn btn-ghost btn-sm" @click="resetFilters" :disabled="loading">重置</button>
      <span style="color:var(--muted);font-size:12px;margin-left:auto">共 {{ total }} 条</span>
    </div>
    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <table v-if="logs.length" style="width:100%;border-collapse:collapse;font-size:12px">
      <thead>
        <tr style="text-align:left;border-bottom:1px solid var(--border)">
          <th style="padding:6px">时间</th>
          <th style="padding:6px">凭据</th>
          <th style="padding:6px">客户端模型</th>
          <th style="padding:6px">出站模型</th>
          <th style="padding:6px">成功</th>
          <th style="padding:6px">错误类型</th>
          <th style="padding:6px">Token (入/出)</th>
          <th style="padding:6px">费用</th>
          <th style="padding:6px">延迟</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(l, i) in logs" :key="i" style="border-bottom:1px solid var(--border)">
          <td style="padding:4px 6px">{{ fmtTs(l.ts) }}</td>
          <td style="padding:4px 6px;color:var(--muted)">#{{ l.credential_id }}</td>
          <td style="padding:4px 6px"><code>{{ l.client_model || '—' }}</code></td>
          <td style="padding:4px 6px"><code>{{ l.outbound_model || '—' }}</code></td>
          <td style="padding:4px 6px">
            <span class="badge" :class="l.success ? 'badge-green' : 'badge-red'">{{ l.success ? 'OK' : 'FAIL' }}</span>
          </td>
          <td style="padding:4px 6px;color:var(--muted)">{{ l.error_kind || '—' }}</td>
          <td style="padding:4px 6px">{{ token(l.prompt_tokens) }} / {{ token(l.completion_tokens) }}</td>
          <td style="padding:4px 6px">{{ l.cost_usd != null ? '$' + Number(l.cost_usd).toFixed(6) : '—' }}</td>
          <td style="padding:4px 6px">{{ l.latency_ms != null ? l.latency_ms + 'ms' : '—' }}</td>
        </tr>
      </tbody>
    </table>
    <div v-if="!loading && logs.length === 0" style="color:var(--muted);text-align:center;padding:20px">暂无日志</div>
    <div v-if="total > 50" style="display:flex;gap:12px;align-items:center;margin-top:12px">
      <button class="btn btn-ghost btn-sm" :disabled="page <= 1" @click="page--;load()">上一页</button>
      <span style="color:var(--muted)">{{ page }} / {{ Math.ceil(total / 50) }}</span>
      <button class="btn btn-ghost btn-sm" :disabled="page >= Math.ceil(total / 50)" @click="page++;load()">下一页</button>
    </div>
  </div>
</template>
