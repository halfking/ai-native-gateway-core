<script setup lang="ts">
// RoutingOverrideView.vue — P7.8: admin UI for routing overrides.
//
// Lets operators pin or ban a model for a (task_type, profile) pair
// based on insights from the P7.2 correlation dashboard. Changes
// take effect within 1 minute on the hot path (OverrideStore refresh).
//
// Four sections:
//   1. Filter bar: active-only, task_type, profile
//   2. List of overrides with mode badge (pin/ban) + actions
//   3. Create form (collapsible)
//   4. Extend modal (per-row)

import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getRoutingOverrides,
  createRoutingOverride,
  deleteRoutingOverride,
  extendRoutingOverride,
  type RoutingOverride,
  type RoutingOverrideCreate,
} from '../api'

const { t } = useI18n()

// ── List state ───────────────────────────────────────────────────
const overrides = ref<RoutingOverride[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const filterActive = ref(true)
const filterTaskType = ref('')
const filterProfile = ref('')

async function loadOverrides() {
  loading.value = true
  error.value = null
  try {
    const r = await getRoutingOverrides({
      active: filterActive.value,
      task_type: filterTaskType.value || undefined,
      profile: filterProfile.value || undefined,
    })
    overrides.value = r.overrides
  } catch (e: any) {
    error.value = e?.message ?? String(e)
  } finally {
    loading.value = false
  }
}

// ── Create form state ────────────────────────────────────────────
const showCreateForm = ref(false)
const createForm = ref<RoutingOverrideCreate>({
  task_type: '',
  profile: 'smart',
  mode: 'ban',
  model_chosen: '',
  reason: '',
})
const createError = ref<string | null>(null)
const createSubmitting = ref(false)

async function submitCreate() {
  createError.value = null
  if (!createForm.value.task_type.trim()) {
    createError.value = t('routingOverride.create.errors.taskTypeRequired')
    return
  }
  if (!createForm.value.reason.trim()) {
    createError.value = t('routingOverride.create.errors.reasonRequired')
    return
  }
  if (createForm.value.mode === 'ban' && !createForm.value.model_chosen?.trim()) {
    createError.value = t('routingOverride.create.errors.banRequiresModel')
    return
  }

  createSubmitting.value = true
  try {
    await createRoutingOverride({
      task_type: createForm.value.task_type.trim(),
      profile: createForm.value.profile || 'smart',
      mode: createForm.value.mode,
      model_chosen: createForm.value.model_chosen?.trim() || undefined,
      reason: createForm.value.reason.trim(),
      expires_at: createForm.value.expires_at || undefined,
    })
    // Reset form + reload list
    createForm.value = { task_type: '', profile: 'smart', mode: 'ban', model_chosen: '', reason: '' }
    showCreateForm.value = false
    await loadOverrides()
  } catch (e: any) {
    createError.value = e?.message ?? String(e)
  } finally {
    createSubmitting.value = false
  }
}

// ── Delete action ───────────────────────────────────────────────
async function deleteOverride(o: RoutingOverride) {
  if (!confirm(t('routingOverride.table.deleteConfirm', {
    id: o.id,
    mode: o.mode,
    model: o.model_chosen ?? '*',
    task: o.task_type,
  }))) {
    return
  }
  try {
    await deleteRoutingOverride(o.id)
    await loadOverrides()
  } catch (e: any) {
    alert(t('routingOverride.table.deleteFailed') + (e?.message ?? e))
  }
}

// ── Extend modal ────────────────────────────────────────────────
const extendId = ref<number | null>(null)
const extendDate = ref('')
const extendError = ref<string | null>(null)

function openExtend(o: RoutingOverride) {
  extendId.value = o.id
  // Default to 1 day from now
  const d = new Date()
  d.setDate(d.getDate() + 1)
  extendDate.value = d.toISOString().slice(0, 16) // YYYY-MM-DDTHH:mm
  extendError.value = null
}

function cancelExtend() {
  extendId.value = null
  extendDate.value = ''
  extendError.value = null
}

async function confirmExtend() {
  if (extendId.value === null) return
  if (!extendDate.value) {
    extendError.value = 'pick a future date'
    return
  }
  try {
    // Convert datetime-local to RFC3339
    const iso = new Date(extendDate.value).toISOString()
    await extendRoutingOverride(extendId.value, iso)
    cancelExtend()
    await loadOverrides()
  } catch (e: any) {
    extendError.value = e?.message ?? String(e)
  }
}

// ── Helpers ─────────────────────────────────────────────────────
function modeClass(mode: string): string {
  return mode === 'ban' ? 'mode-ban' : 'mode-pin'
}

function isExpired(o: RoutingOverride): boolean {
  if (!o.expires_at) return false
  return new Date(o.expires_at) < new Date()
}

function isExpiring(o: RoutingOverride): boolean {
  if (!o.expires_at) return false
  const d = new Date(o.expires_at)
  const now = new Date()
  const daysLeft = (d.getTime() - now.getTime()) / 86400000
  return daysLeft > 0 && daysLeft < 7
}

const summary = computed(() => {
  const total = overrides.value.length
  const bans = overrides.value.filter(o => o.mode === 'ban').length
  const pins = overrides.value.filter(o => o.mode === 'pin').length
  const expiring = overrides.value.filter(isExpiring).length
  return { total, bans, pins, expiring }
})

onMounted(loadOverrides)
</script>

<template>
  <div class="overrides-view">
    <h1>{{ t('routingOverride.title') }}</h1>
    <p class="subtitle">
      {{ t('routingOverride.subtitle') }}
      <router-link to="/correlations">{{ t('routingOverride.correlationsLink') }}</router-link>.
    </p>

    <!-- ── Summary cards ─────────────────────────────────── -->
    <div v-if="overrides.length > 0" class="summary-cards">
      <div class="summary-card">
        <div class="summary-label">{{ t('routingOverride.summary.total') }}</div>
        <div class="summary-value">{{ summary.total }}</div>
      </div>
      <div class="summary-card">
        <div class="summary-label">{{ t('routingOverride.summary.bans') }}</div>
        <div class="summary-value" style="color: #f97316">{{ summary.bans }}</div>
      </div>
      <div class="summary-card">
        <div class="summary-label">{{ t('routingOverride.summary.pins') }}</div>
        <div class="summary-value" style="color: #22c55e">{{ summary.pins }}</div>
      </div>
      <div class="summary-card">
        <div class="summary-label">{{ t('routingOverride.summary.expiring') }}</div>
        <div class="summary-value" :style="{ color: summary.expiring > 0 ? '#eab308' : '#888' }">
          {{ summary.expiring }}
        </div>
      </div>
    </div>

    <!-- ── Filter bar ────────────────────────────────────── -->
    <section class="card">
      <div class="filter-bar">
        <label>
          <input type="checkbox" v-model="filterActive" @change="loadOverrides" />
          {{ t('routingOverride.filter.activeOnly') }}
        </label>
        <label>{{ t('routingOverride.filter.taskType') }}:
          <input v-model="filterTaskType" :placeholder="t('routingOverride.filter.taskTypePlaceholder')"
                 @keyup.enter="loadOverrides" />
        </label>
        <label>{{ t('routingOverride.filter.profile') }}:
          <select v-model="filterProfile" @change="loadOverrides">
            <option value="">{{ t('routingOverride.filter.all') }}</option>
            <option value="smart">smart</option>
            <option value="speed_first">speed_first</option>
            <option value="cost_first">cost_first</option>
          </select>
        </label>
        <button @click="loadOverrides" :disabled="loading">
          {{ loading ? t('routingOverride.filter.loading') : t('routingOverride.filter.refresh') }}
        </button>
        <button @click="showCreateForm = !showCreateForm" class="btn-new">
          {{ showCreateForm ? t('routingOverride.filter.cancel') : t('routingOverride.filter.newOverride') }}
        </button>
      </div>

      <p v-if="error" class="error">⚠️ {{ error }}</p>
    </section>

    <!-- ── Create form ───────────────────────────────────── -->
    <section v-if="showCreateForm" class="card create-form">
      <h2>{{ t('routingOverride.create.title') }}</h2>
      <p class="hint">
        {{ t('routingOverride.create.hintPin') }}<br>
        {{ t('routingOverride.create.hintBan') }}
      </p>

      <div class="form-grid">
        <label>Task type *
          <input v-model="createForm.task_type" placeholder="e.g. code, reasoning" />
        </label>
        <label>Profile
          <select v-model="createForm.profile">
            <option value="smart">smart</option>
            <option value="speed_first">speed_first</option>
            <option value="cost_first">cost_first</option>
          </select>
        </label>
        <label>Mode *
          <select v-model="createForm.mode">
            <option value="ban">ban</option>
            <option value="pin">pin</option>
          </select>
        </label>
        <label>Model (required for ban)
          <input v-model="createForm.model_chosen" placeholder="e.g. gpt-4o, claude-3-5-sonnet" />
        </label>
        <label>Expires at (optional)
          <input v-model="createForm.expires_at" type="datetime-local" />
        </label>
        <label class="full-width">Reason * (audit trail)
          <input v-model="createForm.reason" placeholder="e.g. P7.2 correlation shows 45% success on reasoning" />
        </label>
      </div>

      <p v-if="createError" class="error">⚠️ {{ createError }}</p>

      <div class="form-actions">
        <button @click="submitCreate" :disabled="createSubmitting" class="btn-create">
          {{ createSubmitting ? 'Creating…' : 'Create override' }}
        </button>
        <button @click="showCreateForm = false">Cancel</button>
      </div>
    </section>

    <!-- ── Extend modal ──────────────────────────────────── -->
    <div v-if="extendId !== null" class="modal-overlay" @click.self="cancelExtend">
      <div class="modal">
        <h3>Extend override #{{ extendId }}</h3>
        <p class="hint">Pick a new expires_at. Pass empty date to make it permanent.</p>
        <label>New expires_at:
          <input v-model="extendDate" type="datetime-local" />
        </label>
        <p v-if="extendError" class="error">⚠️ {{ extendError }}</p>
        <div class="form-actions">
          <button @click="confirmExtend" class="btn-create">Save</button>
          <button @click="cancelExtend">Cancel</button>
        </div>
      </div>
    </div>

    <!-- ── Overrides table ──────────────────────────────── -->
    <section class="card">
      <h2>{{ t('routingOverride.table.title', { n: overrides.length }) }}</h2>
      <p v-if="!loading && overrides.length === 0" class="empty">
        {{ t('routingOverride.table.empty') }}
      </p>

      <table v-else class="overrides-table">
        <thead>
          <tr>
            <th>ID</th>
            <th>Mode</th>
            <th>Task type</th>
            <th>Profile</th>
            <th>Model</th>
            <th>Reason</th>
            <th>Expires</th>
            <th>Created by</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="o in overrides" :key="o.id" :class="{ 'row-expired': isExpired(o), 'row-expiring': isExpiring(o) }">
            <td>{{ o.id }}</td>
            <td><span :class="['mode-badge', modeClass(o.mode)]">{{ o.mode }}</span></td>
            <td><span class="tag tag-task">{{ o.task_type }}</span></td>
            <td>{{ o.profile }}</td>
            <td>
              <span v-if="o.model_chosen" class="tag tag-model">{{ o.model_chosen }}</span>
              <span v-else class="text-muted">any</span>
            </td>
            <td class="reason">{{ o.reason }}</td>
            <td>
              <span v-if="o.expires_at" :class="{ 'text-warn': isExpiring(o) }">
                {{ new Date(o.expires_at).toLocaleString() }}
              </span>
              <span v-else class="text-muted">permanent</span>
            </td>
            <td>{{ o.created_by ?? '—' }}</td>
            <td class="actions">
              <button @click="openExtend(o)" class="btn-extend">Extend</button>
              <button @click="deleteOverride(o)" class="btn-delete">Delete</button>
            </td>
          </tr>
        </tbody>
      </table>
    </section>
  </div>
</template>

<style scoped>
.overrides-view {
  padding: 24px;
  max-width: 1400px;
  margin: 0 auto;
  color: var(--text, #e6e6e6);
}
h1 {
  margin: 0 0 8px;
  font-size: 24px;
}
h2 {
  margin: 0 0 12px;
  font-size: 18px;
  border-bottom: 1px solid var(--border, #2a2a2a);
  padding-bottom: 8px;
}
.subtitle {
  margin: 0 0 24px;
  color: #888;
  font-size: 14px;
}
.subtitle a {
  color: #93c5fd;
  text-decoration: none;
}
.subtitle a:hover {
  text-decoration: underline;
}
.summary-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
.summary-card {
  background: #0e0e0e;
  border: 1px solid #2a2a2a;
  border-radius: 6px;
  padding: 12px 16px;
}
.summary-label {
  font-size: 11px;
  color: #888;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.summary-value {
  font-size: 24px;
  font-weight: 600;
  margin-top: 4px;
}
.card {
  background: var(--card-bg, #1a1a1a);
  border: 1px solid var(--border, #2a2a2a);
  border-radius: 8px;
  padding: 20px;
  margin-bottom: 16px;
}
.filter-bar {
  display: flex;
  gap: 16px;
  align-items: center;
  flex-wrap: wrap;
}
.filter-bar label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: #aaa;
}
.filter-bar input[type="text"],
.filter-bar input:not([type]),
.filter-bar select {
  padding: 4px 8px;
  background: #0e0e0e;
  border: 1px solid #2a2a2a;
  color: inherit;
  border-radius: 4px;
  font-size: 13px;
  min-width: 140px;
}
.filter-bar button {
  padding: 6px 14px;
  background: #2563eb;
  color: #fff;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 13px;
}
.filter-bar button:disabled { opacity: 0.5; cursor: not-allowed; }
.filter-bar button.btn-new {
  background: #16a34a;
}
.error {
  color: #ef4444;
  font-size: 13px;
  margin-top: 8px;
}
.empty {
  color: #888;
  font-size: 13px;
  font-style: italic;
}
.hint {
  color: #888;
  font-size: 13px;
  margin: 0 0 12px;
}
.create-form .form-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 12px 16px;
  margin-bottom: 16px;
}
.create-form .form-grid label {
  display: flex;
  flex-direction: column;
  font-size: 12px;
  color: #aaa;
  gap: 4px;
}
.create-form .form-grid label.full-width {
  grid-column: 1 / -1;
}
.create-form .form-grid input,
.create-form .form-grid select {
  padding: 6px 10px;
  background: #0e0e0e;
  border: 1px solid #2a2a2a;
  color: inherit;
  border-radius: 4px;
  font-size: 13px;
}
.create-form .form-actions {
  display: flex;
  gap: 8px;
}
.create-form .form-actions .btn-create {
  padding: 8px 18px;
  background: #16a34a;
  color: #fff;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 13px;
}
.create-form .form-actions button:not(.btn-create) {
  padding: 8px 18px;
  background: #2a2a2a;
  color: #fff;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 13px;
}
.modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}
.modal {
  background: #1a1a1a;
  border: 1px solid #2a2a2a;
  border-radius: 8px;
  padding: 20px 24px;
  min-width: 360px;
  max-width: 500px;
}
.modal h3 {
  margin: 0 0 12px;
  font-size: 16px;
}
.modal label {
  display: flex;
  flex-direction: column;
  font-size: 12px;
  color: #aaa;
  gap: 4px;
  margin-bottom: 8px;
}
.modal input {
  padding: 6px 10px;
  background: #0e0e0e;
  border: 1px solid #2a2a2a;
  color: inherit;
  border-radius: 4px;
  font-size: 13px;
}
.overrides-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.overrides-table th {
  text-align: left;
  padding: 8px 10px;
  background: #0e0e0e;
  border-bottom: 1px solid #2a2a2a;
  color: #aaa;
  font-weight: 500;
}
.overrides-table td {
  padding: 8px 10px;
  border-bottom: 1px solid #1f1f1f;
  vertical-align: top;
}
.overrides-table tr.row-expired {
  opacity: 0.5;
  text-decoration: line-through;
}
.overrides-table tr.row-expiring {
  background: rgba(234, 179, 8, 0.05);
}
.mode-badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 3px;
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.mode-pin {
  background: #14532d;
  color: #86efac;
}
.mode-ban {
  background: #422006;
  color: #fb923c;
}
.tag {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 11px;
  padding: 2px 6px;
  border-radius: 3px;
  display: inline-block;
}
.tag-model {
  background: #1e293b;
  color: #93c5fd;
}
.tag-task {
  background: #14532d;
  color: #86efac;
}
.text-muted {
  color: #888;
  font-size: 12px;
}
.text-warn {
  color: #eab308;
  font-weight: 600;
}
.reason {
  color: #ccc;
  font-size: 12px;
  max-width: 360px;
  word-break: break-word;
}
.actions {
  display: flex;
  gap: 6px;
  white-space: nowrap;
}
.btn-extend,
.btn-delete {
  padding: 4px 10px;
  border: none;
  border-radius: 3px;
  cursor: pointer;
  font-size: 11px;
}
.btn-extend {
  background: #1e40af;
  color: #fff;
}
.btn-delete {
  background: #6b7280;
  color: #fff;
}
</style>
