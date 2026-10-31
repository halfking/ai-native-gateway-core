<script setup lang="ts">
// TenantModelPolicyPanel.vue — Round 48 (2026-06-21)
//
// Per-tenant model denylist management UI. Mounted inside
// TenantDetailView's "model-policies" tab. Lists current denials,
// allows super_admin to add / patch reason / soft-delete / undelete.
//
// Design choices:
//   - Uses /api/admin/tenants/{code}/model-policies/check to validate
//     the input exists in models_canonical before submit (prevent typos).
//   - Auto-include soft-deleted rows behind a checkbox.
//   - Cache-invalidation is server-side (admin handler calls
//     modelPolicy.Invalidate immediately after the write commits);
//     no client-side cache to manage.
//   - Audit log is shown inline below the table.

import { ref, onMounted } from 'vue'
import { surfaceError } from '../i18n/errorSurface'
import { localeRef } from '../i18n'
import { confirmDialog } from '../composables/useConfirmDialog'
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '../utils/datetime'
import {
  listTenantModelPolicies,
  createTenantModelPolicy,
  deleteTenantModelPolicy,
  undeleteTenantModelPolicy,
  checkTenantModelPolicy,
  listTenantModelPoliciesAudit,
} from '../api'
import type {
  TenantModelPolicy,
  TenantModelPolicyAuditEntry,
  TenantModelPolicyCheckResp,
} from '../api'

const props = defineProps<{ tenantCode: string }>()

const { t } = useI18n()

const policies = ref<TenantModelPolicy[]>([])
const audit = ref<TenantModelPolicyAuditEntry[]>([])
const includeDeleted = ref(false)
const loading = ref(false)
const error = ref('')

const showAddDialog = ref(false)
const addCanonical = ref('')
const addReason = ref('')
const addCheckResult = ref<TenantModelPolicyCheckResp | null>(null)
const addCheckError = ref('')
const submitting = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [p, a] = await Promise.all([
      listTenantModelPolicies(props.tenantCode, { includeDeleted: includeDeleted.value }),
      listTenantModelPoliciesAudit(props.tenantCode, 50),
    ])
    policies.value = p.policies
    audit.value = a.audit
  } catch (e: unknown) {
    error.value = surfaceError(e, { key: 'tenantModelPolicyPanel.error.loadFailed' })
  } finally {
    loading.value = false
  }
}

async function runCheck() {
  addCheckError.value = ''
  addCheckResult.value = null
  const name = addCanonical.value.trim()
  if (!name) return
  try {
    addCheckResult.value = await checkTenantModelPolicy(props.tenantCode, { canonical_name: name })
  } catch (e: unknown) {
    addCheckError.value = surfaceError(e, { key: 'tenantModelPolicyPanel.error.checkFailed' })
  }
}

async function submitAdd() {
  const name = addCanonical.value.trim()
  const reason = addReason.value.trim()
  if (!name) {
    addCheckError.value = t('tenantModelPolicyPanel.error.canonicalNameRequired')
    return
  }
  submitting.value = true
  addCheckError.value = ''
  try {
    await createTenantModelPolicy(props.tenantCode, { canonical_name: name, reason })
    showAddDialog.value = false
    addCanonical.value = ''
    addReason.value = ''
    addCheckResult.value = null
    await load()
  } catch (e: unknown) {
    addCheckError.value = surfaceError(e, { key: 'tenantModelPolicyPanel.error.createFailed' })
  } finally {
    submitting.value = false
  }
}

async function softDelete(p: TenantModelPolicy) {
  if (!(await confirmDialog(t('tenantModelPolicyPanel.confirm.softDelete', { name: p.canonical_name })))) return
  try {
    await deleteTenantModelPolicy(props.tenantCode, p.id)
    await load()
  } catch (e: unknown) {
    error.value = surfaceError(e, { key: 'tenantModelPolicyPanel.error.deleteFailed' })
  }
}

async function restore(p: TenantModelPolicy) {
  try {
    await undeleteTenantModelPolicy(props.tenantCode, p.id)
    await load()
  } catch (e: unknown) {
    error.value = surfaceError(e, { key: 'tenantModelPolicyPanel.error.restoreFailed' })
  }
}

function fmtTime(s: string | null) {
  return formatDateTime(s, { locale: localeRef.value, empty: '-' })
}

function actionLabel(a: string) {
  switch (a) {
    case 'insert': return t('tenantModelPolicyPanel.audit.actionLabels.insert')
    case 'update': return t('tenantModelPolicyPanel.audit.actionLabels.update')
    case 'delete': return t('tenantModelPolicyPanel.audit.actionLabels.delete')
    case 'undelete': return t('tenantModelPolicyPanel.audit.actionLabels.undelete')
    default: return a
  }
}

onMounted(load)
</script>

<template>
  <div class="model-policy-panel">
    <div class="panel-header">
      <div>
        <h3>{{ t('tenantModelPolicyPanel.title') }} (Tenant: {{ tenantCode }})</h3>
        <p class="hint">
          {{ t('tenantModelPolicyPanel.hint') }}
        </p>
      </div>
      <div class="actions">
        <label class="cb">
          <input type="checkbox" v-model="includeDeleted" @change="load" />
          {{ t('tenantModelPolicyPanel.showDeleted') }}
        </label>
        <button class="btn btn-primary" @click="showAddDialog = true">{{ t('tenantModelPolicyPanel.addButton') }}</button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <div v-if="loading" class="loading">{{ t('tenantModelPolicyPanel.loading') }}</div>

    <div v-else style="overflow-x:auto">
      <table class="table" style="width:100%">

        <thead>
          <tr>
            <th>{{ t('tenantModelPolicyPanel.table.canonicalName') }}</th>
            <th>{{ t('tenantModelPolicyPanel.table.reason') }}</th>
            <th>{{ t('tenantModelPolicyPanel.table.createdBy') }}</th>
            <th>{{ t('tenantModelPolicyPanel.table.createdAt') }}</th>
            <th>{{ t('tenantModelPolicyPanel.table.deletedAt') }}</th>
            <th>{{ t('tenantModelPolicyPanel.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in policies" :key="p.id" :class="{ 'is-deleted': p.deleted_at }">
            <td><code>{{ p.canonical_name }}</code></td>
            <td>{{ p.reason || '-' }}</td>
            <td>{{ p.created_by || '-' }}</td>
            <td class="mono">{{ fmtTime(p.created_at) }}</td>
            <td class="mono">{{ fmtTime(p.deleted_at) }}</td>
            <td>
              <button v-if="!p.deleted_at" class="btn btn-sm btn-danger" @click="softDelete(p)">{{ t('tenantModelPolicyPanel.actions.softDelete') }}</button>
              <button v-else class="btn btn-sm" @click="restore(p)">{{ t('tenantModelPolicyPanel.actions.restore') }}</button>
            </td>
          </tr>
          <tr v-if="policies.length === 0">
            <td colspan="6" style="text-align:center; color: var(--muted); padding: 24px">
              {{ t('tenantModelPolicyPanel.empty') }}
            </td>
          </tr>
        </tbody>
    
      </table>
    </div>

    <details class="audit-section" v-if="audit.length > 0">
      <summary>{{ t('tenantModelPolicyPanel.audit.title') }} ({{ t('tenantModelPolicyPanel.audit.recent', { count: audit.length }) }})</summary>
      <div style="overflow-x:auto">
        <table class="table" style="width:100%; margin-top: 8px">

          <thead>
            <tr>
              <th>{{ t('tenantModelPolicyPanel.audit.headers.ts') }}</th>
              <th>{{ t('tenantModelPolicyPanel.audit.headers.action') }}</th>
              <th>{{ t('tenantModelPolicyPanel.audit.headers.canonicalName') }}</th>
              <th>{{ t('tenantModelPolicyPanel.audit.headers.actor') }}</th>
              <th>{{ t('tenantModelPolicyPanel.audit.headers.reason') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="a in audit" :key="a.id">
              <td class="mono">{{ fmtTime(a.ts) }}</td>
              <td>{{ actionLabel(a.action) }}</td>
              <td><code>{{ a.canonical_name }}</code></td>
              <td>{{ a.actor }}</td>
              <td>{{ a.reason || '-' }}</td>
            </tr>
          </tbody>
      
        </table>
      </div>
    </details>

    <!-- Add dialog -->
    <div v-if="showAddDialog" class="modal-overlay" @click.self="showAddDialog = false">
      <div class="modal">
        <h3>{{ t('tenantModelPolicyPanel.dialog.title') }}</h3>
        <p class="hint">{{ t('tenantModelPolicyPanel.dialog.hint') }}</p>
        <div class="form-row">
          <label>{{ t('tenantModelPolicyPanel.dialog.canonicalName') }}</label>
          <input v-model="addCanonical" :placeholder="t('tenantModelPolicyPanel.dialog.canonicalNamePlaceholder')" @blur="runCheck" />
          <button class="btn btn-sm" @click="runCheck" :disabled="!addCanonical">{{ t('tenantModelPolicyPanel.dialog.checkButton') }}</button>
        </div>
        <div v-if="addCheckResult" class="check-result">
          {{ addCheckResult.exists
            ? t('tenantModelPolicyPanel.dialog.checkSuccess', {
                family: addCheckResult.family || '?',
                modality: addCheckResult.modality || '?',
              })
            : t('tenantModelPolicyPanel.dialog.checkWarning') }}
        </div>
        <div v-if="addCheckError" class="alert alert-danger">{{ addCheckError }}</div>
        <div class="form-row">
          <label>{{ t('tenantModelPolicyPanel.dialog.reason') }}</label>
          <input v-model="addReason" :placeholder="t('tenantModelPolicyPanel.dialog.reasonPlaceholder')" />
        </div>
        <div class="modal-actions">
          <button class="btn" @click="showAddDialog = false">{{ t('tenantModelPolicyPanel.dialog.cancel') }}</button>
          <button class="btn btn-primary" :disabled="submitting" @click="submitAdd">
            {{ submitting ? t('tenantModelPolicyPanel.dialog.submitting') : t('tenantModelPolicyPanel.dialog.submit') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.model-policy-panel { padding: 8px 0; }
.panel-header {
  display: flex; justify-content: space-between; align-items: flex-start;
  margin-bottom: 12px; gap: 16px;
}
.panel-header h3 { margin: 0 0 4px 0; font-size: 16px; }
.hint { font-size: 12px; color: var(--muted); margin: 0; max-width: 640px; }
.actions { display: flex; align-items: center; gap: 12px; flex-shrink: 0; }
.cb { font-size: 13px; color: var(--muted); display: flex; align-items: center; gap: 4px; }
.table { border-collapse: collapse; }
.table th, .table td {
  padding: 8px 8px; text-align: left; border-bottom: 1px solid var(--border);
  font-size: 13px;
}
.table th { color: var(--muted); font-weight: 500; }
.table tr.is-deleted td { color: var(--muted); text-decoration: line-through; }
.mono { font-family: 'SF Mono', 'Fira Code', monospace; font-size: 12px; }
.alert { padding: 8px 12px; border-radius: 4px; margin: 8px 0; font-size: 13px; }
.alert-danger { background: var(--danger-bg); color: var(--danger); }
.loading { text-align: center; padding: 24px; color: var(--muted); }
.audit-section { margin-top: 16px; }
.audit-section summary {
  cursor: pointer; font-size: 13px; color: var(--muted); padding: 8px 0;
}
.btn { padding: 4px 12px; border: 1px solid var(--border); background: var(--card);
  border-radius: 4px; cursor: pointer; font-size: 13px; }
.btn:hover { background: color-mix(in srgb, var(--accent) 06%, transparent); }
.btn-primary { background: var(--accent); color: white; border-color: var(--accent); }
.btn-sm { padding: 3px 8px; font-size: 12px; }
.btn-danger { background: var(--danger-bg); color: var(--danger); border-color: var(--danger-bd); }
.modal-overlay {
  position: fixed; inset: 0; background: var(--overlay-strong);
  display: flex; align-items: center; justify-content: center; z-index: 100;
}
.modal {
  background: var(--card); padding: 24px; border-radius: 8px;
  min-width: 480px; max-width: 640px;
}
.modal h3 { margin: 0 0 8px 0; font-size: 16px; }
.form-row { display: flex; align-items: center; gap: 8px; margin: 12px 0; }
.form-row label { font-size: 13px; min-width: 110px; }
.form-row input {
  flex: 1; padding: 4px 8px; border: 1px solid var(--border);
  background: var(--bg); color: var(--text); border-radius: 4px; font-size: 13px;
}
.check-result { font-size: 12px; color: var(--muted); padding: 4px 0; }
.modal-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }
</style>