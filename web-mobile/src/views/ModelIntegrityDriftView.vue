<script setup lang="ts">
// ModelIntegrityDriftView — 指纹漂移事件（2026-10-14，第一百一十批）。
//
// GET /api/admin/model-integrity/fingerprint-drift?days=7&limit=200
//
// 权限：★★ **superAdmin 档**（`admin/handler.go:942-943` 两条都是 `h.superAdmin`）
//   ⇒ 抽屉席**须设** `requiresRole: 'super_admin'`，
//     并同步 `src/components/shell/AppDrawer.spec.ts` 的排序白名单。
//
// ★★★ 本族的租户口径：`:328` 用 `withAllTenantReadOnlyTx`，SQL **选了** `tenant_id`
//   扫进 `item.TenantID`，但 WHERE 只有 `anomaly_type = 'fingerprint_drift'` 与时间窗，
//   **没有任何租户条件**（契约头 (3)）。
//   ⇒ 它与批 104 operational / 批 106 selfCheck / 批 77 routing / 批 109 formatAnomalies
//     属于**同一族「读路径不隔离」**。
//   ★★ 与批 104 / 批 106 的处置**仍然相反**：那两族是 admin 档 + 不隔离
//     ⇒ tenant_admin 能读到跨租户 ⇒ 必须在视图内部按段落门控；
//     本族是 superAdmin 档 ⇒ **档位本身就挡住了**
//     ⇒ 抽屉席设 requiresRole 即是完整处置，**不需要**任何段落级门控。
//   ⇒ 所以本页**没有**角色分支 —— 这是刻意的，不是漏写。
//   ⚠️ 注释里**故意不写「第几次不隔离」**：全局序号在本仓已经腐烂过
//     （api 模块头写「第五次」而批 109 写「第六次」，谁也复核不了），要指认就**按名字列**。
//
// ★★★★ 与第一百零九批（同一大族、同一个超级管理员、同两张表）的关键差异：
//   1. ★★★★★ **本表 `severity` 的表级缺省是 `low`，另一张表是 `medium`**
//      （`462_model_integrity_events.sql:52` vs `454_response_format_anomalies.sql`）
//      ⇒ 「缺省」这件事**绝不能跨表照抄**，判据也不能。
//   2. ★★★★★ **`days` / `limit` 是静默回落，不是 400**：
//      `days <= 0 || days > 30 ⇒ 7`、`limit <= 0 || limit > 500 ⇒ 200`，
//      且 `queryInt` 解析失败直接取缺省 ⇒ 越界值与非法值**都拿到缺省的数据**。
//   3. ★★★★ `days` 回显的是**生效窗口**，不是请求值（与批 76 errors/trend 回显请求值相反）
//      ⇒ 页面拿 `resp.days` 当窗口长度，另用 `driftDaysWasRewritten` 报告被改写。
//   4. ★★★★ `count` **恒等于** `events.length`（`:385` 写的是 `len(items)`），
//      且**响应不回显 limit** ⇒ 截断只能靠「拿满请求上限」反推。
//   5. ★★★★ `context` 走 `jsoncol.Decode`：列是 NULL、列是 JSON `null`、
//      **以及列里的 JSON 解析失败**（`jsoncol.go:57-75`，失败只留 Warn、dst 不动）
//      ——**三种情况都让键被省略** ⇒ ★★ 「键缺失」**不能**证明「没有上下文」。
//   6. ★★★ `provider_id` / `credential_id` 是**指针** + omitempty
//      ⇒ 键在时值可能是真实的 **0**；真值判断会把 0 误判成「没有」。
//   7. ★★★ `resolved` 与 `resolved_at` / `resolution_notes` 之间**没有 DB 约束**
//      ⇒ 两个方向都不能单独断言，判据取并集。
//   8. ★★ `anomaly_type` 是 SQL 里的硬编码常量 ⇒ 每行必然是 `fingerprint_drift`。
//   9. ★★ 排序是 `ORDER BY ts DESC` ⇒ `detected_at` 非增。
//  10. ★★ 桌面 `ModelIntegrityView.vue:597` 的 days 输入框允许 **1..90**，
//      而后端上限是 **30** ⇒ 桌面把 31..90 静默回落成了 7。移动端只给 1..30。
import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchModelIntegrityFingerprintDrift,
  driftDaysIsSendable,
  driftLimitIsSendable,
  driftCountMatchesEvents,
  modelIntegrityDriftAtCap,
  modelIntegrityDriftRoleIsAllowed,
  driftDaysWasRewritten,
  driftHasNumericValue,
  driftNumericValueIsZero,
  driftHasContextKey,
  driftContextIsEmpty,
  driftHasFullResolution,
  driftIsUnresolved,
  driftSeverityIsKnown,
  driftIsNewestFirst,
  driftIsEmptyWindow,
  driftHasForeignTenants,
  driftHasNoTenantTags,
  DRIFT_DAYS_DEFAULT,
  DRIFT_DAYS_MAX,
  DRIFT_LIMIT_DEFAULT,
  DRIFT_LIMIT_MAX,
  DRIFT_SEVERITY_TABLE_DEFAULT,
  type ModelIntegrityDriftRecord,
  type ModelIntegrityDriftResponse,
} from '@/api/modelIntegrityDrift'

useHyperPage({ title: () => (allowed.value ? t('mid.title') : t('common.pageDenied')) })
// 批 150：补视图内兜底。`modelIntegrityDriftRoleIsAllowed` 与 `appNav.ts:229` 的
// `requiresRole: 'super_admin'` 同源；壳层 `hyper-wiring.ts` 已在路由层消费同样的
// `requiresRole` ⇒ 正常导航下 `allowed` 永远 true，本兜底只在路由被绕过时生效。
const auth = useAuthStore()
const role = computed(() => auth.role)
const allowed = computed(() => modelIntegrityDriftRoleIsAllowed(role.value))

// ══ 查询条件 ════════════════════════════════════════════════════════════

// ★★ 只给**发得出去**的档位。越界值后端会静默回落成缺省（不报错），
//   给了等于让用户以为「我查的是 45 天」而实际拿到 7 天的数据。
//   ⚠️ 桌面那个 1..90 的输入框正是这个坑的实例（本页不复制它）。
const DAYS_CHOICES = [1, DRIFT_DAYS_DEFAULT, 14, DRIFT_DAYS_MAX] as const
const days = ref<number>(DRIFT_DAYS_DEFAULT)

const LIMIT_CHOICES = [50, DRIFT_LIMIT_DEFAULT, DRIFT_LIMIT_MAX] as const
const limit = ref<number>(DRIFT_LIMIT_DEFAULT)

const drift = ref<ModelIntegrityDriftResponse | null>(null)
const error = ref<string | null>(null)
const loading = ref(false)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    // ★ 两个守卫保证只发合法值；越界/非法一律不发，交给后端静默回落。
    drift.value = await fetchModelIntegrityFingerprintDrift({
      ...(driftDaysIsSendable(days.value) ? { days: days.value } : {}),
      ...(driftLimitIsSendable(limit.value) ? { limit: limit.value } : {}),
    })
  } catch (err) {
    drift.value = null
    error.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}

// ══ 载荷判读 ════════════════════════════════════════════════════════════

/** ★★ `count` 恒等于本页行数；不等就是载荷被换过。 */
const countMatches = computed(() => (drift.value ? driftCountMatchesEvents(drift.value) : true))
const countViolation = computed(() => drift.value !== null && !countMatches.value)

/** ★★ 响应**不回显 limit** ⇒ 只能靠「拿满请求上限」反推可能被截断。 */
const atCap = computed(() => (drift.value ? modelIntegrityDriftAtCap(drift.value, limit.value) : false))

/** ★★ `days` 回显生效窗口；请求值被回落的次数就是「窗口被改写」。 */
const daysRewritten = computed(() => (drift.value ? driftDaysWasRewritten(drift.value, days.value) : false))

/** ★ `ORDER BY ts DESC` ⇒ detected_at 非增。 */
const orderOk = computed(() => (drift.value ? driftIsNewestFirst(drift.value) : true))

const emptyWindow = computed(() => (drift.value ? driftIsEmptyWindow(drift.value) : false))

/** ★★★ 跨租户信号（superAdmin 档已挡住调用方，但超管**确实**在看全部租户的数据）。 */
const foreignTenants = computed(() => (drift.value ? driftHasForeignTenants(drift.value) : false))
const noTenantTags = computed(() => (drift.value ? driftHasNoTenantTags(drift.value) : false))

// ══ 行判读 ══════════════════════════════════════════════════════════════

/**
 * ★★ `request_id` 是**条件键**（`*string` + omitempty）：
 *   键缺失 = DB 里是 NULL；键在而为空串 = DB 里真的存了 `''`。**两件不同的事。**
 */
function requestIdText(r: ModelIntegrityDriftRecord): string {
  if (r.request_id === undefined) return t('mid.row.absent')
  if (r.request_id === '') return t('mid.row.requestIdEmpty')
  return r.request_id
}

/** ★ 条件键缺失就是「这一列是 NULL」。 */
function optionalText(v: string | undefined): string {
  return v === undefined ? t('mid.row.absent') : v === '' ? t('mid.row.emptyString') : v
}

/**
 * ★★ 三个数字型条件键：键缺 = NULL，而 0 是**真的 0**（指针指向 0，键仍在）。
 *   ⇒ 必须用模块的具名判据 `driftHasNumericValue` / `driftNumericValueIsZero(row, key)`，
 *     它们收的是**行 + 键名**；自己写 `if (row[key])` 会把真实的 0 判成「没有值」。
 */
function numericText(r: ModelIntegrityDriftRecord, key: 'provider_id' | 'credential_id'): string {
  if (!driftHasNumericValue(r, key)) return t('mid.row.nullValue')
  const v = r[key] as number
  if (driftNumericValueIsZero(r, key)) return `0 ${t('mid.row.idSuffix')}`
  return `${v} ${t('mid.row.idSuffix')}`
}

/** ★ `severity` 四值域**只是注释里声明的**，表上没有 CHECK ⇒ 未知值要标出来但不是错误。 */
function severityText(r: ModelIntegrityDriftRecord): string {
  const base = driftSeverityIsKnown(r)
    ? r.severity
    : `${r.severity} ★ ${t('mid.sev.unknown')}`
  return r.severity === DRIFT_SEVERITY_TABLE_DEFAULT ? `${base} ${t('mid.sev.default')}` : base
}

/**
 * ★★★ `context` 键缺失**不能**证明「没有上下文」：`jsoncol.Decode` 有三条路让键消失 ——
 *   列是 NULL、列是 JSON `null`、**列里的 JSON 解析失败**（只留 Warn、dst 不动）。
 * ⇒ 键缺一律说「无法判定」，键在且为空才说「空」。
 */
function contextText(r: ModelIntegrityDriftRecord): string {
  if (!driftHasContextKey(r)) return t('mid.row.contextUnknown')
  return driftContextIsEmpty(r) ? t('mid.row.contextEmpty') : t('mid.row.contextPresent')
}

/**
 * ★ `resolved` 与两个痕迹键之间**没有任何 DB 约束** ⇒ 四个格子必须分开说，
 *   合成两档会把「标记已解决但什么都没留下」和「真有痕迹」混成一句。
 *   ★★ 第一版只分了三档（并集判据 `driftHasResolutionTrace` 对 `resolved === true`
 *     单独成立）⇒ `resolved=true` 且两个痕迹键都缺时被说成「有处置痕迹」，
 *     而那恰恰是最该被指出来的格子。本批判据 ⑧-2 当场把它咬了出来。
 */
function traceText(r: ModelIntegrityDriftRecord): string {
  const hasAt = 'resolved_at' in r
  const hasNotes = 'resolution_notes' in r
  if (driftIsUnresolved(r)) return t('mid.row.unresolved')
  if (driftHasFullResolution(r)) return t('mid.row.fullTrace')
  if (r.resolved === true && !hasAt && !hasNotes) return t('mid.row.traceMismatch')
  return t('mid.row.partialTrace')
}

onBeforeUnmount(() => {
  drift.value = null
  error.value = null
})
</script>

<template>
  <p v-if="!allowed" data-testid="mid-denied" role="alert">{{ t('common.pageDenied') }}</p>
  <div v-else class="mid">
    <section class="mid__sec">
      <header class="mid__head">
        <h2 class="mid__h">{{ t('mid.list.title') }}</h2>
        <button type="button" class="mid__btn" :disabled="loading" data-testid="mid-load" @click="load">
          {{ drift ? t('mid.reload') : t('mid.load') }}
        </button>
      </header>

      <!-- ★★ 越界档位一律不给：后端是静默回落（不报错），给了等于骗用户 -->
      <div class="mid__chips">
        <span class="mid__chip-label">{{ t('mid.filter.days') }}</span>
        <button
          v-for="dp in DAYS_CHOICES"
          :key="dp"
          type="button"
          class="mid__chip-btn"
          :class="{ 'mid__chip-btn--on': dp === days }"
          :data-testid="'mid-days-' + dp"
          @click="days = dp; load()"
        >
          {{ dp }}
        </button>
      </div>

      <div class="mid__chips">
        <span class="mid__chip-label">{{ t('mid.filter.limit') }}</span>
        <button
          v-for="lp in LIMIT_CHOICES"
          :key="lp"
          type="button"
          class="mid__chip-btn"
          :class="{ 'mid__chip-btn--on': lp === limit }"
          :data-testid="'mid-limit-' + lp"
          @click="limit = lp; load()"
        >
          {{ lp }}
        </button>
      </div>

      <p v-if="error" class="mid__msg mid__msg--err" data-testid="mid-error">{{ error }}</p>
      <p v-if="loading" class="mid__msg">{{ t('common.loading') }}</p>

      <template v-if="drift">
        <!-- ★★ count 恒等于本页行数（不是窗口内总数，也不是全表命中数） -->
        <p class="mid__msg" data-testid="mid-count">
          {{ t('mid.list.count', { n: drift.count, len: drift.events.length }) }}
        </p>
        <p v-if="countViolation" class="mid__msg mid__msg--err" data-testid="mid-count-violation">
          {{ t('mid.list.countViolation', { n: drift.count, len: drift.events.length }) }}
        </p>

        <!-- ★★ 回显的是**生效窗口**；请求值被回落时要单独说 -->
        <p class="mid__msg" data-testid="mid-days-echo">
          {{ t('mid.list.daysEcho', { h: drift.days }) }}
        </p>
        <p v-if="daysRewritten" class="mid__msg mid__msg--warn" data-testid="mid-days-rewritten">
          {{ t('mid.list.daysRewritten', { want: days, max: DRIFT_DAYS_MAX }) }}
        </p>

        <!-- ★★ 响应不回显 limit ⇒ 拿满上限只能判「可能截断」 -->
        <p v-if="atCap" class="mid__msg mid__msg--warn" data-testid="mid-at-cap">
          {{ t('mid.list.atCap', { lim: limit }) }}
        </p>

        <p v-if="!orderOk" class="mid__msg mid__msg--err" data-testid="mid-order-bad">
          {{ t('mid.list.orderBad') }}
        </p>

        <!-- ★ 跨租户：超管看到的是全部租户的漂移（含 sample / context） -->
        <p v-if="foreignTenants" class="mid__msg" data-testid="mid-foreign-tenants">
          {{ t('mid.list.foreignTenants') }}
        </p>
        <p v-else-if="noTenantTags" class="mid__msg mid__msg--warn" data-testid="mid-no-tenant-tags">
          {{ t('mid.list.noTenantTags') }}
        </p>

        <p v-if="emptyWindow" class="mid__msg" data-testid="mid-empty">
          {{ t('mid.list.empty') }}
        </p>

        <article
          v-for="r in drift.events"
          :key="r.id"
          class="mid__card"
          :data-testid="'mid-row-' + r.id"
        >
          <div class="mid__row">
            <span class="mid__type">{{ r.anomaly_type }}</span>
            <span
              class="mid__badge"
              :class="{ 'mid__badge--warn': r.severity === 'high' || r.severity === 'critical' }"
            >
              {{ severityText(r) }}
            </span>
            <span class="mid__trace">{{ traceText(r) }}</span>
          </div>
          <dl class="mid__kv">
            <div class="mid__kv-row">
              <dt>{{ t('mid.col.detectedAt') }}</dt><dd>{{ r.detected_at }}</dd>
            </div>
            <div class="mid__kv-row">
              <dt>{{ t('mid.col.requestId') }}</dt>
              <dd :data-testid="'mid-f-' + r.id + '-request_id'">{{ requestIdText(r) }}</dd>
            </div>
            <div class="mid__kv-row">
              <dt>{{ t('mid.col.provider') }}</dt><dd>{{ optionalText(r.provider_code) }}</dd>
            </div>
            <div class="mid__kv-row">
              <dt>{{ t('mid.col.providerId') }}</dt>
              <dd :data-testid="'mid-f-' + r.id + '-provider_id'">{{ numericText(r, 'provider_id') }}</dd>
            </div>
            <div class="mid__kv-row">
              <dt>{{ t('mid.col.credentialId') }}</dt>
              <dd :data-testid="'mid-f-' + r.id + '-credential_id'">
                {{ numericText(r, 'credential_id') }}
              </dd>
            </div>
            <div class="mid__kv-row">
              <dt>{{ t('mid.col.clientModel') }}</dt><dd>{{ optionalText(r.client_model) }}</dd>
            </div>
            <div class="mid__kv-row">
              <dt>{{ t('mid.col.outboundModel') }}</dt><dd>{{ optionalText(r.outbound_model) }}</dd>
            </div>
            <div class="mid__kv-row">
              <dt>{{ t('mid.col.rawModel') }}</dt><dd>{{ optionalText(r.raw_model_name) }}</dd>
            </div>
            <div class="mid__kv-row">
              <dt>{{ t('mid.col.expected') }}</dt><dd>{{ optionalText(r.expected_value) }}</dd>
            </div>
            <div class="mid__kv-row">
              <dt>{{ t('mid.col.actual') }}</dt>
              <dd :data-testid="'mid-f-' + r.id + '-actual_value'">{{ optionalText(r.actual_value) }}</dd>
            </div>
            <div class="mid__kv-row">
              <dt>{{ t('mid.col.sample') }}</dt><dd>{{ optionalText(r.sample) }}</dd>
            </div>
            <!-- ★★★ 键缺 ≠ 没有上下文：jsoncol.Decode 的三条路都让键消失 -->
            <div class="mid__kv-row">
              <dt>{{ t('mid.col.context') }}</dt>
              <dd :data-testid="'mid-f-' + r.id + '-context'">{{ contextText(r) }}</dd>
            </div>
            <div class="mid__kv-row">
              <dt>{{ t('mid.col.tenant') }}</dt><dd>{{ optionalText(r.tenant_id) }}</dd>
            </div>
            <div v-if="r.resolved_at !== undefined" class="mid__kv-row">
              <dt>{{ t('mid.col.resolvedAt') }}</dt><dd>{{ r.resolved_at }}</dd>
            </div>
            <div v-if="r.resolution_notes !== undefined" class="mid__kv-row">
              <dt>{{ t('mid.col.notes') }}</dt><dd>{{ optionalText(r.resolution_notes) }}</dd>
            </div>
          </dl>
        </article>
      </template>
    </section>
  </div>
</template>

<style scoped>
.mid {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.mid__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.mid__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.mid__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

.mid__btn {
  min-height: 48px;
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.mid__chip-btn {
  min-height: 48px;
  padding: 0 12px;
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.mid__chip-btn--on {
  color: var(--app-text);
  border-color: var(--app-text-secondary);
}

.mid__chip-label {
  align-self: center;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.mid__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: center;
}

.mid__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.mid__msg--err {
  color: var(--app-danger);
}

.mid__msg--warn {
  color: var(--app-warning, var(--app-danger));
}

.mid__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.mid__type {
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--app-text);
  word-break: break-all;
}

.mid__badge {
  font-size: 0.6875rem;
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
  color: var(--app-text-secondary);
}

.mid__badge--warn {
  color: var(--app-warning, var(--app-danger));
}

.mid__trace {
  margin-left: auto;
  font-size: 0.6875rem;
  color: var(--app-text-muted);
}

.mid__kv {
  margin: 0;
}

.mid__kv-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
}

.mid__kv-row dt {
  color: var(--app-text-muted);
}

.mid__kv-row dd {
  margin: 0;
  color: var(--app-text);
  word-break: break-all;
  text-align: right;
}
</style>
