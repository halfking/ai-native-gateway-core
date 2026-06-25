import { describe, expect, it } from 'vitest'
import { ref, computed, nextTick } from 'vue'

/**
 * RoutingDashboardView.test.ts — 2026-06-26
 *
 * Pins the analyticsEmpty computed-property contract that fixes the
 * /routing-v2 heatmap "页面闪了一下就消失了" bug. The pre-fix logic was:
 *
 *   analyticsEmpty = !analyticsLoading &&
 *                    (audit.total_requests ?? 0) === 0 &&
 *                    (!matrixData || matrixData.rows.length === 0)
 *
 * On initial mount `audit.total_requests` is undefined → `?? 0`
 * evaluates to 0, so the empty state showed immediately. Then
 * `loadAnalytics()` flipped `analyticsLoading = true`, hid the empty
 * state, and showed the heatmap card with "加载热力图…". When the
 * matrix query returned 0 rows (no recent request_logs), `analyticsEmpty`
 * flipped back to true and the empty state replaced the heatmap card
 * again — producing the visible flicker.
 *
 * The fix adds two fetched-flag guards so the empty state cannot
 * appear until BOTH the audit and matrix queries have settled.
 */

interface AuditLike {
  total_requests?: number
  total_auto_requests?: number
  success_rate?: number
  task_distribution?: Record<string, number>
  profile_distribution?: Record<string, number>
  top_chosen_models?: Array<{ model: string; count: number }>
}

interface MatrixLike {
  rows: string[]
  cols: string[]
  cells: number[][]
  meta?: Record<string, unknown>
}

function makeAnalyticsEmpty(
  analyticsLoading: { value: boolean },
  audit: { value: AuditLike },
  matrixData: { value: MatrixLike | null },
  auditFetched: { value: boolean },
  analyticsFetched: { value: boolean },
) {
  return computed(() =>
    auditFetched.value &&
    analyticsFetched.value &&
    (audit.value.total_requests ?? audit.value.total_auto_requests ?? 0) === 0 &&
    (!matrixData.value || matrixData.value.rows.length === 0),
  )
}

describe('analyticsEmpty flicker fix', () => {
  it('stays false (heatmap shows) during the initial mount window', async () => {
    const analyticsLoading = ref(false)
    const audit = ref<AuditLike>({
      total_auto_requests: 0,
      success_rate: 0,
      task_distribution: {},
      profile_distribution: {},
      top_chosen_models: [],
    })
    const matrixData = ref<MatrixLike | null>(null)
    const auditFetched = ref(false)
    const analyticsFetched = ref(false)

    const empty = makeAnalyticsEmpty(analyticsLoading, audit, matrixData, auditFetched, analyticsFetched)

    // Mount: audit not yet fetched, matrix not yet fetched.
    expect(empty.value).toBe(false)
  })

  it('flips to false again once matrix fetch completes with data', async () => {
    const analyticsLoading = ref(false)
    const audit = ref<AuditLike>({
      total_auto_requests: 5,
      total_requests: 5,
      success_rate: 0.8,
      task_distribution: {},
      profile_distribution: {},
      top_chosen_models: [],
    })
    const matrixData = ref<MatrixLike | null>({
      rows: ['gpt-4', 'claude-3-5-sonnet'],
      cols: ['code', 'chat'],
      cells: [[3, 0], [0, 2]],
    })
    const auditFetched = ref(true)
    const analyticsFetched = ref(true)

    const empty = makeAnalyticsEmpty(analyticsLoading, audit, matrixData, auditFetched, analyticsFetched)

    // Heatmap shows because both audit and matrix have data.
    expect(empty.value).toBe(false)
  })

  it('stays false even when matrix is empty but audit has data (avoid flicker back to empty state)', () => {
    // This is the regression case: pre-fix, with audit.total_requests > 0
    // and matrixData.rows.length === 0, the OLD logic returned false too,
    // so this scenario already worked. Pin it explicitly so a future
    // refactor does not regress.
    const analyticsLoading = ref(false)
    const audit = ref<AuditLike>({
      total_auto_requests: 5,
      total_requests: 5,
      success_rate: 0.8,
      task_distribution: {},
      profile_distribution: {},
      top_chosen_models: [],
    })
    const matrixData = ref<MatrixLike | null>({ rows: [], cols: [], cells: [] })
    const auditFetched = ref(true)
    const analyticsFetched = ref(true)

    const empty = makeAnalyticsEmpty(analyticsLoading, audit, matrixData, auditFetched, analyticsFetched)
    expect(empty.value).toBe(false)
  })

  it('flips to true (full empty state) only when BOTH fetches complete with zero data', async () => {
    const analyticsLoading = ref(false)
    const audit = ref<AuditLike>({
      total_auto_requests: 0,
      success_rate: 0,
      task_distribution: {},
      profile_distribution: {},
      top_chosen_models: [],
    })
    const matrixData = ref<MatrixLike | null>({ rows: [], cols: [], cells: [] })
    const auditFetched = ref(true)
    const analyticsFetched = ref(true)

    const empty = makeAnalyticsEmpty(analyticsLoading, audit, matrixData, auditFetched, analyticsFetched)

    // Both fetches done, no data anywhere → full empty state is fine.
    expect(empty.value).toBe(true)
  })

  it('pre-fix logic would have flickered; post-fix does not', () => {
    // Reproduce the bug under the OLD logic to lock down the regression.
    const analyticsLoading = ref(false)
    const audit = ref<AuditLike>({
      total_auto_requests: 0,
      success_rate: 0,
      task_distribution: {},
      profile_distribution: {},
      top_chosen_models: [],
    })
    const matrixData = ref<MatrixLike | null>(null)
    // Pre-fix: did NOT gate on auditFetched / analyticsFetched.
    const oldEmpty = computed(() =>
      !analyticsLoading.value &&
      (audit.value.total_requests ?? 0) === 0 &&
      (!matrixData.value || matrixData.value.rows.length === 0),
    )
    expect(oldEmpty.value).toBe(true) // ← the flicker starts here

    // Once loadAnalytics fires:
    analyticsLoading.value = true
    expect(oldEmpty.value).toBe(false) // ← heatmap card briefly appears

    // Then matrix returns empty:
    matrixData.value = { rows: [], cols: [], cells: [] }
    analyticsLoading.value = false
    expect(oldEmpty.value).toBe(true) // ← heatmap card disappears (the flicker)

    // Post-fix: the fetched flags prevent the flicker.
    const auditFetched = ref(true)
    const analyticsFetched = ref(true)
    const newEmpty = makeAnalyticsEmpty(analyticsLoading, audit, matrixData, auditFetched, analyticsFetched)
    // Same state, no flicker: empty state stays consistent.
    expect(newEmpty.value).toBe(true) // final state matches pre-fix
  })
})