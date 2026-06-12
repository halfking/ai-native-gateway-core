<script setup lang="ts">
import { ref, watch } from 'vue'
import {
  updateProvider,
  batchRecoverCredentials,
  checkProvider,
  type ProviderDetail,
} from '../../api'

const props = defineProps<{ provider: ProviderDetail }>()
const emit = defineEmits(['refresh'])

const editName = ref(props.provider.display_name)
const editBaseUrl = ref(props.provider.base_url)
const editProtocol = ref(props.provider.protocol)
const editKind = ref(props.provider.kind)
const editCategory = ref(props.provider.category)
const editDiscountRate = ref(props.provider.discount_rate)
const editEgressProfile = ref(props.provider.egress_profile || 'direct')
const editNotes = ref(props.provider.notes || '')
const saving = ref(false)
const msg = ref('')
const batchMsg = ref('')
const batchLoading = ref(false)
const checking = ref(false)
const checkMsg = ref('')

function syncFromProvider(p: ProviderDetail) {
  editName.value = p.display_name
  editBaseUrl.value = p.base_url
  editProtocol.value = p.protocol
  editKind.value = p.kind
  editCategory.value = p.category
  editDiscountRate.value = p.discount_rate
  editEgressProfile.value = p.egress_profile || 'direct'
  editNotes.value = p.notes || ''
}

watch(() => props.provider, syncFromProvider, { deep: true })

function fmtTime(v: string | null | undefined) {
  if (!v) return '—'
  return new Date(v).toLocaleString('zh-CN', { hour12: false })
}

async function save() {
  saving.value = true
  msg.value = ''
  try {
    await updateProvider(props.provider.id, {
      display_name: editName.value || undefined,
      base_url: editBaseUrl.value || undefined,
      protocol: editProtocol.value || undefined,
      kind: editKind.value || undefined,
      category: editCategory.value || undefined,
      discount_rate: editDiscountRate.value != null ? Number(editDiscountRate.value) : undefined,
      egress_profile: editEgressProfile.value || undefined,
      notes: editNotes.value || undefined,
    })
    msg.value = '已保存'
    emit('refresh')
  } catch (e: unknown) {
    msg.value = '保存失败: ' + (e instanceof Error ? e.message : '')
  } finally {
    saving.value = false
  }
}

async function batchRecover() {
  if (!confirm('确认批量恢复所有冷却中凭据？')) return
  batchLoading.value = true
  batchMsg.value = ''
  try {
    const r = await batchRecoverCredentials(props.provider.id)
    batchMsg.value = `恢复 ${r.recovered} 个凭据`
    emit('refresh')
  } catch (e: unknown) {
    batchMsg.value = '失败: ' + (e instanceof Error ? e.message : '')
  } finally {
    batchLoading.value = false
  }
}

async function runHealthCheck() {
  checking.value = true
  checkMsg.value = ''
  try {
    const r = await checkProvider(props.provider.id)
    checkMsg.value = r.reason || '检测已启动'
    setTimeout(() => emit('refresh'), 5000)
  } catch (e: unknown) {
    checkMsg.value = e instanceof Error ? e.message : '检测失败'
  } finally {
    checking.value = false
  }
}
</script>

<template>
  <div class="settings-tab">
    <div class="card settings-card">
      <h4>编辑提供商</h4>
      <div class="readonly-grid">
        <div class="form-group">
          <label>目录代码</label>
          <input class="form-input readonly" :value="provider.catalog_code || '—'" readonly />
        </div>
        <div class="form-group">
          <label>供应商</label>
          <input class="form-input readonly" :value="provider.vendor_name || '—'" readonly />
        </div>
        <div class="form-group">
          <label>Header Profile</label>
          <input class="form-input readonly" :value="provider.header_profile_code || '—'" readonly />
        </div>
      </div>

      <div class="edit-grid">
        <div class="form-group">
          <label>显示名</label>
          <input v-model="editName" class="form-input" />
        </div>
        <div class="form-group">
          <label>Base URL</label>
          <input v-model="editBaseUrl" class="form-input" />
        </div>
        <div class="form-group">
          <label>协议</label>
          <select v-model="editProtocol" class="form-input">
            <option value="openai-completions">OpenAI Completions</option>
            <option value="openai-responses">OpenAI Responses</option>
            <option value="anthropic-messages">Anthropic Messages</option>
            <option value="google-gemini">Google Gemini</option>
            <option value="gemini-generate">Gemini Generate</option>
          </select>
        </div>
        <div class="form-group">
          <label>出境配置</label>
          <select v-model="editEgressProfile" class="form-input">
            <option value="direct">direct</option>
            <option value="proxy">proxy</option>
            <option value="relay">relay</option>
          </select>
        </div>
        <div class="form-group">
          <label>类型</label>
          <select v-model="editKind" class="form-input">
            <option value="cloud">Cloud</option>
            <option value="local">Local</option>
          </select>
        </div>
        <div class="form-group">
          <label>分类</label>
          <select v-model="editCategory" class="form-input">
            <option value="official">Official</option>
            <option value="official_proxy">Official Proxy</option>
            <option value="third_party_relay">Third Party Relay</option>
            <option value="aggregator">Aggregator</option>
            <option value="self_host">Self Host</option>
          </select>
        </div>
        <div class="form-group">
          <label>折扣率</label>
          <input v-model.number="editDiscountRate" type="number" step="0.01" min="0" max="1" class="form-input" />
        </div>
        <div class="form-group form-group--full">
          <label>备注</label>
          <input v-model="editNotes" class="form-input" placeholder="内部说明" />
        </div>
      </div>

      <div class="form-actions">
        <button class="btn btn-primary btn-sm" @click="save" :disabled="saving">
          {{ saving ? '保存中…' : '保存' }}
        </button>
        <span v-if="msg" class="muted" :class="{ 'text-danger': msg.startsWith('保存失败') }">{{ msg }}</span>
      </div>
    </div>

    <div class="card settings-card">
      <h4>批量操作</h4>
      <div class="batch-actions">
        <button class="btn btn-ghost btn-sm" @click="batchRecover" :disabled="batchLoading">
          {{ batchLoading ? '恢复中…' : '批量恢复冷却凭据' }}
        </button>
        <button class="btn btn-ghost btn-sm" @click="runHealthCheck" :disabled="checking">
          {{ checking ? '检测中…' : '健康检测' }}
        </button>
        <span v-if="batchMsg" class="muted">{{ batchMsg }}</span>
        <span v-if="checkMsg" class="muted">{{ checkMsg }}</span>
      </div>
    </div>

    <div class="card settings-card">
      <h4>提供商信息</h4>
      <div class="info-grid">
        <div><span class="info-label">ID</span>{{ provider.id }}</div>
        <div><span class="info-label">代码</span><code>{{ provider.code }}</code></div>
        <div><span class="info-label">目录代码</span><code>{{ provider.catalog_code || '—' }}</code></div>
        <div><span class="info-label">协议</span><code>{{ provider.protocol }}</code></div>
        <div><span class="info-label">Base URL</span><code class="url">{{ provider.base_url || '—' }}</code></div>
        <div><span class="info-label">类型</span>{{ provider.kind }} / {{ provider.category }}</div>
        <div><span class="info-label">出境配置</span>{{ provider.egress_profile || '—' }}</div>
        <div><span class="info-label">国产</span>{{ provider.domestic ? '是' : '否' }}</div>
        <div><span class="info-label">折扣率</span>{{ provider.discount_rate ?? '—' }}</div>
        <div><span class="info-label">厂商</span>{{ provider.vendor_name || '—' }}</div>
        <div><span class="info-label">Header Profile</span>{{ provider.header_profile_code || '—' }}</div>
        <div>
          <span class="info-label">状态</span>
          <span v-if="provider.manual_disabled" class="badge badge-red">手工禁用</span>
          <span v-else-if="provider.enabled" class="badge badge-green">已启用</span>
          <span v-else class="badge badge-gray">已禁用</span>
        </div>
        <div><span class="info-label">创建时间</span>{{ fmtTime(provider.created_at) }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.settings-tab {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.settings-card h4 {
  margin: 0 0 14px;
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
}
.readonly-grid,
.edit-grid,
.info-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px 16px;
}
.form-group { min-width: 0; }
.form-group--full { grid-column: 1 / -1; }
.form-group label {
  display: block;
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 4px;
}
.form-input {
  width: 100%;
  padding: 6px 10px;
  font-size: 13px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  color: var(--text);
}
.form-input.readonly {
  background: var(--card);
  color: var(--muted);
  cursor: default;
}
.form-input:focus { border-color: var(--accent); outline: none; }
.form-actions,
.batch-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
  margin-top: 14px;
}
.muted { color: var(--muted); font-size: 12px; }
.text-danger { color: var(--danger); }
.info-grid {
  font-size: 13px;
  color: var(--text);
}
.info-label {
  display: inline-block;
  min-width: 88px;
  color: var(--muted);
  font-size: 12px;
  margin-right: 8px;
}
.info-grid code.url {
  word-break: break-all;
}
@media (max-width: 720px) {
  .readonly-grid,
  .edit-grid,
  .info-grid {
    grid-template-columns: 1fr;
  }
}
</style>
