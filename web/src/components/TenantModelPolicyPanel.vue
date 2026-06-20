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
    error.value = e instanceof Error ? e.message : '加载失败'
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
    addCheckError.value = e instanceof Error ? e.message : 'check failed'
  }
}

async function submitAdd() {
  const name = addCanonical.value.trim()
  const reason = addReason.value.trim()
  if (!name) {
    addCheckError.value = 'canonical_name 必填'
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
    addCheckError.value = e instanceof Error ? e.message : '创建失败'
  } finally {
    submitting.value = false
  }
}

async function softDelete(p: TenantModelPolicy) {
  if (!confirm(`确认软删除策略 ${p.canonical_name}？(可恢复)`)) return
  try {
    await deleteTenantModelPolicy(props.tenantCode, p.id)
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '删除失败'
  }
}

async function restore(p: TenantModelPolicy) {
  try {
    await undeleteTenantModelPolicy(props.tenantCode, p.id)
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '恢复失败'
  }
}

function fmtTime(s: string | null) {
  if (!s) return '-'
  return new Date(s).toLocaleString('zh-CN')
}

function actionLabel(a: string) {
  switch (a) {
    case 'insert': return '创建'
    case 'update': return '修改'
    case 'delete': return '软删除'
    case 'undelete': return '恢复'
    default: return a
  }
}

onMounted(load)
</script>

<template>
  <div class="model-policy-panel">
    <div class="panel-header">
      <div>
        <h3>模型管控 (Tenant: {{ tenantCode }})</h3>
        <p class="hint">
          在此配置的模型将被该租户下的所有 API key 拒绝（403 model_forbidden）。
          空表 = 该租户无限制（默认）。model="auto" 路径不纳入管控。
        </p>
      </div>
      <div class="actions">
        <label class="cb">
          <input type="checkbox" v-model="includeDeleted" @change="load" />
          显示已删除
        </label>
        <button class="btn btn-primary" @click="showAddDialog = true">+ 添加禁用模型</button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <div v-if="loading" class="loading">加载中…</div>

    <table v-else class="table" style="width:100%">
      <thead>
        <tr>
          <th>canonical_name</th>
          <th>reason</th>
          <th>created_by</th>
          <th>created_at</th>
          <th>deleted_at</th>
          <th>操作</th>
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
            <button v-if="!p.deleted_at" class="btn btn-sm btn-danger" @click="softDelete(p)">软删除</button>
            <button v-else class="btn btn-sm" @click="restore(p)">恢复</button>
          </td>
        </tr>
        <tr v-if="policies.length === 0">
          <td colspan="6" style="text-align:center; color: var(--muted); padding: 24px">
            无策略（默认所有模型允许）
          </td>
        </tr>
      </tbody>
    </table>

    <details class="audit-section" v-if="audit.length > 0">
      <summary>审计日志 (最近 {{ audit.length }} 条)</summary>
      <table class="table" style="width:100%; margin-top: 8px">
        <thead>
          <tr>
            <th>ts</th>
            <th>action</th>
            <th>canonical_name</th>
            <th>actor</th>
            <th>reason</th>
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
    </details>

    <!-- Add dialog -->
    <div v-if="showAddDialog" class="modal-overlay" @click.self="showAddDialog = false">
      <div class="modal">
        <h3>添加禁用模型</h3>
        <p class="hint">在下方输入 canonical_name（必须匹配 models_canonical 表）。</p>
        <div class="form-row">
          <label>canonical_name</label>
          <input v-model="addCanonical" placeholder="例如 minimax-m3" @blur="runCheck" />
          <button class="btn btn-sm" @click="runCheck" :disabled="!addCanonical">校验</button>
        </div>
        <div v-if="addCheckResult" class="check-result">
          {{ addCheckResult.exists
            ? `✓ 找到于 models_canonical（family=${addCheckResult.family || '?'}, modality=${addCheckResult.modality || '?'}）`
            : '⚠ 该 canonical_name 不在 models_canonical（仍允许写入，防御性管控）' }}
        </div>
        <div v-if="addCheckError" class="alert alert-danger">{{ addCheckError }}</div>
        <div class="form-row">
          <label>reason</label>
          <input v-model="addReason" placeholder="可选，例如 成本控制" />
        </div>
        <div class="modal-actions">
          <button class="btn" @click="showAddDialog = false">取消</button>
          <button class="btn btn-primary" :disabled="submitting" @click="submitAdd">
            {{ submitting ? '提交中…' : '提交' }}
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
  padding: 8px 10px; text-align: left; border-bottom: 1px solid var(--border);
  font-size: 13px;
}
.table th { color: var(--muted); font-weight: 500; }
.table tr.is-deleted td { color: var(--muted); text-decoration: line-through; }
.mono { font-family: 'SF Mono', 'Fira Code', monospace; font-size: 12px; }
.alert { padding: 8px 12px; border-radius: 4px; margin: 8px 0; font-size: 13px; }
.alert-danger { background: rgba(239,68,68,.12); color: #f87171; }
.loading { text-align: center; padding: 24px; color: var(--muted); }
.audit-section { margin-top: 16px; }
.audit-section summary {
  cursor: pointer; font-size: 13px; color: var(--muted); padding: 8px 0;
}
.btn { padding: 6px 12px; border: 1px solid var(--border); background: var(--card);
  border-radius: 4px; cursor: pointer; font-size: 13px; }
.btn:hover { background: rgba(99,102,241,.06); }
.btn-primary { background: var(--accent); color: white; border-color: var(--accent); }
.btn-sm { padding: 3px 8px; font-size: 12px; }
.btn-danger { background: rgba(239,68,68,.12); color: #f87171; border-color: rgba(239,68,68,.3); }
.modal-overlay {
  position: fixed; inset: 0; background: rgba(0,0,0,.5);
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
  flex: 1; padding: 6px 10px; border: 1px solid var(--border);
  background: var(--bg); color: var(--text); border-radius: 4px; font-size: 13px;
}
.check-result { font-size: 12px; color: var(--muted); padding: 4px 0; }
.modal-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }
</style>