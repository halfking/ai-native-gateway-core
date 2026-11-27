<script setup lang="ts">
// 组织创建弹窗（三级配额制，2026-11-27）。与 TenantCreateDialog 同构；
// 组织挂在既有租户下（tenant_id 下拉选择），无独立凭据发放。
import { ref } from 'vue'
import { surfaceError } from '../i18n/errorSurface'
import { useI18n } from 'vue-i18n'
import { createOrg, getTenantsAdmin } from '../api'
import type { Org, Tenant } from '../api'
import { onMounted } from 'vue'

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'created'): void
}>()

const { t } = useI18n()
const form = ref({
  tenant_id: '',
  code: '',
  name: '',
  description: '',
})
const submitting = ref(false)
const error = ref('')
const tenants = ref<Tenant[]>([])

onMounted(async () => {
  try {
    tenants.value = await getTenantsAdmin()
  } catch {
    tenants.value = []
  }
})

async function handleSubmit() {
  if (!form.value.tenant_id || !form.value.code || !form.value.name) {
    error.value = t('orgs.create.required')
    return
  }
  submitting.value = true
  error.value = ''
  try {
    await createOrg({
      tenant_id: form.value.tenant_id,
      code: form.value.code,
      name: form.value.name,
      description: form.value.description,
    })
    emit('created')
  } catch (e: unknown) {
    error.value = surfaceError(e, { key: 'orgs.create.createFailed' })
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <div class="modal-card">
      <h3>{{ t('orgs.create.title') }}</h3>
      <div v-if="error" class="alert alert-danger">{{ error }}</div>
      <form @submit.prevent="handleSubmit">
        <div class="form-group">
          <label>{{ t('orgs.create.tenantLabel') }}</label>
          <select v-model="form.tenant_id" required>
            <option value="" disabled>{{ t('orgs.create.tenantPlaceholder') }}</option>
            <option v-for="tt in tenants" :key="tt.code" :value="tt.code">{{ tt.name }} ({{ tt.code }})</option>
          </select>
        </div>
        <div class="form-group">
          <label>{{ t('orgs.create.codeLabel') }}</label>
          <input v-model="form.code" :placeholder="t('orgs.create.codePlaceholder')" pattern="[a-z0-9_-]+" minlength="2" maxlength="64" required />
          <small>{{ t('orgs.create.codeHint') }}</small>
        </div>
        <div class="form-group">
          <label>{{ t('orgs.create.nameLabel') }}</label>
          <input v-model="form.name" :placeholder="t('orgs.create.namePlaceholder')" required />
        </div>
        <div class="form-group">
          <label>{{ t('orgs.create.descLabel') }}</label>
          <textarea v-model="form.description" rows="3" :placeholder="t('orgs.create.descPlaceholder')"></textarea>
        </div>
        <div class="modal-actions">
          <button type="submit" class="btn btn-primary" :disabled="submitting">
            {{ submitting ? t('orgs.create.creating') : t('orgs.create.create') }}
          </button>
          <button type="button" class="btn btn-ghost" @click="emit('close')">{{ t('orgs.create.cancel') }}</button>
        </div>
      </form>
    </div>
  </div>
</template>

<style scoped>
.modal-backdrop {
  position: fixed; inset: 0;
  background: rgba(0, 0, 0, 0.4);
  display: flex; align-items: center; justify-content: center;
  z-index: 1000;
}
.modal-card {
  background: var(--bg);
  border-radius: 8px;
  padding: 24px;
  width: 480px; max-width: 92vw;
  max-height: 85vh; overflow-y: auto;
}
.form-group { margin-bottom: 14px; display: flex; flex-direction: column; gap: 4px; }
.form-group label { font-weight: 600; font-size: 13px; }
.form-group input, .form-group select, .form-group textarea {
  padding: 8px 10px; border: 1px solid var(--border);
  border-radius: 6px; font-size: 13px; background: var(--bg);
  color: var(--text);
}
.form-group small { color: var(--muted); font-size: 12px; }
.modal-actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 16px; }
.alert-danger { background: var(--danger-bg); color: var(--danger); padding: 8px 12px; border-radius: 6px; margin-bottom: 12px; font-size: 13px; }
</style>
