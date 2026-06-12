<script setup lang="ts">
import {
  updateCredential, deleteCredential, checkCredential, startCredentialCheck, getTask, addCredential,
  setCredentialManualDisabled, setDefaultProbeModel, pickDefaultProbeModel,
  resetCredentialAvailability, resetCredentialQuota, forceRecoverCredential,
  type ProviderCredential, type CredentialStatus,
} from '../../api'

const props = defineProps<{
  provider: any
  creds: ProviderCredential[]
}>()
const emit = defineEmits<{ refresh: [] }>()

const saving = ref<Record<number, boolean>>({})
const checking = ref<Record<number, boolean>>({})
const checkMsgs = ref<Record<number, string>>({})
const saveMsgs = ref<Record<number, string>>({})
const diagLoading = ref(false)
const diagResult = ref<{ credential_count: number; results: CredentialCheckResult[] } | null>(null)
const diagError = ref('')

// Add credential modal
const showAddCred = ref(false)
const addCredKey = ref('')
const addCredLabel = ref('')
const addCredSaving = ref(false)
const addCredErr = ref('')

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

import { ref } from 'vue'

function statusBadge(s: string) {
  if (s === 'active') return 'badge-green'
  if (s === 'disabled' || s === 'quota_expired' || s === 'quarantine') return 'badge-red'
  if (s === 'cooling' || s === 'degraded') return 'badge-amber'
  return 'badge-gray'
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
  return '未探测'
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

function asDateInput(v: string | null) {
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

async function save(c: ProviderCredential) {
  saving.value = { ...saving.value, [c.id]: true }
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
  } catch (e: unknown) {
    saveMsgs.value = { ...saveMsgs.value, [c.id]: e instanceof Error ? e.message : '保存失败' }
  } finally {
    saving.value = { ...saving.value, [c.id]: false }
  }
}

async function checkCred(c: ProviderCredential) {
  checking.value = { ...checking.value, [c.id]: true }
  checkMsgs.value = { ...checkMsgs.value, [c.id]: '' }
  try {
    const r = await checkCredential(props.provider.id, c.id)
    checkMsgs.value = { ...checkMsgs.value, [c.id]: `${r.health_status} · ${r.probe_ok ? '探活通过' : '不可用'}` }
    setTimeout(() => emit('refresh'), 3000)
  } catch (e: unknown) {
    checkMsgs.value = { ...checkMsgs.value, [c.id]: e instanceof Error ? e.message : '检测失败' }
  } finally {
    checking.value = { ...checking.value, [c.id]: false }
  }
}

async function delCred(c: ProviderCredential) {
  if (!confirm('确认停用该凭据？')) return
  try {
    await deleteCredential(props.provider.id, c.id)
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '停用失败')
  }
}

// 900-series: manual disable toggle (spec §6.2)
async function toggleManualDisabled(c: ProviderCredential) {
  const next = !c.manual_disabled
  const reason = prompt(`手工${next ? '禁用' : '启用'}该凭据的原因：`, '') ?? ''
  if (reason === null) return
  try {
    await setCredentialManualDisabled(props.provider.id, c.id, next, reason)
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '设置失败')
  }
}

async function setLifecycle(c: ProviderCredential, value: string) {
  try {
    // Re-use existing /lifecycle endpoint (UpdateCredentialLifecycle)
    const { updateCredentialLifecycle } = await import('../../api')
    await updateCredentialLifecycle(props.provider.id, c.id, value)
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '设置失败')
  }
}

async function resetAvailability(c: ProviderCredential) {
  if (!confirm(`重置 ${c.label} 的可用性状态？`)) return
  try {
    await resetCredentialAvailability(props.provider.id, c.id)
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '重置失败')
  }
}

async function resetQuota(c: ProviderCredential) {
  if (!confirm(`重置 ${c.label} 的配额状态？`)) return
  try {
    await resetCredentialQuota(props.provider.id, c.id)
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '重置失败')
  }
}

async function forceRecover(c: ProviderCredential) {
  if (!confirm(`强制触发 ${c.label} 立即恢复探活？`)) return
  try {
    await forceRecoverCredential(c.id)
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '触发失败')
  }
}

async function setDefaultModel(c: ProviderCredential) {
  const v = prompt('手工设置默认探活模型（留空清空）：', c.default_probe_model ?? '')
  if (v === null) return
  try {
    await setDefaultProbeModel(props.provider.id, c.id, v === '' ? null : v, 'admin UI set')
    emit('refresh')
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : '设置失败')
  }
}

async function repickDefault(c: ProviderCredential) {
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
</script>

<template>
  <div>
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
      <h3 style="margin:0">凭据列表</h3>
      <button class="btn btn-primary btn-sm" @click="openAddCred">+ 添加凭据</button>
    </div>
    <div style="overflow-x:auto">
      <table class="data-table" style="width:100%;font-size:12px">
        <thead>
          <tr>
            <th>凭据</th>
            <th>状态 / 生命周期 / 手工</th>
            <th>探活</th>
            <th>默认探活模型</th>
            <th>并发</th>
            <th>有效期</th>
            <th>用量</th>
            <th>标签</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!creds.length"><td colspan="9">暂无凭据</td></tr>
          <tr v-for="c in creds" :key="c.id" :style="c.manual_disabled ? 'background:rgba(220,38,38,0.06)' : ''">
            <td>
              <input v-model="c.label" class="compact-input" />
              <div class="key-fingerprint" :title="'与上游平台核对用，非完整密钥'">
                {{ c.key_masked ?? (c.key_mask_error ? '无法解析' : '—') }}
              </div>
              <div style="font-size:11px;color:var(--muted)">#{{ c.id }} · {{ c.trust_level }}</div>
            </td>
            <td>
              <select v-model="c.status" class="compact-input">
                <option v-for="s in statuses" :key="s.value" :value="s.value">{{ s.label }}</option>
              </select>
              <span class="badge" :class="statusBadge(c.status)">{{ c.status }}</span>
              <div style="margin-top:6px">
                <select
                  :value="c.lifecycle_status"
                  class="compact-input"
                  style="font-size:11px;padding:2px 4px"
                  @change="(e: Event) => setLifecycle(c, (e.target as HTMLSelectElement).value)"
                >
                  <option v-for="s in lifecycleStatuses" :key="s.value" :value="s.value">{{ s.label }}</option>
                </select>
              </div>
              <div style="margin-top:6px;display:flex;align-items:center;gap:4px">
                <input
                  type="checkbox"
                  :id="`md-${c.id}`"
                  :checked="!!c.manual_disabled"
                  @change="toggleManualDisabled(c)"
                />
                <label :for="`md-${c.id}`" style="font-size:11px;cursor:pointer">
                  手工{{ c.manual_disabled ? '已禁用' : '可用' }} 🔒
                </label>
              </div>
              <div v-if="c.state_reason_code" :title="c.state_reason_detail || ''" style="font-size:10px;color:var(--muted);margin-top:2px">
                {{ c.state_reason_code }}
              </div>
            </td>
            <td>
              <span class="badge" :class="healthBadge(c.health_status)">{{ healthLabel(c.health_status) }}</span>
              <div style="font-size:11px;color:var(--muted)">{{ timeText(c.health_checked_at) }}</div>
              <div v-if="c.health_probe_model" style="font-size:10px;color:var(--muted)">
                probe: {{ c.health_probe_model }}
              </div>
              <div v-if="c.health_error" style="font-size:11px;color:var(--danger);max-width:200px;word-break:break-all">{{ c.health_error }}</div>
            </td>
            <td>
              <div v-if="c.default_probe_model" style="font-family:ui-monospace,monospace;font-size:11px">
                {{ c.default_probe_model }}
              </div>
              <div v-else style="font-size:11px;color:var(--muted)">未设置</div>
              <div style="font-size:10px;color:var(--muted)">{{ sourceLabel(c.default_probe_model_source) }}</div>
              <div v-if="c.default_probe_model_picked_at" style="font-size:10px;color:var(--muted)">
                {{ timeText(c.default_probe_model_picked_at) }}
              </div>
            </td>
            <td>
              <input v-model.number="c.concurrency_limit" type="number" min="0" class="compact-input" style="max-width:80px" placeholder="默认5" />
              <div v-if="c.fp_slot_limit != null" style="font-size:11px;color:var(--muted);margin-top:4px">
                槽 {{ c.fp_slots_used ?? 0 }}/{{ c.fp_slot_limit }}
                <span v-if="(c.fp_slots_free ?? 0) === 0" style="color:var(--danger)">已满</span>
                <span v-else>余 {{ c.fp_slots_free }}</span>
              </div>
              <div v-else style="font-size:11px;color:var(--muted);margin-top:4px">无限（0=不限）</div>
            </td>
            <td>
              <input :value="asDateInput(c.effective_at)" type="datetime-local" class="compact-input" @input="c.effective_at = ($event.target as HTMLInputElement).value ? new Date(($event.target as HTMLInputElement).value).toISOString() : null" />
              <input :value="asDateInput(c.expires_at)" type="datetime-local" class="compact-input" @input="c.expires_at = ($event.target as HTMLInputElement).value ? new Date(($event.target as HTMLInputElement).value).toISOString() : null" />
            </td>
            <td>
              <div>{{ c.total_requests }} 次 · {{ money(c.total_cost_usd) }}</div>
              <div style="font-size:11px;color:var(--muted)">余额 {{ money(c.quota_summary?.remaining_usd ?? null) }}</div>
            </td>
            <td>
              <input :value="(c.tags ?? []).join(', ')" class="compact-input" placeholder="tag1, tag2" @input="c.tags = ($event.target as HTMLInputElement).value.split(',').map((t:string) => t.trim()).filter(Boolean)" />
            </td>
            <td class="actions-cell">
              <div class="actions-primary">
                <button class="btn btn-primary btn-sm" @click="save(c)" :disabled="saving[c.id]">{{ saving[c.id] ? '保存中' : '保存' }}</button>
                <button class="btn btn-sm" @click="checkCred(c)" :disabled="checking[c.id]">{{ checking[c.id] ? '检测中' : '检测' }}</button>
                <details class="actions-more">
                  <summary class="btn btn-ghost btn-sm">更多</summary>
                  <div class="actions-menu">
                    <button type="button" class="menu-item" @click="delCred(c)">停用凭据</button>
                    <button type="button" class="menu-item" @click="resetAvailability(c)">重置可用性</button>
                    <button type="button" class="menu-item" @click="resetQuota(c)">重置配额</button>
                    <button type="button" class="menu-item" @click="forceRecover(c)">强制恢复</button>
                    <button type="button" class="menu-item" @click="setDefaultModel(c)">设置默认探活模型</button>
                    <button type="button" class="menu-item" @click="repickDefault(c)">立即重选探活模型</button>
                  </div>
                </details>
              </div>
              <div v-if="saveMsgs[c.id]" class="action-msg action-msg--danger">{{ saveMsgs[c.id] }}</div>
              <div v-if="checkMsgs[c.id]" class="action-msg">{{ checkMsgs[c.id] }}</div>
            </td>
          </tr>
        </tbody>
      </table>
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
.actions-cell {
  min-width: 140px;
  vertical-align: top;
}
.actions-primary {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
}
.actions-more {
  position: relative;
}
.actions-more > summary {
  list-style: none;
  cursor: pointer;
}
.actions-more > summary::-webkit-details-marker {
  display: none;
}
.actions-menu {
  position: absolute;
  right: 0;
  top: calc(100% + 4px);
  z-index: 20;
  min-width: 160px;
  padding: 4px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 6px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.25);
}
.menu-item {
  display: block;
  width: 100%;
  text-align: left;
  border: none;
  background: transparent;
  color: var(--text);
  padding: 6px 10px;
  font-size: 12px;
  border-radius: 4px;
  cursor: pointer;
}
.menu-item:hover {
  background: rgba(99, 102, 241, 0.1);
}
.action-msg {
  font-size: 11px;
  color: var(--muted);
  margin-top: 4px;
}
.action-msg--danger {
  color: var(--danger);
}
.key-fingerprint {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, monospace;
  font-size: 11px;
  color: var(--text);
  margin-bottom: 4px;
  word-break: break-all;
}
</style>
