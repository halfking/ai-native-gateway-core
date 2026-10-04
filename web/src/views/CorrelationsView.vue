<script setup lang="ts">
import { useI18n } from 'vue-i18n'
// CorrelationsView.vue — Auto-route correlation analysis (P7.4).
//
// Visualises the 5 tables returned by
//   GET /api/admin/auto-route/correlations?days=7
// (see admin/auto_route_correlations.go).
//
// The 5 tables are:
//   1. by_model       — per-model success/latency/cost
//   2. by_strategy    — per-strategy success/latency/cost
//   3. by_task_type   — per-task_type success/latency/cost
//   4. by_model_task  — per-(model, task_type) detail
//   5. verdict        — top-3 models per task type, ranked

import { ref, computed, onMounted } from 'vue'
import KxDateRangePicker from '../components/ui/KxDateRangePicker.vue'
import { useSpanDaysRange } from '../composables/useSpanDaysRange'
import type { KxDateRange } from '../components/ui/kx-date-types'
import { useWindowClass } from '../composables/useWindowClass'
import ResponsiveDataView from '../components/ui/ResponsiveDataView.vue'
import type { CardField } from '../components/ui/CardList.vue'
import {
  getAutoRouteCorrelations,
  type AutoRouteCorrelationsResponse,
  type CorrelationRow,
  type CorrelationRowMT,
  type CorrelationVerdict,
} from '../api'

const { t } = useI18n()
const { isCompact } = useWindowClass()

// ── State ────────────────────────────────────────────────────────
const resp = ref<AutoRouteCorrelationsResponse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
const days = ref(7)
const minSamples = ref(20)
const { presets: dayPresets, rangeValue, applyRange } = useSpanDaysRange(days, [
  { days: 1, labelKey: 'common.dateRange.preset.today' },
  { days: 7, labelKey: 'common.dateRange.preset.last7d' },
  { days: 30, labelKey: 'common.dateRange.preset.last30d' },
  { days: 90, labelKey: 'dashboard.range.last90d' },
])

function onRangeApply(range: KxDateRange) {
  applyRange(range)
  void load()
}

async function load() {
  loading.value = true
  error.value = null
  try {
    const data = await getAutoRouteCorrelations({
      days: days.value,
      min_samples: minSamples.value,
    })
    // 防御：模板多处直接写 `resp.by_model.length`。后端若吐 `null`（Go nil 切片）
    // 而不是 `[]`，null.length 抛 TypeError ⇒ /correlations 整页白屏。
    // 后端已在 normalizeCorrelationsResponse 收敛，这里再兜一层，
    // 让「某个数组字段缺失」只影响那一张表，不决定整页生死。
    resp.value = {
      ...data,
      by_model: Array.isArray(data.by_model) ? data.by_model : [],
      by_strategy: Array.isArray(data.by_strategy) ? data.by_strategy : [],
      by_task_type: Array.isArray(data.by_task_type) ? data.by_task_type : [],
      by_model_task: Array.isArray(data.by_model_task) ? data.by_model_task : [],
      verdict: Array.isArray(data.verdict) ? data.verdict : [],
    }
  } catch (e: any) {
    error.value = e?.message ?? String(e)
  } finally {
    loading.value = false
  }
}

// ── Helpers ──────────────────────────────────────────────────────
function successColor(rate: number): string {
  if (rate >= 0.95) return '#22c55e'
  if (rate >= 0.85) return '#84cc16'
  if (rate >= 0.7) return '#eab308'
  if (rate >= 0.5) return '#f97316'
  return '#ef4444'
}

function fmtPct(v: number, digits = 1): string {
  return (v * 100).toFixed(digits) + '%'
}

function fmtMs(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

function fmtUsd(v: number): string {
  if (v === 0) return '$0'
  if (v < 0.001) return `$${(v * 1000).toFixed(2)}m`
  return `$${v.toFixed(4)}`
}

/** 样本数千分位。桌面原本内联 `x.toLocaleString()`（4 处各写一遍），抽出来给卡片共用。 */
function fmtCount(n: number): string {
  return n.toLocaleString()
}

/**
 * ── H6 第十三条切片（2026-10-06）：4 张关联表接 compact 卡片形态 ─────────────────
 *
 * ## 无分页 API ⇒ 只改呈现形态
 *
 * `getAutoRouteCorrelations({days, min_samples})` 一次取回 5 段
 * ⇒ 与第七/八/十/十二同源，**不引入连续加载**（门禁断「页面里不存在
 * `HyperLoadMore` / `createHyperPages`」）。
 *
 * ## 3 张同构表共用**一份**字段政策
 *
 * by_model / by_strategy / by_task_type 的桌面结构逐字相同（各 5 列）。
 * 抽 `rowCardFields` 一份给三处用 —— 写三份就等于给「卡片与表格不一致」留后门。
 *
 * ## 三态归属：**第一种形态（表内三态）** —— `#table` 槽内保留 `v-if`
 *
 * 桌面空态时整张表都不渲染（`<table v-if>` + `<p v-else class="empty">`）。
 * 把 `v-if` 提到容器外 ⇒ 空态下多出一个带边框的**空表壳**（切片七槽内保留的理由）。
 * 容器上的 `:empty` 必须带 `isCompact` 前置，桌面那条 `<p>` 加 `!isCompact` 互斥
 * —— 否则两档同时出、或两档都不出。切片八同源。
 *
 * ## 第四张表的身份是**复合键**：`model` + `task_type`
 *
 * `CardList` 的 `:key` 取自 `titleKey` 的**原始值**，取不到才回退 `idx-N`。
 * 直接 `title-key="model"` ⇒ 同一模型的不同 task_type 撞键（Vue 复用错乱）。
 * ⇒ `modelTaskRows` 逐行补 `card_key = model · task_type`，
 * 卡头与键**同源**（与连接表一个道理），不需要 `titleFormat`。
 * 分隔符用 `·` 而不是桌面 `:key` 里的 `-`：模型名本身带连字符（`gpt-4-chat`），
 * `-` 作分隔符会连读成一片。
 *
 * ## `successTone` 是 5 档 → 3 档的**有意的再编码**（不是漏）
 *
 * 桌面 `successColor` 吐 5 个裸 hex，且在 `<script>` 内 ⇒ §4.6 那条 color 门
 * 只扫 `<style>`，看不见它们。`CardField.tone` 只有 4 个取值，于是按**同一组阈值**
 * （0.95 / 0.85 / 0.7 / 0.5）合并相邻档：≥0.85 good、≥0.7 warn、其余 danger。
 * 0.7 这个切点取自本页自己的提示文案（"success < 70% in a sea of > 95%"），
 * 是这一页原本就在用的判读线。精确档位不丢：数值（`92.0%`）本来就在卡片上，
 * 颜色只承担「好 / 中 / 差」的粗信号。
 *
 * ## verdict（第五段）**不纳入**：它是按 task_type 分组的嵌套排名列表
 *
 * 与切片八的 `insights` 同源 —— `<ol>` + `#rank` + 组内 grid，套 CardList 会把
 * 序号与组头一起拍平。门禁断「verdict 段里没有 card-list」。
 *
 * ## 不动桌面的一处像素：verdict 网格的 `minmax(280px, 1fr)`
 *
 * 280px 是**固定下限**。窄屏下容器算下来只有 320−24×2−20×2 = 232px（360px 机
 * 272px）⇒ 网格列比容器宽，横向溢出。改成 `min(280px, 100%)`：
 * 容器 ≥280px 时 `min()` 取 280px，**与原值逐字等价**（桌面零变化）；
 * 容器不足时取 100%，列不再超出容器。
 */

/**
 * 列名单一真源。桌面 `<th>` 与卡片 `fields[].label` 共用同一批字面量 ——
 * 写两遍就等于给「表格改了列名、卡片忘了改」留后门。
 * 这些是本页**原有的英文硬编码**（模板里本来就写着），本切片 0 新增 i18n 键。
 */
const COLS = {
  model: 'Model',
  strategy: 'Strategy',
  taskType: 'Task type',
  samples: 'Samples',
  success: 'Success',
  latency: 'Latency',
  cost: 'Cost',
} as const

/**
 * 空态文案。桌面原本是模板里的三段字面量 + `<p v-else>`；
 * 抽成常量是为了让 `:empty-text` 与桌面那条 `<p>` 引用**同一份**，
 * 避免改一处漏一处。
 */
const EMPTY_BY_MODEL = 'No data — try lowering min_samples or expanding the window.'
const EMPTY_BY_STRATEGY = 'No data — A/B test may not be enabled or traffic too low.'
const EMPTY_BY_TASK_TYPE = 'No data.'
/**
 * 第四段桌面**没有**空态（`<details>` 收起时 summary 就写着 `0 pairs`）。
 * 但卡片形态下 0 行会渲染成一个空 `<ul>` —— 两种形态的「什么都没有」
 * 表现不一致。所以 compact 补一条；桌面行为逐字不变。
 */
const EMPTY_BY_MODEL_TASK = 'No (model, task_type) pairs.'

/** 成功率 → 卡片 tone。阈值与 `successColor` 同源，5 档合并成 3 档（见上方注释）。 */
function successTone(rate: number): 'good' | 'warn' | 'danger' {
  if (rate >= 0.85) return 'good'
  if (rate >= 0.7) return 'warn'
  return 'danger'
}

/** by_model / by_strategy / by_task_type 三张表共用的卡片字段（桌面同为这 4 列）。 */
const rowCardFields = computed<CardField[]>(() => [
  { key: 'samples', label: COLS.samples, type: 'metric', align: 'end', format: (v) => fmtCount(Number(v)) },
  {
    key: 'success_rate',
    label: COLS.success,
    type: 'badge',
    tone: (row) => successTone(Number(row.success_rate)),
    format: (v) => fmtPct(Number(v)),
  },
  { key: 'avg_latency_ms', label: COLS.latency, format: (v) => fmtMs(Number(v)) },
  { key: 'avg_cost_usd', label: COLS.cost, type: 'metric', align: 'end', format: (v) => fmtUsd(Number(v)) },
])

/**
 * by_model_task 的卡片字段。**桌面那张表没有 Cost 列**（`CorrelationRowMT`
 * 带 `avg_cost_usd`，但表格不渲染它）⇒ 卡片也不出。
 * 红线：卡片不得因为换了形态就露出表格里没有的字段。
 */
const modelTaskCardFields = computed<CardField[]>(() => [
  { key: 'samples', label: COLS.samples, type: 'metric', align: 'end', format: (v) => fmtCount(Number(v)) },
  {
    key: 'success_rate',
    label: COLS.success,
    type: 'badge',
    tone: (row) => successTone(Number(row.success_rate)),
    format: (v) => fmtPct(Number(v)),
  },
  { key: 'avg_latency_ms', label: COLS.latency, format: (v) => fmtMs(Number(v)) },
])

// Group verdict by task_type so we can render as nested cards.
const verdictByTask = computed(() => {
  if (!resp.value) return []
  const map = new Map<string, CorrelationVerdict[]>()
  for (const v of resp.value.verdict) {
    if (!map.has(v.task_type)) map.set(v.task_type, [])
    map.get(v.task_type)!.push(v)
  }
  return Array.from(map.entries()).map(([taskType, verdicts]) => ({
    taskType,
    verdicts: verdicts.sort((a, b) => a.rank - b.rank),
  }))
})

// Sort model-task by success desc, then by model+task
const sortedModelTask = computed(() => {
  if (!resp.value) return [] as CorrelationRowMT[]
  return [...resp.value.by_model_task].sort((a, b) => {
    if (a.success_rate !== b.success_rate) return b.success_rate - a.success_rate
    return a.model.localeCompare(b.model) || a.task_type.localeCompare(b.task_type)
  })
})

/** 给第四张表补一个复合主键。逐行新建对象，不改 `resp` 里的原行。 */
const modelTaskRows = computed(() =>
  sortedModelTask.value.map((r) => ({ ...r, card_key: `${r.model} · ${r.task_type}` })),
)

onMounted(load)
</script>

<template>
  <div class="correlations-view">
    <h1>{{ t('correlations.title') }}</h1>
    <p class="subtitle">
      Cross-references the promoted columns on
      <code>request_logs</code> (P7.2) to surface correlations between
      strategy / model / task type. Useful for answering "which model
      should we blacklist on reasoning?" or "is the LLM fallback path
      actually helping?"
    </p>

    <!-- ── Filter bar ────────────────────────────────────── -->
    <section class="card filter-card">
      <div class="filter-bar">
        <label>Window:
          <KxDateRangePicker
            :model-value="rangeValue"
            :presets="dayPresets"
            :max-span-days="90"
            @apply="onRangeApply"
          />
        </label>
        <label>Min samples:
          <select v-model.number="minSamples" @change="load">
            <option :value="5">5</option>
            <option :value="20">20</option>
            <option :value="50">50</option>
            <option :value="100">100</option>
          </select>
        </label>
        <button @click="load" :disabled="loading">
          {{ loading ? 'Loading…' : 'Refresh' }}
        </button>
      </div>
      <p v-if="error" class="error">⚠️ {{ error }}</p>
    </section>

    <template v-if="resp">
      <p class="meta">
        <span>Generated at: {{ resp.generated_at }}</span>
        <span>Window: {{ resp.window_days }} days</span>
        <span>Min samples: {{ minSamples }}</span>
      </p>

      <!-- ── 1. By model ───────────────────────────────────── -->
      <section class="card" data-testid="corr-by-model">
        <h2>{{ t('correlations.sections.byModel') }}</h2>
        <p class="hint">Per-model success / latency / cost across all auto requests.</p>
        <ResponsiveDataView
          :rows="resp.by_model"
          title-key="label"
          :fields="rowCardFields"
          table-min-width="0px"
          :empty="isCompact && resp.by_model.length === 0"
          :empty-text="EMPTY_BY_MODEL"
        >
          <template #table>
          <table v-if="resp.by_model.length > 0" class="corr-table">
            <thead>
              <tr>
                <th>{{ COLS.model }}</th>
                <th>{{ COLS.samples }}</th>
                <th>{{ COLS.success }}</th>
                <th>{{ COLS.latency }}</th>
                <th>{{ COLS.cost }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in resp.by_model" :key="r.label">
                <td><span class="tag tag-model">{{ r.label }}</span></td>
                <td>{{ fmtCount(r.samples) }}</td>
                <td :style="{ color: successColor(r.success_rate), fontWeight: 600 }">
                  {{ fmtPct(r.success_rate) }}
                </td>
                <td>{{ fmtMs(r.avg_latency_ms) }}</td>
                <td>{{ fmtUsd(r.avg_cost_usd) }}</td>
              </tr>
            </tbody>
          </table>
          </template>
        </ResponsiveDataView>
        <p v-if="!isCompact && resp.by_model.length === 0" class="empty">{{ EMPTY_BY_MODEL }}</p>
      </section>

      <!-- ── 2. By strategy ────────────────────────────────── -->
      <section class="card" data-testid="corr-by-strategy">
        <h2>{{ t('correlations.sections.byStrategy') }}</h2>
        <p class="hint">
          Per-strategy success / latency. Useful for confirming the
          pattern_layered strategy actually outperforms
          baseline_heuristic in production traffic.
        </p>
        <ResponsiveDataView
          :rows="resp.by_strategy"
          title-key="label"
          :fields="rowCardFields"
          table-min-width="0px"
          :empty="isCompact && resp.by_strategy.length === 0"
          :empty-text="EMPTY_BY_STRATEGY"
        >
          <template #table>
          <table v-if="resp.by_strategy.length > 0" class="corr-table">
            <thead>
              <tr>
                <th>{{ COLS.strategy }}</th>
                <th>{{ COLS.samples }}</th>
                <th>{{ COLS.success }}</th>
                <th>{{ COLS.latency }}</th>
                <th>{{ COLS.cost }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in resp.by_strategy" :key="r.label">
                <td><span class="tag tag-strategy">{{ r.label }}</span></td>
                <td>{{ fmtCount(r.samples) }}</td>
                <td :style="{ color: successColor(r.success_rate), fontWeight: 600 }">
                  {{ fmtPct(r.success_rate) }}
                </td>
                <td>{{ fmtMs(r.avg_latency_ms) }}</td>
                <td>{{ fmtUsd(r.avg_cost_usd) }}</td>
              </tr>
            </tbody>
          </table>
          </template>
        </ResponsiveDataView>
        <p v-if="!isCompact && resp.by_strategy.length === 0" class="empty">{{ EMPTY_BY_STRATEGY }}</p>
      </section>

      <!-- ── 3. By task type ──────────────────────────────── -->
      <section class="card" data-testid="corr-by-task-type">
        <h2>{{ t('correlations.sections.byTaskType') }}</h2>
        <p class="hint">Per-task_type success / latency / cost.</p>
        <ResponsiveDataView
          :rows="resp.by_task_type"
          title-key="label"
          :fields="rowCardFields"
          table-min-width="0px"
          :empty="isCompact && resp.by_task_type.length === 0"
          :empty-text="EMPTY_BY_TASK_TYPE"
        >
          <template #table>
          <table v-if="resp.by_task_type.length > 0" class="corr-table">
            <thead>
              <tr>
                <th>{{ COLS.taskType }}</th>
                <th>{{ COLS.samples }}</th>
                <th>{{ COLS.success }}</th>
                <th>{{ COLS.latency }}</th>
                <th>{{ COLS.cost }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in resp.by_task_type" :key="r.label">
                <td><span class="tag tag-task">{{ r.label }}</span></td>
                <td>{{ fmtCount(r.samples) }}</td>
                <td :style="{ color: successColor(r.success_rate), fontWeight: 600 }">
                  {{ fmtPct(r.success_rate) }}
                </td>
                <td>{{ fmtMs(r.avg_latency_ms) }}</td>
                <td>{{ fmtUsd(r.avg_cost_usd) }}</td>
              </tr>
            </tbody>
          </table>
          </template>
        </ResponsiveDataView>
        <p v-if="!isCompact && resp.by_task_type.length === 0" class="empty">{{ EMPTY_BY_TASK_TYPE }}</p>
      </section>

      <!-- ── 4. By (model, task) — outlier detector ────────── -->
      <section class="card" data-testid="corr-by-model-task">
        <h2>{{ t('correlations.sections.outlier') }}</h2>
        <p class="hint">
          A model that performs well on chat but poorly on reasoning
          is a candidate to blacklist via routing overrides. Look for
          rows with success &lt; 70% in a sea of &gt; 95% rows.
        </p>
        <details>
          <summary>{{ sortedModelTask.length }} (model, task_type) pairs</summary>
          <ResponsiveDataView
            :rows="modelTaskRows"
            title-key="card_key"
            :fields="modelTaskCardFields"
            table-min-width="0px"
            :empty="isCompact && sortedModelTask.length === 0"
            :empty-text="EMPTY_BY_MODEL_TASK"
          >
            <template #table>
            <table v-if="sortedModelTask.length > 0" class="corr-table compact">
              <thead>
                <tr>
                  <th>{{ COLS.model }}</th>
                  <th>{{ COLS.taskType }}</th>
                  <th>{{ COLS.samples }}</th>
                  <th>{{ COLS.success }}</th>
                  <th>{{ COLS.latency }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in sortedModelTask" :key="`${r.model}-${r.task_type}`">
                  <td><span class="tag tag-model">{{ r.model }}</span></td>
                  <td><span class="tag tag-task">{{ r.task_type }}</span></td>
                  <td>{{ fmtCount(r.samples) }}</td>
                  <td :style="{ color: successColor(r.success_rate), fontWeight: 600 }">
                    {{ fmtPct(r.success_rate) }}
                  </td>
                  <td>{{ fmtMs(r.avg_latency_ms) }}</td>
                </tr>
              </tbody>
            </table>
            </template>
          </ResponsiveDataView>
        </details>
      </section>

      <!-- ── 5. Verdict: top-3 per task type ────────────────── -->
      <section class="card" data-testid="corr-verdict">
        <h2>{{ t('correlations.sections.topModels') }}</h2>
        <p class="hint">
          Ranked by success rate (ties broken by latency). Use this
          when designing routing overrides or weight profiles.
        </p>
        <div v-if="verdictByTask.length > 0" class="verdict-grid">
          <div v-for="group in verdictByTask" :key="group.taskType" class="verdict-card">
            <h3 class="verdict-task-type">{{ group.taskType }}</h3>
            <ol class="verdict-list">
              <li v-for="v in group.verdicts" :key="`${v.task_type}-${v.rank}`"
                  :class="['verdict-item', `rank-${v.rank}`]">
                <span class="verdict-rank">#{{ v.rank }}</span>
                <span class="verdict-model">{{ v.model }}</span>
                <span :style="{ color: successColor(v.success_rate), fontWeight: 600 }">
                  {{ fmtPct(v.success_rate) }}
                </span>
                <span class="verdict-latency">{{ fmtMs(v.avg_latency_ms) }}</span>
              </li>
            </ol>
          </div>
        </div>
        <p v-else class="empty">No data — need at least {{ minSamples }} samples per (task, model) pair.</p>
      </section>
    </template>
  </div>
</template>

<style scoped>
.correlations-view {
  padding: 24px;
  color: var(--text);
}
h1 {
  margin: 0 0 8px;
  font-size: 24px;
}
.subtitle {
  margin: 0 0 24px;
  color: var(--muted);
  font-size: 14px;
}
.card {
  background: var(--card-bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 20px;
  margin-bottom: 24px;
}
.card h2 {
  margin: 0 0 8px;
  font-size: 18px;
  border-bottom: 1px solid var(--border);
  padding-bottom: 8px;
}
.filter-card { padding: 16px 20px; }
.filter-bar {
  display: flex;
  gap: 16px;
  align-items: center;
  flex-wrap: wrap;
}
.filter-bar label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--muted);
}
.filter-bar select,
.filter-bar input {
  /* width:auto 覆盖全局 input/select width:100%，避免筛选控件占满整行 */
  width: auto;
  padding: 4px 8px;
  background: var(--bg);
  border: 1px solid var(--bg);
  color: inherit;
  border-radius: 4px;
  font-size: 13px;
}
.filter-bar button {
  padding: 6px 14px;
  background: var(--accent);
  color: var(--on-primary);
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 13px;
}
.filter-bar button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.error {
  color: var(--danger);
  font-size: 13px;
  margin-top: 8px;
}
.meta {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--muted);
  margin: 0 0 16px;
}
.hint {
  color: var(--muted);
  font-size: 13px;
  margin: 0 0 12px;
}
.empty {
  color: var(--muted);
  font-size: 13px;
  font-style: italic;
}
.corr-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.corr-table.compact {
  margin-top: 8px;
}
.corr-table th {
  text-align: left;
  padding: 8px 10px;
  background: var(--bg);
  border-bottom: 1px solid var(--bg);
  color: var(--muted);
  font-weight: 500;
}
.corr-table td {
  padding: 8px 10px;
  border-bottom: 1px solid var(--bg);
}
details {
  margin-top: 8px;
}
details summary {
  cursor: pointer;
  color: var(--accent-h);
  font-size: 13px;
  padding: 4px 0;
}
details summary:hover {
  color: var(--info-bd);
}
.tag {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 11px;
  padding: 2px 6px;
  border-radius: 3px;
  display: inline-block;
}
.tag-model {
  background: var(--kx-text);
  color: var(--accent-h);
}
.tag-strategy {
  background: var(--warning-dark);
  color: var(--warning);
}
.tag-task {
  background: var(--success-strong);
  color: var(--success);
}
/*
 * 窄屏不下溢：280px 是固定下限，而容器 = 视口 − 页面 24×2 − 卡片 20×2
 * （320px 机只有 232px，360px 机 272px）⇒ 列宽会超出容器。
 * `min(280px, 100%)`：容器 ≥280px 时取 280px，与原值逐字等价（桌面零变化）；
 * 不足时取 100%，列不再宽于容器。
 */
.verdict-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(280px, 100%), 1fr));
  gap: 16px;
}
.verdict-card {
  background: var(--bg);
  border: 1px solid var(--bg);
  border-radius: 6px;
  padding: 12px 16px;
}
.verdict-task-type {
  margin: 0 0 8px;
  font-size: 13px;
  color: var(--success);
  font-family: 'SF Mono', Menlo, monospace;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.verdict-list {
  list-style: none;
  padding: 0;
  margin: 0;
}
.verdict-item {
  display: grid;
  grid-template-columns: 28px 1fr auto auto;
  gap: 8px;
  align-items: center;
  padding: 6px 0;
  border-bottom: 1px solid var(--bg);
  font-size: 13px;
}
.verdict-item:last-child {
  border-bottom: none;
}
.verdict-item.rank-1 {
  background: linear-gradient(90deg, rgba(34,197,94,0.08), transparent);
  padding-left: 8px;
  margin-left: -8px;
  border-radius: 4px;
}
.verdict-rank {
  font-weight: 700;
  color: var(--accent-h);
  font-family: 'SF Mono', Menlo, monospace;
}
.verdict-model {
  color: var(--border);
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 12px;
}
.verdict-latency {
  color: var(--muted);
  font-size: 12px;
}
</style>
