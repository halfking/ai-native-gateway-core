<script setup lang="ts">
import { ref } from 'vue'
import { createTenant, TENANT_STATUSES, TENANT_STATUS_LABELS } from '../api'

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'created'): void
}>()

const form = ref({
  code: '',
  name: '',
  status: 'active',
  description: '',
  contact_email: '',
})
const submitting = ref(false)
const error = ref('')

async function handleSubmit() {
  if (!form.value.code || !form.value.name) {
    error.value = 'code 和 name 不能为空'
    return
  }
  submitting.value = true
  error.value = ''
  try {
    await createTenant({
      code: form.value.code,
      name: form.value.name,
      status: form.value.status,
      description: form.value.description,
      contact_email: form.value.contact_email,
    })
    emit('created')
    emit('close')
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '创建失败'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <div class="modal-card">
      <h3>新建租户</h3>
      <div v-if="error" class="alert alert-danger">{{ error }}</div>
      <form @submit.prevent="handleSubmit">
        <div class="form-group">
          <label>租户 code *</label>
          <input v-model="form.code" placeholder="例如: acme" pattern="[a-z0-9_-]+" required />
          <small>只能包含小写字母、数字、下划线和连字符</small>
        </div>
        <div class="form-group">
          <label>租户名称 *</label>
          <input v-model="form.name" placeholder="例如: Acme Corp" required />
        </div>
        <div class="form-group">
          <label>状态</label>
          <select v-model="form.status">
            <option v-for="s in TENANT_STATUSES" :key="s" :value="s">{{ TENANT_STATUS_LABELS[s] }}</option>
          </select>
        </div>
        <div class="form-group">
          <label>联系邮箱</label>
          <input v-model="form.contact_email" type="email" placeholder="admin@example.com" />
        </div>
        <div class="form-group">
          <label>描述</label>
          <textarea v-model="form.description" rows="3" placeholder="可选"></textarea>
        </div>
        <div class="modal-actions">
          <button type="submit" class="btn btn-primary" :disabled="submitting">
            {{ submitting ? '创建中…' : '创建' }}
          </button>
          <button type="button" class="btn btn-ghost" @click="emit('close')">取消</button>
        </div>
      </form>
    </div>
  </div>
</template>

<style scoped>
.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0,0,0,.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}
.modal-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 24px;
  width: 480px;
  max-height: 90vh;
  overflow-y: auto;
}
.modal-card h3 { margin: 0 0 16px; }
.form-group { margin-bottom: 12px; }
.form-group label { display: block; font-size: 13px; margin-bottom: 4px; }
.form-group input, .form-group select, .form-group textarea {
  width: 100%;
  padding: 6px 10px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  font-size: 13px;
  font-family: inherit;
}
.form-group small { font-size: 11px; color: var(--muted); }
.modal-actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 16px; }
.alert { padding: 8px 12px; border-radius: 4px; font-size: 13px; margin-bottom: 12px; }
.alert-danger { background: rgba(239,68,68,.1); color: #f87171; border: 1px solid rgba(239,68,68,.3); }
</style>
