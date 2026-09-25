<script setup lang="ts">
// AutoTuningView.vue — 路由调参页（v2 规划 P0③，2026-09-24；P1 增补生成入口）。
//
// tuning admin API（/api/admin/auto-route/tuning，superAdmin 权限）的运营
// 门面：调参提案列表（状态/类别过滤）+ 批准/驳回（批准即热调参，5 分钟内
// 经 bg/tuning_store_refresher 生效）+ 分类质量窗口报表 + 两个按需生成入口
// （P1：人工修正驱动提案 / 信号分析器即时运行）。
// 提案由 feedback_analyzer 与 taskprofile/analyzer 自动生成（回路 C 的
// "自动生成提案"半环）；本页是"人工批准"半环（v2 规划 §4.4：自动的是提案，
// 生效永远是门禁+人工）。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getTuningProposals,
  approveTuningProposal,
  rejectTuningProposal,
  getTuningAccuracy,
  generateCorrectionProposals,
  triggerTuningAnalyze,
  type TuningProposal,
  type TuningProposalStatus,
  type TuningProposalCategory,
} from '../api/tuning'

const { t } = useI18n()

type StatusFilter = TuningProposalStatus | ''
type CategoryFilter = TuningProposalCategory | ''

const statusFilter = ref<StatusFilter>('')
const categoryFilter = ref<CategoryFilter>('')
const limit = ref(50)

const proposals = ref<TuningProposal[]>([])
const loading = ref(false)
const error = ref('')
const notice = ref('')
const acting = ref(0)

const accuracyDays = ref(7)
const accuracyRows = ref<Awaited<ReturnType<typeof getTuningAccuracy>>['breakdown']>([])
const accuracyGeneratedAt = ref('')
const accuracyLoading = ref(false)
const accuracyError = ref('')

const statusTabs = computed(() => [
  { value: '' as StatusFilter, label: t('autoTuning.filter.statusAll') },
  { value: 'pending' as StatusFilter, label: t('autoTuning.filter.pending') },
  { value: 'approved' as StatusFilter, label: t('autoTuning.filter.approved') },
  { value: 'rejected' as StatusFilter, label: t('autoTuning.filter.rejected') },
  { value: 'applied' as StatusFilter, label: t('autoTuning.filter.applied') },
])

async function load() {
  loading.value = true
  error.value = ''
  notice.value = ''
  try {
    const r = await getTuningProposals({
      status: statusFilter.value,
      category: categoryFilter.value,
      limit: limit.value,
    })
    proposals.value = r.proposals
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('autoTuning.loadFailed')
    proposals.value = []
  } finally {
    loading.value = false
  }
}

async function loadAccuracy() {
  accuracyLoading.value = true
  accuracyError.value = ''
  try {
    const r = await getTuningAccuracy(accuracyDays.value)
    accuracyRows.value = r.breakdown
    accuracyGeneratedAt.value = r.generated_at
  } catch (e: unknown) {
    accuracyError.value = e instanceof Error ? e.message : t('autoTuning.loadFailed')
    accuracyRows.value = []
  } finally {
    accuracyLoading.value = false
  }
}

const generating = ref(false)
const generateDays = ref(30)
const analyzing = ref(false)

// P1 回路 C：从人工修正生成提案草稿（读取 → 过闸 → 草稿 → 内联回放 → 待审）。
async function generate() {
  if (!window.confirm(t('autoTuning.action.generateConfirm', { days: generateDays.value }))) return
  generating.value = true
  error.value = ''
  notice.value = ''
  try {
    const r = await generateCorrectionProposals(generateDays.value)
    notice.value = r.generated > 0
      ? t('autoTuning.action.generateDone', { n: r.generated })
      : t('autoTuning.action.generateNone')
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    generating.value = false
  }
}

// 按需运行既有信号分析器（tuning_signals 质量驱动提案）。
async function analyzeNow() {
  if (!window.confirm(t('autoTuning.action.analyzeConfirm'))) return
  analyzing.value = true
  error.value = ''
  notice.value = ''
  try {
    const r = await triggerTuningAnalyze()
    notice.value = t('autoTuning.action.analyzeDone', { at: r.completed_at })
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    analyzing.value = false
  }
}

function categoryLabel(c: TuningProposalCategory): string {
  switch (c) {
    case 'keyword_add': return t('autoTuning.filter.keywordAdd')
    case 'weight_adjust': return t('autoTuning.filter.weightAdjust')
    case 'threshold_change': return t('autoTuning.filter.thresholdChange')
  }
}

function statusClass(s: TuningProposalStatus): string {
  return `status-${s}`
}

// proposal JSON 每类形态不同（bg/feedback_analyzer.go、taskprofile/analyzer.go）：
// keyword_add 带 add 数组/channel,weight_adjust 带 weights 映射,threshold_change
// 带 key/old/new。摘要 = 关键字段 + 截断 JSON。
function proposalSummary(p: TuningProposal): string {
  const rec = p.proposal as Record<string, unknown>
  const parts: string[] = []
  if (Array.isArray(rec.add) && rec.add.length) parts.push(`add=${rec.add.join(',')}`)
  if (typeof rec.keyword === 'string') parts.push(`keyword=${rec.keyword}`)
  if (typeof rec.channel === 'string') parts.push(`channel=${rec.channel}`)
  if (rec.weights && typeof rec.weights === 'object') {
    parts.push(`weights=${JSON.stringify(rec.weights)}`)
  }
  if (typeof rec.key === 'string') parts.push(`key=${rec.key}`)
  if (rec.old !== undefined) parts.push(`${String(rec.old)}→${String(rec.new)}`)
  else if (rec.new !== undefined) parts.push(`new=${JSON.stringify(rec.new)}`)
  if (rec.value !== undefined) parts.push(`value=${JSON.stringify(rec.value)}`)
  const raw = JSON.stringify(rec)
  if (parts.length === 0) return raw.length > 120 ? raw.slice(0, 117) + '...' : raw
  return parts.join(' ')
}

function evidenceSummary(p: TuningProposal): string {
  const e = p.evidence
  const bits: string[] = []
  if (e.sample_count != null) bits.push(`n=${e.sample_count}`)
  if (e.avg_quality != null) bits.push(`q=${e.avg_quality.toFixed(2)}`)
  if (e.confidence != null) bits.push(`conf=${e.confidence.toFixed(2)}`)
  if (e.window_days != null) bits.push(`${e.window_days}d`)
  // corrections 驱动草稿（P1）：对/占比/回放计数
  if (e.auto_task_type && e.human_task_type) bits.push(`${e.auto_task_type}→${e.human_task_type}`)
  if (e.corrected_total != null) bits.push(`corrected=${e.corrected_total}`)
  if (e.domain_hint) bits.push(`hint=${e.domain_hint}(${Math.round((e.hint_share ?? 0) * 100)}%)`)
  if (e.backtest) {
    const b = e.backtest
    if (b.would_touch != null) bits.push(`band=${b.would_touch}`)
    if (b.would_fix_proxy != null) bits.push(`fix≈${b.would_fix_proxy}`)
    if (b.matched != null) bits.push(`matched=${b.matched}`)
    if (b.matched_corrected != null) bits.push(`fixed≈${b.matched_corrected}`)
  }
  return bits.join(' ')
}

async function approve(id: number) {
  if (!window.confirm(t('autoTuning.action.approveConfirm', { id }))) return
  acting.value = id
  error.value = ''
  notice.value = ''
  try {
    const r = await approveTuningProposal(id)
    notice.value = `#${r.id}: ${r.message}`
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    acting.value = 0
  }
}

async function reject(id: number) {
  const reason = window.prompt(t('autoTuning.action.rejectPrompt', { id }), '')
  if (reason === null) return
  acting.value = id
  error.value = ''
  notice.value = ''
  try {
    await rejectTuningProposal(id, reason)
    notice.value = t('autoTuning.action.rejected', { id })
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    acting.value = 0
  }
}

onMounted(() => {
  load()
  loadAccuracy()
})
</script>

<template>
  <div class="stats-page">
    <div class="page-header">
      <h2>{{ t('autoTuning.title') }}</h2>
      <div class="header-actions">
        <label class="inline-label">
          {{ t('autoTuning.action.windowDays') }}
          <select v-model.number="generateDays" class="select-sm">
            <option :value="7">7</option>
            <option :value="30">30</option>
            <option :value="90">90</option>
          </select>
        </label>
        <button class="btn btn-primary btn-sm" :disabled="generating" @click="generate">
          {{ generating ? t('autoTuning.action.generating') : t('autoTuning.action.generate') }}
        </button>
        <button class="btn btn-sm" :disabled="analyzing" @click="analyzeNow">
          {{ analyzing ? t('autoTuning.action.analyzing') : t('autoTuning.action.analyze') }}
        </button>
        <button class="btn btn-sm" :disabled="loading" @click="load">
          {{ loading ? t('autoTuning.refreshing') : t('autoTuning.refresh') }}
        </button>
      </div>
    </div>

    <p class="page-desc">{{ t('autoTuning.desc') }}</p>

    <div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>
    <div v-if="notice" class="alert alert-success" role="status">{{ notice }}</div>

    <div class="filter-bar">
      <button
        v-for="tab in statusTabs"
        :key="tab.value"
        class="chip"
        :class="{ active: statusFilter === tab.value }"
        @click="statusFilter = tab.value; load()"
      >{{ tab.label }}</button>
      <label class="inline-label">
        {{ t('autoTuning.filter.category') }}
        <select v-model="categoryFilter" class="select-sm" @change="load">
          <option value="">{{ t('autoTuning.filter.categoryAll') }}</option>
          <option value="keyword_add">{{ t('autoTuning.filter.keywordAdd') }}</option>
          <option value="weight_adjust">{{ t('autoTuning.filter.weightAdjust') }}</option>
          <option value="threshold_change">{{ t('autoTuning.filter.thresholdChange') }}</option>
        </select>
      </label>
    </div>

    <div v-if="loading" class="loading-container">
      <p>{{ t('autoTuning.loading') }}</p>
    </div>

    <div v-else class="table-wrap">
      <table v-if="proposals.length" class="data-table">
        <thead>
          <tr>
            <th>#</th>
            <th>{{ t('autoTuning.table.ts') }}</th>
            <th>{{ t('autoTuning.table.category') }}</th>
            <th>{{ t('autoTuning.table.taskType') }}</th>
            <th>{{ t('autoTuning.table.proposal') }}</th>
            <th>{{ t('autoTuning.table.evidence') }}</th>
            <th>{{ t('autoTuning.table.status') }}</th>
            <th>{{ t('autoTuning.table.reviewedBy') }}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in proposals" :key="p.id">
            <td class="mono">{{ p.id }}</td>
            <td class="mono dim">{{ p.ts.replace('T', ' ').slice(0, 19) }}</td>
            <td>{{ categoryLabel(p.category) }}</td>
            <td class="mono">{{ p.task_type ?? t('autoTuning.table.global') }}</td>
            <td class="proposal-cell mono">{{ proposalSummary(p) }}</td>
            <td class="dim mono">{{ evidenceSummary(p) }}</td>
            <td>
              <span class="status-badge" :class="statusClass(p.status)">{{ p.status }}</span>
              <div v-if="p.review_note" class="note-cell dim">{{ p.review_note }}</div>
            </td>
            <td class="dim">{{ p.reviewed_by ?? '—' }}</td>
            <td class="actions">
              <template v-if="p.status === 'pending'">
                <button
                  class="btn btn-primary btn-xs"
                  :disabled="acting === p.id"
                  @click="approve(p.id)"
                >{{ t('autoTuning.action.approve') }}</button>
                <button
                  class="btn btn-xs"
                  :disabled="acting === p.id"
                  @click="reject(p.id)"
                >{{ t('autoTuning.action.reject') }}</button>
              </template>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-else class="dim">{{ t('autoTuning.noProposals') }}</p>
    </div>

    <div class="section-header">
      <h3>{{ t('autoTuning.accuracy.title') }}</h3>
      <label class="inline-label">
        {{ t('autoTuning.accuracy.days') }}
        <select v-model.number="accuracyDays" class="select-sm" @change="loadAccuracy">
          <option :value="1">1</option>
          <option :value="7">7</option>
          <option :value="30">30</option>
        </select>
      </label>
    </div>

    <div v-if="accuracyError" class="alert alert-danger" role="alert">{{ accuracyError }}</div>
    <div v-if="accuracyLoading" class="loading-container"><p>...</p></div>
    <div v-else class="table-wrap">
      <table v-if="accuracyRows.length" class="data-table">
        <thead>
          <tr>
            <th>{{ t('autoTuning.accuracy.taskType') }}</th>
            <th>{{ t('autoTuning.accuracy.classifier') }}</th>
            <th>{{ t('autoTuning.accuracy.total') }}</th>
            <th>{{ t('autoTuning.accuracy.avgQuality') }}</th>
            <th>{{ t('autoTuning.accuracy.avgSuccess') }}</th>
            <th>{{ t('autoTuning.accuracy.avgLatency') }}</th>
            <th>{{ t('autoTuning.accuracy.avgCost') }}</th>
            <th>{{ t('autoTuning.accuracy.driftRate') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, i) in accuracyRows" :key="i">
            <td class="mono">{{ row.task_type }}</td>
            <td class="mono dim">{{ row.classifier }}</td>
            <td class="mono">{{ row.total }}</td>
            <td class="mono">{{ row.avg_quality.toFixed(3) }}</td>
            <td class="mono">{{ (row.avg_success * 100).toFixed(1) }}%</td>
            <td class="mono">{{ row.avg_latency.toFixed(0) }}ms</td>
            <td class="mono">${{ row.avg_cost.toFixed(5) }}</td>
            <td class="mono">{{ (row.drift_rate * 100).toFixed(1) }}%</td>
          </tr>
        </tbody>
      </table>
      <p v-else class="dim">{{ t('autoTuning.accuracy.noData') }}</p>
      <p v-if="accuracyGeneratedAt" class="dim generated-at">
        {{ t('autoTuning.accuracy.generatedAt') }}: {{ accuracyGeneratedAt.replace('T', ' ').slice(0, 19) }}
      </p>
    </div>
  </div>
</template>

<style scoped>
.header-actions {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  flex-wrap: wrap;
}
.header-actions .inline-label {
  margin-left: 0;
}
.filter-bar {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin: 1rem 0;
  flex-wrap: wrap;
}
.chip {
  padding: 0.25rem 0.75rem;
  border-radius: 999px;
  border: 1px solid var(--border-color, #d1d5db);
  background: transparent;
  cursor: pointer;
  font-size: 0.8rem;
}
.chip.active {
  background: var(--primary, #2563eb);
  color: #fff;
  border-color: var(--primary, #2563eb);
}
.inline-label {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 0.85rem;
  color: var(--text-secondary, #666);
  margin-left: auto;
}
.select-sm {
  padding: 0.2rem 0.4rem;
  border: 1px solid var(--border-color, #ccc);
  border-radius: 6px;
  background: var(--bg-card, #fff);
}
.table-wrap {
  overflow-x: auto;
}
.data-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.85rem;
}
.data-table th,
.data-table td {
  padding: 0.45rem 0.6rem;
  border-bottom: 1px solid var(--border-color, #e5e7eb);
  text-align: left;
  vertical-align: top;
}
.data-table th {
  font-weight: 600;
  color: var(--text-secondary, #555);
  white-space: nowrap;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  white-space: nowrap;
}
.dim {
  color: var(--text-secondary, #999);
}
.proposal-cell {
  max-width: 24rem;
  overflow-wrap: anywhere;
  white-space: normal;
}
.note-cell {
  font-size: 0.75rem;
  max-width: 12rem;
  overflow-wrap: anywhere;
}
.actions {
  white-space: nowrap;
}
.actions .btn {
  margin-right: 0.35rem;
}
.btn-xs {
  padding: 0.15rem 0.5rem;
  font-size: 0.75rem;
}
.status-badge {
  display: inline-block;
  padding: 0.1rem 0.5rem;
  border-radius: 999px;
  font-size: 0.75rem;
  border: 1px solid var(--border-color, #d1d5db);
}
.status-pending {
  background: rgba(245, 158, 11, 0.15);
  border-color: rgba(245, 158, 11, 0.5);
}
.status-approved {
  background: rgba(59, 130, 246, 0.12);
  border-color: rgba(59, 130, 246, 0.4);
}
.status-applied {
  background: rgba(16, 185, 129, 0.12);
  border-color: rgba(16, 185, 129, 0.4);
}
.status-rejected {
  background: rgba(107, 114, 128, 0.12);
  border-color: rgba(107, 114, 128, 0.4);
}
.section-header {
  display: flex;
  align-items: center;
  gap: 1rem;
  margin: 2rem 0 0.5rem;
}
.section-header h3 {
  margin: 0;
}
.section-header .inline-label {
  margin-left: 0;
}
.generated-at {
  font-size: 0.75rem;
  margin-top: 0.4rem;
}
</style>
