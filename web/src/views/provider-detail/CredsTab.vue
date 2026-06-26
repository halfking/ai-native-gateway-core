<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import {
  updateCredential, deleteCredential, checkCredential,
  addCredential,
  setCredentialManualDisabled, setDefaultProbeModel, pickDefaultProbeModel,
  resetCredentialAvailability, resetCredentialQuota, forceRecoverCredential,
  updateCredentialLifecycle, resetCredentialFpSlots,
  releaseCredentialFpSlot,
  getCredentialFpSlotStats, type FpSlotStats,
  type ProviderCredential, type CredentialStatus,
} from '../../api'
import { ApiError } from '../../api/_core'
import FpSlotVisualizer from '../../components/FpSlotVisualizer.vue'

const props = defineProps<{
  provider: any
  creds: ProviderCredential[]
}>()
const emit = defineEmits<{ refresh: [] }>()

const selected = ref<ProviderCredential | null>(null)
const saving = ref(false)
const checking = ref(false)
const saveMsg = ref('')
const checkMsg = ref('')
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

const statuses: Array<{ value: CredentialStatus; label: string }> = [
  { value: 'active', label: '可用' },
  { value: 'cooling', label: '冷却' },
  { value: 'degraded', label: '降级' },
  { value: 'quarantine', label: '隔离' },
  { value: 'quota_expired', label: '配额耗尽' },
  { value: 'disabled', label: '停用' },
]

const lifecycleStatuses = [
  { value: 'active', label: 'active (正常)' },
  { value: 'disabled', label: 'disabled (停用)' },
  { value: 'suspended', label: 'suspended (挂起)' },
  { value: 'retired', label: 'retired (退役)' },
]

function statusBadge(s: string, manualDisabled?: boolean) {
  if (manualDisabled) return 'badge-red'
  if (s === 'active') return 'badge-green'
  if (s === 'disabled' || s === 'quota_expired' || s === 'quarantine') return 'badge-red'
  if (s === 'cooling' || s === 'degraded') return 'badge-amber'
  return 'badge-gray'
}

function statusLabel(s: string, manualDisabled?: boolean) {
  if (manualDisabled) return '停用'
  return s
}

function healthBadge(s?: string | null) {
  if (s === 'healthy') return 'badge-green'
  if (s === 'warning') return 'badge-amber'
  if (s === 'unreachable') return 'badge-red'
  return 'badge-gray'
}

function healthLabel(s?: string | null) {
  if (s === 'healthy') return '正常'
  if (s === 'warning') return '警示'
  if (s === 'unreachable') return '不可达'
  if (s === 'error') return '错误'
  return '未探测'
}

function probeResultMsg(r: { health_status?: string | null; probe_ok?: boolean; health_source?: string | null }) {
  const status = healthLabel(r.health_status)
  const detail = r.health_source === 'models'
    ? '模型接口正常'
    : r.probe_ok
      ? '探活通过'
      : '不可用'
  return `${status} · ${detail}`
}

function timeText(v?: string | null) {
  if (!v) return '—'
  return new Date(v).toLocaleString('zh-CN', { hour12: false })
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
  if (s === 'manual') return '🔒 手工'
  if (s === 'auto:request_log') return '📊 请求日志'
  if (s === 'auto:domestic_random') return '🎲 国内随机'
  if (s === 'cleared') return '— 已清'
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
}

function closeDrawer() {
  selected.value = null
  saveMsg.value = ''
  saveMsgKind.value = ''
  saveMsgRejectCtx.value = null
  checkMsg.value = ''
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
  if (!addCredKey.value) { addCredErr.value = '请输入 API Key'; return }
  // Client-side pre-check for credentials_fp_slot_vs_concurrency so we
  // surface a friendly 400 before round-tripping to the server. Empty
  // fields are passed as null so the server trigger fills the default.
  if (addCredConcurrency.value != null && addCredFpSlot.value != null
      && addCredFpSlot.value > addCredConcurrency.value) {
    addCredErr.value = `指纹槽 (${addCredFpSlot.value}) 不能超过并发上限 (${addCredConcurrency.value})`
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
    const formatted = formatCredentialError(e, '添加失败')
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
    saveMsg.value = `指纹槽 (${c.fp_slot_limit}) 不能超过并发上限 (${c.concurrency_limit})`
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
    const formatted = formatCredentialError(e, '保存失败')
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
  if (e instanceof ApiError && e.code === 'fp_slot_exceeds_concurrency') {
    const ctx = (e.context && typeof e.context === 'object') ? e.context as any : null
    const ac = ctx?.attempted_concurrency
    const af = ctx?.attempted_fp_slot
    if (ac != null && af != null) {
      return {
        message: `指纹槽 (${af}) 不能超过并发上限 (${ac})`,
        kind: 'fp_slot_exceeds_concurrency',
        context: ctx,
      }
    }
    return { message: e.message, kind: 'fp_slot_exceeds_concurrency', context: ctx }
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
    checkMsg.value = probeResultMsg(r)
    emit('refresh')
  } catch (e: unknown) {
    checkMsg.value = e instanceof Error ? e.message : '检测失败'
  } finally {
    checking.value = false
  }
}

async function delSelected() {
  const c = selected.value
  if (!c || !confirm('确认停用该凭据？')) return
  try {
    await deleteCredential(props.provider.id, c.id)
    closeDrawer()
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '停用失败')
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
      `手工${next ? '禁用' : '启用'}该凭据的原因（必填，会写入审计日志）：`,
      ''
    )
    if (reason === null) return
    if (reason.trim() !== '') {
      try {
        await setCredentialManualDisabled(props.provider.id, c.id, next, reason.trim())
        c.manual_disabled = next
        emit('refresh')
      } catch (e: unknown) {
        alert(e instanceof Error ? e.message : '设置失败')
      }
      return
    }
    alert('原因不能为空，请重新输入。')
  }
}

async function setLifecycle(value: string) {
  const c = selected.value
  if (!c) return
  try {
    await updateCredentialLifecycle(props.provider.id, c.id, value)
    c.lifecycle_status = value as 'active' | 'disabled' | 'suspended' | 'retired' | null
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '设置失败')
  }
}

async function resetAvailability() {
  const c = selected.value
  if (!c || !confirm(`重置 ${c.label} 的可用性状态？`)) return
  try {
    await resetCredentialAvailability(props.provider.id, c.id)
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '重置失败')
  }
}

async function resetQuota() {
  const c = selected.value
  if (!c || !confirm(`重置 ${c.label} 的配额状态？`)) return
  try {
    await resetCredentialQuota(props.provider.id, c.id)
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '重置失败')
  }
}

async function forceRecover() {
  const c = selected.value
  if (!c || !confirm(`强制触发 ${c.label} 立即恢复探活？`)) return
  try {
    await forceRecoverCredential(c.id)
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '触发失败')
  }
}

async function setDefaultModel() {
  const c = selected.value
  if (!c) return
  const v = prompt('手工设置默认探活模型（留空清空）：', c.default_probe_model ?? '')
  if (v === null) return
  try {
    await setDefaultProbeModel(props.provider.id, c.id, v === '' ? null : v, 'admin UI set')
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '设置失败')
  }
}

async function repickDefault() {
  const c = selected.value
  if (!c) return
  try {
    const r = await pickDefaultProbeModel(props.provider.id, c.id)
    if (!r.model) {
      alert('未找到候选模型（可能没有可用绑定）')
    } else {
      alert(`已选: ${r.model} (${r.source})`)
    }
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '重选失败')
  }
}

async function resetFpSlots() {
  const c = selected.value
  if (!c || !confirm(`确认复位 ${c.label} 的指纹槽（将清空所有占用）？`)) return
  try {
    const r = await resetCredentialFpSlots(props.provider.id, c.id)
    alert(`复位成功：清空 ${r.deleted_slots} 个槽位，${r.deleted_pins} 个会话绑定`)
    fpSlotStats.value = null
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '复位失败')
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
    alert(e instanceof Error ? e.message : '释放槽位失败')
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
    alert(e instanceof Error ? e.message : '加载指纹槽统计失败')
  } finally {
    fpSlotStatsLoading.value = false
  }
}

function fmtTtl(seconds: number): string {
  if (seconds <= 0) return '已过期'
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
      <h3 style="margin:0">凭据列表</h3>
      <button class="btn btn-primary btn-sm" @click="openAddCred">+ 添加凭据</button>
    </div>

    <div class="card" style="overflow-x:auto">
      <table class="data-table cred-table">
        <thead>
          <tr>
            <th>凭据</th>
            <th>状态</th>
            <th>探活</th>
            <th>默认探活模型</th>
            <th>并发</th>
            <th>用量</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!creds.length"><td colspan="6">暂无凭据</td></tr>
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
              <div class="cred-label">{{ c.label || `凭据 #${c.id}` }}</div>
              <div class="key-fingerprint" :title="'与上游平台核对用，非完整密钥'">
                {{ c.key_masked ?? (c.key_mask_error ? '无法解析' : '—') }}
              </div>
              <div class="cred-meta">#{{ c.id }} · {{ c.trust_level }}</div>
            </td>
            <td>
              <span class="badge" :class="statusBadge(c.status, c.manual_disabled)">{{ statusLabel(c.status, c.manual_disabled) }}</span>
              <div class="cell-sub">{{ c.lifecycle_status }}</div>
            </td>
            <td>
              <span class="badge" :class="healthBadge(c.health_status)">{{ healthLabel(c.health_status) }}</span>
              <div class="cell-sub">{{ timeText(c.health_checked_at) }}</div>
            </td>
            <td>
              <code v-if="c.default_probe_model" class="mono-sm">{{ c.default_probe_model }}</code>
              <span v-else class="cell-muted">未设置</span>
            </td>
            <td>
              {{ c.concurrency_limit || '不限' }}
              <div v-if="c.fp_slot_limit != null" class="cell-sub">
                槽 {{ c.fp_slots_used ?? 0 }}/{{ c.fp_slot_limit }}
              </div>
            </td>
            <td>
              <div>{{ c.total_requests }} 次</div>
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
            <h3 style="margin:0">{{ selected.label || `凭据 #${selected.id}` }}</h3>
            <div class="drawer-sub">#{{ selected.id }} · {{ selected.trust_level }}</div>
          </div>
          <button type="button" class="btn btn-ghost btn-sm" @click="closeDrawer">关闭</button>
        </div>

        <div class="drawer-body">
          <div class="drawer-section">
            <div class="drawer-section-title">基本信息</div>
            <label class="field-label">标签</label>
            <input v-model="selected.label" class="field-input" />
            <div class="key-fingerprint drawer-key">{{ selected.key_masked ?? '—' }}</div>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">状态</div>
            <div class="field-grid">
              <div>
                <label class="field-label">运行状态</label>
                <select v-model="selected.status" class="field-input">
                  <option v-for="s in statuses" :key="s.value" :value="s.value">{{ s.label }}</option>
                </select>
              </div>
              <div>
                <label class="field-label">生命周期</label>
                <select
                  :value="selected.lifecycle_status"
                  class="field-input"
                  @change="(e: Event) => setLifecycle((e.target as HTMLSelectElement).value)"
                >
                  <option v-for="s in lifecycleStatuses" :key="s.value" :value="s.value">{{ s.label }}</option>
                </select>
              </div>
            </div>
            <label class="manual-toggle">
              <input type="checkbox" :checked="!!selected.manual_disabled" @change="toggleManualDisabled" />
              <span>手工{{ selected.manual_disabled ? '已禁用' : '可用' }} 🔒</span>
            </label>
            <div v-if="selected.state_reason_code" class="cell-sub" :title="selected.state_reason_detail || ''">
              {{ selected.state_reason_code }}
            </div>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">探活</div>
            <div class="info-row">
              <span class="badge" :class="healthBadge(selected.health_status)">{{ healthLabel(selected.health_status) }}</span>
              <span class="cell-muted">{{ timeText(selected.health_checked_at) }}</span>
            </div>
            <div v-if="selected.health_probe_model" class="cell-sub">probe: {{ selected.health_probe_model }}</div>
            <div v-if="selected.health_error" class="cell-sub cell-sub--danger">{{ selected.health_error }}</div>
            <div class="btn-row">
              <button class="btn btn-sm" :disabled="checking" @click="checkSelected">立即检测</button>
            </div>
            <div v-if="checking" class="probe-status probe-status--loading" role="status" aria-live="polite">
              <span class="probe-spinner" aria-hidden="true"></span>
              正在探活…
            </div>
            <div v-else-if="checkMsg" class="cell-sub">{{ checkMsg }}</div>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">默认探活模型</div>
            <code v-if="selected.default_probe_model" class="mono-sm">{{ selected.default_probe_model }}</code>
            <span v-else class="cell-muted">未设置</span>
            <div class="cell-sub">{{ sourceLabel(selected.default_probe_model_source) }}</div>
            <div class="btn-row">
              <button class="btn btn-sm" @click="setDefaultModel">手工设置</button>
              <button class="btn btn-sm" @click="repickDefault">立即重选</button>
            </div>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">并发与有效期</div>
            <div class="field-grid">
              <div>
                <label class="field-label">并发上限（0=不限）</label>
                <input v-model.number="selected.concurrency_limit" type="number" min="0" class="field-input" />
              </div>
              <div>
                <label class="field-label">指纹槽</label>
                <div class="cell-muted" style="margin-bottom:4px">
                  <template v-if="selected.fp_slot_limit != null">
                    {{ selected.fp_slots_used ?? 0 }}/{{ selected.fp_slot_limit }}
                    <span v-if="(selected.fp_slots_free ?? 0) === 0" class="cell-sub--danger">已满</span>
                  </template>
                  <template v-else>无限</template>
                </div>
                <input
                  v-model.number="selected.fp_slot_limit"
                  type="number"
                  min="1"
                  :max="selected.concurrency_limit && selected.concurrency_limit > 0 ? selected.concurrency_limit : 10000"
                  class="field-input"
                  :placeholder="`建议: ${selectedFpSlotHint}`"
                  :title="`建议 ${selectedFpSlotHint}（并发÷4，至少 1）`"
                  @input="selectedFpSlotTouched = true"
                />
                <div class="form-hint" style="display:flex;justify-content:space-between;align-items:center">
                  <span>
                    <template v-if="selectedFpSlotTouched">已手动设置；改动并发不会再自动调整</template>
                    <template v-else>建议 {{ selectedFpSlotHint }}（并发÷4，向下取整，至少 1）</template>
                  </span>
                  <button
                    v-if="selectedFpSlotTouched && selected.fp_slot_limit !== selectedFpSlotHint"
                    class="btn btn-sm btn-ghost"
                    type="button"
                    @click="resetSelectedFpSlot"
                    title="恢复到根据并发自动计算的建议值"
                  >恢复建议值</button>
                </div>
                <div v-if="selected.fp_slot_limit != null" class="btn-row" style="margin-top:4px">
                  <button class="btn btn-sm btn-warning-outline" @click="resetFpSlots" title="清空所有占用的指纹槽">
                    复位槽位
                  </button>
                  <button class="btn btn-sm" @click="loadFpSlotStats" :disabled="fpSlotStatsLoading" title="查看每个槽位的详细状态">
                    {{ fpSlotStatsLoading ? '加载中…' : '查看详情' }}
                  </button>
                </div>
                <div v-if="saveMsg && saveMsgKind === 'fp_slot_exceeds_concurrency'" class="cell-sub cell-sub--danger" style="display:flex;justify-content:space-between;align-items:center;gap:8px">
                  <span>{{ saveMsg }}</span>
                  <button
                    class="btn btn-sm btn-warning-outline"
                    type="button"
                    @click="recoverFromRejection('edit')"
                    title="把并发和槽位恢复到服务器上一次接受的值"
                  >恢复到建议值</button>
                </div>
              </div>
            </div>
            <div class="field-grid" style="margin-top:8px">
              <div>
                <label class="field-label">生效时间</label>
                <input
                  :value="asDateInput(selected.effective_at)"
                  type="datetime-local"
                  class="field-input"
                  @input="onEffectiveInput"
                />
              </div>
              <div>
                <label class="field-label">过期时间</label>
                <input
                  :value="asDateInput(selected.expires_at)"
                  type="datetime-local"
                  class="field-input"
                  @input="onExpiresInput"
                />
              </div>
            </div>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">用量</div>
            <div>{{ selected.total_requests }} 次 · {{ money(selected.total_cost_usd) }}</div>
            <div class="cell-sub">余额 {{ money(selected.quota_summary?.remaining_usd ?? null) }}</div>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">标签</div>
            <input
              :value="(selected.tags ?? []).join(', ')"
              class="field-input"
              placeholder="tag1, tag2"
              @input="onTagsInput"
            />
          </div>

          <div v-if="fpSlotStats" class="drawer-section">
            <div class="drawer-section-title">指纹槽位图</div>
            <div v-if="fpSlotStats.unlimited" class="cell-muted">{{ fpSlotStats.message }}</div>
            <FpSlotVisualizer
              v-else-if="fpSlotStats.slot_limit && fpSlotStats.details"
              :details="fpSlotStats.details"
              :slot-limit="fpSlotStats.slot_limit"
              @release="releaseFpSlot"
            />
            <div v-else-if="fpSlotStats.details" class="cell-muted">无槽位数据</div>
          </div>

          <div class="drawer-section drawer-section--danger">
            <div class="drawer-section-title">高级操作</div>
            <div class="btn-row">
              <button class="btn btn-sm" @click="resetAvailability">重置可用性</button>
              <button class="btn btn-sm" @click="resetQuota">重置配额</button>
              <button class="btn btn-sm" @click="forceRecover">强制恢复</button>
              <button class="btn btn-sm btn-danger-outline" @click="delSelected">停用凭据</button>
            </div>
          </div>
        </div>

        <div class="drawer-footer">
          <div v-if="saveMsg" class="cell-sub cell-sub--danger">{{ saveMsg }}</div>
          <div class="btn-row btn-row--end">
            <button class="btn btn-ghost" @click="closeDrawer">取消</button>
            <button class="btn btn-primary" :disabled="saving" @click="saveSelected">
              {{ saving ? '保存中…' : '保存' }}
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Add Credential Modal -->
    <div class="modal-overlay" v-if="showAddCred" @click.self="showAddCred = false">
      <div class="modal" style="max-width:400px" @click.stop>
        <h3>添加凭据 — {{ provider?.display_name }}</h3>
        <div v-if="addCredErr" class="alert alert-danger" style="display:flex;justify-content:space-between;align-items:center;gap:8px">
          <span>{{ addCredErr }}</span>
          <button
            v-if="addCredErrKind === 'fp_slot_exceeds_concurrency'"
            class="btn btn-sm btn-warning-outline"
            type="button"
            @click="recoverFromRejection('add')"
            title="把表单恢复到服务器上一次接受的值"
          >恢复到建议值</button>
        </div>
        <div class="form-group">
          <label>API Key</label>
          <input v-model="addCredKey" type="password" placeholder="sk-…" autocomplete="off" />
        </div>
        <div class="form-group">
          <label>标签（可选）</label>
          <input v-model="addCredLabel" placeholder="如: 生产密钥" />
        </div>
        <div class="form-group" style="display:grid;grid-template-columns:1fr 1fr;gap:12px">
          <div>
            <label>并发上限（0=不限）</label>
            <input
              v-model.number="addCredConcurrency"
              type="number"
              min="0"
              placeholder="如: 10"
            />
            <div class="form-hint">凭据同时能跑多少请求</div>
          </div>
          <div>
            <label>指纹槽</label>
            <input
              v-model.number="addCredFpSlot"
              type="number"
              min="1"
              :max="addCredConcurrency && addCredConcurrency > 0 ? addCredConcurrency : 10000"
              :placeholder="`建议: ${addCredFpSlotHint}`"
              @input="addCredFpSlotTouched = true"
            />
            <div class="form-hint">
              建议 {{ addCredFpSlotHint }}（并发÷4，向下取整，至少 1）
            </div>
          </div>
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" @click="showAddCred = false">取消</button>
          <button class="btn btn-primary" @click="submitAddCred" :disabled="addCredSaving">
            {{ addCredSaving ? '添加中…' : '添加' }}
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
  background: rgba(99, 102, 241, 0.06);
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
  background: var(--bg-subtle, #161b22);
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
  color: #f59e0b;
  border-color: #f59e0b;
  background: transparent;
}
.btn-warning-outline:hover {
  background: rgba(245, 158, 11, 0.1);
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
  color: var(--accent, #6366f1);
}
.probe-spinner {
  display: inline-block;
  width: 10px;
  height: 10px;
  border: 2px solid rgba(99, 102, 241, 0.3);
  border-top-color: var(--accent, #6366f1);
  border-radius: 50%;
  animation: probe-spin 0.8s linear infinite;
}
@keyframes probe-spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
