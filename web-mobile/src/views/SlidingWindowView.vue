<script setup lang="ts">
// SlidingWindowView — 凭据模型的滑窗调用明细（2026-10-14，第一百一十五批）。
//
// GET /api/credentials/sliding-window?credential_id=X&model=Y&minutes=60&limit=50
//
// ⚠️⚠️⚠️⚠️ 权限档：**`requiresRole: 'super_admin'`** —— 与批 113/114 连续两批**相反**。
//   注册处确实是 admin 档（`admin/handler.go:1528` `h.admin`）。
//   ★★ **本文件头原写的那条「admin 档连管理员隔离都做不到」已被后端补洞覆盖**
//   （audit #153 + 12h-audit 20261017，2026-10-16/17）：
//   `handleSlidingWindow`（`admin/credential_monitor.go:753-863`）今天
//   **有** `tenantID` / `IsTenantAdmin` / `GetTenantID`，两条 SQL 腿都带
//   `AND ($5 = '' OR tenant_id = $5)`（`:905` / `:950`），并有角色门
//   （`:765-768`，`role=user` ⇒ 403）；Redis 快腿对 tenant_admin 直接跳过
//   （`:809`，Redis 键没有 tenant tag）。
//   ⇒ 所以 `'admin'` 档**现在**是安全的；本席仍写 `'super_admin'` 是
//     **过度限制**（收紧一档不必跟着放开），**不是漏洞**。
//   ⚠️ 前端严于后端是兜底不是修复 —— 后端那条路本来就开着。
//   ⚠️ 抽屉隐藏 ≠ 路由拦截（`meta.requiresRole` 全仓无人读）⇒ 视图内还要兜一层。
//
// ★★★★★ 本批同时修掉一个**真缺陷**（见 `api/slidingWindow.ts` 的
//   `SLIDING_WINDOW_SOURCES` 处）：`source` 的回退取值被写成 `'request_logs'`，
//   而后端发的是 **`'_request_logs'`** ⇒ 回退信号判据对真实响应**恒返回 false**。
//   ⇒ 那个错是从**模块头抄漏了一行 Go** 开始，连带常量、实现、3 条用例一起歪，
//     三方互相印证、整套件全绿。本批补了「对着 Go 源码钉」的判据组 (15)。
// ★★★ **2026-10-26 第二轮（同一族、同一处、同样三方互证地错）**：后端在
//   **v1 → session_turns 替换轮**（2026-10-24）把值换成 **`"session_turns"`**，
//   **常量 / 实现 / 用例都没跟着改** ⇒ 回退信号判据又对真实响应恒 false。
//
// ★★★★★ 本族最要紧的六件事（文件头共 14 条）：
//   1. ★★★★★ **四个数字完全互锁**：`total_returned === entries.length ===
//      stats.total`，且 `success + failed === total`。
//   2. ★★★★★ **`error_kinds` 求和「≤」`failed` 而不是相等** —— 失败条目若
//      `err` 是空串就完全不进 `error_kinds`。
//   3. ★★★★★ **`failure_rate` 是派生的，`total === 0` 时恒为 0**（不是 NaN/null）。
//   4. ★★★★★ `source === 'redis'` ⟹ `entries.length > 0`；
//      `'session_turns'` 是**二义的**（Redis 不可用 / 窗口内无数据，同形）。
//   5. ★★★★ **Redis 的错误被整个丢弃**（`entries, _ = …`）⇒ Redis 挂了静默回退，
//      响应里看不出 Redis 故障。
//   6. ★★★★ **`err` 是唯一带 omitempty 的键** ⇒ 成功条目里**键消失**；
//      `rid` 恒在但**可能为空串**；`ts` 是 **unix 毫秒**不是 ISO 串。
import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchSlidingWindow,
  slidingWindowRoleIsPrivileged,
  slidingWindowTenantScopeLabel,
  slidingWindowParamsAreAcceptable,
  slidingWindowTotalMatchesEntries,
  slidingWindowStatsTotalMatchesEntries,
  slidingWindowSuccessPlusFailedEqualsTotal,
  slidingWindowErrorKindSum,
  slidingWindowErrorKindsAtMostFailed,
  slidingWindowErrorKindsCoverEveryFailure,
  slidingWindowFailureRateMatches,
  slidingWindowRedisSourceImpliesEntries,
  slidingWindowFallbackSourceStillHasEntries,
  slidingWindowEntriesAreNonAscending,
  SLIDING_WINDOW_DEFAULT_MINUTES,
  SLIDING_WINDOW_DEFAULT_LIMIT,
  SLIDING_WINDOW_LIMIT_MIN,
  SLIDING_WINDOW_LIMIT_MAX,
  SLIDING_WINDOW_SOURCES,
  type SlidingWindowResponse,
  type CallEntry,
} from '@/api/slidingWindow'

useHyperPage({ title: () => (allowed.value ? t('sw.title') : t('common.pageDenied')) })

const auth = useAuthStore()

// ══ 权限兜底 ══════════════════════════════════════════════════════════════
// ★★★ 本族的判定只有「特权 / 非特权」两档，**没有「仅本租户」**。

const role = computed(() => auth.role)
const allowed = computed(() => slidingWindowRoleIsPrivileged(role.value))
const scopeLabel = computed(() => slidingWindowTenantScopeLabel(role.value))

// ══ 查询条件 ══════════════════════════════════════════════════════════════

const credentialIdRaw = ref('')
const model = ref('')
const minutesRaw = ref(String(SLIDING_WINDOW_DEFAULT_MINUTES))
const limitRaw = ref(String(SLIDING_WINDOW_DEFAULT_LIMIT))

const credentialId = computed(() => {
  const raw = credentialIdRaw.value.trim()
  return raw === '' ? Number.NaN : Number(raw)
})

const limit = computed(() => {
  const raw = limitRaw.value.trim()
  return raw === '' ? Number.NaN : Number(raw)
})

/** ★★ `minutes` 后端**不校验**，所以这里**没有**「会被 400」那一格可挡。 */
const minutes = computed(() => {
  const raw = minutesRaw.value.trim()
  return raw === '' ? Number.NaN : Number(raw)
})

const paramsOk = computed(() =>
  slidingWindowParamsAreAcceptable({
    credentialId: credentialId.value,
    model: model.value,
    limit: limit.value,
  }),
)

const data = ref<SlidingWindowResponse | null>(null)
const winError = ref<string | null>(null)
const loading = ref(false)

async function load(): Promise<void> {
  // ★ 兜底：不允许访问时**连请求都不发**
  if (!allowed.value) return
  loading.value = true
  winError.value = null
  try {
    data.value = await fetchSlidingWindow({
      credentialId: credentialId.value,
      model: model.value,
      minutes: minutes.value,
      limit: limit.value,
    })
  } catch (err) {
    data.value = null
    winError.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}

// ══ 判读 ══════════════════════════════════════════════════════════════════

/** ★ `error_kinds` 是 `Record<string, number>`（**不是数组**）⇒ 用键数判断空。 */
const errorKindCount = computed(() =>
  data.value ? Object.keys(data.value.stats.error_kinds).length : 0,
)

/** ★★ (3) 四个数字锁在一起，任何一个错都会被另外三个抓住。 */
const totalBad = computed(() => (data.value ? !slidingWindowTotalMatchesEntries(data.value) : false))
const statsTotalBad = computed(() =>
  data.value ? !slidingWindowStatsTotalMatchesEntries(data.value) : false,
)
const sumBad = computed(() => (data.value ? !slidingWindowSuccessPlusFailedEqualsTotal(data.value) : false))

/** ★★★★ (4) 求和「≤」failed —— 大于才是错。 */
const kindsBad = computed(() => (data.value ? !slidingWindowErrorKindsAtMostFailed(data.value.stats) : false))
/** ★★ 等号不成立时**不是 bug**，是那些失败没有归类 ⇒ 只说明，不报警。 */
const kindsIncomplete = computed(() =>
  data.value
    ? !slidingWindowErrorKindsCoverEveryFailure(data.value.entries)
    : false,
)

/** ★★★★★ (5) `failure_rate` 与 total/failed 对不上才报。 */
const rateBad = computed(() => (data.value ? !slidingWindowFailureRateMatches(data.value.stats) : false))

/** ★★ (6) `redis` 源却零行 ⇒ 违反不变式（后端必然回退）。 */
const redisEmptyBad = computed(() =>
  data.value ? !slidingWindowRedisSourceImpliesEntries(data.value) : false,
)
/** ★★ `'session_turns'` 有数据是**正常**的两种成因之一，不是错误。 */
const fellBackWithData = computed(() =>
  data.value ? slidingWindowFallbackSourceStillHasEntries(data.value) : false,
)

/** ★★ (14) 无 tiebreak ⇒ 只断言非升序。 */
const notNonAscending = computed(() =>
  data.value ? !slidingWindowEntriesAreNonAscending(data.value.entries) : false,
)

/** ★ (11) `model` 原样回显，**不是**规范化后的实际命中值。 */
const modelEchoBad = computed(() => (data.value ? data.value.model !== model.value : false))
const credentialEchoBad = computed(() =>
  data.value ? data.value.credential_id !== credentialId.value : false,
)

/** ★★ (13) `err` 是唯一带 omitempty 的键 —— 成功条目里**键不存在**。 */
function errText(e: CallEntry): string {
  if (e.ok) return t('sw.field.noError')
  if (e.err === undefined) return t('sw.field.blankError')
  return e.err === '' ? t('sw.field.blankError') : e.err
}

/** ★★ `rid` 恒在但**可能为空串**（`COALESCE(request_id,'')`）⇒ 空串要单独说。 */
function ridText(e: CallEntry): string {
  return e.rid === '' ? t('sw.field.blankRid') : e.rid
}

/** ★ `ts` 是 unix 毫秒。 */
function tsText(e: CallEntry): string {
  return String(e.ts)
}

onBeforeUnmount(() => {
  data.value = null
  winError.value = null
})
</script>

<template>
  <div class="sw">
    <!-- ══ 权限兜底段 ══ -->
    <section v-if="!allowed" class="sw__sec" data-testid="sw-nope">
      <header class="sw__head">
        <h2 class="sw__h">{{ t('sw.title') }}</h2>
      </header>
      <!-- ★★ 这里没有 403 可显示 —— 后端对任何通过认证的主体都返 200。 -->
      <p class="sw__msg sw__msg--err" data-testid="sw-nope-msg">{{ t('sw.nope.msg') }}</p>
      <p class="sw__msg" data-testid="sw-nope-scope">{{ t('sw.scope.note', { scope: scopeLabel }) }}</p>
      <p class="sw__msg" data-testid="sw-scope-legend">{{ t('sw.scope.legend') }}</p>
      <!-- ★★ 明确写出「本族没有为这个角色做过隔离」，别让用户以为是自己没配好 -->
      <p class="sw__msg sw__msg--warn" data-testid="sw-nope-why">{{ t('sw.nope.why') }}</p>
    </section>

    <template v-else>
      <!-- ══ 查询段 ══ -->
      <section class="sw__sec">
        <header class="sw__head">
          <h2 class="sw__h">{{ t('sw.query.title') }}</h2>
          <button
            type="button"
            class="sw__btn"
            :disabled="loading || !paramsOk"
            data-testid="sw-load"
            @click="load"
          >
            {{ data ? t('sw.reload') : t('sw.load') }}
          </button>
        </header>

        <div class="sw__row">
          <label class="sw__label">
            {{ t('sw.query.credentialId') }}
            <input
              v-model="credentialIdRaw"
              type="text"
              inputmode="numeric"
              class="sw__input"
              data-testid="sw-credentialId"
            />
          </label>
          <label class="sw__label">
            {{ t('sw.query.model') }}
            <input v-model="model" type="text" class="sw__input" data-testid="sw-model" />
          </label>
        </div>

        <div class="sw__row">
          <label class="sw__label">
            {{ t('sw.query.minutes', { def: SLIDING_WINDOW_DEFAULT_MINUTES }) }}
            <input v-model="minutesRaw" type="text" inputmode="numeric" class="sw__input" data-testid="sw-minutes" />
          </label>
          <label class="sw__label">
            {{ t('sw.query.limit', { min: SLIDING_WINDOW_LIMIT_MIN, max: SLIDING_WINDOW_LIMIT_MAX, def: SLIDING_WINDOW_DEFAULT_LIMIT }) }}
            <input v-model="limitRaw" type="text" inputmode="numeric" class="sw__input" data-testid="sw-limit" />
          </label>
        </div>

        <!-- ★★ 同族两个参数一个校验一个不校验，这句要说清 -->
        <p class="sw__msg" data-testid="sw-param-note">{{ t('sw.query.note') }}</p>

        <p v-if="!paramsOk" class="sw__msg sw__msg--err" data-testid="sw-params-bad">
          {{ t('sw.query.paramsBad', { min: SLIDING_WINDOW_LIMIT_MIN, max: SLIDING_WINDOW_LIMIT_MAX }) }}
        </p>

        <!-- ★★ 口径据实说明；这不是门，后端对任何角色都返回全量 -->
        <p class="sw__msg" data-testid="sw-scope-note">{{ t('sw.scope.note', { scope: scopeLabel }) }}</p>
        <p class="sw__msg" data-testid="sw-scope-legend">{{ t('sw.scope.legend') }}</p>

        <p v-if="winError" class="sw__msg sw__msg--err" data-testid="sw-error">{{ winError }}</p>
        <p v-if="loading" class="sw__msg">{{ t('common.loading') }}</p>
      </section>

      <!-- ══ 响应段 ══ -->
      <template v-if="data">
        <section class="sw__sec">
          <header class="sw__head">
            <h2 class="sw__h">{{ t('sw.meta.title') }}</h2>
          </header>

          <dl class="sw__kv">
            <div class="sw__kv-row">
              <dt>{{ t('sw.query.credentialId') }}</dt>
              <dd data-testid="sw-s-credential_id">{{ data.credential_id }}</dd>
            </div>
            <div class="sw__kv-row">
              <dt>{{ t('sw.query.model') }}</dt>
              <dd data-testid="sw-s-model">{{ data.model }}</dd>
            </div>
            <div class="sw__kv-row">
              <dt>{{ t('sw.col.windowMinutes') }}</dt>
              <dd data-testid="sw-s-window_minutes">{{ data.window_minutes }}</dd>
            </div>
            <div class="sw__kv-row">
              <dt>{{ t('sw.col.limit') }}</dt>
              <dd data-testid="sw-s-limit">{{ data.limit }}</dd>
            </div>
            <div class="sw__kv-row">
              <dt>{{ t('sw.col.source') }}</dt>
              <dd data-testid="sw-s-source">{{ data.source }}</dd>
            </div>
            <div class="sw__kv-row">
              <dt>{{ t('sw.col.totalReturned') }}</dt>
              <dd data-testid="sw-s-total_returned">{{ data.total_returned }}</dd>
            </div>
          </dl>

          <p v-if="credentialEchoBad" class="sw__msg sw__msg--err" data-testid="sw-credential-echo-bad">
            {{ t('sw.meta.echoBad') }}
          </p>
          <!-- ★★ 回显是**原样**传进来的那个，不是规范化后实际命中的那个 -->
          <p v-if="modelEchoBad" class="sw__msg sw__msg--err" data-testid="sw-model-echo-bad">
            {{ t('sw.meta.modelEchoBad') }}
          </p>

          <p class="sw__msg" data-testid="sw-source-set">
            {{ t('sw.meta.sourceSet', { list: SLIDING_WINDOW_SOURCES.join(' / ') }) }}
          </p>
          <!-- ★★ Redis 挂了会静默回退，响应里看不出 Redis 故障 -->
          <p class="sw__msg sw__msg--warn" data-testid="sw-redis-note">{{ t('sw.meta.redisNote') }}</p>
          <p v-if="fellBackWithData" class="sw__msg" data-testid="sw-fallback-data">
            {{ t('sw.meta.fallbackData') }}
          </p>
          <p v-if="redisEmptyBad" class="sw__msg sw__msg--err" data-testid="sw-redis-empty-bad">
            {{ t('sw.meta.redisEmptyBad') }}
          </p>
        </section>

        <section class="sw__sec">
          <header class="sw__head">
            <h2 class="sw__h">{{ t('sw.stats.title') }}</h2>
          </header>

          <dl class="sw__kv">
            <div class="sw__kv-row">
              <dt>{{ t('sw.col.total') }}</dt>
              <dd data-testid="sw-st-total">{{ data.stats.total }}</dd>
            </div>
            <div class="sw__kv-row">
              <dt>{{ t('sw.col.success') }}</dt>
              <dd data-testid="sw-st-success">{{ data.stats.success }}</dd>
            </div>
            <div class="sw__kv-row">
              <dt>{{ t('sw.col.failed') }}</dt>
              <dd data-testid="sw-st-failed">{{ data.stats.failed }}</dd>
            </div>
            <div class="sw__kv-row">
              <dt>{{ t('sw.col.failureRate') }}</dt>
              <dd data-testid="sw-st-failure_rate">{{ data.stats.failure_rate }}</dd>
            </div>
            <div class="sw__kv-row">
              <dt>{{ t('sw.col.errorKindsSum') }}</dt>
              <dd data-testid="sw-st-error_kinds_sum">{{ slidingWindowErrorKindSum(data.stats) }}</dd>
            </div>
          </dl>

          <!-- ★★ 四个数字互锁的三条告警 -->
          <p v-if="totalBad" class="sw__msg sw__msg--err" data-testid="sw-total-bad">
            {{ t('sw.stats.totalBad') }}
          </p>
          <p v-if="statsTotalBad" class="sw__msg sw__msg--err" data-testid="sw-stats-total-bad">
            {{ t('sw.stats.statsTotalBad') }}
          </p>
          <p v-if="sumBad" class="sw__msg sw__msg--err" data-testid="sw-sum-bad">
            {{ t('sw.stats.sumBad') }}
          </p>
          <p v-if="kindsBad" class="sw__msg sw__msg--err" data-testid="sw-kinds-bad">
            {{ t('sw.stats.kindsBad') }}
          </p>
          <!-- ★★ 这个**不是错**：失败的条目若 err 为空串就不进 error_kinds -->
          <p v-if="kindsIncomplete" class="sw__msg" data-testid="sw-kinds-incomplete">
            {{ t('sw.stats.kindsIncomplete') }}
          </p>
          <p v-if="rateBad" class="sw__msg sw__msg--err" data-testid="sw-rate-bad">
            {{ t('sw.stats.rateBad') }}
          </p>

          <div class="sw__col-head">
            <span>{{ t('sw.col.errorKind') }}</span>
            <span>{{ t('sw.col.count') }}</span>
          </div>
          <div
            v-for="(n, kind) in data.stats.error_kinds"
            :key="kind"
            class="sw__list-row"
            :data-testid="'sw-ek-' + kind"
          >
            <span>{{ kind }}</span>
            <span :data-testid="'sw-ek-' + kind + '-n'">{{ n }}</span>
          </div>
          <!-- ★ 空对象是可以确定说出来的（不是「还没加载」） -->
          <p v-if="errorKindCount === 0" class="sw__msg" data-testid="sw-ek-empty">
            {{ t('sw.stats.kindsEmpty') }}
          </p>
        </section>

        <section class="sw__sec">
          <header class="sw__head">
            <h2 class="sw__h">{{ t('sw.entries.title') }}</h2>
          </header>

          <p v-if="notNonAscending" class="sw__msg sw__msg--err" data-testid="sw-order-bad">
            {{ t('sw.entries.orderBad') }}
          </p>
          <p class="sw__msg" data-testid="sw-order-note">{{ t('sw.entries.orderNote') }}</p>

          <!-- ★ (8) entries 恒为数组，空时是 [] 不是 null -->
          <p v-if="data.entries.length === 0" class="sw__msg" data-testid="sw-entries-empty">
            {{ t('sw.entries.empty') }}
          </p>

          <div v-else class="sw__col-head">
            <span>{{ t('sw.col.ts') }}</span>
            <span>{{ t('sw.col.ok') }}</span>
          </div>

          <article
            v-for="(e, i) in data.entries"
            :key="e.ts + '#' + i"
            class="sw__card"
            :data-testid="'sw-e-' + i"
          >
            <div class="sw__row">
              <span class="sw__ts" :data-testid="'sw-e-' + i + '-ts'">{{ tsText(e) }}</span>
              <span class="sw__badge" :data-testid="'sw-e-' + i + '-ok'">
                {{ e.ok ? t('sw.col.okYes') : t('sw.col.okNo') }}
              </span>
            </div>
            <dl class="sw__kv">
              <div class="sw__kv-row">
                <dt>rid</dt>
                <dd :data-testid="'sw-e-' + i + '-rid'">{{ ridText(e) }}</dd>
              </div>
              <div class="sw__kv-row">
                <dt>lat</dt>
                <dd :data-testid="'sw-e-' + i + '-lat'">{{ e.lat }}</dd>
              </div>
              <!-- ★★ 成功条目的 err 键**根本不存在**（唯一带 omitempty 的键） -->
              <div class="sw__kv-row">
                <dt>err</dt>
                <dd :data-testid="'sw-e-' + i + '-err'">{{ errText(e) }}</dd>
              </div>
            </dl>
          </article>
        </section>
      </template>
    </template>
  </div>
</template>

<style scoped>
.sw {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.sw__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.sw__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.sw__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

.sw__btn {
  min-height: 48px;
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.sw__input {
  min-height: 48px;
  padding: 0 12px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.sw__label {
  display: flex;
  flex-direction: column;
  gap: 2px;
  flex: 1 1 160px;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.sw__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: center;
}

.sw__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.sw__msg--err {
  color: var(--app-danger);
}

.sw__msg--warn {
  color: var(--app-warning, var(--app-danger));
}

.sw__kv {
  margin: 0;
}

.sw__kv-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
}

.sw__kv-row dt {
  color: var(--app-text-muted);
}

.sw__kv-row dd {
  margin: 0;
  color: var(--app-text);
  word-break: break-all;
  text-align: right;
}

.sw__col-head {
  display: flex;
  justify-content: space-between;
  font-size: 0.6875rem;
  color: var(--app-text-muted);
  border-bottom: 1px solid var(--app-border);
  padding-bottom: 2px;
}

.sw__list-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 4px 0;
  font-size: 0.8125rem;
  color: var(--app-text);
}

.sw__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.sw__ts {
  font-size: 0.6875rem;
  color: var(--app-text-muted);
  font-variant-numeric: tabular-nums;
}

.sw__badge {
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--app-primary);
  font-variant-numeric: tabular-nums;
}
</style>