<script setup lang="ts">
// WorkTypesView — 工作类型配置面（2026-10-08，第一百零三批）。
//
// GET /api/admin/work-types
// GET /api/admin/work-types/stats
// GET /api/admin/work-types/l1-task-types
//
// ⚠️★ **整族是 `h.superAdmin` 档**（`admin/handler.go:1434`
//   `wtH.RegisterWorkTypeRoutes(mux, h.superAdmin)`，**不是** `h.admin`）
//   ⇒ tenant_admin 直接 403 ⇒ 抽屉席**必须**设 `requiresRole: 'super_admin'`
//   并同步 `AppDrawer.spec.ts` 的白名单。
// ★ 与批 90（settings，`h.admin`）**同前缀邻域、不同档位** —— 别照抄那一席。
//
// ★★★ 本页最要紧的是**五处「不能当成普通数字/数组」的地方**（详见 api/workTypes.ts 文件头）：
//   1. `model_routes` 的 `null` 与 `[]` **后端都产生不出来**（Go `omitempty`
//      对 slice 同时省略 nil 与 len==0）⇒ 只有「键消失 / 非空数组」两态是真的。
//   2. `count_24h` 是**派生量**（`=== count_direct || === count_l1_proxy`），
//      不是独立计数。
//   3. `count_l1_proxy` 是该 L1 的**全局量**，同一 L1 下多行会重复
//      ⇒ **对它求和会重复计数**，本页因此不显示合计。
//   4. `top_models` **无 tiebreak** ⇒ 同计数项的顺序未定义。
//   5. `system_prompt` / `synced_from_acc_at` 是 `*T + omitempty`
//      ⇒ **键可能整个不存在**，不能当成空串。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import {
  fetchWorkTypes,
  fetchWorkTypeStats,
  fetchL1TaskTypes,
  WORK_TYPES_STATS_WINDOW_HOURS,
  WORK_TYPES_ROUTE_TIERS,
  type WorkTypeConfig,
  type WorkTypeStatEntry,
  type WorkTypeStats,
  type L1TaskTypeMeta,
} from '@/api/workTypes'

useHyperPage({ title: () => t('wt.title') })

const configs = ref<WorkTypeConfig[] | null>(null)
const stats = ref<WorkTypeStats | null>(null)
const l1Items = ref<L1TaskTypeMeta[] | null>(null)
const errors = ref<string[]>([])
const loading = ref(false)
const keyword = ref('')

const rows = computed<WorkTypeConfig[]>(() => {
  const all = configs.value ?? []
  const q = keyword.value.trim().toLowerCase()
  if (!q) return all
  return all.filter(
    (c) =>
      c.key.toLowerCase().includes(q) ||
      c.label.toLowerCase().includes(q) ||
      c.category.toLowerCase().includes(q) ||
      c.l1_task_type.toLowerCase().includes(q),
  )
})

/** ★ 只有 `enabled = TRUE` 的配置才在 stats 里（文件头 (10)）。 */
const enabledKeys = computed(() => new Set(Object.keys(stats.value?.by_work_type ?? {})))

/**
 * ★★★ `model_routes` 只有**两态**是真的（`admin/work_types.go:518` `,omitempty`）：
 *   - 键消失  = 无路由（`routeMap[key]` 对没建条目的 key 是 nil slice ⇒ 被省略）
 *   - 非空数组 = 有路由
 * 而 `null` 与 `[]` **后端都产生不出来** ——
 * Go 的 `omitempty` 对 slice 的判定是 `len() == 0`，**非 nil 的空切片同样被省略**
 * （`getWorkType` 走 `fetchRoutes` 的 `make([]modelRoute, 0)` 也是空的，一样省略）。
 * ⇒ ★★ 所以这两个分支**不是配置状态，是响应异常**，
 *   绝不能渲染成「配了但为空」这种听起来很正常的话。
 */
function routesText(c: WorkTypeConfig): string {
  // ★ 读成局部量再判：`'model_routes' in c` 对可选属性**不做收窄**（vue-tsc TS18048）
  const routes = c.model_routes
  if (routes === undefined) return t('wt.routesAbsent')
  if (routes === null) return t('wt.routesAnomalous')
  if (routes.length === 0) return t('wt.routesAnomalous')
  return routes.map((r) => r.canonical_name).join(' / ')
}

function routesTone(c: WorkTypeConfig): 'info' | 'muted' | 'danger' {
  const routes = c.model_routes
  if (routes === undefined) return 'muted'
  if (routes === null) return 'danger'
  if (routes.length === 0) return 'danger'
  return 'info'
}

/** ★★ `by_work_type` 为空有三种成因（查询失败 / 真没有 / db 为 nil），响应里没有标记
 *  ⇒ 此时**不能说**「这些工作类型不在统计里」，只能说「无法判定」。 */
const byWorkTypeUnknown = computed(() => stats.value !== null && Object.keys(stats.value.by_work_type).length === 0)

function statOf(c: WorkTypeConfig): WorkTypeStatEntry | null {
  return stats.value?.by_work_type[c.key] ?? null
}

/**
 * ★★★ 三个计数一起渲染，不给合计。
 * `count_24h` 是 `count_direct || count_l1_proxy` 的**派生**结果，
 * 而 `count_l1_proxy` 是该 L1 的**全局量**（同一 L1 下多行重复）
 * ⇒ 把它们加起来必然重复计数，所以本页只逐行给，不汇总。
 */
function countsText(c: WorkTypeConfig): string {
  const e = statOf(c)
  if (!e) return ''
  return t('wt.counts', { d: e.count_24h, direct: e.count_direct, proxy: e.count_l1_proxy })
}

/** ★ 统计窗口小时数来自响应，不是硬编码。 */
const windowHours = computed(() => stats.value?.window_hours ?? WORK_TYPES_STATS_WINDOW_HOURS)

/** ★★ 不给合计：`count_l1_proxy` 是 L1 全局量，跨行求和会重复计数。 */
const noAggregateNote = computed(() => t('wt.noAggregate'))

async function load(): Promise<void> {
  loading.value = true
  errors.value = []
  const [c, s, l] = await Promise.allSettled([fetchWorkTypes(), fetchWorkTypeStats(), fetchL1TaskTypes()])
  if (c.status === 'fulfilled') configs.value = c.value
  else errors.value.push(t('wt.loadConfigsFailed'))
  if (s.status === 'fulfilled') stats.value = s.value
  else errors.value.push(t('wt.loadStatsFailed'))
  if (l.status === 'fulfilled') l1Items.value = l.value.items
  else errors.value.push(t('wt.loadL1Failed'))
  // ★ 失败原因原文透出（superAdmin 档最常见的失败就是 403）。
  for (const r of [c, s, l]) {
    if (r.status === 'rejected') {
      const m = (r.reason as Error)?.message
      if (m) errors.value.push(m)
    }
  }
  loading.value = false
}

void load()

onBeforeUnmount(() => {
  configs.value = null
  stats.value = null
  l1Items.value = null
  errors.value = []
})
</script>

<template>
  <div class="wt">
    <p v-if="loading" class="wt__msg">{{ t('common.loading') }}</p>

    <section v-if="errors.length > 0" class="wt__panel">
      <p v-for="(e, i) in errors" :key="i" class="wt__msg wt__msg--err">{{ e }}</p>
    </section>

    <!-- ── 统计条 ── -->
    <section v-if="stats" class="wt__panel">
      <h2 class="wt__h">{{ t('wt.statsTitle', { h: windowHours }) }}</h2>
      <div class="wt__totals">
        <span>{{ t('wt.totalAuto') }} <b class="num">{{ stats.total_auto }}</b></span>
        <span>{{ t('wt.totalSpecified') }} <b class="num">{{ stats.total_specified }}</b></span>
      </div>
      <!-- ★★ 派生的 count_24h 与全局的 count_l1_proxy：给表不给合计 -->
      <p class="wt__warn">{{ noAggregateNote }}</p>
      <p v-if="byWorkTypeUnknown" class="wt__warn">{{ t('wt.byWorkTypeUnknown') }}</p>
      <ul v-if="stats.top_models.length > 0" class="wt__chips">
        <li v-for="m in stats.top_models" :key="m.model" class="wt__chip">
          {{ m.model }} <span class="num">{{ m.count }}</span>
        </li>
      </ul>
      <p v-else class="wt__msg">{{ t('wt.noTopModels') }}</p>
    </section>

    <!-- ── L1 任务类型 ── -->
    <section v-if="l1Items" class="wt__panel">
      <h2 class="wt__h">{{ t('wt.l1Title', { n: l1Items.length }) }}</h2>
      <ul class="wt__chips">
        <li v-for="it in l1Items" :key="it.key" class="wt__chip">{{ it.key }}</li>
      </ul>
    </section>

    <!-- ── 工作类型清单 ── -->
    <section v-if="configs" class="wt__panel">
      <h2 class="wt__h">{{ t('wt.listTitle', { n: configs.length }) }}</h2>
      <input
        v-model="keyword"
        class="wt__input"
        type="search"
        :placeholder="t('wt.filterHint')"
        :aria-label="t('wt.filterHint')"
      />
    </section>

    <p v-if="rows.length === 0 && !loading" class="wt__msg">{{ t('wt.empty') }}</p>

    <article v-for="c in rows" :key="c.key" class="wt__card">
      <div class="card-row">
        <span class="badge" :class="c.enabled ? 'badge--success' : 'badge--muted'">
          {{ c.enabled ? t('wt.enabled') : t('wt.disabled') }}
        </span>
        <span class="wt__key">{{ c.key }}</span>
        <span class="wt__label">{{ c.label }}</span>
      </div>
      <div class="wt__meta">
        <span>{{ t('wt.category') }} {{ c.category }}</span>
        <span>{{ t('wt.l1') }} {{ c.l1_task_type }}</span>
        <span>{{ t('wt.profile') }} {{ c.default_profile }}</span>
        <span v-if="!byWorkTypeUnknown && !enabledKeys.has(c.key)" class="wt__tag">★ {{ t('wt.notInStats') }}</span>
      </div>
      <!-- ★★ count_24h 是派生量：三个数字一起给，不给合计 -->
      <p v-if="statOf(c)" class="wt__note">{{ countsText(c) }}</p>
      <p class="wt__routes" :class="`wt__routes--${routesTone(c)}`">{{ routesText(c) }}</p>
      <!-- ★ omitempty 字段：键可能整个不存在，不能当空串渲染 -->
      <p v-if="'system_prompt' in c" class="wt__note">{{ t('wt.hasPrompt') }}</p>
      <p v-if="'synced_from_acc_at' in c" class="wt__note">
        {{ t('wt.syncedAt') }} {{ c.synced_from_acc_at }}
      </p>
      <p v-if="c.tags.length > 0" class="wt__note">{{ t('wt.tags') }} {{ c.tags.join(' · ') }}</p>
    </article>

    <p v-if="WORK_TYPES_ROUTE_TIERS.length > 0 && rows.length > 0" class="wt__note">
      {{ t('wt.tierLegend') }} {{ WORK_TYPES_ROUTE_TIERS.join(' / ') }}
    </p>
  </div>
</template>

<style scoped>
.wt {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-3);
}

.wt__panel {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.wt__h {
  margin: 0;
  font-size: 0.9375rem;
  font-weight: 600;
}

.wt__msg {
  margin: 0;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}

.wt__msg--err {
  color: var(--app-danger);
}

.wt__warn {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-warning, var(--app-danger));
}

.wt__note {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
  word-break: break-all;
}

.wt__totals {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-3);
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
}

/* R1：新增触控控件一律 ≥48 CSS px */
.wt__input {
  width: 100%;
  min-height: 48px;
  padding: 0 12px;
  font-size: 14px;
  color: inherit;
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.wt__chips {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1);
  margin: 0;
  padding: 0;
  list-style: none;
}

/* chip 是纯展示，不是控件 ⇒ 不受 R1 触控热区约束 */
.wt__chip {
  padding: 2px 8px;
  font-size: 0.6875rem;
  color: var(--app-text-secondary);
  background: var(--app-surface-muted);
  border-radius: var(--app-radius-sm);
  word-break: break-all;
}

.wt__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.wt__key {
  font-size: 0.875rem;
  font-weight: 600;
  word-break: break-all;
}

.wt__label {
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
}

.wt__meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1) var(--app-space-3);
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.wt__tag {
  color: var(--app-warning, var(--app-danger));
}

.wt__routes {
  margin: 0;
  font-size: 0.75rem;
  word-break: break-all;
}

.wt__routes--info {
  color: var(--app-text-secondary);
}

.wt__routes--muted {
  color: var(--app-text-muted);
  font-style: italic;
}

.wt__routes--danger {
  color: var(--app-danger);
}
</style>