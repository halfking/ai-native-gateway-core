<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  getProviderDetail,
  getProviderCredentials,
  toggleProvider,
  checkProvider,
  updateProvider,
  addCredential,
  deleteCredential,
  checkCredential,
  updateCredential,
  revealCredentialKey,
  batchRecoverCredentials,
  getProviderModels,
  toggleModelOfferState,
  getProviderLogs,
  startDiagnose,
  getDiagnoseResult,
  resetCredentialAvailability,
  resetCredentialQuota,
  type Provider,
  type ProviderCredential,
  type CredentialCheckResult,
  type ModelOffer,
  type ProviderLogEntry,
  type DiagnoseResult,
} from '../api'

const route = useRoute()
const router = useRouter()
const providerId = computed(() => Number(route.params.id))

const provider = ref<any>(null)
const credentials = ref<ProviderCredential[]>([])
const loading = ref(true)
const error = ref('')
const activeTab = ref<'overview' | 'credentials' | 'models' | 'logs' | 'settings'>('overview')

// Edit modal
const showEditModal = ref(false)
const editForm = ref({ display_name: '', base_url: '', protocol: '', kind: '', category: '', discount_rate: 1, notes: '' })
const editSaving = ref(false)
const editError = ref('')

// Add credential modal
const showAddCredModal = ref(false)
const newCred = ref({ api_key: '', label: '' })
const addCredSaving = ref(false)
const addCredError = ref('')

// Check status
const checking = ref(false)
const checkMessage = ref('')

// Batch recover
const batchRecovering = ref(false)
const batchRecoverMsg = ref('')

// Models
const modelOffers = ref<ModelOffer[]>([])
const modelsLoading = ref(false)
const modelsError = ref('')

// Logs
const logs = ref<ProviderLogEntry[]>([])
const logsTotal = ref(0)
const logsPage = ref(1)
const logsLoading = ref(false)
const logsError = ref('')
const logsKeyword = ref('')

// Diagnosis
const diagLoading = ref(false)
const diagPolling = ref(false)
const diagResult = ref<DiagnoseResult | null>(null)
const diagError = ref('')

// Credential inline editing
const credSaving = ref<Record<number, boolean>>({})
const credChecking = ref<Record<number, boolean>>({})
const credCheckMsgs = ref<Record<number, string>>({})
const credSaveMsgs = ref<Record<number, string>>({})

const credentialStatuses = [
  { value: 'active', label: '可用' },
  { value: 'cooling', label: '冷却' },
  { value: 'degraded', label: '降级' },
  { value: 'quarantine', label: '隔离' },
  { value: 'quota_expired', label: '配额耗尽' },
  { value: 'disabled', label: '停用' },
]

async function loadProvider() {
  loading.value = true
  error.value = ''
  try {
    const [p, creds] = await Promise.all([
      getProviderDetail(providerId.value),
      getProviderCredentials(providerId.value),
    ])
    provider.value = p
    credentials.value = creds
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function loadModels() {
  modelsLoading.value = true
  modelsError.value = ''
  try {
    modelOffers.value = await getProviderModels(providerId.value)
  } catch (e: unknown) {
    modelsError.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    modelsLoading.value = false
  }
}

async function toggleModel(offer: ModelOffer) {
  try {
    await toggleModelOfferState(providerId.value, offer.id, { available: !offer.available })
    await loadModels()
  } catch (e: unknown) {
    modelsError.value = e instanceof Error ? e.message : '操作失败'
  }
}

async function loadLogs() {
  logsLoading.value = true
  logsError.value = ''
  try {
    const resp = await getProviderLogs(providerId.value, {
      model: logsKeyword.value.trim() || undefined,
      page: logsPage.value,
      page_size: 50,
    })
    logs.value = resp.items
    logsTotal.value = resp.total
  } catch (e: unknown) {
    logsError.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    logsLoading.value = false
  }
}

async function loadDiagResult() {
  try {
    const data = await getDiagnoseResult(providerId.value)
    if (data?.result) diagResult.value = data.result
  } catch { /* no cached result */ }
}

async function runDiagnose() {
  diagLoading.value = true
  diagError.value = ''
  try {
    const { task_id } = await startDiagnose(providerId.value)
    diagPolling.value = true
    const deadline = Date.now() + 120000
    while (Date.now() < deadline) {
      await new Promise(r => setTimeout(r, 2000))
      const data = await getDiagnoseResult(providerId.value)
      if (data?.result) {
        diagResult.value = data.result
        diagPolling.value = false
        return
      }
    }
    diagPolling.value = false
    diagError.value = '诊断超时'
  } catch (e: unknown) {
    diagError.value = e instanceof Error ? e.message : '诊断失败'
    diagPolling.value = false
  } finally {
    diagLoading.value = false
  }
}

function scoreColor(score: number): string {
  if (score >= 80) return '#4caf50'
  if (score >= 50) return '#f0b429'
  return '#f44336'
}

function openEdit() {
  if (!provider.value) return
  editForm.value = {
    display_name: provider.value.display_name || '',
    base_url: provider.value.base_url || '',
    protocol: provider.value.protocol || '',
    kind: provider.value.kind || 'cloud',
    category: provider.value.category || 'official',
    discount_rate: provider.value.discount_rate || 1,
    notes: provider.value.notes || '',
  }
  editError.value = ''
  showEditModal.value = true
}

async function saveEdit() {
  editSaving.value = true
  editError.value = ''
  try {
    await updateProvider(providerId.value, editForm.value)
    showEditModal.value = false
    await loadProvider()
  } catch (e: unknown) {
    editError.value = e instanceof Error ? e.message : '保存失败'
  } finally {
    editSaving.value = false
  }
}

async function handleToggle() {
  if (!provider.value) return
  try {
    await toggleProvider(providerId.value)
    provider.value.enabled = !provider.value.enabled
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '操作失败'
  }
}

async function handleCheck() {
  checking.value = true
  checkMessage.value = ''
  try {
    const result = await checkProvider(providerId.value)
    checkMessage.value = result.reason || '检测已启动'
    setTimeout(() => loadProvider(), 5000)
  } catch (e: unknown) {
    checkMessage.value = e instanceof Error ? e.message : '检测失败'
  } finally {
    checking.value = false
  }
}

async function handleBatchRecover() {
  if (!confirm('确认批量恢复所有冷却中凭据？')) return
  batchRecovering.value = true
  batchRecoverMsg.value = ''
  try {
    const result = await batchRecoverCredentials(providerId.value)
    batchRecoverMsg.value = `恢复 ${result.recovered} 个凭据`
    await loadProvider()
  } catch (e: unknown) {
    batchRecoverMsg.value = e instanceof Error ? e.message : '恢复失败'
  } finally {
    batchRecovering.value = false
  }
}

function openAddCred() {
  newCred.value = { api_key: '', label: '' }
  addCredError.value = ''
  showAddCredModal.value = true
}

async function saveAddCred() {
  if (!newCred.value.api_key) {
    addCredError.value = '请输入 API Key'
    return
  }
  addCredSaving.value = true
  addCredError.value = ''
  try {
    await addCredential(providerId.value, newCred.value)
    showAddCredModal.value = false
    await loadProvider()
  } catch (e: unknown) {
    addCredError.value = e instanceof Error ? e.message : '添加失败'
  } finally {
    addCredSaving.value = false
  }
}

async function saveCred(c: ProviderCredential) {
  credSaving.value = { ...credSaving.value, [c.id]: true }
  credSaveMsgs.value = { ...credSaveMsgs.value, [c.id]: '' }
  try {
    await updateCredential(providerId.value, c.id, {
      label: c.label,
      status: c.status,
      concurrency_limit: c.concurrency_limit || null,
      effective_at: c.effective_at,
      expires_at: c.expires_at,
      tags: c.tags,
      notes: c.notes || '',
    })
    await loadProvider()
  } catch (e: unknown) {
    credSaveMsgs.value = { ...credSaveMsgs.value, [c.id]: e instanceof Error ? e.message : '保存失败' }
  } finally {
    credSaving.value = { ...credSaving.value, [c.id]: false }
  }
}

async function checkCred(c: ProviderCredential) {
  credChecking.value = { ...credChecking.value, [c.id]: true }
  credCheckMsgs.value = { ...credCheckMsgs.value, [c.id]: '' }
  try {
    const r = await checkCredential(providerId.value, c.id)
    credCheckMsgs.value = { ...credCheckMsgs.value, [c.id]: `${r.health_status} · ${r.probe_ok ? '探活通过' : '不可用'}` }
    setTimeout(() => loadProvider(), 3000)
  } catch (e: unknown) {
    credCheckMsgs.value = { ...credCheckMsgs.value, [c.id]: e instanceof Error ? e.message : '检测失败' }
  } finally {
    credChecking.value = { ...credChecking.value, [c.id]: false }
  }
}

async function delCred(c: ProviderCredential) {
  if (!confirm('确认停用该凭据？')) return
  try {
    await deleteCredential(providerId.value, c.id)
    await loadProvider()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '停用失败'
  }
}

async function resetCredAvailability(c: ProviderCredential) {
  if (!confirm('确认重置该凭据的可用性状态？')) return
  try {
    await resetCredentialAvailability(providerId.value, c.id)
    await loadProvider()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '重置失败'
  }
}

async function resetCredQuota(c: ProviderCredential) {
  if (!confirm('确认重置该凭据的配额？')) return
  try {
    await resetCredentialQuota(providerId.value, c.id)
    await loadProvider()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '重置失败'
  }
}

function statusBadge(status: string) {
  if (status === 'active') return 'badge-green'
  if (status === 'disabled' || status === 'quota_expired' || status === 'quarantine') return 'badge-red'
  if (status === 'cooling' || status === 'degraded') return 'badge-amber'
  return 'badge-gray'
}

function healthBadge(status: string | null) {
  if (status === 'healthy') return 'badge-green'
  if (status === 'warning') return 'badge-amber'
  if (status === 'unreachable') return 'badge-red'
  return 'badge-gray'
}

function healthLabel(status: string | null) {
  if (status === 'healthy') return '正常'
  if (status === 'warning') return '警示'
  if (status === 'unreachable') return '不可达'
  return '未探测'
}

function fmtTime(ts: string | null) {
  if (!ts) return '—'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}

function money(v: number | string | null | undefined) {
  if (v == null) return '—'
  const n = typeof v === 'string' ? Number(v) : v
  return Number.isNaN(n) ? '—' : `$${n.toFixed(4)}`
}

function asDateInput(v: string | null) {
  return v ? v.slice(0, 16) : ''
}

function fmtTimeShort(ts: string | null) {
  if (!ts) return '—'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false, month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

onMounted(loadProvider)
</script>

<template>
  <div>
    <div class="page-header" style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px">
      <div style="display:flex;align-items:center;gap:12px">
        <button class="btn btn-ghost btn-sm" @click="router.push('/providers')">← 返回</button>
        <h2 style="margin:0">{{ provider?.display_name || '加载中...' }}</h2>
        <span v-if="provider" :class="['badge', provider.enabled ? 'badge-green' : 'badge-gray']">
          {{ provider.enabled ? '已启用' : '已禁用' }}
        </span>
      </div>
      <div style="display:flex;gap:8px">
        <button class="btn btn-ghost btn-sm" @click="loadProvider" :disabled="loading">刷新</button>
        <button class="btn btn-ghost btn-sm" @click="handleToggle">
          {{ provider?.enabled ? '禁用' : '启用' }}
        </button>
        <button class="btn btn-primary btn-sm" @click="handleCheck" :disabled="checking">
          {{ checking ? '检测中...' : '检测' }}
        </button>
      </div>
    </div>

    <div v-if="checkMessage" style="margin-bottom:12px;padding:8px;background:var(--surface-secondary);border-radius:6px;font-size:13px">
      {{ checkMessage }}
    </div>

    <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>

    <div v-if="loading" style="text-align:center;padding:40px;color:var(--muted)">加载中...</div>

    <template v-else-if="provider">
      <!-- Tab bar -->
      <div style="display:flex;gap:4px;margin-bottom:16px;border-bottom:1px solid var(--border);padding-bottom:8px">
        <button v-for="tab in (['overview','credentials','models','logs','settings','diagnosis'] as const)"
                :key="tab"
                :class="['btn btn-ghost btn-sm', { 'btn-primary': activeTab === tab }]"
                @click="activeTab = tab; if(tab === 'models') loadModels(); if(tab === 'logs') loadLogs(); if(tab === 'diagnosis') loadDiagResult()">
          {{ { overview: '概览', credentials: '凭据', models: '模型', logs: '请求日志', settings: '设置', diagnosis: '诊断' }[tab] }}
        </button>
      </div>

      <!-- Overview Tab -->
      <div v-if="activeTab === 'overview'">
        <div class="stat-grid" style="margin-bottom:20px">
          <div class="stat-card">
            <div class="stat-label">可用凭据</div>
            <div class="stat-value">{{ provider.active_cred_count }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">健康</div>
            <div class="stat-value" style="color:var(--success)">{{ provider.healthy_cred_count }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">冷却</div>
            <div class="stat-value" style="color:var(--warning)">{{ provider.cooling_cred_count }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">不可达</div>
            <div class="stat-value" style="color:var(--danger)">{{ provider.unreachable_cred_count }}</div>
          </div>
        </div>

        <div class="card" style="margin-bottom:16px">
          <h3 style="margin:0 0 12px">基础信息</h3>
          <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;font-size:13px">
            <div><span style="color:var(--muted)">目录代码:</span> <code>{{ provider.catalog_code || '—' }}</code></div>
            <div><span style="color:var(--muted)">Base URL:</span> {{ provider.base_url || '—' }}</div>
            <div><span style="color:var(--muted)">协议:</span> {{ provider.protocol }}</div>
            <div><span style="color:var(--muted)">Header Profile:</span> {{ provider.header_profile_code || '—' }}</div>
            <div><span style="color:var(--muted)">厂商:</span> {{ provider.vendor_name || '—' }}</div>
            <div><span style="color:var(--muted)">状态:</span> <span :class="['badge', provider.enabled ? 'badge-green' : 'badge-gray']">{{ provider.enabled ? '已启用' : '已禁用' }}</span></div>
            <div><span style="color:var(--muted)">最近检查:</span> {{ fmtTime(provider.health_checked_at) }}</div>
          </div>
        </div>

        <div class="card">
          <h3 style="margin:0 0 12px">模型 & 错误</h3>
          <div style="display:grid;grid-template-columns:1fr 1fr 1fr;gap:12px;font-size:13px">
            <div><span style="color:var(--muted)">可用模型:</span> <strong>{{ provider.available_model_count }}</strong></div>
            <div><span style="color:var(--muted)">不可用:</span> {{ provider.unavailable_model_count }}</div>
            <div><span style="color:var(--muted)">24h 错误率:</span> {{ (provider.error_rate_24h * 100).toFixed(1) }}%</div>
          </div>
        </div>
      </div>

      <!-- Credentials Tab -->
      <div v-if="activeTab === 'credentials'">
        <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
          <h3 style="margin:0">凭据列表 ({{ credentials.length }})</h3>
          <button class="btn btn-primary btn-sm" @click="openAddCred">+ 添加凭据</button>
        </div>
        <div style="overflow-x:auto">
          <table class="data-table" style="width:100%;font-size:12px">
            <thead>
              <tr>
                <th>凭据</th>
                <th>状态</th>
                <th>探活</th>
                <th>并发</th>
                <th>有效期</th>
                <th>用量</th>
                <th>标签</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="!credentials.length">
                <td colspan="8" style="text-align:center;padding:24px;color:var(--muted)">暂无凭据</td>
              </tr>
              <tr v-for="c in credentials" :key="c.id">
                <td>
                  <input v-model="c.label" class="compact-input" placeholder="标签" />
                  <div style="font-size:11px;color:var(--muted)">#{{ c.id }} · {{ c.trust_level || '—' }}</div>
                </td>
                <td>
                  <select v-model="c.status" class="compact-input">
                    <option v-for="s in credentialStatuses" :key="s.value" :value="s.value">{{ s.label }}</option>
                  </select>
                  <span :class="['badge', statusBadge(c.status)]">{{ c.status }}</span>
                </td>
                <td>
                  <span :class="['badge', healthBadge(c.health_status)]">{{ healthLabel(c.health_status) }}</span>
                  <div style="font-size:11px;color:var(--muted)">{{ fmtTime(c.health_checked_at) }}</div>
                  <div v-if="c.health_error" style="font-size:11px;color:var(--danger);max-width:200px;word-break:break-all">{{ c.health_error }}</div>
                </td>
                <td>
                  <input v-model.number="c.concurrency_limit" type="number" min="1" class="compact-input" style="max-width:80px" placeholder="不限" />
                </td>
                <td>
                  <input :value="asDateInput(c.effective_at)" type="datetime-local" class="compact-input"
                    @input="c.effective_at = ($event.target as HTMLInputElement).value ? new Date(($event.target as HTMLInputElement).value).toISOString() : null" />
                  <input :value="asDateInput(c.expires_at)" type="datetime-local" class="compact-input"
                    @input="c.expires_at = ($event.target as HTMLInputElement).value ? new Date(($event.target as HTMLInputElement).value).toISOString() : null" />
                </td>
                <td>
                  <div>{{ c.total_requests || 0 }} 次 · {{ money(c.total_cost_usd) }}</div>
                </td>
                <td>
                  <input :value="(c.tags ?? []).join(', ')" class="compact-input" placeholder="tag1, tag2"
                    @input="c.tags = ($event.target as HTMLInputElement).value.split(',').map((t:string) => t.trim()).filter(Boolean)" />
                </td>
                <td>
                  <div style="display:flex;gap:4px;flex-wrap:wrap">
                    <button class="btn btn-primary btn-sm" @click="saveCred(c)" :disabled="credSaving[c.id]">
                      {{ credSaving[c.id] ? '保存中' : '保存' }}
                    </button>
                    <button class="btn btn-sm" @click="checkCred(c)" :disabled="credChecking[c.id]">
                      {{ credChecking[c.id] ? '检测中' : '检测' }}
                    </button>
                    <button class="btn btn-ghost btn-sm" @click="resetCredAvailability(c)">重置可用性</button>
                    <button class="btn btn-ghost btn-sm" @click="resetCredQuota(c)">重置配额</button>
                    <button class="btn btn-sm" @click="delCred(c)">停用</button>
                  </div>
                  <div v-if="credSaveMsgs[c.id]" style="font-size:11px;color:var(--danger);margin-top:4px">{{ credSaveMsgs[c.id] }}</div>
                  <div v-if="credCheckMsgs[c.id]" style="font-size:11px;color:var(--muted);margin-top:4px">{{ credCheckMsgs[c.id] }}</div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Models Tab -->
      <div v-if="activeTab === 'models'">
        <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
          <h3 style="margin:0">模型列表 ({{ modelOffers.length }})</h3>
          <button class="btn btn-ghost btn-sm" @click="loadModels" :disabled="modelsLoading">刷新</button>
        </div>
        <div v-if="modelsError" class="alert alert-danger" style="margin-bottom:12px">{{ modelsError }}</div>
        <div v-if="modelsLoading" style="text-align:center;padding:24px;color:var(--muted)">加载中...</div>
        <div v-else-if="modelOffers.length === 0" class="card" style="text-align:center;padding:24px;color:var(--muted)">
          暂无模型数据
        </div>
        <div v-else style="overflow-x:auto">
          <table class="data-table" style="width:100%;font-size:12px">
            <thead>
              <tr>
                <th>模型名称</th>
                <th>标准化名称</th>
                <th>状态</th>
                <th>P95 延迟</th>
                <th>成功率</th>
                <th>来源</th>
                <th>最后可见</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="o in modelOffers" :key="o.id">
                <td><code style="font-size:11px">{{ o.raw_model_name }}</code></td>
                <td><code style="font-size:11px">{{ o.standardized_name || '—' }}</code></td>
                <td>
                  <span :class="['badge', o.available ? 'badge-green' : 'badge-gray']">
                    {{ o.available ? '可用' : '不可用' }}
                  </span>
                </td>
                <td>{{ o.p95_latency_ms != null ? o.p95_latency_ms + 'ms' : '—' }}</td>
                <td>{{ o.success_rate != null ? (o.success_rate * 100).toFixed(1) + '%' : '—' }}</td>
                <td>{{ o.source || '—' }}</td>
                <td style="font-size:11px">{{ fmtTimeShort(o.last_seen_at) }}</td>
                <td>
                  <button class="btn btn-ghost btn-sm" @click="toggleModel(o)">
                    {{ o.available ? '禁用' : '启用' }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Logs Tab -->
      <div v-if="activeTab === 'logs'">
        <div style="display:flex;gap:12px;flex-wrap:wrap;align-items:center;margin-bottom:12px">
          <input class="input" v-model="logsKeyword" placeholder="搜索模型名..." style="flex:1;max-width:300px" @keyup.enter="logsPage=1;loadLogs()" />
          <button class="btn btn-primary btn-sm" @click="logsPage=1;loadLogs()" :disabled="logsLoading">
            {{ logsLoading ? '加载中...' : '查询' }}
          </button>
          <span style="color:var(--muted);font-size:12px">共 {{ logsTotal }} 条</span>
        </div>
        <div v-if="logsError" class="alert alert-danger" style="margin-bottom:12px">{{ logsError }}</div>
        <div v-if="logsLoading" style="text-align:center;padding:24px;color:var(--muted)">加载中...</div>
        <div v-else-if="logs.length === 0" class="card" style="text-align:center;padding:24px;color:var(--muted)">
          暂无日志
        </div>
        <div v-else style="overflow-x:auto">
          <table class="data-table" style="width:100%;font-size:12px">
            <thead>
              <tr>
                <th>时间</th>
                <th>凭据</th>
                <th>客户端模型</th>
                <th>出站模型</th>
                <th>成功</th>
                <th>错误类型</th>
                <th>Token (入/出)</th>
                <th>费用</th>
                <th>延迟</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(l, i) in logs" :key="i">
                <td style="font-size:11px">{{ fmtTime(l.ts) }}</td>
                <td style="color:var(--muted)">#{{ l.credential_id }}</td>
                <td><code style="font-size:11px">{{ l.client_model || '—' }}</code></td>
                <td><code style="font-size:11px">{{ l.outbound_model || '—' }}</code></td>
                <td>
                  <span :class="['badge', l.success ? 'badge-green' : 'badge-red']">
                    {{ l.success ? 'OK' : 'FAIL' }}
                  </span>
                </td>
                <td style="color:var(--muted)">{{ l.error_kind || '—' }}</td>
                <td>{{ l.prompt_tokens ?? '—' }} / {{ l.completion_tokens ?? '—' }}</td>
                <td>{{ l.cost_usd != null ? '$' + Number(l.cost_usd).toFixed(6) : '—' }}</td>
                <td>{{ l.latency_ms != null ? l.latency_ms + 'ms' : '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="logsTotal > 50" style="display:flex;gap:12px;align-items:center;margin-top:12px">
          <button class="btn btn-ghost btn-sm" :disabled="logsPage <= 1" @click="logsPage--;loadLogs()">上一页</button>
          <span style="color:var(--muted)">{{ logsPage }} / {{ Math.ceil(logsTotal / 50) }}</span>
          <button class="btn btn-ghost btn-sm" :disabled="logsPage >= Math.ceil(logsTotal / 50)" @click="logsPage++;loadLogs()">下一页</button>
        </div>
      </div>

      <!-- Settings Tab -->
      <div v-if="activeTab === 'settings'">
        <div class="card" style="margin-bottom:16px">
          <h3 style="margin:0 0 12px">编辑提供商</h3>
          <div style="display:grid;grid-template-columns:1fr 1fr;gap:16px;max-width:600px">
            <div class="form-group">
              <label>显示名</label>
              <input class="input" v-model="editForm.display_name" />
            </div>
            <div class="form-group">
              <label>Base URL</label>
              <input class="input" v-model="editForm.base_url" />
            </div>
            <div class="form-group">
              <label>协议</label>
              <select class="input" v-model="editForm.protocol">
                <option value="openai-completions">OpenAI Completions</option>
                <option value="openai-responses">OpenAI Responses</option>
                <option value="anthropic-messages">Anthropic Messages</option>
                <option value="gemini-generate">Gemini Generate</option>
              </select>
            </div>
            <div class="form-group">
              <label>类型</label>
              <select class="input" v-model="editForm.kind">
                <option value="cloud">Cloud</option>
                <option value="local">Local</option>
              </select>
            </div>
            <div class="form-group">
              <label>分类</label>
              <select class="input" v-model="editForm.category">
                <option value="official">Official</option>
                <option value="official_proxy">Official Proxy</option>
                <option value="third_party_relay">Third Party Relay</option>
                <option value="aggregator">Aggregator</option>
                <option value="self_host">Self Host</option>
              </select>
            </div>
            <div class="form-group">
              <label>折扣率</label>
              <input class="input" type="number" step="0.01" min="0" max="1" v-model.number="editForm.discount_rate" />
            </div>
            <div class="form-group" style="grid-column:1/-1">
              <label>备注</label>
              <input class="input" v-model="editForm.notes" placeholder="内部说明" />
            </div>
          </div>
          <div style="display:flex;gap:8px;align-items:center">
            <button class="btn btn-primary btn-sm" @click="saveEdit" :disabled="editSaving">
              {{ editSaving ? '保存中...' : '保存' }}
            </button>
            <span v-if="editError" style="color:var(--danger);font-size:13px">{{ editError }}</span>
          </div>
        </div>

        <div class="card" style="margin-bottom:16px">
          <h3 style="margin:0 0 12px">批量操作</h3>
          <div style="display:flex;gap:8px;align-items:center">
            <button class="btn btn-ghost btn-sm" @click="handleBatchRecover" :disabled="batchRecovering">
              {{ batchRecovering ? '恢复中...' : '批量恢复冷却凭据' }}
            </button>
            <span v-if="batchRecoverMsg" style="color:var(--muted);font-size:13px">{{ batchRecoverMsg }}</span>
          </div>
        </div>

        <div class="card">
          <h3 style="margin:0 0 12px">提供商信息</h3>
          <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;font-size:13px">
            <div><span style="color:var(--muted)">ID:</span> {{ provider.id }}</div>
            <div><span style="color:var(--muted)">代码:</span> <code>{{ provider.code }}</code></div>
            <div><span style="color:var(--muted)">目录代码:</span> <code>{{ provider.catalog_code || '—' }}</code></div>
            <div><span style="color:var(--muted)">协议:</span> <code>{{ provider.protocol }}</code></div>
            <div><span style="color:var(--muted)">类型:</span> {{ provider.kind }} / {{ provider.category }}</div>
            <div><span style="color:var(--muted)">出境配置:</span> {{ provider.egress_profile || '—' }}</div>
            <div><span style="color:var(--muted)">国产:</span> {{ provider.domestic ? '是' : '否' }}</div>
            <div><span style="color:var(--muted)">折扣率:</span> {{ provider.discount_rate || '—' }}</div>
            <div><span style="color:var(--muted)">厂商:</span> {{ provider.vendor_name || '—' }}</div>
          </div>
        </div>
      </div>

      <!-- Diagnosis Tab -->
      <div v-if="activeTab === 'diagnosis'">
        <div style="display:flex;gap:12px;align-items:center;margin-bottom:16px">
          <button class="btn btn-primary" @click="runDiagnose" :disabled="diagLoading">
            {{ diagLoading ? (diagPolling ? '诊断中...' : '启动中...') : '运行完整诊断' }}
          </button>
          <span v-if="diagPolling" style="color:var(--muted);font-size:12px">正在探测凭据，请稍候...</span>
        </div>
        <div v-if="diagError" class="alert alert-danger" style="margin-bottom:12px">{{ diagError }}</div>

        <div v-if="diagPolling" style="text-align:center;padding:40px;color:var(--muted)">
          诊断任务执行中，通常需要 30-60 秒...
        </div>

        <template v-if="diagResult">
          <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:12px;margin-bottom:16px">
            <div class="card">
              <div style="font-size:12px;color:var(--muted)">凭据总数</div>
              <div style="font-size:20px;font-weight:600">{{ diagResult.summary?.total_credentials ?? 0 }}</div>
              <div style="font-size:11px;color:var(--muted)">
                <span style="color:#4caf50">健康 {{ diagResult.summary?.healthy ?? 0 }}</span> ·
                <span style="color:#f0b429">降级 {{ diagResult.summary?.degraded ?? 0 }}</span> ·
                <span style="color:#f44336">不可达 {{ diagResult.summary?.unreachable ?? 0 }}</span>
              </div>
            </div>
            <div class="card">
              <div style="font-size:12px;color:var(--muted)">模型覆盖率</div>
              <div style="font-size:20px;font-weight:600">{{ (diagResult.summary?.models_coverage_pct ?? 0).toFixed(1) }}%</div>
            </div>
            <div class="card">
              <div style="font-size:12px;color:var(--muted)">平均延迟</div>
              <div style="font-size:20px;font-weight:600">{{ (diagResult.summary?.avg_latency_ms ?? 0).toFixed(0) }} ms</div>
            </div>
          </div>

          <div v-if="diagResult.error_classification" style="margin-bottom:16px">
            <h4 style="margin:0 0 8px;font-size:14px">24h 错误分类</h4>
            <div style="display:flex;gap:16px;flex-wrap:wrap;font-size:13px">
              <span v-if="diagResult.error_classification.auth_errors">认证: {{ diagResult.error_classification.auth_errors }}</span>
              <span v-if="diagResult.error_classification.rate_limit_errors">限流: {{ diagResult.error_classification.rate_limit_errors }}</span>
              <span v-if="diagResult.error_classification.timeout_errors">超时: {{ diagResult.error_classification.timeout_errors }}</span>
              <span v-if="diagResult.error_classification.model_not_found_errors">模型不存在: {{ diagResult.error_classification.model_not_found_errors }}</span>
              <span v-if="diagResult.error_classification.other_errors">其他: {{ diagResult.error_classification.other_errors }}</span>
              <span v-if="!diagResult.error_classification.auth_errors && !diagResult.error_classification.rate_limit_errors && !diagResult.error_classification.timeout_errors && !diagResult.error_classification.model_not_found_errors && !diagResult.error_classification.other_errors" style="color:var(--muted)">无错误</span>
            </div>
          </div>

          <div v-if="diagResult.health_scores?.length" style="margin-bottom:16px">
            <h4 style="margin:0 0 8px;font-size:14px">凭据健康分数</h4>
            <div style="display:flex;gap:12px;flex-wrap:wrap">
              <div v-for="s in diagResult.health_scores" :key="s.credential_id" style="display:flex;align-items:center;gap:6px;min-width:120px">
                <span style="color:var(--muted);font-size:11px">#{{ s.credential_id }}</span>
                <div style="flex:1;height:6px;background:var(--bg-subtle);border-radius:3px;overflow:hidden">
                  <div :style="{ width: s.score + '%', background: scoreColor(s.score), height: '100%', borderRadius: '3px' }"></div>
                </div>
                <span :style="{ color: scoreColor(s.score), fontWeight: 600, fontSize: '13px', minWidth: '24px' }">{{ s.score.toFixed(0) }}</span>
              </div>
            </div>
          </div>

          <div v-if="diagResult.credentials?.length">
            <h4 style="margin:0 0 8px;font-size:14px">凭据详细探测</h4>
            <div style="overflow-x:auto">
              <table class="data-table" style="width:100%;font-size:12px">
                <thead>
                  <tr>
                    <th>凭据</th>
                    <th>状态</th>
                    <th>熔断</th>
                    <th>Models 探测</th>
                    <th>Chat 探测</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="cd in diagResult.credentials" :key="cd.credential_id">
                    <td>#{{ cd.credential_id }} {{ cd.label }}</td>
                    <td>
                      <span :class="['badge', cd.status === 'active' ? 'badge-green' : 'badge-red']">{{ cd.status }}</span>
                    </td>
                    <td>
                      <span :class="['badge', cd.circuit_state === 'closed' ? 'badge-green' : 'badge-amber']">{{ cd.circuit_state }}</span>
                    </td>
                    <td>
                      <span v-if="cd.models_probe?.error" class="badge badge-red">失败</span>
                      <span v-else class="badge" :class="cd.models_probe?.status_code === 200 ? 'badge-green' : 'badge-red'">
                        {{ cd.models_probe?.status_code || '—' }} · {{ cd.models_probe?.models_count ?? 0 }} 模型 · {{ cd.models_probe?.latency_ms ?? 0 }}ms
                      </span>
                    </td>
                    <td>
                      <span v-if="cd.chat_probe?.error" class="badge badge-red">失败</span>
                      <span v-else class="badge" :class="cd.chat_probe?.status_code === 200 ? 'badge-green' : 'badge-red'">
                        {{ cd.chat_probe?.status_code || '—' }} · {{ cd.chat_probe?.latency_ms ?? 0 }}ms
                      </span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </template>

        <div v-if="!diagLoading && !diagPolling && !diagResult && !diagError" class="card" style="text-align:center;padding:24px;color:var(--muted)">
          点击"运行完整诊断"开始探测凭据健康状态
        </div>
      </div>

      <!-- Settings Tab -->
      <div v-if="activeTab === 'settings'">
        <div class="card" style="margin-bottom:16px">
          <h3 style="margin:0 0 12px">编辑提供商</h3>
          <div style="display:grid;grid-template-columns:1fr 1fr;gap:16px;max-width:600px">
            <div class="form-group">
              <label>显示名</label>
              <input class="input" v-model="editForm.display_name" />
            </div>
            <div class="form-group">
              <label>Base URL</label>
              <input class="input" v-model="editForm.base_url" />
            </div>
            <div class="form-group">
              <label>协议</label>
              <select class="input" v-model="editForm.protocol">
                <option value="openai-completions">OpenAI Completions</option>
                <option value="openai-responses">OpenAI Responses</option>
                <option value="anthropic-messages">Anthropic Messages</option>
                <option value="gemini-generate">Gemini Generate</option>
              </select>
            </div>
            <div class="form-group">
              <label>类型</label>
              <select class="input" v-model="editForm.kind">
                <option value="cloud">Cloud</option>
                <option value="local">Local</option>
              </select>
            </div>
            <div class="form-group">
              <label>分类</label>
              <select class="input" v-model="editForm.category">
                <option value="official">Official</option>
                <option value="official_proxy">Official Proxy</option>
                <option value="third_party_relay">Third Party Relay</option>
                <option value="aggregator">Aggregator</option>
                <option value="self_host">Self Host</option>
              </select>
            </div>
            <div class="form-group">
              <label>折扣率</label>
              <input class="input" type="number" step="0.01" min="0" max="1" v-model.number="editForm.discount_rate" />
            </div>
            <div class="form-group" style="grid-column:1/-1">
              <label>备注</label>
              <input class="input" v-model="editForm.notes" placeholder="内部说明" />
            </div>
          </div>
          <div style="display:flex;gap:8px;align-items:center">
            <button class="btn btn-primary btn-sm" @click="saveEdit" :disabled="editSaving">
              {{ editSaving ? '保存中...' : '保存' }}
            </button>
            <span v-if="editError" style="color:var(--danger);font-size:13px">{{ editError }}</span>
          </div>
        </div>

        <div class="card" style="margin-bottom:16px">
          <h3 style="margin:0 0 12px">批量操作</h3>
          <div style="display:flex;gap:8px;align-items:center">
            <button class="btn btn-ghost btn-sm" @click="handleBatchRecover" :disabled="batchRecovering">
              {{ batchRecovering ? '恢复中...' : '批量恢复冷却凭据' }}
            </button>
            <span v-if="batchRecoverMsg" style="color:var(--muted);font-size:13px">{{ batchRecoverMsg }}</span>
          </div>
        </div>

        <div class="card">
          <h3 style="margin:0 0 12px">提供商信息</h3>
          <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;font-size:13px">
            <div><span style="color:var(--muted)">ID:</span> {{ provider.id }}</div>
            <div><span style="color:var(--muted)">代码:</span> <code>{{ provider.code }}</code></div>
            <div><span style="color:var(--muted)">目录代码:</span> <code>{{ provider.catalog_code || '—' }}</code></div>
            <div><span style="color:var(--muted)">协议:</span> <code>{{ provider.protocol }}</code></div>
            <div><span style="color:var(--muted)">类型:</span> {{ provider.kind }} / {{ provider.category }}</div>
            <div><span style="color:var(--muted)">出境配置:</span> {{ provider.egress_profile || '—' }}</div>
            <div><span style="color:var(--muted)">国产:</span> {{ provider.domestic ? '是' : '否' }}</div>
            <div><span style="color:var(--muted)">折扣率:</span> {{ provider.discount_rate || '—' }}</div>
            <div><span style="color:var(--muted)">厂商:</span> {{ provider.vendor_name || '—' }}</div>
          </div>
        </div>
      </div>
    </template>

    <!-- Edit Modal -->
    <div v-if="showEditModal" class="modal-overlay" @click.self="showEditModal = false">
      <div class="modal" style="max-width:500px">
        <h3>编辑提供商</h3>
        <div v-if="editError" class="alert alert-danger">{{ editError }}</div>
        <div class="form-group">
          <label>显示名称</label>
          <input class="input" v-model="editForm.display_name" />
        </div>
        <div class="form-group">
          <label>Base URL</label>
          <input class="input" v-model="editForm.base_url" />
        </div>
        <div class="form-group">
          <label>协议</label>
          <select class="input" v-model="editForm.protocol">
            <option value="openai-completions">OpenAI Completions</option>
            <option value="openai-responses">OpenAI Responses</option>
            <option value="anthropic-messages">Anthropic Messages</option>
            <option value="gemini-generate">Gemini Generate</option>
          </select>
        </div>
        <div class="form-group">
          <label>类型</label>
          <select class="input" v-model="editForm.kind">
            <option value="cloud">Cloud</option>
            <option value="local">Local</option>
          </select>
        </div>
        <div class="form-group">
          <label>分类</label>
          <select class="input" v-model="editForm.category">
            <option value="official">Official</option>
            <option value="official_proxy">Official Proxy</option>
            <option value="third_party_relay">Third Party Relay</option>
            <option value="aggregator">Aggregator</option>
            <option value="self_host">Self Host</option>
          </select>
        </div>
        <div class="form-group">
          <label>折扣率</label>
          <input class="input" type="number" step="0.01" min="0" max="1" v-model.number="editForm.discount_rate" />
        </div>
        <div class="form-group">
          <label>备注</label>
          <input class="input" v-model="editForm.notes" />
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" @click="showEditModal = false">取消</button>
          <button class="btn btn-primary" @click="saveEdit" :disabled="editSaving">
            {{ editSaving ? '保存中...' : '保存' }}
          </button>
        </div>
      </div>
    </div>

    <!-- Add Credential Modal -->
    <div v-if="showAddCredModal" class="modal-overlay" @click.self="showAddCredModal = false">
      <div class="modal" style="max-width:400px">
        <h3>添加凭据 — {{ provider?.display_name }}</h3>
        <div v-if="addCredError" class="alert alert-danger">{{ addCredError }}</div>
        <div class="form-group">
          <label>API Key</label>
          <input class="input" type="password" v-model="newCred.api_key" placeholder="sk-..." autocomplete="off" />
        </div>
        <div class="form-group">
          <label>标签（可选）</label>
          <input class="input" v-model="newCred.label" placeholder="如: 生产密钥" />
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" @click="showAddCredModal = false">取消</button>
          <button class="btn btn-primary" @click="saveAddCred" :disabled="addCredSaving">
            {{ addCredSaving ? '添加中...' : '添加' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.compact-input {
  width: 100%;
  min-width: 0;
  margin-bottom: 4px;
  padding: 4px 6px;
  font-size: 12px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
}
.compact-input:focus {
  border-color: var(--accent);
  outline: none;
}
</style>
