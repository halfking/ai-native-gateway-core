<script setup lang="ts">
// RoutingDefaultsView.vue — M2 (22 章 §22.6): admin UI for task_default_routing.
//
// Lets operators pin a preferred/fallback model for a (task_type, profile,
// tenant_id) triple — the "explicit default routing" layer. Changes take
// effect within ~1 minute on the hot path (DefaultRoutingStore refresh).
//
// Priority order (documented in 22 §22.4):
//   override ban > override pin > EXPLICIT DEFAULT (this page) > implicit tag > fallback
//
// Sections:
//   1. Summary cards: total / primary / tenant-scoped / expiring
//   2. Filter bar: active-only, task_type, profile
//   3. Create form (collapsible)
//   4. Defaults table with delete
//   5. Audit drawer (last 500 mutations)

import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getRoutingDefaults,
  createRoutingDefault,
  deleteRoutingDefault,
  getRoutingDefaultsAudit,
  type RoutingDefault,
  type RoutingDefaultCreate,
  type RoutingDefaultAuditRow,
} from '../api/tuning'

const { t } = useI18n()

// ── List state ───────────────────────────────────────────────────
const defaults = ref<RoutingDefault[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const filterActive = ref(true)
const filterTaskType = ref('')
const filterProfile = ref('')

async function loadDefaults() {
  loading.value = true
  error.value = null
  try {
    const r = await getRoutingDefaults({
      active: filterActive.value,
      task_type: filterTaskType.value || undefined,
      profile: filterProfile.value || undefined,
    })
    defaults.value = r.defaults
  } catch (e: any) {
    error.value = e?.message ?? String(e)
  } finally {
    loading.value = false
  }
}

// ── Create form state ────────────────────────────────────────────
const showCreateForm = ref(false)
const createForm = ref<RoutingDefaultCreate>({
  task_type: '',
  profile: '', // '' = 通用（任意 profile）
  tier: 'primary',
  canonical_model: '',
  tenant_id: null,
  priority: 100,
  reason: '',
})
const createError = ref<string | null>(null)
const createSubmitting = ref(false)

async function submitCreate() {
  createError.value = null
  if (!createForm.value.task_type.trim()) {
    createError.value = t('routingDefault.create.errors.taskTypeRequired')
    return
  }
  if (!createForm.value.canonical_model.trim()) {
    createError.value = t('routingDefault.create.errors.modelRequired')
    return
  }
  createSubmitting.value = true
  try {
    await createRoutingDefault({
      task_type: createForm.value.task_type.trim(),
      profile: createForm.value.profile || '',
      tier: createForm.value.tier || 'primary',
      canonical_model: createForm.value.canonical_model.trim(),
      tenant_id: createForm.value.tenant_id || null,
      priority: createForm.value.priority ?? 100,
      reason: createForm.value.reason.trim(),
      expires_at: createForm.value.expires_at || undefined,
    })
    createForm.value = {
      task_type: '', profile: '', tier: 'primary',
      canonical_model: '', tenant_id: null, priority: 100, reason: '',
    }
    showCreateForm.value = false
    await loadDefaults()
  } catch (e: any) {
    createError.value = e?.message ?? String(e)
  } finally {
    createSubmitting.value = false
  }
}

// ── Delete ───────────────────────────────────────────────────────
async function deleteDefault(d: RoutingDefault) {
  if (!confirm(t('routingDefault.table.deleteConfirm', {
    id: d.id, model: d.canonical_model, task: d.task_type,
  }))) {
    return
  }
  try {
    await deleteRoutingDefault(d.id)
    await loadDefaults()
  } catch (e: any) {
    alert(t('routingDefault.table.deleteFailed') + (e?.message ?? e))
  }
}

// ── Audit drawer ─────────────────────────────────────────────────
const showAudit = ref(false)
const auditRows = ref<RoutingDefaultAuditRow[]>([])
const auditLoading = ref(false)

async function loadAudit() {
  auditLoading.value = true
  try {
    const r = await getRoutingDefaultsAudit()
    auditRows.value = r.audit
  } catch (e: any) {
    alert(e?.message ?? e)
  } finally {
    auditLoading.value = false
  }
}

function toggleAudit() {
  showAudit.value = !showAudit.value
  if (showAudit.value && auditRows.value.length === 0) {
    loadAudit()
  }
}

// ── Helpers ──────────────────────────────────────────────────────
function tierClass(tier: string): string {
  return 'tier-' + tier
}
function scopeLabel(d: RoutingDefault): string {
  if (d.tenant_id != null) return `tenant ${d.tenant_id}`
  return t('routingDefault.scope.platform')
}
function isExpired(d: RoutingDefault): boolean {
  if (!d.expires_at) return false
  return new Date(d.expires_at) < new Date()
}
function isExpiring(d: RoutingDefault): boolean {
  if (!d.expires_at) return false
  const daysLeft = (new Date(d.expires_at).getTime() - Date.now()) / 86400000
  return daysLeft > 0 && daysLeft < 7
}

const summary = computed(() => {
  const total = defaults.value.length
  const primary = defaults.value.filter(d => d.tier === 'primary').length
  const tenantScoped = defaults.value.filter(d => d.tenant_id != null).length
  const expiring = defaults.value.filter(isExpiring).length
  return { total, primary, tenantScoped, expiring }
})

onMounted(loadDefaults)
</script>

<template>
  <div class="defaults-view">
    <h1>{{ t('routingDefault.title') }}</h1>
    <p class="subtitle">{{ t('routingDefault.subtitle') }}</p>

    <!-- ── Summary cards ─────────────────────────────────── -->
    <div v-if="defaults.length > 0" class="summary-cards">
      <div class="summary-card">
        <div class="summary-label">{{ t('routingDefault.summary.total') }}</div>
        <div class="summary-value">{{ summary.total }}</div>
      </div>
      <div class="summary-card">
        <div class="summary-label">{{ t('routingDefault.summary.primary') }}</div>
        <div class="summary-value text-success">{{ summary.primary }}</div>
      </div>
      <div class="summary-card">
        <div class="summary-label">{{ t('routingDefault.summary.tenantScoped') }}</div>
        <div class="summary-value text-accent">{{ summary.tenantScoped }}</div>
      </div>
      <div class="summary-card">
        <div class="summary-label">{{ t('routingDefault.summary.expiring') }}</div>
        <div :class="['summary-value', summary.expiring > 0 ? 'text-warning' : 'text-muted']">
          {{ summary.expiring }}
        </div>
      </div>
    </div>

    <!-- ── Filter bar ────────────────────────────────────── -->
    <section class="card">
      <div class="filter-bar">
        <label>
          <input type="checkbox" v-model="filterActive" @change="loadDefaults" />
          {{ t('routingDefault.filter.activeOnly') }}
        </label>
        <label>{{ t('routingDefault.filter.taskType') }}:
          <input v-model="filterTaskType" :placeholder="t('routingDefault.filter.taskTypePlaceholder')"
                 @keyup.enter="loadDefaults" />
        </label>
        <label>{{ t('routingDefault.filter.profile') }}:
          <select v-model="filterProfile" @change="loadDefaults">
            <option value="">{{ t('routingDefault.filter.all') }}</option>
            <option value="smart">smart</option>
            <option value="speed_first">speed_first</option>
            <option value="cost_first">cost_first</option>
          </select>
        </label>
        <button @click="loadDefaults" :disabled="loading">
          {{ loading ? t('routingDefault.filter.loading') : t('routingDefault.filter.refresh') }}
        </button>
        <button @click="showCreateForm = !showCreateForm" class="btn-new">
          {{ showCreateForm ? t('routingDefault.filter.cancel') : t('routingDefault.filter.newDefault') }}
        </button>
        <button @click="toggleAudit" class="btn-audit">
          {{ t('routingDefault.filter.audit') }}
        </button>
      </div>
      <p v-if="error" class="error">⚠️ {{ error }}</p>
    </section>

    <!-- ── Create form ───────────────────────────────────── -->
    <section v-if="showCreateForm" class="card create-form">
      <h3>{{ t('routingDefault.create.title') }}</h3>
      <p class="hint">{{ t('routingDefault.create.hint') }}</p>
      <div class="form-grid">
        <label>{{ t('routingDefault.create.taskType') }} *
          <input v-model="createForm.task_type"
                 :placeholder="t('routingDefault.create.taskTypePlaceholder')" />
        </label>
        <label>{{ t('routingDefault.create.profile') }}
          <select v-model="createForm.profile">
            <option value="">({{ t('routingDefault.create.profileAny') }})</option>
            <option value="smart">smart</option>
            <option value="speed_first">speed_first</option>
            <option value="cost_first">cost_first</option>
          </select>
        </label>
        <label>{{ t('routingDefault.create.tier') }}
          <select v-model="createForm.tier">
            <option value="primary">primary</option>
            <option value="secondary">secondary</option>
            <option value="fallback">fallback</option>
          </select>
        </label>
        <label>{{ t('routingDefault.create.model') }} *
          <input v-model="createForm.canonical_model"
                 :placeholder="t('routingDefault.create.modelPlaceholder')" />
        </label>
        <label>{{ t('routingDefault.create.tenantId') }}
          <input type="number" v-model.number="createForm.tenant_id"
                 :placeholder="t('routingDefault.create.tenantIdPlaceholder')" />
        </label>
        <label>{{ t('routingDefault.create.priority') }}
          <input type="number" v-model.number="createForm.priority" />
        </label>
        <label class="full-row">{{ t('routingDefault.create.reason') }}
          <input v-model="createForm.reason" />
        </label>
        <label class="full-row">{{ t('routingDefault.create.expiresAt') }}
          <input type="datetime-local" v-model="createForm.expires_at" />
        </label>
      </div>
      <p v-if="createError" class="error">⚠️ {{ createError }}</p>
      <div class="form-actions">
        <button @click="submitCreate" :disabled="createSubmitting" class="btn-primary">
          {{ createSubmitting ? t('routingDefault.create.submitting') : t('routingDefault.create.submit') }}
        </button>
        <button @click="showCreateForm = false">{{ t('routingDefault.filter.cancel') }}</button>
      </div>
    </section>

    <!-- ── Defaults table ────────────────────────────────── -->
    <section class="card">
      <table v-if="defaults.length > 0" class="defaults-table">
        <thead>
          <tr>
            <th>ID</th>
            <th>{{ t('routingDefault.table.taskType') }}</th>
            <th>{{ t('routingDefault.table.profile') }}</th>
            <th>{{ t('routingDefault.table.tier') }}</th>
            <th>{{ t('routingDefault.table.model') }}</th>
            <th>{{ t('routingDefault.table.scope') }}</th>
            <th>{{ t('routingDefault.table.priority') }}</th>
            <th>{{ t('routingDefault.table.reason') }}</th>
            <th>{{ t('routingDefault.table.expires') }}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="d in defaults" :key="d.id" :class="{ expired: isExpired(d) }">
            <td>{{ d.id }}</td>
            <td><code>{{ d.task_type }}</code></td>
            <td>{{ d.profile || '—' }}</td>
            <td><span class="badge" :class="tierClass(d.tier)">{{ d.tier }}</span></td>
            <td><code>{{ d.canonical_model }}</code></td>
            <td>{{ scopeLabel(d) }}</td>
            <td>{{ d.priority }}</td>
            <td class="reason-cell" :title="d.reason">{{ d.reason || '—' }}</td>
            <td>
              <span v-if="!d.expires_at">—</span>
              <span v-else-if="isExpired(d)" class="expired-tag">
                {{ t('routingDefault.table.expired') }}
              </span>
              <span v-else>{{ d.expires_at.substring(0, 10) }}</span>
            </td>
            <td>
              <button @click="deleteDefault(d)" class="btn-delete">✕</button>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-else-if="!loading" class="empty">{{ t('routingDefault.table.empty') }}</p>
    </section>

    <!-- ── Audit drawer ──────────────────────────────────── -->
    <section v-if="showAudit" class="card audit-section">
      <h3>{{ t('routingDefault.audit.title') }}
        <button @click="loadAudit" :disabled="auditLoading" class="btn-small">
          {{ t('routingDefault.audit.refresh') }}
        </button>
      </h3>
      <table v-if="auditRows.length > 0" class="audit-table">
        <thead>
          <tr>
            <th>{{ t('routingDefault.audit.ts') }}</th>
            <th>{{ t('routingDefault.audit.action') }}</th>
            <th>{{ t('routingDefault.audit.routingId') }}</th>
            <th>{{ t('routingDefault.audit.taskType') }}</th>
            <th>{{ t('routingDefault.audit.model') }}</th>
            <th>{{ t('routingDefault.audit.actor') }}</th>
            <th>{{ t('routingDefault.audit.reason') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="a in auditRows" :key="a.id">
            <td>{{ a.ts }}</td>
            <td><span class="badge" :class="'audit-' + a.action">{{ a.action }}</span></td>
            <td>{{ a.routing_id ?? '—' }}</td>
            <td><code>{{ a.task_type ?? '—' }}</code></td>
            <td><code>{{ a.canonical_model ?? '—' }}</code></td>
            <td>{{ a.actor ?? '—' }}</td>
            <td>{{ a.reason ?? '—' }}</td>
          </tr>
        </tbody>
      </table>
      <p v-else-if="!auditLoading" class="empty">{{ t('routingDefault.audit.empty') }}</p>
    </section>
  </div>
</template>

<style scoped>
/* M2 (22 章 §22.6): routing/defaults — dark theme aligned with sibling
 * RoutingAuditView / RoutingOverrideView. No more "大块的设色背景":
 * summary cards use a tight grid (not full-width flex), cards use the dark
 * `--card` / `--border` tokens, and value colors map to the global
 * success / accent / warning / muted tokens via utility classes. */
.defaults-view {
  max-width: 1400px;
  margin: 0 auto;
  padding: 24px;
  color: var(--text, #e6edf3);
}
.defaults-view h1 { margin: 0 0 8px; font-size: 24px; }
.subtitle { color: var(--muted, #8b949e); margin: 0 0 16px; font-size: 14px; }

.summary-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
.summary-card {
  background: var(--card, #1c2128);
  border: 1px solid var(--border, #30363d);
  border-radius: var(--radius, 8px);
  padding: 12px 16px;
}
.summary-label {
  font-size: 11px;
  color: var(--muted, #8b949e);
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.summary-value {
  font-size: 24px;
  font-weight: 600;
  margin-top: 4px;
}
.text-success { color: var(--success, #3fb950); }
.text-accent  { color: var(--accent-h, #818cf8); }
.text-warning { color: var(--warning, #d29922); }
.text-muted   { color: var(--muted, #8b949e); }

.card {
  background: var(--card, #1c2128);
  border: 1px solid var(--border, #30363d);
  border-radius: var(--radius, 8px);
  padding: 16px;
  margin-bottom: 16px;
  color: var(--text, #e6edf3);
}
.card h3 {
  margin: 0 0 12px;
  font-size: 14px;
  font-weight: 600;
}

.filter-bar {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: center;
}
.filter-bar label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--muted, #8b949e);
}
.filter-bar input,
.filter-bar select {
  width: auto;
  padding: 4px 8px;
  font-size: 12px;
}
.create-form .hint {
  color: var(--muted, #8b949e);
  font-size: 12px;
  margin: 0 0 12px;
}
.form-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 12px;
}
@media (max-width: 720px) {
  .form-grid { grid-template-columns: 1fr; }
}
.form-grid label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
  color: var(--muted, #8b949e);
}
.form-grid .full-row { grid-column: 1 / -1; }
.form-actions {
  display: flex;
  gap: 8px;
  margin-top: 16px;
}
.error {
  color: var(--danger, #f85149);
  font-size: 13px;
  margin: 8px 0 0;
}
.empty {
  text-align: center;
  padding: 32px 16px;
  color: var(--muted, #8b949e);
}

button {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 14px;
  border-radius: var(--radius, 8px);
  border: 1px solid var(--border, #30363d);
  background: var(--card, #1c2128);
  color: var(--text, #e6edf3);
  font-size: 13px;
  cursor: pointer;
  transition: opacity .15s;
}
button:hover:not(:disabled) { opacity: .85; }
button:disabled { opacity: .4; cursor: not-allowed; }
.btn-new,
.btn-primary {
  background: var(--accent, #6366f1);
  border-color: var(--accent, #6366f1);
  color: #fff;
}
.btn-audit {
  background: var(--bg-subtle, #161b22);
  color: var(--text, #e6edf3);
}
.btn-delete {
  background: rgba(248, 81, 73, 0.12);
  border-color: rgba(248, 81, 73, 0.4);
  color: var(--danger, #f85149);
  padding: 4px 8px;
}
.btn-small { font-size: 12px; padding: 4px 10px; }

.defaults-table,
.audit-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.defaults-table th,
.defaults-table td,
.audit-table th,
.audit-table td {
  padding: 8px 12px;
  text-align: left;
  border-bottom: 1px solid var(--border, #30363d);
}
.defaults-table th,
.audit-table th {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--muted, #8b949e);
}
.defaults-table tbody tr:hover,
.audit-table tbody tr:hover {
  background: rgba(255, 255, 255, 0.02);
}
.reason-cell {
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
}
.tier-primary   { background: rgba(63,185,80,0.15);  color: var(--success, #3fb950); }
.tier-secondary { background: rgba(99,102,241,0.15); color: var(--accent-h, #818cf8); }
.tier-fallback  { background: rgba(210,153,34,0.15); color: var(--warning, #d29922); }
.audit-insert   { background: rgba(63,185,80,0.15);  color: var(--success, #3fb950); }
.audit-update   { background: rgba(99,102,241,0.15); color: var(--accent-h, #818cf8); }
.audit-delete   { background: rgba(248,81,73,0.15);  color: var(--danger, #f85149); }
.expired { opacity: 0.55; }
.expired-tag {
  color: var(--danger, #f85149);
  font-weight: 600;
  font-size: 12px;
}
code {
  background: var(--bg-subtle, #161b22);
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 12px;
  color: var(--text, #e6edf3);
}
.audit-section h3 {
  display: flex;
  align-items: center;
  gap: 12px;
}
</style>
