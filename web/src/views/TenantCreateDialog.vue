<script setup lang="ts">
import { ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { createTenant, TENANT_STATUSES } from '../api'
import type { CreateTenantResponse } from '../api'
import { useTenantStatusLabel } from '../composables/useTenantStatusLabel'

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'created'): void
}>()

const { t } = useI18n()
const { tenantStatusLabel } = useTenantStatusLabel()
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

const codeHint = computed(() =>
  t('tenants.create.codeHint', { suffix: `${form.value.code || 'code'}user` }),
)

async function handleSubmit() {
  if (!form.value.code || !form.value.name) {
    error.value = t('tenants.create.codeAndNameRequired')
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
    error.value = e instanceof Error ? e.message : t('tenants.create.createFailed')
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
        <h3>{{ t('tenants.create.title') }}</h3>
        <div v-if="error" class="alert alert-danger">{{ error }}</div>
        <form @submit.prevent="handleSubmit">
          <div class="form-group">
            <label>{{ t('tenants.create.codeLabel') }}</label>
            <input v-model="form.code" :placeholder="t('tenants.create.codePlaceholder')" pattern="[a-z0-9_-]+" required />
            <small>{{ codeHint }}</small>
          </div>
          <div class="form-group">
            <label>{{ t('tenants.create.nameLabel') }}</label>
            <input v-model="form.name" :placeholder="t('tenants.create.namePlaceholder')" required />
          </div>
          <div class="form-group">
            <label>{{ t('tenants.create.statusLabel') }}</label>
            <select v-model="form.status">
              <option v-for="s in TENANT_STATUSES" :key="s" :value="s">{{ tenantStatusLabel(s) }}</option>
            </select>
          </div>
          <div class="form-group">
            <label>{{ t('tenants.create.contactLabel') }}</label>
            <input v-model="form.contact_email" type="email" :placeholder="t('tenants.create.contactPlaceholder')" />
          </div>
          <div class="form-group">
            <label>{{ t('tenants.create.descLabel') }}</label>
            <textarea v-model="form.description" rows="3" :placeholder="t('tenants.create.descPlaceholder')"></textarea>
          </div>
          <div class="modal-actions">
            <button type="submit" class="btn btn-primary" :disabled="submitting">
              {{ submitting ? t('tenants.create.creating') : t('tenants.create.create') }}
            </button>
            <button type="button" class="btn btn-ghost" @click="emit('close')">{{ t('tenants.create.cancel') }}</button>
          </div>
        </form>
      </template>

      <template v-else>
        <h3>{{ t('tenants.create.successTitle') }}</h3>
        <p class="success-hint">{{ t('tenants.create.successHint') }}</p>
        <div class="cred-box">
          <div class="cred-row">
            <span class="cred-label">{{ t('tenants.create.credTenant') }}</span>
            <code>{{ createdResult.name }} ({{ createdResult.code }})</code>
          </div>
          <div v-if="createdResult.default_admin" class="cred-row">
            <span class="cred-label">{{ t('tenants.create.credAdmin') }}</span>
            <code>{{ createdResult.default_admin.username }}</code>
          </div>
          <div v-if="createdResult.initial_password" class="cred-row">
            <span class="cred-label">{{ t('tenants.create.credPassword') }}</span>
            <code class="password">{{ createdResult.initial_password }}</code>
          </div>
        </div>
        <div class="modal-actions">
          <button type="button" class="btn btn-primary" @click="goDetail">{{ t('tenants.create.goDetail') }}</button>
          <button type="button" class="btn btn-ghost" @click="emit('close')">{{ t('tenants.create.close') }}</button>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.modal-backdrop {
  position: fixed;
  inset: 0;
  background: var(--overlay-strong);
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
.alert-danger { background: var(--danger-bg); color: var(--danger); border: 1px solid var(--danger-bd); }
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
  color: var(--warning);
  font-weight: 600;
}
</style>
