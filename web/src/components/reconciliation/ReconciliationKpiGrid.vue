<script setup lang="ts">
// ReconciliationKpiGrid.vue — six KPI cards for the reconciliation page.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RangeReport, ReportView } from '../../api/reportrollup'
import StatCard from '../ui/StatCard.vue'
import { fmtCny, fmtCompact, fmtDuration, fmtInt, fmtPct, fmtUsd, weightedQuality } from './format'

const props = defineProps<{
  report: RangeReport
  view: ReportView
  showCost: boolean
}>()

const { t } = useI18n()

const totals = computed(() => props.report.totals)
const days = computed(() => Math.max(1, props.report.snapshot_dates?.length || props.report.days?.length || 1))

const cards = computed(() => {
  const row = totals.value
  const requests = {
    icon: '📥',
    tone: 'neutral' as const,
    label: t('reports.requests'),
    value: fmtInt(row.request_count),
    sub: `${t('reports.success')} ${fmtInt(row.success_count)} · ${t('reports.errors')} ${fmtInt(row.error_count)} (${fmtPct(row.error_rate)})`,
  }
  const tokens = {
    icon: '🔢',
    tone: 'neutral' as const,
    label: t('reports.totalTokens'),
    value: fmtCompact(row.total_tokens),
    sub: `${t('reports.in')} ${fmtCompact(row.input_tokens)} · ${t('reports.out')} ${fmtCompact(row.output_tokens)} · ${t('reports.cacheRead')} ${fmtCompact(row.cache_read_tokens)} · ${t('reports.cacheWrite')} ${fmtCompact(row.cache_write_tokens)}`,
  }
  const sharedTail = [
    {
      icon: '⏱️',
      tone: 'warning' as const,
      label: t('reports.kpiLatency'),
      value: fmtDuration(row.latency_p50_ms),
      sub: `P95 ${fmtDuration(row.latency_p95_ms)}`,
    },
    {
      icon: '📈',
      tone: 'neutral' as const,
      label: t('reports.kpiDaily'),
      value: fmtInt((row.request_count ?? 0) / days.value),
      sub: `${t('reports.coveragePrefix')} ${props.report.snapshot_dates?.length ?? 0} ${t('reports.daysCovered')}`,
    },
  ]
  if (props.view === 'internal') {
    const persons = props.report.persons?.length ?? 0
    const perPerson = persons > 0 ? Math.round((row.request_count ?? 0) / persons) : 0
    const perReq = (row.request_count ?? 0) > 0 ? (row.credits_charged ?? 0) / row.request_count : 0
    return [
      requests,
      tokens,
      {
        icon: '🪙',
        tone: 'success' as const,
        label: t('reports.internalCredits'),
        value: fmtInt(row.credits_charged),
        sub: `${t('reports.internalCost')} ${fmtCny(row.internal_cost_cents)}`,
      },
      {
        icon: '🏠',
        tone: 'neutral' as const,
        label: t('reports.kpiTenants'),
        value: fmtInt(props.report.tenants?.length ?? 0),
        sub: (props.report.tenants ?? []).map((item) => item.tenant_id).filter(Boolean).slice(0, 3).join(' · '),
      },
      {
        icon: '👤',
        tone: 'warning' as const,
        label: t('reports.kpiPersons'),
        value: fmtInt(persons),
        sub: `${t('reports.kpiPerPersonReq')} ${fmtInt(perPerson)}`,
      },
      {
        icon: '📊',
        tone: 'neutral' as const,
        label: t('reports.kpiCreditsPerReq'),
        value: perReq.toFixed(1),
        sub: `${t('reports.cacheHit')} ${fmtPct(row.cache_hit_ratio)}`,
      },
    ]
  }
  const quality = weightedQuality(props.report.providers ?? [])
  const costCard = props.showCost
    ? {
        icon: '💸',
        tone: 'success' as const,
        label: t('reports.providerCost'),
        value: fmtUsd(row.estimated_cost_cents),
        sub: `${t('reports.cacheHit')} ${fmtPct(row.cache_hit_ratio)} · ${t('reports.cacheRead')} ${fmtCompact(row.cache_read_tokens)}`,
      }
    : {
        icon: '💸',
        tone: 'success' as const,
        label: t('reports.cacheHit'),
        value: fmtPct(row.cache_hit_ratio),
        sub: `${t('reports.cacheRead')} ${fmtCompact(row.cache_read_tokens)}`,
      }
  return [
    requests,
    tokens,
    costCard,
    {
      icon: '🏅',
      tone: 'warning' as const,
      label: t('reports.qualityScore'),
      value: quality == null ? '—' : quality.toFixed(1),
      sub: `${props.report.providers?.length ?? 0} ${t('reports.qualitySub')}`,
    },
    ...sharedTail,
  ]
})
</script>

<template>
  <div class="kpi-grid" data-testid="recon-kpi">
    <StatCard
      v-for="card in cards"
      :key="card.label"
      :icon="card.icon"
      :tone="card.tone"
      :label="card.label"
      :value="card.value"
      :sub="card.sub"
    />
  </div>
</template>

<style scoped>
.kpi-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
</style>
