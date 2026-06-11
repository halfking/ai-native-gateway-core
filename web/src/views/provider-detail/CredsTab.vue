<script setup lang="ts">
import {
  updateCredential, deleteCredential, checkCredential, startCredentialCheck, getTask, addCredential,
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
          <tr v-if="!creds.length"><td colspan="8">暂无凭据</td></tr>
          <tr v-for="c in creds" :key="c.id">
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
            </td>
            <td>
              <span class="badge" :class="healthBadge(c.health_status)">{{ healthLabel(c.health_status) }}</span>
              <div style="font-size:11px;color:var(--muted)">{{ timeText(c.health_checked_at) }}</div>
              <div v-if="c.health_error" style="font-size:11px;color:var(--danger);max-width:200px;word-break:break-all">{{ c.health_error }}</div>
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
            <td>
              <div style="display:flex;gap:4px;flex-wrap:wrap">
                <button class="btn btn-primary btn-sm" @click="save(c)" :disabled="saving[c.id]">{{ saving[c.id] ? '保存中' : '保存' }}</button>
                <button class="btn btn-sm" @click="checkCred(c)" :disabled="checking[c.id]">{{ checking[c.id] ? '检测中' : '检测' }}</button>
                <button class="btn btn-sm" @click="delCred(c)">停用</button>
              </div>
              <div v-if="saveMsgs[c.id]" style="font-size:11px;color:var(--danger);margin-top:4px">{{ saveMsgs[c.id] }}</div>
              <div v-if="checkMsgs[c.id]" style="font-size:11px;color:var(--muted);margin-top:4px">{{ checkMsgs[c.id] }}</div>
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
.key-fingerprint {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, monospace;
  font-size: 11px;
  color: var(--text);
  margin-bottom: 4px;
  word-break: break-all;
}
</style>
