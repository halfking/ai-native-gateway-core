<script setup lang="ts">
// ModelIntegrityView.vue — 模型完整性监控
//
// Mirrors FormatAnomaliesView's layout / interaction (KPI cards,
// filters, paginated table, modal detail drawer, resolve action) but
// is backed by web/src/api/integrity.ts (the /api/admin/model-integrity
// endpoints). It adds a fingerprint-drift tab, which the format
// anomalies view has no equivalent for.
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '../utils/datetime'
import { localeRef } from '../i18n'
import { computed, onMounted, ref } from 'vue'
import { useWindowClass } from '../composables/useWindowClass'
import ResponsiveDataView from '../components/ui/ResponsiveDataView.vue'
import HyperLoadMore from '../components/ui/HyperLoadMore.vue'
import { createHyperPages } from '../lib/shell/hyper/hyperPages'
import type { CardField, CardTone } from '../components/ui/CardList.vue'
import {
  getModelIntegrityEvents,
  getModelIntegritySummary,
  getModelIntegrityFingerprintDrift,
  resolveModelIntegrity,
  type ModelIntegrityRecord,
  type ModelIntegritySummary,
} from '../api/integrity'
import { isSuperAdmin } from '../store'
import ModelPicker from '../components/ModelPicker.vue'
import ProviderPicker from '../components/ProviderPicker.vue'
import AnomalyTypePicker, { type AnomalyTypeOption } from '../components/AnomalyTypePicker.vue'

const { t } = useI18n()
const { isCompact } = useWindowClass()

type Tab = 'events' | 'drift'
const tab = ref<Tab>('events')

const events = ref<ModelIntegrityRecord[]>([])
const summaries = ref<ModelIntegritySummary[]>([])
const driftEvents = ref<ModelIntegrityRecord[]>([])
const loading = ref(false)
const summaryLoading = ref(false)
const driftLoading = ref(false)
const error = ref<string | null>(null)

const providerFilter = ref('')
const modelFilter = ref('')
const anomalyTypeFilter = ref('')
const severityFilter = ref('')
const unresolvedOnly = ref(true)
const page = ref(1)
const pageSize = ref(50)
const total = ref(0)
const summaryHours = ref(24)
const driftDays = ref(7)

const selected = ref<ModelIntegrityRecord | null>(null)
const resolutionNotes = ref('')
const resolving = ref(false)

// Integrity anomaly types come from domains/streaming/integrity/signals.go.
const anomalyTypeLabels: Record<string, string> = {
  model_mismatch: t('modelIntegrityView.anomalyType.model_mismatch'),
  finish_refusal: t('modelIntegrityView.anomalyType.finish_refusal'),
  finish_truncation: t('modelIntegrityView.anomalyType.finish_truncation'),
  token_arith_fail: t('modelIntegrityView.anomalyType.token_arith_fail'),
  empty_response: t('modelIntegrityView.anomalyType.empty_response'),
  repeated_content: t('modelIntegrityView.anomalyType.repeated_content'),
  fingerprint_drift: t('modelIntegrityView.anomalyType.fingerprint_drift'),
}

const anomalyTypeOptions: AnomalyTypeOption[] = [
  { value: '', label: t('modelIntegrityView.anomalyType.all') },
  { value: 'model_mismatch', label: t('modelIntegrityView.anomalyType.model_mismatch'), description: t('modelIntegrityView.anomalyTypeDescription.model_mismatch') },
  { value: 'finish_refusal', label: t('modelIntegrityView.anomalyType.finish_refusal'), description: t('modelIntegrityView.anomalyTypeDescription.finish_refusal') },
  { value: 'finish_truncation', label: t('modelIntegrityView.anomalyType.finish_truncation'), description: t('modelIntegrityView.anomalyTypeDescription.finish_truncation') },
  { value: 'token_arith_fail', label: t('modelIntegrityView.anomalyType.token_arith_fail'), description: t('modelIntegrityView.anomalyTypeDescription.token_arith_fail') },
  { value: 'empty_response', label: t('modelIntegrityView.anomalyType.empty_response'), description: t('modelIntegrityView.anomalyTypeDescription.empty_response') },
  { value: 'repeated_content', label: t('modelIntegrityView.anomalyType.repeated_content'), description: t('modelIntegrityView.anomalyTypeDescription.repeated_content') },
  { value: 'fingerprint_drift', label: t('modelIntegrityView.anomalyType.fingerprint_drift'), description: t('modelIntegrityView.anomalyTypeDescription.fingerprint_drift') },
]

const severityOptions = [
  { value: '', label: t('modelIntegrityView.severity.all') },
  { value: 'low', label: t('modelIntegrityView.severity.low') },
  { value: 'medium', label: t('modelIntegrityView.severity.medium') },
  { value: 'high', label: t('modelIntegrityView.severity.high') },
  { value: 'critical', label: t('modelIntegrityView.severity.critical') },
]

const severityLabels: Record<string, string> = {
  low: t('modelIntegrityView.severity.low'),
  medium: t('modelIntegrityView.severity.medium'),
  high: t('modelIntegrityView.severity.high'),
  critical: t('modelIntegrityView.severity.critical'),
}

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))
const offset = computed(() => (page.value - 1) * pageSize.value)
const totalEvents = computed(() => summaries.value.reduce((sum, s) => sum + s.anomaly_count, 0))
const unresolvedEvents = computed(() => summaries.value.reduce((sum, s) => sum + (s.anomaly_count - s.resolved_count), 0))
const criticalEvents = computed(() => summaries.value.filter((s) => s.severity === 'critical').reduce((sum, s) => sum + s.anomaly_count, 0))

/**
 * ── H6 第十八条切片（2026-10-06）：两张原生表接 compact 卡片 + **首条按 13 §7 走连续加载** ──
 *
 * 两个 Tab（`events` / `drift`）互斥，各一张原生表：
 * **T1 events 8 列 · 桌面分页（`limit` + `offset` 真进请求）**，
 * T2 drift 6 列 · **不分页**（`loadDrift` 没有 limit/offset）。
 * ⇒ **只有 T1 需要连续加载**，T2 与切片十七同形（只改呈现形态）。
 *
 * ## `loadFirst()` 与 `refresh()` 是两回事，不要合并
 *
 * `applyFilters`（查询条件变了）⇒ `loadFirst()`，它会 `resetInternal()` 清空累积行
 * —— 否则**不匹配新条件的旧行会留在屏幕上**。
 * `refreshAll`（用户点刷新）⇒ `refresh()`，它是**保旧刷新**：旧内容留到新数据就绪，
 * 手机上不会白一下。这个区别不是洁癖，是两个场景的诉求本来就不同。
 *
 * ## `pageSize` 同源（13 §7）
 *
 * `pageSize = ref(50)` 且**全文没有任何写入方**（实测：只有初始化 + 三处读）
 * ⇒ `COMPACT_PAGE_SIZE = 50` 与它同源。若哪天本页加了「每页条数」选择器，
 * 两条路径就会发出不同的 `limit` ⇒ 短页判据读的是配置里那个 ⇒ **长列表静默截断**。
 * 门禁断「`pageSize` 没有 `.value =` 写入」+「两处字面量都是 50」。
 *
 * ## ★ `.table-wrap` 只能**摘掉 overflow**，不能整类删（本切片与切片十七的差异）
 *
 * 本页 `.table-wrap` 是 `overflow: auto` **+ `border` + `border-radius` + `background`**
 * —— 它同时是**视觉框**。切片十七那 5 处 `.table-wrap` 只有 `overflow-x: auto`，
 * 所以能整类删；这里整类删会把桌面的圆角边框一起删掉 = 桌面视觉变更。
 * ⇒ **只摘 `overflow`**，框与圆角留在原地，横滚交给容器。
 * 这是「删掉自带 overflow-x 的包裹 div」这条规则的**必要修正**：
 * 判据不是「有没有 overflow」，是「除 overflow 外它还带不带别的东西」。
 *
 * ## ★ 本页**不传** `table-min-width` —— 传了是空操作（同 D12，但成因不同）
 *
 * 容器规则是 `.responsive-data-view__table > :slotted(table)`，命中要求 `<table>` 是
 * **直接子元素**。本页为了让视觉框留在原地，`.table-wrap` 那个 div 仍在
 * ⇒ 直接子元素是 `div.table-wrap`，**`:slotted(table)` 不命中**。
 * D12 那条是「el-table 的根元素是 div」，这里是「我们自己保留了一个 div」——
 * **同一个后果、两个不同成因**。⇒ 与其传一个不生效的值再注释「已设」，
 * 这里**不传**并把成因写清楚。窄屏列宽下限归内容（`.table` 是 `width: 100%`、无 min-width）。
 *
 * ## 徽章 → tone：仍然从 `<style>` 的实际色值反查（同切片十七）
 *
 * | 类 | CSS | tone |
 * | --- | --- | --- |
 * | `badge-critical` | `--danger-bd` | danger |
 * | `badge-high` | `--warning-bd` | warn |
 * | `badge-medium` | `--warning-bd` | warn |
 * | `badge-low` | `--accent-h`（= primary） | neutral |
 * | `status-ok` | `--success` | good |
 * | `status-warn` | `--warning` | warn |
 *
 * ★ **`badge-high` 与 `badge-medium` 收敛成同一档** —— 它们在桌面上
 * **只差背景**（`--warning-bd` vs `--warning-bg`），文字色是同一个。
 * ⇒ 卡片侧只能给 warn。**精确等级不丢**：等级字面量（critical/high/medium/low）
 * 本身就在卡片上，色只是第二层信息。
 * `badge-low` 落在 `--accent-h`（primary）⇒ `CardTone` 无 primary 档 ⇒ neutral，不新造。
 *
 * ## 卡头出**完整 `request_id`**，不跟桌面一起截断到 18 字符
 *
 * 同切片十五的 `session_id`：桌面的 `truncate(request_id, 18)` 是为**窄列**做的，
 * 卡片头承担「唯一句柄」职责。而且 `request_id` **可选**（缺值时桌面出 `—`），
 * 拿它当 `:key` 会退化到下标。⇒ 键用 `id`（后端主键，非可选），脸用完整 `request_id`。
 * `actual_value` 的 20 字符截断**保留**（它不是身份，是普通字段，截断与桌面同口径）。
 *
 * ## `:loading` 带 `isCompact` 前置（本页两张表桌面都有 tbody 内的加载行）
 *
 * 桌面 T1 有 `<tr v-if="loading"><td colspan="8" class="empty">`，
 * T2 有 `driftLoading` 的同类行 ⇒ 不带前置的 `:loading` 会把桌面整张表换成骨架。
 * `:empty` 同理要带 `isCompact &&`，并**排除 busy 与 failed**（13 §7「失败态不显示空态」）。
 */
function severityClass(severity: string) {
  switch (severity) {
    case 'critical':
      return 'badge-critical'
    case 'high':
      return 'badge-high'
    case 'medium':
      return 'badge-medium'
    default:
      return 'badge-low'
  }
}

function fmtTime(value?: string) {
  if (!value) return '—'
  return formatDateTime(value, {
    locale: localeRef.value,
    options: {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    },
  })
}

/** CSS 类名 → 卡片 tone。按 `<style>` 里每类的 `color` 反查，非按类名猜。 */
const TONE_BY_BADGE_CLASS: Record<string, CardTone> = {
  'badge-critical': 'danger',
  'badge-high': 'warn',
  'badge-medium': 'warn',
  'badge-low': 'neutral',
  'status-ok': 'good',
  'status-warn': 'warn',
}

function badgeTone(cls: string): CardTone {
  return TONE_BY_BADGE_CLASS[cls] ?? 'neutral'
}

/** 桌面这一列是 `<code>{{ truncate(request_id, 18) }}</code>`；卡头给完整值（见上方说明）。 */
const recordTitle = (row: Record<string, unknown>) =>
  row.request_id == null || row.request_id === '' ? undefined : String(row.request_id)

/** T1 events 表。8 列 − request_id(卡头) − 操作列 = 6 个字段。 */
const eventFields = computed<CardField[]>(() => [
  { key: 'detected_at', label: t('modelIntegrityView.table.detectedAt'), format: (v) => fmtTime(v == null ? undefined : String(v)) },
  {
    key: 'severity', label: t('modelIntegrityView.table.severity'), type: 'badge',
    format: (v) => (severityLabels[String(v ?? '')] || String(v ?? '')),
    tone: (row) => badgeTone(severityClass(String(row.severity ?? ''))),
  },
  { key: 'anomaly_type', label: t('modelIntegrityView.table.anomalyType'), format: (v) => (anomalyTypeLabels[String(v ?? '')] || String(v ?? '')) },
  { key: 'provider_code', label: t('modelIntegrityView.table.providerModel') },
  { key: 'actual_value', label: t('modelIntegrityView.table.actual'), format: (v) => (v == null || v === '' ? undefined : truncate(String(v), 20)) },
  {
    // ★ `type: 'badge'` 不是装饰：`CardList.fieldTone` 只在 `metric` / `badge` 时读 `tone`，
    //   漏了它 `tone` 会被**静默丢弃**（`data-tone` 属性整个不渲染）。
    //   桌面上这一列是带 `status-ok` / `status-warn` 色的 span，卡片侧必须同口径。
    key: 'resolved', label: t('modelIntegrityView.table.status'), type: 'badge',
    format: (_v, row) => (row.resolved ? t('modelIntegrityView.status.resolved') : t('modelIntegrityView.status.unresolved')),
    tone: (row) => badgeTone(row.resolved ? 'status-ok' : 'status-warn'),
  },
])

/** T2 drift 表。6 列 − request_id(卡头) − 操作列 = 4 个字段。 */
const driftFields = computed<CardField[]>(() => [
  { key: 'detected_at', label: t('modelIntegrityView.table.detectedAt'), format: (v) => fmtTime(v == null ? undefined : String(v)) },
  {
    key: 'severity', label: t('modelIntegrityView.table.severity'), type: 'badge',
    format: (v) => (severityLabels[String(v ?? '')] || String(v ?? '')),
    tone: (row) => badgeTone(severityClass(String(row.severity ?? ''))),
  },
  { key: 'provider_code', label: t('modelIntegrityView.table.providerModel') },
  // ★ 24 不是 20：桌面 T2 这一列是 `truncate(item.actual_value, 24)`。
  //   写成 20 会让同一页的两张表对**同一个字段名**给出两个不同的截断口径，
  //   而卡片与桌面同口径是硬规则（T1 那边是 20，两张表本来就不同，别抄串）。
  { key: 'actual_value', label: t('modelIntegrityView.table.actual'), format: (v) => (v == null || v === '' ? undefined : truncate(String(v), 24)) },
])

function truncate(value: string | undefined, n: number) {
  if (!value) return '—'
  return value.length > n ? value.slice(0, n) + '...' : value
}

/**
 * 查询条件的**唯一真源**。桌面分页与 compact 连续加载共用它 ——
 * 写两遍的话，改了筛选口径只有一边生效，且不会报错。
 */
function filterBody(): Record<string, unknown> {
  return {
    provider: providerFilter.value || undefined,
    model: modelFilter.value || undefined,
    anomaly_type: anomalyTypeFilter.value || undefined,
    severity: severityFilter.value || undefined,
    unresolved_only: unresolvedOnly.value,
  }
}

async function load() {
  loading.value = true
  error.value = null
  try {
    const resp = await getModelIntegrityEvents({
      ...filterBody(),
      limit: pageSize.value,
      offset: offset.value,
    })
    events.value = resp.events
    total.value = resp.count
  } catch (err: any) {
    error.value = err?.message || t('modelIntegrityView.error.loadFailed')
  } finally {
    loading.value = false
  }
}

/**
 * compact 的连续加载。`pageSize` **必须与 `fetchPage` 里发出的 `limit` 同值**
 * （13 §7：短页判据 `result.rows.length < opts.pageSize` 读的是**配置里那个**，
 * 两者不一致时长列表会被**静默截断**）。
 * 本页的 `pageSize` 是 `ref(50)` 且**全文没有任何写入方**（已实测：只有初始化 + 三处读），
 * 所以 `COMPACT_PAGE_SIZE = 50` 与它是同源的。门禁断「`pageSize` 没有 `.value =` 写入」
 * 与「两处字面量都是 50」—— 哪天有人加了「每页条数」选择器，这条会红。
 */
const COMPACT_PAGE_SIZE = 50

const continuous = createHyperPages<ModelIntegrityRecord>({
  pageSize: COMPACT_PAGE_SIZE,
  // `id` 是后端主键。★ 不能用 `request_id`：它可选、且桌面截断到 18 字符，必然撞。
  rowKey: (r) => r.id,
  fetchPage: async (p) => {
    try {
      const resp = await getModelIntegrityEvents({
        ...filterBody(),
        limit: COMPACT_PAGE_SIZE,
        offset: (p - 1) * COMPACT_PAGE_SIZE,
      })
      total.value = resp.count
      return { rows: resp.events || [], total: resp.count }
    } catch (err: unknown) {
      error.value = err instanceof Error ? err.message : t('modelIntegrityView.error.loadFailed')
      // 必须继续抛出：`createHyperPages` 靠它把 state 置成 failed，
      // 而 `:empty` 的判据要靠 failed 挡（13 §7「失败态不显示空态」）。吞掉的话 state 停在 idle，UI 会撒谎。
      throw err
    }
  },
})

/** 两条路径共用的显示行。桌面上 `isCompact` 为假 ⇒ 与 `events` 同值，桌面渲染不变。 */
const displayEvents = computed<ModelIntegrityRecord[]>(() =>
  isCompact.value ? continuous.rows.value : events.value,
)

/** compact 下「正在取第 1 页」。只认 `refreshing` —— `loadingNext` 不该把刷新钮按成忙碌态。 */
const compactBusy = computed(() => continuous.state.value === 'refreshing')

/** compact 侧失败态。**桌面走的是自己的 `error` 横幅**，两者不互相污染。 */
const compactFailed = computed(() => continuous.state.value === 'failed')

/** 页码条仅桌面。compact 走连续加载，两条路径不同时出现在屏幕上。 */
const showPager = computed(() => !isCompact.value && total.value > 0)

async function loadSummary() {
  summaryLoading.value = true
  try {
    const resp = await getModelIntegritySummary(summaryHours.value)
    summaries.value = resp.summaries
  } catch (err: any) {
    error.value = err?.message || t('modelIntegrityView.error.summaryLoadFailed')
  } finally {
    summaryLoading.value = false
  }
}

async function loadDrift() {
  driftLoading.value = true
  error.value = null
  try {
    const resp = await getModelIntegrityFingerprintDrift(driftDays.value)
    driftEvents.value = resp.events
  } catch (err: any) {
    error.value = err?.message || t('modelIntegrityView.error.driftLoadFailed')
  } finally {
    driftLoading.value = false
  }
}

async function refreshAll() {
  // ★ 用户主动点「刷新」⇒ 用 `refresh()`（保旧刷新：旧内容留到新数据就绪），
  //   与 `applyFilters` 的 `loadFirst()` 是两回事，**不要**合并成一个 helper。
  if (isCompact.value) {
    await Promise.all([
      continuous.refresh(),
      loadSummary(),
      tab.value === 'drift' ? loadDrift() : Promise.resolve(),
    ])
    return
  }
  await Promise.all([load(), loadSummary(), tab.value === 'drift' ? loadDrift() : Promise.resolve()])
}

function openDetail(item: ModelIntegrityRecord) {
  selected.value = item
  resolutionNotes.value = item.resolution_notes || ''
}

function closeDetail() {
  selected.value = null
  resolutionNotes.value = ''
}

async function markResolved() {
  if (!selected.value) return
  resolving.value = true
  try {
    await resolveModelIntegrity(selected.value.id, resolutionNotes.value)
    await Promise.all([load(), loadSummary()])
    closeDetail()
  } catch (err: any) {
    error.value = err?.message || t('modelIntegrityView.error.markFailed')
  } finally {
    resolving.value = false
  }
}

function applyFilters() {
  page.value = 1
  // ★ 查询条件变了 ⇒ 已加载的那些页**全部作废**。用 `loadFirst()`（会 `resetInternal`）
  //   而不是 `refresh()`（保旧刷新）—— 后者会把不匹配新条件的旧行留在屏幕上。
  if (isCompact.value) {
    void continuous.loadFirst()
    return
  }
  load()
}

function prevPage() {
  if (page.value <= 1) return
  page.value -= 1
  load()
}

function nextPage() {
  if (page.value >= totalPages.value) return
  page.value += 1
  load()
}

function switchTab(next: Tab) {
  tab.value = next
  if (next === 'drift' && driftEvents.value.length === 0) {
    loadDrift()
  }
}

onMounted(async () => {
  if (!isSuperAdmin()) {
    error.value = t('modelIntegrityView.error.needSuperAdmin')
    return
  }
  // ★ compact 必须在这里**启动连续加载**：`displayEvents` 读的是 `continuous.rows`，
  //   而它初始为空 —— 只调 `load()` 的话，compact 首屏会一直停在空态，
  //   直到用户手动点「查询」。**门禁（首屏 4 张卡）就是抓这个的。**
  await Promise.all([
    isCompact.value ? continuous.loadFirst() : load(),
    loadSummary(),
  ])
})
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1>{{ t('modelIntegrityView.pageTitle') }}</h1>
        <p>{{ t('modelIntegrityView.pageSubtitle') }}</p>
      </div>
      <button class="btn" @click="refreshAll" :disabled="loading || summaryLoading || driftLoading">{{ t('modelIntegrityView.filter.refresh') }}</button>
    </div>

    <div class="tabs">
      <button class="tab" :class="{ active: tab === 'events' }" @click="switchTab('events')">{{ t('modelIntegrityView.tabs.events') }}</button>
      <button class="tab" :class="{ active: tab === 'drift' }" @click="switchTab('drift')">{{ t('modelIntegrityView.tabs.drift') }}</button>
    </div>

    <template v-if="tab === 'events'">
      <div v-if="!summaryLoading" class="stats">
        <div class="stat-card">
          <div class="stat-label">{{ t('modelIntegrityView.stats.total') }}</div>
          <div class="stat-value">{{ totalEvents }}</div>
        </div>
        <div class="stat-card warning">
          <div class="stat-label">{{ t('modelIntegrityView.stats.unresolved') }}</div>
          <div class="stat-value">{{ unresolvedEvents }}</div>
        </div>
        <div class="stat-card danger">
          <div class="stat-label">{{ t('modelIntegrityView.stats.critical') }}</div>
          <div class="stat-value">{{ criticalEvents }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('modelIntegrityView.stats.window') }}</div>
          <div class="stat-value">{{ summaryHours }}h</div>
        </div>
      </div>

      <div class="filters">
        <div class="filter-field filter-field-provider">
          <label for="provider-filter">{{ t('modelIntegrityView.filter.provider') }}</label>
          <ProviderPicker v-model="providerFilter" :title="t('modelIntegrityView.filter.provider')" :placeholder="t('modelIntegrityView.filter.providerPlaceholder')" />
        </div>
        <div class="filter-field filter-field-model">
          <label for="model-filter">{{ t('modelIntegrityView.filter.model') }}</label>
          <ModelPicker v-model="modelFilter" :title="t('modelIntegrityView.filter.model')" :placeholder="t('modelIntegrityView.filter.modelPlaceholder')" />
        </div>
        <div class="filter-field filter-field-type">
          <label for="type-filter">{{ t('modelIntegrityView.filter.anomalyType') }}</label>
          <AnomalyTypePicker v-model="anomalyTypeFilter" :options="anomalyTypeOptions" :title="t('modelIntegrityView.filter.anomalyType')" :placeholder="t('modelIntegrityView.filter.anomalyTypePlaceholder')" />
        </div>
        <div class="filter-field filter-field-severity">
          <label for="severity-filter">{{ t('modelIntegrityView.filter.severity') }}</label>
          <select id="severity-filter" v-model="severityFilter" class="select">
            <option v-for="opt in severityOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
          </select>
        </div>
        <label class="checkbox checkbox-inline">
          <input v-model="unresolvedOnly" type="checkbox" />
          <span>{{ t('modelIntegrityView.filter.unresolvedOnly') }}</span>
        </label>
        <div class="filter-actions">
          <button class="btn btn-primary" @click="applyFilters" :disabled="loading">{{ t('modelIntegrityView.filter.query') }}</button>
          <button class="btn" @click="refreshAll" :disabled="loading || summaryLoading">{{ t('modelIntegrityView.filter.refresh') }}</button>
        </div>
      </div>

      <div v-if="error" class="error-banner">{{ error }}</div>

        <ResponsiveDataView data-testid="mi-events"
          :rows="displayEvents"
          title-key="id"
          :title-format="recordTitle"
          :fields="eventFields"
          :loading="isCompact && compactBusy"
          :empty="isCompact && !compactBusy && !compactFailed && displayEvents.length === 0"
          :empty-text="t('modelIntegrityView.table.noData')"
        >
          <template #table>
          <div class="table-wrap">
            <table class="table">
                        <thead>
                          <tr>
                            <th>{{ t('modelIntegrityView.table.detectedAt') }}</th>
                            <th>{{ t('modelIntegrityView.table.severity') }}</th>
                            <th>{{ t('modelIntegrityView.table.anomalyType') }}</th>
                            <th>{{ t('modelIntegrityView.table.providerModel') }}</th>
                            <th>{{ t('modelIntegrityView.table.requestId') }}</th>
                            <th>{{ t('modelIntegrityView.table.actual') }}</th>
                            <th>{{ t('modelIntegrityView.table.status') }}</th>
                            <th></th>
                          </tr>
                        </thead>
                        <tbody>
                          <tr v-if="loading">
                            <td colspan="8" class="empty">{{ t('modelIntegrityView.table.loading') }}</td>
                          </tr>
                          <tr v-else-if="events.length === 0">
                            <td colspan="8" class="empty">{{ t('modelIntegrityView.table.noData') }}</td>
                          </tr>
                          <tr v-for="item in events" :key="item.id">
                            <td>{{ fmtTime(item.detected_at) }}</td>
                            <td><span class="badge" :class="severityClass(item.severity)">{{ severityLabels[item.severity] || item.severity }}</span></td>
                            <td>{{ anomalyTypeLabels[item.anomaly_type] || item.anomaly_type }}</td>
                            <td>
                              <div class="stacked">
                                <span>{{ item.provider_code || '—' }}</span>
                                <span class="muted">{{ item.raw_model_name || item.client_model || '—' }}</span>
                              </div>
                            </td>
                            <td><code>{{ truncate(item.request_id, 18) }}</code></td>
                            <td><code>{{ truncate(item.actual_value, 20) }}</code></td>
                            <td>
                              <span :class="item.resolved ? 'status-ok' : 'status-warn'">
                                {{ item.resolved ? t('modelIntegrityView.status.resolved') : t('modelIntegrityView.status.unresolved') }}
                              </span>
                            </td>
                            <td><button class="btn btn-link" @click="openDetail(item)">{{ t('modelIntegrityView.table.viewDetail') }}</button></td>
                          </tr>
                        </tbody>
                      </table>
          </div>
          </template>
          <template #actions="{ row }">
            <button class="btn btn-link" @click="openDetail(row as unknown as ModelIntegrityRecord)">
              {{ t('modelIntegrityView.table.viewDetail') }}
            </button>
          </template>
        </ResponsiveDataView>

      <!-- ★ 页码条**仅桌面**（`showPager`）。compact 走连续加载，两条路径不同时出现在屏幕上。
           让它显示却不用（`nextPage` 在 compact 下改的是 `page.value`，而列表读的是
           `continuous.rows`）= 点「下一页」页面纹丝不动 —— 比藏起来更糟的谎。 -->
      <div v-if="showPager" class="pager">
        <button class="btn" @click="prevPage" :disabled="page <= 1 || loading">{{ t('modelIntegrityView.pager.prev') }}</button>
        <span>{{ t('modelIntegrityView.pager.summary', { page, totalPages, total }) }}</span>
        <button class="btn" @click="nextPage" :disabled="page >= totalPages || loading">{{ t('modelIntegrityView.pager.next') }}</button>
      </div>

      <!-- ★ compact 的「继续加载」。桌面不渲染（`v-if="isCompact"`），
           免得两条加载机制同时出现在一个屏幕上。 -->
      <HyperLoadMore
        v-if="isCompact"
        :state="continuous.state.value"
        :has-more="continuous.hasMore.value"
        :loaded-count="continuous.loadedCount.value"
        @load-more="continuous.loadNext()"
        @retry="continuous.retry()"
      />
    </template>

    <template v-else>
      <div class="drift-controls">
        <label>{{ t('modelIntegrityView.drift.days') }}</label>
        <input v-model.number="driftDays" type="number" min="1" max="90" class="input" />
        <button class="btn btn-primary" @click="loadDrift" :disabled="driftLoading">{{ t('modelIntegrityView.drift.query') }}</button>
      </div>

      <div v-if="error" class="error-banner">{{ error }}</div>

        <ResponsiveDataView data-testid="mi-drift"
          :rows="driftEvents"
          title-key="id"
          :title-format="recordTitle"
          :fields="driftFields"
          :loading="isCompact && driftLoading"
          :empty="isCompact && !driftLoading && driftEvents.length === 0"
          :empty-text="t('modelIntegrityView.drift.noData')"
        >
          <template #table>
          <div class="table-wrap">
            <table class="table">
                        <thead>
                          <tr>
                            <th>{{ t('modelIntegrityView.table.detectedAt') }}</th>
                            <th>{{ t('modelIntegrityView.table.severity') }}</th>
                            <th>{{ t('modelIntegrityView.table.providerModel') }}</th>
                            <th>{{ t('modelIntegrityView.table.actual') }}</th>
                            <th>{{ t('modelIntegrityView.table.requestId') }}</th>
                            <th></th>
                          </tr>
                        </thead>
                        <tbody>
                          <tr v-if="driftLoading">
                            <td colspan="6" class="empty">{{ t('modelIntegrityView.table.loading') }}</td>
                          </tr>
                          <tr v-else-if="driftEvents.length === 0">
                            <td colspan="6" class="empty">{{ t('modelIntegrityView.drift.noData') }}</td>
                          </tr>
                          <tr v-for="item in driftEvents" :key="item.id">
                            <td>{{ fmtTime(item.detected_at) }}</td>
                            <td><span class="badge" :class="severityClass(item.severity)">{{ severityLabels[item.severity] || item.severity }}</span></td>
                            <td>
                              <div class="stacked">
                                <span>{{ item.provider_code || '—' }}</span>
                                <span class="muted">{{ item.raw_model_name || item.client_model || '—' }}</span>
                              </div>
                            </td>
                            <td><code>{{ truncate(item.actual_value, 24) }}</code></td>
                            <td><code>{{ truncate(item.request_id, 18) }}</code></td>
                            <td><button class="btn btn-link" @click="openDetail(item)">{{ t('modelIntegrityView.table.viewDetail') }}</button></td>
                          </tr>
                        </tbody>
                      </table>
          </div>
          </template>
          <template #actions="{ row }">
            <button class="btn btn-link" @click="openDetail(row as unknown as ModelIntegrityRecord)">
              {{ t('modelIntegrityView.table.viewDetail') }}
            </button>
          </template>
        </ResponsiveDataView>
    </template>

    <div v-if="selected" class="modal-mask" @click="closeDetail">
      <div class="modal" @click.stop>
        <div class="modal-header">
          <h2>{{ t('modelIntegrityView.detail.title') }}</h2>
          <button class="btn" @click="closeDetail">{{ t('modelIntegrityView.detail.close') }}</button>
        </div>
        <div class="detail-grid">
          <div><strong>{{ t('modelIntegrityView.detail.requestId') }}</strong><div><code>{{ selected.request_id }}</code></div></div>
          <div><strong>{{ t('modelIntegrityView.detail.detectedAt') }}</strong><div>{{ fmtTime(selected.detected_at) }}</div></div>
          <div><strong>{{ t('modelIntegrityView.detail.provider') }}</strong><div>{{ selected.provider_code || '—' }}</div></div>
          <div><strong>{{ t('modelIntegrityView.detail.model') }}</strong><div>{{ selected.raw_model_name || selected.client_model || '—' }}</div></div>
          <div><strong>{{ t('modelIntegrityView.detail.outboundModel') }}</strong><div>{{ selected.outbound_model || '—' }}</div></div>
          <div><strong>{{ t('modelIntegrityView.detail.credential') }}</strong><div>{{ selected.credential_id ?? '—' }}</div></div>
          <div><strong>{{ t('modelIntegrityView.detail.expected') }}</strong><div><code>{{ selected.expected_value || '—' }}</code></div></div>
          <div><strong>{{ t('modelIntegrityView.detail.actual') }}</strong><div><code>{{ selected.actual_value || '—' }}</code></div></div>
        </div>
        <div class="detail-block">
          <strong>{{ t('modelIntegrityView.detail.context') }}</strong>
          <pre>{{ JSON.stringify(selected.context || {}, null, 2) }}</pre>
        </div>
        <div v-if="selected.sample" class="detail-block">
          <strong>{{ t('modelIntegrityView.detail.sample') }}</strong>
          <pre>{{ selected.sample }}</pre>
        </div>
        <div v-if="!selected.resolved" class="detail-block">
          <strong>{{ t('modelIntegrityView.detail.resolutionNotes') }}</strong>
          <textarea v-model="resolutionNotes" rows="4" :placeholder="t('modelIntegrityView.detail.resolutionNotesPlaceholder')" />
          <div class="detail-actions">
            <button class="btn btn-primary" @click="markResolved" :disabled="resolving">
              {{ resolving ? t('modelIntegrityView.detail.processing') : t('modelIntegrityView.detail.markResolved') }}
            </button>
          </div>
        </div>
        <div v-else class="detail-block">
          <strong>{{ t('modelIntegrityView.detail.resolutionInfo') }}</strong>
          <div>{{ fmtTime(selected.resolved_at) }}</div>
          <div class="muted">{{ selected.resolution_notes || t('modelIntegrityView.detail.noNotes') }}</div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page {
  padding: 16px 18px 18px;
  color: var(--text);
}
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  margin-bottom: 16px;
}
.page-header h1 {
  margin: 0 0 4px;
  font-size: 24px;
  line-height: 1.15;
  color: var(--text);
}
.page-header p {
  margin: 0;
  color: var(--muted);
  font-size: 13px;
}
.tabs {
  display: flex;
  gap: 4px;
  margin-bottom: 14px;
  border-bottom: 1px solid var(--border);
}
.tab {
  border: none;
  background: transparent;
  color: var(--muted);
  padding: 8px 14px;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  font: inherit;
}
.tab.active {
  color: var(--text);
  border-bottom-color: var(--accent);
}
.stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 10px;
  margin-bottom: 12px;
}
.stat-card {
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--card);
  padding: 12px 14px;
  box-shadow: inset 0 1px 0 color-mix(in srgb, var(--kx-text) 4%, transparent);
}
.stat-card.warning {
  border-left: 4px solid var(--warning);
}
.stat-card.danger {
  border-left: 4px solid var(--danger);
}
.stat-label {
  color: var(--muted);
  font-size: 12px;
  margin-bottom: 6px;
}
.stat-value {
  font-size: 24px;
  font-weight: 600;
  line-height: 1.1;
  color: var(--text);
}
.filters {
  display: grid;
  grid-template-columns: minmax(150px, 1fr) minmax(170px, 1fr) minmax(200px, 1.2fr) minmax(130px, 1fr) auto auto;
  gap: 10px;
  align-items: end;
  margin-bottom: 12px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--card);
}
.filter-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}
.filter-field label {
  color: var(--muted);
  font-size: 11px;
  line-height: 1;
}
.input,
.select,
textarea {
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 7px 10px;
  font: inherit;
  color: var(--text);
  background: var(--bg-subtle);
}
.input,
.select {
  min-width: 0;
}
.checkbox {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: var(--text);
}
.checkbox-inline {
  align-self: center;
  margin-top: 18px;
  white-space: nowrap;
}
.filter-actions {
  display: inline-flex;
  gap: 8px;
  justify-content: flex-end;
  align-self: center;
  margin-top: 18px;
}
.drift-controls {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--card);
}
.drift-controls .input {
  width: 90px;
}
.btn {
  border: 1px solid var(--border);
  background: var(--bg-subtle);
  color: var(--text);
  border-radius: 6px;
  padding: 7px 12px;
  cursor: pointer;
  transition: background 0.15s ease, border-color 0.15s ease, color 0.15s ease;
}
.btn:hover:not(:disabled) {
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  border-color: color-mix(in srgb, var(--accent) 40%, transparent);
}
.btn:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}
.btn-primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--on-primary);
}
.btn-primary:hover:not(:disabled) {
  background: var(--accent-h);
  border-color: var(--accent-h);
}
.btn-link {
  border: none;
  color: var(--accent-h);
  background: transparent;
  padding: 0;
}
.error-banner {
  margin-bottom: 12px;
  padding: 10px 12px;
  border-radius: 8px;
  background: var(--danger-bg);
  color: var(--danger-bd);
  border: 1px solid var(--danger-bd);
}
/*
 * ★ 本页的 `.table-wrap` 与切片十七那 5 处**不是同一种东西**：
 *   那边只有 `overflow-x: auto`，所以能整类删；
 *   这边它同时是**视觉框**（border + radius + background），
 *   整类删会把桌面的圆角边框一起删掉 = 桌面视觉变更。
 *   ⇒ **只摘 `overflow`**，框与圆角留在原地，横向滚动交给
 *   `.responsive-data-view__table`（否则两个 overflow 容器嵌套 = 双横向滚动条）。
 */
.table-wrap {
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--card);
}
.table {
  width: 100%;
  border-collapse: collapse;
  color: var(--text);
}
.table th,
.table td {
  padding: 10px 12px;
  border-bottom: 1px solid var(--border);
  text-align: left;
  vertical-align: top;
}
.table th {
  background: var(--bg-subtle);
  color: var(--muted);
  font-weight: 600;
  font-size: 12px;
}
.table td {
  font-size: 13px;
}
.table tbody tr:hover {
  background: var(--bg-hover);
}
.empty {
  text-align: center;
  color: var(--muted);
}
.stacked {
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.muted {
  color: var(--muted);
  font-size: 12px;
}
.badge {
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 600;
  border: 1px solid transparent;
}
.badge-low {
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  border-color: color-mix(in srgb, var(--accent) 22%, transparent);
  color: var(--accent-h);
}
.badge-medium {
  background: var(--warning-bg);
  border-color: var(--warning-bd);
  color: var(--warning-bd);
}
.badge-high {
  background: var(--warning-bd);
  border-color: var(--warning-bd);
  color: var(--warning-bd);
}
.badge-critical {
  background: var(--danger-bg);
  border-color: var(--danger-bd);
  color: var(--danger-bd);
}
.status-ok {
  color: var(--success);
}
.status-warn {
  color: var(--warning);
}
code {
  display: inline-block;
  padding: 2px 6px;
  border-radius: 6px;
  background: var(--bg-subtle);
  color: var(--text);
}
.pager {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-top: 12px;
  color: var(--muted);
  font-size: 13px;
}
.modal-mask {
  position: fixed;
  inset: 0;
  background: var(--overlay-strong);
  backdrop-filter: blur(3px);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
}
.modal {
  width: min(920px, 100%);
  max-height: 90vh;
  overflow: auto;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 18px;
  color: var(--text);
  box-shadow: 0 24px 60px var(--overlay-strong);
}
.modal-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 14px;
}
.modal-header h2 {
  margin: 0;
  color: var(--text);
  font-size: 20px;
}
.detail-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 14px;
}
.detail-block {
  margin-bottom: 14px;
}
.detail-block pre {
  margin: 8px 0 0;
  padding: 12px;
  background: var(--bg);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: auto;
  font-size: 12px;
}
.detail-actions {
  margin-top: 12px;
}
textarea {
  width: 100%;
  margin-top: 8px;
}
@media (max-width: 1024px) {
  .filters {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .checkbox-inline,
  .filter-actions {
    margin-top: 0;
    align-self: end;
  }
}
@media (max-width: 1024px) {
  .page {
    padding: 14px;
  }
  .stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .filters {
    grid-template-columns: 1fr;
  }
  .checkbox-inline,
  .filter-actions {
    margin-top: 0;
    justify-content: flex-start;
  }
  .detail-grid {
    grid-template-columns: 1fr;
  }
  .pager {
    flex-direction: column;
    gap: 10px;
  }
}
</style>
