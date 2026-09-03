<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useFormat } from '../../i18n/useFormat'
import { ElMessage } from 'element-plus'
import {
  updateCredential, deleteCredential, checkCredential,
  addCredential,
  setCredentialManualDisabled, setDefaultProbeModel, pickDefaultProbeModel,
  resetCredentialAvailability, resetCredentialQuota, forceRecoverCredential,
  CREDENTIAL_LIFECYCLE_STATUSES, updateCredentialLifecycle, resetCredentialFpSlots,
  releaseCredentialFpSlot,
  getCredentialFpSlotStats, type FpSlotStats,
  type CredentialLifecycleStatus, type ProviderCredential, type CredentialStatus,
  getCredentialModels, type ModelOffer,
  revealCredentialKey,
  rotateCredentialPrimaryKey,
} from '../../api'
import { isSuperAdmin } from '../../store'
import FpSlotVisualizer from '../../components/FpSlotVisualizer.vue'
import CredentialModelsPanel from './CredentialModelsPanel.vue'

const { t: td } = useI18n()
const pd = (k: string, params?: Record<string, unknown>): string =>
  td(`providerDetail.${k}` as never, params as never)
const { fmtDateTime } = useFormat()

const props = defineProps<{
  provider: any
  creds: ProviderCredential[]
}>()
// 2026-07-03: `refresh` triggers a full parent reload (loading state +
// component unmount), which silently destroys the open drawer. Use
// `silentRefresh` for inline edits that must keep the drawer mounted —
// plan type, immediate probe check, manual disable toggle, lifecycle
// change, default probe model pick — so the operator can keep working
// without losing context or half-typed fields.
const emit = defineEmits<{ refresh: []; silentRefresh: []; openErrorDetail: [credentialId: number] }>()

// 2026-09-04: default 租户 tenant_admin 进入本页时为只读 + 凭据 API Key
// 轮换（后端 ProviderConsoleMiddleware 同口径）：除「修改」API Key 外，
// 抽屉内全部编辑控件禁用/隐藏，写接口对其 403。
const canManageCreds = computed(() => isSuperAdmin())

const selected = ref<ProviderCredential | null>(null)
const drawerTab = ref<'info' | 'models'>('info')
watch(() => selected.value?.id, () => { drawerTab.value = 'info' })
const saving = ref(false)
const checking = ref(false)
const saveMsg = ref('')
const checkMsg = ref('')

// 2026-08-31 hzx-2 round-5: cached cmb list for the default-probe-model
// <select>. Loaded once per drawer open so toggling between the info and
// models tab does not refetch. Cleared on close.
const drawerModels = ref<ModelOffer[]>([])
const drawerModelsLoading = ref(false)
async function loadDrawerModels() {
  if (!selected.value) {
    drawerModels.value = []
    return
  }
  drawerModelsLoading.value = true
  try {
    drawerModels.value = await getCredentialModels(props.provider.id, selected.value.id)
  } catch (e: unknown) {
    drawerModels.value = []
    // Best-effort: surface only as console; the UI falls back to free-text
    // input when drawerModels is empty, which is a strictly safer state
    // than showing a stale select.
    // eslint-disable-next-line no-console
    console.warn('load drawer models failed', e)
  } finally {
    drawerModelsLoading.value = false
  }
}

// probeModelOptions: routable bindings under this credential, projected
// to the value the probe layer actually consumes —
// COALESCE(outbound_model_name, raw_model_name), i.e. the PROVIDER-facing
// name the probe request sends verbatim. standardized_name is kept in the
// label for readability. Skips admin_protected and unavailable rows so
// the operator cannot accidentally pin a model that's already been
// retired by the probe path. Value projection matches
// modelcatalog.AutoFillDefaultProbeModel / bg/shared_pick.go — keep the
// three in sync (a standardized_name value here would 404 on NIM-style
// vendors whose raw name carries a "z-ai/..." prefix).
const probeModelOptions = computed(() =>
  drawerModels.value
    .filter(m => m.available && !m.admin_protected)
    .map(m => {
      const value = (m.outbound_model_name && m.outbound_model_name.trim() !== '')
        ? m.outbound_model_name
        : m.raw_model_name
      const std = (m.standardized_name && m.standardized_name.trim() !== '') ? m.standardized_name : ''
      const label = (std && std !== value) ? `${std}  (${value})` : value
      return { value, label, offer: m }
    })
    // Dedup by value: two cmb rows may share the same probe-facing name
    // (e.g. one via canonical_id backfill, one via direct). Show each
    // value only once to avoid confusing the operator.
    .filter((opt, idx, arr) => arr.findIndex(o => o.value === opt.value) === idx)
)
// probeModelHasOptions: true when the operator can pick from a list.
// Drives the <select> vs <input> branching in the template.
const probeModelHasOptions = computed(() => probeModelOptions.value.length > 0)

// selectedDefaultModel: staging value for the inline picker. Synced to
// selected.default_probe_model on drawer open so the operator sees the
// current value immediately; reset on close. The Save button calls
// setDefaultModel() which compares against selected.default_probe_model
// before issuing the PATCH (no-op when unchanged).
const selectedDefaultModel = ref<string>('')
watch(() => selected.value?.id, (id) => {
  selectedDefaultModel.value = selected.value?.default_probe_model ?? ''
})
// saveMsgKind tags the latest saveMsg so the UI can apply a targeted
// style (e.g. red row for constraint violations) without parsing the
// message string. Set alongside saveMsg in formatCredentialError callers.
const saveMsgKind = ref<string>('')
// saveMsgRejectCtx holds the structured error.context from the latest
// PATCH rejection so the "恢复到建议值" button can pre-fill the form
// with the values the server says would have worked. Cleared on open
// and after each successful save.
const saveMsgRejectCtx = ref<{
  attempted_concurrency?: number | null
  attempted_fp_slot?: number | null
  current_concurrency?: number | null
  current_fp_slot?: number | null
} | null>(null)

const showAddCred = ref(false)
const addCredKey = ref('')
const addCredLabel = ref('')
// Add-modal concurrency / fp-slot fields. Defaults mirror the server-side
// handler (admin/provider_credential.go addCredential) and the
// auto_set_fp_slot_limit trigger (migration 039) so the form shows a
// usable starting point that satisfies credentials_fp_slot_vs_concurrency.
const DEFAULT_CONCURRENCY = 10
const DEFAULT_FP_SLOT = 20
const addCredConcurrency = ref<number | null>(DEFAULT_CONCURRENCY)
const addCredFpSlot = ref<number | null>(DEFAULT_FP_SLOT)
// Set to true once the user manually edits the fp-slot input. While false,
// the fp-slot value tracks the auto-computed default (max(1, floor(concurrency/4)))
// so changing concurrency also updates fp-slot. Once the user has "taken
// control" of the field, we respect their override and stop auto-syncing.
const addCredFpSlotTouched = ref(false)
const addCredSaving = ref(false)
const addCredErr = ref('')
const addCredErrKind = ref<string>('')
// addCredRejectCtx captures the structured error.context from a
// constraint-rejection 400, so the "恢复到建议值" button can pre-fill
// the input with the values the server says would have worked. Cleared
// whenever the user starts a new attempt.
const addCredRejectCtx = ref<{
  attempted_concurrency?: number | null
  attempted_fp_slot?: number | null
  current_concurrency?: number | null
  current_fp_slot?: number | null
} | null>(null)

// Auto-computed fp-slot hint based on the current concurrency input. Mirrors
// the auto_set_fp_slot_limit trigger: GREATEST(1, concurrency_limit / 4).
function autoFpSlot(concurrency: number | null | undefined): number {
  if (concurrency == null || concurrency <= 0) return DEFAULT_FP_SLOT
  return Math.max(1, Math.floor(concurrency / 4))
}
const addCredFpSlotHint = computed(() => autoFpSlot(addCredConcurrency.value))

// When the concurrency input changes, keep fp-slot in sync unless the user
// has explicitly edited it. Watch deep to also catch clears-to-empty.
watch(addCredConcurrency, () => {
  if (!addCredFpSlotTouched.value) {
    addCredFpSlot.value = addCredFpSlotHint.value
  }
})

// Edit-side: same auto-suggest behavior for the credentials drawer. The
// `selected` object is a deep-cloned ProviderCredential bound to many
// fields, so we drive the auto-update with a per-credential touched flag
// that resets whenever the drawer opens a different credential. While
// untouched, changing concurrency also re-derives fp_slot_limit (even from
// null/unlimited) so the form always shows a sensible value. The user can
// override the field manually; once they do, the watcher stops syncing.
const selectedFpSlotTouched = ref(false)
const selectedFpSlotHint = computed(() =>
  selected.value ? autoFpSlot(selected.value.concurrency_limit) : DEFAULT_FP_SLOT,
)
watch(
  () => selected.value?.concurrency_limit,
  () => {
    if (selected.value && !selectedFpSlotTouched.value) {
      selected.value.fp_slot_limit = selectedFpSlotHint.value
    }
  },
)

const fpSlotStats = ref<FpSlotStats | null>(null)
const fpSlotStatsLoading = ref(false)

const statuses = computed<Array<{ value: CredentialStatus; label: string }>>(() => [
  { value: 'active', label: pd('creds.statuses.active') },
  { value: 'cooling', label: pd('creds.statuses.cooling') },
  { value: 'degraded', label: pd('creds.statuses.degraded') },
  { value: 'quarantine', label: pd('creds.statuses.quarantine') },
  { value: 'quota_expired', label: pd('creds.statuses.quotaExpired') },
  { value: 'disabled', label: pd('creds.statuses.disabled') },
])

const lifecycleStatuses = computed(() => CREDENTIAL_LIFECYCLE_STATUSES.map((value) => ({
  value,
  label: pd(`creds.lifecycleLabel.${value}`),
})))

// v735: route-side plan type. Empty string means "no plan" (DB NULL).
// The empty option is rendered first so users see "unset" as the
// default state for credentials that have never been assigned a plan.
// Order roughly tracks "most common → least common" so the dropdown
// is scannable for routine operations on /providers/14.
const planTypes = computed(() => [
  { value: '', label: pd('creds.planTypeUnset') },
  { value: 'token', label: pd('creds.planTypes.token') },
  { value: 'token_plan', label: pd('creds.planTypes.tokenPlan') },
  { value: 'code_plan', label: pd('creds.planTypes.codePlan') },
  { value: 'agent_plan', label: pd('creds.planTypes.agentPlan') },
  { value: 'request', label: pd('creds.planTypes.request') },
  { value: 'seat', label: pd('creds.planTypes.seat') },
  { value: 'compute_time', label: pd('creds.planTypes.computeTime') },
  { value: 'flat_quota', label: pd('creds.planTypes.flatQuota') },
  { value: 'free', label: pd('creds.planTypes.free') },
])

function statusBadge(s: string, manualDisabled?: boolean) {
  if (manualDisabled) return 'badge-red'
  if (s === 'active') return 'badge-green'
  if (s === 'disabled' || s === 'quota_expired' || s === 'quarantine') return 'badge-red'
  if (s === 'cooling' || s === 'degraded') return 'badge-amber'
  return 'badge-gray'
}

function statusLabel(s: string, manualDisabled?: boolean) {
  if (manualDisabled) return pd('creds.manualDisabledSuffix')
  return s
}

function healthBadge(s?: string | null) {
  if (s === 'healthy') return 'badge-green'
  if (s === 'warning') return 'badge-amber'
  if (s === 'unreachable') return 'badge-red'
  return 'badge-gray'
}

function healthLabel(s?: string | null) {
  if (s === 'healthy') return pd('creds.health.healthy')
  if (s === 'warning') return pd('creds.health.warning')
  if (s === 'unreachable') return pd('creds.health.unreachable')
  if (s === 'error') return pd('creds.health.error')
  return pd('creds.health.untested')
}

function probeResultMsg(r: { health_status?: string | null; probe_ok?: boolean; health_source?: string | null }) {
  const status = healthLabel(r.health_status)
  const detail = r.health_source === 'models'
    ? pd('creds.health.modelsOk')
    : r.probe_ok
      ? pd('creds.health.probeOk')
      : pd('creds.health.unavailable')
  return `${status} · ${detail}`
}

function timeText(v?: string | null) {
  if (!v) return '—'
  return fmtDateTime(v)
}

function money(v: number | string | null | undefined) {
  if (v == null) return '—'
  const n = typeof v === 'string' ? Number(v) : v
  return Number.isNaN(n) ? '—' : `$${n.toFixed(4)}`
}

function asDateInput(v: string | null | undefined) {
  return v ? v.slice(0, 16) : ''
}

function sourceLabel(s?: string | null) {
  if (!s) return '—'
  if (s === 'manual') return pd('creds.source.manual')
  if (s === 'auto:request_log') return pd('creds.source.autoRequestLog')
  if (s === 'auto:domestic_random') return pd('creds.source.autoDomestic')
  if (s === 'auto:domestic_featured') return pd('creds.source.autoFeatured')
  if (s === 'auto:refresh_latest') return pd('creds.source.autoRefreshLatest')
  if (s === 'cleared') return pd('creds.source.cleared')
  return s
}

function tagsText(c: ProviderCredential) {
  const t = c.tags ?? []
  return t.length ? t.join(', ') : '—'
}

function openDrawer(c: ProviderCredential) {
  selected.value = JSON.parse(JSON.stringify(c)) as ProviderCredential
  // Reset the fp-slot override flag so the auto-suggest kicks in fresh
  // for this credential. Subsequent edits to fp_slot_limit by the user
  // will flip this back to true and stop the watcher from overwriting.
  selectedFpSlotTouched.value = false
  saveMsg.value = ''
  saveMsgKind.value = ''
  saveMsgRejectCtx.value = null
  checkMsg.value = ''
  // 2026-08-31 hzx-2 round-5: kick off cmb load for the default-probe-model
  // <select>. Best-effort: if the load fails we fall back to free-text
  // input via the computed probeModelHasOptions branch.
  loadDrawerModels()
}

// 2026-09-02: reveal / rotate state for the credential API Key. Lives
// alongside the other drawer-bound refs so a close + reopen starts clean.
// `revealedApiKey` is intentionally component-local (not on `selected`):
// we never want it persisted in the cloned ProviderCredential, since the
// copy is serialized for offline edits in some debug paths.
const revealedApiKey = ref<string | null>(null)
const revealing = ref(false)
const rotateModalOpen = ref(false)
const rotateRawModelName = ref('')
const rotateNewApiKey = ref('')
const rotateNewApiKeyConfirm = ref('')
const rotateSubmitting = ref(false)
const rotateErr = ref('')

async function revealApiKey() {
  const c = selected.value
  if (!c) return
  revealing.value = true
  try {
    const r = await revealCredentialKey(props.provider.id, c.id)
    revealedApiKey.value = r.api_key
  } catch (e: unknown) {
    ElMessage.error(e instanceof Error ? e.message : pd('creds.apiKeyRotateFailed'))
  } finally {
    revealing.value = false
  }
}

function hideApiKey() {
  revealedApiKey.value = null
}

async function copyRevealed() {
  if (!revealedApiKey.value) return
  try {
    await navigator.clipboard.writeText(revealedApiKey.value)
    ElMessage.success(td('common.copied' as never, '已复制' as never))
  } catch (e: unknown) {
    // navigator.clipboard can refuse under insecure contexts; surface a
    // friendly hint rather than letting the exception propagate.
    ElMessage.warning(e instanceof Error ? e.message : pd('creds.apiKeyRotateFailed'))
  }
}

function openRotateModal() {
  if (!selected.value) return
  if (!probeModelOptions.value.length) {
    ElMessage.warning(pd('creds.apiKeyRotateHintNoBinding'))
    return
  }
  rotateErr.value = ''
  rotateNewApiKey.value = ''
  rotateNewApiKeyConfirm.value = ''
  // Default the model to the credential's currently pinned default probe
  // model if it's bound, otherwise the first option.
  const pinned = (selected.value.default_probe_model ?? '').trim()
  const found = probeModelOptions.value.find(o => o.value === pinned)
  rotateRawModelName.value = found ? found.value : probeModelOptions.value[0].value
  rotateModalOpen.value = true
}

async function submitRotate() {
  if (!selected.value) return
  const k1 = rotateNewApiKey.value
  const k2 = rotateNewApiKeyConfirm.value
  if (!k1.trim()) {
    rotateErr.value = pd('creds.apiKeyRotateMissing')
    return
  }
  if (k1 !== k2) {
    rotateErr.value = pd('creds.apiKeyRotateMismatch')
    return
  }
  if (!rotateRawModelName.value) {
    rotateErr.value = pd('creds.apiKeyRotateModelLabel')
    return
  }
  rotateSubmitting.value = true
  rotateErr.value = ''
  try {
    await rotateCredentialPrimaryKey(props.provider.id, selected.value.id, {
      api_key: k1,
      raw_model_name: rotateRawModelName.value,
    })
    rotateModalOpen.value = false
    ElMessage.success(pd('creds.apiKeyRotateSuccess'))
    // Drop any cached plaintext before closing — the new key replaces the
    // old one and the drawer will re-fetch key_masked on next open.
    revealedApiKey.value = null
    emit('refresh')
    closeDrawer()
  } catch (e: unknown) {
    rotateErr.value = e instanceof Error ? e.message : pd('creds.apiKeyRotateFailed')
  } finally {
    rotateSubmitting.value = false
  }
}

// When the operator switches credentials inside the drawer, drop the
// previously-revealed plaintext — otherwise A's key would be readable
// through B's drawer. The reveal action is also disabled while `selected`
// is null (button gating).
watch(() => selected.value?.id, () => {
  revealedApiKey.value = null
  rotateModalOpen.value = false
  rotateErr.value = ''
})

function closeDrawer() {
  drawerTab.value = 'info'
  selected.value = null
  saveMsg.value = ''
  saveMsgKind.value = ''
  saveMsgRejectCtx.value = null
  checkMsg.value = ''
  drawerModels.value = []
  revealedApiKey.value = null
  rotateModalOpen.value = false
  rotateErr.value = ''
}

function openAddCred() {
  addCredKey.value = ''
  addCredLabel.value = ''
  addCredConcurrency.value = DEFAULT_CONCURRENCY
  addCredFpSlot.value = autoFpSlot(DEFAULT_CONCURRENCY)
  addCredFpSlotTouched.value = false
  addCredErr.value = ''
  addCredErrKind.value = ''
  addCredRejectCtx.value = null
  showAddCred.value = true
}

async function submitAddCred() {
  if (!addCredKey.value) { addCredErr.value = pd('creds.addCredApiKeyMissing'); return }
  // Client-side pre-check for credentials_fp_slot_vs_concurrency so we
  // surface a friendly 400 before round-tripping to the server. Empty
  // fields are passed as null so the server trigger fills the default.
  if (addCredConcurrency.value != null && addCredFpSlot.value != null
      && addCredFpSlot.value > addCredConcurrency.value) {
    addCredErr.value = pd('creds.fpSlotExceedsConcurrencyAdd', { slot: addCredFpSlot.value, concurrency: addCredConcurrency.value })
    return
  }
  addCredSaving.value = true
  addCredErr.value = ''
  try {
    await addCredential(props.provider.id, {
      api_key: addCredKey.value,
      label: addCredLabel.value || undefined,
      concurrency_limit: addCredConcurrency.value ?? null,
      fp_slot_limit: addCredFpSlot.value ?? null,
    })
    showAddCred.value = false
    emit('refresh')
  } catch (e: unknown) {
    const formatted = formatCredentialError(e, pd('creds.credsAddFailedFallback'))
    addCredErr.value = formatted.message
    addCredErrKind.value = formatted.kind
    addCredRejectCtx.value = formatted.context
  } finally {
    addCredSaving.value = false
  }
}

async function saveSelected() {
  const c = selected.value
  if (!c) return
  // Client-side pre-check for credentials_fp_slot_vs_concurrency so we
  // surface a friendly 400 before round-tripping to the server.
  if (c.concurrency_limit != null && c.fp_slot_limit != null
      && (c.fp_slot_limit as number) > (c.concurrency_limit as number)) {
    saveMsg.value = pd('creds.fpSlotExceedsConcurrencyEdit', { slot: c.fp_slot_limit, concurrency: c.concurrency_limit })
    return
  }
  saving.value = true
  saveMsg.value = ''
  try {
    await updateCredential(props.provider.id, c.id, {
      label: c.label,
      status: c.status,
      concurrency_limit: c.concurrency_limit || null,
      fp_slot_limit: c.fp_slot_limit,
      effective_at: c.effective_at,
      expires_at: c.expires_at,
      tags: c.tags,
      notes: c.notes || '',
    })
    emit('refresh')
    closeDrawer()
  } catch (e: unknown) {
    const formatted = formatCredentialError(e, pd('creds.saveFailed'))
    saveMsg.value = formatted.message
    saveMsgKind.value = formatted.kind
    saveMsgRejectCtx.value = formatted.context
  } finally {
    saving.value = false
  }
}

// formatCredentialError turns a thrown error from the credential API
// into a user-friendly Chinese message plus a kind tag the UI can read
// to apply a targeted style. When the server returns the structured
// envelope (code = "fp_slot_exceeds_concurrency") we render the same
// wording the client-side pre-check uses, so users see consistent copy
// regardless of which side caught the violation.
//
// Returns { message, kind, context }. kind is:
//   ''                     — generic error
//   'fp_slot_exceeds_concurrency' — constraint violation
// context is the structured payload from the server (or null when absent)
// so the caller can render a "恢复到建议值" button.
function formatCredentialError(e: unknown, fallback: string): {
  message: string
  kind: string
  context: {
    attempted_concurrency?: number | null
    attempted_fp_slot?: number | null
    current_concurrency?: number | null
    current_fp_slot?: number | null
  } | null
} {
  const ec = (e as { code?: string; context?: unknown; message?: string })?.code
  if (ec === 'fp_slot_exceeds_concurrency') {
    const ctx = (e as { context?: unknown })?.context
    const ctxObj = (ctx && typeof ctx === 'object') ? ctx as Record<string, unknown> : null
    const ac = ctxObj?.attempted_concurrency as number | undefined | null
    const af = ctxObj?.attempted_fp_slot as number | undefined | null
    if (ac != null && af != null) {
      return {
        message: pd('creds.fpSlotExceedsConcurrencyEdit', { slot: af, concurrency: ac }),
        kind: 'fp_slot_exceeds_concurrency',
        context: ctxObj,
      }
    }
    const fallbackMsg = (e as { message?: string })?.message
    return { message: fallbackMsg ?? '', kind: 'fp_slot_exceeds_concurrency', context: ctxObj }
  }
  return {
    message: e instanceof Error ? e.message : fallback,
    kind: '',
    context: null,
  }
}

// Reset the edit-side fp_slot_limit to the auto-suggested value and
// re-enable the watcher. Bound to the "恢复建议值" affordance shown when
// the user has manually overridden the field.
function resetSelectedFpSlot() {
  if (!selected.value) return
  selected.value.fp_slot_limit = selectedFpSlotHint.value
  selectedFpSlotTouched.value = false
}

// recoverFromRejection is the "一键恢复到建议值" handler. The server's
// structured 400 payload carries the *current* row values, which is the
// best signal of "what would actually pass" — auto-computed from the
// row's own concurrency_limit. We restore those into the form, drop
// the touched flags so the watcher re-engages, and clear the rejection
// banner so the user can re-submit. Falls back to the local autoFpSlot
// helper if the server context is missing or invalid.
function recoverFromRejection(side: 'edit' | 'add') {
  const ctx = side === 'edit' ? saveMsgRejectCtx.value : addCredRejectCtx.value
  // Prefer the server's current_* values; they reflect the row state at
  // rejection time, which is more reliable than recomputing from a stale
  // concurrency_limit input the user may have edited since.
  const serverConcurrency = ctx?.current_concurrency ?? null
  const serverFpSlot = ctx?.current_fp_slot ?? null

  if (side === 'add') {
    if (serverConcurrency != null) addCredConcurrency.value = serverConcurrency
    if (serverFpSlot != null) {
      addCredFpSlot.value = serverFpSlot
    } else {
      // The row had no fp_slot (unlimited). Reset to the auto value for
      // the (possibly newly-set) concurrency so the user gets a usable
      // starting point.
      addCredFpSlot.value = autoFpSlot(addCredConcurrency.value)
    }
    addCredFpSlotTouched.value = false
    addCredErr.value = ''
    addCredErrKind.value = ''
    addCredRejectCtx.value = null
    return
  }

  // side === 'edit'
  if (!selected.value) return
  if (serverConcurrency != null) selected.value.concurrency_limit = serverConcurrency
  if (serverFpSlot != null) {
    selected.value.fp_slot_limit = serverFpSlot
  } else {
    selected.value.fp_slot_limit = selectedFpSlotHint.value
  }
  // Re-enable the auto-sync watcher so subsequent concurrency edits
  // re-derive fp_slot; the user can still flip it back via the inline
  // "恢复建议值" affordance next to the input.
  selectedFpSlotTouched.value = false
  saveMsg.value = ''
  saveMsgKind.value = ''
  saveMsgRejectCtx.value = null
}

// 2026-07-03: emit `silentRefresh` (not `refresh`) so the drawer stays
// mounted after a successful probe. The previous behaviour re-fetched
// provider+creds with `loading=true`, which unmounted CredsTab and
// closed the drawer mid-edit — operators then lost their pending
// label/status/plan_type edits. We keep the local selected.health_*
// state fresh (already updated above) and let the parent quietly sync
// the creds list badge.
async function checkSelected() {
  const c = selected.value
  if (!c) return
  checking.value = true
  checkMsg.value = ''
  try {
    const r = await checkCredential(props.provider.id, c.id)
    if (r.health_status) {
      c.health_status = r.health_status as ProviderCredential['health_status']
      c.health_checked_at = new Date().toISOString()
      if (r.health_error != null) c.health_error = r.health_error
      if (r.health_probe_model != null) c.health_probe_model = r.health_probe_model
    }
    // 2026-09-02: surface the typed upstream-error kind so the drawer can
    // render a tailored hint (e.g. upstreamNonJsonHint) below health_error
    // instead of leaking the raw "<html>…body_bytes=1726" payload.
    if (r.models_error_kind !== undefined) {
      c.health_error_kind = r.models_error_kind ?? null
    }
    checkMsg.value = probeResultMsg(r)
    emit('silentRefresh')
  } catch (e: unknown) {
    checkMsg.value = e instanceof Error ? e.message : pd('creds.checkFailed')
  } finally {
    checking.value = false
  }
}

async function delSelected() {
  const c = selected.value
  if (!c || !confirm(pd('creds.deleteConfirm'))) return
  try {
    await deleteCredential(props.provider.id, c.id)
    closeDrawer()
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : pd('creds.deleteFailed'))
  }
}

async function toggleManualDisabled() {
  const c = selected.value
  if (!c) return
  const next = !c.manual_disabled
  // 2026-06-23: 旧实现用 prompt() 接受空 reason，
  // 导致 model_offer_events.reason_detail="admin: " 没有任何业务上下文，
  // 后续溯源 miniamx-prod-1 误停用原因非常困难。
  // 现在 loop 弹窗，空白 / 仅空白字符视为取消，且禁用按钮在无输入时无法点击。
  while (true) {
    const reason = window.prompt(
      next ? pd('creds.reasonPromptDisable') : pd('creds.reasonPromptEnable'),
      ''
    )
    if (reason === null) return
    if (reason.trim() !== '') {
      try {
        await setCredentialManualDisabled(props.provider.id, c.id, next, reason.trim())
        c.manual_disabled = next
        emit('silentRefresh')
      } catch (e: unknown) {
        alert(e instanceof Error ? e.message : pd('creds.setFailed'))
      }
      return
    }
    alert(pd('creds.reasonEmpty'))
  }
}

async function setLifecycle(value: CredentialLifecycleStatus) {
  const c = selected.value
  if (!c) return
  try {
    await updateCredentialLifecycle(props.provider.id, c.id, value)
    c.lifecycle_status = value
    emit('silentRefresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : pd('creds.lifecycleFailed'))
  }
}

function handleLifecycleChange(event: Event) {
  const value = (event.target as HTMLSelectElement).value
  if (!CREDENTIAL_LIFECYCLE_STATUSES.some((status) => status === value)) return
  void setLifecycle(value as CredentialLifecycleStatus)
}

// v735: route-side plan type — fires immediately on select change so
// the cmb re-derive + candCache invalidation kick in without waiting
// for the drawer's saveSelected flow. Empty string maps to NULL on
// the server side, mirroring how the backend handles clear.
//
// 2026-07-03: emit `silentRefresh` (not `refresh`) so the parent does
// NOT flip `loading=true` and unmount this component — the user is
// still mid-edit in the drawer and would otherwise lose their changes.
async function setPlanType(value: string) {
  const c = selected.value
  if (!c) return
  const newVal = value === '' ? null : value
  if ((c.plan_type ?? null) === newVal) return // no-op
  try {
    await updateCredential(props.provider.id, c.id, { plan_type: newVal })
    c.plan_type = newVal
    emit('silentRefresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : pd('creds.planTypeFailed'))
  }
}

async function resetAvailability() {
  const c = selected.value
  if (!c || !confirm(pd('creds.resetAvailConfirm', { name: c.label }))) return
  try {
    await resetCredentialAvailability(props.provider.id, c.id)
    emit('silentRefresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : pd('creds.resetFailed'))
  }
}

async function resetQuota() {
  const c = selected.value
  if (!c || !confirm(pd('creds.resetQuotaConfirm', { name: c.label }))) return
  try {
    await resetCredentialQuota(props.provider.id, c.id)
    emit('silentRefresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : pd('creds.resetFailed'))
  }
}

async function forceRecover() {
  const c = selected.value
  if (!c || !confirm(pd('creds.forceRecoverConfirm', { name: c.label }))) return
  try {
    await forceRecoverCredential(c.id)
    emit('silentRefresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : pd('creds.triggerFailed'))
  }
}

async function setDefaultModel() {
  const c = selected.value
  if (!c) return
  const v = (selectedDefaultModel.value ?? '').trim()
  if (v === (c.default_probe_model ?? '').trim()) {
    // No-op: operator clicked "保存" without changing the value. The
    // PATCH would still write the same row, but skipping avoids the
    // silentRefresh reload + audit log churn. Also covers "clear an
    // already-empty value" — nothing to clear.
    return
  }
  if (v === '') {
    // Empty input → treat as a clear request (server semantics: model=null).
    await clearDefaultModel()
    return
  }
  try {
    await setDefaultProbeModel(props.provider.id, c.id, v, 'admin UI set')
    // Optimistic local update so the chip below the input flips
    // immediately; the silentRefresh will reconcile any drift.
    c.default_probe_model = v
    c.default_probe_model_source = 'manual'
    c.default_probe_model_picked_at = new Date().toISOString()
    emit('silentRefresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : pd('creds.setFailed'))
  }
}

async function clearDefaultModel() {
  const c = selected.value
  if (!c) return
  try {
    await setDefaultProbeModel(props.provider.id, c.id, null, 'admin UI clear')
    c.default_probe_model = null
    c.default_probe_model_source = 'cleared'
    c.default_probe_model_picked_at = new Date().toISOString()
    selectedDefaultModel.value = ''
    emit('silentRefresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : pd('creds.setFailed'))
  }
}

async function repickDefault() {
  const c = selected.value
  if (!c) return
  try {
    const r = await pickDefaultProbeModel(props.provider.id, c.id)
    if (!r.model) {
      alert(pd('creds.defaultProbeModelPickNone'))
    } else {
      // Server already wrote the new value; reflect it locally so the
      // chip + <select> stay in sync without a hard refresh.
      c.default_probe_model = r.model
      c.default_probe_model_source = (r.source as ProviderCredential['default_probe_model_source']) ?? null
      c.default_probe_model_picked_at = new Date().toISOString()
      selectedDefaultModel.value = r.model
      alert(pd('creds.defaultProbeModelPicked', { model: r.model, source: r.source }))
    }
    emit('silentRefresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : pd('creds.repickFailed'))
  }
}

async function resetFpSlots() {
  const c = selected.value
  if (!c || !confirm(pd('creds.resetFpSlotsConfirm', { name: c.label }))) return
  try {
    const r = await resetCredentialFpSlots(props.provider.id, c.id)
    alert(pd('creds.resetFpSlotsOk', { slots: r.deleted_slots, pins: r.deleted_pins }))
    fpSlotStats.value = null
    emit('silentRefresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : pd('creds.resetFpSlotsFailed'))
  }
}

async function releaseFpSlot(slotIndex: number) {
  const c = selected.value
  if (!c) return
  try {
    const r = await releaseCredentialFpSlot(props.provider.id, c.id, slotIndex)
    if (r.released) {
      // Reload the stats so the UI reflects the freed slot
      fpSlotStats.value = await getCredentialFpSlotStats(props.provider.id, c.id)
    }
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : pd('creds.releaseFailed'))
  }
}

async function loadFpSlotStats() {
  const c = selected.value
  if (!c) return
  fpSlotStatsLoading.value = true
  try {
    fpSlotStats.value = await getCredentialFpSlotStats(props.provider.id, c.id)
  } catch (e: unknown) {
    fpSlotStats.value = null
    alert(e instanceof Error ? e.message : pd('creds.fpSlotStatsFailed'))
  } finally {
    fpSlotStatsLoading.value = false
  }
}

function fmtTtl(seconds: number): string {
  if (seconds <= 0) return pd('creds.expired')
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (hours >= 1) return `${hours}h${minutes}m`
  return `${minutes}m`
}

function holderShort(h: string): string {
  if (!h) return ''
  return h.length > 12 ? `…${h.slice(-8)}` : h
}

function onEffectiveInput(ev: Event) {
  const c = selected.value
  if (!c) return
  const v = (ev.target as HTMLInputElement).value
  c.effective_at = v ? new Date(v).toISOString() : null
}

function onExpiresInput(ev: Event) {
  const c = selected.value
  if (!c) return
  const v = (ev.target as HTMLInputElement).value
  c.expires_at = v ? new Date(v).toISOString() : null
}

function onTagsInput(ev: Event) {
  const c = selected.value
  if (!c) return
  c.tags = (ev.target as HTMLInputElement).value
    .split(',')
    .map(t => t.trim())
    .filter(Boolean)
}
</script>

<template>
  <div>
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
      <h3 style="margin:0">{{ pd('creds.listTitle') }}</h3>
      <button v-if="canManageCreds" class="btn btn-primary btn-sm" @click="openAddCred">{{ pd('creds.addBtn') }}</button>
    </div>

    <div class="card" style="overflow-x:auto">
      <table class="data-table cred-table">
        <thead>
          <tr>
            <th>{{ pd('creds.table.cred') }}</th>
            <th>{{ pd('creds.table.status') }}</th>
            <th>{{ pd('creds.table.probe') }}</th>
            <th>{{ pd('creds.table.defaultProbeModel') }}</th>
            <th>{{ pd('creds.table.concurrency') }}</th>
            <th>{{ pd('creds.table.usage') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!creds.length"><td colspan="6">{{ pd('creds.empty') }}</td></tr>
          <tr
            v-for="c in creds"
            :key="c.id"
            class="cred-row"
            :class="{ 'cred-row--disabled': c.manual_disabled }"
            tabindex="0"
            @click="openDrawer(c)"
            @keydown.enter="openDrawer(c)"
          >
            <td>
              <div class="cred-label">{{ c.label || pd('creds.labelFallback', { id: c.id }) }}</div>
              <div class="key-fingerprint" :title="pd('creds.fingerprintTitle')">
                {{ c.key_masked ?? (c.key_mask_error ? pd('creds.fingerprintUnparsed') : '—') }}
              </div>
              <div class="cred-meta">{{ pd('creds.rowMeta', { id: c.id, trust: c.trust_level }) }}</div>
            </td>
            <td>
              <span class="badge" :class="statusBadge(c.status, c.manual_disabled)">{{ statusLabel(c.status, c.manual_disabled) }}</span>
              <div class="cell-sub">{{ c.lifecycle_status }}</div>
            </td>
            <td>
              <span class="badge" :class="healthBadge(c.health_status)">{{ healthLabel(c.health_status) }}</span>
              <div class="cell-sub">{{ timeText(c.health_checked_at) }}</div>
              <button type="button" class="btn btn-ghost btn-sm" @click.stop="emit('openErrorDetail', c.id)">{{ pd('creds.viewErrorDetail') }}</button>
            </td>
            <td>
              <code v-if="c.default_probe_model" class="mono-sm">{{ c.default_probe_model }}</code>
              <span v-else class="cell-muted">{{ pd('creds.cellUnset') }}</span>
            </td>
            <td>
              {{ c.concurrency_limit || pd('creds.noLimit') }}
              <div v-if="c.fp_slot_limit != null" class="cell-sub">
                {{ pd('creds.slotUsage', { used: c.fp_slots_used ?? 0, limit: c.fp_slot_limit }) }}
              </div>
            </td>
            <td>
              <div>{{ c.total_requests }} {{ pd('creds.requestCountSuffix') }}</div>
              <div class="cell-sub">{{ money(c.total_cost_usd) }}</div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Credential detail drawer -->
    <div v-if="selected" class="drawer-backdrop" @click="closeDrawer">
      <div class="drawer-panel card drawer-panel-wide" @click.stop>
        <div class="drawer-header">
          <div>
            <h3 style="margin:0">{{ selected.label || pd('creds.drawerTitle', { id: selected.id }) }}</h3>
            <div class="drawer-sub">{{ pd('creds.rowMeta', { id: selected.id, trust: selected.trust_level }) }}</div>
            <div class="drawer-tabs" style="margin-top:10px;display:flex;gap:6px">
              <button type="button" class="btn btn-sm" :class="drawerTab === 'info' ? 'btn-primary' : 'btn-ghost'" @click="drawerTab = 'info'">信息</button>
              <!-- 模型 tab 为绑定管理（写操作），仅 super_admin -->
              <button v-if="canManageCreds" type="button" class="btn btn-sm" :class="drawerTab === 'models' ? 'btn-primary' : 'btn-ghost'" @click="drawerTab = 'models'">模型</button>
            </div>
          </div>
          <button type="button" class="btn btn-ghost btn-sm" @click="closeDrawer">{{ pd('creds.drawerClose') }}</button>
        </div>

        <div v-if="drawerTab === 'models' && canManageCreds" class="drawer-body">
          <CredentialModelsPanel :provider-id="provider.id" :credential-id="selected.id" />
        </div>

        <div v-else class="drawer-body">
          <div class="drawer-section">
            <div class="drawer-section-title">{{ pd('creds.drawerSectionBasic') }}</div>
            <label class="field-label">{{ pd('creds.drawerFieldLabel') }}</label>
            <input v-model="selected.label" class="field-input" :disabled="!canManageCreds" />
            <label class="field-label" style="margin-top:6px">{{ pd('creds.drawerFieldApiKey') || 'API Key' }}</label>
            <div class="drawer-key-wrap">
              <div class="key-fingerprint drawer-key">
                {{ revealedApiKey ?? selected.key_masked ?? '—' }}
              </div>
              <div class="btn-row" style="margin-top:6px">
                <template v-if="revealedApiKey">
                  <button class="btn btn-sm" type="button" @click="copyRevealed">{{ pd('creds.apiKeyCopy') }}</button>
                  <button class="btn btn-sm btn-ghost" type="button" @click="hideApiKey">{{ pd('creds.apiKeyHide') }}</button>
                </template>
                <template v-else>
                  <!-- 显示完整 API Key 仅 super_admin；「修改」（轮换）对
                       default 租户 tenant_admin 同样开放（2026-09-04）。 -->
                  <button
                    v-if="canManageCreds"
                    class="btn btn-sm"
                    type="button"
                    :disabled="revealing"
                    @click="revealApiKey"
                  >
                    {{ revealing ? pd('creds.apiKeyRevealing') : pd('creds.apiKeyReveal') }}
                  </button>
                  <button
                    class="btn btn-sm btn-ghost"
                    type="button"
                    :disabled="!probeModelOptions.length"
                    @click="openRotateModal"
                    :title="!probeModelOptions.length ? pd('creds.apiKeyRotateHintNoBinding') : ''"
                  >
                    {{ pd('creds.apiKeyRotateBtn') }}
                  </button>
                </template>
              </div>
              <div v-if="revealedApiKey" class="cell-sub cell-sub--warn" style="margin-top:4px">
                {{ pd('creds.apiKeyWarningReveal') }}
              </div>
              <div v-else-if="!probeModelOptions.length" class="cell-sub" style="margin-top:4px">
                {{ pd('creds.apiKeyRotateHintNoBinding') }}
              </div>
            </div>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">{{ pd('creds.drawerSectionStatus') }}</div>
            <div class="field-grid">
              <div>
                <label class="field-label">{{ pd('creds.drawerFieldStatus') }}</label>
                <select v-model="selected.status" class="field-input" :disabled="!canManageCreds">
                  <option v-for="s in statuses" :key="s.value" :value="s.value">{{ s.label }}</option>
                </select>
              </div>
              <div>
                <label class="field-label">{{ pd('creds.drawerFieldLifecycle') }}</label>
                <select
                  :value="selected.lifecycle_status"
                  class="field-input"
                  :disabled="!canManageCreds"
                  @change="handleLifecycleChange"
                >
                  <option v-for="s in lifecycleStatuses" :key="s.value" :value="s.value">{{ s.label }}</option>
                </select>
              </div>
            </div>
            <div style="margin-top:8px">
              <label class="field-label">{{ pd('creds.drawerFieldPlanType') }}</label>
              <select
                :value="selected.plan_type ?? ''"
                class="field-input"
                :disabled="!canManageCreds"
                @change="(e: Event) => setPlanType((e.target as HTMLSelectElement).value)"
              >
                <option v-for="p in planTypes" :key="p.value" :value="p.value">{{ p.label }}</option>
              </select>
              <div class="cell-sub">{{ pd('creds.planTypeHint') }}</div>
            </div>
            <label class="manual-toggle">
              <input type="checkbox" :checked="!!selected.manual_disabled" :disabled="!canManageCreds" @change="toggleManualDisabled" />
              <span>手工{{ selected.manual_disabled ? pd('creds.manualDisabledSuffix') : pd('creds.manualEnabledSuffix') }} 🔒</span>
            </label>
            <div v-if="selected.state_reason_code" class="cell-sub" :title="selected.state_reason_detail || ''">
              {{ selected.state_reason_code }}
            </div>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">{{ pd('creds.drawerFieldProbe') }}</div>
            <div class="info-row">
              <span class="badge" :class="healthBadge(selected.health_status)">{{ healthLabel(selected.health_status) }}</span>
              <span class="cell-muted">{{ timeText(selected.health_checked_at) }}</span>
            </div>
            <div v-if="selected.health_probe_model" class="cell-sub">probe: {{ selected.health_probe_model }}</div>
            <div v-if="selected.health_error" class="cell-sub cell-sub--danger">{{ selected.health_error }}</div>
            <!-- 2026-09-02: typed upstream-error hint. When models_error_kind
                 is "non_json_body" we know the upstream /v1/models returned
                 HTML / XML rather than OpenAI-compatible JSON (typical
                 reverse-proxy / gateway error page); show an actionable hint
                 instead of leaving the operator guessing at "parse models
                 response failed". -->
            <div
              v-if="selected.health_error_kind === 'non_json_body'"
              class="cell-sub"
              style="margin-top:4px"
            >
              {{ pd('creds.upstreamNonJsonHint') }}
            </div>
            <div v-if="canManageCreds" class="btn-row">
              <button class="btn btn-sm" :disabled="checking" @click="checkSelected">{{ pd('creds.probeCheckNow') }}</button>
            </div>
            <div v-if="checking" class="probe-status probe-status--loading" role="status" aria-live="polite">
              <span class="probe-spinner" aria-hidden="true"></span>
              {{ pd('creds.probeRunning') }}
            </div>
            <div v-else-if="checkMsg" class="cell-sub">{{ checkMsg }}</div>
          </div>

          <!-- 2026-08-31 hzx-2 round-5: default-probe-model picker.
               Prefer <select> over the cmb list (so the operator can never
               pin a model that's not actually bound to this credential).
               Fall back to a free-text input only when the credential has
               zero routable bindings — that's the "manual override" path
               for vendors whose /v1/models is unreachable on first open.
               Source label stays visible below the input so the operator
               knows whether the current value is a manual pin, an auto
               pick, or unfilled. -->
          <div class="drawer-section">
            <div class="drawer-section-title">{{ pd('creds.drawerSectionDefaultProbeModel') }}</div>
            <div v-if="drawerModelsLoading" class="cell-sub">{{ pd('creds.probeLoadingModels') }}</div>
            <template v-else>
              <select
                v-if="probeModelHasOptions"
                v-model="selectedDefaultModel"
                class="field-input"
                :disabled="!canManageCreds"
                :aria-label="pd('creds.drawerSectionDefaultProbeModel')"
              >
                <option value="">{{ pd('creds.probeModelNoneOption') }}</option>
                <option v-for="opt in probeModelOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
              </select>
              <input
                v-else
                v-model="selectedDefaultModel"
                type="text"
                class="field-input"
                :disabled="!canManageCreds"
                :placeholder="pd('creds.probeModelManualPlaceholder')"
                :aria-label="pd('creds.drawerSectionDefaultProbeModel')"
              />
              <div v-if="!probeModelHasOptions" class="cell-sub">{{ pd('creds.probeModelNoBindingsHint') }}</div>
            </template>
            <code v-if="selected.default_probe_model" class="mono-sm" style="margin-top:6px;display:block">{{ selected.default_probe_model }}</code>
            <span v-else class="cell-muted">{{ pd('creds.probeModelUnset') }}</span>
            <div class="cell-sub">{{ sourceLabel(selected.default_probe_model_source) }}</div>
            <div v-if="canManageCreds" class="btn-row">
              <button class="btn btn-sm btn-primary" @click="setDefaultModel">{{ pd('creds.probeSave') }}</button>
              <button class="btn btn-sm" :disabled="selectedDefaultModel === ''" @click="clearDefaultModel">{{ pd('creds.probeClear') }}</button>
              <button class="btn btn-sm" @click="repickDefault">{{ pd('creds.probeRepick') }}</button>
            </div>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">{{ pd('creds.drawerSectionConcurrency') }}</div>
            <div class="field-grid">
              <div>
                <label class="field-label">{{ pd('creds.drawerConcurrency') }}</label>
                <input v-model.number="selected.concurrency_limit" type="number" min="0" class="field-input" :disabled="!canManageCreds" />
              </div>
              <div>
                <label class="field-label">{{ pd('creds.drawerFpSlot') }}</label>
                <div class="cell-muted" style="margin-bottom:4px">
                  <template v-if="selected.fp_slot_limit != null">
                    {{ selected.fp_slots_used ?? 0 }}/{{ selected.fp_slot_limit }}
                    <span v-if="(selected.fp_slots_free ?? 0) === 0" class="cell-sub--danger">{{ pd('creds.drawerFpSlotFull') }}</span>
                  </template>
                  <template v-else>{{ pd('creds.drawerFpSlotInfinite') }}</template>
                </div>
                <input
                  v-model.number="selected.fp_slot_limit"
                  type="number"
                  min="1"
                  :disabled="!canManageCreds"
                  :max="selected.concurrency_limit && selected.concurrency_limit > 0 ? selected.concurrency_limit : 10000"
                  class="field-input"
                  :placeholder="`${pd('creds.drawerFpSlotSuggestPrefix')}${selectedFpSlotHint}`"
                  :title="pd('creds.drawerFpSlotSuggestTitle', { n: selectedFpSlotHint })"
                  @input="selectedFpSlotTouched = true"
                />
                <div class="form-hint" style="display:flex;justify-content:space-between;align-items:center">
                  <span>
                    <template v-if="selectedFpSlotTouched">{{ pd('creds.drawerFpSlotHintTouched') }}</template>
                    <template v-else>{{ pd('creds.drawerFpSlotHintAuto', { n: selectedFpSlotHint }) }}</template>
                  </span>
                  <button
                    v-if="selectedFpSlotTouched && selected.fp_slot_limit !== selectedFpSlotHint"
                    class="btn btn-sm btn-ghost"
                    type="button"
                    @click="resetSelectedFpSlot"
                    :title="pd('creds.drawerFpSlotResetTitle')"
                  >{{ pd('creds.drawerFpSlotReset') }}</button>
                </div>
                <div v-if="selected.fp_slot_limit != null && canManageCreds" class="btn-row" style="margin-top:4px">
                  <button class="btn btn-sm btn-warning-outline" @click="resetFpSlots" :title="pd('creds.drawerFpSlotResetTitle')">
                    {{ pd('creds.drawerFpSlotResetBtn') }}
                  </button>
                  <button class="btn btn-sm" @click="loadFpSlotStats" :disabled="fpSlotStatsLoading" :title="pd('creds.drawerFpSlotDetailsTitle')">
                    {{ fpSlotStatsLoading ? pd('creds.drawerFpSlotDetailsLoading') : pd('creds.drawerFpSlotDetails') }}
                  </button>
                </div>
                <div v-if="saveMsg && saveMsgKind === 'fp_slot_exceeds_concurrency'" class="cell-sub cell-sub--danger" style="display:flex;justify-content:space-between;align-items:center;gap:8px">
                  <span>{{ saveMsg }}</span>
                  <button
                    class="btn btn-sm btn-warning-outline"
                    type="button"
                    @click="recoverFromRejection('edit')"
                    :title="pd('creds.drawerRecoverSuggestedTitle')"
                  >{{ pd('creds.drawerRecoverSuggestedBtn') }}</button>
                </div>
              </div>
            </div>
            <div class="field-grid" style="margin-top:8px">
              <div>
                <label class="field-label">{{ pd('creds.drawerEffectiveAt') }}</label>
                <input
                  :value="asDateInput(selected.effective_at)"
                  type="datetime-local"
                  class="field-input"
                  :disabled="!canManageCreds"
                  @input="onEffectiveInput"
                />
              </div>
              <div>
                <label class="field-label">{{ pd('creds.drawerExpiresAt') }}</label>
                <input
                  :value="asDateInput(selected.expires_at)"
                  type="datetime-local"
                  class="field-input"
                  :disabled="!canManageCreds"
                  @input="onExpiresInput"
                />
              </div>
            </div>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">{{ pd('creds.drawerSectionUsage') }}</div>
            <div>{{ selected.total_requests }} {{ pd('creds.requestCountSuffix') }} · {{ money(selected.total_cost_usd) }}</div>
            <div class="cell-sub">{{ pd('creds.usageBalancePrefix') }} {{ money(selected.quota_summary?.remaining_usd ?? null) }}</div>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">{{ pd('creds.drawerSectionTags') }}</div>
            <input
              :value="(selected.tags ?? []).join(', ')"
              class="field-input"
              :disabled="!canManageCreds"
              :placeholder="pd('creds.drawerTagsPlaceholder')"
              @input="onTagsInput"
            />
          </div>

          <div v-if="fpSlotStats" class="drawer-section">
            <div class="drawer-section-title">{{ pd('creds.drawerSectionFpSlots') }}</div>
            <div v-if="fpSlotStats.unlimited" class="cell-muted">{{ fpSlotStats.message }}</div>
            <FpSlotVisualizer
              v-else-if="fpSlotStats.slot_limit && fpSlotStats.details"
              :details="fpSlotStats.details"
              :slot-limit="fpSlotStats.slot_limit"
              @release="releaseFpSlot"
            />
            <div v-else-if="fpSlotStats.details" class="cell-muted">{{ pd('creds.drawerFpSlotsEmpty') }}</div>
          </div>

          <div v-if="canManageCreds" class="drawer-section drawer-section--danger">
            <div class="drawer-section-title">{{ pd('creds.drawerSectionDanger') }}</div>
            <div class="btn-row">
              <button class="btn btn-sm" @click="resetAvailability">{{ pd('creds.drawerResetAvail') }}</button>
              <button class="btn btn-sm" @click="resetQuota">{{ pd('creds.drawerResetQuota') }}</button>
              <button class="btn btn-sm" @click="forceRecover">{{ pd('creds.drawerForceRecover') }}</button>
              <button class="btn btn-sm btn-danger-outline" @click="delSelected">{{ pd('creds.drawerDisable') }}</button>
            </div>
          </div>
        </div>

        <div v-if="drawerTab === 'info'" class="drawer-footer">
          <div v-if="saveMsg" class="cell-sub cell-sub--danger">{{ saveMsg }}</div>
          <div class="btn-row btn-row--end">
            <button class="btn btn-ghost" @click="closeDrawer">{{ pd('creds.drawerCancel') }}</button>
            <button v-if="canManageCreds" class="btn btn-primary" :disabled="saving" @click="saveSelected">
              {{ saving ? pd('creds.drawerSaving') : pd('creds.drawerSave') }}
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Add Credential Modal -->
    <div class="modal-overlay" v-if="showAddCred" @click.self="showAddCred = false">
      <div class="modal" style="max-width:400px" @click.stop>
        <h3>{{ pd('creds.addCredTitle', { name: provider?.display_name }) }}</h3>
        <div v-if="addCredErr" class="alert alert-danger" style="display:flex;justify-content:space-between;align-items:center;gap:8px">
          <span>{{ addCredErr }}</span>
          <button
            v-if="addCredErrKind === 'fp_slot_exceeds_concurrency'"
            class="btn btn-sm btn-warning-outline"
            type="button"
            @click="recoverFromRejection('add')"
            :title="pd('creds.drawerRecoverSuggestedTitle')"
          >{{ pd('creds.drawerRecoverSuggestedBtn') }}</button>
        </div>
        <div class="form-group">
          <label>{{ pd('creds.addCredApiKey') }}</label>
          <input v-model="addCredKey" type="password" placeholder="sk-…" autocomplete="off" />
        </div>
        <div class="form-group">
          <label>{{ pd('creds.addCredLabel') }}</label>
          <input v-model="addCredLabel" :placeholder="pd('creds.addCredLabelPlaceholder')" />
        </div>
        <div class="form-group" style="display:grid;grid-template-columns:1fr 1fr;gap:12px">
          <div>
            <label>{{ pd('creds.addCredConcurrency') }}</label>
            <input
              v-model.number="addCredConcurrency"
              type="number"
              min="0"
              :placeholder="pd('creds.addCredConcurrencyPlaceholder')"
            />
            <div class="form-hint">{{ pd('creds.addCredConcurrencyHint') }}</div>
          </div>
          <div>
            <label>{{ pd('creds.addCredFpSlot') }}</label>
            <input
              v-model.number="addCredFpSlot"
              type="number"
              min="1"
              :max="addCredConcurrency && addCredConcurrency > 0 ? addCredConcurrency : 10000"
              :placeholder="`${pd('creds.drawerFpSlotSuggestPrefix')}${addCredFpSlotHint}`"
              @input="addCredFpSlotTouched = true"
            />
            <div class="form-hint">
              {{ pd('creds.addCredFpSlotHint', { n: addCredFpSlotHint }) }}
            </div>
          </div>
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" @click="showAddCred = false">{{ pd('creds.addCredCancel') }}</button>
          <button class="btn btn-primary" @click="submitAddCred" :disabled="addCredSaving">
            {{ addCredSaving ? pd('creds.addCredAdding') : pd('creds.addCredAdd') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Rotate API Key Modal — 2026-09-02.
         Asks the operator for a new key plus a confirmation entry, and a
         bound model for the post-commit verification probe. Submitting
         delegates to rotateCredentialPrimaryKey; success closes both the
         modal and the drawer and emits `refresh` so the list re-fetches
         key_masked against the freshly rotated secret. -->
    <div class="modal-overlay" v-if="rotateModalOpen" @click.self="rotateModalOpen = false">
      <div class="modal" style="max-width:480px" @click.stop>
        <h3>{{ pd('creds.apiKeyRotateTitle') }}</h3>
        <div class="alert alert-warn" style="display:flex;justify-content:space-between;align-items:center;gap:8px">
          <span>{{ pd('creds.apiKeyWarningRotate') }}</span>
        </div>
        <div v-if="rotateErr" class="alert alert-danger">{{ rotateErr }}</div>
        <div class="form-group">
          <label>{{ pd('creds.apiKeyRotateModelLabel') }}</label>
          <select v-model="rotateRawModelName" class="field-input">
            <option v-for="opt in probeModelOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
          </select>
        </div>
        <div class="form-group">
          <label>{{ pd('creds.apiKeyRotateNewKeyLabel') }}</label>
          <input v-model="rotateNewApiKey" type="password" autocomplete="off" />
        </div>
        <div class="form-group">
          <label>{{ pd('creds.apiKeyRotateConfirmLabel') }}</label>
          <input v-model="rotateNewApiKeyConfirm" type="password" autocomplete="off" />
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" type="button" @click="rotateModalOpen = false">{{ pd('creds.drawerCancel') }}</button>
          <button class="btn btn-primary" type="button" @click="submitRotate" :disabled="rotateSubmitting">
            {{ rotateSubmitting ? pd('creds.apiKeyRotating') : pd('creds.apiKeyRotateSubmit') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.cred-table {
  width: 100%;
  font-size: 12px;
}
.cred-row {
  cursor: pointer;
}
.cred-row:hover td {
  background: color-mix(in srgb, var(--accent) 6%, transparent);
}
.cred-row--disabled td {
  background: rgba(220, 38, 38, 0.06);
}
.cred-row:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: -2px;
}
.cred-label {
  font-weight: 500;
}
.cred-meta,
.cell-sub {
  font-size: 11px;
  color: var(--muted);
  margin-top: 2px;
}
.form-hint {
  font-size: 11px;
  color: var(--muted);
  margin-top: 2px;
}
.cell-sub--danger {
  color: var(--danger);
}
.cell-sub--warn {
  color: var(--warning);
}
.drawer-key-wrap {
  margin-top: 4px;
}
.cell-muted {
  font-size: 11px;
  color: var(--muted);
}
.mono-sm {
  font-family: ui-monospace, monospace;
  font-size: 11px;
}
.key-fingerprint {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, monospace;
  font-size: 11px;
  color: var(--text);
  margin: 4px 0;
  word-break: break-all;
}
.drawer-key {
  margin-top: 6px;
  padding: 8px;
  background: var(--bg-subtle);
  border-radius: 4px;
}
.drawer-sub {
  font-size: 12px;
  color: var(--muted);
  margin-top: 4px;
}
.drawer-body {
  flex: 1;
  overflow-y: auto;
}
.drawer-footer {
  margin-top: auto;
  padding-top: 16px;
  border-top: 1px solid var(--border);
}
.field-label {
  display: block;
  font-size: 11px;
  color: var(--muted);
  margin-bottom: 4px;
}
.field-input {
  width: 100%;
  padding: 8px 10px;
  font-size: 13px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  margin-bottom: 8px;
}
.field-input:focus {
  border-color: var(--accent);
  outline: none;
}
.field-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
.manual-toggle {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
  font-size: 13px;
  cursor: pointer;
}
.info-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 6px;
}
.btn-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 10px;
}
.btn-row--end {
  justify-content: flex-end;
}
.btn-danger-outline {
  color: var(--danger);
  border-color: var(--danger);
}
.btn-warning-outline {
  color: var(--warning);
  border-color: var(--warning);
  background: transparent;
}
.btn-warning-outline:hover {
  background: var(--warning-bg);
}
.drawer-section--danger {
  padding-top: 12px;
  border-top: 1px dashed var(--border);
}
.fp-slot-summary {
  display: none; /* Moved to FpSlotVisualizer */
}
.probe-status {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
  font-size: 12px;
}
.probe-status--loading {
  color: var(--accent);
}
.probe-spinner {
  display: inline-block;
  width: 10px;
  height: 10px;
  border: 2px solid color-mix(in srgb, var(--accent) 30%, transparent);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: probe-spin 0.8s linear infinite;
}
@keyframes probe-spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
