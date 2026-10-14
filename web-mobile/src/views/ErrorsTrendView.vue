<script setup lang="ts">
// ErrorsTrendView — 供应商错误趋势（2026-10-14，第一百一十三批）。
//
// GET /api/errors/trend
//
// ★★★★★★ 权限档：**`requiresRole: 'admin'`** —— 本仓第二个该档席（第一个是 `users`）。
//   推导链条见 `api/errorsTrend.ts` 文件头「权限档」一节，四步都是当场读码核过的：
//   1. `admin/handler.go:1270` `admin(h.errorsTrendHandlers.getErrorsTrend)`
//      ⇒ `admin.AdminMiddleware`（`admin/auth.go:85-138`）**只认证、不检 role**
//      （`admin/users.go:307-309` 的注释把这写成显式设计）；
//   2. `getErrorsTrend`（`admin/errors_trend.go:75-146`）自己也不检 role；
//   3. 租户范围取 `EffectiveTenantIDAll(r)`（`:112`）⇒
//      `admin/context.go:69-74`：**非 tenant_admin 一律空串**，而空串命中
//      `AND ($7 = '' OR …)` ⇒ **查全租户**；
//   4. ⇒★ effective role 为 `user` 的主体**能读到全租户**，而这个角色**可达**：
//      `admin/shared_principal.go:42` 的 `roleRank` 含 `"user"`，
//      且 `:46-48` 只在本地角色**更低**时下调 ⇒ 本地 super_admin + 外来 `user`
//      ⇒ 生效角色留在 `user`。
//   ⇒ 所以「不设 requiresRole」是错的（`navItemsFor` 默认分支恒 `true`），
//     「super_admin」也过度限制（`tenant_admin` 本该看自己租户，R49-C1 正是为此）。
//
// ⚠️⚠️ **抽屉隐藏 ≠ 路由拦截**：`src/hyper-wiring.ts:34-45` 的 `beforeEach`
//   **只查登录态**，`meta.requiresRole` 全仓无人读取 ⇒ 深链照样能打开页面。
//   ⇒⇒ 本视图**必须**自己再兜一层（见下方 `allowed`），这是**兜底不是修复**：
//   后端那条路本来就开着，前端挡的是「一键可达」。
//
// ★★★★★ 本族最要紧的五件事（详见 api/errorsTrend.ts 文件头八条）：
//   1. ★★★★★ **两个数据源**，`source` 告诉你读的是预聚合还是明细兜底。
//      `fallback` **不等于**「没有错误」—— 只说明预聚合表在这个粒度上没有行。
//      但 `fallback` + `time_series` 空是**确定的**：明细也没有 ⇒ 确实没错误。
//   2. ★★★★★ **读路径刻意绕过 RLS**（`set_config('app.bypass_rls','true',true)`）
//      ⇒「0 行」本身就是一个被文档化的失败模式，租户隔离靠 **WHERE 参数**兜。
//   3. ★★★★★ `summary.unique_requests` 是**各桶相加**，不是去重 ⇒ 它是**上界**。
//   4. ★★★★ `by_supplier` / `by_error_type` 是 map + omitempty ⇒ 空时**整个键省略**
//      （不是 `{}`，不是 `null`）；`top_*` 两个却是**恒数组** ⇒ 同一响应里两种编码并存。
//   5. ★★★ `hours` 是严格三值枚举，`granularity` 缺省由它推导，显式传则严格校验。
import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchErrorsTrend,
  errorsTrendRoleIsAdminTier,
  errorsTrendTenantScopeLabel,
  errorsTrendParamsAreAcceptable,
  errorsTrendGranularityEchoIsConsistent,
  errorsTrendDefaultGranularity,
  errorsTrendPreAggregationMissed,
  errorsTrendWindowIsGenuinelyEmpty,
  errorsTrendSummaryMatchesSeries,
  errorsTrendUniqueRequestsIsUpperBound,
  errorsTrendSeriesIsAscending,
  errorsTrendBreakdownIsTruncated,
  errorsTrendPointHasSupplierDim,
  errorsTrendPointHasErrorTypeDim,
  errorsTrendFilterIsWildcard,
  ERRORS_TREND_HOURS,
  ERRORS_TREND_GRANULARITIES,
  ERRORS_TREND_BREAKDOWN_LIMIT,
  ERRORS_TREND_SOURCES,
  type ErrorsTrendResponse,
  type ErrorsTrendPoint,
} from '@/api/errorsTrend'

useHyperPage({ title: () => t('et.title') })

const auth = useAuthStore()

// ══ 权限兜底 ══════════════════════════════════════════════════════════════
// ★★★ 深链能绕过抽屉 ⇒ 这里自己判一次。理由与 `requiresRole: 'admin'` 相同，
//   但**这是前端兜底**：后端对 `user` 角色照样返回跨租户数据。

const role = computed(() => auth.role)
const allowed = computed(() => errorsTrendRoleIsAdminTier(role.value))
/** ★ 只是**据实说明**口径，不是安全门 —— 后端越权一律表现为「数据看起来是全的」。 */
const scopeLabel = computed(() => errorsTrendTenantScopeLabel(role.value))

// ══ 查询条件 ══════════════════════════════════════════════════════════════

const hours = ref<number>(24)
/** ★ `''` = 不显式发，由后端按 hours 推导（`:81-90`）。 */
const granularity = ref<string>('')
const supplier = ref('')
const errorType = ref('')
const credentialIdRaw = ref('')

/** ★ 空串 = 不发；发出去就必须是正整数（后端 `:97-103` 要求 `> 0`）。 */
const credentialId = computed(() => {
  const raw = credentialIdRaw.value.trim()
  if (raw === '') return null
  const n = Number(raw)
  return Number.isFinite(n) ? n : Number.NaN
})

const paramsOk = computed(() =>
  errorsTrendParamsAreAcceptable({
    hours: hours.value,
    granularity: granularity.value,
    credentialId: credentialId.value,
  }),
)

/** ★★ `hours` 改变而没显式选粒度时，缺省推导的结果会变 ⇒ 这里跟着算出来给用户看。 */
const autoGranularity = computed(() => errorsTrendDefaultGranularity(hours.value))
const granularityWasSent = computed(() => granularity.value !== '')

const data = ref<ErrorsTrendResponse | null>(null)
const trendError = ref<string | null>(null)
const loading = ref(false)

async function load(): Promise<void> {
  // ★ 兜底：不允许访问时**连请求都不发**（抽屉已被 requiresRole 挡过一次）
  if (!allowed.value) return
  loading.value = true
  trendError.value = null
  try {
    data.value = await fetchErrorsTrend({
      hours: hours.value,
      granularity: granularity.value || undefined,
      supplier: supplier.value,
      credentialId: credentialId.value,
      errorType: errorType.value,
    })
  } catch (err) {
    data.value = null
    trendError.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}

// ══ 判读 ══════════════════════════════════════════════════════════════════

/** ★★ `fallback` 只说明「预聚合表在这个粒度上没有行」，**不等于**「没有错误」。 */
const fellBack = computed(() => (data.value ? errorsTrendPreAggregationMissed(data.value) : false))
/** ★★ 但 `fallback` + 空序列是**确定的**：明细也没有 ⇒ 窗口内确实没错误。 */
const genuinelyEmpty = computed(() => (data.value ? errorsTrendWindowIsGenuinelyEmpty(data.value) : false))

/** ★★★ 后端就是这么算的（桶求和），客户端核对。 */
const summaryMismatch = computed(() =>
  data.value ? !errorsTrendSummaryMatchesSeries(data.value) : false,
)
/** ★★ `unique_requests` 是**上界**：汇总值不该**小于**序列求和。 */
const uniqueNotUpperBound = computed(() =>
  data.value ? !errorsTrendUniqueRequestsIsUpperBound(data.value) : false,
)
const notAscending = computed(() => (data.value ? !errorsTrendSeriesIsAscending(data.value) : false))
/** ★ 粒度回显与客户端认知是否一致；显式指定时**真空**，不报。 */
const granularityEchoOk = computed(() =>
  data.value ? errorsTrendGranularityEchoIsConsistent(data.value, granularityWasSent.value) : true,
)

/** ★★ 两个 breakdown 都被截到 10 条 ⇒ 满 10 条时**不知道还剩多少**。 */
const breakdownTruncated = computed(() =>
  data.value ? errorsTrendBreakdownIsTruncated(data.value) : false,
)

function hoursLabel(n: number): string {
  return String(n)
}

function granularityText(g: string): string {
  return g
}

/** ★ 空 map 时**整个键省略**（文件头第 (4) 条）⇒ 只能说「本桶无该维度」，不能显示 `{}`。 */
function supplierDimText(p: ErrorsTrendPoint): string {
  if (!errorsTrendPointHasSupplierDim(p)) return t('et.dim.absent')
  return Object.entries(p.by_supplier!)
    .map(([k, v]) => `${k} ${v}`)
    .join(' / ')
}

function errorTypeDimText(p: ErrorsTrendPoint): string {
  if (!errorsTrendPointHasErrorTypeDim(p)) return t('et.dim.absent')
  return Object.entries(p.by_error_type!)
    .map(([k, v]) => `${k} ${v}`)
    .join(' / ')
}

/** ★ `'all'` 与 `''` 都是「不过滤」的合法写法，页面上要说清。 */
function filterText(v: string): string {
  return errorsTrendFilterIsWildcard(v) ? t('et.filter.wildcard') : v
}

onBeforeUnmount(() => {
  data.value = null
  trendError.value = null
})
</script>

<template>
  <div class="et">
    <!-- ══ 权限兜底段 ══ -->
    <section v-if="!allowed" class="et__sec" data-testid="et-nope">
      <header class="et__head">
        <h2 class="et__h">{{ t('et.title') }}</h2>
      </header>
      <!-- ★★ 这里不是后端 403，而是前端兜底：深链绕过了抽屉席的 requiresRole。 -->
      <p class="et__msg et__msg--err" data-testid="et-nope-msg">{{ t('et.nope') }}</p>
      <p class="et__msg" data-testid="et-nope-scope">{{ t('et.scope.note', { scope: scopeLabel }) }}</p>
    </section>

    <template v-else>
      <!-- ══ 查询段 ══ -->
      <section class="et__sec">
        <header class="et__head">
          <h2 class="et__h">{{ t('et.query.title') }}</h2>
          <button
            type="button"
            class="et__btn"
            :disabled="loading || !paramsOk"
            data-testid="et-load"
            @click="load"
          >
            {{ data ? t('et.reload') : t('et.load') }}
          </button>
        </header>

        <!-- ★★★ hours 是严格三值枚举，越界必然 400 invalid_hours -->
        <div class="et__row">
          <span class="et__label">{{ t('et.query.hours') }}</span>
          <button
            v-for="h in ERRORS_TREND_HOURS"
            :key="h"
            type="button"
            class="et__chip"
            :class="{ 'et__chip--on': hours === h }"
            :data-testid="'et-hours-' + h"
            @click="hours = h"
          >
            {{ hoursLabel(h) }}
          </button>
        </div>

        <!-- ★★ granularity 留空 = 不显式发，由后端按 hours 推导 -->
        <div class="et__row">
          <span class="et__label">{{ t('et.query.granularity') }}</span>
          <button
            type="button"
            class="et__chip"
            :class="{ 'et__chip--on': granularity === '' }"
            data-testid="et-gran-auto"
            @click="granularity = ''"
          >
            {{ t('et.query.auto', { g: autoGranularity }) }}
          </button>
          <button
            v-for="g in ERRORS_TREND_GRANULARITIES"
            :key="g"
            type="button"
            class="et__chip"
            :class="{ 'et__chip--on': granularity === g }"
            :data-testid="'et-gran-' + g"
            @click="granularity = g"
          >
            {{ granularityText(g) }}
          </button>
        </div>

        <div class="et__row">
          <label class="et__label">
            {{ t('et.query.supplier') }}
            <input v-model="supplier" type="text" class="et__input" data-testid="et-supplier" />
          </label>
          <label class="et__label">
            {{ t('et.query.errorType') }}
            <input v-model="errorType" type="text" class="et__input" data-testid="et-errorType" />
          </label>
        </div>

        <div class="et__row">
          <label class="et__label">
            {{ t('et.query.credentialId') }}
            <input
              v-model="credentialIdRaw"
              type="text"
              inputmode="numeric"
              class="et__input"
              data-testid="et-credentialId"
            />
          </label>
        </div>

        <!-- ★★ 四个过滤维度里三个接受 `'all'` 通配，credential_id 只认正整数 -->
        <p class="et__msg" data-testid="et-filter-note">{{ t('et.filter.note') }}</p>

        <p
          v-if="!paramsOk"
          class="et__msg et__msg--err"
          data-testid="et-params-bad"
        >
          {{ t('et.query.paramsBad') }}
        </p>

        <!-- ★★ 口径据实说明：这不是门，只是告诉用户这批数字覆盖多大范围 -->
        <p class="et__msg" data-testid="et-scope-note">{{ t('et.scope.note', { scope: scopeLabel }) }}</p>
        <p class="et__msg" data-testid="et-scope-legend">{{ t('et.scope.legend') }}</p>

        <p v-if="trendError" class="et__msg et__msg--err" data-testid="et-error">{{ trendError }}</p>
        <p v-if="loading" class="et__msg">{{ t('common.loading') }}</p>
      </section>

      <!-- ══ 响应段 ══ -->
      <template v-if="data">
        <section class="et__sec">
          <header class="et__head">
            <h2 class="et__h">{{ t('et.meta.title') }}</h2>
          </header>

          <dl class="et__kv">
            <div class="et__kv-row">
              <dt>{{ t('et.col.source') }}</dt>
              <dd data-testid="et-s-source">{{ data.source }}</dd>
            </div>
            <div class="et__kv-row">
              <dt>{{ t('et.col.granularity') }}</dt>
              <dd data-testid="et-s-granularity">{{ data.granularity }}</dd>
            </div>
            <div class="et__kv-row">
              <dt>{{ t('et.col.hours') }}</dt>
              <dd data-testid="et-s-hours">{{ data.hours }}</dd>
            </div>
            <div class="et__kv-row">
              <dt>{{ t('et.col.since') }}</dt>
              <dd data-testid="et-s-since">{{ data.since }}</dd>
            </div>
            <div class="et__kv-row">
              <dt>{{ t('et.col.until') }}</dt>
              <dd data-testid="et-s-until">{{ data.until }}</dd>
            </div>
          </dl>

          <!-- ★ 两档来源都在页面上明说：`stats` 是预聚合，`fallback` 是明细兜底 -->
          <p class="et__msg" data-testid="et-source-set">
            {{ t('et.meta.sourceSet', { list: ERRORS_TREND_SOURCES.join(' / ') }) }}
          </p>
          <p v-if="fellBack" class="et__msg et__msg--warn" data-testid="et-fallback">
            {{ t('et.meta.fallback') }}
          </p>
          <!-- ★★ 与上一条不同：fallback + 空序列才是「确定没有错误」 -->
          <p v-if="genuinelyEmpty" class="et__msg" data-testid="et-genuinely-empty">
            {{ t('et.meta.genuinelyEmpty') }}
          </p>
          <p v-if="!granularityEchoOk" class="et__msg et__msg--err" data-testid="et-gran-echo-bad">
            {{ t('et.meta.granEchoBad') }}
          </p>
        </section>

        <section class="et__sec">
          <header class="et__head">
            <h2 class="et__h">{{ t('et.summary.title') }}</h2>
          </header>

          <dl class="et__kv">
            <div class="et__kv-row">
              <dt>{{ t('et.col.totalErrors') }}</dt>
              <dd data-testid="et-sum-total_errors">{{ data.summary.total_errors }}</dd>
            </div>
            <div class="et__kv-row">
              <dt>{{ t('et.col.uniqueRequests') }}</dt>
              <dd>
                <span data-testid="et-sum-unique_requests">{{ data.summary.unique_requests }}</span>
                <!-- ★★ 这是桶求和，是**上界**，不是去重计数 -->
                <span class="et__muted" data-testid="et-sum-unique-note">{{ t('et.summary.uniqueNote') }}</span>
              </dd>
            </div>
            <div class="et__kv-row">
              <dt>{{ t('et.col.affectedCredentials') }}</dt>
              <dd data-testid="et-sum-affected_credentials">{{ data.summary.affected_credentials }}</dd>
            </div>
          </dl>

          <!-- ★ 三条可自验不变式，每条都在 .spec.ts 里配了正控 -->
          <p v-if="summaryMismatch" class="et__msg et__msg--err" data-testid="et-summary-mismatch">
            {{ t('et.summary.mismatch') }}
          </p>
          <p v-if="uniqueNotUpperBound" class="et__msg et__msg--err" data-testid="et-unique-not-upper">
            {{ t('et.summary.uniqueNotUpper') }}
          </p>

          <!-- ★★ 两个 breakdown 都被截到 10 条，满 10 时不知道还剩多少 -->
          <p v-if="breakdownTruncated" class="et__msg et__msg--warn" data-testid="et-breakdown-truncated">
            {{ t('et.summary.truncated', { n: ERRORS_TREND_BREAKDOWN_LIMIT }) }}
          </p>

          <div class="et__col-head">
            <span>{{ t('et.col.errorType') }}</span>
            <span>{{ t('et.col.count') }}</span>
          </div>
          <div
            v-for="r in data.summary.top_error_types"
            :key="'te-' + r.key"
            class="et__list-row"
            :data-testid="'et-te-' + r.key"
          >
            <span>{{ r.key }}</span>
            <span :data-testid="'et-te-' + r.key + '-count'">{{ r.count }}</span>
          </div>
          <!-- ★ 两个都是恒数组（显式初始化成 []），所以「空」是可以确定说出来的 -->
          <p
            v-if="data.summary.top_error_types.length === 0"
            class="et__msg"
            data-testid="et-te-empty"
          >
            {{ t('et.summary.emptyTypes') }}
          </p>

          <div class="et__col-head">
            <span>{{ t('et.col.supplier') }}</span>
            <span>{{ t('et.col.count') }}</span>
          </div>
          <div
            v-for="r in data.summary.top_suppliers"
            :key="'ts-' + r.key"
            class="et__list-row"
            :data-testid="'et-ts-' + r.key"
          >
            <span>{{ r.key }}</span>
            <span :data-testid="'et-ts-' + r.key + '-count'">{{ r.count }}</span>
          </div>
          <p v-if="data.summary.top_suppliers.length === 0" class="et__msg" data-testid="et-ts-empty">
            {{ t('et.summary.emptySuppliers') }}
          </p>
        </section>

        <section class="et__sec">
          <header class="et__head">
            <h2 class="et__h">{{ t('et.series.title') }}</h2>
          </header>

          <p v-if="notAscending" class="et__msg et__msg--err" data-testid="et-not-ascending">
            {{ t('et.series.notAscending') }}
          </p>

          <div class="et__col-head">
            <span>{{ t('et.col.timestamp') }}</span>
            <span>{{ t('et.col.errorCount') }}</span>
          </div>

          <article
            v-for="(p, i) in data.time_series"
            :key="p.timestamp + '#' + i"
            class="et__card"
            :data-testid="'et-p-' + i"
          >
            <div class="et__row">
              <span class="et__ts">{{ p.timestamp }}</span>
              <span class="et__badge" :data-testid="'et-p-' + i + '-error_count'">{{ p.error_count }}</span>
            </div>
            <dl class="et__kv">
              <div class="et__kv-row">
                <dt>{{ t('et.col.uniqueRequests') }}</dt>
                <dd :data-testid="'et-p-' + i + '-unique_requests'">{{ p.unique_requests }}</dd>
              </div>
              <div class="et__kv-row">
                <dt>{{ t('et.col.affectedUsers') }}</dt>
                <dd :data-testid="'et-p-' + i + '-affected_users'">{{ p.affected_users }}</dd>
              </div>
              <!-- ★★ 空 map 时**整个键被省略**（不是 `{}` / `null`）⇒ 这里只能写「缺失」 -->
              <div class="et__kv-row">
                <dt>{{ t('et.col.bySupplier') }}</dt>
                <dd :data-testid="'et-p-' + i + '-by_supplier'">{{ supplierDimText(p) }}</dd>
              </div>
              <div class="et__kv-row">
                <dt>{{ t('et.col.byErrorType') }}</dt>
                <dd :data-testid="'et-p-' + i + '-by_error_type'">{{ errorTypeDimText(p) }}</dd>
              </div>
            </dl>
          </article>

          <!-- ★★ 空序列 + 非 fallback ⇒ 是「0 行」这个被文档化的失败模式，不是「没有错误」 -->
          <p
            v-if="data.time_series.length === 0 && !fellBack"
            class="et__msg et__msg--warn"
            data-testid="et-zero-rows-ambiguous"
          >
            {{ t('et.series.zeroRows') }}
          </p>
          <p
            v-if="data.time_series.length === 0"
            class="et__msg"
            data-testid="et-series-empty"
          >
            {{ t('et.series.empty') }}
          </p>
        </section>

        <section class="et__sec">
          <header class="et__head">
            <h2 class="et__h">{{ t('et.applied.title') }}</h2>
          </header>
          <dl class="et__kv">
            <div class="et__kv-row">
              <dt>{{ t('et.col.supplier') }}</dt>
              <dd data-testid="et-applied-supplier">{{ filterText(supplier) }}</dd>
            </div>
            <div class="et__kv-row">
              <dt>{{ t('et.col.errorType') }}</dt>
              <dd data-testid="et-applied-errorType">{{ filterText(errorType) }}</dd>
            </div>
            <div class="et__kv-row">
              <dt>{{ t('et.col.credentialId') }}</dt>
              <!-- ★ 空 = 不过滤；发出去就一定是过滤（0 在后端只作内部哨兵） -->
              <dd data-testid="et-applied-credentialId">
                {{ credentialId === null
                  ? t('et.filter.unfiltered')
                  : credentialIdRaw.trim() }}
              </dd>
            </div>
          </dl>
        </section>
      </template>
    </template>
  </div>
</template>

<style scoped>
.et {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.et__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.et__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.et__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

.et__btn {
  min-height: 48px;
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

/* ★★ 触控控件同样受 R1（≥48 CSS px）约束，判据会解析出来比大小 */
.et__chip {
  min-height: 48px;
  min-width: 48px;
  padding: 0 14px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

/* 选中态照 `ComplianceHitsView` 的 `.ch__chip--on` 定式 —— 用 `--app-primary`，
   本仓没有 `--app-accent`（theme.spec.ts 的未定义 token 棘轮会拦）。 */
.et__chip--on {
  color: var(--app-primary);
  border-color: var(--app-primary);
  font-weight: 600;
}

.et__input {
  min-height: 48px;
  padding: 0 12px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.et__label {
  display: flex;
  flex-direction: column;
  gap: 2px;
  flex: 1 1 140px;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.et__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: center;
}

.et__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.et__msg--err {
  color: var(--app-danger);
}

.et__msg--warn {
  color: var(--app-warning, var(--app-danger));
}

.et__muted {
  margin-left: var(--app-space-2);
  font-size: 0.6875rem;
  color: var(--app-text-muted);
}

.et__kv {
  margin: 0;
}

.et__kv-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
}

.et__kv-row dt {
  color: var(--app-text-muted);
}

.et__kv-row dd {
  margin: 0;
  color: var(--app-text);
  word-break: break-all;
  text-align: right;
}

.et__col-head {
  display: flex;
  justify-content: space-between;
  font-size: 0.6875rem;
  color: var(--app-text-muted);
  border-bottom: 1px solid var(--app-border);
  padding-bottom: 2px;
}

.et__list-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 4px 0;
  font-size: 0.8125rem;
  color: var(--app-text);
}

.et__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.et__ts {
  font-size: 0.6875rem;
  color: var(--app-text-muted);
  font-variant-numeric: tabular-nums;
}

.et__badge {
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--app-danger);
  font-variant-numeric: tabular-nums;
}
</style>
