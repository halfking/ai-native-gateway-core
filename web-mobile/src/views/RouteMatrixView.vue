<script setup lang="ts">
// RouteMatrixView — 模型 × 任务 热力矩阵（/auto-route/analytics/matrix，**superAdmin 档**）。
//
// 数据源：GET /api/admin/auto-route/analytics/matrix?window=&row=&metric=
//
// 它与已上移的 /funnel 是一对：
//   /funnel  = **单个模型**的请求漏斗（单点深挖：这个模型被筛掉多少）
//   本页     = **全体模型** × 任务的横向对比（全局鸟瞰：哪个模型擅长什么）
//
// ⚠️★ 四个后端语义决定了 UI 的做法（详见 api/autoRouteMatrix.ts 文件头）：
//
// (1) ★★ **矩阵单元的 0 有两种完全不同的含义。**
//     `handleMatrix` 的 cellMap 只装「DB 里真有行」的 (模型,任务) 对，
//     渲染成矩形时缺的格子由 Go 侧零值填充。
//     ⇒ 0 至少可能是「该组合**根本没有记录**」，也可能是**真的** 0
//       （success_rate 0% = 全部失败；cost 0 = 没花钱）。
//     两者运营含义完全相反：前者不用管，后者是故障。
//     ★ 唯一能证明「这格有数据」的办法是**切到 count 指标**
//       （COUNT(*) 对任何产出组 ≥1 ⇒ count=0 ⟺ 无记录）。
//     ⇒ 本页：0 一律弱化渲染（非 count 指标下）并挂常驻说明，
//       绝不把 0 渲染成一个醒目的数字，也绝不说成「无数据」。
//
// (2) ★★ **p95 在 7d 下必是近似值，且两种窗口口径不同。**
//     `useMaterializedView`（analytics_materialized.go:53-58）只在
//     `windowLabel == "7d"` 且 MV 新鲜时返回 true：
//       · 7d + MV → `SUM(p95 * request_count) / SUM(request_count)`
//         = **按请求数加权的各小时 p95 的平均**（不是真 p95，天然偏小）
//       · 24h（MV 恒不启用）→ `percentile_cont(0.95)` = 对原始行的真 p95
//     ⇒ 同一个模型，24h 的 p95 必然 ≥ 7d 的 p95。
//     ★ 响应里**没有字段**告诉客户端这次走的哪条路
//       ⇒ p95 指标下**无条件**标注口径，不能只在 7d 时标。
//
// (3) ★ **`__specified__` 是合成键**（analytics.go:36），表示
//     「请求显式指定了模型、网关没做任务分类」。
//     直接渲染会让人以为有个叫这名字的真实任务类型 ⇒ 必须映射成可读文案。
//
// (4) ★ **行 = 模型，列 = 任务**（2026-06-22 轴向交换，analytics.go:271-275）。
//     「matrix」这个词会让人默认行是任务 ⇒ 变量名与文案都钉死这个方向。
//
// (5) **本端点没有降级字段。** meta 里没有 approximate/degraded
//     ⇒ 与 funnel 不同，这里不存在「近似但没说」的形态。唯一的非精确是 (2)。
//
// 整条 analytics 线是 superAdmin（handler.go:1430），导航已按 requiresRole 挡住。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import { t } from '@/i18n'
import {
  fetchRouteMatrix,
  metricKind,
  cellMayBeAbsent,
  formatMatrixValue,
  taskLabel,
  MATRIX_ROWS,
  MATRIX_METRICS,
  P95_IS_ALWAYS_APPROXIMATE,
  type MatrixMetric,
  type MatrixRowDim,
  type MatrixResponse,
} from '@/api/autoRouteMatrix'
import { ANALYTICS_WINDOWS, type AnalyticsWindow } from '@/api/autoRouteInsights'

useHyperPage({ title: () => t('matrix.title') })

const windowSel = ref<AnalyticsWindow>('7d')
const rowDim = ref<MatrixRowDim>('task_type')
const metric = ref<MatrixMetric>('count')
const data = ref<MatrixResponse | null>(null)
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    data.value = await fetchRouteMatrix({ window: windowSel.value, row: rowDim.value, metric: metric.value })
  } catch (err) {
    data.value = null
    error.value = describeError(err)
  } finally {
    loading.value = false
    loaded.value = true
  }
}
void load()

function describeError(err: unknown): string {
  const statusCode = (err as { status?: number })?.status
  if (statusCode === 403) return t('matrix.errForbidden')
  return (err instanceof Error ? err.message : String(err)) || t('common.error')
}

const ROW_CHIPS = computed(() => MATRIX_ROWS.filter((r) => r !== ''))
const METRIC_CHIPS = computed(() => MATRIX_METRICS.filter((m) => m !== ''))

function pickWindow(w: AnalyticsWindow): void {
  if (windowSel.value === w) return
  windowSel.value = w
  void load()
}
function pickRow(r: MatrixRowDim): void {
  if (rowDim.value === r) return
  rowDim.value = r
  void load()
}
function pickMetric(m: MatrixMetric): void {
  if (metric.value === m) return
  metric.value = m
  void load()
}

const rows = computed(() => data.value?.rows ?? [])
const cols = computed(() => data.value?.cols ?? [])
const cells = computed(() => data.value?.cells ?? [])

function cellAt(i: number, j: number): number | null {
  const row = cells.value[i]
  if (!row) return null
  const v = row[j]
  return typeof v === 'number' ? v : null
}

/** ★ 0 的可信度。见文件头 (1)：只有 count 指标能判定 0 = 无记录。 */
const zeroIsUncertain = computed(() => cellMayBeAbsent(metric.value))

function isZero(v: number | null): boolean {
  return v === 0
}

function cellClass(i: number, j: number): string {
  const v = cellAt(i, j)
  if (v === null) return 'mx__cell--none'
  // ★ 非 count 指标下的 0 是「无记录 or 真 0」⇒ 弱化，不给数字的视觉重量
  if (isZero(v)) return zeroIsUncertain.value ? 'mx__cell--faint' : 'mx__cell--empty'
  return 'mx__cell--has'
}

function cellTitle(r: string, c: string, v: number | null): string {
  const name = `${r} × ${taskLabel(c, t)}`
  if (v === null) return `${name}：${t('matrix.noCell')}`
  if (isZero(v) && zeroIsUncertain.value) return `${name}：${t('matrix.zeroAmbiguous')}`
  return `${name}：${formatMatrixValue(metric.value, v)}`
}

/** 该行模型归并掉的别名数。>1 说明「模型好像改了名字」这件事真发生过。 */
function aliasCount(r: string): number {
  const a = data.value?.meta?.row_aliases
  if (!a) return 0
  return Array.isArray(a[r]) ? a[r]!.length : 0
}

// ★ 口径提示的显示条件里**接进** API 层的常量，而不是把它当只测不用的探针。
//   若将来后端加字段能说明本次走的是精确 p95，把常量改成 false，
//   这条提示会自动消失 —— 而不是留一条永远显示的噪音。
const isP95 = computed(() => metricKind(metric.value) === 'duration' && P95_IS_ALWAYS_APPROXIMATE)
const isEmpty = computed(() => loaded.value && !loading.value && rows.value.length === 0)

onBeforeUnmount(() => {
  data.value = null
})
</script>

<template>
  <div class="view-root mx">
    <section class="mx__filters">
      <div class="mx__row" role="group" :aria-label="t('matrix.window')">
        <button
          v-for="w in ANALYTICS_WINDOWS"
          :key="w"
          type="button"
          class="mx__chip"
          :class="{ 'mx__chip--on': windowSel === w }"
          :aria-pressed="windowSel === w"
          @click="pickWindow(w)"
        >
          {{ t('funnel.window' + w) }}
        </button>
      </div>

      <div class="mx__row" role="group" :aria-label="t('matrix.rowDim')">
        <button
          v-for="r in ROW_CHIPS"
          :key="r"
          type="button"
          class="mx__chip mx__chip--sub"
          :class="{ 'mx__chip--on': rowDim === r }"
          :aria-pressed="rowDim === r"
          @click="pickRow(r)"
        >
          {{ t('matrix.row.' + r) }}
        </button>
      </div>

      <!-- ★ 指标是本页最重要的一组 chip：换 metric 换的是**量纲**，
           不只是换个数字。p95 / cost / success_rate 的 0 含义各不相同。 -->
      <div class="mx__row" role="group" :aria-label="t('matrix.metric')">
        <button
          v-for="m in METRIC_CHIPS"
          :key="m"
          type="button"
          class="mx__chip mx__chip--sub"
          :class="{ 'mx__chip--on': metric === m }"
          :aria-pressed="metric === m"
          @click="pickMetric(m)"
        >
          {{ t('matrix.metricName.' + m) }}
        </button>
      </div>
    </section>

    <p v-if="error" class="mx__msg mx__msg--err">{{ error }}</p>

    <!-- ★ p95 口径常驻说明。不是「7d 时才提示」——
         响应里没有字段能告诉客户端走了哪条路，只能无条件标。 -->
    <p v-if="isP95" class="mx__note mx__note--warn">
      <AppIcon name="alert" :size="14" />
      <span>{{ t('matrix.p95Note') }}</span>
    </p>

    <!-- ★ 0 的歧义常驻说明。措辞刻意是「可能是」，不是「就是」——
         后端给不出判据，我们也不替它编。 -->
    <p v-else-if="zeroIsUncertain" class="mx__note">
      <AppIcon name="alert" :size="14" />
      <span>{{ t('matrix.zeroNote') }}</span>
    </p>

    <p v-if="loading" class="mx__msg">{{ t('common.loading') }}</p>
    <p v-else-if="isEmpty" class="mx__msg">{{ t('matrix.empty') }}</p>
    <p v-else-if="!rows.length || !cols.length" class="mx__msg">{{ t('matrix.empty') }}</p>

    <div v-else class="mx__scroll">
      <div class="mx__grid" :style="{ gridTemplateColumns: `minmax(96px, auto) repeat(${cols.length}, minmax(64px, 1fr))` }">
        <!-- ★ 左上角表头：行=模型、列=任务。见文件头 (4)。 -->
        <div class="mx__corner">
          <span class="mx__corner-axis">{{ t('matrix.rowAxis') }}</span>
          <span class="mx__corner-axis mx__corner-axis--col">{{ t('matrix.colAxis') }}</span>
        </div>
        <div v-for="c in cols" :key="c" class="mx__colhead" :title="taskLabel(c, t)">
          {{ taskLabel(c, t) }}
        </div>

        <template v-for="(r, i) in rows" :key="r">
          <div class="mx__rowhead">
            <span class="mx__rowhead-name">{{ r }}</span>
            <!-- ★ 有别名 = 「模型好像改了名字」这件事真发生过，值得说。 -->
            <span v-if="aliasCount(r) > 1" class="mx__alias" :title="t('matrix.aliasNote')">
              +{{ aliasCount(r) - 1 }}
            </span>
          </div>
          <div
            v-for="(c, j) in cols"
            :key="r + '|' + c"
            class="mx__cell"
            :class="cellClass(i, j)"
            :title="cellTitle(r, c, cellAt(i, j))"
          >
            {{ formatMatrixValue(metric, cellAt(i, j)) }}
          </div>
        </template>
      </div>
    </div>

    <p v-if="!loading && rows.length" class="mx__count">
      {{ t('matrix.count', { r: rows.length, c: cols.length }) }}
    </p>
  </div>
</template>

<style scoped>
.mx {
  padding: var(--app-space-3);
}
.mx__filters {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-3);
}
.mx__row {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.mx__chip {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.mx__chip--sub {
  font-size: 12px;
}
.mx__chip--on {
  background: var(--app-primary);
  border-color: var(--app-primary);
  color: var(--app-on-primary);
}
.mx__msg {
  margin: 0 0 var(--app-space-2);
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.mx__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.mx__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0 0 var(--app-space-2);
  padding: 8px 10px;
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.mx__note--warn {
  background: var(--app-warning-soft);
  color: var(--app-warning);
}
.mx__scroll {
  /* ★ 宽表在窄屏必然要横滚。横滚容器是 scrollable region 不是控件，
     但里面每一格都要够大可点 —— 单元 48px 高，chip 48px 高。 */
  overflow-x: auto;
  -webkit-overflow-scrolling: touch;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
}
.mx__grid {
  display: grid;
  min-width: 100%;
}
.mx__corner,
.mx__colhead {
  position: sticky;
  top: 0;
  background: var(--app-surface-muted);
  border-bottom: 1px solid var(--app-border);
  padding: 6px;
  font-size: 11px;
  color: var(--app-text-muted);
  z-index: 2;
}
.mx__corner {
  left: 0;
  z-index: 3;
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 2px;
  border-right: 1px solid var(--app-border);
}
.mx__corner-axis--col {
  color: var(--app-text-secondary);
}
.mx__colhead {
  text-align: center;
  word-break: break-word;
  line-height: 1.3;
}
.mx__rowhead {
  position: sticky;
  left: 0;
  z-index: 1;
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 6px 8px;
  background: var(--app-surface-muted);
  border-right: 1px solid var(--app-border);
  border-bottom: 1px solid var(--app-border);
  font-size: 11px;
  color: var(--app-text);
  word-break: break-word;
}
.mx__rowhead-name {
  min-width: 0;
}
.mx__alias {
  flex-shrink: 0;
  padding: 0 4px;
  border-radius: var(--app-radius-pill);
  background: var(--app-primary-soft);
  color: var(--app-primary);
  font-size: 10px;
}
.mx__cell {
  /* ★ 触控门要求新增控件 ≥48px。矩阵单元在窄屏上是会被点/被扫的
     （用户会想确认某一格到底是 0 还是没数据），44px 在手机上偏小。
     ⇒ 按 48 走。门不通过时唯一能用的豁免是 R1-legacy「新存量」，
     拿它盖住**新写的**代码就是撒谎。 */
  min-height: 48px;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 4px;
  border-bottom: 1px solid var(--app-border);
  border-right: 1px solid var(--app-border);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  text-align: center;
  word-break: break-all;
}
.mx__cell--has {
  color: var(--app-text);
  font-weight: 600;
}
/* ★ count 下的 0 可以确定是「无记录」⇒ 显示明确但弱。 */
.mx__cell--empty {
  color: var(--app-text-muted);
  background: var(--app-surface-muted);
}
/* ★ 非 count 下的 0 含义未定（无记录 or 真 0）⇒ 更弱，且不铺底色，
     避免看起来像「这一格确确实实量到了 0」。 */
.mx__cell--faint {
  color: var(--app-text-muted);
  opacity: 0.6;
}
.mx__cell--none {
  color: var(--app-text-muted);
  opacity: 0.4;
}
.mx__count {
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
}
</style>
