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
  type Provider,
  type ProviderCredential,
  type CredentialCheckResult,
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
const editForm = ref({ display_name: '', base_url: '', protocol: '', notes: '' })
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

function openEdit() {
  if (!provider.value) return
  editForm.value = {
    display_name: provider.value.display_name || '',
    base_url: provider.value.base_url || '',
    protocol: provider.value.protocol || '',
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
        <button v-for="tab in (['overview','credentials','models','logs','settings'] as const)"
                :key="tab"
                :class="['btn btn-ghost btn-sm', { 'btn-primary': activeTab === tab }]"
                @click="activeTab = tab">
          {{ { overview: '概览', credentials: '凭据', models: '模型', logs: '请求日志', settings: '设置' }[tab] }}
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
        <div class="card">
          <h3 style="margin:0 0 12px">模型列表</h3>
          <p style="color:var(--muted);font-size:13px">模型管理功能开发中...</p>
        </div>
      </div>

      <!-- Logs Tab -->
      <div v-if="activeTab === 'logs'">
        <div class="card">
          <h3 style="margin:0 0 12px">请求日志</h3>
          <p style="color:var(--muted);font-size:13px">日志筛选功能开发中...</p>
        </div>
      </div>

      <!-- Settings Tab -->
      <div v-if="activeTab === 'settings'">
        <div class="card">
          <h3 style="margin:0 0 12px">设置</h3>
          <div style="display:grid;grid-template-columns:1fr 1fr;gap:16px;max-width:600px">
            <div class="form-group">
              <label>显示名称</label>
              <input class="input" v-model="provider.display_name" disabled />
            </div>
            <div class="form-group">
              <label>Base URL</label>
              <input class="input" v-model="provider.base_url" disabled />
            </div>
            <div class="form-group">
              <label>协议</label>
              <input class="input" v-model="provider.protocol" disabled />
            </div>
            <div class="form-group">
              <label>备注</label>
              <input class="input" v-model="provider.notes" disabled />
            </div>
          </div>
          <button class="btn btn-primary btn-sm" style="margin-top:16px" @click="openEdit">编辑</button>
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
