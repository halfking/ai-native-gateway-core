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

async function handleDeleteCred(credId: number) {
  if (!confirm('确认停用该凭据？')) return
  try {
    await deleteCredential(providerId.value, credId)
    await loadProvider()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '删除失败'
  }
}

async function handleCheckCred(credId: number) {
  try {
    await checkCredential(providerId.value, credId)
    setTimeout(() => loadProvider(), 3000)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '检测失败'
  }
}

function credStatusClass(status: string) {
  return status === 'active' ? 'badge-green' : status === 'disabled' || status === 'quota_expired' ? 'badge-red' : status === 'cooling' || status === 'degraded' ? 'badge-amber' : 'badge-gray'
}

function healthClass(status: string | null) {
  return status === 'healthy' ? 'badge-green' : status === 'warning' ? 'badge-amber' : status === 'unreachable' ? 'badge-red' : 'badge-gray'
}

function fmtTime(ts: string | null) {
  if (!ts) return '—'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
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
          <h3 style="margin:0">凭据 ({{ credentials.length }})</h3>
          <button class="btn btn-primary btn-sm" @click="openAddCred">+ 添加凭据</button>
        </div>
        <div class="card">
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
              <tr v-for="c in credentials" :key="c.id">
                <td>
                  <div><code style="font-size:11px">{{ c.label || '—' }}</code></div>
                  <div style="font-size:11px;color:var(--muted)">#{{ c.id }} · {{ c.trust_level || '—' }}</div>
                </td>
                <td>
                  <span :class="['badge', credStatusClass(c.status)]">{{ c.status }}</span>
                </td>
                <td>
                  <span :class="['badge', healthClass(c.health_status)]">{{ c.health_status || '未探测' }}</span>
                  <div style="font-size:11px;color:var(--muted)">{{ fmtTime(c.health_checked_at) }}</div>
                </td>
                <td>{{ c.concurrency_limit || '—' }}</td>
                <td style="font-size:11px">
                  <div>{{ c.effective_at ? fmtTime(c.effective_at) : '—' }}</div>
                  <div>{{ c.expires_at ? fmtTime(c.expires_at) : '—' }}</div>
                </td>
                <td style="font-size:11px">{{ c.total_requests || 0 }} 次</td>
                <td style="font-size:11px">{{ c.notes || '—' }}</td>
                <td>
                  <div style="display:flex;gap:4px">
                    <button class="btn btn-ghost btn-sm" @click="handleCheckCred(c.id)" title="检测">检测</button>
                    <button class="btn btn-ghost btn-sm" @click="handleDeleteCred(c.id)" title="停用">停用</button>
                  </div>
                </td>
              </tr>
              <tr v-if="credentials.length === 0">
                <td colspan="8" style="text-align:center;padding:24px;color:var(--muted)">暂无凭据</td>
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
      <div class="modal" style="max-width:500px">
        <h3>添加凭据</h3>
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
            {{ addCredSaving ? '保存中...' : '添加' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
