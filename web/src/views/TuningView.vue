<script setup lang="ts">
// TuningView.vue — Admin UI for the auto-route feedback tuning loop.
//
// Three sections:
//   1. Pending Proposals — list, approve, reject
//   2. Accuracy Dashboard — 7-day quality breakdown by task type
//   3. Manual Trigger — fire the daily analyzer on demand
//
// All data is fetched from /api/admin/auto-route/tuning/* (mounted by
// admin/auto_route_tuning.go). Reuses the existing admin auth flow
// (bearer token from store).

import { ref, computed, onMounted } from 'vue'
import {
  getTuningProposals,
  approveTuningProposal,
  rejectTuningProposal,
  getTuningAccuracy,
  triggerTuningAnalyze,
  type TuningProposal,
  type AccuracyBreakdownRow,
} from '../api'

// ── Proposals section ────────────────────────────────────────────
const proposals = ref<TuningProposal[]>([])
const proposalsLoading = ref(false)
const proposalsError = ref<string | null>(null)
const filterStatus = ref<'pending' | 'approved' | 'rejected' | 'applied' | ''>('pending')
const filterCategory = ref<'keyword_add' | 'weight_adjust' | 'threshold_change' | ''>('')

async function loadProposals() {
  proposalsLoading.value = true
  proposalsError.value = null
  try {
    const r = await getTuningProposals({
      status: filterStatus.value || undefined,
      category: filterCategory.value || undefined,
      limit: 100,
    })
    proposals.value = r.proposals
  } catch (e: any) {
    proposalsError.value = e?.message ?? String(e)
  } finally {
    proposalsLoading.value = false
  }
}

async function approve(p: TuningProposal) {
  if (!confirm(`Approve proposal #${p.id} (${p.category} for ${p.task_type ?? 'global'})?`)) return
  try {
    await approveTuningProposal(p.id)
    await loadProposals()
  } catch (e: any) {
    alert('Approve failed: ' + (e?.message ?? e))
  }
}

const rejectingId = ref<number | null>(null)
const rejectReason = ref('')

async function reject(p: TuningProposal) {
  rejectingId.value = p.id
  rejectReason.value = ''
}

function cancelReject() {
  rejectingId.value = null
  rejectReason.value = ''
}

async function confirmReject() {
  if (rejectingId.value === null) return
  try {
    await rejectTuningProposal(rejectingId.value, rejectReason.value || undefined)
    rejectingId.value = null
    rejectReason.value = ''
    await loadProposals()
  } catch (e: any) {
    alert('Reject failed: ' + (e?.message ?? e))
  }
}

// ── Accuracy dashboard section ───────────────────────────────────
const accuracyRows = ref<AccuracyBreakdownRow[]>([])
const accuracyLoading = ref(false)
const accuracyError = ref<string | null>(null)
const accuracyDays = ref(7)

async function loadAccuracy() {
  accuracyLoading.value = true
  accuracyError.value = null
  try {
    const r = await getTuningAccuracy(accuracyDays.value)
    accuracyRows.value = r.breakdown
  } catch (e: any) {
    accuracyError.value = e?.message ?? String(e)
  } finally {
    accuracyLoading.value = false
  }
}

const accuracySummary = computed(() => {
  if (accuracyRows.value.length === 0) return null
  const total = accuracyRows.value.reduce((acc, r) => acc + r.total, 0)
  const avgQuality = accuracyRows.value.reduce(
    (acc, r) => acc + r.avg_quality * r.total, 0) / Math.max(total, 1)
  const avgSuccess = accuracyRows.value.reduce(
    (acc, r) => acc + r.avg_success * r.total, 0) / Math.max(total, 1)
  const avgDrift = accuracyRows.value.reduce(
    (acc, r) => acc + r.drift_rate * r.total, 0) / Math.max(total, 1)
  return { total, avgQuality, avgSuccess, avgDrift }
})

// ── Manual trigger section ───────────────────────────────────────
const analyzeResult = ref<string | null>(null)
const analyzeLoading = ref(false)

async function triggerAnalyze() {
  if (!confirm('Run the feedback analyzer on demand? May take 30-60s.')) return
  analyzeLoading.value = true
  analyzeResult.value = null
  try {
    const r = await triggerTuningAnalyze()
    analyzeResult.value = `Completed at ${r.completed_at} (triggered_by=${r.triggered_by})`
    await loadProposals()
  } catch (e: any) {
    analyzeResult.value = 'Error: ' + (e?.message ?? e)
  } finally {
    analyzeLoading.value = false
  }
}

// ── Helpers ─────────────────────────────────────────────────────
function categoryLabel(c: string): string {
  switch (c) {
    case 'keyword_add': return 'Add Keyword'
    case 'weight_adjust': return 'Adjust Weight'
    case 'threshold_change': return 'Change Threshold'
    default: return c
  }
}

function evidenceSummary(p: TuningProposal): string {
  const e = p.evidence || {}
  const parts: string[] = []
  if (e.sample_count) parts.push(`n=${e.sample_count}`)
  if (e.actual_success !== undefined) parts.push(`success=${(e.actual_success * 100).toFixed(0)}%`)
  if (e.predicted_match !== undefined) parts.push(`pred_match=${(e.predicted_match * 100).toFixed(0)}%`)
  if (e.avg_quality !== undefined) parts.push(`quality=${(e.avg_quality * 100).toFixed(0)}%`)
  if (e.rationale) parts.push(e.rationale)
  return parts.join(' · ')
}

function qualityColor(q: number): string {
  if (q >= 0.8) return '#22c55e'
  if (q >= 0.6) return '#eab308'
  if (q >= 0.4) return '#f97316'
  return '#ef4444'
}

// ── Init ────────────────────────────────────────────────────────
onMounted(async () => {
  await Promise.all([loadProposals(), loadAccuracy()])
})
</script>

<template>
  <div class="tuning-view">
    <h1>Auto-Route 反馈调优</h1>
    <p class="subtitle">
      Tuning feedback loop for the auto-route classifier. The daily
      analyzer at 02:00 UTC generates proposals from accumulated
      <code>tuning_signals</code>; admins review and approve below.
    </p>

    <!-- ── 1. Pending proposals ────────────────────────────────── -->
    <section class="card">
      <h2>调优提案 (Proposals)</h2>
      <div class="filter-bar">
        <label>Status:
          <select v-model="filterStatus" @change="loadProposals">
            <option value="">(all)</option>
            <option value="pending">pending</option>
            <option value="approved">approved</option>
            <option value="rejected">rejected</option>
            <option value="applied">applied</option>
          </select>
        </label>
        <label>Category:
          <select v-model="filterCategory" @change="loadProposals">
            <option value="">(all)</option>
            <option value="keyword_add">keyword_add</option>
            <option value="weight_adjust">weight_adjust</option>
            <option value="threshold_change">threshold_change</option>
          </select>
        </label>
        <button @click="loadProposals" :disabled="proposalsLoading">
          {{ proposalsLoading ? 'Loading…' : 'Refresh' }}
        </button>
      </div>

      <p v-if="proposalsError" class="error">⚠️ {{ proposalsError }}</p>
      <p v-else-if="proposals.length === 0" class="empty">No proposals match the filter.</p>

      <table v-else class="proposal-table">
        <thead>
          <tr>
            <th>ID</th>
            <th>Created</th>
            <th>Category</th>
            <th>Task</th>
            <th>Proposal</th>
            <th>Evidence</th>
            <th>Status</th>
            <th v-if="filterStatus === 'pending' || !filterStatus">Actions</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="p in proposals" :key="p.id">
            <tr>
              <td>{{ p.id }}</td>
              <td>{{ new Date(p.ts).toLocaleString() }}</td>
              <td><span class="badge">{{ categoryLabel(p.category) }}</span></td>
              <td>{{ p.task_type ?? '—' }}</td>
              <td class="mono">{{ JSON.stringify(p.proposal) }}</td>
              <td class="evidence">{{ evidenceSummary(p) }}</td>
              <td><span :class="'status-' + p.status">{{ p.status }}</span></td>
              <td v-if="filterStatus === 'pending' || !filterStatus">
                <button v-if="p.status === 'pending'" @click="approve(p)" class="btn-approve">Approve</button>
                <button v-if="p.status === 'pending'" @click="reject(p)" class="btn-reject">Reject</button>
              </td>
            </tr>
            <tr v-if="rejectingId === p.id">
              <td colspan="8" class="reject-row">
                <label>Rejection reason:
                  <input v-model="rejectReason" placeholder="optional note" />
                </label>
                <button @click="confirmReject" class="btn-reject">Confirm reject</button>
                <button @click="cancelReject">Cancel</button>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </section>

    <!-- ── 2. Accuracy dashboard ──────────────────────────────── -->
    <section class="card">
      <h2>准确度仪表盘 (Accuracy Dashboard)</h2>
      <div class="filter-bar">
        <label>Window:
          <select v-model.number="accuracyDays" @change="loadAccuracy">
            <option :value="1">1 day</option>
            <option :value="7">7 days</option>
            <option :value="30">30 days</option>
            <option :value="90">90 days</option>
          </select>
        </label>
        <button @click="loadAccuracy" :disabled="accuracyLoading">
          {{ accuracyLoading ? 'Loading…' : 'Refresh' }}
        </button>
      </div>

      <p v-if="accuracyError" class="error">⚠️ {{ accuracyError }}</p>
      <div v-else-if="accuracySummary" class="summary-cards">
        <div class="summary-card">
          <div class="summary-label">Total signals</div>
          <div class="summary-value">{{ accuracySummary.total.toLocaleString() }}</div>
        </div>
        <div class="summary-card">
          <div class="summary-label">Avg quality</div>
          <div class="summary-value" :style="{ color: qualityColor(accuracySummary.avgQuality) }">
            {{ (accuracySummary.avgQuality * 100).toFixed(1) }}%
          </div>
        </div>
        <div class="summary-card">
          <div class="summary-label">Avg success</div>
          <div class="summary-value">
            {{ (accuracySummary.avgSuccess * 100).toFixed(1) }}%
          </div>
        </div>
        <div class="summary-card">
          <div class="summary-label">Drift rate</div>
          <div class="summary-value">
            {{ (accuracySummary.avgDrift * 100).toFixed(1) }}%
          </div>
        </div>
      </div>

      <table v-if="accuracyRows.length > 0" class="accuracy-table">
        <thead>
          <tr>
            <th>Task type</th>
            <th>Classifier</th>
            <th>Total</th>
            <th>Avg quality</th>
            <th>Success</th>
            <th>Latency</th>
            <th>Cost</th>
            <th>Drift</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in accuracyRows" :key="`${r.task_type}-${r.classifier}`">
            <td><span class="task-type">{{ r.task_type }}</span></td>
            <td>{{ r.classifier }}</td>
            <td>{{ r.total.toLocaleString() }}</td>
            <td :style="{ color: qualityColor(r.avg_quality), fontWeight: 600 }">
              {{ (r.avg_quality * 100).toFixed(1) }}%
            </td>
            <td>{{ (r.avg_success * 100).toFixed(1) }}%</td>
            <td>{{ (r.avg_latency * 100).toFixed(1) }}%</td>
            <td>{{ (r.avg_cost * 100).toFixed(1) }}%</td>
            <td :class="{ drift: r.drift_rate > 0.05 }">
              {{ (r.drift_rate * 100).toFixed(1) }}%
            </td>
          </tr>
        </tbody>
      </table>
      <p v-else class="empty">No data yet. Run the gateway for a while and refresh.</p>
    </section>

    <!-- ── 3. Manual trigger ──────────────────────────────────── -->
    <section class="card">
      <h2>手动触发分析 (Manual Analyze)</h2>
      <p class="hint">
        Normally the analyzer runs daily at 02:00 UTC. Click below to
        force an immediate run and refresh the proposal list.
      </p>
      <button @click="triggerAnalyze" :disabled="analyzeLoading" class="btn-trigger">
        {{ analyzeLoading ? 'Running…' : 'Run analyzer now' }}
      </button>
      <p v-if="analyzeResult" class="result">{{ analyzeResult }}</p>
    </section>
  </div>
</template>

<style scoped>
.tuning-view {
  padding: 24px;
  max-width: 1200px;
  margin: 0 auto;
  color: var(--text, #e6e6e6);
}
h1 {
  margin: 0 0 8px;
  font-size: 24px;
}
.subtitle {
  margin: 0 0 24px;
  color: #888;
  font-size: 14px;
}
.card {
  background: var(--card-bg, #1a1a1a);
  border: 1px solid var(--border, #2a2a2a);
  border-radius: 8px;
  padding: 20px;
  margin-bottom: 24px;
}
.card h2 {
  margin: 0 0 16px;
  font-size: 18px;
  border-bottom: 1px solid var(--border, #2a2a2a);
  padding-bottom: 8px;
}
.filter-bar {
  display: flex;
  gap: 16px;
  align-items: center;
  margin-bottom: 16px;
  flex-wrap: wrap;
}
.filter-bar label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: #aaa;
}
.filter-bar select,
.filter-bar input {
  padding: 4px 8px;
  background: #0e0e0e;
  border: 1px solid #2a2a2a;
  color: inherit;
  border-radius: 4px;
  font-size: 13px;
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
.filter-bar button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.error {
  color: #ef4444;
  font-size: 13px;
}
.empty {
  color: #888;
  font-size: 13px;
  font-style: italic;
}
.proposal-table,
.accuracy-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.proposal-table th,
.accuracy-table th {
  text-align: left;
  padding: 8px 10px;
  background: #0e0e0e;
  border-bottom: 1px solid #2a2a2a;
  color: #aaa;
  font-weight: 500;
}
.proposal-table td,
.accuracy-table td {
  padding: 8px 10px;
  border-bottom: 1px solid #1f1f1f;
  vertical-align: top;
}
.mono {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 12px;
  max-width: 320px;
  word-break: break-all;
}
.evidence {
  color: #aaa;
  font-size: 12px;
  max-width: 280px;
}
.badge {
  display: inline-block;
  padding: 2px 8px;
  background: #1e3a8a;
  color: #bfdbfe;
  border-radius: 4px;
  font-size: 11px;
  text-transform: uppercase;
}
.status-pending { color: #eab308; font-weight: 600; }
.status-approved { color: #22c55e; }
.status-rejected { color: #888; text-decoration: line-through; }
.status-applied { color: #22c55e; font-weight: 600; }
.status-expired { color: #888; }
.btn-approve,
.btn-reject,
.btn-trigger {
  padding: 4px 10px;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 12px;
  margin-right: 4px;
}
.btn-approve { background: #16a34a; color: #fff; }
.btn-reject { background: #6b7280; color: #fff; }
.btn-trigger { background: #7c3aed; color: #fff; padding: 8px 16px; }
.reject-row {
  background: #1a0e0e;
}
.reject-row input {
  margin: 0 8px;
  padding: 4px 8px;
  background: #0e0e0e;
  border: 1px solid #2a2a2a;
  color: inherit;
  border-radius: 4px;
  width: 300px;
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
.task-type {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 12px;
  color: #93c5fd;
}
.drift {
  color: #f97316;
  font-weight: 600;
}
.hint {
  color: #888;
  font-size: 13px;
  margin: 0 0 12px;
}
.result {
  margin-top: 12px;
  padding: 8px 12px;
  background: #0e0e0e;
  border-radius: 4px;
  font-size: 13px;
  color: #aaa;
}
</style>
