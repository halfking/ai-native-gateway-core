<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import ModelPicker from '../ModelPicker.vue'
import type { RoutingDefault } from '../../api/tuning'

export type TierKey = 'primary' | 'secondary' | 'fallback'

const props = defineProps<{
  rows: RoutingDefault[]
  taskType: string
  busyId?: number | null
}>()

const emit = defineEmits<{
  add: [payload: { tier: TierKey; canonical_model: string; profile: string; priority: number }]
  patch: [payload: { id: number; body: Record<string, unknown> }]
  detail: [row: RoutingDefault]
  remove: [row: RoutingDefault]
}>()

const { t } = useI18n()

const TIERS: TierKey[] = ['primary', 'secondary', 'fallback']
const PROFILES = [
  { key: '', labelKey: 'routingDefault.profiles.any' },
  { key: 'smart', labelKey: 'routingDefault.profiles.smart' },
  { key: 'speed_first', labelKey: 'routingDefault.profiles.speed_first' },
  { key: 'cost_first', labelKey: 'routingDefault.profiles.cost_first' },
]

const addingTier = ref<TierKey | null>(null)
const addModel = ref('')
const addProfile = ref('smart')
const addPriority = ref(100)
const addError = ref('')
const addSubmitting = ref(false)

const grouped = computed(() => {
  const map: Record<TierKey, RoutingDefault[]> = {
    primary: [],
    secondary: [],
    fallback: [],
  }
  for (const row of props.rows) {
    const tier = (row.tier || 'primary') as TierKey
    if (map[tier]) map[tier].push(row)
  }
  for (const tier of TIERS) {
    map[tier].sort((a, b) => b.priority - a.priority || a.id - b.id)
  }
  return map
})

function openAdd(tier: TierKey) {
  addingTier.value = tier
  addModel.value = ''
  addProfile.value = 'smart'
  addPriority.value = 100
  addError.value = ''
}

function cancelAdd() {
  addingTier.value = null
  addError.value = ''
}

async function submitAdd(tier: TierKey) {
  addError.value = ''
  if (!props.taskType) {
    addError.value = t('routingDefault.empty.needTask')
    return
  }
  if (!addModel.value.trim()) {
    addError.value = t('routingDefault.create.modelRequired')
    return
  }
  addSubmitting.value = true
  try {
    emit('add', {
      tier,
      canonical_model: addModel.value.trim(),
      profile: addProfile.value,
      priority: addPriority.value || 100,
    })
    cancelAdd()
  } finally {
    addSubmitting.value = false
  }
}

function patchField(row: RoutingDefault, body: Record<string, unknown>) {
  emit('patch', { id: row.id, body })
}

function scopeLabel(row: RoutingDefault) {
  return row.tenant_id ? row.tenant_id : t('routingDefault.scope.platform')
}

function toLocalInput(iso?: string) {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function onExpiresChange(row: RoutingDefault, value: string) {
  if (!value) {
    patchField(row, { clear_expires: true })
    return
  }
  patchField(row, { expires_at: new Date(value).toISOString() })
}

function onTenantChange(row: RoutingDefault, value: string) {
  const v = value.trim()
  if (!v) patchField(row, { clear_tenant: true })
  else patchField(row, { tenant_id: v })
}
</script>

<template>
  <div class="tier-groups">
    <section v-for="tier in TIERS" :key="tier" class="tier-group">
      <header class="tier-head">
        <h3>
          <span class="tier-badge" :class="'tier-' + tier">{{ t(`routingDefault.tiers.${tier}`) }}</span>
          <small>{{ grouped[tier].length }}</small>
        </h3>
        <button type="button" class="btn btn-sm" @click="openAdd(tier)">
          {{ t('routingDefault.actions.addModel') }}
        </button>
      </header>

      <div v-if="addingTier === tier" class="add-panel">
        <div class="add-title">{{ t('routingDefault.create.title', { tier: t(`routingDefault.tiers.${tier}`) }) }}</div>
        <div class="add-grid">
          <label>
            {{ t('routingDefault.fields.model') }}
            <ModelPicker v-model="addModel" :placeholder="t('routingDefault.fields.model')" />
          </label>
          <label>
            {{ t('routingDefault.fields.profile') }}
            <select v-model="addProfile">
              <option v-for="p in PROFILES" :key="p.key || 'any'" :value="p.key">{{ t(p.labelKey) }}</option>
            </select>
          </label>
          <label>
            {{ t('routingDefault.fields.priority') }}
            <input v-model.number="addPriority" type="number" />
          </label>
        </div>
        <p v-if="addError" class="error">{{ addError }}</p>
        <div class="add-actions">
          <button type="button" class="btn btn-primary btn-sm" :disabled="addSubmitting" @click="submitAdd(tier)">
            {{ addSubmitting ? t('routingDefault.create.submitting') : t('routingDefault.create.submit') }}
          </button>
          <button type="button" class="btn btn-sm" @click="cancelAdd">{{ t('routingDefault.actions.cancel') }}</button>
        </div>
      </div>

      <div v-if="grouped[tier].length === 0" class="empty">{{ t('routingDefault.empty.group') }}</div>
      <ul v-else class="row-list">
        <li v-for="row in grouped[tier]" :key="row.id" class="row-item" :class="{ busy: busyId === row.id }">
          <div class="row-main">
            <code class="model">{{ row.canonical_model }}</code>
            <div class="profile-seg">
              <button
                v-for="p in PROFILES"
                :key="p.key || 'any'"
                type="button"
                class="seg"
                :class="{ active: (row.profile || '') === p.key }"
                @click="patchField(row, { profile: p.key })"
              >{{ t(p.labelKey) }}</button>
            </div>
          </div>
          <div class="row-meta">
            <label>
              {{ t('routingDefault.fields.priority') }}
              <input
                type="number"
                :value="row.priority"
                @change="patchField(row, { priority: Number(($event.target as HTMLInputElement).value) || 100 })"
              />
            </label>
            <label>
              {{ t('routingDefault.fields.platform') }}
              <input
                type="text"
                :value="row.tenant_id || ''"
                :placeholder="t('routingDefault.scope.platform')"
                @change="onTenantChange(row, ($event.target as HTMLInputElement).value)"
              />
            </label>
            <label class="grow">
              {{ t('routingDefault.fields.reason') }}
              <input
                type="text"
                :value="row.reason || ''"
                @change="patchField(row, { reason: ($event.target as HTMLInputElement).value })"
              />
            </label>
            <label>
              {{ t('routingDefault.fields.expires') }}
              <input
                type="datetime-local"
                :value="toLocalInput(row.expires_at)"
                @change="onExpiresChange(row, ($event.target as HTMLInputElement).value)"
              />
            </label>
            <span class="scope-chip" :title="scopeLabel(row)">{{ scopeLabel(row) }}</span>
          </div>
          <div class="row-actions">
            <button type="button" class="btn btn-sm" @click="emit('detail', row)">{{ t('routingDefault.actions.detail') }}</button>
            <button type="button" class="btn btn-sm btn-danger" @click="emit('remove', row)">{{ t('routingDefault.actions.delete') }}</button>
          </div>
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.tier-groups {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.tier-group {
  border: 1px solid var(--border, #e5e7eb);
  border-radius: 10px;
  padding: 12px;
  background: var(--bg-card, #fff);
}
.tier-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 10px;
}
.tier-head h3 {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  font-size: 14px;
}
.tier-badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 6px;
  font-size: 12px;
  font-weight: 600;
}
.tier-primary { background: #dcfce7; color: #166534; }
.tier-secondary { background: #e0e7ff; color: #3730a3; }
.tier-fallback { background: #f3f4f6; color: #4b5563; }
.add-panel {
  margin-bottom: 12px;
  padding: 10px;
  border-radius: 8px;
  background: var(--bg-muted, #f9fafb);
}
.add-title { font-size: 13px; margin-bottom: 8px; font-weight: 600; }
.add-grid {
  display: grid;
  grid-template-columns: 2fr 1fr 100px;
  gap: 8px;
}
.add-grid label, .row-meta label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
  color: var(--text-muted, #6b7280);
}
.add-grid input, .add-grid select, .row-meta input {
  padding: 6px 8px;
  border: 1px solid var(--border, #d1d5db);
  border-radius: 6px;
  font-size: 13px;
}
.add-actions, .row-actions {
  display: flex;
  gap: 8px;
  margin-top: 8px;
}
.empty {
  color: var(--text-muted, #9ca3af);
  font-size: 13px;
  padding: 8px 0;
}
.row-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.row-item {
  border: 1px solid var(--border, #e5e7eb);
  border-radius: 8px;
  padding: 10px;
}
.row-item.busy { opacity: 0.6; pointer-events: none; }
.row-main {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
}
.model {
  font-size: 13px;
  font-weight: 600;
}
.profile-seg {
  display: inline-flex;
  border: 1px solid var(--border, #d1d5db);
  border-radius: 8px;
  overflow: hidden;
}
.seg {
  border: none;
  background: transparent;
  padding: 4px 10px;
  font-size: 12px;
  cursor: pointer;
}
.seg.active {
  background: var(--bg-accent-soft, #eef2ff);
  color: var(--text-accent, #3730a3);
  font-weight: 600;
}
.row-meta {
  display: grid;
  grid-template-columns: 90px 140px 1fr 170px auto;
  gap: 8px;
  align-items: end;
}
.row-meta .grow { min-width: 120px; }
.scope-chip {
  font-size: 11px;
  padding: 4px 8px;
  border-radius: 999px;
  background: var(--bg-muted, #f3f4f6);
  align-self: center;
}
.error { color: #b91c1c; font-size: 12px; margin: 6px 0 0; }
.btn-danger { color: #b91c1c; }
@media (max-width: 1100px) {
  .add-grid, .row-meta {
    grid-template-columns: 1fr 1fr;
  }
}
</style>
