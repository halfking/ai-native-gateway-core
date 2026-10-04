<script setup lang="ts">
// ReconciliationReport.vue — provider/internal reconciliation layout.
import { ElAlert, ElButton, ElCollapse, ElCollapseItem } from 'element-plus'
import DistributionTable from '../../components/analytics/DistributionTable.vue'
import FailureReasons from '../../components/reconciliation/FailureReasons.vue'
import ReconciliationCharts from '../../components/reconciliation/ReconciliationCharts.vue'
import ReconciliationKpiGrid from '../../components/reconciliation/ReconciliationKpiGrid.vue'
import ReconciliationToolbar from '../../components/reconciliation/ReconciliationToolbar.vue'
import { useReconciliationPage } from './useReconciliationPage'

const {
  t, loading, dimsLoading, exporting, rerunning, errorText, metric, reasonFilter, daysOpen,
  report, dims, view, filters, showCost, hasData, kxRange, coverageText, legacyDays,
  reasons, moneyMode, primaryRows, modelRows, personRows, dayRows, primaryHeaders, modelHeaders,
  personHeaders, dayHeaders, patchFilters, clearExtra, onPrimaryRow, onPersonRow, onReason,
  applyRange, exportXlsx, rerunEndDay, reload, clearReason, setView, setMetric,
} = useReconciliationPage()
</script>

<template>
  <div class="report-page">
    <ReconciliationToolbar
      :view="view"
      :range="kxRange"
      :filters="filters"
      :dims="dims"
      :dims-loading="dimsLoading"
      :loading="loading"
      :exporting="exporting"
      :rerunning="rerunning"
      :coverage="coverageText"
      @update:view="setView"
      @apply-range="applyRange"
      @patch="patchFilters"
      @refresh="reload"
      @export="exportXlsx"
      @rerun="rerunEndDay"
      @clear="clearExtra"
    />

    <el-alert v-if="errorText" :title="errorText" type="error" show-icon :closable="false" class="block" />
    <el-alert
      v-else-if="report && !hasData"
      :title="t('reports.noSnapshots')"
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

    <template v-if="report">
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
  </div>
</template>

<style scoped>
.report-page { padding: 16px; }
.block { margin-top: 16px; }
.section-head { display: flex; flex-wrap: wrap; gap: 8px; align-items: baseline; margin: 8px 0; }
.section-head h2, .panel h3 { margin: 0 0 8px; font-size: 16px; }
.hint { color: var(--text-muted, var(--el-text-color-secondary)); font-size: 12px; }
.spacer { flex: 1; }
.chips { display: flex; gap: 6px; }
.chip {
  border: 1px solid var(--border);
  background: transparent;
  color: inherit;
  border-radius: 999px;
  padding: 2px 10px;
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
