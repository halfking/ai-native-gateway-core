<script setup lang="ts">
// ApprovalQueueView — 审批队列（/approval-queue，**admin 档**）。
//
// 数据源（两条，均 admin 档，api/approvals.ts 文件头）：
//   GET /api/admin/approvals         待办/已办的审批实例列表
//   GET /api/admin/approvals/stats   审批统计
//
// 与既有页面的关系（不重叠）：
//   /approval-config  租户审批**配置**（走 api/approvalConfig.ts）
//   /approval-rules   审批人与**规则**（同）
//   本页              运行中的**审批实例**（走 api/approvals.ts）
//   配置说「该问谁」，规则说「什么条件下拦」，本页说「现在有几条在等」。
//
// ## ⚠️ 权限档位
// cmd/gateway/main.go:7384-7385 两条注册都是 `wrapAdmin(...)`
// ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
//
// ## 不碰写操作
// `/api/v1/approvals/*` 下的 approve / reject / resume 三条
// （main.go:7375-7381）**真的改变审批状态** —— 会导致超时、影响会话是否放行。
// 本页是纯只读队列。
//
// ## ★★★★★ 五处「不能都渲染成同一个东西」
//
// (1) ★★★★★ **`total` 不是真实总数**，它是**本页条数**：
//      `total := len(records)` 而 `records` 已带 Limit/Offset
//      （approval_handler.go:348-350）。连带 `total_pages` **恒为 1**。
//      ⇒ UI 显示的是「本页 N 条」，不是「共 N 条」；
//      ⇒ 翻页判据**不能**用 `total_pages`，只能用「本页取满」。
//
// (2) ★★★★ **`risk_level` / `trigger_type` 可以是空串** ——
//      `buildListItem` 只在 `record.DetectResult != nil` 时才填（:546-549）
//      ⇒ 空串 = **未检测**，不是「低风险」。
//
// (3) ★★★★ **`time_left` 带 omitempty**，只在 pending 且未过期时出现
//      ⇒ **pending 但没有 time_left = 已过期却还标着待审批**。
//      这两者的处置完全相反：一个该显示倒计时，一个该显示「已逾期」。
//
// (4) ★★★ **状态筛选缺省是 `pending`**（:515-517），不是「全部」。
//      ⇒ 页面上必须**明示**当前看的是哪一档，否则「列表为空」会被误读成
//      「一条都没有」而实际是「已全部处理完」。
//
// (5) ★★★ **统计端混着两套口径**：
//      `today_total` / `today_pending` 按「今天」算，
//      与 `start_time`/`end_time` 控制的其余八个字段**无关**
//      ⇒ 同一个 KPI 卡里不能把两者并列成同一口径。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { fmtInt, fmtNum } from '@/utils/format'
import {
  fetchApprovals,
  fetchApprovalStats,
  approvalsPageEffective,
  approvalsPageSizeEffective,
  approvalsStatusDefault,
  approvalsTotalIsPageSize,
  approvalsHasNextPage,
  approvalRiskUnknown,
  approvalCountingDown,
  approvalOverdue,
  approvalDecided,
  approvalCountsDisagree,
  approvalAvgTimeMeaningless,
  APPROVAL_STATUSES,
  type ApprovalStatus,
  type ApprovalListResponse,
  type ApprovalStats,
  type ApprovalItem,
} from '@/api/approvals'

useHyperPage({ title: () => t('approvalQueue.title') })

/** 无数据占位。字形与 0 不同，且带独立 class。 */
const NO_DATA = '—'

type SectionKey = 'stats' | 'list'

const stats = ref<ApprovalStats | null>(null)
const list = ref<ApprovalListResponse | null>(null)

const loading = ref<SectionKey | null>(null)
const loaded = ref<Record<SectionKey, boolean>>({ stats: false, list: false })
const error = ref<Record<SectionKey, string | null>>({ stats: null, list: null })

/* ── 筛选与分页 ────────────────────────────────────────────────────── */

/** ★ 缺省是 pending（后端 :515-517），页面必须显式呈现。 */
const status = ref<ApprovalStatus>('pending')
const page = ref(1)
const pageSize = ref(50)

const statusEcho = computed(() => approvalsStatusDefault(status.value))
const pageEcho = computed(() => approvalsPageEffective(page.value))
const pageSizeEcho = computed(() => approvalsPageSizeEffective(pageSize.value))

function describeError(err: unknown): string {
  const statusCode = (err as { status?: number })?.status
  if (statusCode === 403) return t('approvalQueue.errForbidden')
  return (err instanceof Error ? err.message : String(err)) || t('common.error')
}

async function load(section: SectionKey): Promise<void> {
  loading.value = section
  error.value[section] = null
  try {
    if (section === 'stats') {
      stats.value = await fetchApprovalStats()
    } else {
      list.value = await fetchApprovals({ status: status.value, page: page.value, pageSize: pageSize.value })
    }
  } catch (err) {
    if (section === 'stats') stats.value = null
    if (section === 'list') list.value = null
    error.value[section] = describeError(err)
  } finally {
    loading.value = null
    loaded.value[section] = true
  }
}

/* ── stats (5) ─────────────────────────────────────────────────────── */

const countsDisagree = computed(() => (stats.value ? approvalCountsDisagree(stats.value) : false))
const avgTimeMeaningless = computed(() => (stats.value ? approvalAvgTimeMeaningless(stats.value) : false))

const avgTimeText = computed(() => {
  const s = stats.value
  if (!s) return NO_DATA
  // ★ 分母为 0 时那个 0 无意义，不能讲成「0 秒审批完成」
  if (avgTimeMeaningless.value) return t('approvalQueue.stats.avgNoSamples')
  return fmtNum(s.avg_approval_time_seconds, 1) + t('approvalQueue.stats.secondsUnit')
})

function riskEntries(s: ApprovalStats): Array<[string, number]> {
  return Object.entries(s.by_risk_level)
}

function triggerEntries(s: ApprovalStats): Array<[string, number]> {
  return Object.entries(s.by_trigger_type)
}

/* ── list (1)(2)(3)(4) ─────────────────────────────────────────────── */

const rows = computed<ApprovalItem[]>(() => list.value?.items ?? [])
const hasNext = computed(() => (list.value ? approvalsHasNextPage(list.value) : false))
/** ★★★ `total` 是本页条数 —— 这个判据是 UI 不把它当「总数」的依据。 */
const totalIsPageSize = computed(() => (list.value ? approvalsTotalIsPageSize(list.value) : false))

const overdueCount = computed(() => rows.value.filter(approvalOverdue).length)

function gotoPage(delta: number): void {
  page.value = Math.max(1, page.value + delta)
  void load('list')
}

/** ★ 空串 = 未检测，绝不渲染成「低风险」。 */
function riskText(item: ApprovalItem): { text: string; unknown: boolean } {
  if (approvalRiskUnknown(item)) return { text: t('approvalQueue.notDetected'), unknown: true }
  return { text: item.risk_level, unknown: false }
}

/** ★★★ 三态必须互斥：倒计时 / 已逾期 / 已决定。 */
type ItemState = 'counting' | 'overdue' | 'decided' | 'other'
function itemState(item: ApprovalItem): ItemState {
  if (approvalCountingDown(item)) return 'counting'
  if (approvalOverdue(item)) return 'overdue'
  if (approvalDecided(item)) return 'decided'
  return 'other'
}

function stateText(item: ApprovalItem): string {
  const s = itemState(item)
  if (s === 'counting') return item.time_left!
  if (s === 'overdue') return t('approvalQueue.overdue')
  if (s === 'decided') return item.approved_at ?? t('approvalQueue.decided')
  return NO_DATA
}

/** ★ `status` 是自由字符串（后端不校验），未知值要给出说明而不是原样透传。 */
const KNOWN_STATUSES: readonly string[] = APPROVAL_STATUSES
function statusText(item: ApprovalItem): { text: string; known: boolean } {
  if (KNOWN_STATUSES.includes(item.status)) return { text: t(`approvalQueue.status.${item.status}`), known: true }
  return { text: t('approvalQueue.statusUnknown', { raw: item.status }), known: false }
}

onBeforeUnmount(() => {
  stats.value = null
  list.value = null
})
</script>

<template>
  <div class="view-root aq">
    <!-- ══ 1. 审批统计 ═══════════════════════════════════════════════ -->
    <section class="aq__section">
      <header class="aq__head">
        <h2 class="aq__title">{{ t('approvalQueue.stats.title') }}</h2>
        <button
          type="button"
          class="aq__load"
          :disabled="loading === 'stats'"
          @click="load('stats')"
        >
          {{ loaded.stats && !error.stats ? t('approvalQueue.reload') : t('approvalQueue.load') }}
        </button>
      </header>

      <p v-if="error.stats" class="aq__msg aq__msg--err">{{ error.stats }}</p>
      <p v-if="loading === 'stats'" class="aq__msg">{{ t('common.loading') }}</p>

      <template v-if="stats">
        <!-- ★ 四个状态加起来对不上 total ⇒ 口径出问题 -->
        <p v-if="countsDisagree" class="aq__msg aq__msg--err">
          {{ t('approvalQueue.stats.countsDisagree') }}
        </p>

        <div class="aq__kpis">
          <div class="aq__kpi">
            <span class="aq__kpi-label">{{ t('approvalQueue.stats.total') }}</span>
            <span class="aq__kpi-value">{{ fmtInt(stats.total) }}</span>
          </div>
          <div class="aq__kpi">
            <span class="aq__kpi-label">{{ t('approvalQueue.stats.pending') }}</span>
            <span class="aq__kpi-value">{{ fmtInt(stats.pending) }}</span>
          </div>
          <div class="aq__kpi">
            <span class="aq__kpi-label">{{ t('approvalQueue.stats.approved') }}</span>
            <span class="aq__kpi-value">{{ fmtInt(stats.approved) }}</span>
          </div>
          <div class="aq__kpi">
            <span class="aq__kpi-label">{{ t('approvalQueue.stats.timeout') }}</span>
            <span class="aq__kpi-value">{{ fmtInt(stats.timeout) }}</span>
          </div>
        </div>

        <dl class="aq__kv">
          <!-- ★★ 分母为 0 时无意义，不是「0 秒」 -->
          <div class="aq__kv-row">
            <dt>{{ t('approvalQueue.stats.avgTime') }}</dt>
            <dd :class="{ 'aq__nodata': avgTimeMeaningless }">{{ avgTimeText }}</dd>
          </div>
          <div class="aq__kv-row">
            <dt>{{ t('approvalQueue.stats.rejected') }}</dt>
            <dd>{{ fmtInt(stats.rejected) }}</dd>
          </div>
        </dl>

        <!-- ★★★ 今天口径与区间口径混在同一份响应里，必须分区并各自说明 -->
        <p class="aq__echo">{{ t('approvalQueue.stats.scopeNote') }}</p>
        <div class="aq__kpis">
          <div class="aq__kpi">
            <span class="aq__kpi-label">{{ t('approvalQueue.stats.todayTotal') }}</span>
            <span class="aq__kpi-value">{{ fmtInt(stats.today_total) }}</span>
          </div>
          <div class="aq__kpi">
            <span class="aq__kpi-label">{{ t('approvalQueue.stats.todayPending') }}</span>
            <span class="aq__kpi-value">{{ fmtInt(stats.today_pending) }}</span>
          </div>
        </div>

        <ul v-if="riskEntries(stats).length" class="aq__chips">
          <li v-for="[k, v] in riskEntries(stats)" :key="k" class="aq__chip">
            {{ k }} · {{ fmtInt(v) }}
          </li>
        </ul>
        <ul v-if="triggerEntries(stats).length" class="aq__chips">
          <li v-for="[k, v] in triggerEntries(stats)" :key="k" class="aq__chip">
            {{ k }} · {{ fmtInt(v) }}
          </li>
        </ul>
      </template>
    </section>

    <!-- ══ 2. 审批队列 ═══════════════════════════════════════════════ -->
    <section class="aq__section">
      <header class="aq__head">
        <h2 class="aq__title">{{ t('approvalQueue.list.title') }}</h2>
      </header>

      <div class="aq__filters">
        <label class="aq__field">
          <span class="aq__field-label">{{ t('approvalQueue.statusFilter') }}</span>
          <select v-model="status" class="aq__input">
            <option v-for="s in APPROVAL_STATUSES" :key="s" :value="s">
              {{ t(`approvalQueue.status.${s}`) }}
            </option>
          </select>
        </label>
        <label class="aq__field">
          <span class="aq__field-label">{{ t('approvalQueue.pageSize') }}</span>
          <input v-model.number="pageSize" type="number" class="aq__input" min="1" max="200" />
        </label>
        <!-- ★ 回显后端**实际生效**的取值（page_size >200 会被静默回落 50） -->
        <p class="aq__echo">
          {{ t('approvalQueue.effective', { status: statusEcho, page: pageEcho, pageSize: pageSizeEcho }) }}
        </p>
        <button type="button" class="aq__load" :disabled="loading === 'list'" @click="load('list')">
          {{ loaded.list && !error.list ? t('approvalQueue.reload') : t('approvalQueue.load') }}
        </button>
      </div>

      <p v-if="error.list" class="aq__msg aq__msg--err">{{ error.list }}</p>
      <p v-if="loading === 'list'" class="aq__msg">{{ t('common.loading') }}</p>

      <template v-if="list">
        <!-- ★★★ total 是**本页条数**，不是库里总数 -->
        <p class="aq__echo">
          {{ t('approvalQueue.list.pageCount', { n: list.total }) }}
        </p>
        <!-- ★ 与 items.length 对不上 ⇒ 后端契约漂移 -->
        <p v-if="!totalIsPageSize" class="aq__msg aq__msg--err">
          {{ t('approvalQueue.list.totalMismatch', { total: list.total, shown: list.items.length }) }}
        </p>

        <!-- ★ 已过期的待审批要单独提醒：它们还在挡着会话 -->
        <p v-if="overdueCount > 0" class="aq__msg aq__msg--warn">
          {{ t('approvalQueue.list.overdueWarn', { n: overdueCount }) }}
        </p>

        <!-- ★ 空列表 + 非 pending 筛选 ⇒ 说明是「这一档没有」，不是「一条都没有」 -->
        <p v-if="!rows.length && loaded.list && !error.list" class="aq__msg">
          {{ t('approvalQueue.list.emptyForStatus', { status: t(`approvalQueue.status.${statusEcho}`) }) }}
        </p>

        <ul v-if="rows.length" class="aq__list">
          <li v-for="item in rows" :key="item.id" class="aq__item">
            <div class="aq__item-head">
              <span class="aq__item-title">{{ item.session_id }}</span>
              <!-- ★ 未知 status 要说明，不能原样透传 -->
              <span
                class="aq__badge"
                :class="{ 'aq__badge--unknown': !statusText(item).known }"
              >{{ statusText(item).text }}</span>
            </div>

            <dl class="aq__kv">
              <div class="aq__kv-row">
                <dt>{{ t('approvalQueue.risk') }}</dt>
                <!-- ★ 空串 = 未检测，绝不渲染成「低风险」 -->
                <dd :class="{ 'aq__nodata': riskText(item).unknown }">{{ riskText(item).text }}</dd>
              </div>
              <div class="aq__kv-row">
                <dt>{{ t('approvalQueue.trigger') }}</dt>
                <dd :class="{ 'aq__nodata': item.trigger_type === '' }">
                  {{ item.trigger_type === '' ? t('approvalQueue.notDetected') : item.trigger_type }}
                </dd>
              </div>
              <div class="aq__kv-row">
                <dt>{{ t('approvalQueue.state') }}</dt>
                <!-- ★★★ 倒计时 / 已逾期 / 已决定 三态互斥 -->
                <dd
                  :class="{
                    'aq__nodata': itemState(item) === 'other',
                    'aq__overdue': itemState(item) === 'overdue',
                  }"
                >{{ stateText(item) }}</dd>
              </div>
              <div v-if="item.reason" class="aq__kv-row">
                <dt>{{ t('approvalQueue.reason') }}</dt>
                <dd>{{ item.reason }}</dd>
              </div>
            </dl>
          </li>
        </ul>

        <!-- ★ 翻页只能靠「本页取满」，不能信 total_pages（恒为 1） -->
        <div class="aq__pager">
          <button
            type="button"
            class="aq__pager-btn"
            :disabled="pageEcho <= 1"
            @click="gotoPage(-1)"
          >{{ t('approvalQueue.prev') }}</button>
          <span class="aq__pager-label">{{ t('approvalQueue.page', { page: pageEcho }) }}</span>
          <button
            type="button"
            class="aq__pager-btn"
            :disabled="!hasNext"
            @click="gotoPage(1)"
          >{{ t('approvalQueue.next') }}</button>
        </div>
      </template>
    </section>
  </div>
</template>

<style scoped>
/* ApprovalQueueView — 触控热区一律 ≥48 CSS px（UI规范 17 §4-R1）。 */
.aq__section {
  margin-bottom: 16px;
}
.aq__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}
.aq__title {
  font-size: 15px;
  font-weight: 600;
  margin: 0;
}
.aq__load {
  min-height: 48px;
  min-width: 88px;
  padding: 0 16px;
  border: 1px solid var(--border, #d0d7de);
  border-radius: 8px;
  background: var(--surface, #fff);
  color: inherit;
  font-size: 14px;
  cursor: pointer;
}
.aq__load:disabled {
  opacity: 0.6;
  cursor: default;
}
.aq__filters {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: flex-end;
  margin-bottom: 12px;
}
.aq__field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.aq__field-label {
  font-size: 12px;
  opacity: 0.75;
}
.aq__input {
  min-height: 48px;
  padding: 0 10px;
  border: 1px solid var(--border, #d0d7de);
  border-radius: 8px;
  font-size: 14px;
}
.aq__msg {
  font-size: 13px;
  margin: 4px 0;
  opacity: 0.8;
}
.aq__msg--err {
  color: var(--danger, #cf222e);
  opacity: 1;
}
.aq__msg--warn {
  color: var(--warning, #9a6700);
  opacity: 1;
}
.aq__echo {
  font-size: 12px;
  opacity: 0.7;
  margin: 4px 0;
}
.aq__kpis {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}
.aq__kpi {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 10px;
  border: 1px solid var(--border, #d0d7de);
  border-radius: 8px;
}
.aq__kpi-label {
  font-size: 12px;
  opacity: 0.75;
}
.aq__kpi-value {
  font-size: 18px;
  font-weight: 600;
}
/* ★ 无意义/未知的占位：字形与 class 双锚 */
.aq__nodata {
  opacity: 0.55;
  font-style: italic;
}
/* ★ 「已逾期」要有独立底色 —— 它是本族唯一「会挡住会话」的状态 */
.aq__overdue {
  color: var(--danger, #cf222e);
  font-weight: 600;
  opacity: 1;
}
.aq__badge {
  font-size: 12px;
  padding: 4px 8px;
  border-radius: 999px;
  background: rgba(31, 111, 235, 0.12);
}
.aq__badge--unknown {
  background: rgba(154, 103, 0, 0.14);
  color: var(--warning, #9a6700);
}
.aq__kv {
  margin: 8px 0 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.aq__kv-row {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  font-size: 13px;
}
.aq__kv dt {
  opacity: 0.75;
}
.aq__kv dd {
  margin: 0;
  text-align: right;
  word-break: break-all;
}
.aq__list {
  list-style: none;
  margin: 8px 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.aq__item {
  border: 1px solid var(--border, #d0d7de);
  border-radius: 8px;
  padding: 10px;
}
.aq__item-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 4px;
}
.aq__item-title {
  font-size: 14px;
  font-weight: 600;
  word-break: break-all;
}
.aq__chips {
  list-style: none;
  margin: 8px 0 0;
  padding: 0;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.aq__chip {
  font-size: 12px;
  padding: 8px 10px;
  border: 1px solid var(--border, #d0d7de);
  border-radius: 999px;
  min-height: 48px;
  display: inline-flex;
  align-items: center;
}
.aq__pager {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 12px;
}
.aq__pager-btn {
  min-height: 48px;
  min-width: 72px;
  padding: 0 14px;
  border: 1px solid var(--border, #d0d7de);
  border-radius: 8px;
  background: var(--surface, #fff);
  color: inherit;
  font-size: 14px;
  cursor: pointer;
}
.aq__pager-btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.aq__pager-label {
  font-size: 13px;
  opacity: 0.75;
}
</style>