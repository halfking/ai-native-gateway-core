<script setup lang="ts">
import { ref } from 'vue'
import {
  updateCredential, deleteCredential, checkCredential,
  addCredential,
  setCredentialManualDisabled, setDefaultProbeModel, pickDefaultProbeModel,
  resetCredentialAvailability, resetCredentialQuota, forceRecoverCredential,
  updateCredentialLifecycle, resetCredentialFpSlots,
  getCredentialFpSlotStats, type FpSlotStats,
  type ProviderCredential, type CredentialStatus,
} from '../../api'

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

const showAddCred = ref(false)
const addCredKey = ref('')
const addCredLabel = ref('')
const addCredSaving = ref(false)
const addCredErr = ref('')

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
  saveMsg.value = ''
  checkMsg.value = ''
}

function closeDrawer() {
  selected.value = null
  saveMsg.value = ''
  checkMsg.value = ''
}

function openAddCred() {
  addCredKey.value = ''
  addCredLabel.value = ''
  addCredErr.value = ''
  showAddCred.value = true
}

async function submitAddCred() {
  if (!addCredKey.value) { addCredErr.value = '请输入 API Key'; return }
  addCredSaving.value = true
  addCredErr.value = ''
  try {
    await addCredential(props.provider.id, {
      api_key: addCredKey.value,
      label: addCredLabel.value || undefined,
    })
    showAddCred.value = false
    emit('refresh')
  } catch (e: unknown) {
    addCredErr.value = e instanceof Error ? e.message : '添加失败'
  } finally {
    addCredSaving.value = false
  }
}

async function saveSelected() {
  const c = selected.value
  if (!c) return
  saving.value = true
  saveMsg.value = ''
  try {
    await updateCredential(props.provider.id, c.id, {
      label: c.label,
      status: c.status,
      concurrency_limit: c.concurrency_limit || null,
      effective_at: c.effective_at,
      expires_at: c.expires_at,
      tags: c.tags,
      notes: c.notes || '',
    })
    emit('refresh')
    closeDrawer()
  } catch (e: unknown) {
    saveMsg.value = e instanceof Error ? e.message : '保存失败'
  } finally {
    saving.value = false
  }
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
  const reason = prompt(`手工${next ? '禁用' : '启用'}该凭据的原因：`, '') ?? ''
  if (reason === null) return
  try {
    await setCredentialManualDisabled(props.provider.id, c.id, next, reason)
    c.manual_disabled = next
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '设置失败')
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
                <div class="cell-muted">
                  <template v-if="selected.fp_slot_limit != null">
                    {{ selected.fp_slots_used ?? 0 }}/{{ selected.fp_slot_limit }}
                    <span v-if="(selected.fp_slots_free ?? 0) === 0" class="cell-sub--danger">已满</span>
                  </template>
                  <template v-else>无限</template>
                </div>
                <div v-if="selected.fp_slot_limit != null" class="btn-row" style="margin-top:4px">
                  <button class="btn btn-sm btn-warning-outline" @click="resetFpSlots" title="清空所有占用的指纹槽">
                    复位槽位
                  </button>
                  <button class="btn btn-sm" @click="loadFpSlotStats" :disabled="fpSlotStatsLoading" title="查看每个槽位的详细状态">
                    {{ fpSlotStatsLoading ? '加载中…' : '查看详情' }}
                  </button>
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
            <div class="drawer-section-title">指纹槽详细状态</div>
            <div v-if="fpSlotStats.unlimited" class="cell-muted">{{ fpSlotStats.message }}</div>
            <div v-else>
              <div class="fp-slot-summary">
                <div class="fp-slot-stat">
                  <div class="fp-slot-stat-label">总槽位数</div>
                  <div class="fp-slot-stat-value">{{ fpSlotStats.slot_limit }}</div>
                </div>
                <div class="fp-slot-stat">
                  <div class="fp-slot-stat-label">已占用</div>
                  <div class="fp-slot-stat-value">{{ fpSlotStats.occupied_slots }}</div>
                </div>
                <div class="fp-slot-stat">
                  <div class="fp-slot-stat-label">空闲</div>
                  <div class="fp-slot-stat-value" :class="{ 'fp-slot-stat-value--danger': (fpSlotStats.free_slots ?? 0) === 0 }">
                    {{ fpSlotStats.free_slots }}
                  </div>
                </div>
              </div>
              <div v-if="fpSlotStats.details && fpSlotStats.details.length" class="fp-slot-list">
                <div class="fp-slot-list-header">
                  <span>槽位</span>
                  <span>Holder</span>
                  <span>剩余 TTL</span>
                </div>
                <div
                  v-for="d in fpSlotStats.details"
                  :key="d.index"
                  class="fp-slot-list-row"
                  :class="{ 'fp-slot-list-row--empty': d.expired && !d.holder }"
                >
                  <span class="fp-slot-list-index">#{{ d.index }}</span>
                  <span class="fp-slot-list-holder">
                    <code v-if="d.holder">{{ holderShort(d.holder) }}</code>
                    <span v-else class="cell-muted">空闲</span>
                  </span>
                  <span class="fp-slot-list-ttl" :class="{ 'cell-sub--danger': d.expired }">
                    {{ d.expired && !d.holder ? '—' : fmtTtl(d.ttl_seconds) }}
                  </span>
                </div>
              </div>
              <div v-if="fpSlotStats.holders && fpSlotStats.holders.length" class="cell-sub fp-slot-holders-hint">
                共 {{ fpSlotStats.holders.length }} 个会话占用此凭据的指纹池
              </div>
            </div>
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
        <div v-if="addCredErr" class="alert alert-danger">{{ addCredErr }}</div>
        <div class="form-group">
          <label>API Key</label>
          <input v-model="addCredKey" type="password" placeholder="sk-…" autocomplete="off" />
        </div>
        <div class="form-group">
          <label>标签（可选）</label>
          <input v-model="addCredLabel" placeholder="如: 生产密钥" />
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
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 12px;
  margin-bottom: 12px;
}
.fp-slot-stat {
  background: var(--bg-subtle, #161b22);
  padding: 10px;
  border-radius: 6px;
  text-align: center;
}
.fp-slot-stat-label {
  font-size: 11px;
  color: var(--muted);
  margin-bottom: 4px;
}
.fp-slot-stat-value {
  font-size: 20px;
  font-weight: 600;
  color: var(--text);
}
.fp-slot-stat-value--danger {
  color: var(--danger, #ef4444);
}
.fp-slot-list {
  border: 1px solid var(--border);
  border-radius: 6px;
  overflow: hidden;
  font-size: 12px;
}
.fp-slot-list-header {
  display: grid;
  grid-template-columns: 50px 1fr 80px;
  gap: 8px;
  padding: 6px 10px;
  background: var(--bg-subtle, #161b22);
  font-weight: 500;
  font-size: 11px;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.fp-slot-list-row {
  display: grid;
  grid-template-columns: 50px 1fr 80px;
  gap: 8px;
  padding: 6px 10px;
  border-top: 1px solid var(--border);
  align-items: center;
}
.fp-slot-list-row--empty {
  opacity: 0.5;
}
.fp-slot-list-index {
  font-family: ui-monospace, monospace;
  color: var(--muted);
}
.fp-slot-list-holder code {
  font-family: ui-monospace, monospace;
  font-size: 11px;
  background: var(--bg-subtle, #161b22);
  padding: 2px 6px;
  border-radius: 3px;
}
.fp-slot-list-ttl {
  font-family: ui-monospace, monospace;
  font-size: 11px;
  color: var(--muted);
}
.fp-slot-holders-hint {
  margin-top: 8px;
  font-size: 11px;
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
