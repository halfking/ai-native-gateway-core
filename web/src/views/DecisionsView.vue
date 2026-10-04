<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatTimeOnly } from '../utils/datetime'
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { getDecisions, type RoutingDecision } from '../api'
import ModelPicker from '../components/ModelPicker.vue'
import { credentialDisplayName, useCredentialLabels } from '../composables/useCredentialLabels'
import { useWindowClass } from '../composables/useWindowClass'
import { createHyperPages } from '../lib/shell/hyper/hyperPages'
import ResponsiveDataView from '../components/ui/ResponsiveDataView.vue'
import HyperLoadMore from '../components/ui/HyperLoadMore.vue'
import type { CardField } from '../components/ui/CardList.vue'

const { t } = useI18n()
const { isCompact } = useWindowClass()

// 凭据显示：订阅标签缓存 revision，标签异步加载完成后自动刷新。
const { labelRevision } = useCredentialLabels()
function credentialLabel(id: number | null | undefined): string {
  void labelRevision.value
  return credentialDisplayName(id)
}


const rows = ref<RoutingDecision[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const sinceMinutes = ref(30)
const filterModel = ref('')
const filterSuccess = ref<'' | 'true' | 'false'>('')
const limit = ref(50)
const offset = ref(0)
const total = ref(0)
const autoRefresh = ref(true)

// Detail panel
const selectedRow = ref<RoutingDecision | null>(null)

function openDetail(row: RoutingDecision) {
  selectedRow.value = row
}
function closeDetail() {
  selectedRow.value = null
}

let timer: ReturnType<typeof setInterval> | null = null

/**
 * ── H6 第十一条切片（2026-10-06）：决策流水表接 compact 卡片 + 连续加载 ────────
 *
 * ## 与第七~十条的差别：这一页**有**分页 API
 *
 * `getDecisions` 收 `limit` + `offset` 并回 `total` ⇒ compact 走**连续加载**
 * （`createHyperPages` + `HyperLoadMore`），桌面的上下页码保持原样。
 * 两条路径共用 `filterBody()` —— 筛选条件只有**一份真源**，不写第二遍。
 *
 * ## 三态归属：**第一种形态（表内三态）**
 *
 * 桌面空态是 tbody 里一行 `<td colspan="13">`，而 `<table>` **永远渲染**（没有 v-if），
 * 加载提示是表格**下面**的独立一行。
 * ⇒ 容器不传 `:empty` / `:loading` 会让 compact 侧整块空着；
 * 但要让桌面像素不变，就得**带 `isCompact` 前置**（与切片四同做法）。
 *
 * ## `limit` 选择器在 compact 下隐藏（不是「没用」）
 *
 * 规范 13 §7：**`createHyperPages` 的 `pageSize` 与 `fetchPage` 里发出的限制必须同源** ——
 * 短页判据 `result.rows.length < opts.pageSize` 读的是**配置里那个**。
 * 本页的 `limit` 是用户可变的（20/50/100/200），而 `pageSize` 在创建时就被捕获，
 * 两者一旦不一致就会把长列表**静默截断**。
 * ⇒ compact 用**固定**的 `COMPACT_PAGE_SIZE`，并把桌面那个「每页条数」选择器
 * 在 compact 下**藏起来**：连续加载里没有「每页」的概念，等价控件是「继续加载」。
 * 让它显示却不用，是比藏起来更糟的谎。
 *
 * ## 5s 自动刷新与累积页的冲突（本切片最需要说清的一条）
 *
 * `load()` 是 `rows.value = resp.decisions`，**替换**语义。compact 下照搬会
 * 每 5 秒把用户已经滚下去加载的页**全部丢掉**。
 * ⇒ `tick()` 分派：桌面照旧 `load()`；compact 只在
 * **「还停在第 1 页」且 state 空闲**时 `refresh()`，否则这一拍跳过。
 * 理由：头部略旧，好过把用户正在读的历史从屏幕上抽走。
 *
 * ## `rowKey` 用 `request_id + ts`（对规范 13 §7 字面要求的一处有意偏离）
 *
 * §7 写「不要用时间戳」。本页是**决策流水**，同一 `request_id` 的重试会产出
 * **另一条**决策行（`ts` 不同、结果可能不同）—— 只用 `request_id` 去重会**丢行**，
 * 而丢一条决策记录比多一次去重糟糕得多。
 * 表格自己的 `:key` 也是这个组合，口径与桌面一致。
 *
 * ## `getDecisions` 不接受 `AbortSignal`（§7 那条对本页做不到）
 *
 * api 层签名里没有 signal，取消只能作废结果、省不下流量。改签名要动 api 层，
 * 属独立立项，本切片只登记（见审计台账）。
 */

function startAutoRefresh() {
  stopAutoRefresh()
  if (autoRefresh.value) {
    timer = setInterval(tick, 5000)
  }
}

function stopAutoRefresh() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
}

watch(autoRefresh, (newVal) => {
  if (newVal) {
    startAutoRefresh()
  } else {
    stopAutoRefresh()
  }
})

/**
 * 筛选条件的**唯一真源**。桌面页码路径与 compact 连续加载路径都从这里取 ——
 * 两边各写一遍筛选条件必然漂移（改了紧凑那套、宽松那套没改是最常见的形态）。
 */
function filterBody(): Record<string, unknown> {
  const params: Record<string, unknown> = { since_minutes: sinceMinutes.value }
  if (filterModel.value.trim()) params.model = filterModel.value.trim()
  if (filterSuccess.value !== '') params.success = filterSuccess.value === 'true'
  return params
}

/** compact 连续加载的每页条数。**必须与 `fetchPage` 里发出的 limit 同值**（13 §7）。 */
const COMPACT_PAGE_SIZE = 25

async function load() {
  loading.value = true
  error.value = null
  try {
    const resp = await getDecisions({ ...filterBody(), limit: limit.value, offset: offset.value })
    rows.value = resp.decisions
    total.value = resp.total
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

const continuousTotal = ref(0)

const continuous = createHyperPages<RoutingDecision>({
  pageSize: COMPACT_PAGE_SIZE,
  // 见上方「有意偏离」：重试会产出同 request_id 的另一条决策行。
  rowKey: (r) => `${r.request_id}#${r.ts}`,
  fetchPage: async (p) => {
    try {
      const resp = await getDecisions({
        ...filterBody(),
        limit: COMPACT_PAGE_SIZE,
        offset: (p - 1) * COMPACT_PAGE_SIZE,
      })
      continuousTotal.value = resp.total
      return { rows: resp.decisions || [], total: resp.total }
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : String(e)
      // 继续抛出：`createHyperPages` 靠它把 state 置成 failed（规范 13 §7
      // 「失败态不显示空态」的依据）。吞掉的话 state 停在 idle，UI 会撒谎。
      throw e
    }
  },
})

/** 两条路径共用的显示行。桌面上 `isCompact` 为假 ⇒ 与 `rows` 同值，桌面渲染不变。 */
const displayRows = computed<RoutingDecision[]>(() => (isCompact.value ? continuous.rows.value : rows.value))

/** 页码条仅桌面。compact 走连续加载，两条路径不同时出现在屏幕上。 */
const showPager = computed(() => !isCompact.value && total.value > 0)

/** compact 下「正在取第 1 页」。只认 `refreshing` —— `loadingNext` 不该把刷新钮按成忙碌态。 */
const compactBusy = computed(() => continuous.state.value === 'refreshing')
/** 失败态：首屏就失败时 rows 为空，**这时绝不能出空态**（13 §7）。 */
const compactFailed = computed(() => continuous.state.value === 'failed')
/** 顶栏刷新钮的忙碌态。 */
const busy = computed(() => (isCompact.value ? compactBusy.value : loading.value))

/** 重新取数：按档位分派到两条路径。 */
async function reload() {
  error.value = ''
  if (isCompact.value) {
    // 顺序不能反：先 invalidate（作废在途结果并提 revision），再 loadFirst。
    continuous.invalidate()
    await continuous.loadFirst()
    return
  }
  offset.value = 0
  await load()
}

/** 5s 自动刷新那一拍。见上方「自动刷新与累积页的冲突」。 */
async function tick() {
  if (isCompact.value) {
    // 已经翻过页就别动：refresh() 会作废累积行，把用户正在读的历史抽走。
    if (continuous.loadedPages.value > 1) return
    if (continuous.state.value !== 'idle') return
    error.value = ''
    await continuous.refresh()
    return
  }
  await load()
}

function fmtTs(ts: string) {
  return formatTimeOnly(ts, { locale: 'zh-CN', options: { hour12: false } })
}

function traceList(v: unknown): string {
  void labelRevision.value
  if (!Array.isArray(v) || !v.length) return t('decisions.dash')
  return v
    .map((item) => {
      if (!item || typeof item !== 'object') return String(item)
      const row = item as Record<string, unknown>
      const provider = row.provider_id ?? t('decisions.dash')
      const credential = row.credential_id != null ? credentialLabel(row.credential_id as number) : t('decisions.dash')
      const reason = row.reason ?? row.raw_model ?? ''
      return `p${provider}/${credential} ${reason}`.trim()
    })
    .join(' | ')
}

function resetAndLoad() {
  void reload()
}

function cardTitle(row: Record<string, unknown>): string {
  const ts = row.ts
  return ts == null ? '' : fmtTs(String(ts))
}

/** 解析过程：桌面上是「path / canonical」+ 一行 raw_models 两段，卡片合成一个字段。 */
function resolutionText(row: Record<string, unknown>): string | null {
  const dash = t('decisions.dash')
  const head = `${row.resolution_path ?? dash} / ${row.canonical_model ?? dash}`
  const raws = Array.isArray(row.resolution_raw_models) && row.resolution_raw_models.length
    ? (row.resolution_raw_models as unknown[]).join(', ')
    : dash
  return `${head} · ${raws}`
}

/**
 * compact 卡片字段。**标签全部复用既有词条**（表头 + 抽屉里的），本次切片
 * **0 新增 i18n 键**。
 *
 * 14 列压到 8 个字段，省下的三列是 `candidateChain` / `blockReason` / `error`
 * —— 它们**没有丢**：整卡可点，抽屉里是 `JSON.stringify(decision_trace)` 全量 JSON，
 * 比桌面上那两列被 ellipsis 截断的内容**更全**。
 * 动作色也保住了：`success` 做成 badge + 逐行 tone（good / danger），
 * 对应桌面的 `badge-ok` / `badge-err`。
 */
const cardFields = computed<CardField[]>(() => [
  {
    key: 'success',
    label: t('decisionsView.table.status'),
    type: 'badge',
    tone: (row) => (row.success ? 'good' : 'danger'),
    format: (v) => (v ? t('decisions.stickyHitOk') : t('decisions.stickyHitNo')),
  },
  { key: 'model', label: t('decisionsView.table.model') },
  {
    key: 'resolution_path',
    label: t('decisionsView.detail.modelResolution'),
    format: (_v, row) => resolutionText(row),
  },
  {
    key: 'latency_ms',
    label: t('decisionsView.table.latency'),
    type: 'metric',
    align: 'end',
    format: (v) => (v == null ? null : `${v}${t('decisions.msUnit')}`),
  },
  {
    key: 'chosen_provider_id',
    label: t('decisionsView.table.provider'),
    format: (v) => (v == null ? null : String(v)),
  },
  {
    key: 'outbound_model',
    label: t('decisionsView.table.outboundModel'),
    format: (v) => (v == null || v === '' ? null : String(v)),
  },
  {
    key: 'prompt_tokens',
    label: t('decisionsView.detail.usage'),
    format: (v, row) => {
      const c = row.completion_tokens
      if (v == null && c == null) return null
      return `${v ?? t('decisions.dash')} / ${c ?? t('decisions.dash')}`
    },
  },
  {
    key: 'cost_usd',
    label: t('decisionsView.table.cost'),
    type: 'metric',
    align: 'end',
    // ★ 5 位小数与**表格**一致（抽屉里是 6 位，两处本来就不同；
    //   卡片跟表格走，别顺手统一 —— 那是对桌面的静默变更）。
    format: (v) => (v == null ? null : t('decisions.costUnit') + Number(v).toFixed(5)),
  },
])

onMounted(() => {
  void reload()
  startAutoRefresh()
})
onUnmounted(() => {
  stopAutoRefresh()
})
</script>

<template>
  <div>
    <div class="page-header" style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px">
      <h2 style="margin:0">{{ t('decisionsView.title') }}</h2>
      <label style="display:flex;align-items:center;gap:6px;font-size:12px;color:var(--muted);cursor:pointer;user-select:none">
        <input type="checkbox" v-model="autoRefresh" style="cursor:pointer;margin:0">
        <span>{{ t('decisionsView.autoRefresh') }}</span>
      </label>
    </div>

    <div class="compact-filter-bar compact-filter-bar--stacked">
      <div class="cf-row">
        <select v-model="filterSuccess" class="cf-select cf-status" :title="t('decisionsView.filter.status')" @change="resetAndLoad">
          <option value="">{{ t('decisionsView.filter.statusAll') }}</option>
          <option value="true">{{ t('decisionsView.filter.statusSuccess') }}</option>
          <option value="false">{{ t('decisionsView.filter.statusFailed') }}</option>
        </select>
        <select v-model="sinceMinutes" class="cf-select cf-hours" :title="t('decisionsView.filter.timeRange')" @change="resetAndLoad">
          <option :value="10">{{ t('decisionsView.filter.time10m') }}</option>
          <option :value="30">{{ t('decisionsView.filter.time30m') }}</option>
          <option :value="60">{{ t('decisionsView.filter.time1h') }}</option>
          <option :value="360">{{ t('decisionsView.filter.time6h') }}</option>
          <option :value="1440">{{ t('decisionsView.filter.time24h') }}</option>
        </select>
        <select v-if="!isCompact" v-model="limit" class="cf-select" style="width:72px" :title="t('decisionsView.filter.limit')" @change="resetAndLoad">
          <option :value="20">{{ t('decisionsView.filter.limit20') }}</option>
          <option :value="50">{{ t('decisionsView.filter.limit50') }}</option>
          <option :value="100">{{ t('decisionsView.filter.limit100') }}</option>
          <option :value="200">{{ t('decisionsView.filter.limit200') }}</option>
        </select>
        <button class="btn btn-ghost btn-sm" :disabled="busy" @click="reload">{{ t('decisionsView.filter.refresh') }}</button>
        <span class="cf-meta">{{ t('decisionsView.filter.totalCount', { n: total }) }}</span>
      </div>
      <div class="cf-row cf-row--secondary">
        <div class="cf-field cf-field--grow">
          <span class="cf-label">{{ t('decisionsView.filter.modelLabel') }}</span>
          <div class="decisions-model-picker">
            <ModelPicker
              v-model="filterModel"
              :placeholder="t('decisionsView.filter.modelPlaceholder')"
              :title="t('decisionsView.filter.modelTitle')"
              @update:model-value="resetAndLoad"
            />
          </div>
        </div>
      </div>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>

    <!-- Top Pagination -->
    <div v-if="showPager" class="card" style="margin-bottom:12px;display:flex;justify-content:space-between;align-items:center;font-size:13px">
      <div style="color:var(--muted)" v-html="t('decisionsView.pagination.summary', { total, start: offset + 1, end: Math.min(offset + limit, total) })">
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        <button class="btn btn-ghost btn-sm" :disabled="offset === 0" @click="offset = Math.max(0, offset - limit); load()">{{ t('decisionsView.pagination.prev') }}</button>
        <button class="btn btn-ghost btn-sm" :disabled="offset + limit >= total" @click="offset = offset + limit; load()">{{ t('decisionsView.pagination.next') }}</button>
      </div>
    </div>

    <div class="card" style="overflow:auto">
      <ResponsiveDataView
        :rows="displayRows"
        title-key="request_id"
        :title-format="cardTitle"
        :fields="cardFields"
        table-min-width="1500px"
        :loading="isCompact && compactBusy"
        :empty="isCompact && !compactBusy && !compactFailed && displayRows.length === 0"
        :empty-text="t('decisionsView.table.noData')"
        :clickable="true"
        :clickable-label="t('decisionsView.detail.title')"
        @row-click="openDetail"
      >
        <template #table>
      <table class="data-table" style="min-width:1500px">
        <thead>
          <tr>
            <th>{{ t('decisionsView.table.time') }}</th>
            <th>{{ t('decisionsView.table.status') }}</th>
            <th>{{ t('decisionsView.table.model') }}</th>
            <th>{{ t('decisionsView.table.interpretation') }}</th>
            <th>Tier</th>
            <th>{{ t('decisionsView.table.latency') }}</th>
            <th>{{ t('decisionsView.table.provider') }}</th>
            <th>{{ t('decisionsView.table.outboundModel') }}</th>
            <th>prompt_t</th>
            <th>comp_t</th>
            <th>{{ t('decisionsView.table.cost') }}</th>
            <th>{{ t('decisionsView.table.candidateChain') }}</th>
            <th>{{ t('decisionsView.table.blockReason') }}</th>
            <th>{{ t('decisionsView.table.error') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!displayRows.length && !loading">
            <td colspan="13" style="text-align:center;padding:32px;color:var(--muted)">
              {{ t('decisionsView.table.noData') }}
            </td>
          </tr>
          <tr v-for="r in displayRows" :key="r.request_id + r.ts" :class="{ 'row-fail': !r.success }" class="row-clickable" @click="openDetail(r)">
            <td style="white-space:nowrap;font-size:12px">{{ fmtTs(r.ts) }}</td>
            <td>
              <span :class="r.success ? 'badge-ok' : 'badge-err'">
                {{ r.success ? t('decisions.stickyHitOk') : t('decisions.stickyHitNo') }}
              </span>
            </td>
            <td style="font-size:12px;max-width:160px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">{{ r.model }}</td>
            <td style="font-size:11px;max-width:220px;overflow:hidden;text-overflow:ellipsis">
              <div>{{ r.resolution_path ?? t('decisions.dash') }} / {{ r.canonical_model ?? t('decisions.dash') }}</div>
              <div style="color:var(--muted)">{{ (r.resolution_raw_models || []).join(', ') || t('decisions.dash') }}</div>
            </td>
            <td style="text-align:center">{{ r.tier ?? t('decisions.dash') }}</td>
            <td style="text-align:right">{{ r.latency_ms != null ? r.latency_ms + t('decisions.msUnit') : t('decisions.dash') }}</td>
            <td style="font-size:12px">{{ r.chosen_provider_id ?? t('decisions.dash') }}</td>
            <td style="font-size:12px;max-width:160px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">{{ r.outbound_model ?? t('decisions.dash') }}</td>
            <td style="text-align:right">{{ r.prompt_tokens ?? t('decisions.dash') }}</td>
            <td style="text-align:right">{{ r.completion_tokens ?? t('decisions.dash') }}</td>
            <td style="text-align:right;font-size:12px">
              {{ r.cost_usd != null ? t('decisions.costUnit') + Number(r.cost_usd).toFixed(5) : t('decisions.dash') }}
            </td>
            <td style="font-size:11px;max-width:260px;overflow:hidden;text-overflow:ellipsis">
              {{ traceList((r.decision_trace || {}).planned_candidates) }}
            </td>
            <td style="font-size:11px;max-width:260px;overflow:hidden;text-overflow:ellipsis;color:var(--warning)">
              {{ traceList((r.decision_trace || {}).blocked_candidates) }}
            </td>
            <td style="font-size:11px;color:var(--danger);max-width:140px;overflow:hidden;text-overflow:ellipsis">
              {{ r.failure_detail_code ?? r.error_class ?? '' }}
            </td>
          </tr>
        </tbody>
      </table>
        </template>
      </ResponsiveDataView>
    </div>
    <div v-if="loading" style="text-align:center;padding:8px;font-size:12px;color:var(--muted)">{{ t('decisionsView.loading') }}</div>

    <HyperLoadMore
      v-if="isCompact"
      :state="continuous.state.value"
      :has-more="continuous.hasMore.value"
      :loaded-count="continuous.loadedCount.value"
      @load-more="continuous.loadNext()"
      @retry="continuous.retry()"
    />

    <!-- Pagination -->
    <div v-if="showPager" class="card" style="margin-top:12px;display:flex;justify-content:space-between;align-items:center;font-size:13px">
      <div style="color:var(--muted)" v-html="t('decisionsView.pagination.summary', { total, start: offset + 1, end: Math.min(offset + limit, total) })">
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        <button class="btn btn-ghost btn-sm" :disabled="offset === 0" @click="offset = Math.max(0, offset - limit); load()">{{ t('decisionsView.pagination.prev') }}</button>
        <button class="btn btn-ghost btn-sm" :disabled="offset + limit >= total" @click="offset = offset + limit; load()">{{ t('decisionsView.pagination.next') }}</button>
      </div>
    </div>

    <!-- Row detail modal -->
    <Teleport to="body">
      <div v-if="selectedRow" class="drawer-backdrop" @click="closeDetail">
        <div class="drawer-panel card" @click.stop>
          <div class="drawer-header">
            <span style="font-size:14px;font-weight:600">{{ t('decisionsView.detail.title') }}</span>
            <button class="btn btn-ghost btn-sm" @click="closeDetail">{{ t('decisionsView.detail.close') }}</button>
          </div>
          <div class="detail-body">

            <!-- Basic -->
            <div class="drawer-section">
              <div class="drawer-section-title">{{ t('decisionsView.detail.basicInfo') }}</div>
              <div class="detail-grid">
                <span class="dk">{{ t('decisionsView.detail.time') }}</span><span class="dv">{{ selectedRow.ts }}</span>
                <span class="dk">Request ID</span><span class="dv mono">{{ selectedRow.request_id }}</span>
                <span class="dk">Idempotency Key</span><span class="dv mono">{{ selectedRow.idempotency_key ?? t('decisions.dash') }}</span>
                <span class="dk">Tenant</span><span class="dv mono">{{ selectedRow.tenant_id }}</span>
                <span class="dk">{{ t('decisionsView.detail.status') }}</span>
                <span class="dv">
                  <span :class="selectedRow.success ? 'badge-ok' : 'badge-err'">
                    {{ selectedRow.success ? t('decisions.successOk') : t('decisions.successFail') }}
                  </span>
                </span>
                <span class="dk">{{ t('decisionsView.detail.latency') }}</span><span class="dv">{{ selectedRow.latency_ms != null ? selectedRow.latency_ms + t('decisions.latencyUnit') : t('decisions.dash') }}</span>
                <span class="dk">{{ t('decisionsView.detail.clientModel') }}</span><span class="dv mono">{{ selectedRow.client_model ?? selectedRow.model }}</span>
                <span class="dk">{{ t('decisionsView.detail.outboundModel') }}</span><span class="dv mono">{{ selectedRow.outbound_model ?? t('decisions.dash') }}</span>
                <span class="dk">Request Mode</span><span class="dv">{{ selectedRow.request_mode ?? t('decisions.dash') }}</span>
                <span class="dk">{{ t('decisionsView.detail.protocol') }}</span><span class="dv">{{ selectedRow.egress_protocol ?? t('decisions.dash') }}</span>
                <span class="dk">Sticky Hit</span><span class="dv">{{ selectedRow.sticky_hit ? t('decisions.stickyHitOk') : t('decisions.stickyHitNo') }}</span>
              </div>
            </div>

            <!-- Resolution -->
            <div class="drawer-section">
              <div class="drawer-section-title">{{ t('decisionsView.detail.modelResolution') }}</div>
              <div class="detail-grid">
                <span class="dk">Resolution Path</span><span class="dv mono">{{ selectedRow.resolution_path ?? t('decisions.dash') }}</span>
                <span class="dk">Canonical Model</span><span class="dv mono">{{ selectedRow.canonical_model ?? t('decisions.dash') }}</span>
                <span class="dk">Raw Models</span>
                <span class="dv mono">{{ (selectedRow.resolution_raw_models || []).join(', ') || t('decisions.dash') }}</span>
                <span class="dk">Client Profile</span><span class="dv">{{ selectedRow.client_profile ?? t('decisions.dash') }}</span>
                <span class="dk">Transform Rule</span><span class="dv mono">{{ selectedRow.transform_rule_id ?? t('decisions.dash') }}</span>
              </div>
            </div>

            <!-- Routing -->
            <div class="drawer-section">
              <div class="drawer-section-title">{{ t('decisionsView.detail.routingDecision') }}</div>
              <div class="detail-grid">
                <span class="dk">{{ t('decisionsView.detail.providerId') }}</span><span class="dv">{{ selectedRow.chosen_provider_id ?? t('decisions.dash') }}</span>
                <span class="dk">{{ t('decisionsView.detail.credentialId') }}</span><span class="dv" :title="`#${selectedRow.chosen_credential_id ?? ''}`">{{ selectedRow.chosen_credential_id != null ? credentialLabel(selectedRow.chosen_credential_id) : t('decisions.dash') }}</span>
                <span class="dk">Tier</span><span class="dv">{{ selectedRow.tier ?? t('decisions.dash') }}</span>
                <span class="dk">{{ t('decisionsView.detail.candidatesCount') }}</span><span class="dv">{{ selectedRow.candidates_tried }}</span>
              </div>
            </div>

            <!-- Tokens & Cost -->
            <div class="drawer-section">
              <div class="drawer-section-title">{{ t('decisionsView.detail.usage') }}</div>
              <div class="detail-grid">
                <span class="dk">Prompt Tokens</span><span class="dv">{{ selectedRow.prompt_tokens ?? t('decisions.dash') }}</span>
                <span class="dk">Completion Tokens</span><span class="dv">{{ selectedRow.completion_tokens ?? t('decisions.dash') }}</span>
                <span class="dk">{{ t('decisionsView.detail.costCalc') }}</span>
                <span class="dv">{{ selectedRow.cost_usd != null ? t('decisions.costUnit') + Number(selectedRow.cost_usd).toFixed(6) : t('decisions.dash') }}</span>
                <span class="dk">Request Size</span><span class="dv">{{ selectedRow.request_bytes != null ? selectedRow.request_bytes + t('decisions.bytesUnit') : t('decisions.dash') }}</span>
                <span class="dk">Response Size</span><span class="dv">{{ selectedRow.response_bytes != null ? selectedRow.response_bytes + t('decisions.bytesUnit') : t('decisions.dash') }}</span>
              </div>
            </div>

            <!-- Error -->
            <div v-if="!selectedRow.success" class="drawer-section">
              <div class="drawer-section-title" style="color:var(--danger)">{{ t('decisionsView.detail.errorInfo') }}</div>
              <div class="detail-grid">
                <span class="dk">Error Class</span><span class="dv" style="color:var(--danger)">{{ selectedRow.error_class ?? t('decisions.dash') }}</span>
                <span class="dk">Failure Stage</span><span class="dv">{{ selectedRow.failure_stage ?? t('decisions.dash') }}</span>
                <span class="dk">Failure Code</span><span class="dv mono">{{ selectedRow.failure_detail_code ?? t('decisions.dash') }}</span>
              </div>
            </div>

            <!-- Decision Trace -->
            <div class="drawer-section">
              <div class="drawer-section-title">{{ t('decisionsView.detail.trace') }}</div>
              <pre class="trace-json">{{ JSON.stringify(selectedRow.decision_trace, null, 2) }}</pre>
            </div>

          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.data-table { width: 100%; border-collapse: collapse; }
.data-table th {
  text-align: left;
  padding: 8px 12px;
  font-size: 12px;
  color: var(--muted);
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
.data-table td {
  padding: 7px 12px;
  border-bottom: 1px solid var(--border);
  vertical-align: middle;
}
.row-fail td { background: rgba(239,68,68,.05); }
.badge-ok  { color: var(--success); font-weight: 600; }
.badge-err { color: var(--danger); font-weight: 600; }
.error-banner {
  background: var(--danger-bg);
  border: 1px solid var(--danger);
  border-radius: 8px;
  padding: 12px 16px;
  color: var(--danger);
  margin-bottom: 16px;
}
.row-clickable { cursor: pointer; }
.row-clickable:hover td { background: rgba(var(--accent-rgb), .06); }

.detail-body {
  flex: 1;
  overflow-y: auto;
  padding: 16px 20px;
  display: flex;
  flex-direction: column;
  gap: 20px;
}
.drawer-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.drawer-section-title {
  font-size: 11px;
  font-weight: 700;
  letter-spacing: .06em;
  text-transform: uppercase;
  color: var(--muted);
  padding-bottom: 4px;
  border-bottom: 1px solid var(--border);
}
.detail-grid {
  display: grid;
  grid-template-columns: 140px 1fr;
  gap: 4px 12px;
  font-size: 13px;
}
.dk {
  color: var(--muted);
  font-size: 12px;
  padding: 2px 0;
  white-space: nowrap;
}
.dv {
  word-break: break-all;
  padding: 2px 0;
}
.mono { font-family: monospace; font-size: 12px; }
.trace-json {
  font-family: monospace;
  font-size: 11px;
  white-space: pre-wrap;
  word-break: break-all;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 12px;
  margin: 0;
  max-height: 320px;
  overflow-y: auto;
  color: var(--text);
}
</style>
