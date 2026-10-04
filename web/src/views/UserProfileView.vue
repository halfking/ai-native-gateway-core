<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { getUserProfile, type UserProfileDetail } from '../api/admin'
import * as echarts from 'echarts'
import type { EChartsOption } from 'echarts'


// 2026-09-13 P5：补齐模板使用的 el-* 组件注册（修复运行时 resolve 失败）
import { ElButton, ElCard, ElCol, ElRow, ElTable, ElTableColumn, ElTag } from 'element-plus'
import KxDateRangePicker from '../components/ui/KxDateRangePicker.vue'
import { useSpanDaysRange } from '../composables/useSpanDaysRange'
import { useWindowClass } from '../composables/useWindowClass'
import ResponsiveDataView from '../components/ui/ResponsiveDataView.vue'
import type { CardField } from '../components/ui/CardList.vue'
const { t } = useI18n()
const { isCompact } = useWindowClass()
const route = useRoute()
const router = useRouter()

const owner = computed(() => route.params.owner as string)
const loading = ref(false)
const data = ref<UserProfileDetail | null>(null)
const days = ref(30)
const { presets: dayPresets, rangeValue, applyRange } = useSpanDaysRange(days, [
  { days: 7, labelKey: 'dashboard.range.last7d' },
  { days: 30, labelKey: 'dashboard.range.last30d' },
  { days: 90, labelKey: 'dashboard.range.last90d' },
])

const costChartRef = ref<HTMLElement>()
let costChart: echarts.ECharts | null = null

onMounted(() => void load())

watch(days, () => void load())

onUnmounted(() => {
  costChart?.dispose()
})

async function load() {
  loading.value = true
  try {
    data.value = await getUserProfile(owner.value, days.value)
    await nextTick()
    renderCostChart()
  } catch {
    data.value = null
  } finally {
    loading.value = false
  }
}

function renderCostChart() {
  if (!costChartRef.value || !data.value?.daily_cost_trend?.length) return
  if (!costChart) {
    costChart = echarts.init(costChartRef.value)
  }
  const trend = data.value.daily_cost_trend
  const dates = trend.map(d => d.date)
  const costs = trend.map(d => d.cost)
  const sessions = trend.map(d => d.sessions)

  const option: EChartsOption = {
    tooltip: { trigger: 'axis' },
    legend: { data: ['Cost', 'Sessions'] },
    xAxis: { type: 'category', data: dates },
    yAxis: [
      { type: 'value', name: 'Cost ($)' },
      { type: 'value', name: 'Sessions' },
    ],
    series: [
      {
        name: 'Cost',
        type: 'line',
        data: costs,
        smooth: true,
        yAxisIndex: 0,
        itemStyle: { color: '#409EFF' },
      },
      {
        name: 'Sessions',
        type: 'bar',
        data: sessions,
        yAxisIndex: 1,
        itemStyle: { color: '#67C23A' },
      },
    ],
  }
  costChart.setOption(option, true)
}

function healthGradeColor(grade?: string): 'success' | 'primary' | 'warning' | 'info' | 'danger' | undefined {
  if (!grade) return undefined
  switch (grade) {
    case 'A': return 'success'
    case 'B': return 'primary'
    case 'C': return 'warning'
    case 'D': return 'info'
    case 'F': return 'danger'
    default: return undefined
  }
}

/**
 * ── H6 第十五条切片（2026-10-06）：3 张 el-table 接 compact 卡片形态 ─────────────
 *
 * ## 首条 `el-table` 切片：**不传 `table-min-width`**
 *
 * 容器规则是 `.responsive-data-view__table > :slotted(table)`，而 `el-table` 的根元素
 * 是 `<div class="el-table">`（**已实测**，见 10 §4.6 的 D12）⇒ 传了也不生效。
 * el-table 自带 `el-table__body-wrapper` 内部横向滚动，窄屏下限归 Element 自己管。
 * ⇒ 这里**不传**那个 prop，而不是传一个不生效的值再在注释里说「已设 0px」（那是假的）。
 * 门禁断「本页不出现 `table-min-width`」。
 *
 * ## 无分页 API ⇒ 只改呈现形态
 *
 * `getUserProfile(owner, days)` 一次取回整段，三张表都是它的字段
 * ⇒ 不引入连续加载。
 *
 * ## 不传 `:loading`
 *
 * 桌面刷新时 `data` 保留旧值、三张 el-table 照常渲染旧行（`el-table` 自己没有 loading 态）。
 * 若给容器加 `:loading`，compact 下会变成「表格位置出骨架」，与桌面错位。
 * 页面级的延迟反馈已由 `v-loading` 与顶部 `.empty` 承担。
 *
 * ## 三态：compact 的空态只能来自容器，桌面留给 Element
 *
 * 三张 el-table 桌面**都没有自己的空态行** —— 0 行时 Element 渲染它自带的 "No Data"。
 * 卡片形态下 0 行会是一个空 `<ul>` ⇒ 必须给容器 `:empty`，且**带 `isCompact` 前置**
 * （否则桌面会把 Element 的空态换成我们的 `EmptyState`，那是桌面变更）。
 * 文案复用既有的 `hyper.list.empty` ⇒ **0 新增 i18n 键**。
 *
 * ## `health_grade`：5 档 Element 类型 → 3 档 tone（与桌面同一组判读线）
 *
 * 桌面 `healthGradeColor` 出 5 个 `el-tag` 类型（success/primary/warning/info/danger），
 * `CardField.tone` 只有 4 个取值 ⇒ A/B→good、C/D→warn、F→danger、其余 undefined。
 * **精确等级不丢**：等级字母本身就在卡片上。
 *
 * ## 两处**已知且有界**的口径差异（不是漏）
 *
 * 1. `avg_health` 缺值时桌面 el-table 渲染**空白**，卡片出 `—`。
 *    `CardList` 没有「空白」这个状态（`null`/`undefined`/`''` 一律出 `—`），
 *    而 `—` 就是本仓统一的「无值」记号。**卡片不能渲染空白**，改桌面又越过红线。
 * 3. **最近会话的卡头出完整 `session_id`，不跟桌面一起截断到 16 字符。**
 *    桌面的 `slice(0, 16) + '...'` 是为**表格里的窄列**做的；卡片头承担的是
 *    「唯一句柄」职责（与切片十的 `#覆盖ID` 同理）⇒ 截断会让**同前缀的两行
 *    变成两张一模一样的卡**（fixture 里 `sess-…0001-short` 与 `sess-…0002-mid`
 *    的前 16 字符完全相同）。**这不是不一致，是两个槽位干两件事。**
 * 2. `last_activity` / `created_at` 两列桌面就是**裸 ISO 串**（el-table-column 直接
 *    `prop` 渲染，未经任何格式化）⇒ 卡片照抄同一个口径（见 10 §4.6 的 D13）。
 *    **不趁这次切片修桌面**。
 */

/** 成本：美元 4 位小数。桌面三列都是 `$(x ?? 0).toFixed(4)`，缺值按 0。 */
function fmtUsd4(v: unknown): string {
  return `$${Number(v ?? 0).toFixed(4)}`
}

/** 健康等级 → 卡片 tone。与 `healthGradeColor` 同一组判读线，5 档收敛成 3 档。 */
function healthGradeTone(grade: unknown): 'good' | 'warn' | 'danger' | undefined {
  const g = grade == null ? '' : String(grade)
  if (g === 'A' || g === 'B') return 'good'
  if (g === 'C' || g === 'D') return 'warn'
  if (g === 'F') return 'danger'
  return undefined
}

/** 健康分：缺值出 `—`（桌面是空白，见上方说明）。 */
function fmtHealth(v: unknown): string | undefined {
  return v == null ? undefined : String(v)
}

/** 热门任务表卡片字段。4 列里除 task_id 外的那 3 个。 */
const topTaskFields = computed<CardField[]>(() => [
  { key: 'session_count', label: t('sessions.userProfile.sessionCount'), type: 'metric', align: 'end' },
  { key: 'total_cost', label: t('sessions.userProfile.totalCost'), type: 'metric', align: 'end', format: (v) => fmtUsd4(v) },
  { key: 'avg_health', label: t('sessions.userProfile.avgHealth'), align: 'end', format: (v) => fmtHealth(v) },
])

/**
 * 热门终端用户表卡片字段。桌面那张表**只有 4 列**（没有 `avg_health` 列，
 * 尽管 `top_end_users` 的行对象带这个字段）⇒ 卡片也不出。
 * 红线：卡片不得因为换了形态就露出表格里没有的字段。
 */
const topEndUserFields = computed<CardField[]>(() => [
  { key: 'session_count', label: t('sessions.userProfile.sessionCount'), type: 'metric', align: 'end' },
  { key: 'total_cost_usd', label: t('sessions.userProfile.totalCost'), type: 'metric', align: 'end', format: (v) => fmtUsd4(v) },
  { key: 'last_activity', label: t('sessions.userProfile.lastSeenAt') },
])

/** 最近会话表卡片字段。5 列里除 session_id 外的那 4 个。 */
const recentSessionFields = computed<CardField[]>(() => [
  { key: 'request_count', label: t('sessions.userProfile.requestCount'), type: 'metric', align: 'end' },
  { key: 'cost_usd', label: t('sessions.userProfile.totalCost'), type: 'metric', align: 'end', format: (v) => fmtUsd4(v) },
  {
    key: 'health_grade',
    label: t('sessions.userProfile.avgHealthGrade'),
    type: 'badge',
    tone: (row) => healthGradeTone(row.health_grade),
  },
  { key: 'created_at', label: t('sessions.userProfile.createdAt') },
])
</script>

<template>
  <div class="user-profile-detail">
    <div class="page-header">
      <el-button text @click="router.back()">← {{ t('common.back') }}</el-button>
      <h2>{{ t('sessions.userProfile.detailTitle') }}: {{ owner }}</h2>
      <KxDateRangePicker
        :model-value="rangeValue"
        :presets="dayPresets"
        :max-span-days="90"
        @apply="applyRange"
      />
    </div>

    <div v-loading="loading">
      <div v-if="!loading && !data" class="empty">{{ t('sessions.userProfile.empty') }}</div>

      <!-- Stat cards -->
      <el-row v-if="data" :gutter="16" class="stat-row">
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-card">
              <div class="stat-label">{{ t('sessions.userProfile.sessionCount') }}</div>
              <div class="stat-value">{{ data?.session_count ?? '-' }}</div>
            </div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-card">
              <div class="stat-label">{{ t('sessions.userProfile.totalCost') }}</div>
              <div class="stat-value">${{ (data?.total_cost_usd ?? 0).toFixed(4) }}</div>
            </div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-card">
              <div class="stat-label">{{ t('sessions.userProfile.avgHealth') }}</div>
              <div class="stat-value">{{ data?.avg_health_score ?? '-' }}</div>
            </div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-card">
              <div class="stat-label">{{ t('sessions.userProfile.successRate') }}</div>
              <div class="stat-value">
                {{
                  data && (data.total_success + data.total_errors) > 0
                    ? ((data.total_success / (data.total_success + data.total_errors)) * 100).toFixed(1) + '%'
                    : '-'
                }}
              </div>
            </div>
          </el-card>
        </el-col>
      </el-row>

      <!-- Cost trend chart -->
      <el-card class="chart-card">
        <template #header>{{ t('sessions.userProfile.costTrend') }}</template>
        <div class="chart-container" style="height: 300px" />
      </el-card>

      <!-- Top tasks -->
      <el-card class="section-card" data-testid="up-top-tasks">
        <template #header>{{ t('sessions.userProfile.topTasks') }}</template>
        <ResponsiveDataView
          :rows="data?.top_tasks ?? []"
          title-key="task_id"
          :fields="topTaskFields"
          :empty="isCompact && (data?.top_tasks.length ?? 0) === 0"
          :empty-text="t('hyper.list.empty')"
        >
          <template #table>
        <el-table :data="data?.top_tasks ?? []" stripe size="small">
          <el-table-column prop="task_id" :label="t('sessions.userProfile.taskId')" min-width="200" />
          <el-table-column prop="session_count" :label="t('sessions.userProfile.sessionCount')" width="90" align="right" />
          <el-table-column prop="total_cost" :label="t('sessions.userProfile.totalCost')" width="120" align="right">
            <template #default="scope">${{ (scope?.row?.total_cost ?? 0).toFixed(4) }}</template>
          </el-table-column>
          <el-table-column prop="avg_health" :label="t('sessions.userProfile.avgHealth')" width="80" align="right" />
        </el-table>
          </template>
        </ResponsiveDataView>
      </el-card>

      <!-- Top end users -->
      <el-card class="section-card" data-testid="up-top-end-users">
        <template #header>{{ t('sessions.userProfile.topEndUsers') }}</template>
        <ResponsiveDataView
          :rows="data?.top_end_users ?? []"
          title-key="end_user_id"
          :fields="topEndUserFields"
          :empty="isCompact && (data?.top_end_users.length ?? 0) === 0"
          :empty-text="t('hyper.list.empty')"
        >
          <template #table>
        <el-table :data="data?.top_end_users ?? []" stripe size="small">
          <el-table-column prop="end_user_id" :label="t('sessions.userProfile.endUserId')" min-width="200" />
          <el-table-column prop="session_count" :label="t('sessions.userProfile.sessionCount')" width="90" align="right" />
          <el-table-column prop="total_cost_usd" :label="t('sessions.userProfile.totalCost')" width="120" align="right">
            <template #default="scope">${{ (scope?.row?.total_cost_usd ?? 0).toFixed(4) }}</template>
          </el-table-column>
          <el-table-column prop="last_activity" :label="t('sessions.userProfile.lastSeenAt')" width="170" />
        </el-table>
          </template>
        </ResponsiveDataView>
      </el-card>

      <!-- Recent sessions -->
      <el-card class="section-card" data-testid="up-recent-sessions">
        <template #header>{{ t('sessions.userProfile.recentSessions') }}</template>
        <ResponsiveDataView
          :rows="data?.recent_sessions ?? []"
          title-key="session_id"
          :fields="recentSessionFields"
          :empty="isCompact && (data?.recent_sessions.length ?? 0) === 0"
          :empty-text="t('hyper.list.empty')"
        >
          <template #table>
        <el-table :data="data?.recent_sessions ?? []" stripe size="small">
          <el-table-column prop="session_id" :label="t('sessions.userProfile.sessionId')" min-width="200">
            <template #default="scope">
              <a :href="`/plugins/ai-session-manager/sessions/${encodeURIComponent(scope?.row?.session_id)}`" class="session-link" target="_blank" rel="noopener">
                {{ scope?.row?.session_id?.slice(0, 16) }}...
              </a>
            </template>
          </el-table-column>
          <el-table-column prop="request_count" :label="t('sessions.userProfile.requestCount')" width="80" align="right" />
          <el-table-column prop="cost_usd" :label="t('sessions.userProfile.totalCost')" width="100" align="right">
            <template #default="scope">${{ (scope?.row?.cost_usd ?? 0).toFixed(4) }}</template>
          </el-table-column>
          <el-table-column prop="health_grade" :label="t('sessions.userProfile.avgHealthGrade')" width="80" align="center">
            <template #default="scope">
              <el-tag v-if="scope?.row?.health_grade" :type="healthGradeColor(scope?.row?.health_grade) || undefined" size="small">
                {{ scope?.row?.health_grade }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="created_at" :label="t('sessions.userProfile.createdAt')" width="170" />
        </el-table>
          </template>
          <template #actions="{ row }">
            <a
              class="session-link"
              :href="`/plugins/ai-session-manager/sessions/${encodeURIComponent(String(row.session_id))}`"
              target="_blank"
              rel="noopener"
            >
              {{ t('sessions.userProfile.sessionId') }}
            </a>
          </template>
        </ResponsiveDataView>
      </el-card>
    </div>
  </div>
</template>

<style scoped>
.user-profile-detail { padding: 20px; }
.page-header { display: flex; align-items: center; gap: 12px; margin-bottom: 16px; }
.stat-row { margin-bottom: 16px; }
.stat-card { text-align: center; }
.stat-label { font-size: 13px; color: var(--muted); margin-bottom: 4px; }
.stat-value { font-size: 24px; font-weight: 700; }
.chart-card { margin-bottom: 16px; }
.section-card { margin-bottom: 16px; }
.chart-container { width: 100%; }
.session-link { color: var(--el-color-primary); text-decoration: none; }
.session-link:hover { text-decoration: underline; }
</style>
