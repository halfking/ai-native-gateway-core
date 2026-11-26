<script setup lang="ts">
import { computed, reactive, ref, onMounted, watch } from 'vue'
import { surfaceError } from '../i18n/errorSurface'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  getUserProfileList,
  getUserUsageMatrix,
  type UserProfileSummary,
  type UserUsageMeta,
  type UserUsageMatrixRow,
} from '../api/admin'
import { sortByName } from '../utils/sortByName'
import KxDateRangePicker from '../components/ui/KxDateRangePicker.vue'
import { makeDateRangePresets } from '../components/ui/kxDatePresets'
import type { KxDateRange } from '../components/ui/kx-date-types'
import { parseLocalMinute } from '../utils/datetime'
import { rangeFromQuery } from './admin/session-analytics/saQueryParams'
import { QuestionFilled } from '@element-plus/icons-vue'
import {
  ElButton,
  ElCard,
  ElDrawer,
  ElInput,
  ElPagination,
  ElTable,
  ElTableColumn,
} from 'element-plus'
import {
  emptyDegradation,
  isDegraded,
  mergeDegradation,
  type DegradationPayload,
  type DegradationState,
} from '../composables/useDegradationMarker'


// 2026-09-13 P5：补齐模板使用的 el-* 组件注册（修复运行时 resolve 失败）
const { t } = useI18n()
const router = useRouter()
const route = useRoute()

const loading = ref(false)
const users = ref<UserProfileSummary[]>([])
const total = ref(0)
const search = ref('')
const currentPage = ref(1)
const pageSize = 20

// 列表和展开明细共用接口返回的请求统计窗口。
const kxPresets = makeDateRangePresets('datetime')
const timeRange = ref<KxDateRange | null>(null)
const linkedRange = computed(() => rangeFromQuery(route.query))
let listGeneration = 0
let matrixGeneration = 0

function onRangeApply(range: KxDateRange) {
  timeRange.value = range
  reloadRange()
}

function reloadRange() {
  currentPage.value = 1
  matrixGeneration++
  matrixRows.clear()
  matrixLoading.clear()
  matrixErrors.clear()
  expandedOwners.value = []
  void load()
}

// 2026-10-03：两处此前都在骗用户。
//
// ① `catch { users.value = [] }` —— 无参 catch 连 console 都不记。请求失败时
//    页面渲染成「没有用户」，既没有 console 错误也没有任何提示。
//    本任务的原始要求之一就是「逐页检查 console 错误并修正」；静默吞掉
//    比报错更糟 —— 它让问题不可诊断。
// ② 后端在 schema 落后时返回 200 + 空列表（session_owners 是可选聚合表，
//    本地库未建）。修复前这与「真的没有用户」在页面上完全同形。
const loadError = ref('')
const degraded = ref<DegradationState>(emptyDegradation())

// ── 2026-11-20 数字出处轮 ────────────────────────────────────────────────
// usageMeta 由后端响应回传（source_view / 真实窗口）—— 帮助抽屉与出处行
// 引用它，不在前端硬编码口径文案 ⇒ 口径改动时后端是唯一真源。
const usageMeta = ref<UserUsageMeta | null>(null)
const showHelp = ref(false)

// 展开行：按 owner 惰性拉取员工×模型矩阵；同一窗口内缓存。
const expandedOwners = ref<string[]>([])
const matrixRows = reactive(new Map<string, UserUsageMatrixRow[]>())
const matrixLoading = reactive(new Set<string>())
const matrixErrors = reactive(new Map<string, string>())

const sortedUsers = computed(() => sortByName(users.value))

// 统计窗口的人类可读串（后端回传的真实窗口，缺省 = 近 30 天）。
const usageWindowText = computed(() => {
  const w = usageMeta.value
  if (!w) return ''
  const s = new Date(w.window_start)
  const e = new Date(w.window_end)
  if (Number.isNaN(s.getTime()) || Number.isNaN(e.getTime())) return ''
  const fmt = (d: Date) => {
    const pad = (n: number) => String(n).padStart(2, '0')
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
  }
  return `${fmt(s)} ~ ${fmt(e)}`
})

function formatCost(v: number | undefined | null): string {
  return v == null ? '—' : `$${v.toFixed(4)}`
}

function formatTokens(v: number | undefined | null): string {
  return (v ?? 0).toLocaleString()
}

function formatCredits(v: number | undefined | null): string {
  return v == null ? '—' : v.toLocaleString(undefined, { maximumFractionDigits: 6 })
}

onMounted(() => void load())
watch(linkedRange, () => {
  timeRange.value = null
  reloadRange()
})

async function load() {
  const generation = ++listGeneration
  loading.value = true
  loadError.value = ''
  degraded.value = emptyDegradation()
  try {
    // 时间范围 → ISO（Kx 的分钟级串无秒非规范格式，走补秒解析）。
    let dateFrom = linkedRange.value?.start
    let dateTo = linkedRange.value?.end
    if (timeRange.value) {
      const s = parseLocalMinute(timeRange.value.start)
      const e = parseLocalMinute(timeRange.value.end)
      if (!Number.isNaN(s.getTime())) dateFrom = s.toISOString()
      if (!Number.isNaN(e.getTime())) dateTo = e.toISOString()
    }
    const res = await getUserProfileList({
      limit: pageSize,
      offset: (currentPage.value - 1) * pageSize,
      search: search.value || undefined,
      date_from: dateFrom,
      date_to: dateTo,
    })
    if (generation !== listGeneration) return
    users.value = res.users || []
    total.value = res.total
    usageMeta.value = res.usage_meta ?? null
    if (isDegraded(res)) {
      degraded.value = mergeDegradation(
        degraded.value,
        t('sessions.userProfile.title'),
        res as unknown as DegradationPayload,
      )
    }
  } catch (e) {
    if (generation !== listGeneration) return
    // 记 console（可诊断）+ 记 error（用户可见），两者都不能少。
    console.error('user profile list load failed:', e)
    users.value = []
    total.value = 0
    loadError.value = surfaceError(e)
  } finally {
    if (generation === listGeneration) loading.value = false
  }
}

function currentWindowParams(): { start_date?: string; end_date?: string } {
  if (usageMeta.value) {
    return { start_date: usageMeta.value.window_start, end_date: usageMeta.value.window_end }
  }
  if (timeRange.value) {
    const s = parseLocalMinute(timeRange.value.start)
    const e = parseLocalMinute(timeRange.value.end)
    return {
      start_date: Number.isNaN(s.getTime()) ? undefined : s.toISOString(),
      end_date: Number.isNaN(e.getTime()) ? undefined : e.toISOString(),
    }
  }
  return {}
}

function profileRowKey(owner?: string, tenant?: string): string {
  return JSON.stringify([tenant ?? '', owner ?? ''])
}

const ownerRowKey = (row: UserProfileSummary) => profileRowKey(row.owner_user, row.tenant_id)

async function onExpandChange(_row: unknown, expanded: unknown) {
  const generation = matrixGeneration
  const window = currentWindowParams()
  const rows = (Array.isArray(expanded) ? expanded as UserProfileSummary[] : []).filter(row => row?.owner_user)
  expandedOwners.value = rows.map(ownerRowKey)
  for (const row of rows) {
    const key = ownerRowKey(row)
    if (generation !== matrixGeneration) return
    if (matrixRows.has(key) || matrixLoading.has(key)) continue
    matrixLoading.add(key)
    matrixErrors.delete(key)
    try {
      const res = await getUserUsageMatrix({
        ...window,
        owner: row.owner_user,
        tenant_id: row.tenant_id,
        limit: 200,
      })
      if (generation !== matrixGeneration) return
      if (isDegraded(res)) {
        matrixErrors.set(key, t('sessions.userProfile.modelUsageLoadFailed'))
      } else {
        matrixRows.set(key, res.rows ?? [])
      }
    } catch (e) {
      if (generation !== matrixGeneration) return
      console.error('usage matrix load failed:', row.owner_user, e)
      matrixErrors.set(key, surfaceError(e))
      matrixRows.set(key, [])
    } finally {
      if (generation === matrixGeneration) matrixLoading.delete(key)
    }
  }
}

function goProfile(owner?: string, tenant?: string) {
  if (!owner) return
  const window = currentWindowParams()
  void router.push({
    path: `/admin/session-analytics/users/${encodeURIComponent(owner)}`,
    query: { tenant_id: tenant, start_time: window.start_date, end_time: window.end_date },
  })
}

function goTask(owner: string, taskId: string) {
  // 员工 → 任务 的下钻：任务详情页自带该任务下的会话列表。
  void owner
  router.push(`/admin/session-analytics/tasks/${encodeURIComponent(taskId)}`)
}

function onSearch() {
  currentPage.value = 1
  void load()
}
</script>

<template>
  <div class="user-profile-list">
    <div class="page-header">
      <h2>{{ t('sessions.userProfile.title') }}</h2>
      <div class="header-actions">
        <!-- 2026-11-20 数字出处轮：统计口径从右侧抽屉弹出（公式+出处+窗口）。
             带文字（与详情页同形态）⇒ Maestro text: 能点中（icon-only 圆钮选不中）。 -->
        <el-button
          type="text"
          :icon="QuestionFilled"
          data-testid="user-profile-list-help"
          @click="showHelp = true"
        >
          {{ t('sessions.userProfile.showHelp') }}
        </el-button>
        <!-- 2026-11-19 时间范围统一轮：时间选择放页面右上角，统一 KxDateRangePicker。 -->
        <KxDateRangePicker
          :model-value="timeRange"
          :presets="kxPresets"
          precision="datetime"
          data-testid="user-profile-header-range"
          @apply="onRangeApply"
        />
      </div>
    </div>

    <el-card>
      <template #header>
        <div class="card-header-toolbar">
          <el-input
            v-model="search"
            :placeholder="t('sessions.userProfileSearchPlaceholder')"
            style="width: 280px"
            clearable
            @clear="onSearch"
            @keyup.enter="onSearch"
          />
          <!-- 数字出处行：后端 usage_meta 是唯一真源，缺省（近 30 天）也如实展示。 -->
          <span v-if="usageMeta" class="usage-meta-line" data-testid="user-profile-usage-meta">
            {{ t('sessions.userProfile.usageMetaLine', { source: usageMeta.source_view, window: usageWindowText }) }}
          </span>
        </div>
      </template>

      <!--
        降级 vs 失败 vs 空，三者必须可区分。
        修复前三种情况渲染出完全相同的页面：一个空表格 + 「没有用户」。

        ⚠️ degraded 时**刻意不显示**下面的「没有用户」：服务端没算出来，
        说「没有用户」是把「不知道」讲成「知道」。这是本页此前最有害的一处。
      -->
      <div v-if="degraded.active" class="alert alert-warning" role="status">
        <strong>{{ t('dataLifecycle.usageCost.degraded.title') }}</strong>
        {{ t('dataLifecycle.usageCost.degraded.hint') }}
        <ul class="degraded-reasons">
          <li v-for="r in degraded.reasons" :key="r">{{ r }}</li>
        </ul>
      </div>

      <div v-if="loadError" class="alert alert-danger">{{ loadError }}</div>

      <el-table
        v-loading="loading"
        :data="sortedUsers"
        stripe
        :expand-row-keys="expandedOwners"
        :row-key="ownerRowKey"
        @expand-change="onExpandChange"
      >
        <!-- 2026-11-20 数字出处轮：展开行 = 该员工窗口内「用了哪些模型、各多少」。 -->
        <el-table-column type="expand" width="44">
          <template #default="scope">
            <div class="expand-block">
              <div class="expand-title">{{ t('sessions.userProfile.expandModelsTitle', { owner: scope?.row?.owner_user }) }}</div>
              <div v-if="matrixLoading.has(profileRowKey(scope?.row?.owner_user, scope?.row?.tenant_id))" class="expand-hint">
                {{ t('sessions.userProfile.loading') }}
              </div>
              <div v-else-if="matrixErrors.get(profileRowKey(scope?.row?.owner_user, scope?.row?.tenant_id))" class="alert alert-danger">
                {{ matrixErrors.get(profileRowKey(scope?.row?.owner_user, scope?.row?.tenant_id)) }}
              </div>
              <el-table
                v-else
                :data="matrixRows.get(profileRowKey(scope?.row?.owner_user, scope?.row?.tenant_id)) ?? []"
                size="small"
                class="expand-table"
              >
                <el-table-column :label="t('sessions.userProfile.modelName')" prop="model_name" min-width="180" />
                <el-table-column :label="t('sessions.userProfile.provider')" prop="provider" min-width="120" />
                <el-table-column :label="t('sessions.userProfile.usageColRequests')" prop="request_count" width="90" align="right" />
                <el-table-column :label="t('sessions.userProfile.inputTokens')" prop="input_tokens" width="110" align="right">
                  <template #default="s">{{ formatTokens(s?.row?.input_tokens) }}</template>
                </el-table-column>
                <el-table-column :label="t('sessions.userProfile.outputTokens')" prop="output_tokens" width="110" align="right">
                  <template #default="s">{{ formatTokens(s?.row?.output_tokens) }}</template>
                </el-table-column>
                <el-table-column :label="t('sessions.userProfile.usageColTokens')" prop="total_tokens" width="120" align="right">
                  <template #default="s">{{ formatTokens(s?.row?.total_tokens) }}</template>
                </el-table-column>
                <el-table-column :label="t('common.unit.credits')" prop="internal_credits" width="120" align="right">
                  <template #default="s">{{ formatCredits(s?.row?.internal_credits) }}</template>
                </el-table-column>
                <el-table-column :label="t('sessions.userProfile.usageColExternalCost')" prop="external_cost_usd" width="120" align="right">
                  <template #default="s">{{ formatCost(s?.row?.external_cost_usd) }}</template>
                </el-table-column>
                <el-table-column :label="t('sessions.userProfile.actionDetail')" width="90" fixed="right">
                  <template #default="s">
                    <el-button size="small" link type="primary" @click="goProfile(scope?.row?.owner_user, scope?.row?.tenant_id)">
                      {{ t('sessions.userProfile.expandToProfile') }}
                    </el-button>
                  </template>
                </el-table-column>
              </el-table>
              <div
                v-if="!matrixLoading.has(profileRowKey(scope?.row?.owner_user, scope?.row?.tenant_id))
                  && !matrixErrors.get(profileRowKey(scope?.row?.owner_user, scope?.row?.tenant_id))
                  && (matrixRows.get(profileRowKey(scope?.row?.owner_user, scope?.row?.tenant_id)) ?? []).length === 0"
                class="expand-hint"
              >
                {{ t('sessions.userProfile.expandModelsEmpty') }}
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column :label="t('sessions.userProfile.ownerUser')" prop="owner_user" min-width="160" />
        <el-table-column :label="t('sessions.userProfile.sessionCount')" prop="session_count" width="100" align="right" />
        <el-table-column :label="t('sessions.userProfile.usageColRequests')" width="100" align="right">
          <template #default="scope">{{ scope?.row?.window_usage?.request_count ?? '—' }}</template>
        </el-table-column>
        <el-table-column :label="t('sessions.userProfile.usageColTokens')" width="130" align="right">
          <template #default="scope">{{ scope?.row?.window_usage ? formatTokens(scope.row.window_usage.total_tokens) : '—' }}</template>
        </el-table-column>
        <el-table-column :label="t('common.unit.credits')" width="130" align="right">
          <template #default="scope">{{ formatCredits(scope?.row?.window_usage?.internal_credits) }}</template>
        </el-table-column>
        <el-table-column :label="t('sessions.userProfile.usageColExternalCost')" width="130" align="right">
          <template #default="scope">{{ scope?.row?.window_usage ? formatCost(scope.row.window_usage.external_cost_usd) : '—' }}</template>
        </el-table-column>
        <el-table-column :label="t('sessions.userProfile.usageColModels')" width="100" align="right">
          <template #default="scope">{{ scope?.row?.window_usage?.model_count ?? '—' }}</template>
        </el-table-column>
        <el-table-column :label="t('sessions.userProfile.totalCost')" prop="total_cost_usd" width="120" align="right">
          <template #default="scope">
            {{ formatCost(scope?.row?.total_cost_usd) }}
          </template>
        </el-table-column>
        <el-table-column :label="t('sessions.userProfile.avgCostPerSession')" prop="avg_cost_per_session" width="120" align="right">
          <template #default="scope">
            ${{ (scope?.row?.avg_cost_per_session ?? 0).toFixed(4) }}
          </template>
        </el-table-column>
        <el-table-column :label="t('sessions.userProfile.endUserCount')" prop="end_user_count" width="110" align="right" />
        <el-table-column :label="t('sessions.userProfile.lastSeenAt')" prop="last_seen_at" width="170" />
        <el-table-column :label="t('sessions.userProfile.actionDetail')" width="100" fixed="right">
          <template #default="scope">
            <!-- ★ 按钮文字必须与列头「详情」不同：Maestro textRegex 按先序命中，
                 撞上列头（TH）就永远点不到行按钮（2026-11-20 首跑实测）。 -->
            <el-button size="small" type="primary" link @click="goProfile(scope?.row?.owner_user, scope?.row?.tenant_id)">
              {{ t('sessions.userProfile.actionGoDetail') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <!--
        「没有用户」只在**确实拿到了一份成功的、非降级的空列表**时显示。
        降级/失败时都不能说「没有用户」—— 那是把「不知道」说成「知道」。
      -->
      <div
        v-if="!loading && users.length === 0 && !degraded.active && !loadError"
        class="empty"
      >{{ t('sessions.userProfile.empty') }}</div>

      <div v-if="total > pageSize" class="pagination-wrap">
        <el-pagination
          v-model:current-page="currentPage"
          :page-size="pageSize"
          :total="total"
          layout="prev, pager, next"
          @current-change="load"
        />
      </div>
    </el-card>

    <el-drawer
      v-model="showHelp"
      :title="t('sessions.userProfile.helpTitle')"
      size="50%"
      direction="rtl"
    >
      <div class="help-content">
        <section class="help-section">
          <h3>{{ t('sessions.userProfile.listHelp.windowSection') }}</h3>
          <div v-if="usageMeta" class="formula">{{ usageWindowText }}</div>
        </section>

        <section class="help-section">
          <h3>{{ t('sessions.userProfile.helpContent.costCalculation') }}</h3>
          <p>{{ t('common.unit.credits') }}</p>
          <p>{{ t('sessions.userProfile.externalCost') }} (USD)</p>
        </section>

        <section class="help-section">
          <h3>{{ t('sessions.userProfile.helpContent.dataSource') }}</h3>
          <div v-if="usageMeta" class="formula">{{ usageMeta.source_view }}</div>
        </section>

        <section class="help-section">
          <h3>{{ t('sessions.userProfile.helpContent.timeRangeNote') }}</h3>
          <p>{{ t('sessions.userProfile.helpContent.timeRangeDesc') }}</p>
        </section>
      </div>
    </el-drawer>
  </div>
</template>

<style scoped>
.user-profile-list { padding: 16px; }
.page-header { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 16px; }
.header-actions { display: flex; align-items: center; gap: 12px; }
.card-header-toolbar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.usage-meta-line { color: var(--el-text-color-secondary, #909399); font-size: 12px; }
.pagination-wrap { margin-top: 16px; display: flex; justify-content: center; }
.expand-block { padding: 8px 24px 8px 48px; }
.expand-title { font-weight: 600; margin-bottom: 8px; }
.expand-hint { color: var(--el-text-color-secondary, #909399); font-size: 12px; padding: 8px 0; }
.expand-table { width: 100%; }
.help-content { display: flex; flex-direction: column; gap: 16px; }
.help-section h3 { margin: 0 0 6px; font-size: 15px; }
.help-section p { margin: 0 0 6px; font-size: 13px; line-height: 1.7; }
.help-section .formula {
  font-family: monospace;
  background: var(--el-fill-color-light, #f5f7fa);
  border-radius: 4px;
  padding: 6px 10px;
  font-size: 12px;
}
</style>
