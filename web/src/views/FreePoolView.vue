<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import {
  getFreePoolStatus,
  getFreePoolMethods,
  getFreePoolKeys,
  getFreePoolSignupHub,
  addFreePoolKey,
  registerFreeProvider,
  importFreePoolEnv,
  discoverFreePool,
  bridgeFreePoolOAuth,
  bootstrapFreePool,
  probeFreePoolCredential,
  quickEntryFreePool,
  createFreePoolTempEmail,
  pollFreePoolTempEmail,
  type FreePoolStatusResponse,
  type FreePoolEntry,
  type FreePoolCatalogEntry,
  type FreePoolModelEntry,
  type FreePoolMethod,
  type FreePoolAuditRule,
  type FreePoolKeyEntry,
  type SignupHubResponse,
  type SignupPlatformEntry,
} from '../api'

const poolData  = ref<FreePoolStatusResponse | null>(null)
const poolKeys  = ref<FreePoolKeyEntry[]>([])
const methodsData = ref<{ methods: FreePoolMethod[]; audit_rules: FreePoolAuditRule[]; scheduler: { interval_sec: number; last_result: Record<string, unknown> } } | null>(null)
const loading   = ref(false)
const syncing   = ref(false)
const error     = ref('')
const message   = ref('')
const modelQuery = ref('')
const activeTab = ref<'models' | 'providers' | 'catalog' | 'keys' | 'guide' | 'assistant'>('assistant')

const showAddForm = ref(false)
const showKeyForm = ref(false)
const signupHub = ref<SignupHubResponse | null>(null)
const hubCategory = ref('all')
const hubQuery = ref('')

const quickEntry = ref({
  signup_url: '',
  base_url: '',
  api_key: '',
  display_name: '',
  catalog_code: '',
  source: 'signup',
  source_detail: '',
  platform_id: '',
})
const quickProbing = ref(false)
const quickSaving = ref(false)
const probeResult = ref<Record<string, unknown> | null>(null)

const tempEmail = ref<{ address: string; password: string; token: string; web_url: string } | null>(null)
const tempEmailLoading = ref(false)
const tempInbox = ref<Array<{ id: string; from?: string; subject?: string; intro?: string }>>([])
const tempPolling = ref(false)
const keySubmitting = ref(false)
const newKey = ref({
  catalog_code: 'openrouter-free',
  api_key: '',
  source: 'signup',
  source_detail: '',
  label: '',
})
const submitting   = ref(false)
const newProvider = ref({
  catalog_code: '',
  display_name: '',
  base_url: '',
  protocol: 'openai-completions',
  api_key: '',
  models: '',
})

const catalog = computed(() => poolData.value?.catalog ?? [])
const liveModels = computed(() => poolData.value?.models ?? [])

const filteredModels = computed(() => {
  const q = modelQuery.value.trim().toLowerCase()
  if (!q) return liveModels.value
  return liveModels.value.filter(m =>
    m.raw_model_name.toLowerCase().includes(q)
    || m.provider_name.toLowerCase().includes(q)
    || m.catalog_code.toLowerCase().includes(q),
  )
})

const routableModels = computed(() => liveModels.value.filter(m => m.routable))

const catalogSummary = computed(() => {
  const items = catalog.value
  const registered = items.filter(c => c.pool_registered).length
  const templateModels = items.reduce((n, c) => n + (c.model_count_template || 0), 0)
  return { total: items.length, registered, templateModels }
})

const hubPlatforms = computed(() => {
  const rows = signupHub.value?.platforms ?? []
  const q = hubQuery.value.trim().toLowerCase()
  return rows.filter(p => {
    if (hubCategory.value !== 'all' && p.category !== hubCategory.value) return false
    if (!q) return true
    return (
      p.name.toLowerCase().includes(q)
      || p.catalog_code.toLowerCase().includes(q)
      || p.notes.toLowerCase().includes(q)
      || (p.tags && p.tags.some(t => t.toLowerCase().includes(q)))
    )
  })
})

const hubCategories = computed(() => signupHub.value?.categories ?? [])

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [status, methods, keysRes, hub] = await Promise.all([
      getFreePoolStatus(),
      getFreePoolMethods(),
      getFreePoolKeys(),
      getFreePoolSignupHub(),
    ])
    poolData.value = status
    methodsData.value = methods
    poolKeys.value = keysRes.keys
    signupHub.value = hub
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function runBootstrap() {
  syncing.value = true
  error.value = ''
  message.value = ''
  try {
    const res = await bootstrapFreePool()
    const mirror = (res.mirror as { registered?: number })?.registered ?? 0
    const discover = (res.discover as { registered?: number })?.registered ?? 0
    message.value = `一键建设完成：镜像 ${mirror} 个 Provider，发现/更新 ${discover} 个`
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '一键建设失败'
  } finally {
    syncing.value = false
  }
}

async function runBridgeOAuth() {
  syncing.value = true
  error.value = ''
  message.value = ''
  try {
    const res = await bridgeFreePoolOAuth()
    message.value = `OAuth 桥接：${res.registered ?? 0} 个 OAuth 凭证已注入池子`
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'OAuth 桥接失败'
  } finally {
    syncing.value = false
  }
}

async function runDiscover() {
  syncing.value = true
  error.value = ''
  message.value = ''
  try {
    const res = await discoverFreePool()
    message.value = `自动学习完成：本轮注册/更新 ${res.registered ?? 0} 个 Provider`
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '自动学习失败'
  } finally {
    syncing.value = false
  }
}

async function runImportEnv() {
  syncing.value = true
  error.value = ''
  message.value = ''
  try {
    const res = await importFreePoolEnv()
    message.value = `环境变量导入：${res.registered ?? 0} 个 Key 已注入池子`
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '环境变量导入失败'
  } finally {
    syncing.value = false
  }
}

function useTemplate(tpl: FreePoolCatalogEntry) {
  newProvider.value.catalog_code = tpl.catalog_code
  newProvider.value.display_name = tpl.display_name
  newProvider.value.base_url = tpl.base_url
  newProvider.value.models = (tpl.models || []).join(',')
  showAddForm.value = true
}

function fillFromPlatform(p: SignupPlatformEntry) {
  quickEntry.value.platform_id = p.id
  quickEntry.value.signup_url = p.signup_url
  quickEntry.value.base_url = p.base_url
  quickEntry.value.catalog_code = p.catalog_code
  quickEntry.value.display_name = p.display_name
  quickEntry.value.source_detail = p.name
  activeTab.value = 'assistant'
  probeResult.value = null
}

function openUrl(url: string) {
  if (url) window.open(url, '_blank', 'noopener,noreferrer')
}

async function runQuickProbe() {
  if (!quickEntry.value.base_url.trim()) {
    error.value = '请填写 Base URL'
    return
  }
  quickProbing.value = true
  error.value = ''
  message.value = ''
  try {
    const res = await probeFreePoolCredential({
      base_url: quickEntry.value.base_url.trim(),
      api_key: quickEntry.value.api_key.trim() || undefined,
    })
    probeResult.value = res.probe
    message.value = res.probe?.ok
      ? `探活通过 · HTTP ${res.probe.status_code} · 模型 ${res.probe.model_count ?? 0} 个`
      : `探活未通过：${res.probe?.reason || res.probe?.error || 'unknown'}`
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '探活失败'
  } finally {
    quickProbing.value = false
  }
}

async function runQuickSave(probeFirst = true) {
  if (!quickEntry.value.base_url.trim()) {
    error.value = '请填写 Base URL'
    return
  }
  quickSaving.value = true
  error.value = ''
  message.value = ''
  try {
    const res = await quickEntryFreePool({
      signup_url: quickEntry.value.signup_url || undefined,
      base_url: quickEntry.value.base_url.trim(),
      api_key: quickEntry.value.api_key.trim() || undefined,
      display_name: quickEntry.value.display_name || undefined,
      catalog_code: quickEntry.value.catalog_code || undefined,
      source: quickEntry.value.source,
      source_detail: quickEntry.value.source_detail || undefined,
      platform_id: quickEntry.value.platform_id || undefined,
      probe_first: probeFirst,
      save: true,
    })
    probeResult.value = res.probe ?? null
    if (res.status === 'ok') {
      message.value = `凭据已入库 · catalog=${res.catalog_code} · credential #${res.credential_id ?? '—'}`
      quickEntry.value.api_key = ''
      await load()
    } else {
      error.value = res.error || `入库失败 (${res.status})`
    }
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '入库失败'
  } finally {
    quickSaving.value = false
  }
}

async function generateTempEmail() {
  tempEmailLoading.value = true
  error.value = ''
  try {
    const res = await createFreePoolTempEmail()
    if (!res.ok || !res.address) {
      error.value = res.error || '临时邮箱创建失败'
      return
    }
    tempEmail.value = {
      address: res.address,
      password: res.password || '',
      token: res.token || '',
      web_url: res.web_url || 'https://mail.tm/en/',
    }
    tempInbox.value = []
    message.value = `临时邮箱已生成：${res.address}`
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '临时邮箱创建失败'
  } finally {
    tempEmailLoading.value = false
  }
}

async function pollTempInbox() {
  if (!tempEmail.value?.token) return
  tempPolling.value = true
  try {
    const res = await pollFreePoolTempEmail(tempEmail.value.token)
    if (res.ok && res.messages) {
      tempInbox.value = res.messages
      message.value = `收件箱 ${res.total ?? res.messages.length} 封`
    } else {
      error.value = res.error || '拉取邮件失败'
    }
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '拉取邮件失败'
  } finally {
    tempPolling.value = false
  }
}

async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    message.value = '已复制到剪贴板'
  } catch {
    error.value = '复制失败，请手动选择复制'
  }
}

async function submitKey() {
  if (!newKey.value.catalog_code || (!newKey.value.api_key && newKey.value.source !== 'no_key')) {
    error.value = '请填写 catalog_code 和 api_key'
    return
  }
  keySubmitting.value = true
  error.value = ''
  message.value = ''
  try {
    await addFreePoolKey({
      catalog_code: newKey.value.catalog_code,
      api_key: newKey.value.api_key,
      source: newKey.value.source,
      source_detail: newKey.value.source_detail || undefined,
      label: newKey.value.label || undefined,
    })
    message.value = '凭据已加密写入数据库'
    showKeyForm.value = false
    newKey.value = { catalog_code: 'openrouter-free', api_key: '', source: 'signup', source_detail: '', label: '' }
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '写入失败'
  } finally {
    keySubmitting.value = false
  }
}

async function submitNew() {
  if (!newProvider.value.catalog_code || !newProvider.value.base_url) {
    error.value = '请填写 Catalog Code 和 Base URL'
    return
  }
  submitting.value = true
  error.value = ''
  message.value = ''
  try {
    const models = newProvider.value.models
      .split(',')
      .map(s => s.trim())
      .filter(Boolean)

    const res = await registerFreeProvider({
      catalog_code: newProvider.value.catalog_code,
      display_name: newProvider.value.display_name || newProvider.value.catalog_code,
      base_url: newProvider.value.base_url,
      protocol: newProvider.value.protocol,
      api_key: newProvider.value.api_key || undefined,
      models: models.length > 0 ? models : undefined,
    })
    message.value = `Provider 注册成功 (ID: ${res.provider_id})`
    showAddForm.value = false
    newProvider.value = {
      catalog_code: '',
      display_name: '',
      base_url: '',
      protocol: 'openai-completions',
      api_key: '',
      models: '',
    }
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '注册失败'
  } finally {
    submitting.value = false
  }
}

function statusBadgeClass(entry: FreePoolEntry | FreePoolModelEntry): string {
  if (entry.credential_status !== 'active') return 'badge-red'
  if (entry.availability_state === 'rate_limited' || entry.availability_state === 'cooling' || entry.availability_state === 'unreachable') return 'badge-orange'
  const quota = entry.quota_state
  if (quota === 'exhausted' || quota === 'balance_exhausted') return 'badge-orange'
  if ('routable' in entry && !entry.routable) return 'badge-orange'
  return 'badge-green'
}

function statusLabel(entry: FreePoolEntry): string {
  if (entry.credential_status !== 'active') return '已禁用'
  if (entry.availability_state === 'rate_limited') return '限流'
  if (entry.availability_state === 'cooling') return '冷却'
  if (entry.availability_state === 'unreachable') return '不可达'
  if (entry.quota_state === 'exhausted') return '配额用尽'
  return '可用'
}

function modelStatusLabel(m: FreePoolModelEntry): string {
  if (m.credential_status !== 'active') return '已禁用'
  if (!m.available) return '已关闭'
  if (!m.routable) return '不可路由'
  return '可路由'
}

function riskClass(risk: string): string {
  if (risk === 'high') return 'risk-high'
  if (risk === 'medium') return 'risk-medium'
  return 'risk-low'
}

function acquisitionLabel(mode: string): string {
  const map: Record<string, string> = {
    signup: '注册 Key',
    env: '环境变量',
    no_key: '无需 Key',
    oauth: 'OAuth',
    mirrored: '镜像',
    manual: '手动',
    discovered: '自动发现',
  }
  return map[mode] || mode
}

function categoryLabel(id: string): string {
  const row = hubCategories.value.find(c => c.id === id)
  return row?.label || id
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <h2>免费资源池</h2>
      <div style="display:flex;gap:8px;flex-wrap:wrap">
        <button class="btn btn-ghost" @click="load" :disabled="loading || syncing">刷新</button>
        <button class="btn btn-ghost" @click="runBootstrap" :disabled="loading || syncing">
          {{ syncing ? '处理中…' : '一键建设' }}
        </button>
        <button class="btn btn-ghost" @click="runBridgeOAuth" :disabled="loading || syncing">
          OAuth 桥接
        </button>
        <button class="btn btn-ghost" @click="runImportEnv" :disabled="loading || syncing">
          导入环境变量 Key
        </button>
        <button class="btn btn-ghost" @click="runDiscover" :disabled="loading || syncing">
          自动学习并注册
        </button>
        <button class="btn btn-primary" @click="activeTab = 'assistant'; showKeyForm = false; showAddForm = false">
          快速录入
        </button>
        <button class="btn btn-primary" @click="showKeyForm = !showKeyForm">
          {{ showKeyForm ? '取消' : '写入 DB 凭据' }}
        </button>
        <button class="btn btn-primary" @click="showAddForm = !showAddForm">
          {{ showAddForm ? '取消' : '添加 Provider' }}
        </button>
      </div>
    </div>
    <p style="color:var(--muted);margin-bottom:20px">
      管理免费模型资源池。已注册模型路由优先级最高（routing_tier = 9，composite_score = 0）。
      下方「免费模型清单」展示当前池内全部可用模型；「模板目录」展示已知免费 Provider 及其预期模型。
    </p>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="message" class="alert alert-success">{{ message }}</div>

    <div v-if="poolData" class="stat-row" style="margin-bottom:20px">
      <div class="stat-inline">
        <span class="stat-label">可路由模型</span>
        <span class="stat-value stat-ok">{{ poolData.stats.routable_models }}</span>
        <span class="stat-sub">/ {{ poolData.stats.free_models }} offer</span>
      </div>
      <div class="stat-inline">
        <span class="stat-label">可用 Provider</span>
        <span class="stat-value stat-ok">{{ poolData.stats.available_providers }}</span>
        <span class="stat-sub">/ {{ poolData.stats.total_providers }}</span>
      </div>
      <div class="stat-inline">
        <span class="stat-label">模板已接入</span>
        <span class="stat-value">{{ poolData.stats.catalog_registered }}</span>
        <span class="stat-sub">/ {{ poolData.stats.catalog_templates }} 模板</span>
      </div>
      <div class="stat-inline">
        <span class="stat-label">模板模型</span>
        <span class="stat-value">{{ catalogSummary.templateModels }}</span>
        <span class="stat-sub">目录已知</span>
      </div>
    </div>

    <div v-if="showKeyForm" class="card" style="margin-bottom:20px">
      <h3 style="margin-top:0">写入免费池凭据（加密存 DB）</h3>
      <p class="cell-muted" style="margin:0 0 12px">
        Key 写入 <code class="model-code">credentials.secret_ciphertext</code>，来源写入 <code class="model-code">acquisition_source / acquisition_detail</code>。
      </p>
      <div class="form-grid">
        <div class="form-item">
          <label>Catalog Code *</label>
          <select v-model="newKey.catalog_code" class="input">
            <option v-for="tpl in catalog" :key="tpl.catalog_code" :value="tpl.catalog_code">
              {{ tpl.display_name }} ({{ tpl.catalog_code }})
            </option>
          </select>
        </div>
        <div class="form-item">
          <label>来源类型</label>
          <select v-model="newKey.source" class="input">
            <option value="signup">signup（官方注册）</option>
            <option value="env">env（环境变量导入）</option>
            <option value="manual">manual（手动）</option>
            <option value="oauth">oauth</option>
            <option value="mirrored">mirrored</option>
          </select>
        </div>
        <div class="form-item" style="grid-column:1/-1">
          <label>API Key *</label>
          <input v-model="newKey.api_key" class="input" type="password" placeholder="sk-..." />
        </div>
        <div class="form-item">
          <label>来源说明</label>
          <input v-model="newKey.source_detail" class="input" placeholder="如 OpenRouter void / AIGoCode dev" />
        </div>
        <div class="form-item">
          <label>凭据标签</label>
          <input v-model="newKey.label" class="input" placeholder="可选，默认自动生成" />
        </div>
      </div>
      <div style="margin-top:12px;display:flex;gap:8px">
        <button class="btn btn-primary" :disabled="keySubmitting" @click="submitKey">
          {{ keySubmitting ? '写入中…' : '加密写入数据库' }}
        </button>
        <button class="btn btn-ghost" @click="showKeyForm = false">取消</button>
      </div>
    </div>

    <div v-if="showAddForm" class="card" style="margin-bottom:20px">
      <h3 style="margin-top:0">添加免费 Provider</h3>
      <div class="form-grid">
        <div class="form-item">
          <label>Catalog Code *</label>
          <input v-model="newProvider.catalog_code" class="input" placeholder="例如: zhipu-free" />
        </div>
        <div class="form-item">
          <label>显示名称</label>
          <input v-model="newProvider.display_name" class="input" placeholder="例如: Zhipu GLM Free" />
        </div>
        <div class="form-item" style="grid-column: 1 / -1">
          <label>Base URL *</label>
          <input v-model="newProvider.base_url" class="input" placeholder="https://api.example.com/v1" />
        </div>
        <div class="form-item">
          <label>协议</label>
          <select v-model="newProvider.protocol" class="input">
            <option value="openai-completions">openai-completions</option>
            <option value="openai-responses">openai-responses</option>
            <option value="anthropic-messages">anthropic-messages</option>
          </select>
        </div>
        <div class="form-item">
          <label>API Key</label>
          <input v-model="newProvider.api_key" class="input" type="password" placeholder="可选" />
        </div>
        <div class="form-item" style="grid-column: 1 / -1">
          <label>模型列表 (逗号分隔)</label>
          <input v-model="newProvider.models" class="input" placeholder="例如: gpt-4o-mini, gpt-3.5-turbo" />
        </div>
      </div>

      <div style="margin-top:16px">
        <h4 style="font-size:13px;margin-bottom:8px;color:var(--muted)">已知免费 Provider 模板：</h4>
        <div style="display:flex;gap:8px;flex-wrap:wrap">
          <button
            v-for="tpl in catalog"
            :key="tpl.catalog_code"
            class="btn btn-ghost btn-sm"
            @click="useTemplate(tpl)"
            style="font-size:12px"
            :title="tpl.pool_registered ? '已接入池子' : (tpl.env_configured ? '环境变量已配置' : (tpl.needs_key ? '需注册或配置环境变量' : '无需 Key'))"
          >
            {{ tpl.display_name }}{{ tpl.pool_registered ? ' ✓' : (tpl.env_configured ? ' 🔑' : '') }}
          </button>
        </div>
      </div>

      <div style="margin-top:16px;display:flex;gap:8px;align-items:center">
        <button class="btn btn-primary" @click="submitNew" :disabled="submitting">
          {{ submitting ? '注册中…' : '注册 Provider' }}
        </button>
        <button class="btn btn-ghost" @click="showAddForm = false" :disabled="submitting">取消</button>
      </div>
    </div>

    <div v-if="loading" class="empty">加载中…</div>

    <template v-else-if="poolData">
      <div class="tab-bar" style="margin-bottom:16px">
        <button class="tab-btn" :class="{ active: activeTab === 'assistant' }" @click="activeTab = 'assistant'">
          注册助手
        </button>
        <button class="tab-btn" :class="{ active: activeTab === 'models' }" @click="activeTab = 'models'">
          免费模型清单 ({{ liveModels.length }})
        </button>
        <button class="tab-btn" :class="{ active: activeTab === 'providers' }" @click="activeTab = 'providers'">
          Provider ({{ poolData.pool.length }})
        </button>
        <button class="tab-btn" :class="{ active: activeTab === 'catalog' }" @click="activeTab = 'catalog'">
          模板目录 ({{ catalog.length }})
        </button>
        <button class="tab-btn" :class="{ active: activeTab === 'keys' }" @click="activeTab = 'keys'">
          凭据来源 ({{ poolKeys.length }})
        </button>
        <button class="tab-btn" :class="{ active: activeTab === 'guide' }" @click="activeTab = 'guide'">
          获取方式与审计
        </button>
      </div>

      <!-- Assistant tab -->
      <div v-if="activeTab === 'assistant'" class="assistant-layout">
        <div class="card quick-entry-card">
          <h3 style="margin-top:0">快速录入凭据</h3>
          <p class="cell-muted" style="margin:0 0 12px">
            粘贴注册页、Base URL 与 API Key → 探活验证 → 加密写入数据库。也可从下方平台卡片一键填入。
          </p>
          <div class="form-grid">
            <div class="form-item" style="grid-column:1/-1">
              <label>注册 / 文档页 URL</label>
              <input v-model="quickEntry.signup_url" class="input" placeholder="https://openrouter.ai/signup" />
            </div>
            <div class="form-item" style="grid-column:1/-1">
              <label>Base URL *</label>
              <input v-model="quickEntry.base_url" class="input" placeholder="https://openrouter.ai/api/v1" />
            </div>
            <div class="form-item">
              <label>Catalog Code</label>
              <input v-model="quickEntry.catalog_code" class="input" placeholder="留空则自动生成" />
            </div>
            <div class="form-item">
              <label>显示名称</label>
              <input v-model="quickEntry.display_name" class="input" placeholder="Provider 显示名" />
            </div>
            <div class="form-item" style="grid-column:1/-1">
              <label>API Key</label>
              <input v-model="quickEntry.api_key" class="input" type="password" placeholder="sk-..." />
            </div>
            <div class="form-item">
              <label>来源类型</label>
              <select v-model="quickEntry.source" class="input">
                <option value="signup">signup（官方注册）</option>
                <option value="manual">manual（中转/手动）</option>
                <option value="env">env</option>
                <option value="discovered">discovered</option>
              </select>
            </div>
            <div class="form-item">
              <label>来源说明</label>
              <input v-model="quickEntry.source_detail" class="input" placeholder="如 AIGoCode VS Code 插件" />
            </div>
          </div>
          <div v-if="probeResult" class="probe-box">
            <strong>探活结果</strong>
            <pre>{{ JSON.stringify(probeResult, null, 2) }}</pre>
          </div>
          <div style="margin-top:12px;display:flex;gap:8px;flex-wrap:wrap">
            <button class="btn btn-ghost" :disabled="quickProbing || quickSaving" @click="runQuickProbe">
              {{ quickProbing ? '探活中…' : '仅探活验证' }}
            </button>
            <button class="btn btn-primary" :disabled="quickProbing || quickSaving" @click="runQuickSave(true)">
              {{ quickSaving ? '入库中…' : '探活并入库' }}
            </button>
            <button
              v-if="quickEntry.signup_url"
              class="btn btn-ghost"
              @click="openUrl(quickEntry.signup_url)"
            >打开注册页</button>
          </div>
        </div>

        <div class="card">
          <h3 style="margin-top:0">临时邮箱（注册用）</h3>
          <p class="cell-muted" style="margin:0 0 12px">
            一键生成 mail.tm 邮箱收验证码。注册完成后请尽快复制 Key 并入库。
          </p>
          <div style="display:flex;gap:8px;flex-wrap:wrap;margin-bottom:12px">
            <button class="btn btn-primary" :disabled="tempEmailLoading" @click="generateTempEmail">
              {{ tempEmailLoading ? '生成中…' : '生成临时邮箱' }}
            </button>
            <button
              v-if="tempEmail"
              class="btn btn-ghost"
              :disabled="tempPolling"
              @click="pollTempInbox"
            >{{ tempPolling ? '刷新中…' : '刷新收件箱' }}</button>
            <button v-if="tempEmail" class="btn btn-ghost" @click="openUrl(tempEmail.web_url)">打开 mail.tm</button>
          </div>
          <div v-if="tempEmail" class="temp-email-box">
            <div class="temp-row">
              <span class="cell-muted">地址</span>
              <code>{{ tempEmail.address }}</code>
              <button class="btn btn-ghost btn-sm" @click="copyText(tempEmail.address)">复制</button>
            </div>
            <div class="temp-row">
              <span class="cell-muted">密码</span>
              <code>{{ tempEmail.password }}</code>
              <button class="btn btn-ghost btn-sm" @click="copyText(tempEmail.password)">复制</button>
            </div>
          </div>
          <ul v-if="tempInbox.length" class="inbox-list">
            <li v-for="m in tempInbox" :key="m.id">
              <strong>{{ m.subject || '(无主题)' }}</strong>
              <div class="cell-muted">{{ m.from }} · {{ m.intro }}</div>
            </li>
          </ul>
          <div v-if="signupHub" class="tool-links" style="margin-top:16px">
            <span class="cell-muted">其他邮箱工具：</span>
            <button
              v-for="t in signupHub.tools.filter(x => x.tool_type === 'temp_email' && !x.builtin)"
              :key="t.id"
              class="btn btn-ghost btn-sm"
              @click="openUrl(t.url)"
            >{{ t.name }}</button>
          </div>
        </div>

        <div class="card platform-hub">
          <div class="section-header">
            <div>
              <h3 style="margin:0">免费 Token 平台导航</h3>
              <p class="cell-muted" style="margin:4px 0 0">官方免费层 + 中转平台 + 社区端点 · 点击打开注册或填入快速录入</p>
            </div>
            <input v-model="hubQuery" class="input search-input" placeholder="搜索平台…" />
          </div>
          <div class="hub-filters">
            <button
              class="tab-btn btn-sm"
              :class="{ active: hubCategory === 'all' }"
              @click="hubCategory = 'all'"
            >全部</button>
            <button
              v-for="cat in hubCategories"
              :key="cat.id"
              class="tab-btn btn-sm"
              :class="{ active: hubCategory === cat.id }"
              @click="hubCategory = cat.id"
            >{{ cat.label }}</button>
          </div>
          <div class="platform-grid">
            <div v-for="p in hubPlatforms" :key="p.id" class="platform-card">
              <div class="platform-head">
                <strong>{{ p.name }}</strong>
                <span class="badge" :class="p.pool_registered ? 'badge-green' : 'badge-gray'">
                  {{ p.pool_registered ? '已接入' : '未接入' }}
                </span>
              </div>
              <div class="cell-muted">{{ categoryLabel(p.category) }} · {{ p.difficulty }}</div>
              <code class="model-code" style="display:block;margin:6px 0">{{ p.base_url }}</code>
              <p v-if="p.notes" class="cell-muted" style="margin:0 0 8px">{{ p.notes }}</p>
              <div v-if="p.models_hint" class="cell-muted">模型：{{ p.models_hint }}</div>
              <div v-if="p.tags.length" class="tag-row">
                <span v-for="tag in p.tags" :key="tag" class="model-tag template">{{ tag }}</span>
              </div>
              <div class="platform-actions">
                <button class="btn btn-primary btn-sm" @click="fillFromPlatform(p)">填入录入</button>
                <button class="btn btn-ghost btn-sm" @click="openUrl(p.signup_url)">打开注册</button>
                <button
                  v-if="p.api_key_url && p.api_key_url !== p.signup_url"
                  class="btn btn-ghost btn-sm"
                  @click="openUrl(p.api_key_url)"
                >获取 Key</button>
              </div>
            </div>
          </div>
          <div v-if="signupHub?.workflow?.length" style="margin-top:20px">
            <h4 style="margin-bottom:8px">推荐工作流</h4>
            <ol class="guide-steps">
              <li v-for="step in signupHub.workflow" :key="step.step">
                <strong>{{ step.title }}</strong> — {{ step.detail }}
              </li>
            </ol>
          </div>
        </div>
      </div>

      <!-- Models tab -->
      <div v-if="activeTab === 'models'" class="card">
        <div class="section-header">
          <div>
            <h3 style="margin:0">当前池内免费模型</h3>
            <p class="cell-muted" style="margin:4px 0 0">
              可路由 {{ routableModels.length }} 个 · 调用时使用 raw_model_name
            </p>
          </div>
          <input v-model="modelQuery" class="input search-input" placeholder="搜索模型 / Provider / catalog…" />
        </div>

        <div v-if="filteredModels.length === 0" class="empty">暂无匹配的免费模型</div>
        <table v-else>
          <thead>
            <tr>
              <th>模型名称</th>
              <th>Provider</th>
              <th>Catalog</th>
              <th>Tier</th>
              <th>状态</th>
              <th>凭据</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in filteredModels" :key="m.offer_id">
              <td>
                <code class="model-code">{{ m.raw_model_name }}</code>
                <div v-if="m.standardized_name && m.standardized_name !== m.raw_model_name" class="cell-muted">
                  标准名: {{ m.standardized_name }}
                </div>
              </td>
              <td>{{ m.provider_name }}</td>
              <td><code style="font-size:11px">{{ m.catalog_code }}</code></td>
              <td><span class="tier-pill">T{{ m.routing_tier }}</span></td>
              <td>
                <span class="badge" :class="statusBadgeClass(m)">{{ modelStatusLabel(m) }}</span>
              </td>
              <td>
                <div class="cell-muted">{{ m.credential_label }}</div>
                <div class="cell-muted">#{{ m.credential_id }}</div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Providers tab -->
      <div v-if="activeTab === 'providers'" class="card">
        <h3 style="margin-top:0">Provider 列表</h3>
        <div v-if="poolData.pool.length === 0" class="empty">暂无免费 Provider</div>
        <table v-else>
          <thead>
            <tr>
              <th>Catalog / 名称</th>
              <th>凭据</th>
              <th>状态</th>
              <th>模型列表</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="entry in poolData.pool" :key="entry.credential_id">
              <td>
                <code style="font-size:11px">{{ entry.catalog_code }}</code>
                <div>{{ entry.provider_name }}</div>
              </td>
              <td>
                <div>{{ entry.credential_label }}</div>
                <div class="cell-muted">#{{ entry.credential_id }} · {{ entry.availability_state || 'ready' }}</div>
              </td>
              <td>
                <span class="badge" :class="statusBadgeClass(entry)">{{ statusLabel(entry) }}</span>
              </td>
              <td>
                <div v-if="entry.models && entry.models.length" class="model-tags">
                  <span
                    v-for="m in entry.models"
                    :key="m.offer_id"
                    class="model-tag"
                    :class="{ routable: m.routable, dim: !m.available }"
                    :title="m.routable ? '可路由' : '不可路由'"
                  >{{ m.raw_model_name }}</span>
                </div>
                <div v-else class="cell-muted">无模型 offer</div>
                <div class="cell-muted">免费 {{ entry.free_offers }} / 可用 {{ entry.available_offers }}</div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Catalog tab -->
      <div v-if="activeTab === 'catalog'" class="card">
        <h3 style="margin-top:0">已知免费 Provider 模板</h3>
        <p class="cell-muted" style="margin-top:0;margin-bottom:16px">
          模板列出的模型为官方/社区免费层预期模型；「已接入」表示当前池子中有 active 凭据。
        </p>
        <table>
          <thead>
            <tr>
              <th>Provider</th>
              <th>接入</th>
              <th>获取方式</th>
              <th>模板模型</th>
              <th>池内 live 模型</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="tpl in catalog" :key="tpl.catalog_code">
              <td>
                <div>{{ tpl.display_name }}</div>
                <code style="font-size:11px">{{ tpl.catalog_code }}</code>
                <div class="cell-muted">{{ tpl.base_url }}</div>
              </td>
              <td>
                <span class="badge" :class="tpl.pool_registered ? 'badge-green' : 'badge-gray'">
                  {{ tpl.pool_registered ? '已接入' : '未接入' }}
                </span>
                <div v-if="tpl.env_configured" class="cell-muted">环境变量已配置</div>
              </td>
              <td>
                <span class="acq-pill">{{ acquisitionLabel(tpl.acquisition_mode) }}</span>
                <div v-if="tpl.needs_key && tpl.env_vars.length" class="cell-muted">
                  {{ tpl.env_vars.join(' / ') }}
                </div>
                <div v-if="tpl.rpm_limit" class="cell-muted">~{{ tpl.rpm_limit }} RPM</div>
              </td>
              <td>
                <div class="model-tags">
                  <span v-for="name in (tpl.models || [])" :key="name" class="model-tag template">{{ name }}</span>
                </div>
              </td>
              <td>
                <div v-if="tpl.live_models.length" class="model-tags">
                  <span v-for="name in tpl.live_models" :key="name" class="model-tag routable">{{ name }}</span>
                </div>
                <span v-else class="cell-muted">—</span>
              </td>
              <td>
                <a v-if="tpl.signup_url" :href="tpl.signup_url" target="_blank" rel="noopener" class="btn btn-ghost btn-sm">注册</a>
                <button class="btn btn-ghost btn-sm" @click="useTemplate(tpl)">填入表单</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Keys tab -->
      <div v-if="activeTab === 'keys'" class="card">
        <h3 style="margin-top:0">数据库中的免费池凭据</h3>
        <p class="cell-muted" style="margin:0 0 12px">
          所有 Key 以 AES 加密存储在 PostgreSQL；下方仅显示掩码。环境变量导入后也会同步到此表。
        </p>
        <table v-if="poolKeys.length">
          <thead>
            <tr>
              <th>Provider</th>
              <th>Key（掩码）</th>
              <th>来源</th>
              <th>说明</th>
              <th>状态</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="k in poolKeys" :key="k.credential_id">
              <td>
                <div>{{ k.provider_name }}</div>
                <code style="font-size:11px">{{ k.catalog_code }}</code>
                <div class="cell-muted">#{{ k.credential_id }} · {{ k.credential_label }}</div>
              </td>
              <td><code class="model-code">{{ k.key_masked || (k.has_secret ? '***' : '无 Key') }}</code></td>
              <td><span class="acq-pill">{{ acquisitionLabel(k.acquisition_source || 'manual') }}</span></td>
              <td class="cell-muted">{{ k.acquisition_detail || '—' }}</td>
              <td>
                <span class="badge" :class="k.credential_status === 'active' ? 'badge-green' : 'badge-gray'">
                  {{ k.credential_status }}
                </span>
                <div class="cell-muted">{{ k.availability_state }}</div>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else class="empty">暂无凭据，点击「写入 DB 凭据」添加</div>
      </div>

      <!-- Guide tab -->
      <div v-if="activeTab === 'guide' && methodsData" class="card">
        <h3 style="margin-top:0">获取方式与使用</h3>
        <p class="cell-muted" style="margin-top:0;margin-bottom:16px">
          定时任务每 {{ Math.round(methodsData.scheduler.interval_sec / 60) }} 分钟运行 discovery（env 导入 + no-key + OAuth + GitHub 列表学习）。
          Key 写入 <code class="model-code">config/free-pool.env</code>，勿提交 Git。
        </p>
        <div class="guide-grid">
          <div v-for="m in methodsData.methods" :key="m.mode" class="guide-card">
            <div class="guide-head">
              <strong>{{ m.title }}</strong>
              <span class="acq-pill" :class="riskClass(m.risk)">{{ acquisitionLabel(m.mode) }}</span>
            </div>
            <p class="cell-muted">{{ m.summary }}</p>
            <ol class="guide-steps">
              <li v-for="(step, i) in m.steps" :key="i">{{ step }}</li>
            </ol>
            <div class="cell-muted">{{ m.automated ? '✓ 可自动化' : '需人工' }} · 风险 {{ m.risk }}</div>
          </div>
        </div>
        <h4 style="margin:24px 0 12px">安全审计规则</h4>
        <table>
          <thead>
            <tr><th>规则</th><th>状态</th><th>说明</th></tr>
          </thead>
          <tbody>
            <tr v-for="rule in methodsData.audit_rules" :key="rule.id">
              <td>{{ rule.title }}</td>
              <td><span class="badge badge-green">{{ rule.status }}</span></td>
              <td class="cell-muted">{{ rule.detail }}</td>
            </tr>
          </tbody>
        </table>
        <div v-if="methodsData.scheduler.last_result && Object.keys(methodsData.scheduler.last_result).length" style="margin-top:16px">
          <h4 style="margin-bottom:8px">最近一次定时 discovery</h4>
          <pre class="discovery-log">{{ JSON.stringify(methodsData.scheduler.last_result, null, 2) }}</pre>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.cell-muted { color: var(--muted); font-size: 11px; margin-top: 3px; }

.stat-row {
  display: flex;
  flex-wrap: nowrap;
  gap: 10px;
  overflow-x: auto;
}
.stat-inline {
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex: 1 1 0;
  min-width: 160px;
  padding: 10px 14px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  white-space: nowrap;
}
.stat-label { font-size: 12px; color: var(--muted); }
.stat-value { font-size: 20px; font-weight: 700; color: var(--text); }
.stat-value.stat-ok { color: var(--success); }
.stat-sub { font-size: 12px; color: var(--muted); }

.form-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
.form-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.form-item label {
  font-size: 12px;
  font-weight: 600;
  color: var(--muted);
}

.tab-bar { display: flex; gap: 8px; flex-wrap: wrap; }
.tab-btn {
  border: 1px solid var(--border);
  background: var(--bg-subtle, var(--card));
  color: var(--text);
  padding: 8px 14px;
  border-radius: 8px;
  cursor: pointer;
  font-size: 13px;
  transition: background .15s, color .15s;
}
.tab-btn:hover:not(.active) {
  border-color: var(--accent);
  color: var(--accent-h);
}
.tab-btn.active {
  background: var(--accent);
  color: #fff;
  border-color: transparent;
}

.section-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 12px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}
.search-input { max-width: 280px; min-width: 200px; }

.model-code { font-size: 12px; font-weight: 600; color: var(--accent-h); }
.model-tags { display: flex; flex-wrap: wrap; gap: 6px; }
.model-tag {
  display: inline-block;
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 999px;
  background: rgba(139,148,158,.12);
  color: var(--text);
  border: 1px solid var(--border);
}
.model-tag.routable {
  background: rgba(63,185,80,.15);
  border-color: rgba(63,185,80,.35);
  color: var(--success);
}
.model-tag.template {
  background: rgba(99,102,241,.12);
  border-color: rgba(99,102,241,.35);
  color: var(--accent-h);
}
.model-tag.dim { opacity: 0.45; }

.tier-pill {
  font-size: 11px;
  font-weight: 700;
  padding: 2px 8px;
  border-radius: 6px;
  background: rgba(210,153,34,.15);
  color: var(--warning);
}
.acq-pill {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 6px;
  background: rgba(139,148,158,.12);
  color: var(--text);
}
.acq-pill.risk-high { color: var(--danger); background: rgba(248,81,73,.12); }
.acq-pill.risk-medium { color: var(--warning); background: rgba(210,153,34,.12); }
.acq-pill.risk-low { color: var(--success); background: rgba(63,185,80,.12); }

.guide-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 12px;
}
.guide-card {
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 12px;
  background: var(--bg-subtle, var(--bg));
}
.guide-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}
.guide-steps {
  margin: 8px 0 8px 18px;
  font-size: 12px;
  color: var(--text);
}
.discovery-log {
  font-size: 11px;
  padding: 12px;
  border-radius: var(--radius);
  background: var(--bg);
  border: 1px solid var(--border);
  color: var(--muted);
  overflow-x: auto;
  max-height: 240px;
}

.badge-orange { background: rgba(210,153,34,.15); color: var(--warning); }
.badge-gray { background: rgba(139,148,158,.15); color: var(--muted); }

.assistant-layout {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.platform-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 12px;
  margin-top: 12px;
}
.platform-card {
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 12px;
  background: var(--bg-subtle, var(--bg));
}
.platform-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
}
.platform-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 10px;
}
.hub-filters {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.btn-sm { font-size: 12px; padding: 4px 10px; }
.tag-row { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 6px; }
.probe-box {
  margin-top: 12px;
  padding: 10px;
  border-radius: var(--radius);
  border: 1px solid var(--border);
  background: var(--bg);
}
.probe-box pre {
  font-size: 11px;
  margin: 8px 0 0;
  overflow-x: auto;
  max-height: 120px;
  color: var(--muted);
}
.temp-email-box {
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 10px;
  background: var(--bg-subtle, var(--bg));
}
.temp-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 6px;
}
.temp-row code { font-size: 12px; word-break: break-all; }
.inbox-list {
  margin: 12px 0 0;
  padding-left: 18px;
  font-size: 12px;
}
.tool-links { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
</style>
