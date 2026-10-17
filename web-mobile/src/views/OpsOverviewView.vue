<script setup lang="ts">
// OpsOverviewView — 运维总览（平台机队 / 区域 / 部署节点 / 故障 / 升级 /
// 离线激活 / 运行时指标），一个聚合包端点（2026-10-14，第一百一十七批）。
//
// GET /api/admin/ops/overview
// ⇒ 后端把 **11 个子查询并发跑**合进一个 `map[string]any`（`admin/ops_overview.go:78-130`），
//   本页**只发这一个请求**，不拆成 9 次往返（`:46-47` 的注释就是这个意思）。
//
// ── 权限 ────────────────────────────────────────────────────────────────────
// ★★★★★★ 权限档：**`requiresRole: 'super_admin'`** —— 归**第三类**
//   （服务端**完全没有租户维度** ⇒ 过度限制），与批 115/116 同处置。
//   12h-audit 20261017 更新：批 158e（eb0aee4ec）已把注册处改为
//   `mux.HandleFunc("/api/admin/ops/overview", h.superAdmin(h.handleOpsOverview))`
//   （`admin/handler.go:1134`，平台聚合 8 表全无 tenant_id ⇒ 档位本身就是门）。
//   ⇒ 后端**现在有真门**（SuperAdminMiddleware 逐字比 role）；本页的抽屉隐藏
//   + 兜底从「唯一的门」降级为**纵深防御**。历史记录保留在 ①-③：
//   ① 注册处曾传 `admin(...)`（AdminMiddleware 不检 role，auth.go:85-140）；
//   ② `ops_overview.go` 全文没有 `AuthContext`/`IsTenantAdmin`/`Role`；
//   ③ 11 条子查询全是全局表，`grep tenant` 打不到是设计上就没有。
//
// ★★★★ 为什么这族数据连 tenant_admin 都不该看（不是洁癖）：
//   `offline_requests` 每一行带 `license_key` / `hardware_hash` / `activation_code`
//   （`ops_overview.go:299-337`）—— 那是**别的租户的**凭据与硬件指纹；
//   `deployment_nodes` / `runtime_metrics_summary` 是**全平台机队**的 IP、版本与用量。
//
// ★★★★★ 本族最要紧的五件事（源文件头共 17 条，见 `api/opsOverview.ts`）：
//   1. ★★★★★ **键数恒在 2..12**（1 个恒在键 + 0..11 个成功的子查询）。
//      单个子查询失败 ⇒ **那个键直接缺失**、响应仍是 200（`:117-125`），
//      **全部失败才 500** ⇒ 客户端无法区分「这个子查询失败」与「后端版本不含它」。
//   2. ★★★★★ `data_plane_tables` 是**唯一恒在**的子查询键（`:288-292` 把错误吞成 `-1`）
//      ⇒ 缺键只有一种解释：响应不是这个端点发的（见 (6) 与本批新增的判据）。
//   3. ★★★★★ `region_stats` 前三行**恒**是 `local` / `245` / `154`（`:203` 硬编码），
//      缺失时补零值 + `missing: true`；**其余行的顺序由 map 迭代决定 ⇒ 不确定**。
//   4. ★★★★ `recent_faults.total <= fault_stats.open_events`（跨子查询上界）；
//      而 `recent_faults.total === events.length`、`recent_upgrades.total === items.length`
//      是**容器内自洽** —— 两个容器**键名还不一样**（`events` vs `items`）。
//   5. ★★★★ 同一个文件里两种 nil 编码并存：`region_stats[].last_heartbeat` 是
//      **键缺失**，而 `runtime_metrics_summary[].last_update` 是**裸 `null`**。
//      ⇒ 判据必须分开写，页面也不能用同一个占位逻辑解释两者。
import { computed, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchOpsOverview,
  opsOverviewRoleIsAllowed,
  opsOverviewDbNotConfiguredMessage,
  opsMissingSectionKeys,
  opsSectionCount,
  opsGeneratedAtIsSecondPrecision,
  opsResponseKeyCountIsInRange,
  opsDataPlaneKeysAreComplete,
  opsDataPlaneCountIsUnavailable,
  opsCenterStatusCountsFitTotal,
  opsListSectionTotalsMatchLengths,
  opsListSectionMismatchKinds,
  opsRegionIsPlaceholder,
  opsRegionPlaceholderIsAllZero,
  opsRegionPreambleCoversExpected,
  opsRegionExtraRowsFollowPreamble,
  opsOfflineTimestampMirrorsCreatedAt,
  opsOfflineIsApproved,
  opsRecentFaultsWithinOpenEvents,
  opsRecentFaultsAreAllNew,
  opsRuntimeRowMayBeOffline,
  opsRuntimeZeroIsAmbiguous,
  opsRuntimeHasMetrics,
  opsRuntimeLastUpdateIsNull,
  OPS_OVERVIEW_SECTION_KEYS,
  OPS_DATA_PLANE_KEYS,
  type OpsOverviewResponse,
} from '@/api/opsOverview'

useHyperPage({ title: () => (allowed.value ? t('ops.title') : t('common.pageDenied')) })

const auth = useAuthStore()

// ══ 权限兜底 ══════════════════════════════════════════════════════════════
// ★★★ 兜的是「不发请求」，不是数据访问 —— 后端这一族**没有门可兜**。

const role = computed(() => auth.role)
const allowed = computed(() => opsOverviewRoleIsAllowed(role.value))

// ══ 状态 ══════════════════════════════════════════════════════════════════

const data = ref<OpsOverviewResponse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

const centerStats = computed(() => data.value?.center_stats)
const regionStats = computed(() => data.value?.region_stats)
const deployNodes = computed(() => data.value?.deployment_nodes)
const dataPlane = computed(() => data.value?.data_plane_tables)
const offlineRequests = computed(() => data.value?.offline_requests)
const faultStats = computed(() => data.value?.fault_stats)
const recentFaults = computed(() => data.value?.recent_faults)
const recentUpgrades = computed(() => data.value?.recent_upgrades)
const downloadStats = computed(() => data.value?.download_stats)
const runtimeRows = computed(() => data.value?.runtime_metrics_summary)

/** ★★ `null`（没取到）与 `{}`（取到了但是空）必须分开显示，不能都当「无」。 */
const hasData = computed(() => data.value !== null)

// ★★★★★ 下面两个是**本批真踩出来的**：模板里原本写的是 `rows!.generated_at` /
//   `rows!.license_total`。`!` 只是**编译期**的非空断言，运行时 `rows` 就是 `null`
//   ⇒ 首屏（还没点读取）渲染就抛 `Cannot read properties of null` ⇒ 48 条用例整片红。
//   ⇒ 所以凡是要**按存在与否分支**的顶层键，一律搬进 `<script setup>` 的受守卫 computed。
const generatedAt = computed(() => (data.value ? data.value.generated_at : ''))
const licenseTotal = computed(() => (data.value ? data.value.license_total : undefined))

// ── 采集完整性（见 1 / 2）──────────────────────────────────────────────────

/** ★★★ 成功的子查询个数（0..11）。是「**该出现**」的那一面，不能只钉红线。 */
const sectionCount = computed(() => (data.value ? opsSectionCount(data.value) : 0))
const missingKeys = computed(() => (data.value ? opsMissingSectionKeys(data.value) : []))
/** ★ 只有 1 个子查询成功时才算「全降级」；0 个成功时后端会 500（这里仍兜住）。 */
const fullyDegraded = computed(() => sectionCount.value === 0)
/** ★★★★★ 键数越界 ⇒ 响应不是这个端点发的。 */
const keyCountBad = computed(() => (data.value ? !opsResponseKeyCountIsInRange(data.value) : false))
const keyCount = computed(() => (data.value ? Object.keys(data.value).length : 0))
/** ★★★★ `generated_at` 是秒级 RFC3339（`:115` `Format(time.RFC3339)`）⇒ 带小数点就是异常。 */
const generatedAtBad = computed(() =>
  data.value ? !opsGeneratedAtIsSecondPrecision(data.value.generated_at) : false,
)

// ── 自检红线（每一条都能被变异打掉）───────────────────────────────────────

/** ★★★★★ 见 2：`data_plane_tables` 恒在且六项齐全。 */
const dataPlaneBad = computed(() => (dataPlane.value ? !opsDataPlaneKeysAreComplete(dataPlane.value) : false))
/** ★★★★ `-1` 是「这一项查询失败」的哨兵，不是计数（`:289`）。 */
const dataPlaneUnavailable = computed(
  () =>
    dataPlane.value
      ? OPS_DATA_PLANE_KEYS.filter((k) => opsDataPlaneCountIsUnavailable(dataPlane.value![k])).length
      : 0,
)
/** ★★★★ 三个 `COUNT FILTER` 互斥 ⇒ 和必然 ≤ total。 */
const centerBad = computed(() => (centerStats.value ? !opsCenterStatusCountsFitTotal(centerStats.value) : false))
/** ★★★★ 容器内自洽（见 4）。返回「是哪一段」是为了页面能说清是谁不对。 */
const totalsBad = computed(() => (data.value ? !opsListSectionTotalsMatchLengths(data.value) : false))
const totalsBadKinds = computed(() => (data.value ? opsListSectionMismatchKinds(data.value) : []))
/** ★★★★★ 跨子查询上界（见 4）：只取 `new` 的列表必然不大于三态总数。 */
const faultRangeBad = computed(() => (data.value ? !opsRecentFaultsWithinOpenEvents(data.value) : false))
/** ★★★★ SQL 硬编码 `status = 'new'`（`:363`）⇒ 每行都必须是 `new`。 */
const faultStatusBad = computed(() => (data.value ? !opsRecentFaultsAreAllNew(data.value) : false))
/** ★★★★ 前三行恒是三个硬编码 region（见 3）。 */
const regionPreambleBad = computed(() =>
  regionStats.value ? !opsRegionPreambleCoversExpected(regionStats.value) : false,
)
/** ★★★ 额外行只能排在硬编码三行之后（顺序不定，但不该**穿插**在前三行里）。 */
const regionExtraBad = computed(() =>
  regionStats.value ? !opsRegionExtraRowsFollowPreamble(regionStats.value) : false,
)
/** ★★★★ 占位行恒为四个计数全 0（`:211-218`）⇒ 打了 `missing` 却有数就是异常。 */
const regionPlaceholderBad = computed(
  () =>
    regionStats.value
      ? regionStats.value.filter((r) => opsRegionIsPlaceholder(r) && !opsRegionPlaceholderIsAllZero(r)).length
      : 0,
)
const regionPlaceholderCount = computed(
  () => (regionStats.value ? regionStats.value.filter(opsRegionIsPlaceholder).length : 0),
)
/** ★★★★ 同一个 `createdAt` 写了两遍（`:328-329`）⇒ 逐行必然相等。 */
const offlineMirrorBad = computed(
  () =>
    offlineRequests.value
      ? offlineRequests.value.filter((r) => !opsOfflineTimestampMirrorsCreatedAt(r)).length
      : 0,
)
const offlineApproved = computed(
  () => (offlineRequests.value ? offlineRequests.value.filter(opsOfflineIsApproved).length : 0),
)

// ── 运行时段（见 5）────────────────────────────────────────────────────────

/** ★★★ `last_update` 可能是裸 `null` ⇒ 与 `region_stats` 的键缺失**不是一回事**。 */
const runtimeNullUpdate = computed(() =>
  runtimeRows.value ? runtimeRows.value.filter(opsRuntimeLastUpdateIsNull).length : 0,
)
/** ★★★ 三个 AVG 全 0 是**二义**的（真 0 / 24h 无指标）⇒ 只报「可能二义」。 */
const runtimeZeroAmbiguous = computed(() =>
  runtimeRows.value ? runtimeRows.value.filter(opsRuntimeZeroIsAmbiguous).length : 0,
)
/** ★★★ 榜上出现 ≠ 在线（`:488` 的 WHERE 让离线实例有指标时也上榜）。 */
const runtimeOfflineRows = computed(() =>
  runtimeRows.value ? runtimeRows.value.filter(opsRuntimeRowMayBeOffline).length : 0,
)
/** ★★ 真正「有指标」的行数 ⇒ 与上面那个二义计数一起看才有意义。 */
const runtimeWithMetrics = computed(() =>
  runtimeRows.value ? runtimeRows.value.filter(opsRuntimeHasMetrics).length : 0,
)

// ══ 取数 ══════════════════════════════════════════════════════════════════

function describe(err: unknown): string {
  const msg = (err as Error)?.message || String(err)
  if (opsOverviewDbNotConfiguredMessage(msg)) return t('ops.err.dbNotConfigured')
  // ⚠️ 本族**不设** forbidden 分支：后端这一族没有 role 门，403 不该从它来。
  //   留着一条 403 归因会把「别处挡的」说成「这一族挡的」。
  return msg
}

async function load(): Promise<void> {
  // ★★ 兜底：不允许访问时**连请求都不发**。
  if (!allowed.value) return
  loading.value = true
  error.value = null
  try {
    data.value = await fetchOpsOverview()
  } catch (err) {
    data.value = null
    error.value = describe(err)
  } finally {
    loading.value = false
  }
}

async function reload(): Promise<void> {
  await load()
}
</script>

<template>
  <div class="opsv">
    <!-- ★★ 深链兜底：不是后端 403（这族后端没有门），而是前端在**发请求之前**就挡住。 -->
    <section v-if="!allowed" class="opsv__sec" data-testid="opsv-nope">
      <header class="opsv__head">
        <h2 class="opsv__h">{{ t('ops.title') }}</h2>
      </header>
      <p class="opsv__msg opsv__msg--err" data-testid="opsv-nope-msg">{{ t('ops.nope.msg') }}</p>
      <p class="opsv__msg" data-testid="opsv-nope-why">{{ t('ops.nope.why') }}</p>
    </section>

    <template v-else>
      <!-- ══ 全局操作 ══ -->
      <section class="opsv__sec">
        <header class="opsv__head">
          <h2 class="opsv__h">{{ t('ops.load.title') }}</h2>
          <button
            type="button"
            class="opsv__btn"
            data-testid="opsv-load"
            :disabled="loading"
            @click="reload"
          >
            {{ loading ? t('common.loading') : t('ops.load.btn') }}
          </button>
        </header>
        <p class="opsv__msg" data-testid="opsv-scope-note">{{ t('ops.scope.note') }}</p>
      </section>

      <p v-if="error" class="opsv__msg opsv__msg--err" data-testid="opsv-error">{{ error }}</p>

      <template v-if="!error">
        <!-- ══ 段①：采集完整性 ══ -->
        <section class="opsv__sec">
          <header class="opsv__head">
            <h2 class="opsv__h">{{ t('ops.fresh.title') }}</h2>
            <span class="opsv__badge" data-testid="opsv-section-count">{{ sectionCount }}</span>
          </header>

          <p v-if="!hasData && !loading" class="opsv__msg" data-testid="opsv-empty">
            {{ t('ops.fresh.empty') }}
          </p>

          <template v-if="hasData">
            <div class="opsv__row">
              <span>{{ t('ops.fresh.generatedAt') }}</span>
              <span class="opsv__mono" data-testid="opsv-generated-at">{{ generatedAt }}</span>
            </div>
            <!-- ★ 「该出现」的一面：成功段数 + 缺失键清单，不能只报红线。 -->
            <p class="opsv__msg" data-testid="opsv-sections">
              {{ t('ops.fresh.sections', { n: sectionCount, total: OPS_OVERVIEW_SECTION_KEYS.length }) }}
            </p>
            <p class="opsv__msg" data-testid="opsv-missing">
              {{ t('ops.fresh.missing', { list: missingKeys.length ? missingKeys.join(' / ') : t('ops.fresh.none') }) }}
            </p>
            <p class="opsv__msg" data-testid="opsv-key-count">
              {{ t('ops.fresh.keyCount', { n: keyCount }) }}
            </p>

            <p v-if="generatedAtBad" class="opsv__msg opsv__msg--err" data-testid="opsv-generated-bad">
              {{ t('ops.fresh.precisionBad') }}
            </p>
            <p v-if="keyCountBad" class="opsv__msg opsv__msg--err" data-testid="opsv-keycount-bad">
              {{ t('ops.fresh.keyCountBad') }}
            </p>
            <p v-if="fullyDegraded" class="opsv__msg opsv__msg--err" data-testid="opsv-degraded">
              {{ t('ops.fresh.degraded') }}
            </p>
            <!-- ★★★ 这条红线放在**采集完整性**段而不是故障段：它是**响应级**不变式
                 （两个「最近列表」的 total 与容器长度一致），与某一个段取没取到无关。
                 ★★ 变异台 S25 顶出来的：原先它嵌在 `<template v-if="recentFaults">` 里
                   ⇒ 两个容器都缺时那一行**根本不渲染** ⇒ 「缺键不报违规」恒真。 -->
            <p v-if="totalsBad" class="opsv__msg opsv__msg--err" data-testid="opsv-totals-bad">
              {{ t('ops.fault.totalsBad', { list: totalsBadKinds.join(' / ') }) }}
            </p>
          </template>
        </section>

        <!-- ══ 段②：平台概览 ══ -->
        <section class="opsv__sec">
          <header class="opsv__head">
            <h2 class="opsv__h">{{ t('ops.summary.title') }}</h2>
          </header>

          <p v-if="!centerStats && !downloadStats" class="opsv__msg" data-testid="opsv-summary-empty">
            {{ t('ops.summary.empty') }}
          </p>

          <template v-if="centerStats">
            <ul class="opsv__list">
              <li class="opsv__card" data-testid="opsv-center-card">
                <div class="opsv__row">
                  <span class="opsv__strong">{{ t('ops.field.instances') }}</span>
                  <span class="opsv__badge">{{ centerStats.total_instances }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.summary.online') }}</span>
                  <span class="opsv__mono">{{ centerStats.online_instances }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.summary.offline') }}</span>
                  <span class="opsv__mono">{{ centerStats.offline_instances }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.summary.degraded') }}</span>
                  <span class="opsv__mono">{{ centerStats.degraded_instances }}</span>
                </div>
              </li>
            </ul>
            <p v-if="centerBad" class="opsv__msg opsv__msg--err" data-testid="opsv-center-bad">
              {{ t('ops.summary.centerBad') }}
            </p>
          </template>

          <template v-if="downloadStats">
            <ul class="opsv__list">
              <li class="opsv__card" data-testid="opsv-download-card">
                <div class="opsv__row">
                  <span class="opsv__strong">{{ t('ops.summary.downloads') }}</span>
                  <span class="opsv__badge">{{ downloadStats.total_downloads }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.summary.today') }}</span>
                  <span class="opsv__mono">{{ downloadStats.today_downloads }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.summary.week') }}</span>
                  <span class="opsv__mono">{{ downloadStats.week_downloads }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.summary.supporters') }}</span>
                  <span class="opsv__mono">{{ downloadStats.supporter_count }}</span>
                </div>
              </li>
            </ul>
          </template>

          <p v-if="licenseTotal !== undefined" class="opsv__msg" data-testid="opsv-license-total">
            {{ t('ops.summary.licenseTotal', { n: licenseTotal }) }}
          </p>
        </section>

        <!-- ══ 段③：数据面表计数 ══ -->
        <section class="opsv__sec">
          <header class="opsv__head">
            <h2 class="opsv__h">{{ t('ops.dataPlane.title') }}</h2>
            <span v-if="dataPlane" class="opsv__badge" data-testid="opsv-dataplane-count">
              {{ OPS_DATA_PLANE_KEYS.length }}
            </span>
          </header>

          <p v-if="!dataPlane" class="opsv__msg" data-testid="opsv-dataplane-absent">
            {{ t('ops.dataPlane.absent') }}
          </p>

          <template v-if="dataPlane">
            <p v-if="dataPlaneUnavailable" class="opsv__msg" data-testid="opsv-dataplane-unavailable">
              {{ t('ops.dataPlane.unavailable', { n: dataPlaneUnavailable }) }}
            </p>
            <p v-if="dataPlaneBad" class="opsv__msg opsv__msg--err" data-testid="opsv-dataplane-bad">
              {{ t('ops.dataPlane.bad') }}
            </p>
            <ul class="opsv__list">
              <li
                v-for="k in OPS_DATA_PLANE_KEYS"
                :key="k"
                class="opsv__card"
                :data-testid="`opsv-dataplane-row-${k}`"
              >
                <div class="opsv__row">
                  <span class="opsv__strong">{{ k }}</span>
                  <span class="opsv__badge">{{ dataPlane[k] }}</span>
                </div>
              </li>
            </ul>
          </template>
        </section>

        <!-- ══ 段④：区域 ══ -->
        <section class="opsv__sec">
          <header class="opsv__head">
            <h2 class="opsv__h">{{ t('ops.region.title') }}</h2>
            <span v-if="regionStats" class="opsv__badge" data-testid="opsv-region-count">
              {{ regionStats.length }}
            </span>
          </header>

          <p v-if="!regionStats" class="opsv__msg" data-testid="opsv-region-absent">{{ t('ops.region.absent') }}</p>

          <template v-if="regionStats">
            <p v-if="regionPlaceholderCount" class="opsv__msg" data-testid="opsv-region-placeholder">
              {{ t('ops.region.placeholder', { n: regionPlaceholderCount }) }}
            </p>
            <p v-if="regionPlaceholderBad" class="opsv__msg opsv__msg--err" data-testid="opsv-region-placeholder-bad">
              {{ t('ops.region.placeholderBad', { n: regionPlaceholderBad }) }}
            </p>
            <p v-if="regionPreambleBad" class="opsv__msg opsv__msg--err" data-testid="opsv-region-preamble-bad">
              {{ t('ops.region.preambleBad') }}
            </p>
            <p v-if="regionExtraBad" class="opsv__msg opsv__msg--err" data-testid="opsv-region-extra-bad">
              {{ t('ops.region.extraBad') }}
            </p>

            <ul class="opsv__list">
              <li
                v-for="(r, i) in regionStats"
                :key="r.region"
                class="opsv__card"
                :data-testid="`opsv-region-row-${r.region}-${i}`"
              >
                <div class="opsv__row">
                  <span class="opsv__strong">{{ r.region }}</span>
                  <span v-if="opsRegionIsPlaceholder(r)" class="opsv__tag" data-testid="opsv-region-missing-tag">
                    {{ t('ops.region.missingTag') }}
                  </span>
                  <span v-else class="opsv__badge">{{ r.total_instances }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.summary.online') }}</span>
                  <span class="opsv__mono">{{ r.online_instances }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.summary.offline') }}</span>
                  <span class="opsv__mono">{{ r.offline_instances }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.summary.degraded') }}</span>
                  <span class="opsv__mono">{{ r.degraded_instances }}</span>
                </div>
                <!-- ★ 见 5：`last_heartbeat` 是**键缺失**编码，不是 null。 -->
                <p class="opsv__ts">
                  {{ t('ops.field.lastHeartbeat') }}:
                  {{ 'last_heartbeat' in r ? r.last_heartbeat : t('ops.field.absent') }}
                </p>
              </li>
            </ul>
          </template>
        </section>

        <!-- ══ 段⑤：部署节点 ══ -->
        <section class="opsv__sec">
          <header class="opsv__head">
            <h2 class="opsv__h">{{ t('ops.nodes.title') }}</h2>
            <span v-if="deployNodes" class="opsv__badge" data-testid="opsv-nodes-count">{{ deployNodes.length }}</span>
          </header>

          <p v-if="!deployNodes" class="opsv__msg" data-testid="opsv-nodes-absent">{{ t('ops.nodes.absent') }}</p>
          <p v-else-if="!deployNodes.length" class="opsv__msg" data-testid="opsv-nodes-empty">
            {{ t('ops.nodes.empty') }}
          </p>

          <ul v-else class="opsv__list">
            <li
              v-for="n in deployNodes"
              :key="n.instance_id"
              class="opsv__card"
              :data-testid="`opsv-node-row-${n.instance_id}`"
            >
              <div class="opsv__row">
                <span class="opsv__strong">{{ n.hostname }}</span>
                <span class="opsv__badge">{{ n.status }}</span>
              </div>
              <div class="opsv__list-row">
                <span>{{ t('ops.field.ip') }}</span>
                <span class="opsv__mono">{{ n.ip_address }}</span>
              </div>
              <div class="opsv__list-row">
                <span>{{ t('ops.field.region') }}</span>
                <span class="opsv__mono">{{ n.region }}</span>
              </div>
              <div class="opsv__list-row">
                <span>{{ t('ops.field.build') }}</span>
                <span class="opsv__mono">{{ n.version }} / {{ n.build_seq }}</span>
              </div>
              <p class="opsv__ts">{{ t('ops.field.lastHeartbeat') }}: {{ n.last_heartbeat }}</p>
            </li>
          </ul>
        </section>

        <!-- ══ 段⑥：故障 ══ -->
        <section class="opsv__sec">
          <header class="opsv__head">
            <h2 class="opsv__h">{{ t('ops.fault.title') }}</h2>
            <span v-if="faultStats" class="opsv__badge" data-testid="opsv-fault-open">
              {{ faultStats.open_events }}
            </span>
          </header>

          <p v-if="!faultStats" class="opsv__msg" data-testid="opsv-fault-absent">{{ t('ops.fault.absent') }}</p>

          <template v-if="recentFaults">
            <p v-if="faultRangeBad" class="opsv__msg opsv__msg--err" data-testid="opsv-fault-range-bad">
              {{ t('ops.fault.rangeBad') }}
            </p>
            <p v-if="faultStatusBad" class="opsv__msg opsv__msg--err" data-testid="opsv-fault-status-bad">
              {{ t('ops.fault.statusBad') }}
            </p>

            <p v-if="!recentFaults.events.length" class="opsv__msg" data-testid="opsv-fault-empty">
              {{ t('ops.fault.empty') }}
            </p>

            <ul v-else class="opsv__list">
              <li
                v-for="(e, i) in recentFaults.events"
                :key="`${e.id}-${i}`"
                class="opsv__card"
                :data-testid="`opsv-fault-row-${e.id}`"
              >
                <div class="opsv__row">
                  <span class="opsv__strong">{{ e.title }}</span>
                  <span class="opsv__badge">{{ e.severity }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.field.rule') }}</span>
                  <span class="opsv__mono">{{ e.rule_name }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.field.source') }}</span>
                  <span class="opsv__mono">{{ e.source }}</span>
                </div>
                <p class="opsv__ts">{{ t('ops.field.detectedAt') }}: {{ e.detected_at }}</p>
              </li>
            </ul>
          </template>
        </section>

        <!-- ══ 段⑦：升级 ══ -->
        <section class="opsv__sec">
          <header class="opsv__head">
            <h2 class="opsv__h">{{ t('ops.upgrade.title') }}</h2>
            <span v-if="recentUpgrades" class="opsv__badge" data-testid="opsv-upgrade-total">
              {{ recentUpgrades.total }}
            </span>
          </header>

          <p v-if="!recentUpgrades" class="opsv__msg" data-testid="opsv-upgrade-absent">
            {{ t('ops.upgrade.absent') }}
          </p>

          <template v-if="recentUpgrades">
            <p v-if="!recentUpgrades.items.length" class="opsv__msg" data-testid="opsv-upgrade-empty">
              {{ t('ops.upgrade.empty') }}
            </p>

            <ul v-else class="opsv__list">
              <li
                v-for="(u, i) in recentUpgrades.items"
                :key="`${u.instance_id}-${i}`"
                class="opsv__card"
                :data-testid="`opsv-upgrade-row-${i}`"
              >
                <div class="opsv__row">
                  <span class="opsv__strong">{{ u.instance_id }}</span>
                  <span class="opsv__badge">{{ u.status }}</span>
                </div>
                <!-- ★★★ `version` 走 COALESCE，兜底是**空串**（`:400`）⇒ 空串要显式说清。 -->
                <div class="opsv__list-row">
                  <span>{{ t('ops.upgrade.version') }}</span>
                  <span class="opsv__mono">
                    {{ u.version === '' ? t('ops.upgrade.versionEmpty') : u.version }}
                  </span>
                </div>
                <p class="opsv__ts">
                  {{ t('ops.field.startedAt') }}: {{ u.started_at }} ·
                  {{ t('ops.field.completedAt') }}:
                  {{ 'completed_at' in u ? u.completed_at : t('ops.field.absent') }}
                </p>
              </li>
            </ul>
          </template>
        </section>

        <!-- ══ 段⑧：离线激活请求（★ 本族最敏感的一段）═════════════════════════ -->
        <section class="opsv__sec">
          <header class="opsv__head">
            <h2 class="opsv__h">{{ t('ops.offline.title') }}</h2>
            <span v-if="offlineRequests" class="opsv__badge" data-testid="opsv-offline-count">
              {{ offlineRequests.length }}
            </span>
          </header>

          <p class="opsv__msg" data-testid="opsv-offline-note">{{ t('ops.offline.note') }}</p>

          <p v-if="!offlineRequests" class="opsv__msg" data-testid="opsv-offline-absent">
            {{ t('ops.offline.absent') }}
          </p>

          <template v-if="offlineRequests">
            <p class="opsv__msg" data-testid="opsv-offline-approved">
              {{ t('ops.offline.approved', { n: offlineApproved }) }}
            </p>
            <p v-if="offlineMirrorBad" class="opsv__msg opsv__msg--err" data-testid="opsv-offline-mirror-bad">
              {{ t('ops.offline.mirrorBad', { n: offlineMirrorBad }) }}
            </p>
            <p v-if="!offlineRequests.length" class="opsv__msg" data-testid="opsv-offline-empty">
              {{ t('ops.offline.empty') }}
            </p>

            <ul v-else class="opsv__list">
              <li
                v-for="(r, i) in offlineRequests"
                :key="`${r.request_id}-${i}`"
                class="opsv__card"
                :data-testid="`opsv-offline-row-${i}`"
              >
                <div class="opsv__row">
                  <span class="opsv__strong">{{ r.device_name }}</span>
                  <span class="opsv__badge">{{ r.status }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.field.licenseKey') }}</span>
                  <span class="opsv__mono">{{ r.license_key }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.field.hardwareHash') }}</span>
                  <span class="opsv__mono">{{ r.hardware_hash }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.field.requestId') }}</span>
                  <span class="opsv__mono">{{ r.request_id }}</span>
                </div>
                <!-- ★★ `approved_at` / `activation_code` 都是**条件键**（`:332-337`）。 -->
                <p class="opsv__ts">
                  {{ t('ops.field.createdAt') }}: {{ r.created_at }} ·
                  {{ t('ops.field.approvedAt') }}:
                  {{ opsOfflineIsApproved(r) ? r.approved_at : t('ops.field.absent') }}
                </p>
                <p v-if="'activation_code' in r" class="opsv__ts" data-testid="opsv-offline-code">
                  {{ t('ops.field.activationCode') }}: {{ r.activation_code }}
                </p>
              </li>
            </ul>
          </template>
        </section>

        <!-- ══ 段⑨：运行时指标 ══ -->
        <section class="opsv__sec">
          <header class="opsv__head">
            <h2 class="opsv__h">{{ t('ops.runtime.title') }}</h2>
            <span v-if="runtimeRows" class="opsv__badge" data-testid="opsv-runtime-count">{{ runtimeRows.length }}</span>
          </header>

          <p v-if="!runtimeRows" class="opsv__msg" data-testid="opsv-runtime-absent">{{ t('ops.runtime.absent') }}</p>

          <template v-if="runtimeRows">
            <p class="opsv__msg" data-testid="opsv-runtime-metrics">
              {{ t('ops.runtime.withMetrics', { n: runtimeWithMetrics }) }}
            </p>
            <p class="opsv__msg" data-testid="opsv-runtime-null-update">
              {{ t('ops.runtime.nullUpdate', { n: runtimeNullUpdate }) }}
            </p>
            <p v-if="runtimeZeroAmbiguous" class="opsv__msg" data-testid="opsv-runtime-zero">
              {{ t('ops.runtime.zeroAmbiguous', { n: runtimeZeroAmbiguous }) }}
            </p>
            <p v-if="runtimeOfflineRows" class="opsv__msg" data-testid="opsv-runtime-offline">
              {{ t('ops.runtime.offlineRows', { n: runtimeOfflineRows }) }}
            </p>

            <p v-if="!runtimeRows.length" class="opsv__msg" data-testid="opsv-runtime-empty">
              {{ t('ops.runtime.empty') }}
            </p>

            <ul v-else class="opsv__list">
              <li
                v-for="m in runtimeRows"
                :key="m.instance_id"
                class="opsv__card"
                :data-testid="`opsv-runtime-row-${m.instance_id}`"
              >
                <div class="opsv__row">
                  <span class="opsv__strong">{{ m.hostname }}</span>
                  <span class="opsv__badge">{{ m.status }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.runtime.cpu') }}</span>
                  <span class="opsv__mono">{{ m.avg_cpu_pct }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.runtime.mem') }}</span>
                  <span class="opsv__mono">{{ m.avg_mem_pct }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.runtime.tps') }}</span>
                  <span class="opsv__mono">{{ m.avg_tps }}</span>
                </div>
                <div class="opsv__list-row">
                  <span>{{ t('ops.runtime.p99') }}</span>
                  <span class="opsv__mono">{{ m.max_p99_ms }}</span>
                </div>
                <!-- ★★ 见 5：这里可能是**裸 null**，与上一段的键缺失**不是一回事**。 -->
                <p class="opsv__ts">
                  {{ t('ops.runtime.lastUpdate') }}:
                  {{ opsRuntimeLastUpdateIsNull(m) ? t('ops.field.absent') : m.last_update }}
                </p>
              </li>
            </ul>
          </template>
        </section>
      </template>
    </template>
  </div>
</template>

<style scoped>
.opsv {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.opsv__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.opsv__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.opsv__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

/* ★ R1：新增触控控件一律 ≥48 CSS px。 */
.opsv__btn {
  min-height: 48px;
}

.opsv__btn {
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.opsv__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.opsv__msg--err {
  color: var(--app-danger);
}

.opsv__list {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.opsv__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.opsv__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: center;
  justify-content: space-between;
}

.opsv__list-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}

.opsv__mono {
  color: var(--app-text);
  word-break: break-all;
  text-align: right;
}

.opsv__strong {
  font-size: 0.8125rem;
  font-weight: 600;
  color: var(--app-text);
}

.opsv__badge {
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--app-primary);
  font-variant-numeric: tabular-nums;
}

/* ★ 占位标记（region 缺失行）：用 --app-primary，本仓**没有** --app-accent。 */
.opsv__tag {
  font-size: 0.6875rem;
  font-weight: 600;
  color: var(--app-primary);
}

.opsv__ts {
  margin: 0;
  font-size: 0.6875rem;
  color: var(--app-text-muted);
  font-variant-numeric: tabular-nums;
}
</style>