<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatDateTime, formatTimeOnly, parseLocalMinute } from '../utils/datetime'
import { localeRef } from '../i18n'
import { fmtDateCompact } from '../i18n/useFormat'
import { ref, onMounted, computed } from 'vue'
import { getAuditLogs, type AuditLogEntry } from '../api'
// 2026-09-13 P2：筛选/分页收敛到 ui 组件（方案 §4.5.4/§4.5.6）
import FilterBar from '../components/ui/FilterBar.vue'
import type { FilterDefinition } from '../components/ui/filter-types'
import PaginationBar from '../components/ui/PaginationBar.vue'
// 2026-10-05 H6 第四条切片：呈现形态与加载方式是**两个独立维度**（规范 03 §1 / 13 §1）。
//   桌面 → 表格 + 页码（既有表格、徽章、行样式与页码条逐字保留）
//   compact → 卡片 + 连续加载
import { useWindowClass } from '../composables/useWindowClass'
import { createHyperPages } from '../lib/shell/hyper/hyperPages'
import ResponsiveDataView from '../components/ui/ResponsiveDataView.vue'
import HyperLoadMore from '../components/ui/HyperLoadMore.vue'
import type { CardField } from '../components/ui/CardList.vue'

const { t, tm } = useI18n()
const { isCompact } = useWindowClass()
const entries = ref<AuditLogEntry[]>([])
const total = ref(0)
const page = ref(1)
const size = ref(50)
const loading = ref(false)
const error = ref('')

const filters = ref<Record<string, string>>({ actor: '', action: '', from: '', to: '' })

// FilterBar 声明式定义（label 走 computed 以随语言切换更新）。
// 2026-09-30 审计 P3-5：新模板 daterange 仅渲染 def.label，fromLabel/toLabel 为
// 死契约已删除；label 复用既有 fromLabel 词条（不再另立新词条）。
const filterDefs = computed<FilterDefinition[]>(() => [
  { key: 'actor', type: 'search', label: t('auditLog.filter.actorLabel'), placeholder: t('auditLog.filter.actorPlaceholder') },
  { key: 'action', type: 'search', label: t('auditLog.filter.actionLabel'), placeholder: t('auditLog.filter.actionPlaceholder') },
  { key: 'time', type: 'daterange', fromKey: 'from', toKey: 'to', label: t('auditLog.filter.fromLabel') },
])

const detailVisible = ref(false)
const detailEntry = ref<AuditLogEntry | null>(null)

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / size.value)))

/**
 * 筛选条件的**唯一真源**。页码路径与连续加载路径都从这里取 ——
 * 各写一份必然漂移，症状是「桌面筛了，compact 没筛」。
 * 不含 page / size：那是加载方式，不是筛选条件。
 */
function filterBody() {
  return {
    actor: filters.value.actor.trim() || undefined,
    action: filters.value.action.trim() || undefined,
    // filters 存 datetime-local 的 'YYYY-MM-DDTHH:mm'（无秒非规范格式，补秒解析，P3-3）
    from: filters.value.from ? parseLocalMinute(filters.value.from).toISOString() : undefined,
    to: filters.value.to ? parseLocalMinute(filters.value.to).toISOString() : undefined,
  }
}

/** 桌面页码路径。表格与分页条的既有行为逐字保留。 */
async function load() {
  loading.value = true
  error.value = ''
  try {
    const r = await getAuditLogs({
      ...filterBody(),
      page: page.value,
      size: size.value,
    })
    entries.value = r.entries || []
    total.value = r.total || 0
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('auditLog.loadFailed')
    entries.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

function resetPageAndLoad() {
  void reload()
}

function changePage(delta: number) {
  const next = page.value + delta
  if (next < 1 || next > totalPages.value) return
  page.value = next
  load()
}

function clearFilters() {
  filters.value = { actor: '', action: '', from: '', to: '' }
  resetPageAndLoad()
}

function onPageSizeChange(next: number) {
  size.value = next
  resetPageAndLoad()
}

function actionBadgeClass(action: string): string {
  if (action.startsWith('user.create')) return 'badge-system'
  if (action.startsWith('user.delete')) return 'badge-red'
  if (action.startsWith('user.')) return 'badge-blue'
  if (action.startsWith('auth.login_failed') || action.startsWith('auth.rate_limited')) return 'badge-red'
  if (action.startsWith('auth.')) return 'badge-green'
  return 'badge-gray'
}

/**
 * 动作译名表。**不能靠 `te('auditLog.actions.' + action)` 拼 key** ——
 * 词条键本身带点号（`'user.delete'`），vue-i18n 的路径解析会把
 * `auditLog.actions.user.delete` 拆成四段去查 `actions.user`，永远查不到。
 * 2026-10-05 实测（真实 zh-CN 词条 + 真实 createI18n）：
 * `te('auditLog.actions.user.delete')` → `false`，
 * `t(...)` → 原样返回 key，`tm('auditLog.actions')` → 能取到全部平铺键。
 * ⇒ 8 个语言各 12 条动作译名（96 条）此前**全是死数据**，界面只出原始动作码。
 *
 * 这里直接取平铺表按属性取，绕开路径解析；换语言由 computed 自动跟随。
 */
const actionMessages = computed<Record<string, string>>(() => {
  // `tm` 直调会触发 TS2589（vue-i18n 的重载集对着全量词条表展开过深），
  // 所以先把**函数本身**收成宽松签名，绕开重载推导；运行时行为不变。
  const raw = (tm as unknown as (key: string) => unknown)('auditLog.actions')
  return raw && typeof raw === 'object' ? (raw as Record<string, string>) : {}
})

function actionLabel(action: string): string {
  const normalized = action.replace(/^authentication\./, 'auth.')
  const table = actionMessages.value
  return table[action] ?? table[normalized] ?? action
}

function fmtTime(s: string) {
  if (!s) return t('auditLog.dash')
  return formatTimeOnly(s, { locale: localeRef.value, options: { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' } })
}

function fmtTs(s: string) {
  if (!s) return t('auditLog.dash')
  return formatDateTime(s, { locale: localeRef.value, options: { hour12: false } })
}

function fmtJson(v: unknown): string {
  if (v == null) return ''
  if (typeof v === 'string') {
    try {
      return JSON.stringify(JSON.parse(v), null, 2)
    } catch {
      return v
    }
  }
  try {
    return JSON.stringify(v, null, 2)
  } catch {
    return String(v)
  }
}

function detailPreview(e: AuditLogEntry): string {
  const raw = e.after_json ?? e.before_json
  if (raw == null) return t('auditLog.dash')
  const text = typeof raw === 'string' ? raw : JSON.stringify(raw)
  if (text.length <= 80) return text
  return text.slice(0, 79) + '…'
}

function openDetail(e: AuditLogEntry) {
  detailEntry.value = e
  detailVisible.value = true
}

function closeDetail() {
  detailVisible.value = false
  detailEntry.value = null
}

/** 卡片的目标列文本。由 `target_type` + `target_id` 两个字段拼成。 */
function targetText(e: AuditLogEntry): string | null {
  if (!e.target_type) return null
  return `${e.target_type} #${e.target_id ?? '?'}`
}

// ── compact 连续加载 ──────────────────────────────────────────────────────
// 独立于上面的页码状态机：两者不共享 ref、不互相写。
/**
 * compact 的每页条数。与桌面默认 `size` 同值（50），但**不复用 `size` ref** ——
 * 页大小是桌面页码控件的旋钮，compact 下那个控件不渲染；
 * 让两条路径共用一个 ref 等于把「加载方式」和「筛选/展示」重新耦合回去。
 */
const COMPACT_PAGE_SIZE = 50

/** 服务端 total。页码路径有自己的 `total` ref，两条路径不共享。 */
const continuousTotal = ref(0)

const continuous = createHyperPages<AuditLogEntry>({
  pageSize: COMPACT_PAGE_SIZE,
  // id 是后端主键且稳定。**不能用数组下标** —— 连续加载第 2 页的下标 0
  // 是另一行，去重会把它当成与第 1 页第 0 行相同，表现为「行随机消失」。
  rowKey: (e) => e.id,
  fetchPage: async (p) => {
    try {
      const r = await getAuditLogs({ ...filterBody(), page: p, size: COMPACT_PAGE_SIZE })
      continuousTotal.value = r.total || 0
      return { rows: r.entries || [], total: r.total || 0 }
    } catch (e: unknown) {
      // 必须**再抛**：不抛的话 `createHyperPages` 收不到异常，
      // 状态会停在 refreshing，尾部控件永远显示「加载中」而不会转成可重试。
      error.value = e instanceof Error ? e.message : t('auditLog.loadFailed')
      throw e
    }
  },
})

/** 实际展示的行：按档位二选一。筛选是服务端做的，两条路径都不需要客户端过滤。 */
const rows = computed<AuditLogEntry[]>(() => (isCompact.value ? continuous.rows.value : entries.value))

/** 顶栏计数：桌面取页码路径的 total，compact 取连续加载带回来的服务端 total。 */
const totalCount = computed(() => (isCompact.value ? continuousTotal.value : total.value))

/**
 * 卡头：审计记录没有「单号」可当主识别，最有用的标题是**这条记录做了什么**
 * 的译名（`user.delete` → 「删除用户」）。时间/操作员/目标降为普通字段。
 * 算不出译名时回落到原始动作码（`titleKey="action"` 兜底），不会出空卡头。
 *
 * 形参用 `Record<string, unknown>`：组件契约是那个形状（泛型组件无法把
 * `T` 传进 prop 的函数类型），写成 `AuditLogEntry` 会被逆变检查拒掉。
 */
function cardTitle(row: Record<string, unknown>): string {
  return actionLabel(String(row.action ?? ''))
}

/**
 * compact 卡片的字段定义。**没有 `format` 就只能渲染原始值** ——
 * 目标是两个字段拼的（要整行取）、详情要截断、时间要本地化，
 * 所以这里必须逐个接格式化钩子。
 */
const cardFields = computed<CardField[]>(() => [
  { key: 'ts', label: t('auditLog.table.headers.time'), format: (v) => (v == null ? null : fmtTs(String(v))) },
  {
    key: 'actor',
    label: t('auditLog.table.headers.actor'),
    format: (v) => (v == null || v === '' ? null : String(v)),
  },
  {
    key: 'target_type',
    label: t('auditLog.table.headers.target'),
    format: (_v, row) => targetText(row as unknown as AuditLogEntry),
  },
  {
    key: 'after_json',
    label: t('auditLog.table.headers.details'),
    format: (_v, row) => detailPreview(row as unknown as AuditLogEntry),
  },
])

/** 页码条：仅桌面。compact 走连续加载，两条路径不同时出现在屏幕上。 */
const showPager = computed(() => !isCompact.value && !loading.value && total.value > 0)

/**
 * compact 下的「正在取第 1 页」。**只认 refreshing** ——
 * `loadingNext` 是滚动加载更多，不该把刷新钮按成忙碌态。
 */
const compactBusy = computed(() => continuous.state.value === 'refreshing')
/**
 * 失败态。★ 2026-10-06 切片十一实测补的（不是读码推断）：
 * 首屏取数失败时 `state === 'failed'`、`rows` 为空，而原判据只看 `!compactBusy`
 * （failed 时它恰好为假 ⇒ 取反为真）⇒ **错误横幅与「暂无审计记录」同时出现**，
 * 违反 13 §7「失败态不显示空态」。实测证据：compact + `getAuditLogs` reject
 * ⇒ `banner="audit boom"` 与 `empty="暂无审计记录"` 同时在屏上。
 */
const compactFailed = computed(() => continuous.state.value === 'failed')

/** 顶栏刷新钮的忙碌态：桌面下与 `loading` 同值，桌面渲染结果不变。 */
const busy = computed(() => (isCompact.value ? compactBusy.value : loading.value))

/** 重新取数：按档位分派到两条路径，且 compact 下先作废在途结果再重取。 */
async function reload() {
  if (isCompact.value) {
    // 顺序不能反：先 invalidate（作废在途结果并提 revision），再 loadFirst，
    // 否则旧请求可能在新请求之后落地。
    error.value = ''
    continuous.invalidate()
    await continuous.loadFirst()
    return
  }
  page.value = 1
  await load()
}

onMounted(reload)
</script>

<template>
  <div class="audit-page">
    <div class="page-header">
      <h2>{{ t('auditLog.page.title') }}</h2>
      <div class="header-actions">
        <span class="count-chip" aria-live="polite">{{ t('auditLog.page.totalChip', { n: totalCount }) }}</span>
        <button class="btn btn-primary btn-sm" :disabled="busy" @click="reload">
          {{ busy ? t('auditLog.page.refreshing') : t('auditLog.page.refresh') }}
        </button>
      </div>
    </div>

    <p class="page-desc">{{ t('auditLog.page.desc') }}</p>

    <div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>

    <FilterBar
      v-model="filters"
      :definitions="filterDefs"
      :loading="loading"
      @search="resetPageAndLoad"
      @clear="resetPageAndLoad"
    />

    <PaginationBar
      v-if="showPager"
      :page="page"
      :page-size="size"
      :total="total"
      :page-sizes="[25, 50, 100, 200]"
      @prev="changePage(-1)"
      @next="changePage(1)"
      @change-size="onPageSizeChange"
    />

    <!--
      2026-10-05 H6：双模板 + 双加载方式。
      · 桌面   → 表格（表头/表体/徽章/行样式逐字未改）+ 页码条
      · compact → 卡片 + 连续加载 + 底部 sentinel

      ★ 桌面三态**仍由表格自己出**：`.state-cell` 那两行 `<tr>` 原样留着。
        所以 `:loading` / `:empty` 都带 `isCompact` 前置 ——
        交给容器裁定的只有 compact。桌面刷新时若交给容器，整张表连 `<thead>`
        一起消失（原来只是表内多一行「加载中…」），那是桌面观感回退。

      ★ `.table-wrap` 删掉了：容器自带 `overflow-x`，两个横滚容器嵌套会出双滚动条。
        横向滚动本身没丢，只是上移了一层。

      ★ `table-min-width="0px"`：本页原本**没有**表级 min-width，列宽由
        `.col-*` 的 min/max 自己撑。传组件默认的 720px 会在 1024–1200 的内容宽度上
        多出一条原本不存在的横滚动条 —— 那不是「保护窄屏」，是桌面回归。

      ★ 唯一一处 compact 观感差异（如实记录）：compact 空态只出标题，
        不出桌面那行 11px 灰字提示（`emptyHint`）——
        `EmptyState` 只有一个文本位，硬塞两行会把它变成本页特例。
    -->
    <div class="card table-card">
      <ResponsiveDataView
        :rows="rows"
        title-key="action"
        :title-format="cardTitle"
        :fields="cardFields"
        table-min-width="0px"
        :loading="isCompact && compactBusy"
        :empty="isCompact && !compactBusy && !compactFailed && rows.length === 0"
        :empty-text="t('auditLog.page.emptyTitle')"
        :clickable="true"
        :clickable-label="t('auditLog.table.headers.details')"
        @row-click="openDetail"
      >
        <template #table>
      <table class="data-table audit-table">
          <thead>
            <tr>
              <th class="col-time">{{ t('auditLog.table.headers.time') }}</th>
              <th class="col-actor">{{ t('auditLog.table.headers.actor') }}</th>
              <th class="col-action">{{ t('auditLog.table.headers.action') }}</th>
              <th class="col-target">{{ t('auditLog.table.headers.target') }}</th>
              <th class="col-details">{{ t('auditLog.table.headers.details') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="loading">
              <td colspan="5" class="state-cell">{{ t('auditLog.page.loading') }}</td>
            </tr>
            <tr v-else-if="!entries.length">
              <td colspan="5" class="state-cell">
                <p>{{ t('auditLog.page.emptyTitle') }}</p>
                <p class="text-muted">{{ t('auditLog.page.emptyHint') }}</p>
              </td>
            </tr>
            <tr
              v-for="e in entries"
              v-else
              :key="e.id"
              class="audit-row"
              tabindex="0"
              :aria-label="`${e.actor} ${e.action}`"
              @click="openDetail(e)"
              @keyup.enter="openDetail(e)"
            >
              <td class="col-time" :title="fmtTs(e.ts)">
                <div class="cell-line1">{{ fmtDateCompact(e.ts) }}</div>
                <div class="cell-line2">{{ fmtTime(e.ts) }}</div>
              </td>
              <td class="col-actor">
                <span class="actor-name">{{ e.actor || t('auditLog.dash') }}</span>
              </td>
              <td class="col-action">
                <span class="badge" :class="actionBadgeClass(e.action)" :title="e.action">
                  {{ actionLabel(e.action) }}
                </span>
              </td>
              <td class="col-target">
                <template v-if="e.target_type">
                  <span class="target-type">{{ e.target_type }}</span>
                  <span class="target-id">#{{ e.target_id ?? '?' }}</span>
                </template>
                <span v-else class="text-muted">—</span>
              </td>
              <td class="col-details">
                <code class="detail-preview" :title="detailPreview(e)">{{ detailPreview(e) }}</code>
              </td>
            </tr>
          </tbody>
        </table>
        </template>
      </ResponsiveDataView>
    </div>

    <!-- 连续加载尾部：仅 compact。屏幕上不会同时出现两个「加载更多」语义。 -->
    <HyperLoadMore
      v-if="isCompact"
      :state="continuous.state.value"
      :has-more="continuous.hasMore.value"
      :loaded-count="continuous.loadedCount.value"
      @load-more="continuous.loadNext()"
      @retry="continuous.retry()"
    />

    <PaginationBar
      v-if="showPager"
      :page="page"
      :page-size="size"
      :total="total"
      :page-sizes="[25, 50, 100, 200]"
      @prev="changePage(-1)"
      @next="changePage(1)"
      @change-size="onPageSizeChange"
    />

    <div v-if="detailVisible && detailEntry" class="drawer-backdrop" @click="closeDetail">
      <div class="drawer-panel card drawer-panel-wide" role="dialog" aria-labelledby="audit-detail-title" @click.stop>
        <div class="drawer-header">
          <h3 id="audit-detail-title">{{ t('auditLog.detail.titleWithId', { id: detailEntry.id }) }}</h3>
          <button class="btn btn-sm btn-ghost" @click="closeDetail">{{ t('auditLog.detail.close') }}</button>
        </div>

        <div class="drawer-section detail-meta">
          <span><strong>{{ t('auditLog.detail.metaTime') }}</strong> {{ fmtTs(detailEntry.ts) }}</span>
          <span><strong>{{ t('auditLog.detail.metaActor') }}</strong> {{ detailEntry.actor || t('auditLog.dash') }}</span>
          <span>
            <strong>{{ t('auditLog.detail.metaAction') }}</strong>
            <span class="badge" :class="actionBadgeClass(detailEntry.action)">{{ actionLabel(detailEntry.action) }}</span>
          </span>
          <span v-if="detailEntry.target_type">
            <strong>{{ t('auditLog.detail.metaTarget') }}</strong> {{ detailEntry.target_type }} #{{ detailEntry.target_id ?? '?' }}
          </span>
        </div>

        <div v-if="detailEntry.before_json" class="drawer-section">
          <div class="drawer-section-title">{{ t('auditLog.detail.beforeTitle') }}</div>
          <pre class="json-block">{{ fmtJson(detailEntry.before_json) }}</pre>
        </div>

        <div v-if="detailEntry.after_json" class="drawer-section">
          <div class="drawer-section-title">{{ t('auditLog.detail.afterTitle') }}</div>
          <pre class="json-block">{{ fmtJson(detailEntry.after_json) }}</pre>
        </div>

        <div v-if="!detailEntry.before_json && !detailEntry.after_json" class="drawer-section">
          <p class="text-muted">{{ t('auditLog.detail.noExtra') }}</p>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-header h2 {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.page-desc {
  margin: -12px 0 16px;
  font-size: 12px;
  color: var(--muted);
}

.count-chip {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  color: var(--accent-h);
}

.table-card {
  padding: 0;
  overflow: hidden;
}

.audit-table {
  width: 100%;
  font-size: 12px;
}

.audit-table th,
.audit-table td {
  padding: 8px 12px;
  vertical-align: top;
}

.col-time {
  width: 5rem;
  white-space: nowrap;
}

.col-actor {
  min-width: 6rem;
  max-width: 10rem;
}

.col-action {
  min-width: 7rem;
  max-width: 11rem;
}

.col-target {
  min-width: 6rem;
  max-width: 9rem;
}

.col-details {
  min-width: 12rem;
}

.cell-line1 {
  font-size: 12px;
  line-height: 1.35;
}

.cell-line2 {
  color: var(--muted);
  font-size: 10px;
  line-height: 1.35;
  margin-top: 2px;
  font-variant-numeric: tabular-nums;
}

.actor-name {
  font-weight: 600;
  word-break: break-all;
}

.target-type {
  font-size: 12px;
}

.target-id {
  margin-left: 4px;
  font-family: ui-monospace, monospace;
  font-size: 11px;
  color: var(--muted);
}

.detail-preview {
  display: block;
  font-family: ui-monospace, monospace;
  font-size: 11px;
  color: var(--muted);
  word-break: break-all;
  white-space: pre-wrap;
  line-height: 1.4;
  background: transparent;
  padding: 0;
}

.audit-row {
  cursor: pointer;
}

.audit-row:hover td,
.audit-row:focus-visible td {
  background: color-mix(in srgb, var(--accent) 8%, transparent);
}

.audit-row:focus-visible {
  outline: none;
}

.state-cell {
  text-align: center;
  padding: 36px 16px !important;
  color: var(--muted);
}

.state-cell p {
  margin: 0 0 4px;
}

.text-muted {
  color: var(--muted);
  font-size: 11px;
}

.detail-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 12px 20px;
  font-size: 12px;
}

.json-block {
  margin: 0;
  padding: 12px;
  border-radius: var(--radius);
  border: 1px solid var(--border);
  background: var(--bg);
  font-family: ui-monospace, monospace;
  font-size: 11px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 320px;
  overflow: auto;
}

@media (prefers-reduced-motion: reduce) {
  .audit-row:hover td,
  .audit-row:focus-visible td {
    transition: none;
  }
}
</style>
