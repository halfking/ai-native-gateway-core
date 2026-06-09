<script setup lang="ts">
import { ref, watch } from 'vue'
import { updateProvider, batchRecoverCredentials, type ProviderDetail } from '../../api'

const props = defineProps<{ provider: ProviderDetail }>()
const emit = defineEmits(['refresh'])

const editName = ref(props.provider.display_name)
const editBaseUrl = ref(props.provider.base_url)
const editProtocol = ref(props.provider.protocol)
const editKind = ref(props.provider.kind)
const editCategory = ref(props.provider.category)
const editDiscountRate = ref(props.provider.discount_rate)
const editNotes = ref(props.provider.notes || '')
const saving = ref(false)
const msg = ref('')
const batchMsg = ref('')
const batchLoading = ref(false)

watch(() => props.provider, (p) => {
  editName.value = p.display_name
  editBaseUrl.value = p.base_url
  editProtocol.value = p.protocol
  editKind.value = p.kind
  editCategory.value = p.category
  editDiscountRate.value = p.discount_rate
  editNotes.value = p.notes || ''
})

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
      discount_rate: editDiscountRate.value ? Number(editDiscountRate.value) : undefined,
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
  } catch (e: unknown) {
    batchMsg.value = '失败: ' + (e instanceof Error ? e.message : '')
  } finally {
    batchLoading.value = false
  }
}
</script>

<template>
  <div>
    <div class="section">
      <h4>编辑提供商</h4>
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
          <option value="anthropic-messages">Anthropic Messages</option>
          <option value="google-gemini">Google Gemini</option>
          <option value="openai-responses">OpenAI Responses</option>
        </select>
      </div>
      <div class="form-row">
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
          <input v-model="editDiscountRate" type="number" step="0.01" min="0" max="1" class="form-input" />
        </div>
      </div>
      <div class="form-group">
        <label>备注</label>
        <input v-model="editNotes" class="form-input" placeholder="内部说明" />
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        <button class="btn btn-primary" @click="save" :disabled="saving">
          {{ saving ? '保存中...' : '保存' }}
        </button>
        <span v-if="msg" class="muted">{{ msg }}</span>
      </div>
    </div>

    <div class="section" style="margin-top:24px">
      <h4>批量操作</h4>
      <div class="batch-actions">
        <button class="btn btn-ghost" @click="batchRecover" :disabled="batchLoading">
          {{ batchLoading ? '恢复中...' : '批量恢复冷却凭据' }}
        </button>
        <span v-if="batchMsg" class="muted">{{ batchMsg }}</span>
      </div>
    </div>

    <div class="section" style="margin-top:24px">
      <h4>提供商信息</h4>
      <div class="info-grid">
        <div><span class="info-label">ID</span>{{ provider.id }}</div>
        <div><span class="info-label">代码</span><code>{{ provider.code }}</code></div>
        <div><span class="info-label">目录代码</span><code>{{ provider.catalog_code || '-' }}</code></div>
        <div><span class="info-label">协议</span><code>{{ provider.protocol }}</code></div>
        <div><span class="info-label">类型</span>{{ provider.kind }} / {{ provider.category }}</div>
        <div><span class="info-label">出境配置</span>{{ provider.egress_profile || '-' }}</div>
        <div><span class="info-label">国产</span>{{ provider.domestic ? '是' : '否' }}</div>
        <div><span class="info-label">折扣率</span>{{ provider.discount_rate || '-' }}</div>
        <div><span class="info-label">厂商</span>{{ provider.vendor_name || '-' }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.section { }
h4 { margin: 0 0 12px; font-size: 14px; color: var(--text); }
.form-group { margin-bottom: 12px; }
.form-group label { display: block; font-size: 12px; color: var(--muted); margin-bottom: 4px; }
.form-input {
  width: 100%;
  max-width: 480px;
  padding: 6px 10px;
  font-size: 13px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
}
.form-input:focus { border-color: var(--accent); outline: none; }
.form-row { display: flex; gap: 12px; }
.form-row .form-group { flex: 1; }
.muted { color: var(--muted); font-size: 12px; }
.batch-actions { display: flex; gap: 8px; align-items: center; }
.info-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 6px 24px;
  font-size: 13px;
  color: var(--text);
}
.info-label { color: var(--muted); margin-right: 8px; font-size: 12px; }
</style>
