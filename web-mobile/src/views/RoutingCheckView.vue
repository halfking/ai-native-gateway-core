<script setup lang="ts">
// RoutingCheckView — 路由检查（explain）：输入模型名 → 看这次请求会路由到哪、
// 以及**为什么不是**别的凭据。数据源 GET /api/routing/resolve。
//
// 17 §2 原先把「路由调试」整个划给 desktopOnly。本轮收进来的部分是**只读
// explain**（admin 档，tenant_admin 也能用），不是桌面的候选重排/策略编辑
// （那些是 superAdmin + 乐观并发版本号，写操作留在桌面）。
//
// 后端 admin/routing.go:273-275 特意**始终返回全部候选**并带上不可用原因，
// 所以这里能把被阻塞的候选也显示出来 —— 那正是「路由检查」最有用的部分。
import { computed, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { resolveRouting, type RoutingCandidate, type RoutingResolveResponse } from '@/api/credentialsOps'
import {
  fetchRoutingOverview,
  overviewBlockReason,
  overviewFeaturedFilterInert,
  overviewMetricsMayBePlaceholder,
  overviewRoutableKeysDisagree,
  type RoutingOverviewResponse,
} from '@/api/routingRead'
import { ApiError } from '@/api/client'
import { t } from '@/i18n'
import AppIcon from '@/components/common/AppIcon.vue'

useHyperPage({ title: () => t('routing.title') })

const model = ref('')
const loading = ref(false)
const error = ref<string | null>(null)
const result = ref<RoutingResolveResponse | null>(null)

/** 只有输入非空才允许查询 —— 后端 model 缺失直接 400（admin/routing.go:272-275）。 */
const canQuery = computed(() => model.value.trim().length > 0 && !loading.value)

async function run(): Promise<void> {
  if (!canQuery.value) return
  loading.value = true
  error.value = null
  result.value = null
  try {
    result.value = await resolveRouting(model.value.trim())
  } catch (err) {
    error.value = describeError(err)
  } finally {
    loading.value = false
  }
}

function describeError(err: unknown): string {
  const status = err instanceof ApiError ? err.status : undefined
  if (status === 400) return t('routing.errBadRequest')
  if (status === 403) return t('routing.errForbidden')
  return err instanceof Error ? err.message : t('common.error')
}

/** 「无变体」时后端返回空 candidates 的合法对象（admin/routing.go:288-296）——不是错误。 */
const noCandidates = computed(() => result.value !== null && result.value.candidates.length === 0)

// ── 全量可路由性总览（GET /api/routing/overview，admin 档）─────────────────
//
// 与 explain 的分工：explain 回答「**这一个模型**会去哪」，overview 回答
//「**全网**哪些 (模型,供应商,凭据) 组合不可路由、为什么」。
// 后者是排障时的第一眼：一眼看到「全红」还是「只有几个红」。
//
// ★ **按需加载**：它返回全网 model_offers × credentials 的笛卡尔积，量级不小，
// 而 explain 的输入框是随手可用的 ⇒ 自动拉会让每次进页面都付这个代价。
const overview = ref<RoutingOverviewResponse | null>(null)
const overviewLoading = ref(false)
const overviewError = ref<string | null>(null)

async function loadOverview(): Promise<void> {
  if (overviewLoading.value) return
  overviewLoading.value = true
  overviewError.value = null
  try {
    overview.value = await fetchRoutingOverview()
  } catch (err) {
    overviewError.value = describeError(err)
  } finally {
    overviewLoading.value = false
  }
}

/** ★ 可路由 / 被阻塞 两栏，互斥且穷尽（后端每行必属其一）。 */
const overviewRows = computed(() => overview.value?.rows ?? [])
const overviewRoutableRows = computed(() => overviewRows.value.filter((r) => r.runtime_routable))
const overviewBlockedRows = computed(() => overviewRows.value.filter((r) => !r.runtime_routable))

const available = computed(() => (result.value?.candidates ?? []).filter((c) => c.available))
const blocked = computed(() => (result.value?.candidates ?? []).filter((c) => !c.available))

function candidateBadge(c: RoutingCandidate): { cls: string; label: string } {
  if (c.available) return { cls: 'badge--success', label: t('routing.routable') }
  if (c.circuit_state === 'open') return { cls: 'badge--danger', label: t('routing.circuitOpen') }
  if (c.quota_state && c.quota_state !== 'ok') return { cls: 'badge--warning', label: t('routing.quotaBlocked') }
  if (c.availability_state && c.availability_state !== 'ready') {
    return { cls: 'badge--warning', label: c.availability_state }
  }
  return { cls: 'badge--muted', label: c.block_reason || t('routing.unavailable') }
}

function submitOnEnter(ev: KeyboardEvent): void {
  if ((ev as KeyboardEvent).key === 'Enter') run()
}
</script>

<template>
  <div class="view-root routing">
    <div class="routing__search">
      <AppIcon name="search" :size="18" />
      <input
        v-model="model"
        type="search"
        :placeholder="t('routing.searchPlaceholder')"
        :aria-label="t('routing.modelLabel')"
        :disabled="loading"
        @keydown="submitOnEnter"
      />
      <button type="button" class="btn btn--primary routing__go" :disabled="!canQuery" @click="run">
        {{ loading ? t('common.loading') : t('routing.check') }}
      </button>
    </div>

    <p class="routing__hint">{{ t('routing.hint') }}</p>

    <!-- 全量可路由性总览。与上面的 explain 按需共存，不自动拉。 -->
    <div class="routing__overview">
      <div class="routing__overview-head">
        <h3 class="page__section-title routing__section">{{ t('routing.overviewTitle') }}</h3>
        <button
          v-if="!overview"
          type="button"
          class="btn btn--sm"
          :disabled="overviewLoading"
          @click="loadOverview"
        >
          {{ t('routing.overviewLoad') }}
        </button>
      </div>

      <p v-if="overviewLoading" class="routing__hint">{{ t('common.loading') }}</p>
      <p v-else-if="overviewError" class="routing__error" role="alert">{{ overviewError }}</p>

      <template v-else-if="overview">
        <!-- ★★ featured 查询的 error 被后端丢弃（`_ = QueryRow`）⇒ 空数组无信号。
             而 featured_only 且 featured 为空时过滤条件整个不下发 → 退化成全量。 -->
        <p v-if="overviewFeaturedFilterInert(overview)" class="routing__hint routing__warn">
          {{ t('routing.overviewNoFeatured') }}
        </p>

        <div class="data-card routing__summary">
          <div class="card-field">
            <span>{{ t('routing.overviewRoutable') }}</span>
            <span class="card-field__value num">{{ overviewRoutableRows.length }}</span>
          </div>
          <div class="card-field">
            <span>{{ t('routing.overviewBlocked') }}</span>
            <span class="card-field__value num">{{ overviewBlockedRows.length }}</span>
          </div>
          <div class="card-field">
            <span>{{ t('routing.overviewTotal') }}</span>
            <span class="card-field__value num">{{ overviewRows.length }}</span>
          </div>
        </div>

        <template v-if="overviewBlockedRows.length > 0">
          <h3 class="page__section-title routing__section">{{ t('routing.overviewBlockedList') }}</h3>
          <ul class="routing__ov-list">
            <li v-for="r in overviewBlockedRows" :key="`${r.credential_id}-${r.model_name}`" class="routing__ov-item">
              <span class="routing__ov-model">{{ r.model_name }}</span>
              <span class="routing__ov-cred">{{ r.credential_label }}</span>
              <!-- ★ 阻塞原因：可路由的行根本没有这个键，所以不能拿它当可路由判据 -->
              <span class="routing__ov-reason">{{ overviewBlockReason(r) || t('routing.overviewNoReason') }}</span>
              <!-- ★★ 编造默认值（COALESCE 0.9 / 9999）⇒ 这两个数可能是兜的，不是实测 -->
              <span v-if="overviewMetricsMayBePlaceholder(r)" class="routing__ov-warn">
                {{ t('routing.overviewMetricsPlaceholder') }}
              </span>
              <span v-if="overviewRoutableKeysDisagree(r)" class="routing__ov-warn">
                {{ t('routing.overviewKeysDisagree') }}
              </span>
            </li>
          </ul>
        </template>
      </template>
    </div>

    <p v-if="error" class="routing__error" role="alert">{{ error }}</p>

    <template v-if="result">
      <div class="data-card routing__summary">
        <div class="card-field">
          <span>{{ t('routing.canonical') }}</span>
          <span class="card-field__value">{{ result.canonical_name || '—' }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('routing.resolutionPath') }}</span>
          <span class="card-field__value">{{ result.resolution_path }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('routing.rawModels') }}</span>
          <span class="card-field__value">{{ result.raw_models.join(', ') || '—' }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('routing.candidateCount') }}</span>
          <span class="card-field__value num">
            {{ t('routing.availableOfTotal', { available: available.length, total: result.candidates.length }) }}
          </span>
        </div>
      </div>

      <p v-if="noCandidates" class="routing__empty">{{ t('routing.noCandidates') }}</p>

      <template v-else>
        <h3 class="page__section-title routing__section">{{ t('routing.routableCandidates') }}</h3>
        <div v-for="c in available" :key="`av-${c.credential_id}`" class="data-card routing__card">
          <div class="card-row">
            <span class="routing__name">
              <span class="routing__rank">#{{ c.rank }}</span>
              {{ c.provider_name }} · {{ c.credential_label || c.credential_id }}
            </span>
            <span class="badge" :class="candidateBadge(c).cls">{{ candidateBadge(c).label }}</span>
          </div>
          <div class="routing__fields">
            <span class="routing__field">{{ t('routing.tier') }} {{ c.tier }}</span>
            <span class="routing__field">{{ t('routing.weight') }} {{ c.weight }}</span>
            <span class="routing__field">{{ t('routing.successRate') }} {{ (c.success_rate * 100).toFixed(1) }}%</span>
            <span class="routing__field">{{ t('nodes.latency') }} {{ Math.round(c.p95_latency_ms) }}ms</span>
          </div>
        </div>

        <h3 v-if="blocked.length > 0" class="page__section-title routing__section">
          {{ t('routing.blockedCandidates', { n: blocked.length }) }}
        </h3>
        <div v-for="c in blocked" :key="`bl-${c.credential_id}`" class="data-card routing__card routing__card--blocked">
          <div class="card-row">
            <span class="routing__name">
              <span class="routing__rank">#{{ c.rank }}</span>
              {{ c.provider_name }} · {{ c.credential_label || c.credential_id }}
            </span>
            <span class="badge" :class="candidateBadge(c).cls">{{ candidateBadge(c).label }}</span>
          </div>
          <div class="routing__fields">
            <span v-if="c.block_reason" class="routing__field routing__field--reason">{{ c.block_reason }}</span>
            <span v-if="c.availability_recover_at" class="routing__field">
              {{ t('routing.recoverAt') }} {{ c.availability_recover_at }}
            </span>
          </div>
        </div>
      </template>
    </template>
  </div>
</template>

<style scoped>
.routing__search {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  height: 48px;
  padding: 0 var(--app-space-2) 0 var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  color: var(--app-text-muted);
}

.routing__search input {
  flex: 1;
  border: none;
  background: transparent;
  color: var(--app-text);
  font-size: var(--app-font-input);
  min-width: 0;
  height: 100%;
  align-self: stretch;
}

.routing__search input:focus {
  outline: none;
}

.routing__go {
  min-height: 48px;
  flex: 0 0 auto;
  /* R1（17 §4-R1 / 06 §7）：新增触控控件一律 ≥48 CSS px。
     44px 是存量 .btn 的下限、不是新标准；这些 chip 是 2026-10-06 新增的，
     走的是自定义选择器而非 .btn，故此前落在 32-40px —— 违反 R1。 */
}

.routing__hint {
  margin: var(--app-space-2) 0 var(--app-space-3);
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.routing__error {
  color: var(--app-danger);
  font-size: 0.875rem;
  margin-bottom: var(--app-space-3);
}

.routing__summary,
.routing__card {
  margin-bottom: var(--app-space-2);
}

.routing__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.routing__card--blocked {
  opacity: 0.82;
  border-style: dashed;
}

.routing__name {
  display: inline-flex;
  align-items: center;
  gap: var(--app-space-2);
  font-weight: 600;
  font-size: 0.9375rem;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.routing__rank {
  font-variant-numeric: tabular-nums;
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.routing__fields {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1) var(--app-space-3);
}

.routing__field {
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.routing__field--reason {
  color: var(--app-danger);
}

.routing__section {
  margin: var(--app-space-4) 0 var(--app-space-2);
  margin-inline: 0;
}

.routing__empty {
  color: var(--app-text-secondary);
  font-size: 0.875rem;
  padding: var(--app-space-4) 0;
}

/* ── 全量可路由性总览 ────────────────────────────────────────────── */
.routing__overview {
  margin-top: var(--app-space-3);
}

.routing__overview-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.routing__warn {
  color: var(--app-warning);
}

.routing__ov-list {
  list-style: none;
  margin: var(--app-space-2) 0 0;
  padding: 0;
}

.routing__ov-item {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  padding: var(--app-space-2) 0;
  border-top: 1px solid var(--app-border);
  font-size: 0.8125rem;
}

.routing__ov-model {
  color: var(--app-text-primary);
  font-weight: 600;
}

.routing__ov-cred,
.routing__ov-reason {
  color: var(--app-text-secondary);
}

.routing__ov-reason {
  color: var(--app-danger);
}

.routing__ov-warn {
  color: var(--app-warning);
  font-size: 0.75rem;
}
</style>
