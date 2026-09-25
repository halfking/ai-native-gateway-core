<script setup lang="ts">
// ReconciliationReport.vue — 对账报表（2026-09-25 落地轮）
// 供应商对帐（全流量/成本口径）与内部对帐（业务流量/积分+内部价口径）
// 双视角；区间汇总 + 按供应商/租户/人员/模型/天 分组 + Excel 双 sheet 导出。
// 数据来自每日凌晨自动聚合的 report_snapshots，区间查询不回扫原始日志。
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { Download, Refresh } from '@element-plus/icons-vue'
import { useRoute } from 'vue-router'
import {
  downloadReportExport,
  getReportSummary,
  runReportRollup,
  type RangeReport,
  type ReportView,
} from '../../api/reportrollup'

const { t } = useI18n()
const route = useRoute()

const loading = ref(false)
const exporting = ref(false)
const rerunning = ref(false)
// 2026-09-26 审计轮：支持 ?view=internal 深链（「租户用户→结算报表」菜单
// 入口直开内部视角）。非法值回落 provider，与后端 reportViewFilter 同口径。
const initialView = route.query.view === 'internal' ? 'internal' : 'provider'
const view = ref<ReportView>(initialView)

// 默认区间：昨日往前 7 天（今日快照 T+1 凌晨才生成）。
function fmtDay(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}
function defaultRange(): [string, string] {
  const end = new Date(Date.now() - 86400000)
  const start = new Date(Date.now() - 7 * 86400000)
  return [fmtDay(start), fmtDay(end)]
}
const range = ref<[string, string]>(defaultRange())

const report = ref<RangeReport | null>(null)
const errorText = ref('')

const hasData = computed(() => !!report.value && report.value.snapshot_dates.length > 0)

async function refresh() {
  if (!range.value || range.value.length !== 2) return
  loading.value = true
  errorText.value = ''
  try {
    report.value = await getReportSummary({
      start: range.value[0],
      end: range.value[1],
      view: view.value,
    })
  } catch (e: any) {
    errorText.value = e?.message ?? String(e)
    report.value = null
  } finally {
    loading.value = false
  }
}

async function exportXlsx() {
  if (!range.value || range.value.length !== 2) return
  exporting.value = true
  try {
    await downloadReportExport({
      start: range.value[0],
      end: range.value[1],
      view: view.value,
    })
  } catch (e: any) {
    ElMessage.error(`${t('common.exportFailed', '导出失败')}: ${e?.message ?? e}`)
  } finally {
    exporting.value = false
  }
}

async function rerunYesterday() {
  rerunning.value = true
  try {
    const res = await runReportRollup(range.value?.[1] ?? '')
    ElMessage.success(`${t('reports.rerunDone', '重跑完成')}: ${res.date} rows=${res.rows_written}`)
    await refresh()
  } catch (e: any) {
    ElMessage.error(`${t('reports.rerunFailed', '重跑失败')}: ${e?.message ?? e}`)
  } finally {
    rerunning.value = false
  }
}

function fmtInt(n: number | undefined | null): string {
  return (n ?? 0).toLocaleString('en-US')
}
function fmtMoneyFromCents(cents: number | undefined | null): string {
  return ((cents ?? 0) / 100).toFixed(2)
}
function fmtPct(v: number | undefined | null): string {
  return `${((v ?? 0) * 100).toFixed(2)}%`
}
function fmtRatio(v: number | null | undefined): string {
  return v == null ? '-' : `${(v * 100).toFixed(1)}%`
}
function fmtBreakdown(br: Record<string, number> | undefined): string {
  if (!br) return '-'
  const entries = Object.entries(br).filter(([, v]) => v > 0)
  if (!entries.length) return '-'
  return entries.sort((a, b) => b[1] - a[1]).map(([k, v]) => `${k}:${v}`).join('  ')
}
const coverageText = computed(() => {
  if (!report.value) return ''
  const days = report.value.snapshot_dates.length
  return `${days} ${t('reports.daysCovered', '天快照')}`
})

onMounted(refresh)
</script>

<template>
  <div class="report-page">
    <div class="toolbar">
      <el-radio-group v-model="view" @change="refresh">
        <el-radio-button value="provider">{{ t('reports.providerView', '供应商对帐') }}</el-radio-button>
        <el-radio-button value="internal">{{ t('reports.internalView', '内部对帐') }}</el-radio-button>
      </el-radio-group>
      <el-date-picker
        v-model="range"
        type="daterange"
        value-format="YYYY-MM-DD"
        :clearable="false"
        :start-placeholder="t('common.startDate', '开始日期')"
        :end-placeholder="t('common.endDate', '结束日期')"
        style="width: 260px"
      />
      <el-button type="primary" :icon="Refresh" :loading="loading" @click="refresh">
        {{ t('common.refresh', '刷新') }}
      </el-button>
      <el-button :icon="Download" :loading="exporting" @click="exportXlsx">
        {{ t('reports.exportExcel', '导出 Excel') }}
      </el-button>
      <el-button :loading="rerunning" @click="rerunYesterday">
        {{ t('reports.rerun', '重跑结束日') }}
      </el-button>
      <span v-if="coverageText" class="coverage">{{ coverageText }}</span>
    </div>

    <el-alert v-if="errorText" :title="errorText" type="error" show-icon :closable="false" class="block" />
    <el-alert
      v-else-if="report && !hasData"
      :title="t('reports.noSnapshots', '该区间没有报表快照（每日聚合任务在凌晨生成前一日数据，或用「重跑结束日」补算）')"
      type="info"
      show-icon
      :closable="false"
      class="block"
    />

    <template v-if="report">
      <!-- 总计卡片 -->
      <div class="cards">
        <div class="card">
          <div class="card-label">{{ t('reports.requests', '请求数') }}</div>
          <div class="card-value">{{ fmtInt(report.totals.request_count) }}</div>
          <div class="card-sub">{{ fmtInt(report.totals.success_count) }} ✓ / {{ fmtInt(report.totals.error_count) }} ✗（{{ fmtPct(report.totals.error_rate) }}）</div>
        </div>
        <div class="card">
          <div class="card-label">{{ t('reports.totalTokens', '总 tokens') }}</div>
          <div class="card-value">{{ fmtInt(report.totals.total_tokens) }}</div>
          <div class="card-sub">
            {{ t('reports.in', '入') }} {{ fmtInt(report.totals.input_tokens) }} ·
            {{ t('reports.out', '出') }} {{ fmtInt(report.totals.output_tokens) }} ·
            {{ t('reports.cacheRead', '缓存读') }} {{ fmtInt(report.totals.cache_read_tokens) }} ·
            {{ t('reports.cacheWrite', '缓存写') }} {{ fmtInt(report.totals.cache_write_tokens) }}
          </div>
        </div>
        <div class="card" v-if="view === 'provider'">
          <div class="card-label">{{ t('reports.providerCost', '供应商成本') }}</div>
          <div class="card-value">{{ fmtMoneyFromCents(report.totals.estimated_cost_cents) }}</div>
          <div class="card-sub">{{ report.totals.currency || 'USD' }} · {{ t('reports.cacheHit', '缓存命中') }} {{ fmtRatio(report.totals.cache_hit_ratio) }}</div>
        </div>
        <div class="card" v-else>
          <div class="card-label">{{ t('reports.internalCredits', '内部积分') }}</div>
          <div class="card-value">{{ fmtInt(report.totals.credits_charged) }}</div>
          <div class="card-sub">
            {{ t('reports.internalCost', '内部金额') }} {{ fmtMoneyFromCents(report.totals.internal_cost_cents) }} {{ report.totals.internal_currency || 'CNY' }}
          </div>
        </div>
      </div>

      <!-- 分组表：供应商 或 租户 -->
      <h3 class="section">{{ view === 'provider' ? t('reports.byProvider', '按供应商') : t('reports.byTenant', '按租户') }}</h3>
      <el-table :data="view === 'provider' ? report.providers : report.tenants" size="small" border stripe>
        <el-table-column v-if="view === 'provider'" prop="provider_name" :label="t('reports.provider', '供应商')" min-width="140">
          <template #default="{ row }">{{ row.provider_name || row.provider_id }}</template>
        </el-table-column>
        <el-table-column v-else prop="tenant_id" :label="t('reports.tenant', '租户')" min-width="120" />
        <el-table-column :label="t('reports.requests', '请求数')" width="110" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.request_count) }}</template>
        </el-table-column>
        <el-table-column :label="t('reports.success', '成功')" width="100" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.success_count) }}</template>
        </el-table-column>
        <el-table-column :label="t('reports.errors', '失败')" width="100" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.error_count) }}（{{ fmtPct(row.totals.error_rate) }}）</template>
        </el-table-column>
        <el-table-column v-if="view === 'provider'" :label="t('reports.qualityScore', '质量评分')" width="100" align="right">
          <template #default="{ row }">{{ row.quality_score != null ? row.quality_score.toFixed(1) : '-' }}</template>
        </el-table-column>
        <el-table-column :label="t('reports.totalTokens', '总 tokens')" width="130" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.total_tokens) }}</template>
        </el-table-column>
        <el-table-column v-if="view === 'provider'" :label="t('reports.cost', '成本')" width="120" align="right">
          <template #default="{ row }">{{ fmtMoneyFromCents(row.totals.estimated_cost_cents) }} {{ row.totals.currency }}</template>
        </el-table-column>
        <el-table-column v-else :label="t('reports.internalCredits', '内部积分')" width="120" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.credits_charged) }}</template>
        </el-table-column>
        <el-table-column :label="t('reports.errorBreakdown', '失败原因分布')" min-width="260">
          <template #default="{ row }">{{ fmtBreakdown(row.error_breakdown) }}</template>
        </el-table-column>
      </el-table>

      <!-- 人员（内部视角） -->
      <template v-if="view === 'internal'">
        <h3 class="section">{{ t('reports.byPerson', '按人员') }}</h3>
        <el-table :data="report.persons" size="small" border stripe>
          <el-table-column prop="tenant_id" :label="t('reports.tenant', '租户')" width="120" />
          <el-table-column prop="person" :label="t('reports.person', '人员')" min-width="140" />
          <el-table-column :label="t('reports.requests', '请求数')" width="110" align="right">
            <template #default="{ row }">{{ fmtInt(row.totals.request_count) }}</template>
          </el-table-column>
          <el-table-column :label="t('reports.errors', '失败')" width="100" align="right">
            <template #default="{ row }">{{ fmtInt(row.totals.error_count) }}</template>
          </el-table-column>
          <el-table-column :label="t('reports.internalCredits', '内部积分')" width="120" align="right">
            <template #default="{ row }">{{ fmtInt(row.totals.credits_charged) }}</template>
          </el-table-column>
          <el-table-column :label="t('reports.internalCost', '内部金额')" width="130" align="right">
            <template #default="{ row }">{{ fmtMoneyFromCents(row.totals.internal_cost_cents) }} {{ row.totals.internal_currency }}</template>
          </el-table-column>
        </el-table>
      </template>

      <!-- 按模型 -->
      <h3 class="section">{{ t('reports.byModel', '按模型') }}</h3>
      <el-table :data="report.models" size="small" border stripe>
        <el-table-column v-if="view === 'provider'" :label="t('reports.provider', '供应商')" width="140">
          <template #default="{ row }">{{ row.provider_name || row.provider_id || '-' }}</template>
        </el-table-column>
        <el-table-column prop="raw_model_name" :label="t('reports.model', '模型')" min-width="200" />
        <el-table-column :label="t('reports.requests', '请求数')" width="110" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.request_count) }}</template>
        </el-table-column>
        <el-table-column :label="t('reports.errors', '失败')" width="100" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.error_count) }}（{{ fmtPct(row.totals.error_rate) }}）</template>
        </el-table-column>
        <el-table-column label="P50 (ms)" width="100" align="right">
          <template #default="{ row }">{{ row.totals.latency_p50_ms?.toFixed(0) }}</template>
        </el-table-column>
        <el-table-column label="P95 (ms)" width="100" align="right">
          <template #default="{ row }">{{ row.totals.latency_p95_ms?.toFixed(0) }}</template>
        </el-table-column>
        <el-table-column :label="t('reports.cacheHit', '缓存命中')" width="100" align="right">
          <template #default="{ row }">{{ fmtRatio(row.totals.cache_hit_ratio) }}</template>
        </el-table-column>
        <el-table-column v-if="view === 'provider'" :label="t('reports.cost', '成本')" width="120" align="right">
          <template #default="{ row }">{{ fmtMoneyFromCents(row.totals.estimated_cost_cents) }}</template>
        </el-table-column>
        <el-table-column v-else :label="t('reports.internalCredits', '内部积分')" width="120" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.credits_charged) }}</template>
        </el-table-column>
        <el-table-column :label="t('reports.errorBreakdown', '失败原因分布')" min-width="240">
          <template #default="{ row }">{{ fmtBreakdown(row.error_breakdown) }}</template>
        </el-table-column>
      </el-table>

      <!-- 按天 -->
      <h3 class="section">{{ t('reports.byDay', '按天') }}</h3>
      <el-table :data="report.days" size="small" border stripe>
        <el-table-column prop="date" :label="t('reports.date', '日期')" width="130" />
        <el-table-column :label="t('reports.requests', '请求数')" width="110" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.request_count) }}</template>
        </el-table-column>
        <el-table-column :label="t('reports.success', '成功')" width="100" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.success_count) }}</template>
        </el-table-column>
        <el-table-column :label="t('reports.errors', '失败')" width="100" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.error_count) }}（{{ fmtPct(row.totals.error_rate) }}）</template>
        </el-table-column>
        <el-table-column :label="t('reports.totalTokens', '总 tokens')" width="130" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.total_tokens) }}</template>
        </el-table-column>
        <el-table-column v-if="view === 'provider'" :label="t('reports.cost', '成本')" width="130" align="right">
          <template #default="{ row }">{{ fmtMoneyFromCents(row.totals.estimated_cost_cents) }} {{ row.totals.currency }}</template>
        </el-table-column>
        <el-table-column v-else :label="t('reports.internalCredits', '内部积分')" width="130" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.credits_charged) }}</template>
        </el-table-column>
      </el-table>
    </template>
  </div>
</template>

<style scoped>
.report-page {
  padding: 16px;
}
.toolbar {
  display: flex;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
  margin-bottom: 16px;
}
.coverage {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.block {
  margin-bottom: 16px;
}
.cards {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
  margin-bottom: 16px;
}
.card {
  flex: 1;
  min-width: 220px;
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  padding: 12px 16px;
  background: var(--el-bg-color);
}
.card-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.card-value {
  font-size: 24px;
  font-weight: 600;
  margin: 4px 0;
}
.card-sub {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.section {
  margin: 20px 0 8px;
  font-size: 15px;
}
</style>
