<script setup lang="ts">
// UsageEnhancedView — 用量成本增强面（2026-10-14，第一百二十二批）。
//
// GET /api/admin/usage/cost-trend        （group_by + start/end）
// GET /api/admin/usage/period-compare     （current/previous 两个 YYYY-MM）
// GET /api/admin/usage/cache-economics    （start/end）
//
// ══ 权限 ══════════════════════════════════════════════════════════════════
// ★★★★★★ 权限档**第一类（有租户维度）**，但**本族是第一个「服务端缺角色下限」的**。
//
//   三条端点都挂在 `admin/handler.go:1289` 的**前缀子路由**上：
//     `mux.HandleFunc("/api/admin/usage/", h.admin(h.HandleUsageAdmin))`
//   → `admin/usage.go:49` `HandleUsageAdmin` 按 path 尾段分发到
//     `usageCostTrend`(`usage_enhanced.go:128`) / `usagePeriodCompare`(`:347`)
//     / `usageCacheEconomics`(`:616`)。
//
//   ★★ 三个 handler **都有**租户过滤（`EffectiveTenantIDAll(r)` + `AND ... tenant_id = $N`）
//     ⇒ 各租户互相隔离 ⇒ **不是**第三类（没有「载荷含别人的东西」的判定权）。
//
//   ★★★★★ **但 `h.admin` 没有任何角色下限**：
//     `h.admin` = `AdminMiddleware`（`handler.go:894` → `auth.go:85`）全文只有
//     「验 JWT」+「强制改密码」；而 `pkg/identity/verifier.go` 的 `supportedRole`
//     —— **`supportedRole("user")` 返回 `true`** —— 它校验「角色**是已知的**」，
//     **不是**「角色**够高**」。
//
//   ⇒ `EffectiveTenantIDAll` 只认 `tenant_admin`，所以普通 `user` 拿到的是 `""`，
//     而 `""` 意味着「**不拼** tenant_id 条件」⇒ ★★★ **普通 user 读到跨租户成本数据**。
//
//   ⇒ 前端因此设 `requiresRole: 'admin'`（不是 super_admin）：
//     挡住 `user`，放行 `super_admin` / `tenant_admin` / `admin_key`。
//   ⚠️ **前端严于后端是兜底，不是修复** —— 该补的是后端那道角色下限。
//     与已登记的后端待办②（errors-trend / model-history「effective role=user
//     可读跨租户」）是**同一类缺陷的第三个实例**，而本族落在**成本**上，更敏感。
import { computed, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchCacheEconomics,
  fetchPeriodCompare,
  usageRoleNeedsAdminGate,
  usageAllTenantsScopeIsLegitimate,
  cacheEconomicsDegraded,
  cacheEconomicsDegradedReason,
  cacheHitRatioMeaningless,
  compressionSavedUnreliable,
  compressedCountMayBeFailed,
  periodCompareDegraded,
  periodChangeMeaningless,
  TREND_THRESHOLD_PCT,
  type CacheEconomicsResponse,
  type PeriodCompareResponse,
} from '@/api/usageEnhanced'

useHyperPage({ title: () => t('ue.title') })

const auth = useAuthStore()

// ══ 权限兜底 ══════════════════════════════════════════════════════════════
// ★ 与批 116/120（服务端是真门）不同：本族服务端**确实**按租户隔离，
//   但**没有角色下限** ⇒ 这一层兜的是「不该到这里的主体根本别进来」。

const role = computed(() => auth.role)
const allowed = computed(() => !usageRoleNeedsAdminGate(role.value))
const allTenantsScope = computed(() => usageAllTenantsScopeIsLegitimate(role.value))

// ══ 取数 ══════════════════════════════════════════════════════════════════

const cache = ref<CacheEconomicsResponse | null>(null)
const compare = ref<PeriodCompareResponse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

async function load(): Promise<void> {
  if (!allowed.value) return
  loading.value = true
  error.value = null
  try {
    // ★★ 三条端点的缺省时间窗各不相同（7 / 无 / 30 天），这里各自显式传，
    //   避免「忘了传」时被各自的缺省悄悄改掉口径。
    cache.value = await fetchCacheEconomics()
    compare.value = await fetchPeriodCompare({ current: currentPeriod(), previous: previousPeriod() })
  } catch (err) {
    cache.value = null
    compare.value = null
    error.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}

/** 上一个自然月 —— `period-compare` 只收 `YYYY-MM`，不接受时间窗。 */
function currentPeriod(): string {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
}
function previousPeriod(): string {
  const d = new Date()
  const p = new Date(d.getFullYear(), d.getMonth() - 1, 1)
  return `${p.getFullYear()}-${String(p.getMonth() + 1).padStart(2, '0')}`
}

// ══ 自检红线 ══════════════════════════════════════════════════════════════
// ★ `degraded` 是**恒发**字段 ⇒ 「服务端确认过它没降级」是可断言的。

/** ★★★★★ 降级时服务端返 200 + 全 0 ⇒ 不读 `degraded` 就会把「查崩了」显示成「没花钱」。 */
const cacheDegraded = computed(() => (cache.value ? cacheEconomicsDegraded(cache.value) : false))
const compareDegraded = computed(() => (compare.value ? periodCompareDegraded(compare.value) : false))
/** ★★★★ 降级原因**只在降级时**才有那个键** ⇒ 「缺失」与「空串」都算降级。 */
const degradedReason = computed(() =>
  cache.value ? cacheEconomicsDegradedReason(cache.value) : null,
)
/** ★★★ 命中率在分母为 0 时无意义（显示 0% 会被读成「一条都没命中」）。 */
const hitRatioMeaningless = computed(() =>
  cache.value ? cacheHitRatioMeaningless(cache.value) : false,
)
/**
 * ★★★★★ `compression_saved` 的唯一输入 `compressed_requests` 查询失败是**静默**的
 * （服务端只 `slog.Warn`）⇒ 它为 0 时分不清「真的没有压缩请求」与「计数查询失败」。
 */
const compressionUnreliable = computed(() =>
  cache.value ? compressionSavedUnreliable(cache.value) : false,
)
const compressedCountSuspect = computed(() =>
  cache.value ? compressedCountMayBeFailed(cache.value) : false,
)
/** ★★★ 环比「变了 0%」在后端是「两个周期都没数据」，不是「持平」。 */
const compareMeaningless = computed(() =>
  compare.value ? periodChangeMeaningless(compare.value) : false,
)

// ══ 「该出现」的一面 ══════════════════════════════════════════════════════
// ★ 红线之外的「没降级」也必须说出来，否则用户无法区分「正常」与「没加载」。

const cacheHealthy = computed(() => (cache.value ? !cacheEconomicsDegraded(cache.value) : false))
const thresholdPct = computed(() => TREND_THRESHOLD_PCT)

/** ★★ 三个「节省」数字全是按硬编码假设推出来的 ⇒ 必须标「估算」。 */
const SAVINGS_ESTIMATE_FIELDS = ['dollars_saved', 'compression_saved', 'total_saved'] as const

/** ★★ super_admin / admin_key 看到的是「全部租户」—— 这必须写进页面，不能让它静默发生。 */
const scopeNoteKey = computed(() =>
  allTenantsScope.value ? 'ue.scope.allTenants' : 'ue.scope.ownTenant',
)
</script>

<template>
  <div class="ue">
    <!-- ★★ 深链兜底：不是后端 403，而是前端在**发请求之前**就挡住。 -->
    <section v-if="!allowed" class="ue__sec" data-testid="ue-nope">
      <header class="ue__head">
        <h2 class="ue__h">{{ t('ue.title') }}</h2>
      </header>
      <p class="ue__msg ue__msg--err" data-testid="ue-nope-msg">{{ t('ue.nope.msg') }}</p>
      <p class="ue__msg" data-testid="ue-nope-why">{{ t('ue.nope.why') }}</p>
    </section>

    <template v-else>
      <section class="ue__sec">
        <header class="ue__head">
          <h2 class="ue__h">{{ t('ue.load.title') }}</h2>
          <button type="button" class="ue__btn" data-testid="ue-load" :disabled="loading" @click="load">
            {{ loading ? t('common.loading') : t('ue.load.btn') }}
          </button>
        </header>
        <p class="ue__msg" data-testid="ue-scope-note">{{ t(scopeNoteKey) }}</p>
        <p class="ue__msg" data-testid="ue-estimate-note">{{ t('ue.estimate.note') }}</p>

        <p v-if="error" class="ue__msg ue__msg--err" data-testid="ue-error">{{ error }}</p>
      </section>

      <!-- ══ 缓存与压缩经济性 ══ -->
      <section v-if="cache" class="ue__sec">
        <header class="ue__head">
          <h2 class="ue__h">{{ t('ue.cache.title') }}</h2>
          <span v-if="cacheHealthy" class="ue__badge" data-testid="ue-cache-ok">
            {{ t('ue.cache.ok') }}
          </span>
        </header>

        <p v-if="cacheDegraded" class="ue__msg ue__msg--err" data-testid="ue-cache-degraded">
          {{ t('ue.cache.degraded') }}
        </p>
        <p v-if="degradedReason" class="ue__msg ue__msg--err" data-testid="ue-degraded-reason">
          {{ degradedReason }}
        </p>
        <p v-if="hitRatioMeaningless" class="ue__msg ue__msg--err" data-testid="ue-hit-ratio-meaningless">
          {{ t('ue.check.hitRatioMeaningless') }}
        </p>
        <p v-if="compressedCountSuspect" class="ue__msg ue__msg--err" data-testid="ue-compressed-suspect">
          {{ t('ue.check.compressedSuspect') }}
        </p>
        <p v-if="compressionUnreliable" class="ue__msg ue__msg--err" data-testid="ue-compression-unreliable">
          {{ t('ue.check.compressionUnreliable') }}
        </p>

        <dl class="ue__dl">
          <div class="ue__kv">
            <dt>{{ t('ue.field.totalRequests') }}</dt>
            <dd class="ue__mono" data-testid="ue-total-requests">{{ cache.total_requests }}</dd>
          </div>
          <div class="ue__kv">
            <dt>{{ t('ue.field.cacheReadTokens') }}</dt>
            <dd class="ue__mono" data-testid="ue-cache-read-tokens">{{ cache.cache_read_tokens }}</dd>
          </div>
          <div v-for="f in SAVINGS_ESTIMATE_FIELDS" :key="f" class="ue__kv">
            <dt>
              {{ t(`ue.field.${f}`) }}
              <span class="ue__tag" :data-testid="`ue-badge-${f}`">{{ t('ue.estimate.badge') }}</span>
            </dt>
            <dd class="ue__mono" :data-testid="`ue-${f}`">{{ cache[f] }}</dd>
          </div>
        </dl>
      </section>

      <!-- ══ 环比 ══ -->
      <section v-if="compare" class="ue__sec">
        <header class="ue__head">
          <h2 class="ue__h">{{ t('ue.compare.title') }}</h2>
        </header>

        <p v-if="compareDegraded" class="ue__msg ue__msg--err" data-testid="ue-compare-degraded">
          {{ t('ue.compare.degraded') }}
        </p>
        <p v-if="compareMeaningless" class="ue__msg ue__msg--err" data-testid="ue-compare-meaningless">
          {{ t('ue.check.periodMeaningless') }}
        </p>
        <p class="ue__msg" data-testid="ue-threshold">{{ t('ue.check.threshold', { pct: thresholdPct }) }}</p>
      </section>
    </template>
  </div>
</template>

<style scoped>
.ue {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.ue__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.ue__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.ue__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

/* ★ R1：新增触控控件 ≥48 CSS px。 */
.ue__btn {
  min-height: 48px;
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.ue__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.ue__msg--err {
  color: var(--app-danger);
}

.ue__dl {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  margin: 0;
}

.ue__kv {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 4px 0;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
  border-bottom: 1px solid var(--app-border);
}

.ue__kv dt {
  margin: 0;
}

.ue__mono {
  margin: 0;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
  text-align: right;
}

.ue__tag {
  font-size: 0.625rem;
  color: var(--app-text-muted);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  padding: 0 4px;
}

.ue__badge {
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--app-primary);
  font-variant-numeric: tabular-nums;
}
</style>
