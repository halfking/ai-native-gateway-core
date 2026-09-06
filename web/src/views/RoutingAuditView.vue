<script setup lang="ts">
// RoutingAuditView.vue — P9.2: admin UI for routing override audit log.

import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { fmtDateTime24h } from '../i18n/useFormat'
import {
  getRoutingAudit,
  type RoutingAuditEntry,
} from '../api'

const { t } = useI18n()

const entries = ref<RoutingAuditEntry[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const filterAction = ref<'' | 'insert' | 'update' | 'delete'>('')
const filterActor = ref('')
const filterOverrideId = ref<number | null>(null)
const filterDays = ref(7)
const filterLimit = ref(200)

const expandedId = ref<number | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    const r = await getRoutingAudit({
      action: filterAction.value,
      actor: filterActor.value || undefined,
      override_id: filterOverrideId.value ?? undefined,
      days: filterDays.value,
      limit: filterLimit.value,
    })
    entries.value = r.entries
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function actionClass(a: string): string {
  switch (a) {
    case 'insert': return 'action-insert'
    case 'update': return 'action-update'
    case 'delete': return 'action-delete'
    default: return ''
  }
}

function actionLabel(a: string): string {
  const key = `routingAudit.actions.${a}` as 'routingAudit.actions.insert'
  if (a === 'insert' || a === 'update' || a === 'delete') return t(key)
  return a
}

function shortModel(m?: string): string {
  if (!m) return '—'
  return m.length > 18 ? m.slice(0, 15) + '...' : m
}

const summary = computed(() => {
  const total = entries.value.length
  return {
    total,
    insert: entries.value.filter(e => e.action === 'insert').length,
    update: entries.value.filter(e => e.action === 'update').length,
    delete: entries.value.filter(e => e.action === 'delete').length,
  }
})

onMounted(load)
</script>

<template>
  <div class="audit-view">
    <h1>{{ t('routingAudit.title') }}</h1>
    <p class="subtitle">{{ t('routingAudit.subtitle') }}</p>

    <div v-if="entries.length > 0" class="summary-cards">
      <div class="summary-card">
        <div class="summary-label">{{ t('routingAudit.summary.total') }}</div>
        <div class="summary-value">{{ summary.total }}</div>
      </div>
      <div class="summary-card">
        <div class="summary-label">{{ t('routingAudit.summary.inserts') }}</div>
        <div class="summary-value" style="color: #22c55e">{{ summary.insert }}</div>
      </div>
      <div class="summary-card">
        <div class="summary-label">{{ t('routingAudit.summary.updates') }}</div>
        <div class="summary-value" style="color: #3b82f6">{{ summary.update }}</div>
      </div>
      <div class="summary-card">
        <div class="summary-label">{{ t('routingAudit.summary.deletes') }}</div>
        <div class="summary-value" style="color: #ef4444">{{ summary.delete }}</div>
      </div>
    </div>

    <section class="card">
      <div class="filter-bar">
        <label>{{ t('routingAudit.filter.action') }}:
          <select v-model="filterAction" @change="load">
            <option value="">{{ t('routingAudit.filter.all') }}</option>
            <option value="insert">insert</option>
            <option value="update">update</option>
            <option value="delete">delete</option>
          </select>
        </label>
        <label>{{ t('routingAudit.filter.actor') }}:
          <input v-model="filterActor" :placeholder="t('routingAudit.filter.actorPlaceholder')"
                 @keyup.enter="load" />
        </label>
        <label>{{ t('routingAudit.filter.overrideId') }}:
          <input v-model.number="filterOverrideId" type="number" min="1"
                 :placeholder="t('routingAudit.filter.overrideIdPlaceholder')" @keyup.enter="load" />
        </label>
        <label>{{ t('routingAudit.filter.window') }}:
          <select v-model.number="filterDays" @change="load">
            <option :value="1">{{ t('routingAudit.filter.days.d1') }}</option>
            <option :value="7">{{ t('routingAudit.filter.days.d7') }}</option>
            <option :value="30">{{ t('routingAudit.filter.days.d30') }}</option>
            <option :value="90">{{ t('routingAudit.filter.days.d90') }}</option>
          </select>
        </label>
        <label>{{ t('routingAudit.filter.limit') }}:
          <select v-model.number="filterLimit" @change="load">
            <option :value="50">{{ t('routingAudit.filter.limits.l50') }}</option>
            <option :value="200">{{ t('routingAudit.filter.limits.l200') }}</option>
            <option :value="500">{{ t('routingAudit.filter.limits.l500') }}</option>
            <option :value="1000">{{ t('routingAudit.filter.limits.l1000') }}</option>
          </select>
        </label>
        <button @click="load" :disabled="loading">
          {{ loading ? t('routingAudit.filter.loading') : t('routingAudit.filter.refresh') }}
        </button>
      </div>
      <p v-if="error" class="error">⚠️ {{ error }}</p>
    </section>

    <section class="card">
      <h2>{{ t('routingAudit.table.title', { n: entries.length }) }}</h2>
      <p v-if="!loading && entries.length === 0" class="empty">
        {{ t('routingAudit.table.empty') }}
      </p>

      <table v-else class="audit-table">
        <thead>
          <tr>
            <th>{{ t('routingAudit.table.headers.when') }}</th>
            <th>{{ t('routingAudit.table.headers.action') }}</th>
            <th>{{ t('routingAudit.table.headers.override') }}</th>
            <th>{{ t('routingAudit.table.headers.taskProfileMode') }}</th>
            <th>{{ t('routingAudit.table.headers.model') }}</th>
            <th>{{ t('routingAudit.table.headers.reason') }}</th>
            <th>{{ t('routingAudit.table.headers.actor') }}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <template v-for="e in entries" :key="e.id">
            <tr :class="['audit-row', actionClass(e.action)]">
              <td class="mono">{{ fmtDateTime24h(e.ts) }}</td>
              <td>
                <span :class="['action-badge', actionClass(e.action)]">
                  {{ actionLabel(e.action) }}
                </span>
              </td>
              <td class="mono">
                <router-link v-if="e.override_id" :to="`/routing/overrides#${e.override_id}`">
                  #{{ e.override_id }}
                </router-link>
                <span v-else class="text-muted">—</span>
              </td>
              <td>
                <span v-if="e.task_type" class="tag tag-task">{{ e.task_type }}</span>
                <span v-if="e.profile" class="tag tag-profile">{{ e.profile }}</span>
                <span v-if="e.mode" :class="['tag', 'mode-' + e.mode]">{{ e.mode }}</span>
              </td>
              <td><span class="tag tag-model">{{ shortModel(e.model_chosen) }}</span></td>
              <td class="reason">{{ e.reason ?? '—' }}</td>
              <td><span class="actor">{{ e.actor ?? 'system' }}</span></td>
              <td>
                <button v-if="e.expires_at || e.old_expires_at"
                        @click="expandedId = expandedId === e.id ? null : e.id"
                        class="btn-expand">
                  {{ expandedId === e.id ? '−' : '+' }}
                </button>
              </td>
            </tr>
            <tr v-if="expandedId === e.id" class="expand-row">
              <td colspan="8">
                <div class="diff">
                  <div v-if="e.old_expires_at" class="diff-field">
                    <span class="diff-label">{{ t('routingAudit.expand.oldExpires') }}:</span>
                    <code>{{ e.old_expires_at }}</code>
                  </div>
                  <div v-if="e.expires_at" class="diff-field">
                    <span class="diff-label">{{ t('routingAudit.expand.newExpires') }}:</span>
                    <code>{{ e.expires_at }}</code>
                  </div>
                  <div v-if="!e.old_expires_at && !e.expires_at" class="text-muted">
                    {{ t('routingAudit.expand.noDiff') }}
                  </div>
                </div>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </section>
  </div>
</template>

<style scoped>
.audit-view {
  padding: 24px;
  max-width: 1400px;
  margin: 0 auto;
  color: var(--text);
}
h1 { margin: 0 0 8px; font-size: 24px; }
h2 {
  margin: 0 0 12px;
  font-size: 18px;
  border-bottom: 1px solid var(--border);
  padding-bottom: 8px;
}
.subtitle {
  margin: 0 0 24px;
  color: var(--muted);
  font-size: 14px;
}
.summary-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
.summary-card {
  background: var(--bg);
  border: 1px solid var(--bg);
  border-radius: 6px;
  padding: 12px 16px;
}
.summary-label {
  font-size: 11px;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.summary-value {
  font-size: 24px;
  font-weight: 600;
  margin-top: 4px;
}
.card {
  background: var(--card-bg);
  border: 1px solid var(--border);
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
  color: var(--muted);
}
.filter-bar input,
.filter-bar select {
  /* width:auto 覆盖全局 input/select width:100%，避免筛选控件占满整行 */
  width: auto;
  padding: 4px 8px;
  background: var(--bg);
  border: 1px solid var(--bg);
  color: inherit;
  border-radius: 4px;
  font-size: 13px;
  min-width: 120px;
}
.filter-bar button {
  padding: 6px 14px;
  background: var(--accent);
  color: var(--on-primary);
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 13px;
}
.filter-bar button:disabled { opacity: 0.5; cursor: not-allowed; }
.error {
  color: var(--danger);
  font-size: 13px;
  margin-top: 8px;
}
.empty {
  color: var(--muted);
  font-size: 13px;
  font-style: italic;
}
.audit-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.audit-table th {
  text-align: left;
  padding: 8px 10px;
  background: var(--bg);
  border-bottom: 1px solid var(--bg);
  color: var(--muted);
  font-weight: 500;
}
.audit-table td {
  padding: 8px 10px;
  border-bottom: 1px solid var(--bg);
  vertical-align: top;
}
.mono {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 12px;
}
.text-muted { color: var(--muted); }
.action-badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 3px;
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.action-insert { background: var(--success-strong); color: var(--success); }
.action-update { background: var(--accent-dark); color: var(--accent-h); }
.action-delete { background: var(--danger-dark); color: var(--danger-bd); }
.actor {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 12px;
  color: var(--warning);
}
.tag {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 11px;
  padding: 2px 6px;
  border-radius: 3px;
  display: inline-block;
  margin-right: 4px;
}
.tag-task { background: var(--success-strong); color: var(--success); }
.tag-profile { background: var(--kx-text); color: var(--accent-h); }
.tag-model { background: var(--kx-text); color: var(--accent-h); }
.mode-ban { background: var(--warning-dark); color: var(--warning); }
.mode-pin { background: var(--success-strong); color: var(--success); }
.reason {
  color: var(--border);
  font-size: 12px;
  max-width: 320px;
  word-break: break-word;
}
.btn-expand {
  width: 24px;
  height: 24px;
  border: 1px solid var(--bg);
  background: var(--bg);
  color: var(--muted);
  border-radius: 4px;
  cursor: pointer;
  font-size: 14px;
  font-weight: 600;
  display: flex;
  align-items: center;
  justify-content: center;
}
.btn-expand:hover { background: var(--kx-text); }
.expand-row {
  background: #050505;
}
.expand-row td {
  padding: 12px 16px;
}
.diff {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.diff-field {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}
.diff-label {
  color: var(--muted);
  min-width: 140px;
}
.diff-field code {
  background: var(--bg);
  padding: 2px 6px;
  border-radius: 3px;
  color: var(--warning);
  font-family: 'SF Mono', Menlo, monospace;
}
.audit-row a {
  color: var(--accent-h);
  text-decoration: none;
}
.audit-row a:hover {
  text-decoration: underline;
}
</style>
