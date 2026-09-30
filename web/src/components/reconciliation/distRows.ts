// distRows.ts — turn a RangeReport into share-bar rows for the reconciliation page.
import { UNASSIGNED_ID, type RangeReport, type ReportTotals } from '../../api/reportrollup'
import type { BarTone, DistRow } from './distTypes'
import {
  failTone,
  fmtCny,
  fmtCompact,
  fmtDuration,
  fmtInt,
  fmtPct,
  fmtUsd,
  topReasons,
} from './format'

export type Metric = 'token' | 'money'

const TONES: BarTone[] = ['primary', 'success', 'purple', 'cyan']

function metricValue(totals: ReportTotals, metric: Metric, moneyCents: boolean): number {
  if (metric === 'token') return totals.total_tokens ?? 0
  return moneyCents ? (totals.estimated_cost_cents ?? 0) : (totals.credits_charged ?? 0)
}

function requestShare(count: number, total: number): string {
  if (!(total > 0)) return '—'
  return fmtPct(count / total)
}

/** Bar width is the row's share of the range total, matching the on-page hint. */
function rangeTotal(report: RangeReport, metric: Metric, moneyCents: boolean): number {
  if (metric === 'token') return report.totals?.total_tokens ?? 0
  return moneyCents ? (report.totals?.estimated_cost_cents ?? 0) : (report.totals?.credits_charged ?? 0)
}

function barPct(value: number, total: number): number {
  if (!(total > 0)) return 0
  return (value / total) * 100
}

function moneyText(totals: ReportTotals, showCost: boolean): string {
  return showCost ? fmtUsd(totals.estimated_cost_cents) : fmtInt(totals.credits_charged)
}

export function providerDist(
  report: RangeReport,
  metric: Metric,
  showCost: boolean,
  unassigned: string,
): DistRow[] {
  const totalReq = report.totals?.request_count ?? 0
  const src = [...(report.providers ?? [])].sort((a, b) => {
    const av = metricValue(a.totals, metric, showCost)
    const bv = metricValue(b.totals, metric, showCost)
    return bv - av
  })
  const vals = src.map((row) => metricValue(row.totals, metric, showCost))
  const total = rangeTotal(report, metric, showCost)
  return src.map((row, index) => ({
    key: String(row.provider_id),
    name: row.provider_id === UNASSIGNED_ID ? unassigned : (row.provider_name || String(row.provider_id)),
    pct: barPct(vals[index], total),
    tone: TONES[index % TONES.length],
    cells: [
      { text: fmtInt(row.totals.request_count), sub: requestShare(row.totals.request_count, totalReq) },
      { text: fmtCompact(row.totals.total_tokens) },
      { text: moneyText(row.totals, showCost) },
      { text: fmtPct(row.totals.error_rate), tone: failTone(row.totals.error_rate) },
      { text: row.quality_score == null ? '—' : row.quality_score.toFixed(1) },
    ],
  }))
}

export function tenantDist(report: RangeReport, metric: Metric): DistRow[] {
  const totalReq = report.totals?.request_count ?? 0
  const src = [...(report.tenants ?? [])].sort((a, b) => {
    const av = metric === 'token' ? a.totals.total_tokens : a.totals.credits_charged
    const bv = metric === 'token' ? b.totals.total_tokens : b.totals.credits_charged
    return (bv ?? 0) - (av ?? 0)
  })
  const moneyCents = false
  const vals = src.map((row) => metricValue(row.totals, metric, moneyCents))
  const total = rangeTotal(report, metric, moneyCents)
  return src.map((row, index) => ({
    key: row.tenant_id,
    name: row.tenant_id || '—',
    pct: barPct(vals[index], total),
    tone: TONES[index % TONES.length],
    cells: [
      { text: fmtInt(row.totals.request_count), sub: requestShare(row.totals.request_count, totalReq) },
      { text: fmtCompact(row.totals.total_tokens) },
      { text: fmtInt(row.totals.credits_charged), sub: fmtCny(row.totals.internal_cost_cents) },
      { text: fmtPct(row.totals.error_rate), tone: failTone(row.totals.error_rate) },
      { text: requestShare(vals[index], total) },
    ],
  }))
}

export function modelDist(
  report: RangeReport,
  metric: Metric,
  showCost: boolean,
  reason: string | null,
): DistRow[] {
  const moneyCents = showCost && report.view !== 'internal'
  let src = [...(report.model_totals ?? report.models ?? [])]
  if (reason) src = src.filter((row) => (row.error_breakdown?.[reason] ?? 0) > 0)
  src.sort((a, b) => metricValue(b.totals, metric, moneyCents) - metricValue(a.totals, metric, moneyCents))
  const vals = src.map((row) => metricValue(row.totals, metric, moneyCents))
  const total = rangeTotal(report, metric, moneyCents)
  return src.map((row, index) => ({
    key: row.raw_model_name,
    name: row.raw_model_name || '—',
    sub: row.provider_name,
    pct: barPct(vals[index], total),
    tone: TONES[index % TONES.length],
    reasons: topReasons(row.error_breakdown, 6),
    cells: [
      { text: fmtInt(row.totals.request_count) },
      { text: fmtCompact(row.totals.total_tokens) },
      { text: moneyCents ? moneyText(row.totals, true) : fmtInt(row.totals.credits_charged) },
      { text: fmtDuration(row.totals.latency_p95_ms) },
      { text: fmtPct(row.totals.cache_hit_ratio) },
    ],
  }))
}

export function personDist(report: RangeReport): DistRow[] {
  const src = [...(report.persons ?? [])].sort(
    (a, b) => (b.totals.request_count ?? 0) - (a.totals.request_count ?? 0),
  )
  const totalReq = report.totals?.request_count ?? 0
  return src.map((row) => ({
    key: `${row.tenant_id}\u0000${row.person}`,
    name: row.person || '—',
    sub: row.tenant_id,
    pct: barPct(row.totals.request_count ?? 0, totalReq),
    tone: 'success' as const,
    cells: [
      { text: fmtInt(row.totals.request_count) },
      { text: fmtCompact(row.totals.total_tokens) },
      { text: fmtInt(row.totals.credits_charged) },
      { text: fmtCny(row.totals.internal_cost_cents) },
    ],
  }))
}

export function dayDist(report: RangeReport, showCost: boolean, uncoveredLabel?: string): DistRow[] {
  const covered = report.snapshot_dates
  const coveredSet = Array.isArray(covered) ? new Set(covered) : null
  const useCost = showCost && report.view !== 'internal'
  return (report.days ?? []).map((row) => ({
    key: row.date,
    name: row.date,
    sub: coveredSet && !coveredSet.has(row.date) && uncoveredLabel ? uncoveredLabel : undefined,
    pct: 0,
    cells: [
      { text: fmtInt(row.totals.request_count) },
      { text: fmtInt(row.totals.success_count) },
      { text: fmtInt(row.totals.error_count) },
      { text: fmtPct(row.totals.error_rate), tone: failTone(row.totals.error_rate) },
      { text: fmtCompact(row.totals.total_tokens) },
      { text: useCost ? fmtUsd(row.totals.estimated_cost_cents) : fmtInt(row.totals.credits_charged) },
    ],
  }))
}
