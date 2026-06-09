<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import {
  getProviders, createProvider, updateProvider, toggleProvider,
  addCredential, deleteCredential, getCatalog, getProviderCredentials,
  updateCredential, checkProvider, checkCredential, diagnoseProvider,
  getBackgroundTasksStatus,
  type Provider, type CatalogEntry, type ProviderCredential, type CredentialStatus,
  type BackgroundTasksStatus, type CredentialCheckResult,
} from '../api'

const providers = ref<Provider[]>([])
const catalog   = ref<CatalogEntry[]>([])
const loading   = ref(false)
const error     = ref('')
const router = useRouter()
const credentialsByProvider = ref<Record<number, ProviderCredential[]>>({})
const credentialLoading = ref<Record<number, boolean>>({})
const credentialSaving = ref<Record<number, boolean>>({})
const credentialErrors = ref<Record<number, string>>({})

// ── Filter & sort state ──────────────────────────────────────────────────────
const filterSearch = ref('')
// Default to "available" (healthy) on entry — operators mostly care about
// what is actually usable.  The "全部" tab in the filter bar lets users
// widen the view on demand.
const filterHealthStatus = ref('healthy')
const filterFreeModel = ref<'all' | 'yes' | 'no'>('all')
let _searchDebounceTimer: ReturnType<typeof setTimeout> | null = null

const healthStatusOptions = [
  { value: 'all', label: '全部' },
  { value: 'healthy', label: '可用' },
  { value: 'warning', label: '警告' },
  { value: 'unreachable', label: '不可用' },
]

const freeModelOptions = [
  { value: 'all', label: '全部' },
  { value: 'yes', label: '含免费' },
  { value: 'no',  label: '不含免费' },
]

const bgStatus = ref<BackgroundTasksStatus | null>(null)
let _bgPollTimer: ReturnType<typeof setInterval> | null = null

function fmtElapsed(sec: number | null | undefined): string {
  if (sec == null) return ''
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`
  return `${Math.floor(sec / 3600)}h${Math.floor((sec % 3600) / 60)}m`
}

function fmtTimeAgo(iso: string | null | undefined): string {
  if (!iso) return '—'
  const diff = (Date.now() - new Date(iso).getTime()) / 1000
  if (diff < 60) return `${Math.round(diff)}秒前`
  if (diff < 3600) return `${Math.floor(diff / 60)}分钟前`
  if (diff < 86400) return `${Math.floor(diff / 3600)}小时前`
  return `${Math.floor(diff / 86400)}天前`
}

const credentialStatuses: Array<{ value: CredentialStatus; label: string }> = [
  { value: 'active', label: '可用' },
  { value: 'cooling', label: '冷却' },
  { value: 'degraded', label: '降级' },
  { value: 'quarantine', label: '隔离' },
  { value: 'quota_expired', label: '配额耗尽' },
  { value: 'disabled', label: '停用' },
]

// ── Add provider modal ──────────────────────────────────────────────────────
const showAdd      = ref(false)
const isCustom     = ref(false)
const addCode      = ref('')
const addName      = ref('')
const addBaseUrl   = ref('')
const addProtocol  = ref('openai-completions')
const addNotes     = ref('')
const addSaving    = ref(false)
const addErr       = ref('')

// Derive base_url placeholder from currently-selected catalog entry
const selectedCat = computed<CatalogEntry | undefined>(
  () => catalog.value.find(c => c.code === addCode.value)
)

// When catalog selection changes, prefill base_url with template
function onCatalogChange() {
  if (!isCustom.value && selectedCat.value) {
    addBaseUrl.value = selectedCat.value.base_url_template ?? ''
  }
}

function openAdd() {
  isCustom.value    = false
  addCode.value     = catalog.value[0]?.code ?? ''
  addName.value     = ''
  addBaseUrl.value  = catalog.value[0]?.base_url_template ?? ''
  addProtocol.value = 'openai-completions'
  addNotes.value    = ''
  addErr.value      = ''
  showAdd.value     = true
}

async function submitAdd() {
  addErr.value = ''
  if (isCustom.value) {
    if (!addName.value.trim()) { addErr.value = '请输入自定义供应商名称'; return }
    if (!addBaseUrl.value.trim()) { addErr.value = '请输入 Base URL'; return }
    addSaving.value = true
    try {
      const r = await createProvider({
        catalog_code: '__custom__',
        display_name: addName.value.trim(),
        base_url: addBaseUrl.value.trim(),
        protocol: addProtocol.value,
        notes: addNotes.value || undefined,
      })
      await load()
      showAdd.value = false
    } catch (e: unknown) {
      addErr.value = e instanceof Error ? e.message : '创建失败'
    } finally {
      addSaving.value = false
    }
    return
  }
  if (!addCode.value) { addErr.value = '请选择目录'; return }
  addSaving.value = true
  try {
    await createProvider({
      catalog_code: addCode.value,
      display_name: addName.value || undefined,
      base_url: addBaseUrl.value || undefined,
      notes: addNotes.value || undefined,
    })
    await load()
    showAdd.value = false
  } catch (e: unknown) {
    addErr.value = e instanceof Error ? e.message : '创建失败'
  } finally {
    addSaving.value = false
  }
}

// ── Edit provider modal ─────────────────────────────────────────────────────
const showEdit      = ref(false)
const editProvider  = ref<Provider | null>(null)
const editName      = ref('')
const editBaseUrl   = ref('')
const editProtocol  = ref('')
const editNotes     = ref('')
const editSaving    = ref(false)
const editErr       = ref('')

function openEdit(p: Provider) {
  editProvider.value = p
  editName.value     = p.display_name
  editBaseUrl.value  = p.base_url ?? ''
  editProtocol.value = p.protocol ?? 'openai-completions'
  editNotes.value    = p.notes ?? ''
  editErr.value      = ''
  showEdit.value     = true
}

async function submitEdit() {
  if (!editProvider.value) return
  editSaving.value = true
  editErr.value    = ''
  try {
    await updateProvider(editProvider.value.id, {
      display_name: editName.value || undefined,
      base_url: editBaseUrl.value || undefined,
      protocol: editProtocol.value || undefined,
      notes: editNotes.value || undefined,
    })
    await load()
    showEdit.value = false
  } catch (e: unknown) {
    editErr.value = e instanceof Error ? e.message : '保存失败'
  } finally {
    editSaving.value = false
  }
}

// ── Manage credentials modal ──────────────────────────────────────────────
const showManageCred = ref(false)
const manageProvider = ref<Provider | null>(null)

async function openManageCred(p: Provider) {
  manageProvider.value = p
  showManageCred.value = true
  await loadCredentials(p.id)
}

function closeManageCred() {
  showManageCred.value = false
  manageProvider.value = null
}

// ── Add credential modal ────────────────────────────────────────────────────
const showCred     = ref(false)
const credProvider = ref<Provider | null>(null)
const credKey      = ref('')
const credLabel    = ref('')
const credSaving   = ref(false)
const credErr      = ref('')

function openCred(p: Provider) {
  credProvider.value = p
  credKey.value      = ''
  credLabel.value    = ''
  credErr.value      = ''
  showCred.value     = true
}

async function submitCred() {
  if (!credKey.value) { credErr.value = '请输入 API Key'; return }
  if (!credProvider.value) return
  credSaving.value = true
  credErr.value    = ''
  try {
    const pid = credProvider.value.id
    await addCredential(pid, { api_key: credKey.value, label: credLabel.value || undefined })
    await loadCredentials(pid)
    const activeCount = (credentialsByProvider.value[pid] ?? []).filter((c) => c.status === 'active').length
    credProvider.value.active_credential_count = activeCount
    const listed = providers.value.find((row) => row.id === pid)
    if (listed) listed.active_credential_count = activeCount
    showCred.value = false
  } catch (e: unknown) {
    credErr.value = e instanceof Error ? e.message : '添加失败'
  } finally {
    credSaving.value = false
  }
}

async function delCred(p: Provider, credId: number) {
  if (!confirm('确认停用该凭据？')) return
  try {
    await deleteCredential(p.id, credId)
    await loadCredentials(p.id)
    const activeCount = (credentialsByProvider.value[p.id] ?? []).filter((c) => c.status === 'active').length
    p.active_credential_count = activeCount
    const listed = providers.value.find((row) => row.id === p.id)
    if (listed) listed.active_credential_count = activeCount
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '删除失败'
  }
}

// ── Credential list / status management ───────────────────────────────────
async function loadCredentials(providerId: number) {
  credentialLoading.value = { ...credentialLoading.value, [providerId]: true }
  credentialErrors.value = { ...credentialErrors.value, [providerId]: '' }
  try {
    const rows = await getProviderCredentials(providerId)
    credentialsByProvider.value = { ...credentialsByProvider.value, [providerId]: rows }
  } catch (e: unknown) {
    credentialErrors.value = {
      ...credentialErrors.value,
      [providerId]: e instanceof Error ? e.message : '凭据加载失败',
    }
  } finally {
    credentialLoading.value = { ...credentialLoading.value, [providerId]: false }
  }
}

async function saveCredential(p: Provider, c: ProviderCredential) {
  credentialSaving.value = { ...credentialSaving.value, [c.id]: true }
  try {
    await updateCredential(p.id, c.id, {
      label: c.label,
      status: c.status,
      concurrency_limit: c.concurrency_limit || null,
      effective_at: c.effective_at,
      expires_at: c.expires_at,
      tags: c.tags,
      notes: c.notes || '',
      balance_usd: c.balance_usd != null ? Number(c.balance_usd) : null,
    })
    await loadCredentials(p.id)
    p.active_credential_count = (credentialsByProvider.value[p.id] ?? []).filter((row) => row.status === 'active').length
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '保存凭据失败'
  } finally {
    credentialSaving.value = { ...credentialSaving.value, [c.id]: false }
  }
}

function statusBadgeClass(status: string): string {
  if (status === 'active') return 'badge-green'
  if (status === 'disabled' || status === 'quota_expired' || status === 'quarantine') return 'badge-red'
  if (status === 'cooling' || status === 'degraded') return 'badge-amber'
  return 'badge-gray'
}

function healthBadgeClass(status?: string | null): string {
  if (status === 'healthy') return 'badge-green'
  if (status === 'warning') return 'badge-amber'
  if (status === 'unreachable') return 'badge-red'
  return 'badge-gray'
}

function healthLabel(status?: string | null): string {
  if (status === 'healthy') return '正常'
  if (status === 'warning') return '警示'
  if (status === 'unreachable') return '不可达'
  return '未探测'
}

function healthWarningLabel(code?: string | null): string {
  if (code === 'models_unavailable_but_probe_ok') return '模型列表异常，但调用成功'
  if (code === 'probe_skipped_no_model') return '模型列表异常，且无模型可实探'
  if (code === 'probe_failed_authentication_failed') return '模型列表异常，且探测鉴权失败'
  if (code === 'probe_failed_rate_limited') return '模型列表异常，且探测命中限流'
  if (code === 'probe_failed_request_failed') return '模型列表异常，且探测请求失败'
  return ''
}

function timeText(v?: string | null): string {
  if (!v) return '—'
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString('zh-CN', { hour12: false })
}

function money(v: number | string | null | undefined): string {
  if (v === null || v === undefined) return '—'
  const n = typeof v === 'string' ? Number(v) : v
  return Number.isNaN(n) ? '—' : `$${n.toFixed(4)}`
}

function asDateInput(v: string | null): string {
  if (!v) return ''
  return v.slice(0, 16)
}

function setDateInput(c: ProviderCredential, field: 'effective_at' | 'expires_at', value: string) {
  c[field] = value ? new Date(value).toISOString() : null
}

function setDateInputFromEvent(c: ProviderCredential, field: 'effective_at' | 'expires_at', event: Event) {
  setDateInput(c, field, (event.target as HTMLInputElement).value)
}

function tagsText(c: ProviderCredential): string {
  return (c.tags ?? []).join(', ')
}

function setTagsText(c: ProviderCredential, value: string) {
  c.tags = value.split(',').map((t) => t.trim()).filter(Boolean)
}

function setTagsTextFromEvent(c: ProviderCredential, event: Event) {
  setTagsText(c, (event.target as HTMLInputElement).value)
}

// ── Toggle ──────────────────────────────────────────────────────────────────
async function toggle(p: Provider) {
  try {
    await toggleProvider(p.id)
    p.enabled = !p.enabled
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '操作失败'
  }
}

// ── Check single provider ────────────────────────────────────────────────────
const checkingProvider = ref<Record<number, boolean>>({})
const checkResults = ref<Record<number, string>>({})

async function checkSingleProvider(p: Provider) {
  checkingProvider.value = { ...checkingProvider.value, [p.id]: true }
  checkResults.value = { ...checkResults.value, [p.id]: '' }
  try {
    const r = await checkProvider(p.id)
    checkResults.value = { ...checkResults.value, [p.id]: r.reason === 'started' ? '检测已启动' : '已在检测中' }
    // Refresh after short delay to pick up health status
    setTimeout(() => load(), 5000)
  } catch (e: unknown) {
    checkResults.value = { ...checkResults.value, [p.id]: e instanceof Error ? e.message : '检测失败' }
  } finally {
    checkingProvider.value = { ...checkingProvider.value, [p.id]: false }
  }
}

// ── Check single credential ────────────────────────────────────────────────
const checkingCredential = ref<Record<number, boolean>>({})
const credentialCheckResults = ref<Record<number, string>>({})

async function checkSingleCredential(prov: Provider, cred: { id: number }) {
  checkingCredential.value = { ...checkingCredential.value, [cred.id]: true }
  credentialCheckResults.value = { ...credentialCheckResults.value, [cred.id]: '' }
  try {
    const r = await checkCredential(prov.id, cred.id)
    credentialCheckResults.value = {
      ...credentialCheckResults.value,
      [cred.id]: `状态: ${r.health_status} · ${r.health_source === 'models' ? '模型接口正常' : r.probe_ok ? '探活通过' : '不可用'}`,
    }
    // Refresh credentials to pick up new health status
    setTimeout(() => loadCredentials(prov.id), 3000)
  } catch (e: unknown) {
    credentialCheckResults.value = {
      ...credentialCheckResults.value,
      [cred.id]: e instanceof Error ? e.message : '检测失败',
    }
  } finally {
    checkingCredential.value = { ...checkingCredential.value, [cred.id]: false }
  }
}

// ── Diagnose (deep probe: request URL / method / sanitized headers / response) ──
const diagnoseProviderId = ref<number | null>(null)
const diagnoseLoading = ref(false)
const diagnoseResult = ref<{ provider_id: number; credential_count: number; results: CredentialCheckResult[] } | null>(null)
const diagnoseError = ref('')
const expandedCredId = ref<number | null>(null)

async function openDiagnose(prov: Provider) {
  diagnoseProviderId.value = prov.id
  diagnoseError.value = ''
  diagnoseResult.value = null
  expandedCredId.value = null
  diagnoseLoading.value = true
  try {
    const r = await diagnoseProvider(prov.id, { force: true })
    diagnoseResult.value = r
  } catch (e: unknown) {
    diagnoseError.value = e instanceof Error ? e.message : '诊断失败'
  } finally {
    diagnoseLoading.value = false
  }
}

function closeDiagnose() {
  diagnoseProviderId.value = null
  diagnoseResult.value = null
  diagnoseError.value = ''
  expandedCredId.value = null
}

function sourceBadgeClass(source: string | null | undefined): string {
  switch (source) {
    case 'api':           return 'badge badge-green'
    case 'manifest':      return 'badge badge-amber'
    case 'manifest_only': return 'badge badge-amber'
    case 'none':          return 'badge badge-red'
    default:              return 'badge'
  }
}

function sourceLabel(source: string | null | undefined): string {
  switch (source) {
    case 'api':           return 'API 绿'
    case 'manifest':      return 'Manifest 黄'
    case 'manifest_only': return '仅 Manifest'
    case 'none':          return '未验证'
    default:              return source || '未验证'
  }
}

function asJson(v: unknown): string {
  try { return JSON.stringify(v, null, 2) } catch { return String(v) }
}

function toggleCredDetail(credId: number) {
  expandedCredId.value = expandedCredId.value === credId ? null : credId
}

// ── Load ────────────────────────────────────────────────────────────────────
async function load() {
  loading.value = true
  error.value = ''
  try {
    const hasFreeParam =
      filterFreeModel.value === 'all'
        ? undefined
        : filterFreeModel.value === 'yes'
    const [p, c] = await Promise.all([
      getProviders({
        search: filterSearch.value || undefined,
        health_status: filterHealthStatus.value || undefined,
        has_free_model: hasFreeParam,
      }),
      getCatalog(),
    ])
    providers.value = p
    catalog.value   = c
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

function onSearchInput() {
  if (_searchDebounceTimer) clearTimeout(_searchDebounceTimer)
  _searchDebounceTimer = setTimeout(() => load(), 300)
}

function onHealthStatusChange(status: string) {
  filterHealthStatus.value = status
  load()
}

function onFreeModelChange(value: 'all' | 'yes' | 'no') {
  filterFreeModel.value = value
  load()
}

async function loadBgStatus() {
  try {
    bgStatus.value = await getBackgroundTasksStatus()
  } catch { /* ignore */ }
}

onMounted(() => {
  load()
  loadBgStatus()
  _bgPollTimer = setInterval(loadBgStatus, 15000)
})

onUnmounted(() => {
  if (_bgPollTimer) clearInterval(_bgPollTimer)
})
</script>

<template>
  <div>
    <div class="page-header">
      <h2>提供商管理</h2>
      <button class="btn btn-primary" @click="openAdd">+ 添加提供商</button>
    </div>

    <div class="bg-status-bar" v-if="bgStatus">
      <div class="bg-status-item">
        <span class="bg-dot" :class="bgStatus.discovery.alive ? 'dot-green' : 'dot-red'"></span>
        <span class="bg-label">模型发现</span>
        <template v-if="bgStatus.discovery.running">
          <span class="badge badge-blue">检测中 {{ fmtElapsed(bgStatus.discovery.elapsed_seconds) }}</span>
        </template>
        <template v-else-if="bgStatus.discovery.alive">
          <span class="badge badge-green">正常</span>
          <span class="bg-muted">上次: {{ fmtTimeAgo(bgStatus.discovery.finished_at) }}</span>
        </template>
        <template v-else>
          <span class="badge badge-red">已停止</span>
        </template>
        <span class="bg-muted" v-if="bgStatus.discovery.error">错误: {{ bgStatus.discovery.error.slice(0, 60) }}</span>
      </div>
      <div class="bg-status-item">
        <span class="bg-dot" :class="bgStatus.probe_loop.alive ? 'dot-green' : 'dot-red'"></span>
        <span class="bg-label">快速探测</span>
        <span class="badge" :class="bgStatus.probe_loop.alive ? 'badge-green' : 'badge-red'">{{ bgStatus.probe_loop.alive ? '运行' : '停止' }}</span>
        <span class="bg-muted" v-if="bgStatus.probe_loop.checks_last_10m != null">10m内 {{ bgStatus.probe_loop.checks_last_10m }} 次</span>
      </div>
      <div class="bg-status-item">
        <span class="bg-dot" :class="bgStatus.cycler.alive ? 'dot-green' : 'dot-red'"></span>
        <span class="bg-label">凭据巡检</span>
        <span class="badge" :class="bgStatus.cycler.alive ? 'badge-green' : 'badge-red'">{{ bgStatus.cycler.alive ? '运行' : '停止' }}</span>
        <span class="bg-muted" v-if="bgStatus.cycler.last_check_at">上次: {{ fmtTimeAgo(bgStatus.cycler.last_check_at) }}</span>
      </div>
      <div class="bg-status-item">
        <span class="bg-dot" :class="bgStatus.recovery.alive ? 'dot-green' : 'dot-red'"></span>
        <span class="bg-label">自动恢复</span>
        <span class="badge" :class="bgStatus.recovery.alive ? 'badge-green' : 'badge-red'">{{ bgStatus.recovery.alive ? '运行' : '停止' }}</span>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="loading" class="empty">加载中…</div>

    <!-- ── Filter Bar ────────────────────────────────────────────────────── -->
    <div class="filter-bar" v-if="!loading">
      <div class="filter-search">
        <input
          v-model="filterSearch"
          @input="onSearchInput"
          placeholder="搜索显示名…"
          class="filter-input"
        />
        <span class="filter-search-icon">🔍</span>
      </div>
      <div class="filter-tabs">
        <button
          v-for="opt in healthStatusOptions"
          :key="opt.value"
          class="filter-tab"
          :class="{ active: filterHealthStatus === opt.value }"
          @click="onHealthStatusChange(opt.value)"
        >{{ opt.label }}</button>
      </div>
      <div class="filter-divider" aria-hidden="true"></div>
      <div class="filter-tabs">
        <span class="filter-tab-label">免费模型</span>
        <button
          v-for="opt in freeModelOptions"
          :key="opt.value"
          class="filter-tab"
          :class="{ active: filterFreeModel === opt.value }"
          @click="onFreeModelChange(opt.value as 'all' | 'yes' | 'no')"
        >{{ opt.label }}</button>
      </div>
    </div>

    <div class="card" v-if="!loading">
      <table>
        <thead>
          <tr>
            <th>显示名</th>
            <th>目录代码</th>
            <th>Header Profile</th>
            <th>Base URL</th>
            <th>凭据数</th>
            <th>可用模型</th>
            <th>免费模型</th>
            <th>24h 错误率</th>
            <th>系统健康</th>
            <th>状态</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in providers" :key="p.id" @click="router.push('/providers/' + p.id)" style="cursor:pointer">
            <td>
              <div style="font-weight:500">{{ p.display_name }}</div>
              <div style="font-size:11px;color:var(--muted)" v-if="p.notes">{{ p.notes }}</div>
            </td>
            <td><code style="font-size:12px">{{ p.catalog_code }}</code></td>
            <td><code style="font-size:11px">{{ p.header_profile_code || '—' }}</code></td>
            <td>
              <div style="font-size:12px;color:var(--muted);max-width:220px;word-break:break-all">
                {{ p.base_url || '—' }}
              </div>
            </td>
            <td>
              <span class="badge" :class="p.active_credential_count > 0 ? 'badge-green' : 'badge-red'">
                {{ p.active_credential_count }}
              </span>
            </td>
            <td>
              <span style="font-size:12px">{{ (p as any).available_model_count ?? '—' }}</span>
            </td>
            <td>
              <span
                class="badge"
                :class="(p.free_model_count ?? 0) > 0 ? 'badge-green' : 'badge-gray'"
                :title="(p.free_model_count ?? 0) > 0 ? '该供应商存在免费 (billing_mode=free) 的模型' : '该供应商没有免费模型'"
              >{{ p.free_model_count ?? 0 }}</span>
            </td>
            <td>
              <span style="font-size:12px">{{ (p as any).error_rate_24h != null ? Number((p as any).error_rate_24h).toFixed(1) + '%' : '—' }}</span>
            </td>
            <td>
              <span class="badge" :class="healthBadgeClass(p.health_status)">
                {{ healthLabel(p.health_status) }}
              </span>
              <div class="muted">检查 {{ timeText(p.health_checked_at) }}</div>
              <div class="muted" v-if="(p.warning_credential_count ?? 0) > 0">警示 {{ p.warning_credential_count }}</div>
            </td>
            <td>
              <span class="badge" :class="p.enabled ? 'badge-green' : 'badge-gray'">
                {{ p.enabled ? '已启用' : '已禁用' }}
              </span>
            </td>
            <td>
              <div style="display:flex;gap:6px;flex-wrap:wrap">
                <button class="btn btn-ghost btn-sm" @click="openEdit(p)">编辑</button>
                <button class="btn btn-ghost btn-sm" @click="toggle(p)">
                  {{ p.enabled ? '禁用' : '启用' }}
                </button>
                <button class="btn btn-ghost btn-sm" @click="openCred(p)">+ 凭据</button>
                <button class="btn btn-ghost btn-sm" @click="openManageCred(p)">管理凭据</button>
                <button
                  class="btn btn-ghost btn-sm"
                  @click="checkSingleProvider(p)"
                  :disabled="checkingProvider[p.id]"
                  title="对该供应商所有凭据执行一次健康检测"
                >{{ checkingProvider[p.id] ? '检测中…' : '检测' }}</button>
              </div>
              <div v-if="checkResults[p.id]" style="font-size:11px;color:var(--muted);margin-top:4px">
                {{ checkResults[p.id] }}
              </div>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-if="!loading && providers.length === 0" class="empty">尚未配置任何提供商</div>
    </div>

    <!-- ── Add Provider Modal ─────────────────────────────────────────────── -->
    <div class="modal-overlay" v-if="showAdd" @click.self="showAdd = false">
      <div class="modal" style="max-width:500px">
        <h3>添加提供商</h3>
        <div v-if="addErr" class="alert alert-danger">{{ addErr }}</div>

        <!-- Toggle custom mode -->
        <div class="form-group" style="display:flex;align-items:center;gap:10px">
          <input id="customToggle" type="checkbox" v-model="isCustom" style="width:auto" />
          <label for="customToggle" style="margin:0;cursor:pointer">自定义供应商（不在目录中）</label>
        </div>

        <!-- Catalog mode -->
        <template v-if="!isCustom">
          <div class="form-group">
            <label>选择目录</label>
            <select v-model="addCode" @change="onCatalogChange">
              <option v-for="c in catalog" :key="c.code" :value="c.code">
                {{ c.display_name }} ({{ c.code }})
              </option>
            </select>
          </div>
          <div class="form-group">
            <label>显示名（可选，留空使用目录默认名）</label>
            <input v-model="addName" placeholder="例: 我的 OpenAI" />
          </div>
        </template>

        <!-- Custom mode -->
        <template v-else>
          <div class="form-group">
            <label>供应商名称 <span style="color:var(--danger)">*</span></label>
            <input v-model="addName" placeholder="例: 私有 Ollama 集群" />
          </div>
          <div class="form-group">
            <label>协议</label>
            <select v-model="addProtocol">
              <option value="openai-completions">OpenAI 兼容 (openai-completions)</option>
              <option value="anthropic">Anthropic</option>
              <option value="ollama">Ollama</option>
              <option value="cohere">Cohere</option>
              <option value="gemini">Gemini</option>
            </select>
          </div>
        </template>

        <!-- Base URL (always shown) -->
        <div class="form-group">
          <label>Base URL{{ isCustom ? ' *' : '（可选，覆盖目录默认值）' }}</label>
          <input
            v-model="addBaseUrl"
            :placeholder="isCustom ? 'https://your-api.example.com/v1' : (selectedCat?.base_url_template || 'https://api.example.com/v1')"
          />
          <div v-if="!isCustom && selectedCat" style="font-size:11px;color:var(--muted);margin-top:4px">
            目录默认: {{ selectedCat.base_url_template }}
          </div>
        </div>

        <div class="form-group">
          <label>备注（可选）</label>
          <input v-model="addNotes" placeholder="内部说明" />
        </div>

        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" @click="showAdd = false">取消</button>
          <button class="btn btn-primary" @click="submitAdd" :disabled="addSaving">
            {{ addSaving ? '保存中…' : '确认添加' }}
          </button>
        </div>
      </div>
    </div>

    <!-- ── Edit Provider Modal ───────────────────────────────────────────── -->
    <div class="modal-overlay" v-if="showEdit" @click.self="showEdit = false">
      <div class="modal" style="max-width:500px">
        <h3>编辑提供商 — {{ editProvider?.display_name }}</h3>
        <div v-if="editErr" class="alert alert-danger">{{ editErr }}</div>
        <div class="form-group">
          <label>目录代码</label>
          <input :value="editProvider?.catalog_code || '—'" disabled class="muted" />
        </div>
        <div class="form-group">
          <label>供应商</label>
          <input :value="editProvider?.vendor_name || '—'" disabled class="muted" />
        </div>
        <div class="form-group">
          <label>Header Profile</label>
          <input :value="editProvider?.header_profile_code || '—'" disabled class="muted" />
        </div>
        <div class="form-group">
          <label>显示名</label>
          <input v-model="editName" placeholder="供应商显示名称" />
        </div>
        <div class="form-group">
          <label>Protocol</label>
          <select v-model="editProtocol">
            <option value="openai-completions">OpenAI Completions</option>
            <option value="openai-responses">OpenAI Responses</option>
            <option value="anthropic-messages">Anthropic Messages</option>
            <option value="gemini-generate">Gemini Generate</option>
          </select>
        </div>
        <div class="form-group">
          <label>Base URL</label>
          <input v-model="editBaseUrl" placeholder="https://api.example.com/v1" />
        </div>
        <div class="form-group">
          <label>备注</label>
          <input v-model="editNotes" placeholder="内部说明" />
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" @click="showEdit = false">取消</button>
          <button class="btn btn-primary" @click="submitEdit" :disabled="editSaving">
            {{ editSaving ? '保存中…' : '保存' }}
          </button>
        </div>
      </div>
    </div>

    <!-- ── Manage Credentials Modal ───────────────────────────────────────── -->
    <div class="modal-overlay" v-if="showManageCred && manageProvider" @click.self="closeManageCred">
      <div class="modal modal-wide" @click.stop>
        <div class="credential-toolbar">
          <div>
            <h3 style="margin:0">管理凭据 — {{ manageProvider.display_name }}</h3>
            <div class="muted" style="margin-top:4px">
              {{ manageProvider.catalog_code }} · {{ manageProvider.base_url || '—' }}
            </div>
          </div>
          <div style="display:flex;gap:8px;flex-shrink:0">
            <button
              class="btn btn-ghost btn-sm"
              @click="loadCredentials(manageProvider.id)"
              :disabled="credentialLoading[manageProvider.id]"
            >刷新</button>
            <button class="btn btn-primary btn-sm" @click="openCred(manageProvider)">+ 凭据</button>
            <button class="btn btn-ghost btn-sm" @click="closeManageCred">关闭</button>
          </div>
        </div>
        <div v-if="credentialErrors[manageProvider.id]" class="alert alert-danger">{{ credentialErrors[manageProvider.id] }}</div>
        <div v-if="credentialLoading[manageProvider.id]" class="empty">凭据加载中…</div>
        <div v-else class="credential-scroll">
          <table class="credential-table">
            <thead>
              <tr>
                <th>凭据</th>
                <th>状态</th>
                <th>系统探活</th>
                <th>模型清单</th>
                <th>并发</th>
                <th>有效期</th>
                <th>用量 / 余额</th>
                <th>标签 / 备注</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="c in credentialsByProvider[manageProvider.id] || []" :key="c.id">
                <td>
                  <input v-model="c.label" class="compact-input" />
                  <div class="muted">#{{ c.id }} · {{ c.trust_level }}</div>
                </td>
                <td>
                  <select v-model="c.status" class="compact-input">
                    <option v-for="s in credentialStatuses" :key="s.value" :value="s.value">{{ s.label }}</option>
                  </select>
                  <div><span class="badge" :class="statusBadgeClass(c.status)">{{ c.status }}</span></div>
                </td>
                <td>
                  <div><span class="badge" :class="healthBadgeClass(c.health_status)">{{ healthLabel(c.health_status) }}</span></div>
                  <div class="muted">检查 {{ timeText(c.health_checked_at) }}</div>
                  <div class="muted" v-if="c.health_warning_code">{{ healthWarningLabel(c.health_warning_code) }}</div>
                  <div class="muted" v-if="c.health_probe_model">Probe {{ c.health_probe_model }}</div>
                  <div class="muted health-error" v-if="c.health_error">{{ c.health_error }}</div>
                </td>
                <td>
                  <div>
                    <span
                      class="badge"
                      :class="c.api_models_ok === true ? 'badge badge-green' : c.api_models_ok === false ? 'badge badge-red' : 'badge'"
                      :title="c.api_models_ok === true ? '模型清单 API 验证通过' : c.api_models_ok === false ? 'API 验证失败: ' + (c.api_models_error || '') : '尚未验证'"
                    >{{ c.api_models_ok === true ? 'API 绿' : c.api_models_ok === false ? 'API 红' : '未验证' }}</span>
                  </div>
                  <div class="muted" v-if="c.api_models_last_checked_at">检查 {{ timeText(c.api_models_last_checked_at) }}</div>
                  <div class="muted health-error" v-if="c.api_models_error">{{ c.api_models_error }}</div>
                </td>
                <td>
                  <input v-model.number="c.concurrency_limit" type="number" min="0" class="compact-input number" placeholder="默认5" />
                  <div v-if="c.fp_slot_limit != null" class="muted" style="font-size:11px;margin-top:4px">
                    槽 {{ c.fp_slots_used ?? 0 }}/{{ c.fp_slot_limit }}
                    <span v-if="(c.fp_slots_free ?? 0) === 0" style="color:var(--danger)">已满</span>
                    <span v-else>余 {{ c.fp_slots_free }}</span>
                  </div>
                  <div v-else class="muted" style="font-size:11px;margin-top:4px">无限（0=不限）</div>
                </td>
                <td>
                  <input :value="asDateInput(c.effective_at)" type="datetime-local" class="compact-input" @input="setDateInputFromEvent(c, 'effective_at', $event)" />
                  <input :value="asDateInput(c.expires_at)" type="datetime-local" class="compact-input" @input="setDateInputFromEvent(c, 'expires_at', $event)" />
                </td>
                <td>
                  <div>{{ c.total_requests }} 次 · {{ money(c.total_cost_usd) }}</div>
                  <div class="muted">余额 <input v-model.number="c.balance_usd" type="number" min="0" step="100" class="compact-input number" style="width:80px;display:inline-block" placeholder="—" /></div>
                  <div v-if="c.quota_summary?.any_exhausted" class="badge badge-red">配额耗尽</div>
                </td>
                <td>
                  <input :value="tagsText(c)" class="compact-input" placeholder="tag1, tag2" @input="setTagsTextFromEvent(c, $event)" />
                  <input v-model="c.notes" class="compact-input" placeholder="备注" />
                </td>
                <td>
                  <button class="btn btn-primary btn-sm" @click="saveCredential(manageProvider, c)" :disabled="credentialSaving[c.id]">
                    {{ credentialSaving[c.id] ? '保存中' : '保存' }}
                  </button>
                  <button
                    class="btn btn-ghost btn-sm"
                    @click="checkSingleCredential(manageProvider, c)"
                    :disabled="checkingCredential[c.id]"
                    title="对此凭据执行一次健康检测"
                  >{{ checkingCredential[c.id] ? '检测中' : '检测' }}</button>
                  <button class="btn btn-ghost btn-sm" @click="openDiagnose(manageProvider)">诊断</button>
                  <button class="btn btn-ghost btn-sm" @click="delCred(manageProvider, c.id)">停用</button>
                  <div v-if="credentialCheckResults[c.id]" style="font-size:11px;color:var(--muted);margin-top:4px">
                    {{ credentialCheckResults[c.id] }}
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-if="!(credentialsByProvider[manageProvider.id] || []).length" class="empty">暂无凭据，点击「+ 凭据」添加</div>
        </div>
      </div>
    </div>

    <!-- ── Diagnose Modal ───────────────────────────────────────────────── -->
    <div class="modal-overlay modal-overlay-stacked" v-if="diagnoseProviderId !== null" @click.self="closeDiagnose">
      <div class="modal modal-wide">
        <h3>供应商诊断 — 实际请求/响应抓包 <span class="muted" style="font-size:12px">(凭据已脱敏)</span></h3>
        <div v-if="diagnoseLoading" class="muted">诊断中…</div>
        <div v-else-if="diagnoseError" class="alert alert-danger">{{ diagnoseError }}</div>
        <div v-else-if="diagnoseResult">
          <div class="muted" style="margin-bottom:12px">
            共 {{ diagnoseResult.credential_count }} 个凭据，点击行展开详细请求/响应抓包
          </div>
          <table class="data-table">
            <thead>
              <tr>
                <th>凭据</th>
                <th>模型源</th>
                <th>系统健康</th>
                <th>状态码</th>
                <th>延迟</th>
                <th>Endpoint 模板</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="r in diagnoseResult.results" :key="r.credential_id">
                <tr>
                  <td>
                    <div>#{{ r.credential_id }}</div>
                    <div class="muted" v-if="r.effective_source === 'manifest_only'">manifest-only supplier</div>
                  </td>
                  <td>
                    <span class="badge" :class="sourceBadgeClass(r.effective_source)">
                      {{ sourceLabel(r.effective_source) }}
                    </span>
                  </td>
                  <td>
                    <span class="badge" :class="healthBadgeClass(r.health_status)">
                      {{ healthLabel(r.health_status) }}
                    </span>
                    <div class="muted" v-if="r.health_warning_code">{{ healthWarningLabel(r.health_warning_code) }}</div>
                  </td>
                  <td>
                    <span v-if="r.response_status" :class="r.response_status === 200 ? 'badge badge-green' : 'badge badge-red'">
                      {{ r.response_status }}
                    </span>
                    <span v-else class="muted">—</span>
                    <div class="muted" v-if="r.attempt_index > 0">第 {{ r.attempt_index + 1 }} 次（probe fallback）</div>
                  </td>
                  <td>
                    <span v-if="r.health_latency_ms !== null">{{ r.health_latency_ms }} ms</span>
                    <span v-else class="muted">—</span>
                  </td>
                  <td>
                    <code style="font-size:11px">{{ r.models_endpoint_template || '(auto)' }}</code>
                    <div class="muted" v-if="r.models_endpoint_resolved">
                      → <code style="font-size:11px">{{ r.models_endpoint_resolved }}</code>
                    </div>
                  </td>
                  <td>
                    <button class="btn btn-ghost btn-sm" @click="toggleCredDetail(r.credential_id)">
                      {{ expandedCredId === r.credential_id ? '收起' : '展开' }}
                    </button>
                  </td>
                </tr>
                <tr v-if="expandedCredId === r.credential_id">
                  <td colspan="7" style="background:rgba(0,0,0,0.2);padding:12px">
                    <div class="diag-section">
                      <h4>📤 请求</h4>
                      <div><strong>URL:</strong> <code style="font-size:12px">{{ r.request_url || '(未发出)' }}</code></div>
                      <div><strong>Method:</strong> <code>{{ r.request_method }}</code></div>
                      <div><strong>Headers (脱敏):</strong>
                        <pre style="margin:4px 0;padding:8px;background:rgba(0,0,0,0.3);border-radius:4px;font-size:11px;overflow-x:auto">{{ asJson(r.request_headers_sanitized) }}</pre>
                      </div>
                      <div v-if="r.request_body_preview"><strong>Body:</strong>
                        <pre style="margin:4px 0;padding:8px;background:rgba(0,0,0,0.3);border-radius:4px;font-size:11px;overflow-x:auto">{{ r.request_body_preview }}</pre>
                      </div>
                    </div>
                    <div class="diag-section" style="margin-top:12px">
                      <h4>📥 响应</h4>
                      <div><strong>Status:</strong> <code>{{ r.response_status || '(no response)' }}</code></div>
                      <div v-if="r.response_headers && Object.keys(r.response_headers).length"><strong>Headers:</strong>
                        <pre style="margin:4px 0;padding:8px;background:rgba(0,0,0,0.3);border-radius:4px;font-size:11px;overflow-x:auto">{{ asJson(r.response_headers) }}</pre>
                      </div>
                      <div v-if="r.response_body_preview"><strong>Body (2KB):</strong>
                        <pre style="margin:4px 0;padding:8px;background:rgba(0,0,0,0.3);border-radius:4px;font-size:11px;overflow-x:auto;max-height:200px">{{ r.response_body_preview }}</pre>
                      </div>
                    </div>
                    <div v-if="r.health_error" class="diag-section" style="margin-top:12px">
                      <h4>⚠️ 错误</h4>
                      <pre style="margin:4px 0;padding:8px;background:rgba(180,40,40,0.2);border-radius:4px;font-size:11px;overflow-x:auto">{{ r.health_error }}</pre>
                    </div>
                    <div v-if="r.returned_models && r.returned_models.length" class="diag-section" style="margin-top:12px">
                      <h4>✅ 解析到的模型 ({{ r.returned_models.length }})</h4>
                      <div style="font-size:11px;line-height:1.6">
                        <code v-for="m in r.returned_models.slice(0, 30)" :key="m" style="margin-right:6px;display:inline-block;padding:2px 6px;background:rgba(0,255,128,0.1);border-radius:3px">{{ m }}</code>
                        <span v-if="r.returned_models.length > 30" class="muted">… 共 {{ r.returned_models.length }} 个</span>
                      </div>
                    </div>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-primary" @click="closeDiagnose">关闭</button>
        </div>
      </div>
    </div>

    <!-- ── Add Credential Modal ──────────────────────────────────────────── -->
    <div class="modal-overlay modal-overlay-stacked" v-if="showCred" @click.self="showCred = false">
      <div class="modal">
        <h3>添加凭据 — {{ credProvider?.display_name }}</h3>
        <div v-if="credErr" class="alert alert-danger">{{ credErr }}</div>
        <div class="form-group">
          <label>API Key</label>
          <input v-model="credKey" type="password" placeholder="sk-…" autocomplete="off" />
        </div>
        <div class="form-group">
          <label>标签（可选）</label>
          <input v-model="credLabel" placeholder="如: 生产密钥" />
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" @click="showCred = false">取消</button>
          <button class="btn btn-primary" @click="submitCred" :disabled="credSaving">
            {{ credSaving ? '保存中…' : '添加' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* ── Filter Bar ─────────────────────────────────────────────────────────── */
.filter-bar {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}
.filter-search {
  position: relative;
  flex: 1;
  min-width: 200px;
  max-width: 320px;
}
.filter-input {
  width: 100%;
  padding: 8px 12px 8px 32px;
  font-size: 13px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  outline: none;
  transition: border-color 0.15s;
}
.filter-input:focus {
  border-color: var(--accent);
}
.filter-search-icon {
  position: absolute;
  left: 10px;
  top: 50%;
  transform: translateY(-50%);
  font-size: 13px;
  pointer-events: none;
  opacity: 0.5;
}
.filter-tabs {
  display: flex;
  gap: 4px;
  background: var(--bg-subtle);
  border-radius: 6px;
  padding: 3px;
}
.filter-tab {
  padding: 6px 14px;
  font-size: 13px;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--muted);
  cursor: pointer;
  transition: all 0.15s;
  white-space: nowrap;
}
.filter-tab:hover {
  color: var(--text);
  background: rgba(255,255,255,0.05);
}
.filter-tab.active {
  background: var(--accent);
  color: #fff;
}
.filter-divider {
  width: 1px;
  align-self: stretch;
  background: var(--border);
  opacity: 0.5;
  margin: 0 4px;
}
.filter-tab-label {
  font-size: 12px;
  color: var(--muted);
  padding: 6px 8px 6px 4px;
  white-space: nowrap;
}

.modal-wide {
  max-width: min(1200px, 92vw);
  width: 100%;
  max-height: 88vh;
  display: flex;
  flex-direction: column;
  padding: 20px 24px;
}
.modal-overlay-stacked {
  z-index: 110;
}
.credential-toolbar {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  margin-bottom: 16px;
  color: var(--text);
}
.credential-scroll {
  overflow: auto;
  flex: 1;
  min-height: 0;
}
.credential-table {
  table-layout: auto;
  min-width: 100%;
  background: transparent;
}
.credential-table th {
  color: var(--muted);
  background: var(--bg-subtle);
}
.credential-table td {
  vertical-align: top;
  font-size: 12px;
  background: transparent;
  color: var(--text);
  border-bottom: 1px solid var(--border);
}
.credential-table tbody tr:last-child td {
  border-bottom: none;
}
.credential-table tbody tr:hover td {
  background: rgba(255,255,255,.03);
}
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
.compact-input.number {
  max-width: 88px;
}
.muted {
  color: var(--muted);
  font-size: 11px;
}
.health-error {
  max-width: 240px;
  word-break: break-all;
}
.badge-amber {
  background: rgba(210,153,34,.18);
  color: #f0b429;
}
.diag-section h4 {
  margin: 0 0 6px 0;
  font-size: 13px;
  color: var(--muted);
  font-weight: 600;
}
.diag-section pre {
  white-space: pre-wrap;
  word-break: break-all;
}
.bg-status-bar {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
  padding: 10px 16px;
  margin-bottom: 16px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  font-size: 13px;
  color: var(--text);
}
.bg-status-item {
  display: flex;
  align-items: center;
  gap: 6px;
}
.bg-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  display: inline-block;
  flex-shrink: 0;
}
.dot-green { background: #4caf50; }
.dot-red { background: #f44336; }
.bg-label {
  font-weight: 500;
  margin-right: 2px;
}
.bg-muted {
  color: var(--muted);
  font-size: 11px;
}
.badge-blue {
  background: rgba(33,150,243,.18);
  color: #42a5f5;
}
@media (max-width: 1000px) {
  .credential-table {
    min-width: 960px;
  }
  .bg-status-bar {
    flex-direction: column;
    gap: 8px;
  }
}
</style>
