<script setup lang="ts">
// RoutingAuditView.vue — P9.2: admin UI for routing override audit log.
//
// Surfaces every INSERT/UPDATE/DELETE on the routing_overrides
// table (P7.9 trigger + P7.9.1 app-level log) so admins can
// answer "who changed what when".
//
// Three sections:
//   1. Filter bar (action, actor, override_id, days, limit)
//   2. Audit table with colour-coded action badges
//   3. JSON diff display (before/after details, expandable)

import { ref, computed, onMounted } from 'vue'
import {
  getRoutingAudit,
  type RoutingAuditEntry,
} from '../api'

// ── State ────────────────────────────────────────────────────────
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
  } catch (e: any) {
    error.value = e?.message ?? String(e)
  } finally {
    loading.value = false
  }
}

// ── Helpers ─────────────────────────────────────────────────────
function actionClass(a: string): string {
  switch (a) {
    case 'insert': return 'action-insert'
    case 'update': return 'action-update'
    case 'delete': return 'action-delete'
    default: return ''
  }
}

function actionLabel(a: string): string {
  switch (a) {
    case 'insert': return 'Create'
    case 'update': return 'Update'
    case 'delete': return 'Delete'
    default: return a
  }
}

function shortModel(m?: string): string {
  if (!m) return '—'
  return m.length > 18 ? m.slice(0, 15) + '...' : m
}

function fmtDate(d?: string): string {
  if (!d) return '—'
  return new Date(d).toLocaleString()
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
    <h1>Routing Overrides Audit</h1>
    <p class="subtitle">
      Every change to the routing_overrides table is recorded
      (P7.9 trigger + P7.9.1 app-level log) with the actor,
      action, and the row state. Use this to answer "who banned
      which model, when, and why".
    </p>

    <!-- ── Summary cards ─────────────────────────────────────── -->
    <div v-if="entries.length > 0" class="summary-cards">
      <div class="summary-card">
        <div class="summary-label">Total</div>
        <div class="summary-value">{{ summary.total }}</div>
      </div>
      <div class="summary-card">
        <div class="summary-label">Inserts</div>
        <div class="summary-value" style="color: #22c55e">{{ summary.insert }}</div>
      </div>
      <div class="summary-card">
        <div class="summary-label">Updates</div>
        <div class="summary-value" style="color: #3b82f6">{{ summary.update }}</div>
      </div>
      <div class="summary-card">
        <div class="summary-label">Deletes</div>
        <div class="summary-value" style="color: #ef4444">{{ summary.delete }}</div>
      </div>
    </div>

    <!-- ── Filter bar ──────────────────────────────────────── -->
    <section class="card">
      <div class="filter-bar">
        <label>Action:
          <select v-model="filterAction" @change="load">
            <option value="">(all)</option>
            <option value="insert">insert</option>
            <option value="update">update</option>
            <option value="delete">delete</option>
          </select>
        </label>
        <label>Actor:
          <input v-model="filterActor" placeholder="admin username"
                 @keyup.enter="load" />
        </label>
        <label>Override ID:
          <input v-model.number="filterOverrideId" type="number" min="1"
                 placeholder="e.g. 42" @keyup.enter="load" />
        </label>
        <label>Window:
          <select v-model.number="filterDays" @change="load">
            <option :value="1">1 day</option>
            <option :value="7">7 days</option>
            <option :value="30">30 days</option>
            <option :value="90">90 days</option>
          </select>
        </label>
        <label>Limit:
          <select v-model.number="filterLimit" @change="load">
            <option :value="50">50</option>
            <option :value="200">200</option>
            <option :value="500">500</option>
            <option :value="1000">1000</option>
          </select>
        </label>
        <button @click="load" :disabled="loading">
          {{ loading ? 'Loading…' : 'Refresh' }}
        </button>
      </div>
      <p v-if="error" class="error">⚠️ {{ error }}</p>
    </section>

    <!-- ── Audit table ─────────────────────────────────────── -->
    <section class="card">
      <h2>Audit entries ({{ entries.length }})</h2>
      <p v-if="!loading && entries.length === 0" class="empty">
        No audit entries match the filter.
      </p>

      <table v-else class="audit-table">
        <thead>
          <tr>
            <th>When</th>
            <th>Action</th>
            <th>Override</th>
            <th>Task / Profile / Mode</th>
            <th>Model</th>
            <th>Reason</th>
            <th>Actor</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <template v-for="e in entries" :key="e.id">
            <tr :class="['audit-row', actionClass(e.action)]">
              <td class="mono">{{ fmtDate(e.ts) }}</td>
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
                    <span class="diff-label">Old expires_at:</span>
                    <code>{{ e.old_expires_at }}</code>
                  </div>
                  <div v-if="e.expires_at" class="diff-field">
                    <span class="diff-label">New expires_at:</span>
                    <code>{{ e.expires_at }}</code>
                  </div>
                  <div v-if="!e.old_expires_at && !e.expires_at" class="text-muted">
                    No diff fields for this action.
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
  color: var(--text, #e6e6e6);
}
h1 { margin: 0 0 8px; font-size: 24px; }
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
.summary-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
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
.filter-bar input,
.filter-bar select {
  padding: 4px 8px;
  background: #0e0e0e;
  border: 1px solid #2a2a2a;
  color: inherit;
  border-radius: 4px;
  font-size: 13px;
  min-width: 120px;
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
.audit-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.audit-table th {
  text-align: left;
  padding: 8px 10px;
  background: #0e0e0e;
  border-bottom: 1px solid #2a2a2a;
  color: #aaa;
  font-weight: 500;
}
.audit-table td {
  padding: 8px 10px;
  border-bottom: 1px solid #1f1f1f;
  vertical-align: top;
}
.mono {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 12px;
}
.text-muted { color: #888; }
.action-badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 3px;
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.action-insert { background: #14532d; color: #86efac; }
.action-update { background: #1e3a8a; color: #93c5fd; }
.action-delete { background: #7f1d1d; color: #fca5a5; }
.actor {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 12px;
  color: #fbbf24;
}
.tag {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 11px;
  padding: 2px 6px;
  border-radius: 3px;
  display: inline-block;
  margin-right: 4px;
}
.tag-task { background: #14532d; color: #86efac; }
.tag-profile { background: #1e293b; color: #93c5fd; }
.tag-model { background: #1e293b; color: #93c5fd; }
.mode-ban { background: #422006; color: #fb923c; }
.mode-pin { background: #14532d; color: #86efac; }
.reason {
  color: #ccc;
  font-size: 12px;
  max-width: 320px;
  word-break: break-word;
}
.btn-expand {
  width: 24px;
  height: 24px;
  border: 1px solid #2a2a2a;
  background: #0e0e0e;
  color: #aaa;
  border-radius: 4px;
  cursor: pointer;
  font-size: 14px;
  font-weight: 600;
  display: flex;
  align-items: center;
  justify-content: center;
}
.btn-expand:hover { background: #1a1a1a; }
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
  color: #888;
  min-width: 140px;
}
.diff-field code {
  background: #0e0e0e;
  padding: 2px 6px;
  border-radius: 3px;
  color: #fbbf24;
  font-family: 'SF Mono', Menlo, monospace;
}
.audit-row a {
  color: #93c5fd;
  text-decoration: none;
}
.audit-row a:hover {
  text-decoration: underline;
}
</style>
