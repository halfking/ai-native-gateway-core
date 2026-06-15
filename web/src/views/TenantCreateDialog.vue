<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { createTenant, TENANT_STATUSES, TENANT_STATUS_LABELS } from '../api'
import type { CreateTenantResponse } from '../api'

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'created'): void
}>()

const router = useRouter()
const form = ref({
  code: '',
  name: '',
  status: 'active',
  description: '',
  contact_email: '',
})
const submitting = ref(false)
const error = ref('')
const createdResult = ref<CreateTenantResponse | null>(null)

async function handleSubmit() {
  if (!form.value.code || !form.value.name) {
    error.value = 'code 和 name 不能为空'
    return
  }
  submitting.value = true
  error.value = ''
  try {
    createdResult.value = await createTenant({
      code: form.value.code,
      name: form.value.name,
      status: form.value.status,
      description: form.value.description,
      contact_email: form.value.contact_email,
    })
    emit('created')
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '创建失败'
  } finally {
    submitting.value = false
  }
}

function goDetail() {
  if (createdResult.value) {
    router.push(`/tenants/${createdResult.value.code}`)
  }
  emit('close')
}
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <div class="modal-card">
      <template v-if="!createdResult">
        <h3>新建租户</h3>
        <div v-if="error" class="alert alert-danger">{{ error }}</div>
        <form @submit.prevent="handleSubmit">
          <div class="form-group">
            <label>租户 code *</label>
            <input v-model="form.code" placeholder="例如: acme" pattern="[a-z0-9_-]+" required />
            <small>只能包含小写字母、数字、下划线和连字符；将自动创建管理员账号 {code}user</small>
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
      </template>

      <template v-else>
        <h3>✅ 租户创建成功</h3>
        <p class="success-hint">默认管理员已创建，初始密码仅显示一次，请妥善保存。</p>
        <div class="cred-box">
          <div class="cred-row">
            <span class="cred-label">租户</span>
            <code>{{ createdResult.name }} ({{ createdResult.code }})</code>
          </div>
          <div v-if="createdResult.default_admin" class="cred-row">
            <span class="cred-label">管理员</span>
            <code>{{ createdResult.default_admin.username }}</code>
          </div>
          <div v-if="createdResult.initial_password" class="cred-row">
            <span class="cred-label">初始密码</span>
            <code class="password">{{ createdResult.initial_password }}</code>
          </div>
        </div>
        <div class="modal-actions">
          <button type="button" class="btn btn-primary" @click="goDetail">进入租户详情</button>
          <button type="button" class="btn btn-ghost" @click="emit('close')">关闭</button>
        </div>
      </template>
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
.success-hint { font-size: 13px; color: var(--muted); margin: 0 0 12px; }
.cred-box {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
  margin-bottom: 8px;
}
.cred-row {
  display: flex;
  gap: 12px;
  padding: 6px 0;
  font-size: 13px;
  align-items: baseline;
}
.cred-label {
  width: 72px;
  flex-shrink: 0;
  color: var(--muted);
}
.cred-row code {
  font-family: 'SF Mono', 'Fira Code', monospace;
  word-break: break-all;
}
.cred-row code.password {
  color: #fbbf24;
  font-weight: 600;
}
</style>
