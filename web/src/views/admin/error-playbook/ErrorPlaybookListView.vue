<template>
  <div class="error-playbook-view">
    <PageHeader :title="t('errorPlaybook.title')" :subtitle="t('errorPlaybook.subtitle')">
      <template #actions>
        <button class="ep-btn" type="button" :disabled="listLoading" @click="load">
          {{ t('errorPlaybook.action.refresh') }}
        </button>
        <button class="ep-btn ep-btn--primary" type="button" @click="openCreate">
          {{ t('errorPlaybook.action.create') }}
        </button>
      </template>
    </PageHeader>

    <SaStateBanner
      v-if="listError"
      kind="error"
      :title="t('errorPlaybook.error.listFailed')"
      :reason="listError"
      :action-label="t('errorPlaybook.action.retry')"
      @action="load"
    />

    <!-- 统计卡：/stats 的 SQL 聚合口径（EntrySummary/ExecutionSummary） -->
    <StatsRow v-if="stats" class="ep-stats">
      <StatCard :label="t('errorPlaybook.stats.totalEntries')" :value="fmt(stats.total_entries)" />
      <StatCard
        :label="t('errorPlaybook.stats.enabledEntries')"
        :value="fmt(stats.enabled_entries)"
        tone="success"
      />
      <StatCard
        :label="t('errorPlaybook.stats.autoExecuteEntries')"
        :value="fmt(stats.auto_execute_entries)"
      />
      <StatCard
        :label="t('errorPlaybook.stats.executions24h')"
        :value="fmt(exec24hTotal)"
        :sub="t('errorPlaybook.stats.executionsTotal') + ': ' + fmt(stats.executions?.total)"
      />
      <StatCard
        :label="t('errorPlaybook.stats.pendingUnknown')"
        :value="fmt(pendingUnknownTotal)"
      />
      <StatCard
        :label="t('errorPlaybook.stats.engineState')"
        :value="stats.engine_wired ? t('errorPlaybook.stats.engineOn') : t('errorPlaybook.stats.engineOff')"
        :tone="stats.engine_wired ? 'success' : 'warning'"
        :sub="stats.llm_wired ? t('errorPlaybook.stats.llmOn') : t('errorPlaybook.stats.llmOff')"
      />
    </StatsRow>

    <div class="ep-filters">
      <label class="ep-field">
        <span class="ep-field__label">{{ t('errorPlaybook.filter.query') }}</span>
        <input v-model="qDraft" class="ep-input" type="text" />
      </label>
      <label class="ep-field">
        <span class="ep-field__label">{{ t('errorPlaybook.filter.source') }}</span>
        <select v-model="sourceFilter" class="ep-input">
          <option value="">{{ t('errorPlaybook.filter.all') }}</option>
          <option value="seed">{{ t('errorPlaybook.source.seed') }}</option>
          <option value="manual">{{ t('errorPlaybook.source.manual') }}</option>
          <option value="pi_analysis">{{ t('errorPlaybook.source.pi_analysis') }}</option>
          <option value="refinement">{{ t('errorPlaybook.source.refinement') }}</option>
        </select>
      </label>
      <label class="ep-field">
        <span class="ep-field__label">{{ t('errorPlaybook.filter.enabled') }}</span>
        <select v-model="enabledFilter" class="ep-input">
          <option value="">{{ t('errorPlaybook.filter.all') }}</option>
          <option value="true">{{ t('errorPlaybook.filter.enabledOnly') }}</option>
          <option value="false">{{ t('errorPlaybook.filter.disabledOnly') }}</option>
        </select>
      </label>
      <button class="ep-btn ep-btn--primary" type="button" @click="applyFilters">
        {{ t('errorPlaybook.filter.apply') }}
      </button>
      <button v-if="hasFilters" class="ep-btn" type="button" @click="clearFilters">
        {{ t('errorPlaybook.action.reset') }}
      </button>
    </div>

    <DataTable
      :loading="listLoading"
      :empty="!listLoading && rows.length === 0"
      :empty-text="emptyText"
      min-width="1080px"
    >
      <table class="ep-table">
        <thead>
          <tr>
            <th>
              <button class="ep-sort" type="button" @click="toggleNameSort">
                {{ t('errorPlaybook.col.title') }}
                <span v-if="nameSort !== 'none'" class="ep-sort__arrow">
                  {{ nameSort === 'asc' ? '▲' : '▼' }}
                </span>
              </button>
            </th>
            <th>{{ t('errorPlaybook.col.errorType') }}</th>
            <th>{{ t('errorPlaybook.col.planKind') }}</th>
            <th>{{ t('errorPlaybook.col.source') }}</th>
            <th>{{ t('errorPlaybook.col.enabled') }}</th>
            <th>{{ t('errorPlaybook.col.autoExecute') }}</th>
            <th>{{ t('errorPlaybook.col.hits') }}</th>
            <th>{{ t('errorPlaybook.col.successRate') }}</th>
            <th>{{ t('errorPlaybook.col.lastStatus') }}</th>
            <th>{{ t('errorPlaybook.col.lastExecutedAt') }}</th>
            <th>{{ t('errorPlaybook.action.viewDetail') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in sortedRows" :key="e.id">
            <td>
              <button class="ep-link" type="button" @click="openDetail(e.id)">
                {{ e.title || e.error_type }}
              </button>
            </td>
            <td class="ep-mono">{{ e.error_type }}</td>
            <td>{{ t(`errorPlaybook.kind.${e.plan_kind}`) }}</td>
            <td>{{ t(`errorPlaybook.source.${e.source}`) }}</td>
            <td>{{ e.enabled ? '✓' : '—' }}</td>
            <td>{{ e.auto_execute ? '✓' : '—' }}</td>
            <td>{{ fmt(e.hit_count) }}</td>
            <td>{{ fmt(e.success_count) }} / {{ fmt(e.failure_count) }}</td>
            <td>
              <span v-if="e.last_status" class="ep-chip" :class="statusClass(e.last_status)">
                {{ statusLabel(e.last_status) }}
              </span>
              <span v-else>—</span>
            </td>
            <td>{{ formatTime(e.last_executed_at) }}</td>
            <td>
              <button class="ep-link" type="button" @click="openDetail(e.id)">
                {{ t('errorPlaybook.action.viewDetail') }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </DataTable>

    <PaginationBar
      :page="page"
      :page-size="pageSize"
      :total="total"
      @prev="prevPage"
      @next="nextPage"
      @change-size="changePageSize"
    />

    <!-- 新建条目（手工录入；归一化与门控在后端，422 原文原样展示） -->
    <div v-if="createOpen" class="ep-modal-backdrop" @click.self="closeCreate">
      <div class="ep-modal" role="dialog" :aria-label="t('errorPlaybook.action.create')">
        <h3 class="ep-modal__title">{{ t('errorPlaybook.action.create') }}</h3>
        <SaStateBanner
          v-if="createError"
          kind="error"
          :title="t('errorPlaybook.banner.validationTitle')"
          :reason="createError"
        />
        <div class="ep-modal__body">
          <label class="ep-field">
            <span class="ep-field__label">{{ t('errorPlaybook.form.errorType') }}</span>
            <input v-model="form.errorType" class="ep-input" type="text" />
            <span class="ep-field__hint">{{ t('errorPlaybook.form.errorTypeHint') }}</span>
          </label>
          <label class="ep-field">
            <span class="ep-field__label">{{ t('errorPlaybook.form.matchPattern') }}</span>
            <input v-model="form.matchPattern" class="ep-input ep-input--mono" type="text" />
            <span class="ep-field__hint">{{ t('errorPlaybook.form.matchPatternHint') }}</span>
          </label>
          <label class="ep-field">
            <span class="ep-field__label">{{ t('errorPlaybook.form.title') }}</span>
            <input v-model="form.title" class="ep-input" type="text" />
          </label>
          <label class="ep-field">
            <span class="ep-field__label">{{ t('errorPlaybook.form.description') }}</span>
            <textarea v-model="form.description" class="ep-input ep-input--area" rows="2" />
          </label>
          <label class="ep-field">
            <span class="ep-field__label">{{ t('errorPlaybook.form.rootCause') }}</span>
            <textarea v-model="form.rootCause" class="ep-input ep-input--area" rows="2" />
          </label>
          <PlanEditor ref="createPlanEditor" />
          <div class="ep-form-grid">
            <label class="ep-radio">
              <input v-model="form.enabled" type="checkbox" />
              <span>{{ t('errorPlaybook.form.entryEnabled') }}</span>
            </label>
            <label class="ep-radio">
              <input v-model="form.autoExecute" type="checkbox" />
              <span>{{ t('errorPlaybook.form.entryAutoExecute') }}</span>
            </label>
          </div>
        </div>
        <div class="ep-modal__actions">
          <button class="ep-btn" type="button" @click="closeCreate">
            {{ t('errorPlaybook.action.cancel') }}
          </button>
          <button
            class="ep-btn ep-btn--primary"
            type="button"
            :disabled="creating"
            @click="submitCreate"
          >
            {{ t('errorPlaybook.action.save') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
// ErrorPlaybookListView.vue — 错误处理手册列表页（2026-11-21）
//
// 排序口径：默认按服务端返回序（last_executed_at 的闭集序）。标题列可点击
// 切换本地名称排序（升/降）——「有名称的列表按名称排序便于查找」是全站约定
//（契约 C），与默认序用「默认/点击切换」共存而不是互相覆盖。
// ★ 用 localeCompare 而不是码点序：码点序会把中文标题排得很乱。

import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import PageHeader from '../../../components/ui/PageHeader.vue'
import DataTable from '../../../components/ui/DataTable.vue'
import PaginationBar from '../../../components/ui/PaginationBar.vue'
import StatsRow from '../../../components/ui/StatsRow.vue'
import StatCard from '../../../components/ui/StatCard.vue'
import SaStateBanner from '../session-analytics/components/SaStateBanner.vue'
import PlanEditor from './components/PlanEditor.vue'
import { useErrorPlaybookLoader } from './useErrorPlaybookLoader'
import {
  listEntries,
  createEntry,
  getStats,
  type PlaybookEntry,
  type PlaybookStats,
  type EntryWriteBody,
} from '../../../api/errorPlaybook'

const { t } = useI18n()
const router = useRouter()
const { loading: listLoading, error: listError, run } = useErrorPlaybookLoader()

const stats = ref<PlaybookStats | null>(null)
const rows = ref<PlaybookEntry[]>([])
const total = ref(0)
const pageSize = ref(50)
const offset = ref(0)

const qDraft = ref('')
const appliedQuery = ref('')
const sourceFilter = ref('')
const enabledFilter = ref<'' | 'true' | 'false'>('')

const nameSort = ref<'none' | 'asc' | 'desc'>('none')

const page = computed(() => Math.floor(offset.value / pageSize.value) + 1)
const hasFilters = computed(
  () => appliedQuery.value !== '' || sourceFilter.value !== '' || enabledFilter.value !== '',
)

/** 空态文案：区分「首次使用」「确实没有」与「被筛选条件筛掉了」。 */
const emptyText = computed(() => {
  if (hasFilters.value) return t('errorPlaybook.empty.filteredEntries')
  if (stats.value && Number(stats.value.total_entries ?? 0) === 0) {
    return t('errorPlaybook.empty.firstUseEntries')
  }
  return t('errorPlaybook.empty.noResultEntries')
})

const exec24hTotal = computed<number>(() => {
  const m = stats.value?.executions?.last_24h
  if (!m) return 0
  return Object.values(m).reduce((acc, v) => acc + Number(v || 0), 0)
})

const pendingUnknownTotal = computed<number>(() => {
  const m = stats.value?.pending_unknown
  if (!m) return 0
  return Object.values(m).reduce((acc, v) => acc + Number(v || 0), 0)
})

function nameOf(e: PlaybookEntry): string {
  return (e.title || e.error_type || '').trim()
}

/** 本地名称排序（默认不启用，点了标题列才启用）。 */
const sortedRows = computed<PlaybookEntry[]>(() => {
  if (nameSort.value === 'none') return rows.value
  const dir = nameSort.value === 'asc' ? 1 : -1
  return [...rows.value].sort((a, b) => dir * nameOf(a).localeCompare(nameOf(b)))
})

function toggleNameSort(): void {
  nameSort.value = nameSort.value === 'none' ? 'asc' : nameSort.value === 'asc' ? 'desc' : 'none'
}

function fmt(v: number | null | undefined): string {
  if (v === null || v === undefined || Number.isNaN(v)) return '—'
  return Number(v).toLocaleString()
}

function formatTime(value: string | null): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return d.toLocaleString()
}

function statusLabel(status: string): string {
  const known = ['succeeded', 'failed', 'skipped', 'awaiting_approval', 'no_action']
  return known.includes(status) ? t(`errorPlaybook.status.${status}`) : status
}

function statusClass(status: string): string {
  return `ep-chip--${status}`
}

async function load(): Promise<void> {
  const enabledParam =
    enabledFilter.value === 'true' ? true : enabledFilter.value === 'false' ? false : undefined
  const res = await run(
    () =>
      listEntries({
        q: appliedQuery.value || undefined,
        source: sourceFilter.value || undefined,
        enabled: enabledParam,
        limit: pageSize.value,
        offset: offset.value,
      }),
    'errorPlaybook.error.listFailed',
  )
  if (!res) return
  rows.value = res.entries ?? []
  total.value = res.total ?? 0

  const s = await run(getStats, 'errorPlaybook.error.statsFailed')
  if (s) stats.value = s
}

function applyFilters(): void {
  appliedQuery.value = qDraft.value.trim()
  offset.value = 0
  void load()
}

function clearFilters(): void {
  qDraft.value = ''
  appliedQuery.value = ''
  sourceFilter.value = ''
  enabledFilter.value = ''
  offset.value = 0
  void load()
}

function prevPage(): void {
  offset.value = Math.max(0, offset.value - pageSize.value)
  void load()
}

function nextPage(): void {
  if (offset.value + pageSize.value >= total.value) return
  offset.value += pageSize.value
  void load()
}

function changePageSize(size: number): void {
  pageSize.value = size
  offset.value = 0
  void load()
}

function openDetail(id: number): void {
  void router.push({ name: 'error-playbook-detail', params: { id: String(id) } })
}

// ── 新建条目 ──
const createOpen = ref(false)
const creating = ref(false)
const createError = ref('')
const createPlanEditor = ref<InstanceType<typeof PlanEditor> | null>(null)
const form = ref({
  errorType: '',
  matchPattern: '',
  title: '',
  description: '',
  rootCause: '',
  enabled: true,
  autoExecute: false,
})

function openCreate(): void {
  createError.value = ''
  createOpen.value = true
}

function closeCreate(): void {
  createOpen.value = false
}

async function submitCreate(): Promise<void> {
  createError.value = ''
  const plan = createPlanEditor.value?.getPayload()
  if (!plan) return
  if (!plan.ok) {
    createError.value = plan.error
    return
  }
  const body: EntryWriteBody = {
    error_type: form.value.errorType.trim(),
    match_pattern: form.value.matchPattern.trim(),
    title: form.value.title.trim(),
    description: form.value.description.trim(),
    root_cause: form.value.rootCause.trim(),
    plan_kind: plan.payload.plan_kind,
    plan: plan.payload.plan,
    enabled: form.value.enabled,
    auto_execute: form.value.autoExecute,
  }
  creating.value = true
  try {
    await createEntry(body)
    createOpen.value = false
    form.value = {
      errorType: '',
      matchPattern: '',
      title: '',
      description: '',
      rootCause: '',
      enabled: true,
      autoExecute: false,
    }
    await load()
  } catch (e) {
    // 后端 422 的门控原文（error_type is required / 路由未登记 …）原样透出
    createError.value = e instanceof Error ? e.message : String(e)
  } finally {
    creating.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.error-playbook-view {
  padding: 20px;
}

.ep-stats {
  margin-bottom: 12px;
}

.ep-btn {
  padding: 6px 14px;
  border-radius: var(--radius-card, 8px);
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text);
  font-size: 13px;
  cursor: pointer;
}

.ep-btn--primary {
  border-color: var(--accent);
  background: var(--accent);
  color: var(--surface);
}

.ep-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.ep-filters {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 12px;
  margin-bottom: 12px;
}

.ep-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.ep-field__label {
  color: var(--muted);
  font-size: 12px;
}

.ep-field__hint {
  color: var(--muted);
  font-size: 11px;
}

.ep-input {
  padding: 6px 10px;
  border-radius: var(--radius-card, 8px);
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text);
  font-size: 13px;
  min-width: 160px;
}

.ep-table {
  width: 100%;
  border-collapse: collapse;
}

.ep-table th {
  text-align: left;
  color: var(--muted);
  font-weight: 500;
  white-space: nowrap;
}

.ep-table td {
  color: var(--text);
  white-space: nowrap;
}

.ep-mono {
  font-family: ui-monospace, monospace;
  font-size: 12px;
}

.ep-sort {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 0;
  border: none;
  background: none;
  color: inherit;
  font: inherit;
  cursor: pointer;
}

.ep-sort__arrow {
  font-size: 10px;
}

.ep-link {
  padding: 0;
  border: none;
  background: none;
  color: var(--accent);
  font: inherit;
  cursor: pointer;
  text-align: left;
}

.ep-chip {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 11px;
  border: 1px solid var(--border);
  color: var(--muted);
}

.ep-chip--succeeded {
  border-color: var(--success, #2e7d32);
  color: var(--success, #2e7d32);
}

.ep-chip--failed {
  border-color: var(--danger, #c0392b);
  color: var(--danger, #c0392b);
}

.ep-chip--awaiting_approval {
  border-color: var(--warning, #b8860b);
  color: var(--warning, #b8860b);
}

.ep-modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.4);
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding: 40px 16px;
  overflow-y: auto;
  z-index: 100;
}

.ep-modal {
  width: 720px;
  max-width: 100%;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-card, 10px);
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.ep-modal__title {
  margin: 0;
  font-size: 15px;
}

.ep-modal__body {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.ep-form-grid {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
}

.ep-radio {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text);
  cursor: pointer;
}

.ep-modal__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

@media (max-width: 768px) {
  .error-playbook-view {
    padding: 12px;
  }
}
</style>
