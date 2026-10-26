<script setup lang="ts">
// SessionAnalyticsFilterOptionsView — 会话分析页的模型/提供商筛选项（2026-10-15，第一百二十八批）。
//
// GET /api/admin/session-analytics/filter-options
//
// ══════════════════════════════════════════════════════════════════════════
// ★★★★★ 本页与兄弟端点 `TurnsFilterOptionsView` **四处方向相反**，抄错就是全错
// ══════════════════════════════════════════════════════════════════════════
//
//   兄弟：/api/admin/turns/sessions/filter-options（批 127）
//   本页：/api/admin/session-analytics/filter-options（本批）
//
//   | 维度                | 本页（session-analytics）      | 兄弟（turns/sessions）    |
//   |---------------------|-------------------------------|---------------------------|
//   | 维度个数            | **2**（models / providers）   | 8                          |
//   | 无数据时的编码      | ★★★★★ **`null`**             | ★★★★★ `[]`                |
//   | **半空形状**        | ★★★★★ **可达且合法**          | ★★★★★ **不可达**          |
//   | 租户可见范围        | ★★★★★ **super_admin 看全部租户** | ★★★★★ 永远租户内         |
//   | 无 DB 时            | ★★★★ **404（整棵树不注册）** | 503（恒注册）              |
//   | 能 `?tenant=` 收窄？ | ★★★★ **不能**               | 能                         |
//   | scan 失败           | ★★★★ 静默跳过该行             | 500                        |
//   | 超时                | 10s                           | 12s                        |
//
// ══ 权限 ══════════════════════════════════════════════════════════════════
// ★★★★★ `requiresRole: 'admin'`。
//
//   注册 `cmd/gateway/main.go:7409`：
//     mux.HandleFunc("/api/admin/session-analytics/", wrapSessionAnalytics(adminHandler.RouteSessionAnalytics))
//   ⇒ `wrapSessionAnalytics` = `newWrapSessionAnalytics`（`main_admin_wrappers.go:42`）
//   ⇒ ★★ 它是**双模式**的，而**两条分支都不给浏览器用户设角色下限**：
//     （甲）session-service JWT 未启用 ⇒ `admin.AdminMiddleware`
//           ⇒ `admin/auth.go:86` 起，**全文无任何 `claims.Role` 比对**
//     （乙）已启用且验签通过 ⇒ `admin.SessionAnalyticsMiddleware`
//           （`admin/session_service_auth.go:18`）**确实**逐字检了
//           `hasScope(claims.Scope, "session:read")` + `claims.TenantID != ""`
//           ⇒ ★ 但那是被强制成 `Role: "tenant_admin"` 的**服务间调用**，
//             **不是浏览器用户**的路；浏览器用户走（甲）⇒ **零角色下限**。
//
//   ⇒ **两条独立理由，缺一不可**：
//     （甲）**需要 admin**：载荷是**租户级聚合清单** ——
//          「这个租户所有人跑过的模型与提供商」。一个只该看自己请求的
//          普通用户，没理由看到整个租户的模型/提供商清单。
//     （乙）**不过度限制到 super_admin**：除 super_admin 外，handler 对两条查询
//          **都**拼了 `AND tenant_id = $1`（`session_analytics_top.go:213`、`:226`）
//          ⇒ 非 super_admin 拿到的是**自己租户内**的 ⇒ 按批 119 边界不过度限制。
//
//   ⚠️ **别把兄弟端点的结论搬过来**：兄弟那边 super_admin 也只看自己租户，
//     本页 super_admin **看全部租户的聚合**，且**没有 `?tenant=` 手段收窄**
//     （`effectiveScopeTenant` 不读查询参数，`admin/session_tenant.go:180`）。
//
//   ⇒ ★★ **服务端这一层是唯一的门**（对浏览器用户而言）；前端严于后端是**兜底不是修复**。
import { computed, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchSessionAnalyticsFilterOptions,
  sessionAnalyticsFilterOptionsRoleNeedsAdminGate,
  sessionAnalyticsFilterOptionsIsTenantAgnostic,
  sessionAnalyticsFilterOptionsIsEmpty,
  sessionAnalyticsFilterOptionsHasAnyValue,
  sessionAnalyticsFilterOptionsIsHalfEmpty,
  filterOptionList,
  filterOptionValuesAreSortedUnique,
  SESSION_ANALYTICS_FILTER_OPTION_KEYS,
  SESSION_ANALYTICS_FILTER_WINDOW_DAYS,
  type SessionAnalyticsFilterOptions,
} from '@/api/sessionAnalyticsFilterOptions'

useHyperPage({ title: () => (allowed.value ? t('saf.title') : t('common.pageDenied')) })

const auth = useAuthStore()

// ══ 权限兜底（★ 本族它是唯一的门，不是体验兜底）═══════════════════════════

const role = computed(() => auth.role)
const allowed = computed(() => !sessionAnalyticsFilterOptionsRoleNeedsAdminGate(role.value))

/**
 * ★★ super_admin 看到的是**全部租户**的聚合，且**无法用参数收窄**（模块头 (4)(5)）。
 *   ⇒ 这一档必须在页面上写明，否则用户会以为「这就是我这个租户的清单」。
 */
const isCrossTenantView = computed(() => role.value === 'super_admin' || role.value === 'admin_key')

// ══ 取数 ══════════════════════════════════════════════════════════════════

const options = ref<SessionAnalyticsFilterOptions | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

async function load(): Promise<void> {
  // ★ 双重保险：抽屉席/路由已挡一道，这里再挡是为了**深链直达**
  //   （壳层 `meta.requiresRole` 全仓无人消费，见批 116 待办）。
  if (!allowed.value) return
  loading.value = true
  error.value = null
  try {
    options.value = await fetchSessionAnalyticsFilterOptions()
  } catch (err) {
    options.value = null
    error.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}

// ══ 派生 ══════════════════════════════════════════════════════════════════

const isEmpty = computed(() => options.value !== null && sessionAnalyticsFilterOptionsIsEmpty(options.value))
const hasAny = computed(() => options.value !== null && sessionAnalyticsFilterOptionsHasAnyValue(options.value))

/**
 * ★★★★★ (3) **半空形状是本端点最该被页面显式承认的一档。**
 *
 *   `{models: [...], providers: null}` 与它的镜像都是**合法且可达**的
 *   （`models_used text[] DEFAULT '{}' NOT NULL` 而 `providers text[]` 可空；
 *   `UNNEST` 空数组/NULL 都只产生零行而不是报错）。
 *
 *   ⇒ `IsEmpty`（全空）与 `HasAnyValue`（非全空）**都覆盖不到它**：
 *     它会被 `HasAnyValue` 判成「有数据」，页面于是正常渲染，
 *     **用户看不到任何「还缺一边」的提示** —— 而另一边其实一个值都没有。
 *   ⇒ ★★ 兄弟端点**不需要**这个分支：那边 8 个维度共用一个 `[]`，半空不可达。
 *     这就是「同叶名兄弟不可互相套用」的第四例。
 */
const halfEmpty = computed(
  () => options.value !== null && sessionAnalyticsFilterOptionsIsHalfEmpty(options.value),
)

/**
 * ★★ 两个维度**显式列出**，不用 `SESSION_ANALYTICS_FILTER_OPTION_KEYS` 循环。
 *
 *   与兄弟端点的对照：兄弟那边 8 个维度的**窗口口径不一样**
 *   （只有 3 个有 30 天窗口，其余 5 个是全历史）⇒ 必须分开写文案。
 *   本页**两个维度都是 30 天窗口**（`:210`、`:223`）⇒ 口径一致，
 *   但仍显式列出：为的是让「一共只有 2 个维度」这件事在代码里可核对，
 *   而不是靠一个「恰好长度为 2 的常量」隐式表达。
 */
interface Dim {
  key: (typeof SESSION_ANALYTICS_FILTER_OPTION_KEYS)[number]
  labelKey: string
  /** ★ 后端聚合的数组列名（`session_summaries.models_used` / `.providers`）。 */
  sourceColumn: string
}

const DIMS: readonly Dim[] = [
  { key: 'models', labelKey: 'saf.dim.models', sourceColumn: 'models_used' },
  { key: 'providers', labelKey: 'saf.dim.providers', sourceColumn: 'providers' },
]

/**
 * ★★ (2) 必须过 `filterOptionList` 把 `null` 归一成 `[]` ——
 *   否则「网关还没跑过流量」（后端发 `null`）会让模板里的 `.length` 崩。
 *   ★ 这与兄弟端点**成因相反**：那边是后端漏了空数组，这边是后端**故意**发 null。
 */
function valuesOf(dim: Dim): readonly string[] {
  return options.value ? filterOptionList(options.value[dim.key]) : []
}

/** ★ (2) 该维度后端发的是 `null`（而非 `[]`）—— 两种「空」在页面上要分开说。 */
function wasNull(dim: Dim): boolean {
  return options.value !== null && options.value[dim.key] === null
}

/** ★ (9) 后端保证按值升序且无重复；这里把它做成**页面可见的事实**而不是假定。 */
function sortedUnique(dim: Dim): boolean {
  return filterOptionValuesAreSortedUnique(valuesOf(dim))
}

/** ★ (2) 空维度清单。 */
const emptyDims = computed(() => DIMS.filter((d) => valuesOf(d).length === 0))

/** ★ (4) 载荷里不含任何租户标识 —— 旁证「这是聚合清单而非逐条会话记录」。 */
const tenantAgnostic = computed(
  () => options.value !== null && sessionAnalyticsFilterOptionsIsTenantAgnostic(options.value),
)

const windowDays = SESSION_ANALYTICS_FILTER_WINDOW_DAYS
const keyCount = SESSION_ANALYTICS_FILTER_OPTION_KEYS.length
</script>

<template>
  <div class="saf">
    <!-- ★★ 深链兜底：本族它是**唯一的门**（服务端对浏览器用户没有角色下限）。 -->
    <section v-if="!allowed" class="saf__sec" data-testid="saf-nope">
      <header class="saf__head">
        <h2 class="saf__h">{{ t('saf.title') }}</h2>
      </header>
      <p class="saf__msg saf__msg--err" data-testid="saf-nope-msg">{{ t('saf.nope.msg') }}</p>
      <p class="saf__msg" data-testid="saf-nope-why">{{ t('saf.nope.why') }}</p>
    </section>

    <template v-else>
      <section class="saf__sec">
        <header class="saf__head">
          <h2 class="saf__h">{{ t('saf.load.title') }}</h2>
          <button
            type="button"
            class="saf__btn"
            data-testid="saf-load"
            :disabled="loading"
            @click="load"
          >
            {{ loading ? t('common.loading') : t('saf.load.btn') }}
          </button>
        </header>
        <p class="saf__msg" data-testid="saf-readonly-note">{{ t('saf.readonly.note') }}</p>
        <!-- ★ 说明「这一层是唯一的门」，别让人以为后端也在挡。 -->
        <p class="saf__msg saf__msg--warn" data-testid="saf-gate-note">{{ t('saf.gate.note') }}</p>
        <p v-if="error" class="saf__msg saf__msg--err" data-testid="saf-error">{{ error }}</p>
      </section>

      <!-- ★★ 跨租户档必须写在**取数之前**：这一档的可见范围与别人不同。 -->
      <section v-if="isCrossTenantView" class="saf__sec" data-testid="saf-crosstenant">
        <p class="saf__msg saf__msg--warn" data-testid="saf-crosstenant-msg">
          {{ t('saf.crossTenant.note') }}
        </p>
      </section>

      <section v-if="options" class="saf__sec" data-testid="saf-state">
        <header class="saf__head">
          <h2 class="saf__h">{{ t('saf.state.title') }}</h2>
          <span
            v-if="hasAny && !isEmpty"
            class="saf__badge"
            data-testid="saf-nonempty"
          >
            {{ t('saf.badge.nonEmpty') }}
          </span>
        </header>

        <p v-if="isEmpty" class="saf__msg" data-testid="saf-empty">{{ t('saf.empty') }}</p>
        <!-- ★★ 半空必须单独提示：否则用户会把「一边没值」当成正常数据。 -->
        <p
          v-if="halfEmpty"
          class="saf__msg saf__msg--warn"
          data-testid="saf-half-empty"
        >
          {{ t('saf.halfEmpty', { list: emptyDims.map((d) => t(d.labelKey)).join(' / ') }) }}
        </p>
        <p v-if="emptyDims.length > 0" class="saf__msg" data-testid="saf-empty-dims">
          {{ t('saf.emptyDims', { list: emptyDims.map((d) => t(d.labelKey)).join(' / ') }) }}
        </p>
        <!-- ★ 把「不过度限制」的依据摆出来：非 super_admin 拿到的是租户内的。 -->
        <p v-if="tenantAgnostic" class="saf__msg" data-testid="saf-no-tenant">
          {{ t('saf.tenant.agnostic') }}
        </p>
        <!-- ★ 两个维度都是 30 天窗口（与兄弟端点不同：那边只有 3/8 有窗口）。 -->
        <p class="saf__msg" data-testid="saf-window-note">
          {{ t('saf.windowNote', { days: windowDays, n: keyCount }) }}
        </p>

        <section
          v-for="d in DIMS"
          :key="d.key"
          class="saf__dim"
          :data-testid="'saf-dim-' + d.key"
        >
          <header class="saf__dhead">
            <h3 class="saf__h3">{{ t(d.labelKey) }}</h3>
            <!-- ★ (9) 后端保证的排序/去重，把它显示出来而不是默默假定。 -->
            <span
              v-if="sortedUnique(d)"
              class="saf__badge"
              :data-testid="'saf-sorted-' + d.key"
            >
              {{ t('saf.sortedUnique') }}
            </span>
          </header>
          <!-- ★ (9)(10) 聚合来源列 + 窗口天数，必须真的带上占位符参数。 -->
          <p class="saf__msg" :data-testid="'saf-note-' + d.key">
            {{ t('saf.dimNote', { column: d.sourceColumn, days: windowDays }) }}
          </p>
          <ul
            v-if="valuesOf(d).length > 0"
            class="saf__list"
            :data-testid="'saf-list-' + d.key"
          >
            <li
              v-for="(v, i) in valuesOf(d)"
              :key="i"
              class="saf__item"
              :data-testid="'saf-item-' + d.key + '-' + i"
            >
              {{ v }}
            </li>
          </ul>
          <p v-else class="saf__msg" :data-testid="'saf-none-' + d.key">
            <!-- ★★ (2) 「后端发的是 null」与「查到了但是空」要分开说。 -->
            {{ wasNull(d) ? t('saf.dim.nullEmpty') : t('saf.dim.empty') }}
          </p>
        </section>
      </section>
    </template>
  </div>
</template>

<style scoped>
.saf {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 12px;
}
.saf__sec {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
  border-radius: 12px;
  background: var(--app-surface, #fff);
  border: 1px solid var(--app-border, #e5e7eb);
}
.saf__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.saf__h {
  font-size: 16px;
  font-weight: 600;
  margin: 0;
}
.saf__h3 {
  font-size: 14px;
  font-weight: 600;
  margin: 0;
}
.saf__msg {
  font-size: 13px;
  line-height: 1.5;
  margin: 0;
  /* ★★ 用 `--app-text-secondary`（theme.css 亮/暗两套都已定义），
     不要自造 `--app-text-dim` —— `styles/theme.spec.ts` 的
     「未定义 token 引用棘轮」会当场红（本批真踩到了：初版写了
     `--app-text-dim` ⇒ 全量第 6 条红，与 v1DataHorizon 那族无关）。 */
  color: var(--app-text-secondary);
}
.saf__msg--err {
  color: var(--app-danger, #dc2626);
}
.saf__msg--warn {
  color: var(--app-warning, #b45309);
}
.saf__badge {
  font-size: 12px;
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--app-primary, #2563eb);
  color: #fff;
  white-space: nowrap;
}
/* ★ 触控目标 ≥48 CSS px（touch:check 的硬门）。 */
.saf__btn {
  min-height: 48px;
  min-width: 96px;
  padding: 0 16px;
  border-radius: 10px;
  border: 1px solid var(--app-border, #e5e7eb);
  background: var(--app-primary, #2563eb);
  color: #fff;
  font-size: 14px;
  cursor: pointer;
}
.saf__btn:disabled {
  opacity: 0.6;
  cursor: default;
}
.saf__dim {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding-top: 8px;
  border-top: 1px solid var(--app-border, #e5e7eb);
}
.saf__dhead {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.saf__list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.saf__item {
  font-size: 13px;
  padding: 4px 10px;
  border-radius: 8px;
  background: var(--app-bg, #f3f4f6);
  border: 1px solid var(--app-border, #e5e7eb);
  word-break: break-all;
}
</style>
