// useReconciliationPage.ts — query, filters, and derived rows for the reconciliation page.
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  downloadReportExport,
  getReportDimensions,
  getReportSummary,
  runReportRollup,
  type DimensionOptions,
  type RangeReport,
  type ReportFilter,
  type ReportView,
} from '../../api/reportrollup'
import { isPlatformOpsView } from '../../store'
import { dayDist, modelDist, personDist, providerDist, tenantDist, type Metric } from '../../components/reconciliation/distRows'
import { topReasons } from '../../components/reconciliation/format'
import type { KxDateRange } from '../../components/ui/kx-date-types'
import { defaultSnapshotRange } from '../../components/reconciliation/snapshotRange'

function numQuery(value: unknown): number | undefined {
  if (typeof value !== 'string' || value === '') return undefined
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : undefined
}

function cleanFilters(input: ReportFilter): ReportFilter {
  const next: ReportFilter = {}
  if (input.provider_id != null && !Number.isNaN(input.provider_id)) next.provider_id = input.provider_id
  if (input.credential_id != null && !Number.isNaN(input.credential_id)) next.credential_id = input.credential_id
  if (input.api_key_id != null && !Number.isNaN(input.api_key_id)) next.api_key_id = input.api_key_id
  if (input.tenant_id) next.tenant_id = input.tenant_id
  if (input.person) next.person = input.person
  if (input.model) next.model = input.model
  return next
}

export function useReconciliationPage() {
  const { t } = useI18n()
  const route = useRoute()
  const router = useRouter()

  const loading = ref(false)
  const dimsLoading = ref(false)
  const exporting = ref(false)
  const rerunning = ref(false)
  const errorText = ref('')
  const metric = ref<Metric>('token')
  const reasonFilter = ref<string | null>(null)
  const daysOpen = ref<string[]>([])
  const report = ref<RangeReport | null>(null)
  const dims = ref<DimensionOptions | null>(null)
  const view = ref<ReportView>(route.query.view === 'internal' ? 'internal' : 'provider')
  const range = ref<[string, string]>(defaultSnapshotRange())
  const filters = ref<ReportFilter>({
    provider_id: numQuery(route.query.provider_id),
    model: typeof route.query.model === 'string' ? route.query.model : undefined,
    tenant_id: typeof route.query.tenant_id === 'string' ? route.query.tenant_id : undefined,
    person: typeof route.query.person === 'string' ? route.query.person : undefined,
  })
  let fetchGen = 0
  let applyingRoute = false

  const showCost = computed(() => isPlatformOpsView())
  const hasData = computed(() => !!report.value && report.value.snapshot_dates.length > 0)
  const kxRange = computed(() => ({ start: range.value[0], end: range.value[1] }))
  const coverageText = computed(() => {
    if (!report.value) return ''
    return `${t('reports.coveragePrefix')} ${report.value.snapshot_dates.length} ${t('reports.daysCovered')}`
  })
  const legacyDays = computed(() => report.value?.coverage?.legacy_dates ?? [])
  const reasons = computed(() => topReasons(report.value?.error_breakdown, 8))
  const moneyMode = computed(() => (showCost.value && view.value === 'provider' ? 'cost' : 'credits'))
  const primaryRows = computed(() => {
    if (!report.value) return []
    return view.value === 'provider'
      ? providerDist(report.value, metric.value, showCost.value, t('reports.unassigned'))
      : tenantDist(report.value, metric.value)
  })
  const modelRows = computed(() =>
    report.value ? modelDist(report.value, metric.value, showCost.value, reasonFilter.value) : [],
  )
  const personRows = computed(() => (report.value && view.value === 'internal' ? personDist(report.value) : []))
  const dayRows = computed(() => (
    report.value ? dayDist(report.value, showCost.value, t('reports.unaggregatedDay')) : []
  ))
  const moneyHeader = computed(() => (moneyMode.value === 'cost' ? t('reports.legendCost') : t('reports.internalCredits')))
  const primaryHeaders = computed(() =>
    view.value === 'provider'
      ? [t('reports.requests'), t('reports.totalTokens'), moneyHeader.value, t('reports.errorRate'), t('reports.qualityScore')]
      : [t('reports.requests'), t('reports.totalTokens'), t('reports.internalCredits'), t('reports.errorRate'), t('reports.sharePct')],
  )
  const modelHeaders = computed(() => [t('reports.requests'), t('reports.totalTokens'), moneyHeader.value, 'P95', t('reports.cacheHit')])
  const personHeaders = computed(() => [t('reports.requests'), t('reports.totalTokens'), t('reports.internalCredits'), t('reports.internalCost')])
  const dayHeaders = computed(() => [
    t('reports.requests'), t('reports.success'), t('reports.errors'), t('reports.errorRate'), t('reports.totalTokens'), moneyHeader.value,
  ])

  async function refresh() {
    const gen = ++fetchGen
    loading.value = true
    errorText.value = ''
    try {
      const next = await getReportSummary({
        start: range.value[0],
        end: range.value[1],
        view: view.value,
        detail: false,
        ...cleanFilters(filters.value),
      })
      if (gen !== fetchGen) return
      report.value = next
    } catch (err: unknown) {
      if (gen !== fetchGen) return
      errorText.value = err instanceof Error ? err.message : String(err)
      report.value = null
    } finally {
      if (gen === fetchGen) loading.value = false
    }
  }

  async function refreshDimensions() {
    dimsLoading.value = true
    try {
      dims.value = await getReportDimensions({ start: range.value[0], end: range.value[1], view: view.value })
    } catch {
      dims.value = null
    } finally {
      dimsLoading.value = false
    }
  }

  function reload() {
    void Promise.all([refresh(), refreshDimensions()])
  }

  function patchFilters(partial: Partial<ReportFilter>) {
    filters.value = cleanFilters({ ...filters.value, ...partial })
  }

  function clearExtra() {
    filters.value = cleanFilters({ provider_id: filters.value.provider_id, model: filters.value.model })
  }

  function onPrimaryRow(key: string) {
    if (view.value === 'provider') patchFilters({ provider_id: Number(key) })
    else patchFilters({ tenant_id: key })
  }

  function onPersonRow(key: string) {
    const [tenant, person] = key.split('\u0000')
    patchFilters({ tenant_id: tenant || undefined, person: person || undefined })
  }

  function onReason(code: string) {
    reasonFilter.value = reasonFilter.value === code ? null : code
  }

  function clearReason() {
    reasonFilter.value = null
  }

  function setView(next: ReportView) {
    view.value = next
  }

  function setMetric(next: Metric) {
    metric.value = next
  }

  function applyRange(value: KxDateRange) {
    range.value = [value.start, value.end]
    reload()
  }

  async function exportXlsx() {
    exporting.value = true
    try {
      await downloadReportExport({
        start: range.value[0],
        end: range.value[1],
        view: view.value,
        detail: false,
        group: view.value === 'provider' ? 'provider' : 'tenant',
        ...cleanFilters(filters.value),
      })
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : String(err)
      ElMessage.error(`${t('common.exportFailed')}: ${message}`)
    } finally {
      exporting.value = false
    }
  }

  async function rerunEndDay() {
    rerunning.value = true
    try {
      const res = await runReportRollup(range.value[1] ?? '')
      ElMessage.success(`${t('reports.rerunDone')}: ${res.date} rows=${res.rows_written}`)
      reload()
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : String(err)
      ElMessage.error(`${t('reports.rerunFailed')}: ${message}`)
    } finally {
      rerunning.value = false
    }
  }

  function currentQuery(): Record<string, string> {
    const query: Record<string, string> = { view: view.value }
    if (filters.value.provider_id != null) query.provider_id = String(filters.value.provider_id)
    if (filters.value.model) query.model = filters.value.model
    if (filters.value.tenant_id) query.tenant_id = filters.value.tenant_id
    if (filters.value.person) query.person = filters.value.person
    return query
  }

  function sameQuery(query: Record<string, string>): boolean {
    const keys = ['view', 'provider_id', 'model', 'tenant_id', 'person'] as const
    return keys.every((key) => String(route.query[key] ?? '') === (query[key] ?? ''))
  }

  function syncQuery() {
    if (applyingRoute) return
    const query = currentQuery()
    if (sameQuery(query)) return
    if (typeof router.push === 'function') void router.push({ query })
  }

  function applyRouteQuery() {
    const nextView: ReportView = route.query.view === 'internal' ? 'internal' : 'provider'
    const nextPid = numQuery(route.query.provider_id)
    const nextModel = typeof route.query.model === 'string' ? route.query.model : undefined
    const nextTenant = typeof route.query.tenant_id === 'string' ? route.query.tenant_id : undefined
    const nextPerson = typeof route.query.person === 'string' ? route.query.person : undefined
    const viewChanged = view.value !== nextView
    const filterChanged = filters.value.provider_id !== nextPid
      || (filters.value.model ?? undefined) !== nextModel
      || (filters.value.tenant_id ?? undefined) !== nextTenant
      || (filters.value.person ?? undefined) !== nextPerson
    if (!viewChanged && !filterChanged) return
    applyingRoute = true
    if (viewChanged) {
      reasonFilter.value = null
      view.value = nextView
    }
    if (filterChanged) {
      filters.value = cleanFilters({
        ...filters.value,
        provider_id: nextPid,
        model: nextModel,
        tenant_id: nextTenant,
        person: nextPerson,
      })
    }
    applyingRoute = false
    reload()
  }

  watch(view, () => {
    if (applyingRoute) return
    reasonFilter.value = null
    reload()
    syncQuery()
  }, { flush: 'sync' })
  watch(filters, () => {
    if (applyingRoute) return
    void refresh()
    syncQuery()
  }, { deep: true, flush: 'sync' })
  watch(
    () => [route.query.view, route.query.provider_id, route.query.model, route.query.tenant_id, route.query.person] as const,
    () => applyRouteQuery(),
  )

  onMounted(reload)

  return {
    t, loading, dimsLoading, exporting, rerunning, errorText, metric, reasonFilter, daysOpen,
    report, dims, view, filters, showCost, hasData, kxRange, coverageText, legacyDays,
    reasons, moneyMode, primaryRows, modelRows, personRows, dayRows, primaryHeaders, modelHeaders,
    personHeaders, dayHeaders, patchFilters, clearExtra, onPrimaryRow, onPersonRow, onReason,
    applyRange, exportXlsx, rerunEndDay, reload, clearReason, setView, setMetric,
  }
}
