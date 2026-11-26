<script setup lang="ts">
// ReconciliationReport.vue — provider/internal reconciliation layout.
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ElAlert, ElButton, ElCollapse, ElCollapseItem } from 'element-plus'
import DistributionTable from '../../components/analytics/DistributionTable.vue'
import FailureReasons from '../../components/reconciliation/FailureReasons.vue'
import ReconciliationCharts from '../../components/reconciliation/ReconciliationCharts.vue'
import ReconciliationKpiGrid from '../../components/reconciliation/ReconciliationKpiGrid.vue'
import ReconciliationToolbar from '../../components/reconciliation/ReconciliationToolbar.vue'
import DailyReconciliationPanel from '../../components/reconciliation/DailyReconciliationPanel.vue'
import { readDailyReconciliationDefault, type DailyReconciliationCalendar } from '../../api/requestReconciliation'
import { isSuperAdmin } from '../../store'
import { snapshotQueryKeys, useReconciliationPage } from './useReconciliationPage'

const route = useRoute()
const platform = computed(isSuperAdmin)
const snapshotLink = computed(() => snapshotQueryKeys.some(key => typeof route.query[key] === 'string' && route.query[key] !== ''))
const mode = ref<'pending' | 'legacy' | 'daily'>(platform.value && !snapshotLink.value ? 'pending' : 'legacy')
const showSnapshots = computed(() => mode.value === 'legacy')
const primaryCalendar = ref<DailyReconciliationCalendar | null>(null)
const policyRequest = new AbortController()
let policyTimer: ReturnType<typeof setTimeout> | undefined
let disposed = false

const {
  t, loading, dimsLoading, exporting, errorText, metric, reasonFilter, daysOpen,
  report, dims, view, filters, showCost, hasData, kxRange, coverageText, legacyDays,
  reasons, moneyMode, primaryRows, modelRows, personRows, dayRows, primaryHeaders, modelHeaders,
  personHeaders, dayHeaders, patchFilters, clearExtra, onPrimaryRow, onPersonRow, onReason,
  applyRange, exportXlsx, reload, clearReason, setView, setMetric,
} = useReconciliationPage(showSnapshots)

watch(() => snapshotQueryKeys.map(key => route.query[key]), () => {
  if (snapshotLink.value) mode.value = 'legacy'
})
onMounted(async () => {
  if (!platform.value) return
  policyTimer = setTimeout(() => {
    policyRequest.abort()
    if (mode.value === 'pending') mode.value = 'legacy'
  }, 3500)
  try {
    const calendar = await readDailyReconciliationDefault({ signal: policyRequest.signal })
    if (disposed || policyRequest.signal.aborted || !platform.value) return
    primaryCalendar.value = calendar
    if (calendar && mode.value === 'pending' && !snapshotLink.value) mode.value = 'daily'
  } catch {
    // An older or unavailable gateway keeps the snapshot view.
  } finally {
    clearTimeout(policyTimer)
    if (!disposed && mode.value === 'pending') mode.value = 'legacy'
  }
})
onBeforeUnmount(() => {
  disposed = true
  clearTimeout(policyTimer)
  policyRequest.abort()
})
</script>

<template>
  <div class="report-page">
    <div v-if="primaryCalendar && platform" class="reconciliation-mode">
      <button type="button" class="btn btn-secondary" data-testid="reconciliation-mode-daily" :aria-pressed="mode === 'daily'" @click="mode = 'daily'">{{ t('reports.daily.title') }}</button>
      <button type="button" class="btn btn-secondary" data-testid="reconciliation-mode-legacy" :aria-pressed="mode === 'legacy'" @click="mode = 'legacy'">{{ t('reports.projectedView') }}</button>
    </div>
    <p v-if="mode === 'pending'" role="status">{{ t('reports.daily.loading') }}</p>
    <ReconciliationToolbar
      v-if="showSnapshots"
      :view="view"
      :range="kxRange"
      :filters="filters"
      :dims="dims"
      :dims-loading="dimsLoading"
      :loading="loading"
      :exporting="exporting"
      :coverage="coverageText"
      @update:view="setView"
      @apply-range="applyRange"
      @patch="patchFilters"
      @refresh="reload"
      @export="exportXlsx"
      @clear="clearExtra"
    />

    <DailyReconciliationPanel
      v-if="platform && (mode === 'daily' || (showSnapshots && !primaryCalendar))"
      :primary-calendar="mode === 'daily' ? primaryCalendar ?? undefined : undefined"
    />

    <template v-if="showSnapshots">
    <el-alert v-if="errorText" :title="errorText" type="error" show-icon :closable="false" class="block" />
    <el-alert
      v-else-if="report && !hasData"
      :title="t(report.source === 'request_metrics' ? 'reports.projectedEmpty' : 'reports.noSnapshots')"
      type="info"
      show-icon
      :closable="false"
      class="block"
    />
    <el-alert
      v-else-if="legacyDays.length > 0"
      type="warning"
      show-icon
      :closable="false"
      class="block"
      :title="`${t('reports.legacyCoverage')}${legacyDays.length}${t('reports.legacyCoverageRest')}`"
    />

    <p v-if="report?.projection" class="hint">{{ t('reports.projectedBasis', { zone: report.projection.timezone }) }}</p>
    <el-alert v-if="report?.projection?.pending_since" :title="t('reports.projectedPending')" type="warning" :closable="false" show-icon class="block" />
    <p v-if="showCost && report?.totals.usd_unmeasured_requests" class="hint">{{ t('reports.projectedMissingUSD', { n: report.totals.usd_unmeasured_requests }) }}</p>
    <template v-if="report && (report.source !== 'request_metrics' || hasData)">
      <ReconciliationKpiGrid :report="report" :view="view" :show-cost="showCost" />
      <ReconciliationCharts
        :days="report.days"
        :covered-dates="report.snapshot_dates"
        :money="moneyMode"
      />

      <div class="section-head">
        <h2>{{ view === 'provider' ? t('reports.byProvider') : t('reports.byTenant') }}</h2>
        <span class="hint">{{ t('reports.distHint') }}</span>
        <span class="spacer" />
        <div class="chips">
          <button type="button" data-testid="metric-token" class="chip" :class="{ on: metric === 'token' }" @click="setMetric('token')">{{ t('reports.byToken') }}</button>
          <button type="button" data-testid="metric-money" class="chip" :class="{ on: metric === 'money' }" @click="setMetric('money')">{{ t('reports.byMoney') }}</button>
        </div>
      </div>
      <div class="dist-grid">
        <div class="panel">
          <DistributionTable
            test-id="primary-dist"
            :rows="primaryRows"
            :name-header="view === 'provider' ? t('reports.provider') : t('reports.tenant')"
            :headers="primaryHeaders"
            @row="onPrimaryRow"
          />
        </div>
        <div class="panel">
          <h3>{{ t('reports.reasonTitle') }}</h3>
          <FailureReasons :items="reasons" :active="reasonFilter" @pick="onReason" />
          <p v-if="reasonFilter" class="hint">
            {{ t('reports.reasonFilterHint') }}
            <el-button link @click="clearReason">{{ t('reports.clearFilter') }}</el-button>
          </p>
          <p v-else class="hint">{{ t('reports.reasonTotalPrefix') }} {{ report.totals.error_count }} {{ t('reports.reasonTotalSuffix') }}</p>
        </div>
      </div>

      <div class="dist-grid block">
        <div class="panel">
          <h3>{{ t('reports.byModel') }}</h3>
          <DistributionTable
            test-id="model-dist"
            :rows="modelRows"
            :name-header="t('reports.model')"
            :headers="modelHeaders"
            @row="patchFilters({ model: $event })"
          />
        </div>
        <div v-if="view === 'internal'" class="panel">
          <h3>{{ t('reports.byPerson') }}</h3>
          <DistributionTable
            test-id="person-dist"
            :rows="personRows"
            :name-header="t('reports.person')"
            :headers="personHeaders"
            @row="onPersonRow"
          />
        </div>
      </div>

      <el-collapse v-model="daysOpen" class="block">
        <el-collapse-item name="days" :title="t('reports.byDay')">
          <DistributionTable
            v-if="daysOpen.includes('days')"
            test-id="day-dist"
            :rows="dayRows"
            :show-bar="false"
            :clickable="false"
            :name-header="t('reports.date')"
            :headers="dayHeaders"
          />
        </el-collapse-item>
      </el-collapse>
    </template>
    </template>
  </div>
</template>

<style scoped>
.report-page { padding: 16px; }
.reconciliation-mode { display: flex; flex-wrap: wrap; gap: var(--space-2); }
.reconciliation-mode [aria-pressed="true"] { font-weight: 700; }
.block { margin-top: 16px; }
.section-head { display: flex; flex-wrap: wrap; gap: 8px; align-items: baseline; margin: 8px 0; }
.section-head h2, .panel h3 { margin: 0 0 8px; font-size: 16px; }
.hint { color: var(--text-muted, var(--el-text-color-secondary)); font-size: 12px; }
.spacer { flex: 1; }
.chips { display: flex; gap: 4px; }
.chip {
  border: 1px solid var(--border);
  background: transparent;
  color: inherit;
  border-radius: 999px;
  padding: 2px 8px;
  cursor: pointer;
  font: inherit;
}
.chip.on { border-color: var(--accent); color: var(--accent); }
.dist-grid {
  display: grid;
  grid-template-columns: minmax(0, 2fr) minmax(240px, 1fr);
  gap: 12px;
}
.panel {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
  background: var(--card);
  min-width: 0;
}
@media (max-width: 768px) {
  .dist-grid { grid-template-columns: 1fr; }
}
</style>
