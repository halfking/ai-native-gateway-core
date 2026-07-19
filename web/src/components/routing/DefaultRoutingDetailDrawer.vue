<script setup lang="ts">
import { reactive, watch, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import ModelPicker from '../ModelPicker.vue'
import type { RoutingDefault, RoutingDefaultUpdate } from '../../api/tuning'

const props = defineProps<{
  row: RoutingDefault | null
}>()

const emit = defineEmits<{
  close: []
  save: [payload: { id: number; body: RoutingDefaultUpdate }]
  remove: [row: RoutingDefault]
}>()

const { t } = useI18n()
const saving = ref(false)
const error = ref('')
const form = reactive({
  tier: 'primary' as RoutingDefault['tier'],
  profile: '',
  canonical_model: '',
  tenant_id: '',
  priority: 100,
  reason: '',
  expires_at: '',
  clear_expires: false,
})

const PROFILES = [
  { key: '', labelKey: 'routingDefault.profiles.any' },
  { key: 'smart', labelKey: 'routingDefault.profiles.smart' },
  { key: 'speed_first', labelKey: 'routingDefault.profiles.speed_first' },
  { key: 'cost_first', labelKey: 'routingDefault.profiles.cost_first' },
]

function toLocalInput(iso?: string) {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

watch(() => props.row, (row) => {
  error.value = ''
  if (!row) return
  form.tier = row.tier
  form.profile = row.profile || ''
  form.canonical_model = row.canonical_model
  form.tenant_id = row.tenant_id || ''
  form.priority = row.priority
  form.reason = row.reason || ''
  form.expires_at = toLocalInput(row.expires_at)
  form.clear_expires = false
}, { immediate: true })

async function onSave() {
  if (!props.row) return
  error.value = ''
  saving.value = true
  try {
    const body: RoutingDefaultUpdate = {
      tier: form.tier,
      profile: form.profile,
      canonical_model: form.canonical_model.trim(),
      priority: form.priority,
      reason: form.reason,
    }
    if (!form.tenant_id.trim()) body.clear_tenant = true
    else body.tenant_id = form.tenant_id.trim()
    if (form.clear_expires || !form.expires_at) body.clear_expires = true
    else body.expires_at = new Date(form.expires_at).toISOString()
    emit('save', { id: props.row.id, body })
  } catch (e: any) {
    error.value = e?.message ?? String(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Teleport to="body">
    <div v-if="row" class="drawer-overlay" @click.self="emit('close')">
      <aside class="drawer" role="dialog" :aria-label="t('routingDefault.detail.title', { id: row.id })">
        <header class="drawer-head">
          <h3>{{ t('routingDefault.detail.title', { id: row.id }) }}</h3>
          <button type="button" class="close" @click="emit('close')">×</button>
        </header>
        <div class="drawer-body">
          <label>
            {{ t('routingDefault.fields.taskType') }}
            <input :value="row.task_type" disabled />
          </label>
          <label>
            {{ t('routingDefault.fields.tier') }}
            <select v-model="form.tier">
              <option value="primary">{{ t('routingDefault.tiers.primary') }}</option>
              <option value="secondary">{{ t('routingDefault.tiers.secondary') }}</option>
              <option value="fallback">{{ t('routingDefault.tiers.fallback') }}</option>
            </select>
          </label>
          <label>
            {{ t('routingDefault.fields.model') }}
            <ModelPicker v-model="form.canonical_model" />
          </label>
          <label>
            {{ t('routingDefault.fields.profile') }}
            <select v-model="form.profile">
              <option v-for="p in PROFILES" :key="p.key || 'any'" :value="p.key">{{ t(p.labelKey) }}</option>
            </select>
          </label>
          <label>
            {{ t('routingDefault.fields.priority') }}
            <input v-model.number="form.priority" type="number" />
          </label>
          <label>
            {{ t('routingDefault.fields.platform') }}
            <input v-model="form.tenant_id" type="text" :placeholder="t('routingDefault.scope.platform')" />
          </label>
          <label>
            {{ t('routingDefault.fields.reason') }}
            <input v-model="form.reason" type="text" />
          </label>
          <label>
            {{ t('routingDefault.fields.expires') }}
            <input v-model="form.expires_at" type="datetime-local" :disabled="form.clear_expires" />
          </label>
          <label class="check">
            <input v-model="form.clear_expires" type="checkbox" />
            {{ t('routingDefault.detail.clearExpires') }}
          </label>
          <p v-if="error" class="error">{{ error }}</p>
        </div>
        <footer class="drawer-foot">
          <button type="button" class="btn btn-primary" :disabled="saving" @click="onSave">
            {{ saving ? t('routingDefault.actions.saving') : t('routingDefault.actions.save') }}
          </button>
          <button type="button" class="btn" @click="emit('close')">{{ t('routingDefault.actions.cancel') }}</button>
          <button type="button" class="btn btn-danger" @click="emit('remove', row)">{{ t('routingDefault.actions.delete') }}</button>
        </footer>
      </aside>
    </div>
  </Teleport>
</template>

<style scoped>
.drawer-overlay {
  position: fixed;
  inset: 0;
  background: rgba(15, 23, 42, 0.35);
  z-index: 80;
  display: flex;
  justify-content: flex-end;
}
.drawer {
  width: min(420px, 100vw);
  height: 100%;
  background: var(--bg-card, #fff);
  box-shadow: -8px 0 24px rgba(0,0,0,0.12);
  display: flex;
  flex-direction: column;
}
.drawer-head, .drawer-foot {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border, #e5e7eb);
}
.drawer-foot {
  border-bottom: none;
  border-top: 1px solid var(--border, #e5e7eb);
  margin-top: auto;
}
.drawer-head h3 { margin: 0; font-size: 15px; flex: 1; }
.close {
  border: none;
  background: transparent;
  font-size: 22px;
  cursor: pointer;
  line-height: 1;
}
.drawer-body {
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  overflow: auto;
}
.drawer-body label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
  color: var(--text-muted, #6b7280);
}
.drawer-body input,
.drawer-body select {
  padding: 8px 10px;
  border: 1px solid var(--border, #d1d5db);
  border-radius: 6px;
  font-size: 13px;
}
.check {
  flex-direction: row !important;
  align-items: center;
  gap: 8px !important;
}
.error { color: #b91c1c; font-size: 12px; }
.btn-danger { color: #b91c1c; margin-left: auto; }
</style>
