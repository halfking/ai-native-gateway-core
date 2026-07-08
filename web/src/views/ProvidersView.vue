<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { localeRef } from '../i18n'
import {
  getProviders, createProvider, updateProvider, toggleProvider,
  addCredential, deleteCredential, getCatalog, getProviderCredentials,
  updateCredential, checkProvider, checkCredential, diagnoseProvider,
  getBackgroundTasksStatus, probeURL, probeProviderURL,
  type Provider, type CatalogEntry, type ProviderCredential, type CredentialStatus,
  type BackgroundTasksStatus, type CredentialCheckResult, type ProbeURLResult,
} from '../api'

const { t } = useI18n()
const pm = (k: string, params?: Record<string, unknown>): string =>
  t(`providers.${k}` as never, params as never)

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
// 2026-07-08: filter selections are persisted to localStorage so each
// operator lands on the same view they last configured. The credential
// health dimension defaults to 'healthy' (凭据正常) per the operator
// request: on first visit the list opens focused on currently-usable
// credentials, and the active chip survives reloads / new tabs.
const FILTER_STORAGE_KEY = 'llmgw_providers_filter_v1'

type PersistedFilter = {
  healthStatus: 'all' | 'healthy' | 'warning' | 'unreachable' | 'unknown'
  routability:  'all' | 'available' | 'unavailable' | 'no_models' | 'manual_disabled'
  freeModel:    'all' | 'yes' | 'no'
  search:       string
}

const VALID_HEALTH   = ['all', 'healthy', 'warning', 'unreachable', 'unknown'] as const
const VALID_ROUTE    = ['all', 'available', 'unavailable', 'no_models', 'manual_disabled'] as const
const VALID_FREEMOD  = ['all', 'yes', 'no'] as const

function readPersistedFilter(): Partial<PersistedFilter> {
  try {
    const raw = localStorage.getItem(FILTER_STORAGE_KEY)
    if (!raw) return {}
    const parsed = JSON.parse(raw) as Partial<PersistedFilter>
    return parsed && typeof parsed === 'object' ? parsed : {}
  } catch {
    return {}
  }
}

function writePersistedFilter(patch: Partial<PersistedFilter>) {
  try {
    const current = readPersistedFilter()
    const next: PersistedFilter = {
      healthStatus: patch.healthStatus ?? current.healthStatus ?? 'healthy',
      routability:  patch.routability  ?? current.routability  ?? 'available',
      freeModel:    patch.freeModel    ?? current.freeModel    ?? 'all',
      search:       patch.search       ?? current.search       ?? '',
    }
    localStorage.setItem(FILTER_STORAGE_KEY, JSON.stringify(next))
  } catch {
    // localStorage unavailable / quota exceeded — non-fatal
  }
}

const _persisted = readPersistedFilter()
const filterSearch = ref<string>(
  typeof _persisted.search === 'string' ? _persisted.search : '',
)
// 2026-07-03 v738: split into two orthogonal filter dimensions. The
// healthStatus dimension is now scoped to credential probe health
// only (healthy / warning / unreachable / unknown). Routability is
// the new dimension that answers "can this provider route traffic
// right now" and is independent of credential health.
const filterHealthStatus = ref<'all' | 'healthy' | 'warning' | 'unreachable' | 'unknown'>(
  VALID_HEALTH.includes(_persisted.healthStatus as typeof VALID_HEALTH[number])
    ? (_persisted.healthStatus as 'all' | 'healthy' | 'warning' | 'unreachable' | 'unknown')
    : 'healthy',
)
const filterRoutability = ref<'all' | 'available' | 'unavailable' | 'no_models' | 'manual_disabled'>(
  VALID_ROUTE.includes(_persisted.routability as typeof VALID_ROUTE[number])
    ? (_persisted.routability as 'all' | 'available' | 'unavailable' | 'no_models' | 'manual_disabled')
    : 'available',
)
const filterFreeModel = ref<'all' | 'yes' | 'no'>(
  VALID_FREEMOD.includes(_persisted.freeModel as typeof VALID_FREEMOD[number])
    ? (_persisted.freeModel as 'all' | 'yes' | 'no')
    : 'all',
)
let _searchDebounceTimer: ReturnType<typeof setTimeout> | null = null

// 2026-07-03 v738: visibleProviders is now a no-op (returns providers
// verbatim). The server-side routability + health_status + manual_disabled
// filters do all the work; this just keeps the v-for="p in visibleProviders"
// line in the template stable. The previous client-side .filter() was
// removed because the visibleProviders comment in the previous revision
// noted it raced with the stale provider.Client candidate cache (see
// admin/provider_offer_force_recover.go:437 — fixed in v733 but the
// client-side filter was still risky).
const visibleProviders = computed<Provider[]>(() => providers.value)

const healthStatusOptions = computed(() => [
  { value: 'all',         label: pm('filter.healthChipAll') },
  { value: 'healthy',     label: pm('filter.healthChipHealthy') },
  { value: 'warning',     label: pm('filter.healthChipWarning') },
  { value: 'unreachable', label: pm('filter.healthChipUnreachable') },
])

// 2026-07-03 v738: routability chip options. "可用" here means
// "routable_binding_count > 0" (i.e. at least one model binding is
// currently routable), not "has at least one healthy credential".
// This fixes the original "可用" 误标为手工禁用供应商 bug reported
// on 2026-07-03.
const routabilityOptions = computed(() => [
  { value: 'all',            label: pm('filter.routabilityChipAll') },
  { value: 'available',      label: pm('filter.routabilityChipAvailable') },
  { value: 'unavailable',    label: pm('filter.routabilityChipUnavailable') },
  { value: 'no_models',      label: pm('filter.routabilityChipNoModels') },
  { value: 'manual_disabled', label: pm('filter.routabilityChipManualDisabled') },
])

const freeModelOptions = computed(() => [
  { value: 'all', label: pm('filter.freeChipAll') },
  { value: 'yes', label: pm('filter.freeChipYes') },
  { value: 'no',  label: pm('filter.freeChipNo') },
])

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
  if (diff < 60)    return `${Math.round(diff)}${pm('time.second')}`
  if (diff < 3600)  return `${Math.floor(diff / 60)}${pm('time.minute')}`
  if (diff < 86400) return `${Math.floor(diff / 3600)}${pm('time.hour')}`
  return `${Math.floor(diff / 86400)}${pm('time.day')}`
}

const credentialStatuses = computed<Array<{ value: CredentialStatus; label: string }>>(() => [
  { value: 'active',        label: pm('credential.status.active') },
  { value: 'cooling',       label: pm('credential.status.cooling') },
  { value: 'degraded',      label: pm('credential.status.degraded') },
  { value: 'quarantine',    label: pm('credential.status.quarantine') },
  { value: 'quota_expired', label: pm('credential.status.quota_expired') },
  { value: 'disabled',      label: pm('credential.status.disabled') },
])

function providerChannelLabel(category: string | null | undefined): { label: string; cls: string } {
  if (category === 'official') return { label: pm('list.channel.official'), cls: 'badge-blue' }
  if (!category)               return { label: pm('list.channel.unknown'), cls: 'badge-gray' }
  return { label: pm('list.channel.relay'), cls: 'badge-orange' }
}

// ── Add provider modal ──────────────────────────────────────────────────────
const showAdd      = ref(false)
const isCustom     = ref(false)
const addCode      = ref('')
const addCodeCustom = ref('')
const addName      = ref('')
const addBaseUrl   = ref('')
const addProtocol  = ref('openai-completions')
const addNotes     = ref('')
const addSaving    = ref(false)
const addErr       = ref('')
const addProbeResult = ref<ProbeURLResult | null>(null)
const addProbing   = ref(false)

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
  addCodeCustom.value = ''
  addName.value     = ''
  addBaseUrl.value  = catalog.value[0]?.base_url_template ?? ''
  addProtocol.value = 'openai-completions'
  addNotes.value    = ''
  addErr.value      = ''
  addProbeResult.value = null
  showAdd.value     = true
}

async function doProbe() {
  const url = isCustom.value ? addBaseUrl.value.trim() : addBaseUrl.value.trim()
  if (!url) { addErr.value = pm('create.errors.baseUrlRequired'); return }
  addProbing.value = true
  addProbeResult.value = null
  addErr.value = ''
  try {
    const r = await probeURL({ base_url: url })
    addProbeResult.value = r
    if (r.reachable && r.protocol && !isCustom.value) {
      addProtocol.value = r.protocol
    }
  } catch (e: unknown) {
    addProbeResult.value = { reachable: false, error: e instanceof Error ? e.message : pm('create.errors.probeFailed') }
  } finally {
    addProbing.value = false
  }
}

async function submitAdd() {
  addErr.value = ''
  if (isCustom.value) {
    if (!addCodeCustom.value.trim()) { addErr.value = pm('create.errors.customCodeRequired'); return }
    if (!addName.value.trim()) { addErr.value = pm('create.errors.customNameRequired'); return }
    if (!addBaseUrl.value.trim()) { addErr.value = pm('create.errors.customBaseUrlRequired'); return }
    addSaving.value = true
    try {
      const r = await createProvider({
        catalog_code: '__custom__',
        code: addCodeCustom.value.trim(),
        display_name: addName.value.trim(),
        base_url: addBaseUrl.value.trim(),
        protocol: addProtocol.value,
        notes: addNotes.value || undefined,
      })
      await load()
      showAdd.value = false
    } catch (e: unknown) {
      addErr.value = e instanceof Error ? e.message : pm('create.errors.createFailed')
    } finally {
      addSaving.value = false
    }
    return
  }
  if (!addCode.value) { addErr.value = pm('create.errors.catalogRequired'); return }
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
    addErr.value = e instanceof Error ? e.message : pm('create.errors.createFailed')
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
const editProbeResult = ref<ProbeURLResult | null>(null)
const editProbing   = ref(false)

function openEdit(p: Provider) {
  editProvider.value = p
  editName.value     = p.display_name
  editBaseUrl.value  = p.base_url ?? ''
  editProtocol.value = p.protocol ?? 'openai-completions'
  editNotes.value    = p.notes ?? ''
  editErr.value      = ''
  editProbeResult.value = null
  showEdit.value     = true
}

async function doEditProbe() {
  if (!editProvider.value) return
  const url = editBaseUrl.value.trim()
  if (!url) { editErr.value = pm('edit.errors.baseUrlRequired'); return }
  editProbing.value = true
  editProbeResult.value = null
  editErr.value = ''
  try {
    const r = await probeProviderURL(editProvider.value.id)
    editProbeResult.value = r
  } catch (e: unknown) {
    editProbeResult.value = { reachable: false, error: e instanceof Error ? e.message : pm('edit.errors.probeFailed') }
  } finally {
    editProbing.value = false
  }
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
    editErr.value = e instanceof Error ? e.message : pm('edit.errors.saveFailed')
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
const showCred         = ref(false)
const credProvider     = ref<Provider | null>(null)
const credKey          = ref('')
const credLabel        = ref('')
const credSaving       = ref(false)
const credErr          = ref('')
const credProbeStatus  = ref<string | null>(null) // "probing" | "done" | "failed"

function openCred(p: Provider) {
  credProvider.value = p
  credKey.value      = ''
  credLabel.value    = ''
  credErr.value      = ''
  credProbeStatus.value = null
  showCred.value     = true
}

async function submitCred() {
  if (!credKey.value) { credErr.value = pm('credential.errors.apiKeyRequired'); return }
  if (!credProvider.value) return
  credSaving.value = true
  credErr.value    = ''
  credProbeStatus.value = null
  try {
    const pid = credProvider.value.id
    const { id: credId } = await addCredential(pid, { api_key: credKey.value, label: credLabel.value || undefined })
    await loadCredentials(pid)

    // ── Auto probe after credential creation ──
    credProbeStatus.value = 'probing'
    credErr.value = ''
    try {
      await checkCredential(pid, credId)
      credProbeStatus.value = 'done'
      await loadCredentials(pid)
    } catch {
      credProbeStatus.value = 'failed'
    }

    const activeCount = (credentialsByProvider.value[pid] ?? []).filter((c) => c.status === 'active').length
    credProvider.value.active_credential_count = activeCount
    const listed = providers.value.find((row) => row.id === pid)
    if (listed) listed.active_credential_count = activeCount
    // Close after a brief delay so the user sees the probe result
    setTimeout(() => { showCred.value = false }, 1500)
  } catch (e: unknown) {
    credErr.value = e instanceof Error ? e.message : pm('credential.errors.addFailed')
  } finally {
    credSaving.value = false
  }
}

async function delCred(p: Provider, credId: number) {
  if (!confirm(pm('credential.errors.deleteConfirm'))) return
  try {
    await deleteCredential(p.id, credId)
    await loadCredentials(p.id)
    const activeCount = (credentialsByProvider.value[p.id] ?? []).filter((c) => c.status === 'active').length
    p.active_credential_count = activeCount
    const listed = providers.value.find((row) => row.id === p.id)
    if (listed) listed.active_credential_count = activeCount
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : pm('credential.errors.deleteFailed')
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
      [providerId]: e instanceof Error ? e.message : pm('credential.errors.loadFailed'),
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
    error.value = e instanceof Error ? e.message : pm('credential.errors.saveFailed')
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

// 2026-07-03 v738: routability badge helpers. The routability column is
// the orthogonal dimension to health_status — it answers "can this
// provider route traffic right now" rather than "are its credentials
// probe-healthy". A provider can have all credentials in
// health_status=healthy but still be routability=unavailable (e.g. all
// credentials in quota_exhausted state), and vice versa.
function routabilityBadgeClass(r?: string | null): string {
  if (r === 'available') return 'badge-green'
  if (r === 'unavailable') return 'badge-red'
  if (r === 'no_models') return 'badge-gray'
  if (r === 'manual_disabled') return 'badge-red'
  return 'badge-gray'
}
function routabilityLabel(r?: string | null): string {
  if (r === 'available') return pm('filter.routabilityBadgeAvailable')
  if (r === 'unavailable') return pm('filter.routabilityBadgeUnavailable')
  if (r === 'no_models') return pm('filter.routabilityBadgeNoModels')
  if (r === 'manual_disabled') return pm('filter.routabilityBadgeManualDisabled')
  return '—'
}

function healthLabel(status?: string | null): string {
  if (status === 'healthy')     return pm('list.health.healthy')
  if (status === 'warning')     return pm('list.health.warning')
  if (status === 'unreachable') return pm('list.health.unreachable')
  return pm('list.health.none')
}

function healthWarningLabel(code?: string | null): string {
  if (code === 'models_unavailable_but_probe_ok')     return pm('list.health.warningLabel.models_unavailable_but_probe_ok')
  if (code === 'probe_skipped_no_model')              return pm('list.health.warningLabel.probe_skipped_no_model')
  if (code === 'probe_failed_authentication_failed') return pm('list.health.warningLabel.probe_failed_authentication_failed')
  if (code === 'probe_failed_rate_limited')          return pm('list.health.warningLabel.probe_failed_rate_limited')
  if (code === 'probe_failed_request_failed')         return pm('list.health.warningLabel.probe_failed_request_failed')
  return ''
}

function timeText(v?: string | null): string {
  if (!v) return '—'
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString(localeRef.value, { hour12: false })
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
    error.value = e instanceof Error ? e.message : pm('credential.errors.toggleFailed')
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
    checkResults.value = { ...checkResults.value, [p.id]: r.reason === 'started' ? pm('check.providerStarted') : pm('check.providerRunning') }
    // Refresh after short delay to pick up health status
    setTimeout(() => load(), 5000)
  } catch (e: unknown) {
    checkResults.value = { ...checkResults.value, [p.id]: e instanceof Error ? e.message : pm('check.providerFailed') }
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
      [cred.id]: `${pm('check.credentialStatusPrefix', { status: r.health_status })}${r.health_source === 'models' ? pm('check.credentialModels') : r.probe_ok ? pm('check.credentialProbeOk') : pm('check.credentialUnreachable')}`,
    }
    // Refresh credentials to pick up new health status
    setTimeout(() => loadCredentials(prov.id), 3000)
  } catch (e: unknown) {
    credentialCheckResults.value = {
      ...credentialCheckResults.value,
      [cred.id]: e instanceof Error ? e.message : pm('check.credentialFailed'),
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
    diagnoseResult.value = r as never
  } catch (e: unknown) {
    diagnoseError.value = e instanceof Error ? e.message : pm('check.diagnoseFailed')
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
    case 'api':           return pm('list.source.api')
    case 'manifest':      return pm('list.source.manifest')
    case 'manifest_only': return pm('list.source.manifest_only')
    case 'none':          return pm('list.source.none')
    default:              return source || pm('list.source.none')
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
        // 2026-07-03 v738: send routability as its own filter. The backend
        // (admin/providers.go listProviders) supports the
        // ?routability=available|unavailable|no_models|manual_disabled|all
        // query param and post-filters rows. Default to 'available' on
        // first load so operators land on a "currently usable" view.
        routability: filterRoutability.value,
        has_free_model: hasFreeParam,
        // 2026-07-03 v738: the previous design had a client-side
        // "Show manually disabled" checkbox that sent manual_disabled=true
        // to the backend. With routability, manual_disabled rows now have
        // their own chip, so the bool toggle is removed from the UI.
        manual_disabled: 'all',
      }),
      getCatalog(),
    ])
    providers.value = p
    catalog.value   = c
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : pm('error.loadFailed')
  } finally {
    loading.value = false
  }
}

function onSearchInput() {
  writePersistedFilter({ search: filterSearch.value })
  if (_searchDebounceTimer) clearTimeout(_searchDebounceTimer)
  _searchDebounceTimer = setTimeout(() => load(), 300)
}

function onHealthStatusChange(status: 'all' | 'healthy' | 'warning' | 'unreachable' | 'unknown') {
  filterHealthStatus.value = status
  writePersistedFilter({ healthStatus: status })
  load()
}

function onRoutabilityChange(value: 'all' | 'available' | 'unavailable' | 'no_models' | 'manual_disabled') {
  filterRoutability.value = value
  writePersistedFilter({ routability: value })
  load()
}

function onFreeModelChange(value: 'all' | 'yes' | 'no') {
  filterFreeModel.value = value
  writePersistedFilter({ freeModel: value })
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
      <h2>{{ pm('page.title') }}</h2>
      <button class="btn btn-primary" @click="openAdd">{{ pm('page.addBtn') }}</button>
    </div>

    <div class="bg-status-bar" v-if="bgStatus">
      <div class="bg-status-item">
        <span class="bg-dot" :class="bgStatus.discovery.alive ? 'dot-green' : 'dot-red'"></span>
        <span class="bg-label">{{ pm('bgStatus.task.discovery') }}</span>
        <template v-if="bgStatus.discovery.running">
          <span class="badge badge-blue">{{ pm('bgStatus.task.discoveryRunning', { elapsed: fmtElapsed(bgStatus.discovery.elapsed_seconds) }) }}</span>
        </template>
        <template v-else-if="bgStatus.discovery.alive">
          <span class="badge badge-green">{{ pm('bgStatus.task.discoveryHealthy') }}</span>
          <span class="bg-muted">{{ pm('bgStatus.task.discoveryLast', { ago: fmtTimeAgo(bgStatus.discovery.finished_at) }) }}</span>
        </template>
        <template v-else>
          <span class="badge badge-red">{{ pm('bgStatus.task.discoveryStopped') }}</span>
        </template>
        <span class="bg-muted" v-if="bgStatus.discovery.error">{{ pm('bgStatus.task.discoveryError') }}{{ bgStatus.discovery.error.slice(0, 60) }}</span>
      </div>
      <div class="bg-status-item">
        <span class="bg-dot" :class="bgStatus.probe_loop.alive ? 'dot-green' : 'dot-red'"></span>
        <span class="bg-label">{{ pm('bgStatus.task.fastProbe') }}</span>
        <span class="badge" :class="bgStatus.probe_loop.alive ? 'badge-green' : 'badge-red'">{{ bgStatus.probe_loop.alive ? pm('bgStatus.task.fastProbeRunning') : pm('bgStatus.task.fastProbeStopped') }}</span>
        <span class="bg-muted" v-if="bgStatus.probe_loop.checks_last_10m != null">{{ pm('bgStatus.task.fastProbeCount', { n: bgStatus.probe_loop.checks_last_10m }) }}</span>
      </div>
      <div class="bg-status-item">
        <span class="bg-dot" :class="bgStatus.cycler.alive ? 'dot-green' : 'dot-red'"></span>
        <span class="bg-label">{{ pm('bgStatus.task.cycler') }}</span>
        <span class="badge" :class="bgStatus.cycler.alive ? 'badge-green' : 'badge-red'">{{ bgStatus.cycler.alive ? pm('bgStatus.task.cyclerRunning') : pm('bgStatus.task.cyclerStopped') }}</span>
        <span class="bg-muted" v-if="bgStatus.cycler.last_check_at">{{ pm('bgStatus.task.cyclerLast', { ago: fmtTimeAgo(bgStatus.cycler.last_check_at) }) }}</span>
      </div>
      <div class="bg-status-item">
        <span class="bg-dot" :class="bgStatus.recovery.alive ? 'dot-green' : 'dot-red'"></span>
        <span class="bg-label">{{ pm('bgStatus.task.recovery') }}</span>
        <span class="badge" :class="bgStatus.recovery.alive ? 'badge-green' : 'badge-red'">{{ bgStatus.recovery.alive ? pm('bgStatus.task.recoveryRunning') : pm('bgStatus.task.recoveryStopped') }}</span>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="loading" class="empty">{{ pm('page.loading') }}</div>

    <!-- ── Filter Bar ────────────────────────────────────────────────────── -->
    <div class="filter-bar" v-if="!loading">
      <div class="filter-search">
        <input
          v-model="filterSearch"
          @input="onSearchInput"
          :placeholder="pm('filter.searchPlaceholder')"
          class="filter-input"
        />
        <span class="filter-search-icon">🔍</span>
      </div>
      <div class="filter-tabs">
        <span class="filter-tab-label">{{ pm('filter.healthChipGroup') }}</span>
        <button
          v-for="opt in healthStatusOptions"
          :key="opt.value"
          class="filter-tab"
          :class="{ active: filterHealthStatus === opt.value }"
          @click="onHealthStatusChange(opt.value as 'all' | 'healthy' | 'warning' | 'unreachable' | 'unknown')"
        >{{ opt.label }}</button>
      </div>
      <div class="filter-divider" aria-hidden="true"></div>
      <!-- 2026-07-03 v738: new "可路由性" chip group. This is the
           dimension that drives the "全部 / 可用 / 不可用 / 无模型 /
           已禁用" semantics the user reported. Default to "可用" so
           the operator lands on a routable-only view. -->
      <div class="filter-tabs">
        <span class="filter-tab-label">{{ pm('filter.routabilityChipGroup') }}</span>
        <button
          v-for="opt in routabilityOptions"
          :key="opt.value"
          class="filter-tab"
          :class="{ active: filterRoutability === opt.value }"
          @click="onRoutabilityChange(opt.value as 'all' | 'available' | 'unavailable' | 'no_models' | 'manual_disabled')"
        >{{ opt.label }}</button>
      </div>
      <div class="filter-divider" aria-hidden="true"></div>
      <div class="filter-tabs">
        <span class="filter-tab-label">{{ pm('filter.freeModelGroup') }}</span>
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
            <th>{{ pm('list.table.displayName') }}</th>
            <th>{{ pm('list.table.channel') }}</th>
            <th>{{ pm('list.table.catalogCode') }}</th>
            <th>{{ pm('list.table.headerProfile') }}</th>
            <th>{{ pm('list.table.baseUrl') }}</th>
            <th>{{ pm('list.table.credentials') }}</th>
            <th>{{ pm('list.table.availableModels') }}</th>
            <th>{{ pm('list.table.freeModels') }}</th>
            <th>{{ pm('list.table.errorRate24h') }}</th>
            <th>{{ pm('list.table.health') }}</th>
            <!-- 2026-07-03 v738: new column showing routability
                 (available / unavailable / no_models / manual_disabled).
                 The "可用" / "不可用" semantics that used to live on
                 health_status now live on routability. -->
            <th>{{ pm('filter.routabilityChipGroup') }}</th>
            <th>{{ pm('list.table.status') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="p in visibleProviders"
            :key="p.id"
            class="provider-row"
            :class="{ 'row-manual-disabled': p.manual_disabled }"
            tabindex="0"
            @click="router.push('/providers/' + p.id)"
            @keydown.enter="router.push('/providers/' + p.id)"
          >
            <td>
              <div style="font-weight:500;display:flex;align-items:center;gap:6px;flex-wrap:wrap">
                <span>{{ p.display_name }}</span>
                <!-- 2026-07-03 v738: surface manual_disabled at a glance so
                     the operator cannot mistake a disabled vendor for an
                     active one with healthy credentials. -->
                <span
                  v-if="p.manual_disabled"
                  class="badge badge-red"
                  :title="pm('list.manualDisabledTooltip')"
                >{{ pm('list.manualDisabledBadge') }}</span>
              </div>
              <div style="font-size:11px;color:var(--muted)" v-if="p.notes">{{ p.notes }}</div>
            </td>
            <td>
              <span class="badge" :class="providerChannelLabel(p.category).cls">
                {{ providerChannelLabel(p.category).label }}
              </span>
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
                :title="(p.free_model_count ?? 0) > 0 ? pm('filter.freeBadgeTooltipYes') : pm('filter.freeBadgeTooltipNo')"
              >{{ p.free_model_count ?? 0 }}</span>
            </td>
            <td>
              <span style="font-size:12px">{{ (p as any).error_rate_24h != null ? Number((p as any).error_rate_24h).toFixed(1) + '%' : '—' }}</span>
            </td>
            <td>
              <span class="badge" :class="healthBadgeClass(p.health_status)">
                {{ healthLabel(p.health_status) }}
              </span>
              <div class="muted">{{ pm('list.checkedAtPrefix') }} {{ timeText(p.health_checked_at) }}</div>
              <div class="muted" v-if="(p.warning_credential_count ?? 0) > 0">{{ pm('list.warningsPrefix') }} {{ p.warning_credential_count }}</div>
            </td>
            <td>
              <span class="badge" :class="routabilityBadgeClass(p.routability)">
                {{ routabilityLabel(p.routability) }}
              </span>
              <div class="muted" v-if="p.routable_binding_count != null && p.total_binding_count != null">
                {{ p.routable_binding_count }} / {{ p.total_binding_count }}
              </div>
            </td>
            <td>
              <span
                v-if="p.manual_disabled"
                class="badge badge-red"
                :title="pm('list.manualDisabledTooltip')"
              >{{ pm('list.manualDisabledBadge') }}</span>
              <span v-else class="badge" :class="p.enabled ? 'badge-green' : 'badge-gray'">
                {{ p.enabled ? pm('list.enabledBadge') : pm('list.disabledBadge') }}
              </span>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-if="!loading && visibleProviders.length === 0" class="empty">{{ pm('list.empty') }}</div>
    </div>

    <!-- ── Add Provider Modal ─────────────────────────────────────────────── -->
    <div class="modal-overlay" v-if="showAdd" @click.self="showAdd = false">
      <div class="modal" style="max-width:500px" @click.stop>
        <h3>{{ pm('create.title') }}</h3>
        <div v-if="addErr" class="alert alert-danger">{{ addErr }}</div>

        <!-- Toggle custom mode -->
        <div class="form-group" style="display:flex;align-items:center;gap:10px">
          <input id="customToggle" type="checkbox" v-model="isCustom" style="width:auto" />
          <label for="customToggle" style="margin:0;cursor:pointer">{{ pm('create.customToggle') }}</label>
        </div>

        <!-- Catalog mode -->
        <template v-if="!isCustom">
          <div class="form-group">
            <label>{{ pm('create.selectCatalog') }}</label>
            <select v-model="addCode" @change="onCatalogChange">
              <option v-for="c in catalog" :key="c.code" :value="c.code">
                {{ c.display_name }} ({{ c.code }})
              </option>
            </select>
          </div>
          <div class="form-group">
            <label>{{ pm('create.displayNameLabel') }}</label>
            <input v-model="addName" :placeholder="pm('create.displayNamePlaceholder')" />
          </div>
        </template>

        <!-- Custom mode -->
        <template v-else>
          <div class="form-group">
            <label>{{ pm('create.providerCodeLabel') }} <span style="color:var(--danger)">*</span></label>
            <input v-model="addCodeCustom" :placeholder="pm('create.providerCodePlaceholder')" />
          </div>
          <div class="form-group">
            <label>{{ pm('create.providerNameLabel') }} <span style="color:var(--danger)">*</span></label>
            <input v-model="addName" :placeholder="pm('create.providerNamePlaceholder')" />
          </div>
          <div class="form-group">
            <label>{{ pm('create.protocolLabel') }}</label>
            <select v-model="addProtocol">
              <option value="openai-completions">{{ pm('create.protocols.openai-completions') }}</option>
              <option value="anthropic">{{ pm('create.protocols.anthropic') }}</option>
              <option value="ollama">{{ pm('create.protocols.ollama') }}</option>
              <option value="cohere">{{ pm('create.protocols.cohere') }}</option>
              <option value="gemini">{{ pm('create.protocols.gemini') }}</option>
            </select>
          </div>
        </template>

        <!-- Base URL (always shown) -->
        <div class="form-group">
          <label>Base URL{{ isCustom ? pm('create.baseUrlRequired') : pm('create.baseUrlOptional') }}</label>
          <div style="display:flex;gap:8px">
            <input
              v-model="addBaseUrl"
              :placeholder="isCustom ? pm('create.baseUrlPlaceholder') : (selectedCat?.base_url_template || pm('create.baseUrlPlaceholderFallback'))"
              style="flex:1"
            />
            <button
              class="btn btn-sm"
              :class="addProbeResult?.reachable ? 'btn-green' : 'btn-ghost'"
              @click="doProbe"
              :disabled="addProbing || !addBaseUrl.trim()"
              :title="pm('create.probeTooltip')"
            >{{ addProbing ? pm('create.probeBtnProbing') : pm('create.probeBtn') }}</button>
          </div>
          <div v-if="isCustom && selectedCat" style="font-size:11px;color:var(--muted);margin-top:4px">
            {{ pm('create.probeHintCatalogMatch') }}{{ selectedCat.base_url_template }}
          </div>
          <div v-if="addProbeResult" style="margin-top:6px;font-size:12px">
            <template v-if="addProbeResult.reachable">
              <span style="color:var(--success)">{{ pm('create.probeOk') }}</span>
              <span v-if="addProbeResult.protocol" class="probe-detail probe-muted">
                {{ pm('create.probeOkProtocol') }}{{ addProbeResult.protocol }}
              </span>
              <span v-if="addProbeResult.models_count != null" class="probe-detail probe-muted">
                {{ pm('create.probeOkModels') }}{{ addProbeResult.models_count }}
              </span>
              <span v-if="addProbeResult.auth_ok === false" class="probe-detail probe-warning">
                {{ pm('create.probeWarn') }}
              </span>
            </template>
            <template v-else>
              <span style="color:var(--danger)">{{ pm('create.probeFail') }}</span>
              <span v-if="addProbeResult.error" class="probe-detail probe-muted">{{ addProbeResult.error }}</span>
            </template>
          </div>
        </div>

        <div class="form-group">
          <label>{{ pm('create.remarkLabel') }}</label>
          <input v-model="addNotes" :placeholder="pm('create.remarkPlaceholder')" />
        </div>

        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" @click="showAdd = false">{{ pm('common.button.cancel') }}</button>
          <button class="btn btn-primary" @click="submitAdd" :disabled="addSaving">
            {{ addSaving ? pm('create.submitting') : pm('create.submit') }}
          </button>
        </div>
      </div>
    </div>

    <!-- ── Edit Provider Modal ───────────────────────────────────────────── -->
    <div class="modal-overlay" v-if="showEdit" @click.self="showEdit = false">
      <div class="modal" style="max-width:500px" @click.stop>
        <h3>{{ pm('edit.title', { name: editProvider?.display_name }) }}</h3>
        <div v-if="editErr" class="alert alert-danger">{{ editErr }}</div>
        <div class="form-group">
          <label>{{ pm('edit.catalogCode') }}</label>
          <input :value="editProvider?.catalog_code || '—'" disabled class="muted" />
        </div>
        <div class="form-group">
          <label>{{ pm('edit.vendor') }}</label>
          <input :value="editProvider?.vendor_name || '—'" disabled class="muted" />
        </div>
        <div class="form-group">
          <label>{{ pm('edit.headerProfile') }}</label>
          <input :value="editProvider?.header_profile_code || '—'" disabled class="muted" />
        </div>
        <div class="form-group">
          <label>{{ pm('edit.displayName') }}</label>
          <input v-model="editName" :placeholder="pm('edit.displayNamePlaceholder')" />
        </div>
        <div class="form-group">
          <label>{{ pm('edit.protocol') }}</label>
          <select v-model="editProtocol">
            <option value="openai-completions">{{ pm('edit.protocols.openai-completions') }}</option>
            <option value="openai-responses">{{ pm('edit.protocols.openai-responses') }}</option>
            <option value="anthropic-messages">{{ pm('edit.protocols.anthropic-messages') }}</option>
            <option value="gemini-generate">{{ pm('edit.protocols.gemini-generate') }}</option>
          </select>
        </div>
        <div class="form-group">
          <label>{{ pm('edit.baseUrl') }}</label>
          <div style="display:flex;gap:8px">
            <input v-model="editBaseUrl" :placeholder="pm('edit.baseUrlPlaceholder')" style="flex:1" />
            <button
              class="btn btn-sm"
              :class="editProbeResult?.reachable ? 'btn-green' : 'btn-ghost'"
              @click="doEditProbe"
              :disabled="editProbing || !editBaseUrl.trim()"
              :title="pm('edit.probeTooltip')"
            >{{ editProbing ? pm('edit.probeBtnProbing') : pm('edit.probeBtn') }}</button>
          </div>
          <div v-if="editProbeResult" style="margin-top:6px;font-size:12px">
            <template v-if="editProbeResult.reachable">
              <span style="color:var(--success)">{{ pm('edit.probeOk') }}</span>
              <span v-if="editProbeResult.protocol" class="probe-detail probe-muted">
                {{ pm('edit.probeOkProtocol') }}{{ editProbeResult.protocol }}
              </span>
              <span v-if="editProbeResult.models_count != null" class="probe-detail probe-muted">
                {{ pm('edit.probeOkModels', { n: editProbeResult.models_count }) }}
              </span>
              <span v-if="editProbeResult.auth_ok === false" class="probe-detail probe-warning">
                {{ pm('edit.probeWarn') }}
              </span>
            </template>
            <template v-else>
              <span style="color:var(--danger)">{{ pm('edit.probeFail') }}</span>
              <span v-if="editProbeResult.error" class="probe-detail probe-muted">{{ editProbeResult.error }}</span>
            </template>
          </div>
        </div>
        <div class="form-group">
          <label>{{ pm('edit.remark') }}</label>
          <input v-model="editNotes" :placeholder="pm('edit.remarkPlaceholder')" />
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" @click="showEdit = false">{{ pm('common.button.cancel') }}</button>
          <button class="btn btn-primary" @click="submitEdit" :disabled="editSaving">
            {{ editSaving ? t('keys.common.loading') : pm('common.button.save') }}
          </button>
        </div>
      </div>
    </div>

    <!-- ── Manage Credentials Modal ───────────────────────────────────────── -->
    <div class="drawer-backdrop" v-if="showManageCred && manageProvider" @click="closeManageCred">
      <div class="drawer-panel card drawer-panel-wide" @click.stop>
        <div class="credential-toolbar">
          <div>
            <h3 style="margin:0">{{ pm('credential.drawerTitle', { name: manageProvider.display_name }) }}</h3>
            <div class="muted" style="margin-top:4px">
              {{ manageProvider.catalog_code }} · {{ manageProvider.base_url || '—' }}
            </div>
          </div>
          <div style="display:flex;gap:8px;flex-shrink:0">
            <button
              class="btn btn-ghost btn-sm"
              @click="loadCredentials(manageProvider.id)"
              :disabled="credentialLoading[manageProvider.id]"
            >{{ pm('credential.refresh') }}</button>
            <button class="btn btn-primary btn-sm" @click="openCred(manageProvider)">{{ pm('credential.addBtn') }}</button>
            <button class="btn btn-ghost btn-sm" @click="closeManageCred">{{ t('keys.common.close') }}</button>
          </div>
        </div>
        <div v-if="credentialErrors[manageProvider.id]" class="alert alert-danger">{{ credentialErrors[manageProvider.id] }}</div>
        <div v-if="credentialLoading[manageProvider.id]" class="empty">{{ t('keys.common.loading') }}</div>
        <div v-else class="credential-scroll">
          <table class="credential-table">
            <thead>
              <tr>
                <th>{{ pm('credential.table.id') }}</th>
                <th>{{ pm('credential.table.status') }}</th>
                <th>{{ pm('credential.table.probe') }}</th>
                <th>{{ pm('credential.table.models') }}</th>
                <th>{{ pm('credential.table.concurrency') }}</th>
                <th>{{ pm('credential.table.expires') }}</th>
                <th>{{ pm('credential.table.usage') }}</th>
                <th>{{ pm('credential.table.tags') }}</th>
                <th>{{ pm('common.table.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="c in credentialsByProvider[manageProvider.id] || []" :key="c.id">
                <td>
                  <input v-model="c.label" class="compact-input" />
                  <div class="muted">{{ pm('credential.row.idTrustLine', { id: c.id, trust: c.trust_level }) }}</div>
                </td>
                <td>
                  <select v-model="c.status" class="compact-input">
                    <option v-for="s in credentialStatuses" :key="s.value" :value="s.value">{{ s.label }}</option>
                  </select>
                  <div><span class="badge" :class="statusBadgeClass(c.status)">{{ c.status }}</span></div>
                </td>
                <td>
                  <div><span class="badge" :class="healthBadgeClass(c.health_status)">{{ healthLabel(c.health_status) }}</span></div>
                  <div class="muted">{{ pm('list.checkedAtPrefix') }} {{ timeText(c.health_checked_at) }}</div>
                  <div class="muted" v-if="c.health_warning_code">{{ healthWarningLabel(c.health_warning_code) }}</div>
                  <div class="muted" v-if="c.health_probe_model">{{ pm('credential.row.healthProbeModel', { model: c.health_probe_model }) }}</div>
                  <div class="muted health-error" v-if="c.health_error">{{ c.health_error }}</div>
                </td>
                <td>
                  <div>
                    <span
                      class="badge"
                      :class="c.api_models_ok === true ? 'badge badge-green' : c.api_models_ok === false ? 'badge badge-red' : 'badge'"
                      :title="c.api_models_ok === true ? pm('credential.row.apiOkTitle') : c.api_models_ok === false ? pm('credential.row.apiFailTitlePrefix') + (c.api_models_error || '') : pm('credential.row.apiUnknownTitle')"
                    >{{ c.api_models_ok === true ? pm('credential.row.apiOk') : c.api_models_ok === false ? pm('credential.row.apiFail') : pm('credential.row.apiUnknown') }}</span>
                  </div>
                  <div class="muted" v-if="c.api_models_last_checked_at">{{ pm('list.checkedAtPrefix') }} {{ timeText(c.api_models_last_checked_at) }}</div>
                  <div class="muted health-error" v-if="c.api_models_error">{{ c.api_models_error }}</div>
                </td>
                <td>
                  <input v-model.number="c.concurrency_limit" type="number" min="0" class="compact-input number" :placeholder="pm('credential.row.concurrencyPlaceholder')" />
                  <div v-if="c.fp_slot_limit != null" class="muted" style="font-size:11px;margin-top:4px">
                    {{ pm('credential.row.slotUsage', { used: c.fp_slots_used ?? 0, limit: c.fp_slot_limit }) }}
                    <span v-if="(c.fp_slots_free ?? 0) === 0" style="color:var(--danger)">{{ pm('credential.row.slotFull') }}</span>
                    <span v-else>{{ pm('credential.row.slotFree', { free: c.fp_slots_free }) }}</span>
                  </div>
                  <div v-else class="muted" style="font-size:11px;margin-top:4px">{{ pm('credential.row.slotUnlimited') }}</div>
                </td>
                <td>
                  <input :value="asDateInput(c.effective_at)" type="datetime-local" class="compact-input" @input="setDateInputFromEvent(c, 'effective_at', $event)" />
                  <input :value="asDateInput(c.expires_at)" type="datetime-local" class="compact-input" @input="setDateInputFromEvent(c, 'expires_at', $event)" />
                </td>
                <td>
                  <div>{{ c.total_requests }}{{ pm('credential.row.usageSeparator') }}{{ money(c.total_cost_usd) }}</div>
                  <div class="muted">{{ pm('credential.row.balanceLabel') }} <input v-model.number="c.balance_usd" type="number" min="0" step="100" class="compact-input number" style="width:80px;display:inline-block" placeholder="—" /></div>
                  <div v-if="c.quota_summary?.any_exhausted" class="badge badge-red">{{ pm('credential.row.quotaExhausted') }}</div>
                </td>
                <td>
                  <input :value="tagsText(c)" class="compact-input" :placeholder="pm('credential.row.tagsPlaceholder')" @input="setTagsTextFromEvent(c, $event)" />
                  <input v-model="c.notes" class="compact-input" :placeholder="pm('credential.row.notesPlaceholder')" />
                </td>
                <td>
                  <button class="btn btn-primary btn-sm" @click="saveCredential(manageProvider, c)" :disabled="credentialSaving[c.id]">
                    {{ credentialSaving[c.id] ? t('keys.common.loading') : pm('common.button.save') }}
                  </button>
                  <button
                    class="btn btn-ghost btn-sm"
                    @click="checkSingleCredential(manageProvider, c)"
                    :disabled="checkingCredential[c.id]"
                    :title="pm('credential.row.checkTooltip')"
                  >{{ checkingCredential[c.id] ? pm('credential.row.checkingBtn') : pm('credential.row.checkBtn') }}</button>
                  <button class="btn btn-ghost btn-sm" @click="openDiagnose(manageProvider)">{{ pm('credential.row.diagnose') }}</button>
                  <button class="btn btn-ghost btn-sm" @click="delCred(manageProvider, c.id)">{{ pm('credential.row.disable') }}</button>
                  <div v-if="credentialCheckResults[c.id]" style="font-size:11px;color:var(--muted);margin-top:4px">
                    {{ credentialCheckResults[c.id] }}
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-if="!(credentialsByProvider[manageProvider.id] || []).length" class="empty">{{ pm('credential.empty') }}</div>
        </div>
      </div>
    </div>

    <!-- ── Diagnose Modal ───────────────────────────────────────────────── -->
    <div class="drawer-backdrop" style="z-index:110" v-if="diagnoseProviderId !== null" @click="closeDiagnose">
      <div class="drawer-panel card drawer-panel-wide" @click.stop>
        <h3>{{ pm('diagnose.title') }} <span class="muted" style="font-size:12px">{{ pm('diagnose.subtitle') }}</span></h3>
        <div v-if="diagnoseLoading" class="muted">{{ pm('diagnose.loading') }}</div>
        <div v-else-if="diagnoseError" class="alert alert-danger">{{ diagnoseError }}</div>
        <div v-else-if="diagnoseResult">
          <div class="muted" style="margin-bottom:12px">
            {{ pm('diagnose.summary', { n: diagnoseResult.credential_count }) }}
          </div>
          <table class="data-table">
            <thead>
              <tr>
                <th>{{ pm('diagnose.table.credential') }}</th>
                <th>{{ pm('diagnose.table.modelSource') }}</th>
                <th>{{ pm('diagnose.table.health') }}</th>
                <th>{{ pm('diagnose.table.statusCode') }}</th>
                <th>{{ pm('diagnose.table.latency') }}</th>
                <th>{{ pm('diagnose.table.endpoint') }}</th>
                <th>{{ pm('diagnose.table.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="r in diagnoseResult.results" :key="r.credential_id">
                <tr>
                  <td>
                    <div>#{{ r.credential_id }}</div>
                    <div class="muted" v-if="r.effective_source === 'manifest_only'">{{ pm('diagnose.manifestOnly') }}</div>
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
                    <div class="muted" v-if="r.attempt_index > 0">{{ pm('diagnose.attemptFooter', { n: r.attempt_index + 1 }) }}</div>
                  </td>
                  <td>
                    <span v-if="r.health_latency_ms !== null">{{ r.health_latency_ms }} {{ pm('diagnose.msUnit') }}</span>
                    <span v-else class="muted">—</span>
                  </td>
                  <td>
                    <code style="font-size:11px">{{ r.models_endpoint_template || pm('diagnose.autoEndpoint') }}</code>
                    <div class="muted" v-if="r.models_endpoint_resolved">
                      → <code style="font-size:11px">{{ r.models_endpoint_resolved }}</code>
                    </div>
                  </td>
                  <td>
                    <button class="btn btn-ghost btn-sm" @click="toggleCredDetail(r.credential_id)">
                      {{ expandedCredId === r.credential_id ? pm('diagnose.collapse') : pm('diagnose.expand') }}
                    </button>
                  </td>
                </tr>
                <tr v-if="expandedCredId === r.credential_id">
                  <td colspan="7" style="background:rgba(0,0,0,0.2);padding:12px">
                    <div class="diag-section">
                      <h4>{{ pm('diagnose.reqHeader') }}</h4>
                      <div><strong>{{ pm('diagnose.reqUrlLabel') }}</strong> <code style="font-size:12px">{{ r.request_url || pm('diagnose.reqNotSent') }}</code></div>
                      <div><strong>{{ pm('diagnose.reqMethodLabel') }}</strong> <code>{{ r.request_method }}</code></div>
                      <div><strong>{{ pm('diagnose.reqHeadersLabel') }}</strong>
                        <pre style="margin:4px 0;padding:8px;background:rgba(0,0,0,0.3);border-radius:4px;font-size:11px;overflow-x:auto">{{ asJson(r.request_headers_sanitized) }}</pre>
                      </div>
                      <div v-if="r.request_body_preview"><strong>{{ pm('diagnose.reqBodyLabel') }}</strong>
                        <pre style="margin:4px 0;padding:8px;background:rgba(0,0,0,0.3);border-radius:4px;font-size:11px;overflow-x:auto">{{ r.request_body_preview }}</pre>
                      </div>
                    </div>
                    <div class="diag-section" style="margin-top:12px">
                      <h4>{{ pm('diagnose.respHeader') }}</h4>
                      <div><strong>{{ pm('diagnose.respStatusLabel') }}</strong> <code>{{ r.response_status || pm('diagnose.respNoResponse') }}</code></div>
                      <div v-if="r.response_headers && Object.keys(r.response_headers).length"><strong>{{ pm('diagnose.respHeadersLabel') }}</strong>
                        <pre style="margin:4px 0;padding:8px;background:rgba(0,0,0,0.3);border-radius:4px;font-size:11px;overflow-x:auto">{{ asJson(r.response_headers) }}</pre>
                      </div>
                      <div v-if="r.response_body_preview"><strong>{{ pm('diagnose.respBodyLabel') }}</strong>
                        <pre style="margin:4px 0;padding:8px;background:rgba(0,0,0,0.3);border-radius:4px;font-size:11px;overflow-x:auto;max-height:200px">{{ r.response_body_preview }}</pre>
                      </div>
                    </div>
                    <div v-if="r.health_error" class="diag-section" style="margin-top:12px">
                      <h4>{{ pm('diagnose.errHeader') }}</h4>
                      <pre style="margin:4px 0;padding:8px;background:rgba(180,40,40,0.2);border-radius:4px;font-size:11px;overflow-x:auto">{{ r.health_error }}</pre>
                    </div>
                    <div v-if="r.returned_models && r.returned_models.length" class="diag-section" style="margin-top:12px">
                      <h4>{{ pm('diagnose.modelsHeader', { n: r.returned_models.length }) }}</h4>
                      <div style="font-size:11px;line-height:1.6">
                        <code v-for="m in r.returned_models.slice(0, 30)" :key="m" class="diag-model-chip">{{ m }}</code>
                        <span v-if="r.returned_models.length > 30" class="muted">{{ pm('diagnose.modelsTruncated', { n: r.returned_models.length }) }}</span>
                      </div>
                    </div>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-primary" @click="closeDiagnose">{{ t('keys.common.close') }}</button>
        </div>
      </div>
    </div>

    <!-- ── Add Credential Modal ──────────────────────────────────────────── -->
    <div class="modal-overlay" style="z-index:110" v-if="showCred" @click.self="showCred = false">
      <div class="modal" @click.stop>
        <h3>{{ pm('credential.addDialog.title', { name: credProvider?.display_name }) }}</h3>
        <div v-if="credErr" class="alert alert-danger">{{ credErr }}</div>
        <div class="form-group">
          <label>{{ pm('credential.addDialog.apiKeyLabel') }}</label>
          <input v-model="credKey" :placeholder="pm('credential.addDialog.apiKeyPlaceholder')" />
        </div>
        <div class="form-group">
          <label>{{ pm('credential.addDialog.tagsLabel') }}</label>
          <input v-model="credLabel" :placeholder="pm('credential.addDialog.tagsPlaceholder')" />
        </div>
        <div v-if="credProbeStatus" class="alert" :class="credProbeStatus === 'done' ? 'alert-success' : credProbeStatus === 'failed' ? 'alert-warning' : 'alert-info'">
          <template v-if="credProbeStatus === 'probing'">{{ pm('credential.addDialog.probeStatusProbing') }}</template>
          <template v-else-if="credProbeStatus === 'done'">{{ pm('credential.addDialog.probeStatusDone') }}</template>
          <template v-else-if="credProbeStatus === 'failed'">{{ pm('credential.addDialog.probeStatusFailed') }}</template>
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" @click="showCred = false">{{ pm('common.button.cancel') }}</button>
          <button class="btn btn-primary" @click="submitCred" :disabled="credSaving">
            {{ credSaving ? pm('credential.addDialog.submitting') : (credProbeStatus === 'probing' ? pm('credential.addDialog.probeBtn') : pm('credential.addDialog.submit')) }}
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
  gap: 8px;
  margin-bottom: 12px;
  flex-wrap: nowrap;
  overflow-x: auto;
}
.filter-search {
  position: relative;
  flex: 1;
  min-width: 200px;
  max-width: 320px;
}
.filter-input {
  width: 100%;
  padding: 6px 10px 6px 30px;
  font-size: 12px;
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
  margin-inline-end: 2px;
}
.bg-muted {
  color: var(--muted);
  font-size: 11px;
}
.badge-blue {
  background: rgba(33,150,243,.18);
  color: #42a5f5;
}
.badge-orange {
  background: rgba(210,153,34,.15);
  color: var(--warning);
}
.provider-row {
  cursor: pointer;
}
.provider-row:hover td {
  background: rgba(99, 102, 241, 0.06);
}
.provider-row:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: -2px;
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

.probe-detail {
  margin-inline-start: 8px;
}
.probe-muted {
  color: var(--muted);
}
.probe-warning {
  color: var(--warning);
}

.diag-model-chip {
  margin-inline-end: 6px;
  display: inline-block;
  padding: 2px 6px;
  background: rgba(0, 255, 128, 0.1);
  border-radius: 3px;
}

/* 2026-07-03 v738: visually de-emphasize manually-disabled rows so an
   operator scanning the table cannot mistake them for active vendors.
   Same palette as the manual_disabled badge (red tint). */
.provider-row.row-manual-disabled {
  background: rgba(255, 80, 80, 0.06);
}
.provider-row.row-manual-disabled:hover {
  background: rgba(255, 80, 80, 0.12);
}
</style>
